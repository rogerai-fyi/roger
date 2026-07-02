package client

// rc_test.go covers the /remote-control client surface (rc.go, v5.0.0) against a REAL
// httptest broker with REAL request signing - no mocks. Owner-side helpers (enable / list /
// attach / join / rotate / revoke / send / stream) assert the exact wire shape the broker
// sees: the signed method+path+body, the X-Roger-Attach bearer, and the error mapping
// (payoutErr on non-2xx, ErrBrokerUnreachable on transport failure). The host RCBridge is
// driven end to end: poll delivery, 204 re-poll, the 401 self-stop on revoke, the transport
// backoff exit, event batching to /rc/{sid}/events, and the Emit drop rules.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// rcSigned reports whether the request carries a VALID roger signature over exactly raw.
func rcSigned(r *http.Request, raw []byte) bool {
	ts, _ := strconv.ParseInt(r.Header.Get(protocol.HeaderTS), 10, 64)
	_, ok := protocol.VerifyRequest(r.Header.Get(protocol.HeaderPubkey), r.Header.Get(protocol.HeaderSig), ts, r.Method, r.URL.Path, raw)
	return ok
}

// closedServer returns a base URL whose listener is already closed - a guaranteed
// transport error for the ErrBrokerUnreachable branches.
func closedServer() string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

// waitTrue polls cond until it is true or the deadline lapses.
func waitTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestEnableRC: EnableRC signs POST /rc/enable with {"name":...}, parses the one-time enable
// result verbatim, and returns a bridge wired to the session id + host token.
func TestEnableRC(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var gotPath, gotName string
	var signed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotPath = r.Method + " " + r.URL.Path
		signed = rcSigned(r, raw)
		var b map[string]string
		_ = json.Unmarshal(raw, &b)
		gotName = b["name"]
		_ = json.NewEncoder(w).Encode(RCEnableResult{
			SessionID: "rcs_1", Name: b["name"], Code: "RC ABCD-EFGH", CodeShort: "ABCD-EFGH",
			CodeDisplay: "RC 147.520 MHz · ••••-••••", HostToken: "ht_secret", CodeExpires: 1234,
		})
	}))
	defer srv.Close()

	rb, res, err := EnableRC(srv.URL, "otter · RogerAI")
	if err != nil {
		t.Fatalf("EnableRC: %v", err)
	}
	if gotPath != "POST /rc/enable" {
		t.Errorf("path = %q, want POST /rc/enable", gotPath)
	}
	if !signed {
		t.Error("EnableRC must sign the request")
	}
	if gotName != "otter · RogerAI" {
		t.Errorf("name = %q, want the session name in the body", gotName)
	}
	if res.SessionID != "rcs_1" || res.Code != "RC ABCD-EFGH" || res.CodeShort != "ABCD-EFGH" ||
		res.HostToken != "ht_secret" || res.CodeExpires != 1234 {
		t.Errorf("enable result not parsed verbatim: %+v", res)
	}
	if rb == nil || rb.SessionID() != "rcs_1" {
		t.Fatalf("bridge should be wired to the session id, got %+v", rb)
	}
	rb.Stop() // never Run() here; just release the ctx
}

// TestEnableRCErrors: a broker error surfaces its {"error"} message; garbage JSON on a 2xx is
// an error; a dead broker maps to ErrBrokerUnreachable. No bridge in any case.
func TestEnableRCErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{"broker 403", http.StatusForbidden, `{"error":"remote control needs a login"}`, "remote control needs a login"},
		{"bad json on 200", http.StatusOK, `{not json`, "invalid character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			rb, _, err := EnableRC(srv.URL, "n")
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantSub)
			}
			if rb != nil {
				t.Fatal("no bridge on error")
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		_, _, err := EnableRC(closedServer(), "n")
		if !errors.Is(err, ErrBrokerUnreachable) {
			t.Fatalf("err = %v, want ErrBrokerUnreachable", err)
		}
	})
}

