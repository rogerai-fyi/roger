package main

// remote_test.go covers the `roger remote` CLI (remote.go, v5.0.0) end to end against a REAL
// httptest broker - no mocks: the roster/off/link calls run the signed client helpers, the
// attach test streams REAL SSE frames of every kind and asserts the printed narration, and
// the input loop reads a REAL swapped os.Stdin pipe so the y/n confirm gate is exercised the
// way a terminal drives it (a bare y/n only answers while a confirm is actually pending).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/tui"
)

// TestConfirmGate: set arms the gate with an id; take consumes it exactly once; clear disarms.
func TestConfirmGate(t *testing.T) {
	g := &confirmGate{}
	if p, id := g.take(); p || id != "" {
		t.Fatalf("an empty gate must not report pending, got %v %q", p, id)
	}
	g.set("cf1")
	p, id := g.take()
	if !p || id != "cf1" {
		t.Fatalf("take = %v %q, want pending cf1", p, id)
	}
	if p, _ := g.take(); p {
		t.Fatal("take must consume the pending confirm (second take empty)")
	}
	g.set("cf2")
	g.clear()
	if p, _ := g.take(); p {
		t.Fatal("clear must disarm the gate")
	}
}

// TestRCLinkURLShape: the deep link is r.html (static-host exact path) with the code in the
// FRAGMENT so it never reaches server logs; an empty short yields the bare page.
func TestRCLinkURLShape(t *testing.T) {
	cases := []struct{ short, want string }{
		{"", "https://rogerai.fyi/r.html"},
		{"8FK3-9MQ2", "https://rogerai.fyi/r.html#8FK3-9MQ2"},
	}
	for _, tc := range cases {
		if got := rcLinkURL(tc.short); got != tc.want {
			t.Errorf("rcLinkURL(%q) = %q, want %q", tc.short, got, tc.want)
		}
	}
}

// rcRosterServer serves a canned signed-roster broker for list/off/link tests.
func rcRosterServer(t *testing.T, roster string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rc/sessions":
			fmt.Fprint(w, roster)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRemoteListStates: the roster prints the honesty line then one row per session with the
// right state glyph - ● live / ○ offline / · ended - and the attach hint.
func TestRemoteListStates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := rcRosterServer(t, `{"sessions":[
		{"id":"rcs_live","name":"otter · repo","online":true,"revoked":false},
		{"id":"rcs_off","name":"otter · lab","online":false,"revoked":false},
		{"id":"rcs_done","name":"otter · old","online":true,"revoked":true}]}`)
	out := captureStdout(t, func() {
		if err := remoteList(config{Broker: srv.URL}); err != nil {
			t.Errorf("remoteList: %v", err)
		}
	})
	if !strings.Contains(out, "relayed via the broker (TLS, not E2E)") {
		t.Errorf("the honesty line must lead the roster:\n%s", out)
	}
	for _, want := range []string{"● live", "○ offline", "· ended", "rcs_live", "otter · lab", "roger remote attach <code>"} {
		if !strings.Contains(out, want) {
			t.Errorf("roster output missing %q:\n%s", want, out)
		}
	}
}

// TestRemoteListEmptyAndError: no sessions prints the how-to hint; a broker error surfaces.
func TestRemoteListEmptyAndError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	empty := rcRosterServer(t, `{"sessions":[]}`)
	out := captureStdout(t, func() {
		if err := remoteList(config{Broker: empty.URL}); err != nil {
			t.Errorf("remoteList: %v", err)
		}
	})
	if !strings.Contains(out, "no remote sessions") || !strings.Contains(out, "/remote-control") {
		t.Errorf("empty roster should point at /remote-control:\n%s", out)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"login required"}`)
	}))
	defer errSrv.Close()
	if err := remoteList(config{Broker: errSrv.URL}); err == nil || !strings.Contains(err.Error(), "login required") {
		t.Fatalf("a broker error must surface, got %v", err)
	}
}

// TestRemoteOff: no id revokes ALL (/rc/revoke-all); an id revokes ONE (/rc/{id}/disable);
// each prints its own confirmation; a broker error surfaces.
func TestRemoteOff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	out := captureStdout(t, func() {
		if err := remoteOff(config{Broker: srv.URL}, ""); err != nil {
			t.Errorf("remoteOff all: %v", err)
		}
		if err := remoteOff(config{Broker: srv.URL}, "rcs_7"); err != nil {
			t.Errorf("remoteOff one: %v", err)
		}
	})
	if len(paths) != 2 || paths[0] != "/rc/revoke-all" || paths[1] != "/rc/rcs_7/disable" {
		t.Fatalf("paths = %v, want revoke-all then rcs_7/disable", paths)
	}
	if !strings.Contains(out, "all remote sessions ended.") || !strings.Contains(out, "remote session rcs_7 ended.") {
		t.Errorf("confirmations missing:\n%s", out)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"not yours"}`)
	}))
	defer errSrv.Close()
	if err := remoteOff(config{Broker: errSrv.URL}, "x"); err == nil || !strings.Contains(err.Error(), "not yours") {
		t.Fatalf("a broker error must surface, got %v", err)
	}
}

