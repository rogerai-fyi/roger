package agent

// Executable spec: features/tower/standalone_streaming.feature, the scenarios the REAL station
// loop decides (@station, plus the @tower ones about what a current station puts on the wire).
//
// No mocks of our own code: a tower.State on disk, the real localplane Handler over HTTP, the
// real ServeLocalTower polling it, and a fake OpenAI-shaped upstream that streams. A recorder
// in front of the plane keeps every station request and the plane's status for it, so the
// assertions read the actual wire.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/localplane"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/tower"
)

const stationStreamFeature = "../../features/tower/standalone_streaming.feature"

// wireCall is one station request the plane received, and what the plane answered.
type wireCall struct {
	path   string
	header http.Header
	body   []byte
	status int
	reply  []byte
	at     time.Time
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *statusRecorder) WriteHeader(c int) { r.status = c; r.ResponseWriter.WriteHeader(c) }
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// streamUpstream is the station's own model server: it records each request and streams its
// script, optionally holding after the first tokens until released.
type streamUpstream struct {
	mu        sync.Mutex
	srv       *httptest.Server
	requests  [][]byte
	cancelled chan struct{}
	tokens    []string
	gap       time.Duration
	holdAfter int           // tokens sent before holding (0 = never hold)
	release   chan struct{} // closed to let a held stream finish
	echoKey   string        // a token that carries the upstream key, to prove redaction
	started   time.Time     // when the streamed tokens began
	ended     time.Time
}

func newStreamUpstream(t *testing.T) *streamUpstream {
	u := &streamUpstream{cancelled: make(chan struct{}, 4), release: make(chan struct{}), gap: time.Millisecond,
		tokens: []string{"Hello", ", ", "world"}}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *streamUpstream) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.requests = append(u.requests, body)
	tokens, gap, hold, key := append([]string(nil), u.tokens...), u.gap, u.holdAfter, u.echoKey
	u.mu.Unlock()
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"up-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"`+strings.Join(tokens, "")+`"},"finish_reason":"stop"}]}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	send := func(content string) {
		b, _ := json.Marshal(map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}}}})
		_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		fl.Flush()
	}
	if key != "" {
		send("my key is " + key)
	}
	u.mu.Lock()
	u.started = time.Now()
	u.mu.Unlock()
	for i, tok := range tokens {
		if hold > 0 && i == hold {
			select {
			case <-u.release:
			case <-r.Context().Done():
				u.cancelled <- struct{}{}
				return
			}
		}
		select {
		case <-r.Context().Done():
			u.cancelled <- struct{}{}
			return
		case <-time.After(gap):
		}
		send(tok)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	fl.Flush()
	u.mu.Lock()
	u.ended = time.Now()
	u.mu.Unlock()
}

func (u *streamUpstream) lastStreamFlag() (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.requests) == 0 {
		return false, fmt.Errorf("the upstream saw no request")
	}
	var r struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(u.requests[len(u.requests)-1], &r); err != nil || r.Stream == nil {
		return false, fmt.Errorf("the upstream request carried no stream flag: %s", u.requests[len(u.requests)-1])
	}
	return *r.Stream, nil
}

// consumer is one consumer request read as it arrives.
type consumer struct {
	mu      sync.Mutex
	status  int
	header  http.Header
	body    bytes.Buffer
	lines   []string
	firstAt time.Time
	done    chan struct{}
	cancel  context.CancelFunc
}

func (c *consumer) data() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, l := range c.lines {
		if strings.HasPrefix(l, "data: ") {
			out = append(out, strings.TrimPrefix(l, "data: "))
		}
	}
	return out
}

func (c *consumer) contents() []string {
	var out []string
	for _, d := range c.data() {
		var m struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(d), &m) == nil && len(m.Choices) > 0 && m.Choices[0].Delta.Content != "" {
			out = append(out, m.Choices[0].Delta.Content)
		}
	}
	return out
}

func (c *consumer) wait(d time.Duration) error {
	select {
	case <-c.done:
		return nil
	case <-time.After(d):
		return fmt.Errorf("the consumer's response did not end within %v", d)
	}
}

