package main

// join_owner_screens_bdd_test.go runs the owner's-screen scenarios of features/join against a REAL
// serving Tower (serve.Prepare over TLS, its admin socket), a REAL client (`roger join`) and the
// REAL TUI built from tuiHooks (owner_join.go): the request card answered with y, the invite
// panel opened with the invite key, and the Tower's fingerprint where the owner looks.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/joininvite"
	"rogerai.fm/roger/v7/internal/towercli"
	"rogerai.fm/roger/v7/internal/towerpin"
	"rogerai.fm/roger/v7/internal/tui"
)

const surfacesFeature = "../../features/join/surfaces.feature"

// liveBuf is a writer the test can read while the join still runs.
type liveBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *liveBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *liveBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type ownerScreens struct {
	*approveBDD
	console string
	shown   consoleView
	m       tea.Model
	out     *liveBuf
	done    chan error
	inv     joininvite.Invite
	line    string
	copied  tea.Cmd
	edge    *edgeHost
	root    string // the authority's root fingerprint
}

// screen is the TUI the owner has open, on [3] EDGE.
func (s *ownerScreens) screen() {
	h := tuiHooks(loadConfig())
	s.m = tui.NewWithHooks(loadConfig().Broker, "", nil, h)
	s.m, _ = s.m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	s.m, _ = s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	s.refresh()
}

// refresh runs one poll of the owner's view, the way the TUI's own loop does.
func (s *ownerScreens) refresh() {
	var cmd tea.Cmd
	s.m, cmd = s.m.Update(tui.OwnerJoinRefresh{})
	if cmd != nil {
		s.m, _ = s.m.Update(cmd())
	}
}

// press sends a key and runs what it asked for (never a clipboard write: that would reach the
// developer's real clipboard).
func (s *ownerScreens) press(key string) {
	var cmd tea.Cmd
	s.m, cmd = s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	if key == "c" {
		s.copied = cmd
		return
	}
	if cmd != nil {
		s.m, _ = s.m.Update(cmd())
	}
}

// edgeScreen is an Edge authority serving on this machine, with its TUI open on [3] EDGE: the TUI's
// hooks wired to the authority the way the launch wires them (startEdge).
func (s *ownerScreens) edgeScreen() error {
	if out, code := edgeRun(s.t, "edge", "authority", "local", "shed"); code != 0 {
		return fmt.Errorf("roger edge authority local said %s", out)
	}
	h, err := newEdgeHost("shed")
	if err != nil {
		return err
	}
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return err
	}
	s.root = local.Descriptor().Fingerprint
	h.startAuthority()
	s.t.Cleanup(h.closeListeners)
	s.edge = h
	hooks := tuiHooks(loadConfig())
	h.wire(&hooks)
	s.m = tui.NewWithHooks(loadConfig().Broker, "", nil, hooks)
	s.m, _ = s.m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	s.m, _ = s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	s.refresh()
	return nil
}

// edgeAddr is where the authority answers, on loopback.
func (s *ownerScreens) edgeAddr() string {
	_, port, _ := net.SplitHostPort(s.edge.authorityAddr())
	return "127.0.0.1:" + port
}

func (s *ownerScreens) view() string { return ansi.Strip(s.m.View()) }

// askLive starts `roger join <addr> laptop-x` and returns once the owner can see the request.
func (s *ownerScreens) askLive() error {
	s.out, s.done = &liveBuf{}, make(chan error, 1)
	go func() { s.done <- joinPairAt(s.out, s.addr, "laptop-x", false) }()
	_, err := s.waiting(1)
	return err
}

func (s *ownerScreens) phrase() string { return clientPhrase(s.out.String()) }

func (s *ownerScreens) joined() error {
	select {
	case err := <-s.done:
		return err
	case <-time.After(10 * time.Second):
		return fmt.Errorf("roger join never finished")
	}
}

// shownLine is the join line as the screen shows it: its rows run from "roger join" to the copy
// hint, and read together they must be the whole line.
func (s *ownerScreens) shownLine() string {
	v := s.view()
	start := strings.Index(v, "roger join 'roger://")
	end := strings.Index(v, "c copies the line")
	if start < 0 || end < start {
		return ""
	}
	return strings.Join(strings.Fields(v[start:end]), " ")
}

