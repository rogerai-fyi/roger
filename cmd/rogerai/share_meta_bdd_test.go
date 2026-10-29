package main

// share_meta_bdd_test.go makes features/categories/share_cli.feature EXECUTABLE: the REAL
// cmdShare (explicit --upstream path, which skips network detection, so the guess comes from
// the model id), with the go-live captured at the agentStart seam (captureShareConfig) and
// config.json read back from an isolated XDG_CONFIG_HOME.
//
// The seams are installed and the relay-fabric workers joined PER SCENARIO (Before/After)
// rather than through runShare/captureShareConfig, which tie both to a *testing.T: under godog
// every scenario shares one, and an invalid-flag scenario - which by design never reaches
// go-live - would otherwise leave nothing for runShare's join to wait on.

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/protocol"
)

type shareMetaState struct {
	t      *testing.T
	cancel context.CancelFunc
	wg     sync.WaitGroup
	undo   func()
	model  string
	cfg    config
	got    *agent.Config
	err    error
	ran    bool // a share has run in this scenario (so a config.json step is a Then)
	out    string
	before map[string]protocol.Taxonomy
}

func (s *shareMetaState) share(model, _ string) error { s.model = model; return nil }

func (s *shareMetaState) saved(model, cat, tags string) error {
	c, sub := protocol.SplitCategory(cat)
	if s.cfg.Meta == nil {
		s.cfg.Meta = map[string]protocol.Taxonomy{}
	}
	var t []string
	if tags != "" {
		t = strings.Split(tags, ",")
	}
	s.cfg.Meta[model] = protocol.Taxonomy{Category: c, Subcategory: sub, Tags: t}
	return saveConfig(s.cfg)
}

func (s *shareMetaState) run(flags string) error {
	s.ran = true
	s.before = loadConfig().Meta
	*s.got = agent.Config{}
	args := append([]string{s.model, "--upstream", "http://127.0.0.1:1234/v1"}, strings.Fields(flags)...)
	s.out = captureStdout(s.t, func() {
		done := make(chan error, 1)
		go func() { done <- cmdShare(s.cfg, args) }()
		select {
		case s.err = <-done:
		case <-time.After(20 * time.Second):
			s.err = fmt.Errorf("cmdShare did not return")
		}
	})
	return nil
}

// install points the go-live seams at stubs for one scenario: agentStart records the config
// it was handed, shareBlock returns, and every relay-fabric worker is tracked so After can
// cancel and join it before the scenario's config directory goes away.
func (s *shareMetaState) install() {
	// ISOLATION FIRST, for every scenario whatever its Givens: cmdShare reads config.json,
	// auth.json and the node key from the config dir, and the relay-fabric join acts on a
	// linked login it finds there. A scenario must never see the operator's real ones.
	s.t.Setenv("XDG_CONFIG_HOME", s.t.TempDir())
	s.model = "qwen3-vl-8b"
	s.cfg = config{Broker: "https://b", User: "u"}
	s.got = &agent.Config{}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	origStart, origBlock, origFabric := agentStart, shareBlock, startRelayFabric
	agentStart = func(c agent.Config) (*agent.Session, error) { *s.got = c; return &agent.Session{}, nil }
	shareBlock = func() {}
	startRelayFabric = func(cfg agent.Config) {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); joinRelayFabric(ctx, cfg) }()
	}
	s.undo = func() { agentStart, shareBlock, startRelayFabric = origStart, origBlock, origFabric }
}

func (s *shareMetaState) teardown() {
	s.cancel()
	s.wg.Wait()
	s.undo()
}

func (s *shareMetaState) offerTax(cat, sub, src string) error {
	if s.err != nil {
		return fmt.Errorf("share failed: %v", s.err)
	}
	t := s.got.Taxonomy
	if t.Category != cat || t.Subcategory != sub || t.TaxonomySource != src {
		return fmt.Errorf("offer %s/%s (%s), got %s/%s (%s)", cat, sub, src, t.Category, t.Subcategory, t.TaxonomySource)
	}
	return nil
}
func (s *shareMetaState) offerTags(tags string) error {
	if got := strings.Join(s.got.Taxonomy.Tags, ","); got != tags {
		return fmt.Errorf("offer tags %q, got %q", tags, got)
	}
	return nil
}
func (s *shareMetaState) offerFields(tuning, base, policy, license string) error {
	t := s.got.Taxonomy
	if t.Tuning != tuning || t.BaseModel != base || t.ContentPolicy != policy || t.License != license {
		return fmt.Errorf("offer fields: %+v", t)
	}
	return nil
}
func (s *shareMetaState) configHas(model, cat, tags string) error {
	t := loadConfig().Meta[model]
	got := t.Category
	if t.Subcategory != "" {
		got += "/" + t.Subcategory
	}
	if got != cat || strings.Join(t.Tags, ",") != tags {
		return fmt.Errorf("share_meta[%s] = %q %q, got %q %q", model, cat, tags, got, strings.Join(t.Tags, ","))
	}
	return nil
}
func (s *shareMetaState) failed(reason string) error {
	if s.err == nil || !strings.Contains(s.err.Error(), reason) {
		return fmt.Errorf("want a share error mentioning %q, got %v", reason, s.err)
	}
	return nil
}
func (s *shareMetaState) nothingOnAir() error {
	if s.got.Model != "" {
		return fmt.Errorf("%q went on air", s.got.Model)
	}
	return nil
}
func (s *shareMetaState) unchanged(model string) error {
	if after := loadConfig().Meta; !reflect.DeepEqual(after[model], s.before[model]) {
		return fmt.Errorf("share_meta[%s] changed: %+v -> %+v", model, s.before[model], after[model])
	}
	return nil
}
func (s *shareMetaState) helpLists(a, b, c, d, e, f string) error {
	for _, flag := range []string{a, b, c, d, e, f} {
		if !strings.Contains(s.out, flag) {
			return fmt.Errorf("--advanced output does not list %s: %q (err %v, model %q)", flag, s.out, s.err, s.got.Model)
		}
	}
	return nil
}

func TestShareMetaCLIBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &shareMetaState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = shareMetaState{t: t}
				st.install()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.teardown()
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a share of model `+q+` detected as `+q+`$`, st.share)
			sc.Step(`^config.json share_meta for `+q+` has category `+q+` and tags `+q+`$`, func(m, c, tg string) error {
				if !st.ran {
					return st.saved(m, c, tg) // a Given: seed it
				}
				return st.configHas(m, c, tg) // a Then: read it back
			})
			sc.Step(`^the operator runs share with `+q+`$`, st.run)
			sc.Step(`^the offer taxonomy is category `+q+`, subcategory `+q+` and source `+q+`$`, st.offerTax)
			sc.Step(`^the offer tags are `+q+`$`, st.offerTags)
			sc.Step(`^the offer carries tuning `+q+`, base model `+q+`, content policy `+q+` and license `+q+`$`, st.offerFields)
			sc.Step(`^share fails with an error mentioning `+q+`$`, st.failed)
			sc.Step(`^nothing went on air$`, st.nothingOnAir)
			sc.Step(`^config.json share_meta for `+q+` is unchanged$`, st.unchanged)
			sc.Step(`^the help lists `+q+`, `+q+`, `+q+`, `+q+`, `+q+` and `+q+`$`, st.helpLists)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/share_cli.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/share_cli behavior scenarios failed (see godog output above)")
	}
}
