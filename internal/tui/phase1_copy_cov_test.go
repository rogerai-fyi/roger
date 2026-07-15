package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOSC52Envelope(t *testing.T) {
	got := osc52("hi") // base64("hi") == "aGk="
	if !strings.HasPrefix(got, "\x1b]52;c;") || !strings.HasSuffix(got, "\a") || !strings.Contains(got, "aGk=") {
		t.Errorf("osc52(\"hi\") = %q, want the OSC52 envelope with base64 aGk=", got)
	}
}

func TestConnectExportBlock(t *testing.T) {
	b := connectExport("http://127.0.0.1:4141/v1", "roger-local", "qwen3-4b")
	for _, w := range []string{
		"export OPENAI_BASE_URL=http://127.0.0.1:4141/v1",
		"export OPENAI_API_KEY=roger-local",
		"export OPENAI_MODEL=qwen3-4b",
	} {
		if !strings.Contains(b, w) {
			t.Errorf("connectExport missing %q in:\n%s", w, b)
		}
	}
}

func TestTranscriptTextStripsANSI(t *testing.T) {
	m := model{transcript: []string{stLive.Render("◂ hello"), stDim.Render("· world")}}
	txt := m.transcriptText()
	if !strings.Contains(txt, "hello") || !strings.Contains(txt, "world") {
		t.Errorf("transcriptText = %q, want clean hello + world", txt)
	}
	if strings.Contains(txt, "\x1b[") {
		t.Errorf("transcriptText still contains ANSI escapes: %q", txt)
	}
}

// chatModelForCopy is a connected, modeChat model with an endpoint + key (for the
// connect/copy paths).
func chatModelForCopy() model {
	m := seedFor(120, modeChat, false)
	m.connected = &offer{NodeID: "nyx", Model: "qwen3-4b", Online: true}
	m.endpoint = "http://127.0.0.1:4141/v1"
	m.apikey = "roger-local"
	return m
}

func TestSlashConnectRendersAndCopies(t *testing.T) {
	out, cmd := chatModelForCopy().runSession("/connect")
	body := stripANSI(strings.Join(asModel(out).transcript, "\n"))
	for _, w := range []string{"CONNECT", "http://127.0.0.1:4141/v1", "roger-local", "qwen3-4b", "copied"} {
		if !strings.Contains(body, w) {
			t.Errorf("/connect transcript missing %q:\n%s", w, body)
		}
	}
	if cmd == nil {
		t.Error("/connect should return a clipboard cmd")
	}
}

func TestSlashCopyLastReplyAndAll(t *testing.T) {
	out0, _ := chatModelForCopy().runSession("/copy")
	if !strings.Contains(stripANSI(strings.Join(asModel(out0).transcript, "\n")), "nothing to copy") {
		t.Error("/copy with no reply should say nothing to copy")
	}
	m := chatModelForCopy()
	m.lastReply = "forty-two"
	out1, cmd1 := m.runSession("/copy")
	if cmd1 == nil || !strings.Contains(stripANSI(strings.Join(asModel(out1).transcript, "\n")), "copied the last reply") {
		t.Error("/copy should copy the last reply (with a cmd)")
	}
	m2 := chatModelForCopy()
	m2.transcript = []string{stLive.Render("◂ hi there")}
	out2, cmd2 := m2.runSession("/copy all")
	if cmd2 == nil || !strings.Contains(stripANSI(strings.Join(asModel(out2).transcript, "\n")), "copied the transcript") {
		t.Error("/copy all should copy the transcript (with a cmd)")
	}
}

func TestSlashMouseToggle(t *testing.T) {
	// Default is wheel-scroll (mouseOff=false: the wheel scrolls transcripts as real
	// mouse events, arrows mean history). /mouse toggles OUT to native-select (copy
	// without shift-drag), and again back to wheel-scroll.
	m := chatModelForCopy()
	if m.mouseOff {
		t.Fatal("default should be wheel-scroll (mouseOff=false) so the wheel scrolls the transcripts")
	}
	out, cmd := m.runSession("/mouse")
	if !asModel(out).mouseOff || cmd == nil {
		t.Error("/mouse from the default should switch to native-select (mouseOff=true) + DisableMouse")
	}
	out2, cmd2 := asModel(out).runSession("/mouse")
	if asModel(out2).mouseOff || cmd2 == nil {
		t.Error("/mouse again should restore wheel-scroll (mouseOff=false) + a cmd")
	}
}

