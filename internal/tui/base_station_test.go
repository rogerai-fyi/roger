package tui

// base_station_test.go covers the BASE STATION side of rc.go (v5.0.0) the existing
// rc_test.go host tests leave open: the [p] roster screen (footnote states, fetch/refresh,
// key handling, rendering), the in-TUI VIEWER of a session hosted elsewhere (owner join,
// SSE frame rendering of every kind, generation staleness, the y/n confirm gate, esc
// teardown), the host-side /remote-control off + tee branches, and the small tui.go
// helpers the coverage pass flagged (countOnline, mergeStickyBand, transmitLine). All
// hooks are the package's established function-field seams; the fake bridge is the same
// one rc_test.go uses.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/harness"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// bsSeed builds a logged-in browse model for BASE STATION tests.
func bsSeed() model {
	m := browseSeed(120)
	m.ghLogin = "lramos85"
	m.loggedIn = true
	return m
}

// bsKey builds a key message by name, covering the special keys the roster/viewer bind.
func bsKey(name string) tea.KeyMsg {
	switch name {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	}
	return kmRunes(name)
}

// TestPrivateFootnoteStates: the THE BAND footnote earns the red ◉ only for a LIVE remote
// session (or this machine hosting), shows a dim bands-only line otherwise, and vanishes
// when there is nothing to show.
func TestPrivateFootnoteStates(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*model)
		want     []string
		wantNone bool
	}{
		{"nothing to show", func(m *model) {}, nil, true},
		{"one live session", func(m *model) {
			m.rcSessions = []RemoteSessionRow{{ID: "r1", Name: "a", Online: true}}
		}, []string{"live: 1 remote session", "[p]"}, false},
		{"two live sessions pluralize", func(m *model) {
			m.rcSessions = []RemoteSessionRow{{ID: "r1", Online: true}, {ID: "r2", Online: true}}
		}, []string{"live: 2 remote sessions"}, false},
		{"a revoked session is not live", func(m *model) {
			m.rcSessions = []RemoteSessionRow{{ID: "r1", Online: true, Revoked: true}}
			m.rcBands = []BandRow{{ID: "b1"}}
		}, []string{"base station: 1 private band", "[p]"}, false},
		{"bands only stays dim", func(m *model) {
			m.rcBands = []BandRow{{ID: "b1"}, {ID: "b2"}}
		}, []string{"base station: 2 private bands", "[p]"}, false},
		{"hosting here counts as live before the roster refreshes", func(m *model) {
			m.rcBridge = newFakeBridge()
		}, []string{"live: 1 remote session"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := bsSeed()
			tc.mutate(&m)
			got := stripANSI(m.privateFootnote())
			if tc.wantNone {
				if got != "" {
					t.Fatalf("footnote = %q, want empty", got)
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("footnote missing %q: %q", w, got)
				}
			}
		})
	}
}

// TestEnterPrivateFetchesRoster: [p] enters modePrivate and the fetch Cmd loads sessions +
// bands through the hooks; the roster message lands them on the model and clears rcErr.
func TestEnterPrivateFetchesRoster(t *testing.T) {
	m := bsSeed()
	m.hooks.RCList = func(broker string) ([]RemoteSessionRow, error) {
		return []RemoteSessionRow{{ID: "rcs_1", Name: "otter · repo", Online: true}}, nil
	}
	m.hooks.BandList = func(broker string) ([]BandRow, error) {
		return []BandRow{{ID: "b1", Label: "home", Status: "active"}}, nil
	}
	m.rcErr = "stale error"
	nm, cmd := m.enterPrivate()
	gm := asModel(nm)
	if gm.mode != modePrivate || cmd == nil {
		t.Fatalf("enterPrivate: mode=%d cmd=%v, want modePrivate + a fetch cmd", gm.mode, cmd)
	}
	if !strings.Contains(stripANSI(gm.status), "BASE STATION") {
		t.Errorf("status = %q, want the BASE STATION label", stripANSI(gm.status))
	}
	msg, ok := cmd().(remoteRosterMsg)
	if !ok || msg.err != nil || len(msg.sessions) != 1 || len(msg.bands) != 1 {
		t.Fatalf("fetch = %+v (ok=%v), want 1 session + 1 band", msg, ok)
	}
	var tm tea.Model = gm
	tm, _ = tm.Update(msg)
	gm = asModel(tm)
	if len(gm.rcSessions) != 1 || len(gm.rcBands) != 1 || gm.rcErr != "" {
		t.Fatalf("roster not landed: sessions=%d bands=%d err=%q", len(gm.rcSessions), len(gm.rcBands), gm.rcErr)
	}
}

// TestFetchRemoteRosterBranches: a roster error rides out on msg.err; a bands error keeps
// the sessions (bands stay nil); nil hooks yield an empty message rather than a panic.
func TestFetchRemoteRosterBranches(t *testing.T) {
	m := bsSeed()
	m.hooks.RCList = func(string) ([]RemoteSessionRow, error) { return nil, errors.New("roster down") }
	m.hooks.BandList = func(string) ([]BandRow, error) { return nil, errors.New("bands down") }
	msg := m.fetchRemoteRoster()().(remoteRosterMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "roster down") {
		t.Fatalf("err = %v, want the roster error", msg.err)
	}
	if msg.bands != nil {
		t.Fatalf("a bands error must not land bands, got %+v", msg.bands)
	}

	m.hooks.RCList, m.hooks.BandList = nil, nil
	msg = m.fetchRemoteRoster()().(remoteRosterMsg)
	if msg.err != nil || msg.sessions != nil || msg.bands != nil {
		t.Fatalf("nil hooks should yield an empty roster msg, got %+v", msg)
	}
}

