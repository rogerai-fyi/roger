package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"rogerai.fm/roger/v6/internal/detect"
)

// fakeBroker is a minimal broker that lets agent.Start succeed (register ok) and stay
// on air (heartbeat ok), so a Controller can really start/stop sessions in a test.
func fakeBroker(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func newCtrl(t *testing.T, cfg Config) *Controller {
	if cfg.Broker == "" {
		cfg.Broker = fakeBroker(t)
	}
	if cfg.Station == "" {
		cfg.Station = "amber-fox"
	}
	c := New(cfg)
	c.SetRows([]ShareRow{
		{Model: "free-1", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
		{Model: "free-2", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
		{Model: "paid", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
	})
	c.SetPricing("paid", Pricing{Out: 2})
	return c
}

func TestToggleOnAirStartsAndStops(t *testing.T) {
	c := newCtrl(t, Config{})
	res := c.ToggleOnAir("free-1")
	if res.Err != nil || res.WentOff || res.AtLimit || res.LoginNeeded {
		t.Fatalf("first toggle should go on air cleanly, got %+v", res)
	}
	if c.OnAirCount() != 1 {
		t.Fatalf("on-air count = %d, want 1", c.OnAirCount())
	}
	if _, on := c.Headline(); !on {
		t.Fatal("headline should report on air")
	}
	off := c.ToggleOnAir("free-1")
	if !off.WentOff {
		t.Fatalf("second toggle should go off air, got %+v", off)
	}
	if c.OnAirCount() != 0 {
		t.Fatalf("on-air count after off = %d, want 0", c.OnAirCount())
	}
}

func TestPricedShareLoginGated(t *testing.T) {
	c := newCtrl(t, Config{})
	if res := c.ToggleOnAir("paid"); !res.LoginNeeded {
		t.Fatalf("priced share without login should be gated, got %+v", res)
	}
	if c.OnAirCount() != 0 {
		t.Fatal("a gated priced share must not go on air")
	}
	c.SetLoggedIn(true)
	res := c.ToggleOnAir("paid")
	if res.Err != nil || res.LoginNeeded || !res.Priced || res.PriceOut != 2 {
		t.Fatalf("logged-in priced share should start priced, got %+v", res)
	}
}

func TestSoftMaxOnAirCap(t *testing.T) {
	c := newCtrl(t, Config{MaxOnAir: 1})
	if res := c.ToggleOnAir("free-1"); res.Err != nil {
		t.Fatalf("first on air: %+v", res)
	}
	if res := c.ToggleOnAir("free-2"); !res.AtLimit {
		t.Fatalf("second on air at cap 1 should be blocked, got %+v", res)
	}
	if c.OnAirCount() != 1 {
		t.Fatalf("on-air count = %d, want 1 (cap held)", c.OnAirCount())
	}
}

func TestPickUpstreamKey(t *testing.T) {
	const headUp, headKey = "http://127.0.0.1:8080/v1/chat/completions", "sk-headline"
	// A row with its OWN key always uses it.
	if got := pickUpstreamKey("http://127.0.0.1:9999/v1/chat/completions", "sk-own", headUp, headKey); got != "sk-own" {
		t.Errorf("row with own key = %q, want sk-own", got)
	}
	// A keyless row on the headline upstream inherits the headline key.
	if got := pickUpstreamKey(headUp, "", headUp, headKey); got != headKey {
		t.Errorf("keyless row on headline upstream = %q, want the headline key", got)
	}
	// A keyless row on a DIFFERENT upstream gets NO key — the headline bearer is not sprayed.
	if got := pickUpstreamKey("http://127.0.0.1:9999/v1/chat/completions", "", headUp, headKey); got != "" {
		t.Errorf("keyless row on a different upstream = %q, want empty (no spray)", got)
	}
	// Equivalent spellings of the same endpoint still count as the same (normalized).
	if got := pickUpstreamKey("http://127.0.0.1:8080/v1", "", headUp, headKey); got != headKey {
		t.Errorf("keyless row on the same endpoint (different spelling) = %q, want the headline key", got)
	}
}

func TestDefaultMaxOnAir(t *testing.T) {
	c := newCtrl(t, Config{})
	if c.MaxOnAir() != DefaultMaxOnAir {
		t.Fatalf("default cap = %d, want %d", c.MaxOnAir(), DefaultMaxOnAir)
	}
}

func TestSnapshotRedactsUpstreamKey(t *testing.T) {
	const secret = "sk-super-secret-key"
	c := newCtrl(t, Config{Upstream: "http://127.0.0.1:1234/v1", UpstreamKey: secret})
	snap := c.Snapshot()
	if snap.Upstream == "" {
		t.Fatal("snapshot should carry the (non-secret) upstream endpoint")
	}
	blob, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatalf("snapshot JSON leaked the upstream key:\n%s", blob)
	}
	// The key is still reachable in-process (the agent needs it to authenticate).
	if c.UpstreamKey() != secret {
		t.Fatalf("UpstreamKey() = %q, want the live key", c.UpstreamKey())
	}
}

func TestSetPricingPersists(t *testing.T) {
	var got *Pricing
	var gotModel string
	c := newCtrl(t, Config{Hooks: Hooks{SavePrice: func(m string, p Pricing) { gotModel = m; cp := p; got = &cp }}})
	c.SetPricing("free-1", Pricing{In: 1, Out: 3})
	if got == nil || got.Out != 3 || gotModel != "free-1" {
		t.Fatalf("SavePrice hook = (%q,%+v), want free-1 out 3", gotModel, got)
	}
	if p := c.PricingFor("free-1"); p.Out != 3 {
		t.Fatalf("PricingFor = %+v, want out 3", p)
	}
}

func TestRenamePersists(t *testing.T) {
	var got string
	c := newCtrl(t, Config{Hooks: Hooks{SaveStation: func(s string) { got = s }}})
	c.Rename("violet-owl")
	if got != "violet-owl" || c.Station() != "violet-owl" {
		t.Fatalf("rename: hook=%q station=%q, want violet-owl", got, c.Station())
	}
}

func TestLoadRowsFlattensAndPersistsUpstream(t *testing.T) {
	var savedUp, savedKey string
	c := New(Config{Station: "amber-fox", Hooks: Hooks{SaveUpstream: func(u, k string) { savedUp, savedKey = u, k }}})
	c.LoadRows([]detect.Found{{
		BaseURL: "http://127.0.0.1:8081/v1", Chat: "http://127.0.0.1:8081/v1/chat/completions",
		Key: "sk-1", Models: []string{"a", "b", "a"}, // dup a -> de-duped
	}})
	if rows := c.Rows(); len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (de-duped)", len(rows))
	}
	if savedUp != "http://127.0.0.1:8081/v1" || savedKey != "sk-1" {
		t.Fatalf("SaveUpstream = (%q,%q), want the verified endpoint+key", savedUp, savedKey)
	}
	// Re-loading the same endpoint is a no-op (no rewrite).
	savedUp = ""
	c.LoadRows([]detect.Found{{BaseURL: "http://127.0.0.1:8081/v1", Chat: "http://127.0.0.1:8081/v1/chat/completions", Key: "sk-1", Models: []string{"a"}}})
	if savedUp != "" {
		t.Fatal("re-loading the already-saved endpoint should not rewrite config")
	}
}

// TestLoadRowsNoPersistDoesNotWrite: the passive launch scan populates rows but must NOT
// rewrite saved config; an explicit LoadRows still persists a newly-verified endpoint.
func TestLoadRowsNoPersistDoesNotWrite(t *testing.T) {
	saved := 0
	c := New(Config{Station: "amber-fox", Hooks: Hooks{SaveUpstream: func(u, k string) { saved++ }}})
	found := []detect.Found{{
		BaseURL: "http://127.0.0.1:9001/v1", Chat: "http://127.0.0.1:9001/v1/chat/completions",
		Models: []string{"m"},
	}}
	c.LoadRowsNoPersist(found)
	if saved != 0 {
		t.Fatalf("LoadRowsNoPersist must not persist; SaveUpstream called %d times", saved)
	}
	if len(c.Rows()) != 1 {
		t.Fatalf("LoadRowsNoPersist should still populate rows; got %d", len(c.Rows()))
	}
	// An explicit re-detect DOES persist the (still-unsaved) endpoint, exactly once.
	c.LoadRows(found)
	if saved != 1 {
		t.Fatalf("LoadRows should persist a new endpoint once; got %d", saved)
	}
}

// TestDetectFallsBackToSavedUpstream: with no pasted URL, Detect probes the saved/verified
// upstream first — so a custom/non-default endpoint (the one the CLI seeds) is found instead
// of the SHARE tab staying empty after a bare port scan.
func TestDetectFallsBackToSavedUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "saved-model"}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(Config{Station: "amber-fox", Upstream: srv.URL}) // the saved upstream
	found, _ := c.Detect("", "")                              // re-detect with no pasted URL
	got := false
	for _, f := range found {
		for _, m := range f.Models {
			if m == "saved-model" {
				got = true
			}
		}
	}
	if !got {
		t.Fatalf("Detect() should probe the saved upstream and find its model; got %+v", found)
	}
}

