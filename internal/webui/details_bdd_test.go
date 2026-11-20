package webui

// details_bdd_test.go makes the @webui scenarios of features/details EXECUTABLE: the REAL
// /api/browse (over an httptest broker) and /api/share/meta routes on the console's real
// Handler and a real node.Controller, and the console's details script read as shipped.
// The DOM behaviour of that script is web/test/details.test.mjs.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/catalog"
	"rogerai.fm/roger/v7/internal/protocol"
)

type detailsWebState struct {
	meta   metaState
	offers []map[string]any
	raw    bool
	broker *httptest.Server
	srv    *httptest.Server
	got    []map[string]any
	src    string
}

func unescWeb(v string) string {
	if u, err := strconv.Unquote(`"` + strings.ReplaceAll(v, `"`, `\"`) + `"`); err == nil {
		return u
	}
	return v
}

func (s *detailsWebState) onAir(t *godog.Table) error {
	head := t.Rows[0].Cells
	for _, row := range t.Rows[1:] {
		o := map[string]any{"online": true}
		tx := protocol.Taxonomy{}
		for i, c := range row.Cells {
			v := strings.TrimSpace(c.Value)
			switch head[i].Value {
			case "node":
				o["node_id"] = v
			case "model":
				o["model"] = v
			case "summary":
				tx.Summary = v
			case "best_at":
				tx.BestAt = v
			case "not_for":
				tx.NotFor = v
			case "base_model":
				tx.BaseModel = v
			case "tuning":
				tx.Tuning = v
			case "license":
				tx.License = v
			}
		}
		o["tx"] = tx
		s.offers = append(s.offers, o)
	}
	return nil
}

// notes stamps a curated note the way the broker does (catalog.Notes.Apply).
func (s *detailsWebState) notes(string) error {
	b, _ := json.Marshal([]catalog.Note{{ID: "demo-1b-fire-decision", Summary: "Picks one fire-defense action from site state.",
		BestAt: "Choosing among a fixed list of actions.", NotFor: "Chat. It answers with one action only.",
		BaseModel: "demo-1b", License: "Apache-2.0", Page: "/research-demo-1b.html#fire"}})
	ns, err := catalog.ParseNotes(b)
	for _, o := range s.offers {
		o["tx"] = ns.Apply(o["model"].(string), o["tx"].(protocol.Taxonomy).Canonical("chat"))
	}
	return err
}

func (s *detailsWebState) published() []map[string]any {
	var out []map[string]any
	for _, o := range s.offers {
		b, _ := json.Marshal(o["tx"])
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		for k, v := range o {
			if k != "tx" {
				m[k] = v
			}
		}
		out = append(out, m)
	}
	return out
}

func (s *detailsWebState) readBrowse() error {
	s.broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": s.published()})
	}))
	srv := New(s.meta.ctrl, Options{Broker: s.broker.URL})
	s.srv = httptest.NewServer(srv.Handler())
	resp, err := http.Get(s.srv.URL + "/api/browse?t=" + srv.Token())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/api/browse = %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(&s.got)
}
func (s *detailsWebState) carries(a, b, c, d, e string) error {
	sent := s.published()
	for i, o := range s.got {
		for _, k := range []string{a, b, c, d, e} {
			if want, ok := sent[i][k]; ok && o[k] != want {
				return fmt.Errorf("offer %v: %s %v, broker sent %v", o["model"], k, o[k], want)
			}
		}
	}
	for _, o := range s.got {
		if o["model"] == "demo-1b-fire-decision" && (o["details_source"] != "rogerai" || o["page"] != "/research-demo-1b.html#fire") {
			return fmt.Errorf("the curated offer lost its source or page: %v", o)
		}
	}
	return nil
}

// re-canonicalized on read: the console's half.
func (s *detailsWebState) oldBroker() error { s.raw = true; return nil }
func (s *detailsWebState) itsOffer(model, sum string) error {
	s.offers = append(s.offers, map[string]any{"node_id": "o1", "model": model, "online": true,
		"tx": protocol.Taxonomy{Summary: unescWeb(sum)}})
	return nil
}
func (s *detailsWebState) eachShows(v string) error {
	for _, o := range s.got {
		if o["summary"] == v {
			return nil
		}
	}
	return fmt.Errorf("/api/browse summary is not %q: %v", v, s.got)
}