func (c *consumer) waitContents(n int, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if len(c.contents()) >= n {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("the consumer received %q, want %d content events", c.contents(), n)
}

type stationStreamState struct {
	batchWait error // the batching scenario judges its wait after the chunk count
	t         *testing.T
	st        *tower.State
	plane     *httptest.Server
	clientPv  ed25519.PrivateKey
	clientH   string
	nodePv    ed25519.PrivateKey
	up        *streamUpstream
	cancel    context.CancelFunc

	mu       sync.Mutex
	calls    []wireCall
	arrivals []wireCall // every station request as it ARRIVES (a long-poll answers later)

	cons      *consumer
	consumers []*consumer
}

func (s *stationStreamState) reset() {
	s.batchWait = nil
	if s.cancel != nil {
		s.cancel()
	}
	for _, c := range s.consumers {
		c.cancel()
	}
	if s.plane != nil {
		s.plane.Close()
	}
	*s = stationStreamState{t: s.t}
}

func (s *stationStreamState) towerUp() error {
	dir := s.t.TempDir()
	if _, err := tower.Init(dir, tower.ModeStandalone); err != nil {
		return err
	}
	st, err := tower.Open(dir)
	if err != nil {
		return err
	}
	s.st = st
	h := localplane.New(st).Handler()
	s.plane = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/local/") {
			h.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.mu.Lock()
		s.arrivals = append(s.arrivals, wireCall{path: r.URL.Path, body: body, at: time.Now()})
		s.mu.Unlock()
		rec := &statusRecorder{ResponseWriter: w}
		h.ServeHTTP(rec, r)
		s.mu.Lock()
		s.calls = append(s.calls, wireCall{path: r.URL.Path, header: r.Header.Clone(), body: body, status: rec.status, reply: rec.buf.Bytes(), at: time.Now()})
		s.mu.Unlock()
	}))
	return nil
}

func (s *stationStreamState) admitClient() error {
	_, s.clientPv, _ = ed25519.GenerateKey(nil)
	s.clientH = idFromPriv(s.clientPv)
	inv, code, err := s.st.CreateInvitation(s.clientH, time.Hour, 5)
	if err != nil {
		return err
	}
	_, err = s.st.ConsumeInvitation(inv.ID, code, s.clientH)
	return err
}

const shedUpstreamKey = "sk-shed-upstream-key-0123"

func (s *stationStreamState) attachShed() error {
	_, s.nodePv, _ = ed25519.GenerateKey(nil)
	if _, err := s.st.AttachStation("shed", idFromPriv(s.nodePv), []string{"qwen3-8b"}); err != nil {
		return err
	}
	s.up = newStreamUpstream(s.t)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	cfg := Config{Broker: s.plane.URL, Upstream: s.up.srv.URL, UpstreamKey: shedUpstreamKey, Model: "qwen3-8b"}
	go func() { _ = ServeLocalTower(ctx, cfg, s.nodePv, io.Discard) }()
	return nil
}

func (s *stationStreamState) post(body string) *consumer {
	ctx, cancel := context.WithCancel(context.Background())
	c := &consumer{done: make(chan struct{}), cancel: cancel}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.plane.URL+"/v1/chat/completions", strings.NewReader(body))
	nonce := protocol.NewNonce()
	pub, ts, sig := protocol.SignRequestNonce(s.clientPv, http.MethodPost, "/v1/chat/completions", []byte(body), nonce)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	req.Header.Set(protocol.HeaderNonce, nonce)
	go func() {
		defer close(c.done)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		c.mu.Lock()
		c.status, c.header = resp.StatusCode, resp.Header.Clone()
		c.mu.Unlock()
		rd := bufio.NewReader(resp.Body)
		for {
			line, err := rd.ReadString('\n')
			if line != "" {
				c.mu.Lock()
				c.body.WriteString(line)
				if l := strings.TrimRight(line, "\r\n"); l != "" {
					if c.firstAt.IsZero() && strings.HasPrefix(l, "data: ") {
						c.firstAt = time.Now()
					}
					c.lines = append(c.lines, l)
				}
				c.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	s.consumers = append(s.consumers, c)
	return c
}

func promptWith(stream string) string {
	if stream == "" {
		return `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`
	}
	return `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}],"stream":` + stream + `}`
}

func (s *stationStreamState) callsTo(path string) []wireCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []wireCall
	for _, c := range s.calls {
		if c.path == path {
			out = append(out, c)
		}
	}
	return out
}

// ---- 1. happy path -------------------------------------------------------------------

func (s *stationStreamState) postsStreamTrue() error {
	// The upstream holds after its first token, so "the first event before the station has
	// finished" is a fact about timing, not luck.
	s.up.holdAfter = 1
	s.cons = s.post(promptWith("true"))
	if err := s.cons.waitContents(1, 5*time.Second); err != nil {
		return err
	}
	close(s.up.release)
	return s.cons.wait(5 * time.Second)
}

func (s *stationStreamState) contentTypeSSE() error {
	if ct := s.cons.header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		return fmt.Errorf("Content-Type %q, want text/event-stream", ct)
	}
	return nil
}

