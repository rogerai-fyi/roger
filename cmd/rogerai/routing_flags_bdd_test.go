package main

// routing_flags_bdd_test.go makes the @cli scenarios of features/routing/regression_pins.feature
// EXECUTABLE against the REAL stack, end to end:
//
//   - the REAL broker binary (go build ./cmd/rogerai-broker, once per test run), started as a
//     subprocess per scenario in single-instance in-memory mode with the Background's fee,
//     consumer default out-cap and register ceiling as its env/flags;
//   - REAL stations: internal/agent.Start (the same code `roger share` and the TUI booth run)
//     registering signed offers (model / quant / price / capabilities) on that broker and
//     long-polling it, each serving from its own OpenAI-shaped httptest upstream (the
//     established seam for "the local model server"; it records what it served so "who
//     served" is observable twice: the X-RogerAI-Provider header and the upstream's log);
//   - the REAL `roger` binary (go build ., once per run), exec'd per scenario with a temp
//     XDG_CONFIG_HOME so `roger use` / `roger config set-limit` / `roger limits` / `-h` read and
//     write a real config.json. exec rather than in-process because cmdUse's flag set is
//     flag.ExitOnError: an unknown flag (which is what several RED scenarios send) would
//     os.Exit the test binary;
//   - a transparent RECORDER (httputil.ReverseProxy) between `roger use` and the broker that
//     logs the headers + body of every /v1/chat/completions hop. It forwards untouched; it is
//     how "the request body carries roger.pref" is observed. It is a recorder, not a mock.
//
// Two steps are only PARTLY observable from this package and say so inline: "the broker's
// effective out-cap is $X/1M" (the broker exposes no per-request cap; the wire value is
// asserted here and the honor/clamp rule is pinned by the @broker runner) and "the routing
// pass ran with the <pref> profile" (observed through the /admin/live routing_pref_<value>
// counter the contract introduces - absent today, which is the RED).
//
// No mocks: the broker, the stations' poll/serve loops, the proxy and the CLI are production
// code paths; only the local model server behind each station is an httptest stand-in.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
)

// ── built binaries (once per test run) ──────────────────────────────────────────────────

var (
	rfBuildOnce sync.Once
	rfBrokerBin string
	rfRogerBin  string
	rfBuildErr  error
)

func rfBuild(t *testing.T) {
	rfBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "rf-bins-")
		if err != nil {
			rfBuildErr = err
			return
		}
		rfBrokerBin = filepath.Join(dir, "rogerai-broker")
		rfRogerBin = filepath.Join(dir, "roger")
		for _, b := range [][2]string{{rfBrokerBin, "../rogerai-broker"}, {rfRogerBin, "."}} {
			cmd := exec.Command("go", "build", "-o", b[0], b[1])
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				rfBuildErr = fmt.Errorf("build %s: %v\n%s", b[1], err, out)
				return
			}
		}
	})
	if rfBuildErr != nil {
		t.Fatal(rfBuildErr)
	}
}

// rfFreePort reuses the production free-port scan (onboard.go freePort) from a random high
// start, rather than opening a listener of its own: the sharing path's listener call sites
// are pinned as an exact set by web/test/broadcast-gpu-isolation.test.mjs.
func rfFreePort(t *testing.T) int {
	p, err := freePort(20000 + int(time.Now().UnixNano()%20000))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ── recorder: every proxy -> broker chat hop, verbatim ──────────────────────────────────

type rfHop struct {
	header http.Header
	body   []byte
	status int
	resp   http.Header
}

type rfRecorder struct {
	mu   sync.Mutex
	hops []rfHop
	srv  *httptest.Server
}

func newRfRecorder(brokerURL string) *rfRecorder {
	rec := &rfRecorder{}
	target, _ := url.Parse(brokerURL)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ModifyResponse = func(resp *http.Response) error {
		if resp.Request != nil && strings.HasSuffix(resp.Request.URL.Path, "/v1/chat/completions") {
			rec.mu.Lock()
			if n := len(rec.hops); n > 0 {
				rec.hops[n-1].status = resp.StatusCode
				rec.hops[n-1].resp = resp.Header.Clone()
			}
			rec.mu.Unlock()
		}
		return nil
	}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(b))
			r.ContentLength = int64(len(b))
			rec.mu.Lock()
			rec.hops = append(rec.hops, rfHop{header: r.Header.Clone(), body: b})
			rec.mu.Unlock()
		}
		rp.ServeHTTP(w, r)
	}))
	return rec
}

func (r *rfRecorder) chatHops() []rfHop {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rfHop(nil), r.hops...)
}

func (r *rfRecorder) last() (rfHop, error) {
	h := r.chatHops()
	if len(h) == 0 {
		return rfHop{}, fmt.Errorf("no /v1/chat/completions request reached the broker")
	}
	return h[len(h)-1], nil
}

// ── a station: internal/agent.Start against an httptest "local model server" ───────────

type rfStation struct {
	id      string
	quant   string
	sess    *agent.Session
	up      *httptest.Server
	mu      sync.Mutex
	served  []string // the user-message markers this upstream generated for
	stopped bool
	deny    atomic.Bool // answer 429 (Retry-After 1) so the broker fails over past this station
}

