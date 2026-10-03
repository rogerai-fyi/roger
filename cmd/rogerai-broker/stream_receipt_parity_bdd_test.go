package main

// stream_receipt_parity_bdd_test.go makes features/trust/stream_receipt_parity.feature
// EXECUTABLE against the REAL broker: the upstream-failover harness (foState via rpState: a real
// relayBroker over the real store - Postgres when ROGERAI_TEST_DATABASE_URL is set - a real
// shared store over miniredis, and real stations that stream through /agent/stream and post
// their signed receipts through /agent/result). Each station's local model is a scripted
// httptest upstream; nothing in the broker is stubbed.
//
// Observation points (stated, so a step's strength is visible):
//   - The consumer is an httptest recorder wrapped in sr3Writer, which also records the wall
//     time of every write: "forwarded as they arrive" is read from the gaps between content
//     writes (the script spaces its frames 150 ms apart), not from the final body alone.
//   - "at the first frame" reads the header snapshot the recorder takes at the first
//     WriteHeader (httptest.ResponseRecorder.Result), i.e. what was committed with the first
//     frame - never the live header map, which a late Set would still change.
//   - The tokenizer sidecar is this runner's own: text carrying the completion marker SR3C is
//     counted as the scenario's completion re-count, everything else as its prompt re-count
//     (default 1,000,000 = "never below the claim"), so billed = min(claim, re-count) is set
//     per scenario exactly.
//   - The request id is the shortest job id the stations were dispatched in this relay (the
//     first attempt's id IS the request id; a failover's is "<id>-n", cooling.go attemptID).
//   - "the ledger debit" is the summed Cost of the consumer's ledger entries whose request id is
//     the request or one of its attempts (store.RecentByUser).
//
// Scenarios driven elsewhere carry @proxy / @tui / @harness (the local proxy, the TUI meter,
// the agent harness meter); the key-funded scenario is @slice5 (no key object exists yet); the
// Tower-relay-key receipt signature is @later (founder-approved deferral: the broker never holds
// a Tower's hub key).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"crypto/rand"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// sr3Marker tags every scripted completion so the runner's tokenizer can tell a completion
// re-count from a prompt re-count.
const sr3Marker = "SR3C"

// sr3Write is one write the consumer received, with its wall time.
type sr3Write struct {
	at time.Time
	b  []byte
}

// sr3Writer is the consumer side of a relay: a recorder that also keeps every write with its
// time, and can cancel the request the moment the first content frame arrives (the disconnect
// scenario), recording what was written after that.
type sr3Writer struct {
	*recWriter
	mu         sync.Mutex
	writes     []sr3Write
	cancelOn   bool
	cancel     context.CancelFunc
	cancelled  bool
	postCancel []byte
}

func (w *sr3Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	cp := append([]byte(nil), p...)
	w.writes = append(w.writes, sr3Write{at: time.Now(), b: cp})
	if w.cancelled {
		w.postCancel = append(w.postCancel, cp...)
	}
	if w.cancelOn && !w.cancelled && bytes.Contains(p, []byte(sr3Marker)) {
		w.cancelled = true
		w.cancel()
	}
	w.mu.Unlock()
	return w.recWriter.Write(p)
}

func (w *sr3Writer) Flush() {}

type sr3State struct {
	*rpState

	sidecar       *httptest.Server
	recPrompt     int // the sidecar's prompt re-count (0 = 1,000,000)
	recCompletion int // the sidecar's completion re-count (0 = 1,000,000)

	scripted     map[string]bool // stations a Given scripted (the rest get the default stream)
	before       map[string]int  // results posted per station before the last relay
	reqID        string
	w            *sr3Writer
	t0, t1       time.Time
	ctxCancel    bool
	keepalives   int
	contentN     int    // content frames the scripts emitted (for the old-client view)
	expectText   string // the completion the scripts emitted, assembled
	stationUsage string // a station's own usage frame line, forwarded verbatim
	forged       string // a forged marker a station or Tower injected
	promptText   string // the message text the request carried
	quoteHdr     http.Header
	nsCode       int
	nsHdr        http.Header
	nsBody       []byte
	idle         time.Duration
	stopCustom   chan struct{}
	customWG     sync.WaitGroup
	wrapped      *mf1Store
	tpsBefore    map[string]float64
	towerSrvUp   []*httptest.Server
	auth         func(r *http.Request, body []byte) // nil = sign as the consumer
}

func (s *sr3State) reset() error {
	s.teardown()
	if err := s.rpState.resetPins(); err != nil {
		return err
	}
	s.recPrompt, s.recCompletion = 0, 0
	s.sidecar = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		n := s.recPrompt
		if strings.Contains(in.Text, sr3Marker) {
			n = s.recCompletion
		}
		if n == 0 {
			n = 1_000_000
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": n, "exact": true})
	}))
	s.b.recount = recountConfig{url: s.sidecar.URL, tolerance: 0.02, strikeTolerance: 0.25, client: &http.Client{Timeout: 4 * time.Second}}
	s.b.lockWin = 24 * time.Hour
	s.scripted, s.before = map[string]bool{}, map[string]int{}
	s.reqID, s.w, s.ctxCancel = "", nil, false
	s.keepalives, s.contentN, s.expectText, s.stationUsage, s.forged, s.promptText = 0, 0, "", "", "", ""
	s.quoteHdr, s.nsCode, s.nsHdr, s.nsBody = nil, 0, nil, nil
	s.idle = 0
	s.stopCustom = make(chan struct{})
	s.wrapped = nil
	s.tpsBefore = map[string]float64{}
	s.auth = nil
	return nil
}

func (s *sr3State) teardown() {
	if s.stopCustom != nil {
		close(s.stopCustom)
		s.customWG.Wait()
		s.stopCustom = nil
	}
	if s.sidecar != nil {
		s.sidecar.Close()
		s.sidecar = nil
	}
	for _, u := range s.towerSrvUp {
		u.Close()
	}
	s.towerSrvUp = nil
	if s.rpState != nil && s.foState != nil {
		s.teardownPins()
	}
}

// --- scripts ------------------------------------------------------------------------

type sr3Frame struct {
	line  string
	sleep time.Duration
}

func sr3Content(i int, name string) string {
	return fmt.Sprintf(`data: {"choices":[{"delta":{"content":"%s part %d from %s "}}]}`, sr3Marker, i, name)
}

