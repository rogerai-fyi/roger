// Package agent is the provider side ("rogerai share"). It registers with a
// broker and then DIALS OUT - N outbound long-poll loops pull relayed jobs from
// the broker, serve them against the local OpenAI-compatible upstream, sign a
// lineage receipt, and POST the result back. No inbound ports, no public URL,
// no tunnel dependency (the AI-Horde pattern). NAT-friendly everywhere.
package agent

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// shareFeeRate is the platform take used only to ESTIMATE the session's live
// earnings panel (the broker is the source of truth at settle). Matches the
// broker default; override with ROGERAI_FEE for an accurate local readout.
const shareFeeRate = 0.30

// Config is everything `rogerai share` needs to become a provider: the broker to
// register with, the local upstream to serve against, the single model offer and
// its pricing/schedule, and operational knobs (poll concurrency, confidential
// attestation, bridge token).
type Config struct {
	Broker, Upstream, UpstreamKey string
	NodeID, Region, HW, Model     string
	PriceIn, PriceOut             float64
	Ctx, Parallel                 int
	BridgeToken                   string
	Confidential                  bool
	Attestation                   string
	Schedule                      []protocol.PriceWindow
}

var (
	mu       sync.Mutex
	lastHash string
)

// Session is a running in-process share (the TUI's /share). It exposes live
// counters so the ON-AIR panel can render connections + earnings without the
// agent importing the TUI. Stop ends the poll loops. Earnings here are the
// node's gross owner-share in credits (= dollars), summed from served receipts.
type Session struct {
	cfg           Config
	servedReqs    atomic.Int64
	servedToks    atomic.Int64
	earningsMicro atomic.Int64 // owner-share in millionths of a credit (avoid float races)
	stop          chan struct{}
	rereg         *reregistrar // shared self-healing re-register coordinator
}

// reregistrar is the node's self-healing coordinator. The broker is in-memory:
// a redeploy/restart wipes its node registry, after which every poll/heartbeat
// gets 404 "unknown node" (or 401/403 once the token no longer matches). This
// holds the CURRENT bridge token (refreshed on every re-register, since each
// register issues a new one) behind a mutex so all pollers + the heartbeat read
// the live token each iteration, and single-flights the re-register so N
// concurrent workers hitting 404 cause exactly ONE re-register, not N.
type reregistrar struct {
	broker string
	reg    protocol.NodeRegistration
	priv   ed25519.PrivateKey

	mu    sync.Mutex
	cond  *sync.Cond
	token string // the live bridge token (workers read this every iteration)
	gen   uint64 // bumped on every successful re-register
	busy  bool   // a re-register is in flight (single-flight gate)
}

func newReregistrar(broker string, reg protocol.NodeRegistration, priv ed25519.PrivateKey) *reregistrar {
	rr := &reregistrar{broker: broker, reg: reg, priv: priv, token: reg.BridgeToken}
	rr.cond = sync.NewCond(&rr.mu)
	return rr
}

// curToken returns the live bridge token plus the generation it belongs to
// (workers call this every iteration so a refreshed token after a re-register is
// picked up immediately; the generation is passed back into recover so the
// single-flight gate knows which re-register a 404 is reacting to).
func (rr *reregistrar) curToken() (string, uint64) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.token, rr.gen
}

