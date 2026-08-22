package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// THE CONFIRM GATE, END TO END.
//
// FOUNDER 2026-08-21, three times: "why is it still not asking permission" / "i still
// haven't seen it ask me for confirmation for anything".
//
// Every existing test injected an already-made confirm into the UI and checked how it
// RENDERED. Nothing proved the other half - that a model calling a gated tool actually
// produces one - so a break anywhere between the loop and the confirmer would have been
// invisible to the suite and visible only as a tool that ran without asking.

// gateLoop drives one turn against a fake upstream that calls `tool` once, and reports
// which tools the confirmer was asked about.
func gateLoop(t *testing.T, tool, args string, approve bool) (asked []string, transcript string) {
	return gateLoopPolicy(t, tool, args, approve, nil)
}

// gateLoopPolicy is gateLoop with an explicit front-end ConfirmPolicy.
func gateLoopPolicy(t *testing.T, tool, args string, approve bool, pol ConfirmPolicy) (asked []string, transcript string) {
	t.Helper()
	step := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		if step == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"` + tool + `","arguments":` + args + `}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer up.Close()

	l := NewLoop(t.TempDir(), "sys", BrokerCompleter(up.URL, "u", "m", false, 0, nil),
		func(name string, _ map[string]any) bool {
			asked = append(asked, name)
			return approve
		})
	l.NeedsConfirm = pol
	var b strings.Builder
	out, err := l.Send(context.Background(), "go", func(e Event) {
		if e.Kind == EventToolResult {
			b.WriteString(e.Result)
		}
	})
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	_ = out
	return asked, b.String()
}

// THE HEADLINE: a mutating tool ASKS before it runs.
func TestMutatingToolsAskFirst(t *testing.T) {
	for _, tc := range []struct{ tool, args string }{
		{"write_file", `"{\"path\":\"x.txt\",\"content\":\"hi\"}"`},
		{"run_shell", `"{\"cmd\":\"echo hi\"}"`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			asked, _ := gateLoop(t, tc.tool, tc.args, true)
			if len(asked) == 0 {
				t.Fatalf("%s ran WITHOUT asking - the confirm gate never fired", tc.tool)
			}
			if asked[0] != tc.tool {
				t.Errorf("the confirm named %q, not %q", asked[0], tc.tool)
			}
		})
	}
}

// A DENIED confirm must not run the tool, and must tell the model why so it can adapt
// rather than silently retrying the same call forever.
func TestADeniedConfirmDoesNotRunTheTool(t *testing.T) {
	asked, transcript := gateLoop(t, "write_file", `"{\"path\":\"x.txt\",\"content\":\"hi\"}"`, false)
	if len(asked) == 0 {
		t.Fatal("nothing was asked")
	}
	if !strings.Contains(strings.ToLower(transcript), "denied") {
		t.Errorf("a denial must be fed back to the model in words, got %q", transcript)
	}
}

// NEGATIVE HALF: read-only tools must NOT ask. A gate on everything is a gate nobody
// reads, and the whole value of the prompt is that it means something when it appears.
func TestReadOnlyToolsDoNotAsk(t *testing.T) {
	asked, _ := gateLoop(t, "list_dir", `"{\"path\":\".\"}"`, true)
	if len(asked) != 0 {
		t.Errorf("a read-only tool asked for confirmation: %v", asked)
	}
}

// A FRONT-END POLICY can widen the gate. The terminal uses this for web_fetch: the tool
// changes nothing on the machine, so it is not Mutating, but it reaches out to an arbitrary
// host and pulls untrusted text back into a conversation that also has write_file and
// run_shell in it. Headless callers keep the default and are unaffected.
func TestAPolicyCanWidenTheGate(t *testing.T) {
	fetchArgs := `"{\"url\":\"https://example.com\"}"`
	// Default policy: a fetch runs unasked.
	if asked, _ := gateLoop(t, "web_fetch", fetchArgs, true); len(asked) != 0 {
		t.Errorf("the DEFAULT policy gated a fetch, which would change every headless caller: %v", asked)
	}
	// Widened: it asks.
	asked, _ := gateLoopPolicy(t, "web_fetch", fetchArgs, true,
		func(tl Tool) bool { return tl.Name == "web_fetch" })
	if len(asked) == 0 || asked[0] != "web_fetch" {
		t.Errorf("a widened policy did not gate the fetch: %v", asked)
	}
}

// A POLICY CANNOT OPEN THE GATE. needsConfirm ORs with Mutating, so no front-end can talk
// the loop out of asking about a write or a shell - the one direction that must never be
// available, because it is the direction that loses the operator's veto.
func TestAPolicyCannotOpenTheGate(t *testing.T) {
	asked, _ := gateLoopPolicy(t, "write_file", `"{\"path\":\"x.txt\",\"content\":\"hi\"}"`, true,
		func(Tool) bool { return false })
	if len(asked) == 0 {
		t.Fatal("a policy returning false suppressed the confirm on write_file")
	}
}