// TestListRC: the roster GET is signed, parses sessions verbatim, and maps errors.
func TestListRC(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rc/sessions" || !rcSigned(r, nil) {
			t.Errorf("want a signed GET /rc/sessions, got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"sessions":[{"id":"rcs_1","name":"otter","code_display":"RC ···","online":true,"revoked":false,"created_at":9}]}`)
	}))
	defer srv.Close()
	sessions, err := ListRC(srv.URL)
	if err != nil {
		t.Fatalf("ListRC: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "rcs_1" || sessions[0].Name != "otter" || !sessions[0].Online || sessions[0].CreatedAt != 9 {
		t.Fatalf("roster not parsed: %+v", sessions)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"nope"}`)
	}))
	defer errSrv.Close()
	if _, err := ListRC(errSrv.URL); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("non-2xx should surface the broker message, got %v", err)
	}
	if _, err := ListRC(closedServer()); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("dead broker should map to ErrBrokerUnreachable, got %v", err)
	}
}

// TestListBands: the BASE STATION bands GET is signed, parses bands verbatim, and maps errors.
func TestListBands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/bands" || !rcSigned(r, nil) {
			t.Errorf("want a signed GET /bands, got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"bands":[{"id":"b1","display":"147.520 MHz · ••••","label":"home","node_id":"n1","status":"active","revoked":false}]}`)
	}))
	defer srv.Close()
	bands, err := ListBands(srv.URL)
	if err != nil {
		t.Fatalf("ListBands: %v", err)
	}
	if len(bands) != 1 || bands[0].ID != "b1" || bands[0].Label != "home" || bands[0].Status != "active" {
		t.Fatalf("bands not parsed: %+v", bands)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		fmt.Fprint(w, `{"error":"short and stout"}`)
	}))
	defer errSrv.Close()
	if _, err := ListBands(errSrv.URL); err == nil || !strings.Contains(err.Error(), "short and stout") {
		t.Fatalf("non-2xx should surface the broker message, got %v", err)
	}
	if _, err := ListBands(closedServer()); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("dead broker should map to ErrBrokerUnreachable, got %v", err)
	}
}

// TestAttachRC: attach signs POST /rc/attach with {"code"} and returns the attach token.
func TestAttachRC(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/rc/attach" || !rcSigned(r, raw) {
			t.Errorf("want a signed POST /rc/attach, got %s %s", r.Method, r.URL.Path)
		}
		var b map[string]string
		_ = json.Unmarshal(raw, &b)
		if b["code"] != "RC ABCD-EFGH" {
			t.Errorf("code = %q, want the pasted code verbatim", b["code"])
		}
		fmt.Fprint(w, `{"session_id":"rcs_1","name":"otter","attach_token":"at_1"}`)
	}))
	defer srv.Close()
	att, err := AttachRC(srv.URL, "RC ABCD-EFGH")
	if err != nil {
		t.Fatalf("AttachRC: %v", err)
	}
	if att.SessionID != "rcs_1" || att.Name != "otter" || att.AttachToken != "at_1" {
		t.Fatalf("attach result not parsed: %+v", att)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"no station on this code"}`)
	}))
	defer errSrv.Close()
	if _, err := AttachRC(errSrv.URL, "x"); err == nil || !strings.Contains(err.Error(), "no station on this code") {
		t.Fatalf("non-2xx should surface the broker message, got %v", err)
	}
	if _, err := AttachRC(closedServer(), "x"); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("dead broker should map to ErrBrokerUnreachable, got %v", err)
	}
}

// TestJoinRC: the owner join signs POST /rc/{sid}/join (no body) and returns the token.
func TestJoinRC(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rc/rcs_9/join" || !rcSigned(r, nil) {
			t.Errorf("want a signed POST /rc/rcs_9/join, got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"session_id":"rcs_9","attach_token":"at_join"}`)
	}))
	defer srv.Close()
	token, err := JoinRC(srv.URL, "rcs_9")
	if err != nil {
		t.Fatalf("JoinRC: %v", err)
	}
	if token != "at_join" {
		t.Fatalf("token = %q, want at_join", token)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"not yours"}`)
	}))
	defer errSrv.Close()
	if _, err := JoinRC(errSrv.URL, "rcs_9"); err == nil || !strings.Contains(err.Error(), "not yours") {
		t.Fatalf("non-2xx should surface the broker message, got %v", err)
	}
	if _, err := JoinRC(closedServer(), "rcs_9"); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("dead broker should map to ErrBrokerUnreachable, got %v", err)
	}
}

// TestRotateRCCode: rotate signs POST /rc/{sid}/code and returns the fresh code + short tail.
func TestRotateRCCode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rc/rcs_2/code" || !rcSigned(r, nil) {
			t.Errorf("want a signed POST /rc/rcs_2/code, got %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"code":"RC WXYZ-1234","code_short":"WXYZ-1234"}`)
	}))
	defer srv.Close()
	code, short, err := RotateRCCode(srv.URL, "rcs_2")
	if err != nil {
		t.Fatalf("RotateRCCode: %v", err)
	}
	if code != "RC WXYZ-1234" || short != "WXYZ-1234" {
		t.Fatalf("code/short = %q/%q, want the minted pair", code, short)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		fmt.Fprint(w, `{"error":"session ended"}`)
	}))
	defer errSrv.Close()
	if _, _, err := RotateRCCode(errSrv.URL, "rcs_2"); err == nil || !strings.Contains(err.Error(), "session ended") {
		t.Fatalf("non-2xx should surface the broker message, got %v", err)
	}
	if _, _, err := RotateRCCode(closedServer(), "rcs_2"); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("dead broker should map to ErrBrokerUnreachable, got %v", err)
	}
}