func (s *rfStation) sawMarker(m string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.served {
		if strings.Contains(x, m) {
			return true
		}
	}
	return false
}

// ── scenario state ─────────────────────────────────────────────────────────────────────

type rfState struct {
	t *testing.T

	// broker subprocess
	brokerCmd *exec.Cmd
	// brokerEnv are extra env entries for the broker subprocess (e.g. a fast probe interval
	// for a scenario that needs the broker to verify a station's tool calling).
	brokerEnv []string
	// toolStations makes every station's upstream answer the broker's liveness and
	// tool-call canaries the way a tool-capable model would.
	toolStations bool
	brokerURL    string
	adminKey     string
	feeRate      string
	defaultCap   string
	ceiling      string
	brokerLog    bytes.Buffer

	rec      *rfRecorder
	stations []*rfStation
	stationN int
	keyDir   string // XDG for the in-process stations (node.key)

	// the roger subprocess (config dir, last command)
	cfgDir     string
	useCmd     *exec.Cmd
	useOut     *rfBuf
	useErr     *rfBuf
	useExited  chan struct{}
	useExit    error
	proxyPort  int
	sessionKey string

	lastOut    string
	lastErr    string
	lastCode   int
	cfgBefore  []byte
	commandRan bool // a roger command ran in this scenario: "the config has" is then an assertion

	chatSent   int
	responses  []*http.Response
	respBodys  [][]byte
	markers    []string
	liveBefore map[string]float64
}

type rfBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *rfBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *rfBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (s *rfState) reset(t *testing.T) {
	rfBuild(t)
	*s = rfState{t: t, feeRate: "0.30", defaultCap: "10", ceiling: "100"}
	s.keyDir = t.TempDir()
	s.cfgDir = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", s.keyDir) // the in-process stations' node.key lands here
	t.Setenv("ROGER_BROKER", "")
	t.Setenv("NO_COLOR", "1")
}

func (s *rfState) teardown() {
	s.stopUse()
	for _, st := range s.stations {
		if !st.stopped {
			st.sess.Stop()
			st.stopped = true
		}
		st.up.Close()
	}
	if s.rec != nil && s.rec.srv != nil {
		s.rec.srv.Close()
	}
	if s.brokerCmd != nil && s.brokerCmd.Process != nil {
		_ = s.brokerCmd.Process.Kill()
		_ = s.brokerCmd.Wait()
	}
}

// ── Background ─────────────────────────────────────────────────────────────────────────

func (s *rfState) brokerWithEmptyRegistry() error {
	// The broker is started lazily by the first step that needs it so the fee / cap / ceiling
	// Background lines can still set its env first.
	return nil
}
func (s *rfState) feeRateIs(pct int) error {
	s.feeRate = fmt.Sprintf("%.2f", float64(pct)/100)
	return nil
}
func (s *rfState) consumerDefaultCap(v string) error { s.defaultCap = v; return nil }
func (s *rfState) registerCeiling(v string) error    { s.ceiling = v; return nil }

func (s *rfState) ensureBroker() error {
	if s.brokerURL != "" {
		return nil
	}
	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	s.adminKey = hex.EncodeToString(seed)
	port := rfFreePort(s.t)
	s.brokerURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := s.startBrokerProc(port); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(s.brokerURL + "/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				s.rec = newRfRecorder(s.brokerURL)
				s.writeConfig(map[string]any{})
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("broker did not come up: %s", s.brokerLog.String())
}

// startBrokerProc starts the real broker binary on port with the harness env plus brokerEnv.
func (s *rfState) startBrokerProc(port int) error {
	cmd := exec.Command(rfBrokerBin, "-addr", fmt.Sprintf("127.0.0.1:%d", port), "-fee", s.feeRate)
	cmd.Env = append(rfCleanEnv(),
		"BROKER_PRIVATE_KEY="+s.adminKey,
		"ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_OUT="+s.defaultCap,
		"ROGERAI_MAX_PRICE_OUT="+s.ceiling,
		"ROGERAI_MODERATION_MODE=off",
		// A denied station's 429 cools it for 1 s (the stub's Retry-After), so one candidate
		// observation does not bleed into the next step.
		"ROGERAI_STATION_COOLDOWN_DEFAULT=1s",
	)
	cmd.Env = append(cmd.Env, s.brokerEnv...)
	cmd.Stdout = &s.brokerLog
	cmd.Stderr = &s.brokerLog
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start broker: %w", err)
	}
	s.brokerCmd = cmd
	return nil
}

