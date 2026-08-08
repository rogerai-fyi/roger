package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `roger bands` - the CLI half of private-band management.
//
// THE GAP: `roger share --private` MINTS a band from the CLI, but there was no CLI verb to
// see, move or revoke one afterwards. An operator who minted from a terminal had no way to
// find out what they held, and the broker's own quota refusal told them to "revoke an
// existing band first" - an action the CLI could not perform.
// Spec: features/sharing/band_management.feature.

// bandsBroker stands up a broker stub covering the three band endpoints, recording what
// the CLI actually sent.
func bandsBroker(t *testing.T, bands string) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/bands":
			_, _ = w.Write([]byte(bands))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"ok":true,"revoked":true}`))
		case r.Method == http.MethodPatch:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			calls = append(calls, "node_id="+strings_(body["node_id"]))
			_, _ = w.Write([]byte(`{"ok":true,"moved":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func strings_(v any) string { s, _ := v.(string); return s }

const oneBand = `{"bands":[{"id":"band_1","display":"145.225 MHz · ••••-••••",
	"node_id":"roggentoo-gemma-4-31b","status":"active","created_at":1000}]}`

func TestBandsListPrintsWhatYouHoldAndWhereItLives(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, _ := bandsBroker(t, oneBand)
	cfg := config{Broker: srv.URL, User: "u_gh_1"}

	out := captureStdout(t, func() {
		if err := cmdBands(cfg, []string{"list"}); err != nil {
			t.Fatalf("bands list: %v", err)
		}
	})

	// The node id is the fact an operator cannot get anywhere else: which model, and which
	// machine, is holding their one free slot.
	if !strings.Contains(out, "roggentoo-gemma-4-31b") {
		t.Errorf("the list must say which model the band is on, got:\n%s", out)
	}
	if !strings.Contains(out, "145.225 MHz") {
		t.Errorf("the list must show the masked display, got:\n%s", out)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("the list must show status, got:\n%s", out)
	}
	// It must never print a secret - the code was shown once at mint and never stored.
	for _, leak := range []string{"code_hash", "8F3K"} {
		if strings.Contains(out, leak) {
			t.Errorf("the list leaked %q", leak)
		}
	}
}

// An empty list must say the ONE thing that gets you a band, not just "none".
func TestBandsListEmptySaysHowToMintOne(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, _ := bandsBroker(t, `{"bands":[]}`)
	cfg := config{Broker: srv.URL, User: "u_gh_1"}

	out := captureStdout(t, func() {
		if err := cmdBands(cfg, []string{"list"}); err != nil {
			t.Fatalf("bands list: %v", err)
		}
	})
	if !strings.Contains(out, "--private") {
		t.Errorf("an empty list should name the command that mints one, got:\n%s", out)
	}
}

// Revoke is irreversible, so the CLI must require the band id explicitly - never revoke
// "the only one" implicitly.
func TestBandsRevokeNeedsAnExplicitID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, calls := bandsBroker(t, oneBand)
	cfg := config{Broker: srv.URL, User: "u_gh_1"}

	if err := cmdBands(cfg, []string{"revoke"}); err == nil {
		t.Error("revoke with no id must error rather than guess which band to burn")
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "DELETE") {
			t.Fatal("a revoke was sent despite no band id being given")
		}
	}
}

func TestBandsRevokeCallsTheBroker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, calls := bandsBroker(t, oneBand)
	cfg := config{Broker: srv.URL, User: "u_gh_1"}

	if err := cmdBands(cfg, []string{"revoke", "band_1"}); err != nil {
		t.Fatalf("bands revoke: %v", err)
	}
	if !containsCall(*calls, "DELETE /bands/band_1") {
		t.Errorf("calls = %v, want DELETE /bands/band_1", *calls)
	}
}

// Move is the one that keeps the code alive, so it must be reachable from the CLI too.
func TestBandsMoveSendsTheStationScopedNodeID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, calls := bandsBroker(t, oneBand)
	cfg := config{Broker: srv.URL, User: "u_gh_1", Station: "eager-puma-54"}

	if err := cmdBands(cfg, []string{"move", "band_1", "qwen3-vl-8b"}); err != nil {
		t.Fatalf("bands move: %v", err)
	}
	if !containsCall(*calls, "PATCH /bands/band_1") {
		t.Errorf("calls = %v, want PATCH /bands/band_1", *calls)
	}
	// It must send "<station>-<model>", the id the share path registers - not a bare model.
	if !containsCall(*calls, "node_id=eager-puma-54-qwen3-vl-8b") {
		t.Errorf("calls = %v, want the station-scoped node id", *calls)
	}
}

