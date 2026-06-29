package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/detect"
)

// TestShareRowsFlattenAcrossEndpoints is the SHARE-shows-only-one-model regression:
// on a multi-endpoint box, /share must list EVERY detected server x its models
// (de-duped by id), each row carrying its OWN upstream, not just found[0]'s models.
func TestShareRowsFlattenAcrossEndpoints(t *testing.T) {
	found := []detect.Found{
		{Name: "cpu-bots", BaseURL: "http://127.0.0.1:8060/v1", Chat: "http://127.0.0.1:8060/v1/chat/completions", Models: []string{"gpt-oss-20b"}},
		{Name: "llama.cpp", BaseURL: "http://127.0.0.1:8080/v1", Chat: "http://127.0.0.1:8080/v1/chat/completions", Models: []string{"gpt-oss-120b"}},
		{Name: "port:8788", BaseURL: "http://127.0.0.1:8788/v1", Chat: "http://127.0.0.1:8788/v1/chat/completions", Models: []string{"duck-auto", "duck-fast", "gpt-oss-20b"}}, // dup id
		{Name: "port:8081", BaseURL: "http://127.0.0.1:8081/v1", Chat: "http://127.0.0.1:8081/v1/chat/completions", Models: []string{"qwen3-vl-8b"}},
	}
	mm := New("http://broker.local", "tester")
	mm.loadShareRows(found)

	// Every distinct model is present (the dup gpt-oss-20b appears once).
	want := []string{"gpt-oss-20b", "gpt-oss-120b", "duck-auto", "duck-fast", "qwen3-vl-8b"}
	got := map[string]string{} // model -> upstream
	for _, r := range mm.shareRows {
		got[r.model] = r.upstream
	}
	if len(mm.shareRows) != len(want) {
		t.Fatalf("flattened rows = %d (%v), want %d distinct models %v", len(mm.shareRows), got, len(want), want)
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("flattened share table missing model %q (rows=%v)", w, got)
		}
	}

	// Each row carries the upstream of the server that actually serves it.
	cases := map[string]string{
		"gpt-oss-20b":  "http://127.0.0.1:8060/v1/chat/completions", // first server wins the dup
		"gpt-oss-120b": "http://127.0.0.1:8080/v1/chat/completions",
		"qwen3-vl-8b":  "http://127.0.0.1:8081/v1/chat/completions",
		"duck-auto":    "http://127.0.0.1:8788/v1/chat/completions",
	}
	for mdl, up := range cases {
		if got[mdl] != up {
			t.Errorf("row %q upstream = %q, want %q", mdl, got[mdl], up)
		}
	}
}

// TestSavedModelSortsFirst: the saved onboarding model is placed at the cursor (row 0)
// even when it is served by a later endpoint, so the obvious default is selected.
func TestSavedModelSortsFirst(t *testing.T) {
	found := []detect.Found{
		{Name: "a", Chat: "http://127.0.0.1:8060/v1/chat/completions", Models: []string{"gpt-oss-20b"}},
		{Name: "b", Chat: "http://127.0.0.1:8081/v1/chat/completions", Models: []string{"qwen3-vl-8b"}},
	}
	mm := NewWithHooks("http://broker.local", "tester", nil, Hooks{ShareModel: "qwen3-vl-8b"})
	mm.loadShareRows(found)
	if len(mm.shareRows) == 0 || mm.shareRows[0].model != "qwen3-vl-8b" {
		t.Fatalf("saved model should sort first, rows=%v", mm.shareRows)
	}
	// And it keeps its own upstream after the sort.
	if mm.shareRows[0].upstream != "http://127.0.0.1:8081/v1/chat/completions" {
		t.Errorf("sorted-first row lost its upstream: %q", mm.shareRows[0].upstream)
	}
}

// TestToggleShareUsesRowUpstream: toggling a row on air starts the agent.Session
// against THAT row's upstream, not a single shared m.shareUp. We toggle a row whose
// upstream differs from shareUp and assert the live session points at the row's URL.
func TestToggleShareUsesRowUpstream(t *testing.T) {
	// A fake broker that accepts the node registration so agent.Start succeeds and the
	// session is stored (otherwise a real register POST would fail and bail out).
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer broker.Close()

	mm := New(broker.URL, "tester")
	mm.shareUp = "http://127.0.0.1:8060/v1/chat/completions" // headline default
	mm.setShareRows([]shareRow{
		{model: "gpt-oss-20b", ctx: 32768, upstream: "http://127.0.0.1:8060/v1/chat/completions"},
		{model: "qwen3-vl-8b", ctx: 32768, upstream: "http://127.0.0.1:8081/v1/chat/completions"},
	})
	mm.shareCursor = 1 // the qwen3-vl-8b row, served by :8081 (NOT shareUp)
	mm.toggleShareAt(1)
	sess := mm.shares["qwen3-vl-8b"]
	if sess == nil {
		t.Fatal("toggling row 1 on air should start a session for qwen3-vl-8b")
	}
	defer sess.Stop()
	if up := sess.Upstream(); up != "http://127.0.0.1:8081/v1/chat/completions" {
		t.Errorf("on-air session upstream = %q, want the row's own :8081 upstream (not shareUp)", up)
	}
}

