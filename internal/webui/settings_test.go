package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rogerai.fm/roger/v5/internal/node"
)

// THE FULLER SURFACE. Per-band spend caps and private-band management were TUI-only, so an
// operator working in the browser could neither see the limits bounding their spend nor
// manage a band once it existed.

// limitServer returns a console wired to an in-memory limit store, plus the store, so a
// test can assert that a POST reached the SAME place a terminal edit would.
func limitServer(t *testing.T) (*httptest.Server, *Server, map[string]SpendLimit) {
	t.Helper()
	store := map[string]SpendLimit{"m1": {MaxOut: 2, MinTPS: 5}}
	s := New(testCtrl(), Options{
		ReadLimits: func() map[string]SpendLimit { return store },
		WriteLimit: func(model string, l SpendLimit) { store[model] = l },
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s, store
}

func TestLimitsListsEveryBandIncludingUnset(t *testing.T) {
	srv, s, _ := limitServer(t)
	resp, err := http.Get(srv.URL + "/api/limits?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Limits []limitRow `json:"limits"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)

	got := map[string]limitRow{}
	for _, r := range out.Limits {
		got[r.Model] = r
	}
	// The one with a cap, and the ones the node serves that have none - losing sight of a
	// band is how an unexplained refusal happens later.
	if got["m1"].MaxOut != 2 {
		t.Errorf("m1's cap did not survive the round trip: %+v", got["m1"])
	}
	if _, ok := got["m2"]; !ok {
		t.Errorf("a band this node serves is missing from the spend table: %+v", out.Limits)
	}
}

// A POST must reach the SAME store the terminal edits. Two stores would let the browser
// and the TUI disagree about what the operator is willing to pay.
func TestLimitPostWritesTheSharedStore(t *testing.T) {
	srv, s, store := limitServer(t)
	body := strings.NewReader(`{"model":"m2","max_out":1.5,"min_tps":9}`)
	resp, err := http.Post(srv.URL+"/api/limits?t="+s.Token(), "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/limits = %d, want 200", resp.StatusCode)
	}
	if store["m2"].MaxOut != 1.5 || store["m2"].MinTPS != 9 {
		t.Errorf("the console wrote somewhere else: %+v", store["m2"])
	}
}

// A NEGATIVE cap is refused rather than clamped. It is not a smaller cap - it is a value
// the spend path would have to interpret, and silently rewriting what the operator typed
// is worse than telling them.
func TestLimitPostRefusesANegativeCap(t *testing.T) {
	srv, s, store := limitServer(t)
	body := strings.NewReader(`{"model":"m2","max_out":-1}`)
	resp, err := http.Post(srv.URL+"/api/limits?t="+s.Token(), "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a negative cap = %d, want 400", resp.StatusCode)
	}
	if _, ok := store["m2"]; ok {
		t.Error("a refused cap was written anyway")
	}
}

// With no store wired the table reports itself UNAVAILABLE rather than empty: an empty
// table reads as "you have no caps", which is a different and wrong claim.
func TestLimitsSayUnavailableRatherThanEmpty(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/limits?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["configured"] != false {
		t.Errorf("a node with no limit store must say so: %+v", out)
	}
}

// BANDS. Without a broker the list is empty-and-unconfigured, never an error page: the
// console is usable on a node that has never logged in.
func TestBandsWithoutABrokerAreUnconfiguredNotBroken(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/bands?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/bands = %d, want 200", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["configured"] != false {
		t.Errorf("want configured:false, got %+v", out)
	}
}

// Every band mutation needs an id, and an unknown action is a 404 rather than falling
// through to something destructive.
func TestBandActionsValidate(t *testing.T) {
	s := New(testCtrl(), Options{Broker: "http://127.0.0.1:0"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	post := func(path, body string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path+"?t="+s.Token(), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("/api/bands/rotate", `{}`); got != http.StatusBadRequest {
		t.Errorf("rotate with no id = %d, want 400", got)
	}
	if got := post("/api/bands/wat", `{"id":"band_1"}`); got != http.StatusNotFound {
		t.Errorf("an unknown band action = %d, want 404 (it must not fall through)", got)
	}
}

// The node-id join must not split on "-": a station callsign can contain hyphens, and a
// wrong guess would take the WRONG model off air on a revoke.
func TestModelForNodeSurvivesAHyphenatedStation(t *testing.T) {
	c := node.New(node.Config{Station: "eager-puma-54"})
	c.SetRows([]node.ShareRow{{Model: "grok-4.6", Ctx: 8192}})
	s := New(c, Options{})
	if got := s.modelForNode("eager-puma-54-grok-4-6"); got != "grok-4.6" {
		t.Errorf("modelForNode = %q, want grok-4.6", got)
	}
	if got := s.modelForNode("other-box-llama-3"); got != "" {
		t.Errorf("a band on another machine resolved to %q", got)
	}
}

// THE CONSOLE LANDS ON CHAT (founder: "lets make sure we start the webui on the chat").
// It used to open on SHARE - the provider surface - which answered "what am I
// broadcasting?" to someone who had come to talk to a model.
func TestTheConsoleLandsOnChat(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// io.ReadAll, not a single Read: one Read returns one chunk, and the panels live
	// past the first one - the assertions below would pass on a truncated page.
	raw, err := io.ReadAll(resp.Body)
	if err != nil || len(raw) == 0 {
		t.Fatalf("could not read the console page: %v", err)
	}
	page := string(raw)
	// The CHAT panel must not be hidden on first paint, and SHARE must be - otherwise the
	// browser shows the wrong tab for a frame before the script corrects it.
	if strings.Contains(page, `id="panel-chat" role="tabpanel" hidden`) {
		t.Error("the CHAT panel is hidden on first paint - the console still lands on SHARE")
	}
	if !strings.Contains(page, `id="panel-share" role="tabpanel" hidden`) {
		t.Error("the SHARE panel is visible on first paint alongside CHAT")
	}
	if !strings.Contains(page, `data-tab="settings"`) {
		t.Error("the SETTINGS tab is missing from the console shell")
	}
}