func (s *stationStreamState) freeLocalHeaders() error {
	if s.cons.header.Get("X-Roger-Cost") != "0" || s.cons.header.Get("X-Roger-Local") != "1" {
		return fmt.Errorf("headers %v, want X-Roger-Cost 0 and X-Roger-Local 1", s.cons.header)
	}
	return nil
}

func (s *stationStreamState) firstBeforeFinished() error {
	s.up.mu.Lock()
	ended := s.up.ended
	s.up.mu.Unlock()
	if s.cons.firstAt.IsZero() || ended.IsZero() || !s.cons.firstAt.Before(ended) {
		return fmt.Errorf("the first event (%v) did not arrive before the upstream finished (%v)", s.cons.firstAt, ended)
	}
	return nil
}

func (s *stationStreamState) inOrder() error {
	if got := strings.Join(s.cons.contents(), ""); got != "Hello, world" {
		return fmt.Errorf("the consumer received %q, want the upstream's order", got)
	}
	return nil
}

func (s *stationStreamState) endsUsageDone() error {
	d := s.cons.data()
	if len(d) < 2 || d[len(d)-1] != "[DONE]" || !strings.Contains(d[len(d)-2], `"usage"`) {
		return fmt.Errorf("the stream does not end with a usage chunk and [DONE]: %q", d)
	}
	return nil
}

func (s *stationStreamState) postsStreamTrueQuick() error {
	s.cons = s.post(promptWith("true"))
	return s.cons.wait(5 * time.Second)
}

func (s *stationStreamState) upstreamStream(want string) error {
	got, err := s.up.lastStreamFlag()
	if err != nil {
		return err
	}
	if strconv.FormatBool(got) != want {
		return fmt.Errorf("the station's upstream request had stream %v, want %s", got, want)
	}
	return nil
}

func (s *stationStreamState) postsNonStream() error {
	s.cons = s.post(promptWith(""))
	return s.cons.wait(5 * time.Second)
}

func (s *stationStreamState) returnedWhole() error {
	if n := len(s.callsTo("/local/complete")); n != 1 {
		return fmt.Errorf("%d posts to /local/complete, want 1", n)
	}
	if n := len(s.callsTo("/local/chunk")); n != 0 {
		return fmt.Errorf("a non-stream job posted %d chunks", n)
	}
	return nil
}

func (s *stationStreamState) oneJSONAnswer() error {
	if ct := s.cons.header.Get("Content-Type"); ct != "application/json" {
		return fmt.Errorf("Content-Type %q, want application/json", ct)
	}
	return nil
}

func (s *stationStreamState) bytesAsToday() error {
	// Today the Tower relays the station's answer verbatim, and the station relays its
	// upstream's answer verbatim: so the consumer's bytes are the upstream's bytes.
	want := `{"id":"up-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello, world"},"finish_reason":"stop"}]}`
	s.cons.mu.Lock()
	got := strings.TrimSpace(s.cons.body.String())
	s.cons.mu.Unlock()
	if got != want {
		return fmt.Errorf("the consumer received %s, want the upstream's bytes %s", got, want)
	}
	return nil
}

func (s *stationStreamState) postsStreamSetTo(v string) error {
	s.cons = s.post(promptWith(v))
	return s.cons.wait(5 * time.Second)
}

func (s *stationStreamState) receivesShape(shape string) error {
	ct := s.cons.header.Get("Content-Type")
	switch shape {
	case "an SSE stream":
		if !strings.Contains(ct, "text/event-stream") {
			return fmt.Errorf("Content-Type %q, want an SSE stream", ct)
		}
	case "one JSON answer":
		if ct != "application/json" {
			return fmt.Errorf("Content-Type %q, want one JSON answer", ct)
		}
	default:
		return fmt.Errorf("unknown shape %q", shape)
	}
	return nil
}

// ---- 2. the chunk route, as the station uses it ------------------------------------------

