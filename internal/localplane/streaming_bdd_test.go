package localplane

// Executable spec: features/tower/standalone_streaming.feature, the @tower scenarios that do
// not need the real station loop (those run in internal/agent, beside ServeLocalTower) and
// the @structural one.
//
// The REAL plane: a tower.State on disk, this package's real Handler served over HTTP, a
// client and stations with real ed25519 keys. The "station" here is the wire peer, a signed
// HTTP caller of /local/poll, /local/chunk and /local/complete, so each refusal and each byte
// the consumer receives is the plane's own.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/structural"
	"rogerai.fm/roger/v7/internal/tower"
)

const streamFeature = "../../features/tower/standalone_streaming.feature"

// ---- the wire peers ------------------------------------------------------------------

// signedPost signs body with priv (always with a fresh nonce, as current roger does) and
// posts it; the request is returned too so a scenario can replay it verbatim.
func signedPost(base, path string, body []byte, priv ed25519.PrivateKey) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	nonce := protocol.NewNonce()
	pub, ts, sig := protocol.SignRequestNonce(priv, http.MethodPost, path, body, nonce)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	req.Header.Set(protocol.HeaderNonce, nonce)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func doBody(c *http.Client, req *http.Request) (int, string, error) {
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

// rawStation is a signed station on the wire.
type rawStation struct {
	name string
	priv ed25519.PrivateKey
	base string
	c    *http.Client
}

func (r *rawStation) poll(streams bool) (id string, stream bool, err error) {
	var body []byte
	if streams {
		body = []byte(`{"streams":true}`)
	}
	req, err := signedPost(r.base, "/local/poll", body, r.priv)
	if err != nil {
		return "", false, err
	}
	code, out, err := doBody(r.c, req)
	if err != nil {
		return "", false, err
	}
	if code != http.StatusOK {
		return "", false, fmt.Errorf("poll: status %d %s", code, out)
	}
	var j struct {
		JobID  string `json:"job_id"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		return "", false, err
	}
	return j.JobID, j.Stream, nil
}

func chunkBody(jobID string, seq int, data string, final bool, errMsg string) []byte {
	m := map[string]any{"job_id": jobID, "seq": seq, "data": data}
	if final {
		m["final"] = true
	}
	if errMsg != "" {
		m["error"] = errMsg
	}
	b, _ := json.Marshal(m)
	return b
}

func (r *rawStation) chunk(jobID string, seq int, data string, final bool, errMsg string) (int, string, *http.Request) {
	body := chunkBody(jobID, seq, data, final, errMsg)
	req, _ := signedPost(r.base, "/local/chunk", body, r.priv)
	replay := req.Clone(context.Background())
	replay.Body = io.NopCloser(bytes.NewReader(body))
	code, out, err := doBody(r.c, req)
	if err != nil {
		return 0, err.Error(), replay
	}
	return code, out, replay
}

func (r *rawStation) complete(jobID string, answer string) (int, string) {
	body, _ := json.Marshal(map[string]any{"job_id": jobID, "answer": json.RawMessage(answer)})
	req, _ := signedPost(r.base, "/local/complete", body, r.priv)
	code, out, err := doBody(r.c, req)
	if err != nil {
		return 0, err.Error()
	}
	return code, out
}

// sseConsumer is one consumer request in flight: its response headers and the SSE lines it
// has received so far, read as they arrive.
type sseConsumer struct {
	mu      sync.Mutex
	status  int
	header  http.Header
	lines   []string
	body    bytes.Buffer
	done    chan struct{}
	cancel  context.CancelFunc
	arrived chan string // each data line as it arrives
}

func (c *sseConsumer) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func (c *sseConsumer) dataLines() []string {
	var out []string
	for _, l := range c.snapshot() {
		if strings.HasPrefix(l, "data: ") {
			out = append(out, strings.TrimPrefix(l, "data: "))
		}
	}
	return out
}

func (c *sseConsumer) wait(d time.Duration) error {
	select {
	case <-c.done:
		return nil
	case <-time.After(d):
		return fmt.Errorf("the consumer's response did not end within %v", d)
	}
}

// ---- the scenario state --------------------------------------------------------------

type streamTowerState struct {
	t       *testing.T
	st      *tower.State
	srv     *Server
	http    *httptest.Server
	c       *http.Client
	client  ed25519.PrivateKey
	clientH string
	shed    *rawStation
	garage  *rawStation
	stray   *rawStation // a signed key that is not an attached station
	fake    *fakeClock

	cons       *sseConsumer
	consumers  []*sseConsumer
	jobID      string
	lastCode   int
	lastBody   string
	codes      []int
	replayReq  *http.Request
	unauth     []string
	streamFlag string
	extraCode  int
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *fakeClock) add(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func newKeyPair() ed25519.PrivateKey {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	return priv
}

func keyHashOf(priv ed25519.PrivateKey) string {
	return protocol.UserIDFromPubkey(hex.EncodeToString(priv.Public().(ed25519.PublicKey)))
}

func (s *streamTowerState) reset() {
	if s.http != nil {
		s.http.Close()
	}
	for _, c := range s.consumers {
		c.cancel()
	}
	*s = streamTowerState{t: s.t}
}

func (s *streamTowerState) towerUp() error {
	dir := s.t.TempDir()
	if _, err := tower.Init(dir, tower.ModeStandalone); err != nil {
		return err
	}
	st, err := tower.Open(dir)
	if err != nil {
		return err
	}
	s.st = st
	s.srv = New(st)
	s.srv.completionTimeout = 5 * time.Second
	s.http = httptest.NewServer(s.srv.Handler())
	s.c = &http.Client{Timeout: 30 * time.Second}
	s.stray = &rawStation{name: "stray", priv: newKeyPair(), base: s.http.URL, c: s.c}
	return nil
}

func (s *streamTowerState) admitClient() error {
	s.client = newKeyPair()
	s.clientH = keyHashOf(s.client)
	inv, code, err := s.st.CreateInvitation(s.clientH, time.Hour, 5)
	if err != nil {
		return err
	}
	_, err = s.st.ConsumeInvitation(inv.ID, code, s.clientH)
	return err
}

func (s *streamTowerState) attach(name string) (*rawStation, error) {
	priv := newKeyPair()
	if _, err := s.st.AttachStation(name, keyHashOf(priv), []string{"qwen3-8b"}); err != nil {
		return nil, err
	}
	return &rawStation{name: name, priv: priv, base: s.http.URL, c: s.c}, nil
}

func (s *streamTowerState) attachShed() error {
	var err error
	s.shed, err = s.attach("shed")
	return err
}

func (s *streamTowerState) attachGarage() error {
	var err error
	s.garage, err = s.attach("garage")
	return err
}

// attachShedCurated stands "shed" up as a curated proxy. A Station id cannot change kind, so
// the curated shed is a fresh id under a fresh key, and it is the one that takes the job.
func (s *streamTowerState) attachShedCurated() error {
	priv := newKeyPair()
	if _, err := s.st.AttachCuratedStation("shed-curated", keyHashOf(priv), []string{"qwen3-8b"}, "upstream-co"); err != nil {
		return err
	}
	s.shed = &rawStation{name: "shed-curated", priv: priv, base: s.http.URL, c: s.c}
	return nil
}

// post sends a consumer completion and returns at once; the response is read as it arrives.
func (s *streamTowerState) post(body string) *sseConsumer {
	ctx, cancel := context.WithCancel(context.Background())
	c := &sseConsumer{done: make(chan struct{}), cancel: cancel, arrived: make(chan string, 1024)}
	req, _ := signedPost(s.http.URL, "/v1/chat/completions", []byte(body), s.client)
	req = req.WithContext(ctx)
	go func() {
		defer close(c.done)
		resp, err := s.c.Do(req)
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
				l := strings.TrimRight(line, "\r\n")
				if l != "" {
					c.lines = append(c.lines, l)
				}
				c.mu.Unlock()
				if strings.HasPrefix(l, "data: ") {
					c.arrived <- strings.TrimPrefix(l, "data: ")
				}
			}
			if err != nil {
				return
			}
		}
	}()
	s.consumers = append(s.consumers, c)
	return c
}

func streamPrompt() string {
	return `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}],"stream":true}`
}

// startStream posts a streamed request and has shed take the job announcing chunk support.
func (s *streamTowerState) startStream() error {
	s.cons = s.post(streamPrompt())
	id, stream, err := s.shed.poll(true)
	if err != nil {
		return err
	}
	if !stream {
		return fmt.Errorf("the job was not handed out as a stream")
	}
	s.jobID = id
	return nil
}

func contentEvent(text string) string {
	b, _ := json.Marshal(map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}})
	return "data: " + string(b) + "\n\n"
}

// waitData waits until the consumer has received n data lines.
func (c *sseConsumer) waitData(n int, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if len(c.dataLines()) >= n {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("the consumer received %d data lines, want %d: %q", len(c.dataLines()), n, c.dataLines())
}

func (c *sseConsumer) contents() []string {
	var out []string
	for _, d := range c.dataLines() {
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

// ---- 1. the happy path ----------------------------------------------------------------

func (s *streamTowerState) aStreamedRequestCompletes() error {
	if err := s.startStream(); err != nil {
		return err
	}
	if code, out, _ := s.shed.chunk(s.jobID, 0, contentEvent("a"), false, ""); code != 200 {
		return fmt.Errorf("chunk 0: %d %s", code, out)
	}
	if code, out, _ := s.shed.chunk(s.jobID, 1, contentEvent("b"), true, ""); code != 200 {
		return fmt.Errorf("final chunk: %d %s", code, out)
	}
	return s.cons.wait(5 * time.Second)
}

func (s *streamTowerState) exactlyOneReceipt() error {
	recs, err := s.st.Receipts(0)
	if err != nil {
		return err
	}
	n := 0
	for _, r := range recs {
		if r.ClientKeyHash == s.clientH && r.StationID == "shed" && r.Model == "qwen3-8b" {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d local receipts for the client, shed and qwen3-8b, want exactly 1", n)
	}
	return nil
}

func (s *streamTowerState) receiptIsBookkeeping() error {
	recs, err := s.st.Receipts(0)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(recs)
	for _, bad := range []string{"price", "cost_usd", "amount", "wallet", "charge"} {
		if strings.Contains(strings.ToLower(string(b)), bad) {
			return fmt.Errorf("the local receipt carries a billing field %q: %s", bad, b)
		}
	}
	return nil
}

func (s *streamTowerState) curatedBeforeFirstEvent() error {
	s.cons.mu.Lock()
	defer s.cons.mu.Unlock()
	if s.cons.header.Get("X-Roger-Curated") != "upstream-co" {
		return fmt.Errorf("X-Roger-Curated = %q, want upstream-co (headers arrive before the first event)", s.cons.header.Get("X-Roger-Curated"))
	}
	return nil
}

// ---- 2. authentication -----------------------------------------------------------------

func (s *streamTowerState) unsignedChunk() error {
	if err := s.startStream(); err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodPost, s.http.URL+"/local/chunk", bytes.NewReader(chunkBody(s.jobID, 0, contentEvent("x"), false, "")))
	s.lastCode, s.lastBody, _ = doBody(s.c, req)
	return nil
}

func (s *streamTowerState) unattachedChunk() error {
	if err := s.startStream(); err != nil {
		return err
	}
	s.lastCode, s.lastBody, _ = s.stray.chunk(s.jobID, 0, contentEvent("x"), false, "")
	return nil
}

func (s *streamTowerState) garageChunksShedsJob() error {
	if err := s.startStream(); err != nil {
		return err
	}
	s.lastCode, s.lastBody, _ = s.garage.chunk(s.jobID, 0, contentEvent("x"), false, "")
	return nil
}

func (s *streamTowerState) unknownJobChunk() error {
	s.lastCode, s.lastBody, _ = s.shed.chunk("no-such-job", 0, contentEvent("x"), false, "")
	return nil
}

func (s *streamTowerState) uniform401() error {
	if s.lastCode != http.StatusUnauthorized || s.lastBody != `{"error":"unauthorized"}` {
		return fmt.Errorf("got %d %q, want the byte-identical uniform 401", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *streamTowerState) nothingReachedConsumer() error {
	time.Sleep(50 * time.Millisecond)
	if n := len(s.cons.dataLines()); n != 0 {
		return fmt.Errorf("the consumer received %d events: %q", n, s.cons.dataLines())
	}
	return nil
}

func (s *streamTowerState) shedCanStillComplete() error {
	if code, out, _ := s.shed.chunk(s.jobID, 0, contentEvent("from shed"), true, ""); code != 200 {
		return fmt.Errorf("shed's chunk: %d %s", code, out)
	}
	if err := s.cons.wait(5 * time.Second); err != nil {
		return err
	}
	if got := s.cons.contents(); len(got) != 1 || got[0] != "from shed" {
		return fmt.Errorf("the consumer received %q, want shed's chunk", got)
	}
	return nil
}

func (s *streamTowerState) unauthAllRoutes() error {
	for _, p := range []string{"/local/chunk", "/local/complete", "/local/poll", "/v1/chat/completions"} {
		req, _ := http.NewRequest(http.MethodPost, s.http.URL+p, strings.NewReader(`{}`))
		code, body, err := doBody(s.c, req)
		if err != nil {
			return err
		}
		s.unauth = append(s.unauth, fmt.Sprintf("%d %s", code, body))
	}
	return nil
}

func (s *streamTowerState) allFourIdentical() error {
	for _, u := range s.unauth[1:] {
		if u != s.unauth[0] {
			return fmt.Errorf("the refusals differ: %q", s.unauth)
		}
	}
	if !strings.HasPrefix(s.unauth[0], "401 ") {
		return fmt.Errorf("the refusal is %q, want a 401", s.unauth[0])
	}
	return nil
}

// ---- 3. sequence, replay, duplicates -----------------------------------------------------

func (s *streamTowerState) chunks012() error {
	if err := s.startStream(); err != nil {
		return err
	}
	for i, t := range []string{"zero", "one", "two"} {
		if code, out, _ := s.shed.chunk(s.jobID, i, contentEvent(t), i == 2, ""); code != 200 {
			return fmt.Errorf("chunk %d: %d %s", i, code, out)
		}
	}
	return s.cons.wait(5 * time.Second)
}

func (s *streamTowerState) received012() error {
	if got := strings.Join(s.cons.contents(), ","); got != "zero,one,two" {
		return fmt.Errorf("the consumer received %q, want zero,one,two", got)
	}
	return nil
}

func (s *streamTowerState) chunk0then2() error {
	if err := s.startStream(); err != nil {
		return err
	}
	if code, out, _ := s.shed.chunk(s.jobID, 0, contentEvent("zero"), false, ""); code != 200 {
		return fmt.Errorf("chunk 0: %d %s", code, out)
	}
	s.lastCode, s.lastBody, _ = s.shed.chunk(s.jobID, 2, contentEvent("two"), false, "")
	return nil
}

func (s *streamTowerState) refused409Expected1() error {
	if s.lastCode != http.StatusConflict {
		return fmt.Errorf("chunk 2 got %d %s, want 409", s.lastCode, s.lastBody)
	}
	var m struct {
		Expected *int `json:"expected_seq"`
	}
	if json.Unmarshal([]byte(s.lastBody), &m) != nil || m.Expected == nil || *m.Expected != 1 {
		return fmt.Errorf("the 409 %s does not name the expected sequence 1", s.lastBody)
	}
	return nil
}

func (s *streamTowerState) onlyChunk0() error {
	if err := s.cons.waitData(1, 3*time.Second); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	if got := s.cons.contents(); len(got) != 1 || got[0] != "zero" {
		return fmt.Errorf("the consumer received %q, want only chunk 0", got)
	}
	return nil
}

func (s *streamTowerState) chunk0Twice() error {
	if err := s.startStream(); err != nil {
		return err
	}
	if code, out, _ := s.shed.chunk(s.jobID, 0, contentEvent("zero"), false, ""); code != 200 {
		return fmt.Errorf("chunk 0: %d %s", code, out)
	}
	s.lastCode, s.lastBody, _ = s.shed.chunk(s.jobID, 0, contentEvent("zero"), false, "")
	return nil
}

func (s *streamTowerState) secondRefused409() error {
	if s.lastCode != http.StatusConflict {
		return fmt.Errorf("the duplicate got %d %s, want 409", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *streamTowerState) chunk0ExactlyOnce() error {
	if err := s.cons.waitData(1, 3*time.Second); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	if got := s.cons.contents(); len(got) != 1 {
		return fmt.Errorf("the consumer received %q, want chunk 0 exactly once", got)
	}
	return nil
}

func (s *streamTowerState) chunkCapturedAndReplayed() error {
	if err := s.startStream(); err != nil {
		return err
	}
	code, out, replay := s.shed.chunk(s.jobID, 0, contentEvent("zero"), false, "")
	if code != 200 {
		return fmt.Errorf("chunk 0: %d %s", code, out)
	}
	s.lastCode, s.lastBody, _ = doBody(s.c, replay)
	return nil
}

func (s *streamTowerState) replayIs401() error { return s.uniform401() }

func (s *streamTowerState) finalThenAnother() error {
	if err := s.startStream(); err != nil {
		return err
	}
	if code, out, _ := s.shed.chunk(s.jobID, 0, contentEvent("zero"), true, ""); code != 200 {
		return fmt.Errorf("final chunk: %d %s", code, out)
	}
	if err := s.cons.wait(5 * time.Second); err != nil {
		return err
	}
	s.extraCode, s.lastBody, _ = s.shed.chunk(s.jobID, 1, contentEvent("late"), false, "")
	return nil
}

func (s *streamTowerState) extraRefused() error {
	if s.extraCode == http.StatusOK {
		return fmt.Errorf("a chunk after the final was accepted: %s", s.lastBody)
	}
	return nil
}

func (s *streamTowerState) streamEndedWithDone() error {
	d := s.cons.dataLines()
	if len(d) == 0 || d[len(d)-1] != "[DONE]" {
		return fmt.Errorf("the stream did not end with [DONE]: %q", d)
	}
	for _, c := range s.cons.contents() {
		if c == "late" {
			return fmt.Errorf("the late chunk reached the consumer")
		}
	}
	return nil
}

func (s *streamTowerState) shedAnnouncedStreams() error { return s.startStream() }

func (s *streamTowerState) completeStreamedJob() error {
	s.lastCode, s.lastBody = s.shed.complete(s.jobID, `{"choices":[{"message":{"content":"whole"}}]}`)
	return nil
}

func (s *streamTowerState) refused409Streamed() error {
	if s.lastCode != http.StatusConflict || !strings.Contains(s.lastBody, "streamed") {
		return fmt.Errorf("got %d %s, want a 409 saying the job is streamed", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *streamTowerState) chunkForNonStream() error {
	s.cons = s.post(`{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`)
	id, stream, err := s.shed.poll(true)
	if err != nil {
		return err
	}
	if stream {
		return fmt.Errorf("a non-stream job was handed out as a stream")
	}
	s.jobID = id
	s.lastCode, s.lastBody, _ = s.shed.chunk(id, 0, contentEvent("x"), false, "")
	return nil
}

func (s *streamTowerState) refused409NotStreamed() error {
	if s.lastCode != http.StatusConflict || !strings.Contains(s.lastBody, "not streamed") {
		return fmt.Errorf("got %d %s, want a 409 saying the job is not streamed", s.lastCode, s.lastBody)
	}
	return nil
}

// ---- 4. failure mid-stream ----------------------------------------------------------------

func (s *streamTowerState) postedChunks01() error {
	s.srv.streamIdle = 300 * time.Millisecond
	if err := s.startStream(); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if code, out, _ := s.shed.chunk(s.jobID, i, contentEvent(strconv.Itoa(i)), false, ""); code != 200 {
			return fmt.Errorf("chunk %d: %d %s", i, code, out)
		}
	}
	return nil
}

func (s *streamTowerState) postedChunk0() error {
	if err := s.startStream(); err != nil {
		return err
	}
	if code, out, _ := s.shed.chunk(s.jobID, 0, contentEvent("zero"), false, ""); code != 200 {
		return fmt.Errorf("chunk 0: %d %s", code, out)
	}
	return nil
}

func (s *streamTowerState) noFurtherChunk() error { return s.cons.wait(5 * time.Second) }

func (s *streamTowerState) errorEventSaying(want string) error {
	if err := s.cons.wait(5 * time.Second); err != nil {
		return err
	}
	for _, d := range s.cons.dataLines() {
		var m struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(d), &m) == nil && strings.Contains(m.Error.Message, want) {
			return nil
		}
	}
	return fmt.Errorf("no error event saying %q in %q", want, s.cons.dataLines())
}

func (s *streamTowerState) stationStoppedEvent() error {
	return s.errorEventSaying("the local station stopped mid-reply")
}

func (s *streamTowerState) usageZero() error {
	for _, d := range s.cons.dataLines() {
		var m struct {
			Usage *struct {
				Cost *float64 `json:"cost"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(d), &m) == nil && m.Usage != nil && m.Usage.Cost != nil && *m.Usage.Cost == 0 {
			return nil
		}
	}
	return fmt.Errorf("no usage chunk with cost 0 in %q", s.cons.dataLines())
}

