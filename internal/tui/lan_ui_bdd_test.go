package tui

// Executable spec: features/lan/ui_visibility.feature and money_and_accounts.feature (@tui). The
// REAL model renders with the shared LAN dial set (SetLANDial), exactly as `roger --lan` sets it.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"rogerai.fm/roger/v7/internal/bddrun"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
	"github.com/muesli/termenv"

	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge"
)

type lanUIBDD struct {
	t          *testing.T
	m          model
	out        string
	bands      []client.LANBand
	edgeUnread string
	asked      bool // the recording broker saw a request
	world      pingWorldModel
}

func (s *lanUIBDD) reset() {
	s.bands = []client.LANBand{
		{Model: "qwen3-8b", Sources: []client.LANSource{{Kind: client.LANSelf, Verified: true}}},
		{Model: "gpt-oss-20b", Sources: []client.LANSource{{Kind: client.LANEdge, ID: "jetson", Verified: true}}},
		{Model: "llama-3.3-70b", Sources: []client.LANSource{{Kind: client.LANTower, ID: "192.168.1.20:8787", Addr: "192.168.1.20:8787"}}},
	}
	s.edgeUnread, s.asked = "", false
	SetLANDial(func() client.LANDial { return client.LANDial{Bands: s.bands, EdgeUnread: s.edgeUnread} })
	s.out = ""
}

