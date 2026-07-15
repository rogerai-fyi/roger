package tui

// Founder spec (2026-07-14, session note):
//   - ctrl+p is the PERMS key, not a history alias: in AGENT it cycles the tool-approval
//     mode exactly like bare /perms (history recall stays on Up/Down; ctrl+n keeps its
//     newer-recall alias). It works MID-TURN - the whole point is instant toggling.
//   - /perms and /yolo typed mid-turn execute IMMEDIATELY instead of being parked in the
//     prompt queue (the queued form fired late and cycled the mode unpredictably).
//   - In the channel (chat) ctrl+p no longer recalls; it points at the AGENT instead.
//   - The browser node console no longer auto-opens by default; `w` in BROWSE and /webui
//     in AGENT open it on demand at Hooks.ConsoleURL.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeTTY makes openURL's interactive() gate pass and records every attempted browser
// open, so a test can assert an open without spawning a process.
func fakeTTY(t *testing.T) *[]string {
	t.Helper()
	origIn, origOut, origExec := stdinIsTTY, stdoutIsTTY, openURLExec
	var opened []string
	stdinIsTTY = func() bool { return true }
	stdoutIsTTY = func() bool { return true }
	openURLExec = func(url string) { opened = append(opened, url) }
	t.Cleanup(func() { stdinIsTTY, stdoutIsTTY, openURLExec = origIn, origOut, origExec })
	return &opened
}

// TestAgentCtrlPCyclesPerms: ctrl+p cycles confirm -> edits -> all -> confirm, with the
// same visible feedback as /perms (masthead chip, loud ember on the full bypass).
func TestAgentCtrlPCyclesPerms(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	if got := agentPermMode(m.agent.perms.Load()); got != permConfirm {
		t.Fatalf("default mode = %v want confirm", got)
	}
	press := func(want agentPermMode) {
		t.Helper()
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
		m = asModel(out)
		if got := agentPermMode(m.agent.perms.Load()); got != want {
			t.Fatalf("ctrl+p -> %v want %v", got, want)
		}
	}
	press(permEdits)
	if !strings.Contains(stripANSI(m.agentPermTag()), "auto-edits") {
		t.Error("edits mode should chip the masthead")
	}
	press(permAll)
	if !strings.Contains(stripANSI(strings.Join(m.agentLines, "\n")), "! tools auto-all") {
		t.Error("the full bypass should print the loud ember line")
	}
	press(permConfirm)
}

// TestAgentIdleHelpAdvertisesPermsKey: the founder's root complaint was that toggling
// perms felt invisible. The idle help tail must name the ⌃p key next to the mode, so
// the shortcut is discoverable without reading docs.
func TestAgentIdleHelpAdvertisesPermsKey(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	m.agentBusy = false
	v := stripANSI(m.agentView(120))
	if !strings.Contains(v, "⌃p") {
		t.Errorf("idle help should advertise the ⌃p perms key, got:\n%s", v)
	}
}

// TestAgentCtrlPWorksMidTurn: the founder pain - toggling approvals while a turn runs
// must apply NOW (the mode is an atomic the confirmer reads live), never queue.
func TestAgentCtrlPWorksMidTurn(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	m.agentBusy = true
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = asModel(out)
	if got := agentPermMode(m.agent.perms.Load()); got != permEdits {
		t.Fatalf("mid-turn ctrl+p -> %v want edits", got)
	}
	if len(m.agentQueued) != 0 {
		t.Fatalf("ctrl+p must never queue, got %d queued", len(m.agentQueued))
	}
}

// TestAgentCtrlPAtConfirmGate: ctrl+p at a pending tool-approval confirm cycles perms
// (never the surprise DENY the confirm modal's default branch used to give). When the
// escalated mode now auto-approves the pending tool, the gate resolves as approved -
// the intuitive "stop asking me, allow this"; otherwise it cycles and stays pending.
func TestAgentCtrlPAtConfirmGate(t *testing.T) {
	// run_shell: confirm -> edits (still gated) -> all (auto-approved).
	m := agentSeed(t, "http://broker.local")
	resp := make(chan bool, 1)
	m.agentPendingConfirm = &agentConfirm{tool: "run_shell", resp: resp}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = asModel(out)
	if got := agentPermMode(m.agent.perms.Load()); got != permEdits {
		t.Fatalf("first ctrl+p at gate -> %v want edits", got)
	}
	if m.agentPendingConfirm == nil {
		t.Fatal("edits does not cover run_shell - the gate must stay pending, not resolve")
	}
	if len(resp) != 0 {
		t.Fatal("the pending tool must NOT be answered while still gated")
	}
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = asModel(out)
	if got := agentPermMode(m.agent.perms.Load()); got != permAll {
		t.Fatalf("second ctrl+p -> %v want all", got)
	}
	if m.agentPendingConfirm != nil {
		t.Fatal("auto-all covers run_shell - the gate should resolve, not linger")
	}
	if got := <-resp; !got {
		t.Fatal("escalating past the tool's bar should APPROVE it, not deny")
	}

	// write_file is covered the moment we reach edits - one press approves.
	m2 := agentSeed(t, "http://broker.local")
	resp2 := make(chan bool, 1)
	m2.agentPendingConfirm = &agentConfirm{tool: "write_file", resp: resp2}
	out, _ = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m2 = asModel(out)
	if m2.agentPendingConfirm != nil || len(resp2) != 1 || !<-resp2 {
		t.Fatal("edits covers write_file - one ctrl+p should approve the gate")
	}
}