func (s *streamTowerState) doneLast() error { return s.streamEndedWithDone() }

func (s *streamTowerState) usageZeroAndDone() error {
	if err := s.usageZero(); err != nil {
		return err
	}
	return s.streamEndedWithDone()
}

func (s *streamTowerState) abandonedLateRefused() error {
	code, out, _ := s.shed.chunk(s.jobID, 2, contentEvent("late"), false, "")
	if code == http.StatusOK {
		return fmt.Errorf("a late chunk was accepted: %s", out)
	}
	return nil
}

func (s *streamTowerState) finalWithError() error {
	code, out, _ := s.shed.chunk(s.jobID, 1, "", true, "upstream said: invalid key sk-shed-upstream-secret")
	if code != 200 {
		return fmt.Errorf("the error chunk: %d %s", code, out)
	}
	return nil
}

func (s *streamTowerState) upstreamFailureEvent() error {
	return s.errorEventSaying("local upstream failure")
}

func (s *streamTowerState) upstreamTextNotEchoed() error {
	s.cons.mu.Lock()
	defer s.cons.mu.Unlock()
	if strings.Contains(s.cons.body.String(), "sk-shed-upstream-secret") || strings.Contains(s.cons.body.String(), "invalid key") {
		return fmt.Errorf("the station's upstream error text reached the consumer: %s", s.cons.body.String())
	}
	return nil
}

