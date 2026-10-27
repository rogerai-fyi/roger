package node

// declaration_bdd_test.go makes the controller half of features/categories/declaration.feature
// (every scenario not tagged @console) EXECUTABLE against the REAL Controller. Going on air is
// captured at the startAgent process-edge seam (as capabilities_bdd_test.go does): the
// agent.Config it receives is what agent.Start turns into the registered offer.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
)

type saveCall struct {
	model string
	t     protocol.Taxonomy
}

type declState struct {
	c      *Controller
	rows   []ShareRow
	meta   map[string]protocol.Taxonomy
	cfgs   map[string]agent.Config
	starts map[string]int
	saves  []saveCall
	err    error
}

func splitTagsCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func (s *declState) build() {
	s.c = New(Config{Broker: "http://broker.local", Station: "kitchen", Meta: s.meta, Hooks: Hooks{
		SaveMeta: func(model string, t protocol.Taxonomy) { s.saves = append(s.saves, saveCall{model, t}) },
	}})
	s.c.SetRows(s.rows)
}

func (s *declState) addRow(model, modality, inferred string) error {
	cat, sub := protocol.SplitCategory(inferred)
	s.rows = append(s.rows, ShareRow{Model: model, Modality: modality, Upstream: "http://127.0.0.1:8081/v1/chat/completions",
		Inferred: protocol.Taxonomy{Category: cat, Subcategory: sub, TaxonomySource: protocol.SourceInferred}})
	s.build()
	return nil
}
func (s *declState) background(model, inferred string) error {
	s.cfgs, s.starts = map[string]agent.Config{}, map[string]int{}
	return s.addRow(model, "", inferred)
}
func (s *declState) detected(model, inferred string) error { return s.addRow(model, "", inferred) }
func (s *declState) detectedTTS(model string) error {
	return s.addRow(model, protocol.ModalityTTS, "audio/tts")
}

func (s *declState) declare(model string, t protocol.Taxonomy) error {
	s.err = s.c.SetMeta(model, t)
	return nil
}
func (s *declState) declareCatTags(model, cat, tags string) error {
	c, sub := protocol.SplitCategory(cat)
	return s.declare(model, protocol.Taxonomy{Category: c, Subcategory: sub, Tags: splitTagsCSV(tags)})
}
func (s *declState) declareTags(model, tags string) error {
	return s.declare(model, protocol.Taxonomy{Tags: splitTagsCSV(tags)})
}
func (s *declState) declareTuning(model, v string) error {
	return s.declare(model, protocol.Taxonomy{Tuning: v})
}
func (s *declState) declarePolicy(model, v string) error {
	return s.declare(model, protocol.Taxonomy{ContentPolicy: v})
}
func (s *declState) declareAll(model, cat, tags, tuning, base, policy, license string) error {
	c, sub := protocol.SplitCategory(cat)
	return s.declare(model, protocol.Taxonomy{Category: c, Subcategory: sub, Tags: splitTagsCSV(tags),
		Tuning: tuning, BaseModel: base, ContentPolicy: policy, License: license})
}
func (s *declState) clear(model string) error { s.err = s.c.ClearMeta(model); return nil }

func (s *declState) savedInConfig(model, cat, src string) error {
	c, sub := protocol.SplitCategory(cat)
	s.meta = map[string]protocol.Taxonomy{model: {Category: c, Subcategory: sub, TaxonomySource: src}}
	return nil
}
func (s *declState) savedTax(model string, t protocol.Taxonomy) error {
	s.meta = map[string]protocol.Taxonomy{model: t}
	return nil
}
func (s *declState) savedTags(model, tags string) error {
	return s.savedTax(model, protocol.Taxonomy{Tags: splitTagsCSV(tags)})
}
func (s *declState) savedPolicy(model, p string) error {
	return s.savedTax(model, protocol.Taxonomy{ContentPolicy: p})
}
func (s *declState) startAndClear(model string) error { s.build(); return s.clear(model) }
func (s *declState) declareLong(model, field string, n int) error {
	t := protocol.Taxonomy{License: strings.Repeat("l", n)}
	if field == "base model" {
		t = protocol.Taxonomy{BaseModel: strings.Repeat("b", n)}
	}
	return s.declare(model, t)
}
func (s *declState) accepted() error {
	if s.err != nil {
		return fmt.Errorf("the declaration was refused: %v", s.err)
	}
	return nil
}
func (s *declState) startAndOnAir(model string) error { s.build(); return s.onAir(model) }

func (s *declState) onAir(model string) error {
	real := startAgent
	defer func() { startAgent = real }()
	startAgent = func(cfg agent.Config) (*agent.Session, error) {
		s.cfgs[cfg.Model] = cfg
		s.starts[cfg.Model]++
		return &agent.Session{}, nil
	}
	if res := s.c.ToggleOnAir(model); res.Err != nil {
		return fmt.Errorf("on air %q: %v", model, res.Err)
	}
	if _, ok := s.cfgs[model]; !ok {
		return fmt.Errorf("putting %q on air never reached the agent", model)
	}
	return nil
}

func (s *declState) onAirTax(model, cat, sub, src string) error {
	t := s.cfgs[model].Taxonomy
	if t.Category != cat || t.Subcategory != sub || t.TaxonomySource != src {
		return fmt.Errorf("on-air %s: %s/%s (%s), got %s/%s (%s)", model, cat, sub, src, t.Category, t.Subcategory, t.TaxonomySource)
	}
	return nil
}
func (s *declState) onAirTags(model, tags string) error {
	if got := strings.Join(s.cfgs[model].Taxonomy.Tags, ","); got != tags {
		return fmt.Errorf("on-air %s tags %q, got %q", model, tags, got)
	}
	return nil
}
func (s *declState) onAirFields(model, tuning, base, policy, license string) error {
	t := s.cfgs[model].Taxonomy
	if t.Tuning != tuning || t.BaseModel != base || t.ContentPolicy != policy || t.License != license {
		return fmt.Errorf("on-air %s: %+v", model, t)
	}
	return nil
}
func (s *declState) notRestarted(model string) error {
	if s.starts[model] != 1 {
		return fmt.Errorf("%q was started %d times, want once", model, s.starts[model])
	}
	return nil
}

