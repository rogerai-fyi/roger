package webui

// Executable spec: features/edge/empty_edge_simple.feature (@console) - the console's Edge view
// mirrors the simple empty state. The real console server over HTTP (GET /api/edge, POST
// /api/edge/joinprompt) with a status filled the way the host fills it, and the SHIPPED
// console-edge-empty.js EXECUTED with node against that server, so the state the browser draws
// and the join it sends are the ones tested. The join backend is the test's, parsing with the
// real invite parser as the host does.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/joincode"
	"rogerai.fm/roger/v7/internal/joininvite"
	"rogerai.fm/roger/v7/internal/look"
	"rogerai.fm/roger/v7/internal/store"
)

type emptySimpleConsoleBDD struct {
	t      *testing.T
	now    time.Time
	status edge.SelfStatus
	texts  []string
	srv    *httptest.Server
	tok    string
	out    struct {
		Text    string   `json:"text"`
		Buttons []string `json:"buttons"`
		Field   bool     `json:"field"`
		Tower   string   `json:"tower"`
		Ask     struct {
			State  string `json:"state"`
			Phrase string `json:"phrase"`
			Text   string `json:"text"`
		} `json:"ask"`
	}
}

func (s *emptySimpleConsoleBDD) reset() {
	if s.srv != nil {
		s.srv.Close()
	}
	s.now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	s.status = edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, ScansOnRequest: true, IntervalS: 30,
		LastPass: s.now.Unix(), Nearby: 2}
	s.texts = nil
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	srv := New(nil, Options{Edge: EdgeHooks{
		Self: "workshop", Fleet: fleet,
		Candidates: func() []store.EdgeNode { return nil },
		Status:     func() edge.SelfStatus { return s.status },
		Now:        func() time.Time { return s.now },
		JoinPrompt: &edge.JoinPrompt{Start: func(text string) (*edge.JoinAsk, error) {
			s.texts = append(s.texts, text)
			if strings.Contains(text, "://") {
				in, err := joininvite.Parse(text)
				if err != nil {
					return nil, errors.New(look.JoinSaysNotInvite)
				}
				if in.Kind != joininvite.Edge {
					return nil, errors.New(look.JoinSaysTower)
				}
				s.status.Enrolled, s.status.Name = true, "bench"
				return nil, nil
			}
			return &edge.JoinAsk{Phrase: "amber-kettle-harbor", Until: s.now.Add(2 * time.Minute),
				Wait: func() error { select {} }, Cancel: func() {}}, nil
		}},
	}})
	s.tok = srv.Token()
	s.srv = httptest.NewServer(srv.Handler())
}

// open runs the shipped module against the live server: it reads /api/edge, renders the empty
// state into a minimal document and, when asked, clicks join and submits what the test says.
func (s *emptySimpleConsoleBDD) open(then string) error {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		return fmt.Errorf("node is needed to execute the console's JavaScript: %v", err)
	}
	script := `
const api = require(process.argv[1]);
const base = process.argv[2], tok = process.argv[3], then = process.argv[4], tower = process.argv[5];
const out = { text: "", buttons: [], field: false, tower: "", ask: {} };
function el(tag) {
  return { tag, children: [], attrs: {}, hidden: false, value: "", textContent: "", className: "", handlers: {},
    appendChild(c) { this.children.push(c); return c; },
    setAttribute(k, v) { this.attrs[k] = String(v); }, getAttribute(k) { return this.attrs[k]; },
    addEventListener(n, f) { this.handlers[n] = f; }, replaceChildren() { this.children = []; },
    focus() {} };
}
function text(e) { return [e.textContent].concat(e.children.map(text)).join(" "); }
function visible(e) { return e.hidden ? [] : [e].concat(e.children.flatMap(visible)); }
const host = el("div"); host.hidden = true;
const doc = { createElement: el };
const actions = {
  fetch: (path, opts) => fetch(base + path + (path.includes("?") ? "&" : "?") + "t=" + tok, opts),
  poll: false, // the test reads each answer once; the browser polls the wait
};
(async () => {
  const r = await actions.fetch("/api/edge", { method: "GET" });
  const d = await r.json();
  const view = api.render(doc, host, d, actions);
  out.text = visible(host).map((e) => e.textContent).filter(Boolean).join("\n");
  out.buttons = visible(host).filter((e) => e.tag === "button").map((e) => e.textContent);
  if (then === "join") {
    view.openJoin();
    out.field = visible(host).some((e) => e.tag === "input");
    const a = await view.join(tower);
    out.tower = a.says || "";
    const b = await view.join("192.168.1.10:8791");
    out.ask = { state: b.state, phrase: b.phrase || "", text: visible(host).map((e) => e.textContent).filter(Boolean).join("\n") };
  }
  process.stdout.write(JSON.stringify(out));
})().catch((e) => { console.error(e); process.exit(2); });
`
	tower := joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{"192.168.1.20:8787"}, FP: strings.Repeat("ab", 32), Code: joincode.Tower.NewCode()}.String()
	cmd := exec.Command(nodeBin, "-e", script, "./assets/console-edge-empty.js", s.srv.URL, s.tok, then, tower)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("console-edge-empty.js failed: %v: %s", err, b)
	}
	return json.Unmarshal(b, &s.out)
}

