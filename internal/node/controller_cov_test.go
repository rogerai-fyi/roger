package node

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestSchedToProtocolConverts: an empty/nil window set yields no schedule, and a non-empty
// set maps field-for-field (times, in/out, free) to the wire protocol.PriceWindow the agent
// publishes — including a FREE in-window window (zero price, Free=true).
func TestSchedToProtocolConverts(t *testing.T) {
	if got := SchedToProtocol(nil); got != nil {
		t.Fatalf("SchedToProtocol(nil) = %v, want nil", got)
	}
	if got := SchedToProtocol([]SchedWindow{}); got != nil {
		t.Fatalf("SchedToProtocol(empty) = %v, want nil", got)
	}
	in := []SchedWindow{
		{Start: "08:00", End: "20:00", In: 1.5, Out: 3, Free: false},
		{Start: "20:00", End: "08:00", In: 0, Out: 0, Free: true},
	}
	got := SchedToProtocol(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	want := []protocol.PriceWindow{
		{Start: "08:00", End: "20:00", In: 1.5, Out: 3, Free: false},
		{Start: "20:00", End: "08:00", In: 0, Out: 0, Free: true},
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Start != w.Start || g.End != w.End || g.In != w.In || g.Out != w.Out || g.Free != w.Free {
			t.Errorf("window %d = %+v, want %+v", i, g, w)
		}
	}
}

// TestLinkLabel maps every agent link state to the UI link token (the SSE/TUI link column);
// an unknown state degrades to "connecting" (the conservative default, never a false on-air).
func TestLinkLabel(t *testing.T) {
	cases := []struct {
		st   agent.LinkState
		want string
	}{
		{agent.LinkOnAir, "on-air"},
		{agent.LinkReconnecting, "reconnecting"},
		{agent.LinkConnecting, "connecting"},
		{agent.LinkState(99), "connecting"},
	}
	for _, tc := range cases {
		if got := linkLabel(tc.st); got != tc.want {
			t.Errorf("linkLabel(%v) = %q, want %q", tc.st, got, tc.want)
		}
	}
}

// TestNewSeedsSavedPrices: New copies cfg.Prices into the controller (so on-air uses the
// operator's saved per-model price+schedule) and copies the map rather than aliasing it.
func TestNewSeedsSavedPrices(t *testing.T) {
	src := map[string]Pricing{
		"m": {In: 4, Out: 9, Windows: []SchedWindow{{Start: "00:00", End: "06:00", Free: true}}},
	}
	c := New(Config{Station: "amber-fox", Prices: src})
	got := c.PricingFor("m")
	if got.In != 4 || got.Out != 9 || len(got.Windows) != 1 || !got.Windows[0].Free {
		t.Fatalf("PricingFor(m) = %+v, want the seeded saved price", got)
	}
	// Mutating the source map after New must not bleed in (New ranges into its own map).
	src["m2"] = Pricing{Out: 5}
	if _, ok := c.Prices()["m2"]; ok {
		t.Error("New must copy cfg.Prices, not alias the caller's map")
	}
}

// TestStopAllStopsRealAndSkipsNil: StopAll stops every live session and clears the registry,
// skipping a nil entry without panicking (the nil-skip branch).
func TestStopAllStopsRealAndSkipsNil(t *testing.T) {
	c := newCtrl(t, Config{})
	if res := c.ToggleOnAir("free-1"); res.Err != nil {
		t.Fatalf("free-1 on air: %+v", res)
	}
	c.Adopt("free-2", nil) // a nil session in the registry (the skip branch)
	if c.OnAirCount() != 1 {
		t.Fatalf("on-air count = %d, want 1 (nil not counted)", c.OnAirCount())
	}
	c.StopAll()
	if c.OnAirCount() != 0 {
		t.Fatalf("after StopAll, on air = %d, want 0", c.OnAirCount())
	}
	if len(c.Sessions()) != 0 {
		t.Fatalf("StopAll should clear the registry; got %d entries", len(c.Sessions()))
	}
}

// TestTogglePrivateAtLimitAndCycle covers TogglePrivate's cap guard for an off-air model
// and the private<->public cycle on a LIVE session (stop+restart with new visibility).
func TestTogglePrivateAtLimitAndCycle(t *testing.T) {
	c := newCtrl(t, Config{MaxOnAir: 1})
	c.SetLoggedIn(true)

	// Fill the single on-air slot with a public free session.
	if res := c.ToggleOnAir("free-1"); res.Err != nil {
		t.Fatalf("free-1 on air: %+v", res)
	}
	// free-2 is OFF air and the node is at the cap -> going private is blocked.
	if res := c.TogglePrivate("free-2"); !res.AtLimit {
		t.Fatalf("TogglePrivate at cap should be AtLimit, got %+v", res)
	}
	if c.Private()["free-2"] {
		t.Error("a cap-blocked TogglePrivate must not flip the flag")
	}

	// free-1 IS on air: toggling private stops+restarts it private (wasOn path), and even
	// at the cap of 1 it is allowed because it is a restart, not an extra band.
	res := c.TogglePrivate("free-1")
	if res.Err != nil || !res.NowPrivate {
		t.Fatalf("free-1 -> private: %+v", res)
	}
	if !c.Private()["free-1"] || c.OnAirCount() != 1 {
		t.Fatalf("free-1 should be private and still on air; private=%v onair=%d", c.Private()["free-1"], c.OnAirCount())
	}
	// Toggle again: private -> public (goPrivate=false branch), still exactly one session.
	res = c.TogglePrivate("free-1")
	if res.Err != nil || res.NowPrivate {
		t.Fatalf("free-1 -> public: %+v", res)
	}
	if c.Private()["free-1"] {
		t.Error("free-1 should be public after the second toggle")
	}
	if c.OnAirCount() != 1 {
		t.Fatalf("on-air count after cycle = %d, want 1", c.OnAirCount())
	}
}

// TestPricingForShareModelDefault: with no per-model override the onboarding default model
// shares at the saved onboarding price; any other model is free; an explicit price wins.
func TestPricingForShareModelDefault(t *testing.T) {
	c := New(Config{Station: "x", ShareModel: "llama-3", SharePriceI: 2, SharePriceO: 6})
	if p := c.PricingFor("llama-3"); p.In != 2 || p.Out != 6 {
		t.Fatalf("PricingFor(default) = %+v, want in 2 out 6", p)
	}
	if p := c.PricingFor("other"); p.In != 0 || p.Out != 0 || len(p.Windows) != 0 {
		t.Fatalf("PricingFor(other) = %+v, want free", p)
	}
	c.SetPricing("llama-3", Pricing{Out: 11})
	if p := c.PricingFor("llama-3"); p.Out != 11 {
		t.Fatalf("explicit per-model price should win over the onboarding default: %+v", p)
	}
}

// TestStartUsesHeadlineUpstreamFallback: a row with no per-row upstream goes on air against
// the controller's headline upstream (the pre-per-row fallback in startLocked).
func TestStartUsesHeadlineUpstreamFallback(t *testing.T) {
	c := newCtrl(t, Config{Upstream: "http://127.0.0.1:0/v1"})
	c.SetRows([]ShareRow{{Model: "norow-up", Ctx: 4096}}) // empty Upstream -> fallback
	res := c.ToggleOnAir("norow-up")
	if res.Err != nil || res.WentOff {
		t.Fatalf("on air with headline-upstream fallback: %+v", res)
	}
	if c.OnAirCount() != 1 {
		t.Fatalf("on-air count = %d, want 1", c.OnAirCount())
	}
}

// TestToggleErrorWhenBrokerRejects: when agent.Start cannot register (a real broker that
// rejects the registration), both ToggleOnAir and TogglePrivate surface the error and leave
// nothing on air / no flag flipped — a failed start never half-registers a session.
func TestToggleErrorWhenBrokerRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // registration never takes
	}))
	defer srv.Close()
	c := New(Config{Broker: srv.URL, Station: "amber-fox"})
	c.SetRows([]ShareRow{{Model: "m", Ctx: 4096, Upstream: "http://127.0.0.1:0/v1/chat/completions"}})

	res := c.ToggleOnAir("m")
	if res.Err == nil {
		t.Fatal("ToggleOnAir should surface the broker register failure")
	}
	if c.OnAirCount() != 0 {
		t.Fatalf("a failed start must not register a session; on air = %d", c.OnAirCount())
	}

	c.SetLoggedIn(true)
	pres := c.TogglePrivate("m")
	if pres.Err == nil {
		t.Fatal("TogglePrivate should surface the broker register failure")
	}
	if c.Private()["m"] {
		t.Error("a failed private start must not flip the private flag")
	}
	if c.OnAirCount() != 0 {
		t.Fatalf("a failed private start must not register a session; on air = %d", c.OnAirCount())
	}
}

// TestHeadlineEmptyAndNilSkip: Headline reports not-on-air for an empty registry and for a
// registry holding only a nil session (the s==nil skip + the final false return).
func TestHeadlineEmptyAndNilSkip(t *testing.T) {
	c := newCtrl(t, Config{})
	if s, on := c.Headline(); on || s != nil {
		t.Fatalf("fresh controller Headline = (%v,%v), want (nil,false)", s, on)
	}
	c.Adopt("free-1", nil) // only a nil session -> still not on air
	if s, on := c.Headline(); on || s != nil {
		t.Fatalf("nil-only Headline = (%v,%v), want (nil,false)", s, on)
	}
}