func sr3Usage(p, c int) string {
	return fmt.Sprintf(`data: {"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, p, c)
}

const sr3Done = "data: [DONE]"

// sse scripts station name to answer every request with these SSE events, in order. The
// station loop captures the usage frame as the receipt's claim (upstream_failover harness).
func (s *sr3State) sse(name string, frames []sr3Frame) {
	s.scripted[name] = true
	for _, f := range frames {
		if strings.Contains(f.line, sr3Marker) && strings.HasPrefix(f.line, "data:") && strings.Contains(f.line, `"content"`) {
			s.contentN++
			var d struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(f.line, "data: ")), &d) == nil && len(d.Choices) > 0 {
				s.expectText += d.Choices[0].Delta.Content
			}
		}
	}
	s.st(name).set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, f := range frames {
			if f.sleep > 0 {
				time.Sleep(f.sleep)
			}
			if f.line == "" {
				continue
			}
			fmt.Fprint(w, f.line+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	})
}

// defaultStream is what an unscripted station streams: two content frames, its usage claim
// (40 prompt, 20 completion tokens), then [DONE].
func (s *sr3State) defaultStream(name string, p, c int) {
	s.sse(name, []sr3Frame{{line: sr3Content(1, name)}, {line: sr3Content(2, name)}, {line: sr3Usage(p, c)}, {line: sr3Done}})
}

// --- Background -----------------------------------------------------------------------

func (s *sr3State) onAirInOut(name, model string, in, out float64) error {
	s.model = model
	s.station(name, in, out)
	return nil
}

func (s *sr3State) fundedWallet() error { return s.ensureFunded() }

// --- relays -------------------------------------------------------------------------

func (s *sr3State) snapshotResults() {
	s.before = map[string]int{}
	for n, st := range s.stations {
		s.before[n] = len(st.postedResults())
	}
}

// dispatchedIDs is every job id a station was dispatched in the last relay, by station.
func (s *sr3State) dispatchedIDs() map[string][]string {
	out := map[string][]string{}
	for n, st := range s.stations {
		res := st.postedResults()
		for _, r := range res[s.before[n]:] {
			if id, _ := r["id"].(string); id != "" {
				out[n] = append(out[n], id)
			}
		}
	}
	return out
}

var sr3AttemptSuffix = regexp.MustCompile(`-\d+$`)

func (s *sr3State) resolveReqID() {
	s.reqID = ""
	// The relay names its request id on every response (X-RogerAI-Request-Id, founder ruling
	// 2026-10-02); the dispatched-job and receipt reads below remain for a response without it.
	if v := s.lastHdr.Get("X-RogerAI-Request-Id"); v != "" {
		s.reqID = v
		return
	}
	var ids []string
	for _, l := range s.dispatchedIDs() {
		ids = append(ids, l...)
	}
	sort.Slice(ids, func(i, j int) bool {
		return len(ids[i]) < len(ids[j]) || (len(ids[i]) == len(ids[j]) && ids[i] < ids[j])
	})
	if len(ids) > 0 {
		s.reqID = sr3AttemptSuffix.ReplaceAllString(ids[0], "")
		return
	}
	// The bridge path dispatches no direct station: read the request id off the receipt the
	// answer carries (the X-RogerAI-Receipt header, or the stream chunk's).
	if enc := s.lastHdr.Get("X-RogerAI-Receipt"); enc != "" {
		if rec, err := protocol.DecodeReceipt(enc); err == nil {
			s.reqID = sr3AttemptSuffix.ReplaceAllString(rec.RequestID, "")
			return
		}
	}
	if ch, err := s.chunk(); err == nil {
		if rec, err := s.chunkReceipt(ch); err == nil {
			s.reqID = sr3AttemptSuffix.ReplaceAllString(rec.RequestID, "")
		}
	}
}

// send fires one real relay with the scenario's shaping (rpState.requestBody + headers) through
// an sr3Writer, then resolves the request id.
func (s *sr3State) send(stream bool) error {
	for n := range s.stations {
		if !s.scripted[n] {
			s.defaultStream(n, 40, 20)
		}
	}
	if len(s.stations) > 1 {
		s.landOrder()
	}
	s.heartbeat()
	priv, err := s.caller()
	if err != nil {
		return err
	}
	body := s.requestBody(stream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	if s.auth != nil {
		s.auth(r, body)
	} else {
		signReq(r, priv, body)
	}
	if s.pref != "" {
		r.Header.Set("X-Roger-Pref", s.pref)
	}
	if s.minTPS != "" {
		r.Header.Set("X-Roger-Min-TPS", s.minTPS)
	}
	w := &sr3Writer{recWriter: &recWriter{ResponseRecorder: httptest.NewRecorder()}, cancelOn: s.ctxCancel, cancel: cancel}
	s.snapshotResults()
	s.t0 = time.Now()
	s.b.relay(w, r)
	s.t1 = time.Now()
	s.w = w
	s.lastRec, s.lastCode, s.lastBody, s.lastHdr = w.recWriter, w.Code, w.Body.Bytes(), w.Header()
	s.batch = append(s.batch, rpResult{code: w.Code, hdr: w.Header(), body: w.Body.Bytes()})
	s.resolveReqID()
	return nil
}

func (s *sr3State) streamServed(model string) error {
	s.model = model
	if err := s.send(true); err != nil {
		return err
	}
	if s.lastCode != 200 {
		return fmt.Errorf("stream = %d, want 200: %.400s", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *sr3State) streamServedBy(model, name string) error {
	if err := s.streamServed(model); err != nil {
		return err
	}
	if got := s.lastHdr.Get("X-RogerAI-Provider"); got != s.st(name).id {
		return fmt.Errorf("X-RogerAI-Provider = %q, want %s (%s)", got, name, s.st(name).id)
	}
	return nil
}

func (s *sr3State) streamServedTokens(model, name string, p, c int) error {
	s.defaultStream(name, p, c)
	return s.streamServedBy(model, name)
}

func (s *sr3State) streamWithOptions(model, opts, name string) error {
	if opts != "absent" {
		var v any
		if err := json.Unmarshal([]byte(opts), &v); err != nil {
			return fmt.Errorf("stream_options %q is not JSON: %v", opts, err)
		}
		s.extraBody = map[string]any{"stream_options": v}
	}
	return s.streamServedBy(model, name)
}

func (s *sr3State) streamSettlesAt(model, cost string) error {
	// $0.000420 at $0.20/$0.60 per 1M = 600 prompt + 500 completion tokens.
	want := sr3f(cost)
	p, c := 600, 500
	if math.Abs((float64(p)*0.20+float64(c)*0.60)/1e6-want) > 1e-12 {
		return fmt.Errorf("fixture arithmetic: %d/%d tokens do not cost %s", p, c, cost)
	}
	s.promptOfBytes(4000)
	s.defaultStream("n-1", p, c)
	return s.streamServed(model)
}

// streamAtTPS: the script spaces its frames so the stream takes about a second for 42
// completion tokens; the measured figure is whatever the broker timed (tps = billed out /
// elapsed), compared within 15% (the dispatch overhead is not zero).
func (s *sr3State) streamAtTPS(model, name, tps string) error {
	want := sr3f(tps)
	c := int(want)
	s.sse(name, []sr3Frame{{line: sr3Content(1, name)}, {line: sr3Content(2, name), sleep: 500 * time.Millisecond}, {line: sr3Usage(40, c), sleep: 500 * time.Millisecond}, {line: sr3Done}})
	s.b.metricsMu.Lock()
	if v, ok := s.b.tps[s.st(name).id]; ok {
		s.tpsBefore[name] = v
	}
	s.b.metricsMu.Unlock()
	return s.streamServedBy(model, name)
}

func (s *sr3State) nonStreamServed(model string) error {
	s.model = model
	if err := s.send(false); err != nil {
		return err
	}
	s.nsCode, s.nsHdr, s.nsBody = s.lastCode, s.lastHdr.Clone(), append([]byte(nil), s.lastBody...)
	if s.nsCode != 200 {
		return fmt.Errorf("non-stream relay = %d: %.300s", s.nsCode, s.nsBody)
	}
	return nil
}

func (s *sr3State) streamFallback(model, list, name string) error {
	var models []string
	if err := json.Unmarshal([]byte(list), &models); err != nil {
		return err
	}
	s.extraBody = map[string]any{"models": models}
	return s.streamServedBy(model, name)
}

func (s *sr3State) streamBridged(model string) error {
	if err := s.streamServed(model); err != nil {
		return err
	}
	if s.lastHdr.Get("X-RogerAI-Relay") == "" {
		return fmt.Errorf("the stream was not served through the bridge (no X-RogerAI-Relay): provider=%q", s.lastHdr.Get("X-RogerAI-Provider"))
	}
	return nil
}

func (s *sr3State) streamEnds() error { return s.streamServed(s.model) }

func (s *sr3State) plainStream() error { return s.streamServed(s.model) }

// --- Givens: station behaviour ------------------------------------------------------

func (s *sr3State) threeFramesDoneReceipt(name string) error {
	s.sse(name, []sr3Frame{
		{line: sr3Content(1, name)},
		{line: sr3Content(2, name), sleep: 150 * time.Millisecond},
		{line: sr3Content(3, name), sleep: 150 * time.Millisecond},
		{line: sr3Done},
	})
	return nil
}

func (s *sr3State) finalUsageFrame(name, usage string) error {
	var u struct {
		P int `json:"prompt_tokens"`
		C int `json:"completion_tokens"`
	}
	if err := json.Unmarshal([]byte(usage), &u); err != nil {
		return err
	}
	s.stationUsage = fmt.Sprintf(`data: {"choices":[],"usage":%s}`, usage)
	s.sse(name, []sr3Frame{{line: sr3Content(1, name)}, {line: s.stationUsage}, {line: sr3Done}})
	return nil
}

func (s *sr3State) keepaliveComments(name string) error {
	s.keepalives = 3
	s.sse(name, []sr3Frame{
		{line: ": keepalive"}, {line: sr3Content(1, name)}, {line: ": keepalive"},
		{line: sr3Content(2, name)}, {line: ": keepalive"}, {line: sr3Usage(40, 20)}, {line: sr3Done},
	})
	return nil
}

func (s *sr3State) recountEnabled() error {
	if !s.b.recount.enabled() {
		return fmt.Errorf("the broker re-count is not enabled")
	}
	return nil
}

func (s *sr3State) claimsCompletionCountedAs(name string, claim, counted int) error {
	s.recCompletion = counted
	s.promptOfBytes(2000)
	s.defaultStream(name, 200, claim)
	return nil
}

// promptOfBytes makes the request's only message a plain text of about n bytes.
func (s *sr3State) promptOfBytes(n int) {
	s.promptText = strings.Repeat("lorem ", n/6)
	if s.extraBody == nil {
		s.extraBody = map[string]any{}
	}
	s.extraBody["messages"] = []map[string]any{{"role": "user", "content": s.promptText}}
}

func (s *sr3State) holdAtMost(amount string) error {
	if s.extraBody == nil {
		s.extraBody = map[string]any{}
	}
	s.extraBody["provider"] = map[string]any{"max_price": map[string]any{"request": sr3f(amount)}}
	return nil
}

func (s *sr3State) claimsTokensCosting(name, cost string) error {
	// $0.002000 at $0.20/$0.60 per 1M = 1000 prompt + 3000 completion tokens.
	p, c := 1000, 3000
	if math.Abs((float64(p)*0.20+float64(c)*0.60)/1e6-sr3f(cost)) > 1e-12 {
		return fmt.Errorf("fixture arithmetic: %d/%d tokens do not cost %s", p, c, cost)
	}
	msgs := s.extraBody
	s.promptOfBytes(6000)
	if prov, ok := msgs["provider"]; ok {
		s.extraBody["provider"] = prov
	}
	s.defaultStream(name, p, c)
	return nil
}

func (s *sr3State) billedAt(name, cost string) error {
	// $0.00000036 = 1 completion token at $0.36/1M (no input price, no prompt claim).
	st := s.st(name)
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.PriceIn, o.PriceOut = 0, sr3f(cost)*1e6 })
	st.priceIn, st.priceOut = 0, sr3f(cost)*1e6
	s.defaultStream(name, 0, 1)
	return nil
}

func (s *sr3State) promptClaim4K(name string, claim, counted int) error {
	s.recPrompt = counted
	s.promptOfBytes(4096)
	s.defaultStream(name, claim, 20)
	return nil
}

func (s *sr3State) firstQuoted(name, model, in, out string) error {
	st := s.st(name)
	if math.Abs(st.priceIn-sr3f(in)) > 1e-12 || math.Abs(st.priceOut-sr3f(out)) > 1e-12 {
		return fmt.Errorf("%s is on air at %g/%g, not %s/%s", name, st.priceIn, st.priceOut, in, out)
	}
	s.model = model
	s.scripted[name] = true
	st.set(func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"%s quoted"}}],"usage":{"prompt_tokens":40,"completion_tokens":20}}`, sr3Marker)
	})
	if err := s.send(false); err != nil {
		return err
	}
	if s.lastCode != 200 {
		return fmt.Errorf("the quoting relay = %d: %.300s", s.lastCode, s.lastBody)
	}
	s.quoteHdr = s.lastHdr.Clone()
	s.scripted[name] = false // the stream that follows gets the default script
	return nil
}

func (s *sr3State) insideFreeWindow(name string) error {
	now := time.Now().UTC()
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	win := protocol.PriceWindow{Start: from.Format("15:04"), End: to.Format("15:04"), Free: true}
	s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) { o.Schedule = []protocol.PriceWindow{win} })
	return nil
}

// balanceBefore gives the caller a fresh signed, logged-in wallet holding exactly this much.
func (s *sr3State) balanceBefore(amount string) error {
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<30))
	id := gid.Int64() + 1_000_000
	if err := s.db.BindOwner(store.Owner{GitHubID: id, Login: "sr3-bal-" + s.nonce, Pubkey: pub}); err != nil {
		return err
	}
	s.consumerPriv, s.wallet = priv, "u_gh_"+strconv.FormatInt(id, 10)
	if _, err := s.db.AddCredits(s.wallet, sr3f(amount)); err != nil {
		return err
	}
	bal, err := s.db.PeekBalance(s.wallet)
	if err != nil {
		return err
	}
	if math.Abs(bal-sr3f(amount)) > 1e-9 {
		return fmt.Errorf("the fresh wallet holds %g, want %s", bal, amount)
	}
	s.funded, s.foState.start = true, bal
	return nil
}

func (s *sr3State) identicalBothWays(name string) error {
	st := s.st(name)
	s.scripted[name] = true
	s.promptOfBytes(2000)
	text := sr3Marker + " the same answer either way"
	st.set(func(_ int, w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text)
			fmt.Fprint(w, sr3Usage(300, 120)+"\n\n")
			fmt.Fprint(w, sr3Done+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":300,"completion_tokens":120}}`, text)
	})
	return nil
}

// monthlyCapSpent sets the cap and books a real $4.00 spend this month through the store's
// own hold + finalize (the path every settled relay takes).
func (s *sr3State) monthlyCapSpent(capv, spent string) error {
	if err := s.ensureFunded(); err != nil {
		return err
	}
	if err := s.db.SetMonthlyCap(s.wallet, sr3f(capv)); err != nil {
		return err
	}
	amt := sr3f(spent)
	id := "sr3prior" + s.nonce
	ok, err := s.db.HoldFor(s.wallet, id, amt)
	if err != nil || !ok {
		return fmt.Errorf("prior hold: ok=%v err=%v", ok, err)
	}
	rec := protocol.UsageReceipt{RequestID: id, NodeID: s.st("n-1").id, User: s.wallet, Model: s.model, TS: time.Now().Unix()}
	if _, err := s.db.Finalize(s.wallet, s.st("n-1").id, amt, amt, amt*0.7, rec); err != nil {
		return fmt.Errorf("prior spend: %v", err)
	}
	return nil
}