// TestInstantCommandsAreRealCommands: every instantAgentCommand token must be a command
// runAgentCommand actually dispatches - otherwise an instant token would fall through to
// the model picker (the unknown-command default) instead of running now. Guards the two
// parallel lists against drift (review finding).
func TestInstantCommandsAreRealCommands(t *testing.T) {
	for _, tok := range []string{"/perms", "/permissions", "/yolo", "/webui", "/console", "/web"} {
		if !instantAgentCommand(tok) {
			t.Fatalf("%s should classify as instant", tok)
		}
		m := agentSeed(t, "http://broker.local")
		out, _ := m.runAgentCommand(tok)
		if asModel(out).agentPicker {
			t.Errorf("%s is instant but runAgentCommand did not handle it (fell to the model picker)", tok)
		}
	}
}

// TestPermsCommandBypassesQueue: typing /perms (or /yolo) mid-turn executes immediately
// instead of parking in the prompt queue - the queued form fired after the turn and
// cycled the mode unpredictably (screenshot: two "queued · /perms" then a late double
// cycle through auto-all).
func TestPermsCommandBypassesQueue(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	m.agentBusy = true
	send := func(line string) {
		t.Helper()
		m.agentIn.SetValue(line)
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = asModel(out)
	}
	send("/perms")
	if got := agentPermMode(m.agent.perms.Load()); got != permEdits {
		t.Fatalf("mid-turn /perms -> %v want edits (must run now, not queue)", got)
	}
	if len(m.agentQueued) != 0 {
		t.Fatalf("/perms must not be queued, got %d queued", len(m.agentQueued))
	}
	send("/yolo")
	if got := agentPermMode(m.agent.perms.Load()); got != permAll {
		t.Fatalf("mid-turn /yolo -> %v want all", got)
	}
	// A REAL prompt still queues while busy - the bypass is only for instant commands.
	send("do the thing")
	if len(m.agentQueued) != 1 {
		t.Fatalf("a chat prompt should still queue mid-turn, got %d queued", len(m.agentQueued))
	}
}

// TestInstantAgentCommandTable: the instant-command classifier, including the
// defensive empty line (unreachable from the enter handler, which trims first).
func TestInstantAgentCommandTable(t *testing.T) {
	for line, want := range map[string]bool{
		"/perms": true, "/permissions": true, "/yolo": true, "/perms all": true,
		"/webui": true, "/console": true, "/web": true,
		"/clear": false, "/model x": false, "do the thing": false, "": false, "   ": false,
	} {
		if got := instantAgentCommand(line); got != want {
			t.Errorf("instantAgentCommand(%q) = %v want %v", line, got, want)
		}
	}
}

// TestChatCtrlPNoLongerRecalls: in the channel ctrl+p is not a recall alias anymore -
// the draft stays put and the status points at the AGENT (Up/Down still recall).
func TestChatCtrlPNoLongerRecalls(t *testing.T) {
	m := tallChat(t, 5)
	mm := asModel(m)
	mm.chatHist.add("recall me")
	mm.chatIn.SetValue("a draft")
	m = mm
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if got := asModel(m).chatIn.Value(); got != "a draft" {
		t.Fatalf("ctrl+p must not touch the draft, got %q", got)
	}
	s := stripANSI(asModel(m).status)
	if !strings.Contains(s, "AGENT") || !strings.Contains(s, "shift+tab") {
		t.Errorf("ctrl+p in chat should point at the AGENT via shift+tab, status = %q", s)
	}
	if strings.Contains(s, "0 opens") || strings.Contains(s, "(or 0)") {
		t.Errorf("chat has no 0-opens-agent binding - the hint must not claim it, status = %q", s)
	}
}

