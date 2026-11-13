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

// CLOSE SAYS WHETHER IT ENDED ANYTHING. The governance record writes session_closed only
// for a close that ended the Tower's current session; a close naming a superseded or
// unknown session must not hide the live one from the "went quiet" sweep.
func TestCloseReportsWhetherItEndedTheCurrentSession(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	old := openOn(t, a, "tw-1", "")
	cur := openOn(t, a, "tw-1", "")

	require.False(t, a.Close(old, "tw-1"), "a superseded session ends nothing")
	require.False(t, a.Close("made-up", "tw-1"))
	require.False(t, a.Close(cur, "tw-other"), "the right id for the wrong Tower ends nothing")
	require.True(t, a.Live("tw-1"), "and the live session is untouched")

	// Another instance that never held it can still end it, through the shared record.
	require.True(t, b.Close(cur, "tw-1"))
	require.False(t, b.Close(cur, "tw-1"), "a second close ends nothing")
}

func TestCloseWithoutAMirrorReportsTheLocalSession(t *testing.T) {
	s := New(mirrorCfg(nil))
	cur := openOn(t, s, "tw-1", "")
	require.True(t, s.Close(cur, "tw-1"))
	require.False(t, s.Close(cur, "tw-1"))
}

func TestCurrentNamesTheTowersSession(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	_, ok := a.Current("tw-1")
	require.False(t, ok)
	cur := openOn(t, a, "tw-1", "")
	got, ok := a.Current("tw-1")
	require.True(t, ok)
	require.Equal(t, cur, got)
	got, ok = b.Current("tw-1")
	require.True(t, ok, "another instance reads it from the shared record")
	require.Equal(t, cur, got)
	_, ok = New(mirrorCfg(errMirror{})).Current("tw-1")
	require.False(t, ok)
}
