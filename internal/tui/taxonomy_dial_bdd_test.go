package tui

// taxonomy_dial_bdd_test.go - the godog harness for features/categories/tui_dial.feature. It
// drives the REAL dial: offers grouped by groupBands, the badge and legend builders, the band
// detail view, and the G key through Update - no seams.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/protocol"
)

type dialTaxBDD struct {
	m      model
	offers []offer
	seen   []string
	detail string
}

func (s *dialTaxBDD) regroup() {
	s.m.bands = groupBands(s.offers, nil)
	s.m.scanned = true
}

func stationOffer(id, model, cat, sub, src, tags, policy string) offer {
	o := offer{NodeID: id, Model: model, Online: true, FreeNow: true}
	o.Taxonomy = protocol.Taxonomy{Category: cat, Subcategory: sub, TaxonomySource: src, ContentPolicy: policy}
	if tags != "" {
		o.Tags = strings.Split(tags, ",")
	}
	return o
}

func (s *dialTaxBDD) stations(t *godog.Table) error {
	s.m = browseSeed(140)
	s.offers = nil
	for _, r := range t.Rows[1:] {
		c := r.Cells
		s.offers = append(s.offers, stationOffer(c[0].Value, c[1].Value, c[2].Value, c[3].Value, c[4].Value, c[5].Value, c[6].Value))
	}
	s.regroup()
	return nil
}

func (s *dialTaxBDD) alsoServes(id, model, pair string) error {
	cat, sub := protocol.SplitCategory(pair)
	s.offers = append(s.offers, stationOffer(id, model, cat, sub, protocol.SourceDeclared, "", ""))
	s.regroup()
	return nil
}

func (s *dialTaxBDD) alsoServesAs(kind, id, model, pair string) error {
	cat, sub := protocol.SplitCategory(pair)
	o := stationOffer(id, model, cat, sub, protocol.SourceDeclared, "", "")
	switch kind {
	case "curated":
		o.Curated, o.CuratedProvider = true, "upstream"
	case "offline":
		o.Online = false
	}
	s.offers = append(s.offers, o)
	s.regroup()
	return nil
}

func (s *dialTaxBDD) band(model string) (band, error) {
	for _, b := range s.m.bands {
		if b.model == model {
			return b, nil
		}
	}
	return band{}, fmt.Errorf("no band %q on the dial", model)
}

func (s *dialTaxBDD) look() error { return nil }

func (s *dialTaxBDD) labelled(model, want string) error {
	b, err := s.band(model)
	if err != nil {
		return err
	}
	if got := bandCategoryLabel(b); got != want {
		return fmt.Errorf("band %s labelled %q, got %q", model, want, got)
	}
	if badge := plainBandBadge(b, &LimitStore{}, false); !strings.Contains(badge, want) {
		return fmt.Errorf("band %s's row does not show %q: %q", model, want, badge)
	}
	return nil
}
func (s *dialTaxBDD) noLabel(model string) error {
	b, err := s.band(model)
	if err != nil {
		return err
	}
	if got := bandCategoryLabel(b); got != "" {
		return fmt.Errorf("band %s has a label %q", model, got)
	}
	return nil
}
func (s *dialTaxBDD) grade(model, want string) error {
	b, err := s.band(model)
	if err != nil {
		return err
	}
	if badge := plainBandBadge(b, &LimitStore{}, false); !strings.Contains(badge, want) {
		return fmt.Errorf("band %s's row does not show grade %q: %q", model, want, badge)
	}
	return nil
}
func (s *dialTaxBDD) noGrade(model string) error {
	b, err := s.band(model)
	if err != nil {
		return err
	}
	badge := plainBandBadge(b, &LimitStore{}, false)
	for _, g := range []string{"uncensored", "reduced-refusal", "aligned"} {
		if strings.Contains(badge, g) {
			return fmt.Errorf("band %s shows grade %q: %q", model, g, badge)
		}
	}
	return nil
}
func (s *dialTaxBDD) legend(mark, text string) error {
	if l := stripANSI(bandBadgeLegend()); !strings.Contains(l, mark+" "+text) {
		return fmt.Errorf("legend %q does not explain %q as %q", l, mark, text)
	}
	return nil
}

func (s *dialTaxBDD) open(model string) error {
	b, err := s.band(model)
	if err != nil {
		return err
	}
	s.m.detailBand = b
	s.detail = stripANSI(s.m.bandDetailView(160))
	return nil
}
func (s *dialTaxBDD) detailCat(pair string, n, of int) error {
	want := fmt.Sprintf("%s · declared by %d of %d stations", pair, n, of)
	if !strings.Contains(s.detail, want) {
		return fmt.Errorf("detail lacks %q:\n%s", want, s.detail)
	}
	return nil
}
func (s *dialTaxBDD) detailHas(what, v string) error {
	want := what + " " + v
	if !strings.Contains(s.detail, want) {
		return fmt.Errorf("detail lacks %q:\n%s", want, s.detail)
	}
	return nil
}
func (s *dialTaxBDD) detailSays(v string) error {
	if !strings.Contains(s.detail, v) {
		return fmt.Errorf("detail lacks %q:\n%s", v, s.detail)
	}
	return nil
}

