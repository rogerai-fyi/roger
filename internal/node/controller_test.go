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

// AMENDED 2026-08-23: Detect no longer returns the saved upstream ALONE, so "the result
// contains the saved model" is no longer the whole guarantee - it is now one entry in a
// full-machine scan. What must still hold is that the saved/verified endpoint is SEEDED
// into that scan as a priority candidate, because a custom/keyed endpoint is on none of
// the default ports and would otherwise never be probed at all.
//
// The scan is stubbed rather than run: the property is "the saved endpoint reaches the
// scan", and inferring that from whatever happens to be listening on the developer's box
// would be both slower and weaker.
func TestDetectSeedsTheSavedUpstreamIntoTheScan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "saved-model"}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var seeded []string
	restore := detectFull
	detectFull = func(extra ...string) ([]detect.Found, []string) {
		seeded = extra
		// What the real DetectFull does with a priority candidate: probe it, keep it first.
		f, st := detect.ProbeKey(extra[0], "")
		if st != detect.Reachable {
			return nil, nil
		}
		return []detect.Found{f}, nil
	}
	defer func() { detectFull = restore }()

	c := New(Config{Station: "amber-fox", Upstream: srv.URL}) // the saved upstream
	found, _ := c.Detect("", "")                              // re-detect with no pasted URL
	if len(seeded) == 0 || !strings.Contains(seeded[0], srv.URL) {
		t.Fatalf("the scan was seeded with %v, which does not derive from the saved endpoint %q", seeded, srv.URL)
	}
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

// A saved upstream that is REACHABLE but serves nothing must not short-circuit the scan.
// This was a dead end in the field: the console's saved upstream answered /v1/models with
// an empty list, Detect returned only that server, the SHARE tab said "No models detected
// yet. Try re-detect" - and re-detect took the same short-circuit, so the advice the UI
// gave could never work while a dozen other backends sat listening on the same machine.
func TestDetectDoesNotShortCircuitOnAnEmptyUpstream(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer empty.Close()

	// Stub the machine scan: the property under test is that Detect FALLS THROUGH to it,
	// which is stronger and more honest than inferring it from whatever happens to be
	// listening on the developer's box - and it does not port-scan that box to find out.
	scanned := ""
	restore := detectFull
	detectFull = func(extra ...string) ([]detect.Found, []string) {
		if len(extra) > 0 {
			scanned = extra[0]
		}
		return []detect.Found{{Name: "real", BaseURL: "http://127.0.0.1:8081/v1", Models: []string{"qwen3-vl-8b"}}}, nil
	}
	defer func() { detectFull = restore }()

	c := New(Config{Station: "amber-fox", Upstream: empty.URL})
	found, _ := c.Detect("", "")

	if scanned == "" {
		t.Fatal("Detect stopped at a reachable-but-empty upstream instead of scanning on")
	}
	// Seeded FROM the saved endpoint (normalised to its chat URL), so it still wins
	// de-dup rather than being lost by falling through.
	if !strings.Contains(scanned, empty.URL) {
		t.Errorf("the scan was seeded with %q, which does not derive from the saved endpoint %q", scanned, empty.URL)
	}
	if len(found) != 1 || len(found[0].Models) == 0 {
		t.Fatalf("found = %v, want the server that actually serves models", found)
	}
}

// An empty server must not become the station's upstream even when it answers first.
// Being reachable and being useful are different questions, and only the second one
// should decide what a station is bound to.
func TestEmptyServerNeverBecomesTheUpstream(t *testing.T) {
	c := New(Config{Station: "amber-fox"})
	c.LoadRows([]detect.Found{
		{Name: "empty", BaseURL: "http://127.0.0.1:3000/v1", Chat: "http://127.0.0.1:3000/v1/chat/completions"},
		{Name: "real", BaseURL: "http://127.0.0.1:8081/v1", Chat: "http://127.0.0.1:8081/v1/chat/completions",
			Models: []string{"qwen3-vl-8b"}},
	})
	if got := c.Snapshot().Upstream; !strings.Contains(got, "8081") {
		t.Fatalf("upstream = %q, want the server that actually serves models (:8081)", got)
	}
}

// With nothing serving anywhere, still report where we looked rather than blanking - the
// operator needs to see which endpoint was tried.
func TestAllEmptyStillReportsAnUpstream(t *testing.T) {
	c := New(Config{Station: "amber-fox"})
	c.LoadRows([]detect.Found{
		{Name: "empty", BaseURL: "http://127.0.0.1:3000/v1", Chat: "http://127.0.0.1:3000/v1/chat/completions"},
	})
	if got := c.Snapshot().Upstream; got == "" {
		t.Fatal("upstream blanked when every server was empty; it should name the one tried")
	}
}