func (s *ownerScreens) openInvite() error {
	s.press("I")
	line := joininvite.JoinLine(strings.ReplaceAll(joininvite.FromLine(s.shownLine()), " ", ""))
	m := joinLineRE.FindStringSubmatch(line)
	if m == nil {
		return fmt.Errorf("the TUI shows no whole join line after the invite key: %q\n%s", line, s.view())
	}
	in, err := joininvite.Parse(m[1])
	if err != nil {
		return fmt.Errorf("the join line on screen is not whole: %v", err)
	}
	s.inv, s.line = in, m[0]
	return nil
}

func (s *ownerScreens) has(want string) error {
	if v := s.view(); !strings.Contains(v, want) {
		return fmt.Errorf("the screen lacks %q:\n%s", want, v)
	}
	return nil
}

func TestJoinOwnerScreensBDD(t *testing.T) {
	s := &ownerScreens{approveBDD: &approveBDD{t: t}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				if err := s.reset(); err != nil {
					return ctx, err
				}
				s.m, s.copied, s.console = nil, nil, ""
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if s.done != nil {
					select {
					case <-s.done:
					case <-time.After(3 * time.Second):
					}
					s.done = nil
				}
				return ctx, nil
			})
			sc.Step(`^a standalone Tower "lab" is serving at 192\.168\.1\.20:8787 over TLS$`, func() error { return nil })
			sc.Step(`^a standalone Tower "lab" serves at 192\.168\.1\.20:8787 over TLS with key fingerprint "sha256:9f3c\.\.\.e1"$`, func() error { return nil })
			sc.Step(`^a standalone Tower "lab" is serving at 192\.168\.1\.20:8787 over TLS with its own key$`, func() error { return nil })
			sc.Step(`^the Tower's identity fingerprint is "sha256:9f3c\.\.\.e1"$`, func() error { return nil })
			sc.Step(`^the owner is signed in to nothing \(no Roger Core account is needed\)$`, func() error {
				s.t.Setenv("ROGER_BROKER", "http://127.0.0.1:1") // nothing below may need Roger Core
				return nil
			})
			sc.Step(`^the owner has the Tower's TUI open$`, func() error { s.screen(); return nil })

			// --- request and approve ------------------------------------------------------------
			sc.Step(`^the client runs "roger join 192\.168\.1\.20:8787"$`, s.askLive)
			sc.Step(`^a join request arrives$`, func() error {
				if s.m == nil {
					s.screen()
				}
				return s.askLive()
			})
			sc.Step(`^the client prints "asking lab to let you in\. Check this phrase matches the owner's screen: amber-river-71"$`, func() error {
				p := s.phrase()
				if p == "" || !strings.Contains(s.out.String(), "asking lab to let you in. Check this phrase matches the owner's screen: "+p) {
					return fmt.Errorf("the client printed %q", s.out.String())
				}
				return nil
			})
			card := func() error {
				s.refresh()
				return s.has("laptop-x wants to join · " + s.phrase() + " · y approves, n refuses")
			}
			sc.Step(`^the owner's TUI shows "laptop-x wants to join · amber-river-71 · y approves, n refuses"$`, card)
			sc.Step(`^the TUI shows "<name> wants to join · <phrase> · y approves, n refuses"$`, card)
			sc.Step(`^it says "approve only if <name> shows the same phrase"$`, func() error {
				return s.has("approve only if laptop-x shows the same phrase")
			})
			sc.Step(`^the owner presses y$`, func() error { s.press("y"); return s.joined() })
			sc.Step(`^the Tower admits the client's key$`, func() error {
				if !s.admitted() {
					return fmt.Errorf("the client's key was not admitted")
				}
				return nil
			})
			sc.Step(`^the client pins the Tower's TLS fingerprint as seen during the pairing$`, func() error {
				if got := loadConfig().LanTowerPins[s.addr]; got != s.fp {
					return fmt.Errorf("pinned %q, the Tower serves %q", got, s.fp)
				}
				return nil
			})
			sc.Step(`^the client prints "joined the Tower lab" and one next step$`, func() error {
				if out := s.out.String(); !strings.Contains(out, "joined the Tower lab") || !strings.Contains(out, "try: ") {
					return fmt.Errorf("the client printed %q", out)
				}
				return nil
			})

			// --- an Edge authority approves -----------------------------------------------------
			sc.Step(`^an Edge authority with its TUI open$`, s.edgeScreen)
			sc.Step(`^a device runs "roger join <authority address>"$`, func() error {
				asSecondMachine(s.t)
				s.out, s.done = &liveBuf{}, make(chan error, 1)
				go func() { s.done <- joinPairAt(s.out, s.edgeAddr(), "workshop", false) }()
				for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
					if s.phrase() != "" {
						return nil
					}
					select {
					case err := <-s.done:
						return fmt.Errorf("roger join <authority> ended before asking (%v): %s", err, s.out.String())
					default:
					}
				}
				return fmt.Errorf("the device never showed a phrase: %q", s.out.String())
			})
			sc.Step(`^the authority shows the device's request with a phrase$`, func() error {
				s.refresh()
				return s.has("workshop wants to join · " + s.phrase() + " · y approves, n refuses")
			})
			sc.Step(`^the device enrolls, and its root is the one the phrase covered$`, func() error {
				id, _, ok, err := edgeIdentityStore().LoadIdentity()
				if err != nil || !ok || id == nil {
					return fmt.Errorf("no Edge identity was kept (%v): %s", err, s.out.String())
				}
				if got, want := edgeauth.RootFingerprint(id.Root), s.root; got != want {
					return fmt.Errorf("kept root %s, the authority's is %s", got, want)
				}
				if !strings.Contains(s.out.String(), "asking shed to let you in") {
					return fmt.Errorf("the device did not name the Edge it asked: %q", s.out.String())
				}
				return nil
			})

			// --- the invite panel ---------------------------------------------------------------
			sc.Step(`^the owner is on the Tower's row in the TUI$`, func() error { s.screen(); return s.has("Tower lab") })
			sc.Step(`^the owner pressed the invite key on the Tower row$`, func() error { s.screen(); return s.openInvite() })
			sc.Step(`^they press the invite key$`, s.openInvite)
			sc.Step(`^the TUI shows the same "roger join \.\.\." line and the QR$`, func() error {
				if s.inv.FP != s.fp || len(s.inv.Addrs) == 0 || s.inv.Addrs[0] != s.addr {
					return fmt.Errorf("the invite names %v %q, the Tower serves at %s under %q", s.inv.Addrs, s.inv.FP, s.addr, s.fp)
				}
				return s.has("▀")
			})
			sc.Step(`^the line can be copied with the TUI's existing copy action$`, func() error {
				s.press("c")
				if s.copied == nil {
					return fmt.Errorf("c copied nothing")
				}
				return s.has("Copied to clipboard")
			})
			sc.Step(`^one panel shows the "roger join \.\.\." line, a copy hint, the QR and the minutes left$`, func() error {
				for _, want := range []string{"c copies the line", "▀", "15 min left"} {
					if err := s.has(want); err != nil {
						return err
					}
				}
				return nil
			})
			sc.Step(`^when the invite is used it shows "<name> joined" in place$`, func() error {
				in, _ := joininvite.Parse(joininvite.FromLine(s.line))
				if _, err := joinTower(loadConfig(), in, []string{"laptop-x"}, nil); err != nil {
					return err
				}
				s.refresh()
				return s.has("laptop-x joined")
			})

			// --- the console --------------------------------------------------------------------
			sc.Step(`^the owner has the web console open and no TUI$`, func() error { s.consoleOpen(); return nil })
			sc.Step(`^the owner has the web console open$`, func() error { s.consoleOpen(); return nil })
			sc.Step(`^the client asks to join$`, s.askLive)
			sc.Step(`^the console shows the request with its phrase and Approve and Refuse buttons$`, func() error {
				r, err := s.consoleRender("", "")
				if err != nil {
					return err
				}
				for _, want := range []string{"laptop-x wants to join · " + s.phrase(), "approve only if laptop-x shows the same phrase"} {
					if !strings.Contains(r.Text, want) {
						return fmt.Errorf("the console shows %q, without %q", r.Text, want)
					}
				}
				if !strings.Contains(strings.Join(r.Buttons, ","), "Approve,Refuse") {
					return fmt.Errorf("the console's buttons are %v", r.Buttons)
				}
				return nil
			})
			sc.Step(`^the owner presses Approve$`, func() error {
				r, err := s.consoleRender("Approve", "")
				if err != nil {
					return err
				}
				if r.Clicked == nil {
					return fmt.Errorf("Approve asked for nothing")
				}
				return s.consolePost("/api/towers/decide", r.Clicked, nil)
			})
			sc.Step(`^the client joins$`, func() error {
				if err := s.joined(); err != nil {
					return err
				}
				if !s.admitted() {
					return fmt.Errorf("the client's key was not admitted")
				}
				return nil
			})
			sc.Step(`^they press "Invite a device"$`, func() error {
				v, err := s.consoleInvite()
				s.shown = v
				return err
			})
			sc.Step(`^the console shows the "roger join \.\.\." line, a copy button and the QR$`, func() error {
				m := joinLineRE.FindStringSubmatch(s.shown.Text)
				if m == nil {
					return fmt.Errorf("the console shows no join line: %q", s.shown.Text)
				}
				in, err := joininvite.Parse(m[1])
				if err != nil || in.FP != s.fp {
					return fmt.Errorf("the console's invite %q does not name this Tower (%v)", m[1], err)
				}
				if !strings.Contains(strings.Join(s.shown.Buttons, ","), "Copy") || !strings.Contains(s.shown.Text, "▀") {
					return fmt.Errorf("no copy button or QR: %v %q", s.shown.Buttons, s.shown.Text)
				}
				return nil
			})
			sc.Step(`^the console's invite panel and request card use the same sentences as the TUI$`, func() error {
				s.screen()
				s.consoleOpen()
				if err := s.askLive(); err != nil {
					return err
				}
				s.refresh()
				r, err := s.consoleRender("", "")
				if err != nil {
					return err
				}
				for _, want := range []string{"laptop-x wants to join · " + s.phrase(), "approve only if laptop-x shows the same phrase"} {
					if !strings.Contains(r.Text, want) {
						return fmt.Errorf("the console lacks %q", want)
					}
					if err := s.has(want); err != nil {
						return err
					}
				}
				inv, err := s.consoleInvite()
				if err != nil {
					return err
				}
				if err := s.openInvite(); err != nil {
					return err
				}
				for _, want := range []string{"invite for one device, valid 15 min:", "on the other machine run:"} {
					if !strings.Contains(inv.Text, want) {
						return fmt.Errorf("the console's invite lacks %q: %q", want, inv.Text)
					}
					if err := s.has(want); err != nil {
						return err
					}
				}
				return nil
			})

			// --- the fingerprint where the owner looks ------------------------------------------
			sc.Step(`^`+"`roger tower status`"+`, the Tower row in the TUI and the console all show the short fingerprint$`, func() error {
				short := towerpin.Short(s.fp)
				var b strings.Builder
				if code := runTowerCLI(&b, "status", "--dir", s.dir); code != 0 || !strings.Contains(b.String(), short) {
					return fmt.Errorf("roger tower status said %q", b.String())
				}
				s.screen()
				if err := s.has("Tower lab · " + s.addr + " · " + short); err != nil {
					return err
				}
				return s.consoleShowsFingerprint(short)
			})
			sc.Step(`^`+"`roger tower status --full`"+` shows the full fingerprint$`, func() error {
				var b strings.Builder
				if code := runTowerCLI(&b, "status", "--dir", s.dir, "--full"); code != 0 || !strings.Contains(b.String(), "sha256:"+s.fp) {
					return fmt.Errorf("roger tower status --full said %q", b.String())
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{approveFeature + ":27", approveFeature + ":36", approveFeature + ":49", joinInviteFeature + ":84", joinInviteFeature + ":93",
				surfacesFeature + ":39", surfacesFeature + ":44", surfacesFeature + ":49", towerIdentityFeature + ":26"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the owner's join screens failed")
	}
}

func runTowerCLI(out io.Writer, args ...string) int { return towercli.Run(args, out, out) }

// consoleShowsFingerprint: the console's Towers block, drawn by the shipped JS, names the Tower
// with its short fingerprint.
func (s *ownerScreens) consoleShowsFingerprint(short string) error {
	s.consoleOpen()
	r, err := s.consoleRender("", "")
	if err != nil {
		return err
	}
	if want := "Tower lab · " + s.addr + " · " + short; !strings.Contains(r.Text, want) {
		return fmt.Errorf("the console shows %q, want %q", r.Text, want)
	}
	return nil
}

// consoleInvite presses the console's "Invite a device" and draws the invite it got back.
func (s *ownerScreens) consoleInvite() (consoleView, error) {
	r, err := s.consoleRender("Invite a device", "")
	if err != nil {
		return r, err
	}
	if r.Clicked == nil {
		return r, fmt.Errorf("the console shows no Invite a device button: %v", r.Buttons)
	}
	var inv json.RawMessage
	if err := s.consolePost("/api/towers/invite", r.Clicked, &inv); err != nil {
		return r, err
	}
	return s.consoleRender("", string(inv))
}

// consoleOpen is the web console as `roger webui` starts it, over the same hooks as the TUI.
func (s *ownerScreens) consoleOpen() {
	if s.console != "" {
		return
	}
	cfg := loadConfig()
	h := tuiHooks(cfg)
	s.console = startWebConsole(cfg, tui.NewController(cfg.Broker, h), "0", nil, &h)
}

func (s *ownerScreens) consoleURL(path string) string {
	u, _ := url.Parse(s.console)
	u.Path = path
	return u.String()
}

func (s *ownerScreens) consoleGet(path string, v any) error {
	u, _ := url.Parse(s.consoleURL(path))
	q := u.Query()
	q.Set("t", mustToken(s.console))
	u.RawQuery = q.Encode()
	resp, err := http.Get(u.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (s *ownerScreens) consolePost(path string, body, v any) error {
	b, _ := json.Marshal(body)
	u, _ := url.Parse(s.consoleURL(path))
	u.RawQuery = url.Values{"t": {mustToken(s.console)}}.Encode()
	req, _ := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", consoleOrigin(s.console))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s: %d %s", path, resp.StatusCode, rb)
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func mustToken(consoleURL string) string {
	u, _ := url.Parse(consoleURL)
	return u.Query().Get("t")
}

func consoleOrigin(consoleURL string) string {
	u, _ := url.Parse(consoleURL)
	return u.Scheme + "://" + u.Host
}

// consoleView is what the shipped console-towers.js drew: its text, its buttons, and what a click
// on the named button asked for.
type consoleView struct {
	Text    string   `json:"text"`
	Buttons []string `json:"buttons"`
	Clicked any      `json:"clicked"`
}

// consoleRender reads /api/towers, runs the shipped console-towers.js over it in node with a
// minimal document, and clicks the button labelled click (when given). invite, when set, is an
// invite JSON drawn with renderInvite instead.
func (s *ownerScreens) consoleRender(click, invite string) (consoleView, error) {
	var view json.RawMessage
	if err := s.consoleGet("/api/towers", &view); err != nil {
		return consoleView{}, err
	}
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		return consoleView{}, fmt.Errorf("node is needed to execute the console's JavaScript: %v", err)
	}
	script := `
const api = require(process.argv[1]);
const view = JSON.parse(process.argv[2]), click = process.argv[3], invite = process.argv[4];
function node(tag) {
  return { tag, children: [], textContent: "", className: "", hidden: false, handlers: {},
    get firstChild() { return this.children[0] || null; },
    appendChild(c) { this.children.push(c); return c; },
    removeChild(c) { this.children = this.children.filter((x) => x !== c); },
    addEventListener(ev, fn) { this.handlers[ev] = fn; } };
}
const boxes = { "edge-towers": node("div"), "edge-invite": node("div") };
const doc = { createElement: node, getElementById: (id) => boxes[id] || null };
let clicked = null;
const on = { invite: (t) => { clicked = { dir: t.dir }; }, decide: (r, ok) => { clicked = { dir: r.dir, id: r.id, approve: ok }; },
  copy: (line) => { clicked = { copy: line }; } };
const box = invite ? boxes["edge-invite"] : boxes["edge-towers"];
if (invite) api.renderInvite(doc, JSON.parse(invite), null, on); else api.render(doc, view, on);
const texts = [], buttons = [];
(function walk(n) { if (n.tag === "button") { buttons.push(n.textContent); if (n.textContent === click) n.handlers.click(); }
  else if (n.textContent) texts.push(n.textContent); n.children.forEach(walk); })(box);
process.stdout.write(JSON.stringify({ text: texts.join("\n"), buttons, clicked }));
`
	out, err := exec.Command(nodeBin, "-e", script, "../../internal/webui/assets/console-towers.js", string(view), click, invite).CombinedOutput()
	if err != nil {
		return consoleView{}, fmt.Errorf("console-towers.js failed: %v: %s", err, out)
	}
	var r consoleView
	if err := json.Unmarshal(out, &r); err != nil {
		return consoleView{}, fmt.Errorf("unexpected output %q: %v", out, err)
	}
	return r, nil
}
