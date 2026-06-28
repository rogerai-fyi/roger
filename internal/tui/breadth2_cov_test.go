package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/detect"
)

// withColor temporarily forces the animated (non-quiet) render path so the ping
// animation + corner-Ping frame selectors run their real switch arms (in a test
// process stdout is a pipe, so quiet is otherwise always true). Restores on cleanup.
func withColor(t *testing.T) {
	t.Helper()
	orig := quiet
	quiet = false
	t.Cleanup(func() { quiet = orig })
}

// TestPingAnimationArms drives pingPose + the corner-Ping frame/word selectors across
// every state with animation ON, so the per-state arms (TX swell, static, idle scene,
// thinking/streaming/tool/blink corner heads) all run - not just the frozen quiet pose.
func TestPingAnimationArms(t *testing.T) {
	withColor(t)

	// pingPose across its three carrier states + a caption line, several frames each.
	for _, st := range []pingState{pingTx, pingStatic, pingIdle} {
		for f := 0; f < 8; f++ {
			if strings.TrimSpace(stripANSI(pingPose(st, f, 60, "standing by"))) == "" {
				t.Fatalf("pingPose(state=%v frame=%d) rendered blank", st, f)
			}
		}
	}

	// cornerFrameFor: every pose, many frames, must yield a non-empty head/eye (the
	// blink splice fires on a hash hit somewhere in this range).
	sawBlink := false
	for _, p := range []agentPose{poseWaiting, poseThinking, poseStreaming, poseTool} {
		for f := 0; f < 60; f++ {
			head, eye := cornerFrameFor(p, f)
			if head == (cornerHead{}) || eye == "" {
				t.Fatalf("cornerFrameFor(%v,%d) returned an empty frame", p, f)
			}
			if eye == "-" {
				sawBlink = true
			}
		}
	}
	if !sawBlink {
		t.Error("the waiting corner-Ping should blink (eye '-') at least once across 60 frames")
	}

	// agentCornerPing full (multi-row) block + cornerWord vary by state.
	full := agentCornerPing(poseStreaming, 1, false, false)
	if len(full) != 3 {
		t.Errorf("the full corner-Ping should be a 3-line head, got %d lines", len(full))
	}
	if cornerWord(poseStreaming, 0) == cornerWord(poseWaiting, 0) {
		t.Error("cornerWord should differ between streaming and waiting")
	}
}

// TestRunAgentCommands covers the in-AGENT slash command set: /clear resets, /help
// lists, /persona shows dj.md, /model <name> jumps to a candidate, /model <bad> hints,
// and an unknown command hints - none send a chat turn.
func TestRunAgentCommands(t *testing.T) {
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)

	// /help lists the command help (two lines).
	hm, _ := am.runAgentCommand("/help")
	if !strings.Contains(stripANSI(asModel(hm).View()), "/model") {
		t.Errorf("/help should list the AGENT commands")
	}

	// /persona names the dj.md path.
	pm, _ := am.runAgentCommand("/persona")
	if !strings.Contains(stripANSI(asModel(pm).View()), "persona:") {
		t.Errorf("/persona should show where dj.md lives")
	}

	// /model <name> jumps straight to a known candidate.
	jm, _ := am.runAgentCommand("/model llama-3.3-70b-instruct")
	jam := asModel(jm)
	if jam.agent.model != "llama-3.3-70b-instruct" {
		t.Errorf("/model <name> should re-point the agent, got %q", jam.agent.model)
	}

	// /model <bad> hints (no switch).
	bm, _ := am.runAgentCommand("/model not-a-real-model")
	if !strings.Contains(stripANSI(asModel(bm).View()), "no candidate model matches") {
		t.Errorf("/model <bad> should hint that nothing matched")
	}

	// /clear resets the session transcript + cost.
	dirty := am
	dirty.agentCost = 2
	cm, _ := dirty.runAgentCommand("/clear")
	if asModel(cm).agentCost != 0 || len(asModel(cm).agentLines) == 0 {
		t.Errorf("/clear should reset cost + leave a 'cleared' note")
	}

	// unknown command hints.
	um, _ := am.runAgentCommand("/frobnicate")
	if !strings.Contains(stripANSI(asModel(um).View()), "unknown:") {
		t.Errorf("an unknown /command should hint")
	}
}

