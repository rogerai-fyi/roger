package tui

// taxonomy_share_bdd_test.go - the godog harness for features/categories/tui_share_editor.feature.
// It drives the REAL SHARE tab through Update over the model's REAL node.Controller (SetRows +
// syncShareCache, as detection does), and reads back the controller's declaration.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/node"
	"rogerai.fm/roger/v7/internal/protocol"
)

type shareTaxBDD struct {
	m     model
	rows  []node.ShareRow
	snap  node.Snapshot
	cycle []string
}

func catPair(t protocol.Taxonomy) string {
	if t.Subcategory == "" {
		return t.Category
	}
	return t.Category + "/" + t.Subcategory
}

func (s *shareTaxBDD) list(model, modality, inferred string) error {
	if s.m.ctrl == nil {
		s.m = browseSeed(160)
	}
	cat, sub := protocol.SplitCategory(inferred)
	s.rows = append(s.rows, node.ShareRow{Model: model, Modality: modality, Upstream: "http://127.0.0.1:8081/v1/chat/completions",
		Inferred: protocol.Taxonomy{Category: cat, Subcategory: sub, TaxonomySource: protocol.SourceInferred}})
	s.m.ctrl.SetRows(s.rows)
	s.m.syncShareCache()
	s.m.mode = modeShare
	return nil
}
func (s *shareTaxBDD) lists(model, inferred string) error { return s.list(model, "", inferred) }
func (s *shareTaxBDD) listsTTS(model string) error {
	return s.list(model, protocol.ModalityTTS, "audio/tts")
}

func (s *shareTaxBDD) declared(model, pair, tags string) error {
	cat, sub := protocol.SplitCategory(pair)
	var t []string
	if tags != "" {
		t = strings.Split(tags, ",")
	}
	if err := s.m.ctrl.SetMeta(model, protocol.Taxonomy{Category: cat, Subcategory: sub, Tags: t}); err != nil {
		return err
	}
	s.m.syncShareCache()
	return nil
}
func (s *shareTaxBDD) declaredPlain(model, pair string) error { return s.declared(model, pair, "") }
func (s *shareTaxBDD) declaredLicense(model, license string) error {
	if err := s.m.ctrl.SetMeta(model, protocol.Taxonomy{License: license}); err != nil {
		return err
	}
	s.m.syncShareCache()
	return nil
}

func (s *shareTaxBDD) view() string { return stripANSI(s.m.shareView(160)) }

func (s *shareTaxBDD) rowShows(model, want string) error {
	for _, line := range strings.Split(s.view(), "\n") {
		if strings.Contains(line, model) {
			if strings.Contains(line, want) {
				return nil
			}
			return fmt.Errorf("row %s shows %q, want category %q", model, line, want)
		}
	}
	return fmt.Errorf("no row %s on the SHARE tab", model)
}

func (s *shareTaxBDD) snapshot() error { s.snap = s.m.ctrl.Snapshot(); return nil }
func (s *shareTaxBDD) snapRow(model, declared, inferred string) error {
	for _, r := range s.snap.Rows {
		if r.Model != model {
			continue
		}
		var d, i string
		if r.Declared != nil {
			d = catPair(*r.Declared)
		}
		if r.Inferred != nil {
			i = catPair(*r.Inferred)
		}
		if d != declared || i != inferred {
			return fmt.Errorf("snapshot %s: declared %q inferred %q, got %q %q", model, declared, inferred, d, i)
		}
		return nil
	}
	return fmt.Errorf("no snapshot row %s", model)
}

func (s *shareTaxBDD) cursor(model string) error {
	for i, r := range s.m.shareRows {
		if r.model == model {
			s.m.shareCursor = i
			return nil
		}
	}
	return fmt.Errorf("no share row %s", model)
}

