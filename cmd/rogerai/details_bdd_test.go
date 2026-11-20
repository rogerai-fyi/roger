package main

// details_bdd_test.go makes the @cli scenarios of features/details EXECUTABLE: the REAL
// cmdShare with the --summary/--best-at/--not-for flags (on shareMetaState's seams, config.json
// in an isolated XDG_CONFIG_HOME), and the REAL dispatch of `roger models <id>` against an
// httptest broker whose /discover is what the broker publishes (catalog.Notes.Apply over the
// canonical taxonomy, the function the broker calls).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/catalog"
	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/protocol"
)

var cliDemoNote = catalog.Note{ID: "demo-1b-fire-decision", Summary: "Picks one fire-defense action from site state.",
	BestAt: "Choosing among a fixed list of actions.", NotFor: "Chat. It answers with one action only.",
	BaseModel: "demo-1b", License: "Apache-2.0", Page: "/research-demo-1b.html#fire"}

type detailsCLIState struct {
	share  shareMetaState
	offers []client.Offer
	raw    bool // the broker does not normalize
	notes  catalog.Notes
	srv    *httptest.Server
	lan    bool
	lanHit bool
	out    string
	err    error
}

// splitArgs splits a command line on spaces, honoring double quotes ("" is an empty arg).
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQ, have = !inQ, true
		case r == ' ' && !inQ:
			if have {
				out = append(out, cur.String())
			}
			cur.Reset()
			have = false
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

func expandFlags(s string) string {
	for _, n := range []int{141, 201} {
		s = strings.ReplaceAll(s, "<"+strconv.Itoa(n)+" a>", strings.Repeat("a", n))
	}
	return s
}

// runShare is shareMetaState.run with quoted arguments.
func (s *detailsCLIState) runShare(flags string) error {
	st := &s.share
	st.ran = true
	st.before = loadConfig().Meta
	*st.got = agent.Config{}
	args := append([]string{st.model, "--upstream", "http://127.0.0.1:1234/v1"}, splitArgs(expandFlags(flags))...)
	if flags == "--help" { // flag.ExitOnError would exit the test binary: read the usage it prints
		st.out = shareUsage
		return nil
	}
	st.out = captureStdout(st.t, func() { st.err = cmdShare(st.cfg, args) })
	return nil
}

func (s *detailsCLIState) background(model, _ string) error { s.share.model = model; return nil }
func (s *detailsCLIState) savedSummary(model, sum string) error {
	return s.saveMeta(model, protocol.Taxonomy{Summary: sum})
}
func (s *detailsCLIState) savedCatSummary(model, cat, sum string) error {
	c, sub := protocol.SplitCategory(cat)
	return s.saveMeta(model, protocol.Taxonomy{Category: c, Subcategory: sub, Summary: sum})
}
func (s *detailsCLIState) saveMeta(model string, t protocol.Taxonomy) error {
	if s.share.ran {
		got := loadConfig().Meta[model]
		if got.Summary != t.Summary || got.Category != t.Category || got.Subcategory != t.Subcategory {
			return fmt.Errorf("share_meta[%s] = %+v, want %+v", model, got, t)
		}
		return nil
	}
	c := loadConfig()
	if c.Meta == nil {
		c.Meta = map[string]protocol.Taxonomy{}
	}
	c.Meta[model] = t
	s.share.cfg.Meta = c.Meta
	return saveConfig(c)
}
func (s *detailsCLIState) noCatNoSummary(model string) error {
	t := loadConfig().Meta[model]
	if t.Category != "" || t.Summary != "" {
		return fmt.Errorf("share_meta[%s] = %+v, want no category and no summary", model, t)
	}
	return nil
}
func (s *detailsCLIState) hasBest(model, v string) error {
	if got := loadConfig().Meta[model].BestAt; got != v {
		return fmt.Errorf("share_meta[%s].best_at %q, want %q", model, got, v)
	}
	return nil
}
func (s *detailsCLIState) noSummaryBest(model, v string) error {
	if t := loadConfig().Meta[model]; t.Summary != "" || t.BestAt != v {
		return fmt.Errorf("share_meta[%s] = %+v", model, t)
	}
	return nil
}
func (s *detailsCLIState) combined(model, cat, tuning, base, sum string) error {
	t := loadConfig().Meta[model]
	got := fmt.Sprintf("%s|%s|%s|%s", catPair(t), t.Tuning, t.BaseModel, t.Summary)
	if want := fmt.Sprintf("%s|%s|%s|%s", cat, tuning, base, sum); got != want {
		return fmt.Errorf("share_meta[%s] %q, want %q", model, got, want)
	}
	return nil
}
func catPair(t protocol.Taxonomy) string {
	if t.Subcategory == "" {
		return t.Category
	}
	return t.Category + "/" + t.Subcategory
}
func (s *detailsCLIState) onAir(field, model, v string) error {
	if s.share.err != nil {
		return fmt.Errorf("share failed: %v", s.share.err)
	}
	t := s.share.got.Taxonomy
	got := map[string]string{"summary": t.Summary, "best_at": t.BestAt, "not_for": t.NotFor, "details source": t.DetailsSource}[field]
	if got != v {
		return fmt.Errorf("on-air %s %q, want %q", field, got, v)
	}
	return nil
}
func (s *detailsCLIState) helpLists(a, b, c string) error {
	for _, f := range []string{a, b, c} {
		if !strings.Contains(s.share.out, f) {
			return fmt.Errorf("--advanced does not list %s: %q", f, s.share.out)
		}
	}
	return nil
}
func (s *detailsCLIState) helpSays(v string) error {
	if !strings.Contains(s.share.out, v) {
		return fmt.Errorf("--advanced does not say %q: %q", v, s.share.out)
	}
	return nil
}
func (s *detailsCLIState) basicLacks(f string) error {
	if strings.Contains(s.share.out, f) {
		return fmt.Errorf("the basic help lists %s", f)
	}
	return nil
}