// TestAgentModelPickerKeys opens the /model picker (multiple candidates) and drives its
// modal keys: down/up move the cursor, enter selects, and on a fresh open esc cancels
// keeping the current model.
func TestAgentModelPickerKeys(t *testing.T) {
	base := browseSeed(120) // seeds two online bands -> >1 candidate
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)

	// Open the picker via the bare /model command.
	om, _ := am.runAgentCommand("/model")
	pm := asModel(om)
	if !pm.agentPicker || len(pm.agentPickerRows) < 2 {
		t.Fatalf("bare /model should open a multi-candidate picker, picker=%v rows=%d", pm.agentPicker, len(pm.agentPickerRows))
	}

	// down then up navigate the modal (via onAgentKey while the picker owns keys).
	var pmm tea.Model = pm
	pmm, _ = pmm.Update(tea.KeyMsg{Type: tea.KeyDown})
	if asModel(pmm).agentPickerCursor != 1 {
		t.Errorf("down should move the picker cursor to 1, got %d", asModel(pmm).agentPickerCursor)
	}
	pmm, _ = pmm.Update(tea.KeyMsg{Type: tea.KeyUp})
	if asModel(pmm).agentPickerCursor != 0 {
		t.Errorf("up should move the picker cursor back to 0, got %d", asModel(pmm).agentPickerCursor)
	}

	// enter selects the highlighted candidate + closes the picker.
	selModel := asModel(pmm).agentPickerRows[1]
	var navm tea.Model = pm
	navm, _ = navm.Update(tea.KeyMsg{Type: tea.KeyDown})
	navm, _ = navm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := asModel(navm)
	if got.agentPicker {
		t.Error("enter should close the picker")
	}
	if got.agent.model != selModel {
		t.Errorf("enter should pick the highlighted model %q, got %q", selModel, got.agent.model)
	}

	// esc on a fresh picker cancels (keeps the current model).
	var escm tea.Model = pm
	escm, _ = escm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if asModel(escm).agentPicker {
		t.Error("esc should close the picker")
	}
}

// TestAgentKeyScrollAndRecall covers the non-text AGENT keys: pgup/pgdown/ctrl+u/ctrl+d
// scroll the transcript, up/down recall sent prompts, esc (idle) leaves to BROWSE, and
// esc (busy) cancels the in-flight turn instead of leaving.
func TestAgentKeyScrollAndRecall(t *testing.T) {
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)
	am.agentHist.add("first prompt")
	am.agentHist.add("second prompt")

	var m tea.Model = am
	for _, k := range []string{"pgup", "pgdown", "ctrl+u", "ctrl+d"} {
		m, _ = m.Update(keyMsg2ByName(k))
		if strings.TrimSpace(m.View()) == "" {
			t.Fatalf("AGENT view blank after %q", k)
		}
	}

	// Up recalls the most recent sent prompt onto the input.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if v := asModel(m).agentIn.Value(); v != "second prompt" {
		t.Errorf("up should recall the latest sent prompt, got %q", v)
	}
	// Down walks newer / restores the draft.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})

	// esc while idle leaves AGENT for BROWSE.
	im, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if asModel(im).mode != modeBrowse {
		t.Errorf("idle esc should leave AGENT to BROWSE, got %v", asModel(im).mode)
	}

	// esc while busy cancels the turn (stays in AGENT).
	bm := asModel(nm)
	bm.agentBusy = true
	cancelled := false
	bm.agent.cancel = func() { cancelled = true }
	cm, _ := bm.onAgentKey(tea.KeyMsg{Type: tea.KeyEsc})
	if asModel(cm).mode != modeAgent {
		t.Error("busy esc should stay in AGENT (cancel, not leave)")
	}
	if !cancelled {
		t.Error("busy esc should call the turn's cancel func")
	}
}