func mtKey(name string) tea.KeyMsg {
	switch name {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+x":
		return tea.KeyMsg{Type: tea.KeyCtrlX}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return keyMsg(name)
}

func (s *shareTaxBDD) send(msgs ...tea.KeyMsg) {
	var tm tea.Model = s.m
	for _, k := range msgs {
		tm, _ = tm.Update(k)
	}
	s.m = asModel(tm)
}

func (s *shareTaxBDD) press(name string) error { s.send(mtKey(name)); return nil }

func (s *shareTaxBDD) open(model string) error {
	if err := s.cursor(model); err != nil {
		return err
	}
	s.send(keyMsg("g"))
	return s.isOpen(model)
}
func (s *shareTaxBDD) openBlank(model string) error {
	if err := s.open(model); err != nil {
		return err
	}
	s.m.mtCat, s.m.mtSub = "", ""
	return nil
}
func (s *shareTaxBDD) isOpen(model string) error {
	if s.m.mode != modeShareMeta || s.m.mtModel != model {
		return fmt.Errorf("the category editor is not open for %s (mode %v, model %q, status %q)", model, s.m.mode, s.m.mtModel, stripANSI(s.m.status))
	}
	return nil
}
func (s *shareTaxBDD) closed() error {
	if s.m.mode != modeShare {
		return fmt.Errorf("the category editor is still open (mode %v)", s.m.mode)
	}
	return nil
}
func (s *shareTaxBDD) shows(cat, sub string) error {
	if s.m.mtCat != cat || s.m.mtSub != sub {
		return fmt.Errorf("editor shows %q/%q, want %q/%q", s.m.mtCat, s.m.mtSub, cat, sub)
	}
	if v := stripANSI(s.m.shareMetaView(160)); cat != "" && !strings.Contains(v, cat) {
		return fmt.Errorf("the editor view does not show %q:\n%s", cat, v)
	}
	return nil
}
func (s *shareTaxBDD) showsTags(tags string) error {
	if s.m.mtTags != tags {
		return fmt.Errorf("editor tags %q, want %q", s.m.mtTags, tags)
	}
	return nil
}
func (s *shareTaxBDD) guessNote() error {
	if v := stripANSI(s.m.shareMetaView(160)); !strings.Contains(v, "guess until saved") {
		return fmt.Errorf("the editor does not say the values are a guess:\n%s", v)
	}
	return nil
}
func (s *shareTaxBDD) notLoggedIn() error { s.m.loggedIn = false; return nil }

func (s *shareTaxBDD) focus(field int) {
	for i := 0; i < mtFieldCount && s.m.mtField != field; i++ {
		s.send(mtKey("tab"))
	}
}
func (s *shareTaxBDD) cycleField(name string, n int, field string) error {
	f := mtFieldCat
	if field == "subcategory" {
		f = mtFieldSub
	}
	s.focus(f)
	s.cycle = nil
	for i := 0; i < n; i++ {
		s.send(mtKey(name))
		s.cycle = append(s.cycle, s.m.mtCat)
	}
	return nil
}
func (s *shareTaxBDD) cycled(a, b, c, d, e, f string) error {
	want := []string{a, b, c, d, e, f, ""}
	if strings.Join(s.cycle, "|") != strings.Join(want, "|") {
		return fmt.Errorf("the category field went %q, want %q", s.cycle, want)
	}
	return nil
}
func (s *shareTaxBDD) typeTags(text string) error {
	s.focus(mtFieldTags)
	// As a terminal sends it: a space is its own key (tea.KeySpace), not a rune.
	for _, r := range text {
		if r == ' ' {
			s.send(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		s.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return nil
}
func (s *shareTaxBDD) holds(model, pair, tags string) error {
	d, _, _ := s.m.ctrl.MetaFor(model)
	if catPair(d) != pair || strings.Join(d.Tags, ",") != tags {
		return fmt.Errorf("controller holds %+v for %s", d, model)
	}
	return nil
}
func (s *shareTaxBDD) holdsNothing(model string) error {
	if d, _, _ := s.m.ctrl.MetaFor(model); d.Declared() {
		return fmt.Errorf("controller holds %+v for %s", d, model)
	}
	return nil
}

// onAir adopts a session into the REAL controller (the host-owned-session path), so the
// TUI's next sync sees the model on air exactly as it would a live one.
func (s *shareTaxBDD) onAir(model string) error {
	s.m.ctrl.Adopt(model, &agent.Session{})
	s.m.syncShareCache()
	return nil
}
func (s *shareTaxBDD) status(want string) error {
	if got := stripANSI(s.m.status); !strings.Contains(got, want) {
		return fmt.Errorf("status %q does not say %q", got, want)
	}
	return nil
}
func (s *shareTaxBDD) editorErr(want string) error {
	if !strings.Contains(s.m.mtErr, want) {
		return fmt.Errorf("editor error %q does not mention %q", s.m.mtErr, want)
	}
	return nil
}
func (s *shareTaxBDD) viewShare() error { s.m.mode = modeShare; return nil }
func (s *shareTaxBDD) hints(want string) error {
	if f := stripANSI(s.m.footer(160)); !strings.Contains(f, want) {
		return fmt.Errorf("the SHARE footer %q does not include %q", f, want)
	}
	return nil
}

func TestTaxonomyShareEditorBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &shareTaxBDD{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = shareTaxBDD{}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^the SHARE tab lists `+q+` inferred as `+q+`$`, st.lists)
			sc.Step(`^the SHARE tab lists the tts model `+q+`$`, st.listsTTS)
			sc.Step(`^`+q+` is declared as `+q+` with tags `+q+`$`, st.declared)
			sc.Step(`^`+q+` is declared as `+q+`$`, st.declaredPlain)
			sc.Step(`^`+q+` is declared with only the license `+q+`$`, st.declaredLicense)
			sc.Step(`^the operator views the SHARE tab$`, st.viewShare)
			sc.Step(`^the row `+q+` shows category `+q+`$`, st.rowShows)
			sc.Step(`^the node snapshot is taken$`, st.snapshot)
			sc.Step(`^the snapshot row `+q+` has declared `+q+` and inferred `+q+`$`, st.snapRow)
			sc.Step(`^the cursor is on the row `+q+`$`, st.cursor)
			sc.Step(`^the operator presses `+q+`$`, st.press)
			sc.Step(`^the category editor is open for `+q+` with nothing chosen$`, st.openBlank)
			sc.Step(`^the category editor is open for `+q+`$`, func(m string) error {
				if st.m.mode == modeShareMeta || st.m.mtModel != "" {
					return st.isOpen(m) // a Then
				}
				return st.open(m) // a Given
			})
			sc.Step(`^the category editor is closed$`, st.closed)
			sc.Step(`^the editor shows category `+q+` and subcategory `+q+`$`, st.shows)
			sc.Step(`^the editor shows tags `+q+`$`, st.showsTags)
			sc.Step(`^the editor says the values are a guess until saved$`, st.guessNote)
			sc.Step(`^the operator is not logged in$`, st.notLoggedIn)
			sc.Step(`^the operator presses `+q+` (\d+) times on the (category|subcategory) field$`, st.cycleField)
			sc.Step(`^the category field went `+q+`, `+q+`, `+q+`, `+q+`, `+q+`, `+q+`, then none$`, st.cycled)
			sc.Step(`^the operator types `+q+` into the tags field$`, st.typeTags)
			sc.Step(`^the controller holds for `+q+` a declaration `+q+` with tags `+q+`$`, st.holds)
			sc.Step(`^the controller holds no declaration for `+q+`$`, st.holdsNothing)
			sc.Step(`^`+q+` is on air$`, st.onAir)
			sc.Step(`^the status line says `+q+`$`, st.status)
			sc.Step(`^the editor shows the error `+q+`$`, st.editorErr)
			sc.Step(`^the key hints include `+q+`$`, st.hints)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/tui_share_editor.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/tui_share_editor behavior scenarios failed (see godog output above)")
	}
}
