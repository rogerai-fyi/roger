package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rogerai-fyi/roger/internal/detect"
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
