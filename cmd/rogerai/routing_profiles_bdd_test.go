package main

// routing_profiles_bdd_test.go makes features/cli/routing_flags.feature and
// features/cli/profiles.feature EXECUTABLE against the REAL stack, reusing the harness of
// routing_flags_bdd_test.go (rfState): the REAL broker binary as a subprocess, REAL stations
// (internal/agent.Start) serving from httptest "local model servers", the REAL `roger` binary
// exec'd per command with a temp XDG_CONFIG_HOME, and the transparent recorder in front of the
// broker that logs every proxy -> broker chat hop.
//
// Additions over rfState, each stated here so nothing is hidden:
//   - a FRONT recorder between `roger` and the rf recorder: it logs EVERY request the CLI
//     makes (paths, bodies), so "no request to /account or /me carries the profile" and "no
//     request is retried to negotiate" are observable; it can answer GET /v1/models with 404
//     to play an old broker (the negotiation probe's positive signal, contract §9). It forwards
//     everything else untouched; it is a recorder, not a mock.
//   - ROGERAI_NO_UPDATE_CHECK=1 in roger's env: the cached "update available" notice is
//     unrelated output that would make every "stderr is exactly one line" step meaningless.
//   - "the tune-time body" is the body of the FIRST chat hop after the command: the step sends
//     one chat through the local endpoint (when `roger use` is still running) and reads what the
//     broker received. A refused command (exit != 0) has no tune-time body and the step fails
//     naming the exit and stderr.
//   - profile resolution is observed through the CLI that will own it (contract §9: profiles
//     are resolved by the first-party client): "profile p is resolved" runs
//     `roger use @profile/p --yes` and reads the tune-time body; "merged with the request" and
//     every guest step POST a guest body to the running proxy and read what reached the broker.
//   - "the reasoning fallback is off" starts an extra station whose model answers with
//     reasoning_content only, refuses the original station for one request so the broker fails
//     over to it, and checks the guest received the raw (empty) content.
//
// No mocks: the broker, stations, proxy and CLI are production code paths.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/client"
)

// ── front recorder ─────────────────────────────────────────────────────────────────────

type cf4Hit struct {
	method, path string
	body         []byte
}

type cf4State struct {
	*rfState

	front     *httptest.Server
	hitsMu    sync.Mutex
	hits      []cf4Hit
	models404 bool

	stationModels map[string]bool

	asserted  []string // body keys asserted by "the tune-time body carries" in this scenario
	tuneBody  map[string]any
	tuneRaw   []byte
	tuneHdr   http.Header
	bandCode  string // the one band code the front answers /bands/resolve for
	bandModel string

	resolved    map[string]any // "the body object"
	resolvedRaw []byte

	requestKeys map[string]any // "the request sets ..."
	requestBody string         // "the request body is ..." / "the request is ..."

	guestCode int
	guestBody []byte
	guestHdr  http.Header
	hopsMark  int

	routingLine string
	outputs     []string // "runs each of"

	cfgBaseline    []byte
	cfgBaseMod     time.Time
	cfgInode       uint64
	profilesSnap   map[string]any
	lastSetPath    string
	lastSetValue   any
	goodPref       string
	lastNaming     []string
	lastExitWasUse bool
}

func (s *cf4State) reset(t *testing.T) {
	s.rfState = &rfState{}
	s.rfState.reset(t)
	s.front, s.hits, s.models404 = nil, nil, false
	s.stationModels = map[string]bool{}
	s.asserted, s.tuneBody, s.tuneRaw = nil, nil, nil
	s.resolved, s.resolvedRaw = nil, nil
	s.requestKeys, s.requestBody = map[string]any{}, ""
	s.guestCode, s.guestBody, s.guestHdr, s.hopsMark = 0, nil, nil, 0
	s.routingLine, s.outputs = "", nil
	s.cfgBaseline, s.cfgInode, s.profilesSnap = nil, 0, nil
	s.lastSetPath, s.lastSetValue, s.goodPref, s.lastNaming = "", nil, "", nil
}

// configUnchanged / noRequestReachedBroker shadow the rfState methods so a step registered
// as s.configUnchanged resolves s.rfState when it RUNS: reset installs a fresh rfState per
// scenario, and a promoted method value would stay bound to the first one.
func (s *cf4State) configUnchanged() error        { return s.rfState.configUnchanged() }
func (s *cf4State) noRequestReachedBroker() error { return s.rfState.noRequestReachedBroker() }

func (s *cf4State) teardown() {
	s.rfState.teardown()
	if s.front != nil {
		s.front.Close()
	}
}

// ensure starts the broker (rfState) and the front recorder, and points config.json at the
// front.
func (s *cf4State) ensure() error {
	if err := s.ensureBroker(); err != nil {
		return err
	}
	if s.front != nil {
		return nil
	}
	target, _ := url.Parse(s.rec.srv.URL)
	rp := httputil.NewSingleHostReverseProxy(target)
	s.front = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		s.hitsMu.Lock()
		s.hits = append(s.hits, cf4Hit{method: r.Method, path: r.URL.Path, body: b})
		m404 := s.models404
		code, bandModel := s.bandCode, s.bandModel
		s.hitsMu.Unlock()
		if m404 && r.URL.Path == "/v1/models" {
			http.NotFound(w, r)
			return
		}
		// A private band needs a GitHub-linked owner, which a subprocess harness cannot log in
		// as; so the band LOOKUP for the one scripted code is answered here (same precedent as
		// the /v1/models 404 above). The chat itself, and the header under test, still go to the
		// real broker through the recorder.
		if code != "" && r.URL.Path == "/bands/resolve" {
			var q struct {
				Freq string `json:"freq"`
			}
			_ = json.Unmarshal(b, &q)
			if q.Freq == code {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"offers":[{"node_id":"n-band","model":%q,"price_in":0,"price_out":0,"online":true}],"band":{"display":"private band"}}`, bandModel)
				return
			}
		}
		rp.ServeHTTP(w, r)
	}))
	m := s.cfgRaw()
	m["broker"] = s.front.URL
	return s.writeCfg(m)
}

func (s *cf4State) writeCfg(m map[string]any) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.MkdirAll(filepath.Dir(s.cfgPath()), 0700)
	return os.WriteFile(s.cfgPath(), b, 0600)
}

func (s *cf4State) cfgMap() map[string]any {
	b, err := os.ReadFile(s.cfgPath())
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func (s *cf4State) frontHits() []cf4Hit {
	s.hitsMu.Lock()
	defer s.hitsMu.Unlock()
	return append([]cf4Hit(nil), s.hits...)
}

// ── values ────────────────────────────────────────────────────────────────────────────

var cf4BareKey = regexp.MustCompile(`([{,]\s*)([A-Za-z_][A-Za-z0-9_]*)\s*:`)

// cf4Value parses a step value: JSON first, then the spec's relaxed object form
// ({max_out: 3}), else the raw text as a string.
func cf4Value(text string) any {
	text = strings.TrimSpace(text)
	var v any
	if json.Unmarshal([]byte(text), &v) == nil {
		return v
	}
	relaxed := cf4BareKey.ReplaceAllString(text, `$1"$2":`)
	if json.Unmarshal([]byte(relaxed), &v) == nil {
		return v
	}
	return text
}

func cf4Canon(v any) string {
	b, _ := json.Marshal(v)
	var back any
	_ = json.Unmarshal(b, &back)
	c, _ := json.Marshal(back)
	return string(c)
}

func cf4Eq(got, want any) bool { return cf4Canon(got) == cf4Canon(want) }

// cf4Clauses splits "a = 1, b = [2, 3] and no c key" into clauses at separators that are
// followed by the start of a clause (a dotted key or "no "), never inside a value.
var cf4ClauseStart = regexp.MustCompile(`^(no |[a-z_][a-z0-9_.]*( =| "| \[| \{| true| false| -?[0-9]|$))`)

func cf4Clauses(text string) []string {
	text = strings.TrimSpace(text)
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		for _, sep := range []string{", and ", ", ", " and "} {
			if strings.HasPrefix(text[i:], sep) && cf4ClauseStart.MatchString(text[i+len(sep):]) {
				out = append(out, strings.TrimSpace(text[start:i]))
				start = i + len(sep)
				i = start - 1
				break
			}
		}
	}
	out = append(out, strings.TrimSpace(text[start:]))
	return out
}

var (
	cf4ParenTail  = regexp.MustCompile(`\s*\([^()]*\)\s*$`)
	cf4NoAnywhere = regexp.MustCompile(`^no "([^"]+)"( anywhere| string in any field)$`)
	cf4NoKey      = regexp.MustCompile(`^no ([a-z_][a-z0-9_.]*)( key)?$`)
	cf4Assign     = regexp.MustCompile(`^([a-z_][a-z0-9_.]*) = (.+)$`)
	cf4Bare       = regexp.MustCompile(`^([a-z_][a-z0-9_.]*) ("|\[|\{|true|false|-?[0-9])(.*)$`)
)

// cf4Check evaluates clauses against a body.
func cf4Check(body map[string]any, raw []byte, text string) error {
	text = cf4ParenTail.ReplaceAllString(strings.TrimSpace(text), "") // a trailing "(note)" is commentary
	for _, c := range cf4Clauses(text) {
		c = cf4ParenTail.ReplaceAllString(c, "")
		if c == "" {
			continue
		}
		if m := cf4NoAnywhere.FindStringSubmatch(c); m != nil {
			if bytes.Contains(raw, []byte(m[1])) {
				return fmt.Errorf("%q appears in the body: %s", m[1], raw)
			}
			continue
		}
		if m := cf4NoKey.FindStringSubmatch(c); m != nil {
			if v, ok := rfWalk(body, m[1]); ok {
				return fmt.Errorf("body carries %s = %s, want absent (body %s)", m[1], cf4Canon(v), raw)
			}
			continue
		}
		var path, val string
		if m := cf4Assign.FindStringSubmatch(c); m != nil {
			path, val = m[1], m[2]
		} else if m := cf4Bare.FindStringSubmatch(c); m != nil {
			path, val = m[1], m[2]+m[3]
		} else {
			return fmt.Errorf("cannot read clause %q", c)
		}
		want := cf4Value(val)
		got, ok := rfWalk(body, path)
		if !ok {
			return fmt.Errorf("body carries no %s (want %s); body %s", path, cf4Canon(want), raw)
		}
		if !cf4Eq(got, want) {
			return fmt.Errorf("body %s = %s, want %s; body %s", path, cf4Canon(got), cf4Canon(want), raw)
		}
	}
	return nil
}

// ── command line ───────────────────────────────────────────────────────────────────────

// cf4Split is a shell-ish split honoring double and single quotes; inside single quotes a
// backslash-escaped quote (the spec writes '[\"n1\"]') is unescaped.
func cf4Split(line string) []string {
	var out []string
	var cur strings.Builder
	had := false
	q := rune(0)
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case q != 0 && r == '\\' && i+1 < len(rs) && rs[i+1] == '"':
			cur.WriteRune('"')
			i++
		case q != 0 && r == q:
			q = 0
		case q == 0 && (r == '"' || r == '\''):
			q, had = r, true
		case q == 0 && r == ' ':
			if cur.Len() > 0 || had {
				out = append(out, cur.String())
				cur.Reset()
				had = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 || had {
		out = append(out, cur.String())
	}
	return out
}

func cf4Expand(line string) string {
	if strings.Contains(line, "<33 comma-separated ids>") {
		var ids []string
		for i := 1; i <= 33; i++ {
			ids = append(ids, fmt.Sprintf("n%d", i))
		}
		line = strings.ReplaceAll(line, "<33 comma-separated ids>", strings.Join(ids, ","))
	}
	return line
}

func (s *cf4State) rogerEnv() []string {
	env := append(s.rfState.rogerEnv(), "ROGERAI_NO_UPDATE_CHECK=1")
	if s.front != nil {
		// A config.json that degrades to defaults must still reach the test broker, never
		// production (roger reads ROGER_BROKER over the config, main.go:305).
		env = append(env, "ROGER_BROKER="+s.front.URL)
	}
	return env
}

func cf4IsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

func cf4HasYes(args []string) bool {
	for _, a := range args {
		if a == "--yes" || a == "-yes" {
			return true
		}
	}
	return false
}

var cf4Sugar = regexp.MustCompile(`(:(free|floor|nitro))+$`)

// stationModelFor names the model a `roger use` invocation will tune: the positional with
// its sugar stripped, a profile's model/models[0], else qwen3-32b.
func (s *cf4State) stationModelFor(args []string) string {
	pos := ""
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		pos = args[1]
	}
	prof := ""
	for i, a := range args {
		if (a == "--profile" || a == "-profile") && i+1 < len(args) {
			prof = args[i+1]
		}
	}
	if strings.HasPrefix(pos, "@profile/") {
		prof, pos = strings.TrimPrefix(pos, "@profile/"), ""
	}
	if pos == "" && prof != "" {
		if p, ok := rfWalk(s.cfgMap(), "profiles."+prof); ok {
			pm, _ := p.(map[string]any)
			if m, ok := pm["model"].(string); ok && m != "" {
				pos = m
			} else if l, ok := pm["models"].([]any); ok && len(l) > 0 {
				pos, _ = l[0].(string)
			}
		}
	}
	if pos == "" {
		pos = "qwen3-32b"
	}
	return cf4Sugar.ReplaceAllString(pos, "")
}

func (s *cf4State) ensureStationFor(model string) error {
	if s.stationModels[model] {
		return nil
	}
	s.stationN++
	if _, err := s.startStation(fmt.Sprintf("n-cf4-%d", s.stationN), model, "", 0); err != nil {
		return err
	}
	s.stationModels[model] = true
	return nil
}

func (s *cf4State) snapshotCfg() {
	if b, err := os.ReadFile(s.cfgPath()); err == nil {
		s.cfgBefore = b
		if fi, err := os.Stat(s.cfgPath()); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				s.cfgInode = st.Ino
			}
		}
	}
	if p, ok := rfWalk(s.cfgMap(), "profiles"); ok {
		s.profilesSnap, _ = p.(map[string]any)
	} else {
		s.profilesSnap = nil
	}
}

// runLine runs one `roger ...` command line.
func (s *cf4State) runLine(line string) error {
	args := cf4Split(cf4Expand(line))
	if len(args) == 0 || args[0] != "roger" {
		return fmt.Errorf("expected a `roger ...` command, got %q", line)
	}
	args = args[1:]
	if err := s.ensure(); err != nil {
		return err
	}
	s.snapshotCfg()
	s.commandRan = true
	s.tuneBody, s.tuneRaw = nil, nil
	if len(args) > 0 && (args[0] == "use" || args[0] == "connect" || args[0] == "tune") && !cf4IsHelp(args) && cf4HasYes(args) {
		return s.startUseCF4(args)
	}
	s.stopUse()
	s.lastExitWasUse = false
	if len(args) > 0 && args[0] == "use" && !cf4IsHelp(args) {
		if err := s.ensureStationFor(s.stationModelFor(args)); err != nil {
			return err
		}
	}
	s.runTimed(args...)
	return nil
}

// runTimed execs a roger command with empty stdin and a bound, capturing exit and output.
func (s *cf4State) runTimed(args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rfRogerBin, args...)
	cmd.Env = s.rogerEnv()
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	s.lastOut, s.lastErr, s.lastCode = so.String(), se.String(), 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			s.lastCode = ee.ExitCode()
		} else {
			s.lastCode = -1
		}
	}
}