func (s *sr3State) answers429BeforeFrame(name string) error {
	s.scripted[name] = true
	s.st(name).script429("")
	return nil
}

func (s *sr3State) onAirAndServes(name, model string) error {
	ref := s.st("n-1")
	s.model = model
	s.station(name, ref.priceIn, ref.priceOut)
	return nil
}

func (s *sr3State) noStationFor(model string) error {
	for _, st := range s.stations {
		if st.model == model {
			return fmt.Errorf("%s is on air for %s", st.name, model)
		}
	}
	return nil
}

func (s *sr3State) contentErrorReceipt(name string, claim int) error {
	s.sse(name, []sr3Frame{
		{line: sr3Content(1, name)}, {line: sr3Content(2, name)},
		{line: `data: {"error":{"message":"upstream exploded mid-stream"}}`},
		{line: sr3Usage(40, claim)},
	})
	return nil
}

func (s *sr3State) silentAfterOne(name string) error {
	s.idle = 400 * time.Millisecond
	s.b.streamIdleTimeout = s.idle
	s.sse(name, []sr3Frame{{line: sr3Content(1, name)}, {line: sr3Usage(40, 20), sleep: 3 * s.idle}, {line: sr3Done}})
	return nil
}

func (s *sr3State) emptyFinal(name string) error {
	if len(s.stations) != 1 {
		return fmt.Errorf("%d stations on air, want %s alone", len(s.stations), name)
	}
	s.sse(name, []sr3Frame{{line: sr3Usage(0, 0)}, {line: sr3Done}})
	return nil
}

func (s *sr3State) emptyBeforeFrame(name string) error {
	s.sse(name, nil) // 200, an event-stream with nothing in it
	return nil
}

func (s *sr3State) disconnectsAfterFirst() error {
	s.ctxCancel = true
	return nil
}

func (s *sr3State) finishesAndSendsReceipt(name string) error {
	s.sse(name, []sr3Frame{
		{line: sr3Content(1, name)}, {line: sr3Content(2, name), sleep: 300 * time.Millisecond},
		{line: sr3Usage(40, 20), sleep: 100 * time.Millisecond}, {line: sr3Done},
	})
	return s.send(true)
}

func (s *sr3State) ledgerRefusesSettle() error {
	s.wrapped = &mf1Store{Store: s.b.db, refuseSettleOn: s.st("n-1").id}
	s.b.db = s.wrapped
	return nil
}

func (s *sr3State) reasoningThenContent(name string, c int) error {
	s.sse(name, []sr3Frame{
		{line: `data: {"choices":[{"delta":{"reasoning_content":"thinking it through"}}]}`},
		{line: `data: {"choices":[{"delta":{"reasoning_content":"still thinking"}}]}`},
		{line: sr3Content(1, name)},
		{line: sr3Usage(40, c)}, {line: sr3Done},
	})
	return nil
}

func (s *sr3State) reasoningOnlyLength(name string, c int) error {
	s.sse(name, []sr3Frame{
		{line: `data: {"choices":[{"delta":{"reasoning_content":"thinking ` + sr3Marker + ` at length"}}]}`},
		{line: `data: {"choices":[{"delta":{"reasoning_content":"and more"},"finish_reason":"length"}]}`},
		{line: sr3Usage(40, c)}, {line: sr3Done},
	})
	return nil
}

func (s *sr3State) noDirectFor(model string) error { return s.noDirectNode(model) }

func (s *sr3State) towerViaHub(name, model string) error { return s.towerServes(name, model) }

