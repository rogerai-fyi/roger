package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/harness"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// fakeBridge is a test RemoteBridge: it records emitted frames and lets a test push inbound.
type fakeBridge struct {
	emitted []protocol.RCFrame
	in      chan protocol.RCInbound
	done    chan struct{}
	sid     string
	ran     bool
	stopped bool
	parked  string // the guest-operator interlock ("" = unparked)
	snap    string
}

func newFakeBridge() *fakeBridge {
	return &fakeBridge{in: make(chan protocol.RCInbound, 16), done: make(chan struct{}), sid: "rcs_test"}
}
func (b *fakeBridge) Emit(f protocol.RCFrame)            { b.emitted = append(b.emitted, f) }
func (b *fakeBridge) Inbound() <-chan protocol.RCInbound { return b.in }
func (b *fakeBridge) Done() <-chan struct{}              { return b.done }
func (b *fakeBridge) SessionID() string                  { return b.sid }
func (b *fakeBridge) Disable() error                     { b.stop(); return nil }
func (b *fakeBridge) Stop()                              { b.stop() }
func (b *fakeBridge) Run()                               { b.ran = true }
func (b *fakeBridge) Park(op, snapshot string)           { b.parked, b.snap = op, snapshot }
func (b *fakeBridge) Unpark()                            { b.parked, b.snap = "", "" }
func (b *fakeBridge) stop() {
	if !b.stopped {
		b.stopped = true
		close(b.done)
	}
}

// rcSeedHost builds a logged-in browse model whose RCEnable hook returns the given fake bridge,
// then enters [0] AGENT (so a runtime + transcript exist).
func rcSeedHost(t *testing.T, fb *fakeBridge) model {
	t.Helper()
	m := browseSeed(120)
	m.ghLogin = "lramos85"
	m.hooks.Station = "brave-otter-37"
	m.hooks.RCEnable = func(broker, name string) (RemoteBridge, RemoteInfo, error) {
		return fb, RemoteInfo{SessionID: fb.sid, Name: name, Code: "RC 147.520 MHz · 8FK3-9MQ2", CodeShort: "8FK3-9MQ2", LinkURL: "https://rogerai.fyi/r#8FK3-9MQ2"}, nil
	}
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("0")) // enter [0] AGENT
	return asModel(tm)
}

func kmRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// TestRemoteControlEnable: /remote-control names the session <station · cwd>, prints the block,
// runs the bridge, and stores it.
func TestRemoteControlEnable(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	nm, _ := m.runAgentCommand("/remote-control")
	// The enable is async (a Cmd → remoteEnabledMsg); simulate the round-trip.
	gm := asModel(nm)
	var tm tea.Model = gm
	tm, _ = tm.Update(remoteEnabledMsg{bridge: fb, info: RemoteInfo{
		SessionID: fb.sid, Name: "brave-otter-37 · RogerAI", Code: "RC …", CodeShort: "8FK3-9MQ2",
		LinkURL: "https://rogerai.fyi/r#8FK3-9MQ2",
	}})
	gm = asModel(tm)
	if gm.rcBridge == nil {
		t.Fatal("rcBridge should be stored after enable")
	}
	if !fb.ran {
		t.Fatal("the bridge should be Run() after enable")
	}
	out := stripANSI(gm.View())
	if !strings.Contains(out, "REMOTE CONTROL") || !strings.Contains(out, "not end-to-end encrypted") {
		t.Fatalf("enable block should print the honest label:\n%s", out)
	}
	if !strings.Contains(out, "rogerai.fyi/r#8FK3-9MQ2") {
		t.Fatalf("enable block should print the link URL:\n%s", out)
	}
}

// TestRemoteControlName: the session auto-name uses the station callsign, never a hostname.
func TestRemoteControlName(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	name := m.rcSessionName()
	if !strings.HasPrefix(name, "brave-otter-37") {
		t.Fatalf("session name should lead with the station callsign, got %q", name)
	}
}

// TestRemoteTeeEvents: with a bridge attached, every agent event is mirrored out as a frame.
func TestRemoteTeeEvents(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	m.rcBridge = fb
	var tm tea.Model = m
	tm, _ = tm.Update(agentEventMsg{Kind: harness.EventAssistant, Text: "hello from the host"})
	tm, _ = tm.Update(agentEventMsg{Kind: harness.EventFinal, Text: "done"})
	gm := asModel(tm)
	_ = gm
	kinds := map[string]bool{}
	for _, f := range fb.emitted {
		kinds[f.Kind] = true
	}
	if !kinds[protocol.RCKindAssistant] || !kinds[protocol.RCKindFinal] {
		t.Fatalf("assistant + final frames should be teed to viewers, got %+v", fb.emitted)
	}
}