// TestOnRemoteRosterClamps: an error message sets rcErr; the cursor clamps into the new
// roster (past-the-end pulls back to the last row; an empty roster parks at 0).
func TestOnRemoteRosterClamps(t *testing.T) {
	m := bsSeed()
	m.rcCursor = 5
	nm, _ := m.onRemoteRoster(remoteRosterMsg{sessions: []RemoteSessionRow{{ID: "a"}, {ID: "b"}}})
	gm := asModel(nm)
	if gm.rcCursor != 1 {
		t.Fatalf("cursor = %d, want clamped to the last row (1)", gm.rcCursor)
	}
	nm, _ = gm.onRemoteRoster(remoteRosterMsg{err: errors.New("boom")})
	gm = asModel(nm)
	if gm.rcErr != "boom" {
		t.Fatalf("rcErr = %q, want boom", gm.rcErr)
	}
	if gm.rcCursor != 0 {
		t.Fatalf("cursor = %d, want 0 on an empty roster", gm.rcCursor)
	}
}

// TestPrivateViewRendering: the BASE STATION screen renders the honesty header, every
// session state (live / offline / ended) with the cursor, the private bands with the
// active mark, the freq-entry hint, and a roster error line.
func TestPrivateViewRendering(t *testing.T) {
	m := bsSeed()
	m.rcSessions = []RemoteSessionRow{
		{ID: "r1", Name: "otter · repo", Online: true},
		{ID: "r2", Name: "otter · lab"},
		{ID: "r3", Name: "otter · old", Online: true, Revoked: true},
	}
	m.rcBands = []BandRow{
		{ID: "b1", Label: "home", Display: "147.520 MHz", Status: "active"},
		{ID: "b2", Label: "spare", Display: "146.100 MHz", Status: "revoked"},
	}
	m.rcCursor = 0
	m.rcErr = "roster hiccup"
	out := stripANSI(m.privateView(100))
	for _, want := range []string{
		"BASE STATION", "not end-to-end encrypted",
		"REMOTE SESSIONS", "live", "offline", "ended", "otter · repo",
		"PRIVATE BANDS", "home", "147.520 MHz", "spare",
		"[~]", "roster hiccup",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("privateView missing %q:\n%s", want, out)
		}
	}

	// Empty state: both sections point at how to create their first entry.
	e := bsSeed()
	out = stripANSI(e.privateView(100))
	if !strings.Contains(out, "/remote-control") || !strings.Contains(out, "roger share --private") {
		t.Errorf("empty state should teach both paths:\n%s", out)
	}
}

// TestPrivateViewThroughUpdate: the top-level View routes modePrivate to privateView and
// modeRemoteSession to remoteSessionView.
func TestPrivateViewThroughUpdate(t *testing.T) {
	m := bsSeed()
	m.mode = modePrivate
	if !strings.Contains(stripANSI(m.View()), "BASE STATION") {
		t.Fatal("View() in modePrivate should render BASE STATION")
	}
	m.mode = modeRemoteSession
	m.rsVP = viewport.New(80, 10)
	m.rsIn = textinput.New()
	if !strings.Contains(stripANSI(m.View()), "REMOTE SESSION") {
		t.Fatal("View() in modeRemoteSession should render the viewer")
	}
}

// TestOnPrivateKeyNavigation: esc/h/q/1 return to THE BAND; j/k move with clamps; r
// refreshes; ~ opens freq entry; a preset key falls through (m toggles compact); an
// unbound key is a no-op.
func TestOnPrivateKeyNavigation(t *testing.T) {
	m := bsSeed()
	m.mode = modePrivate
	m.rcSessions = []RemoteSessionRow{{ID: "a"}, {ID: "b"}}

	for _, k := range []string{"esc", "left", "h", "q", "1"} {
		nm, _ := m.onPrivateKey(bsKey(k))
		if asModel(nm).mode != modeBrowse {
			t.Errorf("%q should return to THE BAND, got mode %d", k, asModel(nm).mode)
		}
	}

	m.rcCursor = 0
	nm, _ := m.onPrivateKey(kmRunes("k"))
	if asModel(nm).rcCursor != 0 {
		t.Error("k at the top must not go negative")
	}
	nm, _ = m.onPrivateKey(kmRunes("j"))
	gm := asModel(nm)
	if gm.rcCursor != 1 {
		t.Fatalf("j should move down, got %d", gm.rcCursor)
	}
	nm, _ = gm.onPrivateKey(kmRunes("j"))
	if asModel(nm).rcCursor != 1 {
		t.Error("j at the bottom must clamp")
	}

	m.hooks.RCList = func(string) ([]RemoteSessionRow, error) { return nil, nil }
	if _, cmd := m.onPrivateKey(kmRunes("r")); cmd == nil {
		t.Error("r should refresh the roster (non-nil cmd)")
	}

	nm, _ = m.onPrivateKey(kmRunes("~"))
	gm = asModel(nm)
	if gm.mode != modeFreqEntry || !gm.freqIn.Focused() {
		t.Fatalf("~ should open the freq entry focused, got mode %d", gm.mode)
	}

	nm, _ = m.onPrivateKey(kmRunes("m"))
	if !asModel(nm).compact {
		t.Error("an unmatched key should fall through to the preset bank (m -> compact)")
	}
	nm, cmd := m.onPrivateKey(kmRunes("z"))
	if asModel(nm).mode != modePrivate || cmd != nil {
		t.Error("an unbound key should be a no-op")
	}
}

