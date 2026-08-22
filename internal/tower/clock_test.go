package tower

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

// fixedClock is a ClockSource that claims real time is `off` away from ours, so a skew of
// any size can be driven without touching the machine's clock.
func fixedClock(off time.Duration) ClockSource {
	return func() (time.Time, string, error) { return time.Now().Add(-off), "test reference", nil }
}

func doctorWith(src ClockSource) Report {
	return Doctor(&Config{Mode: ModeStandalone}, WithClockSource(src))
}

// TestAClockPastTheSignatureWindowIsAProblemNotANote. This is the one machine-level fault
// doctor calls a PROBLEM, and the reason is that its consequence is total rather than
// degrading: protocol.VerifyRequest refuses any timestamp more than SigMaxSkew from THIS
// machine's clock, so a Tower out past that window refuses every correctly signed request
// from every honest node and relays nothing at all.
func TestAClockPastTheSignatureWindowIsAProblemNotANote(t *testing.T) {
	for _, off := range []time.Duration{protocol.SigMaxSkew + time.Minute, -(protocol.SigMaxSkew + time.Minute)} {
		rep := doctorWith(fixedClock(off))
		require.False(t, rep.OK, "offset %s left doctor OK", off)
		require.Len(t, rep.Problems, 1)
		p := rep.Problems[0]
		require.Contains(t, p, "relays nothing",
			"the problem does not say what actually happens - an operator cannot act on a number")
		if off > 0 {
			require.Contains(t, p, "AHEAD")
		} else {
			require.Contains(t, p, "BEHIND")
		}
	}
}

// TestAMarginalClockIsANoteNotAProblem. Nothing is broken yet, and calling it broken would
// make doctor exit non-zero on a Tower that works - but the five minutes is a budget shared
// with the node at the other end, and docs/relay-selection-design.md §5.4b's whole argument
// is that an unsynchronised node is ordinary and must not be refused for it. So the note
// has to say whose margin is being spent.
func TestAMarginalClockIsANoteNotAProblem(t *testing.T) {
	rep := doctorWith(fixedClock(ClockWarnSkew + time.Second))
	require.True(t, rep.OK, "a marginal clock made doctor fail; it breaks nothing yet")
	require.Empty(t, rep.Problems)
	joined := strings.Join(rep.Notes, " | ")
	require.Contains(t, joined, "AHEAD")
	require.Contains(t, joined, "shared", "the note does not explain that the window is a shared budget")
}

// TestASmallOffsetSaysNothing: a clock that is right must not generate output an operator
// then learns to ignore.
func TestASmallOffsetSaysNothing(t *testing.T) {
	rep := doctorWith(fixedClock(50 * time.Millisecond))
	require.True(t, rep.OK)
	for _, n := range rep.Notes {
		require.NotContains(t, n, "clock", "a synchronised clock produced a warning: %s", n)
	}
	require.Contains(t, rep.String(), "clock offset:", "the measured offset is not reported at all")
}

// TestAnUnreachableReferenceIsNotAClockFault. A blocked UDP 123 is the ordinary condition
// of a hardened network and says nothing whatever about the clock. Reporting it as a fault
// would send an operator to fix something that is not broken, which is worse than silence.
func TestAnUnreachableReferenceIsNotAClockFault(t *testing.T) {
	src := func() (time.Time, string, error) { return time.Time{}, "NTP nowhere:123", errors.New("i/o timeout") }
	rep := Doctor(&Config{Mode: ModeStandalone}, WithClockSource(src))
	require.True(t, rep.OK, "an unreachable NTP server failed the Tower")
	require.Empty(t, rep.Problems)
	require.Contains(t, rep.String(), "says nothing about your clock")
	require.False(t, rep.Clock.OffsetKnown)
	require.False(t, rep.Clock.Fatal(), "an unmeasured clock must never read as a broken one")
}

// TestNoSourceReportsNotDetermined, and a caller with a REASON for withholding one gets
// that reason printed instead of the generic line. Those are the same absence and
// completely different facts: a standalone Tower is not measuring its clock BECAUSE it
// promises to make no outbound connection, and an operator should be told that rather than
// left to think doctor forgot.
func TestNoSourceReportsNotDetermined(t *testing.T) {
	rep := Doctor(&Config{Mode: ModeStandalone})
	require.False(t, rep.Clock.OffsetKnown)
	require.Contains(t, rep.String(), "clock offset: not determined")

	rep = Doctor(&Config{Mode: ModeStandalone}, WithClockSourceRefused("no outbound connection by design"))
	require.Contains(t, rep.String(), "no outbound connection by design")
}

// TestTheFatalThresholdIsTheProtocolConstantItself, not a number that looks like it. If
// SigMaxSkew ever moves, the threshold has to move with it or doctor starts passing Towers
// the hub will refuse.
func TestTheFatalThresholdIsTheProtocolConstantItself(t *testing.T) {
	require.Equal(t, protocol.SigMaxSkew, ClockFatalSkew)
	require.Equal(t, protocol.SigMaxSkew/10, ClockWarnSkew)

	// And the boundary belongs to the fatal side: at exactly SigMaxSkew, VerifyRequest's
	// comparison (`skew > SigMaxSkew`) still passes for a perfectly-timed request, but a
	// node with any error at all is already refused. Doctor calls that broken.
	c := ClockCheck{OffsetKnown: true, Offset: protocol.SigMaxSkew}
	require.True(t, c.Fatal())
	require.False(t, ClockCheck{OffsetKnown: true, Offset: ClockWarnSkew - time.Millisecond}.Marginal())
}

// TestTheDisciplineProbeIsReportedEvenWithNoReference. It is the half of the check that
// carries a repair - "you are four minutes out" is a symptom, "nothing is steering this
// clock" is an instruction - and it costs nothing, so it must not be skipped just because
// no reference was available.
func TestTheDisciplineProbeIsReportedEvenWithNoReference(t *testing.T) {
	rep := Doctor(&Config{Mode: ModeStandalone})
	require.Contains(t, rep.String(), "clock:")
	if rep.Clock.DisciplineKnown && !rep.Clock.Disciplined {
		require.NotEmpty(t, rep.Clock.DisciplineNote, "an undisciplined clock was reported with no repair")
	}
}

// TestAnUndisciplinedClockIsWorthANoteOnItsOwn: no measured skew yet, but nothing is
// holding it there, and that is the state that becomes the fatal one on its own schedule.
func TestAnUndisciplinedClockIsWorthANoteOnItsOwn(t *testing.T) {
	c := ClockCheck{DisciplineKnown: true, Disciplined: false, DisciplineNote: "Repair: enable a time daemon"}
	require.Contains(t, clockRepair(c), "enable a time daemon")

	// And when a daemon IS running and the clock is still wrong, the advice must not be
	// "install one" - that advice has already been taken, and repeating it wastes the one
	// message the operator will read.
	c2 := ClockCheck{DisciplineKnown: true, Disciplined: true}
	require.Contains(t, clockRepair(c2), "what it is")
	require.NotContains(t, clockRepair(c2), "install another time daemon")
}
