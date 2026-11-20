package catalog

// notes_bdd_test.go makes the @catalog scenarios of features/details/curated_catalog.feature
// EXECUTABLE: the REAL ParseNotes over defective files, and the SHIPPED notes against the
// website's page routes.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
)

func noteJSON(id, summary, page string) string {
	b, _ := json.Marshal(map[string]string{"id": id, "summary": summary, "best_at": "b", "not_for": "n", "page": page})
	return string(b)
}

// defects maps each spec row to the notes file that has it.
var defects = map[string]string{
	"an entry has no id":                           `[` + noteJSON("", "s", "/p.html") + `]`,
	"two entries share an id":                      `[` + noteJSON("a", "s", "/p.html") + `,` + noteJSON("a", "s", "/p.html") + `]`,
	"two ids differ only in case":                  `[` + noteJSON("a-b", "s", "/p.html") + `,` + noteJSON("A-B", "s", "/p.html") + `]`,
	"an entry has no summary":                      `[` + noteJSON("a", "", "/p.html") + `]`,
	"a summary has 141 characters":                 `[` + noteJSON("a", strings.Repeat("s", 141), "/p.html") + `]`,
	"a best_at has 201 characters":                 `[{"id":"a","summary":"s","best_at":"` + strings.Repeat("b", 201) + `"}]`,
	"a not_for has 201 characters":                 `[{"id":"a","summary":"s","not_for":"` + strings.Repeat("n", 201) + `"}]`,
	"a summary contains a control character":       `[` + noteJSON("a", "s\u001bx", "/p.html") + `]`,
	`a page is "https://example.invalid/x"`:        `[` + noteJSON("a", "s", "https://example.invalid/x") + `]`,
	`a page is "javascript:alert(1)"`:              `[` + noteJSON("a", "s", "javascript:alert(1)") + `]`,
	`a page is "//example.invalid/x"`:              `[` + noteJSON("a", "s", "//example.invalid/x") + `]`,
	`a page is "/research-x.html" with a space`:    `[` + noteJSON("a", "s", "/research x.html") + `]`,
	`a summary uses the reserved word "diagnosis"`: `[` + noteJSON("a", "Skin diagnosis.", "/p.html") + `]`,
	"the file is not valid JSON":                   `[{"id":`,
}

type notesState struct {
	body string
	err  error
}

func (s *notesState) defect(d string) error {
	b, ok := defects[d]
	if !ok {
		return fmt.Errorf("no fixture for defect %q", d)
	}
	s.body = b
	return nil
}
func (s *notesState) validate() error { _, s.err = ParseNotes([]byte(s.body)); return nil }
func (s *notesState) failsNaming(reason string) error {
	if s.err == nil || !strings.Contains(s.err.Error(), reason) {
		return fmt.Errorf("expected an error naming %q, got %v", reason, s.err)
	}
	return nil
}

// pagesExist holds every shipped page to a website route: "/research-wave-nano.html#x" is the
// route directory web/app/routes/research-wave-nano.
func (s *notesState) compare() error { return nil }
func (s *notesState) pagesExist() error {
	notes, err := Builtin()
	if err != nil {
		return err
	}
	if len(notes) == 0 {
		return fmt.Errorf("the shipped notes are empty")
	}
	for _, n := range notes {
		if n.Page == "" {
			continue
		}
		p, _, _ := strings.Cut(strings.TrimPrefix(n.Page, "/"), "#")
		dir := filepath.Join("..", "..", "web", "app", "routes", strings.TrimSuffix(p, ".html"))
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return fmt.Errorf("note %q: page %q has no website route %s", n.ID, n.Page, dir)
		}
	}
	return nil
}

func TestCuratedNotesBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &notesState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = notesState{}
				return ctx, nil
			})
			sc.Step(`^RogerAI's curated notes contain:$`, func(*godog.Table) error { return nil }) // Background, used by @broker
			sc.Step(`^a notes file where (.*)$`, st.defect)
			sc.Step(`^the notes are validated$`, st.validate)
			sc.Step(`^validation fails naming "(.*)"$`, st.failsNaming)
			sc.Step(`^the notes are compared with the built website pages$`, st.compare)
			sc.Step(`^every note's page path, without its fragment, names a page in the build$`, st.pagesExist)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/details/curated_catalog.feature"},
			Tags:     "@catalog",
			TestingT: t,
			Strict:   true,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("details @catalog behavior scenarios failed (see godog output above)")
	}
}
