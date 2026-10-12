package main

// Executable spec: features/onboarding/welcome_card.feature, the host's side (untagged): when
// the one-time card is due, who writes welcome_seen, and the `roger onboard` paths that stay.
// The real launch path (tuiLaunch), the real config file in a scratch XDG_CONFIG_HOME, the real
// TUI model driven through its tea.Model interface, and the real `roger onboard` command.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/look"
	"rogerai.fm/roger/v6/internal/tui"
)

type welcomeHostBDD struct {
	t      *testing.T
	dir    string
	tty    bool
	cfg    config
	hooks  tui.Hooks
	due    bool
	tm     tea.Model
	booted int
	order  []string
	err    error
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func (s *welcomeHostBDD) reset() {
	s.dir = s.t.TempDir()
	s.t.Setenv("XDG_CONFIG_HOME", s.dir)
	s.t.Setenv("HOME", s.dir)
	s.tty, s.cfg, s.hooks, s.due, s.tm, s.booted, s.order, s.err = true, config{}, tui.Hooks{}, false, nil, 0, nil, nil
}

func (s *welcomeHostBDD) writeRaw(v map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(v)
	return os.WriteFile(configPath(), b, 0o600)
}

func (s *welcomeHostBDD) onDisk() map[string]any {
	out := map[string]any{}
	b, err := os.ReadFile(configPath())
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// launch runs the no-args launch up to the TUI and builds the TUI the way runTUI does.
func (s *welcomeHostBDD) launch() error {
	s.cfg = loadConfig()
	s.cfg.LastSeenVersion = "" // a first-ever launch plays the version boot
	s.cfg, s.hooks = tuiLaunch(s.cfg, s.tty, func() { s.booted++; s.order = append(s.order, "boot") })
	s.due = s.hooks.WelcomeDue
	s.order = append(s.order, "tui")
	s.tm = tui.NewWithHooks("http://127.0.0.1:1", "tester", nil, s.hooks)
	s.tm, _ = s.tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return nil
}

func (s *welcomeHostBDD) frame() string { return ansiRE.ReplaceAllString(s.tm.View(), "") }

func (s *welcomeHostBDD) cardDrawn() bool {
	return strings.Contains(s.frame(), look.Eyebrow(look.WelcomeHeading))
}

func (s *welcomeHostBDD) press(k string) {
	var msg tea.KeyMsg
	switch k {
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	s.tm, _ = s.tm.Update(msg)
}

// ---- givens -----------------------------------------------------------------

func (s *welcomeHostBDD) freshConfig() error { return nil }

func (s *welcomeHostBDD) onboarded() error { return s.writeRaw(map[string]any{"onboarded": true}) }

func (s *welcomeHostBDD) sharing() error {
	return s.writeRaw(map[string]any{"share": map[string]any{"model": "llama3.2", "port": 4141}})
}

func (s *welcomeHostBDD) shownAndClosed() error {
	if err := s.launch(); err != nil {
		return err
	}
	s.press("esc")
	return nil
}

func (s *welcomeHostBDD) cardOpenGiven() error {
	if err := s.launch(); err != nil {
		return err
	}
	if !s.cardDrawn() {
		return fmt.Errorf("the card is not open on a fresh launch:\n%s", s.frame())
	}
	return nil
}

func (s *welcomeHostBDD) cannotWrite() error {
	if err := s.writeRaw(map[string]any{"broker": defaultBroker}); err != nil {
		return err
	}
	if err := s.launch(); err != nil {
		return err
	}
	dir := filepath.Dir(configPath())
	if err := os.Chmod(dir, 0o500); err != nil {
		return err
	}
	s.t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return nil
}

func (s *welcomeHostBDD) seenTrue() error { return s.writeRaw(map[string]any{"welcome_seen": true}) }

func (s *welcomeHostBDD) seenAndShare() error {
	return s.writeRaw(map[string]any{"welcome_seen": true, "onboarded": true,
		"share": map[string]any{"model": "llama3.2", "port": 4141}})
}

// ---- whens ------------------------------------------------------------------

func (s *welcomeHostBDD) closes(how string) error {
	switch how {
	case "presses esc":
		s.press("esc")
	case "picks a line with its key":
		s.press("1")
	case "picks a line with enter":
		s.press("enter")
	case "presses q":
		s.press("q")
	default:
		return fmt.Errorf("unknown way to close: %q", how)
	}
	return nil
}

func (s *welcomeHostBDD) onboard(args string) error {
	s.err = cmdOnboard(loadConfig(), strings.Fields(args))
	return s.err
}

// ---- thens ------------------------------------------------------------------

func (s *welcomeHostBDD) drawnOverDial() error {
	if !s.due || !s.cardDrawn() {
		return fmt.Errorf("the welcome card is not drawn:\n%s", s.frame())
	}
	f := s.frame()
	if strings.Index(f, "R O G E R") > strings.Index(f, look.Eyebrow(look.WelcomeHeading)) {
		return fmt.Errorf("the card is not under the brand")
	}
	return nil
}

func (s *welcomeHostBDD) noPicker() error {
	src, err := os.ReadFile("onboard.go")
	if err != nil {
		return err
	}
	for _, gone := range []string{"Are you here to use models", "Just use models (open the app)", "func maybeOnboard", "func applyIntent"} {
		if strings.Contains(string(src), gone) {
			return fmt.Errorf("the pre-TUI picker is still there: %q", gone)
		}
	}
	return nil
}

func (s *welcomeHostBDD) opensOnDialNoCard() error {
	if s.due || s.cardDrawn() {
		return fmt.Errorf("the card is shown again")
	}
	if !strings.Contains(s.frame(), "TUNE IN") {
		return fmt.Errorf("the TUI did not open on the dial:\n%s", s.frame())
	}
	return nil
}

func (s *welcomeHostBDD) seenSaved() error {
	if v, _ := s.onDisk()["welcome_seen"].(bool); !v {
		return fmt.Errorf("welcome_seen is not saved: %v", s.onDisk())
	}
	return nil
}

func (s *welcomeHostBDD) notNextLaunch() error {
	if err := s.launch(); err != nil {
		return err
	}
	if s.due || s.cardDrawn() {
		return fmt.Errorf("the card is shown on the next launch")
	}
	return nil
}

func (s *welcomeHostBDD) noCard() error {
	if s.due || (s.tm != nil && s.cardDrawn()) {
		return fmt.Errorf("a welcome card is drawn")
	}
	return nil
}

func (s *welcomeHostBDD) bootThenCard() error {
	if s.booted != 1 {
		return fmt.Errorf("the boot played %d times, want once", s.booted)
	}
	if strings.Join(s.order, ",") != "boot,tui" {
		return fmt.Errorf("launch order %v, want the boot before the TUI", s.order)
	}
	if !s.cardDrawn() {
		return fmt.Errorf("the card is not drawn once the TUI opens")
	}
	return nil
}

func (s *welcomeHostBDD) keepsWorking() error {
	if s.cardDrawn() {
		return fmt.Errorf("the card is still drawn")
	}
	if !strings.Contains(s.frame(), "TUNE IN") {
		return fmt.Errorf("the TUI is not on the dial:\n%s", s.frame())
	}
	return nil
}

func (s *welcomeHostBDD) statusNotRemembered() error {
	if !strings.Contains(s.frame(), "the welcome choice could not be remembered") {
		return fmt.Errorf("the status does not say the choice could not be remembered:\n%s", s.frame())
	}
	return nil
}

func (s *welcomeHostBDD) nextShowsOnce() error {
	if err := s.launch(); err != nil {
		return err
	}
	if !s.due || !s.cardDrawn() {
		return fmt.Errorf("the next launch does not show the card")
	}
	s.press("esc")
	if err := s.launch(); err != nil {
		return err
	}
	if s.due {
		return fmt.Errorf("the card shows more than once after --welcome")
	}
	return nil
}

func (s *welcomeHostBDD) resetBringsBack() error {
	if v, _ := s.onDisk()["welcome_seen"].(bool); v {
		return fmt.Errorf("welcome_seen is still true after --reset")
	}
	if err := s.launch(); err != nil {
		return err
	}
	if !s.due {
		return fmt.Errorf("the next launch does not show the card after --reset")
	}
	return nil
}

func (s *welcomeHostBDD) completesNoTUI() error {
	if s.err != nil {
		return fmt.Errorf("roger onboard failed: %v", s.err)
	}
	if s.tm != nil {
		return fmt.Errorf("a TUI was opened")
	}
	return nil
}

func (s *welcomeHostBDD) offersRerun() error {
	want := []string{"Keep it as is", "Modify my setup", "Reset and start over"}
	if len(rerunChoices) != len(want) {
		return fmt.Errorf("the re-run offers %d choices, want %d", len(rerunChoices), len(want))
	}
	for i, c := range rerunChoices {
		if c.Key != want[i] {
			return fmt.Errorf("choice %d is %q, want %q", i, c.Key, want[i])
		}
	}
	return nil
}

func (s *welcomeHostBDD) rerunTouchesOnlyShare() error {
	in := config{User: "octo", Onboarded: true, Station: "brave-otter-12",
		Share:  &Share{Model: "llama3.2", Port: 4141},
		Prices: map[string]SharePrice{"llama3.2": {PriceIn: 0.2, PriceOut: 0.3}}}
	for _, choice := range []string{"keep", "modify", "reset"} {
		out, _ := applyRerunChoice(in, choice)
		if out.User != in.User || out.Station != in.Station || out.Prices["llama3.2"].PriceOut != in.Prices["llama3.2"].PriceOut {
			return fmt.Errorf("%s touched the account, station or prices", choice)
		}
	}
	return nil
}

func (s *welcomeHostBDD) nothingAsks() error {
	if err := s.launch(); err != nil {
		return err
	}
	if strings.Contains(s.frame(), "Are you here to use models") {
		return fmt.Errorf("the old picker asked")
	}
	return s.noPicker()
}

func TestWelcomeCardFeatureHost(t *testing.T) {
	st := &welcomeHostBDD{t: t}
	// No local model server: the scripted onboard paths take their no-model branch on every
	// machine, instead of probing whatever this one happens to serve.
	stubDetectFull(t, nil, nil)
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Given(`^roger is launched with no arguments on an interactive terminal$`, func() error { st.tty = true; return nil })
			sc.Given(`^a config with no onboarded flag, no saved share and no welcome_seen flag$`, st.freshConfig)
			sc.When(`^the TUI opens$`, st.launch)
			sc.Then(`^the welcome card is drawn over the dial, under the ROGER·AI brand$`, st.drawnOverDial)
			sc.Then(`^no pre-TUI picker runs before it$`, st.noPicker)
			sc.Given(`^the welcome card was shown and closed$`, st.shownAndClosed)
			sc.When(`^roger is launched again$`, st.launch)
			sc.Then(`^the TUI opens on the dial with no card$`, st.opensOnDialNoCard)
			sc.Given(`^the welcome card is open$`, st.cardOpenGiven)
			sc.When(`^the user (presses esc|picks a line with its key|picks a line with enter|presses q)$`, st.closes)
			sc.Then(`^welcome_seen is saved as true$`, st.seenSaved)
			sc.Then(`^the card is not shown on the next launch$`, st.notNextLaunch)
			sc.Given(`^a config with onboarded true$`, st.onboarded)
			sc.Then(`^no welcome card is drawn$`, st.noCard)
			sc.Then(`^welcome_seen is saved as true on the next config save$`, st.seenSaved)
			sc.Given(`^a config with a saved share and no onboarded flag$`, st.sharing)
			sc.Given(`^stdout is not a terminal$`, func() error { st.tty = false; return nil })
			sc.When(`^roger is launched with no arguments$`, st.launch)
			sc.Then(`^no welcome card is drawn and nothing waits for a key$`, st.noCard)
			sc.Given(`^the user ran \x60roger onboard --free --yes\x60 before the first TUI launch$`, func() error { return st.onboard("--free --yes") })
			sc.Given(`^a first-ever launch where the version boot plays$`, func() error { return nil })
			sc.When(`^the boot finishes$`, st.launch)
			sc.Then(`^the TUI opens and only then is the welcome card drawn$`, st.bootThenCard)
			sc.Given(`^the config file cannot be written$`, st.cannotWrite)
			sc.When(`^the user closes the card$`, func() error { st.press("esc"); return nil })
			sc.Then(`^the TUI keeps working on the dial$`, st.keepsWorking)
			sc.Then(`^the status line says the welcome choice could not be remembered$`, st.statusNotRemembered)
			sc.Given(`^welcome_seen is true$`, st.seenTrue)
			sc.When(`^the user runs \x60roger onboard ([^\x60]*)\x60$`, st.onboard)
			sc.Then(`^the next TUI launch shows the welcome card once$`, st.nextShowsOnce)
			sc.Given(`^welcome_seen is true and a saved share$`, st.seenAndShare)
			sc.Then(`^welcome_seen is cleared, so the next TUI launch shows the card$`, st.resetBringsBack)
			sc.Then(`^the share setup completes as before without opening the TUI$`, st.completesNoTUI)
			sc.When(`^the user runs \x60roger onboard\x60 on a terminal$`, func() error { return nil })
			sc.Then(`^it offers Keep it as is, Modify my setup, Reset and start over$`, st.offersRerun)
			sc.Then(`^a re-run touches only the local share setup, never the linked account, prices or earnings$`, st.rerunTouchesOnlyShare)
			sc.Given(`^a brand-new install$`, st.freshConfig)
			sc.Then(`^nothing asks "Are you here to use models, or to share your own\?" before the TUI$`, st.nothingAsks)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "~@tui && ~@console",
			Paths: []string{"../../features/onboarding/welcome_card.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the welcome card host scenarios failed")
	}
}