// TestOnPrivateKeyEnterAndRevoke: enter opens the selected session (out-of-range is a
// no-op); x revokes it through the hook and re-lists; x with no revoke hook is a no-op.
func TestOnPrivateKeyEnterAndRevoke(t *testing.T) {
	m := bsSeed()
	m.mode = modePrivate
	m.rcSessions = []RemoteSessionRow{{ID: "rcs_1", Name: "otter"}}

	// enter with no join hooks: the actionable hint, still on the roster.
	nm, _ := m.onPrivateKey(bsKey("enter"))
	gm := asModel(nm)
	if gm.mode != modePrivate || !strings.Contains(stripANSI(gm.status), "roger remote attach") {
		t.Fatalf("enter without hooks should hint the CLI path, got mode=%d status=%q", gm.mode, stripANSI(gm.status))
	}
	// enter out of range: no-op.
	m.rcCursor = 9
	if _, cmd := m.onPrivateKey(bsKey("enter")); cmd != nil {
		t.Error("enter past the roster should be a no-op")
	}
	m.rcCursor = 0

	// x with no hook: nil cmd.
	if _, cmd := m.onPrivateKey(kmRunes("x")); cmd != nil {
		t.Error("x without a revoke hook should be a no-op")
	}
	// x with hooks: revoke rides then the roster re-lists.
	var revoked string
	m.hooks.RCRevoke = func(broker, id string) error { revoked = id; return nil }
	m.hooks.RCList = func(string) ([]RemoteSessionRow, error) { return []RemoteSessionRow{}, nil }
	m.hooks.BandList = func(string) ([]BandRow, error) { return []BandRow{{ID: "b1"}}, nil }
	_, cmd := m.onPrivateKey(kmRunes("x"))
	if cmd == nil {
		t.Fatal("x with a revoke hook should return a cmd")
	}
	msg, ok := cmd().(remoteRosterMsg)
	if !ok || revoked != "rcs_1" || len(msg.bands) != 1 {
		t.Fatalf("revoke should end rcs_1 then re-list (revoked=%q msg=%+v)", revoked, msg)
	}
}

// TestRevokeRemoteSessionListError: the post-revoke re-list surfaces a roster error.
func TestRevokeRemoteSessionListError(t *testing.T) {
	m := bsSeed()
	m.hooks.RCRevoke = func(string, string) error { return nil }
	m.hooks.RCList = func(string) ([]RemoteSessionRow, error) { return nil, errors.New("gone") }
	msg := m.revokeRemoteSession("rcs_9")().(remoteRosterMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "gone") {
		t.Fatalf("re-list error should ride the roster msg, got %+v", msg)
	}
}

// TestEnterRemoteSessionGuards: the session hosted on THIS machine redirects to [0] AGENT;
// missing hooks hint the CLI; the full path bumps the generation, resets the viewer state,
// and the join Cmd yields the attach token.
func TestEnterRemoteSessionGuards(t *testing.T) {
	m := bsSeed()
	fb := newFakeBridge() // sid rcs_test
	m.rcBridge = fb
	nm, cmd := m.enterRemoteSession(RemoteSessionRow{ID: "rcs_test", Name: "me"})
	gm := asModel(nm)
	if cmd != nil || gm.mode == modeRemoteSession || !strings.Contains(stripANSI(gm.status), "hosted HERE") {
		t.Fatalf("my own hosted session should redirect to [0] AGENT, got status %q", stripANSI(gm.status))
	}

	m.rcBridge = nil
	nm, cmd = m.enterRemoteSession(RemoteSessionRow{ID: "rcs_x"})
	gm = asModel(nm)
	if cmd != nil || !strings.Contains(stripANSI(gm.status), "roger remote attach") {
		t.Fatalf("missing hooks should hint the CLI, got %q", stripANSI(gm.status))
	}

	m.hooks.RCJoin = func(broker, sid string) (string, error) {
		if sid != "rcs_y" {
			t.Errorf("join sid = %q, want rcs_y", sid)
		}
		return "at_ok", nil
	}
	m.hooks.RCStream = func(context.Context, string, string, string, uint64, func(protocol.RCFrame)) error { return nil }
	m.rsLines = []string{"stale line"}
	m.rsSeq = 42
	gen := m.rsGen
	nm, cmd = m.enterRemoteSession(RemoteSessionRow{ID: "rcs_y", Name: "otter · lab"})
	gm = asModel(nm)
	if gm.mode != modeRemoteSession || gm.rsGen != gen+1 || gm.rsLines != nil || gm.rsSeq != 0 || gm.rsPendingConfirm {
		t.Fatalf("viewer state not reset: %+v", gm)
	}
	if !strings.Contains(stripANSI(gm.status), "attaching") {
		t.Errorf("status = %q, want attaching…", stripANSI(gm.status))
	}
	msg, ok := cmd().(remoteAttachedMsg)
	if !ok || msg.token != "at_ok" || msg.gen != gm.rsGen || msg.row.ID != "rcs_y" {
		t.Fatalf("join msg = %+v, want the attach token on this generation", msg)
	}
}

// TestOnRemoteAttachedStaleAndError: a stale generation (or a mode change) drops the
// attach silently; a join error lands on the status line.
func TestOnRemoteAttachedStaleAndError(t *testing.T) {
	m := bsSeed()
	m.mode = modeRemoteSession
	m.rsGen = 2
	nm, cmd := m.onRemoteAttached(remoteAttachedMsg{gen: 1, token: "at_old"})
	if cmd != nil || asModel(nm).rsAttach != "" {
		t.Fatal("a stale attach must be ignored")
	}
	m.mode = modePrivate // navigated away before the join returned
	if _, cmd := m.onRemoteAttached(remoteAttachedMsg{gen: 2, token: "at"}); cmd != nil {
		t.Fatal("an attach after leaving the viewer must be ignored")
	}
	m.mode = modeRemoteSession
	nm, _ = m.onRemoteAttached(remoteAttachedMsg{gen: 2, err: errors.New("boom")})
	if !strings.Contains(stripANSI(asModel(nm).status), "could not attach: boom") {
		t.Fatalf("status = %q, want the attach error", stripANSI(asModel(nm).status))
	}
}