// recover re-registers the node after the broker forgot it (404/401/403). It is
// single-flight: the first caller for a given generation performs the
// re-register (with bounded backoff against a still-down broker) while later
// callers that observed the SAME generation block until it completes, then
// return without re-registering again. seenGen is the generation the caller last
// observed via curToken; if the generation has already advanced, another worker
// already recovered and we return immediately so the caller picks up the fresh
// token on its next iteration. Respects stop.
func (rr *reregistrar) recover(seenGen uint64, stop <-chan struct{}) {
	rr.mu.Lock()
	// Someone already re-registered past the generation we last saw - a fresh
	// token is already available; just let the caller re-read it.
	if rr.gen != seenGen {
		rr.mu.Unlock()
		return
	}
	if rr.busy {
		// A re-register is in flight for this generation; wait for it and ride it.
		for rr.busy && rr.gen == seenGen {
			rr.cond.Wait()
		}
		rr.mu.Unlock()
		return
	}
	rr.busy = true
	rr.mu.Unlock()

	// Re-register with the SAME reg (idempotent on the broker; re-sends the same
	// offers/HW so the node reappears identically in /market + /discover). The
	// only mutated fields are a fresh anti-replay timestamp + signature and a
	// fresh bridge token, so the broker's tunnel adopts the token we will now use.
	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second}
	attempt := 0
	for {
		select {
		case <-stop:
			rr.finishBusy()
			return
		default:
		}
		newTok := randHex(16)
		reg := rr.reg
		reg.BridgeToken = newTok
		reg.TS = time.Now().Unix()
		reg.SignRegistration(rr.priv)
		if err := register(rr.broker, reg); err == nil {
			rr.mu.Lock()
			rr.token = newTok
			rr.gen++
			rr.busy = false
			rr.cond.Broadcast()
			rr.mu.Unlock()
			log.Printf("broker restarted - re-registered node %s", rr.reg.NodeID)
			return
		}
		d := backoff[attempt]
		if attempt < len(backoff)-1 {
			attempt++
		}
		select {
		case <-stop:
			rr.finishBusy()
			return
		case <-time.After(d):
		}
	}
}

// finishBusy clears the single-flight gate without advancing the generation
// (used on the stop path so a blocked waiter is released cleanly).
func (rr *reregistrar) finishBusy() {
	rr.mu.Lock()
	rr.busy = false
	rr.cond.Broadcast()
	rr.mu.Unlock()
}

// Served returns the request + completion-token counts served so far.
func (s *Session) Served() (reqs, tokens int64) {
	return s.servedReqs.Load(), s.servedToks.Load()
}

// Earnings returns the node's accrued owner-share in credits ($).
func (s *Session) Earnings() float64 {
	return float64(s.earningsMicro.Load()) / 1e6
}

// Model / Price / Node / Upstream surface the session's offer for the panel and for
// callers (e.g. the TUI's multi-endpoint SHARE table) that need to confirm which
// local server a model is being served from.
func (s *Session) Model() string            { return s.cfg.Model }
func (s *Session) Price() (in, out float64) { return s.cfg.PriceIn, s.cfg.PriceOut }
func (s *Session) Node() string             { return s.cfg.NodeID }
func (s *Session) Upstream() string         { return s.cfg.Upstream }

// Stop ends the session's poll loops (best-effort; the process can also just exit).
func (s *Session) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

// record folds a served job's receipt into the session counters (called by the
// in-process poll loop after it serves a job).
func (s *Session) record(rec protocol.UsageReceipt, feeRate float64) {
	s.servedReqs.Add(1)
	s.servedToks.Add(int64(rec.CompletionTokens))
	// owner-share = cost * (1 - fee); cost is the node-priced receipt cost.
	owner := rec.Cost() * (1 - feeRate)
	s.earningsMicro.Add(int64(owner*1e6 + 0.5))
}

// Run registers the node with the broker and starts cfg.Parallel outbound
// long-poll workers that serve relayed jobs against the local upstream. It blocks
// forever (the node serves until the process is killed); it returns early only if
// the initial broker registration fails.
func Run(cfg Config) error {
	if _, err := Start(cfg); err != nil {
		return err
	}
	select {} // serve forever
}