// startUseCF4 is rfState.startUse plus: an explicit --port is kept (the scenario names it),
// the station is for the model the invocation tunes, and the env has the update check off.
func (s *cf4State) startUseCF4(args []string) error {
	if err := s.ensureStationFor(s.stationModelFor(args)); err != nil {
		return err
	}
	s.stopUse()
	full := append([]string{}, args...)
	port := 0
	for i, a := range args {
		if (a == "--port" || a == "-port") && i+1 < len(args) {
			port, _ = strconv.Atoi(args[i+1])
		}
	}
	if port == 0 {
		port = rfFreePort(s.t)
		full = append(full, "--port", strconv.Itoa(port))
	}
	s.proxyPort = port
	cmd := exec.Command(rfRogerBin, full...)
	cmd.Env = s.rogerEnv()
	s.useOut, s.useErr = &rfBuf{}, &rfBuf{}
	cmd.Stdout, cmd.Stderr = s.useOut, s.useErr
	if err := cmd.Start(); err != nil {
		return err
	}
	s.useCmd = cmd
	s.lastExitWasUse = true
	s.useExited = make(chan struct{})
	go func() {
		s.useExit = cmd.Wait()
		close(s.useExited)
	}()
	s.liveBefore = s.adminLive()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-s.useExited:
			s.snapshotUse()
			return nil
		default:
		}
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.proxyPort), 100*time.Millisecond); err == nil {
			c.Close()
			if m := regexp.MustCompile(`API KEY\s+(\S+)`).FindStringSubmatch(s.useOut.String()); m != nil {
				s.sessionKey = m[1]
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("roger use neither opened the channel nor exited in time\nstdout:\n%s\nstderr:\n%s", s.useOut.String(), s.useErr.String())
}

func (s *cf4State) useRunning() bool {
	if s.useCmd == nil {
		return false
	}
	select {
	case <-s.useExited:
		s.snapshotUse()
		return false
	default:
		return true
	}
}

// output of the last command (a running `roger use` included).
func (s *cf4State) out() string {
	if s.lastExitWasUse && s.useOut != nil {
		if s.useRunning() {
			return s.useOut.String()
		}
	}
	return s.lastOut
}

func (s *cf4State) errOut() string {
	if s.lastExitWasUse && s.useErr != nil {
		if s.useRunning() {
			return s.useErr.String()
		}
	}
	return s.lastErr
}

func cf4Lines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, " \r"))
		}
	}
	return out
}

func cf4Quoted(text string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// ── the tune-time body ─────────────────────────────────────────────────────────────────

func (s *cf4State) tuneTimeBody() (map[string]any, []byte, error) {
	if s.tuneBody != nil {
		return s.tuneBody, s.tuneRaw, nil
	}
	if !s.useRunning() {
		return nil, nil, fmt.Errorf("no tune-time body: `roger use` is not running (exit %d)\nstdout:\n%s\nstderr:\n%s", s.lastCode, s.lastOut, s.lastErr)
	}
	before := len(s.rec.chatHops())
	if err := s.sendChat(); err != nil {
		return nil, nil, err
	}
	hops := s.rec.chatHops()
	if len(hops) == before {
		r := s.respBodys[len(s.respBodys)-1]
		return nil, nil, fmt.Errorf("the chat reached no broker (proxy answered %d: %s)", s.responses[len(s.responses)-1].StatusCode, r)
	}
	h := hops[before] // the FIRST hop of this chat; a later one is the proxy's failover retry
	var m map[string]any
	if err := json.Unmarshal(h.body, &m); err != nil {
		return nil, nil, fmt.Errorf("broker-bound body is not JSON: %v", err)
	}
	s.tuneBody, s.tuneRaw, s.tuneHdr = m, h.body, h.header
	return m, h.body, nil
}

func (s *cf4State) tuneCarries(key, val string) error {
	m, raw, err := s.tuneTimeBody()
	if err != nil {
		return err
	}
	s.asserted = append(s.asserted, key)
	return cf4Check(m, raw, key+" = "+val)
}

func (s *cf4State) tuneCarriesNo(key string) error {
	m, raw, err := s.tuneTimeBody()
	if err != nil {
		return err
	}
	return cf4Check(m, raw, "no "+key)
}

// routingLeaves lists the dotted leaves of the routing carriers in a body.
func cf4RoutingLeaves(m map[string]any) []string {
	var out []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if mm, ok := v.(map[string]any); ok && len(mm) > 0 {
			for k, vv := range mm {
				walk(prefix+"."+k, vv)
			}
			return
		}
		out = append(out, prefix)
	}
	for _, k := range []string{"models", "provider", "roger"} {
		if v, ok := m[k]; ok {
			walk(k, v)
		}
	}
	return out
}

func (s *cf4State) onlyRoutingKeys(extra ...string) error {
	m, raw, err := s.tuneTimeBody()
	if err != nil {
		return err
	}
	allowed := append(append([]string{"provider.max_price.completion"}, extra...), s.asserted...)
	for _, leaf := range cf4RoutingLeaves(m) {
		ok := false
		for _, a := range allowed {
			if leaf == a || strings.HasPrefix(leaf, a+".") {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("the body carries routing key %s beyond the allowed %v: %s", leaf, allowed, raw)
		}
	}
	return nil
}

func (s *cf4State) noOtherKeysAsserted() error { return s.onlyRoutingKeys() }

func (s *cf4State) noOtherKeysAtAll() error {
	s.asserted = nil
	return s.onlyRoutingKeys()
}

// ── Background / Given ─────────────────────────────────────────────────────────────────

func (s *cf4State) givenConfig() error { return s.ensure() }

func (s *cf4State) ceilingIs(v string) error {
	if s.brokerURL != "" && s.ceiling != v {
		return fmt.Errorf("the broker is already running with ceiling %s, want %s", s.ceiling, v)
	}
	s.ceiling = v
	return nil
}

func (s *cf4State) setPath(path string, v any) error {
	if err := s.ensure(); err != nil {
		return err
	}
	m := s.cfgMap()
	rfSet(m, path, v)
	return s.writeCfg(m)
}

func (s *cf4State) limitIs(path, val string) error {
	return s.setPath("limits."+path, cf4Value(val))
}

func (s *cf4State) oldConfig(path, val string) error {
	if err := s.setPath("limits."+path, cf4Value(val)); err != nil {
		return err
	}
	b, _ := os.ReadFile(s.cfgPath())
	s.cfgBaseline = b
	if fi, err := os.Stat(s.cfgPath()); err == nil {
		s.cfgBaseMod = fi.ModTime()
	}
	return nil
}

var cf4AssignSplit = regexp.MustCompile(` and ([a-z_][a-z0-9_.]* = )`)

func (s *cf4State) setProfileAssignments(name, text string) error {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "only ")
	parts := cf4AssignSplit.Split(text, -1)
	keys := cf4AssignSplit.FindAllStringSubmatch(text, -1)
	assigns := []string{parts[0]}
	for i, k := range keys {
		assigns = append(assigns, k[1]+parts[i+1])
	}
	for _, a := range assigns {
		m := cf4Assign.FindStringSubmatch(strings.TrimSpace(a))
		if m == nil {
			return fmt.Errorf("cannot read profile assignment %q", a)
		}
		if err := s.setPath("profiles."+name+"."+m[1], cf4Value(m[2])); err != nil {
			return err
		}
	}
	return nil
}

