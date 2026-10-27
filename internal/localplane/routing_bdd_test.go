package localplane

// Executable spec: features/tower/localplane_routing.feature (slice 4 of the
// routing-expression set, CONTRACT §5a).
//
// The REAL plane, no mocks: a tower.State on disk, this package's real Handler, an admitted
// client and attached stations with real ed25519 keys. Every station is a goroutine that
// long-polls /local/poll through the handler exactly as `roger share` does, records the job body
// the plane handed it, and completes with an answer that names the station. A consumer request
// is driven in-process through the same handler, so the assertions read the actual wire bytes:
// the job body a station received, the response headers, the persisted local receipts.
//
// Requests are sent LAZILY: a When records the request and the first Then sends it, so an `And
// "s2" never polls` written after the When still takes effect before the plane routes anything.
// Each request carries a unique marker in its messages, so the job a station received is matched
// to the request that produced it.
//
// Error shapes: the plane's approved refusals are {"error": "<text>"}; the contract's coded
// refusals (no_match, unknown_profile) are read as {"error": {"code", "message"}} or, equally,
// {"error": "<message>", "code": "<code>"} - the scenario's claim is the code and the message,
// not the envelope nesting, which the GREEN slice chooses.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"rogerai.fm/roger/v6/internal/bddtest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

const (
	tp4PollTimeout       = 200 * time.Millisecond
	tp4CompletionTimeout = 1500 * time.Millisecond
	tp4PublicNodeID      = "n-pub-7f3a9c41d2"
	tp4FreqCode          = "ZZQ7-9QX2"
)

type tp4Station struct {
	id      string
	priv    ed25519.PrivateKey
	models  []string
	curated string

	mu      sync.Mutex
	polling bool
	cancel  context.CancelFunc
	empty   int // 204 polls seen
}

type tp4Job struct {
	station string
	model   string
	body    []byte // the request the plane handed the station
	at      time.Time
}

type tp4Resp struct {
	code    int
	hdr     http.Header
	body    []byte
	elapsed time.Duration
}

type tp4State struct {
	smokeOut string // the standalone smoke script's output
	t        *testing.T
	st       *tower.State
	srv      *Server
	client   ed25519.PrivateKey

	stations map[string]*tp4Station
	order    []string

	mu   sync.Mutex
	jobs []tp4Job

	// the pending (lazy) request
	reqBody    []byte
	reqHdr     map[string]string
	reqPriv    ed25519.PrivateKey
	reqMarker  string
	sent       bool
	resp       tp4Resp
	pendingSaw map[string]bool // models seen pending in the queue while the request ran
	seq        int

	cfgPath  string
	cfgBytes []byte
	logs     *bytes.Buffer
	prevLog  *os.File
}

func (s *tp4State) reset() {
	s.stopAll()
	s.st, s.srv, s.client = nil, nil, nil
	s.stations, s.order = map[string]*tp4Station{}, nil
	s.jobs = nil
	s.reqBody, s.reqHdr, s.reqPriv, s.reqMarker, s.sent = nil, nil, nil, "", false
	s.resp, s.pendingSaw = tp4Resp{}, nil
	s.cfgPath, s.cfgBytes = "", nil
	s.logs = &bytes.Buffer{}
	log.SetOutput(s.logs)
}

func (s *tp4State) stopAll() {
	for _, st := range s.stations {
		s.stopStation(st)
	}
}

func (s *tp4State) stopStation(st *tp4Station) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
	st.polling = false
}

// --- the station loop ----------------------------------------------------------------------

