package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The in-channel chat turn must carry the caller's STANDING exclusions, not only the
// stations that already failed this turn.
//
// Why it matters: the broker groups offers by model id alone, so a tuned row that means
// "this model at THIS quant" is only honoured if the caller names the stations running a
// different one. The relay path (client.ProxyOptions.ExcludeNodes) already did this; the
// TUI's own chat turn did not, so "tuning a row binds routing" quietly failed for the
// booth's chat while passing for `roger use`.
func TestChatTurnsCarriesTheCallersExclusions(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Roger-Exclude-Nodes")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	_, err := ChatTurns(srv.URL, "user", "m", []ChatTurn{{Role: "user", Content: "hi"}},
		false, 0, "", []string{"node-b", "node-c"})
	if err != nil {
		t.Fatalf("ChatTurns: %v", err)
	}

	for _, want := range []string{"node-b", "node-c"} {
		if !strings.Contains(got, want) {
			t.Errorf("X-Roger-Exclude-Nodes = %q, missing %q - the tuned row does not bind routing", got, want)
		}
	}
}

// No exclusions and no failures means no header at all: an empty exclusion list must not
// become an empty header the broker has to interpret.
func TestChatTurnsSendsNoExclusionHeaderWhenThereIsNothingToExclude(t *testing.T) {
	seen := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, seen = r.Header["X-Roger-Exclude-Nodes"]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	if _, err := ChatTurns(srv.URL, "user", "m", []ChatTurn{{Role: "user", Content: "hi"}},
		false, 0, "", nil); err != nil {
		t.Fatalf("ChatTurns: %v", err)
	}
	if seen {
		t.Error("an exclusion header was sent with nothing to exclude")
	}
}

// Blank entries are dropped rather than sent as empty names.
func TestChatTurnsIgnoresBlankExclusions(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Roger-Exclude-Nodes")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	if _, err := ChatTurns(srv.URL, "user", "m", []ChatTurn{{Role: "user", Content: "hi"}},
		false, 0, "", []string{"  ", "node-a", ""}); err != nil {
		t.Fatalf("ChatTurns: %v", err)
	}
	if got != "node-a" {
		t.Errorf("X-Roger-Exclude-Nodes = %q, want exactly node-a", got)
	}
}
