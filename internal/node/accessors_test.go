package node

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/agent"
)

// TestLoginStateTransitions covers SetLoggedIn (raise-only), Logout, and LoggedIn.
func TestLoginStateTransitions(t *testing.T) {
	c := newCtrl(t, Config{})
	if c.LoggedIn() {
		t.Fatal("a fresh controller should not be logged in")
	}
	c.SetLoggedIn(false) // raise-only: false is a no-op
	if c.LoggedIn() {
		t.Fatal("SetLoggedIn(false) must not log in")
	}
	c.SetLoggedIn(true)
	if !c.LoggedIn() {
		t.Fatal("SetLoggedIn(true) should log in")
	}
	c.Logout()
	if c.LoggedIn() {
		t.Fatal("Logout should clear the login state")
	}
}

// TestSetPricesAndCopy covers SetPrices (bulk seed) and that Prices() returns a copy
// the caller can't use to mutate controller state.
func TestSetPricesAndCopy(t *testing.T) {
	c := newCtrl(t, Config{})
	c.SetPrices(map[string]Pricing{"m1": {In: 1, Out: 2}, "m2": {Out: 5}})
	got := c.Prices()
	if got["m1"].In != 1 || got["m1"].Out != 2 || got["m2"].Out != 5 {
		t.Fatalf("Prices = %+v, want m1{1,2} m2{_,5}", got)
	}
	got["m1"] = Pricing{In: 99} // mutate the returned copy
	if c.Prices()["m1"].In == 99 {
		t.Error("Prices() must return a defensive copy")
	}
}

// TestSnapshotAccessorsCopyAndRedact covers Upstream/SavedUpstream and the defensive
// copies returned by Private()/Sessions().
func TestSnapshotAccessorsCopyAndRedact(t *testing.T) {
	c := New(Config{Station: "x", Upstream: "http://up:8080/v1/chat/completions", UpstreamKey: "sk-secret"})
	if c.Upstream() == "" {
		t.Error("Upstream should be populated from config")
	}
	if up, key := c.SavedUpstream(); up == "" || key != "sk-secret" {
		t.Errorf("SavedUpstream = %q/%q, want the seeded upstream+key", up, key)
	}

	priv := c.Private()
	priv["ghost"] = true // mutate the copy
	if c.Private()["ghost"] {
		t.Error("Private() must return a defensive copy")
	}
	sess := c.Sessions()
	sess["ghost"] = nil // mutate the copy
	if _, ok := c.Sessions()["ghost"]; ok {
		t.Error("Sessions() must return a defensive copy")
	}
}

// TestTogglePrivateLoginGatedAndFlips covers TogglePrivate: an unknown model is a no-op,
// going private is login-gated, and a logged-in toggle flips the per-model flag.
func TestTogglePrivateLoginGatedAndFlips(t *testing.T) {
	c := newCtrl(t, Config{})

	// Unknown model: no row -> zero result, no flag set.
	if res := c.TogglePrivate("ghost"); res.LoginNeeded || res.NowPrivate {
		t.Fatalf("TogglePrivate(unknown) = %+v, want zero result", res)
	}

	// Login-gated when not signed in.
	if res := c.TogglePrivate("free-1"); !res.LoginNeeded {
		t.Fatalf("TogglePrivate without login should be gated, got %+v", res)
	}
	if c.Private()["free-1"] {
		t.Error("a gated TogglePrivate must not flip the flag")
	}

	// Logged in: the flag flips to private.
	c.SetLoggedIn(true)
	if res := c.TogglePrivate("free-1"); res.LoginNeeded || res.AtLimit {
		t.Fatalf("logged-in TogglePrivate should proceed, got %+v", res)
	}
	if !c.Private()["free-1"] {
		t.Error("free-1 should be private after a logged-in toggle")
	}
}

// TestAdoptRegistersSession covers Adopt + that Sessions/Headline observe the adopted
// session (the front-end handing the controller a live session it started elsewhere).
func TestAdoptRegistersSession(t *testing.T) {
	c := newCtrl(t, Config{})
	s := &agent.Session{}
	c.Adopt("free-1", s)
	if c.Sessions()["free-1"] != s {
		t.Fatal("Adopt should register the session under its model")
	}
	if _, on := c.Headline(); !on {
		t.Error("Headline should report on-air once a session is adopted")
	}
}