func catOf(t protocol.Taxonomy) string {
	if t.Subcategory == "" {
		return t.Category
	}
	return t.Category + "/" + t.Subcategory
}

func (s *declState) savedCat(model, cat string) error {
	d, _, _ := s.c.MetaFor(model)
	if got := catOf(d); got != cat {
		return fmt.Errorf("saved declaration for %s: %q, got %q (err %v)", model, cat, got, s.err)
	}
	return nil
}
func (s *declState) savedTagsAre(model, tags string) error {
	d, _, _ := s.c.MetaFor(model)
	if got := strings.Join(d.Tags, ","); got != tags {
		return fmt.Errorf("saved tags for %s: %q, got %q", model, tags, got)
	}
	return nil
}
func (s *declState) refused(reason string) error {
	if s.err == nil || !strings.Contains(s.err.Error(), reason) {
		return fmt.Errorf("want a refusal mentioning %q, got %v", reason, s.err)
	}
	return nil
}
func (s *declState) nothingSaved(model string) error {
	if d, _, _ := s.c.MetaFor(model); d.Declared() {
		return fmt.Errorf("a declaration is held for %s: %+v", model, d)
	}
	for _, c := range s.saves {
		if c.model == model && c.t.Declared() {
			return fmt.Errorf("the host was asked to save %+v for %s", c.t, model)
		}
	}
	return nil
}
func (s *declState) lastSave(model string) (protocol.Taxonomy, error) {
	for i := len(s.saves) - 1; i >= 0; i-- {
		if s.saves[i].model == model {
			return s.saves[i].t, nil
		}
	}
	return protocol.Taxonomy{}, fmt.Errorf("the host was never asked to save for %s", model)
}
func (s *declState) hostSaved(model, cat, tags string) error {
	t, err := s.lastSave(model)
	if err != nil {
		return err
	}
	if catOf(t) != cat || strings.Join(t.Tags, ",") != tags {
		return fmt.Errorf("host saved for %s: %+v", model, t)
	}
	return nil
}
func (s *declState) hostRemoved(model string) error {
	t, err := s.lastSave(model)
	if err != nil {
		return err
	}
	if t.Declared() {
		return fmt.Errorf("host was asked to save %+v, not to remove", t)
	}
	return nil
}

func TestDeclarationBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &declState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = declState{}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a share controller with a detected model `+q+` inferred as `+q+`$`, st.background)
			sc.Step(`^a detected model `+q+` inferred as `+q+`$`, st.detected)
			sc.Step(`^a detected tts model `+q+`$`, st.detectedTTS)
			sc.Step(`^the operator declares for `+q+` category `+q+`, tags `+q+`, tuning `+q+`, base model `+q+`, content policy `+q+` and license `+q+`$`, st.declareAll)
			sc.Step(`^the operator declares for `+q+` category `+q+` and tags `+q+`$`, st.declareCatTags)
			sc.Step(`^the operator declares for `+q+` tags `+q+`$`, st.declareTags)
			sc.Step(`^the operator declares for `+q+` tuning `+q+`$`, st.declareTuning)
			sc.Step(`^the operator declares for `+q+` content policy `+q+`$`, st.declarePolicy)
			sc.Step(`^the operator clears the declaration for `+q+`$`, st.clear)
			sc.Step(`^config.json carries a saved declaration for `+q+` with category `+q+` and claim source `+q+`$`, st.savedInConfig)
			sc.Step(`^the controller starts and the operator puts `+q+` on air$`, st.startAndOnAir)
			sc.Step(`^the operator puts `+q+` on air$`, st.onAir)
			sc.Step(`^the on-air taxonomy for `+q+` is category `+q+`, subcategory `+q+` and source `+q+`$`, st.onAirTax)
			sc.Step(`^the on-air tags for `+q+` are `+q+`$`, st.onAirTags)
			sc.Step(`^the on-air offer for `+q+` carries tuning `+q+`, base model `+q+`, content policy `+q+` and license `+q+`$`, st.onAirFields)
			sc.Step(`^the live session for `+q+` was not restarted$`, st.notRestarted)
			sc.Step(`^the saved declaration for `+q+` is category `+q+`$`, st.savedCat)
			sc.Step(`^the saved tags for `+q+` are `+q+`$`, st.savedTagsAre)
			sc.Step(`^config.json carries a saved declaration for `+q+` with tags `+q+`$`, st.savedTags)
			sc.Step(`^config.json carries a saved declaration for `+q+` with content policy `+q+`$`, st.savedPolicy)
			sc.Step(`^the controller starts and the operator clears the declaration for `+q+`$`, st.startAndClear)
			sc.Step(`^the operator declares for `+q+` a (base model|license) of (\d+) characters$`, st.declareLong)
			sc.Step(`^the declaration was accepted$`, st.accepted)
			sc.Step(`^the declaration is refused with a reason mentioning `+q+`$`, st.refused)
			sc.Step(`^nothing was saved for `+q+`$`, st.nothingSaved)
			sc.Step(`^the host was asked to save for `+q+` category `+q+` and tags `+q+`$`, st.hostSaved)
			sc.Step(`^the host was asked to remove the declaration for `+q+`$`, st.hostRemoved)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/declaration.feature"},
			Tags:     "~@console",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/declaration (controller) behavior scenarios failed (see godog output above)")
	}
}