// TestRevokeRC: an id revokes ONE session (/rc/{id}/disable); no id revokes ALL
// (/rc/revoke-all). Both are signed over the EXACT nil body the broker verifies
// (the {} body regression would 403), and errors map like every owner call.
func TestRevokeRC(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cases := []struct {
		name, id, wantPath string
	}{
		{"one session", "rcs_3", "/rc/rcs_3/disable"},
		{"all sessions", "", "/rc/revoke-all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var signed bool
			var bodyLen int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				bodyLen = int64(len(raw))
				gotPath = r.URL.Path
				signed = rcSigned(r, raw)
			}))
			defer srv.Close()
			if err := RevokeRC(srv.URL, tc.id); err != nil {
				t.Fatalf("RevokeRC: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if !signed {
				t.Error("RevokeRC must sign over the empty body the broker verifies")
			}
			if bodyLen != 0 {
				t.Errorf("body must stay empty (signature parity with the broker), got %d bytes", bodyLen)
			}
		})
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"not yours"}`)
	}))
	defer errSrv.Close()
	if err := RevokeRC(errSrv.URL, "rcs_3"); err == nil || !strings.Contains(err.Error(), "not yours") {
		t.Fatalf("non-2xx should surface the broker message, got %v", err)
	}
	if err := RevokeRC(closedServer(), ""); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("dead broker should map to ErrBrokerUnreachable, got %v", err)
	}
}

// TestSendRC: a viewer turn posts signed JSON to /rc/{sid}/send with the X-Roger-Attach
// bearer, round-tripping the RCInbound verbatim.
func TestSendRC(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var got protocol.RCInbound
	var attach string
	var signed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		signed = rcSigned(r, raw)
		attach = r.Header.Get("X-Roger-Attach")
		if r.URL.Path != "/rc/rcs_4/send" {
			t.Errorf("path = %q, want /rc/rcs_4/send", r.URL.Path)
		}
		_ = json.Unmarshal(raw, &got)
	}))
	defer srv.Close()
	in := protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: true, ConfirmID: "cf1", Origin: "cli"}
	if err := SendRC(srv.URL, "rcs_4", "at_send", in); err != nil {
		t.Fatalf("SendRC: %v", err)
	}
	if !signed {
		t.Error("SendRC must sign the request")
	}
	if attach != "at_send" {
		t.Errorf("X-Roger-Attach = %q, want at_send", attach)
	}
	if got.Kind != protocol.RCInConfirm || !got.Approve || got.ConfirmID != "cf1" || got.Origin != "cli" {
		t.Errorf("inbound did not round-trip: %+v", got)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"bad attach token"}`)
	}))
	defer errSrv.Close()
	if err := SendRC(errSrv.URL, "s", "a", in); err == nil || !strings.Contains(err.Error(), "bad attach token") {
		t.Fatalf("non-2xx should surface the broker message, got %v", err)
	}
	if err := SendRC(closedServer(), "s", "a", in); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("dead broker should map to ErrBrokerUnreachable, got %v", err)
	}
}

