package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// browseWithBands builds a sized BROWSE model with two live bands so the s/S keys
// have a real list to act on.
func browseWithBands(t *testing.T) tea.Model {
	t.Helper()
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "a", Model: "alpha", PriceOut: 0.9, Online: true, TPS: 10},
		{NodeID: "b", Model: "bravo", PriceOut: 0.1, Online: true, TPS: 90},
	})
	m, _ = m.Update(tickMsg{})
	return m
}

// TestSortKeyDistinctFromShare: capital S cycles the sort dial and NEVER opens
// SHARE; lowercase s opens SHARE. The two are case-distinct so arrow-nav + [2]
// still reach Share but S is sort-only (fix #1).
func TestSortKeyDistinctFromShare(t *testing.T) {
	m := browseWithBands(t)

	// Capital S advances the sort and stays in BROWSE - it must NOT open SHARE.
	before := asModel(m).sortMode
	m = keyPress(m, "S")
	after := asModel(m)
	if after.mode != modeBrowse {
		t.Errorf("S must NOT open SHARE - expected to stay in BROWSE, got %v", after.mode)
	}
	if after.inShareSection() {
		t.Errorf("S must NOT enter the SHARE section")
	}
	if after.sortMode == before {
		t.Errorf("S should advance the sort dial (was %d, still %d)", before, after.sortMode)
	}

	// Lowercase s opens SHARE.
	s := keyPress(browseWithBands(t), "s")
	if !asModel(s).inShareSection() {
		t.Errorf("lowercase s should open SHARE, got mode %v", asModel(s).mode)
	}
}

// TestLoginPanelIsConfirmable: pressing [L]/arrowing to it opens a confirm panel
// and NEVER acts on its own (fix #5). Logged out -> press-enter prompt; only an
// explicit ENTER starts the flow. Logged in -> a logout confirm; y logs out, n/esc
// keep the session.
func TestLoginPanelIsConfirmable(t *testing.T) {
	began := false
	hooks := Hooks{
		LoginBegin: func(broker, id string) (LoginDevice, error) {
			began = true
			return LoginDevice{VerificationURI: "https://github.com/login/device", UserCode: "FD9D-8F33", Handle: "h"}, nil
		},
		LoginPoll: func(broker, id string, d LoginDevice) (string, error) { return "octocat", nil },
		Logout:    func() error { return nil },
	}
	base := NewWithHooks("http://broker.local", "tester", nil, hooks)
	base.width, base.height = 100, 30

	// LOGGED OUT: [L] opens the panel; arrowing in does NOT start the flow.
	var m tea.Model = base
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	if asModel(m).mode != modeLogin {
		t.Fatalf("[L] should open the login panel, got %v", asModel(m).mode)
	}
	if began {
		t.Errorf("opening / arrowing onto [L] must NOT start the device flow")
	}
	if !strings.Contains(stripANSI(asModel(m).View()), "press enter") {
		t.Errorf("logged-out panel should show the press-enter prompt:\n%s", stripANSI(asModel(m).View()))
	}
	// ENTER inside the panel starts the flow.
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		m, _ = m.Update(cmd())
	}
	if !began {
		t.Errorf("ENTER in the logged-out panel should start the device flow")
	}

	// LOGGED IN: [L] opens the LOGOUT confirm; y logs out, n keeps the session.
	li := base
	li.ghLogin = "octocat"
	li.loggedIn = true
	var lm tea.Model = li
	lm, _ = lm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	if asModel(lm).mode != modeLogin {
		t.Fatalf("[L] should open the logout panel, got %v", asModel(lm).mode)
	}
	if got := stripANSI(asModel(lm).View()); !strings.Contains(got, "log out?") {
		t.Errorf("logged-in panel should show the logout confirm:\n%s", got)
	}
	// n keeps the session.
	keep, _ := lm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if !asModel(keep).loggedInState() {
		t.Errorf("n must keep the user logged in")
	}
	// esc keeps the session too.
	esc, _ := lm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !asModel(esc).loggedInState() {
		t.Errorf("esc must keep the user logged in")
	}
	// y logs out (run the returned cmd + its message).
	var ycmd tea.Cmd
	out, ycmd := lm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if ycmd != nil {
		out, _ = out.Update(ycmd())
	}
	if asModel(out).loggedInState() {
		t.Errorf("y must log the user out (session cleared)")
	}
}