func (s *stationStreamState) streamedJobServed() error {
	s.up.tokens = []string{"a", "b", "c", "d"}
	s.up.gap = 80 * time.Millisecond // slower than the 50 ms batch, so it takes several chunks
	return s.postsStreamTrueQuick()
}

func (s *stationStreamState) eachChunkSigned() error {
	chunks := s.callsTo("/local/chunk")
	if len(chunks) < 2 {
		return fmt.Errorf("the station posted %d chunks, want several", len(chunks))
	}
	for i, c := range chunks {
		if c.header.Get(protocol.HeaderSig) == "" || c.header.Get(protocol.HeaderPubkey) == "" || c.header.Get(protocol.HeaderNonce) == "" {
			return fmt.Errorf("chunk %d was not a signed request with a nonce", i)
		}
		if c.status != http.StatusOK {
			return fmt.Errorf("chunk %d was answered %d %s", i, c.status, c.reply)
		}
	}
	return nil
}

type chunkWire struct {
	JobID string `json:"job_id"`
	Seq   *int   `json:"seq"`
	Data  string `json:"data"`
	Final bool   `json:"final"`
}

func (s *stationStreamState) chunkWires() ([]chunkWire, error) {
	var out []chunkWire
	for _, c := range s.callsTo("/local/chunk") {
		var cw chunkWire
		if err := json.Unmarshal(c.body, &cw); err != nil {
			return nil, err
		}
		out = append(out, cw)
	}
	return out, nil
}

func (s *stationStreamState) carriesIDSeqBytes() error {
	ws, err := s.chunkWires()
	if err != nil {
		return err
	}
	for i, cw := range ws {
		if cw.JobID == "" || cw.Seq == nil || *cw.Seq != i {
			return fmt.Errorf("chunk %d carried job %q seq %v", i, cw.JobID, cw.Seq)
		}
	}
	if !strings.Contains(ws[0].Data, "data: ") {
		return fmt.Errorf("the first chunk carried no SSE bytes: %q", ws[0].Data)
	}
	return nil
}

func (s *stationStreamState) lastFinal() error {
	ws, err := s.chunkWires()
	if err != nil {
		return err
	}
	for i, cw := range ws {
		if cw.Final != (i == len(ws)-1) {
			return fmt.Errorf("chunk %d of %d had final=%v", i, len(ws), cw.Final)
		}
	}
	return nil
}

// ---- 5. the consumer goes away ------------------------------------------------------------

func (s *stationStreamState) streamInProgress() error {
	// A long, slow reply: after the hold it keeps producing, so a station that ignores the 410
	// would still be reading it seconds later.
	toks := []string{"first"}
	for i := 0; i < 100; i++ {
		toks = append(toks, ".")
	}
	s.up.tokens = toks
	s.up.gap = 30 * time.Millisecond
	s.up.holdAfter = 1
	s.cons = s.post(promptWith("true"))
	return s.cons.waitContents(1, 5*time.Second)
}

func (s *stationStreamState) consumerDisconnects() error {
	s.cons.cancel()
	time.Sleep(100 * time.Millisecond) // the plane notices the closed socket
	close(s.up.release)
	return nil
}

func (s *stationStreamState) jobAbandoned() error {
	return s.waitFor(func() bool {
		for _, c := range s.callsTo("/local/chunk") {
			if c.status == http.StatusGone {
				return true
			}
		}
		return false
	}, "the station's next chunk was never refused as gone")
}

func (s *stationStreamState) next410ConsumerLeft() error {
	for _, c := range s.callsTo("/local/chunk") {
		if c.status == http.StatusGone && strings.Contains(string(c.reply), "the consumer left") {
			return nil
		}
	}
	return fmt.Errorf("no chunk was refused with 410 saying the consumer left")
}

func (s *stationStreamState) stationStopsUpstream() error {
	select {
	case <-s.up.cancelled:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the station kept reading its upstream after the 410")
	}
}

func (s *stationStreamState) receiptIfForwarded() error {
	recs, err := s.st.Receipts(0)
	if err != nil {
		return err
	}
	if len(recs) != 1 {
		return fmt.Errorf("%d receipts after a chunk reached the consumer, want 1", len(recs))
	}
	return nil
}

func (s *stationStreamState) streamOnShed() error { return s.streamInProgress() }

func (s *stationStreamState) towerAnswers410() error { return s.consumerDisconnects() }

func (s *stationStreamState) shedCancelsUpstream() error { return s.stationStopsUpstream() }