// TestRefreshAgentModelSwitch: re-resolving the agent model after a new channel opens
// re-points the runtime and notes the switch.
func TestRefreshAgentModelSwitch(t *testing.T) {
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)
	before := len(am.agentLines)
	am.connected = &offer{NodeID: "n2", Model: "qwen3-coder-30b", Online: true}
	am.refreshAgentModel()
	if am.agent.model != "qwen3-coder-30b" {
		t.Errorf("refreshAgentModel should follow the new channel, got %q", am.agent.model)
	}
	if len(am.agentLines) <= before {
		t.Error("a model switch should note the change in the transcript")
	}
	// Idempotent when the model is unchanged.
	again := len(am.agentLines)
	am.refreshAgentModel()
	if len(am.agentLines) != again {
		t.Error("refreshAgentModel should be a no-op when the model is unchanged")
	}
}

// keyMsg2ByName builds a special (non-rune) key message by its bubbletea name.
func keyMsg2ByName(name string) tea.KeyMsg {
	switch name {
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	}
	return tea.KeyMsg{}
}

// TestSharesDetectedBranches covers onSharesDetected: a found set builds the table (and
// the /share <model> shortcut flips it on air), an empty initial scan drops into the
// guided wizard, an empty re-detect from the table stays put with a note, and a
// key-protected server routes onto the paste row asking for a key.
func TestSharesDetectedBranches(t *testing.T) {
	found := []detect.Found{{Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Chat: "http://127.0.0.1:11434/v1/chat/completions", Models: []string{"gpt-oss-20b"}}}

	// Found rows -> provider table.
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 30
	fm, _ := m.onSharesDetected(found, nil)
	if asModel(fm).mode != modeShare || len(asModel(fm).shareRows) == 0 {
		t.Fatalf("a found set should build the provider table, mode=%v rows=%d", asModel(fm).mode, len(asModel(fm).shareRows))
	}

	// Empty initial scan (setupOnEmpty) -> guided wizard.
	w := New("http://broker.local", "tester")
	w.width, w.height = 100, 30
	w.setupOnEmpty = true
	wm, _ := w.onSharesDetected(nil, nil)
	if asModel(wm).mode != modeShareSetup {
		t.Errorf("an empty initial scan should open the guided wizard, mode=%v", asModel(wm).mode)
	}

	// Empty re-detect from inside the table (setupOnEmpty=false) stays on the table.
	r := New("http://broker.local", "tester")
	r.width, r.height = 100, 30
	r.mode = modeShare
	r.setupOnEmpty = false
	rm, _ := r.onSharesDetected(nil, nil)
	if asModel(rm).mode != modeShare {
		t.Errorf("an empty re-detect should stay on the table, mode=%v", asModel(rm).mode)
	}
	if !strings.Contains(stripANSI(asModel(rm).status), "still nothing") {
		t.Errorf("an empty re-detect should note nothing was found, got %q", stripANSI(asModel(rm).status))
	}

	// A key-protected server routes onto the paste row, awaiting a key.
	k := New("http://broker.local", "tester")
	k.width, k.height = 100, 30
	k.setupOnEmpty = true
	km, _ := k.onSharesDetected(nil, []string{"http://127.0.0.1:8081/v1"})
	kam := asModel(km)
	if kam.mode != modeShareSetup || !kam.setupAwaitKey {
		t.Errorf("a key-protected server should ask for a key on the paste row, mode=%v awaitKey=%v", kam.mode, kam.setupAwaitKey)
	}
	if kam.setupPaste != "http://127.0.0.1:8081/v1" {
		t.Errorf("the protected URL should be pre-filled, got %q", kam.setupPaste)
	}
}

