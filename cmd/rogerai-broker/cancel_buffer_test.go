package main

// cancel_buffer_test.go: every cancel is buffered, even with a subscriber listening, so a
// cancel published while a poll was answering another one is never lost (slice-6 audit
// 2026-10-06). The publish is only a wake-up; the poll drains the buffer.

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

func TestCancelPushBuffersWithASubscriber(t *testing.T) {
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = vs.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgs, unsub, err := vs.cancelSubscribe(ctx, "n1")
	require.NoError(t, err)
	defer unsub()
	time.Sleep(50 * time.Millisecond) // let the subscription land

	require.NoError(t, vs.cancelPush("n1", []byte(`{"id":"j1","until":9999999999999}`), time.Minute))
	require.NoError(t, vs.cancelPush("n1", []byte(`{"id":"j2","until":9999999999999}`), time.Minute))
	select {
	case <-msgs:
	case <-time.After(2 * time.Second):
		t.Fatal("no wake-up published to the subscriber")
	}
	got, err := vs.cancelDrain("n1")
	require.NoError(t, err)
	require.Len(t, got, 2, "both cancels are in the buffer, though a subscriber was listening")
}