func (s *streamTowerState) garageAlsoServes() error { return s.attachGarage() }

func (s *streamTowerState) shedTookAndPosted0() error {
	s.srv.streamIdle = 300 * time.Millisecond
	return s.postedChunk0()
}

func (s *streamTowerState) shedStops() error { return s.cons.wait(5 * time.Second) }

func (s *streamTowerState) notOfferedToGarage() error {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.srv.pollTimeout = 200 * time.Millisecond
	if j, ok := s.srv.q.poll(ctx, "garage", []string{"qwen3-8b"}, true); ok {
		return fmt.Errorf("the broken job %s was offered to garage", j.id)
	}
	return nil
}

func (s *streamTowerState) errorNotSecondReply() error {
	if got := s.cons.contents(); len(got) != 1 {
		return fmt.Errorf("the consumer received %q, want only chunk 0 then the error event", got)
	}
	return s.stationStoppedEvent()
}

func (s *streamTowerState) noStationTakes() error {
	s.srv.completionTimeout = 200 * time.Millisecond
	s.cons = s.post(streamPrompt())
	return s.cons.wait(5 * time.Second)
}

func (s *streamTowerState) plain504() error {
	s.cons.mu.Lock()
	defer s.cons.mu.Unlock()
	if s.cons.status != http.StatusGatewayTimeout || !strings.Contains(s.cons.body.String(), "no local station served this request in time") {
		return fmt.Errorf("got %d %s, want the plain 504", s.cons.status, s.cons.body.String())
	}
	return nil
}