// rcSSE writes one SSE data frame.
func rcSSE(w http.ResponseWriter, f protocol.RCFrame) {
	b, _ := json.Marshal(f)
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", f.Seq, b)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

// TestStreamRC: the viewer stream delivers each data: frame in order, skips id:/comment/
// garbage lines, and returns nil at the terminal ended frame. Last-Event-ID rides only when
// lastSeq > 0, and the attach bearer + signature are on the GET.
func TestStreamRC(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cases := []struct {
		name     string
		lastSeq  uint64
		wantLast string
	}{
		{"fresh stream (no Last-Event-ID)", 0, ""},
		{"resume from seq 7", 7, "7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lastID, attach string
			var signed bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				lastID = r.Header.Get("Last-Event-ID")
				attach = r.Header.Get("X-Roger-Attach")
				signed = rcSigned(r, nil)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": comment line\n\n")
				rcSSE(w, protocol.RCFrame{Seq: 8, Kind: protocol.RCKindUser, Origin: "local", Text: "hi"})
				fmt.Fprint(w, "data: {not json\n\n") // must be skipped, not fatal
				rcSSE(w, protocol.RCFrame{Seq: 9, Kind: protocol.RCKindAssistant, Text: "hello"})
				rcSSE(w, protocol.RCFrame{Seq: 10, Kind: protocol.RCKindEnded})
				rcSSE(w, protocol.RCFrame{Seq: 11, Kind: protocol.RCKindUser, Text: "after end - never seen"})
			}))
			defer srv.Close()

			var got []protocol.RCFrame
			err := StreamRC(context.Background(), srv.URL, "rcs_5", "at_stream", tc.lastSeq, func(f protocol.RCFrame) {
				got = append(got, f)
			})
			if err != nil {
				t.Fatalf("StreamRC: %v", err)
			}
			if lastID != tc.wantLast {
				t.Errorf("Last-Event-ID = %q, want %q", lastID, tc.wantLast)
			}
			if attach != "at_stream" || !signed {
				t.Errorf("stream GET must carry the attach bearer + signature (attach=%q signed=%v)", attach, signed)
			}
			if len(got) != 3 || got[0].Text != "hi" || got[1].Text != "hello" || got[2].Kind != protocol.RCKindEnded {
				t.Fatalf("frames = %+v, want user+assistant+ended and NOTHING after ended", got)
			}
		})
	}
}

// TestStreamRCEOFAndErrors: a stream that just closes (no ended) returns nil; a non-2xx maps
// the broker message; a dead broker maps to ErrBrokerUnreachable.
func TestStreamRCEOFAndErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rcSSE(w, protocol.RCFrame{Seq: 1, Kind: protocol.RCKindUser, Text: "only one"})
	}))
	defer srv.Close()
	var n int
	if err := StreamRC(context.Background(), srv.URL, "s", "a", 0, func(protocol.RCFrame) { n++ }); err != nil {
		t.Fatalf("EOF without ended should return nil, got %v", err)
	}
	if n != 1 {
		t.Fatalf("frames = %d, want 1", n)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"attach token expired"}`)
	}))
	defer errSrv.Close()
	err := StreamRC(context.Background(), errSrv.URL, "s", "a", 0, func(protocol.RCFrame) {})
	if err == nil || !strings.Contains(err.Error(), "attach token expired") {
		t.Fatalf("non-2xx should surface the broker message, got %v", err)
	}
	if err := StreamRC(context.Background(), closedServer(), "s", "a", 0, func(protocol.RCFrame) {}); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("dead broker should map to ErrBrokerUnreachable, got %v", err)
	}
}

// enableBridge builds a live RCBridge against mux via the real EnableRC handshake.
func enableBridge(t *testing.T, mux *http.ServeMux) (*RCBridge, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/rc/enable", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"session_id":"rcs_b","host_token":"ht_b"}`)
	})
	rb, _, err := EnableRC(srv.URL, "bridge test")
	if err != nil {
		t.Fatalf("EnableRC: %v", err)
	}
	t.Cleanup(rb.Stop)
	return rb, srv
}

// TestRCBridgeEmitRules: a nil bridge Emit is a no-op; a stopped bridge drops; a FULL buffer
// drops instead of blocking the UI goroutine; Stop is idempotent and closes Done.
func TestRCBridgeEmitRules(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var nilBridge *RCBridge
	nilBridge.Emit(protocol.RCFrame{Kind: protocol.RCKindUser}) // must not panic

	rb, _ := enableBridge(t, http.NewServeMux())
	// Never Run(): fill the 256-frame buffer past the brim; the overflow must not block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 300; i++ {
			rb.Emit(protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Emit must DROP on a full buffer, not block the UI goroutine")
	}

	select {
	case <-rb.Done():
		t.Fatal("Done must stay open before Stop")
	default:
	}
	rb.Stop()
	rb.Stop() // idempotent
	select {
	case <-rb.Done():
	default:
		t.Fatal("Done must be closed after Stop")
	}
	rb.Emit(protocol.RCFrame{Kind: protocol.RCKindUser}) // stopped: dropped, no panic
}