// TestRemoteConfirmFanIn: a remote confirm answers the pending gate (no local keypress) and
// tees a confirm_done to viewers.
func TestRemoteConfirmFanIn(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	m.rcBridge = fb
	// Stage a pending confirm as the confirm handler would.
	resp := make(chan bool, 1)
	c := agentConfirm{tool: "run_shell", args: map[string]any{"cmd": "ls"}, resp: resp}
	m.agentPendingConfirm = &c
	// A remote APPROVE arrives.
	var tm tea.Model = m
	tm, _ = tm.Update(remoteInboundMsg(protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: true, Origin: "web (lramos85)"}))
	gm := asModel(tm)
	if gm.agentPendingConfirm != nil {
		t.Fatal("the pending confirm should be cleared after a remote answer")
	}
	select {
	case v := <-resp:
		if !v {
			t.Fatal("a remote approve should send true on the confirm resp channel")
		}
	default:
		t.Fatal("the confirm resp channel should have received the remote answer")
	}
	sawDone := false
	for _, f := range fb.emitted {
		if f.Kind == protocol.RCKindConfirmDone {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatal("a confirm_done frame should be teed to viewers")
	}
}

// TestRemoteBackfill: a backfill request is answered with the transcript addressed to the viewer.
func TestRemoteBackfill(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	m.rcBridge = fb
	m.agentLines = []string{"▸ hi", "◂ hello"}
	var tm tea.Model = m
	tm, _ = tm.Update(remoteInboundMsg(protocol.RCInbound{Kind: protocol.RCInBackfill, Viewer: "v1"}))
	_ = asModel(tm)
	var bf *protocol.RCFrame
	for i := range fb.emitted {
		if fb.emitted[i].Kind == protocol.RCKindBackfill {
			bf = &fb.emitted[i]
		}
	}
	if bf == nil || bf.Viewer != "v1" || !strings.Contains(bf.Text, "hello") {
		t.Fatalf("backfill should carry the transcript addressed to the viewer, got %+v", bf)
	}
}

// TestPrivateFootnoteAndKey: the [p] footnote appears when logged in with sessions, and `p`
// from THE BAND enters BASE STATION.
func TestPrivateFootnoteAndKey(t *testing.T) {
	m := browseSeed(120)
	m.ghLogin = "lramos85"
	m.loggedIn = true
	m.rcSessions = []RemoteSessionRow{{ID: "rcs_1", Name: "brave-otter · RogerAI", Online: true}}
	foot := m.privateFootnote()
	if !strings.Contains(stripANSI(foot), "[p]") {
		t.Fatalf("footnote should offer [p], got %q", stripANSI(foot))
	}
	var tm tea.Model = m
	tm, _ = tm.Update(kmRunes("p"))
	if asModel(tm).mode != modePrivate {
		t.Fatalf("p should enter BASE STATION (modePrivate), got mode %d", asModel(tm).mode)
	}
}

// TestRemoteHostEndOnRevoke: when the bridge stops (remote revoke / disconnect), the host
// clears rcBridge and stops showing LIVE — the goroutine-leak / stale-LIVE fix.
func TestRemoteHostEndOnRevoke(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	m.rcBridge = fb
	// Arm the drain (as onRemoteEnabled does), then stop the bridge (poll 401'd remotely).
	cmd := waitRemoteInbound(fb)
	fb.Stop() // closes done
	msg := cmd()
	if _, ok := msg.(remoteHostEndMsg); !ok {
		t.Fatalf("a stopped bridge should yield remoteHostEndMsg, got %T", msg)
	}
	var tm tea.Model = m
	tm, _ = tm.Update(msg)
	if asModel(tm).rcBridge != nil {
		t.Fatal("rcBridge must be cleared after the host session ends")
	}
}

// TestRemoteStaleConfirmRejected: a remote confirm answer carrying a STALE id (for an
// already-resolved confirm) must NOT resolve a newer pending confirm.
func TestRemoteStaleConfirmRejected(t *testing.T) {
	fb := newFakeBridge()
	m := rcSeedHost(t, fb)
	m.rcBridge = fb
	// A NEW confirm is pending with id "cf-new".
	resp := make(chan bool, 1)
	c := agentConfirm{tool: "run_shell", args: map[string]any{"cmd": "rm -rf /"}, resp: resp}
	m.agentPendingConfirm = &c
	m.rcConfirmID = "cf-new"
	// A stale approve for the OLD confirm "cf-old" arrives.
	var tm tea.Model = m
	tm, _ = tm.Update(remoteInboundMsg(protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: true, ConfirmID: "cf-old", Origin: "web"}))
	gm := asModel(tm)
	if gm.agentPendingConfirm == nil {
		t.Fatal("a stale confirm id must NOT resolve the current pending confirm")
	}
	select {
	case <-resp:
		t.Fatal("the confirm resp channel must not receive a stale answer")
	default:
	}
	// The matching id DOES resolve it.
	tm, _ = gm.Update(remoteInboundMsg(protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: false, ConfirmID: "cf-new", Origin: "web"}))
	if asModel(tm).agentPendingConfirm != nil {
		t.Fatal("the matching confirm id should resolve the pending confirm")
	}
}

// TestPrivateLoggedOutNoop: `p` is a no-op when logged out (base station needs an account).
func TestPrivateLoggedOutNoop(t *testing.T) {
	m := browseSeed(120)
	m.ghLogin = ""
	m.loggedIn = false
	if m.privateFootnote() != "" {
		t.Fatal("no footnote when logged out")
	}
	var tm tea.Model = m
	tm, _ = tm.Update(kmRunes("p"))
	if asModel(tm).mode == modePrivate {
		t.Fatal("p must not enter BASE STATION when logged out")
	}
}
