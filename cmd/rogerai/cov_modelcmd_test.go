package main

// Coverage for `roger model`'s small words: the fix offered for each way a fetch fails, the job line
// for each state a fetch can be left in, and a foreground serve that ends when its context does.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/localmodel"
)

func TestModelFixForEveryFailure(t *testing.T) {
	for reason, want := range map[string]string{
		"no_space":     "free some space",
		"gated":        "set HF_TOKEN",
		"corrupt":      "nothing was kept",
		"unreachable":  "check this machine's network",
		"rate_limited": "wait a minute",
		"not_found":    "the file is gone from the index",
	} {
		require.Contains(t, modelFixFor(reason), want, reason)
	}
	require.Empty(t, modelFixFor("something new"), "an unknown failure offers no made-up fix")
}

func TestModelJobLineForEveryState(t *testing.T) {
	j := localmodel.Job{Repo: "acme/Tiny-GGUF", File: "Tiny-Q4_K_M.gguf", Done: 1 << 30, Total: 4 << 30}
	j.State = localmodel.JobFetching
	require.Equal(t, "fetching acme/Tiny-GGUF Tiny-Q4_K_M.gguf: 1.0 GiB of 4.0 GiB", modelJobLine(j))
	j.Rate = 2 << 20
	require.Contains(t, modelJobLine(j), " · 2.0 MiB/s")
	j.State, j.Err = localmodel.JobFailed, "not enough free disk"
	require.Equal(t, "the last fetch (acme/Tiny-GGUF Tiny-Q4_K_M.gguf) failed: not enough free disk", modelJobLine(j))
	j.State = localmodel.JobInterrupted
	require.Contains(t, modelJobLine(j), "was interrupted at 1.0 GiB of 4.0 GiB - run it again and it resumes")
	j.State = ""
	require.Empty(t, modelJobLine(j), "a finished fetch has no line")
}

func TestModelUntilDoneEndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { modelUntilDone(ctx); close(done) }()
	select {
	case <-done:
		t.Fatal("a foreground serve must wait for its end, not return at once")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled serve did not end")
	}
}