// restartBroker restarts the broker process on the SAME port with brokerEnv, so the recorder
// and every proxy already pointed at it keep working. Only valid before any station registers
// (the in-memory registry starts empty again).
func (s *rfState) restartBroker() error {
	if s.brokerCmd == nil {
		return s.ensureBroker()
	}
	if len(s.stations) > 0 {
		return fmt.Errorf("restartBroker: %d station(s) already registered", len(s.stations))
	}
	_ = s.brokerCmd.Process.Kill()
	_ = s.brokerCmd.Wait()
	u, err := url.Parse(s.brokerURL)
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(u.Port())
	if err := s.startBrokerProc(port); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(s.brokerURL + "/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("broker did not come back: %s", s.brokerLog.String())
}

func rfCleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k := strings.SplitN(kv, "=", 2)[0]
		switch k {
		case "PORT", "DATABASE_URL", "ROGERAI_REDIS_URL", "ROGERAI_MULTI_INSTANCE", "ROGER_BROKER", "XDG_CONFIG_HOME":
			continue
		}
		env = append(env, kv)
	}
	return env
}

// writeConfig writes the roger subprocess's config.json: broker = the recorder, user fixed,
// plus whatever extra top-level keys a scenario asked for (limits...).
func (s *rfState) writeConfig(extra map[string]any) {
	m := map[string]any{"broker": s.rec.srv.URL, "user": "u-cli"}
	if b, err := os.ReadFile(s.cfgPath()); err == nil {
		_ = json.Unmarshal(b, &m)
		m["broker"] = s.rec.srv.URL
	}
	for k, v := range extra {
		m[k] = v
	}
	_ = os.MkdirAll(filepath.Dir(s.cfgPath()), 0700)
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(s.cfgPath(), b, 0600); err != nil {
		s.t.Fatal(err)
	}
}

func (s *rfState) cfgPath() string { return filepath.Join(s.cfgDir, "rogerai", "config.json") }

func (s *rfState) cfgRaw() map[string]any {
	b, err := os.ReadFile(s.cfgPath())
	if err != nil {
		s.t.Fatalf("read config: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		s.t.Fatalf("config.json is not JSON: %v", err)
	}
	return m
}

// cfgGet walks a dotted path with quoted segments allowed: limits.models."qwen3-32b".pref
func rfSplitPath(p string) []string {
	var parts []string
	var cur strings.Builder
	inQ := false
	for _, r := range p {
		switch {
		case r == '"':
			inQ = !inQ
		case r == '.' && !inQ:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, cur.String())
	return parts
}

func rfWalk(m any, path string) (any, bool) {
	cur := m
	for _, seg := range rfSplitPath(path) {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func rfSet(m map[string]any, path string, v any) {
	segs := rfSplitPath(path)
	cur := m
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	cur[segs[len(segs)-1]] = v
}

// ── stations ───────────────────────────────────────────────────────────────────────────

func (s *rfState) startStation(id, model, quant string, priceOut float64) (*rfStation, error) {
	if err := s.ensureBroker(); err != nil {
		return nil, err
	}
	st := &rfStation{id: id, quant: quant}
	st.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(b, &req)
		if s.toolStations && !req.Stream {
			if reply, ok := rfModelAnswer(req.Tools, req.Messages); ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(reply(model))
				return
			}
		}
		if st.deny.Load() {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
			return
		}
		if n := len(req.Messages); n > 0 {
			if c, ok := req.Messages[n-1].Content.(string); ok {
				st.mu.Lock()
				st.served = append(st.served, c)
				st.mu.Unlock()
			}
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"roger\"},\"finish_reason\":null}]}\n\n", model)
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6}}\n\n", model)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"roger"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`, model)
	}))
	sess, err := agent.Start(agent.Config{
		Broker: s.brokerURL, Upstream: st.up.URL + "/v1/chat/completions",
		NodeID: id, Region: "home", HW: "test", Model: model,
		PriceIn: 0, PriceOut: priceOut, Ctx: 8192, Parallel: 2, Quant: quant,
	})
	if err != nil {
		st.up.Close()
		return nil, fmt.Errorf("station %s: %w", id, err)
	}
	st.sess = sess
	s.stations = append(s.stations, st)
	// On air = visible on /discover.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.discoverHas(id) {
			return st, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("station %s never appeared on /discover", id)
}

func (s *rfState) discoverHas(id string) bool {
	resp, err := http.Get(s.brokerURL + "/discover")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return strings.Contains(string(b), `"node_id":"`+id+`"`)
}

func (s *rfState) ensureAnyStation(model string) error {
	if len(s.stations) > 0 {
		return nil
	}
	// `roger use` refuses to open a channel with no station on air, so a scenario that
	// gives none gets one plain free station (no quant label, no capabilities).
	s.stationN++
	_, err := s.startStation(fmt.Sprintf("n-free-%d", s.stationN), model, "", 0)
	return err
}

func (s *rfState) tunedBand(model string) error { return s.ensureAnyStation(model) }

func (s *rfState) nodeOnAirWithQuant(id, model, quant string) error {
	_, err := s.startStation(id, model, quant, 0)
	return err
}
func (s *rfState) nodeOnAirNoQuant(id, model string) error {
	_, err := s.startStation(id, model, "", 0)
	return err
}
func (s *rfState) nodeRegistersAfterProxy(id, model, quant string) error {
	if s.useCmd == nil {
		return fmt.Errorf("the proxy is not running")
	}
	_, err := s.startStation(id, model, quant, 0)
	return err
}

