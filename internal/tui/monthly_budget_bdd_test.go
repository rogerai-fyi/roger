package tui

// monthly_budget_bdd_test.go - the godog harness for features/money/monthly_budget_tui.feature.
//
// Everything drives the REAL limits screen through its key handler, against a REAL
// httptest broker for the PATCH /account/limit call - a money control is not something to
// test against a stub of itself.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
)

type budgetBDD struct {
	t *testing.T
	m model

	mu       sync.Mutex
	patches  []float64 // every cap the broker was asked to set
	refuse   bool
	srv      *httptest.Server
	lastCmds []tea.Cmd
}

func (s *budgetBDD) brokerPatches() []float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]float64(nil), s.patches...)
}

func (s *budgetBDD) startBroker() {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/account/limit" && r.Method == http.MethodPatch {
			s.mu.Lock()
			refuse := s.refuse
			s.mu.Unlock()
			if refuse {
				http.Error(w, "the broker refused", http.StatusForbidden)
				return
			}
			var in struct {
				Cap float64 `json:"monthly_cap"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			s.mu.Lock()
			s.patches = append(s.patches, in.Cap)
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"monthly_cap": in.Cap, "monthly_spend": 0.00672})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	s.t.Cleanup(s.srv.Close)
}

func (s *budgetBDD) loggedInOnLimits() error {
	s.startBroker()
	s.m = browseSeed(120)
	s.m.broker = s.srv.URL
	s.m.user = "tester"
	s.m.loggedIn = true
	s.m.limits = &LimitStore{Models: map[string]Limit{"gpt-oss-20b": {MaxOut: 2}}}
	s.m.enterLimits()
	s.m.mode = modeLimits
	return nil
}

func (s *budgetBDD) notLoggedIn() error {
	if err := s.loggedInOnLimits(); err != nil {
		return err
	}
	s.m.user = ""
	s.m.loggedIn = false
	return nil
}

func (s *budgetBDD) noCap() error { s.m.monthlyCap = 0; return nil }

func (s *budgetBDD) capOf(amount string) error {
	fmt.Sscanf(amount, "$%f", &s.m.monthlyCap)
	if s.m.monthlyCap == 0 {
		return fmt.Errorf("fixture cap %q did not parse", amount)
	}
	return nil
}

// key drives one keypress through the real limits handler, running any Cmd it returns
// (the broker call rides a Cmd) and feeding its message back, as the Bubble Tea loop does.
func (s *budgetBDD) key(k tea.KeyMsg) {
	out, cmd := s.m.onLimitsKey(k)
	s.m = asModel(out)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			out2, _ := s.m.Update(msg)
			s.m = asModel(out2)
		}
	}
}

func (s *budgetBDD) keys(text string) {
	for _, r := range text {
		s.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// toBudgetRow moves the cursor up onto the wallet's budget row.
func (s *budgetBDD) toBudgetRow() {
	for i := 0; i < 10 && !s.m.onBudgetRow(); i++ {
		s.key(tea.KeyMsg{Type: tea.KeyUp})
	}
}

func (s *budgetBDD) editAndEnter(value string) error {
	s.toBudgetRow()
	if !s.m.onBudgetRow() {
		return fmt.Errorf("the cursor never reached the monthly budget row")
	}
	s.key(tea.KeyMsg{Type: tea.KeyEnter})
	// Typing replaces the prefill, as a fresh entry: clear first.
	for i := 0; i < 12; i++ {
		s.key(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	s.keys(value)
	s.key(tea.KeyMsg{Type: tea.KeyEnter})
	return nil
}

func (s *budgetBDD) editBudget() error {
	s.toBudgetRow()
	if !s.m.onBudgetRow() {
		return fmt.Errorf("the cursor never reached the monthly budget row")
	}
	s.key(tea.KeyMsg{Type: tea.KeyEnter})
	return nil
}

func (s *budgetBDD) editThenEsc() error {
	if err := s.editBudget(); err != nil {
		return err
	}
	s.key(tea.KeyMsg{Type: tea.KeyEsc})
	return nil
}

func (s *budgetBDD) rowText() string {
	return stripANSI(monthlyBudgetLine(s.m))
}

// --- Then steps --------------------------------------------------------------

func (s *budgetBDD) rowNamesEditKey() error {
	if !strings.Contains(s.rowText(), "edit") {
		return fmt.Errorf("the row offers no way to edit it: %q", s.rowText())
	}
	return nil
}

func (s *budgetBDD) narrowRowNamesIt() error {
	s.m.width = 80
	return s.rowNamesEditKey()
}

func (s *budgetBDD) brokerAskedToSet(amount string) error {
	var want float64
	fmt.Sscanf(amount, "$%f", &want)
	for _, p := range s.brokerPatches() {
		if p == want {
			return nil
		}
	}
	return fmt.Errorf("the broker was never asked for a %s cap; patches: %v", amount, s.brokerPatches())
}

func (s *budgetBDD) brokerAskedToClear() error { return s.brokerAskedToSet("$0") }

func (s *budgetBDD) rowShowsNewCapNow() error {
	if s.m.monthlyCap != 25 {
		return fmt.Errorf("the cap on screen is %v; it must update from the broker's reply, "+
			"not wait for the next balance poll", s.m.monthlyCap)
	}
	if !strings.Contains(s.rowText(), "25") {
		return fmt.Errorf("the row does not show the new cap: %q", s.rowText())
	}
	return nil
}

func (s *budgetBDD) editorPrefilled(want string) error {
	if !s.m.editingBudget() {
		return fmt.Errorf("the budget editor did not open")
	}
	if s.m.editBuf != want {
		return fmt.Errorf("the editor should start from the current value: got %q, want %q", s.m.editBuf, want)
	}
	return nil
}

func (s *budgetBDD) rowShowsNoCap() error {
	if s.m.monthlyCap != 0 || !strings.Contains(s.rowText(), "no cap") {
		return fmt.Errorf("cap=%v row=%q", s.m.monthlyCap, s.rowText())
	}
	return nil
}

func (s *budgetBDD) capUnchanged() error {
	if s.m.monthlyCap != 25 {
		return fmt.Errorf("esc changed the cap to %v", s.m.monthlyCap)
	}
	return nil
}

func (s *budgetBDD) brokerNotCalled() error {
	if n := len(s.brokerPatches()); n != 0 {
		return fmt.Errorf("the broker was called %d time(s): %v", n, s.brokerPatches())
	}
	return nil
}

func (s *budgetBDD) refusedWithMessage() error {
	if !s.m.editingBudget() {
		return fmt.Errorf("a refused value should leave the editor open to fix")
	}
	if strings.TrimSpace(stripANSI(s.m.status)) == "" {
		return fmt.Errorf("a refusal must say why")
	}
	return nil
}

func (s *budgetBDD) brokerWillRefuse() error {
	s.mu.Lock()
	s.refuse = true
	s.mu.Unlock()
	return nil
}

func (s *budgetBDD) rowStillShows(amount string) error {
	var want float64
	fmt.Sscanf(amount, "$%f", &want)
	if s.m.monthlyCap != want {
		return fmt.Errorf("a failed change moved the cap on screen: %v", s.m.monthlyCap)
	}
	return nil
}

func (s *budgetBDD) failureShown() error {
	if strings.TrimSpace(stripANSI(s.m.status)) == "" {
		return fmt.Errorf("a broker failure must be shown, not swallowed")
	}
	return nil
}

func (s *budgetBDD) rowSaysLogIn() error {
	if !strings.Contains(s.rowText(), "log in") {
		return fmt.Errorf("logged out, the row should point at logging in: %q", s.rowText())
	}
	return nil
}

func (s *budgetBDD) cannotBeEdited() error {
	s.toBudgetRow()
	s.key(tea.KeyMsg{Type: tea.KeyEnter})
	if s.m.editingBudget() {
		return fmt.Errorf("a logged-out session opened the budget editor")
	}
	return nil
}

func (s *budgetBDD) terminal80() error { s.m.width = 80; return nil }

func (s *budgetBDD) rowShowsAffordanceNarrow() error { return s.rowNamesEditKey() }

func (s *budgetBDD) nothingWraps() error {
	for _, ln := range strings.Split(monthlyBudgetLine(s.m), "\n") {
		if w := visibleWidth(stripANSI(ln)); w > 80 {
			return fmt.Errorf("the row is %d cells wide on an 80-column terminal", w)
		}
	}
	return nil
}

func visibleWidth(s string) int { return len([]rune(s)) }

func TestMonthlyBudgetFeature(t *testing.T) {
	st := &budgetBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = budgetBDD{t: t}
				return c, nil
			})
			sc.Step(`^I am logged in on the spend-limits screen$`, st.loggedInOnLimits)
			sc.Step(`^I am not logged in$`, st.notLoggedIn)
			sc.Step(`^no monthly cap is set$`, st.noCap)
			sc.Step(`^a monthly cap of (\$[0-9.]+)$`, st.capOf)
			sc.Step(`^I edit the monthly budget and enter "([^"]*)"$`, st.editAndEnter)
			sc.Step(`^I edit the monthly budget$`, st.editBudget)
			sc.Step(`^I edit the monthly budget and press esc$`, st.editThenEsc)
			sc.Step(`^the monthly budget row names the key that edits it$`, st.rowNamesEditKey)
			sc.Step(`^it does so on an 80-column terminal too$`, st.narrowRowNamesIt)
			sc.Step(`^the broker is asked to set a (\$[0-9.]+) monthly cap$`, st.brokerAskedToSet)
			sc.Step(`^the broker is asked to clear the cap$`, st.brokerAskedToClear)
			sc.Step(`^the row shows the new cap without waiting for the next balance poll$`, st.rowShowsNewCapNow)
			sc.Step(`^the editor is prefilled with "([^"]*)"$`, st.editorPrefilled)
			sc.Step(`^the row shows "no cap" again$`, st.rowShowsNoCap)
			sc.Step(`^the cap is unchanged$`, st.capUnchanged)
			sc.Step(`^the broker is not called$`, st.brokerNotCalled)
			sc.Step(`^the edit is refused with a message$`, st.refusedWithMessage)
			sc.Step(`^the broker will refuse the change$`, st.brokerWillRefuse)
			sc.Step(`^the row still shows (\$[0-9.]+)$`, st.rowStillShows)
			sc.Step(`^the failure is shown to the operator$`, st.failureShown)
			sc.Step(`^the monthly budget row says to log in$`, st.rowSaysLogIn)
			sc.Step(`^it cannot be edited$`, st.cannotBeEdited)
			sc.Step(`^the terminal is 80 columns wide$`, st.terminal80)
			sc.Step(`^the monthly budget row still shows an edit affordance$`, st.rowShowsAffordanceNarrow)
			sc.Step(`^nothing on the row wraps$`, st.nothingWraps)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/money/monthly_budget_tui.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("monthly budget scenarios failed")
	}
}