func TestBandsMoveNeedsBothArguments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, _ := bandsBroker(t, oneBand)
	cfg := config{Broker: srv.URL, User: "u_gh_1", Station: "s"}

	if err := cmdBands(cfg, []string{"move"}); err == nil {
		t.Error("move with no args must error")
	}
	if err := cmdBands(cfg, []string{"move", "band_1"}); err == nil {
		t.Error("move with no destination model must error")
	}
}

// Usage and unknown-subcommand behaviour, matching `roger grant`.
func TestBandsUsageAndUnknown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: "https://b", User: "u"}
	if err := cmdBands(cfg, nil); err != nil {
		t.Errorf("cmdBands(nil) = %v, want nil (usage)", err)
	}
	if err := cmdBands(cfg, []string{"bogus"}); err == nil {
		t.Error("an unknown bands subcommand should error")
	}
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// The paths that only run when something goes wrong. They are the ones an operator meets on
// their worst day, so a broker refusal must reach them intact rather than being swallowed
// or reworded into something they cannot act on.

func TestBandsHelpPrintsUsage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: "https://b", User: "u"}
	for _, arg := range []string{"help", "--help", "-h"} {
		out := captureStdout(t, func() {
			if err := cmdBands(cfg, []string{arg}); err != nil {
				t.Fatalf("bands %s: %v", arg, err)
			}
		})
		// The usage must name how a band is CREATED, because there is no `bands create` -
		// a band is only born by putting a model on air privately.
		if !strings.Contains(out, "--private") {
			t.Errorf("bands %s should name `share --private`, got:\n%s", arg, out)
		}
		if !strings.Contains(out, "move") || !strings.Contains(out, "revoke") {
			t.Errorf("bands %s should list move and revoke, got:\n%s", arg, out)
		}
	}
}

// A broker refusal must surface verbatim. The 409 the operator is most likely to hit says
// the destination already carries a band - reworded or swallowed, they cannot act on it.
func TestBandsMoveSurfacesTheBrokerRefusal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const refusal = "that model already carries its own private band - move or revoke that one first"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"message":"` + refusal + `"}}`))
	}))
	defer srv.Close()
	cfg := config{Broker: srv.URL, User: "u_gh_1", Station: "eager-puma-54"}

	err := cmdBands(cfg, []string{"move", "band_1", "qwen3-vl-8b"})
	if err == nil {
		t.Fatal("a 409 must reach the operator as an error")
	}
	if err.Error() != refusal {
		t.Errorf("error = %q,\nwant the broker's sentence %q", err.Error(), refusal)
	}
}

func TestBandsRevokeSurfacesTheBrokerRefusal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no such band"}}`))
	}))
	defer srv.Close()
	cfg := config{Broker: srv.URL, User: "u_gh_1"}

	err := cmdBands(cfg, []string{"revoke", "band_gone"})
	if err == nil || err.Error() != "no such band" {
		t.Errorf("err = %v, want the bare sentence \"no such band\"", err)
	}
}

func TestBandsListSurfacesTheBrokerRefusal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"managing private bands requires a GitHub-linked owner - run ` + "`roger login`" + `"}}`))
	}))
	defer srv.Close()
	cfg := config{Broker: srv.URL, User: "u_gh_1"}

	err := cmdBands(cfg, []string{"list"})
	if err == nil {
		t.Fatal("a 403 must reach the operator as an error, never an empty list")
	}
	// The remedy must survive - this is the same failure that made the WEBSITE render a
	// confident "No private bands yet" to every owner.
	if !strings.Contains(err.Error(), "roger login") {
		t.Errorf("the refusal lost its remedy: %q", err)
	}
}

// A move needs a station callsign to build "<station>-<model>". Without one it must refuse
// rather than send a bare model id the broker would bind to a node nothing registers.
func TestBandsMoveWithoutAStationRefuses(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, calls := bandsBroker(t, oneBand)
	cfg := config{Broker: srv.URL, User: "u_gh_1"} // no Station

	err := cmdBands(cfg, []string{"move", "band_1", "qwen3-vl-8b"})
	if err == nil {
		t.Fatal("a move with no station callsign must refuse")
	}
	if !strings.Contains(err.Error(), "station") {
		t.Errorf("the refusal should name the missing callsign, got %q", err)
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "PATCH") {
			t.Fatal("a move was sent despite having no station to scope the node id with")
		}
	}
}

// A band with no node bound renders a placeholder rather than an empty column.
func TestBandsListRendersAnUnboundBand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, _ := bandsBroker(t, `{"bands":[{"id":"band_x","display":"150 MHz","status":"active","node_id":""}]}`)
	cfg := config{Broker: srv.URL, User: "u_gh_1"}

	out := captureStdout(t, func() {
		if err := cmdBands(cfg, []string{"list"}); err != nil {
			t.Fatalf("bands list: %v", err)
		}
	})
	if !strings.Contains(out, "band_x") {
		t.Errorf("the band is missing from the list:\n%s", out)
	}
}
