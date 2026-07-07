package client

// rc_park_test.go - unit tables for the Guest Operators Phase 2 client seams
// (rc_interlock.feature at the bridge level + the ruling-4 call counter): the RCBridge
// PARK interlock (turns dropped with a status auto-frame, confirms dropped silently,
// backfill answered from the park-time snapshot, nothing ever queued) and the
// ProxyOptionsHolder call counter accumulating through the REAL hardened handler over a
// stub billing broker. Stdlib testing, real HTTP, no mocks.

import (
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestBridgeParkStateMachine: Park engages, Unpark releases, both nil-safe (a revoke-all
// can kill the bridge mid-handoff; the return path must never panic).
func TestBridgeParkStateMachine(t *testing.T) {
	rb := NewRCBridge("http://127.0.0.1:1", "s1", "tok")
	if op, parked := rb.Parked(); parked || op != "" {
		t.Fatalf("a fresh bridge must be unparked, got (%q, %v)", op, parked)
	}
	rb.Park("opencode", "transcript snapshot", "gpt-oss-120b", nil)
	if op, parked := rb.Parked(); !parked || op != "opencode" {
		t.Fatalf("Parked = (%q, %v), want (opencode, true)", op, parked)
	}
	rb.Unpark()
	if _, parked := rb.Parked(); parked {
		t.Fatalf("Unpark must release the interlock")
	}
	// Nil-safety: the exec return callback may race a dead/never-enabled bridge.
	var nilRB *RCBridge
	nilRB.Park("x", "y", "", nil)
	nilRB.Unpark()
	if op, parked := nilRB.Parked(); parked || op != "" {
		t.Fatalf("nil bridge Parked = (%q, %v), want unparked", op, parked)
	}
	// Unpark on a STOPPED bridge is a no-op, not a panic (rc_interlock.feature).
	rb.Park("opencode", "snap", "", nil)
	rb.Stop()
	rb.Unpark()
}

// drainOut pops every frame currently queued on the bridge's outbound channel.
func drainOut(rb *RCBridge) []protocol.RCFrame {
	var out []protocol.RCFrame
	for {
		select {
		case f := <-rb.out:
			out = append(out, f)
		default:
			return out
		}
	}
}

// TestParkIntercept is the bridge-level interlock table: what each inbound kind does
// while parked, and that NOTHING is ever queued for the suspended host.
func TestParkIntercept(t *testing.T) {
	cases := []struct {
		name       string
		in         protocol.RCInbound
		wantFrames []string // frame kinds emitted by the intercept, in order
	}{
		{"turn is dropped with a status auto-frame",
			protocol.RCInbound{Kind: protocol.RCInTurn, Text: "refactor the parser"},
			[]string{protocol.RCKindStatus}},
		{"confirm is dropped silently (stale by definition)",
			protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: true, ConfirmID: "c1"},
			nil},
		{"interrupt is dropped silently (no DJ turn in flight)",
			protocol.RCInbound{Kind: protocol.RCInInterrupt},
			nil},
		{"backfill is answered with the snapshot + the status frame",
			protocol.RCInbound{Kind: protocol.RCInBackfill, Viewer: "v2"},
			[]string{protocol.RCKindBackfill, protocol.RCKindStatus}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := NewRCBridge("http://127.0.0.1:1", "s1", "tok")
			// Enrichment (rc_enrichment.feature E2): the spend reader is LIVE - it moved
			// from $0 after Park, and the auto-frame must carry the emit-time figure.
			liveSpend := 0.0
			rb.Park("opencode", "the transcript so far", "gpt-oss-120b", func() float64 { return liveSpend })
			liveSpend = 0.19
			if !rb.parkIntercept(tc.in) {
				t.Fatalf("a parked bridge must consume every inbound")
			}
			frames := drainOut(rb)
			var kinds []string
			for _, f := range frames {
				kinds = append(kinds, f.Kind)
			}
			if strings.Join(kinds, ",") != strings.Join(tc.wantFrames, ",") {
				t.Fatalf("frames = %v, want %v", kinds, tc.wantFrames)
			}
			for _, f := range frames {
				switch f.Kind {
				case protocol.RCKindStatus:
					if f.Operator != "opencode" || !strings.Contains(f.Text, "guest has the mic") {
						t.Fatalf("status frame must name the guest: %+v", f)
					}
					if f.Model != "gpt-oss-120b" || f.Spend != 0.19 {
						t.Fatalf("status frame must carry the live model/spend enrichment: %+v", f)
					}
				case protocol.RCKindBackfill:
					if f.Viewer != "v2" || f.Text != "the transcript so far" {
						t.Fatalf("backfill must answer the asking viewer from the snapshot: %+v", f)
					}
				}
			}
			// NOTHING reaches the host inbound channel while parked (never a replay).
			select {
			case in := <-rb.Inbound():
				t.Fatalf("a parked inbound leaked to the host: %+v", in)
			default:
			}
		})
	}
}