// TestLoginPanelLabelFlips: the preset label reads LOGIN when logged out and
// LOGOUT when logged in (fix #5 + #4).
func TestLoginPanelLabelFlips(t *testing.T) {
	out := New("http://broker.local", "tester")
	out.width, out.height = 100, 30
	if !strings.Contains(stripANSI(out.View()), "LOGIN") {
		t.Errorf("logged-out preset bar should read [L] LOGIN:\n%s", stripANSI(out.View()))
	}

	in := out
	in.ghLogin = "octocat"
	in.loggedIn = true
	v := stripANSI(in.View())
	if !strings.Contains(v, "LOGOUT") {
		t.Errorf("logged-in preset bar should read [L] LOGOUT:\n%s", v)
	}
}

// TestLoginDevicePanelRenders: the in-flight device-flow panel is left-aligned,
// shows the URL + code + a waiting line, and survives NO_COLOR at a narrow width
// (no overflow) (fix #2/#3).
func TestLoginDevicePanelRenders(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, width := range []int{100, 44} {
		m := New("http://broker.local", "tester")
		m.width, m.height = width, 30
		m.mode = modeLogin
		m.loginWaiting = true
		m.loginDevice = LoginDevice{VerificationURI: "https://github.com/login/device", UserCode: "FD9D-8F33"}
		v := stripANSI(m.View())
		for _, want := range []string{"GITHUB LOGIN", "https://github.com/login/device", "FD9D-8F33", "waiting for authorization"} {
			if !strings.Contains(v, want) {
				t.Errorf("width %d: device panel missing %q:\n%s", width, want, v)
			}
		}
		// The code line is LEFT-aligned (under the labels), not centered.
		if !strings.Contains(v, "code   FD9D-8F33") {
			t.Errorf("width %d: code should be left-aligned by its label:\n%s", width, v)
		}
	}
}

// TestHeaderShowsLoginState: the header shows ✓ @username when logged in (with the
// resolved login name, not blank) and the logged-out hint when anonymous (fix #4).
// The identity callsign is the lineage/verified-operator ✓ - NOT the confidential ◆,
// which is reserved for TEE-attested nodes.
func TestHeaderShowsLoginState(t *testing.T) {
	in := New("http://broker.local", "tester")
	in.width, in.height = 100, 30
	in.ghLogin = "octocat"
	var lm tea.Model = in
	lm, _ = lm.Update(balanceMsg{balance: 12.5, loggedIn: true})
	v := stripANSI(lm.View())
	if !strings.Contains(v, "@octocat") {
		t.Errorf("logged-in header should show %s @octocat (resolved name):\n%s", glyphLineage, v)
	}
	if !strings.Contains(v, glyphLineage) { // the ✓ verified-operator mark
		t.Errorf("logged-in header should carry the %s verified-operator callsign mark:\n%s", glyphLineage, v)
	}
	// The identity mark must NOT be the confidential ◆ (no TEE node is involved here).
	if strings.Contains(v, glyphConf) {
		t.Errorf("logged-in identity must not use the confidential ◆ mark:\n%s", v)
	}

	out := New("http://broker.local", "tester")
	out.width, out.height = 100, 30
	var om tea.Model = out
	om, _ = om.Update(balanceMsg{loggedIn: false})
	ov := stripANSI(om.View())
	if !strings.Contains(ov, "not logged in") || !strings.Contains(ov, "/login") {
		t.Errorf("logged-out header should show the not-logged-in · /login hint:\n%s", ov)
	}
}

// TestOpenURLCommandPerOS: the open-URL helper picks the right launcher per GOOS.
// We assert the command selection only (we never exec it).
func TestOpenURLCommandPerOS(t *testing.T) {
	const url = "https://github.com/login/device"
	cases := []struct {
		goos     string
		wantName string
		wantArg0 string
	}{
		{"linux", "xdg-open", url},
		{"freebsd", "xdg-open", url},
		{"darwin", "open", url},
		{"windows", "rundll32", "url.dll,FileProtocolHandler"},
	}
	for _, c := range cases {
		name, args := openURLCommand(c.goos, url)
		if name != c.wantName {
			t.Errorf("%s: command = %q, want %q", c.goos, name, c.wantName)
		}
		if len(args) == 0 || args[0] != c.wantArg0 {
			t.Errorf("%s: first arg = %v, want %q", c.goos, args, c.wantArg0)
		}
		// The URL must always be passed (last arg) so the launcher actually opens it.
		if args[len(args)-1] != url {
			t.Errorf("%s: URL not passed to launcher: %v", c.goos, args)
		}
	}
}