// Start registers the node and launches its outbound poll loops, returning a
// Session for live stats + Stop (the TUI's in-process /share). It does NOT block.
func Start(cfg Config) (*Session, error) {
	priv := loadOrCreateKey()
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	token := cfg.BridgeToken
	if token == "" {
		token = randHex(16)
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = 4
	}

	offer := protocol.ModelOffer{Model: cfg.Model, PriceIn: cfg.PriceIn, PriceOut: cfg.PriceOut, Ctx: cfg.Ctx, Schedule: cfg.Schedule}
	reg := protocol.NodeRegistration{
		NodeID: cfg.NodeID, PubKey: pubHex, BridgeToken: token,
		Region: cfg.Region, HW: cfg.HW, Offers: []protocol.ModelOffer{offer},
		Confidential: cfg.Confidential, Attestation: cfg.Attestation,
	}
	reg.TS = time.Now().Unix()
	reg.SignRegistration(priv) // prove we hold PubKey's private key
	if err := register(cfg.Broker, reg); err != nil {
		return nil, fmt.Errorf("register with %s: %w", cfg.Broker, err)
	}
	// Self-healing: the reregistrar holds the live token and re-registers (with the
	// same reg, idempotently) when the in-memory broker forgets the node after a
	// restart. All pollers + the heartbeat read its token each iteration.
	rereg := newReregistrar(cfg.Broker, reg, priv)
	sess := &Session{cfg: cfg, stop: make(chan struct{}), rereg: rereg}
	go heartbeatUntil(cfg.Broker, cfg.NodeID, rereg, sess.stop)

	log.Printf("sharing: node=%s broker=%s upstream=%s model=%s ($%.2f/$%.2f per 1M) pollers=%d",
		cfg.NodeID, cfg.Broker, cfg.Upstream, cfg.Model, cfg.PriceIn, cfg.PriceOut, cfg.Parallel)

	for i := 0; i < cfg.Parallel; i++ {
		go pollLoop(cfg, offer, priv, sess)
	}
	return sess, nil
}

// pollLoop: one outbound long-poll worker. Pulls a job, serves it, posts result.
// It reads the live token from the session's reregistrar each iteration, and on a
// 404 (broker forgot the node after a restart) or 401/403 (stale token) routes to
// a single-flight re-register instead of the silent retry, so the share heals
// itself rather than polling a dead registration forever.
func pollLoop(cfg Config, offer protocol.ModelOffer, priv ed25519.PrivateKey, sess *Session) {
	poll := &http.Client{Timeout: 35 * time.Second} // must exceed the broker's hold
	up := &http.Client{Timeout: 120 * time.Second}
	pollURL := cfg.Broker + "/agent/poll?node=" + url.QueryEscape(cfg.NodeID)
	for {
		select {
		case <-sess.stop:
			return // /share went off air
		default:
		}
		token, gen := sess.rereg.curToken()
		req, _ := http.NewRequest(http.MethodGet, pollURL, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := poll.Do(req)
		if err != nil {
			// Transient network error: keep the existing short retry (the broker may
			// just be momentarily unreachable, not have forgotten us).
			time.Sleep(2 * time.Second)
			continue
		}
		if resp.StatusCode == http.StatusNoContent {
			resp.Body.Close() // long-poll timed out with no work - re-poll immediately
			continue
		}
		if brokerForgot(resp.StatusCode) {
			resp.Body.Close()
			// The broker has no record of this node (restart) or our token no longer
			// matches - re-register (single-flight across all pollers) and resume.
			sess.rereg.recover(gen, sess.stop)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			time.Sleep(2 * time.Second)
			continue
		}
		var job protocol.Job
		json.NewDecoder(resp.Body).Decode(&job)
		resp.Body.Close()
		if isStream(job.Body) {
			rec := serveStream(cfg, offer, priv, token, job)
			recordIf(sess, rec)
		} else {
			res := serve(cfg, offer, priv, up, job)
			postResult(poll, cfg, token, res)
			recordIf(sess, res.Receipt)
		}
	}
}

// brokerForgot reports whether a node-facing status means the broker no longer
// knows this node: 404 (registry wiped by a restart, "unknown node") or 401/403
// (the token the broker has on file no longer matches ours). Both are healed by
// re-registering, not by the silent retry.
func brokerForgot(status int) bool {
	return status == http.StatusNotFound ||
		status == http.StatusUnauthorized ||
		status == http.StatusForbidden
}

// recordIf folds a served receipt into the session counters (no-op without a session).
func recordIf(sess *Session, rec protocol.UsageReceipt) {
	if sess != nil && rec.RequestID != "" {
		sess.record(rec, shareFeeRate)
	}
}

// heartbeatUntil heartbeats every 10s until stop is closed. The live BridgeToken
// (from the reregistrar, refreshed on every re-register) is sent as a Bearer so
// the broker can authenticate the heartbeat (an unsigned or forged node_id is
// rejected). Like the pollers, a 404 (or 401/403) means the broker forgot the
// node after a restart, so the heartbeat also triggers a single-flight
// re-register instead of silently failing forever.
func heartbeatUntil(broker, nodeID string, rereg *reregistrar, stop <-chan struct{}) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			token, gen := rereg.curToken()
			b, _ := json.Marshal(map[string]string{"node_id": nodeID})
			req, err := http.NewRequest(http.MethodPost, broker+"/nodes/heartbeat", bytes.NewReader(b))
			if err != nil {
				continue
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				continue // transient; the pollers also retry/heal
			}
			status := resp.StatusCode
			resp.Body.Close()
			if brokerForgot(status) {
				rereg.recover(gen, stop)
			}
		}
	}
}