func (s *cf4State) profileSets(name, text string) error { return s.setProfileAssignments(name, text) }

func (s *cf4State) profileIs(name, obj string) error {
	return s.setPath("profiles."+name, cf4Value(obj))
}

// profileContains merges JSON fragments ("max_tokens": 512 and "system": "be brief").
func (s *cf4State) profileContains(name, frag string) error {
	var obj map[string]any
	if json.Unmarshal([]byte("{"+frag+"}"), &obj) != nil {
		obj = map[string]any{}
		for _, f := range strings.Split(frag, " and ") {
			var part map[string]any
			if err := json.Unmarshal([]byte("{"+strings.ReplaceAll(f, "…", "")+"}"), &part); err != nil {
				return fmt.Errorf("cannot read profile fragment %q: %v", f, err)
			}
			for k, v := range part {
				obj[k] = v
			}
		}
	}
	m := s.cfgMap()
	p, _ := rfWalk(m, "profiles."+name)
	pm, _ := p.(map[string]any)
	if pm == nil {
		pm = map[string]any{}
	}
	for k, v := range obj {
		pm[k] = v
	}
	rfSet(m, "profiles."+name, pm)
	return s.writeCfg(m)
}

func (s *cf4State) configContainsDoc(doc *godog.DocString) error {
	if err := s.ensure(); err != nil {
		return err
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(doc.Content), &extra); err != nil {
		return err
	}
	m := s.cfgMap()
	for k, v := range extra {
		m[k] = v
	}
	return s.writeCfg(m)
}

func (s *cf4State) configProfilesRaw(val string) error {
	return s.setPath("profiles", cf4Value(val))
}

func (s *cf4State) configTruncated() error {
	if err := s.ensure(); err != nil {
		return err
	}
	// Keep the test broker reachable on the defaults path: ROGER_BROKER points at the front
	// (roger reads it, main.go:305), so a fallback to defaults never reaches production.
	b, _ := os.ReadFile(s.cfgPath())
	if len(b) > 20 {
		b = b[:len(b)/2]
	}
	return os.WriteFile(s.cfgPath(), b, 0600)
}

func cf4ExpandName(name string) string {
	if name == `""` {
		return ""
	}
	if strings.HasPrefix(name, "65-char-name") {
		return strings.Repeat("a", 65)
	}
	return name
}

func (s *cf4State) definesProfile(name string) error {
	name = cf4ExpandName(name)
	if err := s.ensure(); err != nil {
		return err
	}
	m := s.cfgMap()
	profs, _ := m["profiles"].(map[string]any)
	if profs == nil {
		profs = map[string]any{}
	}
	profs[name] = map[string]any{"roger": map[string]any{"pref": "fast"}}
	m["profiles"] = profs
	return s.writeCfg(m)
}

func (s *cf4State) profilesExist(a, b string) error {
	if err := s.profileIs(a, `{"roger": {"pref": "fast"}}`); err != nil {
		return err
	}
	return s.profileIs(b, `{"roger": {"pref": "cheap"}}`)
}

func (s *cf4State) profileExists(name string) error {
	return s.profileIs(name, `{"roger": {"pref": "fast"}}`)
}

func (s *cf4State) noProfileNamed(name string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	m := s.cfgMap()
	if profs, ok := m["profiles"].(map[string]any); ok {
		delete(profs, name)
	}
	return s.writeCfg(m)
}

func (s *cf4State) codingAndCheap() error {
	if err := s.profileIs("coding", `{"provider": {"sort": "throughput", "only": ["n1","n2"]}, "roger": {"require": ["tools"]}}`); err != nil {
		return err
	}
	return s.profileIs("cheap", `{"roger": {"pref": "cheap"}}`)
}

func (s *cf4State) configCarriesOthers() error {
	if err := s.ensure(); err != nil {
		return err
	}
	m := s.cfgMap()
	// The config struct's real types (main.go config): a wrong type would make the loader
	// treat the file as corrupt, which is not what this scenario is about.
	m["share"] = map[string]any{"model": "qwen3-32b", "port": 8080}
	m["share_prices"] = map[string]any{"qwen3-32b": map[string]any{"price_out": 0.4}}
	m["share_voices"] = map[string]any{"voice": map[string]any{}}
	m["palette"] = "mono"
	m["agent_perms"] = "confirm"
	return s.writeCfg(m)
}

func (s *cf4State) requestSets(key, val string) error {
	s.requestKeys[key] = cf4Value(val)
	return nil
}

const cf4Messages = `[{"role":"user","content":"say roger"}]`

func (s *cf4State) requestIs(body string) error {
	s.requestBody = strings.ReplaceAll(body, "[...]", cf4Messages)
	return nil
}

// ── the proxy as resolver / guest ──────────────────────────────────────────────────────

func (s *cf4State) ensureProxy(model string) error {
	if s.useRunning() {
		return nil
	}
	return s.runLine("roger use " + model + " --yes")
}

// guestSend POSTs a guest body to the running proxy and records the guest response and,
// when it reached the broker, the broker-bound body as "the body object".
func (s *cf4State) guestSend(body string) error {
	if !s.useRunning() {
		if err := s.ensureProxy("qwen3-32b"); err != nil {
			return err
		}
		if !s.useRunning() {
			return fmt.Errorf("roger use exited (code %d): %s%s", s.lastCode, s.lastOut, s.lastErr)
		}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return fmt.Errorf("bad guest body %q: %v", body, err)
	}
	if _, ok := obj["messages"]; !ok {
		var msgs any
		_ = json.Unmarshal([]byte(cf4Messages), &msgs)
		obj["messages"] = msgs
	}
	b, _ := json.Marshal(obj)
	s.hopsMark = len(s.rec.chatHops())
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", s.proxyPort), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.sessionKey)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s.guestCode, s.guestBody, s.guestHdr = resp.StatusCode, rb, resp.Header
	hops := s.rec.chatHops()
	s.resolved, s.resolvedRaw = nil, nil
	if len(hops) > s.hopsMark {
		h := hops[len(hops)-1]
		_ = json.Unmarshal(h.body, &s.resolved)
		s.resolvedRaw = h.body
	}
	return nil
}

func (s *cf4State) guestSends(body string) error {
	return s.guestSend(strings.ReplaceAll(body, "[...]", cf4Messages))
}

func (s *cf4State) mergedWithRequest(name string) error {
	if err := s.ensureProxy("qwen3-32b"); err != nil {
		return err
	}
	obj := map[string]any{"model": "@profile/" + name}
	for k, v := range s.requestKeys {
		rfSet(obj, k, v)
	}
	b, _ := json.Marshal(obj)
	return s.guestSend(string(b))
}

func (s *cf4State) resolveByProxy() error {
	if err := s.ensureProxy("qwen3-32b"); err != nil {
		return err
	}
	return s.guestSend(s.requestBody)
}

func (s *cf4State) requestNames(model string) error {
	if err := s.ensureProxy("qwen3-32b"); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]any{"model": model})
	return s.guestSend(string(b))
}

func (s *cf4State) proxyTunedTo(model string) error {
	return s.runLine("roger use " + model + " --yes")
}

func (s *cf4State) liveProxyBand(model string) error { return s.proxyTunedTo(model) }

func (s *cf4State) proxyTunedWith(flags string) error {
	return s.runLine("roger use qwen3-32b " + flags + " --yes")
}

func (s *cf4State) proxyIsLive() error { return s.ensureProxy("qwen3-32b") }

func (s *cf4State) brokerReceives(text string) error {
	if strings.HasPrefix(text, "the last good ") {
		return s.brokerReceivesLastGood()
	}
	if m := regexp.MustCompile(`^the X-Roger-Freq header for "([^"]+)"$`).FindStringSubmatch(text); m != nil {
		return s.brokerFreqHeader(m[1])
	}
	if text == "the tuned band's model, as for any carrier-less foreign id" {
		return s.brokerTunedModel()
	}
	if s.resolved == nil {
		return fmt.Errorf("nothing reached the broker (guest got %d: %s)", s.guestCode, s.guestBody)
	}
	return cf4Check(s.resolved, s.resolvedRaw, text)
}

// guestModelsResolved: a guest names a profile and its own models[] (corrected 2026-10-04).
func (s *cf4State) guestModelsResolved(model, models string) error {
	b, _ := json.Marshal(map[string]any{"model": model, "models": cf4Value(models)})
	return s.guestSend(string(b))
}

// guestFreqResolved: a guest names a profile and its own roger.freq.
func (s *cf4State) guestFreqResolved(model, freq string) error {
	b, _ := json.Marshal(map[string]any{"model": model, "roger": map[string]any{"freq": freq}})
	return s.guestSend(string(b))
}

func (s *cf4State) guestLocal400Code(code string) error {
	if s.guestCode != http.StatusBadRequest {
		return fmt.Errorf("guest status %d, want a local 400: %s", s.guestCode, s.guestBody)
	}
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if json.Unmarshal(s.guestBody, &e) != nil || e.Error.Message == "" {
		return fmt.Errorf("guest 400 is not OpenAI-shaped: %s", s.guestBody)
	}
	if e.Error.Code != code {
		return fmt.Errorf("guest 400 error.code %q, want %q: %s", e.Error.Code, code, s.guestBody)
	}
	return nil
}

// lastHopSinceGuest is the broker-bound hop the last guest request produced.
func (s *cf4State) lastHopSinceGuest() (rfHop, error) {
	hops := s.rec.chatHops()
	if len(hops) <= s.hopsMark {
		return rfHop{}, fmt.Errorf("nothing reached the broker (guest got %d: %s)", s.guestCode, s.guestBody)
	}
	return hops[len(hops)-1], nil
}

func (s *cf4State) brokerFreqHeader(want string) error {
	h, err := s.lastHopSinceGuest()
	if err != nil {
		return err
	}
	if got := h.header.Get("X-Roger-Freq"); got != want {
		return fmt.Errorf("X-Roger-Freq = %q, want %q", got, want)
	}
	return nil
}

func (s *cf4State) bodyNoFreq() error {
	h, err := s.lastHopSinceGuest()
	if err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal(h.body, &m)
	if _, ok := rfWalk(m, "roger.freq"); ok {
		return fmt.Errorf("the broker-bound body carries roger.freq: %s", h.body)
	}
	return nil
}

func (s *cf4State) notResolvedAsProfile() error {
	if s.guestCode >= 400 && strings.Contains(string(s.guestBody), "profile") {
		return fmt.Errorf("the proxy treated it as a profile reference: %d %s", s.guestCode, s.guestBody)
	}
	return nil
}

func (s *cf4State) brokerTunedModel() error {
	h, err := s.lastHopSinceGuest()
	if err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal(h.body, &m)
	if got, _ := m["model"].(string); got != "qwen3-32b" {
		return fmt.Errorf("the broker received model %q, want the tuned band's \"qwen3-32b\": %s", got, h.body)
	}
	return nil
}

func (s *cf4State) brokerReceivesLastGood() error {
	return s.brokerReceives(`roger.pref = "` + s.goodPref + `"`)
}