// seed renders a TUI at width w whose dial came from fetchOffers (the LAN dial in LAN mode),
// logged in with a balance, the way a running `roger --lan` would be.
func (s *lanUIBDD) seed(w int, broker string) {
	var tm tea.Model = New(broker, "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: w, Height: 30})
	tm, _ = tm.Update(fetchOffers(broker)())
	tm, _ = tm.Update(balanceMsg{balance: 42.17, loggedIn: true})
	tm, _ = tm.Update(tickMsg{})
	s.m = tm.(model)
}

func (s *lanUIBDD) headerHas(want string) error {
	// the header is everything above its rule line
	var head strings.Builder
	for _, ln := range strings.Split(stripANSI(s.out), "\n") {
		if t := strings.TrimSpace(ln); len(t) > 8 && strings.Trim(t, "─-") == "" {
			break
		}
		head.WriteString(ln + "\n")
	}
	if !regexp.MustCompile(`\b` + want + `\b`).MatchString(head.String()) {
		return fmt.Errorf("the header lacks %q:\n%s", want, head.String())
	}
	return nil
}

func TestLANUIBDD(t *testing.T) {
	s := &lanUIBDD{t: t}
	paths := []string{}
	for _, l := range []int{31, 43, 49, 55, 61, 66, 72} {
		paths = append(paths, fmt.Sprintf("../../features/lan/ui_visibility.feature:%d", l))
	}
	for _, l := range []int{35, 100} {
		paths = append(paths, fmt.Sprintf("../../features/lan/money_and_accounts.feature:%d", l))
	}
	paths = append(paths, "../../features/lan/share_in_lan_mode.feature:64")
	for _, l := range []int{220, 228, 330, 341, 367} {
		paths = append(paths, fmt.Sprintf("../../features/lan/discovery_and_dial.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				SetLANDial(nil)
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			var edgeStatus *edge.SelfStatus
			sc.Step(`^edge\.prefer is "([^"]*)"$`, func(v string) error {
				edgeStatus = &edge.SelfStatus{Enrolled: true, Root: edge.RootLocal, Authority: "shed", AuthorityLocal: true, Prefer: v}
				return nil
			})
			sc.Step(`^the Edge screen renders$`, func() error {
				s.seed(120, "http://broker.invalid")
				s.out = edgeModeBadge(edgeState{status: edgeStatus})
				return nil
			})
			sc.Step(`^the header shows the root badge and "LAN"$`, func() error {
				if !strings.Contains(s.out, edgeStatus.RootBadge()) || !strings.Contains(s.out, "LAN") {
					return fmt.Errorf("the Edge header is %q, want the root badge %q and LAN", s.out, edgeStatus.RootBadge())
				}
				return nil
			})
			sc.Step(`^it does not show "LOCAL" as the preference$`, func() error {
				if strings.Contains(s.out, edgeStatus.PreferBadge()) || strings.Contains(s.out, "LOCAL-ONLY") {
					return fmt.Errorf("the Edge header still shows the preference: %q", s.out)
				}
				return nil
			})
			sc.Step(`^the TUI renders at (\d+) columns ?(in the windowshade)?$`, func(w int, shade string) error {
				s.seed(w, "http://broker.invalid")
				s.m.compact = shade != ""
				s.out = s.m.View()
				return nil
			})
			sc.Step(`^the header contains the text "([^"]*)"$`, s.headerHas)
			sc.Step(`^NO_COLOR is set$`, func() error { s.t.Setenv("NO_COLOR", "1"); return nil })
			sc.Step(`^the header renders$`, func() error {
				s.seed(100, "http://broker.invalid")
				s.out = s.m.View()
				return nil
			})
			sc.Step(`^the LAN badge is readable as text alone$`, func() error { return s.headerHas("LAN") })
			sc.Step(`^the header renders with colour$`, func() error {
				prev := lipgloss.ColorProfile()
				lipgloss.SetColorProfile(termenv.ANSI256)
				defer lipgloss.SetColorProfile(prev)
				s.out = lanBadge(false)
				return nil
			})
			sc.Step(`^the LAN badge is not drawn in the brand red$`, func() error {
				prev := lipgloss.ColorProfile()
				lipgloss.SetColorProfile(termenv.ANSI256)
				defer lipgloss.SetColorProfile(prev)
				red := strings.TrimSuffix(stRed.Render("x"), "x\x1b[0m")
				if red == "" || strings.Contains(s.out, red) {
					return fmt.Errorf("the badge carries the red style: %q", s.out)
				}
				return nil
			})
			sc.Step(`^the brand red dot is unchanged$`, func() error {
				if stRed.GetForeground() != cRed {
					return fmt.Errorf("the brand red changed")
				}
				return nil
			})
			sc.Step(`^(\d+) local bands and (\d+) market bands are on air$`, func(local, market int) error {
				if local != len(s.bands) {
					return fmt.Errorf("the scenario's LAN dial has %d bands", len(s.bands))
				}
				asked := false
				broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					asked = true
					var b strings.Builder
					b.WriteString(`{"offers":[`)
					for i := 0; i < market; i++ {
						if i > 0 {
							b.WriteString(",")
						}
						fmt.Fprintf(&b, `{"model":"market-%d","node_id":"n%d","online":true}`, i, i)
					}
					b.WriteString(`]}`)
					_, _ = w.Write([]byte(b.String()))
				}))
				s.t.Cleanup(broker.Close)
				s.seed(120, broker.URL)
				if asked {
					return fmt.Errorf("the TUI asked the market for bands in LAN mode")
				}
				s.out = s.m.View()
				return nil
			})
			sc.Step(`^it reads "([^"]*)"$`, func(want string) error {
				if !strings.Contains(stripANSI(s.out), want) {
					return fmt.Errorf("it does not read %q:\n%s", want, stripANSI(s.out))
				}
				return nil
			})
			// discovery_and_dial: the dial's rows, and the empty LAN as a status.
			sc.Step(`^the same three sources$`, func() error { return nil })
			sc.Step(`^the dial renders at (\d+) columns$`, func(w int) error {
				s.seedRecorded(w)
				s.out = stripANSI(s.m.browseView(w))
				return nil
			})
			sc.Step(`^the "([^"]*)" row's source reads "([^"]*)"$`, func(band, want string) error {
				row := lanRow(s.out, band)
				if row == "" {
					return fmt.Errorf("no %q row:\n%s", band, s.out)
				}
				if !strings.Contains(row, want) {
					return fmt.Errorf("the %q row does not name its source %q: %q", band, want, row)
				}
				return nil
			})
			sc.Step(`^no row shows a \$/1M price$`, func() error {
				if strings.Contains(s.out, "$/1M") || regexp.MustCompile(`(^|\s)\$?\d+\.\d\d(\s|$)`).MatchString(s.out) {
					return fmt.Errorf("the LAN dial shows a price:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^each row shows "([^"]*)"$`, func(want string) error {
				for _, b := range s.bands {
					if row := lanRow(s.out, b.Model); !strings.Contains(strings.ToLower(row), want) {
						return fmt.Errorf("the %q row does not show %q: %q", b.Model, want, row)
					}
				}
				return nil
			})
			sc.Step(`^this machine serves nothing$`, func() error { s.bands = nil; return nil })
			sc.Step(`^this Edge has no member serving a band$`, func() error { return nil })
			sc.Step(`^lan_towers is empty and the broker setting is not local$`, func() error { return nil })
			sc.Step(`^the Edge record cannot be read$`, func() error {
				s.bands, s.edgeUnread = nil, "open edge.json: permission denied"
				return nil
			})
			sc.Step(`^it says no local band is on air$`, func() error { return s.outHas("no local band is on air") })
			sc.Step(`^it says what it checked: this machine, the Edge, and Towers$`, func() error {
				line := lanLineWith(s.out, "checked")
				for _, w := range []string{"this machine", "Edge", "Tower"} {
					if !strings.Contains(line, w) {
						return fmt.Errorf("the checked line does not name %q: %q\n%s", w, line, s.out)
					}
				}
				return nil
			})
			sc.Step(`^it names the three ways to get one: serve a model here, add an Edge member, add a Tower$`, func() error {
				for _, w := range []string{"serve a model here", "add an Edge member", "add a Tower"} {
					if err := s.outHas(w); err != nil {
						return err
					}
				}
				return nil
			})
			sc.Step(`^it does not show market bands as a fallback$`, func() error {
				for _, w := range []string{"no stations on air", "OPEN MARKET", "broker"} {
					if strings.Contains(s.out, w) {
						return fmt.Errorf("the empty LAN dial reads like the market (%q):\n%s", w, s.out)
					}
				}
				if s.asked {
					return fmt.Errorf("the empty LAN dial asked the broker")
				}
				return nil
			})
			sc.Step(`^it says the Edge could not be read, with the reason$`, func() error {
				if err := s.outHas("Edge could not be read"); err != nil {
					return err
				}
				return s.outHas("permission denied")
			})
			sc.Step(`^it does not claim the Edge serves nothing$`, func() error {
				if line := lanLineWith(s.out, "checked"); strings.Contains(line, "Edge") {
					return fmt.Errorf("an unread Edge is listed as checked: %q", line)
				}
				return nil
			})
			// discovery_and_dial:367 - the standalone Ping World (`roger --lan --ping`) in LAN mode.
			sc.Step(`^the Ping World screen opens$`, func() error {
				broker := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
					s.asked = true
					_, _ = rw.Write([]byte(`{"offers":[{"model":"market-band","node_id":"n1","online":true}]}`))
				}))
				s.t.Cleanup(broker.Close)
				var pm tea.Model = pingWorldModel{w: 100, h: 30, seed: 7, broker: broker.URL}
				pm, _ = pm.Update(worldFetch(broker.URL)())
				s.world = pm.(pingWorldModel)
				s.out = stripANSI(pm.View())
				return nil
			})
			sc.Step(`^no request is made to the public broker's /discover$`, func() error {
				if s.asked {
					return fmt.Errorf("Ping World asked the broker's /discover in LAN mode")
				}
				return nil
			})
			sc.Step(`^it draws the local sources only, or says it has nothing local to draw$`, func() error {
				if len(s.bands) == 0 {
					return s.outHas("nothing local on air")
				}
				// What the world draws its towers from: exactly the LAN bands, no market band.
				want := map[string]bool{}
				for _, b := range s.bands {
					want[b.Model] = true
				}
				if s.world.data == nil || len(s.world.data.stations) != len(want) {
					return fmt.Errorf("Ping World draws %+v, want the %d LAN bands", s.world.data, len(want))
				}
				for _, st := range s.world.data.stations {
					if !want[st.model] {
						return fmt.Errorf("Ping World draws a band that is not local: %q", st.model)
					}
				}
				return nil
			})
			sc.Step(`^the dial renders$`, func() error {
				s.seedRecorded(120)
				s.out = stripANSI(s.m.View())
				return nil
			})
			sc.Step(`^its title names LAN$`, func() error {
				if !strings.Contains(stripANSI(s.out), "LAN DIAL") {
					return fmt.Errorf("the dial's title does not name LAN:\n%s", stripANSI(s.out))
				}
				return nil
			})
			sc.Step(`^the owner tuned "([^"]*)" from the LAN Tower at ([0-9.:]+)$`, func(band, _ string) error {
				s.seed(120, "http://broker.invalid")
				for i := range s.m.offers {
					if s.m.offers[i].Model == band {
						o := s.m.offers[i]
						s.m.connected = &o
					}
				}
				if s.m.connected == nil {
					return fmt.Errorf("%s is not on the LAN dial", band)
				}
				return nil
			})
			sc.Step(`^the connected plate renders$`, func() error { s.out = s.m.View(); return nil })
			sc.Step(`^it names "([^"]*)" and "([^"]*)"$`, func(a, b string) error {
				o := stripANSI(s.out)
				for _, want := range []string{a, b} {
					if !strings.Contains(o, want) {
						return fmt.Errorf("the plate does not name %q:\n%s", want, o)
					}
				}
				return nil
			})
			sc.Step(`^the TUI is open in LAN mode$`, func() error { s.seed(120, "http://broker.invalid"); return nil })
			sc.Step(`^the owner selects a local band to tune$`, func() error {
				s.m.haveBal, s.m.balance, s.m.showDetail = true, 42.17, true
				for _, bd := range s.m.bands {
					if bd.model == "llama-3.3-70b" {
						s.m.q = quote{b: bd, typical: 800}
					}
				}
				s.m.mode = modeConnectConfirm
				s.out = s.m.confirmView(120)
				return nil
			})
			sc.Step(`^the confirm plate shows the band, its source and "([^"]*)"$`, func(want string) error {
				o := stripANSI(s.out)
				for _, w := range []string{"llama-3.3-70b", "tower: 192.168.1.20", want} {
					if !strings.Contains(o, w) {
						return fmt.Errorf("the confirm plate lacks %q:\n%s", w, o)
					}
				}
				return nil
			})
			sc.Step(`^it shows no balance, no estimated cost and no hold text$`, func() error {
				o := strings.ToLower(stripANSI(s.out))
				for _, w := range []string{"balance", "/ reply", "hold", "$/1m"} {
					if strings.Contains(o, w) {
						return fmt.Errorf("the confirm plate shows %q:\n%s", w, o)
					}
				}
				return nil
			})
			sc.Step(`^the TUI is open in LAN mode and logged in$`, func() error { s.seed(120, "http://broker.invalid"); return nil })
			sc.Step(`^it shows the LAN badge where the balance tag would be$`, func() error {
				if err := s.headerHas("LAN"); err != nil {
					return err
				}
				if head := stripANSI(s.m.header(120)); strings.Contains(head, "$42.17") {
					return fmt.Errorf("the header still carries the balance tag:\n%s", head)
				}
				return nil
			})
			sc.Step("^this machine serves \"([^\"]*)\" with `roger model serve`$", func(string) error { return nil })
			sc.Step(`^SHARE is on in LAN mode, serving the Tower at ([0-9.:]+) and the Edge$`, func(addr string) error {
				s.m = New("http://127.0.0.1:1", "tester")
				s.m.hooks.LANTowers = func() []string { return []string{"http://" + addr} }
				s.m.shareRows = []shareRow{{model: "qwen3-8b"}, {model: "gpt-oss-20b"}}
				s.m.shares = map[string]*agent.Session{"qwen3-8b": {}}
				return nil
			})
			sc.Step(`^the SHARE section renders$`, func() error { s.out = stripANSI(s.m.shareView(120)); return nil })
			sc.Step(`^it reads "([^"]*)" and lists "([^"]*)" and "([^"]*)"$`, func(a, b, c string) error {
				for _, want := range []string{a, b, c} {
					if !strings.Contains(s.out, want) {
						return fmt.Errorf("SHARE lacks %q:\n%s", want, s.out)
					}
				}
				return nil
			})
			sc.Step(`^it shows no earnings, price or payout text$`, func() error {
				low := strings.ToLower(s.out)
				for _, bad := range []string{"earn", "price", "payout", "at list", "$"} {
					if strings.Contains(low, bad) {
						return fmt.Errorf("SHARE in LAN mode shows %q:\n%s", bad, s.out)
					}
				}
				return nil
			})
			sc.Step(`^the balance is still reachable from the account section$`, func() error {
				if !strings.Contains(stripANSI(s.m.accountTag(false)), "$42.17") {
					return fmt.Errorf("the account section lost the balance")
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("LAN UI scenarios failed")
	}
}

// seedRecorded renders a LAN TUI at width w against a recording broker, so a step can say the
// broker was never asked.
func (s *lanUIBDD) seedRecorded(w int) {
	broker := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		s.asked = true
		_, _ = rw.Write([]byte(`{"offers":[{"model":"market-band","node_id":"n1","online":true}]}`))
	}))
	s.t.Cleanup(broker.Close)
	s.seed(w, broker.URL)
}

func (s *lanUIBDD) outHas(want string) error {
	if !strings.Contains(s.out, want) {
		return fmt.Errorf("it does not say %q:\n%s", want, s.out)
	}
	return nil
}

// lanRow is the dial line naming band, or "".
func lanRow(out, band string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, " "+band+" ") || strings.HasSuffix(strings.TrimRight(ln, " "), " "+band) {
			return ln
		}
	}
	return ""
}

// lanLineWith is the first line containing want, or "".
func lanLineWith(out, want string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, want) {
			return ln
		}
	}
	return ""
}