func (s *stationStreamState) shedPollsAgain() error {
	var after time.Time
	for _, c := range s.callsTo("/local/chunk") {
		if c.status == http.StatusGone {
			after = c.at
		}
	}
	return s.waitFor(func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, c := range s.arrivals {
			if c.path == "/local/poll" && c.at.After(after) {
				return true
			}
		}
		return false
	}, "shed did not poll for new work after the 410")
}

// ---- 6. batching ---------------------------------------------------------------------------

func (s *stationStreamState) thousandTokensInASecond() error {
	toks := make([]string, 1000)
	for i := range toks {
		toks[i] = "t"
	}
	s.up.tokens = toks
	s.up.gap = time.Millisecond
	// The wait's error is judged in the Then, AFTER the chunk count: a station that posts per
	// token is caught by the count ("at most 20"), not by a slow stream timing out.
	s.batchWait = s.postsStreamTrueQuick()
	return nil
}

func (s *stationStreamState) atMost20() error {
	ws, err := s.chunkWires()
	if err != nil {
		return err
	}
	data := 0
	for _, cw := range ws {
		if !cw.Final {
			data++
		}
	}
	// "1000 tokens in one second" is the fixture's aim, but each 1 ms gap can run long on a busy
	// machine, so the stream may span a little more than a second. The rule is the batch window:
	// at most one chunk per 50 ms of stream (20 for a second), plus one for the window the stream
	// ends in. A station posting per token posts about 1000 and is caught either way.
	s.up.mu.Lock()
	dur := s.up.ended.Sub(s.up.started)
	s.up.mu.Unlock()
	if dur <= 0 { // the stream has not ended (a station that posts per token is still going)
		dur = time.Second
	}
	allowed := 20
	if w := int((dur+streamBatchEvery-1)/streamBatchEvery) + 1; w > allowed {
		allowed = w
	}
	if data > allowed {
		return fmt.Errorf("the station posted %d chunks for %s of tokens, want at most %d (one per 50 ms window)", data, dur.Round(time.Millisecond), allowed)
	}
	if s.batchWait != nil {
		return s.batchWait
	}
	if got := len(strings.Join(s.cons.contents(), "")); got != 1000 {
		return fmt.Errorf("the consumer received %d tokens, want 1000", got)
	}
	return nil
}

// ---- 7. announcement and what the station puts on the wire ----------------------------------

func (s *stationStreamState) currentStationPolls() error { return s.postsStreamTrueQuick() }

func (s *stationStreamState) pollCarriesStreams() error {
	polls := s.callsTo("/local/poll")
	if len(polls) == 0 {
		return fmt.Errorf("no poll")
	}
	for _, p := range polls {
		if !bytes.Contains(p.body, []byte(`"streams":true`)) {
			return fmt.Errorf("a poll carried %q, want \"streams\": true", p.body)
		}
	}
	return nil
}

func (s *stationStreamState) handedAsStream() error {
	for _, p := range s.callsTo("/local/poll") {
		if p.status == http.StatusOK && bytes.Contains(p.reply, []byte(`"stream":true`)) {
			return nil
		}
	}
	return fmt.Errorf("the streamed job was not handed out as a stream")
}

func (s *stationStreamState) streamedRequestCompletes() error {
	s.up.echoKey = shedUpstreamKey
	return s.postsStreamTrueQuick()
}

func (s *stationStreamState) noIdentityInStream() error {
	s.cons.mu.Lock()
	body := s.cons.body.String()
	s.cons.mu.Unlock()
	for _, leak := range []string{idFromPriv(s.nodePv), s.up.srv.URL, strings.TrimPrefix(s.up.srv.URL, "http://"), shedUpstreamKey, "shed"} {
		if strings.Contains(body, leak) {
			return fmt.Errorf("the stream carried %q: %s", leak, body)
		}
	}
	if len(s.cons.contents()) == 0 {
		return fmt.Errorf("the stream carried no content at all")
	}
	return nil
}

func (s *stationStreamState) waitFor(cond func() bool, msg string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("%s", msg)
}