func (s *cf4State) treatedAsPlain() error {
	if s.resolved == nil {
		return fmt.Errorf("nothing reached the broker (guest got %d: %s)", s.guestCode, s.guestBody)
	}
	if got, _ := s.resolved["model"].(string); got != "@Profile/coding" {
		return fmt.Errorf("the broker received model %q: the proxy did not treat \"@Profile/coding\" as a plain model id", got)
	}
	return nil
}

func (s *cf4State) guest400(msg string) error {
	if s.guestCode != http.StatusBadRequest {
		return fmt.Errorf("guest status %d, want 400: %s", s.guestCode, s.guestBody)
	}
	var e struct {
		Error struct {
			Type, Message string
		} `json:"error"`
	}
	if json.Unmarshal(s.guestBody, &e) != nil || e.Error.Message == "" {
		return fmt.Errorf("guest 400 is not OpenAI-shaped: %s", s.guestBody)
	}
	if !strings.Contains(e.Error.Message, msg) {
		return fmt.Errorf("guest 400 message %q lacks %q", e.Error.Message, msg)
	}
	return nil
}

func (s *cf4State) nothingReached() error {
	if n := len(s.rec.chatHops()) - s.hopsMark; n != 0 {
		return fmt.Errorf("%d chat request(s) reached the broker, want none", n)
	}
	return nil
}

func (s *cf4State) resolutionFails(a, b string) error {
	if s.guestCode < 400 {
		return fmt.Errorf("the merge did not fail (guest %d), broker got %s", s.guestCode, s.resolvedRaw)
	}
	for _, w := range []string{a, b} {
		if !strings.Contains(string(s.guestBody), w) {
			return fmt.Errorf("the failure does not name %q: %s", w, s.guestBody)
		}
	}
	return nil
}

func (s *cf4State) failureSaysProfile() error {
	if !strings.Contains(string(s.guestBody), "profile") {
		return fmt.Errorf("the failure does not say which key came from the profile: %s", s.guestBody)
	}
	return nil
}

func (s *cf4State) guestShapeUnchanged() error {
	if s.guestCode != 200 {
		return fmt.Errorf("guest status %d: %s", s.guestCode, s.guestBody)
	}
	var r struct {
		Choices []any `json:"choices"`
	}
	if json.Unmarshal(s.guestBody, &r) != nil || len(r.Choices) == 0 {
		return fmt.Errorf("guest response is not an OpenAI chat completion: %s", s.guestBody)
	}
	return nil
}