// TestConcurrentToggle exercises the lock: two front-ends (the TUI goroutine and the
// web server) toggling the same node concurrently must never race. Run with -race.
func TestConcurrentToggle(t *testing.T) {
	c := newCtrl(t, Config{MaxOnAir: 10})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.ToggleOnAir("free-1")
			c.Snapshot()
			c.ToggleOnAir("free-2")
			c.OnAirCount()
		}()
	}
	wg.Wait()
	c.StopAll()
	if c.OnAirCount() != 0 {
		t.Fatalf("after StopAll, on air = %d, want 0", c.OnAirCount())
	}
}

// ── THE ZOMBIE BAND ──────────────────────────────────────────────────────────
// Revoking a band deletes it broker-side, but the node stayed REGISTERED PRIVATE with
// no band behind it: hidden from the market and reachable by nobody. Worse, the private
// flag survived - so the operator's next `h`, the obvious way to mint a fresh code,
// computed goPrivate = !true = FALSE and re-registered the model PUBLICLY. The only
// documented way to rotate a code took the model through the open market.
func TestBandRevokedClearsThePrivateFlag(t *testing.T) {
	c := New(Config{Station: "amber-fox", Upstream: "http://127.0.0.1:1/v1"})
	c.SetRows([]ShareRow{{Model: "grok-4.6", Ctx: 8192}})
	c.private["grok-4.6"] = true

	if !c.BandRevoked("grok-4.6") {
		t.Fatal("revoking a band on a flagged model must reconcile it")
	}
	if c.private["grok-4.6"] {
		t.Error("the private flag must be cleared - leaving it set is what makes the next toggle PUBLISH")
	}
	if c.sessions["grok-4.6"] != nil {
		t.Error("the model must be off air: its only route in was just revoked")
	}
}