// TestRCBridgePollDelivery: Run()'s poll loop delivers a broker turn onto Inbound(), treats a
// 204 as a silent re-poll, skips a garbage body, and on a 401 (session revoked) stops the
// bridge cleanly - Done closes without any local Stop call.
func TestRCBridgePollDelivery(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mux := http.NewServeMux()
	var polls atomic.Int64
	var auth atomic.Value
	mux.HandleFunc("/rc/rcs_b/poll", func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		switch polls.Add(1) {
		case 1:
			fmt.Fprint(w, `{"kind":"turn","text":"remote says hi","origin":"phone"}`)
		case 2:
			w.WriteHeader(http.StatusNoContent) // normal empty poll
		case 3:
			fmt.Fprint(w, `{garbage`) // ignored, loop continues
		default:
			w.WriteHeader(http.StatusUnauthorized) // revoked: the bridge must self-stop
		}
	})
	mux.HandleFunc("/rc/rcs_b/events", func(w http.ResponseWriter, r *http.Request) {})
	rb, _ := enableBridge(t, mux)
	rb.Run()

	select {
	case in := <-rb.Inbound():
		if in.Kind != protocol.RCInTurn || in.Text != "remote says hi" || in.Origin != "phone" {
			t.Fatalf("inbound = %+v, want the remote turn verbatim", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the poll loop should deliver the remote turn onto Inbound()")
	}
	select {
	case <-rb.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a 401 poll (revoked session) must Stop the bridge")
	}
	if got, _ := auth.Load().(string); got != "Bearer ht_b" {
		t.Fatalf("poll auth = %q, want the HOST TOKEN bearer", got)
	}
	if polls.Load() < 4 {
		t.Fatalf("polls = %d, want the 204 + garbage re-polls before the 401", polls.Load())
	}
}

// TestRCBridgeEventPumpBatches: emitted frames are POSTed to /rc/{sid}/events with the host
// bearer; frames emitted together coalesce into one batch. Disable revokes AND stops.
func TestRCBridgeEventPumpBatches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mux := http.NewServeMux()
	var mu sync.Mutex
	var got []protocol.RCFrame
	var batches int
	var auth string
	var disabled atomic.Bool
	mux.HandleFunc("/rc/rcs_b/poll", func(w http.ResponseWriter, r *http.Request) {
		if disabled.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/rc/rcs_b/events", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var frames []protocol.RCFrame
		_ = json.Unmarshal(raw, &frames)
		mu.Lock()
		got = append(got, frames...)
		batches++
		auth = r.Header.Get("Authorization")
		mu.Unlock()
	})
	mux.HandleFunc("/rc/rcs_b/disable", func(w http.ResponseWriter, r *http.Request) {
		disabled.Store(true)
	})
	rb, _ := enableBridge(t, mux)

	// Queue three frames BEFORE Run so the pump drains them as one coalesced batch.
	rb.Emit(protocol.RCFrame{Kind: protocol.RCKindUser, Text: "one"})
	rb.Emit(protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "two"})
	rb.Emit(protocol.RCFrame{Kind: protocol.RCKindFinal, Text: "three"})
	rb.Run()

	waitTrue(t, "all three frames to reach /events", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 3
	})
	mu.Lock()
	if got[0].Text != "one" || got[1].Text != "two" || got[2].Text != "three" {
		t.Fatalf("frames out of order: %+v", got)
	}
	if batches != 1 {
		t.Errorf("batches = %d, want the burst coalesced into 1 POST", batches)
	}
	if auth != "Bearer ht_b" {
		t.Errorf("events auth = %q, want the HOST TOKEN bearer", auth)
	}
	mu.Unlock()

	if err := rb.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if !disabled.Load() {
		t.Fatal("Disable must revoke the session on the broker")
	}
	select {
	case <-rb.Done():
	default:
		t.Fatal("Disable must stop the bridge")
	}
}

// TestRCBridgePollBackoffStop: with the broker gone, the poll loop enters backoff; Stop()
// during the backoff wait exits promptly (no goroutine parked on a dead broker). postEvents
// against the dead broker is best-effort (no panic).
func TestRCBridgePollBackoffStop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rb, srv := enableBridge(t, http.NewServeMux())
	srv.Close() // the broker vanishes before Run
	rb.Run()
	rb.Emit(protocol.RCFrame{Kind: protocol.RCKindUser, Text: "into the void"}) // postEvents error path
	time.Sleep(50 * time.Millisecond)                                           // let both loops hit the dead broker
	stopDone := make(chan struct{})
	go func() { rb.Stop(); close(stopDone) }()
	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop must interrupt the poll backoff promptly")
	}
	select {
	case <-rb.Done():
	case <-time.After(time.Second):
		t.Fatal("Done must close after Stop")
	}
}