// ── the roger subprocess ───────────────────────────────────────────────────────────────

func rfSplitArgs(line string) []string {
	// A minimal shell-ish split honoring double quotes (the spec writes `--max-out " UNLIMITED "`).
	var out []string
	var cur strings.Builder
	inQ, had := false, false
	for _, r := range line {
		switch {
		case r == '"':
			inQ = !inQ
			had = true
		case r == ' ' && !inQ:
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

func (s *rfState) rogerEnv() []string {
	return append(rfCleanEnv(), "XDG_CONFIG_HOME="+s.cfgDir, "HOME="+s.cfgDir, "NO_COLOR=1")
}

// runOnce execs a short-lived roger command and captures exit code + output.
func (s *rfState) runOnce(args ...string) {
	cmd := exec.Command(rfRogerBin, args...)
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

func (s *rfState) operatorRuns(line string) error {
	args := rfSplitArgs(line)
	if len(args) == 0 || args[0] != "roger" {
		return fmt.Errorf("expected a `roger ...` command, got %q", line)
	}
	args = args[1:]
	if err := s.ensureBroker(); err != nil {
		return err
	}
	if b, err := os.ReadFile(s.cfgPath()); err == nil {
		s.cfgBefore = b
	}
	s.commandRan = true
	if len(args) > 0 && args[0] == "use" && !rfHasFlag(args, "-h") {
		return s.startUse(args)
	}
	s.runOnce(args...)
	return nil
}

func rfHasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f || a == "-"+f || a == "--help" {
			return true
		}
	}
	return false
}

func (s *rfState) startUse(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("roger use needs a model")
	}
	if err := s.ensureAnyStation(args[1]); err != nil {
		return err
	}
	s.stopUse()
	s.proxyPort = 0
	full := append([]string{}, args...)
	if !rfHasFlag(args, "-yes") && !rfHasFlag(args, "--yes") {
		full = append(full, "--yes")
	}
	// No --port: `roger use` binds a free port itself and names it on the plate, so there is
	// no window between picking a port here and the child binding it.
	cmd := exec.Command(rfRogerBin, full...)
	cmd.Env = s.rogerEnv()
	s.useOut, s.useErr = &rfBuf{}, &rfBuf{}
	cmd.Stdout, cmd.Stderr = s.useOut, s.useErr
	if err := cmd.Start(); err != nil {
		return err
	}
	s.useCmd = cmd
	s.useExited = make(chan struct{})
	go func() {
		s.useExit = cmd.Wait()
		close(s.useExited)
	}()
	s.liveBefore = s.adminLive()
	// Ready = the local endpoint answers, or the command exited (a refusal scenario).
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-s.useExited:
			s.snapshotUse()
			return nil
		default:
		}
		if pm := regexp.MustCompile(`BASE URL\s+http://127\.0\.0\.1:(\d+)/v1`).FindStringSubmatch(s.useOut.String()); pm != nil {
			s.proxyPort, _ = strconv.Atoi(pm[1])
		}
		if s.proxyPort == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.proxyPort), 100*time.Millisecond); err == nil {
			c.Close()
			m := regexp.MustCompile(`API KEY\s+(\S+)`).FindStringSubmatch(s.useOut.String())
			if m != nil {
				s.sessionKey = m[1]
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("roger use neither opened the channel nor exited in time\nstdout:\n%s\nstderr:\n%s", s.useOut.String(), s.useErr.String())
}

// snapshotUse copies a finished `roger use` run into lastOut/lastErr/lastCode.
func (s *rfState) snapshotUse() {
	s.lastOut, s.lastErr, s.lastCode = s.useOut.String(), s.useErr.String(), 0
	if s.useExit != nil {
		if ee, ok := s.useExit.(*exec.ExitError); ok {
			s.lastCode = ee.ExitCode()
		} else {
			s.lastCode = -1
		}
	}
}

func (s *rfState) stopUse() {
	if s.useCmd == nil || s.useCmd.Process == nil {
		return
	}
	select {
	case <-s.useExited:
	default:
		_ = s.useCmd.Process.Kill()
		<-s.useExited
	}
	s.useCmd = nil
}

func (s *rfState) useIsRunning(line string) error { return s.operatorRuns(line) }

// ── chat through the local proxy ───────────────────────────────────────────────────────

func (s *rfState) sendChat() error {
	if s.useCmd == nil {
		return fmt.Errorf("no `roger use` is running")
	}
	select {
	case <-s.useExited:
		s.snapshotUse()
		return fmt.Errorf("roger use exited (code %d) before the chat request:\n%s%s", s.lastCode, s.lastOut, s.lastErr)
	default:
	}
	s.chatSent++
	marker := fmt.Sprintf("marker-%d-%d", os.Getpid(), s.chatSent)
	body := fmt.Sprintf(`{"model":"qwen3-32b","messages":[{"role":"user","content":"say roger %s"}],"max_tokens":8}`, marker)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", s.proxyPort), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.sessionKey)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s.responses = append(s.responses, resp)
	s.respBodys = append(s.respBodys, b)
	s.markers = append(s.markers, marker)
	return nil
}

func (s *rfState) chatGoesThroughProxy() error {
	if s.useCmd == nil {
		// A scenario that gives config + stations and goes straight to "a chat request goes
		// through the local proxy" means the plain `roger use qwen3-32b` proxy.
		if err := s.operatorRuns("roger use qwen3-32b"); err != nil {
			return err
		}
	}
	return s.sendChat()
}

// ── /admin/live ────────────────────────────────────────────────────────────────────────

func (s *rfState) adminLive() map[string]float64 {
	out := map[string]float64{}
	if s.brokerURL == "" {
		return out
	}
	req, _ := http.NewRequest(http.MethodGet, s.brokerURL+"/admin/live", nil)
	req.Header.Set("X-Roger-Admin", s.adminKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				walk(k, vv)
			}
		case float64:
			out[prefix] = x
		}
	}
	walk("", m)
	return out
}