// TestRemoteViewerStreamsFrames: the attach starts the SSE stream through the hook; frames
// arrive as remoteFrameMsg on the live generation, render into the transcript, and keep
// re-arming; when the stream closes the viewer reports it.
func TestRemoteViewerStreamsFrames(t *testing.T) {
	m := bsSeed()
	m.mode = modeRemoteSession
	m.rsGen = 3
	m.rsRow = RemoteSessionRow{ID: "rcs_s", Name: "otter · lab"}
	m.rsVP = viewport.New(80, 10)
	m.hooks.RCStream = func(ctx context.Context, broker, sid, attach string, since uint64, onFrame func(protocol.RCFrame)) error {
		if sid != "rcs_s" || attach != "at_s" {
			t.Errorf("stream args = %q/%q, want rcs_s/at_s", sid, attach)
		}
		onFrame(protocol.RCFrame{Seq: 1, Kind: protocol.RCKindAssistant, Text: "hello from the host"})
		return nil
	}
	nm, cmd := m.onRemoteAttached(remoteAttachedMsg{gen: 3, row: m.rsRow, token: "at_s"})
	gm := asModel(nm)
	if gm.rsAttach != "at_s" || cmd == nil {
		t.Fatalf("attach should store the token + arm the stream, got %q", gm.rsAttach)
	}
	msg := cmd() // the buffered frame
	fm, ok := msg.(remoteFrameMsg)
	if !ok || fm.f.Text != "hello from the host" || fm.gen != 3 {
		t.Fatalf("first stream msg = %+v, want the assistant frame on gen 3", msg)
	}
	var tm tea.Model = gm
	tm, cmd = tm.Update(fm)
	gm = asModel(tm)
	if len(gm.rsLines) != 1 || !strings.Contains(stripANSI(gm.rsLines[0]), "hello from the host") {
		t.Fatalf("frame should render into the transcript, got %v", gm.rsLines)
	}
	if cmd == nil {
		t.Fatal("the viewer must re-arm while open on the live generation")
	}
	// The stream func returned; its channel is closed - the next wait reports the end.
	end := gm.reArmRemoteStream()()
	em, ok := end.(remoteViewerEndMsg)
	if !ok || em.gen != 3 {
		t.Fatalf("end msg = %+v, want remoteViewerEndMsg gen 3", end)
	}
	tm, _ = gm.Update(end)
	if !strings.Contains(stripANSI(asModel(tm).status), "stream ended") {
		t.Fatalf("status = %q, want stream ended", stripANSI(asModel(tm).status))
	}
}

// TestRemoteViewerStaleGeneration: frames and end notices from an OLD generation are
// dropped - they can't touch the new session's transcript or status.
func TestRemoteViewerStaleGeneration(t *testing.T) {
	m := bsSeed()
	m.mode = modeRemoteSession
	m.rsGen = 5
	m.rsVP = viewport.New(80, 10)
	m.status = "fresh"
	var tm tea.Model = m
	tm, cmd := tm.Update(remoteFrameMsg{gen: 4, f: protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "old"}})
	gm := asModel(tm)
	if len(gm.rsLines) != 0 || cmd != nil {
		t.Fatal("a stale frame must not render or re-arm")
	}
	tm, _ = gm.Update(remoteViewerEndMsg{gen: 4})
	if asModel(tm).status != "fresh" {
		t.Fatal("a stale end notice must not clobber the status")
	}
}

