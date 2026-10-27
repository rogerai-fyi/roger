package client

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The in-channel chat turn must carry the caller's STANDING exclusions, not only the
// stations that already failed this turn - as provider.ignore in the request body, the one
// carrier policy every in-booth path speaks (X-Roger-Exclude-Nodes is the old-broker
// header-mode form).
//
// Why it matters: a caller's standing exclusions are how a station it will not accept stays
// out of the re-pick. The relay path already did this; the TUI's own chat turn did not, so
// "an exclusion binds routing" quietly failed for the booth's chat while passing for
// `roger use`.
func TestChatTurnsCarriesTheCallersExclusions(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = bodyIgnore(r)
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
			t.Errorf("provider.ignore = %q, missing %q - the standing exclusion does not bind routing", got, want)
		}
	}
}

// No exclusions and no failures means no key at all: an empty exclusion list must not
// become an empty provider.ignore (or header) the broker has to interpret.
func TestChatTurnsSendsNoExclusionHeaderWhenThereIsNothingToExclude(t *testing.T) {
	seen := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hdr := r.Header["X-Roger-Exclude-Nodes"]
		seen = hdr || bodyIgnore(r) != "" || strings.Contains(readBody(r), `"ignore"`)
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
		got = bodyIgnore(r)
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
		t.Errorf("provider.ignore = %q, want exactly node-a", got)
	}
}

// readBody returns the request body (re-readable by the handler's other helpers).
func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	return string(b)
}

// bodyIgnore renders provider.ignore from a recorded request body as the comma-joined list.
func bodyIgnore(r *http.Request) string {
	var m struct {
		Provider struct {
			Ignore []string `json:"ignore"`
		} `json:"provider"`
	}
	_ = json.Unmarshal([]byte(readBody(r)), &m)
	return strings.Join(m.Provider.Ignore, ",")
}
