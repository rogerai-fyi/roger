package main

import (
	"errors"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/detect"
)

// stubPreflight points the share path's hardware probe at a chosen machine, so the
// reporting and advisory behaviour can be driven both ways on whatever box the suite
// happens to run on. The gatherers behind detectLocalHW are seam'd separately (hwRun,
// hwReadFile, hwDiskFreeMiB) - this seam is above them, at the answer.
func stubPreflight(t *testing.T, hw detect.LocalHW) {
	t.Helper()
	orig := sharePreflight
	sharePreflight = func() detect.Preflight { return detect.Assess(hw) }
	t.Cleanup(func() { sharePreflight = orig })
}

// capableBox / weakBox are the two machines every case below is one of.
func capableBox() detect.LocalHW {
	return detect.LocalHW{
		Class:        detect.HWSingleGPU,
		GPUs:         []detect.LocalGPU{{Model: "NVIDIA GeForce RTX 4090", VRAMMiB: 24564}},
		VRAMTotalMiB: 24564, VRAMKnown: true,
		RAMTotalMiB: 64266, RAMKnown: true,
		DiskFreeMiB: 400000, DiskKnown: true, DiskPath: "/home/op",
		CPUCores: 16,
	}
}

func weakBox() detect.LocalHW {
	return detect.LocalHW{
		Class:       detect.HWCPU,
		RAMTotalMiB: 8192, RAMKnown: true,
		DiskFreeMiB: 4096, DiskKnown: true, DiskPath: "/home/op",
		CPUCores: 2,
	}
}

// TestShareCheckReportsAndExitsWithoutTouchingAnything. `roger share --check` answers a
// question about the MACHINE, so it must not probe the model server, demand a login, or
// register: an operator asking "is this box worth it?" should be able to find out before
// they have set anything up. The register seam is armed and must stay untouched.
func TestShareCheckReportsAndExitsWithoutTouchingAnything(t *testing.T) {
	useTempConfig(t)
	stubPreflight(t, capableBox())

	registered := false
	origStart, origBlock := agentStart, shareBlock
	agentStart = func(agent.Config) (*agent.Session, error) { registered = true; return &agent.Session{}, nil }
	shareBlock = func() {}
	t.Cleanup(func() { agentStart, shareBlock = origStart, origBlock })

	var err error
	out := captureStdout(t, func() { err = cmdShare(config{Broker: "https://b"}, []string{"--check"}) })
	if err != nil {
		t.Fatalf("cmdShare(--check) on a capable box = %v, want nil", err)
	}
	if registered {
		t.Error("`share --check` registered with the broker - it is supposed to report and exit")
	}
	if !strings.Contains(out, "preflight: CLEARS THE BAR") {
		t.Errorf("no verdict in the report:\n%s", out)
	}
	if !strings.Contains(out, "local only") {
		t.Errorf("the report does not tell the operator it is local:\n%s", out)
	}
}

// TestShareCheckExitsNonZeroBelowTheBarAndSaysItIsNotABlock. The non-zero exit is for
// whoever is scripting the decision; it is the only thing here that looks like a refusal,
// so the message it carries has to say plainly that it is not one.
func TestShareCheckExitsNonZeroBelowTheBar(t *testing.T) {
	useTempConfig(t)
	stubPreflight(t, weakBox())

	var err error
	out := captureStdout(t, func() { err = cmdShare(config{Broker: "https://b"}, []string{"--check"}) })
	if !errors.Is(err, errPreflightBelow) {
		t.Fatalf("cmdShare(--check) on a weak box = %v, want errPreflightBelow", err)
	}
	if !strings.Contains(err.Error(), "not blocked") {
		t.Errorf("the non-zero exit does not say sharing is still allowed: %v", err)
	}
	if !strings.Contains(out, "preflight: BELOW THE BAR") {
		t.Errorf("no verdict in the report:\n%s", out)
	}
	if !strings.Contains(out, "what happens if you share anyway") {
		t.Errorf("the report scolds without saying what actually happens:\n%s", out)
	}
}