func isStream(body []byte) bool {
	var p struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &p)
	return p.Stream
}

// serveStream serves a streaming (SSE) job: it streams the upstream response to
// the broker's /agent/stream (which pipes it to the waiting client), captures
// token usage from the final chunk, then posts a signed receipt to settle. The
// node asks the upstream to include a usage chunk so we can meter the stream.
func serveStream(cfg Config, offer protocol.ModelOffer, priv ed25519.PrivateKey, token string, job protocol.Job) protocol.UsageReceipt {
	client := &http.Client{Timeout: 10 * time.Minute} // streams can be long
	upReq, _ := http.NewRequest(http.MethodPost, cfg.Upstream, bytes.NewReader(withUsageOption(job.Body)))
	upReq.Header.Set("Content-Type", "application/json")
	if cfg.UpstreamKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+cfg.UpstreamKey)
	}
	resp, err := client.Do(upReq)
	if err != nil {
		postResult(client, cfg, token, protocol.JobResult{ID: job.ID, Status: http.StatusBadGateway})
		return protocol.UsageReceipt{}
	}
	defer resp.Body.Close()

	// Pipe upstream SSE -> broker, scanning for the usage chunk as it flows.
	pr, pw := io.Pipe()
	var promptTok, compTok int
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Bytes()
			pw.Write(line)
			pw.Write([]byte{'\n'})
			if bytes.Contains(line, []byte(`"usage"`)) {
				if p, c, ok := parseUsage(line); ok {
					promptTok, compTok = p, c
				}
			}
		}
		pw.Close()
	}()

	streamURL := cfg.Broker + "/agent/stream?node=" + url.QueryEscape(cfg.NodeID) + "&job=" + url.QueryEscape(job.ID)
	sreq, _ := http.NewRequest(http.MethodPost, streamURL, pr)
	sreq.Header.Set("Authorization", "Bearer "+token)
	sreq.Header.Set("Content-Type", "text/event-stream")
	if sresp, err := client.Do(sreq); err == nil { // blocks until the stream finishes
		sresp.Body.Close()
	}

	rec := protocol.UsageReceipt{
		RequestID: job.ID, NodeID: cfg.NodeID, User: job.User, Model: cfg.Model,
		PromptTokens: promptTok, CompletionTokens: compTok,
		PriceIn: offer.PriceIn, PriceOut: offer.PriceOut, TS: time.Now().Unix(),
		LineageMethod: "p0-upstream-usage-stream",
	}
	mu.Lock()
	rec.PrevHash = lastHash
	rec.SignNode(priv)
	lastHash = rec.Hash()
	mu.Unlock()
	postResult(client, cfg, token, protocol.JobResult{ID: job.ID, Status: resp.StatusCode, Receipt: rec})
	return rec
}