// the console's details source, as shipped.
func (s *detailsWebState) inspect() error {
	b, err := os.ReadFile("assets/console-details.js")
	s.src = string(b)
	return err
}
func (s *detailsWebState) noMarkup(a, b, c string) error {
	for _, banned := range []string{a, b, c} {
		if strings.Contains(s.src, banned) {
			return fmt.Errorf("console-details.js contains %q", banned)
		}
	}
	if !strings.Contains(s.src, "textContent") {
		return fmt.Errorf("console-details.js never sets textContent")
	}
	return nil
}

// meta (declaration.feature)
func (s *detailsWebState) saveSummary(sum string) error {
	return s.meta.send(http.MethodPost, "/api/share/meta", map[string]any{"model": "demo-1b-fire-decision", "summary": sum}, true)
}
func (s *detailsWebState) carried(sum string) error {
	var r struct {
		Declared protocol.Taxonomy `json:"declared"`
	}
	_ = json.Unmarshal(s.meta.body, &r)
	if s.meta.code != http.StatusOK || r.Declared.Summary != sum {
		return fmt.Errorf("/api/share/meta = %d %s", s.meta.code, s.meta.body)
	}
	return nil
}
func (s *detailsWebState) saved(model, sum string) error {
	d, _, _ := s.meta.ctrl.MetaFor(model)
	if d.Summary != sum {
		return fmt.Errorf("saved summary %q, want %q", d.Summary, sum)
	}
	return nil
}
func (s *detailsWebState) postLong(n int) error { return s.saveSummary(strings.Repeat("s", n)) }
func (s *detailsWebState) badRequest(msg string) error {
	if s.meta.code != http.StatusBadRequest || !strings.Contains(string(s.meta.body), msg) {
		return fmt.Errorf("expected 400 %q, got %d %s", msg, s.meta.code, s.meta.body)
	}
	return nil
}

func TestDetailsConsoleBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &detailsWebState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = detailsWebState{}
				_ = st.meta.background("demo-1b-fire-decision", "language/chat")
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				for _, ts := range []*httptest.Server{st.broker, st.srv, st.meta.ts} {
					if ts != nil {
						ts.Close()
					}
				}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a share controller with a detected model `+q+` inferred as `+q+`$`, func(string, string) error { return nil })
			sc.Step(`^these on-air offers:$`, st.onAir)
			sc.Step(`^RogerAI's curated notes describe (.*) as in curated_catalog\.feature$`, st.notes)
			sc.Step(`^a client reads /api/browse$`, st.readBrowse)
			sc.Step(`^each offer carries `+q+`, `+q+`, `+q+`, `+q+` and `+q+` when the broker sent them$`, st.carries)
			sc.Step(`^a broker that does not normalize details \(an older or third-party broker\)$`, st.oldBroker)
			sc.Step(`^its /discover offer for `+q+` has summary "(.*)"$`, st.itsOffer)
			sc.Step(`^the TUI, the local console and the CLI read that offer$`, st.readBrowse)
			sc.Step(`^each shows the summary "(.*)"$`, st.eachShows)
			sc.Step(`^the console's details source is inspected$`, st.inspect)
			sc.Step(`^it contains no `+q+`, `+q+` or `+q+`$`, st.noMarkup)
			sc.Step(`^the operator saves summary `+q+` in the console modal$`, st.saveSummary)
			sc.Step(`^POST /api/share/meta carried summary `+q+`$`, st.carried)
			sc.Step(`^the saved declaration for `+q+` has summary `+q+`$`, st.saved)
			sc.Step(`^a client posts /api/share/meta with a (\d+)-character summary$`, st.postLong)
			sc.Step(`^the response is 400 with `+q+`$`, st.badRequest)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths: []string{"../../features/details/web_about.feature", "../../features/details/text_sanitization.feature",
				"../../features/details/declaration.feature"},
			Tags:     "@webui",
			TestingT: t,
			Strict:   true,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("details @webui behavior scenarios failed (see godog output above)")
	}
}