// towerForgesRogerai stands up a live sealed fabric (rpState.standUpTower's recipe) whose model
// answers with a usage object carrying a forged rogerai block, in both shapes.
func (s *sr3State) towerForgesRogerai(name string) error {
	model := "gemma-3-27b"
	s.model = model
	s.forged = "FORGED-SR3-" + s.nonce
	forged := fmt.Sprintf(`{"receipt":"%s","node":"evil","cost":0}`, s.forged)
	return s.customTower(name, model, func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := io.ReadAll(r.Body)
		if bytes.Contains(reqBody, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tower says hi\"}}]}\n\n")
			fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5,\"rogerai\":%s}}\n\n", forged)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"tower says hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":5,"rogerai":%s}}`, forged)
	})
}

// customTower is rpState.standUpTower with the model's handler supplied by the scenario.
func (s *sr3State) customTower(name, model string, handler http.HandlerFunc) error {
	if err := s.ensureTower(); err != nil {
		return err
	}
	t, b, srv := s.t, s.b, s.towerSrv
	tw := &rpTower{name: name}
	upstream := httptest.NewServer(handler)
	tw.closers = append(tw.closers, upstream.Close)
	hubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	tw.closers = append(tw.closers, func() { _ = hubLn.Close() })
	lt := liveEdgeTower(t, b, srv, name+"-op-"+s.nonce, hubLn.Addr().String())
	tw.id = lt.id
	hub := towerhub.New()
	hubServer := towerhub.NewServer(hub, func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, lt.id, time.Now())
		return att, station, gerr
	}, towerhub.ServerOptions{TowerID: lt.id, EpochKey: lt.priv, SubmitTTL: 10 * time.Second, PollTTL: 500 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hubServer.Submit)
	mux.HandleFunc(towerhub.PathPoll, hubServer.Poll)
	mux.HandleFunc(towerhub.PathComplete, hubServer.Complete)
	go func() { _ = http.Serve(hubLn, mux) }()
	nodeOp := signedInOperator(t, b, name+"-node-"+s.nonce)
	shareNodeID := registerShareNode(t, b, nodeOp)
	ctx, cancel := context.WithCancel(context.Background())
	tw.closers = append(tw.closers, cancel)
	go func() {
		_ = agent.ServeTower(ctx, agent.Config{
			NodeID: shareNodeID, Broker: srv.URL, Model: model, Modality: "chat",
			PriceIn: 0, PriceOut: 0, Upstream: upstream.URL, Parallel: 1,
		}, nodeOp.priv, t.TempDir(), io.Discard, nil)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ats, aerr := b.tower.stations.ByTower(lt.id)
		if aerr == nil && len(ats) > 0 {
			hubServer.RegisterNode(ats[0].StationID, hubAuthOf(t, ats[0]))
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tower %s: the share node never attached", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
	rows, err := b.tower.routable.ByTower(lt.id, time.Now())
	if err != nil || len(rows) == 0 {
		return fmt.Errorf("tower %s: no routable row (%v)", name, err)
	}
	tw.nodeID = rows[0].NodeID
	s.towers[name] = tw
	return nil
}

func (s *sr3State) oldClient() error { return nil } // the Thens read the stream as such a client would

func (s *sr3State) usageClaimsFor(name string, claim, real int) error {
	s.recCompletion = real
	s.promptOfBytes(2000)
	s.defaultStream(name, 200, claim)
	return nil
}

// receiptClaims: the harness station signs its receipt with the claim it read off its own
// usage frame, so the preceding Given (the usage frame) already set it; this pins that.
func (s *sr3State) receiptClaims(name string, claim int) error {
	if !s.scripted[name] {
		return fmt.Errorf("%s has no scripted usage frame to claim %d from", name, claim)
	}
	return nil
}

func (s *sr3State) forgedRogeraiFrame(name string) error {
	s.forged = "FORGED-SR3-" + s.nonce
	s.sse(name, []sr3Frame{
		{line: sr3Content(1, name)},
		{line: fmt.Sprintf(`data: {"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":20,"rogerai":{"receipt":"%s","node":"%s","cost":0}}}`, s.forged, s.st(name).id)},
		{line: sr3Done},
	})
	return nil
}

// customLoop replaces the broker-side tunnel of station name with one this runner drains: the
// harness station goroutine keeps its own (now unused) channel, so the two never race.
func (s *sr3State) customLoop(name string, mode string) {
	st := s.st(name)
	s.scripted[name] = true
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: st.tun.token}
	s.b.mu.Lock()
	s.b.tunnels[st.id] = tun
	s.b.mu.Unlock()
	stop := s.stopCustom
	s.customWG.Add(1)
	go func() {
		defer s.customWG.Done()
		for {
			select {
			case <-stop:
				return
			case job := <-tun.jobs:
				var lines []string
				var pt, ct int
				switch mode {
				case "noReceipt":
					lines = []string{sr3Content(1, name), sr3Done}
				default:
					lines = []string{sr3Content(1, name), sr3Usage(40, 20), sr3Done}
					pt, ct = 40, 20
				}
				var buf bytes.Buffer
				for _, l := range lines {
					buf.WriteString(l + "\n\n")
				}
				sreq := httptest.NewRequest(http.MethodPost, "/agent/stream?node="+st.id+"&job="+job.ID, &buf)
				sreq.Header.Set("Authorization", "Bearer "+tun.token)
				s.b.agentStream(httptest.NewRecorder(), sreq)
				if mode == "noReceipt" {
					continue
				}
				rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: st.id, User: job.User, Model: st.model,
					PromptTokens: pt, CompletionTokens: ct, PriceIn: st.priceIn, PriceOut: st.priceOut, TS: time.Now().Unix(),
					LineageMethod: "p0-upstream-usage"}
				st.mu.Lock()
				rec.PrevHash = st.lastHash
				rec.SignNode(st.priv)
				st.lastHash = rec.Hash()
				st.mu.Unlock()
				wire, _ := json.Marshal(protocol.JobResult{ID: job.ID, Status: 200, Receipt: rec})
				var posted map[string]any
				_ = json.Unmarshal(wire, &posted)
				st.mu.Lock()
				st.results = append(st.results, posted)
				st.mu.Unlock()
				for i := 0; i < 2; i++ { // two receipts for the same job
					r := httptest.NewRequest(http.MethodPost, "/agent/result?node="+st.id, bytes.NewReader(wire))
					r.Header.Set("Authorization", "Bearer "+tun.token)
					s.b.agentResult(httptest.NewRecorder(), r)
				}
			}
		}
	}()
}

func (s *sr3State) doneNoReceipt(name string) error {
	s.idle = 400 * time.Millisecond
	s.b.streamIdleTimeout = s.idle
	s.customLoop(name, "noReceipt")
	return nil
}

func (s *sr3State) twoReceipts(name string) error {
	s.customLoop(name, "dup")
	return nil
}

func (s *sr3State) onPrivateBand() error {
	st := s.st("n-1")
	s.b.private[st.id] = true
	code := "147.520 MHz · SRBC-" + strings.ToUpper(s.nonce[:4])
	s.freq = code
	if s.extraBody == nil {
		s.extraBody = map[string]any{}
	}
	s.extraBody["roger"] = map[string]any{"freq": code}
	s.promptOfBytes(600)
	return s.db.CreateBand(store.Band{ID: "band_" + s.nonce, CodeHash: protocol.BandCodeHash(code), CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: st.acct, NodeID: st.id, CreatedAt: time.Now().Unix()})
}

func (s *sr3State) streamAndNonStreamSame() error {
	if err := s.identicalBothWays("n-1"); err != nil {
		return err
	}
	if err := s.nonStreamServed(s.model); err != nil {
		return err
	}
	return s.streamServedBy(s.model, "n-1")
}

// --- reading the stream ------------------------------------------------------------

// sr3Events splits the body into its SSE events (blank-line separated, trailing empties dropped).
func sr3Events(body []byte) []string {
	var out []string
	for _, e := range strings.Split(string(body), "\n\n") {
		// a blank line between events is a separator, not part of the next event
		if e = strings.TrimLeft(e, "\n"); strings.TrimSpace(e) != "" {
			out = append(out, e)
		}
	}
	return out
}

// dataOf is an event's data payload ("" for a comment-only event).
func dataOf(ev string) string {
	var parts []string
	for _, l := range strings.Split(ev, "\n") {
		if strings.HasPrefix(l, "data:") {
			parts = append(parts, strings.TrimSpace(strings.TrimPrefix(l, "data:")))
		}
	}
	return strings.Join(parts, "\n")
}

func hasRogerai(payload string) bool {
	var m map[string]any
	if json.Unmarshal([]byte(payload), &m) != nil {
		return false
	}
	u, _ := m["usage"].(map[string]any)
	_, ok := u["rogerai"]
	return ok
}

// chunkIndex is the event index of the LAST event carrying usage.rogerai (-1 = none).
func (s *sr3State) chunkIndex() int {
	evs := sr3Events(s.lastBody)
	for i := len(evs) - 1; i >= 0; i-- {
		if hasRogerai(dataOf(evs[i])) {
			return i
		}
	}
	return -1
}

func (s *sr3State) chunk() (map[string]any, error) {
	i := s.chunkIndex()
	if i < 0 {
		return nil, fmt.Errorf("no event in the stream carries a usage.rogerai object: %.600s", s.lastBody)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(dataOf(sr3Events(s.lastBody)[i])), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *sr3State) doneIndices() []int {
	var out []int
	for i, ev := range sr3Events(s.lastBody) {
		if dataOf(ev) == "[DONE]" {
			out = append(out, i)
		}
	}
	return out
}

func sr3Path(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func (s *sr3State) usageVal(path string) (any, map[string]any, error) {
	ch, err := s.chunk()
	if err != nil {
		return nil, nil, err
	}
	v, ok := sr3Path(ch, "usage."+path)
	if !ok {
		return nil, ch, fmt.Errorf("the usage chunk has no usage.%s: %v", path, ch)
	}
	return v, ch, nil
}

func (s *sr3State) usageNum(path string) (float64, error) {
	v, ch, err := s.usageVal(path)
	if err != nil {
		return 0, err
	}
	f, ok := v.(float64)
	if !ok {
		return 0, fmt.Errorf("usage.%s is %T %v, not a number: %v", path, v, v, ch)
	}
	return f, nil
}

func (s *sr3State) chunkReceipt(ch map[string]any) (protocol.UsageReceipt, error) {
	v, _ := sr3Path(ch, "usage.rogerai.receipt")
	enc, _ := v.(string)
	if enc == "" {
		return protocol.UsageReceipt{}, fmt.Errorf("usage.rogerai.receipt is absent: %v", ch)
	}
	return protocol.DecodeReceipt(enc)
}

func (s *sr3State) receiptOfChunk() (protocol.UsageReceipt, error) {
	ch, err := s.chunk()
	if err != nil {
		return protocol.UsageReceipt{}, err
	}
	return s.chunkReceipt(ch)
}

// idOf maps a scenario name (station or Tower) to the id the broker uses; anything else as is.
func (s *sr3State) nameID(v string) string {
	if st, ok := s.stations[v]; ok {
		return st.id
	}
	if tw, ok := s.towers[v]; ok {
		return tw.id
	}
	return v
}

// debit is the consumer's summed ledger cost for the request and its attempts.
func (s *sr3State) debit() (float64, int, error) {
	if s.reqID == "" {
		return 0, 0, fmt.Errorf("no request id was resolved for the last relay")
	}
	es, err := s.db.RecentByUser(s.wallet, 2000)
	if err != nil {
		return 0, 0, err
	}
	total, n := 0.0, 0
	for _, e := range es {
		if e.RequestID == s.reqID || strings.HasPrefix(e.RequestID, s.reqID+"-") {
			total += e.Cost
			if e.Cost > 0 {
				n++
			}
		}
	}
	return total, n, nil
}

func (s *sr3State) brokerPub() string {
	return hex.EncodeToString(s.b.priv.Public().(ed25519.PublicKey))
}

func sr3f(v string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return f
}

func approx(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }

// --- Then: shape and placement --------------------------------------------------------

func (s *sr3State) lastBeforeDoneIsChunk() error {
	evs := sr3Events(s.lastBody)
	d := s.doneIndices()
	if len(d) == 0 || d[len(d)-1] == 0 {
		return fmt.Errorf("no [DONE] (or nothing before it): %.600s", s.lastBody)
	}
	last := d[len(d)-1]
	prev := -1
	for i := last - 1; i >= 0; i-- {
		if dataOf(evs[i]) != "" {
			prev = i
			break
		}
	}
	if prev < 0 {
		return fmt.Errorf("no data frame before [DONE]")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(dataOf(evs[prev])), &m); err != nil {
		return fmt.Errorf("the frame before [DONE] is not an object: %q", dataOf(evs[prev]))
	}
	if c, ok := m["choices"].([]any); !ok || len(c) != 0 {
		return fmt.Errorf("the frame before [DONE] has choices %v, want []", m["choices"])
	}
	if !hasRogerai(dataOf(evs[prev])) {
		return fmt.Errorf("the frame before [DONE] is not the broker's (no usage.rogerai): %v", m)
	}
	return nil
}

func (s *sr3State) itsKeyIs(key, want string) error {
	ch, err := s.chunk()
	if err != nil {
		return err
	}
	got, _ := ch[key].(string)
	if got != want {
		return fmt.Errorf("the chunk's %q is %v, want %q", key, ch[key], want)
	}
	return nil
}

func (s *sr3State) itsIDIsRequestID() error {
	if s.reqID == "" {
		return fmt.Errorf("no request id was dispatched")
	}
	return s.itsKeyIs("id", s.reqID)
}

func (s *sr3State) doneIsFinal() error {
	evs := sr3Events(s.lastBody)
	lastData := -1
	for i, ev := range evs {
		if dataOf(ev) != "" {
			lastData = i
		}
	}
	if lastData < 0 || dataOf(evs[lastData]) != "[DONE]" {
		return fmt.Errorf("the last data frame is not [DONE]: %.400s", s.lastBody)
	}
	if n := len(s.doneIndices()); n != 1 {
		return fmt.Errorf("%d [DONE] frames, want exactly one", n)
	}
	return nil
}

func (s *sr3State) usageNumIs(path, want string) error {
	f, err := s.usageNum(path)
	if err != nil {
		return err
	}
	w := sr3f(want)
	if path == "rogerai.tps" {
		// measured on the wall clock: compared within 15% (see streamAtTPS)
		if math.Abs(f-w) > 0.15*w {
			return fmt.Errorf("usage.rogerai.tps = %g, want ~%g", f, w)
		}
		return nil
	}
	if !approx(f, w) {
		return fmt.Errorf("usage.%s = %v, want %s", path, f, want)
	}
	return nil
}

func (s *sr3State) usageStrIs(path, want string) error {
	v, ch, err := s.usageVal(path)
	if err != nil {
		return err
	}
	got, _ := v.(string)
	if got != s.nameID(want) {
		return fmt.Errorf("usage.%s = %v, want %q (%s): %v", path, v, want, s.nameID(want), ch)
	}
	return nil
}

func (s *sr3State) costIsSettled() error {
	f, err := s.usageNum("cost")
	if err != nil {
		return err
	}
	d, _, err := s.debit()
	if err != nil {
		return err
	}
	if !approx(f, d) {
		return fmt.Errorf("usage.cost = %v, the settled ledger debit is %v", f, d)
	}
	return nil
}

func splitKeys(list string) []string {
	list = strings.ReplaceAll(list, " and ", ", ")
	list = strings.ReplaceAll(list, " or ", ", ")
	var out []string
	for _, k := range strings.Split(list, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func (s *sr3State) rogeraiHasKeys(list string) error {
	v, ch, err := s.usageVal("rogerai")
	if err != nil {
		return err
	}
	m, _ := v.(map[string]any)
	for _, k := range splitKeys(list) {
		if _, ok := m[k]; !ok {
			return fmt.Errorf("usage.rogerai has no key %q: %v", k, ch)
		}
	}
	return nil
}

func (s *sr3State) rogeraiNoKey(k string) error {
	v, _, err := s.usageVal("rogerai")
	if err != nil {
		return err
	}
	if _, ok := v.(map[string]any)[k]; ok {
		return fmt.Errorf("usage.rogerai carries %q = %v", k, v.(map[string]any)[k])
	}
	return nil
}

func (s *sr3State) rogeraiNoneOf(list string) error {
	for _, k := range splitKeys(list) {
		if err := s.rogeraiNoKey(k); err != nil {
			return err
		}
	}
	return nil
}

func (s *sr3State) rogeraiFrames() int {
	n := 0
	for _, ev := range sr3Events(s.lastBody) {
		if hasRogerai(dataOf(ev)) {
			n++
		}
	}
	return n
}

func (s *sr3State) exactlyOneRogerai() error {
	if n := s.rogeraiFrames(); n != 1 {
		return fmt.Errorf("%d frames carry usage.rogerai, want exactly one", n)
	}
	return nil
}

func (s *sr3State) chunkFramedAlone() error {
	i := s.chunkIndex()
	if i < 0 {
		return fmt.Errorf("no usage chunk in the stream")
	}
	ev := sr3Events(s.lastBody)[i]
	if !strings.HasPrefix(ev, "data: ") || strings.Contains(ev, "\n") {
		return fmt.Errorf("the usage chunk is not one `data: <json>` line: %q", ev)
	}
	if !bytes.Contains(s.lastBody, []byte(ev+"\n\n")) {
		return fmt.Errorf("the usage chunk is not terminated by a blank line")
	}
	return nil
}

func (s *sr3State) notMerged() error {
	i := s.chunkIndex()
	if i < 0 {
		return fmt.Errorf("no usage chunk in the stream")
	}
	ev := sr3Events(s.lastBody)[i]
	if strings.Count(ev, "data:") != 1 || strings.Contains(ev, sr3Marker) {
		return fmt.Errorf("the usage chunk shares its event with a station frame: %q", ev)
	}
	return nil
}

func (s *sr3State) endsWithChunkBeforeDone() error { return s.lastBeforeDoneIsChunk() }

func (s *sr3State) contentWrites() []time.Time {
	var out []time.Time
	if s.w == nil {
		return nil
	}
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	for _, wr := range s.w.writes {
		if bytes.Contains(wr.b, []byte(sr3Marker)) {
			out = append(out, wr.at)
		}
	}
	return out
}

func (s *sr3State) threeAsTheyArrive() error {
	ts := s.contentWrites()
	if len(ts) != 3 {
		return fmt.Errorf("%d content writes reached the consumer, want 3: %.400s", len(ts), s.lastBody)
	}
	for i := 1; i < 3; i++ {
		if gap := ts[i].Sub(ts[i-1]); gap < 100*time.Millisecond {
			return fmt.Errorf("content frames %d and %d reached the consumer %v apart (the station spaced them 150 ms): buffered, not forwarded as they arrived", i, i+1, gap)
		}
	}
	ci := s.chunkIndex()
	for i, ev := range sr3Events(s.lastBody) {
		if strings.Contains(ev, sr3Marker) && ci >= 0 && i > ci {
			return fmt.Errorf("a content frame follows the usage chunk")
		}
	}
	return nil
}

func (s *sr3State) noDoneBeforeChunk() error {
	ci := s.chunkIndex()
	if ci < 0 {
		return fmt.Errorf("no usage chunk in the stream: %.400s", s.lastBody)
	}
	for _, d := range s.doneIndices() {
		if d < ci {
			return fmt.Errorf("[DONE] (event %d) reached the consumer before the usage chunk (event %d)", d, ci)
		}
	}
	return nil
}

func (s *sr3State) doneAfterChunk() error {
	ci := s.chunkIndex()
	d := s.doneIndices()
	if ci < 0 || len(d) != 1 || d[0] < ci {
		return fmt.Errorf("want exactly one [DONE] after the usage chunk; chunk at %d, [DONE] at %v", ci, d)
	}
	return nil
}

func (s *sr3State) stationUsageForwarded() error {
	if s.stationUsage == "" || !bytes.Contains(s.lastBody, []byte(s.stationUsage+"\n\n")) {
		return fmt.Errorf("the station's usage frame %q was not forwarded unchanged: %.500s", s.stationUsage, s.lastBody)
	}
	return nil
}

func (s *sr3State) brokerChunkFollowsStation() error {
	ci := s.chunkIndex()
	si := -1
	for i, ev := range sr3Events(s.lastBody) {
		if ev == s.stationUsage {
			si = i
		}
	}
	if si < 0 || ci <= si {
		return fmt.Errorf("the broker's chunk (event %d) does not follow the station's usage frame (event %d)", ci, si)
	}
	return nil
}

func (s *sr3State) commentIndex() (int, string) {
	for i, ev := range sr3Events(s.lastBody) {
		if strings.HasPrefix(ev, ": rogerai-cost=") {
			return i, strings.TrimPrefix(strings.TrimSpace(ev), ": rogerai-cost=")
		}
	}
	return -1, ""
}

func (s *sr3State) commentFollowsChunk() error {
	ci := s.chunkIndex()
	i, _ := s.commentIndex()
	if ci < 0 || i <= ci {
		return fmt.Errorf("the `: rogerai-cost=` comment (event %d) does not follow the usage chunk (event %d): %.400s", i, ci, s.lastBody)
	}
	return nil
}

func (s *sr3State) commentEqualsCost() error {
	_, x := s.commentIndex()
	f, err := s.usageNum("cost")
	if err != nil {
		return err
	}
	if x != fmtCostHeader(f) {
		return fmt.Errorf("comment %q, usage.cost %v formats as %q", x, f, fmtCostHeader(f))
	}
	return nil
}

func (s *sr3State) commentDeprecatedInOpenAPI() error {
	b, err := os.ReadFile("openapi.yaml")
	if err != nil {
		return err
	}
	txt := string(b)
	i := strings.Index(txt, "rogerai-cost=")
	for i >= 0 {
		win := txt[max(0, i-400):min(len(txt), i+400)]
		if strings.Contains(strings.ToLower(win), "deprecated") {
			return nil
		}
		j := strings.Index(txt[i+1:], "rogerai-cost=")
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return fmt.Errorf("openapi.yaml does not mark the `: rogerai-cost=` comment deprecated")
}

func (s *sr3State) stationCommentsForwarded() error {
	evs := sr3Events(s.lastBody)
	n := 0
	for _, ev := range evs {
		if strings.TrimSpace(ev) == ": keepalive" {
			n++
		}
	}
	if n != s.keepalives {
		return fmt.Errorf("%d of the station's %d keepalive comments reached the consumer", n, s.keepalives)
	}
	return nil
}

func (s *sr3State) chunkLastBeforeDone() error { return s.lastBeforeDoneIsChunk() }

// --- Then: receipts ---------------------------------------------------------------------

func (s *sr3State) receiptDecodes() error {
	_, err := s.receiptOfChunk()
	return err
}

func (s *sr3State) receiptNames(name, model string) error {
	rec, err := s.receiptOfChunk()
	if err != nil {
		return err
	}
	if sr3AttemptSuffix.ReplaceAllString(rec.RequestID, "") != s.reqID || rec.NodeID != s.nameID(name) || rec.Model != model {
		return fmt.Errorf("receipt names request %q node %q model %q, want %q %q %q", rec.RequestID, rec.NodeID, rec.Model, s.reqID, s.nameID(name), model)
	}
	return nil
}

func (s *sr3State) receiptSigVersion(v int) error {
	rec, err := s.receiptOfChunk()
	if err != nil {
		return err
	}
	if rec.SigVersion != v {
		return fmt.Errorf("receipt SigVersion %d, want %d", rec.SigVersion, v)
	}
	return nil
}

func (s *sr3State) receiptVerifyBroker() error {
	rec, err := s.receiptOfChunk()
	if err != nil {
		return err
	}
	if !rec.VerifyBroker(s.brokerPub()) {
		return fmt.Errorf("VerifyBroker fails on the chunk's receipt")
	}
	return nil
}

func (s *sr3State) coverageCovered() error {
	rec, err := s.receiptOfChunk()
	if err != nil {
		return err
	}
	ok, covers := rec.VerifyBrokerCoverage(s.brokerPub())
	if !ok || !covers {
		return fmt.Errorf("broker coverage ok=%v covers=%v, want both", ok, covers)
	}
	return nil
}

func (s *sr3State) nodeSigVerifies(name string) error {
	rec, err := s.receiptOfChunk()
	if err != nil {
		return err
	}
	if !rec.VerifyNode(s.st(name).pubHex) {
		return fmt.Errorf("the receipt's node signature does not verify against %s's key", name)
	}
	return nil
}

func (s *sr3State) receiptBrokerCompletion(n int) error {
	rec, err := s.receiptOfChunk()
	if err != nil {
		return err
	}
	if rec.BrokerCompletionTokens != n {
		return fmt.Errorf("BrokerCompletionTokens %d, want %d", rec.BrokerCompletionTokens, n)
	}
	return nil
}

func (s *sr3State) costFormula(pin string, c int, pout string) error {
	p, err := s.usageNum("prompt_tokens")
	if err != nil {
		return err
	}
	want := (p*sr3f(pin) + float64(c)*sr3f(pout)) / 1e6
	return s.usageNumIs("cost", strconv.FormatFloat(want, 'g', -1, 64))
}

func (s *sr3State) debitEqualsCost() error { return s.costIsSettled() }

func (s *sr3State) ledgerDebitIs(amount string) error {
	d, _, err := s.debit()
	if err != nil {
		return err
	}
	if !approx(d, sr3f(amount)) {
		return fmt.Errorf("ledger debit %v, want %s", d, amount)
	}
	return nil
}

func (s *sr3State) commentReads(x string) error {
	_, got := s.commentIndex()
	if got != x {
		return fmt.Errorf("the comment reads %q, want %q", got, x)
	}
	return nil
}

func (s *sr3State) promptClampedNot(want, not int) error {
	f, err := s.usageNum("prompt_tokens")
	if err != nil {
		return err
	}
	if int(f) != want {
		return fmt.Errorf("usage.prompt_tokens = %v, want %d (not the claimed %d)", f, want, not)
	}
	return nil
}

func (s *sr3State) lockedUntilIsLock() error {
	if s.quoteHdr == nil {
		return fmt.Errorf("no first quote was taken")
	}
	price := s.quoteHdr.Get("X-RogerAI-Price")
	var lock int64
	for _, part := range strings.Split(price, ";") {
		if strings.HasPrefix(part, "locked_until=") {
			v := strings.TrimPrefix(part, "locked_until=")
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				lock = t.Unix()
			} else {
				lock, _ = strconv.ParseInt(v, 10, 64)
			}
		}
	}
	if lock == 0 {
		return fmt.Errorf("the first quote's X-RogerAI-Price %q names no lock (headers %v)", price, s.quoteHdr)
	}
	return s.usageNumIs("rogerai.locked_until", strconv.FormatInt(lock, 10))
}

func (s *sr3State) storedTPSUpdated(_ string) error {
	f, err := s.usageNum("rogerai.tps")
	if err != nil {
		return err
	}
	s.b.metricsMu.Lock()
	got, ok := s.b.tps[s.st("n-1").id]
	s.b.metricsMu.Unlock()
	if !ok {
		return fmt.Errorf("no stored tps estimate for n-1")
	}
	want := f
	if prev, had := s.tpsBefore["n-1"]; had {
		want = 0.3*f + 0.7*prev
	}
	if math.Abs(got-want) > 1e-6 {
		return fmt.Errorf("stored tps %v, want %v (from the chunk's %v)", got, want, f)
	}
	return nil
}

func (s *sr3State) eqHeader(path, header string, intHdr bool) error {
	f, err := s.usageNum(path)
	if err != nil {
		return err
	}
	h := s.nsHdr.Get(header)
	if h == "" {
		return fmt.Errorf("the non-stream relay carried no %s", header)
	}
	if !approx(f, sr3f(h)) {
		return fmt.Errorf("stream usage.%s = %v, non-stream %s = %s", path, f, header, h)
	}
	return nil
}

func (s *sr3State) eqTokensIn() error {
	return s.eqHeader("prompt_tokens", "X-RogerAI-Tokens-In", true)
}
func (s *sr3State) eqTokensOut() error {
	return s.eqHeader("completion_tokens", "X-RogerAI-Tokens-Out", true)
}
func (s *sr3State) eqCost() error { return s.eqHeader("cost", "X-RogerAI-Cost", false) }

func priceParts(h string) (in, out float64) {
	for _, part := range strings.Split(h, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.TrimSpace(kv[0]) {
		case "in":
			in = sr3f(kv[1])
		case "out":
			out = sr3f(kv[1])
		}
	}
	return
}

func (s *sr3State) eqPrice() error {
	in, out := priceParts(s.nsHdr.Get("X-RogerAI-Price"))
	pin, err := s.usageNum("rogerai.price_in")
	if err != nil {
		return err
	}
	pout, err := s.usageNum("rogerai.price_out")
	if err != nil {
		return err
	}
	if !approx(pin, in) || !approx(pout, out) {
		return fmt.Errorf("stream prices %v/%v, X-RogerAI-Price %q", pin, pout, s.nsHdr.Get("X-RogerAI-Price"))
	}
	return nil
}

func (s *sr3State) eqProvider() error {
	return s.usageStrIs("rogerai.node", s.nsHdr.Get("X-RogerAI-Provider"))
}

// --- Then: headers ---------------------------------------------------------------------

func (s *sr3State) firstFrameHeaders() http.Header {
	if s.w == nil {
		return http.Header{}
	}
	return s.w.recWriter.Result().Header
}

func (s *sr3State) firstFrameHeaderIs(name, want string) error {
	got := s.firstFrameHeaders().Get(name)
	if got != s.nameID(want) {
		return fmt.Errorf("%s at the first frame = %q, want %q", name, got, s.nameID(want))
	}
	return nil
}

func (s *sr3State) firstFramePriceLocked() error {
	h := s.firstFrameHeaders().Get("X-RogerAI-Price")
	in, out := priceParts(h)
	st := s.st("n-1")
	if h == "" || !approx(in, st.priceIn) || !approx(out, st.priceOut) {
		return fmt.Errorf("X-RogerAI-Price at the first frame = %q, want in=%g out=%g", h, st.priceIn, st.priceOut)
	}
	return nil
}

func (s *sr3State) firstFrameHas(list string) error {
	h := s.firstFrameHeaders()
	for _, k := range splitKeys(list) {
		if h.Get(k) == "" {
			return fmt.Errorf("%s is not among the headers committed with the first frame: %v", k, h)
		}
	}
	return nil
}

func (s *sr3State) headersCarryNone(list string) error {
	h := s.firstFrameHeaders()
	live := s.lastHdr
	for _, k := range splitKeys(list) {
		if h.Get(k) != "" || live.Get(k) != "" {
			return fmt.Errorf("the stream response carries %s = %q", k, h.Get(k)+live.Get(k))
		}
	}
	return nil
}

func (s *sr3State) noTrailer() error {
	res := s.w.recWriter.Result()
	if len(res.Trailer) > 0 || res.Header.Get("Trailer") != "" {
		return fmt.Errorf("the stream declares trailers: %v %q", res.Trailer, res.Header.Get("Trailer"))
	}
	return nil
}

func (s *sr3State) sees200From(name string) error {
	if s.lastCode != 200 || s.lastHdr.Get("X-RogerAI-Provider") != s.nameID(name) {
		return fmt.Errorf("consumer saw %d from %q, want 200 from %s", s.lastCode, s.lastHdr.Get("X-RogerAI-Provider"), name)
	}
	return nil
}

// --- Then: failover, errors, disconnects ------------------------------------------------

func (s *sr3State) receiptNamesSecondAttempt(name string) error {
	rec, err := s.receiptOfChunk()
	if err != nil {
		return err
	}
	if rec.NodeID != s.nameID(name) || rec.RequestID != s.reqID+"-2" {
		return fmt.Errorf("chunk receipt names %q/%q, want %s/%s", rec.NodeID, rec.RequestID, s.nameID(name), s.reqID+"-2")
	}
	return nil
}

func (s *sr3State) failedZeroReceipt(name string) error {
	rec, keys, err := s.storedReceipt(s.reqID)
	if err != nil {
		return err
	}
	if rec.NodeID != s.nameID(name) {
		return fmt.Errorf("attempt 1's receipt is %s's, want %s's", rec.NodeID, name)
	}
	if vr, _ := keys["void_reason"].(string); vr == "" {
		return fmt.Errorf("attempt 1's receipt names no void_reason: %v", keys)
	}
	es, err := s.db.RecentByUser(s.wallet, 2000)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.RequestID == s.reqID && e.Cost != 0 {
			return fmt.Errorf("attempt 1 cost %v, want $0", e.Cost)
		}
	}
	return nil
}

func (s *sr3State) failedNotInStream() error {
	rec, _, err := s.storedReceipt(s.reqID)
	if err != nil {
		return err
	}
	if bytes.Contains(s.lastBody, []byte(protocol.EncodeReceipt(rec))) || bytes.Contains(s.lastBody, []byte(rec.NodeID)) {
		return fmt.Errorf("the failed attempt's receipt or station id reached the stream")
	}
	return nil
}

func (s *sr3State) chunkTopModel(want string) error { return s.itsKeyIs("model", want) }

func (s *sr3State) firstFrameModel(want string) error {
	return s.firstFrameHeaderIs("X-RogerAI-Model", want)
}

func (s *sr3State) noSecondStation() error {
	n := 0
	for _, l := range s.dispatchedIDs() {
		n += len(l)
	}
	if n != 1 {
		return fmt.Errorf("%d attempts were dispatched, want exactly one", n)
	}
	return nil
}

func (s *sr3State) chunkBilledFor(n int) error {
	if err := s.usageNumIs("completion_tokens", strconv.Itoa(n)); err != nil {
		return err
	}
	return s.receiptBrokerCompletion(n)
}

func (s *sr3State) chunkCostZero() error { return s.usageNumIs("cost", "0") }

func (s *sr3State) voidNamesStall() error {
	v, _, err := s.usageVal("rogerai.void_reason")
	if err != nil {
		return err
	}
	if r, _ := v.(string); !strings.Contains(r, "stall") {
		return fmt.Errorf("void_reason %v does not name the stall", v)
	}
	return nil
}

func (s *sr3State) completionZeroCostZero() error {
	if err := s.usageNumIs("completion_tokens", "0"); err != nil {
		return err
	}
	return s.usageNumIs("cost", "0")
}

func (s *sr3State) voidPresent() error {
	v, _, err := s.usageVal("rogerai.void_reason")
	if err != nil {
		return err
	}
	if r, _ := v.(string); r == "" {
		return fmt.Errorf("void_reason is empty")
	}
	return nil
}

func (s *sr3State) holdReleasedFull() error {
	holds, releases, spends, _, err := s.holdRows()
	if err != nil {
		return err
	}
	if holds == 0 || releases < holds || spends != 0 {
		return fmt.Errorf("holds %d releases %d spends %d, want every hold released and no spend", holds, releases, spends)
	}
	return nil
}

func (s *sr3State) noChunkAfterClose() error {
	if s.w == nil || !s.w.cancelled {
		return fmt.Errorf("the consumer never disconnected (no content frame arrived)")
	}
	s.w.mu.Lock()
	post := append([]byte(nil), s.w.postCancel...)
	s.w.mu.Unlock()
	if bytes.Contains(post, []byte(`"rogerai"`)) {
		return fmt.Errorf("a usage chunk was written after the consumer disconnected: %q", post)
	}
	return nil
}

func (s *sr3State) settlesAtBilled() error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, n, err := s.debit()
		if err == nil && n > 0 && d > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the cancelled stream never settled (debit %v, %d spend rows, %v)", d, n, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *sr3State) receiptInChain(name string) error {
	rec, _, err := s.storedReceipt(s.reqID)
	if err != nil {
		return err
	}
	if rec.NodeID != s.nameID(name) {
		return fmt.Errorf("stored receipt names %s, want %s", rec.NodeID, name)
	}
	return nil
}

func (s *sr3State) generationCancelled() error {
	r := httptest.NewRequest(http.MethodGet, "/generation?id="+s.reqID, nil)
	signReq(r, s.consumerPriv, nil)
	w := httptest.NewRecorder()
	s.b.routes().ServeHTTP(w, r)
	if w.Code != 200 {
		return fmt.Errorf("GET /generation = %d %.200s", w.Code, w.Body.String())
	}
	var rec map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rec); err != nil {
		return err
	}
	d, _, _ := s.debit()
	if c, _ := rec["cost"].(float64); !approx(c, d) || rec["cancelled"] != true {
		return fmt.Errorf("record cost %v cancelled %v, want %v and true", rec["cost"], rec["cancelled"], d)
	}
	return nil
}

func (s *sr3State) holdReleased() error {
	holds, releases, spends, _, err := s.holdRows()
	if err != nil {
		return err
	}
	if releases == 0 || spends != 0 || holds == 0 {
		return fmt.Errorf("holds %d releases %d spends %d, want the hold released and nothing spent", holds, releases, spends)
	}
	return nil
}

func (s *sr3State) noCostComment() error {
	if i, _ := s.commentIndex(); i >= 0 {
		return fmt.Errorf("a `: rogerai-cost=` comment was written for an unsettled stream")
	}
	return nil
}

func (s *sr3State) completionAsTodaysRule() error {
	f, err := s.usageNum("completion_tokens")
	if err != nil {
		return err
	}
	rec, _, err := s.storedReceipt(s.reqID)
	if err != nil {
		return err
	}
	if int(f) != rec.BrokerCompletionTokens {
		return fmt.Errorf("usage.completion_tokens %v, the settled receipt billed %d", f, rec.BrokerCompletionTokens)
	}
	return nil
}

func (s *sr3State) neitherVoidedNorStruck() error {
	_, keys, err := s.storedReceipt(s.reqID)
	if err != nil {
		return err
	}
	if vr, _ := keys["void_reason"].(string); vr != "" {
		return fmt.Errorf("the stream was voided: %s", vr)
	}
	strikes, err := s.strikesOf("n-1")
	if err != nil {
		return err
	}
	if len(strikes) != 0 {
		return fmt.Errorf("n-1 was struck: %v", strikes)
	}
	return nil
}

func (s *sr3State) billedFor300() error {
	if err := s.completionAsTodaysRule(); err != nil {
		return err
	}
	f, _ := s.usageNum("completion_tokens")
	if f <= 0 {
		return fmt.Errorf("usage.completion_tokens is %v for a 300-token reasoning stream", f)
	}
	return nil
}

func (s *sr3State) costAboveZero() error {
	f, err := s.usageNum("cost")
	if err != nil {
		return err
	}
	if f <= 0 {
		return fmt.Errorf("usage.cost %v, want > 0", f)
	}
	return nil
}

// --- Then: the bridge ---------------------------------------------------------------------

func (s *sr3State) forgedStripped() error {
	if s.forged != "" && bytes.Contains(s.lastBody, []byte(s.forged)) {
		return fmt.Errorf("the forged rogerai object reached the consumer: %.500s", s.lastBody)
	}
	return s.exactlyOneRogerai()
}

func (s *sr3State) ownChunkSettled() error {
	rec, err := s.receiptOfChunk()
	if err != nil {
		return err
	}
	if !rec.VerifyBroker(s.brokerPub()) {
		return fmt.Errorf("the broker's chunk receipt does not verify")
	}
	return s.lastBeforeDoneIsChunk()
}

// --- Then: an old client ------------------------------------------------------------------

func (s *sr3State) oldClientView() (string, int, bool) {
	var text strings.Builder
	n, sawDone := 0, false
	for _, ev := range sr3Events(s.lastBody) {
		d := dataOf(ev)
		if d == "" {
			continue
		}
		if d == "[DONE]" {
			sawDone = true
			break
		}
		var m struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(d), &m) != nil {
			continue
		}
		for _, c := range m.Choices {
			if c.Delta.Content != "" {
				n++
				text.WriteString(c.Delta.Content)
			}
		}
	}
	return text.String(), n, sawDone
}

func (s *sr3State) oldClientSeesAll() error {
	_, n, done := s.oldClientView()
	if !done || n != s.contentN {
		return fmt.Errorf("an old client saw %d of %d content frames, [DONE]=%v", n, s.contentN, done)
	}
	return nil
}

func (s *sr3State) oldClientAssembled() error {
	got, _, _ := s.oldClientView()
	if got != s.expectText {
		return fmt.Errorf("assembled %q, want %q", got, s.expectText)
	}
	return nil
}

// --- Then: adversarial ---------------------------------------------------------------------

func (s *sr3State) billsRecounted(n int) error {
	return s.usageNumIs("completion_tokens", strconv.Itoa(n))
}

func (s *sr3State) completionAtMost(n int) error {
	f, err := s.usageNum("completion_tokens")
	if err != nil {
		return err
	}
	if f > float64(n) {
		return fmt.Errorf("usage.completion_tokens %v > %d", f, n)
	}
	return nil
}

func (s *sr3State) costAtMostFor(n int) error {
	f, err := s.usageNum("cost")
	if err != nil {
		return err
	}
	p, err := s.usageNum("prompt_tokens")
	if err != nil {
		return err
	}
	st := s.st("n-1")
	limit := (p*st.priceIn + float64(n)*st.priceOut) / 1e6
	if f > limit+1e-12 {
		return fmt.Errorf("usage.cost %v exceeds the cost of %d completion tokens (%v)", f, n, limit)
	}
	return nil
}

func (s *sr3State) forgedLogged(name, phrase string) error {
	id := s.nameID(name)
	for _, l := range strings.Split(s.logs.String(), "\n") {
		if strings.Contains(l, id) && strings.Contains(l, phrase) {
			return nil
		}
	}
	return fmt.Errorf("no log line names %s and %q", id, phrase)
}

func (s *sr3State) notStruck(name string) error {
	strikes, err := s.strikesOf(name)
	if err != nil {
		return err
	}
	if len(strikes) != 0 {
		return fmt.Errorf("%s was struck: %v", name, strikes)
	}
	return nil
}

func (s *sr3State) genuineChunk() error {
	rec, err := s.receiptOfChunk()
	if err != nil {
		return err
	}
	if !rec.VerifyBroker(s.brokerPub()) || !rec.VerifyNode(s.st("n-1").pubHex) {
		return fmt.Errorf("the chunk's receipt is not the genuine co-signed one")
	}
	return nil
}

func (s *sr3State) withheldUntilIdle() error {
	if s.idle == 0 {
		return fmt.Errorf("no idle window was set for this scenario")
	}
	if el := s.t1.Sub(s.t0); el < s.idle {
		return fmt.Errorf("the stream ended after %v, before the %v idle window", el, s.idle)
	}
	return s.doneAfterChunk()
}

func (s *sr3State) voidedChunkThenDone() error {
	if err := s.chunkCostZero(); err != nil {
		return err
	}
	if err := s.voidPresent(); err != nil {
		return err
	}
	return s.doneAfterChunk()
}

func (s *sr3State) settlesOnce() error {
	time.Sleep(200 * time.Millisecond) // a second receipt, if accepted, would settle by now
	_, n, err := s.debit()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%d spend rows for the request, want exactly one", n)
	}
	return nil
}

func (s *sr3State) chunkNoText() error {
	ch, err := s.chunk()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(ch)
	if bytes.Contains(raw, []byte(sr3Marker)) || (s.promptText != "" && bytes.Contains(raw, []byte(strings.TrimSpace(s.promptText)[:20]))) {
		return fmt.Errorf("the usage chunk carries message text: %s", raw)
	}
	return nil
}

func (s *sr3State) chunkNoBand() error {
	ch, err := s.chunk()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(ch)
	code := s.freq
	if i := strings.LastIndex(code, " "); i >= 0 {
		code = code[i+1:]
	}
	if code == "" || bytes.Contains(raw, []byte(code)) {
		return fmt.Errorf("the usage chunk carries the band code (%q): %s", code, raw)
	}
	return nil
}

func (s *sr3State) costsEqualDigit() error {
	f, err := s.usageNum("cost")
	if err != nil {
		return err
	}
	if fmtCostHeader(f) != s.nsHdr.Get("X-RogerAI-Cost") {
		return fmt.Errorf("stream cost %s, non-stream X-RogerAI-Cost %s", fmtCostHeader(f), s.nsHdr.Get("X-RogerAI-Cost"))
	}
	return nil
}

// lit anchors a literal step sentence.
func lit(sentence string) string { return "^" + regexp.QuoteMeta(sentence) + "$" }

func (s *sr3State) register(sc *godog.ScenarioContext) {
	// Background
	sc.Step(lit("a broker with an empty in-memory node registry"), s.emptyRegistry)
	sc.Step(lit("the fee rate is 30%"), s.feeRate30)
	sc.Step(`^node "([^"]*)" is on air for "([^"]*)" at in-price \$([0-9.]+)/1M and out-price \$([0-9.]+)/1M$`, func(n, m, in, out string) error {
		return s.onAirInOut(n, m, sr3f(in), sr3f(out))
	})
	sc.Step(lit("the caller has a funded wallet"), s.fundedWallet)

	// When
	sc.Step(`^a streaming request for "([^"]*)" is served by "([^"]*)"$`, s.streamServedBy)
	sc.Step(`^a streaming request for "([^"]*)" is served by "([^"]*)" with (\d+) prompt and (\d+) completion tokens$`, s.streamServedTokens)
	sc.Step(`^a streaming request for "([^"]*)" with stream_options (.+) is served by "([^"]*)"$`, s.streamWithOptions)
	sc.Step(`^a streaming request for "([^"]*)" is served$`, s.streamServed)
	sc.Step(`^a streaming request for "([^"]*)" is served and settles at \$([0-9.]+)$`, s.streamSettlesAt)
	sc.Step(`^a streaming request for "([^"]*)" is served by "([^"]*)" at a measured ([0-9.]+) tok/s$`, s.streamAtTPS)
	sc.Step(`^a non-streaming request for "([^"]*)" is served$`, s.nonStreamServed)
	sc.Step(`^a streaming request with model "([^"]*)" and models (\[.*\]) is served by "([^"]*)"$`, s.streamFallback)
	sc.Step(`^"([^"]*)" finishes the stream and sends its receipt$`, s.finishesAndSendsReceipt)
	sc.Step(`^a streaming request for "([^"]*)" is served through the bridge$`, s.streamBridged)
	sc.Step(lit("the stream ends"), s.streamEnds)
	sc.Step(lit("a streaming request is served"), s.plainStream)
	sc.Step(lit("a stream and a non-stream relay bill the same counts at the same locked price"), s.streamAndNonStreamSame)

	// Given: station behaviour
	sc.Step(`^"([^"]*)" streams three content frames, then \[DONE\], then its receipt$`, s.threeFramesDoneReceipt)
	sc.Step(`^"([^"]*)" streams a final frame with usage (\{.*\})$`, s.finalUsageFrame)
	sc.Step(`^"([^"]*)" streams ": keepalive" comments between content frames$`, s.keepaliveComments)
	sc.Step(lit("the broker re-count is enabled"), s.recountEnabled)
	sc.Step(`^"([^"]*)" claims (\d+) completion tokens for a completion the broker counts as (\d+)$`, s.claimsCompletionCountedAs)
	sc.Step(`^the request's hold authorizes at most \$([0-9.]+)$`, s.holdAtMost)
	sc.Step(`^"([^"]*)" claims tokens whose cost would be \$([0-9.]+)$`, s.claimsTokensCosting)
	sc.Step(`^"([^"]*)" serves a stream billed at \$([0-9.]+)$`, s.billedAt)
	sc.Step(`^"([^"]*)" claims (\d+) prompt tokens? for a 4 KiB prompt the broker re-counts as (\d+)$`, s.promptClaim4K)
	sc.Step(`^the caller was first quoted "([^"]*)" for "([^"]*)" at \$([0-9.]+)/\$([0-9.]+) with a 24h lock$`, s.firstQuoted)
	sc.Step(`^"([^"]*)" is inside a published free window$`, s.insideFreeWindow)
	sc.Step(`^the caller's balance is \$([0-9.]+) before the request$`, s.balanceBefore)
	sc.Step(`^"([^"]*)" answers identically whether streamed or not$`, s.identicalBothWays)
	sc.Step(`^the caller has a monthly cap of \$([0-9.]+) and has spent \$([0-9.]+)$`, s.monthlyCapSpent)
	sc.Step(`^"([^"]*)" answers with an upstream 429 before any frame$`, s.answers429BeforeFrame)
	sc.Step(`^node "([^"]*)" is on air for "([^"]*)" and serves$`, s.onAirAndServes)
	sc.Step(`^no station is on air for "([^"]*)"$`, s.noStationFor)
	sc.Step(`^"([^"]*)" streams two content frames, then an error frame, then a receipt claiming (\d+) completion tokens$`, s.contentErrorReceipt)
	sc.Step(`^"([^"]*)" streams one content frame and then goes silent past the idle window$`, s.silentAfterOne)
	sc.Step(`^"([^"]*)" is the only station and streams no content, zero tokens, then \[DONE\]$`, s.emptyFinal)
	sc.Step(`^"([^"]*)" returns 200 with zero output before any frame$`, s.emptyBeforeFrame)
	sc.Step(lit("the consumer disconnects after the first content frame"), s.disconnectsAfterFirst)
	sc.Step(lit("the ledger refuses the stream's settle"), s.ledgerRefusesSettle)
	sc.Step(`^"([^"]*)" streams reasoning deltas then a content delta, with usage completion_tokens (\d+)$`, s.reasoningThenContent)
	sc.Step(`^"([^"]*)" streams reasoning deltas only and ends with finish_reason "length" and usage completion_tokens (\d+)$`, s.reasoningOnlyLength)
	sc.Step(`^no direct node is on air for "([^"]*)"$`, s.noDirectFor)
	sc.Step(`^an approved Tower "([^"]*)" serves "([^"]*)" via its hub$`, s.towerViaHub)
	sc.Step(`^an approved Tower "([^"]*)" emits a frame carrying a usage\.rogerai object of its own$`, s.towerForgesRogerai)
	sc.Step(lit("a client that ignores unknown frames and stops at [DONE]"), s.oldClient)
	sc.Step(`^"([^"]*)" streams a usage frame claiming completion_tokens (\d+) for a (\d+)-token completion$`, s.usageClaimsFor)
	sc.Step(`^"([^"]*)"'s receipt claims (\d+) completion tokens?$`, s.receiptClaims)
	sc.Step(`^"([^"]*)" streams a frame carrying usage\.rogerai with a forged receipt$`, s.forgedRogeraiFrame)
	sc.Step(`^"([^"]*)" streams \[DONE\] and then never sends a receipt$`, s.doneNoReceipt)
	sc.Step(`^"([^"]*)" sends two receipts for the same job$`, s.twoReceipts)
	sc.Step(lit("the request was made on a private band"), s.onPrivateBand)

	// Then: shape and placement
	sc.Step(lit("the last `data:` frame before `data: [DONE]` is an object with \"choices\" equal to []"), s.lastBeforeDoneIsChunk)
	sc.Step(lit(`its "object" is "chat.completion.chunk"`), func() error { return s.itsKeyIs("object", "chat.completion.chunk") })
	sc.Step(lit(`its "id" is the request id`), s.itsIDIsRequestID)
	sc.Step(`^its "model" is "([^"]*)"$`, func(m string) error { return s.itsKeyIs("model", m) })
	sc.Step(lit("`data: [DONE]` is the final frame of the stream"), s.doneIsFinal)
	sc.Step(`^(?:the usage chunk has |the usage chunk's |its )?usage\.([a-z_.]+) (?:is |equal to |equals )?(-?[0-9]+(?:\.[0-9]+)?)$`, s.usageNumIs)
	sc.Step(`^(?:the usage chunk's |the chunk's )?usage\.([a-z_.]+) is "([^"]*)"$`, s.usageStrIs)
	sc.Step(`^the usage chunk has usage\.([a-z_.]+) "([^"]*)"$`, s.usageStrIs)
	sc.Step(lit("usage.cost equal to the settled cost"), s.costIsSettled)
	sc.Step(`^usage\.rogerai has the keys (.+)$`, s.rogeraiHasKeys)
	sc.Step(`^usage\.rogerai has no key "([^"]*)"$`, s.rogeraiNoKey)
	sc.Step(`^usage\.rogerai has none of the keys (.+)$`, s.rogeraiNoneOf)
	sc.Step(lit("exactly one frame in the stream carries a usage.rogerai object"), s.exactlyOneRogerai)
	sc.Step(lit("the usage chunk is framed as `data: <json>\\n\\n`"), s.chunkFramedAlone)
	sc.Step(lit("it is not merged into the station's last frame"), s.notMerged)
	sc.Step(lit("the stream ends with the broker's usage chunk before [DONE]"), s.endsWithChunkBeforeDone)
	sc.Step(lit("the consumer receives the three content frames as they arrive"), s.threeAsTheyArrive)
	sc.Step(lit("the consumer does not receive [DONE] before the broker's usage chunk"), s.noDoneBeforeChunk)
	sc.Step(lit("the consumer receives [DONE] after it"), s.doneAfterChunk)
	sc.Step(lit("the station's usage frame is forwarded unchanged"), s.stationUsageForwarded)
	sc.Step(lit("the broker's usage chunk follows it"), s.brokerChunkFollowsStation)
	sc.Step(lit("only the broker's carries usage.rogerai"), s.exactlyOneRogerai)
	sc.Step(lit("a comment line `: rogerai-cost=<x>` follows the usage chunk"), s.commentFollowsChunk)
	sc.Step(lit("<x> equals usage.cost formatted by fmtCostHeader"), s.commentEqualsCost)
	sc.Step(lit("the comment is marked deprecated in the OpenAPI description"), s.commentDeprecatedInOpenAPI)
	sc.Step(lit("every station comment is forwarded as it arrives"), s.stationCommentsForwarded)
	sc.Step(lit("the broker's usage chunk still comes last before [DONE]"), s.chunkLastBeforeDone)

	// Then: receipts and equality
	sc.Step(lit("usage.rogerai.receipt decodes with DecodeReceipt"), s.receiptDecodes)
	sc.Step(`^the decoded receipt names the request id, "([^"]*)" and "([^"]*)"$`, s.receiptNames)
	sc.Step(`^the decoded receipt carries broker signature version (\d+)$`, s.receiptSigVersion)
	sc.Step(lit("VerifyBroker verifies it over the broker canonical form"), s.receiptVerifyBroker)
	sc.Step(lit("the coverage report says the billed counts are covered"), s.coverageCovered)
	sc.Step(`^the decoded receipt's node signature verifies against "([^"]*)"'s key$`, s.nodeSigVerifies)
	sc.Step(`^the decoded receipt's BrokerCompletionTokens is (\d+)$`, s.receiptBrokerCompletion)
	sc.Step(`^usage\.cost equals \(prompt_billed \* ([0-9.]+) \+ (\d+) \* ([0-9.]+)\) / 1e6$`, s.costFormula)
	sc.Step(lit("the ledger debit for the request equals usage.cost"), s.debitEqualsCost)
	sc.Step(`^the ledger debit is \$([0-9.]+)$`, s.ledgerDebitIs)
	sc.Step("^the comment reads \x60: rogerai-cost=([0-9.]+)\x60$", s.commentReads)
	sc.Step(`^usage\.prompt_tokens is (\d+), the clamped re-count, not (\d+)$`, s.promptClampedNot)
	sc.Step(lit("usage.rogerai.locked_until is the lock's unix expiry"), s.lockedUntilIsLock)
	sc.Step(`^the node's stored tps estimate was updated from the same figure$`, func() error { return s.storedTPSUpdated("") })
	sc.Step(lit("the stream's usage.prompt_tokens equals the non-stream X-RogerAI-Tokens-In"), s.eqTokensIn)
	sc.Step(lit("usage.completion_tokens equals X-RogerAI-Tokens-Out"), s.eqTokensOut)
	sc.Step(lit("usage.cost equals X-RogerAI-Cost"), s.eqCost)
	sc.Step(lit("usage.rogerai.price_in / price_out equal X-RogerAI-Price"), s.eqPrice)
	sc.Step(lit("usage.rogerai.node equals X-RogerAI-Provider"), s.eqProvider)

	// Then: headers
	sc.Step(`^the response headers at the first frame include ([A-Za-z-]+) "([^"]*)"$`, s.firstFrameHeaderIs)
	sc.Step(lit("X-RogerAI-Price with the locked in/out prices"), s.firstFramePriceLocked)
	sc.Step(`^(Content-Type|X-RogerAI-[A-Za-z-]+) "([^"]*)"$`, s.firstFrameHeaderIs)
	sc.Step(`^the response headers at the first frame include (X-RogerAI-[A-Za-z-]+ and X-RogerAI-[A-Za-z-]+)$`, s.firstFrameHas)
	sc.Step(`^the response headers carry no (.+)$`, s.headersCarryNone)
	sc.Step(lit("no HTTP trailer is used"), s.noTrailer)
	sc.Step(`^the consumer sees a 200 whose X-RogerAI-Provider is "([^"]*)"$`, s.sees200From)

	// Then: failover, errors, disconnects
	sc.Step(`^the usage chunk's receipt names "([^"]*)" and the attempt id of the second attempt$`, s.receiptNamesSecondAttempt)
	sc.Step(`^the failed attempt's \$0 receipt exists in "([^"]*)"'s chain$`, s.failedZeroReceipt)
	sc.Step(lit("it is not in the stream"), s.failedNotInStream)
	sc.Step(`^the chunk's "model" is "([^"]*)"$`, s.chunkTopModel)
	sc.Step(`^X-RogerAI-Model at the first frame is "([^"]*)"$`, s.firstFrameModel)
	sc.Step(lit("no second station is tried"), s.noSecondStation)
	sc.Step(`^the stream ends with a usage chunk whose usage\.completion_tokens is the billed count for (\d+)$`, s.chunkBilledFor)
	sc.Step(lit("usage.cost equals the settled cost for those tokens"), s.costIsSettled)
	sc.Step(lit("[DONE] follows the chunk"), s.doneAfterChunk)
	sc.Step(lit("the stream ends with a usage chunk whose usage.cost is 0"), s.chunkCostZero)
	sc.Step(lit("usage.rogerai.void_reason names the stall"), s.voidNamesStall)
	sc.Step(lit("the usage chunk has usage.completion_tokens 0 and usage.cost 0"), s.completionZeroCostZero)
	sc.Step(lit("usage.rogerai.void_reason is present"), s.voidPresent)
	sc.Step(lit("the hold is released in full"), s.holdReleasedFull)
	sc.Step(lit("no usage chunk is written to the closed connection"), s.noChunkAfterClose)
	sc.Step(lit("the request settles at the billed cost"), s.settlesAtBilled)
	sc.Step(`^the receipt is in "([^"]*)"'s chain$`, s.receiptInChain)
	sc.Step(lit(`GET /generation for the request shows the cost and "cancelled" true`), s.generationCancelled)
	sc.Step(lit("the hold is released"), s.holdReleased)
	sc.Step(lit("no `: rogerai-cost=` comment is written"), s.noCostComment)
	sc.Step(lit("usage.completion_tokens equals the billed count today's rule produces for that stream"), s.completionAsTodaysRule)
	sc.Step(lit("the stream is neither voided nor struck"), s.neitherVoidedNorStruck)
	sc.Step(lit("usage.completion_tokens is the billed count for 300 under today's rule"), s.billedFor300)
	sc.Step(lit("usage.cost is above 0"), s.costAboveZero)

	// Then: bridge
	sc.Step(lit("that frame reaches the consumer without its usage.rogerai key"), s.forgedStripped)
	sc.Step(lit("the broker's own chunk follows with the settled figures"), s.ownChunkSettled)

	// Then: an old client
	sc.Step(lit("the client sees every content frame and [DONE]"), s.oldClientSeesAll)
	sc.Step(lit("the client's assembled completion is unchanged"), s.oldClientAssembled)

	// Then: adversarial
	sc.Step(`^the broker's chunk bills the re-counted (\d+) where the re-count applies$`, s.billsRecounted)
	sc.Step(`^the broker's chunk bills min\(claim, re-count\) = (\d+) completion tokens?$`, s.billsRecounted)
	sc.Step(lit("the ledger debit follows the broker's chunk, not the station's frame"), s.debitEqualsCost)
	sc.Step(`^usage\.completion_tokens is at most (\d+)$`, s.completionAtMost)
	sc.Step(`^usage\.cost is at most the cost of (\d+) completion tokens$`, s.costAtMostFor)
	sc.Step(lit("the forwarded frame has no usage.rogerai key"), s.forgedStripped)
	sc.Step(`^a log line names "([^"]*)" and "([^"]*)"$`, s.forgedLogged)
	sc.Step(lit("the station is not struck for it"), func() error { return s.notStruck("n-1") })
	sc.Step(lit("the broker's own chunk follows with the genuine receipt"), s.genuineChunk)
	sc.Step(lit("the broker withholds [DONE] until the idle window expires"), s.withheldUntilIdle)
	sc.Step(lit("the stream ends with a voided usage chunk (cost 0, void_reason set) and then [DONE]"), s.voidedChunkThenDone)
	sc.Step(lit("exactly one usage chunk is written"), s.exactlyOneRogerai)
	sc.Step(lit("the request settles once"), s.settlesOnce)
	sc.Step(lit("the usage chunk contains no message text"), s.chunkNoText)
	sc.Step(lit("it contains no band code"), s.chunkNoBand)
	sc.Step(lit("their costs are equal to the last digit"), s.costsEqualDigit)
}

func TestStreamReceiptParityBDD(t *testing.T) {
	st := &sr3State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardown()
	})
	suite := godog.TestSuite{
		Name: "stream_receipt_parity",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, st.reset() })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.teardown()
				return ctx, nil
			})
			st.register(sc)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/trust/stream_receipt_parity.feature"},
			Tags: "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later && ~@slice5", TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("stream_receipt_parity scenarios failed")
	}
}

// keep bufio imported for the harness parity of the station reader (scanner-based upstreams).
var _ = bufio.NewScanner