// ── Then: the wire (recorder) ──────────────────────────────────────────────────────────

func (s *rfState) lastBody() (map[string]any, error) {
	h, err := s.rec.last()
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(h.body, &m); err != nil {
		return nil, fmt.Errorf("broker-bound body is not JSON: %v", err)
	}
	return m, nil
}

func (s *rfState) bodyCarriesString(key, want string) error {
	m, err := s.lastBody()
	if err != nil {
		return err
	}
	v, ok := rfWalk(m, key)
	if !ok {
		return fmt.Errorf("request body carries no %s (keys: %v)", key, rfKeys(m))
	}
	if fmt.Sprint(v) != want {
		return fmt.Errorf("%s = %v, want %q", key, v, want)
	}
	return nil
}

func (s *rfState) bodyCarriesNo(key string) error {
	m, err := s.lastBody()
	if err != nil {
		return err
	}
	if v, ok := rfWalk(m, key); ok {
		return fmt.Errorf("request body carries %s = %v, want absent", key, v)
	}
	return nil
}

func (s *rfState) bodyCarriesList(key, list string) error {
	m, err := s.lastBody()
	if err != nil {
		return err
	}
	var want []string
	if err := json.Unmarshal([]byte(list), &want); err != nil {
		return fmt.Errorf("bad list in step: %v", err)
	}
	v, ok := rfWalk(m, key)
	if !ok {
		return fmt.Errorf("request body carries no %s (keys: %v)", key, rfKeys(m))
	}
	got, _ := json.Marshal(v)
	wantB, _ := json.Marshal(want)
	if string(got) != string(wantB) {
		return fmt.Errorf("%s = %s, want %s", key, got, wantB)
	}
	return nil
}