// TestSlashHelpAliases: /? (the short help) + /commands + /help + /h all show the command
// list (not "unknown"), and the listing surfaces /agent.
func TestSlashHelpAliases(t *testing.T) {
	for _, c := range []string{"/?", "/commands", "/help", "/h"} {
		out, _ := chatModelForCopy().runSession(c)
		tx := stripANSI(strings.Join(asModel(out).transcript, "\n"))
		if strings.Contains(tx, "unknown") {
			t.Errorf("%q should show the command list, got unknown: %s", c, tx)
		}
		if !strings.Contains(tx, "/agent") {
			t.Errorf("%q listing should surface /agent; got: %s", c, tx)
		}
	}
}

func TestCtrlYYanksLastReply(t *testing.T) {
	m := chatModelForCopy()
	m.lastReply = "the answer"
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if cmd == nil || !strings.Contains(stripANSI(asModel(out).status), "Copied to clipboard") {
		t.Errorf("ctrl+y should copy + toast; status=%q cmd=%v", stripANSI(asModel(out).status), cmd != nil)
	}
	out2, _ := chatModelForCopy().Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if !strings.Contains(stripANSI(asModel(out2).status), "nothing to copy") {
		t.Errorf("ctrl+y with no reply should say nothing to copy; got %q", stripANSI(asModel(out2).status))
	}
}

func TestClearAndDisconnectResetLastReply(t *testing.T) {
	// /clear: stale lastReply must be wiped so ctrl+y/`/copy` can't yank cleared text.
	m := chatModelForCopy()
	m.lastReply = "secret prior answer"
	m.transcript = []string{stLive.Render("◂ secret prior answer")}
	out, _ := m.runSession("/clear")
	if got := asModel(out).lastReply; got != "" {
		t.Errorf("/clear left lastReply = %q, want empty", got)
	}
	// disconnect: same — leaving a channel must drop the prior reply.
	m2 := chatModelForCopy()
	m2.lastReply = "prior channel reply"
	out2, _ := m2.disconnect()
	if got := asModel(out2).lastReply; got != "" {
		t.Errorf("disconnect left lastReply = %q, want empty", got)
	}
}

func TestClipboardWriteCmdRuns(t *testing.T) {
	// Exercise the actual cmd body (OSC52 emit + copyToClipboard) — not just cmd != nil.
	if clipboardWrite("") != nil {
		t.Error("clipboardWrite(\"\") should return a nil cmd")
	}
	cmd := clipboardWrite("payload")
	if cmd == nil {
		t.Fatal("clipboardWrite(non-empty) should return a cmd")
	}
	if msg := cmd(); msg != nil { // runs fmt.Print(osc52) + copyToClipboard
		t.Errorf("clipboardWrite cmd should return a nil msg, got %#v", msg)
	}
}

func TestChannelFooterDedupAndHints(t *testing.T) {
	m := seedFor(120, modeChat, false)
	m.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	v := stripANSI(m.View())
	if strings.Contains(v, "enter sends") {
		t.Error("channel still renders the duplicate in-view key line ('enter sends')")
	}
	if !strings.Contains(v, "copy") || !strings.Contains(v, "/connect") {
		t.Errorf("channel hint bar should surface copy + /connect; rendered:\n%s", v)
	}
}

func TestCtrlOTogglesNativeSelect(t *testing.T) {
	// Default wheel-scroll; ctrl+o toggles to native-select (copy without shift-drag),
	// then back to wheel-scroll.
	out, cmd := chatModelForCopy().Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !asModel(out).mouseOff || cmd == nil {
		t.Error("ctrl+o from the default should switch to native-select (mouseOff=true) + DisableMouse")
	}
	out2, cmd2 := asModel(out).Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if asModel(out2).mouseOff || cmd2 == nil {
		t.Error("ctrl+o again should restore wheel-scroll (mouseOff=false) + a cmd")
	}
}
