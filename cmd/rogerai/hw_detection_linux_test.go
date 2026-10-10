package main

import "testing"

// TestHWDetection covers the Linux hardware probe (returns whatever the host has; the
// point is to exercise the detection code paths, GPU-less or not). The raw-CPU detectHW()
// helper was removed (superseded by the privacy-bucketed detectHWClass the node advertises).
func TestHWDetection(t *testing.T) {
	if detectHWClass() == "" {
		t.Error("detectHWClass should return a class label")
	}
	// GPU counts are >= 0 (0 on a GPU-less CI box) - just exercise the probes.
	if nvidiaGPUCount() < 0 || rocmGPUCount() < 0 {
		t.Error("GPU counts must be non-negative")
	}
	// runHW is a small command-runner helper: a known command returns output, a missing
	// one returns "" (never panics).
	if out, ok := runHW("echo", "hi"); !ok || out == "" {
		t.Errorf("runHW(echo) = %q/%v, want output", out, ok)
	}
	if _, ok := runHW("definitely-not-a-real-binary-xyz"); ok {
		t.Error("runHW(missing) should report ok=false")
	}
}