func (s *emptySimpleConsoleBDD) mirrors() error {
	for _, w := range []string{"YOUR EDGE", look.EmptyEdgeState, look.EmptyEdgeSay} {
		if !strings.Contains(s.out.Text, w) {
			return fmt.Errorf("the console's empty state lacks %q:\n%s", w, s.out.Text)
		}
	}
	if len(s.out.Buttons) != 2 || s.out.Buttons[0] != look.EmptyEdgeStart || s.out.Buttons[1] != look.EmptyEdgeJoin {
		return fmt.Errorf("the buttons are %v", s.out.Buttons)
	}
	return nil
}

func (s *emptySimpleConsoleBDD) sameScan() error {
	first, second := s.status.Scan().Lines()
	for _, w := range []string{first, second} {
		if !strings.Contains(s.out.Text, w) {
			return fmt.Errorf("the console lacks the scan line %q:\n%s", w, s.out.Text)
		}
	}
	if !strings.Contains(second, "2 roger machines seen nearby") {
		return fmt.Errorf("the test status did not count: %q", second)
	}
	return nil
}

func (s *emptySimpleConsoleBDD) clicksJoin() error { return s.open("join") }

func (s *emptySimpleConsoleBDD) oneField() error {
	if !s.out.Field {
		return fmt.Errorf("clicking join showed no field")
	}
	if s.out.Tower != look.JoinSaysTower {
		return fmt.Errorf("a Tower invite says %q, want %q", s.out.Tower, look.JoinSaysTower)
	}
	if s.out.Ask.State != "waiting" || s.out.Ask.Phrase != "amber-kettle-harbor" {
		return fmt.Errorf("an address did not wait with the phrase: %+v", s.out.Ask)
	}
	if !strings.Contains(s.out.Ask.Text, "amber-kettle-harbor") || !strings.Contains(s.out.Ask.Text, look.EmptyEdgePhraseNote) {
		return fmt.Errorf("the phrase and its note are not drawn:\n%s", s.out.Ask.Text)
	}
	if len(s.texts) != 2 {
		return fmt.Errorf("the backend saw %v", s.texts)
	}
	return nil
}

func (s *emptySimpleConsoleBDD) noOldFacts(a, b, c, d string) error {
	for _, w := range []string{a, b, c, d} {
		if strings.Contains(s.out.Text, w) {
			return fmt.Errorf("the console's empty state says %q:\n%s", w, s.out.Text)
		}
	}
	// The shell hides the old facts block whenever the server sends the simple state.
	js, _ := os.ReadFile("assets/console.js")
	if !regexp.MustCompile(`RogerConsoleEdgeEmpty\.render\(`).Match(js) || !regexp.MustCompile(`show\(\$\("edge-empty"\),\s*empty\s*&&\s*!d\.empty\)`).Match(js) {
		return fmt.Errorf("console.js does not swap the old facts block for the simple state")
	}
	html, _ := os.ReadFile("assets/console.html")
	if !strings.Contains(string(html), `id="edge-empty-simple"`) || !strings.Contains(string(html), `/assets/console-edge-empty.js`) {
		return fmt.Errorf("console.html has no simple empty slot or does not load console-edge-empty.js")
	}
	return nil
}

func TestEmptyEdgeSimpleFeatureConsole(t *testing.T) {
	st := &emptySimpleConsoleBDD{t: t}
	t.Cleanup(func() {
		if st.srv != nil {
			st.srv.Close()
		}
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Given(`^this machine is in no Edge \(not enrolled and not an authority\)$`, func() error { return nil })
			sc.When(`^the console's Edge view is opened$`, func() error { return st.open("") })
			sc.Then(`^it shows "YOUR EDGE · not set up yet", the same sentence, and two buttons: start an Edge here, join an Edge you started elsewhere$`, st.mirrors)
			sc.Then(`^the same scan line and nearby count$`, st.sameScan)
			sc.When(`^the owner clicks join an Edge you started elsewhere$`, st.clicksJoin)
			sc.Then(`^one field accepts an invite or an address, with the same outcomes as the TUI's j prompt$`, st.oneField)
			sc.Then(`^it does not say "([^"]*)", "([^"]*)", "([^"]*)" or "([^"]*)"$`, st.noOldFacts)

			// §7 @console: NEARBY with Invite and Approve.
			nb := &nearbyConsoleBDD{}
			t.Cleanup(func() {
				if nb.srv != nil {
					nb.srv.Close()
				}
			})
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { nb.reset(t); return c, nil })
			sc.Given(`^a phone advertises itself with node id "([^"]*)"$`, nb.phonePings)
			sc.When(`^the owner opens the console's Edge view$`, nb.opens)
			sc.Then(`^a NEARBY list shows the row with Invite and Approve buttons$`, nb.listWithButtons)
			sc.Then(`^Invite shows the join line and the QR, and Approve waits for the ask and shows the phrase$`, nb.inviteAndApprove)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@console",
			Paths: []string{"../../features/edge/empty_edge_simple.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the console's simple empty Edge scenarios failed")
	}
}
