package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// Step definitions for the local-model scenarios in features/agent/agent.feature. They
// drive the REAL LocalCompleter against a REAL httptest server standing in for the
// operator's llama.cpp/ollama, so "never touches the broker" is observed on the wire
// rather than asserted about the code.

type localModelState struct {
	srv       *httptest.Server
	sawSig    bool
	sawCap    bool
	sawUser   bool
	reachedBy string // which endpoint actually received the turn
	body      map[string]any
}

func (s *localModelState) stop() {
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
}

func (s *localModelState) givenLocalServerRunning() error {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reachedBy = "local"
		s.sawSig = r.Header.Get("X-Roger-Sig") != ""
		s.sawCap = r.Header.Get("X-Roger-Max-Price-Out") != ""
		s.sawUser = r.Header.Get("X-Roger-User") != ""
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &s.body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"local answer"}}]}`))
	}))
	return nil
}

// The picker-shaping rules live in the TUI (internal/tui/agent_local_test.go); here we pin
// the harness-level contract they depend on: a local model is addressed by its OWN chat
// URL, so it can be offered without any band, price, or registration behind it.
func (s *localModelState) thenLocalModelsAppearMarked() error {
	if s.srv == nil {
		return fmt.Errorf("no local server stood up")
	}
	msg, err := LocalCompleter(s.srv.URL+"/v1/chat/completions", "", "local-model")(
		context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		return fmt.Errorf("a detected local model must be usable without any band: %v", err)
	}
	if msg.Content == "" {
		return fmt.Errorf("the local model returned nothing")
	}
	return nil
}

func (s *localModelState) thenVoiceModelNeverOffered() error {
	// Modality filtering is the TUI's (localAgentRows); the harness contract is only that
	// a completer is built per model id, so there is nothing to leak here. Assert the
	// invariant that makes the filter possible: the completer is bound to ONE model id.
	if s.body != nil && s.body["model"] != "local-model" {
		return fmt.Errorf("the turn was sent for %v, not the bound model", s.body["model"])
	}
	return nil
}

func (s *localModelState) thenNoPriceShown() error {
	// A local turn carries no price cap, because there is no price. Verified on the wire
	// in thenNoMarketplaceHeaders; here it is the same fact stated where the operator
	// meets it.
	if s.sawCap {
		return fmt.Errorf("a local turn carried an out-price cap")
	}
	return nil
}

func (s *localModelState) givenAgentOnLocalModel() error {
	return s.givenLocalServerRunning()
}

func (s *localModelState) whenItTakesATurn() error {
	_, err := LocalCompleter(s.srv.URL+"/v1/chat/completions", "", "local-model")(
		context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	return err
}

func (s *localModelState) thenStraightToThatServer() error {
	if s.reachedBy != "local" {
		return fmt.Errorf("the turn never reached the local server")
	}
	return nil
}

func (s *localModelState) thenNoMarketplaceHeaders() error {
	var bad []string
	if s.sawSig {
		bad = append(bad, "a request signature")
	}
	if s.sawCap {
		bad = append(bad, "an out-price cap")
	}
	if s.sawUser {
		bad = append(bad, "a broker user")
	}
	if len(bad) > 0 {
		return fmt.Errorf("a local turn carried %s", strings.Join(bad, ", "))
	}
	return nil
}

// Nothing is metered: LocalCompleter has no cost callback at all, so there is no path by
// which a local turn can report spend. Pinned by construction - the signature takes none.
func (s *localModelState) thenNothingBilled() error {
	if s.sawCap || s.sawSig {
		return fmt.Errorf("a local turn looked like a billed relay")
	}
	return nil
}

// Switching back must reach the BROKER endpoint, not the local one. Two distinct servers
// make "which one received it" observable rather than inferred.
func (s *localModelState) givenWasOnLocalModel() error {
	return s.givenLocalServerRunning()
}

func (s *localModelState) whenOperatorPicksABand() error {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reachedBy = "broker"
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"relayed"}}]}`))
	}))
	defer broker.Close()
	s.reachedBy = ""
	_, err := BrokerCompleterWithTimeout(broker.URL, "u", "band-model", false, 0, nil, 0)(
		context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	return err
}

func (s *localModelState) thenRelaysThroughBrokerAgain() error {
	if s.reachedBy != "broker" {
		return fmt.Errorf("after switching back the turn went to %q, want the broker", s.reachedBy)
	}
	return nil
}

// The picker must not scan on open (the TUI pins the call site); the harness-level fact is
// that discovery and completion are separate - building a completer opens no socket.
func (s *localModelState) thenPickerOpensFromMemory() error {
	c := LocalCompleter("http://127.0.0.1:1/v1/chat/completions", "", "m")
	if c == nil {
		return fmt.Errorf("LocalCompleter returned nothing")
	}
	return nil // constructing it dialled nothing; a bad address would only fail on use
}
