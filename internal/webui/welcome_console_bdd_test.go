package webui

// Executable spec: features/onboarding/welcome_card.feature (@console) - the console's one-time
// welcome card. The real console server over HTTP (GET /api/welcome, POST /api/welcome/seen),
// and the SHIPPED console-welcome.js EXECUTED with node against that server, so the card the
// browser draws and the request it sends on closing are the ones tested.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/look"
)

type welcomeConsoleBDD struct {
	t     *testing.T
	seen  bool
	saves int
	srv   *httptest.Server
	tok   string
	out   struct {
		Shown  bool
		Items  []struct{ Key, Label, What, Dest string }
		Picked []string
		Closed bool
	}
}

func (s *welcomeConsoleBDD) reset() {
	if s.srv != nil {
		s.srv.Close()
	}
	s.seen, s.saves = false, 0
	srv := New(nil, Options{
		WelcomeSeen:     func() bool { return s.seen },
		SaveWelcomeSeen: func() error { s.saves++; s.seen = true; return nil },
	})
	s.tok = srv.Token()
	s.srv = httptest.NewServer(srv.Handler())
}

// open runs the shipped module against the live server: it reads /api/welcome, renders the
// card into a minimal document, then picks or closes as asked, sending the real request.
func (s *welcomeConsoleBDD) open(then string) error {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		return fmt.Errorf("node is needed to execute the console's JavaScript: %v", err)
	}
	script := `
const api = require(process.argv[1]);
const base = process.argv[2], tok = process.argv[3], then = process.argv[4];
const out = { shown: false, items: [], picked: [], closed: false };
function el(tag) {
  return { tag, children: [], attrs: {}, hidden: false, textContent: "", className: "",
    appendChild(c) { this.children.push(c); return c; },
    setAttribute(k, v) { this.attrs[k] = String(v); }, getAttribute(k) { return this.attrs[k]; },
    addEventListener() {}, replaceChildren() { this.children = []; } };
}
const host = el("div"); host.hidden = true;
const doc = { getElementById: (id) => (id === "welcome" ? host : null), createElement: el };
const actions = {
  fetch: (path, opts) => fetch(base + path + (path.includes("?") ? "&" : "?") + "t=" + tok, opts),
  tab: (name) => out.picked.push("tab:" + name),
  login: () => out.picked.push("login"),
};
(async () => {
  const card = await api.mount(doc, actions);
  out.shown = !host.hidden;
  for (const o of api.options()) out.items.push({ key: o.key, label: o.label, what: o.what, dest: o.dest });
  if (card && then === "close") { await card.close(); out.closed = true; }
  if (card && then.startsWith("pick:")) { await card.pick(then.slice(5)); }
  process.stdout.write(JSON.stringify(out));
})().catch((e) => { console.error(e); process.exit(2); });
`
	cmd := exec.Command(nodeBin, "-e", script, "./assets/console-welcome.js", s.srv.URL, s.tok, then)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("console-welcome.js failed: %v: %s", err, b)
	}
	return json.Unmarshal(b, &s.out)
}

// The console's six lines: the TUI's, with Web console as the Terminal app.
var consoleDest = map[string]string{"1": "tab:browse", "0": "tab:chat", "2": "tab:share", "3": "tab:edge", "w": "terminal", "l": "login"}

func (s *welcomeConsoleBDD) sameSix() error {
	if !s.out.Shown {
		return fmt.Errorf("the console shows no welcome card")
	}
	if len(s.out.Items) != len(look.WelcomeOptions) {
		return fmt.Errorf("the console card has %d lines, want %d", len(s.out.Items), len(look.WelcomeOptions))
	}
	for i, o := range look.WelcomeOptions {
		it := s.out.Items[i]
		if it.Key != o.Key {
			return fmt.Errorf("line %d is %q, want key %q", i, it.Key, o.Key)
		}
		if o.Key != "w" && (it.Label != o.Label || it.What != o.What) {
			return fmt.Errorf("line %s reads %q %q, want %q %q", o.Key, it.Label, it.What, o.Label, o.What)
		}
		if it.Dest != consoleDest[o.Key] {
			return fmt.Errorf("line %s goes to %q, want %q", o.Key, it.Dest, consoleDest[o.Key])
		}
	}
	return nil
}

func (s *welcomeConsoleBDD) closingSaves() error {
	if err := s.open("close"); err != nil {
		return err
	}
	if !s.out.Closed || s.saves != 1 || !s.seen {
		return fmt.Errorf("closing the card saved welcome_seen %d times (seen %v), want once", s.saves, s.seen)
	}
	if err := s.open(""); err != nil {
		return err
	}
	if s.out.Shown {
		return fmt.Errorf("the card is shown again after it was closed")
	}
	return nil
}

func (s *welcomeConsoleBDD) noCard() error {
	if s.out.Shown {
		return fmt.Errorf("a welcome card is shown in the console")
	}
	return nil
}

func (s *welcomeConsoleBDD) terminalLine() error {
	for _, it := range s.out.Items {
		if it.Key == "w" {
			if it.Label != "Terminal app" || !strings.Contains(it.What, "roger") {
				return fmt.Errorf("the w line reads %q %q, want Terminal app with the command", it.Label, it.What)
			}
			if strings.Contains(it.Label, "Web console") {
				return fmt.Errorf("the console still offers itself")
			}
			return nil
		}
	}
	return fmt.Errorf("no w line")
}

func (s *welcomeConsoleBDD) consoleLogin() error {
	if err := s.open("pick:l"); err != nil {
		return err
	}
	if len(s.out.Picked) != 1 || s.out.Picked[0] != "login" {
		return fmt.Errorf("picking Log in did %v, want the console's own login", s.out.Picked)
	}
	js, _ := os.ReadFile("assets/console.js")
	if !regexp.MustCompile(`RogerConsoleWelcome\.mount\([\s\S]{0,400}login:[^\n]*loginBegin\(\)`).Match(js) {
		return fmt.Errorf("console.js does not wire the card's Log in to loginBegin")
	}
	html, _ := os.ReadFile("assets/console.html")
	if !strings.Contains(string(html), `id="welcome"`) || !strings.Contains(string(html), `/assets/console-welcome.js`) {
		return fmt.Errorf("console.html has no welcome slot or does not load console-welcome.js")
	}
	return nil
}

func TestWelcomeCardFeatureConsole(t *testing.T) {
	st := &welcomeConsoleBDD{t: t}
	t.Cleanup(func() {
		if st.srv != nil {
			st.srv.Close()
		}
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Given(`^roger is launched with no arguments on an interactive terminal$`, func() error { return nil })
			sc.Given(`^welcome_seen is false$`, func() error { st.seen = false; return nil })
			sc.When(`^the web console is opened for the first time$`, func() error { return st.open("") })
			sc.Then(`^it shows the same six lines with the same destinations$`, st.sameSix)
			sc.Then(`^closing it saves welcome_seen so the TUI will not show the card either$`, st.closingSaves)
			sc.Given(`^the welcome card was closed in the TUI$`, func() error { st.seen = true; return nil })
			sc.When(`^the web console is opened$`, func() error { return st.open("") })
			sc.Then(`^no welcome card is shown there$`, st.noCard)
			sc.When(`^the console's welcome card is open$`, func() error { return st.open("") })
			sc.Then(`^"Web console" is replaced by "Terminal app" with the command to run roger$`, st.terminalLine)
			sc.Then(`^"Log in" starts the console's own login$`, st.consoleLogin)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@console",
			Paths: []string{"../../features/onboarding/welcome_card.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the console welcome card scenarios failed")
	}
}
