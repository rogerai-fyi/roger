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
	if s := stripANSI(asModel(m).status); !strings.Contains(s, "AGENT") {
		t.Errorf("ctrl+p in chat should point at the AGENT, status = %q", s)
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
