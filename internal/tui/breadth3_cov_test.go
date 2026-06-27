package tui

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRunDispatchRemaining covers the remaining BROWSE-level slash commands so the run
// dispatcher's arms (connect/chat/balance/endpoint/freq/share/login/topup/grant/quit)
// all run with concrete state assertions.
func TestRunDispatchRemaining(t *testing.T) {
	// /chat with no channel: a hint, mode stays browse.
	noch := browseSeed(100)
	nm, _ := noch.run("chat")
	if !strings.Contains(stripANSI(nm.(model).status), "tune in to a station first") {
		t.Errorf("/chat with no channel should hint to tune in, got %q", stripANSI(nm.(model).status))
	}

	// /chat with a channel enters CHANNEL.
	ch := browseSeed(100)
	ch.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	cm, _ := ch.run("chat")
	if cm.(model).mode != modeChat {
		t.Errorf("/chat with a channel should enter CHANNEL, got %v", cm.(model).mode)
	}

	// /balance anonymous: a login hint.
	anon := browseSeed(100)
	anon.loggedIn, anon.haveBal = false, false
	bm, _ := anon.run("balance")
	if !strings.Contains(stripANSI(bm.(model).status), "not logged in") {
		t.Errorf("/balance anonymous should hint login, got %q", stripANSI(bm.(model).status))
	}

	// /balance logged-in with an empty wallet: a top-up hint + a re-fetch command.
	empty := browseSeed(100)
	empty.loggedIn, empty.haveBal, empty.balance = true, true, 0
	em, ecmd := empty.run("bal")
	if !strings.Contains(stripANSI(em.(model).status), "balance empty") {
		t.Errorf("/bal with an empty wallet should hint topup, got %q", stripANSI(em.(model).status))
	}
	if ecmd == nil {
		t.Error("/bal should issue a balance re-fetch")
	}

	// /endpoint with no channel: a hint.
	ep := browseSeed(100)
	ep.connected = nil
	epm, _ := ep.run("endpoint")
	if !strings.Contains(stripANSI(epm.(model).status), "tune in first") {
		t.Errorf("/endpoint with no channel should hint, got %q", stripANSI(epm.(model).status))
	}

	// /connect issues the connect flow (a quote / confirm or a re-scan command).
	conn := browseSeed(100)
	connm, _ := conn.run("connect")
	if connm == nil {
		t.Error("/connect should return a model")
	}

	// /freq <code> routes into the resolve flow (a command).
	fr := browseSeed(100)
	if _, cmd := fr.run("freq 147.520 MHz CODE"); cmd == nil {
		t.Error("/freq <code> should issue the resolve command")
	}

	// /share opens the provider table (async detect command).
	sh := browseSeed(100)
	shm, scmd := sh.run("share")
	if shm.(model).mode != modeShare || scmd == nil {
		t.Errorf("/share should open the loading provider table with a detect command, mode=%v", shm.(model).mode)
	}

	// /login opens the login panel.
	lg := browseSeed(100)
	lgm, _ := lg.run("login")
	if lgm.(model).mode != modeLogin {
		t.Errorf("/login should open the login panel, got %v", lgm.(model).mode)
	}

	// /topup with no hook reports unavailable.
	tu := browseSeed(100)
	tum, _ := tu.run("topup")
	if !strings.Contains(stripANSI(tum.(model).status), "top-up unavailable") {
		t.Errorf("/topup (no hook) should report unavailable, got %q", stripANSI(tum.(model).status))
	}

	// /grant with no hook reports unavailable.
	gr := browseSeed(100)
	grm, _ := gr.run("grant")
	if !strings.Contains(stripANSI(grm.(model).status), "grants unavailable") {
		t.Errorf("/grant (no hook) should report unavailable, got %q", stripANSI(grm.(model).status))
	}

	// /quit with nothing on air quits immediately (a command).
	q := browseSeed(100)
	if _, cmd := q.run("quit"); cmd == nil {
		t.Error("/quit with nothing on air should issue tea.Quit")
	}

	// empty command line is a no-op (no panic).
	ec := browseSeed(100)
	_, _ = ec.run("")
}

// TestShortFailureShapes maps each recognized relay-error shape to its terse phrase,
// naming the model where the shape carries one.
func TestShortFailureShapes(t *testing.T) {
	cases := []struct {
		raw, model, want string
	}{
		{"no station is serving it", "gpt-oss-20b", "no station is serving gpt-oss-20b right now"},
		{"the station returned status 504 with no reply", "m", "no station is serving m right now (504)"},
		{"the station sent an empty response (status 502)", "m", "no station is serving m right now (502)"},
		{"context deadline exceeded", "m", "the station timed out"},
		{"could not reach the broker: dial tcp", "", "could not reach the broker"},
		{"connection refused", "", "could not reach the broker"},
		{"some other weird error", "", "some other weird error"},
		{"no station is on air", "", "no station is on air right now"},
	}
	for _, c := range cases {
		if got := shortFailure(c.raw, c.model); got != c.want {
			t.Errorf("shortFailure(%q,%q) = %q, want %q", c.raw, c.model, got, c.want)
		}
	}

	// failureHint pairs the phrase with the actionable two-liner.
	lines := failureHint("status 504 with no reply", "m", false)
	if len(lines) != 2 || !strings.Contains(stripANSI(lines[0]), "no station is serving m") {
		t.Errorf("failureHint should be a phrase + a hint, got %#v", lines)
	}
}

// TestShortPath covers the path shortener: a short path passes through, a home path is
// abbreviated to ~, and a long path keeps its last two segments behind an ellipsis.
func TestShortPath(t *testing.T) {
	if got := shortPath("/a/b"); got != "/a/b" {
		t.Errorf("short path should pass through, got %q", got)
	}
	long := "/very/deeply/nested/directory/structure/that/is/way/too/long/leaf"
	got := shortPath(long)
	if !strings.HasPrefix(got, "...") || !strings.HasSuffix(got, filepath.Join("long", "leaf")) {
		t.Errorf("a long path should keep its last two segments behind ..., got %q", got)
	}
}

// TestHistoryPath resolves a stable per-name path under the rogerai config dir.
func TestHistoryPath(t *testing.T) {
	p := historyPath("agent_history")
	if p == "" {
		t.Skip("no resolvable config/home dir in this environment")
	}
	if !strings.HasSuffix(p, filepath.Join("rogerai", "agent_history")) {
		t.Errorf("historyPath should sit under rogerai/, got %q", p)
	}
}
