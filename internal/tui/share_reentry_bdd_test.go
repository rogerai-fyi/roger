package tui

// share_reentry_bdd_test.go - the godog harness for features/sharing/share_reentry.feature.
// Drives the REAL doShare / onSharesDetected / view path; detection results are injected
// as the message the async scan would deliver, which is exactly how they arrive.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/detect"
)

type reentryBDD struct {
	t       *testing.T
	m       model
	lastCmd tea.Cmd
	curWas  string
}

func found(models ...string) []detect.Found {
	return []detect.Found{{Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1",
		Chat: "http://127.0.0.1:11434/v1/chat/completions", Models: models}}
}

func (s *reentryBDD) scannedTable() error {
	// A broker that accepts registrations, so ON-AIR toggles genuinely start (the toggle
	// registers the node; against a dead URL it bails and the row stays off air).
	srv := fakeBroker(s.t)
	// Built on the fake broker from the start (the controller captures the broker URL at
	// construction), then sized and ticked like browseSeed does.
	var tm tea.Model = NewWithHooks(srv.URL, "tester", nil, Hooks{})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	tm, _ = tm.Update(balanceMsg{balance: 42.17, loggedIn: true})
	s.m = tm.(model)
	// The first, loud open, landing its result - the state every later scenario starts from.
	out, _ := s.m.doShare(nil)
	s.m = asModel(out)
	out, _ = s.m.onSharesDetected(found("gpt-oss-20b", "qwen3-vl-8b"), nil)
	s.m = asModel(out)
	if len(s.m.shareRows) != 2 {
		return fmt.Errorf("seed expected 2 rows, got %d", len(s.m.shareRows))
	}
	return nil
}

func (s *reentryBDD) noScanYet() error {
	s.m = browseSeed(120)
	s.m.user = "tester"
	return nil
}

func (s *reentryBDD) openShare() error {
	out, cmd := s.m.doShare(nil)
	s.m = asModel(out)
	s.lastCmd = cmd
	return nil
}

func (s *reentryBDD) leaveAndReturn() error {
	// [1] TUNE IN, then [2] SHARE - the founder's exact hop.
	s.m.mode = modeBrowse
	out, cmd := s.m.doShare(nil)
	s.m = asModel(out)
	s.lastCmd = cmd
	return nil
}

func (s *reentryBDD) view() string { return stripANSI(s.m.shareView(100)) }

func (s *reentryBDD) scanningPoseShown() error {
	if !s.m.shareLoading || !strings.Contains(s.view(), "scanning the band") {
		return fmt.Errorf("the first open should scan loudly; loading=%v", s.m.shareLoading)
	}
	return nil
}

func (s *reentryBDD) detectionStarted() error {
	if s.lastCmd == nil {
		return fmt.Errorf("no detection was started")
	}
	return nil
}

func (s *reentryBDD) tableAtOnce() error {
	v := s.view()
	for _, mdl := range []string{"gpt-oss-20b", "qwen3-vl-8b"} {
		if !strings.Contains(v, mdl) {
			return fmt.Errorf("the previous rows should render at once; %q missing:\n%s", mdl, v)
		}
	}
	return nil
}

func (s *reentryBDD) noScanningPose() error {
	if s.m.shareLoading {
		return fmt.Errorf("re-entry must not enter the loading pose")
	}
	if strings.Contains(s.view(), "scanning the band for local models") {
		return fmt.Errorf("the scanning pose replaced the table on re-entry:\n%s", s.view())
	}
	return nil
}

func (s *reentryBDD) headerSaysRefreshing() error {
	if !strings.Contains(s.view(), "refreshing") {
		return fmt.Errorf("a quiet refresh should say so:\n%s", s.view())
	}
	return nil
}

func (s *reentryBDD) cursorOnModel() error {
	// Put the cursor on the SECOND model, so an index reset to 0 is detectable.
	for i, r := range s.m.shareRows {
		if r.model == "qwen3-vl-8b" {
			s.m.shareCursor = i
			s.curWas = r.model
			return nil
		}
	}
	return fmt.Errorf("fixture model missing")
}

func (s *reentryBDD) refreshLandsWithExtra() error {
	if err := s.leaveAndReturn(); err != nil {
		return err
	}
	out, _ := s.m.onSharesDetected(found("gpt-oss-20b", "llama-3.3-8b", "qwen3-vl-8b"), nil)
	s.m = asModel(out)
	return nil
}

func (s *reentryBDD) newModelAppears() error {
	if !strings.Contains(s.view(), "llama-3.3-8b") {
		return fmt.Errorf("the refreshed model is missing:\n%s", s.view())
	}
	return nil
}

func (s *reentryBDD) cursorStillOnIt() error {
	if s.m.shareCursor >= len(s.m.shareRows) || s.m.shareRows[s.m.shareCursor].model != s.curWas {
		got := "(out of range)"
		if s.m.shareCursor < len(s.m.shareRows) {
			got = s.m.shareRows[s.m.shareCursor].model
		}
		return fmt.Errorf("the refresh moved the cursor: was on %q, now on %q", s.curWas, got)
	}
	return nil
}

func (s *reentryBDD) modelOnAir() error {
	mm := &s.m
	for i, r := range s.m.shareRows {
		if r.model == "gpt-oss-20b" {
			mm.toggleShareAt(i)
			s.m = *mm
			if s.m.ctrl == nil || !s.m.ctrl.IsOnAir("gpt-oss-20b") {
				return fmt.Errorf("fixture could not put the model on air")
			}
			return nil
		}
	}
	return fmt.Errorf("fixture model missing")
}

