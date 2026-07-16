package tui

// operator_steps_tui_test.go - godog step definitions for the TUI half of the Guest
// Operators Phase 2 spec set (features/operator/operator_command|handoff_lifecycle|
// rc_interlock.feature): they drive the REAL bubbletea model (the rc_confirm_bdd pattern),
// a REAL ProxyOptionsHolder behind a real httptest proxy + stub billing broker (spend and
// call counts accumulate through the actual money path, never set by hand), and a REAL
// client.RCBridge polling a stub RC broker for the interlock. The only seams are the
// package-level exec/terminal/scan seams in operator.go - no mocks.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/operator"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// opBDD is the shared per-scenario state for the whole operator spec set (the pure
// detection/config steps live in operator_steps_pure_test.go).
type opBDD struct {
	t *testing.T

	// pure detection seams
	pathOf      map[string]string
	lookErr     map[string]error
	probeOuts   map[string]string
	probeErrs   map[string]error
	probeBlocks map[string]bool
	ds          []operator.Detection
	parsed      string
	parsedOK    bool

	// sandbox roots (per scenario)
	scratchRoot string
	workdir     string
	home        string

	// config materialization
	sess       operator.Session
	launch     operator.Launch
	prevLaunch operator.Launch
	cleanup    func() error
	matErr     error
	matGuest   string
	projectCfg []byte
	realFiles  map[string][]byte
	homeSnap   map[string]string
	workSnap   map[string]string
	parentEnv  []string

	// TUI
	tm            tea.Model
	holder        *client.ProxyOptionsHolder
	keyAtSeed     string
	budgetAtSeed  float64           // holder budget at seed (the "unchanged" baseline)
	launchWorkdir string            // what the operatorWorkdir seam resolves for this scenario
	tuiPaths      map[string]string // guest bin -> fake path for the TUI-side detect seam
	deskMkt       []offer           // accumulated market offers for the pickAutoBand pure steps

	// money servers (real HTTP through the real proxy handler)
	brokerSrv *httptest.Server
	proxySrv  *httptest.Server
	proxyHits atomic.Int64
	costMu    sync.Mutex
	costs     []string // per-call X-RogerAI-Cost values the stub broker bills

	// exec seam recordings
	execCmds     []*exec.Cmd
	parkedAtExec []bool
	spawnFail    bool
	resetBuf     *bytes.Buffer
	collected    []tea.Msg
	retCalls     int64
	retSpend     float64
	firstDir     string

	// a standalone remote-control VIEWER model (the desktop half that renders relayed frames)
	viewer model

	// RC interlock (a REAL client.RCBridge against a stub RC broker)
	rcSrv    *httptest.Server
	rcQueue  chan protocol.RCInbound
	rc401    atomic.Bool
	bridge   *client.RCBridge
	framesMu sync.Mutex
	frames   []protocol.RCFrame

	// operator frame enrichment (rc_enrichment.feature)
	builtFrameJSON []byte            // wire JSON of the constructor-built frame (E4)
	cmpStart       *protocol.RCFrame // the handoff-start frame under comparison (E6)
	cmpParked      *protocol.RCFrame // the parked-turn auto-frame under comparison (E6)
}

// seamExec is the recording exec seam: it captures the composed child command (and
// whether the bridge was already parked at issue time) instead of suspending the test
// terminal; scenarios deliver operatorDoneMsg to simulate the child outcome.
func (s *opBDD) seamExec(c *exec.Cmd, _ func(error) tea.Msg) tea.Cmd {
	parked := false
	if s.bridge != nil {
		_, parked = s.bridge.Parked()
	}
	s.execCmds = append(s.execCmds, c)
	s.parkedAtExec = append(s.parkedAtExec, parked)
	return nil
}

// opTermTap routes the operator terminal-reset seam into the live scenario's buffer.
type opTermTap struct{ s *opBDD }

func (w opTermTap) Write(p []byte) (int, error) { return w.s.resetBuf.Write(p) }

func (s *opBDD) reset(t *testing.T) {
	s.closeServers()
	*s = opBDD{
		t:           t,
		pathOf:      map[string]string{},
		lookErr:     map[string]error{},
		probeOuts:   map[string]string{},
		probeErrs:   map[string]error{},
		probeBlocks: map[string]bool{},
		tuiPaths:    map[string]string{},
		resetBuf:    &bytes.Buffer{},
	}
	s.scratchRoot = t.TempDir()
	s.workdir = t.TempDir()
	s.home = t.TempDir()
	operatorScratchRoot = s.scratchRoot
	// Phase 3: the plate's workdir seam resolves the scenario's sandbox project dir by
	// default, and the process HOME is THIS scenario's sandbox home so the exactly-$HOME
	// double confirm compares against the live env (never the developer's real home).
	s.launchWorkdir = s.workdir
	os.Setenv("HOME", s.home)
}

func (s *opBDD) closeServers() {
	if s.bridge != nil {
		s.bridge.Stop()
	}
	for _, srv := range []*httptest.Server{s.brokerSrv, s.proxySrv, s.rcSrv} {
		if srv != nil {
			srv.Close()
		}
	}
}

// --- model plumbing ---------------------------------------------------------------------

func (s *opBDD) model() model { return asModel(s.tm) }

func (s *opBDD) mutate(f func(m *model)) {
	m := asModel(s.tm)
	f(&m)
	s.tm = m
}

// update feeds one message through the REAL Update loop and returns the emitted Cmd.
func (s *opBDD) update(msg tea.Msg) tea.Cmd {
	nm, cmd := s.tm.Update(msg)
	s.tm = nm
	return cmd
}

// pressKey sends a key and folds any synchronously-produced msg back in (the r re-scan
// cmd, the staging tick) - the same round-trip the bubbletea runtime performs.
func (s *opBDD) pressKey(k string) {
	cmd := s.update(keyMsg(k))
	for _, msg := range collectCmdMsgs(cmd) {
		if msg != nil {
			s.update(msg)
		}
	}
}