func (s *cf4State) handRolledToBroker(body string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	var obj map[string]any
	_ = json.Unmarshal([]byte(body), &obj)
	var msgs any
	_ = json.Unmarshal([]byte(cf4Messages), &msgs)
	obj["messages"] = msgs
	b, _ := json.Marshal(obj)
	// A hand-rolled caller is still a signed one (any real client signs): the broker answers
	// an unsigned spend with 401 before it reads the body, which is not this scenario.
	req, _ := http.NewRequest(http.MethodPost, s.brokerURL+"/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	client.SignRequest(req, b)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s.guestCode, s.guestBody, s.guestHdr = resp.StatusCode, rb, resp.Header
	return nil
}

func (s *cf4State) brokerAnswers400Code(code string) error {
	if s.guestCode != 400 {
		return fmt.Errorf("broker answered %d, want 400: %s", s.guestCode, s.guestBody)
	}
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(s.guestBody, &e)
	if e.Error.Code != code {
		return fmt.Errorf("error.code %q, want %q: %s", e.Error.Code, code, s.guestBody)
	}
	return nil
}

func (s *cf4State) noHoldPlaced() error {
	if c := s.guestHdr.Get("X-RogerAI-Cost"); c != "0" {
		return fmt.Errorf("X-RogerAI-Cost = %q, want \"0\" on a refusal", c)
	}
	if s.guestHdr.Get("X-RogerAI-Receipt") != "" {
		return fmt.Errorf("a refusal carried a receipt")
	}
	return nil
}

// ── profile resolution through the CLI ─────────────────────────────────────────────────

func (s *cf4State) takeTuneAsResolved() error {
	m, raw, err := s.tuneTimeBody()
	if err != nil {
		return err
	}
	s.resolved, s.resolvedRaw = m, raw
	return nil
}

func (s *cf4State) profileResolved(name string) error {
	if err := s.runLine("roger use @profile/" + name + " --yes"); err != nil {
		return err
	}
	return s.takeTuneAsResolved()
}

func (s *cf4State) profileResolvedFor(name, model string) error {
	if err := s.runLine("roger use " + model + " --profile " + name + " --yes"); err != nil {
		return err
	}
	return s.takeTuneAsResolved()
}

func (s *cf4State) defaultResolvedFor(model string) error {
	if err := s.runLine("roger use " + model + " --yes"); err != nil {
		return err
	}
	return s.takeTuneAsResolved()
}

func (s *cf4State) bodyObjectCarries(text string) error {
	if s.resolved == nil {
		return fmt.Errorf("no body object resolved (guest %d: %s; last exit %d: %s)", s.guestCode, s.guestBody, s.lastCode, s.lastErr)
	}
	return cf4Check(s.resolved, s.resolvedRaw, text)
}

func (s *cf4State) bodyEqualsProfileBlocks() error {
	if s.resolved == nil {
		return fmt.Errorf("no body object resolved")
	}
	p, _ := rfWalk(s.cfgMap(), "profiles.coding")
	pm, _ := p.(map[string]any)
	for _, k := range []string{"models", "provider", "roger"} {
		if !cf4Eq(s.resolved[k], pm[k]) {
			return fmt.Errorf("body %s = %s, profile %s = %s", k, cf4Canon(s.resolved[k]), k, cf4Canon(pm[k]))
		}
	}
	return nil
}

func (s *cf4State) otherProfileKeysIntact() error {
	if s.resolved == nil {
		return fmt.Errorf("no body object resolved (guest %d: %s)", s.guestCode, s.guestBody)
	}
	p, _ := rfWalk(s.cfgMap(), "profiles.p")
	pm, _ := p.(map[string]any)
	for _, leaf := range cf4RoutingLeaves(pm) {
		if _, overridden := s.requestKeys[leaf]; overridden {
			continue
		}
		want, _ := rfWalk(pm, leaf)
		if err := cf4Check(s.resolved, s.resolvedRaw, leaf+" = "+cf4Canon(want)); err != nil {
			return err
		}
	}
	return nil
}

func (s *cf4State) resolvedCarriesNeither() error {
	if s.resolved == nil {
		if err := s.profileResolvedFor("coding", "qwen3-32b"); err != nil {
			return err
		}
	}
	if v, ok := s.resolved["max_tokens"]; ok && cf4Eq(v, 512) {
		return fmt.Errorf("the profile's max_tokens 512 reached the broker")
	}
	if bytes.Contains(s.resolvedRaw, []byte("be brief")) {
		return fmt.Errorf("the profile's system text reached the broker")
	}
	return nil
}

// ── Then: CLI output ───────────────────────────────────────────────────────────────────

func (s *cf4State) exitNonZero() error {
	if s.useCmd != nil {
		select {
		case <-s.useExited:
			s.snapshotUse()
		case <-time.After(5 * time.Second):
			return fmt.Errorf("the command is still running (it opened the channel instead of refusing)")
		}
	}
	if s.lastCode == 0 {
		return fmt.Errorf("exit code 0, want non-zero\nstdout:\n%s\nstderr:\n%s", s.lastOut, s.lastErr)
	}
	return nil
}

func (s *cf4State) exitNonZeroCfgUnchanged() error {
	if err := s.exitNonZero(); err != nil {
		return err
	}
	return s.configUnchanged()
}

func (s *cf4State) stderrLines() []string {
	if s.useCmd != nil {
		select {
		case <-s.useExited:
			s.snapshotUse()
		case <-time.After(3 * time.Second):
		}
	}
	return cf4Lines(s.errOut())
}

func (s *cf4State) stderrOneLine() error {
	l := s.stderrLines()
	if len(l) != 1 {
		return fmt.Errorf("stderr has %d lines, want exactly one:\n%s", len(l), strings.Join(l, "\n"))
	}
	return nil
}

func (s *cf4State) stderrOneLineNaming(rest string) error {
	if err := s.stderrOneLine(); err != nil {
		return err
	}
	s.lastNaming = cf4Quoted(rest)
	line := s.stderrLines()[0]
	for _, w := range s.lastNaming {
		if !strings.Contains(line, w) {
			return fmt.Errorf("the stderr line does not name %q: %s", w, line)
		}
	}
	return nil
}

func (s *cf4State) stderrOneLineSaying(want string) error {
	if err := s.stderrOneLine(); err != nil {
		return err
	}
	if line := s.stderrLines()[0]; !strings.Contains(line, want) {
		return fmt.Errorf("stderr line %q does not say %q", line, want)
	}
	return nil
}

func (s *cf4State) thatLineNames(want string) error {
	l := s.stderrLines()
	if len(l) == 0 || !strings.Contains(l[0], want) {
		return fmt.Errorf("stderr does not name %q:\n%s", want, strings.Join(l, "\n"))
	}
	return nil
}

func (s *cf4State) linePrintedOnce() error {
	text := s.errOut()
	for _, w := range s.lastNaming {
		if n := strings.Count(text, w); n != 1 {
			return fmt.Errorf("%q is printed %d times, want once:\n%s", w, n, text)
		}
	}
	return nil
}

func (s *cf4State) stderrNames(want string) error {
	s.stderrLines()
	for _, w := range strings.Fields(want) {
		if !strings.Contains(s.errOut(), w) && strings.HasPrefix(w, "--") {
			return fmt.Errorf("stderr does not name %q:\n%s", w, s.errOut())
		}
	}
	if !strings.HasPrefix(want, "--") && !strings.Contains(s.errOut(), want) {
		return fmt.Errorf("stderr lacks %q:\n%s", want, s.errOut())
	}
	return nil
}

func (s *cf4State) stderrSays(want string) error {
	s.stderrLines()
	if !strings.Contains(s.errOut(), want) {
		return fmt.Errorf("stderr lacks %q:\n%s", want, s.errOut())
	}
	return nil
}

func (s *cf4State) stderrHasExactlyLine(want string) error {
	n := 0
	for _, l := range s.stderrLines() {
		if strings.TrimSpace(l) == want || strings.Contains(l, want) {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("stderr has %d line(s) %q, want exactly one:\n%s", n, want, s.errOut())
	}
	return nil
}

func (s *cf4State) stdoutSays(want string) error {
	if !strings.Contains(s.out(), want) {
		return fmt.Errorf("stdout lacks %q:\n%s", want, s.out())
	}
	return nil
}

func (s *cf4State) stdoutIs(doc *godog.DocString) error {
	got := cf4Lines(s.out())
	want := cf4Lines(doc.Content)
	norm := func(l []string) string {
		var o []string
		for _, x := range l {
			o = append(o, strings.Join(strings.Fields(x), " "))
		}
		return strings.Join(o, "\n")
	}
	if norm(got) != norm(want) {
		return fmt.Errorf("stdout:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	return nil
}

func (s *cf4State) stdoutListsUnder(items, heading string) error {
	out := s.out()
	i := strings.Index(out, heading)
	if i < 0 {
		return fmt.Errorf("stdout has no %q section:\n%s", heading, out)
	}
	rest := out[i+len(heading):]
	for _, it := range strings.Split(items, ",") {
		it = strings.TrimSpace(it)
		if !strings.Contains(rest, it) {
			return fmt.Errorf("%q is not listed under %q:\n%s", it, heading, out)
		}
	}
	return nil
}

func (s *cf4State) stdoutShows(key, val string) error {
	for _, l := range cf4Lines(s.out()) {
		if strings.Contains(l, key) && strings.Contains(l, strings.Trim(val, `"`)) {
			return nil
		}
	}
	return fmt.Errorf("stdout shows no %s = %s:\n%s", key, val, s.out())
}

func (s *cf4State) appearsNowhereOut(code string) error {
	if strings.Contains(s.out(), code) {
		return fmt.Errorf("%q appears on stdout:\n%s", code, s.out())
	}
	return nil
}

func (s *cf4State) appearsNowhereOutErr(code string) error {
	if err := s.appearsNowhereOut(code); err != nil {
		return err
	}
	if strings.Contains(s.errOut(), code) {
		return fmt.Errorf("%q appears on stderr:\n%s", code, s.errOut())
	}
	return nil
}

func (s *cf4State) runsEach(list string) error {
	s.outputs = nil
	for _, c := range cf4Quoted(list) {
		if err := s.runLine(c); err != nil {
			return err
		}
		s.outputs = append(s.outputs, s.out()+s.errOut())
	}
	return nil
}

func (s *cf4State) appearsInNone(code string) error {
	for i, o := range s.outputs {
		if strings.Contains(o, code) {
			return fmt.Errorf("%q appears in output %d:\n%s", code, i+1, o)
		}
	}
	return nil
}

func (s *cf4State) tuneProceeds() error {
	if !s.useRunning() {
		return fmt.Errorf("the tune did not proceed (exit %d)\nstdout:\n%s\nstderr:\n%s", s.lastCode, s.lastOut, s.lastErr)
	}
	return nil
}

func (s *cf4State) tuneNoProfileWarning() error {
	if err := s.tuneProceeds(); err != nil {
		return err
	}
	if out := s.out() + s.errOut(); strings.Contains(strings.ToLower(out), "profile") {
		return fmt.Errorf("the tune printed a profile warning:\n%s", out)
	}
	return nil
}

func (s *cf4State) tuneOnDefaults() error { return s.tuneProceeds() }

func (s *cf4State) notMovedCorrupt() error {
	if _, err := os.Stat(s.cfgPath() + ".corrupt"); err == nil {
		return fmt.Errorf("config.json was moved to .corrupt")
	}
	return nil
}

func (s *cf4State) movedCorrupt() error {
	if _, err := os.Stat(s.cfgPath() + ".corrupt"); err != nil {
		return fmt.Errorf("config.json was not moved to .corrupt: %v", err)
	}
	return nil
}

func (s *cf4State) profileListed(outcome string) error {
	out := s.out() + s.errOut()
	m := s.cfgMap()
	profs, _ := m["profiles"].(map[string]any)
	name := ""
	for k := range profs {
		name = k
	}
	if outcome == "listed" {
		for _, l := range cf4Lines(s.out()) {
			if f := strings.Fields(l); len(f) > 0 && f[0] == name {
				return nil
			}
		}
		return fmt.Errorf("profile %q is not listed (exit %d):\n%s", name, s.lastCode, out)
	}
	want := strings.Trim(strings.TrimPrefix(outcome, "rejected as "), `"`)
	if !strings.Contains(out, want) {
		return fmt.Errorf("profile %q not rejected as %q (exit %d):\n%s", name, want, s.lastCode, out)
	}
	return nil
}

func (s *cf4State) definedDefaultIgnored() error {
	for _, l := range cf4Lines(s.out()) {
		if strings.HasPrefix(strings.TrimSpace(l), "default") {
			if !strings.Contains(l, "built from limits") {
				return fmt.Errorf("the default line is not the folded limits profile: %s", l)
			}
			return nil
		}
	}
	return fmt.Errorf("profile list has no default line:\n%s", s.out())
}

// ── Then: config ───────────────────────────────────────────────────────────────────────

func (s *cf4State) cfgPathEquals(path, val string) error {
	v, ok := rfWalk(s.cfgMap(), path)
	if !ok {
		return fmt.Errorf("config has no %s", path)
	}
	if !cf4Eq(v, cf4Value(val)) {
		return fmt.Errorf("config %s = %s, want %s", path, cf4Canon(v), cf4Canon(cf4Value(val)))
	}
	return nil
}

func (s *cf4State) limitEquals(path, val string) error { return s.cfgPathEquals(path, val) }

func (s *cf4State) cfgHasNoKey(path, key string) error {
	if v, ok := rfWalk(s.cfgMap(), path+"."+key); ok {
		return fmt.Errorf("config %s.%s = %s, want absent", path, key, cf4Canon(v))
	}
	return nil
}

func (s *cf4State) cfgHasNo(path string) error {
	if v, ok := rfWalk(s.cfgMap(), path); ok {
		return fmt.Errorf("config still has %s = %s", path, cf4Canon(v))
	}
	return nil
}

func (s *cf4State) notRewritten() error {
	b, _ := os.ReadFile(s.cfgPath())
	if !bytes.Equal(b, s.cfgBaseline) {
		return fmt.Errorf("config.json was rewritten:\nbefore:\n%s\nafter:\n%s", s.cfgBaseline, b)
	}
	if fi, err := os.Stat(s.cfgPath()); err == nil && !fi.ModTime().Equal(s.cfgBaseMod) {
		return fmt.Errorf("config.json mtime changed (rewritten with the same bytes)")
	}
	return nil
}

func (s *cf4State) writeAtomic() error {
	fi, err := os.Stat(s.cfgPath())
	if err != nil {
		return err
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	if st == nil || st.Ino == s.cfgInode {
		return fmt.Errorf("config.json kept its inode: not written by temp file + rename")
	}
	return nil
}

func (s *cf4State) profileExistsExactly(name string) error {
	p, ok := rfWalk(s.cfgMap(), "profiles."+name)
	if !ok {
		return fmt.Errorf("no profile %q", name)
	}
	want := map[string]any{}
	if s.lastSetPath != "" {
		rfSet(want, s.lastSetPath, s.lastSetValue)
	}
	if !cf4Eq(p, want) {
		return fmt.Errorf("profile %s = %s, want exactly %s", name, cf4Canon(p), cf4Canon(want))
	}
	return nil
}

func (s *cf4State) profileEquals(name, val string) error {
	return s.cfgPathEquals("profiles."+name, val)
}

func (s *cf4State) noProfileExists(name string) error { return s.cfgHasNo("profiles." + name) }

func (s *cf4State) otherProfilesUntouched() error {
	m := s.cfgMap()
	profs, _ := m["profiles"].(map[string]any)
	for k, v := range s.profilesSnap {
		if k == "coding" {
			continue
		}
		if !cf4Eq(profs[k], v) {
			return fmt.Errorf("profile %s changed", k)
		}
	}
	return nil
}

func (s *cf4State) nonProfileKeysUnchanged() error {
	var before map[string]any
	_ = json.Unmarshal(s.cfgBefore, &before)
	after := s.cfgMap()
	delete(before, "profiles")
	delete(after, "profiles")
	if !cf4Eq(before, after) {
		return fmt.Errorf("non-profile keys changed:\nbefore %s\nafter  %s", cf4Canon(before), cf4Canon(after))
	}
	return nil
}

// stdoutJSONBlock: the first JSON object on stdout equals the routing part of the body the
// CLI sends for the same profile (`roger use qwen3-32b --profile <p> --yes`).
func (s *cf4State) stdoutJSONBlock() error {
	out := s.out()
	i := strings.Index(out, "{")
	if i < 0 {
		return fmt.Errorf("stdout has no JSON block:\n%s", out)
	}
	dec := json.NewDecoder(strings.NewReader(out[i:]))
	var shown map[string]any
	if err := dec.Decode(&shown); err != nil {
		return fmt.Errorf("stdout's JSON block does not parse: %v\n%s", err, out)
	}
	// The comparison runs `roger use`, which would replace the show output the next step
	// reads: keep the show command's output as "the last command" afterwards.
	showOut, showErr, showCode := s.lastOut, s.lastErr, s.lastCode
	defer func() {
		s.stopUse()
		s.lastExitWasUse = false
		s.lastOut, s.lastErr, s.lastCode = showOut, showErr, showCode
	}()
	if err := s.profileResolvedFor("coding", "qwen3-32b"); err != nil {
		return err
	}
	for _, k := range []string{"models", "provider", "roger"} {
		if !cf4Eq(shown[k], s.resolved[k]) {
			return fmt.Errorf("shown %s = %s, resolved %s = %s", k, cf4Canon(shown[k]), k, cf4Canon(s.resolved[k]))
		}
	}
	return nil
}

func (s *cf4State) valueNotPrinted() error {
	if strings.Contains(s.out()+s.errOut(), "sk-") {
		return fmt.Errorf("the api_key value is printed:\n%s%s", s.out(), s.errOut())
	}
	return nil
}

func (s *cf4State) keysAnnotated(list string) error {
	for _, src := range strings.Split(list, "/") {
		if src = strings.TrimSpace(src); !strings.Contains(s.out(), src) {
			return fmt.Errorf("stdout annotates no key with source %q:\n%s", src, s.out())
		}
	}
	return nil
}

func (s *cf4State) profileSetCmd(line string) error {
	if err := s.runLine(line); err != nil {
		return err
	}
	args := cf4Split(line)
	if len(args) >= 6 && args[1] == "profile" && args[2] == "set" {
		s.lastSetPath, s.lastSetValue = args[4], cf4Value(args[5])
	}
	return nil
}

func (s *cf4State) twoConcurrentSets() error {
	if err := s.ensure(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, n := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			cmd := exec.Command(rfRogerBin, "profile", "set", n, "roger.pref", "fast")
			cmd.Env = s.rogerEnv()
			if err := cmd.Run(); err != nil {
				codes[i] = 1
			}
		}(i, n)
	}
	wg.Wait()
	if codes[0] != 0 || codes[1] != 0 {
		return fmt.Errorf("a concurrent `roger profile set` failed: %v", codes)
	}
	return nil
}

func (s *cf4State) bothKeysPresent() error {
	for _, n := range []string{"alpha", "beta"} {
		if err := s.cfgPathEquals("profiles."+n+".roger.pref", `"fast"`); err != nil {
			return err
		}
	}
	return nil
}

func (s *cf4State) rowShows(model, list string) error {
	for _, l := range cf4Lines(s.out()) {
		if strings.Contains(l, model) {
			for _, w := range cf4Quoted(list) {
				if !strings.Contains(l, w) {
					return fmt.Errorf("the %s row lacks %q: %s", model, w, l)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("no row for %q:\n%s", model, s.out())
}

// ── freshness ──────────────────────────────────────────────────────────────────────────

func (s *cf4State) userTunesWith(line string) error { return s.runLine(line) }

func (s *cf4State) bumpMtime() {
	t := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(s.cfgPath(), t, t)
}

func (s *cf4State) cfgChangesProfile(name, text string) error {
	if err := s.setProfileAssignments(name, text); err != nil {
		return err
	}
	s.bumpMtime()
	return nil
}

func (s *cf4State) chatWithoutProfile() error {
	if err := s.sendChat(); err != nil {
		return err
	}
	h, err := s.rec.last()
	if err != nil {
		return err
	}
	s.resolved, s.resolvedRaw = nil, h.body
	return json.Unmarshal(h.body, &s.resolved)
}

func (s *cf4State) rereadByMtime() error {
	// Change the profile but restore the previous mtime: a proxy that re-reads by mtime keeps
	// what it had; one that re-reads on every request picks the new value up.
	fi, err := os.Stat(s.cfgPath())
	if err != nil {
		return err
	}
	prev := fi.ModTime()
	before := s.resolvedRaw
	if err := s.setProfileAssignments("coding", `roger.pref = "zzz-mtime-probe"`); err != nil {
		return err
	}
	_ = os.Chtimes(s.cfgPath(), prev, prev)
	if err := s.guestSend(`{"model": "@profile/coding"}`); err != nil {
		return err
	}
	if bytes.Contains(s.resolvedRaw, []byte("zzz-mtime-probe")) {
		return fmt.Errorf("the proxy re-read config.json although its mtime did not change")
	}
	_ = before
	return nil
}

func (s *cf4State) proxyResolvedOnce(name string) error {
	if _, ok := rfWalk(s.cfgMap(), "profiles."+name); !ok {
		if err := s.profileSets(name, `roger.pref = "fast"`); err != nil {
			return err
		}
	}
	if err := s.ensureProxy("qwen3-32b"); err != nil {
		return err
	}
	if err := s.guestSend(`{"model": "@profile/` + name + `"}`); err != nil {
		return err
	}
	if s.resolved != nil {
		if v, ok := rfWalk(s.resolved, "roger.pref"); ok {
			s.goodPref, _ = v.(string)
		}
	}
	if s.goodPref == "" {
		return fmt.Errorf("the proxy did not resolve profile %s once (guest %d: %s; broker body %s)", name, s.guestCode, s.guestBody, s.resolvedRaw)
	}
	return nil
}

func (s *cf4State) cfgReplacedTruncated() error {
	b, _ := os.ReadFile(s.cfgPath())
	if err := os.WriteFile(s.cfgPath(), b[:len(b)/2], 0600); err != nil {
		return err
	}
	s.bumpMtime()
	return nil
}

func (s *cf4State) oneWarningNamesConfig() error {
	n := 0
	for _, l := range cf4Lines(s.useOut.String() + s.useErr.String()) {
		if strings.Contains(l, "config.json") {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d line(s) name config.json, want exactly one:\n%s%s", n, s.useOut.String(), s.useErr.String())
	}
	return nil
}

func (s *cf4State) tunesWithAProfile() error {
	if _, ok := rfWalk(s.cfgMap(), "profiles.p"); !ok {
		if err := s.profileSets("p", `roger.pref = "fast"`); err != nil {
			return err
		}
	}
	if err := s.runLine("roger use qwen3-32b --profile p --yes"); err != nil {
		return err
	}
	_, _, err := s.tuneTimeBody()
	return err
}

func (s *cf4State) noAccountCarriesProfile() error {
	for _, h := range s.frontHits() {
		if strings.HasPrefix(h.path, "/account") || h.path == "/me" {
			if bytes.Contains(h.body, []byte("profile")) {
				return fmt.Errorf("%s %s carried the profile: %s", h.method, h.path, h.body)
			}
		}
	}
	return nil
}

func (s *cf4State) brokerSeesOnlyResolved() error {
	for _, h := range s.rec.chatHops() {
		if bytes.Contains(h.body, []byte("@profile/")) || bytes.Contains(h.body, []byte(`"profile"`)) {
			return fmt.Errorf("the broker saw a profile reference: %s", h.body)
		}
	}
	return nil
}

// ── routing line / help / release surface ──────────────────────────────────────────────

func (s *cf4State) routingLines() []string {
	var out []string
	for _, l := range cf4Lines(s.out()) {
		if strings.HasPrefix(strings.TrimSpace(l), "routing:") {
			out = append(out, l)
		}
	}
	return out
}

func (s *cf4State) promptOneRoutingLine() error {
	l := s.routingLines()
	if len(l) != 1 {
		return fmt.Errorf("%d line(s) begin with \"routing:\", want exactly one:\n%s", len(l), s.out())
	}
	s.routingLine = l[0]
	return nil
}

func (s *cf4State) thatLineReads(want string) error {
	if strings.TrimSpace(s.routingLine) != want {
		return fmt.Errorf("routing line %q, want %q", strings.TrimSpace(s.routingLine), want)
	}
	return nil
}

func (s *cf4State) monoStylesOnly() error {
	// The CLI's plain-text plates carry no ANSI styling at all (client.go, main.go); a routing
	// line with any escape sequence adds a color beyond the existing styles.
	if strings.Contains(s.routingLine, "\x1b") {
		return fmt.Errorf("the routing line carries an escape sequence: %q", s.routingLine)
	}
	return nil
}

func (s *cf4State) routingLineReads(want string) error {
	if err := s.promptOneRoutingLine(); err != nil {
		return err
	}
	return s.thatLineReads(want)
}

func (s *cf4State) noRoutingLine() error {
	if l := s.routingLines(); len(l) != 0 {
		return fmt.Errorf("the connect prompt has a routing line: %v", l)
	}
	return nil
}

func (s *cf4State) outCapWordingUnchanged() error {
	// Today's connect plate names the cap as "your max ... $/1M out" (client.go Use).
	if !strings.Contains(s.out(), "your max") || !strings.Contains(s.out(), "$/1M out") {
		return fmt.Errorf("the existing out-cap wording is missing:\n%s", s.out())
	}
	return nil
}

func (s *cf4State) routingLineContains(want string) error {
	if err := s.promptOneRoutingLine(); err != nil {
		return err
	}
	if !strings.Contains(s.routingLine, want) {
		return fmt.Errorf("routing line %q lacks %q", s.routingLine, want)
	}
	return nil
}

func (s *cf4State) noConfirmShown() error {
	if strings.Contains(s.out(), "open the channel? (y/N)") || strings.Contains(s.out(), "TYPE THE OUT-PRICE") {
		return fmt.Errorf("a confirm prompt was shown:\n%s", s.out())
	}
	return nil
}

func (s *cf4State) stdoutOneRoutingLine() error { return s.promptOneRoutingLine() }

func (s *cf4State) connectNamesBand(band, mark string) error {
	for _, l := range cf4Lines(s.out()) {
		if strings.Contains(l, band) && strings.Contains(l, mark) {
			return nil
		}
	}
	return fmt.Errorf("no connect line names %q with the mark %q:\n%s", band, mark, s.out())
}

func (s *cf4State) tunedBandIs(model string) error {
	if !s.useRunning() {
		return fmt.Errorf("roger use is not running (exit %d): %s", s.lastCode, s.lastErr)
	}
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v1/models", s.proxyPort), nil)
	req.Header.Set("Authorization", "Bearer "+s.sessionKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var l struct {
		Data []struct{ ID string } `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&l)
	if len(l.Data) != 1 || l.Data[0].ID != model {
		return fmt.Errorf("the proxy's tuned band is %+v, want %q", l.Data, model)
	}
	return nil
}

func (s *cf4State) helpFor(flag string) []string {
	s.runTimed("use", "-h")
	var lines []string
	for _, l := range cf4Lines(s.lastOut + s.lastErr) {
		if strings.Contains(l, strings.TrimPrefix(flag, "--")) {
			lines = append(lines, l)
		}
	}
	return lines
}

func (s *cf4State) helpSays(flag, want string) error {
	l := s.helpFor(flag)
	if !strings.Contains(strings.Join(l, "\n"), want) {
		return fmt.Errorf("help for %s does not say %q:\n%s", flag, want, strings.Join(l, "\n"))
	}
	return nil
}

func (s *cf4State) helpNoLonger(flag, bad string) error {
	if l := s.helpFor(flag); strings.Contains(strings.Join(l, "\n"), bad) {
		return fmt.Errorf("help for %s still contains %q:\n%s", flag, bad, strings.Join(l, "\n"))
	}
	return nil
}

func (s *cf4State) brokerTreatsAs(v string) error {
	if _, _, err := s.tuneTimeBody(); err != nil {
		return err
	}
	if !strings.Contains(s.brokerLog.String(), "max_out="+v) {
		return fmt.Errorf("the broker's routing log line does not show max_out=%s:\n%s", v, s.brokerLog.String())
	}
	return nil
}

func (s *cf4State) endpointBinds(port string) error {
	if !s.useRunning() {
		return fmt.Errorf("roger use exited (code %d): %s%s", s.lastCode, s.lastOut, s.lastErr)
	}
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
	if err != nil {
		return fmt.Errorf("nothing listens on port %s: %v", port, err)
	}
	c.Close()
	return nil
}

// reasoningOff: an extra station answering with reasoning_content only; the original
// stations refuse one request so the broker fails over to it; raw mode returns the empty
// content unchanged instead of surfacing the reasoning as content.
func (s *cf4State) reasoningOff() error {
	if !s.useRunning() {
		return fmt.Errorf("roger use exited (code %d): %s%s", s.lastCode, s.lastOut, s.lastErr)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"qwen3-32b","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"thinking out loud"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	sess, err := agent.Start(agent.Config{
		Broker: s.brokerURL, Upstream: up.URL + "/v1/chat/completions",
		NodeID: "n-reasoning", Region: "home", HW: "test", Model: "qwen3-32b", Ctx: 8192, Parallel: 2,
	})
	if err != nil {
		up.Close()
		return err
	}
	s.stations = append(s.stations, &rfStation{id: "n-reasoning", sess: sess, up: up})
	deadline := time.Now().Add(10 * time.Second)
	for !s.discoverHas("n-reasoning") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	for _, st := range s.stations {
		if st.id != "n-reasoning" {
			st.deny.Store(true)
		}
	}
	err = s.sendChat()
	for _, st := range s.stations {
		st.deny.Store(false)
	}
	if err != nil {
		return err
	}
	body := s.respBodys[len(s.respBodys)-1]
	var r struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(body, &r)
	if len(r.Choices) == 0 {
		return fmt.Errorf("no completion came back: %s", body)
	}
	if r.Choices[0].Message.Content != "" {
		return fmt.Errorf("the reasoning was surfaced as content %q: the fallback is on", r.Choices[0].Message.Content)
	}
	return nil
}

func (s *cf4State) noRoutingKeyBeyondCap() error { return s.noOtherKeysAtAll() }

func (s *cf4State) noNodeHeader() error {
	if _, _, err := s.tuneTimeBody(); err != nil {
		return err
	}
	h, _ := s.rec.last()
	if v := h.header.Get("X-Roger-Node"); v != "" {
		return fmt.Errorf("X-Roger-Node = %q", v)
	}
	return nil
}

func (s *cf4State) noExcludeHeader() error {
	if _, _, err := s.tuneTimeBody(); err != nil {
		return err
	}
	h, _ := s.rec.last()
	if v := h.header.Get("X-Roger-Exclude-Nodes"); v != "" {
		return fmt.Errorf("X-Roger-Exclude-Nodes = %q derived from a scan", v)
	}
	return nil
}

func (s *cf4State) chatLocalEndpoint() error { return s.sendChat() }

func (s *cf4State) brokerRequestCarries(text string) error {
	h, err := s.rec.last()
	if err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal(h.body, &m)
	return cf4Check(m, h.body, text)
}

func (s *cf4State) requestHasNoHeaders(a, b string) error {
	h, err := s.rec.last()
	if err != nil {
		return err
	}
	for _, n := range []string{a, b} {
		if v := h.header.Get(n); v != "" {
			return fmt.Errorf("header %s = %q, want absent", n, v)
		}
	}
	return nil
}

func (s *cf4State) stillMaxPriceOut() error {
	h, err := s.rec.last()
	if err != nil {
		return err
	}
	if h.header.Get("X-Roger-Max-Price-Out") == "" {
		return fmt.Errorf("X-Roger-Max-Price-Out is missing")
	}
	return nil
}

func (s *cf4State) brokerModels404() error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.hitsMu.Lock()
	s.models404 = true
	s.hitsMu.Unlock()
	return nil
}

func (s *cf4State) tuneWarnsOnce(want string) error {
	out := s.useOut.String() + s.useErr.String()
	if n := strings.Count(out, want); n != 1 {
		return fmt.Errorf("the tune output has %q %d time(s), want once:\n%s", want, n, out)
	}
	return nil
}

func (s *cf4State) carriesTPSNoBody(v string) error {
	h, err := s.rec.last()
	if err != nil {
		return err
	}
	if got := h.header.Get("X-Roger-Min-TPS"); got != v {
		return fmt.Errorf("X-Roger-Min-TPS = %q, want %q", got, v)
	}
	var m map[string]any
	_ = json.Unmarshal(h.body, &m)
	for _, k := range []string{"models", "provider", "roger"} {
		if _, ok := m[k]; ok {
			return fmt.Errorf("header-mode body still carries %q: %s", k, h.body)
		}
	}
	return nil
}

func (s *cf4State) noNegotiationRetry() error {
	if n, want := len(s.rec.chatHops()), s.chatSent; n != want {
		return fmt.Errorf("%d chat hops for %d chat(s): a request was retried", n, want)
	}
	return nil
}

func (s *cf4State) helpHeading(want string) error {
	l := cf4Lines(s.out() + s.errOut())
	if len(l) == 0 || !strings.HasPrefix(strings.TrimSpace(l[0]), want) {
		return fmt.Errorf("the heading does not begin with %q:\n%s", want, strings.Join(l, "\n"))
	}
	return nil
}

func (s *cf4State) useOnceInList() error {
	n := 0
	for _, l := range cf4Lines(s.out() + s.errOut()) {
		// the command list writes each entry as "  roger <command> ..."
		if f := strings.Fields(l); len(f) > 1 && f[0] == "roger" && f[1] == "use" {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("\"use\" appears %d times in the command list", n)
	}
	return nil
}

var cf4NewFlags = []string{"--models", "--only", "--order", "--exclude", "--node", "--no-fallbacks", "--sort", "--require", "--params", "--min-ctx", "--max-ttft", "--trust", "--region", "--max-cost", "--profile"}

func (s *cf4State) newFlagsOnlyUnderUse() error {
	help := s.out() + s.errOut()
	for _, f := range cf4NewFlags {
		if strings.Contains(help, f) {
			return fmt.Errorf("`roger help` lists %s (the routing flags belong under `roger use --help`)", f)
		}
	}
	return nil
}

func (s *cf4State) useHelpRoutingSection() error {
	help := s.out() + s.errOut()
	if !regexp.MustCompile(`(?im)^\s*routing\b`).MatchString(help) {
		return fmt.Errorf("`roger use --help` has no routing section:\n%s", help)
	}
	for _, f := range append(cf4NewFlags, "--pref", "--quant", "--self-hosted") {
		if !strings.Contains(help, f) {
			return fmt.Errorf("`roger use --help` does not list %s:\n%s", f, help)
		}
	}
	return nil
}

func (s *cf4State) advancedStillLists(want string) error {
	if !strings.Contains(s.out()+s.errOut(), want) {
		return fmt.Errorf("the advanced section does not list %q:\n%s", want, s.out()+s.errOut())
	}
	return nil
}

func (s *cf4State) conciseUnknown() error {
	l := s.stderrLines()
	if len(l) != 1 || !strings.Contains(l[0], "unknown command") {
		return fmt.Errorf("stderr is not one concise unknown-command line:\n%s", strings.Join(l, "\n"))
	}
	return nil
}

// ── runners ────────────────────────────────────────────────────────────────────────────

func cf4Register(sc *godog.ScenarioContext, s *cf4State) {
	// Background / Given
	sc.Step(`^a config with no limits and no profiles$`, s.givenConfig)
	sc.Step(`^the broker at "[^"]*" accepts the routing body object$`, s.givenConfig)
	sc.Step(`^a temp config dir with an otherwise-default config\.json$`, s.givenConfig)
	sc.Step(`^the broker's register ceiling for output price is \$([0-9.]+)/1M$`, s.ceilingIs)
	sc.Step(`^limits\.(\S+) is (.+)$`, s.limitIs)
	sc.Step(`^a config written by v6\.12\.0 with limits\.(\S+) = (.+)$`, s.oldConfig)
	sc.Step(`^profile "([^"]+)" sets (.+)$`, s.profileSets)
	sc.Step(`^profile "([^"]+)" is (\{.*\})$`, s.profileIs)
	sc.Step(`^profile "([^"]+)" contains (.+)$`, s.profileContains)
	sc.Step(`^config\.json contains:$`, s.configContainsDoc)
	sc.Step(`^config\.json has "profiles": (.+)$`, s.configProfilesRaw)
	sc.Step(`^config\.json is truncated mid-object$`, s.configTruncated)
	sc.Step(`^config\.json defines a profile named "(.*)"$`, s.definesProfile)
	sc.Step(`^profiles "([^"]+)" and "([^"]+)" exist$`, s.profilesExist)
	sc.Step(`^profile "([^"]+)" exists$`, s.profileExists)
	sc.Step(`^no profile named "([^"]+)"$`, s.noProfileNamed)
	sc.Step(`^profiles "coding" \(sort throughput, only n1,n2, require tools\) and "cheap" \(pref cheap\)$`, s.codingAndCheap)
	sc.Step(`^config\.json carries share, share_prices, voices, palette and agent_perms$`, s.configCarriesOthers)
	sc.Step(`^the request sets ([a-z_.]+) = (.+)$`, s.requestSets)
	sc.Step(`^the request is (\{.*\})$`, s.requestIs)
	sc.Step(`^the request body is (\{.*\})$`, s.requestIs)
	sc.Step(`^the broker answers 404 to GET /v1/models$`, s.brokerModels404)
	sc.Step(`^the local proxy is tuned to "([^"]+)"$`, s.proxyTunedTo)
	sc.Step(`^a live proxy session with band model "([^"]+)"$`, s.liveProxyBand)
	sc.Step(`^the proxy was tuned with (--.+)$`, s.proxyTunedWith)
	sc.Step(`^the local proxy is live$`, s.proxyIsLive)
	sc.Step(`^the proxy resolved profile "([^"]+)" once$`, s.proxyResolvedOnce)
	sc.Step(`^a hand-rolled caller sends (\{.*\}) straight to the broker$`, s.handRolledToBroker)
	sc.Step(`^the user tunes with "([^"]+)"$`, s.userTunesWith)

	// When
	sc.Step(`^the user runs "(.*)"$`, s.profileSetCmd)
	sc.Step(`^the user runs each of (.+)$`, s.runsEach)
	sc.Step(`^profile "([^"]+)" is resolved$`, s.profileResolved)
	sc.Step(`^profile "([^"]+)" is resolved for "([^"]+)"$`, s.profileResolvedFor)
	sc.Step(`^the default profile is resolved for "([^"]+)"$`, s.defaultResolvedFor)
	sc.Step(`^profile "([^"]+)" is merged with the request$`, s.mergedWithRequest)
	sc.Step(`^the request is resolved by the local proxy$`, s.resolveByProxy)
	sc.Step(`^the local proxy resolves the request$`, s.resolveByProxy)
	sc.Step(`^the request names "([^"]+)"$`, s.requestNames)
	sc.Step(`^the guest sends (\{.*\})$`, s.guestSends)
	sc.Step(`^a guest sends (\{.*\})$`, s.guestSends)
	sc.Step(`^a chat request goes through the local endpoint$`, s.chatLocalEndpoint)
	sc.Step(`^config\.json changes profile "([^"]+)" to (.+)$`, s.cfgChangesProfile)
	sc.Step(`^a chat request without a profile reference goes through the endpoint$`, s.chatWithoutProfile)
	sc.Step(`^config\.json is replaced by a truncated file$`, s.cfgReplacedTruncated)
	sc.Step(`^two "roger profile set" commands run at once on different profiles$`, s.twoConcurrentSets)
	sc.Step(`^the user tunes with a profile$`, s.tunesWithAProfile)

	// Then: tune-time body
	sc.Step(`^the tune-time body carries ([a-z_.]+) = (.+)$`, s.tuneCarries)
	sc.Step(`^the tune-time body carries no ([a-z_.]+) key$`, s.tuneCarriesNo)
	sc.Step(`^the tune-time request carries the X-Roger-Freq header "([^"]+)"$`, s.tuneFreqHeader)
	sc.Step(`^a private band with code "([^"]+)" resolves for "([^"]+)"$`, s.privateBandResolves)
	sc.Step(`^the broker verifies tool calling on its stations$`, s.brokerVerifiesTools)
	sc.Step(`^the band's station has verified tool calling$`, s.bandStationToolsVerified)
	sc.Step(`^no other routing key is present beyond the always-present default out-cap \(provider\.max_price\.completion = 10\)$`, s.noOtherKeysAsserted)
	sc.Step(`^no other routing key is present$`, s.noOtherKeysAtAll)
	sc.Step(`^no routing key is present in the body beyond the always-present default out-cap$`, s.noRoutingKeyBeyondCap)
	sc.Step(`^no X-Roger-Node header is set when the broker accepts the body object$`, s.noNodeHeader)
	sc.Step(`^no X-Roger-Exclude-Nodes header is derived from a discover scan$`, s.noExcludeHeader)
	sc.Step(`^the broker treats it as ([0-9.]+)$`, s.brokerTreatsAs)
	sc.Step(`^the local endpoint binds port (\d+)$`, s.endpointBinds)
	sc.Step(`^the reasoning fallback is off for the session$`, s.reasoningOff)
	sc.Step(`^the tuned band's model is "([^"]+)"$`, s.tunedBandIs)

	// Then: profile body object / broker
	sc.Step(`^the body object equals the profile's models, provider and roger blocks verbatim$`, s.bodyEqualsProfileBlocks)
	sc.Step(`^the body object carries (.+)$`, s.bodyObjectCarries)
	sc.Step(`^every other profile key is intact$`, s.otherProfileKeysIntact)
	sc.Step(`^the resolved body object carries neither$`, s.resolvedCarriesNeither)
	sc.Step(`^the resolution fails naming "([^"]+)" and "([^"]+)"$`, s.resolutionFails)
	sc.Step(`^the message says which came from the profile$`, s.failureSaysProfile)
	sc.Step(`^the broker receives (.+)$`, s.brokerReceives)
	sc.Step(`^the guest receives an OpenAI-shaped 400 "([^"]+)"$`, s.guest400)
	sc.Step(`^the guest receives a local 400 with error\.code "([^"]+)"$`, s.guestLocal400Code)
	sc.Step(`^a guest request with model "([^"]+)" and models (\[.*\]) is resolved by the local proxy$`, s.guestModelsResolved)
	sc.Step(`^a guest request with model "([^"]+)" and roger\.freq "([^"]+)" is resolved by the local proxy$`, s.guestFreqResolved)
	sc.Step(`^the body carries no roger\.freq$`, s.bodyNoFreq)
	sc.Step(`^the local proxy does not resolve it as a profile$`, s.notResolvedAsProfile)
	sc.Step(`^nothing reaches the broker$`, s.nothingReached)
	sc.Step(`^the local proxy treats it as a plain model id \(which no station serves\)$`, s.treatedAsPlain)
	sc.Step(`^the broker answers 400 with error\.code "([^"]+)"$`, s.brokerAnswers400Code)
	sc.Step(`^no hold is placed$`, s.noHoldPlaced)
	sc.Step(`^the guest's response is unchanged in shape$`, s.guestShapeUnchanged)
	sc.Step(`^config\.json was re-read because its mtime changed, not on every request$`, s.rereadByMtime)
	sc.Step(`^one warning line names config\.json$`, s.oneWarningNamesConfig)
	sc.Step(`^no request to /account or /me carries the profile$`, s.noAccountCarriesProfile)
	sc.Step(`^the broker sees only the resolved body object$`, s.brokerSeesOnlyResolved)

	// Then: CLI output
	sc.Step(`^the exit code is non-zero$`, s.exitNonZero)
	sc.Step(`^the exit code is non-zero and config\.json is unchanged$`, s.exitNonZeroCfgUnchanged)
	sc.Step(`^stderr is exactly one line$`, s.stderrOneLine)
	sc.Step(`^stderr is exactly one line naming (.+)$`, s.stderrOneLineNaming)
	sc.Step(`^stderr is exactly one line saying "(.+)"$`, s.stderrOneLineSaying)
	sc.Step(`^that line names "([^"]+)"$`, s.thatLineNames)
	sc.Step(`^the line is printed once$`, s.linePrintedOnce)
	sc.Step(`^stderr names the usage "([^"]+)"$`, s.stderrSays)
	sc.Step(`^stderr names "([^"]+)"$`, s.stderrNames)
	sc.Step(`^stderr says "(.+)"$`, s.stderrSays)
	sc.Step(`^stderr has exactly one line "(.+)"$`, s.stderrHasExactlyLine)
	sc.Step(`^stderr is the concise unknown-command line, printed once$`, s.conciseUnknown)
	sc.Step(`^stdout says "(.+)"$`, s.stdoutSays)
	sc.Step(`^stdout contains "(.+)"$`, s.stdoutSays)
	sc.Step(`^stdout is:$`, s.stdoutIs)
	sc.Step(`^the value is not printed$`, s.valueNotPrinted)
	sc.Step(`^stdout contains a JSON block equal to the resolved body object$`, s.stdoutJSONBlock)
	sc.Step(`^each key is annotated with its source \(([^)]+)\)$`, s.keysAnnotated)
	sc.Step(`^stdout lists "([^"]+)" under "([^"]+)"$`, s.stdoutListsUnder)
	sc.Step(`^stdout shows ([a-z_.]+) = (.+)$`, s.stdoutShows)
	sc.Step(`^"([^"]+)" appears nowhere on stdout$`, s.appearsNowhereOut)
	sc.Step(`^"([^"]+)" appears nowhere on stdout or stderr$`, s.appearsNowhereOutErr)
	sc.Step(`^"([^"]+)" appears in none of the outputs$`, s.appearsInNone)
	sc.Step(`^the tune proceeds$`, s.tuneProceeds)
	sc.Step(`^the tune proceeds without any profile warning$`, s.tuneNoProfileWarning)
	sc.Step(`^the tune proceeds on defaults$`, s.tuneOnDefaults)
	sc.Step(`^config\.json is not moved to \.corrupt$`, s.notMovedCorrupt)
	sc.Step(`^config\.json is moved to config\.json\.corrupt$`, s.movedCorrupt)
	sc.Step(`^the profile is (listed|rejected as "[^"]+")$`, s.profileListed)
	sc.Step(`^the defined "default" is ignored$`, s.definedDefaultIgnored)
	sc.Step(`^the help text for (--[a-z-]+) says "(.+)"$`, s.helpSays)
	sc.Step(`^the help text for (--[a-z-]+) reads "(.+)"$`, s.helpSays)
	sc.Step(`^the help text for (--[a-z-]+) no longer contains "(.+)"$`, s.helpNoLonger)
	sc.Step(`^the connect line says "([^"]+)"$`, s.stdoutSays)
	sc.Step(`^the connect line names the band as "([^"]+)" with the mark "([^"]+)"$`, s.connectNamesBand)
	sc.Step(`^the connect prompt contains exactly one line beginning with "routing:"$`, s.promptOneRoutingLine)
	sc.Step(`^that line reads "(.+)"$`, s.thatLineReads)
	sc.Step(`^the line uses the monospace brand styles only \(no colors beyond the existing dim and ember\)$`, s.monoStylesOnly)
	sc.Step(`^the routing line reads "(.+)"$`, s.routingLineReads)
	sc.Step(`^the connect prompt has no line beginning with "routing:"$`, s.noRoutingLine)
	sc.Step(`^the existing "out cap" wording is unchanged$`, s.outCapWordingUnchanged)
	sc.Step(`^the routing line contains "(.+)"$`, s.routingLineContains)
	sc.Step(`^no confirm prompt is shown$`, s.noConfirmShown)
	sc.Step(`^stdout contains exactly one line beginning with "routing:"$`, s.stdoutOneRoutingLine)
	sc.Step(`^the heading begins with "([^"]+)"$`, s.helpHeading)
	sc.Step(`^"use" appears once in the command list$`, s.useOnceInList)
	sc.Step(`^the new flags appear only under "roger use --help"$`, s.newFlagsOnlyUnderUse)
	sc.Step(`^the output has a "routing" section listing every flag in this file$`, s.useHelpRoutingSection)
	sc.Step(`^the "advanced" section still lists "([^"]+)"$`, s.advancedStillLists)

	// Then: config
	sc.Step(`^config (limits\.\S+) has no ([a-z_]+)$`, s.cfgHasNoKey)
	sc.Step(`^config (limits\.\S+) equals (.+)$`, s.cfgPathEquals)
	sc.Step(`^(limits\.\S+) equals (.+)$`, s.limitEquals)
	sc.Step(`^config has no (limits\.\S+)$`, s.cfgHasNo)
	sc.Step(`^the config file is unchanged$`, s.configUnchanged)
	sc.Step(`^the config file is not rewritten$`, s.notRewritten)
	sc.Step(`^config\.json (profiles\.\S+) equals (.+)$`, s.cfgPathEquals)
	sc.Step(`^the write is atomic \(temp file \+ rename\) like saveConfig$`, s.writeAtomic)
	sc.Step(`^profile "([^"]+)" exists with exactly that key$`, s.profileExistsExactly)
	sc.Step(`^profile "([^"]+)" equals (\{.*\})$`, s.profileEquals)
	sc.Step(`^no profile named "([^"]+)" exists$`, s.noProfileExists)
	sc.Step(`^other profiles are untouched$`, s.otherProfilesUntouched)
	sc.Step(`^every non-profiles key of config\.json is unchanged$`, s.nonProfileKeysUnchanged)
	sc.Step(`^both keys are present afterwards$`, s.bothKeysPresent)
	sc.Step(`^the row for "([^"]+)" shows (.+)$`, s.rowShows)

	// Then: headers vs body
	sc.Step(`^the broker request body carries (.+)$`, s.brokerRequestCarries)
	sc.Step(`^the request has no (X-Roger-[A-Za-z-]+) and no (X-Roger-[A-Za-z-]+) header$`, s.requestHasNoHeaders)
	sc.Step(`^the request still carries X-Roger-Max-Price-Out \(the headless overpay guard\) for one release$`, s.stillMaxPriceOut)
	sc.Step(`^the tune output warns once "(.+)"$`, s.tuneWarnsOnce)
	sc.Step(`^the request carries X-Roger-Min-TPS: ([0-9.]+) and no routing body object$`, s.carriesTPSNoBody)
	sc.Step(`^no request is retried to negotiate$`, s.noNegotiationRetry)
	sc.Step(`^no request reaches the broker$`, s.noRequestReachedBroker)
}

func cf4Suite(t *testing.T, name, path string) {
	if testing.Short() {
		t.Skip("builds and runs the real broker + roger binaries")
	}
	s := &cf4State{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset(t)
				t.Setenv("ROGERAI_NO_UPDATE_CHECK", "1")
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.teardown()
				return ctx, nil
			})
			cf4Register(sc, s)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{path},
			Tags:     "~@tui && ~@proxy && ~@harness && ~@docs && ~@later && ~@slice5",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatalf("%s: failing scenarios", name)
	}
}

func TestRoutingFlagsCLIBDD(t *testing.T) {
	cf4Suite(t, "features/cli/routing_flags.feature", "../../features/cli/routing_flags.feature")
}

func TestRoutingProfilesCLIBDD(t *testing.T) {
	cf4Suite(t, "features/cli/profiles.feature", "../../features/cli/profiles.feature")
}

// tuneFreqHeader: the band code rides the X-Roger-Freq header of the tune-time request.
func (s *cf4State) tuneFreqHeader(want string) error {
	if _, _, err := s.tuneTimeBody(); err != nil {
		return err
	}
	if got := s.tuneHdr.Get("X-Roger-Freq"); got != want {
		return fmt.Errorf("tune-time X-Roger-Freq = %q, want %q", got, want)
	}
	return nil
}

// brokerVerifiesTools restarts the (still station-less) broker with a 1 s probe interval and
// makes every station's upstream answer the liveness and tool-call canaries as a tool-capable
// model would, so the broker's OWN canary grants the tools capability (it never trusts a
// declared one).
func (s *cf4State) brokerVerifiesTools() error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.toolStations = true
	s.brokerEnv = append(s.brokerEnv, "ROGERAI_PROBE_INTERVAL=1", "ROGERAI_PROBE_CEILING=2")
	return s.restartBroker()
}

// bandStationToolsVerified waits until /discover shows the tools capability on a station.
func (s *cf4State) bandStationToolsVerified() error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.brokerURL + "/discover")
		if err == nil {
			var d struct {
				Offers []struct {
					NodeID       string   `json:"node_id"`
					Capabilities []string `json:"capabilities"`
				} `json:"offers"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&d)
			resp.Body.Close()
			for _, o := range d.Offers {
				for _, c := range o.Capabilities {
					if c == "tools" {
						return nil
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("no station earned the tools capability within 30 s (broker log tail: %s)", cf4Tail(s.brokerLog.String(), 600))
}

func cf4Tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// privateBandResolves scripts the band lookup for one code (see the front recorder).
func (s *cf4State) privateBandResolves(code, model string) error {
	if err := s.ensure(); err != nil {
		return err
	}
	s.hitsMu.Lock()
	s.bandCode, s.bandModel = code, model
	s.hitsMu.Unlock()
	return nil
}
