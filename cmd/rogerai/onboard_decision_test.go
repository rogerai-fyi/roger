package main

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/detect"
)

// These tests make features/onboarding/onboarding.feature's two TTY-gated scenarios
// EXECUTABLE without a PTY, by table-testing the pure decision functions extracted from
// runWizard's huh-form glue (the menu RENDERING stays interactive; the DECISIONS do not).
//
//   - applyRerunChoice  <- Scenario "Re-running setup updates config without wiping
//     earnings/identity": Keep / Modify / Reset, and the INVARIANT that only the local
//     share config is ever touched (the linked GitHub identity `User` + the earnings-side
//     price config are left intact; earnings themselves live broker-side, never in cfg).
//   - applyIntent       <- Scenario "First run onboards": the consume vs free vs earn
//     decision the welcome menu produces.

// TestApplyRerunChoice pins the re-run menu: keep ends the wizard untouched; reset forgets
// ONLY the local share; modify (and any unknown choice) proceeds keeping the share — and in
// every case the linked identity, broker, prices, and station survive.
func TestApplyRerunChoice(t *testing.T) {
	base := func() config {
		return config{
			Broker:    "https://b",
			User:      "octocat", // the linked GitHub identity (the payout identity)
			Onboarded: true,
			Share:     &Share{Model: "m", Port: 4140, PriceIn: 0.20, PriceOut: 0.30},
			Prices:    map[string]SharePrice{"m": {PriceIn: 0.20, PriceOut: 0.30}},
			Station:   "brave-otter-37",
		}
	}

	t.Run("keep ends the wizard, config untouched", func(t *testing.T) {
		out, done := applyRerunChoice(base(), "keep")
		if !done {
			t.Fatal("keep must end the wizard (done=true)")
		}
		if out.Share == nil {
			t.Fatal("keep must leave the existing share config in place")
		}
	})

	t.Run("modify keeps the share and proceeds", func(t *testing.T) {
		out, done := applyRerunChoice(base(), "modify")
		if done {
			t.Fatal("modify must proceed to reconfigure (done=false)")
		}
		if out.Share == nil {
			t.Fatal("modify keeps the existing share until the next step reconfigures it")
		}
	})

	t.Run("reset forgets ONLY the local share", func(t *testing.T) {
		out, done := applyRerunChoice(base(), "reset")
		if done {
			t.Fatal("reset must proceed to reconfigure (done=false)")
		}
		if out.Share != nil {
			t.Fatalf("reset must clear the local share, got %+v", out.Share)
		}
		if !out.Onboarded {
			t.Error("reset forgets the share, not the onboarded flag")
		}
	})

	t.Run("an unknown choice proceeds like modify", func(t *testing.T) {
		out, done := applyRerunChoice(base(), "???")
		if done || out.Share == nil {
			t.Fatalf("unknown choice should proceed keeping the share: done=%v share=%+v", done, out.Share)
		}
	})

	// THE INVARIANT (scenario 6): no menu choice ever touches the linked identity, the
	// broker, the price config, or the station. Earnings live broker-side and are not in
	// cfg at all, so re-running setup cannot move money.
	for _, choice := range []string{"keep", "modify", "reset", "???"} {
		out, _ := applyRerunChoice(base(), choice)
		if out.User != "octocat" {
			t.Errorf("%q wiped the linked identity: User=%q", choice, out.User)
		}
		if out.Broker != "https://b" {
			t.Errorf("%q changed the broker: %q", choice, out.Broker)
		}
		if out.Station != "brave-otter-37" {
			t.Errorf("%q changed the station: %q", choice, out.Station)
		}
		if len(out.Prices) != 1 {
			t.Errorf("%q disturbed the saved price config: %+v", choice, out.Prices)
		}
	}
}

// TestApplyIntent pins the welcome menu's outcome: consume just marks onboarded and
// launches; free/earn hand off to finishShare (free = no price, earn = the default price);
// an unknown intent is treated as consume. The linked identity survives every path.
func TestApplyIntent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubDetectFull(t, []detect.Found{{BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"wz"}}}, nil)
	base := config{Broker: "https://b", User: "octocat"}

	t.Run("consume marks onboarded and launches on defaults", func(t *testing.T) {
		out, ran, err := applyIntent(base, "consume", wizardOpts{})
		if err != nil || !ran || !out.Onboarded {
			t.Fatalf("consume = ran %v onboarded %v err %v", ran, out.Onboarded, err)
		}
		if out.Share != nil {
			t.Fatalf("consume is not a provider; no share config: %+v", out.Share)
		}
		if out.User != "octocat" {
			t.Errorf("consume must not touch the linked identity: %q", out.User)
		}
	})

	t.Run("free reconfigures share with no price, identity intact", func(t *testing.T) {
		out, ran, err := applyIntent(base, "free", wizardOpts{yes: true})
		if err != nil || !ran || out.Share == nil {
			t.Fatalf("free = ran %v err %v share %+v", ran, err, out.Share)
		}
		if out.Share.PriceIn != 0 || out.Share.PriceOut != 0 {
			t.Errorf("free share must carry no price: %+v", out.Share)
		}
		if out.User != "octocat" {
			t.Errorf("free must not touch the linked identity: %q", out.User)
		}
	})

	t.Run("earn reconfigures share with the default price, identity intact", func(t *testing.T) {
		out, ran, err := applyIntent(base, "earn", wizardOpts{yes: true})
		if err != nil || !ran || out.Share == nil {
			t.Fatalf("earn = ran %v err %v share %+v", ran, err, out.Share)
		}
		if out.Share.PriceIn != 0.20 || out.Share.PriceOut != 0.30 {
			t.Errorf("earn share must carry the default price: %+v", out.Share)
		}
		if out.User != "octocat" {
			t.Errorf("earn must not touch the linked identity: %q", out.User)
		}
	})

	t.Run("an unknown intent is treated as consume", func(t *testing.T) {
		out, ran, err := applyIntent(base, "mystery", wizardOpts{})
		if err != nil || !ran || !out.Onboarded || out.Share != nil {
			t.Fatalf("unknown intent should behave like consume: ran %v onboarded %v share %+v err %v", ran, out.Onboarded, out.Share, err)
		}
	})
}
