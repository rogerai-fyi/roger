package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/harness"
)

// toolCallBroker answers the FIRST completion with a tool call and every later one with
// a plain answer, so a turn reaches the confirm gate through the real loop rather than a
// hand-built agentConfirm.
func toolCallBroker(t *testing.T, tool, args string) *httptest.Server {
	t.Helper()
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			return
		}
		n++
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "tool_calls": []map[string]any{{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": tool, "arguments": args},
				}}},
			}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"message": map[string]any{"role": "assistant", "content": "done"},
		}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestAGateThatWasRaisedIsNeverWithdrawn pins the rule that a confirm the operator can
// SEE is always the operator's to answer.
//
// Cancellation is decided BEFORE the gate is offered: a stopped turn must not ask
// permission for work nobody wants. But once the gate is on screen the decision belongs
// to the person looking at it. The confirmer used to also race the abort against the
// ANSWER, so a turn cancelled while the gate was up abandoned it: the modal stayed on
// screen swallowing keys, and when the operator finally pressed y nothing was listening -
// their approval went into a buffered channel nobody would ever read, and the tool was
// recorded as approved without running.
//
// The observable is exactly that: after the operator answers, did the confirmer take the
// answer? A buffered resp channel that still holds a value is an approval that went
// nowhere.
func TestAGateThatWasRaisedIsNeverWithdrawn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := toolCallBroker(t, "read_file", fmt.Sprintf(`{"path":%q}`, "note.txt"))

	base := browseSeed(120)
	base.broker = srv.URL
	base.user = "tester"
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	base.sessionWorkdir, base.sessionWorkdirAvailable = dir, true

	nm, _ := base.enterAgent()
	am := asModel(nm)
	// Gate the read instead of web_fetch, so the turn reaches a confirm without reaching
	// the network.
	am.agent.loop.NeedsConfirm = func(tl harness.Tool) bool { return tl.Name == "read_file" }
	am.agentBusy = true
	am.startAgentTurn("read the note")()

	// Pump the drain until the gate surfaces.
	var gate *agentConfirm
	deadline := time.Now().Add(20 * time.Second)
	for gate == nil && time.Now().Before(deadline) {
		cmd := am.waitAgentEvent()
		if cmd == nil {
			t.Fatal("no drain")
		}
		msg := cmd()
		out, _ := am.Update(msg)
		am = asModel(out)
		if c, ok := msg.(agentConfirmMsg); ok {
			cc := agentConfirm(c)
			gate = &cc
		}
	}
	if gate == nil {
		t.Fatal("the turn never reached a confirm gate")
	}
	if am.agentPendingConfirm == nil {
		t.Fatal("the gate reached the UI but never became a pending confirm")
	}

	// THE FORCE-STOP, with the gate already on screen.
	if am.agent.cancel != nil {
		am.agent.cancel()
	}
	time.Sleep(150 * time.Millisecond) // let any abandonment happen

	// The operator answers the gate they can see.
	out, _ := am.onAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	am = asModel(out)

	// Did anyone take the answer?
	settled := time.Now().Add(5 * time.Second)
	for len(gate.resp) > 0 && time.Now().Before(settled) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(gate.resp) > 0 {
		t.Fatal("the operator approved a gate that was on screen and nothing consumed the " +
			"answer: the confirmer abandoned a confirm it had already shown, so the approval " +
			"went nowhere and the turn was told the tool was denied")
	}
}
