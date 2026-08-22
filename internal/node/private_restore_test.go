package node

import (
	"errors"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/agent"
)

// TestTogglePrivateRestoresOnFailure pins the invariant that a REJECTED visibility change
// is a no-op, not an outage.
//
// The founder hit this live: flipping an on-air model to a private band returned "broker
// rejected registration", and because TogglePrivate is a stop-then-start, the model that
// had been happily serving the public market was left OFF AIR. The operator asked to
// change how a row is LISTED; a broker refusal must never cost them the broadcast they
// already had.
func TestTogglePrivateRestoresOnFailure(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetLoggedIn(true)

	// free-1 is happily on air, public.
	if res := c.ToggleOnAir("free-1"); res.Err != nil {
		t.Fatalf("free-1 on air: %+v", res)
	}
	if c.OnAirCount() != 1 {
		t.Fatalf("setup: on-air count = %d, want 1", c.OnAirCount())
	}

	// The broker now refuses PRIVATE registrations (the real 403: band quota / owner
	// binding) but still accepts public ones - exactly the live shape.
	real := startAgent
	defer func() { startAgent = real }()
	rejected := errors.New("register with https://broker.example: broker rejected registration (403): private band limit reached (free plan allows 1) - revoke an existing band first")
	startAgent = func(cfg agent.Config) (*agent.Session, error) {
		if cfg.Private {
			return nil, rejected
		}
		return real(cfg)
	}

	res := c.TogglePrivate("free-1")

	if res.Err == nil {
		t.Fatal("a refused private registration must surface its error")
	}
	if !res.Restored {
		t.Error("Restored must report that the previous session was put back")
	}
	if res.NowPrivate || c.Private()["free-1"] {
		t.Error("a failed flip must leave the row public")
	}
	// The whole point: still broadcasting.
	if c.OnAirCount() != 1 {
		t.Fatalf("a failed private flip took the model OFF AIR (on-air count = %d, want 1)", c.OnAirCount())
	}
	if c.Sessions()["free-1"] == nil {
		t.Error("free-1 must still hold a live session after a failed flip")
	}
}

// TestTogglePrivateReportsUnrestorableOutage: when the restore ALSO fails there is no
// pretending - Restored stays false so the front-ends say the row went off air rather
// than claiming an unchanged broadcast that is not running.
func TestTogglePrivateReportsUnrestorableOutage(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetLoggedIn(true)
	if res := c.ToggleOnAir("free-1"); res.Err != nil {
		t.Fatalf("free-1 on air: %+v", res)
	}

	real := startAgent
	defer func() { startAgent = real }()
	startAgent = func(agent.Config) (*agent.Session, error) { return nil, errors.New("broker unreachable") }

	res := c.TogglePrivate("free-1")
	if res.Err == nil {
		t.Fatal("want the start error")
	}
	if res.Restored {
		t.Error("Restored must be false when the row could not be put back")
	}
	if c.OnAirCount() != 0 {
		t.Fatalf("on-air count = %d, want 0 (nothing could be restored)", c.OnAirCount())
	}
}

// TestErrReasonLeadsWithTheActionableClause: the status bar is ONE line, so the broker's
// own sentence has to come first or it is the part that gets clipped. This is the bug the
// founder saw as "...: brok".
func TestErrReasonLeadsWithTheActionableClause(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "full register + rejection wrapping is stripped to the reason",
			err:  errors.New(`register with https://broker.rogerai.fyi: broker rejected registration (403): a private band requires a GitHub-linked owner - run ` + "`roger login`"),
			want: "a private band requires a GitHub-linked owner - run `roger login`",
		},
		{
			name: "band quota reason survives",
			err:  errors.New("register with https://broker.rogerai.fyi: broker rejected registration (403): private band limit reached (free plan allows 1) - revoke an existing band first"),
			want: "private band limit reached (free plan allows 1) - revoke an existing band first",
		},
		{
			name: "an empty body keeps the status code, the only signal there is",
			err:  errors.New("register with https://broker.example: broker returned status 502"),
			want: "broker returned status 502",
		},
		{
			name: "a transport failure is passed through untouched",
			err:  errors.New(`Post "https://broker.example/nodes/register": dial tcp: connection refused`),
			want: `Post "https://broker.example/nodes/register": dial tcp: connection refused`,
		},
		{name: "nil is empty", err: nil, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrReason(tc.err); got != tc.want {
				t.Errorf("ErrReason()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestErrReasonFitsANarrowStatusBar is the regression in its original form: the reason must
// survive an 80-column clip, which the un-stripped chain did not.
func TestErrReasonFitsANarrowStatusBar(t *testing.T) {
	err := errors.New("register with https://broker.rogerai.fyi: broker rejected registration (403): private band limit reached (free plan allows 1)")
	reason := ErrReason(err)
	if len(reason) > 78 {
		t.Fatalf("reason is %d chars, too long to survive a status bar: %q", len(reason), reason)
	}
	if !strings.Contains(reason, "private band limit reached") {
		t.Errorf("the actionable clause was lost: %q", reason)
	}
}
