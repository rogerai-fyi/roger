package main

import (
	"os"
	"testing"
)

// TestMainHandsOverToTheShim runs main with the loop guard set, so the shim answers without
// handing the test process to anything: main passes the shim's exit code to the process.
func TestMainHandsOverToTheShim(t *testing.T) {
	t.Setenv("ROGER_TOWER_SHIM_HOP", "1")
	got := -1
	osExit = func(c int) { got = c }
	defer func() { osExit = os.Exit }()
	main()
	if got != 1 {
		t.Fatalf("main exited %d, want the shim's loop refusal (1)", got)
	}
}