func rfKeys(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func (s *rfState) bodyCarriesPrefTpsConf(pref string, tps int, conf string) error {
	if err := s.bodyCarriesString("roger.pref", pref); err != nil {
		return err
	}
	if err := s.bodyCarriesString("roger.min_tps", strconv.Itoa(tps)); err != nil {
		return err
	}
	return s.bodyCarriesString("roger.confidential", conf)
}

func (s *rfState) headerIs(name, want string) error {
	if s.chatSent == 0 {
		// A scenario that asserts the wire without a chat step: send one.
		if err := s.sendChat(); err != nil {
			return err
		}
	}
	h, err := s.rec.last()
	if err != nil {
		return err
	}
	if got := h.header.Get(name); got != want {
		return fmt.Errorf("header %s = %q, want %q", name, got, want)
	}
	return nil
}

func (s *rfState) headerPresent(name string) error {
	h, err := s.rec.last()
	if err != nil {
		return err
	}
	if h.header.Get(name) == "" {
		return fmt.Errorf("header %s absent", name)
	}
	return nil
}

func (s *rfState) headersAbsent(list string) error {
	h, err := s.rec.last()
	if err != nil {
		return err
	}
	for _, n := range regexp.MustCompile(`X-Roger[A-Za-z-]+`).FindAllString(list, -1) {
		if v := h.header.Get(n); v != "" {
			return fmt.Errorf("header %s = %q, want absent", n, v)
		}
	}
	return nil
}

func (s *rfState) noQuantExclude() error {
	h, err := s.rec.last()
	if err != nil {
		return err
	}
	if v := h.header.Get("X-Roger-Exclude-Nodes"); v != "" {
		return fmt.Errorf("X-Roger-Exclude-Nodes = %q (quant expressed as an exclude list)", v)
	}
	return nil
}

func (s *rfState) noRequestReachedBroker() error {
	if n := len(s.rec.chatHops()); n != 0 {
		return fmt.Errorf("%d chat request(s) reached the broker", n)
	}
	return nil
}

// ── Then: the routing pass (real broker, /admin/live counters) ────────────────────────

func (s *rfState) routingPassRanWith(pref string) error {
	if s.chatSent == 0 {
		if err := s.sendChat(); err != nil {
			return err
		}
	}
	key := "routing_pref_" + pref
	after := s.adminLive()
	got, ok := after[key]
	if !ok {
		return fmt.Errorf("/admin/live has no %s counter (the routing pass is not observable by pref today)", key)
	}
	if got-s.liveBefore[key] < 1 {
		return fmt.Errorf("%s did not increase (before %v, after %v)", key, s.liveBefore[key], got)
	}
	return nil
}

func (s *rfState) bodyPrefAndPass(pref string) error {
	if err := s.bodyCarriesString("roger.pref", pref); err != nil {
		return err
	}
	return s.routingPassRanWith(pref)
}

func (s *rfState) effectiveOutCap(v string) error {
	// PARTLY OBSERVABLE: the broker exposes no per-request effective cap. This asserts the
	// wire value the broker was sent equals the expected effective value; the "honored as
	// sent between the default and the ceiling" rule itself is pinned by the @broker runner.
	return s.headerIs("X-Roger-Max-Price-Out", v)
}

// probeCandidate answers "is <id> eligible for this request?" through the REAL broker: every
// OTHER station on air answers 429 for one request, so the broker's own failover plan reaches
// <id> iff <id> survives the routing filters. (A plain sample cannot tell: with two
// candidates, power-of-two-choices deterministically serves the better score.) The denied
// stations cool for 1 s (their Retry-After), and the step waits that out before returning.
func (s *rfState) probeCandidate(id string) (string, error) {
	if s.useCmd == nil {
		if err := s.operatorRuns("roger use qwen3-32b"); err != nil {
			return "", err
		}
	}
	for _, st := range s.stations {
		if st.id != id {
			st.deny.Store(true)
		}
	}
	err := s.sendChat()
	for _, st := range s.stations {
		st.deny.Store(false)
	}
	if err != nil {
		return "", err
	}
	served := s.responses[len(s.responses)-1].Header.Get("X-RogerAI-Provider")
	time.Sleep(1200 * time.Millisecond) // let the denied stations' cooldown expire
	return served, nil
}

func (s *rfState) notCandidate(id string) error {
	served, err := s.probeCandidate(id)
	if err != nil {
		return err
	}
	if served == id {
		return fmt.Errorf("%s served with every other station refusing, so it was a candidate", id)
	}
	for _, st := range s.stations {
		if st.id == id && st.sawMarker(s.markers[len(s.markers)-1]) {
			return fmt.Errorf("%s's upstream generated for the request, so it was a candidate", id)
		}
	}
	return nil
}

func (s *rfState) isCandidate(id string) error {
	served, err := s.probeCandidate(id)
	if err != nil {
		return err
	}
	if served != id {
		r := s.responses[len(s.responses)-1]
		return fmt.Errorf("%s did not serve with every other station refusing (status %d, served by %q), so it was not a candidate", id, r.StatusCode, served)
	}
	return nil
}

func (s *rfState) serves(id string) error { return s.isCandidate(id) }

// ── Then: the CLI itself ───────────────────────────────────────────────────────────────

func (s *rfState) exitsNonZero() error {
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

func (s *rfState) messageNamesFourValues() error {
	out := s.lastOut + s.lastErr
	for _, v := range []string{"cheap", "balanced", "fast", "reliable"} {
		if !strings.Contains(out, v) {
			return fmt.Errorf("message does not name %q:\n%s", v, out)
		}
	}
	return nil
}

func (s *rfState) messageNames(want string) error {
	out := s.lastOut + s.lastErr
	if s.useCmd != nil {
		select {
		case <-s.useExited:
			s.snapshotUse()
			out = s.lastOut + s.lastErr
		default:
		}
	}
	if !strings.Contains(out, want) {
		return fmt.Errorf("message does not contain %q:\n%s", want, out)
	}
	return nil
}

func (s *rfState) cliPrints(want string) error {
	out := s.useOut.String() + s.useErr.String()
	if !strings.Contains(out, want) {
		return fmt.Errorf("CLI output lacks %q:\n%s", want, out)
	}
	return nil
}

func (s *rfState) connectLineSays(want string) error { return s.cliPrints(want) }

func (s *rfState) helpDescribesMaxOut(cmdline string) error {
	args := rfSplitArgs(cmdline)[1:]
	s.runOnce(args...)
	out := s.lastOut + s.lastErr
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "max-out") {
			line += l + "\n"
		}
	}
	if line == "" {
		return fmt.Errorf("`%s` printed no --max-out help:\n%s", cmdline, out)
	}
	if !strings.Contains(line, "the default $10/1M cap") {
		return fmt.Errorf("--max-out help does not say \"the default $10/1M cap\": %s", line)
	}
	if strings.Contains(line, "no cap") {
		return fmt.Errorf("--max-out help still says \"no cap\": %s", line)
	}
	return nil
}

func (s *rfState) useHelpDescribes() error { return s.helpDescribesMaxOut("roger use -h") }
func (s *rfState) setLimitHelpDescribes() error {
	return s.helpDescribesMaxOut("roger config set-limit -h")
}

func (s *rfState) limitsShowsPref() error {
	s.runOnce("limits")
	if !strings.Contains(s.lastOut, "pref=reliable") {
		return fmt.Errorf("`roger limits` does not show the pref beside the limits:\n%s", s.lastOut)
	}
	return nil
}