// A band pointing at another machine's model is not ours to reconcile.
func TestBandRevokedIgnoresAModelWeDoNotHave(t *testing.T) {
	c := New(Config{Station: "amber-fox", Upstream: "http://127.0.0.1:1/v1"})
	c.SetRows([]ShareRow{{Model: "grok-4.6", Ctx: 8192}})
	if c.BandRevoked("some-other-machines-model") {
		t.Error("a model we do not have must report nothing was reconciled")
	}
}

// Nothing to reconcile reports false, so the caller does not claim an action it did not
// take.
func TestBandRevokedOnAnIdleModelDoesNothing(t *testing.T) {
	c := New(Config{Station: "amber-fox", Upstream: "http://127.0.0.1:1/v1"})
	c.SetRows([]ShareRow{{Model: "grok-4.6", Ctx: 8192}})
	if c.BandRevoked("grok-4.6") {
		t.Error("a model that is neither on air nor flagged private needs no reconciliation")
	}
}

// ON AIR MUST RESUME AT THE ROW'S RECORDED VISIBILITY.
//
// ToggleOnAir passed `false` unconditionally, so a model on a PRIVATE band that was taken
// off air and put back on with the same key came back on the OPEN MARKET - while
// private[model] stayed true, so every surface went on rendering it as PRIVATE. An
// operator who hid a model, toggled it off and on, and read their own SHARE row had no way
// to learn they were now broadcasting to everyone. Same family as the zombie band: a path
// that silently publishes something the operator deliberately hid.
func TestOnAirResumesPrivateNotPublic(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetLoggedIn(true)

	if res := c.TogglePrivate("free-1"); !res.NowPrivate {
		t.Fatalf("precondition: the model did not go private (%+v)", res)
	}
	if !c.Private()["free-1"] {
		t.Fatal("precondition: the private flag was not recorded")
	}

	// Off, then back on with the SAME key an operator uses in SHARE.
	if off := c.ToggleOnAir("free-1"); !off.WentOff {
		t.Fatalf("the model did not go off air (%+v)", off)
	}
	back := c.ToggleOnAir("free-1")
	if back.Err != nil {
		t.Fatalf("bringing it back on air failed: %v", back.Err)
	}
	if !back.NowPrivate {
		t.Error("a private model came back on air PUBLICLY - the hidden model is now on the open market")
	}
	if !c.Private()["free-1"] {
		t.Error("the private flag was lost across an off/on cycle")
	}
}

// NEGATIVE HALF: a model that was never private must still come back PUBLIC, or the fix
// above could be satisfied by making every start private.
func TestOnAirResumesPublicForAPublicRow(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetLoggedIn(true)

	if on := c.ToggleOnAir("free-2"); on.Err != nil {
		t.Fatalf("first start failed: %v", on.Err)
	}
	if c.Private()["free-2"] {
		t.Fatal("a plain share was recorded as private")
	}
	c.ToggleOnAir("free-2")
	back := c.ToggleOnAir("free-2")
	if back.NowPrivate {
		t.Error("a public share came back on air PRIVATE - it vanished from the market it was listed on")
	}
}

// A private start is login-gated exactly as a priced one is. Refusing is the SAFE failure:
// starting PUBLIC because we could not start private is the leak this guards.
func TestOnAirRefusesAPrivateRowWhenLoggedOut(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetLoggedIn(true)
	c.TogglePrivate("free-1")
	c.ToggleOnAir("free-1") // off air
	c.Logout()              // the real clear: SetLoggedIn(false) is a deliberate no-op

	res := c.ToggleOnAir("free-1")
	if !res.LoginNeeded {
		t.Fatalf("a private row started while logged out (%+v)", res)
	}
	if res.NowPrivate {
		t.Error("a refused start reported itself as on air")
	}
}

// A nil controller is the TUI's pre-share state, and the band card reads pricing while
// rendering. Before the guard this panicked on a mutex lock — the card crashed the app
// for anyone who opened it before setting up a share.
func TestPricingForOnNilControllerIsFreeNotAPanic(t *testing.T) {
	var c *Controller
	if got := c.PricingFor("anything"); got.In != 0 || got.Out != 0 {
		t.Fatalf("nil controller should price free, got %+v", got)
	}
}