// ---- roger models
func (s *detailsCLIState) chatOffers(t *godog.Table) error {
	head := t.Rows[0].Cells
	for _, row := range t.Rows[1:] {
		v := map[string]string{}
		for i, c := range row.Cells {
			v[head[i].Value] = strings.TrimSpace(c.Value)
		}
		s.offers = append(s.offers, client.Offer{NodeID: v["node"], Model: v["model"], Online: true,
			Taxonomy: protocol.Taxonomy{Tuning: v["tuning"], BaseModel: v["base_model"], License: v["license"],
				Summary: v["summary"], BestAt: v["best_at"], NotFor: v["not_for"]}})
	}
	return nil
}
func (s *detailsCLIState) notesDescribe(string) error {
	b, _ := json.Marshal([]catalog.Note{cliDemoNote})
	n, err := catalog.ParseNotes(b)
	s.notes = n
	return err
}
func (s *detailsCLIState) published() []client.Offer {
	out := make([]client.Offer, len(s.offers))
	for i, o := range s.offers {
		if !s.raw {
			o.Taxonomy = s.notes.Apply(o.Model, o.Taxonomy.Canonical("chat"))
		}
		out[i] = o
	}
	return out
}
func (s *detailsCLIState) serve() string {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.lanHit = s.lanHit || s.lan
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": s.published()})
	}))
	return s.srv.URL
}
func (s *detailsCLIState) run(cmd string, env map[string]string) error {
	url := s.serve()
	cfg := config{Broker: url}
	if s.lan { // the REAL LAN read (client.LANOffers, signed), against a Tower at url
		cfg = config{LanTowers: []string{strings.TrimPrefix(url, "http://")}}
		cfg.lan.LAN = true
	}
	for k, v := range env {
		s.share.t.Setenv(k, v)
	}
	args := splitArgs(cmd)[1:] // drop "roger"
	s.out = captureStdout(s.share.t, func() { s.err = dispatch(cfg, args) })
	if s.err != nil {
		s.out += s.err.Error() + "\n"
	}
	return nil
}
func (s *detailsCLIState) userRuns(cmd string) error {
	return s.run(cmd, map[string]string{"COLUMNS": ""})
}
func (s *detailsCLIState) userRunsWide(cmd string, cols int) error {
	return s.run(cmd, map[string]string{"COLUMNS": strconv.Itoa(cols)})
}