// TestOnRemoteFrameKinds: every frame kind renders its own transcript shape - the user
// origin (defaulting to "someone"), blank assistant/final skipped, tool call/result, the
// confirm gate arming/clearing with its id, the backfill PREPEND, errors, and the terminal
// ended notice - while Seq tracks the high-water mark.
func TestOnRemoteFrameKinds(t *testing.T) {
	base := bsSeed()
	base.mode = modeRemoteSession
	base.rsGen = 1
	base.rsVP = viewport.New(80, 10)

	cases := []struct {
		name  string
		f     protocol.RCFrame
		want  string // substring of the LAST line ("" = no new line)
		check func(t *testing.T, gm model)
	}{
		{"user with origin", protocol.RCFrame{Seq: 2, Kind: protocol.RCKindUser, Origin: "phone", Text: "hi"}, "(phone) hi", nil},
		{"user without origin", protocol.RCFrame{Kind: protocol.RCKindUser, Text: "hi"}, "(someone) hi", nil},
		{"assistant text", protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "hello"}, "hello", nil},
		{"blank assistant skipped", protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "  "}, "", nil},
		{"final text", protocol.RCFrame{Kind: protocol.RCKindFinal, Text: "done"}, "done", nil},
		{"tool call", protocol.RCFrame{Kind: protocol.RCKindToolCall, Tool: "Bash"}, "Bash", nil},
		{"tool result", protocol.RCFrame{Kind: protocol.RCKindToolResult, Tool: "Bash"}, "✓ Bash", nil},
		{"confirm req arms the gate", protocol.RCFrame{Kind: protocol.RCKindConfirmReq, Tool: "Edit", ConfirmID: "cf1"}, "[y] approve", func(t *testing.T, gm model) {
			if !gm.rsPendingConfirm || gm.rsConfirmID != "cf1" {
				t.Errorf("confirm gate not armed: pending=%v id=%q", gm.rsPendingConfirm, gm.rsConfirmID)
			}
		}},
		{"error", protocol.RCFrame{Kind: protocol.RCKindError, Text: "boom"}, "boom", nil},
		// Guest Operators iteration-2 finding: the desktop viewer used to DROP RCKindStatus
		// frames, so a handoff looked dead. A status frame now renders as a dim line -
		// operator-aware for a guest handoff, the plain text for the DJ-back transition,
		// and nothing when the frame carries neither (content-blind: only the guest name).
		{"status names the operator", protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "opencode", Text: "guest has the mic: opencode - the DJ answers when the handoff ends"}, "guest has the mic: opencode", func(t *testing.T, gm model) {
			// content-blind + tidy: the operator-aware line is the short handoff line, not
			// the frame's full sentence (a bare frame degrades to the pre-enrichment line).
			if last := stripANSI(gm.rsLines[len(gm.rsLines)-1]); strings.Contains(last, "answers when the handoff ends") {
				t.Errorf("the viewer line should be the short operator-aware line, got %q", last)
			}
		}},
		// Operator frame enrichment (rc_enrichment.feature, founder ruling 3): with
		// model/spend metadata the viewer renders "<op> has the mic on <model> · $<spend>",
		// degrading piecewise (no model drops "on", zero spend drops "· $").
		{"status enriched with model and spend", protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "opencode", Model: "gpt-oss-120b", Spend: 0.19, Text: "guest has the mic: opencode - the DJ answers when the handoff ends"}, "opencode has the mic on gpt-oss-120b · $0.19", nil},
		{"status enriched model only (zero spend drops the money)", protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "opencode", Model: "gpt-oss-120b", Text: "guest has the mic: opencode - the DJ answers when the handoff ends"}, "opencode has the mic on gpt-oss-120b", func(t *testing.T, gm model) {
			if last := stripANSI(gm.rsLines[len(gm.rsLines)-1]); strings.Contains(last, "$") {
				t.Errorf("a zero-spend frame must not render a money figure, got %q", last)
			}
		}},
		{"status enriched spend only (no model drops the on-clause)", protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "aider", Spend: 1.05, Text: "guest has the mic: aider - the DJ answers when the handoff ends"}, "aider has the mic · $1.05", func(t *testing.T, gm model) {
			if last := stripANSI(gm.rsLines[len(gm.rsLines)-1]); strings.Contains(last, " on ") {
				t.Errorf("a model-less frame must not render an on-clause, got %q", last)
			}
		}},
		{"status DJ back has no operator", protocol.RCFrame{Kind: protocol.RCKindStatus, Text: "the DJ is back at the desk"}, "the DJ is back at the desk", nil},
		{"status with neither text nor operator is skipped", protocol.RCFrame{Kind: protocol.RCKindStatus}, "", nil},
		{"ended clears + reports", protocol.RCFrame{Kind: protocol.RCKindEnded}, "session ended on the host", func(t *testing.T, gm model) {
			if gm.rsPendingConfirm {
				t.Error("ended must clear a pending confirm")
			}
			if !strings.Contains(stripANSI(gm.status), "session ended") {
				t.Errorf("status = %q, want session ended", stripANSI(gm.status))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			m.rsPendingConfirm = true // ended must clear it; others may overwrite
			if tc.f.Kind == protocol.RCKindConfirmReq {
				m.rsPendingConfirm = false
			}
			before := len(m.rsLines)
			nm, _ := m.onRemoteFrame(remoteFrameMsg{gen: 1, f: tc.f})
			gm := asModel(nm)
			if tc.want == "" {
				if len(gm.rsLines) != before {
					t.Fatalf("a blank frame must not add a line: %v", gm.rsLines)
				}
			} else {
				if len(gm.rsLines) == before {
					t.Fatalf("no line rendered for %s", tc.f.Kind)
				}
				last := stripANSI(gm.rsLines[len(gm.rsLines)-1])
				if !strings.Contains(last, tc.want) {
					t.Fatalf("last line = %q, want substring %q", last, tc.want)
				}
			}
			if tc.f.Seq > 0 && gm.rsSeq != tc.f.Seq {
				t.Errorf("rsSeq = %d, want the frame seq %d", gm.rsSeq, tc.f.Seq)
			}
			if tc.check != nil {
				tc.check(t, gm)
			}
		})
	}
}

// TestOnRemoteFrameConfirmDoneAndBackfill: confirm_done renders approved/denied (nil
// Approve reads as denied) and clears the gate; a backfill PREPENDS the transcript
// snapshot; a blank backfill is skipped.
func TestOnRemoteFrameConfirmDoneAndBackfill(t *testing.T) {
	m := bsSeed()
	m.mode = modeRemoteSession
	m.rsGen = 1
	m.rsVP = viewport.New(80, 10)
	m.rsPendingConfirm = true
	yes := true
	nm, _ := m.onRemoteFrame(remoteFrameMsg{gen: 1, f: protocol.RCFrame{Kind: protocol.RCKindConfirmDone, Approve: &yes, Origin: "web"}})
	gm := asModel(nm)
	if gm.rsPendingConfirm || !strings.Contains(stripANSI(gm.rsLines[0]), "approved from web") {
		t.Fatalf("confirm_done should clear the gate + say approved, got %v", gm.rsLines)
	}
	nm, _ = gm.onRemoteFrame(remoteFrameMsg{gen: 1, f: protocol.RCFrame{Kind: protocol.RCKindConfirmDone, Origin: "cli"}})
	gm = asModel(nm)
	if !strings.Contains(stripANSI(gm.rsLines[1]), "denied from cli") {
		t.Fatalf("a nil approve should read denied, got %v", gm.rsLines)
	}
	nm, _ = gm.onRemoteFrame(remoteFrameMsg{gen: 1, f: protocol.RCFrame{Kind: protocol.RCKindBackfill, Text: "earlier transcript"}})
	gm = asModel(nm)
	if !strings.Contains(stripANSI(gm.rsLines[0]), "earlier transcript") {
		t.Fatalf("backfill must PREPEND, got head %q", stripANSI(gm.rsLines[0]))
	}
	before := len(gm.rsLines)
	nm, _ = gm.onRemoteFrame(remoteFrameMsg{gen: 1, f: protocol.RCFrame{Kind: protocol.RCKindBackfill, Text: " "}})
	if len(asModel(nm).rsLines) != before {
		t.Fatal("a blank backfill must be skipped")
	}
}