// TestPresetBarRenders: the preset bank is always visible and shows all five preset
// buttons with their keys + labels (NO_COLOR-safe).
func TestPresetBarRenders(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	v := stripANSI(mm.View())
	for _, want := range []string{"[1]", "TUNE IN", "[2]", "SHARE", "[3]", "CONFIG", "[L]", "LOGIN", "[?]", "HELP"} {
		if !strings.Contains(v, want) {
			t.Errorf("preset bar missing %q:\n%s", want, v)
		}
	}
	// The current mode (browse -> TUNE IN) is lit: under NO_COLOR a leading dot marks it.
	if !strings.Contains(v, "•[1] TUNE IN") {
		t.Errorf("the active preset (TUNE IN) should be lit (• marker under NO_COLOR):\n%s", v)
	}
}

// TestPresetKeysSwitchMode: pressing a preset number/key from BROWSE jumps to that
// mode/action - 2 opens SHARE, 3 opens CONFIG (limits), ? opens HELP.
func TestPresetKeysSwitchMode(t *testing.T) {
	// Deterministic detection so [2] SHARE opens the provider table, not a network scan.
	old := detectShares
	detectShares = func(extra ...string) ([]detect.Found, []string) {
		return []detect.Found{{Name: "t", Chat: "http://x/v1/chat/completions", Models: []string{"gpt-oss-20b"}}}, nil
	}
	defer func() { detectShares = old }()

	press := func(r rune) tea.Model {
		mm := New("http://broker.local", "tester")
		mm.width, mm.height = 100, 30
		var m tea.Model = mm
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		return m
	}
	if got := asModel(press('2')).mode; got != modeShare {
		t.Errorf("[2] should open SHARE (modeShare), got %v", got)
	}
	if got := asModel(press('3')).mode; got != modeLimits {
		t.Errorf("[3] should open CONFIG (modeLimits), got %v", got)
	}
	if got := asModel(press('?')).mode; got != modeHelp {
		t.Errorf("[?] should open HELP, got %v", got)
	}
	// From SHARE, [1] returns to TUNE IN (browse).
	share := press('2')
	one, _ := share.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if got := asModel(one).mode; got != modeBrowse {
		t.Errorf("[1] from SHARE should return to TUNE IN (browse), got %v", got)
	}
}

// TestPresetBarLitMode: the lit preset tracks the active mode (CONFIG lights up in
// the limits screen, SHARE lights up in the provider table).
func TestPresetBarLitMode(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.mode = modeLimits
	if !strings.Contains(stripANSI(mm.View()), "•[3] CONFIG") {
		t.Errorf("CONFIG preset should be lit in the limits screen:\n%s", stripANSI(mm.View()))
	}
	mm.mode = modeShare
	if !strings.Contains(stripANSI(mm.View()), "•[2] SHARE") {
		t.Errorf("SHARE preset should be lit in the provider table:\n%s", stripANSI(mm.View()))
	}
}

// TestPresetDoesNotStealTypedDigits: digits typed into the command palette, the chat
// input, or a numeric limit editor must NOT be hijacked by the preset bank.
func TestPresetDoesNotStealTypedDigits(t *testing.T) {
	// Command palette: "/" then "2" must reach the input, not switch to SHARE.
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	var m tea.Model = mm
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	cm := asModel(m)
	if cm.mode != modeCommand || cm.cmd.Value() != "2" {
		t.Errorf("a digit typed in the command palette must be input, not a preset jump (mode=%v val=%q)", cm.mode, cm.cmd.Value())
	}

	// Limits editor: entering edit then typing "3" must build the number, not jump.
	lm := New("http://broker.local", "tester")
	lm.width, lm.height = 100, 30
	lm.limModels = []string{"gpt-oss-20b"}
	lm.mode = modeLimits
	lm.editField = 0 // editing the max-out field
	var l tea.Model = lm
	l, _ = l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if asModel(l).mode != modeLimits {
		t.Errorf("a digit typed in the limits editor must not trigger a preset jump, got mode %v", asModel(l).mode)
	}
}