// AMENDED 2026-08-23: this used to lock the SHORT-CIRCUIT - "a saved endpoint that DOES
// serve models is returned alone, without a full scan". That behaviour was the bug the
// founder reported: with the saved upstream on :8060 serving exactly one model, the
// console's SHARE tab showed one model while `roger detect` in the next terminal listed
// twenty-seven across twelve servers, and re-detect could never get past it.
//
// Re-anchored to the guarantee the short-circuit was actually protecting, which survives:
// a serving saved endpoint is still IN the result, still first (so loadRows binds the
// station to it), and it no longer costs the rest of the machine to keep it there.
func TestAServingSavedUpstreamStillLeadsButNoLongerHidesTheFleet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "saved-model"}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	scanned := false
	restore := detectFull
	detectFull = func(extra ...string) ([]detect.Found, []string) {
		scanned = true
		f, st := detect.ProbeKey(extra[0], "")
		out := []detect.Found{}
		if st == detect.Reachable {
			out = append(out, f) // the priority candidate leads, as the real scan does
		}
		return append(out, detect.Found{
			Name: "port:8081", BaseURL: "http://127.0.0.1:8081/v1",
			Chat: "http://127.0.0.1:8081/v1/chat/completions", Models: []string{"qwen3-vl-8b"},
		}), nil
	}
	defer func() { detectFull = restore }()

	c := New(Config{Station: "amber-fox", Upstream: srv.URL})
	found, _ := c.Detect("", "")
	if !scanned {
		t.Fatal("a serving saved upstream must no longer short-circuit the machine scan")
	}
	if len(found) != 2 {
		t.Fatalf("found = %+v, want the saved endpoint AND the rest of the machine", found)
	}
	if len(found[0].Models) != 1 || found[0].Models[0] != "saved-model" {
		t.Errorf("the saved endpoint must still lead (loadRows binds the station to it); got %+v", found[0])
	}
	if found[1].Models[0] != "qwen3-vl-8b" {
		t.Errorf("the rest of the fleet must survive; got %+v", found[1])
	}
}

// The keyed endpoint is the reason the short-circuit existed: detect.DetectFull takes URLs
// only (`extra ...string`), so it probes a key-protected endpoint with the ENVIRONMENT's
// keys and never the one the operator pasted. That endpoint therefore comes back 401 - as
// a needKey entry, not a server - and its models are lost.
//
// So the keyed probe is merged into the scan instead of replacing it: the whole fleet AND
// the keyed endpoint, de-duplicated by base URL, with the keyed Found taking the slot
// because it is the only one holding the credential the row needs to go on air.
func TestDetectMergesTheKeyedEndpointIntoTheFullScan(t *testing.T) {
	const key = "sk-paste-me"
	keyed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "walled-model"}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer keyed.Close()
	base := strings.TrimRight(keyed.URL, "/") + "/v1"

	restore := detectFull
	detectFull = func(extra ...string) ([]detect.Found, []string) {
		// What the real scan sees WITHOUT the key: the rest of the machine, plus this
		// endpoint reported as "present but needs a key".
		return []detect.Found{{
			Name: "port:8081", BaseURL: "http://127.0.0.1:8081/v1",
			Chat: "http://127.0.0.1:8081/v1/chat/completions", Models: []string{"qwen3-vl-8b"},
		}}, []string{base}
	}
	defer func() { detectFull = restore }()

	c := New(Config{Station: "amber-fox"})
	found, needKey := c.Detect(keyed.URL, key)

	var walled *detect.Found
	fleet := false
	for i := range found {
		for _, m := range found[i].Models {
			if m == "walled-model" {
				walled = &found[i]
			}
			if m == "qwen3-vl-8b" {
				fleet = true
			}
		}
	}
	if walled == nil {
		t.Fatalf("the keyed endpoint's models were lost; got %+v", found)
	}
	if !fleet {
		t.Errorf("merging the keyed endpoint must not cost the rest of the machine; got %+v", found)
	}
	if walled.Key != key {
		t.Errorf("the merged row must carry the key it was opened with, got %q - without it the row cannot go on air", walled.Key)
	}
	for _, n := range needKey {
		if strings.TrimRight(n, "/") == base {
			t.Errorf("%s was opened with the pasted key; it must not still be reported as needing one", base)
		}
	}
}

// De-duplication is by BASE URL and keeps position: the keyed probe takes the scan's slot
// for the same endpoint rather than appending a twin. A twin would put the same models in
// the catalog twice and - worse - let the KEYLESS copy win firstServing, binding the
// station to an endpoint it has no credential for.
func TestKeyedMergeReplacesTheScanEntryInPlace(t *testing.T) {
	base := "http://127.0.0.1:9999/v1"
	scan := []detect.Found{
		{Name: "port:9999", BaseURL: base, Chat: base + "/chat/completions", Models: []string{"stale"}},
		{Name: "port:8081", BaseURL: "http://127.0.0.1:8081/v1", Models: []string{"qwen3-vl-8b"}},
	}
	got := mergeKeyed(scan, detect.Found{
		Name: "configured", BaseURL: base, Chat: base + "/chat/completions",
		Models: []string{"fresh"}, Key: "sk-1",
	})
	if len(got) != 2 {
		t.Fatalf("merge appended a twin instead of replacing: %+v", got)
	}
	if got[0].Models[0] != "fresh" || got[0].Key != "sk-1" {
		t.Errorf("the keyed probe must take the slot (it is the one with the credential); got %+v", got[0])
	}
	if got[1].Models[0] != "qwen3-vl-8b" {
		t.Errorf("the merge must not disturb the rest of the scan; got %+v", got[1])
	}
	// An endpoint the scan missed entirely is appended, not dropped.
	got = mergeKeyed(scan, detect.Found{BaseURL: "http://127.0.0.1:7777/v1", Models: []string{"new"}})
	if len(got) != 3 {
		t.Fatalf("an unscanned endpoint must be appended; got %+v", got)
	}
}