// TestShareSetupKeys drives the guided-setup wizard keys: down/up move, a named tool
// pick shows its one-liner + goes to the loading table, pasting builds the URL char by
// char, backspace trims it, and esc leaves.
func TestShareSetupKeys(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 30
	m.mode = modeShareSetup

	var tm tea.Model = m
	// down moves the cursor.
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyDown})
	if asModel(tm).setupCursor != 1 {
		t.Errorf("down should advance the setup cursor, got %d", asModel(tm).setupCursor)
	}
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyUp})
	if asModel(tm).setupCursor != 0 {
		t.Errorf("up should move the setup cursor back, got %d", asModel(tm).setupCursor)
	}

	// Picking a named tool (enter on a non-paste row) checks for it (loading table).
	cur := asModel(tm)
	pickm, cmd := cur.onShareSetupKey(tea.KeyMsg{Type: tea.KeyEnter})
	if asModel(pickm).mode != modeShare || !asModel(pickm).shareLoading {
		t.Errorf("a named-tool pick should enter the loading table, mode=%v loading=%v", asModel(pickm).mode, asModel(pickm).shareLoading)
	}
	if cmd == nil {
		t.Error("a named-tool pick should fire a detection command")
	}

	// Paste row: move to the last option, then type a URL char by char.
	paste := New("http://broker.local", "tester")
	paste.width, paste.height = 100, 30
	paste.mode = modeShareSetup
	paste.setupCursor = len(setupOptions) - 1
	var pm tea.Model = paste
	for _, r := range "127.0.0.1:9" {
		pm, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if asModel(pm).setupPaste != "127.0.0.1:9" {
		t.Errorf("paste keys should build the URL, got %q", asModel(pm).setupPaste)
	}
	pm, _ = pm.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if asModel(pm).setupPaste != "127.0.0.1:" {
		t.Errorf("backspace should trim the pasted URL, got %q", asModel(pm).setupPaste)
	}
	// An empty paste + enter asks for the endpoint.
	empty := paste
	empty.setupPaste = ""
	em, _ := empty.onShareSetupKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(stripANSI(asModel(em).setupErr), "paste your endpoint") {
		t.Errorf("an empty paste enter should prompt for the endpoint, got %q", asModel(em).setupErr)
	}

	// esc leaves the wizard to browse.
	escm, _ := paste.onShareSetupKey(tea.KeyMsg{Type: tea.KeyEsc})
	if asModel(escm).mode != modeBrowse {
		t.Errorf("esc should leave the wizard to browse, got %v", asModel(escm).mode)
	}
}

// TestMonthlyBudgetLine covers the budget readout across its states: anonymous, no cap,
// under cap, near-cap warning, and limit-reached.
func TestMonthlyBudgetLine(t *testing.T) {
	anon := stripANSI(monthlyBudgetLine(model{}))
	if !strings.Contains(anon, "log in to set a monthly spend limit") {
		t.Errorf("anonymous budget line should prompt login, got %q", anon)
	}

	li := func(cap, spend float64) model {
		return model{loggedIn: true, haveBal: true, monthlyCap: cap, monthlySpend: spend}
	}
	if got := stripANSI(monthlyBudgetLine(li(0, 3))); !strings.Contains(got, "no cap") {
		t.Errorf("no-cap budget line should say no cap, got %q", got)
	}
	if got := stripANSI(monthlyBudgetLine(li(100, 10))); !strings.Contains(got, "this month") || strings.Contains(got, "⚠") {
		t.Errorf("under-cap budget line should be calm, got %q", got)
	}
	if got := stripANSI(monthlyBudgetLine(li(100, 85))); !strings.Contains(got, "% used") {
		t.Errorf("near-cap budget line should warn with a percentage, got %q", got)
	}
	if got := stripANSI(monthlyBudgetLine(li(100, 120))); !strings.Contains(got, "limit reached") {
		t.Errorf("over-cap budget line should say limit reached, got %q", got)
	}
}