// withMotion runs fn with the animation un-frozen (quiet=false), restoring it
// after. Tests run non-TTY, where quiet is true and motion freezes to one frame;
// this lets the rotation/animation logic be exercised deterministically.
func withMotion(fn func()) {
	old := quiet
	quiet = false
	defer func() { quiet = old }()
	fn()
}

// TestEmptyBandCTAStatic (audit #10): the empty-band line is now ONE static CTA, not a
// rotating carousel - the CTA text is identical across frames (only the signal-bar
// shimmer beside it animates, carrying the "live, not frozen" cue).
func TestEmptyBandCTAStatic(t *testing.T) {
	withMotion(func() {
		if emptyBandCTA(false) != emptyBandCTA(false) {
			t.Errorf("empty-band CTA must be stable, not a rotation")
		}
	})
	if !strings.Contains(stripANSI(emptyBandCTA(false)), "No stations on air") {
		t.Errorf("empty-band CTA should name the empty band: %q", stripANSI(emptyBandCTA(false)))
	}
}

// TestWorkingSpinnerRotates: the working spinner renders the on-air beacon ((•)) and
// a ROTATING radio phrase from the one coherent DJ voice.
func TestWorkingSpinnerRotates(t *testing.T) {
	withMotion(func() {
		seen := map[string]bool{}
		for f := 0; f < cornerCadence*len(workingPhrases); f += cornerCadence {
			seen[workingPhrase(f)] = true
		}
		if len(seen) != len(workingPhrases) {
			t.Errorf("working phrase should rotate through all %d phrases, saw %d: %v", len(workingPhrases), len(seen), seen)
		}
		// The spinner line carries a carrier ring + a phrase.
		line := stripANSI(workingSpinner(0))
		if !strings.Contains(line, ")") || !strings.Contains(line, workingPhrases[0]) {
			t.Errorf("working spinner should show the beacon + a phrase, got %q", line)
		}
	})
}

// TestIdleMascotDesynchronized: the rebuilt idle mascot is not a 2-frame metronome -
// over a window of frames it plays a varied repertoire (several distinct poses),
// including a blink, and is fully deterministic for a given frame.
func TestIdleMascotDesynchronized(t *testing.T) {
	poses := map[string]bool{}
	sawBlink := false
	for f := 0; f < 400; f++ {
		pf, eye := idleScene(f)
		poses[strings.Join(pf.lines[:], "|")] = true
		if eye == "-" {
			sawBlink = true
		}
		// Determinism: same frame -> same pose.
		pf2, eye2 := idleScene(f)
		if pf2 != pf || eye2 != eye {
			t.Fatalf("idleScene(%d) is not deterministic", f)
		}
	}
	if len(poses) < 4 {
		t.Errorf("idle mascot should show a varied repertoire (>=4 distinct poses), got %d", len(poses))
	}
	if !sawBlink {
		t.Errorf("idle mascot should blink occasionally")
	}
}

// TestNoColorLivelinessRenders: the idle-with-hint screen, the working spinner, and
// the preset bar all render with NO ANSI under NO_COLOR and stay within a narrow
// width (the non-TTY / piped, narrow-safe contract).
func TestNoColorLivelinessRenders(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, w := range []int{0, 40, 64, 80} {
		// Idle-with-hint empty-band screen (scanned, no bands).
		mm := New("http://broker.local", "tester")
		mm.width, mm.height = w, 24
		var m tea.Model = mm
		m, _ = m.Update(offersMsg{}) // scanned=true, no offers -> idle band
		m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})
		out := m.View()
		if strings.Contains(out, "\x1b[") {
			t.Errorf("width %d: idle screen emitted ANSI under NO_COLOR", w)
		}
		eff := w
		if eff == 0 {
			eff = 88
		}
		for _, line := range strings.Split(out, "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > eff {
				t.Errorf("width %d: idle line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
	}
	// The working spinner + preset bar contain no ANSI under NO_COLOR.
	if strings.Contains(workingSpinner(3), "\x1b[") {
		t.Errorf("working spinner emitted ANSI under NO_COLOR: %q", workingSpinner(3))
	}
	pm := New("http://broker.local", "tester")
	pm.width = 40
	if strings.Contains(pm.presetBar(40), "\x1b[") {
		t.Errorf("preset bar emitted ANSI under NO_COLOR: %q", pm.presetBar(40))
	}
}
