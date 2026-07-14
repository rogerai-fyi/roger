package harness

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestExtendableTimeoutExpires proves the deadline fires with cause DeadlineExceeded
// (a timeout, not an abort) and that Deadline() is reported so BrokerCompleter's
// default-timeout fallback stays out of the way.
func TestExtendableTimeoutExpires(t *testing.T) {
	ctx, _, cancel := ExtendableTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, has := ctx.Deadline(); !has {
		t.Fatal("ExtendableTimeout ctx must report a deadline")
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("ctx did not expire")
	}
	if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		t.Fatalf("cause = %v, want DeadlineExceeded", context.Cause(ctx))
	}
}

// TestExtendableTimeoutExtend proves a grant pushes the deadline back: the ctx
// outlives its original deadline and only expires after the extension.
func TestExtendableTimeoutExtend(t *testing.T) {
	ctx, extend, cancel := ExtendableTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	extend(150 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatal("ctx expired at the ORIGINAL deadline despite the extension")
	case <-time.After(90 * time.Millisecond):
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("ctx never expired after the extension ran out")
	}
}

// TestExtendableTimeoutCancel proves cancel reads as an abort (cause Canceled), the
// shape BrokerCompleter maps to "turn cancelled".
func TestExtendableTimeoutCancel(t *testing.T) {
	ctx, _, cancel := ExtendableTimeout(context.Background(), time.Hour)
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop the ctx")
	}
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatalf("cause = %v, want Canceled", context.Cause(ctx))
	}
}