// TestCoarseRegionBuckets covers the region bucketing across macro-regions, substring
// matches, bare two-letter codes, and the unmatched/empty fallbacks.
func TestCoarseRegionBuckets(t *testing.T) {
	cases := map[string]string{
		"us-west": "US-W", "iad": "US-E", "dfw": "US-C", "america": "US",
		"london": "UK", "frankfurt": "DE", "amsterdam": "NL", "paris": "FR",
		"europe": "EU", "toronto": "CA", "sydney": "AU", "tokyo": "JP",
		"singapore": "SG", "mumbai": "IN", "sao paulo": "BR", "seoul": "KR",
		"us": "US", "eu": "EU", "de": "DE", "nl": "NL", "fr": "FR", "ca": "CA",
		"au": "AU", "jp": "JP", "sg": "SG", "in": "IN", "br": "BR", "kr": "KR",
		"": "", "qzqzq-nowhere": "",
	}
	for in, want := range cases {
		if got := coarseRegion(in); got != want {
			t.Errorf("coarseRegion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoginFlow drives the [L] device-login flow end-to-end through the host hooks:
// begin -> the started message arms polling -> poll returns the login -> the wallet
// flips logged-in; then a logout confirm clears it.
func TestLoginFlow(t *testing.T) {
	var polled bool
	hooks := Hooks{
		GitHubID: "cli-id",
		LoginBegin: func(broker, clientID string) (LoginDevice, error) {
			return LoginDevice{VerificationURI: "https://github.com/login/device", UserCode: "ABCD-1234"}, nil
		},
		LoginPoll: func(broker, clientID string, d LoginDevice) (string, error) {
			polled = true
			return "octocat", nil
		},
		Logout: func() error { return nil },
	}
	m := NewWithHooks("http://broker.local", "tester", nil, hooks)
	m.width, m.height = 100, 30

	// startLogin issues the begin command.
	_, beginCmd := m.startLogin()
	if beginCmd == nil {
		t.Fatal("startLogin should issue the device-flow begin command")
	}
	started, ok := beginCmd().(loginStartedMsg)
	if !ok || started.UserCode != "ABCD-1234" {
		t.Fatalf("begin should yield the device code, got %#v", beginCmd())
	}

	// Feeding the started message arms polling.
	var tm tea.Model = m
	tm, pollCmd := tm.Update(loginStartedMsg(started))
	if !asModel(tm).loginWaiting {
		t.Error("the started message should mark the login as waiting")
	}
	if pollCmd == nil {
		t.Fatal("the started message should arm the login poll")
	}
	lmsg := pollCmd()
	if !polled {
		t.Error("the poll hook should have been invoked")
	}
	if string(lmsg.(loginMsg)) != "octocat" {
		t.Fatalf("poll should yield the github login, got %#v", lmsg)
	}

	// The login message flips the wallet to logged-in.
	tm, _ = tm.Update(lmsg)
	if got := asModel(tm); got.ghLogin != "octocat" || !got.loggedIn {
		t.Errorf("loginMsg should flip the model logged-in, got login=%q loggedIn=%v", got.ghLogin, got.loggedIn)
	}

	// startLogout issues the logout command -> logoutMsg clears the session.
	lo := asModel(tm)
	_, loCmd := lo.startLogout()
	if loCmd == nil {
		t.Fatal("startLogout should issue the logout command")
	}
	if _, ok := loCmd().(logoutMsg); !ok {
		t.Fatalf("logout should yield a logoutMsg, got %#v", loCmd())
	}
	out, _ := tm.Update(logoutMsg{})
	if asModel(out).loggedIn || asModel(out).ghLogin != "" {
		t.Error("logoutMsg should clear the login state")
	}
}

// TestStartLoginUnavailable: with no login hooks the panel reports the CLI fallback.
func TestStartLoginUnavailable(t *testing.T) {
	m := New("http://broker.local", "tester")
	nm, _ := m.startLogin()
	if !strings.Contains(stripANSI(asModel(nm).status), "login unavailable") {
		t.Errorf("no-hook startLogin should report the CLI fallback, got %q", stripANSI(asModel(nm).status))
	}
	lo, _ := m.startLogout()
	if !strings.Contains(stripANSI(asModel(lo).status), "logout unavailable") {
		t.Errorf("no-hook startLogout should report the CLI fallback, got %q", stripANSI(asModel(lo).status))
	}
}
