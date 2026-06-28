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
	out, cmd := chatModelForCopy().runSession("/mouse")
	if !asModel(out).mouseOff || cmd == nil {
		t.Error("/mouse should toggle native-select ON (mouseOff=true) + return DisableMouse")
	}
}

func TestCtrlYYanksLastReply(t *testing.T) {
	m := chatModelForCopy()
	m.lastReply = "the answer"
	out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if cmd == nil || !strings.Contains(stripANSI(asModel(out).status), "copied the last reply") {
		t.Errorf("ctrl+y should copy + toast; status=%q cmd=%v", stripANSI(asModel(out).status), cmd != nil)
	}
	out2, _ := chatModelForCopy().Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if !strings.Contains(stripANSI(asModel(out2).status), "nothing to copy") {
		t.Errorf("ctrl+y with no reply should say nothing to copy; got %q", stripANSI(asModel(out2).status))
	}
}

func TestCtrlOTogglesNativeSelect(t *testing.T) {
	out, cmd := chatModelForCopy().Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !asModel(out).mouseOff || cmd == nil {
		t.Error("ctrl+o should disable mouse (native select ON) + return a cmd")
	}
}
