package detect

// inference_bdd_test.go makes features/categories/inference.feature EXECUTABLE against the
// REAL InferTaxonomy and a real ProbeKey scan of an httptest OpenAI-compatible server.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
)

type inferState struct {
	model, modality string
	caps            []string
	got             protocol.Taxonomy
	srv             *httptest.Server
	found           Found
}

func (s *inferState) detected(model, modality, caps string) error {
	s.model, s.modality = model, modality
	s.caps = nil
	if caps != "" {
		s.caps = strings.Split(caps, ",")
	}
	return nil
}

func (s *inferState) infer() error { s.got = InferTaxonomy(s.model, s.modality, s.caps); return nil }

func (s *inferState) catIs(cat, sub string) error {
	if s.got.Category != cat || s.got.Subcategory != sub {
		return fmt.Errorf("%q: inferred %s/%s, got %s/%s", s.model, cat, sub, s.got.Category, s.got.Subcategory)
	}
	return nil
}
func (s *inferState) sourceIs(src string) error {
	if s.got.TaxonomySource != src {
		return fmt.Errorf("%q: source %q, got %q", s.model, src, s.got.TaxonomySource)
	}
	return nil
}
func (s *inferState) policyIs(p string) error {
	if s.got.ContentPolicy != p {
		return fmt.Errorf("%q: content policy %q, got %q", s.model, p, s.got.ContentPolicy)
	}
	return nil
}
func (s *inferState) noExtras() error {
	g := s.got
	if len(g.Tags) != 0 || g.Tuning != "" || g.BaseModel != "" || g.License != "" {
		return fmt.Errorf("inference invented fields: %+v", g)
	}
	return nil
}
func (s *inferState) empty() error {
	if !reflect.DeepEqual(s.got, protocol.Taxonomy{}) {
		return fmt.Errorf("want an empty taxonomy, got %+v", s.got)
	}
	return nil
}
func (s *inferState) canonical() error {
	c := s.got.Canonical(s.modality)
	if !reflect.DeepEqual(c, s.got) {
		return fmt.Errorf("normalizing changed the inference: %+v -> %+v", s.got, c)
	}
	return nil
}

func (s *inferState) server(a, b string) error {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": a}, {"id": b}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	return nil
}
func (s *inferState) scan() error {
	f, _ := ProbeKey(s.srv.URL+"/v1", "")
	s.found = f
	if len(f.Models) == 0 {
		return fmt.Errorf("the scan found no models")
	}
	return nil
}
func (s *inferState) scanInferred(model, want string) error {
	t := s.found.InferredTaxonomy(model)
	if got := t.Category + "/" + t.Subcategory; got != want {
		return fmt.Errorf("scan inferred %q for %q, got %q", want, model, got)
	}
	return nil
}

func TestInferenceBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &inferState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = inferState{}
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.srv != nil {
					st.srv.Close()
				}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a detected model `+q+` with modality `+q+` and capabilities `+q+`$`, st.detected)
			sc.Step(`^the node infers its taxonomy$`, st.infer)
			sc.Step(`^the inferred category is `+q+` and subcategory `+q+`$`, st.catIs)
			sc.Step(`^the inferred claim source is `+q+`$`, st.sourceIs)
			sc.Step(`^the inferred content policy is `+q+`$`, st.policyIs)
			sc.Step(`^the inferred taxonomy has no tags, tuning, base model or license$`, st.noExtras)
			sc.Step(`^the inferred taxonomy is empty$`, st.empty)
			sc.Step(`^normalizing the inferred taxonomy changes nothing$`, st.canonical)
			sc.Step(`^a local server that serves `+q+` and `+q+`$`, st.server)
			sc.Step(`^the node scans for local models$`, st.scan)
			sc.Step(`^the scan's inferred taxonomy for `+q+` is `+q+`$`, st.scanInferred)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/inference.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/inference behavior scenarios failed (see godog output above)")
	}
}