func (s *rfState) limitsShows(want string) error {
	s.runOnce("limits")
	if !strings.Contains(s.lastOut, want) {
		return fmt.Errorf("`roger limits` lacks %q:\n%s", want, s.lastOut)
	}
	return nil
}

// ── config.json ────────────────────────────────────────────────────────────────────────

func (s *rfState) configHasString(path, v string) error {
	if err := s.ensureBroker(); err != nil {
		return err
	}
	if s.commandRan {
		// After a command this is the assertion form ("Then the config has ...").
		return s.configSaysString(path, v)
	}
	m := s.cfgRaw()
	rfSet(m, path, v)
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(s.cfgPath(), b, 0600)
}

func (s *rfState) configHasList(path, list string) error {
	if err := s.ensureBroker(); err != nil {
		return err
	}
	var v []string
	if err := json.Unmarshal([]byte(list), &v); err != nil {
		return err
	}
	m := s.cfgRaw()
	rfSet(m, path, v)
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(s.cfgPath(), b, 0600)
}

func (s *rfState) configSaysString(path, want string) error {
	v, ok := rfWalk(s.cfgRaw(), path)
	if !ok {
		return fmt.Errorf("config has no %s", path)
	}
	if fmt.Sprint(v) != want {
		return fmt.Errorf("config %s = %v, want %q", path, v, want)
	}
	return nil
}

func (s *rfState) configSaysNumber(path string, want float64) error {
	v, ok := rfWalk(s.cfgRaw(), path)
	if !ok {
		return fmt.Errorf("config has no %s", path)
	}
	f, _ := v.(float64)
	if f != want {
		return fmt.Errorf("config %s = %v, want %v", path, v, want)
	}
	return nil
}

func (s *rfState) persistedStillList(list string) error {
	// The only quant rule a scenario sets is limits.models."qwen3-32b".quants.
	v, ok := rfWalk(s.cfgRaw(), `limits.models."qwen3-32b".quants`)
	if !ok {
		return fmt.Errorf("config lost limits.models.qwen3-32b.quants")
	}
	got, _ := json.Marshal(v)
	if string(got) != list {
		return fmt.Errorf("persisted quants = %s, want %s", got, list)
	}
	return nil
}

func (s *rfState) configUnchanged() error {
	b, err := os.ReadFile(s.cfgPath())
	if err != nil {
		return err
	}
	if !bytes.Equal(b, s.cfgBefore) {
		return fmt.Errorf("config changed:\nbefore:\n%s\nafter:\n%s", s.cfgBefore, b)
	}
	return nil
}

// ── runner ─────────────────────────────────────────────────────────────────────────────

