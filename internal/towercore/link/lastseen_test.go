package link

// LastSeen answers "when did Core last hear from this Tower" for the governance record and
// the admin view (features/tower/governance_record.feature), from this instance's session
// or the shared mirror, whichever is newer.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLastSeenIsUnknownForATowerNeverHeardFrom(t *testing.T) {
	s := New(mirrorCfg(NewMemMirror()))
	_, ok := s.LastSeen("tw-1")
	require.False(t, ok)
}

func TestLastSeenComesFromTheLocalSession(t *testing.T) {
	s := New(mirrorCfg(nil))
	openOn(t, s, "tw-1", "")
	seen, ok := s.LastSeen("tw-1")
	require.True(t, ok)
	require.WithinDuration(t, time.Now(), seen, time.Second)
}

func TestLastSeenTakesTheNewerOfLocalAndMirror(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	sess := openOn(t, a, "tw-1", "")

	// Another instance (b) never held the session but reads the shared record.
	seen, ok := b.LastSeen("tw-1")
	require.True(t, ok)
	require.WithinDuration(t, time.Now(), seen, time.Second)

	// A newer heartbeat recorded by a peer wins over this instance's older local stamp.
	rec, _, err := m.Get("tw-1")
	require.NoError(t, err)
	rec.LastSeen = time.Now().Add(time.Minute)
	require.NoError(t, m.Put("tw-1", rec))
	seen, ok = a.LastSeen("tw-1")
	require.True(t, ok)
	require.WithinDuration(t, rec.LastSeen, seen, time.Millisecond)
	_ = sess
}

func TestLastSeenSurvivesAFailingMirror(t *testing.T) {
	s := New(mirrorCfg(errMirror{rec: Record{LastSeen: time.Now().Add(time.Hour)}}))
	_, ok := s.LastSeen("tw-1")
	require.False(t, ok, "a mirror that errors supplies nothing, not its stale record")
}
