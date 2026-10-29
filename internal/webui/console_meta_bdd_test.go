package webui

// console_meta_bdd_test.go makes features/categories/console_editor.feature EXECUTABLE: the
// REAL state and categories routes over the console's real Handler, and the console page and
// script as served (the embedded assets) for the wiring the browser runs.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/node"
	"rogerai.fm/roger/v7/internal/protocol"
)

type consoleMetaState struct {
	srv  *Server
	ts   *httptest.Server
	body []byte
}

func (s *consoleMetaState) station(model, inferred string) error {
	cat, sub := protocol.SplitCategory(inferred)
	c := node.New(node.Config{Station: "kitchen"})
	c.SetRows([]node.ShareRow{{Model: model, Upstream: "http://127.0.0.1:8081/v1/chat/completions",
		Inferred: protocol.Taxonomy{Category: cat, Subcategory: sub, TaxonomySource: protocol.SourceInferred}}})
	s.srv = New(c, Options{})
	s.ts = httptest.NewServer(s.srv.Handler())
	return nil
}

func (s *consoleMetaState) get(path string) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	resp, err := http.Get(s.ts.URL + path + sep + "t=" + s.srv.Token())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	s.body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s = %d: %s", path, resp.StatusCode, s.body)
	}
	return nil
}

func (s *consoleMetaState) readState() error { return s.get("/api/state") }
func (s *consoleMetaState) stateRow(model, inferred string) error {
	var snap node.Snapshot
	if err := json.Unmarshal(s.body, &snap); err != nil {
		return err
	}
	for _, r := range snap.Rows {
		if r.Model == model {
			if r.Inferred == nil || r.Inferred.Category+"/"+r.Inferred.Subcategory != inferred {
				return fmt.Errorf("state row %s inferred %+v, want %s", model, r.Inferred, inferred)
			}
			if r.Declared != nil {
				return fmt.Errorf("state row %s carries a declaration %+v", model, r.Declared)
			}
			return nil
		}
	}
	return fmt.Errorf("no state row %s", model)
}

func (s *consoleMetaState) page() error   { return s.get("/") }
func (s *consoleMetaState) script() error { return s.get("/assets/console.js") }

func (s *consoleMetaState) has(re string) error {
	if !regexp.MustCompile(re).Match(s.body) {
		return fmt.Errorf("the served asset does not match %s", re)
	}
	return nil
}

func (s *consoleMetaState) column(name string) error {
	return s.has(`<th[^>]*>` + strings.ToUpper(name) + `</th>`)
}
func (s *consoleMetaState) action(name, beside string) error {
	if err := s.script(); err != nil {
		return err
	}
	if err := s.has(`"btn small ghost", "` + name + `"\);\s*\w+\.setAttribute\("data-act", "category"\)`); err != nil {
		return err
	}
	// BESIDE it: appended to the same actions group right after the Price button.
	return s.has(`actions\.appendChild\(` + strings.ToLower(beside) + `Btn\);\s*actions\.appendChild\(catBtn\)`)
}
func (s *consoleMetaState) uses(method string) error {
	if method == "POST" {
		return s.has(`apiPost\("/api/share/meta"`)
	}
	return s.has(`api\("` + method + `", "/api/share/meta\?model="`)
}
func (s *consoleMetaState) readCategories() error { return s.get("/api/categories") }
func (s *consoleMetaState) lists(names string) error {
	var v struct {
		Categories []struct {
			Name          string   `json:"name"`
			Subcategories []string `json:"subcategories"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(s.body, &v); err != nil {
		return err
	}
	var got []string
	for _, c := range v.Categories {
		got = append(got, c.Name)
		if len(c.Subcategories) == 0 {
			return fmt.Errorf("category %s lists no subcategories", c.Name)
		}
	}
	if g := strings.Join(got, ", "); g != names {
		return fmt.Errorf("categories %q, want %q", g, names)
	}
	return nil
}
func (s *consoleMetaState) guessMark(mark string) error {
	return s.has(`row\.inferred[^\n]*\+ "` + regexp.QuoteMeta(mark) + `"`)
}
func (s *consoleMetaState) refusalShown() error {
	return s.has(`\$\("cat-err"\)\.textContent = e\.message`)
}

// modalSays checks the text is INSIDE the category modal (between its id and its last button).
func (s *consoleMetaState) modalSays(text string) error {
	return s.has(`id="modal-cat"[^§]*?` + regexp.QuoteMeta(text) + `[^§]*?id="cat-save"`)
}

func TestConsoleMetaBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &consoleMetaState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = consoleMetaState{}
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.ts != nil {
					st.ts.Close()
				}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^the console serves a station sharing `+q+` inferred as `+q+`$`, st.station)
			sc.Step(`^the console reads its state$`, st.readState)
			sc.Step(`^the state row `+q+` has inferred category `+q+` and no declaration$`, st.stateRow)
			sc.Step(`^the console page is served$`, st.page)
			sc.Step(`^the console script is served$`, st.script)
			sc.Step(`^the share table has a `+q+` column$`, st.column)
			sc.Step(`^each row offers a `+q+` action beside `+q+`$`, st.action)
			sc.Step(`^it (?:reads a model's taxonomy|saves|clears) with (GET|POST|DELETE) /api/share/meta$`, st.uses)
			sc.Step(`^the console reads the category list$`, st.readCategories)
			sc.Step(`^it lists `+q+` with their subcategories$`, st.lists)
			sc.Step(`^a category it did not get from a declaration is rendered with the guess mark `+q+`$`, st.guessMark)
			sc.Step(`^a refused save shows the server's reason in the modal$`, st.refusalShown)
			sc.Step(`^the category modal says `+q+`$`, st.modalSays)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/console_editor.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/console_editor behavior scenarios failed (see godog output above)")
	}
}