func (s *tp4State) startStation(st *tp4Station) {
	st.mu.Lock()
	if st.polling {
		st.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.cancel, st.polling = cancel, true
	st.mu.Unlock()
	srv := s.srv
	go func() {
		for ctx.Err() == nil {
			rec := httptest.NewRecorder()
			req := signedBody(s.t, st.priv, http.MethodPost, "/local/poll", nil).WithContext(ctx)
			srv.Handler().ServeHTTP(rec, req)
			if ctx.Err() != nil {
				return
			}
			switch rec.Code {
			case http.StatusNoContent:
				st.mu.Lock()
				st.empty++
				st.mu.Unlock()
				continue
			case http.StatusOK:
			default:
				time.Sleep(20 * time.Millisecond)
				continue
			}
			var job struct {
				JobID   string          `json:"job_id"`
				Model   string          `json:"model"`
				Request json.RawMessage `json:"request"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
				continue
			}
			s.mu.Lock()
			s.jobs = append(s.jobs, tp4Job{station: st.id, model: job.Model, body: append([]byte(nil), job.Request...), at: time.Now()})
			s.mu.Unlock()
			// The answer names the station so a test can tell who served without the plane's help.
			answer := fmt.Sprintf(`{"id":"cmpl-%s","model":%q,"choices":[{"message":{"role":"assistant","content":"served by %s"}}]}`,
				job.JobID, job.Model, st.id)
			comp, _ := json.Marshal(map[string]any{"job_id": job.JobID, "answer": json.RawMessage(answer)})
			crec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(crec, signedBody(s.t, st.priv, http.MethodPost, "/local/complete", comp))
		}
	}()
}

// --- Background ------------------------------------------------------------------------------

func (s *tp4State) standaloneTower(client string) error {
	s.st = standaloneState(s.t)
	s.client = admitClient(s.t, s.st)
	s.srv = New(s.st)
	s.srv.pollTimeout = tp4PollTimeout
	s.srv.completionTimeout = tp4CompletionTimeout
	return nil
}

var tp4StationRE = regexp.MustCompile(`"([^"]+)" \(models ([^)]*?)(?:, curated "([^"]+)")?\)`)

func (s *tp4State) localStations(spec string) error {
	ms := tp4StationRE.FindAllStringSubmatch(spec, -1)
	if len(ms) == 0 {
		return fmt.Errorf("could not parse station list %q", spec)
	}
	for _, m := range ms {
		id, models, curated := m[1], strings.Split(m[2], ", "), m[3]
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		keyHash := protocol.UserIDFromPubkey(hexPub(pub))
		if curated != "" {
			_, err = s.st.AttachCuratedStation(id, keyHash, models, curated)
		} else {
			_, err = s.st.AttachStation(id, keyHash, models)
		}
		if err != nil {
			return err
		}
		s.stations[id] = &tp4Station{id: id, priv: priv, models: models, curated: curated}
		s.order = append(s.order, id)
	}
	return nil
}

func (s *tp4State) everyStationPolling() error {
	for _, id := range s.order {
		s.startStation(s.stations[id])
	}
	return nil
}

// --- request building and sending ----------------------------------------------------------

func (s *tp4State) nextMarker() string {
	s.seq++
	return fmt.Sprintf("tp4-req-%d-%d", time.Now().UnixNano(), s.seq)
}

// expand turns the spec's shorthand body into real JSON: `[...]` becomes a one-message array
// carrying the request marker, `<a public node id>` / `<public node id>` a public-looking node
// id, and the `…` freq placeholder a band code.
func (s *tp4State) expand(body string) (string, []byte, error) {
	marker := s.nextMarker()
	b := strings.ReplaceAll(body, "[...]", fmt.Sprintf(`[{"role":"user","content":%q}]`, marker))
	b = strings.ReplaceAll(b, "<a public node id>", tp4PublicNodeID)
	b = strings.ReplaceAll(b, "<public node id>", tp4PublicNodeID)
	b = strings.ReplaceAll(b, `"…"`, fmt.Sprintf(`"147.520 MHz %s"`, tp4FreqCode))
	if !json.Valid([]byte(b)) {
		return "", nil, fmt.Errorf("the scenario body is not valid JSON after expansion: %s", b)
	}
	return marker, []byte(b), nil
}

func (s *tp4State) queue(priv ed25519.PrivateKey, marker string, body []byte, hdr map[string]string) {
	s.reqPriv, s.reqMarker, s.reqBody, s.reqHdr = priv, marker, body, hdr
	s.sent = false
}

// send performs the pending request through the real handler, sampling the queue while it runs
// so "nothing is queued" / "no job was submitted for X" read what the plane actually did.
func (s *tp4State) send() error {
	if s.sent {
		return nil
	}
	if s.reqBody == nil && s.reqPriv == nil {
		return fmt.Errorf("no request was posted in this scenario")
	}
	s.sent = true
	s.resp = s.do(s.reqPriv, s.reqBody, s.reqHdr, nil)
	return nil
}

func (s *tp4State) do(priv ed25519.PrivateKey, body []byte, hdr map[string]string, ctx context.Context) tp4Resp {
	req := signedBody(s.t, priv, http.MethodPost, "/v1/chat/completions", body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	s.pendingSaw = map[string]bool{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			s.srv.q.mu.Lock()
			for _, j := range s.srv.q.pending {
				s.mu.Lock()
				s.pendingSaw[j.model] = true
				s.mu.Unlock()
			}
			for _, j := range s.srv.q.inflight {
				s.mu.Lock()
				s.pendingSaw[j.model] = true
				s.mu.Unlock()
			}
			s.srv.q.mu.Unlock()
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	start := time.Now()
	rec := httptest.NewRecorder()
	s.srv.Handler().ServeHTTP(rec, req)
	el := time.Since(start)
	close(stop)
	wg.Wait()
	return tp4Resp{code: rec.Code, hdr: rec.Header().Clone(), body: rec.Body.Bytes(), elapsed: el}
}

// jobsFor returns the jobs any station received for the request carrying marker.
func (s *tp4State) jobsFor(marker string) []tp4Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []tp4Job
	for _, j := range s.jobs {
		if bytes.Contains(j.body, []byte(marker)) {
			out = append(out, j)
		}
	}
	return out
}

func (s *tp4State) servedBy() string {
	m := regexp.MustCompile(`served by ([a-z0-9-]+)`).FindSubmatch(s.resp.body)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func (s *tp4State) errText() string {
	var v map[string]any
	if json.Unmarshal(s.resp.body, &v) != nil {
		return string(s.resp.body)
	}
	switch e := v["error"].(type) {
	case string:
		return e
	case map[string]any:
		if m, ok := e["message"].(string); ok {
			return m
		}
	}
	return string(s.resp.body)
}

func (s *tp4State) errCode() string {
	var v map[string]any
	if json.Unmarshal(s.resp.body, &v) != nil {
		return ""
	}
	if e, ok := v["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	if c, ok := v["code"].(string); ok {
		return c
	}
	return ""
}

func (s *tp4State) brief() string {
	return fmt.Sprintf("status %d, body %s, headers %v", s.resp.code, strings.TrimSpace(string(s.resp.body)), s.resp.hdr)
}

// --- When ------------------------------------------------------------------------------------

func (s *tp4State) posts(client, body string) error {
	marker, b, err := s.expand(body)
	if err != nil {
		return err
	}
	s.queue(s.client, marker, b, nil)
	return nil
}

func (s *tp4State) postsWithConfidential(client, body, v string) error {
	marker, b, err := s.expand(body)
	if err != nil {
		return err
	}
	s.queue(s.client, marker, b, map[string]string{"X-Roger-Confidential": v})
	return nil
}

func (s *tp4State) unadmittedPostsRouting() error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	marker, b, err := s.expand(`{"model": "qwen3-32b", "provider": "not-an-object", "messages": [...]}`)
	if err != nil {
		return err
	}
	s.queue(priv, marker, b, nil)
	return s.send()
}

func (s *tp4State) exceedsConcurrency(client string) error {
	s.stopAll() // no station claims, so every request holds its slot until the timeout
	n := defaultMaxInFlightPerClient + 1
	results := make(chan tp4Resp, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		_, b, err := s.expand(`{"model": "qwen3-32b", "provider": {"only": ["s1"]}, "messages": [...]}`)
		if err != nil {
			return err
		}
		wg.Add(1)
		go func(b []byte) {
			defer wg.Done()
			req := signedBody(s.t, s.client, http.MethodPost, "/v1/chat/completions", b)
			rec := httptest.NewRecorder()
			s.srv.Handler().ServeHTTP(rec, req)
			results <- tp4Resp{code: rec.Code, hdr: rec.Header(), body: rec.Body.Bytes()}
		}(b)
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	close(results)
	s.sent = true
	for r := range results {
		if r.code == http.StatusTooManyRequests {
			s.resp = r
			return nil
		}
	}
	s.resp = tp4Resp{}
	return nil
}

func (s *tp4State) consoleSpendSet() error {
	dir := s.t.TempDir()
	s.cfgPath = filepath.Join(dir, "rogerai", "config.json")
	if err := os.MkdirAll(filepath.Dir(s.cfgPath), 0o700); err != nil {
		return err
	}
	s.cfgBytes = []byte(`{"limits":{"monthly_cap":42.5,"default":{"max_out":3}}}` + "\n")
	return os.WriteFile(s.cfgPath, s.cfgBytes, 0o600)
}

func (s *tp4State) routingRequestsServed() error {
	for _, body := range []string{
		`{"model": "qwen3-32b", "provider": {"only": ["s2"]}, "messages": [...]}`,
		`{"model": "gpt-4o", "models": ["qwen3-32b"], "roger": {"pref": "fast"}, "messages": [...]}`,
	} {
		_, b, err := s.expand(body)
		if err != nil {
			return err
		}
		_ = s.do(s.client, b, nil, nil)
	}
	return nil
}

func (s *tp4State) noStationClaimsWithin(model string) error {
	s.stopAll()
	s.srv.completionTimeout = 300 * time.Millisecond
	return nil
}

func (s *tp4State) sixModels(client string) error {
	marker, b, err := s.expand(`{"model": "m1", "models": ["m2", "m3", "m4", "m5", "qwen3-32b"], "messages": [...]}`)
	if err != nil {
		return err
	}
	s.queue(s.client, marker, b, nil)
	return nil
}

func (s *tp4State) neverPolls(id string) error {
	st, ok := s.stations[id]
	if !ok {
		return fmt.Errorf("no station %q", id)
	}
	s.stopStation(st)
	return nil
}

func (s *tp4State) postsMalformedCarrier(client string) error {
	marker, b, err := s.expand(`{"model": "qwen3-32b", "provider": {"only": "s1"}, "messages": [...]}`)
	if err != nil {
		return err
	}
	s.queue(s.client, marker, b, nil)
	return nil
}

func (s *tp4State) postsOverSize(client string) error {
	pad := strings.Repeat("x", maxPromptBody+1024)
	body := []byte(fmt.Sprintf(`{"model":"qwen3-32b","provider":{"only":["s1"]},"messages":[{"role":"user","content":%q}]}`, pad))
	s.queue(s.client, "", body, nil)
	return nil
}

func (s *tp4State) anyRoutedRequestServed() error {
	marker, b, err := s.expand(`{"model": "qwen3-32b", "provider": {"only": ["s1"]}, "messages": [...]}`)
	if err != nil {
		return err
	}
	s.queue(s.client, marker, b, nil)
	return s.send()
}

func (s *tp4State) disconnectsAfterDelivered(client, station string) error {
	marker, b, err := s.expand(`{"model": "gpt-4o", "models": ["qwen3-32b"], "messages": [...]}`)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// cancel as soon as a station has delivered this request's answer
		deadline := time.Now().Add(tp4CompletionTimeout)
		for time.Now().Before(deadline) {
			if len(s.jobsFor(marker)) > 0 {
				time.Sleep(5 * time.Millisecond)
				cancel()
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		cancel()
	}()
	s.reqMarker, s.reqBody, s.sent = marker, b, true
	s.resp = s.do(s.client, b, nil, ctx)
	return nil
}

func (s *tp4State) everyRoutingKey(client string) error {
	marker, b, err := s.expand(`{"model": "qwen3-32b", "models": ["llama-3.3-70b"],
		"provider": {"only": ["s1","s2"], "ignore": ["s3"], "order": ["s2"], "allow_fallbacks": true, "sort": "price",
			"quantizations": ["Q8_0"], "max_price": {"prompt": 1, "completion": 1, "request": 1}, "require_parameters": false},
		"roger": {"pref": "fast", "require": ["tools"], "params_b": [7, 70], "min_ctx": 4096, "min_tps": 1,
			"max_ttft_ms": 1500, "self_hosted_only": false, "region": ["eu"], "freq": "…"},
		"messages": [...]}`)
	if err != nil {
		return err
	}
	s.queue(s.client, marker, b, nil)
	return s.send()
}

// smokeCheckRuns RUNS the standalone smoke script: it builds roger, roger-tower and
// roger-tower-local into a temp dir and executes scripts/localplane-routing-smoke.sh against
// them (a fresh Tower with two stations, loopback only). The Thens read its result lines.
func (s *tp4State) smokeCheckRuns() error {
	f, _, err := tp4SmokeScript()
	if err != nil {
		return err
	}
	bin, err := os.MkdirTemp("", "tp4-smoke-bin-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bin)
	for name, pkg := range map[string]string{"roger": "cmd/rogerai", "roger-tower": "cmd/roger-tower", "roger-tower-local": "cmd/roger-tower-local"} {
		b := exec.Command("go", "build", "-o", filepath.Join(bin, name), "./"+pkg)
		b.Dir = "../.."
		if out, err := b.CombinedOutput(); err != nil {
			return fmt.Errorf("building %s for the smoke check: %v\n%s", name, err, out)
		}
	}
	cmd := exec.Command("bash", f)
	cmd.Env = append(os.Environ(), "BIN="+bin)
	out, err := cmd.CombinedOutput()
	s.smokeOut = string(out)
	if err != nil {
		return fmt.Errorf("the smoke check failed: %v\n%s", err, out)
	}
	return nil
}

// smokeSaid checks the run printed each result line.
func (s *tp4State) smokeSaid(lines ...string) error {
	for _, l := range lines {
		if !strings.Contains(s.smokeOut, "ok:   "+l) {
			return fmt.Errorf("the smoke run did not pass %q:\n%s", l, s.smokeOut)
		}
	}
	return nil
}

// --- Then: served / headers -------------------------------------------------------------------

func (s *tp4State) stationServingClaims(model string) error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusOK {
		return fmt.Errorf("want 200 served, got %s", s.brief())
	}
	by := s.servedBy()
	st, ok := s.stations[by]
	if !ok || !contains(st.models, model) {
		return fmt.Errorf("served by %q, which does not serve %q (%s)", by, model, s.brief())
	}
	return nil
}

func (s *tp4State) costAndLocal() error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.hdr.Get("X-Roger-Cost") != "0" || s.resp.hdr.Get("X-Roger-Local") != "1" {
		return fmt.Errorf("X-Roger-Cost=%q X-Roger-Local=%q (%s)", s.resp.hdr.Get("X-Roger-Cost"), s.resp.hdr.Get("X-Roger-Local"), s.brief())
	}
	return nil
}

func (s *tp4State) ignoredAbsent() error {
	if err := s.send(); err != nil {
		return err
	}
	if v, ok := s.resp.hdr["X-Roger-Routing-Ignored"]; ok {
		return fmt.Errorf("X-Roger-Routing-Ignored is present: %v", v)
	}
	return nil
}

func (s *tp4State) carriesCurated(v string) error {
	if err := s.send(); err != nil {
		return err
	}
	if got := s.resp.hdr.Get("X-Roger-Curated"); got != v {
		return fmt.Errorf("X-Roger-Curated = %q, want %q (%s)", got, v, s.brief())
	}
	return nil
}

func (s *tp4State) uniformAuthRefusal() error {
	if s.resp.code != http.StatusUnauthorized || strings.TrimSpace(string(s.resp.body)) != `{"error":"unauthorized"}` {
		return fmt.Errorf("want the uniform 401 refusal, got %s", s.brief())
	}
	if _, ok := s.resp.hdr["X-Roger-Routing-Ignored"]; ok {
		return fmt.Errorf("the refusal names routing keys, so the carrier was parsed before auth")
	}
	return nil
}

func (s *tp4State) approved429() error {
	if s.resp.code != http.StatusTooManyRequests {
		return fmt.Errorf("no request was refused for per-client concurrency (last %s)", s.brief())
	}
	if !strings.Contains(string(s.resp.body), "too many concurrent requests for this client") {
		return fmt.Errorf("the 429 is not the approved one: %s", s.resp.body)
	}
	for _, st := range s.stations {
		s.startStation(st)
	}
	return nil
}

func (s *tp4State) consoleSpendUnchanged() error {
	b, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(b, s.cfgBytes) {
		return fmt.Errorf("the console spend setting changed:\n%s\nwant\n%s", b, s.cfgBytes)
	}
	return nil
}

func (s *tp4State) jobSubmittedFor(model string) error {
	if err := s.send(); err != nil {
		return err
	}
	jobs := s.jobsFor(s.reqMarker)
	if len(jobs) == 0 {
		return fmt.Errorf("no station received a job for this request (%s)", s.brief())
	}
	if jobs[0].model != model {
		return fmt.Errorf("the job was submitted for %q, want %q", jobs[0].model, model)
	}
	return nil
}

func (s *tp4State) carriesServedModel(v string) error {
	if err := s.send(); err != nil {
		return err
	}
	if got := s.resp.hdr.Get("X-RogerAI-Model"); got != v {
		return fmt.Errorf("X-RogerAI-Model = %q, want %q (%s)", got, v, s.brief())
	}
	return nil
}

func (s *tp4State) is404(msg string) error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusNotFound || s.errText() != msg {
		return fmt.Errorf("want 404 %q, got %s", msg, s.brief())
	}
	return nil
}

func (s *tp4State) nothingDialed() error {
	if err := s.send(); err != nil {
		return err
	}
	if n := len(s.jobsFor(s.reqMarker)); n != 0 {
		return fmt.Errorf("%d station(s) received the job", n)
	}
	if len(s.pendingSaw) != 0 {
		return fmt.Errorf("the plane queued work for %v", keysOf(s.pendingSaw))
	}
	return nil
}

func (s *tp4State) approved504() error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusGatewayTimeout || s.errText() != "no local station served this request in time" {
		return fmt.Errorf("want the approved 504, got %s", s.brief())
	}
	return nil
}

func (s *tp4State) noJobSubmittedFor(model string) error {
	if err := s.send(); err != nil {
		return err
	}
	if s.pendingSaw[model] {
		return fmt.Errorf("a job was queued for %q", model)
	}
	for _, j := range s.jobsFor(s.reqMarker) {
		if j.model == model {
			return fmt.Errorf("station %q received a job for %q", j.station, model)
		}
	}
	return nil
}

func (s *tp4State) is400Error(msg string) error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusBadRequest || s.errText() != msg {
		return fmt.Errorf("want 400 %q, got %s", msg, s.brief())
	}
	return nil
}

// jobBodyFields decodes the job body the serving station received for this request.
func (s *tp4State) jobBody() (map[string]json.RawMessage, []byte, error) {
	if err := s.send(); err != nil {
		return nil, nil, err
	}
	jobs := s.jobsFor(s.reqMarker)
	if len(jobs) == 0 {
		return nil, nil, fmt.Errorf("no station received a job for this request (%s)", s.brief())
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(jobs[0].body, &m); err != nil {
		return nil, nil, fmt.Errorf("the job body is not a JSON object: %s", jobs[0].body)
	}
	return m, jobs[0].body, nil
}

func (s *tp4State) jobHasModelNoModels(model string) error {
	m, raw, err := s.jobBody()
	if err != nil {
		return err
	}
	var got string
	_ = json.Unmarshal(m["model"], &got)
	if got != model {
		return fmt.Errorf("the station's job body has model %q, want %q: %s", got, model, raw)
	}
	if _, ok := m["models"]; ok {
		return fmt.Errorf("the station's job body still carries models: %s", raw)
	}
	return nil
}

// --- Then: only / ignore / order -------------------------------------------------------------

const tp4Repeats = 6

// repeatServed re-sends the scenario's request tp4Repeats times (fresh markers) and returns who
// served each. A random claimant passes "only X" by luck about once in 2^n, so one run of a
// two-station race is not evidence; six are.
func (s *tp4State) repeatServed() ([]string, error) {
	if err := s.send(); err != nil {
		return nil, err
	}
	var served []string
	served = append(served, s.servedBy()+fmt.Sprintf("(%d)", s.resp.code))
	base := s.reqBody
	for i := 1; i < tp4Repeats; i++ {
		marker := s.nextMarker()
		b := bytes.Replace(base, []byte(s.reqMarker), []byte(marker), 1)
		r := s.do(s.client, b, nil, nil)
		by := ""
		if m := regexp.MustCompile(`served by ([a-z0-9-]+)`).FindSubmatch(r.body); m != nil {
			by = string(m[1])
		}
		served = append(served, fmt.Sprintf("%s(%d)", by, r.code))
	}
	return served, nil
}

func (s *tp4State) onlyPollHanded(id string) error {
	served, err := s.repeatServed()
	if err != nil {
		return err
	}
	for _, x := range served {
		if x != id+"(200)" {
			return fmt.Errorf("want every request served by %q only, got %v", id, served)
		}
	}
	return nil
}

func (s *tp4State) pollingGets204(id, model string) error {
	st := s.stations[id]
	// A long-poll answers 204 only when its window expires, and the scenario's requests can
	// all finish inside one window, so wait (bounded) for the station's current poll to end.
	empty := 0
	for deadline := time.Now().Add(3 * tp4PollTimeout); ; time.Sleep(10 * time.Millisecond) {
		st.mu.Lock()
		empty = st.empty
		st.mu.Unlock()
		if empty > 0 || time.Now().After(deadline) {
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.station == id && j.model == model {
			return fmt.Errorf("%q was handed a %q job", id, model)
		}
	}
	if empty == 0 {
		return fmt.Errorf("%q never saw an empty (204) poll", id)
	}
	return nil
}

func (s *tp4State) neverReceivesAndServes(ignored, server string) error {
	served, err := s.repeatServed()
	if err != nil {
		return err
	}
	for _, x := range served {
		if x != server+"(200)" {
			return fmt.Errorf("want every request served by %q (and never %q), got %v", server, ignored, served)
		}
	}
	return nil
}

func (s *tp4State) is503CodeMsg(code, msg string) error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusServiceUnavailable || s.errCode() != code || s.errText() != msg {
		return fmt.Errorf("want 503 %s %q, got %s", code, msg, s.brief())
	}
	return nil
}

func (s *tp4State) noRetryAfter() error {
	if v := s.resp.hdr.Get("Retry-After"); v != "" {
		return fmt.Errorf("the 503 carries Retry-After %q", v)
	}
	return nil
}

func (s *tp4State) nothingQueued() error {
	if err := s.send(); err != nil {
		return err
	}
	if len(s.pendingSaw) != 0 {
		return fmt.Errorf("the plane queued work for %v (%s)", keysOf(s.pendingSaw), s.brief())
	}
	if n := len(s.jobsFor(s.reqMarker)); n != 0 && s.reqMarker != "" {
		return fmt.Errorf("%d station(s) received the job", n)
	}
	return nil
}

func (s *tp4State) firstIntervalOnly(id string) error {
	served, err := s.repeatServed()
	if err != nil {
		return err
	}
	for _, x := range served {
		if x != id+"(200)" {
			return fmt.Errorf("with %q polling, want every ordered request claimed by it, got %v", id, served)
		}
	}
	return nil
}

func (s *tp4State) afterAnyMayClaim() error {
	// The listed station stops polling; the request must wait out the preferred window, then any
	// station serving the model may claim it.
	var listed string
	var o struct {
		Provider struct {
			Order []string `json:"order"`
		} `json:"provider"`
	}
	_ = json.Unmarshal(s.reqBody, &o)
	if len(o.Provider.Order) == 0 {
		return fmt.Errorf("the scenario request names no order")
	}
	listed = o.Provider.Order[0]
	s.stopStation(s.stations[listed])
	defer s.startStation(s.stations[listed])
	marker := s.nextMarker()
	b := bytes.Replace(s.reqBody, []byte(s.reqMarker), []byte(marker), 1)
	r := s.do(s.client, b, nil, nil)
	if r.code != http.StatusOK {
		return fmt.Errorf("with %q silent, want another station to claim after the window, got %d %s", listed, r.code, r.body)
	}
	if r.elapsed < tp4PollTimeout {
		return fmt.Errorf("another station claimed after %v, inside the preferred window (%v): order gave %q no priority", r.elapsed, tp4PollTimeout, listed)
	}
	return nil
}

func (s *tp4State) neverReceivesAnd504(id string) error {
	if err := s.send(); err != nil {
		return err
	}
	for _, j := range s.jobsFor(s.reqMarker) {
		if j.station == id {
			return fmt.Errorf("%q received the job (%s)", id, s.brief())
		}
	}
	return s.approved504()
}

func (s *tp4State) behavesPlain() error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusOK {
		return fmt.Errorf("want a plain 200, got %s", s.brief())
	}
	if n := len(s.jobsFor(s.reqMarker)); n != 1 {
		return fmt.Errorf("want exactly one claim, got %d", n)
	}
	return nil
}

func (s *tp4State) servedNormally() error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusOK {
		return fmt.Errorf("want 200, got %s", s.brief())
	}
	return nil
}

func (s *tp4State) constraint503() error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusServiceUnavailable || s.errCode() != "no_match" || !strings.Contains(s.errText(), "only") {
		return fmt.Errorf("want 503 no_match naming the only constraint, got %s", s.brief())
	}
	return nil
}

func (s *tp4State) nothingPublicRevealed() error {
	if err := tp4NoEgressInSource(); err != nil {
		return err
	}
	low := strings.ToLower(string(s.resp.body))
	for _, w := range []string{"broker", "rogerai.fm", "rogerai.fyi", "market", "tower relay"} {
		if strings.Contains(low, w) {
			return fmt.Errorf("the refusal mentions %q: %s", w, s.resp.body)
		}
	}
	return nil
}

func (s *tp4State) noProviderNoRoger() error {
	m, raw, err := s.jobBody()
	if err != nil {
		return err
	}
	for _, k := range []string{"provider", "roger"} {
		if _, ok := m[k]; ok {
			return fmt.Errorf("the station's job body still carries %q: %s", k, raw)
		}
	}
	return nil
}

// otherFieldsIdentical compares the job body with the consumer's body minus the carriers, value
// bytes for value bytes (insignificant whitespace compacted on both sides).
func (s *tp4State) otherFieldsIdentical() error {
	_, raw, err := s.jobBody()
	if err != nil {
		return err
	}
	want, err := tp4DropKeys(s.reqBody, "provider", "roger", "models")
	if err != nil {
		return err
	}
	var a, b bytes.Buffer
	if err := json.Compact(&a, raw); err != nil {
		return err
	}
	if err := json.Compact(&b, want); err != nil {
		return err
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		return fmt.Errorf("the job body differs beyond the carriers:\n got %s\nwant %s", a.Bytes(), b.Bytes())
	}
	return nil
}

func (s *tp4State) servedByStationServing(model string) error { return s.stationServingClaims(model) }

func (s *tp4State) ignoredHeader() (string, error) {
	if err := s.send(); err != nil {
		return "", err
	}
	v, ok := s.resp.hdr["X-Roger-Routing-Ignored"]
	if !ok {
		return "", fmt.Errorf("no X-Roger-Routing-Ignored header (%s)", s.brief())
	}
	return strings.Join(v, ","), nil
}

func (s *tp4State) carriesIgnored(want string) error {
	got, err := s.ignoredHeader()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("X-Roger-Routing-Ignored = %q, want %q", got, want)
	}
	return nil
}

func (s *tp4State) ignoredIncludes(tok string) error {
	got, err := s.ignoredHeader()
	if err != nil {
		return err
	}
	for _, p := range strings.Split(got, ",") {
		if strings.TrimSpace(p) == tok {
			return nil
		}
	}
	return fmt.Errorf("X-Roger-Routing-Ignored = %q does not include %q", got, tok)
}

func (s *tp4State) is400CodeMsg(code, msg string) error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusBadRequest || s.errCode() != code || s.errText() != msg {
		return fmt.Errorf("want 400 %s %q, got %s", code, msg, s.brief())
	}
	return nil
}

func (s *tp4State) is400Code(code string) error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusBadRequest || s.errCode() != code {
		return fmt.Errorf("want 400 %s, got %s", code, s.brief())
	}
	return nil
}

func (s *tp4State) secretAppearsNowhere(secret string) error {
	if err := s.send(); err != nil {
		return err
	}
	for k, vs := range s.resp.hdr {
		for _, v := range vs {
			if strings.Contains(v, secret) {
				return fmt.Errorf("header %s echoes %q", k, secret)
			}
		}
	}
	if strings.Contains(s.logs.String(), secret) {
		return fmt.Errorf("a log line carries %q", secret)
	}
	recs, err := s.st.Receipts(0)
	if err != nil {
		return err
	}
	for _, r := range recs {
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), secret) {
			return fmt.Errorf("a receipt carries %q", secret)
		}
	}
	return nil
}

func (s *tp4State) is400Naming(key string) error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusBadRequest || !strings.Contains(s.errText(), key) {
		return fmt.Errorf("want 400 naming %q, got %s", key, s.brief())
	}
	return nil
}

func (s *tp4State) is400Text(msg string) error { return s.is400Error(msg) }

func (s *tp4State) tokenSpentOne() error {
	// A frozen limiter clock: no refill between the reads, so the spend is exactly what the
	// request took.
	frozen := time.Now()
	s.srv.rl.mu.Lock()
	s.srv.rl.now = func() time.Time { return frozen }
	s.srv.rl.mu.Unlock()
	key := protocol.UserIDFromPubkey(hexPub(s.client.Public().(ed25519.PublicKey)))
	// prime the bucket so it exists under the frozen clock
	before := s.tokens(key)
	if err := s.send(); err != nil {
		return err
	}
	after := s.tokens(key)
	if s.resp.code != http.StatusBadRequest {
		return fmt.Errorf("the malformed carrier was not a 400 (%s)", s.brief())
	}
	if spent := before - after; spent < 0.999 || spent > 1.001 {
		return fmt.Errorf("the request spent %.3f tokens, want exactly 1", spent)
	}
	return nil
}

func (s *tp4State) tokens(key string) float64 {
	s.srv.rl.mu.Lock()
	defer s.srv.rl.mu.Unlock()
	b, ok := s.srv.rl.buckets[key]
	if !ok {
		return s.srv.rl.burst
	}
	return b.tokens
}

func (s *tp4State) approvedSizeRefusal() error {
	if err := s.send(); err != nil {
		return err
	}
	if s.resp.code != http.StatusUnauthorized && s.resp.code != http.StatusRequestEntityTooLarge {
		return fmt.Errorf("an over-cap body was not refused by the approved bound: %s", s.brief())
	}
	if len(s.pendingSaw) != 0 {
		return fmt.Errorf("an over-cap body was queued")
	}
	return nil
}

func (s *tp4State) lastReceipt() (tower.LocalReceipt, error) {
	recs, err := s.st.Receipts(0)
	if err != nil {
		return tower.LocalReceipt{}, err
	}
	if len(recs) == 0 {
		return tower.LocalReceipt{}, fmt.Errorf("no local receipt was recorded (%s)", s.brief())
	}
	return recs[len(recs)-1], nil
}

func (s *tp4State) receiptNamesServed(model string) error {
	if err := s.send(); err != nil {
		return err
	}
	r, err := s.lastReceipt()
	if err != nil {
		return err
	}
	jobs := s.jobsFor(s.reqMarker)
	if len(jobs) == 0 {
		return fmt.Errorf("no station served this request (%s)", s.brief())
	}
	if r.Model != model || r.StationID != jobs[0].station {
		return fmt.Errorf("the receipt names model %q station %q, want %q by %q", r.Model, r.StationID, model, jobs[0].station)
	}
	return nil
}

func (s *tp4State) receiptShapeUnchanged() error {
	r, err := s.lastReceipt()
	if err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	want := []string{"at", "client_key_hash", "cost", "model", "network_id", "request_id", "root_fingerprint", "station_id"}
	got := keysOfAny(m)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("the receipt keys are %v, want %v", got, want)
	}
	if r.Cost != 0 {
		return fmt.Errorf("the receipt carries cost %d", r.Cost)
	}
	return nil
}

func (s *tp4State) receiptNamesServedModel() error {
	deadline := time.Now().Add(time.Second)
	for {
		recs, err := s.st.Receipts(0)
		if err != nil {
			return err
		}
		for _, r := range recs {
			if r.Model == "qwen3-32b" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no receipt names the served model qwen3-32b (response %s, receipts %d)", s.brief(), len(recs))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *tp4State) noEgressByLinkage() error { return tp4NoEgressInSource() }

func (s *tp4State) noResolveNoDial() error { return tp4NoEgressInSource() }

func (s *tp4State) nothingEchoed() error {
	if err := s.send(); err != nil {
		return err
	}
	all := string(s.resp.body)
	for k, vs := range s.resp.hdr {
		all += k + ":" + strings.Join(vs, ",") + "\n"
	}
	for _, secret := range []string{tp4FreqCode, tp4PublicNodeID} {
		if strings.Contains(all, secret) {
			return fmt.Errorf("the response echoes %q: %s", secret, all)
		}
	}
	return nil
}

func (s *tp4State) constraint503WithFreqIgnored() error {
	if err := s.constraint503(); err != nil {
		return err
	}
	return s.ignoredIncludes("roger.freq")
}

// tp4SmokeScript finds the standalone smoke check: a script under scripts/ that drives a local
// Tower's consumer plane.
func tp4SmokeScript() (string, string, error) {
	files, _ := filepath.Glob("../../scripts/*.sh")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		src := string(b)
		if strings.Contains(src, "roger-tower-local") || strings.Contains(src, "/local/poll") {
			return f, src, nil
		}
	}
	return "", "", fmt.Errorf("no standalone smoke script exists under scripts/ (none drives roger-tower-local or /local/poll)")
}

func (s *tp4State) smokePostsModelsAndOnly() error {
	f, src, err := tp4SmokeScript()
	if err != nil {
		return err
	}
	if !strings.Contains(src, `"models"`) || !strings.Contains(src, `"only"`) {
		return fmt.Errorf("%s posts no models[] request and no only request", f)
	}
	return s.smokeSaid("models[] request served", "provider.only node2: served by node2 six times")
}

func (s *tp4State) smokeAssertsHeaders() error {
	f, src, err := tp4SmokeScript()
	if err != nil {
		return err
	}
	for _, h := range []string{"X-RogerAI-Model", "X-Roger-Cost", "X-Roger-Routing-Ignored"} {
		if !strings.Contains(src, h) {
			return fmt.Errorf("%s does not assert %s", f, h)
		}
	}
	return s.smokeSaid("X-RogerAI-Model: test-model", "X-Roger-Cost: 0", "X-Roger-Routing-Ignored names roger.min_tps")
}

func (s *tp4State) smokeFailsOnCarriers() error {
	f, src, err := tp4SmokeScript()
	if err != nil {
		return err
	}
	if !strings.Contains(src, "provider") || !strings.Contains(src, "roger") {
		return fmt.Errorf("%s never checks the station's job body for carriers", f)
	}
	return s.smokeSaid("no job body carries provider / roger / models")
}

// --- helpers ---------------------------------------------------------------------------------

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOfAny(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tp4DropKeys re-emits a JSON object without the named top-level keys, keeping every other
// key's value bytes exactly as sent.
func tp4DropKeys(body []byte, drop ...string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("not a JSON object: %s", body)
	}
	var out bytes.Buffer
	out.WriteByte('{')
	first := true
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if contains(drop, k) {
			continue
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		out.Write(kb)
		out.WriteByte(':')
		out.Write(v)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// tp4NoEgressInSource is the package's own no-egress gate re-read: no non-test source file of
// the plane names a dial, a listen, a resolver or an exec. Routing keys cannot make an outbound
// connection the code cannot express.
func tp4NoEgressInSource() error {
	forbidden := []string{
		"net.Dial", "net.Listen", "http.Get", "http.Post", "http.NewRequest", "http.Client{",
		"http.DefaultClient", "net.Lookup", "net.Resolver", "exec.Command",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		return err
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		for _, bad := range forbidden {
			if strings.Contains(string(b), bad) {
				return fmt.Errorf("%s names %s: the plane could make an outbound call", f, bad)
			}
		}
	}
	return nil
}

func TestLocalplaneRoutingBDD(t *testing.T) {
	st := &tp4State{t: t, stations: map[string]*tp4Station{}}
	prev := log.Writer()
	defer log.SetOutput(prev)
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.stopAll()
				return c, nil
			})
			// Background
			sc.Step(`^a standalone Tower with admitted client "([^"]*)"$`, st.standaloneTower)
			sc.Step(`^local stations (.+)$`, st.localStations)
			sc.Step(`^every station is polling$`, st.everyStationPolling)
			// When / Given
			sc.Step(`^"([^"]*)" posts (\{.*\})$`, st.posts)
			sc.Step(`^"([^"]*)" posts (\{.*\}) with header X-Roger-Confidential "([^"]*)"$`, st.postsWithConfidential)
			sc.Step(`^an unadmitted caller posts a body carrying a routing object$`, st.unadmittedPostsRouting)
			sc.Step(`^"([^"]*)" exceeds its per-client concurrency with routing bodies$`, st.exceedsConcurrency)
			sc.Step(`^the operator set a console spend value$`, st.consoleSpendSet)
			sc.Step(`^routing requests are served$`, st.routingRequestsServed)
			sc.Step(`^no station claims "([^"]*)" within the completion timeout$`, st.noStationClaimsWithin)
			sc.Step(`^"([^"]*)" posts a body with six distinct models across model and models$`, st.sixModels)
			sc.Step(`^"([^"]*)" never polls$`, st.neverPolls)
			sc.Step(`^"([^"]*)" posts a malformed carrier$`, st.postsMalformedCarrier)
			sc.Step(`^"([^"]*)" posts a body over the plane's size bound$`, st.postsOverSize)
			sc.Step(`^any routed request is served$`, st.anyRoutedRequestServed)
			sc.Step(`^"([^"]*)" disconnects after "([^"]*)" delivered a models\[\] fallback answer$`, st.disconnectsAfterDelivered)
			sc.Step(`^"([^"]*)" posts a body carrying every routing key$`, st.everyRoutingKey)
			sc.Step(`^the standalone smoke check runs against a fresh Tower with two stations$`, st.smokeCheckRuns)
			// Then
			sc.Step(`^a station serving "([^"]*)" claims and answers it$`, st.stationServingClaims)
			sc.Step(`^the response carries X-Roger-Cost: 0 and X-Roger-Local: 1$`, st.costAndLocal)
			sc.Step(`^no X-Roger-Routing-Ignored header is present$`, st.ignoredAbsent)
			sc.Step(`^X-Roger-Routing-Ignored is absent( \(the key was honored trivially\))?$`, func(string) error { return st.ignoredAbsent() })
			sc.Step(`^the response carries X-Roger-Curated: (\S+)$`, st.carriesCurated)
			sc.Step(`^the refusal is the uniform auth refusal and no routing key is parsed$`, st.uniformAuthRefusal)
			sc.Step(`^the 429 is the approved one$`, st.approved429)
			sc.Step(`^the console spend setting is byte-identical afterwards$`, st.consoleSpendUnchanged)
			sc.Step(`^the job is submitted for "([^"]*)"$`, st.jobSubmittedFor)
			sc.Step(`^the response carries X-RogerAI-Model: (\S+)$`, st.carriesServedModel)
			sc.Step(`^X-RogerAI-Model is "([^"]*)"$`, st.carriesServedModel)
			sc.Step(`^the response is 404 "([^"]*)"$`, st.is404)
			sc.Step(`^the response is the approved 404 "([^"]*)"$`, st.is404)
			sc.Step(`^the response is the approved 404 and carries X-Roger-Routing-Ignored: (\S+)$`, func(h string) error {
				if err := st.send(); err != nil {
					return err
				}
				if st.resp.code != http.StatusNotFound {
					return fmt.Errorf("want the approved 404, got %s", st.brief())
				}
				return st.carriesIgnored(h)
			})
			sc.Step(`^nothing is dialed$`, st.nothingDialed)
			sc.Step(`^the response is the approved 504$`, st.approved504)
			sc.Step(`^no job was submitted for "([^"]*)" \(a local plane has no upstream error to fail over on\)$`, st.noJobSubmittedFor)
			sc.Step(`^the response is 400 with error "([^"]*)"$`, st.is400Error)
			sc.Step(`^the station's job body has model "([^"]*)" and no "models" key$`, st.jobHasModelNoModels)
			sc.Step(`^only "([^"]*)"'s poll is handed the job$`, st.onlyPollHanded)
			sc.Step(`^"([^"]*)" polling for "([^"]*)" gets 204$`, st.pollingGets204)
			sc.Step(`^"([^"]*)"'s poll never receives the job and "([^"]*)" serves it$`, st.neverReceivesAndServes)
			sc.Step(`^the response is 503 with error code "([^"]*)" and message "([^"]*)"$`, st.is503CodeMsg)
			sc.Step(`^the response carries no Retry-After \(nothing is cooling on a local plane\)$`, st.noRetryAfter)
			sc.Step(`^nothing is queued$`, st.nothingQueued)
			sc.Step(`^for the first poll interval only "([^"]*)" may claim$`, st.firstIntervalOnly)
			sc.Step(`^after it, if unclaimed, any station serving the model may claim \(allow_fallbacks default true\)$`, st.afterAnyMayClaim)
			sc.Step(`^"([^"]*)" never receives the job and the response is the approved 504$`, st.neverReceivesAnd504)
			sc.Step(`^the behavior equals a plain request \(one claim, no re-dispatch exists on the plane\)$`, st.behavesPlain)
			sc.Step(`^the request is served normally$`, st.servedNormally)
			sc.Step(`^the response is the 503 no_match naming the constraint$`, st.constraint503)
			sc.Step(`^nothing about the public network is consulted or revealed$`, st.nothingPublicRevealed)
			sc.Step(`^the station's job body has neither "provider" nor "roger"$`, st.noProviderNoRoger)
			sc.Step(`^every other field is byte-identical$`, st.otherFieldsIdentical)
			sc.Step(`^the request is served by a station serving "([^"]*)"$`, st.servedByStationServing)
			sc.Step(`^the response carries X-Roger-Routing-Ignored: (\S+)$`, st.carriesIgnored)
			sc.Step(`^X-Roger-Routing-Ignored equals "([^"]*)" exactly$`, st.carriesIgnored)
			sc.Step(`^X-Roger-Routing-Ignored includes "([^"]*)"$`, st.ignoredIncludes)
			sc.Step(`^X-Roger-Routing-Ignored is "([^"]*)"$`, st.carriesIgnored)
			sc.Step(`^the response is 400 with error code "([^"]*)" and message "([^"]*)"$`, st.is400CodeMsg)
			sc.Step(`^the response is 400 with error code "([^"]*)"$`, st.is400Code)
			sc.Step(`^"([^"]*)" appears in no header, log line, or receipt$`, st.secretAppearsNowhere)
			sc.Step(`^the response is 400 naming "([^"]*)"$`, st.is400Naming)
			sc.Step(`^the response is 400 "([^"]*)"$`, st.is400Text)
			sc.Step(`^the per-client token spent is exactly one, as for any request$`, st.tokenSpentOne)
			sc.Step(`^the approved size refusal is returned$`, st.approvedSizeRefusal)
			sc.Step(`^the recorded receipt names model "([^"]*)" and the serving station$`, st.receiptNamesServed)
			sc.Step(`^the receipt shape is unchanged from standalone_consumer_plane\.feature$`, st.receiptShapeUnchanged)
			sc.Step(`^the receipt names the served model$`, st.receiptNamesServedModel)
			sc.Step(`^the no-egress gate still holds by linkage$`, st.noEgressByLinkage)
			sc.Step(`^no name is resolved and no socket is dialed$`, st.noResolveNoDial)
			sc.Step(`^no header or body echoes the freq or the public id$`, st.nothingEchoed)
			sc.Step(`^the response is the constraint 503 no_match \(only\) with the ignored header listing roger\.freq$`, st.constraint503WithFreqIgnored)
			sc.Step(`^it posts one models\[\] request and one only request$`, st.smokePostsModelsAndOnly)
			sc.Step(`^it asserts X-RogerAI-Model, X-Roger-Cost: 0 and X-Roger-Routing-Ignored on an ignored-key request$`, st.smokeAssertsHeaders)
			sc.Step(`^the check fails if the station's job body still carries "provider" or "roger"$`, st.smokeFailsOnCarriers)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Tags:  "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later && ~@slice5",
			Paths: []string{"../../features/tower/localplane_routing.feature"},
		},
	}
	if bddtest.Run(t, &suite) != 0 {
		t.Fatal("the local-plane routing scenarios failed")
	}
}