func TestRoutingFlagsPins(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real broker + roger binaries")
	}
	s := &rfState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.teardown()
				return ctx, nil
			})

			// Background
			sc.Step(`^a broker with an empty in-memory node registry$`, s.brokerWithEmptyRegistry)
			sc.Step(`^the fee rate is (\d+)%$`, s.feeRateIs)
			sc.Step(`^the consumer default out-cap is \$([0-9.]+)/1M$`, s.consumerDefaultCap)
			sc.Step(`^the register out-price ceiling is \$([0-9.]+)/1M$`, s.registerCeiling)

			// Given
			sc.Step(`^a tuned band whose model is "([^"]+)"$`, s.tunedBand)
			sc.Step(`^the config has (limits\.[^ ]+) "([^"]+)"$`, s.configHasString)
			sc.Step(`^the config has (limits\.[^ ]+) (\[.*\])$`, s.configHasList)
			sc.Step(`^node "([^"]+)" is on air for "([^"]+)" with quant "([^"]+)"$`, s.nodeOnAirWithQuant)
			sc.Step(`^node "([^"]+)" is on air for "([^"]+)" with no quant label$`, s.nodeOnAirNoQuant)

			// When
			sc.Step("^the operator runs `([^`]+)`$", s.operatorRuns)
			sc.Step("^`([^`]+)` is running$", s.useIsRunning)
			sc.Step(`^a chat request goes through the local proxy$`, s.chatGoesThroughProxy)
			sc.Step(`^node "([^"]+)" registers "([^"]+)" with quant "([^"]+)" after the proxy started$`, s.nodeRegistersAfterProxy)

			// Then: wire
			sc.Step(`^the request body carries (roger\.[a-z_]+) "([^"]+)"$`, s.bodyCarriesString)
			sc.Step(`^the request body carries no (roger\.[a-z_]+)$`, s.bodyCarriesNo)
			sc.Step(`^the request carries no (X-Roger-Pref) header$`, s.headersAbsent)
			sc.Step(`^the request carries no (X-Roger-[A-Za-z-]+, X-Roger-[A-Za-z-]+ or X-Roger-[A-Za-z-]+) header$`, s.headersAbsent)
			sc.Step(`^the request carries the header (X-Roger-[A-Za-z-]+)$`, s.headerPresent)
			sc.Step(`^the request body carries roger\.pref "([^"]+)", roger\.min_tps (\d+) and roger\.confidential (true|false)$`, s.bodyCarriesPrefTpsConf)
			sc.Step(`^the broker receives the header (X-Roger-[A-Za-z-]+) "([^"]+)"$`, s.headerIs)
			sc.Step(`^the request carries body (roger\.[a-z_]+) (true|false)$`, s.bodyCarriesString)
			sc.Step(`^the request carries body (provider\.quantizations) (\[.*\])$`, s.bodyCarriesList)
			sc.Step(`^the request body carries (provider\.quantizations) (\[.*\])$`, s.bodyCarriesList)
			sc.Step(`^the request carries no (provider\.[a-z_]+) key$`, s.bodyCarriesNo)
			sc.Step(`^the request carries no X-Roger-Exclude-Nodes derived from quant$`, s.noQuantExclude)
			sc.Step(`^no request reaches the broker$`, s.noRequestReachedBroker)

			// Then: routing pass (real broker)
			sc.Step(`^the broker's routing pass ran with the ([a-z]+) profile$`, s.routingPassRanWith)
			sc.Step(`^the request body carries roger\.pref "([^"]+)" and the broker's routing pass ran with the [a-z]+ profile$`, s.bodyPrefAndPass)
			sc.Step(`^the broker's effective out-cap is \$([0-9.]+)/1M$`, s.effectiveOutCap)
			sc.Step(`^"([^"]+)" is NOT a candidate$`, s.notCandidate)
			sc.Step(`^"([^"]+)" is a candidate$`, s.isCandidate)
			sc.Step(`^"([^"]+)" serves$`, s.serves)

			// Then: CLI
			sc.Step(`^the command exits non-zero$`, s.exitsNonZero)
			sc.Step(`^the message names the four accepted values$`, s.messageNamesFourValues)
			sc.Step(`^the message names "([^"]+)"$`, s.messageNames)
			sc.Step(`^the CLI prints "([^"]+)"$`, s.cliPrints)
			sc.Step(`^the connect line says "([^"]+)"$`, s.connectLineSays)
			sc.Step("^`roger use -h` describes --max-out 0 as \"the default \\$10/1M cap\", not \"no cap\"$", s.useHelpDescribes)
			sc.Step("^`roger config set-limit -h` describes --max-out 0 the same way$", s.setLimitHelpDescribes)
			sc.Step("^`roger limits` shows the pref beside the price and tps limits$", s.limitsShowsPref)
			sc.Step("^`roger limits` shows \"([^\"]+)\" for it$", s.limitsShows)

			// Then: config
			sc.Step(`^the persisted config still says (limits\.[^ ]+) "([^"]+)"$`, s.configSaysString)
			sc.Step(`^the config has (limits\.[^ ]+) ([0-9.]+)$`, s.configSaysNumber)
			sc.Step(`^the persisted config still says (\[.*\])$`, s.persistedStillList)
			sc.Step(`^the config is unchanged$`, s.configUnchanged)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/regression_pins.feature"},
			Tags:     "@cli",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("features/routing/regression_pins.feature (@cli): failing scenarios")
	}
}

// rfModelAnswer answers the broker's canaries the way a capable model would: a tool-call
// request is answered with a call to the offered function carrying the token the prompt names
// (the nonce is the function name's suffix), and a liveness prompt ("Reply with only the single
// word: X", a small sum) with its answer. Anything else is not handled (ok=false).
func rfModelAnswer(tools []struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}, msgs []struct {
	Content any `json:"content"`
}) (func(model string) []byte, bool) {
	last := ""
	if n := len(msgs); n > 0 {
		last, _ = msgs[n-1].Content.(string)
	}
	if len(tools) > 0 && tools[0].Function.Name != "" {
		name := tools[0].Function.Name
		token := name
		if i := strings.LastIndex(name, "_"); i >= 0 {
			token = name[i+1:]
		}
		args, _ := json.Marshal(map[string]string{"token": token})
		return func(model string) []byte {
			b, _ := json.Marshal(map[string]any{
				"id": "c1", "object": "chat.completion", "model": model,
				"choices": []map[string]any{{"index": 0, "finish_reason": "tool_calls",
					"message": map[string]any{"role": "assistant", "content": nil,
						"tool_calls": []map[string]any{{"id": "call_1", "type": "function",
							"function": map[string]string{"name": name, "arguments": string(args)}}}}}},
				"usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
			})
			return b
		}, true
	}
	answer := ""
	switch {
	case strings.Contains(last, "two plus three"):
		answer = "5"
	case strings.Contains(last, "seven minus four"):
		answer = "3"
	default:
		if m := regexp.MustCompile(`:\s*([A-Z]+)\s*$`).FindStringSubmatch(last); m != nil {
			answer = strings.ToLower(m[1])
		}
	}
	if answer == "" {
		return nil, false
	}
	return func(model string) []byte {
		b, _ := json.Marshal(map[string]any{
			"id": "c1", "object": "chat.completion", "model": model,
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": answer}}},
			"usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 1, "total_tokens": 13},
		})
		return b
	}, true
}