func (s *streamTowerState) noSSEHeaders() error {
	s.cons.mu.Lock()
	defer s.cons.mu.Unlock()
	if strings.Contains(s.cons.header.Get("Content-Type"), "event-stream") {
		return fmt.Errorf("SSE headers were sent on a 504")
	}
	return nil
}

// ---- 6. limits ------------------------------------------------------------------------------

func (s *streamTowerState) chunkOverCap() error {
	if err := s.startStream(); err != nil {
		return err
	}
	s.lastCode, s.lastBody, _ = s.shed.chunk(s.jobID, 0, strings.Repeat("x", 256<<10+1), false, "")
	return nil
}

func (s *streamTowerState) refused413() error {
	if s.lastCode != http.StatusRequestEntityTooLarge {
		return fmt.Errorf("got %d %s, want 413", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *streamTowerState) errorUsageDone() error {
	if err := s.cons.wait(5 * time.Second); err != nil {
		return err
	}
	if err := s.errorEventSaying("size limit"); err != nil {
		return err
	}
	return s.usageZeroAndDone()
}

func (s *streamTowerState) chunksOverTotal() error {
	if err := s.startStream(); err != nil {
		return err
	}
	piece := "data: " + strings.Repeat("y", 250<<10) + "\n\n" // one whole SSE event per chunk
	for seq := 0; seq < 40; seq++ {
		code, out, _ := s.shed.chunk(s.jobID, seq, piece, false, "")
		if code == http.StatusOK {
			continue
		}
		s.lastCode, s.lastBody = code, out
		return nil
	}
	return fmt.Errorf("40 chunks of 250 KiB (over 8 MiB) were all accepted")
}

func (s *streamTowerState) endedTooLarge() error {
	return s.errorEventSaying("the reply exceeded the size limit")
}

func (s *streamTowerState) furtherRefused() error {
	code, out, _ := s.shed.chunk(s.jobID, 99, "z", false, "")
	if code == http.StatusOK {
		return fmt.Errorf("a chunk after the size limit was accepted: %s", out)
	}
	return nil
}

func (s *streamTowerState) stillSendingAfterMax() error {
	if s.srv.streamMax != 10*time.Minute {
		return fmt.Errorf("the stream duration cap is %v, want 10 minutes", s.srv.streamMax)
	}
	s.srv.streamMax = 400 * time.Millisecond
	if err := s.startStream(); err != nil {
		return err
	}
	for seq := 0; seq < 100; seq++ {
		code, _, _ := s.shed.chunk(s.jobID, seq, contentEvent("."), false, "")
		if code != http.StatusOK {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func (s *streamTowerState) endedTooLong() error {
	return s.errorEventSaying("the reply ran too long")
}

func (s *streamTowerState) concurrencyCaps() error {
	if defaultMaxInFlightPerClient < 1 || defaultMaxInFlight < defaultMaxInFlightPerClient {
		return fmt.Errorf("the caps are not set")
	}
	return nil
}

func (s *streamTowerState) clientHasCapOpen() error {
	for i := 0; i < defaultMaxInFlightPerClient; i++ {
		c := s.post(streamPrompt())
		id, _, err := s.shed.poll(true)
		if err != nil {
			return err
		}
		if code, out, _ := s.shed.chunk(id, 0, contentEvent("open"), false, ""); code != 200 {
			return fmt.Errorf("opening stream %d: %d %s", i, code, out)
		}
		if err := c.waitData(1, 3*time.Second); err != nil {
			return err
		}
		if i == 0 {
			s.jobID, s.cons = id, c
		}
	}
	return nil
}

func (s *streamTowerState) nextRefused429() error {
	c := s.post(streamPrompt())
	if err := c.wait(3 * time.Second); err != nil {
		return err
	}
	if c.status != http.StatusTooManyRequests {
		return fmt.Errorf("the next request got %d, want 429", c.status)
	}
	return nil
}

func (s *streamTowerState) oneEndsNextAdmitted() error {
	if code, out, _ := s.shed.chunk(s.jobID, 1, "", true, ""); code != 200 {
		return fmt.Errorf("ending a stream: %d %s", code, out)
	}
	if err := s.cons.wait(3 * time.Second); err != nil {
		return err
	}
	c := s.post(streamPrompt())
	if _, _, err := s.shed.poll(true); err != nil {
		return fmt.Errorf("the next request was not admitted: %v", err)
	}
	c.cancel()
	return nil
}

func (s *streamTowerState) fiveHundredChunks() error {
	// The station posts at its batching pace (a chunk every 50 ms), on a clock the test turns.
	s.fake = &fakeClock{t: time.Unix(1_800_000_000, 0)}
	s.srv.chunkRL = newRateLimiter(s.fake.now, defaultChunkRate, defaultChunkBurst)
	s.srv.stationRL = newRateLimiter(s.fake.now, defaultStationRate, defaultStationBurst)
	if err := s.startStream(); err != nil {
		return err
	}
	go func() {
		for range s.cons.arrived {
		}
	}()
	for seq := 0; seq < 500; seq++ {
		s.fake.add(50 * time.Millisecond)
		code, out, _ := s.shed.chunk(s.jobID, seq, contentEvent("t"), seq == 499, "")
		s.codes = append(s.codes, code)
		if code != http.StatusOK {
			s.lastBody = out
		}
	}
	return nil
}

func (s *streamTowerState) noneRateRefused() error {
	for i, c := range s.codes {
		if c != http.StatusOK {
			return fmt.Errorf("chunk %d was refused with %d %s", i, c, s.lastBody)
		}
	}
	return nil
}

// ---- 7. structure and compatibility ----------------------------------------------------------

func (s *streamTowerState) depsChecked() error { return nil }

func (s *streamTowerState) linksNoCore() error {
	out, err := exec.Command("go", "list", "-deps", "rogerai.fm/roger/v7/internal/localplane/serve").Output()
	if err != nil {
		return err
	}
	for _, dep := range strings.Split(string(out), "\n") {
		for _, bad := range []string{"internal/towerjoin", "internal/towercore", "internal/towerhub"} {
			if strings.Contains(dep, bad) {
				return fmt.Errorf("the standalone serve package links %s", dep)
			}
		}
	}
	return nil
}

func (s *streamTowerState) noOutboundInLocalplane() error {
	if n := structural.ScanSource(s.t, ".", structural.NoOutbound, structural.NoListen); n < 2 {
		return fmt.Errorf("the source scan read %d files", n)
	}
	return nil
}

func (s *streamTowerState) olderStation() error { return nil }

func (s *streamTowerState) olderStationTakesStream() error {
	s.cons = s.post(streamPrompt())
	id, stream, err := s.shed.poll(false)
	if err != nil {
		return err
	}
	if stream {
		return fmt.Errorf("a station that announced no chunk support was handed a stream")
	}
	s.jobID = id
	return nil
}

func (s *streamTowerState) olderCompletes() error {
	code, out := s.shed.complete(s.jobID, `{"choices":[{"message":{"role":"assistant","content":"whole answer"}}]}`)
	if code != 200 {
		return fmt.Errorf("the older station's complete: %d %s", code, out)
	}
	return s.cons.wait(5 * time.Second)
}

func (s *streamTowerState) singleChunkSSE() error {
	s.cons.mu.Lock()
	ct := s.cons.header.Get("Content-Type")
	s.cons.mu.Unlock()
	if !strings.Contains(ct, "text/event-stream") {
		return fmt.Errorf("Content-Type %q, want an SSE stream", ct)
	}
	if got := s.cons.contents(); len(got) != 1 || got[0] != "whole answer" {
		return fmt.Errorf("the consumer received %q, want the whole answer as one chunk", got)
	}
	return s.streamEndedWithDone()
}

func streamLines(lines ...int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = fmt.Sprintf("%s:%d", streamFeature, l)
	}
	return out
}

// ---- the suite -------------------------------------------------------------------------------

func TestStandaloneStreamingTowerFeature(t *testing.T) {
	st := &streamTowerState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				for _, k := range st.consumers {
					k.cancel()
				}
				return c, nil
			})
			sc.Step(`^a standalone Tower serving its LAN with stations attached by key$`, st.towerUp)
			sc.Step(`^an admitted client$`, st.admitClient)
			sc.Step(`^an attached station "shed" that serves "qwen3-8b" from an upstream that can stream$`, st.attachShed)
			sc.Step(`^a streamed request completes$`, st.aStreamedRequestCompletes)
			sc.Step(`^exactly one local receipt is recorded for the client, the station and "qwen3-8b"$`, st.exactlyOneReceipt)
			sc.Step(`^it is bookkeeping only, never a billing shape$`, st.receiptIsBookkeeping)
			sc.Step(`^the station "shed" is attached as curated$`, st.attachShedCurated)
			sc.Step(`^the response carried X-Roger-Curated before the first event$`, st.curatedBeforeFirstEvent)

			sc.Step(`^a chunk for a live job arrives without a signature$`, st.unsignedChunk)
			sc.Step(`^a chunk for a live job arrives signed by a key that is not an attached station$`, st.unattachedChunk)
			sc.Step(`^a second attached station "garage"$`, st.attachGarage)
			sc.Step(`^"garage" posts a chunk for the job "shed" took$`, st.garageChunksShedsJob)
			sc.Step(`^a station posts a chunk for a job id that does not exist$`, st.unknownJobChunk)
			sc.Step(`^the Tower answers the byte-identical uniform 401$`, st.uniform401)
			sc.Step(`^nothing reaches the consumer$`, st.nothingReachedConsumer)
			sc.Step(`^"shed" can still complete the job$`, st.shedCanStillComplete)
			sc.Step(`^an unauthenticated request is sent to "/local/chunk", "/local/complete", "/local/poll" and "/v1/chat/completions"$`, st.unauthAllRoutes)
			sc.Step(`^all four answers are byte-identical$`, st.allFourIdentical)

			sc.Step(`^the station posts chunks 0, 1 and 2 in order$`, st.chunks012)
			sc.Step(`^the consumer receives chunk 0, then 1, then 2$`, st.received012)
			sc.Step(`^the station posts chunk 0 and then chunk 2$`, st.chunk0then2)
			sc.Step(`^chunk 2 is refused with a 409 naming the expected sequence 1$`, st.refused409Expected1)
			sc.Step(`^the consumer has received only chunk 0$`, st.onlyChunk0)
			sc.Step(`^the station posts chunk 0 twice with fresh nonces$`, st.chunk0Twice)
			sc.Step(`^the second is refused with a 409$`, st.secondRefused409)
			sc.Step(`^the consumer receives chunk 0 exactly once$`, st.chunk0ExactlyOnce)
			sc.Step(`^a chunk request is captured and replayed with the same nonce$`, st.chunkCapturedAndReplayed)
			sc.Step(`^the replay is the byte-identical uniform 401$`, st.replayIs401)
			sc.Step(`^the consumer receives that chunk exactly once$`, st.chunk0ExactlyOnce)
			sc.Step(`^the station posts the final chunk and then another chunk$`, st.finalThenAnother)
			sc.Step(`^the extra chunk is refused$`, st.extraRefused)
			sc.Step(`^the consumer's stream already ended with "data: \[DONE\]"$`, st.streamEndedWithDone)
			sc.Step(`^the station announced "streams": true when it polled$`, st.shedAnnouncedStreams)
			sc.Step(`^the station posts a whole answer to "/local/complete" for a streamed job$`, st.completeStreamedJob)
			sc.Step(`^it is refused with a 409 saying the job is streamed$`, st.refused409Streamed)
			sc.Step(`^the station posts a chunk for a job the consumer asked for without a stream$`, st.chunkForNonStream)
			sc.Step(`^it is refused with a 409 saying the job is not streamed$`, st.refused409NotStreamed)

			sc.Step(`^the station has posted chunks 0 and 1$`, st.postedChunks01)
			sc.Step(`^no further chunk arrives within the stream idle timeout$`, st.noFurtherChunk)
			sc.Step(`^the consumer receives an error event saying the local station stopped mid-reply$`, st.stationStoppedEvent)
			sc.Step(`^a usage chunk with cost 0$`, st.usageZero)
			sc.Step(`^"data: \[DONE\]"$`, st.doneLast)
			sc.Step(`^the job is abandoned so a late chunk is refused$`, st.abandonedLateRefused)
			sc.Step(`^the station has posted chunk 0$`, st.postedChunk0)
			sc.Step(`^the station posts a final chunk carrying an error$`, st.finalWithError)
			sc.Step(`^the consumer receives an error event naming a local upstream failure$`, st.upstreamFailureEvent)
			sc.Step(`^a usage chunk with cost 0 and "data: \[DONE\]"$`, st.usageZeroAndDone)
			sc.Step(`^the upstream's own error text is not echoed if it carries the station's upstream key$`, st.upstreamTextNotEchoed)
			sc.Step(`^a second attached station "garage" also serves "qwen3-8b"$`, st.garageAlsoServes)
			sc.Step(`^"shed" took a streamed job and posted chunk 0$`, st.shedTookAndPosted0)
			sc.Step(`^"shed" stops sending$`, st.shedStops)
			sc.Step(`^the job is not offered to "garage"$`, st.notOfferedToGarage)
			sc.Step(`^the consumer receives the error event, not a second reply$`, st.errorNotSecondReply)
			sc.Step(`^no station takes the streamed job within the completion timeout$`, st.noStationTakes)
			sc.Step(`^the consumer receives HTTP 504 with "no local station served this request in time"$`, st.plain504)
			sc.Step(`^no SSE headers were sent$`, st.noSSEHeaders)

			sc.Step(`^the station posts a chunk larger than 256 KiB$`, st.chunkOverCap)
			sc.Step(`^it is refused with 413$`, st.refused413)
			sc.Step(`^the consumer receives an error event, a zero-cost usage chunk and "data: \[DONE\]"$`, st.errorUsageDone)
			sc.Step(`^the station's chunks for one job exceed 8 MiB in total$`, st.chunksOverTotal)
			sc.Step(`^the stream is ended with an error event saying the reply exceeded the size limit$`, st.endedTooLarge)
			sc.Step(`^further chunks are refused$`, st.furtherRefused)
			sc.Step(`^a streamed job is still sending after 10 minutes$`, st.stillSendingAfterMax)
			sc.Step(`^the stream is ended with an error event saying the reply ran too long$`, st.endedTooLong)
			sc.Step(`^the Tower's whole-Tower and per-client concurrency caps$`, st.concurrencyCaps)
			sc.Step(`^a client has the per-client cap of streams open$`, st.clientHasCapOpen)
			sc.Step(`^its next request is refused with 429$`, st.nextRefused429)
			sc.Step(`^when one stream ends, the next request is admitted$`, st.oneEndsNextAdmitted)
			sc.Step(`^a station streams a normal reply of 500 chunks$`, st.fiveHundredChunks)
			sc.Step(`^no chunk is refused by the station rate limit$`, st.noneRateRefused)

			sc.Step(`^the dependency graph of the standalone serve package \(internal/localplane/serve\) is checked$`, st.depsChecked)
			sc.Step(`^it links none of towerjoin, towercore or towerhub$`, st.linksNoCore)
			sc.Step(`^the source-scan gate finds no outbound call in internal/localplane$`, st.noOutboundInLocalplane)
			sc.Step(`^an attached station running a roger version without chunk support$`, st.olderStation)
			sc.Step(`^the client posts a streamed request and that station takes it$`, st.olderStationTakesStream)
			sc.Step(`^the station returns the whole answer to "/local/complete"$`, st.olderCompletes)
			sc.Step(`^the consumer receives it as a single-chunk SSE stream ending with "data: \[DONE\]"$`, st.singleChunkSSE)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			// The Tower-only scenarios, by line: the @tower scenarios about the real station loop
			// (chunk route use, the streams announcement, what the station puts on the wire) run
			// in internal/agent beside ServeLocalTower; bddrun fails this run if a line shifts.
			Paths: streamLines(87, 93, 110, 116, 122, 130, 136, 146, 151, 157, 163, 169, 175, 182,
				191, 200, 208, 217, 263, 271, 278, 286, 293, 313, 320),
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("standalone_streaming.feature (tower) failed")
	}
}