// TestBrowseWOpensConsole: `w` in BROWSE opens the browser node console at the URL the
// host wired into Hooks.ConsoleURL, and says so in the status line.
func TestBrowseWOpensConsole(t *testing.T) {
	opened := fakeTTY(t)
	m := browseSeed(100)
	m.hooks.ConsoleURL = "http://127.0.0.1:4180/?t=abc"
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("w"))
	if len(*opened) != 1 || (*opened)[0] != "http://127.0.0.1:4180/?t=abc" {
		t.Fatalf("w should open the console URL once, got %v", *opened)
	}
	if s := stripANSI(asModel(tm).status); !strings.Contains(s, "4180") {
		t.Errorf("status should show the console URL, got %q", s)
	}
}

// TestBrowseWWithoutConsole: with no console this run (--no-webui), `w` explains
// instead of silently doing nothing.
func TestBrowseWWithoutConsole(t *testing.T) {
	opened := fakeTTY(t)
	m := browseSeed(100)
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("w"))
	if len(*opened) != 0 {
		t.Fatalf("no console URL - nothing should open, got %v", *opened)
	}
	if s := stripANSI(asModel(tm).status); !strings.Contains(s, "no-webui") {
		t.Errorf("status should explain how to get the console, got %q", s)
	}
}

// TestBrowseWebuiCommand: the `/`-palette spelling works from BROWSE and the channel
// runner too (with and without a console this run).
func TestBrowseWebuiCommand(t *testing.T) {
	opened := fakeTTY(t)
	m := browseSeed(100)
	m.hooks.ConsoleURL = "http://127.0.0.1:4180/?t=abc"
	out, _ := m.run("webui")
	if len(*opened) != 1 || (*opened)[0] != "http://127.0.0.1:4180/?t=abc" {
		t.Fatalf("run(webui) should open the console URL, got %v", *opened)
	}
	if s := stripANSI(asModel(out).status); !strings.Contains(s, "4180") {
		t.Errorf("status should show the console URL, got %q", s)
	}
	m2 := browseSeed(100)
	out, _ = m2.run("console")
	if len(*opened) != 1 {
		t.Fatalf("no console URL - run(console) must not open, got %v", *opened)
	}
	if s := stripANSI(asModel(out).status); !strings.Contains(s, "no-webui") {
		t.Errorf("status should explain how to get the console, got %q", s)
	}
	// The channel-session runner spelling too, both with and without a console.
	mc := asModel(tallChat(t, 5))
	mc.hooks.ConsoleURL = "http://127.0.0.1:4180/?t=abc"
	mc.runSession("webui")
	if len(*opened) != 2 {
		t.Fatalf("runSession(webui) should open the console URL, got %v", *opened)
	}
	mc2 := asModel(tallChat(t, 5))
	out, _ = mc2.runSession("console")
	if len(*opened) != 2 {
		t.Fatalf("no console URL - runSession(console) must not open, got %v", *opened)
	}
	// The /? short help stays in lock-step with what runSession accepts.
	mh := asModel(tallChat(t, 5))
	out, _ = mh.runSession("?")
	if joined := stripANSI(strings.Join(asModel(out).transcript, "\n")); !strings.Contains(joined, "/webui") {
		t.Errorf("/? should list /webui, got:\n%s", joined)
	}
}

// TestAgentWebuiCommand: /webui in AGENT opens the console (an instant command - it
// also bypasses the mid-turn queue), /console is an alias, and the no-console run
// explains itself.
func TestAgentWebuiCommand(t *testing.T) {
	opened := fakeTTY(t)
	m := agentSeed(t, "http://broker.local")
	m.hooks.ConsoleURL = "http://127.0.0.1:4180/?t=abc"
	out, _ := m.runAgentCommand("/webui")
	m = asModel(out)
	if len(*opened) != 1 || (*opened)[0] != "http://127.0.0.1:4180/?t=abc" {
		t.Fatalf("/webui should open the console URL, got %v", *opened)
	}
	out, _ = m.runAgentCommand("/console")
	m = asModel(out)
	if len(*opened) != 2 {
		t.Fatalf("/console should alias /webui, got %v", *opened)
	}

	m2 := agentSeed(t, "http://broker.local")
	out, _ = m2.runAgentCommand("/webui")
	m2 = asModel(out)
	if len(*opened) != 2 {
		t.Fatalf("no console URL - /webui must not open, got %v", *opened)
	}
	joined := stripANSI(strings.Join(m2.agentLines, "\n"))
	if !strings.Contains(joined, "no-webui") {
		t.Errorf("/webui without a console should explain, lines:\n%s", joined)
	}

	// Mid-turn: /webui is instant, never queued.
	m.agentBusy = true
	m.agentIn.SetValue("/webui")
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(out)
	if len(m.agentQueued) != 0 {
		t.Fatalf("/webui must not queue mid-turn, got %d queued", len(m.agentQueued))
	}
	if len(*opened) != 3 {
		t.Fatalf("mid-turn /webui should open now, got %v", *opened)
	}
}
