package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// setupModelOnPaste returns a guided-setup model parked on the paste row with url filled.
func setupModelOnPaste(url string) *model {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 30
	m.mode = modeShareSetup
	m.setupCursor = len(setupOptions) - 1
	m.setupPaste = url
	return &m
}

// TestShareSetupPasteReachable: pasting a URL that serves /v1/models verifies it and lands
// the provider table with the served model loaded.
func TestShareSetupPasteReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-oss-20b","max_model_len":32768}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := setupModelOnPaste(srv.URL)
	out, _ := m.onShareSetupKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := asModel(out)
	if got.mode != modeShare {
		t.Fatalf("a reachable paste should land the share table, mode=%v", got.mode)
	}
	if len(got.shareRows) == 0 || got.shareRows[0].model != "gpt-oss-20b" {
		t.Fatalf("the served model should be loaded, rows=%+v", got.shareRows)
	}
	if !strings.Contains(stripANSI(got.status), "verified") {
		t.Errorf("status should report verified, got %q", stripANSI(got.status))
	}
}

// TestShareSetupPasteNeedsKey: a 401 server flips the wizard into the key-entry step; the
// next enter re-verifies WITH the typed key and succeeds.
func TestShareSetupPasteNeedsKey(t *testing.T) {
	// Avoid the letters the wizard reserves for navigation (s/k/j/r/esc) so every char
	// of the key reaches the default typing branch instead of a nav case.
	const goodKey = "pw-1234"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/models") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+goodKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-coder-30b"}]}`))
	}))
	defer srv.Close()

	m := setupModelOnPaste(srv.URL)
	// First enter (no key) -> key-entry step.
	step, _ := m.onShareSetupKey(tea.KeyMsg{Type: tea.KeyEnter})
	sm := asModel(step)
	if !sm.setupAwaitKey {
		t.Fatalf("a 401 server should flip into the key-entry step")
	}
	// Type the key char by char, with one backspace to exercise the key-step backspace.
	km := &sm
	for _, r := range goodKey + "x" {
		km.onShareSetupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	km.onShareSetupKey(tea.KeyMsg{Type: tea.KeyBackspace}) // trims the stray 'x'
	if km.setupKey != goodKey {
		t.Fatalf("typed key should be %q, got %q", goodKey, km.setupKey)
	}
	// Second enter re-verifies with the key -> table.
	done, _ := km.onShareSetupKey(tea.KeyMsg{Type: tea.KeyEnter})
	dm := asModel(done)
	if dm.mode != modeShare || len(dm.shareRows) == 0 {
		t.Fatalf("a keyed re-verify should land the table, mode=%v rows=%d", dm.mode, len(dm.shareRows))
	}
}

// TestShareSetupPasteUnreachable: a server with no /v1/models (404) yields the inline
// "no OpenAI-compatible server" error and stays on the wizard.
func TestShareSetupPasteUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := setupModelOnPaste(srv.URL)
	out, _ := m.onShareSetupKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := asModel(out)
	if !strings.Contains(got.setupErr, "no OpenAI-compatible server") {
		t.Errorf("an unreachable paste should set the no-server error, got %q", got.setupErr)
	}
	if got.mode != modeShareSetup {
		t.Errorf("an unreachable paste should stay on the wizard, mode=%v", got.mode)
	}
}

// TestShareSetupRescanKey: the [r] key drops into the loading table and fires a detection
// command, marking the scan as a re-scan.
func TestShareSetupRescanKey(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 30
	m.mode = modeShareSetup
	out, cmd := m.onShareSetupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	got := asModel(out)
	if got.mode != modeShare || !got.shareLoading || !got.shareRescan {
		t.Fatalf("r should enter the loading re-scan, mode=%v loading=%v rescan=%v", got.mode, got.shareLoading, got.shareRescan)
	}
	if cmd == nil {
		t.Error("r should fire a detection command")
	}
}

// TestShareSetupKeyStepEsc: typed key chars accumulate in the key step, and leaving the
// paste row via up clears a half-typed key so it can't leak onto another URL.
func TestShareSetupKeyStepUpClearsKey(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 30
	m.mode = modeShareSetup
	m.setupCursor = len(setupOptions) - 1
	m.setupAwaitKey = true
	m.setupKey = "sk-partial"
	out, _ := m.onShareSetupKey(tea.KeyMsg{Type: tea.KeyUp})
	got := asModel(out)
	if got.setupKey != "" || got.setupAwaitKey {
		t.Errorf("moving off the paste row should clear the half-typed key, key=%q await=%v", got.setupKey, got.setupAwaitKey)
	}
}