// TestRemoteLinkCode: minting prints the one-time code, the phone URL (fragment deep link),
// and the terminal attach hint; a broker error surfaces.
func TestRemoteLinkCode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rc/rcs_5/code" {
			t.Errorf("path = %q, want /rc/rcs_5/code", r.URL.Path)
		}
		fmt.Fprint(w, `{"code":"RC 8FK3-9MQ2","code_short":"8FK3-9MQ2"}`)
	}))
	defer srv.Close()
	out := captureStdout(t, func() {
		if err := remoteLinkCode(config{Broker: srv.URL}, "rcs_5"); err != nil {
			t.Errorf("remoteLinkCode: %v", err)
		}
	})
	for _, want := range []string{"RC 8FK3-9MQ2", "https://rogerai.fyi/r.html#8FK3-9MQ2", "roger remote attach 8FK3-9MQ2"} {
		if !strings.Contains(out, want) {
			t.Errorf("link output missing %q:\n%s", want, out)
		}
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		fmt.Fprint(w, `{"error":"session ended"}`)
	}))
	defer errSrv.Close()
	if err := remoteLinkCode(config{Broker: errSrv.URL}, "rcs_5"); err == nil || !strings.Contains(err.Error(), "session ended") {
		t.Fatalf("a broker error must surface, got %v", err)
	}
}

// TestRemoteLinkHelp: the no-arg link prints the how-to (enable inside [0] AGENT, the r.html
// deep link, mint + attach commands).
func TestRemoteLinkHelp(t *testing.T) {
	out := captureStdout(t, func() {
		if err := remoteLinkHelp(); err != nil {
			t.Errorf("remoteLinkHelp: %v", err)
		}
	})
	for _, want := range []string{"/remote-control", "rogerai.fyi/r.html#<code>", "roger remote link <session-id>", "roger remote attach <code>"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
}

// TestCmdRemoteDispatch: every subcommand routes (list default, ls, off/stop/revoke, link,
// attach usage) and an unknown sub errors with the try-list.
func TestCmdRemoteDispatch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	empty := rcRosterServer(t, `{"sessions":[]}`)
	cfg := config{Broker: empty.URL}

	cases := []struct {
		name    string
		args    []string
		wantErr string // "" = nil error
	}{
		{"default is list", nil, ""},
		{"ls alias", []string{"ls"}, ""},
		{"attach needs a code", []string{"attach"}, "usage: roger remote attach"},
		{"off all", []string{"off"}, ""},
		{"stop one", []string{"stop", "rcs_1"}, ""},
		{"revoke alias", []string{"revoke", "rcs_1"}, ""},
		{"link help", []string{"link"}, ""},
		{"link mints", []string{"link", "rcs_2"}, ""},
		{"unknown sub", []string{"dance"}, `unknown: roger remote "dance"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			_ = captureStdout(t, func() { err = cmdRemote(cfg, tc.args) })
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("cmdRemote(%v) = %v, want nil", tc.args, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("cmdRemote(%v) = %v, want substring %q", tc.args, err, tc.wantErr)
			}
		})
	}
}

// TestCmdRemoteAttachJoinsCode: `remote attach 8FK3 9MQ2` joins the args into ONE code before
// the exchange (codes can be pasted with spaces).
func TestCmdRemoteAttachJoinsCode(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var gotCode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]string
		_ = json.Unmarshal(raw, &b)
		gotCode = b["code"]
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"no session on this code"}`)
	}))
	defer srv.Close()
	err := cmdRemote(config{Broker: srv.URL}, []string{"attach", "8FK3", "9MQ2"})
	if err == nil || !strings.Contains(err.Error(), "no session on this code") {
		t.Fatalf("attach error must surface, got %v", err)
	}
	if gotCode != "8FK3 9MQ2" {
		t.Fatalf("code = %q, want the args joined with a space", gotCode)
	}
}