func TestStandaloneStreamingStationFeature(t *testing.T) {
	st := &stationStreamState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.cancel != nil {
					st.cancel()
				}
				for _, k := range st.consumers {
					k.cancel()
				}
				return c, nil
			})
			sc.Step(`^a standalone Tower serving its LAN with stations attached by key$`, st.towerUp)
			sc.Step(`^an admitted client$`, st.admitClient)
			sc.Step(`^an attached station "shed" that serves "qwen3-8b" from an upstream that can stream$`, st.attachShed)

			sc.Step(`^the client posts a chat completion for "qwen3-8b" with "stream": true$`, st.postsStreamTrueFor)
			sc.Step(`^the response has Content-Type "text/event-stream"$`, st.contentTypeSSE)
			sc.Step(`^it carries X-Roger-Cost "0" and X-Roger-Local "1" before the first event$`, st.freeLocalHeaders)
			sc.Step(`^the consumer receives the first content event before the station has finished$`, st.firstBeforeFinished)
			sc.Step(`^the events arrive in the order the upstream produced them$`, st.inOrder)
			sc.Step(`^the stream ends with a usage chunk and "data: \[DONE\]"$`, st.endsUsageDone)
			sc.Step(`^the station's upstream request has "stream": (true|false)$`, st.upstreamStream)
			sc.Step(`^the client posts a chat completion for "qwen3-8b" without "stream"$`, st.postsNonStream)
			sc.Step(`^the station returns the whole answer to /local/complete$`, st.returnedWhole)
			sc.Step(`^the consumer receives one JSON answer with Content-Type "application/json"$`, st.oneJSONAnswer)
			sc.Step(`^the bytes are identical to what the Tower returns today$`, st.bytesAsToday)
			sc.Step(`^the client posts a chat completion for "qwen3-8b" with stream set to (.+)$`, st.postsStreamSetTo)
			sc.Step(`^the consumer receives (an SSE stream|one JSON answer)$`, st.receivesShape)

			sc.Step(`^a streamed job is served$`, st.streamedJobServed)
			sc.Step(`^each chunk arrives on "/local/chunk" as a signed request$`, st.eachChunkSigned)
			sc.Step(`^each carries the job id, a sequence number, and the chunk bytes$`, st.carriesIDSeqBytes)
			sc.Step(`^the last carries "final": true$`, st.lastFinal)

			sc.Step(`^a streamed job is in progress$`, st.streamInProgress)
			sc.Step(`^the consumer disconnects$`, st.consumerDisconnects)
			sc.Step(`^the job is abandoned$`, st.jobAbandoned)
			sc.Step(`^the station's next chunk is refused with a 410 saying the consumer left$`, st.next410ConsumerLeft)
			sc.Step(`^the station stops reading its upstream$`, st.stationStopsUpstream)
			sc.Step(`^a local receipt is recorded only if at least one chunk reached the consumer$`, st.receiptIfForwarded)
			sc.Step(`^a streamed job is in progress on "shed"$`, st.streamOnShed)
			sc.Step(`^the Tower answers a chunk with 410$`, st.towerAnswers410)
			sc.Step(`^"shed" cancels its upstream request$`, st.shedCancelsUpstream)
			sc.Step(`^"shed" polls for new work$`, st.shedPollsAgain)

			sc.Step(`^the upstream produces 1000 tokens in one second$`, st.thousandTokensInASecond)
			sc.Step(`^the station posts at most 20 chunks for that second$`, st.atMost20)

			sc.Step(`^a current station polls "/local/poll"$`, st.currentStationPolls)
			sc.Step(`^its poll carries "streams": true$`, st.pollCarriesStreams)
			sc.Step(`^the Tower hands it streamed jobs as streamed jobs$`, st.handedAsStream)
			sc.Step(`^a streamed request completes$`, st.streamedRequestCompletes)
			sc.Step(`^no event names the station's key, its upstream URL or its upstream key$`, st.noIdentityInStream)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			// The station-loop scenarios, by line; bddrun fails this run if a line shifts.
			Paths: stationLines(50, 59, 65, 73, 103, 243, 252, 301, 331, 337),
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("standalone_streaming.feature (station) failed")
	}
}

// postsStreamTrueFor is "posts with stream: true": the first scenario reads timing, so its
// upstream holds after the first token; the others just complete.
func (s *stationStreamState) postsStreamTrueFor() error {
	if len(s.up.tokens) == 3 && s.up.gap == time.Millisecond && s.up.holdAfter == 0 && s.up.echoKey == "" {
		return s.postsStreamTrue()
	}
	return s.postsStreamTrueQuick()
}

func stationLines(lines ...int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = fmt.Sprintf("%s:%d", stationStreamFeature, l)
	}
	return out
}