// withUsageOption sets stream_options.include_usage so the upstream emits a final
// usage chunk (OpenAI streaming) - needed to meter the stream.
func withUsageOption(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	if b, err := json.Marshal(m); err == nil {
		return b
	}
	return body
}

// parseUsage extracts token counts from an SSE "data: {...usage...}" line.
func parseUsage(line []byte) (prompt, completion int, ok bool) {
	i := bytes.IndexByte(line, '{')
	if i < 0 {
		return 0, 0, false
	}
	var d struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line[i:], &d) == nil && (d.Usage.PromptTokens > 0 || d.Usage.CompletionTokens > 0) {
		return d.Usage.PromptTokens, d.Usage.CompletionTokens, true
	}
	return 0, 0, false
}

func serve(cfg Config, offer protocol.ModelOffer, priv ed25519.PrivateKey, up *http.Client, job protocol.Job) protocol.JobResult {
	upReq, _ := http.NewRequest(http.MethodPost, cfg.Upstream, bytes.NewReader(job.Body))
	upReq.Header.Set("Content-Type", "application/json")
	if cfg.UpstreamKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+cfg.UpstreamKey)
	}
	resp, err := up.Do(upReq)
	if err != nil {
		return protocol.JobResult{ID: job.ID, Status: http.StatusBadGateway, Body: json.RawMessage(`{"error":"upstream unreachable"}`)}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var parsed struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(respBody, &parsed)

	rec := protocol.UsageReceipt{
		RequestID: job.ID, NodeID: cfg.NodeID, User: job.User, Model: cfg.Model,
		PromptTokens: parsed.Usage.PromptTokens, CompletionTokens: parsed.Usage.CompletionTokens,
		PriceIn: offer.PriceIn, PriceOut: offer.PriceOut, TS: time.Now().Unix(),
		LineageMethod: "p0-upstream-usage",
	}
	mu.Lock()
	rec.PrevHash = lastHash
	rec.SignNode(priv)
	lastHash = rec.Hash()
	mu.Unlock()
	return protocol.JobResult{ID: job.ID, Status: resp.StatusCode, Body: respBody, Receipt: rec}
}

func postResult(client *http.Client, cfg Config, token string, res protocol.JobResult) {
	b, _ := json.Marshal(res)
	req, _ := http.NewRequest(http.MethodPost, cfg.Broker+"/agent/result?node="+url.QueryEscape(cfg.NodeID), bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func register(broker string, reg protocol.NodeRegistration) error {
	b, _ := json.Marshal(reg)
	req, _ := http.NewRequest(http.MethodPost, broker+"/nodes/register", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	// Sign the registration with the OWNER's user key too: a node advertising a
	// nonzero price is an earning node and the broker requires the signing pubkey to
	// be bound to a GitHub owner (`rogerai login`). Free/unsigned sharing still works.
	client.SignRequest(req, b)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Surface a broker rejection instead of silently "succeeding" - otherwise
		// the node would start poll loops against a registration that didn't take.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("broker rejected registration (%d): %s", resp.StatusCode, bytes.TrimSpace(msg))
		}
		return fmt.Errorf("broker returned status %d", resp.StatusCode)
	}
	log.Printf("registered with broker %s as node %s", broker, reg.NodeID)
	return nil
}

func loadOrCreateKey() ed25519.PrivateKey {
	dir, _ := os.UserConfigDir()
	path := filepath.Join(dir, "rogerai", "node.key")
	if data, err := os.ReadFile(path); err == nil {
		if raw, err := hex.DecodeString(string(bytes.TrimSpace(data))); err == nil && len(raw) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(raw)
		}
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0600)
	log.Printf("generated node key at %s", path)
	return priv
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