// swapStdin replaces os.Stdin with a fresh pipe for the test, restoring it on cleanup.
func swapStdin(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = w.Close()
		_ = r.Close()
	})
	return w
}

// sseFrame writes one RCFrame as an SSE data line.
func sseFrame(w http.ResponseWriter, f protocol.RCFrame) {
	b, _ := json.Marshal(f)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

// TestRemoteAttachNarratesEveryFrameKind: attach exchanges the code, then narrates the live
// stream - user turns with origin, assistant/final text (blank skipped), tool call/result,
// the confirm prompt, both confirm outcomes, the backfill divider, errors, and the ended
// notice - then prints "detached." when the stream closes.
func TestRemoteAttachNarratesEveryFrameKind(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_ = swapStdin(t) // keep the input loop parked on an open, silent pipe

	approve := true
	deny := false
	mux := http.NewServeMux()
	mux.HandleFunc("/rc/attach", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"session_id":"rcs_a","name":"otter · repo","attach_token":"at_a"}`)
	})
	mux.HandleFunc("/rc/rcs_a/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Roger-Attach") != "at_a" {
			t.Errorf("stream must carry the attach token, got %q", r.Header.Get("X-Roger-Attach"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindUser, Origin: "phone", Text: "hi"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "hello"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "   "}) // blank: skipped
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindFinal, Text: "done"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindToolCall, Tool: "Bash"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindToolResult, Tool: "Bash"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindConfirmReq, Tool: "Edit", ConfirmID: "cf1"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindConfirmDone, Approve: &approve, Origin: "web"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindConfirmDone, Approve: &deny, Origin: "cli"})
		// A guest-operator handoff (enriched) then the DJ-back return: the CLI viewer must
		// narrate both (it used to DROP every status frame, so a handoff looked dead).
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "opencode", Model: "gpt-oss-120b", Spend: 0.19, Text: "guest has the mic: opencode - the DJ answers when the handoff ends"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindStatus, Text: "the DJ is back at the desk"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindStatus}) // neither operator nor text: skipped
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindBackfill, Text: "earlier transcript"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindBackfill, Text: ""}) // blank: skipped
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindError, Text: "boom"})
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindEnded})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := captureStdout(t, func() {
		if err := remoteAttach(config{Broker: srv.URL}, "8FK3-9MQ2"); err != nil {
			t.Errorf("remoteAttach: %v", err)
		}
	})
	for _, want := range []string{
		`attached to "otter · repo"`,
		"▸ (phone) hi",
		"◂ hello",
		"◂ done",
		"◉ Bash",
		"✓ Bash",
		"? Edit — type 'y' to approve",
		"✓ approved from web",
		"✓ denied from cli",
		"◉ opencode has the mic on gpt-oss-120b · $0.19",
		"the DJ is back at the desk",
		"earlier transcript",
		"(live from here)",
		"✕ boom",
		"— the session ended on the host —",
		"detached.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("narration missing %q:\n%s", want, out)
		}
	}
}

// TestRemoteAttachErrors: a bad code surfaces the attach error; a stream that 401s after
// attach surfaces too (the ctx was NOT locally cancelled).
func TestRemoteAttachErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_ = swapStdin(t)

	badCode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"no session on this code"}`)
	}))
	defer badCode.Close()
	if err := remoteAttach(config{Broker: badCode.URL}, "nope"); err == nil || !strings.Contains(err.Error(), "no session on this code") {
		t.Fatalf("attach error must surface, got %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/rc/attach", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"session_id":"rcs_e","name":"n","attach_token":"at_e"}`)
	})
	mux.HandleFunc("/rc/rcs_e/stream", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"attach token expired"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var err error
	_ = captureStdout(t, func() { err = remoteAttach(config{Broker: srv.URL}, "code") })
	if err == nil || !strings.Contains(err.Error(), "attach token expired") {
		t.Fatalf("stream error must surface, got %v", err)
	}
}

// TestRemoteInputLoopConfirmGate: typed lines become turns; a bare y/n is a CONFIRM answer
// ONLY while the gate is pending (carrying the confirm id), otherwise an ordinary turn; blank
// lines are skipped; EOF ends the loop; a pre-cancelled ctx ends it before reading.
func TestRemoteInputLoopConfirmGate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var mu sync.Mutex
	var sent []protocol.RCInbound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rc/rcs_i/send" || r.Header.Get("X-Roger-Attach") != "at_i" {
			t.Errorf("want POST /rc/rcs_i/send with the attach token, got %s %s", r.URL.Path, r.Header.Get("X-Roger-Attach"))
		}
		raw, _ := io.ReadAll(r.Body)
		var in protocol.RCInbound
		_ = json.Unmarshal(raw, &in)
		mu.Lock()
		sent = append(sent, in)
		mu.Unlock()
	}))
	defer srv.Close()

	w := swapStdin(t)
	stdin := os.Stdin // captured on the test goroutine, ordered with the swap + restore
	gate := &confirmGate{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		remoteInputLoop(ctx, srv.URL, "rcs_i", "at_i", stdin, gate)
		close(loopDone)
	}()

	waitSent := func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			l := len(sent)
			mu.Unlock()
			if l >= n {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d sends", n)
	}

	fmt.Fprint(w, "   \n")         // blank: skipped, nothing sent
	fmt.Fprint(w, "hello there\n") // ordinary turn
	waitSent(1)
	gate.set("cf9")
	fmt.Fprint(w, "y\n") // pending: a confirm APPROVE carrying cf9
	waitSent(2)
	fmt.Fprint(w, "no\n") // gate consumed: an ordinary turn again
	waitSent(3)
	gate.set("cf10")
	fmt.Fprint(w, "N\n") // pending: a confirm DENY carrying cf10
	waitSent(4)
	_ = w.Close() // EOF: the loop must return
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("EOF on stdin must end the input loop")
	}

	mu.Lock()
	defer mu.Unlock()
	want := []protocol.RCInbound{
		{Kind: protocol.RCInTurn, Text: "hello there"},
		{Kind: protocol.RCInConfirm, Approve: true, ConfirmID: "cf9"},
		{Kind: protocol.RCInTurn, Text: "no"},
		{Kind: protocol.RCInConfirm, Approve: false, ConfirmID: "cf10"},
	}
	if len(sent) != len(want) {
		t.Fatalf("sent %d messages, want %d: %+v", len(sent), len(want), sent)
	}
	for i := range want {
		if sent[i].Kind != want[i].Kind || sent[i].Text != want[i].Text ||
			sent[i].Approve != want[i].Approve || sent[i].ConfirmID != want[i].ConfirmID {
			t.Errorf("sent[%d] = %+v, want %+v", i, sent[i], want[i])
		}
	}
}

// TestTuiHooksRCClosures: the BASE STATION hooks wired by tuiHooks adapt the client RC
// calls onto the TUI shapes - enable returns a live bridge + the r.html deep link, the
// roster/bands map row for row, attach/join/send/stream/revoke hit the right endpoints -
// and the list closures surface a broker error as (nil, err).
func TestTuiHooksRCClosures(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/rc/enable", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"session_id":"rcs_h","name":"otter · repo","code":"RC 8FK3-9MQ2","code_short":"8FK3-9MQ2","host_token":"ht_h"}`)
	})
	mux.HandleFunc("/rc/sessions", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sessions":[{"id":"rcs_h","name":"otter · repo","code_display":"RC ···","online":true,"revoked":false}]}`)
	})
	mux.HandleFunc("/bands", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"bands":[{"id":"b1","display":"147.520 MHz","label":"home","status":"active"}]}`)
	})
	mux.HandleFunc("/rc/attach", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"session_id":"rcs_h","name":"otter · repo","attach_token":"at_h"}`)
	})
	mux.HandleFunc("/rc/rcs_h/join", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"session_id":"rcs_h","attach_token":"at_join"}`)
	})
	mux.HandleFunc("/rc/rcs_h/send", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	mux.HandleFunc("/rc/rcs_h/disable", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	mux.HandleFunc("/rc/rcs_h/stream", func(w http.ResponseWriter, r *http.Request) {
		sseFrame(w, protocol.RCFrame{Kind: protocol.RCKindEnded})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := tuiHooks(config{Broker: srv.URL, User: "u"})

	br, info, err := h.RCEnable(srv.URL, "otter · repo")
	if err != nil || br == nil {
		t.Fatalf("RCEnable hook: bridge=%v err=%v", br, err)
	}
	defer br.Stop()
	if info.SessionID != "rcs_h" || info.CodeShort != "8FK3-9MQ2" || info.LinkURL != "https://rogerai.fyi/r.html#8FK3-9MQ2" {
		t.Fatalf("RCEnable info = %+v, want the parsed result + the r.html deep link", info)
	}

	rows, err := h.RCList(srv.URL)
	if err != nil || len(rows) != 1 || rows[0].ID != "rcs_h" || rows[0].Name != "otter · repo" || !rows[0].Online {
		t.Fatalf("RCList hook = %+v/%v, want the mapped roster row", rows, err)
	}
	bands, err := h.BandList(srv.URL)
	if err != nil || len(bands) != 1 || bands[0].ID != "b1" || bands[0].Label != "home" || bands[0].Status != "active" {
		t.Fatalf("BandList hook = %+v/%v, want the mapped band row", bands, err)
	}
	if err := h.RCRevoke(srv.URL, "rcs_h"); err != nil {
		t.Fatalf("RCRevoke hook: %v", err)
	}
	attach, sid, name, err := h.RCAttach(srv.URL, "8FK3-9MQ2")
	if err != nil || attach != "at_h" || sid != "rcs_h" || name != "otter · repo" {
		t.Fatalf("RCAttach hook = %q %q %q %v", attach, sid, name, err)
	}
	token, err := h.RCJoin(srv.URL, "rcs_h")
	if err != nil || token != "at_join" {
		t.Fatalf("RCJoin hook = %q/%v, want at_join", token, err)
	}
	if err := h.RCSend(srv.URL, "rcs_h", token, protocol.RCInbound{Kind: protocol.RCInTurn, Text: "hi"}); err != nil {
		t.Fatalf("RCSend hook: %v", err)
	}
	var kinds []string
	err = h.RCStream(context.Background(), srv.URL, "rcs_h", token, 0, func(f protocol.RCFrame) { kinds = append(kinds, f.Kind) })
	if err != nil || len(kinds) != 1 || kinds[0] != protocol.RCKindEnded {
		t.Fatalf("RCStream hook = %v/%v, want the ended frame", kinds, err)
	}

	// A broker error comes back as (nil, err) from the list closures and err from enable.
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"nope"}`)
	}))
	defer errSrv.Close()
	if rows, err := h.RCList(errSrv.URL); err == nil || rows != nil {
		t.Fatalf("RCList error path = %+v/%v, want nil rows + err", rows, err)
	}
	if bands, err := h.BandList(errSrv.URL); err == nil || bands != nil {
		t.Fatalf("BandList error path = %+v/%v, want nil bands + err", bands, err)
	}
	if br, _, err := h.RCEnable(errSrv.URL, "n"); err == nil || br != nil {
		t.Fatalf("RCEnable error path = %v/%v, want nil bridge + err", br, err)
	}

	// The split login closures: begin surfaces the no-client-id error without any network;
	// poll rejects a foreign handle (both stay offline - the real device flow is GitHub's).
	if _, err := h.LoginBegin(srv.URL, ""); err == nil {
		t.Fatal("LoginBegin hook must surface the missing-client-id error")
	}
	if _, err := h.LoginPoll(srv.URL, "cid", tui.LoginDevice{Handle: "not-a-device"}); err == nil {
		t.Fatal("LoginPoll hook must reject an invalid handle")
	}
}

// TestRemoteInputLoopCtxDone: a cancelled ctx ends the loop at the top select without
// touching stdin.
func TestRemoteInputLoopCtxDone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_ = swapStdin(t) // open pipe: a read would block forever, proving the ctx exit
	stdin := os.Stdin
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		remoteInputLoop(ctx, "http://127.0.0.1:0", "s", "a", stdin, &confirmGate{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a cancelled ctx must end the input loop")
	}
}
