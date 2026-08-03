package tower

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The persistence seam. Contract: features/tower/modes.feature durable-startup scenarios.
//
// Why a seam at all: `internal/tower` is covered by a gate test that fails if any file in
// it gains the ability to reach the network, and a database driver dials. Keeping the
// driver behind an interface - implemented in a separate package - is what lets the
// standalone core stay provably egress-free while still having durable storage.
//
// The concurrency contract matters as much as the durability one. A file-backed Tower is
// serialized by the identity-directory lock; a database-backed one can have several
// processes, so a write must fail rather than silently overwrite a newer revision.

func TestFileStoreRoundTripsAdmissionState(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	s := NewFileStore(dir)

	snap, err := s.Load()
	require.NoError(t, err)
	require.NotEmpty(t, snap.HMACKey, "a fresh store mints its verifier secret")
	require.Zero(t, snap.Revision, "nothing has been written yet")

	snap.Operator = &Credential{ClientKeyHash: "k1", Role: RoleLocalOperator}
	next, err := s.Save(snap)
	require.NoError(t, err)
	require.Equal(t, int64(1), next, "a write advances the revision")

	again, err := s.Load()
	require.NoError(t, err)
	require.NotNil(t, again.Operator)
	require.Equal(t, "k1", again.Operator.ClientKeyHash)
	require.Equal(t, int64(1), again.Revision)
}

// A stale write must lose. Two processes reading the same revision and both writing is
// exactly how one operator's admission silently overwrites another's.
func TestAStaleWriteIsRefused(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	s := NewFileStore(dir)

	first, err := s.Load()
	require.NoError(t, err)
	second, err := s.Load() // another process, same revision
	require.NoError(t, err)

	first.Operator = &Credential{ClientKeyHash: "winner"}
	_, err = s.Save(first)
	require.NoError(t, err)

	second.Operator = &Credential{ClientKeyHash: "loser"}
	_, err = s.Save(second)
	require.ErrorIs(t, err, ErrStaleWrite, "a write from a stale read must be refused")

	got, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, "winner", got.Operator.ClientKeyHash, "the first writer stands")
}

func TestSnapshotCarriesEverythingThatMustSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)

	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err)
	_, err = st.AttachStation("st-1", "sk1", []string{"m"})
	require.NoError(t, err)

	snap, err := NewFileStore(dir).Load()
	require.NoError(t, err)
	require.NotEmpty(t, snap.HMACKey, "the verifier secret must survive, or every open invitation dies")
	require.NotNil(t, snap.Operator, "the operator must survive, or the network has nobody in charge")
	require.Len(t, snap.Stations, 1, "attached Stations must survive")
	require.Len(t, snap.Invitations, 1, "invitation history must survive, or a consumed code could be replayed")
}

// A Tower reopened from the same directory sees what the previous process wrote. This is
// the property the whole seam exists for.
func TestStateSurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)

	first, err := Open(dir)
	require.NoError(t, err)
	inv, code, err := first.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = first.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err)

	second, err := Open(dir)
	require.NoError(t, err)
	op, err := second.LocalOperator()
	require.NoError(t, err)
	require.Equal(t, testClientKey, op.ClientKeyHash)

	// And the consumed code is still consumed - persistence is what stops a replay.
	_, err = second.ConsumeInvitation(inv.ID, code, testClientKey)
	require.Error(t, err)
}

func TestFileStoreReportsCorruptState(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	require.NoError(t, writeFileForTest(dir, bootstrapFile, "{broken"))

	_, err = NewFileStore(dir).Load()
	require.Error(t, err, "unreadable state must not read as empty state")
}

func writeFileForTest(dir, name, body string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(body), keyPerm)
}

// --- the store seam itself -------------------------------------------------

// memStore is a Store that records what it was asked to do, so the seam can be exercised
// without a database. It also proves the seam is genuinely pluggable.
type memStore struct {
	snap  *Snapshot
	loads int
	saves int
	fail  error
}

func (m *memStore) Load() (*Snapshot, error) {
	m.loads++
	if m.fail != nil {
		return nil, m.fail
	}
	if m.snap == nil {
		return NewSnapshot()
	}
	c := *m.snap
	return &c, nil
}

func (m *memStore) Save(s *Snapshot) (int64, error) {
	m.saves++
	if m.fail != nil {
		return 0, m.fail
	}
	if m.snap != nil && m.snap.Revision != s.Revision {
		return 0, ErrStaleWrite
	}
	c := *s
	c.Revision = s.Revision + 1
	m.snap = &c
	s.Revision = c.Revision
	return c.Revision, nil
}

func TestWithStoreRedirectsEveryReadAndWrite(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)

	ms := &memStore{}
	st = st.WithStore(ms)

	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err)

	require.Positive(t, ms.loads, "reads must go through the supplied store")
	require.Positive(t, ms.saves, "writes must go through the supplied store")

	// Nothing leaked into the data directory: the seam replaced it entirely.
	_, statErr := os.Stat(filepath.Join(dir, bootstrapFile))
	require.True(t, os.IsNotExist(statErr), "a supplied store must replace the file store, not shadow it")
}

// A store that cannot be read must not let a Tower admit anyone. Failing open here would
// mint a second operator on a network that already has one.
func TestAFailingStoreRefusesAdmission(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)
	st = st.WithStore(&memStore{fail: errors.New("database unavailable")})

	_, _, err = st.CreateInvitation(testClientKey, time.Hour, 5)
	require.Error(t, err)
	_, err = st.ConsumeInvitation("id", "code", testClientKey)
	require.Error(t, err)
	_, err = st.LocalOperator()
	require.Error(t, err)
	_, err = st.Stations()
	require.Error(t, err)
}

func TestDirReportsTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)
	require.Equal(t, dir, st.Dir())
}