// userRunsPiped runs with stdout a pipe (captureStdout) against a broker that passes ESC
// through untouched, in the summary and the tuning, so "no control bytes" can fail.
func (s *detailsCLIState) userRunsPiped(cmd string) error {
	s.raw = true
	for i := range s.offers {
		s.offers[i].Summary += "\x1b[31m red"
		s.offers[i].Tuning = "\x1b[2Jinstruct"
		s.offers[i].BaseModel += "\x1b]52;c;aGk=\x07"
	}
	return s.userRuns(cmd)
}
func (s *detailsCLIState) outputIs(doc *godog.DocString) error {
	if strings.TrimRight(s.out, "\n") != strings.TrimRight(doc.Content, "\n") {
		return fmt.Errorf("output:\n%s\nwant:\n%s", s.out, doc.Content)
	}
	return nil
}
func (s *detailsCLIState) exitCode(n int) error {
	if (n == 0) != (s.err == nil) {
		return fmt.Errorf("exit %d, got err %v", n, s.err)
	}
	return nil
}
func (s *detailsCLIState) noByte() error {
	if strings.ContainsRune(s.out, 0x1b) {
		return fmt.Errorf("output carries ESC: %q", s.out)
	}
	return nil
}

// jsonEqual holds --json to the block /v1/models carries for the Background's someone-7b,
// written out (cmd/rogerai-broker's own runner pins /v1/models to the same keys).
func (s *detailsCLIState) jsonEqual(model, key string) error {
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s.out)), &got); err != nil {
		return fmt.Errorf("not one JSON object: %v\n%s", err, s.out)
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(`{"`+key+`":"`+model+`","source":"declared","summary":"A 7B chat model.","best_at":"Long answers.",
		"not_for":"Short replies.","base_model":"base-7b","tuning":"instruct","license":"MIT","declared_by":2,"stations":3}`), &want)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("--json %v, want %v", got, want)
	}
	return nil
}
func (s *detailsCLIState) says(v string) error {
	if !strings.Contains(s.out, v) {
		return fmt.Errorf("output does not say %q:\n%s", v, s.out)
	}
	return nil
}
func (s *detailsCLIState) closeMatch(v string) error { return s.says(v) }
func (s *detailsCLIState) sameListing(string) error {
	for _, o := range s.offers {
		if o.Summary != "" && strings.Contains(s.out, o.Summary) {
			return fmt.Errorf("the plain listing gained a summary column: %q", o.Summary)
		}
	}
	if !strings.Contains(s.out, "MODEL") || !strings.Contains(s.out, "someone-7b") {
		return fmt.Errorf("not the station listing:\n%s", s.out)
	}
	return nil
}
func (s *detailsCLIState) lineEnds(model, sum string) error {
	for _, l := range strings.Split(s.out, "\n") {
		if strings.Contains(l, " "+model+" ") {
			if len([]rune(l)) > 120 || !(strings.HasSuffix(l, sum) || strings.HasSuffix(l, "…")) || !strings.Contains(l, sum[:4]) {
				return fmt.Errorf("the %s line %q does not end with its summary within 120 columns", model, l)
			}
			return nil
		}
	}
	return fmt.Errorf("no %s line:\n%s", model, s.out)
}
func (s *detailsCLIState) lineNoSummary(model string) error {
	for _, l := range strings.Split(s.out, "\n") {
		if strings.Contains(l, " "+model+" ") && strings.Contains(l, "  ·  ") {
			return fmt.Errorf("the %s line carries a summary: %q", model, l)
		}
	}
	return nil
}
func (s *detailsCLIState) lanMode() error { s.lan = true; return nil }
func (s *detailsCLIState) readFromLAN() error {
	if !s.lanHit || s.err != nil || !strings.Contains(s.out, "A 7B chat model.") {
		return fmt.Errorf("the LAN tower was not read (hit %v, err %v):\n%s", s.lanHit, s.err, s.out)
	}
	return nil
}

// text_sanitization: the CLI's half of "re-canonicalized on read".
func (s *detailsCLIState) oldBroker() error { s.raw = true; return nil }
func (s *detailsCLIState) itsOffer(model, sum string) error {
	s.offers = append(s.offers, client.Offer{NodeID: "o1", Model: model, Online: true,
		Taxonomy: protocol.Taxonomy{Summary: unescCLI(sum)}})
	return nil
}
func unescCLI(v string) string {
	if u, err := strconv.Unquote(`"` + strings.ReplaceAll(v, `"`, `\"`) + `"`); err == nil {
		return u
	}
	return v
}
func (s *detailsCLIState) readsOffer() error { return s.userRuns("roger models demo-1b-chat") }

func TestDetailsCLIBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &detailsCLIState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = detailsCLIState{share: shareMetaState{t: t}}
				st.share.install()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.share.teardown()
				if st.srv != nil {
					st.srv.Close()
				}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a share controller with a detected model `+q+` inferred as `+q+`$`, st.background)
			sc.Step(`^the operator runs share with '(.*)'$`, st.runShare)
			sc.Step(`^the operator runs share with "(.*)"$`, st.runShare)
			sc.Step(`^the on-air (summary|best_at|not_for|details source) for `+q+` is `+q+`$`, st.onAir)
			sc.Step(`^config.json share_meta for `+q+` has summary `+q+`$`, st.savedSummary)
			sc.Step(`^config.json share_meta for `+q+` has category `+q+` and summary `+q+`$`, st.savedCatSummary)
			sc.Step(`^config.json share_meta for `+q+` has no category and no summary$`, st.noCatNoSummary)
			sc.Step(`^config.json share_meta for `+q+` has best_at `+q+`$`, st.hasBest)
			sc.Step(`^config.json share_meta for `+q+` has no summary and best_at `+q+`$`, st.noSummaryBest)
			sc.Step(`^config.json share_meta for `+q+` has category `+q+`, tuning `+q+`, base model `+q+` and summary `+q+`$`, st.combined)
			sc.Step(`^share fails with an error mentioning `+q+`$`, st.share.failed)
			sc.Step(`^nothing went on air$`, st.share.nothingOnAir)
			sc.Step(`^config.json share_meta for `+q+` is unchanged$`, st.share.unchanged)
			sc.Step(`^the help lists `+q+`, `+q+` and `+q+`$`, st.helpLists)
			sc.Step(`^the help says each is shown to people browsing the market$`, func() error { return st.helpSays("shown to people browsing the market") })
			sc.Step(`^the help says any taxonomy or details flag replaces the saved declaration$`, func() error {
				return st.helpSays("any taxonomy or details flag replaces the model's saved declaration")
			})
			sc.Step(`^the basic help does not list `+q+`$`, st.basicLacks)

			sc.Step(`^these on-air chat offers:$`, st.chatOffers)
			sc.Step(`^RogerAI's curated notes describe (.*) as in curated_catalog\.feature$`, st.notesDescribe)
			sc.Step(`^the user runs `+q+`$`, st.userRuns)
			sc.Step(`^the user runs `+q+` in a (\d+)-column terminal$`, st.userRunsWide)
			sc.Step(`^the user runs `+q+` with stdout not a terminal$`, st.userRunsPiped)
			sc.Step(`^the output is:$`, st.outputIs)
			sc.Step(`^the exit code is (\d+)$`, st.exitCode)
			sc.Step(`^the output contains no byte 0x1b$`, st.noByte)
			sc.Step(`^the output is one JSON object equal to the /v1/models rogerai\.details for `+q+` plus `+q+`$`, st.jsonEqual)
			sc.Step(`^the output says `+q+`$`, st.says)
			sc.Step(`^the output lists `+q+` as a close match$`, st.closeMatch)
			sc.Step(`^the output is the same listing as `+q+` before this change$`, st.sameListing)
			sc.Step(`^the `+q+` line ends with `+q+` cut to fit$`, st.lineEnds)
			sc.Step(`^the `+q+` line has no summary$`, st.lineNoSummary)
			sc.Step(`^the CLI is in LAN mode$`, st.lanMode)
			sc.Step(`^the details are read from the LAN tower's /discover$`, st.readFromLAN)
			sc.Step(`^a broker that does not normalize details \(an older or third-party broker\)$`, st.oldBroker)
			sc.Step(`^its /discover offer for `+q+` has summary "(.*)"$`, st.itsOffer)
			sc.Step(`^the TUI, the local console and the CLI read that offer$`, st.readsOffer)
			sc.Step(`^each shows the summary "(.*)"$`, st.says)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths: []string{"../../features/details/declaration.feature", "../../features/details/api_cli.feature",
				"../../features/details/text_sanitization.feature"},
			Tags:     "@cli",
			TestingT: t,
			Strict:   true,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("details @cli behavior scenarios failed (see godog output above)")
	}
}