func (s *dialTaxBDD) press(key string, n int) error {
	var tm tea.Model = s.m
	s.seen = nil
	for i := 0; i < n; i++ {
		tm, _ = tm.Update(keyMsg(key))
		s.seen = append(s.seen, asModel(tm).fCat)
	}
	s.m = asModel(tm)
	return nil
}
func (s *dialTaxBDD) cycle(a, b, c string) error {
	want := []string{a, b, c, ""}
	if strings.Join(s.seen, "|") != strings.Join(want, "|") {
		return fmt.Errorf("the category filter went %q, want %q", s.seen, want)
	}
	return nil
}
func (s *dialTaxBDD) visible(want string) error {
	var got []string
	for _, b := range s.m.visibleBands() {
		got = append(got, b.model)
	}
	sort.Strings(got)
	w := []string{}
	if want != "" {
		w = strings.Split(want, ",")
	}
	sort.Strings(w)
	if strings.Join(got, ",") != strings.Join(w, ",") {
		return fmt.Errorf("visible bands %q, got %q", w, got)
	}
	return nil
}
func (s *dialTaxBDD) header(want string) error {
	vis := s.m.visibleBands()
	if l := stripANSI(s.m.filterLine(len(vis), len(s.m.bands))); !strings.Contains(l, want) {
		return fmt.Errorf("the dial header %q does not show %q", l, want)
	}
	return nil
}
func (s *dialTaxBDD) notVisible(model string) error {
	for _, b := range s.m.visibleBands() {
		if b.model == model {
			return fmt.Errorf("band %s is visible", model)
		}
	}
	return nil
}
func (s *dialTaxBDD) freeOn() error { s.m.fFree = true; return nil }
func (s *dialTaxBDD) charges(id string) error {
	for i := range s.offers {
		if s.offers[i].NodeID == id {
			s.offers[i].FreeNow, s.offers[i].PriceIn, s.offers[i].PriceOut = false, 1, 2
		}
	}
	s.regroup()
	return nil
}
func (s *dialTaxBDD) onlyUncategorized() error {
	s.m = browseSeed(140)
	s.offers = []offer{stationOffer("u1", "plain-a", "", "", "", "", ""), stationOffer("u2", "plain-b", "", "", "", "", "")}
	s.regroup()
	return nil
}
func (s *dialTaxBDD) filterOff() error {
	if s.m.fCat != "" {
		return fmt.Errorf("the category filter is %q, want off", s.m.fCat)
	}
	return nil
}
func (s *dialTaxBDD) status(want string) error {
	if got := stripANSI(s.m.status); !strings.Contains(got, want) {
		return fmt.Errorf("status %q does not say %q", got, want)
	}
	return nil
}
func (s *dialTaxBDD) cursorOn(model string) error {
	for i, b := range s.m.visibleBands() {
		if b.model == model {
			s.m.cursor, s.m.selectedModel = i, model
			return nil
		}
	}
	return fmt.Errorf("band %s is not visible to put the cursor on", model)
}
func (s *dialTaxBDD) cursorIs(model string) error {
	vis := s.m.visibleBands()
	if s.m.cursor < 0 || s.m.cursor >= len(vis) || vis[s.m.cursor].model != model {
		return fmt.Errorf("cursor %d is not on %s (visible %d)", s.m.cursor, model, len(vis))
	}
	return nil
}

func TestTaxonomyDialBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &dialTaxBDD{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = dialTaxBDD{}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^the dial shows these stations:$`, st.stations)
			sc.Step(`^station `+q+` also serves `+q+` as `+q+` declared$`, st.alsoServes)
			sc.Step(`^(curated|offline) station `+q+` also serves `+q+` as `+q+` declared$`, st.alsoServesAs)
			sc.Step(`^the operator looks at the dial$`, st.look)
			sc.Step(`^the band `+q+` is labelled `+q+`$`, st.labelled)
			sc.Step(`^the band `+q+` has no category label$`, st.noLabel)
			sc.Step(`^the band `+q+` shows the grade `+q+`$`, st.grade)
			sc.Step(`^the band `+q+` shows no grade badge$`, st.noGrade)
			sc.Step(`^the badge legend explains `+q+` as `+q+`$`, st.legend)
			sc.Step(`^the operator opens the band `+q+`$`, st.open)
			sc.Step(`^the detail shows category `+q+` declared by (\d+) of (\d+) stations$`, st.detailCat)
			sc.Step(`^the detail shows (tags|content policies) `+q+`$`, st.detailHas)
			sc.Step(`^the detail says `+q+`$`, st.detailSays)
			sc.Step(`^the operator presses `+q+` (\d+) times$`, st.press)
			sc.Step(`^the category filter went `+q+`, `+q+`, `+q+`, then off$`, st.cycle)
			sc.Step(`^the visible bands are `+q+`$`, st.visible)
			sc.Step(`^the dial header shows `+q+`$`, st.header)
			sc.Step(`^the band `+q+` is not visible$`, st.notVisible)
			sc.Step(`^the free filter is on$`, st.freeOn)
			sc.Step(`^station `+q+` charges for its band$`, st.charges)
			sc.Step(`^the dial shows only uncategorized stations$`, st.onlyUncategorized)
			sc.Step(`^the category filter is off$`, st.filterOff)
			sc.Step(`^the status line says `+q+`$`, st.status)
			sc.Step(`^the cursor is on the band `+q+`$`, func(m string) error {
				if st.seen == nil {
					return st.cursorOn(m) // a Given
				}
				return st.cursorIs(m) // a Then, after a key press
			})
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/tui_dial.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/tui_dial behavior scenarios failed (see godog output above)")
	}
}