// TestRemoteSessionViewRender: the viewer shows the session name, the LIVE dot, the honest
// private label, the transcript, and the input line.
func TestRemoteSessionViewRender(t *testing.T) {
	m := bsSeed()
	m.rsRow = RemoteSessionRow{ID: "r", Name: "otter · lab"}
	m.rsLines = []string{"◂ hello"}
	m.rsVP = viewport.New(80, 10)
	ti := textinput.New()
	ti.Placeholder = "ask from here — it runs on the host"
	m.rsIn = ti
	out := stripANSI(m.remoteSessionView(100))
	for _, want := range []string{"REMOTE SESSION · otter · lab", "LIVE", "tools run on the host", "hello", "ask from here"} {
		if !strings.Contains(out, want) {
			t.Errorf("viewer missing %q:\n%s", want, out)
		}
	}
}

// TestOnRemoteSessionKeys: esc cancels the stream + returns to the roster; enter sends a
// typed turn through RCSend and clears the input (empty is a no-op); y/n answer ONLY a
// pending confirm (with its id) and otherwise type into the input.
func TestOnRemoteSessionKeys(t *testing.T) {
	m := bsSeed()
	m.mode = modeRemoteSession
	m.rsRow = RemoteSessionRow{ID: "rcs_k"}
	m.rsAttach = "at_k"
	var sent []protocol.RCInbound
	m.hooks.RCSend = func(broker, sid, attach string, in protocol.RCInbound) error {
		if sid != "rcs_k" || attach != "at_k" {
			t.Errorf("send args = %q/%q, want rcs_k/at_k", sid, attach)
		}
		sent = append(sent, in)
		return nil
	}
	ti := textinput.New()
	ti.Focus()
	m.rsIn = ti

	// esc cancels the stream goroutine and returns to the roster.
	canceled := false
	m.rsCancel = func() { canceled = true }
	m.rsFrames = make(chan protocol.RCFrame)
	nm, _ := m.onRemoteSessionKey(bsKey("esc"))
	gm := asModel(nm)
	if !canceled || gm.rsFrames != nil || gm.mode != modePrivate {
		t.Fatalf("esc should cancel + clear + return to the roster (canceled=%v mode=%d)", canceled, gm.mode)
	}

	// enter on an empty input is a no-op.
	if _, cmd := m.onRemoteSessionKey(bsKey("enter")); cmd != nil {
		t.Fatal("enter with no text should be a no-op")
	}
	// typed text rides as a turn and the input clears.
	m.rsIn.SetValue("  run the tests  ")
	nm, cmd := m.onRemoteSessionKey(bsKey("enter"))
	gm = asModel(nm)
	if gm.rsIn.Value() != "" || cmd == nil {
		t.Fatalf("enter should clear the input + return a send cmd, got %q", gm.rsIn.Value())
	}
	_ = cmd()
	if len(sent) != 1 || sent[0].Kind != protocol.RCInTurn || sent[0].Text != "run the tests" {
		t.Fatalf("sent = %+v, want the trimmed turn", sent)
	}

	// y with a pending confirm answers it, carrying the id.
	m.rsPendingConfirm = true
	m.rsConfirmID = "cf7"
	nm, cmd = m.onRemoteSessionKey(kmRunes("y"))
	gm = asModel(nm)
	if gm.rsPendingConfirm || cmd == nil {
		t.Fatal("y should consume the pending confirm")
	}
	_ = cmd()
	if len(sent) != 2 || sent[1].Kind != protocol.RCInConfirm || !sent[1].Approve || sent[1].ConfirmID != "cf7" {
		t.Fatalf("sent = %+v, want an approve carrying cf7", sent[1])
	}
	// N denies.
	m.rsPendingConfirm = true
	m.rsConfirmID = "cf8"
	_, cmd = m.onRemoteSessionKey(kmRunes("N"))
	_ = cmd()
	if len(sent) != 3 || sent[2].Approve || sent[2].ConfirmID != "cf8" {
		t.Fatalf("sent = %+v, want a deny carrying cf8", sent[2])
	}
	// y with NO pending confirm types into the input.
	m.rsPendingConfirm = false
	m.rsIn.SetValue("")
	nm, _ = m.onRemoteSessionKey(kmRunes("y"))
	if asModel(nm).rsIn.Value() != "y" {
		t.Fatalf("a bare y with no pending confirm must be typed, got %q", asModel(nm).rsIn.Value())
	}
}

// TestSendRemoteTurnNilHook: with no RCSend hook the send degrades to a no-op cmd.
func TestSendRemoteTurnNilHook(t *testing.T) {
	m := bsSeed()
	if cmd := m.sendRemoteTurn(protocol.RCInbound{Kind: protocol.RCInTurn, Text: "x"}); cmd != nil {
		t.Fatal("no hook -> nil cmd")
	}
}

// TestWaitRemoteFrameAndRearm: a buffered frame yields remoteFrameMsg on its generation; a
// closed channel yields remoteViewerEndMsg; re-arming with no live channel is nil.
func TestWaitRemoteFrameAndRearm(t *testing.T) {
	ch := make(chan protocol.RCFrame, 1)
	ch <- protocol.RCFrame{Kind: protocol.RCKindUser, Text: "hi"}
	msg := waitRemoteFrame(ch, 7)()
	fm, ok := msg.(remoteFrameMsg)
	if !ok || fm.gen != 7 || fm.f.Text != "hi" {
		t.Fatalf("msg = %+v, want the frame on gen 7", msg)
	}
	close(ch)
	if _, ok := waitRemoteFrame(ch, 7)().(remoteViewerEndMsg); !ok {
		t.Fatal("a closed channel should yield remoteViewerEndMsg")
	}

	m := bsSeed()
	if m.reArmRemoteStream() != nil {
		t.Fatal("no live channel -> nil re-arm")
	}
	m.rsFrames = ch
	if m.reArmRemoteStream() == nil {
		t.Fatal("a live channel -> a wait cmd")
	}
}