func (s *reentryBDD) refreshLands() error {
	if err := s.leaveAndReturn(); err != nil {
		return err
	}
	out, _ := s.m.onSharesDetected(found("gpt-oss-20b", "qwen3-vl-8b"), nil)
	s.m = asModel(out)
	return nil
}

func (s *reentryBDD) stillOnAir() error {
	if s.m.ctrl == nil || !s.m.ctrl.IsOnAir("gpt-oss-20b") {
		return fmt.Errorf("the refresh took the station off air")
	}
	if !strings.Contains(s.view(), "ON-AIR") {
		return fmt.Errorf("the table no longer shows ON-AIR:\n%s", s.view())
	}
	return nil
}

func (s *reentryBDD) refreshLandsEmpty() error {
	if err := s.leaveAndReturn(); err != nil {
		return err
	}
	out, _ := s.m.onSharesDetected(nil, nil)
	s.m = asModel(out)
	return nil
}

func (s *reentryBDD) previousRowsStillShown() error { return s.tableAtOnce() }

func (s *reentryBDD) quietNothingNote() error {
	st := stripANSI(s.m.status)
	if !strings.Contains(st, "nothing") {
		return fmt.Errorf("an empty refresh should say so quietly, got status %q", st)
	}
	return nil
}

func (s *reentryBDD) notInWizard() error {
	if s.m.mode == modeShareSetup {
		return fmt.Errorf("an empty background refresh dropped the operator into the setup wizard")
	}
	return nil
}

func (s *reentryBDD) leaveBeforeRefresh() error {
	if err := s.leaveAndReturn(); err != nil {
		return err
	}
	// The operator hops away while the background refresh is still in flight...
	s.m.mode = modeBrowse
	// ...and THEN the result lands.
	out, _ := s.m.onSharesDetected(found("gpt-oss-20b", "qwen3-vl-8b"), nil)
	s.m = asModel(out)
	return nil
}

func (s *reentryBDD) foldedSilently() error {
	if len(s.m.shareRows) != 2 {
		return fmt.Errorf("the result should still fold into the catalog")
	}
	return nil
}

func (s *reentryBDD) screenUnchanged() error {
	if s.m.mode != modeBrowse {
		return fmt.Errorf("a late refresh yanked the operator to mode %d", s.m.mode)
	}
	return nil
}

func (s *reentryBDD) tableShowing() error {
	if s.m.mode != modeShare || len(s.m.shareRows) == 0 {
		return fmt.Errorf("expected the share table on screen")
	}
	return nil
}

func (s *reentryBDD) pressR() error {
	out, cmd := s.m.onShareKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	s.m = asModel(out)
	s.lastCmd = cmd
	return nil
}

func (s *reentryBDD) rowsStayDuringRescan() error {
	if err := s.tableAtOnce(); err != nil {
		return err
	}
	if strings.Contains(s.view(), "scanning the band for local models") && !strings.Contains(s.view(), "gpt-oss-20b") {
		return fmt.Errorf("the manual re-scan cleared the table")
	}
	return s.detectionStarted()
}

func TestShareReentryFeature(t *testing.T) {
	st := &reentryBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = reentryBDD{t: t}
				return c, nil
			})
			sc.Step(`^a share table was already scanned with models on it$`, st.scannedTable)
			sc.Step(`^no scan has happened yet this session$`, st.noScanYet)
			sc.Step(`^I open SHARE$`, st.openShare)
			sc.Step(`^the scanning pose is shown$`, st.scanningPoseShown)
			sc.Step(`^a detection is started$`, st.detectionStarted)
			sc.Step(`^I leave SHARE and come back$`, st.leaveAndReturn)
			sc.Step(`^the table renders at once with the previous rows$`, st.tableAtOnce)
			sc.Step(`^the scanning pose is not shown$`, st.noScanningPose)
			sc.Step(`^a detection is started in the background$`, st.detectionStarted)
			sc.Step(`^the header says it is refreshing$`, st.headerSaysRefreshing)
			sc.Step(`^the cursor is on a specific model$`, st.cursorOnModel)
			sc.Step(`^a background refresh lands with an extra model$`, st.refreshLandsWithExtra)
			sc.Step(`^the new model appears in the table$`, st.newModelAppears)
			sc.Step(`^the cursor is still on the model it was on$`, st.cursorStillOnIt)
			sc.Step(`^a model is ON-AIR$`, st.modelOnAir)
			sc.Step(`^a background refresh lands$`, st.refreshLands)
			sc.Step(`^that model still shows ON-AIR$`, st.stillOnAir)
			sc.Step(`^a background refresh lands with no servers found$`, st.refreshLandsEmpty)
			sc.Step(`^the previous rows are still shown$`, st.previousRowsStillShown)
			sc.Step(`^a quiet note says the re-scan found nothing$`, st.quietNothingNote)
			sc.Step(`^the operator is not dropped into the setup wizard$`, st.notInWizard)
			sc.Step(`^I leave SHARE before the background refresh lands$`, st.leaveBeforeRefresh)
			sc.Step(`^the refresh result is folded in silently$`, st.foldedSilently)
			sc.Step(`^the screen I am on does not change$`, st.screenUnchanged)
			sc.Step(`^the table is showing$`, st.tableShowing)
			sc.Step(`^I press r$`, st.pressR)
			sc.Step(`^the rows stay visible while the re-scan runs$`, st.rowsStayDuringRescan)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/sharing/share_reentry.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("share re-entry scenarios failed")
	}
}