// collectCmdMsgs executes a Cmd tree (Batch-aware) and returns the produced messages.
func collectCmdMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectCmdMsgs(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func (s *opBDD) view() string { return stripANSI(s.model().View()) }

// --- money servers: a stub billing broker + the REAL hardened proxy over it -------------

func (s *opBDD) startMoneyServers(model string) {
	s.brokerSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.costMu.Lock()
		cost := "0"
		if len(s.costs) > 0 {
			cost, s.costs = s.costs[0], s.costs[1:]
		}
		s.costMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if cost != "0" {
			w.Header().Set("X-RogerAI-Cost", cost)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"roger"}}]}`))
	}))
	s.holder = client.NewProxyOptionsHolder(client.ProxyOptions{
		Broker: s.brokerSrv.URL, User: "tester", Model: model, SessionKey: client.NewSessionKey(),
	})
	inner := client.ProxyHandlerLive(s.holder)
	s.proxySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.proxyHits.Add(1)
		inner.ServeHTTP(w, r)
	}))
}

// proxyCallsCosts drives N real guest-shaped requests through the REAL proxy (bearer
// auth, model rewrite, budget gate); each is billed the given cost by the stub broker.
func (s *opBDD) proxyCallsCosts(costs []string) error {
	key := s.holder.Get().SessionKey
	for _, c := range costs {
		s.costMu.Lock()
		s.costs = append(s.costs, c)
		s.costMu.Unlock()
		req, _ := http.NewRequest(http.MethodPost, s.proxySrv.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"whatever-the-guest-defaults-to"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("proxy call: %v", err)
		}
		resp.Body.Close()
	}
	return nil
}

// --- seeding ------------------------------------------------------------------------------

// seedTUI enters [0] AGENT off a real seeded BROWSE and (when a band model is given)
// wires a live holder + real proxy endpoint like openChannel does.
func (s *opBDD) seedTUI(bandModel string) {
	m := browseSeed(120)
	m.mouseOff = false // the handoff specs default to "the user has the mouse on"
	// Wire the live proxy holder BEFORE entering AGENT - the real order is "tune in, then
	// [0]", so enterAgent sees a live channel and lands on the ask prompt (not the fresh
	// DESK auto-tune landing, which is only for a genuinely fresh session with no holder).
	if bandModel != "" {
		s.startMoneyServers(bandModel)
		m.proxyHolder = s.holder
		m.endpoint = s.proxySrv.URL + "/v1"
		m.broker = s.brokerSrv.URL
		s.keyAtSeed = s.holder.Get().SessionKey
		s.budgetAtSeed = s.holder.Get().Budget
	}
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("0"))
	s.tm = asModel(tm)
}

func (s *opBDD) agentSessionAtPrompt() error {
	s.seedTUI("qwen3-32b-fp8")
	return nil
}

func (s *opBDD) agentSessionTuned(bandModel string) error {
	s.seedTUI(bandModel)
	return nil
}

func (s *opBDD) addDetected(name string) error {
	g, err := registryGuest(name)
	if err != nil {
		return err
	}
	s.tuiPaths[g.Bin] = "/fake/" + g.Bin
	s.mutate(func(m *model) {
		m.operatorDetections = append(m.operatorDetections,
			operator.Detection{Guest: g, Path: "/fake/" + g.Bin, Version: g.KnownGood})
	})
	return nil
}

func (s *opBDD) detectedGuests(a string) error { return s.addDetected(a) }

func (s *opBDD) detectedGuestsTwo(a, b string) error {
	if err := s.addDetected(a); err != nil {
		return err
	}
	return s.addDetected(b)
}

func (s *opBDD) noGuestsDetected() error {
	s.tuiPaths = map[string]string{}
	s.mutate(func(m *model) {
		m.operatorDetections = nil
	})
	return nil
}

func (s *opBDD) undetectedGuestsTwo(a, b string) error {
	if err := s.undetectedGuest(a); err != nil {
		return err
	}
	return s.undetectedGuest(b)
}

// undetectedGuest verifies the named registry guest is absent from the scenario's desk
// (it was simply never detected - the Given documents the state, this pins it).
func (s *opBDD) undetectedGuest(name string) error {
	for _, d := range s.model().operatorDetections {
		if d.Guest.Name == name {
			return fmt.Errorf("%s unexpectedly detected", name)
		}
	}
	return nil
}

func (s *opBDD) detectedGuestRequiresSetup(name string) error {
	s.mutate(func(m *model) {
		m.operatorDetections = append(m.operatorDetections, operator.Detection{
			Guest: operator.Guest{
				Name: name, Bin: name, Provider: "anthropic", InstallHint: "n/a", KnownGood: "0.0.1",
				NeedsSetup: true,
				SetupNote:  name + " needs a key first - export its API key, then /operator " + name,
			},
			Path: "/fake/" + name, Version: "0.0.1",
		})
	})
	return nil
}

// tuiDetectEnv backs the operatorDetectEnv seam for the r re-scan: it resolves exactly
// the fake paths the scenario declared and probes the registry's known-good versions.
func (s *opBDD) tuiDetectEnv() operator.Env {
	return operator.Env{
		LookPath: func(bin string) (string, error) {
			if p, ok := s.tuiPaths[bin]; ok {
				return p, nil
			}
			return "", errors.New("executable file not found in $PATH")
		},
		Probe: func(bin string) (string, error) {
			for _, g := range operator.Registry() {
				if s.tuiPaths[g.Bin] == bin {
					return g.KnownGood, nil
				}
			}
			return "", errors.New("unknown bin")
		},
	}
}

// --- command + picker steps -----------------------------------------------------------------

func (s *opBDD) userRuns(line string) error {
	nm, _ := s.model().runAgentCommand(line)
	s.tm = nm
	return nil
}

func (s *opBDD) userHasTyped(text string) error {
	s.tm = typeRunes(s.tm, text)
	return nil
}

func (s *opBDD) registryContains(cmd string) error {
	if !containsStr(agentCommands, cmd) {
		return fmt.Errorf("agentCommands %v lacks %s", agentCommands, cmd)
	}
	return nil
}

func (s *opBDD) registryNotContains(cmd string) error {
	if containsStr(agentCommands, cmd) {
		return fmt.Errorf("agentCommands must not list the alias %s", cmd)
	}
	return nil
}

func (s *opBDD) registrySortedLowercaseUnique() error {
	seen := map[string]bool{}
	for i, c := range agentCommands {
		if c != strings.ToLower(c) {
			return fmt.Errorf("%s is not lowercase", c)
		}
		if seen[c] {
			return fmt.Errorf("%s is duplicated", c)
		}
		seen[c] = true
		if i > 0 && agentCommands[i-1] >= c {
			return fmt.Errorf("registry not sorted at %s", c)
		}
	}
	return nil
}

func (s *opBDD) transcriptDoesNotShow(text string) error {
	if strings.Contains(s.view(), text) {
		return fmt.Errorf("transcript shows %q:\n%s", text, s.view())
	}
	return nil
}

func (s *opBDD) transcriptShows(text string) error {
	if !strings.Contains(s.view(), text) {
		return fmt.Errorf("transcript lacks %q:\n%s", text, s.view())
	}
	return nil
}

func (s *opBDD) transcriptNotes(text string) error { return s.transcriptShows(text) }

func (s *opBDD) stripIncludes(cmd string) error {
	cands := agentSlashCandidates(s.model().agentIn.Value())
	if !containsStr(cands, cmd) {
		return fmt.Errorf("candidates %v lack %s", cands, cmd)
	}
	return nil
}

func (s *opBDD) stripNeverIncludesAliases(a, b string) error {
	cands := agentSlashCandidates(s.model().agentIn.Value())
	if containsStr(cands, a) || containsStr(cands, b) {
		return fmt.Errorf("candidates %v suggest an alias", cands)
	}
	return nil
}

func (s *opBDD) noPickerOpens() error {
	if s.model().operatorPicker {
		return fmt.Errorf("the operator picker opened")
	}
	return nil
}

func (s *opBDD) pickerOpen() error {
	if !s.model().operatorPicker {
		return fmt.Errorf("the operator picker is not open")
	}
	return nil
}

func (s *opBDD) pickerClosed() error {
	if s.model().operatorPicker {
		return fmt.Errorf("the operator picker is still open")
	}
	return nil
}

func (s *opBDD) pickerRowLabels() []string {
	var out []string
	for _, r := range s.model().operatorRows {
		out = append(out, r.label)
	}
	return out
}

func (s *opBDD) pickerRowsAre(a, b string) error {
	want := []string{"DJ", a, b}
	got := s.pickerRowLabels()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("picker rows = %v, want %v", got, want)
	}
	return nil
}

func (s *opBDD) pickerRowsWithSuggestion(detected string) error {
	rows := s.model().operatorRows
	if len(rows) != 3 || rows[0].label != "DJ" || rows[1].label != detected || !rows[2].suggestion {
		return fmt.Errorf("picker rows = %v (want DJ, %s, one suggestion)", s.pickerRowLabels(), detected)
	}
	for _, r := range rows[:2] {
		if r.suggestion {
			return fmt.Errorf("only the LAST row may be the suggestion")
		}
	}
	return nil
}

func (s *opBDD) suggestionShowsInstallHint() error {
	rows := s.model().operatorRows
	last := rows[len(rows)-1]
	if !last.suggestion || last.hint == "" {
		return fmt.Errorf("no suggestion hint on the last row: %+v", last)
	}
	if !strings.Contains(s.view(), last.hint) {
		return fmt.Errorf("the rendered picker lacks the install hint %q", last.hint)
	}
	return nil
}

func (s *opBDD) pressDownPastLast() error {
	for i := 0; i < len(s.model().operatorRows)+3; i++ {
		s.pressKey("down")
	}
	return nil
}

func (s *opBDD) cursorStaysOn(label string) error {
	m := s.model()
	if m.operatorCursor >= len(m.operatorRows) || m.operatorRows[m.operatorCursor].label != label {
		return fmt.Errorf("cursor on %v, want %s", s.pickerRowLabels(), label)
	}
	return nil
}

func (s *opBDD) cursorNeverOnSuggestion() error {
	m := s.model()
	if m.operatorRows[m.operatorCursor].suggestion {
		return fmt.Errorf("the cursor landed on the suggestion row")
	}
	s.pressKey("down") // even one more press stays off it
	m = s.model()
	if m.operatorRows[m.operatorCursor].suggestion {
		return fmt.Errorf("the cursor reached the suggestion row after another down")
	}
	return nil
}

func (s *opBDD) userPicks(label string) error {
	m := s.model()
	target := -1
	for i, r := range m.operatorRows {
		if r.label == label {
			target = i
		}
	}
	if target < 0 {
		return fmt.Errorf("no picker row %q in %v", label, s.pickerRowLabels())
	}
	for i := 0; i < len(m.operatorRows)+2 && s.model().operatorCursor != target; i++ {
		if s.model().operatorCursor < target {
			s.pressKey("down")
		} else {
			s.pressKey("up")
		}
	}
	if s.model().operatorCursor != target {
		return fmt.Errorf("could not navigate to row %q", label)
	}
	s.pressKey("enter")
	return nil
}

func (s *opBDD) userPressesEsc() error { s.pressKey("esc"); return nil }
func (s *opBDD) userPresses(k string) error {
	// y/enter on an open plate emits the staging tick as a Cmd; deliver the KEY only and
	// leave the tick to the explicit exec steps (fireExec) so "only after that paint is
	// the exec issued" stays a real ordering assertion.
	if s.model().operatorPlate != nil && (k == "y" || k == "enter") {
		s.update(keyMsg(k))
		return nil
	}
	if k == "down" { // a real arrow key, not the runes d-o-w-n typed into the prompt
		s.update(tea.KeyMsg{Type: tea.KeyDown})
		return nil
	}
	s.pressKey(k)
	return nil
}

func (s *opBDD) pickerStillOpen() error { return s.pickerOpen() }

func (s *opBDD) tuiDidNotSwitchModes() error {
	if s.model().mode != modeAgent {
		return fmt.Errorf("the TUI left AGENT mode (mode=%d)", s.model().mode)
	}
	return nil
}

func (s *opBDD) guestAppearsOnPath(name string) error {
	g, err := registryGuest(name)
	if err != nil {
		return err
	}
	s.tuiPaths[g.Bin] = "/fake/" + g.Bin
	return nil
}

func (s *opBDD) pickerRowsInclude(label string) error {
	if !containsStr(s.pickerRowLabels(), label) {
		return fmt.Errorf("picker rows %v lack %s", s.pickerRowLabels(), label)
	}
	return nil
}

func (s *opBDD) transcriptShowsInstallHint(name string) error {
	g, err := registryGuest(name)
	if err != nil {
		return err
	}
	return s.transcriptShows(g.InstallHint)
}

func (s *opBDD) noChatTurnSubmitted() error {
	m := s.model()
	if m.agentBusy || len(m.agentQueued) != 0 {
		return fmt.Errorf("a chat turn was submitted/queued")
	}
	return nil
}

func (s *opBDD) nameNotKnownOperator() error { return s.transcriptShows("is not a known operator") }

// --- handoff lifecycle steps ---------------------------------------------------------------

func (s *opBDD) holderDisconnected() error {
	s.holder.Disconnect()
	return nil
}

func (s *opBDD) djTurnInFlight() error {
	s.mutate(func(m *model) { m.agentBusy = true })
	return nil
}

func (s *opBDD) djTurnInFlightAndQueued() error {
	s.mutate(func(m *model) {
		m.agentBusy = true
		m.agentQueued = append(m.agentQueued, queuedPrompt{text: "parked ask"})
	})
	return nil
}

func (s *opBDD) noChildLaunched() error {
	if len(s.execCmds) != 0 {
		return fmt.Errorf("a child exec was issued: %v", s.execCmds[0].Args)
	}
	return nil
}

func (s *opBDD) pointsAtTuningIn() error { return s.transcriptShows("tune in first") }
func (s *opBDD) notesDJMidTurn() error   { return s.transcriptShows("mid-turn") }

// fireExec advances a staged handoff to the exec (the staging tick's message).
func (s *opBDD) fireExec() {
	if h := s.model().operatorHandoff; h != nil && !h.execing {
		s.update(operatorExecMsg{})
	}
}

// ensureHandoff makes sure the full begin path ran for the named guest: /operator <name>
// if nothing is staged, the Phase 3 pre-launch plate accepted with the local y, then the
// staging tick. (The plate interposes on every pick since Phase 3; a Phase 2 lifecycle
// scenario reaches its staged/exec assertions through the accepted plate - the invariants
// it pins are unchanged.)
func (s *opBDD) ensureHandoff(name string) error {
	if s.model().operatorHandoff == nil && len(s.execCmds) == 0 && s.model().operatorPlate == nil {
		if err := s.userRuns("/operator " + name); err != nil {
			return err
		}
	}
	s.acceptPlateIfUp()
	s.fireExec()
	if len(s.execCmds) == 0 {
		return fmt.Errorf("no exec was issued for %s (transcript: %s)", name, s.view())
	}
	return nil
}

func (s *opBDD) handoffBegins(name string) error {
	if err := s.ensureHandoff(name); err != nil {
		return err
	}
	last := s.execCmds[len(s.execCmds)-1]
	if !strings.Contains(last.Path, name) {
		return fmt.Errorf("exec path %q is not the %s binary", last.Path, name)
	}
	return nil
}

func (s *opBDD) guestHasTheMic() error { return s.ensureHandoff("opencode") }

// acceptPlateIfUp advances an open pre-launch plate with the local y (the Phase 3 gate
// between picking and staging), WITHOUT executing the emitted staging tick - the exec is
// always advanced explicitly (fireExec) so exec-ordering assertions stay meaningful.
func (s *opBDD) acceptPlateIfUp() {
	if s.model().operatorPlate != nil {
		s.update(keyMsg("y"))
	}
}

func (s *opBDD) nextPaintShows(text string) error {
	// A Phase 2 scenario asserting the staged PATCHING paint reaches it through the
	// Phase 3 plate's local accept (the plate interposes before staging by spec).
	if strings.Contains(text, "PATCHING") {
		s.acceptPlateIfUp()
	}
	if !strings.Contains(s.view(), text) {
		return fmt.Errorf("the staged paint lacks %q:\n%s", text, s.view())
	}
	return nil
}

func (s *opBDD) paintShowsMicTo(name string) error {
	v := s.view()
	if !strings.Contains(v, "mic to") || !strings.Contains(v, name) {
		return fmt.Errorf("no mic-to line for %s:\n%s", name, v)
	}
	return nil
}

func (s *opBDD) paintShowsOnBand(mdl string) error {
	v := s.view()
	if !strings.Contains(v, "on band") || !strings.Contains(v, mdl) {
		return fmt.Errorf("no on-band line for %s:\n%s", mdl, v)
	}
	return nil
}

func (s *opBDD) paintShowsWireLine(text string) error { return s.nextPaintShows(text) }

func (s *opBDD) execOnlyAfterPaint() error {
	if len(s.execCmds) != 0 {
		return fmt.Errorf("the exec was issued before the staged paint")
	}
	s.fireExec()
	if len(s.execCmds) != 1 {
		return fmt.Errorf("the staging tick did not issue exactly one exec (%d)", len(s.execCmds))
	}
	return nil
}

func (s *opBDD) paintShowsBaseURL() error {
	s.acceptPlateIfUp() // the staged paint sits one accepted plate past the pick
	return s.nextPaintShows(s.model().endpoint)
}

func (s *opBDD) paintShowsModel(mdl string) error {
	s.acceptPlateIfUp()
	v := s.view()
	if !strings.Contains(v, "MODEL") || !strings.Contains(v, mdl) {
		return fmt.Errorf("no MODEL %s on the staged paint:\n%s", mdl, v)
	}
	return nil
}

func (s *opBDD) bandRetunedSinceBind() error {
	s.holder.SetBand(client.ProxyOptions{Broker: s.brokerSrv.URL, User: "tester", Model: "llama-3.3-70b"})
	return nil
}

func (s *opBDD) childCarriesCurrentOptions() error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	last := s.execCmds[len(s.execCmds)-1]
	argv := strings.Join(last.Args, " ")
	if !strings.Contains(argv, "llama-3.3-70b") {
		return fmt.Errorf("child argv %q does not carry the re-tuned model", argv)
	}
	cfg := envValue(last.Env, "OPENCODE_CONFIG")
	b, err := os.ReadFile(cfg)
	if err != nil {
		return fmt.Errorf("read scratch config: %v", err)
	}
	if !strings.Contains(string(b), s.model().endpoint) {
		return fmt.Errorf("scratch config does not carry the live endpoint")
	}
	return nil
}

func (s *opBDD) neverFrozenOptions() error {
	last := s.execCmds[len(s.execCmds)-1]
	if strings.Contains(strings.Join(last.Args, " "), "qwen3-32b-fp8") {
		return fmt.Errorf("child argv carries the first-bind model")
	}
	return nil
}

func (s *opBDD) previousSessionSpent(amount string) error {
	return s.proxyCallsCosts([]string{amount})
}

func (s *opBDD) holderBudgetIsDefault() error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	if b := s.holder.Get().Budget; b != client.DefaultSessionBudget {
		return fmt.Errorf("holder budget = %v, want %v", b, client.DefaultSessionBudget)
	}
	return nil
}

func (s *opBDD) holderSpendZero() error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	if v := s.holder.Spent(); v != 0 {
		return fmt.Errorf("holder spend = %v, want $0.00", v)
	}
	return nil
}

func (s *opBDD) holderCallsZero() error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	if n := s.holder.Calls(); n != 0 {
		return fmt.Errorf("holder calls = %d, want 0", n)
	}
	return nil
}

func (s *opBDD) childEnvCarriesSameKey() error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	last := s.execCmds[len(s.execCmds)-1]
	if envValue(last.Env, operator.SessionKeyEnv) != s.holder.Get().SessionKey {
		return fmt.Errorf("child env key differs from the proxy's")
	}
	return nil
}

func (s *opBDD) keyNotRotated() error {
	if s.holder.Get().SessionKey != s.keyAtSeed {
		return fmt.Errorf("the handoff rotated the session bearer key")
	}
	return nil
}

// doReturn delivers the ExecProcess callback and executes the emitted Cmd tree
// (balance refresh + mouse restore) collecting the produced messages.
func (s *opBDD) doReturn(err error) {
	if s.holder != nil {
		s.retCalls, s.retSpend = s.holder.Calls(), s.holder.Spent()
	}
	if h := s.model().operatorHandoff; h != nil && h.launch.Dir != "" {
		s.firstDir = h.launch.Dir
	}
	cmd := s.update(operatorDoneMsg{err: err})
	s.collected = collectCmdMsgs(cmd)
}

// realExitErr produces a REAL *exec.ExitError with the given code (a real child ran).
func realExitErr(code int) error {
	return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
}

// realKillErr produces a REAL signal-death *exec.ExitError (exit code -1).
func realKillErr() error {
	return exec.Command("sh", "-c", "kill -KILL $$").Run()
}

func (s *opBDD) guestReturnsAfter(minutes, calls int, spend string) error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	s.model().operatorHandoff.start = time.Now().Add(-time.Duration(minutes) * time.Minute)
	costs := make([]string, calls)
	costs[0] = spend // one metered call carries the whole session cost; the rest bill $0
	for i := 1; i < calls; i++ {
		costs[i] = "0"
	}
	if err := s.proxyCallsCosts(costs); err != nil {
		return err
	}
	s.doReturn(nil)
	return nil
}

func (s *opBDD) guestReturnsExit(code int) error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	if code == 0 {
		s.doReturn(nil)
	} else {
		s.doReturn(realExitErr(code))
	}
	return nil
}

func (s *opBDD) guestKilled() error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	s.doReturn(realKillErr())
	return nil
}

func (s *opBDD) guestReturnsAnyExit() error { return s.guestReturnsExit(1) }
func (s *opBDD) guestReturnsPlain() error {
	s.doReturn(nil)
	return nil
}

func (s *opBDD) resetPreambleRan() error {
	got := s.resetBuf.String()
	// \x1b[?1004l (focus reporting off) joined the preamble in the iteration-1 fix pass:
	// a guest can leave focus-event reporting on, spraying ESC[I/ESC[O into the prompt.
	for _, seq := range []string{"\x1b[<u", "\x1b[?1000l", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1006l", "\x1b[?1004l", "\x1b[?2004l"} {
		if !strings.Contains(got, seq) {
			return fmt.Errorf("terminal reset preamble missing %q (got %q)", seq, got)
		}
	}
	return nil
}

func (s *opBDD) mouseModesDisabled() error {
	got := s.resetBuf.String()
	for _, seq := range []string{"\x1b[?1000l", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1006l"} {
		if !strings.Contains(got, seq) {
			return fmt.Errorf("mouse disable missing %q", seq)
		}
	}
	return nil
}

func (s *opBDD) mouseReenabled() error {
	for _, msg := range s.collected {
		if strings.Contains(fmt.Sprintf("%T", msg), "enableMouseCellMotion") {
			return nil
		}
	}
	return fmt.Errorf("mouse cell motion was not re-enabled (msgs: %v)", s.collectedTypes())
}

func (s *opBDD) mouseNotReenabled() error {
	for _, msg := range s.collected {
		if strings.Contains(fmt.Sprintf("%T", msg), "enableMouse") {
			return fmt.Errorf("mouse reporting was re-enabled despite m.mouseOff")
		}
	}
	return nil
}

func (s *opBDD) collectedTypes() []string {
	var out []string
	for _, m := range s.collected {
		out = append(out, fmt.Sprintf("%T", m))
	}
	return out
}

func (s *opBDD) balanceRefreshRequested() error {
	for _, msg := range s.collected {
		if _, ok := msg.(balanceMsg); ok {
			return nil
		}
	}
	return fmt.Errorf("no balance refresh among %v", s.collectedTypes())
}

func (s *opBDD) userMouseOff() error {
	s.mutate(func(m *model) { m.mouseOff = true })
	return nil
}

// summaryFigures parses "N calls · $X.YZ" out of the rendered summary line.
func (s *opBDD) summaryFigures() (int64, string, error) {
	re := regexp.MustCompile(`(\d+) calls? · \$([0-9.]+)`)
	match := re.FindStringSubmatch(s.view())
	if match == nil {
		return 0, "", fmt.Errorf("no summary line in:\n%s", s.view())
	}
	n, _ := strconv.ParseInt(match[1], 10, 64)
	return n, match[2], nil
}

func (s *opBDD) summaryCallsEqualHolder() error {
	n, _, err := s.summaryFigures()
	if err != nil {
		return err
	}
	if n != s.retCalls {
		return fmt.Errorf("summary calls %d != holder counter %d", n, s.retCalls)
	}
	return nil
}

func (s *opBDD) summarySpendEqualsHolder() error {
	_, spend, err := s.summaryFigures()
	if err != nil {
		return err
	}
	if want := fmt.Sprintf("%.2f", s.retSpend); spend != want {
		return fmt.Errorf("summary spend $%s != holder Spent() $%s", spend, want)
	}
	return nil
}

func (s *opBDD) noteIsCalm() error {
	v := s.view()
	if !strings.Contains(v, "✕ the guest dropped off") {
		return fmt.Errorf("the drop-off note is missing its calm ✕ form:\n%s", v)
	}
	if strings.Contains(v, "couldn't hand the mic off") {
		return fmt.Errorf("a drop-off must not be styled as a launch error")
	}
	return nil
}

func (s *opBDD) restoreAndSummaryRan() error {
	if err := s.resetPreambleRan(); err != nil {
		return err
	}
	return s.transcriptShows("had the mic for")
}

func (s *opBDD) summaryLineRenders() error {
	if _, _, err := s.summaryFigures(); err != nil {
		return err
	}
	return nil
}

func (s *opBDD) scratchConfigCleaned() error { return s.noOperatorScratchRemains() }

func (s *opBDD) binaryVanishes() error {
	s.spawnFail = true
	return nil
}

func (s *opBDD) tuiPaintingAgain() error {
	if s.spawnFail {
		s.acceptPlateIfUp()
		s.fireExec()
		s.doReturn(&exec.Error{Name: "opencode", Err: exec.ErrNotFound})
		s.spawnFail = false
	}
	if s.model().operatorHandoff != nil {
		return fmt.Errorf("a handoff is still holding the screen")
	}
	if !strings.Contains(s.view(), "ask ›") {
		return fmt.Errorf("the ask prompt is not painting:\n%s", s.view())
	}
	return nil
}

func (s *opBDD) errorNoteNamesLaunchFailure() error {
	return s.transcriptShows("couldn't hand the mic off")
}

func (s *opBDD) guestSpendsToBudget() error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	return s.proxyCallsCosts([]string{"0.70", "0.70", "0.70"}) // the 3rd crosses the $2 ceiling
}

func (s *opBDD) summaryNotesBudgetReached() error {
	return s.transcriptShows("session budget was reached")
}

func (s *opBDD) summarySpendAtOrPastBudget() error {
	_, spend, err := s.summaryFigures()
	if err != nil {
		return err
	}
	v, _ := strconv.ParseFloat(spend, 64)
	if v < client.DefaultSessionBudget {
		return fmt.Errorf("summary spend $%s is under the $%.2f ceiling", spend, client.DefaultSessionBudget)
	}
	return nil
}

func (s *opBDD) bandGoesOffAir() error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	if err := s.proxyCallsCosts([]string{"0.05"}); err != nil { // spend settled before the drop
		return err
	}
	s.holder.Disconnect()
	return nil
}

func (s *opBDD) summaryShowsSpendBeforeDrop() error { return s.transcriptShows("$0.05") }

func (s *opBDD) deskUsableForRetune() error {
	m := s.model()
	if m.mode != modeAgent || m.operatorHandoff != nil || m.agentBusy {
		return fmt.Errorf("the desk is not usable (mode=%d busy=%v)", m.mode, m.agentBusy)
	}
	return nil
}

func (s *opBDD) noKeyHandlingDuringExec() error {
	before := s.view()
	for _, k := range []string{"x", "1", "esc", "enter"} {
		s.pressKey(k)
	}
	if s.view() != before {
		return fmt.Errorf("a key changed the suspended TUI")
	}
	if s.model().agentIn.Value() != "" {
		return fmt.Errorf("a key reached the prompt input during the handoff")
	}
	return nil
}

func (s *opBDD) guestReturnsAfterSpending(amount string) error {
	if err := s.ensureHandoff("opencode"); err != nil {
		return err
	}
	if err := s.proxyCallsCosts([]string{amount}); err != nil {
		return err
	}
	s.doReturn(nil)
	return nil
}

func (s *opBDD) userRunsOperatorAgain() error {
	if err := s.userRuns("/operator opencode"); err != nil {
		return err
	}
	s.acceptPlateIfUp()
	s.fireExec()
	return nil
}

func (s *opBDD) secondHandoffFreshScratch() error {
	dirs := scratchDirsUnder(s.scratchRoot)
	if len(dirs) != 1 {
		return fmt.Errorf("scratch dirs = %v, want exactly the second session's", dirs)
	}
	if dirs[0] == s.firstDir {
		return fmt.Errorf("the second handoff reused the first scratch dir")
	}
	return nil
}

func (s *opBDD) holderSpendZeroAgain() error {
	if v := s.holder.Spent(); v != 0 {
		return fmt.Errorf("holder spend = %v after the second handoff, want $0.00", v)
	}
	return nil
}

func (s *opBDD) holderCallsZeroAgain() error {
	if n := s.holder.Calls(); n != 0 {
		return fmt.Errorf("holder calls = %d after the second handoff, want 0", n)
	}
	return nil
}

func (s *opBDD) namedGuestReturns(name string) error {
	if err := s.ensureHandoff(name); err != nil {
		return err
	}
	s.doReturn(nil)
	return nil
}

func (s *opBDD) aiderCarriesNoOpencodeArtifacts() error {
	if err := s.ensureHandoff("aider"); err != nil {
		return err
	}
	last := s.execCmds[len(s.execCmds)-1]
	if !strings.Contains(last.Path, "aider") {
		return fmt.Errorf("the second exec is not aider: %s", last.Path)
	}
	if envValue(last.Env, "OPENCODE_CONFIG") != "" {
		return fmt.Errorf("aider child env carries OPENCODE_CONFIG")
	}
	return nil
}

func (s *opBDD) firstScratchGone() error {
	if s.firstDir == "" {
		return nil // the first guest (aider two-guest flow) had no scratch dir at all
	}
	if _, err := os.Stat(s.firstDir); !os.IsNotExist(err) {
		return fmt.Errorf("the first session's scratch dir remains: %s", s.firstDir)
	}
	return nil
}

func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env { // last one wins, matching child-process semantics
		if strings.HasPrefix(kv, key+"=") {
			v = strings.TrimPrefix(kv, key+"=")
		}
	}
	return v
}

// --- RC interlock steps -------------------------------------------------------------------

const opRCSession = "opsess"

func (s *opBDD) startRCBroker() {
	s.rcQueue = make(chan protocol.RCInbound, 32)
	mux := http.NewServeMux()
	mux.HandleFunc("/rc/"+opRCSession+"/poll", func(w http.ResponseWriter, r *http.Request) {
		if s.rc401.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		select {
		case in := <-s.rcQueue:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(in)
		case <-time.After(40 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("/rc/"+opRCSession+"/events", func(w http.ResponseWriter, r *http.Request) {
		var fs []protocol.RCFrame
		_ = json.NewDecoder(r.Body).Decode(&fs)
		s.framesMu.Lock()
		s.frames = append(s.frames, fs...)
		s.framesMu.Unlock()
	})
	s.rcSrv = httptest.NewServer(mux)
	s.bridge = client.NewRCBridge(s.rcSrv.URL, opRCSession, "host-token")
	s.bridge.Run()
}

func (s *opBDD) hostSessionWithBridge() error {
	s.seedTUI("qwen3-32b-fp8")
	s.startRCBroker()
	s.mutate(func(m *model) { m.rcBridge = s.bridge })
	return nil
}

func (s *opBDD) tunedBandAndDetectedGuest(name string) error { return s.addDetected(name) }

func (s *opBDD) framesSnapshot() []protocol.RCFrame {
	s.framesMu.Lock()
	defer s.framesMu.Unlock()
	return append([]protocol.RCFrame(nil), s.frames...)
}

func (s *opBDD) statusFrameCount() int {
	n := 0
	for _, f := range s.framesSnapshot() {
		if f.Kind == protocol.RCKindStatus {
			n++
		}
	}
	return n
}

func waitFor(cond func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func (s *opBDD) statusFrameEmittedBeforeExec() error {
	if len(s.execCmds) == 0 {
		return fmt.Errorf("no exec was issued")
	}
	if !waitFor(func() bool { return s.statusFrameCount() > 0 }, 2*time.Second) {
		return fmt.Errorf("no status frame reached the broker")
	}
	// "before the exec": the emit precedes the Park in onOperatorExec and the Park is
	// strictly proven to precede the exec (parkedAtExec); the pump preserves order, so
	// the FIRST frame on the wire must be the handoff-start status frame.
	first := s.framesSnapshot()[0]
	if first.Kind != protocol.RCKindStatus || first.Operator == "" {
		return fmt.Errorf("the first wire frame is not the operator status frame: %+v", first)
	}
	if len(s.parkedAtExec) == 0 || !s.parkedAtExec[len(s.parkedAtExec)-1] {
		return fmt.Errorf("the exec was issued before the interlock engaged")
	}
	return nil
}

func (s *opBDD) frameNamesOperator(name string) error {
	for _, f := range s.framesSnapshot() {
		if f.Kind == protocol.RCKindStatus && f.Operator == name {
			return nil
		}
	}
	return fmt.Errorf("no status frame names operator %q (frames: %+v)", name, s.framesSnapshot())
}

func (s *opBDD) bridgeIsParked() error {
	if _, parked := s.bridge.Parked(); !parked {
		return fmt.Errorf("the bridge is not parked")
	}
	return nil
}

func (s *opBDD) parkedBeforeExecReturned() error {
	if len(s.parkedAtExec) == 0 || !s.parkedAtExec[len(s.parkedAtExec)-1] {
		return fmt.Errorf("the bridge was not parked when the exec cmd was issued")
	}
	return nil
}

// viewerSendsTurn queues an inbound turn on the stub broker and waits for the bridge to
// consume it (parked: the status auto-frame lands; unparked: it reaches Inbound()).
func (s *opBDD) viewerSendsTurn(text string) error {
	base := s.statusFrameCount()
	s.rcQueue <- protocol.RCInbound{Kind: protocol.RCInTurn, Text: text, Origin: "phone"}
	_, parked := s.bridge.Parked()
	if parked {
		if !waitFor(func() bool { return s.statusFrameCount() > base }, 2*time.Second) {
			return fmt.Errorf("the parked bridge never answered the turn with a status frame")
		}
		return nil
	}
	if !waitFor(func() bool { return len(s.rcQueue) == 0 }, 2*time.Second) {
		return fmt.Errorf("the bridge never polled the turn")
	}
	return nil
}

func (s *opBDD) turnNotQueuedForDJ() error {
	select {
	case in := <-s.bridge.Inbound():
		return fmt.Errorf("a parked turn reached the host inbound: %+v", in)
	default:
	}
	if q := s.model().agentQueued; len(q) != 0 {
		return fmt.Errorf("a parked turn was queued for the DJ: %v", q)
	}
	return nil
}

func (s *opBDD) viewersReceiveGuestHasMicStatus() error {
	for _, f := range s.framesSnapshot() {
		if f.Kind == protocol.RCKindStatus && strings.Contains(f.Text, "guest has the mic") {
			return nil
		}
	}
	return fmt.Errorf("no 'guest has the mic' status frame (frames: %+v)", s.framesSnapshot())
}

// --- desktop remote VIEWER (iteration-2 status-frame render finding) --------------------

// viewerAttached stands up a standalone desktop RC viewer in the live-session mode, on a
// fresh generation - the surface that renders broker-relayed frames into its transcript.
func (s *opBDD) viewerAttached() error {
	s.viewer = bsSeed()
	s.viewer.mode = modeRemoteSession
	s.viewer.rsGen = 1
	s.viewer.rsVP = viewport.New(80, 10)
	return nil
}

// viewerReceivesOperatorStatus feeds the REAL guest-has-the-mic status frame (the ONE
// constructor the host + bridge share) through the viewer's real onRemoteFrame.
func (s *opBDD) viewerReceivesOperatorStatus(op string) error {
	nm, _ := s.viewer.onRemoteFrame(remoteFrameMsg{gen: s.viewer.rsGen, f: client.OperatorStatusFrame(op, "", 0)})
	s.viewer = asModel(nm)
	return nil
}

// viewerReceivesDJBack feeds the DJ-back status frame the desk emits on return.
func (s *opBDD) viewerReceivesDJBack() error {
	nm, _ := s.viewer.onRemoteFrame(remoteFrameMsg{gen: s.viewer.rsGen,
		f: protocol.RCFrame{Kind: protocol.RCKindStatus, Text: "the DJ is back at the desk"}})
	s.viewer = asModel(nm)
	return nil
}

// viewerTranscriptShows asserts the rendered viewer transcript carries the text (the frame
// was not silently dropped).
func (s *opBDD) viewerTranscriptShows(text string) error {
	if joined := stripANSI(strings.Join(s.viewer.rsLines, "\n")); strings.Contains(joined, text) {
		return nil
	}
	return fmt.Errorf("the viewer transcript does not show %q (lines: %q)", text, s.viewer.rsLines)
}

func (s *opBDD) noAgentTurnFires(text string) error {
	if err := s.turnNotQueuedForDJ(); err != nil {
		return err
	}
	if strings.Contains(s.view(), text) {
		return fmt.Errorf("the dropped turn %q reached the transcript", text)
	}
	if s.model().agentBusy {
		return fmt.Errorf("an agent turn fired")
	}
	return nil
}

func (s *opBDD) viewersSentNTurns(n int) error {
	for i := 0; i < n; i++ {
		if err := s.viewerSendsTurn(fmt.Sprintf("parked turn %d", i+1)); err != nil {
			return err
		}
	}
	return nil
}

func (s *opBDD) guestReturnsAndUnparks() error {
	s.doReturn(nil)
	if _, parked := s.bridge.Parked(); parked {
		return fmt.Errorf("the bridge is still parked after the return")
	}
	return nil
}

func (s *opBDD) noQueuedTurnFires() error { return s.turnNotQueuedForDJ() }

func (s *opBDD) djReceivesZeroInjectedTurns() error {
	if strings.Contains(s.view(), "parked turn") {
		return fmt.Errorf("a parked-window turn replayed into the DJ")
	}
	return s.turnNotQueuedForDJ()
}

func (s *opBDD) viewerSendsConfirm() error {
	s.rcQueue <- protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: true, ConfirmID: "stale", Origin: "phone"}
	if !waitFor(func() bool { return len(s.rcQueue) == 0 }, 2*time.Second) {
		return fmt.Errorf("the bridge never polled the confirm")
	}
	time.Sleep(50 * time.Millisecond) // let the intercept settle before asserting the drop
	return nil
}

func (s *opBDD) confirmDropped() error {
	select {
	case in := <-s.bridge.Inbound():
		return fmt.Errorf("a parked confirm reached the host: %+v", in)
	default:
		return nil
	}
}

func (s *opBDD) noToolRuns() error {
	if s.model().agentPendingConfirm != nil {
		return fmt.Errorf("a confirm is pending")
	}
	if strings.Contains(s.view(), "WILCO") { // the approve-and-run echo (design overhaul inc 2; was "approved · running")
		return fmt.Errorf("a tool ran off a parked confirm")
	}
	return nil
}

func (s *opBDD) viewerAttachesRequestsBackfill() error {
	s.rcQueue <- protocol.RCInbound{Kind: protocol.RCInBackfill, Viewer: "roadside", Origin: "roadside"}
	if !waitFor(func() bool {
		for _, f := range s.framesSnapshot() {
			if f.Kind == protocol.RCKindBackfill && f.Viewer == "roadside" {
				return true
			}
		}
		return false
	}, 2*time.Second) {
		return fmt.Errorf("no backfill frame was answered for the mid-handoff viewer")
	}
	return nil
}

func (s *opBDD) viewerReceivesTranscriptSnapshot() error {
	for _, f := range s.framesSnapshot() {
		if f.Kind == protocol.RCKindBackfill && f.Viewer == "roadside" && f.Text != "" {
			return nil
		}
	}
	return fmt.Errorf("the backfill answer carries no transcript snapshot")
}

func (s *opBDD) statusFrameDJBack() error {
	if !waitFor(func() bool {
		for _, f := range s.framesSnapshot() {
			if f.Kind == protocol.RCKindStatus && strings.Contains(f.Text, "DJ is back") {
				return true
			}
		}
		return false
	}, 2*time.Second) {
		return fmt.Errorf("no 'DJ is back' status frame after the return")
	}
	return nil
}

func (s *opBDD) subsequentTurnInjectsLikeLocal() error {
	const text = "status check from the road"
	s.rcQueue <- protocol.RCInbound{Kind: protocol.RCInTurn, Text: text, Origin: "phone"}
	select {
	case in := <-s.bridge.Inbound():
		s.update(remoteInboundMsg(in)) // the re-armed drain's round trip
	case <-time.After(2 * time.Second):
		return fmt.Errorf("the unparked bridge never delivered the turn")
	}
	if !strings.Contains(s.view(), text) {
		return fmt.Errorf("the post-handoff turn did not inject into the DJ:\n%s", s.view())
	}
	return nil
}

func (s *opBDD) bridgeNotEnabled() error {
	s.mutate(func(m *model) { m.rcBridge = nil })
	if s.bridge != nil {
		s.bridge.Stop()
	}
	s.framesMu.Lock()
	s.frames = nil
	s.framesMu.Unlock()
	return nil
}

func (s *opBDD) noFrameEmitted() error {
	time.Sleep(120 * time.Millisecond) // give a stray frame the chance to land
	if got := s.framesSnapshot(); len(got) != 0 {
		return fmt.Errorf("frames were emitted with no bridge: %+v", got)
	}
	return nil
}

func (s *opBDD) handoffProceedsNormally() error {
	if len(s.execCmds) == 0 {
		return fmt.Errorf("the handoff did not reach the exec")
	}
	return nil
}

func (s *opBDD) sessionRevokedDuringHandoff() error {
	s.rc401.Store(true)
	select {
	case <-s.bridge.Done():
		return nil
	case <-time.After(3 * time.Second):
		return fmt.Errorf("the 401'd bridge never stopped")
	}
}

func (s *opBDD) unparkIsNoOp() error {
	s.doReturn(nil) // Unpark on the dead bridge must be silent, never a panic
	return nil
}

func (s *opBDD) deskSummaryStillRenders() error { return s.transcriptShows("had the mic for") }

func (s *opBDD) protocolReservesFrameKind(name string) error {
	if protocol.RCKindOperatorStatus != name {
		return fmt.Errorf("RCKindOperatorStatus = %q, want %q", protocol.RCKindOperatorStatus, name)
	}
	return nil
}

func (s *opBDD) protocolReservesInboundKind(name string) error {
	switch name {
	case protocol.RCInOperatorHandoff, protocol.RCInOperatorRecall:
		return nil
	}
	return fmt.Errorf("inbound kind %q is not reserved", name)
}

// reservedKindsNoBehavior EXECUTES the claim: each reserved inbound kind is fed through
// the host's real dispatch (onRemoteInbound) and must change nothing - no turn, no queue,
// no transcript, no confirm (the default case drops unknown kinds; viewers likewise
// ignore unknown frame kinds by the wire contract).
func (s *opBDD) reservedKindsNoBehavior() error {
	before := s.view()
	for _, kind := range []string{protocol.RCInOperatorHandoff, protocol.RCInOperatorRecall} {
		s.update(remoteInboundMsg(protocol.RCInbound{Kind: kind, Text: "future-surface payload"}))
		m := s.model()
		if m.agentBusy || len(m.agentQueued) != 0 || m.agentPendingConfirm != nil {
			return fmt.Errorf("reserved inbound kind %q triggered behavior in v1", kind)
		}
	}
	if s.view() != before {
		return fmt.Errorf("a reserved inbound kind changed the transcript in v1")
	}
	return nil
}

func (s *opBDD) handoffBeginsAndGuestWorks(name string) error {
	if err := s.handoffBegins(name); err != nil {
		return err
	}
	return s.proxyCallsCosts([]string{"0.01", "0"}) // the guest works the band a little
}

func (s *opBDD) noFrameCarriesGuestOutput() error {
	for _, f := range s.framesSnapshot() {
		if f.Kind != protocol.RCKindStatus {
			return fmt.Errorf("a non-status frame leaked during the handoff: %+v", f)
		}
	}
	return nil
}

// --- iteration-1 fix-pass steps (2026-07) ----------------------------------------------------

// viewerTurnQueuedWhileBusy drives a REAL remote turn through the unparked bridge into
// the busy host and pins that it landed on the DJ queue (the finding-#1 setup).
func (s *opBDD) viewerTurnQueuedWhileBusy(text string) error {
	s.rcQueue <- protocol.RCInbound{Kind: protocol.RCInTurn, Text: text, Origin: "phone"}
	var in protocol.RCInbound
	select {
	case in = <-s.bridge.Inbound():
	case <-time.After(2 * time.Second):
		return fmt.Errorf("the bridge never delivered the remote turn")
	}
	s.update(remoteInboundMsg(in))
	if got := len(s.model().agentQueued); got != 1 {
		return fmt.Errorf("the busy host queued %d entries, want 1", got)
	}
	return nil
}

// djTurnFinishesQueueDrains delivers the turn-done message; the Update loop dequeues the
// parked prompt exactly like production (dequeueAgentPrompts via agentDoneMsg).
func (s *opBDD) djTurnFinishesQueueDrains() error {
	s.update(agentDoneMsg{})
	return nil
}

func (s *opBDD) noHandoffStagedNoChild() error {
	if s.model().operatorHandoff != nil {
		return fmt.Errorf("a handoff was staged")
	}
	return s.noChildLaunched()
}

func (s *opBDD) answeredAsChatTurn(text string) error {
	m := s.model()
	if !m.agentBusy {
		return fmt.Errorf("no chat turn started for the drained remote text")
	}
	if !strings.Contains(s.view(), "▌ "+text) { // the user-turn band bar (design overhaul inc 6; was ▸)
		return fmt.Errorf("the transcript lacks the chat-turn echo for %q:\n%s", text, s.view())
	}
	return nil
}

// handoffStagedNotExeced opens the staging window (the PATCHING plate is up, the exec
// tick has not fired) - the exact window findings #3 and #4 live in.
func (s *opBDD) handoffStagedNotExeced(name string) error {
	if err := s.userRuns("/operator " + name); err != nil {
		return err
	}
	s.acceptPlateIfUp() // the Phase 3 plate interposes; staging begins on the local y
	h := s.model().operatorHandoff
	if h == nil || h.execing {
		return fmt.Errorf("the handoff is not in the staging window")
	}
	return nil
}

func (s *opBDD) djSlipsInStagingElapses() error {
	s.mutate(func(m *model) { m.agentBusy = true })
	s.update(operatorExecMsg{})
	return nil
}

func (s *opBDD) handoffAbortedNoChild() error {
	if s.model().operatorHandoff != nil {
		return fmt.Errorf("the handoff is still staged after the abort")
	}
	return s.noChildLaunched()
}

func (s *opBDD) bandDropsDuringStaging() error {
	s.holder.Disconnect()
	return nil
}

func (s *opBDD) stagingBeatElapses() error {
	s.update(operatorExecMsg{})
	return nil
}

func (s *opBDD) bracketedPasteReenabled() error {
	for _, msg := range s.collected {
		if strings.Contains(fmt.Sprintf("%T", msg), "enableBracketedPaste") {
			return nil
		}
	}
	return fmt.Errorf("bracketed paste was not re-enabled in the return cmd set (msgs: %v)", s.collectedTypes())
}

// detectedGuestUnverified seeds a desk detection whose version probe failed (Unverified,
// empty version) - the picker dims "version unknown unproven" for it.
func (s *opBDD) detectedGuestUnverified(name string) error {
	g, err := registryGuest(name)
	if err != nil {
		return err
	}
	s.tuiPaths[g.Bin] = "/fake/" + g.Bin
	s.mutate(func(m *model) {
		m.operatorDetections = append(m.operatorDetections,
			operator.Detection{Guest: g, Path: "/fake/" + g.Bin, Unverified: true})
	})
	return nil
}

// --- suite wiring ---------------------------------------------------------------------------

// initializeOperatorScenarios registers every step of the founder-approved Phase 2 spec
// set against the shared opBDD state.
func initializeOperatorScenarios(t *testing.T, st *opBDD, sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		st.reset(t)
		return ctx, nil
	})

	// ── detection.feature (pure) ──────────────────────────────────────────────
	sc.Step(`^the guest operator registry$`, st.theGuestOperatorRegistry)
	sc.Step(`^the registry lists exactly "([^"]*)", "([^"]*)", "([^"]*)" in that order$`, st.registryListsExactly)
	sc.Step(`^no registry entry is named "([^"]*)" or "([^"]*)"$`, st.noRegistryEntryNamed)
	sc.Step(`^every entry carries a name, a PATH binary, a provider tag, an install hint, and a known-good version$`, st.everyEntryCarriesFields)
	sc.Step(`^the "([^"]*)" entry uses the (scratch-config|scratch-home) strategy with known-good version "([^"]*)"$`,
		func(name, strat, v string) error { return st.entryUsesStrategyWithVersion(name, strat, v) })
	sc.Step(`^the "aider" entry uses the env-and-flags strategy with no config file at all$`, st.aiderEnvFlagsNoConfig)
	sc.Step(`^LookPath resolves "([^"]*)" to "([^"]*)"$`, st.lookPathResolves)
	sc.Step(`^LookPath fails for "([^"]*)" with "([^"]*)"$`, st.lookPathFails)
	sc.Step(`^LookPath fails for every binary$`, st.lookPathFailsForEverything)
	sc.Step(`^LookPath resolves every registry binary$`, st.lookPathResolvesEveryRegistryBinary)
	sc.Step(`^the version probe answers "([^"]*)" with "([^"]*)"$`, st.probeAnswers)
	sc.Step(`^the version probe fails for "([^"]*)" with "([^"]*)"$`, st.probeFails)
	sc.Step(`^the version probe for "([^"]*)" blocks past its deadline$`, st.probeBlocksPastDeadline)
	sc.Step(`^the desk is scanned( again)?$`, func(string) error { return st.deskScanned() })
	sc.Step(`^the detections are "([^"]*)", "([^"]*)", "([^"]*)" in that order$`, st.detectionsInOrder)
	sc.Step(`^the detections are exactly "([^"]*)"$`, st.detectionsExactly)
	sc.Step(`^the detections are empty$`, st.detectionsEmpty)
	sc.Step(`^no error is surfaced$`, st.noErrorSurfaced)
	sc.Step(`^the detections include "([^"]*)"$`, st.detectionsInclude)
	sc.Step(`^the detections do not include "([^"]*)"$`, st.detectionsDoNotInclude)
	sc.Step(`^each detection records the resolved path$`, st.eachDetectionRecordsPath)
	sc.Step(`^the "([^"]*)" detection has version "([^"]*)" and is verified$`, st.detectionVersionVerified)
	sc.Step(`^the "([^"]*)" detection is unverified with an empty version$`, st.detectionUnverifiedEmptyVersion)
	sc.Step(`^the "([^"]*)" detection is unverified$`, st.detectionUnverified)
	sc.Step(`^the "([^"]*)" detection is unverified with version "([^"]*)"$`, st.detectionUnverifiedWithVersion)
	sc.Step(`^the scan completes$`, st.scanCompletes)
	sc.Step(`^the raw version output "([^"]*)" for "([^"]*)" is parsed$`, st.rawVersionParsed)
	sc.Step(`^the parsed version is "([^"]*)"$`, st.parsedVersionIs)
	sc.Step(`^no file was written anywhere$`, st.noFileWrittenAnywhere)
	sc.Step(`^no request hit the local proxy$`, st.noRequestHitProxy)

	// ── config_*.feature + config_isolation.feature (pure) ───────────────────
	sc.Step(`^a live proxy session at "([^"]*)" with session key "([^"]*)" and band model "([^"]*)"$`, st.liveProxySession)
	sc.Step(`^a session scratch dir$`, st.aSessionScratchDir)
	sc.Step(`^the (opencode|hermes|aider) launch is materialized$`, st.materialize)
	sc.Step(`^the (opencode|hermes|aider) launch is materialized and the guest exits$`, st.materializeAndExit)
	sc.Step(`^the (opencode|hermes|aider) launch is materialized again$`, st.materializeAgain)
	sc.Step(`^the aider launch is materialized for any band model$`, func() error { return st.materialize("aider") })
	sc.Step(`^the scratch dir contains exactly one file "([^"]*)"$`, st.scratchContainsExactlyOneFile)
	sc.Step(`^the scratch dir contains "([^"]*)"$`, st.scratchContains)
	sc.Step(`^"([^"]*)" equals the golden artifact:$`, st.fileEqualsGolden)
	sc.Step(`^the argv is exactly "([^"]*)"$`, st.argvIsExactly)
	sc.Step(`^the argv (?:still )?pins "([^"]*)"$`, st.argvPins)
	sc.Step(`^the argv contains "([^"]*)"$`, st.argvContains)
	sc.Step(`^the env additions are exactly:$`, st.envAdditionsExactly)
	sc.Step(`^the parent environment is otherwise inherited unmodified$`, st.parentEnvInherited)
	sc.Step(`^no file under the scratch dir contains the string "([^"]*)"$`, st.noScratchFileContains)
	sc.Step(`^no file on the entire scratch path contains "([^"]*)"$`, st.noScratchFileContains)
	sc.Step(`^the launch workdir contains a project "opencode\.json" selecting model "([^"]*)"$`, st.workdirHasProjectConfig)
	sc.Step(`^the project "opencode\.json" is not modified$`, st.projectConfigNotModified)
	sc.Step(`^the band is re-tuned to model "([^"]*)" before the handoff$`, st.bandRetuned)
	sc.Step(`^"([^"]*)" wires (?:default )?model "([^"]*)"$`, st.fileWiresModel)
	sc.Step(`^the guest exits cleanly$`, st.guestExitsCleanly)
	sc.Step(`^the guest crashes with exit 1$`, st.guestCrashesExit1)
	sc.Step(`^the session scratch dir no longer exists$`, st.scratchDirGone)
	sc.Step(`^the user has a real "([^"]*)"$`, st.userHasRealFile)
	sc.Step(`^"([^"]*)" is byte-identical to before$`, st.realFileByteIdentical)
	sc.Step(`^nothing was written under "([^"]*)"$`, st.nothingWrittenUnder)
	sc.Step(`^"hermes-home/config\.yaml" contains a "providers:" section with an api_key reference$`, st.hermesConfigHasKeyedProviders)
	sc.Step(`^no scratch dir exists for the session$`, st.noScratchDirForSession)
	sc.Step(`^the parent environment already sets OPENAI_API_KEY to "([^"]*)"$`,
		func(v string) error { return st.parentEnvSets("OPENAI_API_KEY", v) })
	sc.Step(`^the child env sets ([A-Z_]+) to "([^"]*)"$`, st.childEnvSets)
	sc.Step(`^a sentinel snapshot of the user's home and the launch workdir$`, st.sentinelSnapshot)
	sc.Step(`^every written path is under the session scratch dir$`, st.everyWrittenPathUnderScratch)
	sc.Step(`^the user's home matches the sentinel snapshot$`, st.homeMatchesSentinel)
	sc.Step(`^the launch workdir matches the sentinel snapshot$`, st.workdirMatchesSentinel)
	sc.Step(`^the session scratch dir has mode 0700$`, st.scratchDirMode0700)
	sc.Step(`^the child working directory is the launch workdir$`, st.childWorkdirIsLaunchWorkdir)
	sc.Step(`^the child working directory is not the session scratch dir$`, st.childWorkdirNotScratch)
	sc.Step(`^no rogerai-operator scratch dir remains for this session$`, st.noOperatorScratchRemains)
	sc.Step(`^the guest removed files inside the scratch dir before exiting$`, st.guestRemovedFilesInScratch)
	sc.Step(`^cleanup still succeeds$`, st.cleanupStillSucceeds)
	sc.Step(`^a leftover "([^"]*)" scratch dir older than 24 hours$`, st.leftoverScratchDirOld)
	sc.Step(`^a fresh "([^"]*)" scratch dir from a running session$`, st.freshScratchDir)
	sc.Step(`^"(rogerai-operator-[^"]*)" is removed$`, st.namedDirRemoved)
	sc.Step(`^"(rogerai-operator-[^"]*)" is untouched$`, st.namedDirUntouched)
	sc.Step(`^the second session scratch dir differs from the first$`, st.secondScratchDiffers)
	sc.Step(`^only the second scratch dir exists$`, st.onlySecondScratchExists)
	sc.Step(`^the child env contains ROGER_SESSION_KEY$`, st.childEnvContainsSessionKey)
	sc.Step(`^the session budget is capped at the default session budget$`, st.budgetCappedAtDefault)

	// ── operator_command.feature (TUI) ─────────────────────────────────────────
	sc.Step(`^an AGENT session at the ask prompt$`, st.agentSessionAtPrompt)
	sc.Step(`^the agent command registry contains "([^"]*)"$`, st.registryContains)
	sc.Step(`^the registry stays sorted, lowercase, and duplicate-free$`, st.registrySortedLowercaseUnique)
	sc.Step(`^the agent command registry does not contain "([^"]*)"$`, st.registryNotContains)
	sc.Step(`^the user runs "([^"]*)"$`, st.userRuns)
	sc.Step(`^the transcript does not show "([^"]*)"$`, st.transcriptDoesNotShow)
	sc.Step(`^the user has typed "([^"]*)"$`, st.userHasTyped)
	sc.Step(`^the suggestion strip includes "([^"]*)"$`, st.stripIncludes)
	sc.Step(`^the suggestion strip never includes "([^"]*)" or "([^"]*)"$`, st.stripNeverIncludesAliases)
	sc.Step(`^no guest operators are detected$`, st.noGuestsDetected)
	sc.Step(`^detected guests "([^"]*)" and "([^"]*)"$`, st.detectedGuestsTwo)
	sc.Step(`^detected guests "([^"]*)"$`, st.detectedGuests)
	sc.Step(`^undetected registry guests "([^"]*)" and "([^"]*)"$`, st.undetectedGuestsTwo)
	sc.Step(`^an undetected registry guest "([^"]*)"$`, st.undetectedGuest)
	sc.Step(`^a detected guest "([^"]*)" that requires setup$`, st.detectedGuestRequiresSetup)
	sc.Step(`^no picker opens$`, st.noPickerOpens)
	sc.Step(`^the transcript notes "([^"]*)"$`, st.transcriptNotes)
	sc.Step(`^the operator picker is open$`, st.pickerOpen)
	sc.Step(`^the picker rows are "DJ", "([^"]*)", "([^"]*)"$`, st.pickerRowsAre)
	sc.Step(`^the picker rows are "DJ", "([^"]*)" and one suggestion row$`, st.pickerRowsWithSuggestion)
	sc.Step(`^the suggestion row shows an install hint$`, st.suggestionShowsInstallHint)
	sc.Step(`^the user presses down past the last selectable row$`, st.pressDownPastLast)
	sc.Step(`^the cursor stays on "([^"]*)"$`, st.cursorStaysOn)
	sc.Step(`^the cursor is never on the suggestion row$`, st.cursorNeverOnSuggestion)
	sc.Step(`^the user picks "([^"]*)"$`, st.userPicks)
	sc.Step(`^the picker closes$`, st.pickerClosed)
	sc.Step(`^the handoff to "([^"]*)" begins$`, st.handoffBegins)
	sc.Step(`^the transcript shows the setup note for "([^"]*)"$`,
		func(name string) error { return st.transcriptShows(name + " needs") })
	sc.Step(`^no child process is launched$`, st.noChildLaunched)
	sc.Step(`^the transcript notes the DJ keeps the mic$`, func() error { return st.transcriptShows("the DJ keeps the mic") })
	sc.Step(`^the user presses esc$`, st.userPressesEsc)
	sc.Step(`^the user presses "([^"]*)"$`, st.userPresses)
	sc.Step(`^the picker is still open$`, st.pickerStillOpen)
	sc.Step(`^the TUI did not switch modes$`, st.tuiDidNotSwitchModes)
	sc.Step(`^"([^"]*)" appears on PATH$`, st.guestAppearsOnPath)
	sc.Step(`^the picker rows include "([^"]*)"$`, st.pickerRowsInclude)
	sc.Step(`^the transcript shows the install hint for "([^"]*)"$`, st.transcriptShowsInstallHint)
	sc.Step(`^no chat turn is submitted$`, st.noChatTurnSubmitted)
	sc.Step(`^the transcript notes the name is not a known operator$`, st.nameNotKnownOperator)

	// ── handoff_lifecycle.feature (TUI) ────────────────────────────────────────
	sc.Step(`^an AGENT session with a tuned band "([^"]*)" and a live proxy holder$`, st.agentSessionTuned)
	sc.Step(`^a detected guest "([^"]*)"$`, st.detectedGuests)
	sc.Step(`^the proxy holder is disconnected$`, st.holderDisconnected)
	sc.Step(`^the transcript points at tuning in first$`, st.pointsAtTuningIn)
	sc.Step(`^a DJ turn is in flight$`, st.djTurnInFlight)
	sc.Step(`^the transcript notes the DJ is mid-turn$`, st.notesDJMidTurn)
	sc.Step(`^a DJ turn is in flight and a prompt is queued$`, st.djTurnInFlightAndQueued)
	sc.Step(`^the next paint shows "([^"]*)"$`, st.nextPaintShows)
	sc.Step(`^it shows the mic-to line for "([^"]*)"$`, st.paintShowsMicTo)
	sc.Step(`^it shows the on-band line for "([^"]*)"$`, st.paintShowsOnBand)
	sc.Step(`^it shows the wire line "([^"]*)"$`, st.paintShowsWireLine)
	sc.Step(`^only after that paint is the exec command issued$`, st.execOnlyAfterPaint)
	sc.Step(`^the staged paint shows the live proxy BASE URL$`, st.paintShowsBaseURL)
	sc.Step(`^the staged paint shows MODEL "([^"]*)"$`, st.paintShowsModel)
	sc.Step(`^the band was re-tuned since the proxy first bound$`, st.bandRetunedSinceBind)
	sc.Step(`^the child wiring carries the CURRENT band model and endpoint$`, st.childCarriesCurrentOptions)
	sc.Step(`^never the options frozen at first bind$`, st.neverFrozenOptions)
	sc.Step(`^the previous session spent "\$([0-9.]+)" of its budget$`, st.previousSessionSpent)
	sc.Step(`^the holder budget is the default session budget$`, st.holderBudgetIsDefault)
	sc.Step(`^the holder spend reads \$0\.00$`, st.holderSpendZero)
	sc.Step(`^the holder call counter reads 0$`, st.holderCallsZero)
	sc.Step(`^the child env carries the SAME session key the proxy enforces$`, st.childEnvCarriesSameKey)
	sc.Step(`^the key was not rotated by the handoff$`, st.keyNotRotated)
	sc.Step(`^the guest returns after (\d+) minutes, (\d+) calls, and \$([0-9.]+) spend with exit 0$`, st.guestReturnsAfter)
	sc.Step(`^the terminal reset preamble ran: kitty keyboard popped, mouse modes off, bracketed paste exited$`, st.resetPreambleRan)
	sc.Step(`^mouse cell motion is re-enabled because the user has the mouse on$`, st.mouseReenabled)
	sc.Step(`^a balance refresh is requested$`, st.balanceRefreshRequested)
	sc.Step(`^the transcript shows "([^"]*)"$`, st.transcriptShows)
	sc.Step(`^the guest returns with exit (\d+)$`, st.guestReturnsExit)
	sc.Step(`^the summary calls figure equals the holder call counter$`, st.summaryCallsEqualHolder)
	sc.Step(`^the summary spend figure equals the holder Spent\(\)$`, st.summarySpendEqualsHolder)
	sc.Step(`^the user disabled the mouse before the handoff$`, st.userMouseOff)
	sc.Step(`^the defensive reset still disabled the guest's mouse modes$`, st.mouseModesDisabled)
	sc.Step(`^mouse reporting is NOT re-enabled$`, st.mouseNotReenabled)
	sc.Step(`^the note is calm, with no error styling escalation beyond the house red ✕$`, st.noteIsCalm)
	sc.Step(`^the terminal restore and summary still run$`, st.restoreAndSummaryRan)
	sc.Step(`^the terminal reset preamble ran$`, st.resetPreambleRan)
	sc.Step(`^the scratch config was cleaned up$`, st.scratchConfigCleaned)
	sc.Step(`^the guest is killed mid-session$`, st.guestKilled)
	sc.Step(`^the summary line still renders with the accumulated calls and spend$`, st.summaryLineRenders)
	sc.Step(`^the guest binary vanishes between detection and exec$`, st.binaryVanishes)
	sc.Step(`^the TUI is painting again$`, st.tuiPaintingAgain)
	sc.Step(`^the transcript shows an error note naming the launch failure$`, st.errorNoteNamesLaunchFailure)
	sc.Step(`^no scratch dir remains$`, st.noOperatorScratchRemains)
	sc.Step(`^the guest returns with any exit$`, st.guestReturnsAnyExit)
	sc.Step(`^the guest spends up to the session budget during the handoff$`, st.guestSpendsToBudget)
	sc.Step(`^the guest returns$`, st.guestReturnsPlain)
	sc.Step(`^the summary notes the session budget was reached$`, st.summaryNotesBudgetReached)
	sc.Step(`^the summary spend figure is at or just past the default session budget$`, st.summarySpendAtOrPastBudget)
	sc.Step(`^the band goes off-air while the guest has the mic$`, st.bandGoesOffAir)
	sc.Step(`^the summary shows the spend accumulated before the drop$`, st.summaryShowsSpendBeforeDrop)
	sc.Step(`^the desk is fully usable for a re-tune$`, st.deskUsableForRetune)
	sc.Step(`^the guest has the mic$`, st.guestHasTheMic)
	sc.Step(`^no TUI key handling runs until the exec callback returns$`, st.noKeyHandlingDuringExec)
	sc.Step(`^the guest returns after spending "\$([0-9.]+)"$`, st.guestReturnsAfterSpending)
	sc.Step(`^the user immediately runs "/operator opencode" again$`, st.userRunsOperatorAgain)
	sc.Step(`^the second handoff gets a fresh scratch dir$`, st.secondHandoffFreshScratch)
	sc.Step(`^the holder spend reads \$0\.00 again$`, st.holderSpendZeroAgain)
	sc.Step(`^the holder call counter reads 0 again$`, st.holderCallsZeroAgain)
	sc.Step(`^the session key is unchanged$`, st.keyNotRotated)
	sc.Step(`^a detected guest "([^"]*)" as well$`, st.detectedGuests)
	sc.Step(`^the guest "([^"]*)" returns$`, st.namedGuestReturns)
	sc.Step(`^the aider wiring carries no opencode artifacts$`, st.aiderCarriesNoOpencodeArtifacts)
	sc.Step(`^the first session's scratch dir is already gone$`, st.firstScratchGone)

	// ── rc_interlock.feature (TUI + a REAL bridge) ─────────────────────────────
	sc.Step(`^a HOST agent session with an attached remote-control bridge$`, st.hostSessionWithBridge)
	sc.Step(`^a tuned band and a detected guest "([^"]*)"$`, st.tunedBandAndDetectedGuest)
	sc.Step(`^a frame of kind "status" is emitted before the exec$`, st.statusFrameEmittedBeforeExec)
	sc.Step(`^the frame names the operator "([^"]*)"$`, st.frameNamesOperator)
	sc.Step(`^the bridge is parked$`, st.bridgeIsParked)
	sc.Step(`^parking happened before the exec command was returned$`, st.parkedBeforeExecReturned)
	sc.Step(`^a viewer sends a turn "([^"]*)"$`, st.viewerSendsTurn)
	sc.Step(`^the turn is not queued for the DJ$`, st.turnNotQueuedForDJ)
	sc.Step(`^the viewers receive a status frame saying the guest has the mic$`, st.viewersReceiveGuestHasMicStatus)
	sc.Step(`^no agent turn ever fires for "([^"]*)"$`, st.noAgentTurnFires)
	sc.Step(`^viewers sent (\d+) turns during the handoff$`, st.viewersSentNTurns)
	sc.Step(`^the guest returns and the bridge unparks$`, st.guestReturnsAndUnparks)
	sc.Step(`^no queued turn fires$`, st.noQueuedTurnFires)
	sc.Step(`^the DJ receives zero injected turns from the parked window$`, st.djReceivesZeroInjectedTurns)
	sc.Step(`^a viewer sends a confirm approval$`, st.viewerSendsConfirm)
	sc.Step(`^it is dropped$`, st.confirmDropped)
	sc.Step(`^no tool runs$`, st.noToolRuns)
	sc.Step(`^a new viewer attaches and requests backfill$`, st.viewerAttachesRequestsBackfill)
	sc.Step(`^the viewer receives the transcript snapshot$`, st.viewerReceivesTranscriptSnapshot)
	sc.Step(`^the viewer receives a status frame saying the guest has the mic$`, st.viewersReceiveGuestHasMicStatus)
	sc.Step(`^a frame of kind "status" is emitted announcing the DJ is back$`, st.statusFrameDJBack)
	sc.Step(`^a subsequent viewer turn injects into the DJ exactly like local typing$`, st.subsequentTurnInjectsLikeLocal)
	sc.Step(`^the remote-control bridge is not enabled$`, st.bridgeNotEnabled)
	sc.Step(`^no frame is emitted$`, st.noFrameEmitted)
	sc.Step(`^the handoff proceeds normally$`, st.handoffProceedsNormally)
	sc.Step(`^the remote session is revoked during the handoff$`, st.sessionRevokedDuringHandoff)
	sc.Step(`^the unpark is a no-op$`, st.unparkIsNoOp)
	sc.Step(`^the desk summary still renders$`, st.deskSummaryStillRenders)
	sc.Step(`^the protocol reserves frame kind "([^"]*)"$`, st.protocolReservesFrameKind)
	sc.Step(`^the protocol reserves inbound kind "([^"]*)"$`, st.protocolReservesInboundKind)
	sc.Step(`^v1 attaches no behavior to any of them$`, st.reservedKindsNoBehavior)
	sc.Step(`^the handoff to "([^"]*)" begins and the guest works for a while$`, st.handoffBeginsAndGuestWorks)
	sc.Step(`^no frame carries any guest terminal output$`, st.noFrameCarriesGuestOutput)
	sc.Step(`^a desktop remote-control viewer attached to a live session$`, st.viewerAttached)
	sc.Step(`^a status frame announces the guest "([^"]*)" has the mic$`, st.viewerReceivesOperatorStatus)
	sc.Step(`^a status frame announces the DJ is back at the desk$`, st.viewerReceivesDJBack)
	sc.Step(`^the viewer transcript shows "([^"]*)"$`, st.viewerTranscriptShows)

	// ── iteration-1 fix-pass regressions (2026-07) ─────────────────────────────
	sc.Step(`^a viewer sends a turn "([^"]*)" that the busy host queues$`, st.viewerTurnQueuedWhileBusy)
	sc.Step(`^the DJ turn finishes and the queue drains$`, st.djTurnFinishesQueueDrains)
	sc.Step(`^no handoff is staged and no child process was launched$`, st.noHandoffStagedNoChild)
	sc.Step(`^"([^"]*)" was answered as a chat turn$`, st.answeredAsChatTurn)
	sc.Step(`^the handoff to "([^"]*)" is staged but not yet execed$`, st.handoffStagedNotExeced)
	sc.Step(`^a DJ turn slips in and the staging beat elapses$`, st.djSlipsInStagingElapses)
	sc.Step(`^the handoff is aborted with no child process launched$`, st.handoffAbortedNoChild)
	sc.Step(`^the band drops during the staging beat$`, st.bandDropsDuringStaging)
	sc.Step(`^the staging beat elapses$`, st.stagingBeatElapses)
	sc.Step(`^bracketed paste is re-enabled in the return command set$`, st.bracketedPasteReenabled)
	sc.Step(`^a detected guest "([^"]*)" whose version probe failed$`, st.detectedGuestUnverified)

	// ── Phase 3: THE DESK view · band gate · pre-launch plate ─────────────────
	initializePhase3Steps(st, sc)

	// ── AGENT [0] desk-entry redesign: focus, auto-tune, badges ───────────────
	initializeDeskEntryScenarios(st, sc)

	// ── operator frame enrichment (rc_enrichment.feature) ─────────────────────
	initializeEnrichmentSteps(st, sc)

	// ── tool-call capability probe: VERIFIED vs INFERRED agent-ready ──────────
	initializeAgentReadyVerifiedSteps(st, sc)
}