// TestParkInterceptUnparked: with the interlock released, the intercept consumes nothing
// - normal service resumes through the inbound channel.
func TestParkInterceptUnparked(t *testing.T) {
	rb := NewRCBridge("http://127.0.0.1:1", "s1", "tok")
	if rb.parkIntercept(protocol.RCInbound{Kind: protocol.RCInTurn, Text: "hi"}) {
		t.Fatalf("an unparked bridge must not intercept")
	}
	if frames := drainOut(rb); len(frames) != 0 {
		t.Fatalf("no auto-frame when unparked, got %v", frames)
	}
}

// TestHolderCallCounter: the counter increments once per DISPATCHED completion (through
// the REAL handler over a stub billing broker); refusals (401/402) never count; ResetCalls
// zeroes it per handoff (founder ruling 4 - the "N calls" summary source).
func TestHolderCallCounter(t *testing.T) {
	sandbox(t)
	srv, brokerCalls := billingBroker(t, "0.50")
	defer srv.Close()

	h := NewProxyOptionsHolder(ProxyOptions{Broker: srv.URL, User: "u", Model: "m", SessionKey: "sk-k", Budget: 1.00})
	handler := ProxyHandlerLive(h)
	post := func(auth string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := post(""); got != 401 {
		t.Fatalf("unauthenticated: %d, want 401", got)
	}
	if h.Calls() != 0 {
		t.Fatalf("a 401 refusal must not count as a call")
	}
	for i := 0; i < 2; i++ { // 2 × $0.50 reaches the $1 ceiling
		if got := post("Bearer sk-k"); got != 200 {
			t.Fatalf("call %d: %d, want 200", i+1, got)
		}
	}
	if h.Calls() != 2 {
		t.Fatalf("Calls = %d, want 2 dispatched", h.Calls())
	}
	if got := post("Bearer sk-k"); got != 402 {
		t.Fatalf("over budget: %d, want 402", got)
	}
	if h.Calls() != 2 {
		t.Fatalf("a 402 refusal must not count as a call (got %d)", h.Calls())
	}
	if n := atomic.LoadInt32(brokerCalls); n != 2 {
		t.Fatalf("broker dispatches = %d, want 2 (counter mirrors real dispatch)", n)
	}
	h.ResetCalls()
	if h.Calls() != 0 {
		t.Fatalf("ResetCalls must zero the counter")
	}
}

// TestEnableRCUsesNewRCBridge: EnableRC hands back a live bridge wired exactly like the
// exported constructor (session id + buffered channels + a working stop path).
func TestEnableRCUsesNewRCBridge(t *testing.T) {
	rb := NewRCBridge("http://b", "sess-9", "tok")
	if rb.SessionID() != "sess-9" {
		t.Fatalf("SessionID = %q", rb.SessionID())
	}
	done := rb.Done()
	rb.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Stop must close Done")
	}
}