// TestRunRemoteCommandOffPaths: /remote-control off with nothing on is a note; with a live
// bridge it disables + clears; /remote-control while already live prints the link; and
// without the enable hook it asks for a login.
func TestRunRemoteCommandOffPaths(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)

	nm, _ := m.runRemoteCommand(true) // off with nothing on
	gm := asModel(nm)
	if !strings.Contains(stripANSI(gm.agentLines[len(gm.agentLines)-1]), "remote control is not on") {
		t.Fatalf("off with nothing on should note it, got %q", stripANSI(gm.agentLines[len(gm.agentLines)-1]))
	}

	m.rcBridge = fb
	nm, _ = m.runRemoteCommand(true) // off with a live bridge
	gm = asModel(nm)
	if gm.rcBridge != nil || !fb.stopped {
		t.Fatal("off should Disable the bridge and clear it")
	}
	if !strings.Contains(stripANSI(gm.agentLines[len(gm.agentLines)-1]), "off the air") {
		t.Fatalf("off note missing, got %q", stripANSI(gm.agentLines[len(gm.agentLines)-1]))
	}

	m.rcBridge = newFakeBridge() // already on the air
	m.rcInfo = RemoteInfo{LinkURL: "https://rogerai.fyi/r.html#8FK3"}
	nm, cmd := m.runRemoteCommand(false)
	gm = asModel(nm)
	if cmd != nil || !strings.Contains(stripANSI(gm.agentLines[len(gm.agentLines)-1]), "already on the air") {
		t.Fatalf("already-on should print the link, got %q", stripANSI(gm.agentLines[len(gm.agentLines)-1]))
	}

	m.rcBridge = nil
	m.hooks.RCEnable = nil
	nm, cmd = m.runRemoteCommand(false)
	gm = asModel(nm)
	if cmd != nil || !strings.Contains(stripANSI(gm.agentLines[len(gm.agentLines)-1]), "roger login") {
		t.Fatalf("no enable hook should ask for a login, got %q", stripANSI(gm.agentLines[len(gm.agentLines)-1]))
	}
}

// TestOnRemoteEnabledError: a failed enable prints the error and stores no bridge.
func TestOnRemoteEnabledError(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	var tm tea.Model = m
	tm, _ = tm.Update(remoteEnabledMsg{err: errors.New("broker sad")})
	gm := asModel(tm)
	if gm.rcBridge != nil || !strings.Contains(stripANSI(gm.agentLines[len(gm.agentLines)-1]), "broker sad") {
		t.Fatalf("enable error should print + store nothing, got %q", stripANSI(gm.agentLines[len(gm.agentLines)-1]))
	}
}

// TestRcSessionNameFallbacks: with no station the name leads "roger"; the cwd basename
// still rides.
func TestRcSessionNameFallbacks(t *testing.T) {
	m := bsSeed()
	m.hooks.Station = "  "
	name := m.rcSessionName()
	if !strings.HasPrefix(name, "roger") {
		t.Fatalf("name = %q, want the roger fallback", name)
	}
}

// TestRcTeeEventAllKinds: with a bridge, every harness event kind tees its frame (tool
// args marshaled); without one the tee is a silent no-op.
func TestRcTeeEventAllKinds(t *testing.T) {
	fb := newFakeBridge()
	m := bsSeed()
	m.rcBridge = fb
	m.rcTeeEvent(harness.Event{Kind: harness.EventAssistant, Text: "a"})
	m.rcTeeEvent(harness.Event{Kind: harness.EventToolCall, Tool: "Bash", Args: map[string]any{"cmd": "ls"}})
	m.rcTeeEvent(harness.Event{Kind: harness.EventToolResult, Tool: "Bash", Result: "ok"})
	m.rcTeeEvent(harness.Event{Kind: harness.EventFinal, Text: "f"})
	m.rcTeeEvent(harness.Event{Kind: harness.EventError, Text: "e"})
	if len(fb.emitted) != 5 {
		t.Fatalf("emitted %d frames, want 5", len(fb.emitted))
	}
	if fb.emitted[1].Kind != protocol.RCKindToolCall || !strings.Contains(fb.emitted[1].Args, "ls") {
		t.Fatalf("tool call frame should carry the JSON args, got %+v", fb.emitted[1])
	}
	if fb.emitted[4].Kind != protocol.RCKindError {
		t.Fatalf("error frame kind = %q", fb.emitted[4].Kind)
	}

	m.rcBridge = nil
	m.rcTeeEvent(harness.Event{Kind: harness.EventFinal, Text: "dropped"}) // no panic, no tee
	if len(fb.emitted) != 5 {
		t.Fatal("no bridge must mean no tee")
	}
}

// TestRcEmitHelpers: the confirm-req mirror carries tool/args/id (and no-ops without a
// bridge or a confirm); the cleared notice tees as an error frame.
func TestRcEmitHelpers(t *testing.T) {
	fb := newFakeBridge()
	m := bsSeed()
	m.rcBridge = fb
	c := &agentConfirm{tool: "Edit", args: map[string]any{"path": "/x"}}
	m.rcEmitConfirmReq(c, "cf1")
	if len(fb.emitted) != 1 || fb.emitted[0].Kind != protocol.RCKindConfirmReq ||
		fb.emitted[0].ConfirmID != "cf1" || !strings.Contains(fb.emitted[0].Args, "/x") {
		t.Fatalf("confirm req frame = %+v", fb.emitted)
	}
	m.rcEmitConfirmReq(nil, "cf2") // nil confirm: no frame
	if len(fb.emitted) != 1 {
		t.Fatal("nil confirm must not tee")
	}
	m.rcEmitCleared()
	if len(fb.emitted) != 2 || fb.emitted[1].Kind != protocol.RCKindError || !strings.Contains(fb.emitted[1].Text, "cleared") {
		t.Fatalf("cleared notice = %+v", fb.emitted)
	}

	m.rcBridge = nil
	m.rcEmitConfirmReq(c, "cf3") // no bridge: no panic
	m.rcEmitConfirmDone(true, "web")
	if len(fb.emitted) != 2 {
		t.Fatal("no bridge must not tee")
	}
}