// TestShareOnAWeakBoxPrintsOneAdvisoryAndSTILLGOESONAIR. This is the requirement that
// outranks every other one in this file: the preflight is advisory. Somebody serving a
// small model on a laptop to their own grant keys is a legitimate user of this software
// and a market-oriented gate must not lock them out.
func TestShareOnAWeakBoxPrintsOneAdvisoryAndStillGoesOnAir(t *testing.T) {
	useTempConfig(t)
	stubPreflight(t, weakBox())
	got := captureShareConfig(t)

	var err error
	out := captureStdout(t, func() {
		err = runShare(t, config{Broker: "https://b", User: "op"},
			[]string{"m1", "--upstream", "http://127.0.0.1:1234/v1"})
	})
	if err != nil {
		t.Fatalf("cmdShare on a below-bar box = %v, want nil - the preflight must never block a share", err)
	}
	if got.Model != "m1" {
		t.Fatalf("the share never reached go-live: %+v", *got)
	}
	if !strings.Contains(out, "on air") {
		t.Errorf("the node did not go on air:\n%s", out)
	}
	if !strings.Contains(out, "below the suggested minimum") {
		t.Errorf("no advisory was printed for a below-bar machine:\n%s", out)
	}
	// One LINE, not the whole report: the on-air line is what the operator is waiting for
	// and a wall of preflight text above it buries the thing they ran the command for.
	if strings.Contains(out, "preflight: BELOW THE BAR") {
		t.Errorf("the full report was dumped on a normal share; --check is where that lives:\n%s", out)
	}
}

// TestShareOnACapableBoxSaysNothing. A machine that clears the bar gets no hardware line
// at all - the on-air output stays as short as it was.
func TestShareOnACapableBoxSaysNothing(t *testing.T) {
	useTempConfig(t)
	stubPreflight(t, capableBox())
	captureShareConfig(t)

	out := captureStdout(t, func() {
		if err := runShare(t, config{Broker: "https://b", User: "op"},
			[]string{"m1", "--upstream", "http://127.0.0.1:1234/v1"}); err != nil {
			t.Errorf("cmdShare = %v, want nil", err)
		}
	})
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  !") {
			t.Errorf("a capable machine was lectured about its hardware: %q", line)
		}
	}
	if strings.Contains(out, "preflight") {
		t.Errorf("the full report was printed on a normal share:\n%s", out)
	}
}

// TestIncompleteIsSilentOnANormalShare. A machine we could not fully measure has done
// nothing wrong. A warning that amounts to "we could not tell", printed on every start,
// is one operators learn to skip past - including on the runs where it says something
// real.
func TestIncompleteIsSilentOnANormalShare(t *testing.T) {
	useTempConfig(t)
	hw := capableBox()
	hw.RAMKnown, hw.RAMTotalMiB = false, 0 // the Windows case
	stubPreflight(t, hw)
	captureShareConfig(t)

	out := captureStdout(t, func() {
		if err := runShare(t, config{Broker: "https://b", User: "op"},
			[]string{"m1", "--upstream", "http://127.0.0.1:1234/v1"}); err != nil {
			t.Errorf("cmdShare = %v, want nil", err)
		}
	})
	// Asserted on the MARKER rather than on the wording: "  !" is the house prefix for an
	// advisory line on this path (softPriceWarn uses it too, and is silent here at price
	// zero). Pinning the wording would let a reworded warning slip through the test that
	// exists to prove no warning is printed.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  !") {
			t.Errorf("an unmeasurable machine was warned as if it had failed: %q", line)
		}
	}
}

// TestShareStillAdvertisesTheBucketAndOnlyTheBucket. cmdShare now takes its advertised
// `hw` off the same probe the preflight ran, which is what stops the class the network
// sees and the report the operator sees from describing two different machines. What must
// NOT have changed is the VALUE: still the four-way privacy bucket, never the rich detail
// sitting right next to it in the same struct.
func TestShareStillAdvertisesTheBucketAndOnlyTheBucket(t *testing.T) {
	useTempConfig(t)
	hw := capableBox()
	hw.Class = detect.HWMultiGPU
	stubPreflight(t, hw)
	got := captureShareConfig(t)

	if err := runShare(t, config{Broker: "https://b", User: "op"},
		[]string{"m1", "--upstream", "http://127.0.0.1:1234/v1"}); err != nil {
		t.Fatalf("cmdShare = %v, want nil", err)
	}
	if got.HW != detect.HWMultiGPU {
		t.Errorf("advertised hw = %q, want the privacy bucket %q", got.HW, detect.HWMultiGPU)
	}
}
