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

// THE STALE SWEEP MUST KNOW WHEN IT DOES NOT KNOW. LastSeenChecked reports a mirror read
// failure instead of answering from whatever this instance happens to hold.
func TestLastSeenCheckedReportsAMirrorFailure(t *testing.T) {
	_, _, err := New(mirrorCfg(errMirror{})).LastSeenChecked("tw-1")
	require.Error(t, err)

	s := New(mirrorCfg(NewMemMirror()))
	openOn(t, s, "tw-1", "")
	seen, ok, err := s.LastSeenChecked("tw-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.WithinDuration(t, time.Now(), seen, time.Second)

	_, ok, err = New(mirrorCfg(nil)).LastSeenChecked("tw-1")
	require.NoError(t, err)
	require.False(t, ok)
}

// WITH A SHARED VIEW, THE SHARED VIEW DECIDES. A Tower opens s-1 on one instance and s-2 on
// another; the first instance still holds s-1 locally. A close of s-1 there must not read as
// ending the Tower's session, or s-2 is never recorded as gone quiet.
func TestACloseOfASessionSupersededOnAnotherInstanceEndsNothing(t *testing.T) {
	m := NewMemMirror()
	a, b := New(mirrorCfg(m)), New(mirrorCfg(m))
	s1 := openOn(t, a, "tw-1", "")
	s2 := openOn(t, b, "tw-1", "")

	require.False(t, a.Close(s1, "tw-1"), "s-1 was superseded on the other instance")
	cur, ok := a.Current("tw-1")
	require.True(t, ok)
	require.Equal(t, s2, cur, "the shared view names the current session, not a stale local copy")
	require.True(t, a.Close(s2, "tw-1"), "closing the current session through any instance ends it")
	_, ok = b.Current("tw-1")
	require.False(t, ok, "and no instance still calls it open")
}

func TestCurrentReadsTheSharedViewOverALocalCopy(t *testing.T) {
	m := NewMemMirror()
	a := New(mirrorCfg(m))
	s1 := openOn(t, a, "tw-1", "")
	require.NoError(t, m.Del("tw-1", s1)) // closed elsewhere
	_, ok := a.Current("tw-1")
	require.False(t, ok, "a local copy of a closed session is not open")
}
