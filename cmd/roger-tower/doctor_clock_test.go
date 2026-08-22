package main

import (
	"strings"
	"testing"

	"encoding/binary"
	"github.com/stretchr/testify/require"
	"net"
	"time"
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

// fakeSkewedNTP answers as a server whose clock is `off` away from ours - the same 25 lines
// clockprobe's own tests use, carried here because the CLI half of the fatal-skew path had
// never run: doctor's exit status is what an ExecStartPre or a readiness probe actually
// reads, and nothing had ever made doctor exit non-zero.
func fakeSkewedNTP(t *testing.T, off time.Duration) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 48)
		for {
			_, addr, rerr := pc.ReadFrom(buf)
			if rerr != nil {
				return
			}
			now := time.Now().Add(off)
			resp := make([]byte, 48)
			resp[0] = 0x1c // LI=0, VN=3, Mode=4 (server)
			const ntpEpochOffset = 2208988800
			for _, at := range []int{32, 40} { // receive + transmit stamps
				sec := uint32(now.Unix() + ntpEpochOffset)
				frac := uint32((int64(now.Nanosecond()) << 32) / int64(time.Second))
				binary.BigEndian.PutUint32(resp[at:at+4], sec)
				binary.BigEndian.PutUint32(resp[at+4:at+8], frac)
			}
			pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

// A fatally skewed clock FAILS doctor - exit status, not prose. The consequence doctor is
// warning about is total (every signed poll refused, the Tower relays nothing), and a
// service unit only ever reads the exit code.
func TestAFatallySkewedClockFailsDoctor(t *testing.T) {
	addr := fakeSkewedNTP(t, 10*time.Minute)
	p := writeConfig(t, joinedYAML)
	out, err := runCLI(t, "doctor", "--config", p, "--clock-check", "--ntp", addr)
	require.Error(t, err, "a clock past the signature window must fail doctor's exit status")
	require.Contains(t, err.Error(), "problem")
	require.Contains(t, out, "signature window", "the report must say WHY the clock matters here")
}
