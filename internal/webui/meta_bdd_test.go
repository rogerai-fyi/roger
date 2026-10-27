package webui

// meta_bdd_test.go makes the @console half of features/categories/declaration.feature
// EXECUTABLE: the REAL /api/share/meta route over the console's real Handler (token +
// localhost guard) and a real node.Controller.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/protocol"
)

type metaState struct {
	saves map[string][]protocol.Taxonomy
	ctrl  *node.Controller
	srv   *Server
	ts    *httptest.Server
	code  int
	body  []byte
}

func (s *metaState) background(model, inferred string) error {
	cat, sub := protocol.SplitCategory(inferred)
	s.saves = map[string][]protocol.Taxonomy{}
	s.ctrl = node.New(node.Config{Station: "kitchen", Hooks: node.Hooks{
		SaveMeta: func(m string, t protocol.Taxonomy) { s.saves[m] = append(s.saves[m], t) },
	}})
	s.ctrl.SetRows([]node.ShareRow{{Model: model, Upstream: "http://127.0.0.1:8081/v1/chat/completions",
		Inferred: protocol.Taxonomy{Category: cat, Subcategory: sub, TaxonomySource: protocol.SourceInferred}}})
	s.srv = New(s.ctrl, Options{})
	s.ts = httptest.NewServer(s.srv.Handler())
	return nil
}

func (s *metaState) declare(model, cat, tags string) error {
	c, sub := protocol.SplitCategory(cat)
	var t []string
	if tags != "" {
		t = strings.Split(tags, ",")
	}
	return s.ctrl.SetMeta(model, protocol.Taxonomy{Category: c, Subcategory: sub, Tags: t})
}

func (s *metaState) send(method, path string, body any, token bool) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.ts.URL+path, rd)
	if token {
		req.Header.Set("X-Roger-Token", s.srv.Token())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	s.code = resp.StatusCode
	s.body, _ = io.ReadAll(resp.Body)
	return nil
}

func (s *metaState) get(model string) error {
	return s.send(http.MethodGet, "/api/share/meta?model="+model, nil, true)
}
func (s *metaState) post(model, cat, tags string) error {
	var t []string
	if tags != "" {
		t = strings.Split(tags, ",")
	}
	return s.send(http.MethodPost, "/api/share/meta", map[string]any{"model": model, "category": cat, "tags": t}, true)
}
func (s *metaState) del(model string) error {
	return s.send(http.MethodDelete, "/api/share/meta?model="+model, nil, true)
}
func (s *metaState) put(model string) error {
	return s.send(http.MethodPut, "/api/share/meta?model="+model, nil, true)
}
func (s *metaState) noToken(method, model string) error {
	var body any
	if method == http.MethodPost {
		body = map[string]any{"model": model, "category": "vision/ocr"}
	}
	return s.send(method, "/api/share/meta?model="+model, body, false)
}

func (s *metaState) bothHalves(declared, inferred string) error {
	var v struct {
		Declared protocol.Taxonomy `json:"declared"`
		Inferred protocol.Taxonomy `json:"inferred"`
	}
	if err := json.Unmarshal(s.body, &v); err != nil {
		return fmt.Errorf("decode %s: %v", s.body, err)
	}
	cat := func(t protocol.Taxonomy) string { return t.Category + "/" + t.Subcategory }
	if cat(v.Declared) != declared || cat(v.Inferred) != inferred {
		return fmt.Errorf("declared %q inferred %q, got %q %q", declared, inferred, cat(v.Declared), cat(v.Inferred))
	}
	return nil
}
func (s *metaState) status(code int) error {
	if s.code != code {
		return fmt.Errorf("status %d, got %d: %s", code, s.code, s.body)
	}
	return nil
}
func (s *metaState) errMentions(r string) error {
	if !strings.Contains(string(s.body), r) {
		return fmt.Errorf("error %q does not mention %q", s.body, r)
	}
	return nil
}
func (s *metaState) savedCat(model, cat string) error {
	d, _, _ := s.ctrl.MetaFor(model)
	got := d.Category
	if d.Subcategory != "" {
		got += "/" + d.Subcategory
	}
	if got != cat {
		return fmt.Errorf("saved for %s: %q, got %q", model, cat, got)
	}
	return nil
}

// nothingSaved: no declaration is held in memory, and the host was never left holding one on
// disk (the last persisted value for the model, if any, is a removal).
func (s *metaState) nothingSaved(model string) error {
	if d, _, _ := s.ctrl.MetaFor(model); d.Declared() {
		return fmt.Errorf("a declaration is held for %s: %+v", model, d)
	}
	if v := s.saves[model]; len(v) > 0 && v[len(v)-1].Declared() {
		return fmt.Errorf("the host was left holding %+v for %s", v[len(v)-1], model)
	}
	return nil
}
func (s *metaState) hostRemoved(model string) error {
	v := s.saves[model]
	if len(v) == 0 || v[len(v)-1].Declared() {
		return fmt.Errorf("the host was not asked to remove the declaration for %s: %+v", model, v)
	}
	return nil
}

func TestShareMetaConsoleBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &metaState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = metaState{}
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.ts != nil {
					st.ts.Close()
				}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a share controller with a detected model `+q+` inferred as `+q+`$`, st.background)
			sc.Step(`^the operator declares for `+q+` category `+q+` and tags `+q+`$`, st.declare)
			sc.Step(`^the console GETs /api/share/meta for `+q+`$`, st.get)
			sc.Step(`^the console POSTs /api/share/meta for `+q+` with category `+q+` and tags `+q+`$`, st.post)
			sc.Step(`^the console DELETEs /api/share/meta for `+q+`$`, st.del)
			sc.Step(`^the console PUTs /api/share/meta for `+q+`$`, st.put)
			sc.Step(`^a request without the console token (GET|POST|DELETE)s /api/share/meta for `+q+`$`, st.noToken)
			sc.Step(`^the console response has declared category `+q+` and inferred category `+q+`$`, st.bothHalves)
			sc.Step(`^the console response status is (\d+)$`, st.status)
			sc.Step(`^the console error mentions `+q+`$`, st.errMentions)
			sc.Step(`^the saved declaration for `+q+` is category `+q+`$`, st.savedCat)
			sc.Step(`^nothing was saved for `+q+`$`, st.nothingSaved)
			sc.Step(`^the host was asked to remove the declaration for `+q+`$`, st.hostRemoved)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/declaration.feature"},
			Tags:     "@console",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/declaration (console) behavior scenarios failed (see godog output above)")
	}
}