// TestWaitRemoteInboundClosedChannel: a closed inbound channel (not just Done) also ends
// the host drain; a nil bridge arms nothing.
func TestWaitRemoteInboundClosedChannel(t *testing.T) {
	if waitRemoteInbound(nil) != nil {
		t.Fatal("nil bridge -> nil cmd")
	}
	fb := newFakeBridge()
	close(fb.in)
	if _, ok := waitRemoteInbound(fb)().(remoteHostEndMsg); !ok {
		t.Fatal("a closed inbound channel should end the host session")
	}
}

// TestOnRemoteInboundTurnPaths: a blank remote turn only re-arms; a turn while the agent
// is busy queues FIFO; a turn while idle submits (building the runtime if needed); an
// unknown kind just re-arms. onRemoteHostEnd with no bridge is a no-op.
func TestOnRemoteInboundTurnPaths(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	m.rcBridge = fb

	nm, _ := m.onRemoteInbound(protocol.RCInbound{Kind: protocol.RCInTurn, Text: "   "})
	if got := len(asModel(nm).agentQueued); got != 0 {
		t.Fatalf("a blank turn must not queue/submit, queued=%d", got)
	}

	busy := m
	busy.agentBusy = true
	nm, _ = busy.onRemoteInbound(protocol.RCInbound{Kind: protocol.RCInTurn, Text: "later please"})
	gm := asModel(nm)
	if len(gm.agentQueued) != 1 || gm.agentQueued[0] != (queuedPrompt{text: "later please", remote: true}) {
		t.Fatalf("a busy host must queue the turn tagged remote, got %v", gm.agentQueued)
	}

	idle := m
	idle.agent = nil // a remote turn can arrive before the host re-enters [0] AGENT
	nm, cmd := idle.onRemoteInbound(protocol.RCInbound{Kind: protocol.RCInTurn, Text: "run now"})
	gm = asModel(nm)
	if gm.agent == nil || !gm.agentBusy || cmd == nil {
		t.Fatal("an idle host must build the runtime and start the turn")
	}
	if !strings.Contains(stripANSI(strings.Join(gm.agentLines, "\n")), "run now") {
		t.Fatal("the injected turn should echo into the transcript")
	}

	nm, cmd = m.onRemoteInbound(protocol.RCInbound{Kind: "mystery"})
	if cmd == nil || len(asModel(nm).agentQueued) != 0 {
		t.Fatal("an unknown kind must only re-arm")
	}

	none := m
	none.rcBridge = nil
	if nm, _ := none.onRemoteHostEnd(); asModel(nm).rcBridge != nil {
		t.Fatal("host end with no bridge is a no-op")
	}
}

// TestCountOnline: only online offers count.
func TestCountOnline(t *testing.T) {
	if got := countOnline(nil); got != 0 {
		t.Fatalf("countOnline(nil) = %d", got)
	}
	o := []offer{{Online: true}, {Online: false}, {Online: true}}
	if got := countOnline(o); got != 2 {
		t.Fatalf("countOnline = %d, want 2", got)
	}
}

// TestMergeStickyBand: no sticky passes through; a fresh scan carrying the model clears
// the sticky (the live offer wins); a scan without it appends an OFFLINE, tunable
// placeholder carrying price/free/lineage.
func TestMergeStickyBand(t *testing.T) {
	m := bsSeed()
	m.lastConnected = nil
	in := []band{{model: "a"}}
	if out := m.mergeStickyBand(in); len(out) != 1 {
		t.Fatalf("nil sticky must pass through, got %d bands", len(out))
	}

	m.lastConnected = &offer{Model: "a"}
	if out := m.mergeStickyBand(in); len(out) != 1 || m.lastConnected != nil {
		t.Fatal("a fresh scan carrying the model must clear the sticky")
	}

	m.lastConnected = &offer{Model: "gone", PriceIn: 0.1, PriceOut: 0.2, Confidential: true}
	out := m.mergeStickyBand([]band{{model: "other"}})
	if len(out) != 2 {
		t.Fatalf("the sticky must append, got %d bands", len(out))
	}
	s := out[1]
	if s.model != "gone" || s.online || s.stations != 0 || s.minOut != 0.2 || s.lineage != 1 || s.free {
		t.Fatalf("sticky band wrong: %+v", s)
	}

	m.lastConnected = &offer{Model: "freebie"}
	out = m.mergeStickyBand(nil)
	if len(out) != 1 || !out[0].free {
		t.Fatalf("a zero-priced sticky should read FREE, got %+v", out)
	}
}

// TestTransmitLineElapsed: the relay indicator stays bare under 2s, reassures from 2s, and
// surfaces the bounded ~5m ceiling from 90s.
func TestTransmitLineElapsed(t *testing.T) {
	if got := stripANSI(transmitLine(0, 0)); strings.Contains(got, "holding") {
		t.Fatalf("no elapsed suffix under 2s, got %q", got)
	}
	if got := stripANSI(transmitLine(1, 5)); !strings.Contains(got, "5s") || !strings.Contains(got, "holding the channel") {
		t.Fatalf("slow line = %q, want the 5s reassurance", got)
	}
	if got := stripANSI(transmitLine(2, 95)); !strings.Contains(got, "95s") || !strings.Contains(got, "~5m") {
		t.Fatalf("very-slow line = %q, want the bounded ~5m ceiling", got)
	}
}
