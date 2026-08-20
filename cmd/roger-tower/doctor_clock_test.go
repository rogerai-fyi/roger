package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A Tower needs no GPU and runs no model. Its requirements are a stable address, an exposed
// port, bandwidth and a synchronised clock, and doctor is where an operator should learn
// about the last one - because a Tower past protocol.SigMaxSkew refuses every honest node
// with a 401 that says nothing about time. These tests cover the CLI half of that: WHEN
// doctor is allowed to measure, which is a question about the mode rather than about time.

// TestDoctorReportsTheClock: the section exists at all, on the ordinary invocation, without
// having to be asked for.
func TestDoctorReportsTheClock(t *testing.T) {
	p := writeConfig(t, joinedYAML)
	// --offline so this test does not depend on reaching an NTP server; the DISCIPLINE half
	// of the check is local and must still be reported.
	out, err := runCLI(t, "doctor", "--config", p, "--offline")
	require.NoError(t, err)
	require.Contains(t, out, "clock:", "doctor says nothing about the clock at all")
	require.Contains(t, out, "clock offset: not determined")
}

// TestStandaloneDoctorMakesNoOutboundClockCall is the important one. A standalone Tower's
// promise is that it makes no outbound connection - a Phase 1 gate, not a setting - and
// doctor must not quietly break it to check a clock. Pointed at an NTP address that would
// fail loudly if it were dialled, a standalone doctor must report that it did not measure,
// and say why, rather than report a failed measurement.
func TestStandaloneDoctorMakesNoOutboundClockCall(t *testing.T) {
	p := writeConfig(t, standaloneYAML)
	out, err := runCLI(t, "doctor", "--config", p, "--ntp", "127.0.0.1:1")
	require.NoError(t, err)
	require.Contains(t, out, "no outbound connection by design",
		"standalone doctor did not explain why the clock went unmeasured")
	require.NotContains(t, out, "could not reach",
		"standalone doctor DIALLED the NTP server; the isolation promise is that it does not")
	require.Contains(t, out, "--clock-check", "the operator is not told how to ask for the measurement")
}

// And the operator can ask. --clock-check is the explicit consent that makes the one UDP
// packet acceptable; here it is pointed somewhere nothing answers, so the proof that it
// tried is the reachability failure - which must be reported as "not checked", never as a
// clock fault.
func TestStandaloneDoctorMeasuresWhenAsked(t *testing.T) {
	p := writeConfig(t, standaloneYAML)
	out, err := runCLI(t, "doctor", "--config", p, "--clock-check", "--ntp", "127.0.0.1:1")
	require.NoError(t, err, "an unreachable NTP server must not fail the Tower")
	require.Contains(t, out, "could not reach")
	require.Contains(t, out, "says nothing about your clock")
	require.Contains(t, out, "doctor: OK")
}

// TestJoinedDoctorMeasuresByDefault: a joined Tower already talks to Roger Core, so there
// is no isolation promise for the clock query to break and the operator should not have to
// know a flag exists to get the check that matters most.
func TestJoinedDoctorMeasuresByDefault(t *testing.T) {
	p := writeConfig(t, joinedYAML)
	out, err := runCLI(t, "doctor", "--config", p, "--ntp", "127.0.0.1:1")
	require.NoError(t, err)
	require.Contains(t, out, "could not reach", "joined doctor did not attempt the clock measurement")
}

// TestDoctorOfflineSkipsItEverywhere, for a host that genuinely has no route out and does
// not want a two-second pause to prove it.
func TestDoctorOfflineSkipsItEverywhere(t *testing.T) {
	for _, y := range []string{joinedYAML, standaloneYAML} {
		out, err := runCLI(t, "doctor", "--config", writeConfig(t, y), "--offline", "--ntp", "127.0.0.1:1")
		require.NoError(t, err)
		require.NotContains(t, out, "could not reach")
		require.True(t, strings.Contains(out, "clock offset: not determined"),
			"--offline still has to say the offset is unknown rather than say nothing:\n%s", out)
	}
}
