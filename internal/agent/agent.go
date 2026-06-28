// Package agent is the provider side ("roger share"). It registers with a
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
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// Config is everything `roger share` needs to become a provider: the broker to
// register with, the local upstream to serve against, the single model offer and
// its pricing/schedule, and operational knobs (poll concurrency, confidential
// attestation, bridge token).
type Config struct {
	Broker, Upstream, UpstreamKey string
	NodeID, Region, HW, Model     string
	PriceIn, PriceOut             float64
	Ctx, Parallel                 int
	// CtxEstimated marks Ctx as the last-resort default (no real per-model window was
	// detected), so the offer carries an honest "estimated" flag instead of presenting
	// a guess as a measured value.
	CtxEstimated bool
	BridgeToken  string
	Confidential bool
	Private      bool // go on air as a PRIVATE band (hidden; freq-code only)
	Schedule     []protocol.PriceWindow
}

var (
	mu       sync.Mutex
	lastHash string
)

// heartbeatInterval is how often the node heartbeats the broker to stay on-air. It
// is a var (not a const) only so tests can lower it; production uses ~10s, well
// inside the broker's nodeTTL liveness window.
var heartbeatInterval = 10 * time.Second

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
	link          atomic.Int32 // LinkState: is the BROKER actually acknowledging us?

	// Private band: the broker-minted band id + the secret frequency code (the code is
	// returned ONCE at the first register and stashed here so the caller - CLI/TUI -
	// can show it once; it is empty on a re-register). BandDisplay is cosmetic.
	bandID      string
	bandCode    string
	bandDisplay string

	// Broker-EFFECTIVE published price for this session's model, from the register
	// response (after any owner-authored web-console override). These default to the
	// locally-requested price and only differ when the broker is overriding it.
	effPriceIn     float64
	effPriceOut    float64
	overrideActive bool // an owner web-console price override is active for this model

	// confidential reports whether the broker GRANTED the confidential ◆ badge on the
	// last register (the response echo). It is meaningful only when this session asked
	// for confidential (cfg.Confidential): a true here means a real TEE quote verified;
	// a false on a confidential request means the claim was downgraded to standard
	// (fail-soft) and the CLI/TUI should say so rather than imply a badge.
	confidential bool
}

// Confidential reports whether the broker granted the confidential ◆ badge on the last
// register. It is only meaningful when this session requested confidential (cfg.Confidential):
// a confidential request that returns false here was downgraded to standard (the broker
// ran require=0 and the quote did not verify - e.g. an unblessed launch measurement).
func (s *Session) Confidential() bool { return s.confidential }

// RequestedConfidential reports whether this session ASKED for the confidential tier,
// so the CLI/TUI can tell "did not ask" apart from "asked but downgraded".
func (s *Session) RequestedConfidential() bool { return s.cfg.Confidential }

// EffectivePrice returns the broker-EFFECTIVE published price for this session's model
// (after any owner web-console override) and whether such an override is active. The
// CLI's on-air line shows this so an owner who priced their node on the web sees the
// real published number, not the locally-requested one.
func (s *Session) EffectivePrice() (priceIn, priceOut float64, override bool) {
	return s.effPriceIn, s.effPriceOut, s.overrideActive
}

// Band returns this session's private band id, the one-time secret code (empty
// unless this register just minted it), and the cosmetic display string. Used by the
// CLI/TUI to show the code exactly once after going private.
func (s *Session) Band() (id, code, display string) {
	return s.bandID, s.bandCode, s.bandDisplay
}

// LinkState is the TRUTHFUL on-air status: whether the broker is actually accepting
// this node (so customers + the website can see it), as observed from the heartbeat.
// The TUI surfaces this instead of a blind "ON AIR" so the operator never sees on-air
// while the broker is rejecting/unreachable (i.e. while customers can't reach them).
type LinkState int32

const (
	// LinkConnecting: registration acknowledged, but no heartbeat has been accepted
	// yet (the opening window right after going on air). Shown as "connecting".
	LinkConnecting LinkState = iota
	// LinkOnAir: the broker is accepting our heartbeats (200) - we are genuinely
	// live and routable. The ONLY state that renders a true "ON AIR".
	LinkOnAir
	// LinkReconnecting: heartbeats are failing - unreachable (network), or the broker
	// forgot us / rejected the token (a self-healing re-register is in flight). We are
	// NOT routable right now; shown as "RECONNECTING".
	LinkReconnecting
)

// Link reports the current truthful link state to the broker (see LinkState).
func (s *Session) Link() LinkState { return LinkState(s.link.Load()) }

// setLink records the latest observed broker link state (called from the heartbeat
// loop on every beat).
func (s *Session) setLink(st LinkState) {
	if s != nil {
		s.link.Store(int32(st))
	}
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
		// Re-attestation: a confidential node must present a FRESH nonce-bound quote on
		// every re-register (the broker spends the nonce single-use and lapses stale
		// attestations). Fetch a new nonce + quote here so the badge survives a broker
		// restart. If re-attestation fails (e.g. transient), drop the confidential
		// claim for this attempt rather than sending a stale/replayed quote - it is
		// re-earned on the next successful re-attest.
		if rr.reg.Confidential {
			if err := attestForRegistration(rr.broker, rr.priv, &reg); err != nil {
				log.Printf("re-attestation failed, re-registering as standard this round: %v", err)
				reg.Confidential = false
				reg.Attestation = ""
				reg.AttestKind = ""
				reg.AttestNonce = ""
			}
		}
		reg.TS = time.Now().Unix()
		reg.SignRegistration(rr.priv)
		// A re-register of a PRIVATE node returns only band_id (never the code again),
		// so the result is intentionally ignored here - the secret is shown only at the
		// initial mint in Start.
		if _, err := register(rr.broker, reg); err == nil {
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
	serveForever() // serve forever
	return nil
}

// serveForever blocks the calling goroutine until the process is killed. It is a
// package-level seam defaulting to the real block-forever (`select {}`), so the
// production Run path is byte-for-byte unchanged (serveForever never returns, so the
// trailing `return nil` is unreachable in production). Tests substitute a returning
// stub to exercise Run's success path without hanging.
var serveForever = func() { select {} }

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

	offer := protocol.ModelOffer{Model: cfg.Model, PriceIn: cfg.PriceIn, PriceOut: cfg.PriceOut, Ctx: cfg.Ctx, CtxEstimated: cfg.CtxEstimated, Schedule: cfg.Schedule}
	reg := protocol.NodeRegistration{
		NodeID: cfg.NodeID, PubKey: pubHex, BridgeToken: token,
		Region: cfg.Region, HW: cfg.HW, Offers: []protocol.ModelOffer{offer},
		Confidential: cfg.Confidential, Private: cfg.Private,
	}
	// Confidential tier: generate a REAL TEE quote bound to (pubkey, fresh broker
	// nonce). On non-TEE hardware this fails - we surface the error so the node does
	// NOT silently send a fake confidential claim. A node that did not ask for
	// confidential skips this entirely.
	if cfg.Confidential {
		if err := attestForRegistration(cfg.Broker, priv, &reg); err != nil {
			return nil, fmt.Errorf("confidential attestation: %w", err)
		}
	}
	reg.TS = time.Now().Unix()
	reg.SignRegistration(priv) // prove we hold PubKey's private key
	regRes, err := register(cfg.Broker, reg)
	if err != nil {
		return nil, fmt.Errorf("register with %s: %w", cfg.Broker, err)
	}
	if regRes.BandID != "" {
		reg.BandID = regRes.BandID // carry the band id on future re-registers
	}
	// Self-healing: the reregistrar holds the live token and re-registers (with the
	// same reg, idempotently) when the in-memory broker forgets the node after a
	// restart. All pollers + the heartbeat read its token each iteration.
	rereg := newReregistrar(cfg.Broker, reg, priv)
	sess := &Session{cfg: cfg, stop: make(chan struct{}), rereg: rereg,
		bandID: regRes.BandID, bandCode: regRes.BandCode, bandDisplay: regRes.BandDisplay,
		// Adopt the broker's confidential-grant echo: a confidential request that was
		// downgraded to standard (fail-soft) lands here as false so the CLI can warn.
		confidential: regRes.Confidential}
	// Adopt the broker-EFFECTIVE price for this model (after any owner web-console
	// override) so the CLI surfaces the real published number, not the requested one.
	sess.effPriceIn, sess.effPriceOut, sess.overrideActive = effectivePriceFor(regRes, cfg.Model, cfg.PriceIn, cfg.PriceOut)
	// Registration was acknowledged (register() returned ok); the link is "connecting"
	// until the first heartbeat is accepted, after which it flips to genuinely ON AIR.
	sess.setLink(LinkConnecting)
	go heartbeatUntil(cfg.Broker, cfg.NodeID, rereg, sess)

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
//
// It also records the TRUTHFUL link state on the session from each beat's outcome
// (200 -> ON AIR; unreachable/rejected -> RECONNECTING), so the provider UI reflects
// whether the broker is actually accepting the node rather than a blind "ON AIR". A
// beat fires immediately on entry (not only after the first 10s tick) so the status
// confirms quickly after going on air.
func heartbeatUntil(broker, nodeID string, rereg *reregistrar, sess *Session) {
	stop := sess.stop
	beat := func() {
		token, gen := rereg.curToken()
		b, _ := json.Marshal(map[string]string{"node_id": nodeID})
		req, err := http.NewRequest(http.MethodPost, broker+"/nodes/heartbeat", bytes.NewReader(b))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			// Broker unreachable: we are NOT routable - tell the operator we are
			// reconnecting, not falsely on-air. The pollers also retry/heal.
			sess.setLink(LinkReconnecting)
			return
		}
		status := resp.StatusCode
		resp.Body.Close()
		switch {
		case status == http.StatusOK:
			// The broker is accepting us: genuinely ON AIR (customers can see us).
			sess.setLink(LinkOnAir)
		case brokerForgot(status):
			// Forgot/rejected (restart or stale token): not routable until the
			// single-flight re-register heals it.
			sess.setLink(LinkReconnecting)
			rereg.recover(gen, stop)
		default:
			sess.setLink(LinkReconnecting)
		}
	}
	beat() // confirm quickly on entry rather than waiting a full tick
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			beat()
		}
	}
}

// redactUpstreamKey strips the node's upstream bearer key from bytes about to be
// relayed back to the broker/consumer. Standard OpenAI-compatible servers never echo
// the request Authorization header into their response, but a misconfigured proxy /
// debug endpoint can put it in an error body - this is defense-in-depth so the node
// operator's OWN upstream key can never leave the machine in a job result. A no-op
// when no key is configured (and never called with an empty key, which would match
// everywhere).
func redactUpstreamKey(b []byte, key string) []byte {
	if key == "" {
		return b
	}
	return bytes.ReplaceAll(b, []byte(key), []byte("[redacted]"))
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
			// Redact the node's own upstream key before it leaves the machine, in case the
			// upstream echoed the request Authorization header into an SSE error chunk.
			line := redactUpstreamKey(sc.Bytes(), cfg.UpstreamKey)
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
	// Belt-and-suspenders: never relay the node's own upstream key, in case the
	// upstream echoed the request Authorization header into its response body.
	respBody = redactUpstreamKey(respBody, cfg.UpstreamKey)

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

// registerResult carries the broker's register response. For a PRIVATE band the
// broker returns band_id on every register and the secret BandCode ONCE (on the
// first register that mints it - empty on every re-register, which is what makes
// the idempotent re-register safe to repeat without re-leaking). BandDisplay is the
// cosmetic "147.520 MHz · ..." string (not secret).
type registerResult struct {
	BandID      string `json:"band_id"`
	BandCode    string `json:"band_code"`    // SECRET, present only at first mint
	BandDisplay string `json:"band_display"` // cosmetic, not secret
	// EffectiveOffers is the broker-EFFECTIVE published offers AFTER any owner-authored
	// web-console override is applied, so the CLI shows the real published price (not the
	// locally-requested one). Overrides names the models that carry an active override.
	EffectiveOffers []protocol.ModelOffer `json:"effective_offers"`
	Overrides       []string              `json:"overrides"`
	// Confidential is the broker's echo of whether the confidential ◆ badge was granted
	// this register (false when not claimed or when a claim was downgraded to standard).
	Confidential bool `json:"confidential"`
}

// effectivePriceFor resolves the broker-EFFECTIVE published price for `model` from a
// register response: it prefers the broker's echoed effective offer (after any
// owner-authored web-console override) and falls back to the requested price when the
// broker echoed none for this model. override reports an active override for the model.
func effectivePriceFor(rr registerResult, model string, reqIn, reqOut float64) (in, out float64, override bool) {
	in, out = reqIn, reqOut
	for _, eo := range rr.EffectiveOffers {
		if eo.Model == model {
			in, out = eo.PriceIn, eo.PriceOut
			break
		}
	}
	for _, m := range rr.Overrides {
		if m == model {
			override = true
			break
		}
	}
	return in, out, override
}

func register(broker string, reg protocol.NodeRegistration) (registerResult, error) {
	b, _ := json.Marshal(reg)
	req, _ := http.NewRequest(http.MethodPost, broker+"/nodes/register", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	// Sign the registration with the OWNER's user key too: a node advertising a
	// nonzero price is an earning node and the broker requires the signing pubkey to
	// be bound to a GitHub owner (`roger login`). Free/unsigned sharing still works.
	client.SignRequest(req, b)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return registerResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Surface a broker rejection instead of silently "succeeding" - otherwise
		// the node would start poll loops against a registration that didn't take.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Surface the broker's reason verbatim for the rejections a user can ACT on: a
		// 403/401 owner-auth failure, AND a 429 hard per-owner on-air cap ("station limit
		// reached: ... take one off air"). The share UX shows this message so the operator
		// knows to free a slot rather than seeing a bare "status 429".
		if msg = bytes.TrimSpace(msg); len(msg) > 0 &&
			(resp.StatusCode == http.StatusForbidden ||
				resp.StatusCode == http.StatusUnauthorized ||
				resp.StatusCode == http.StatusTooManyRequests) {
			return registerResult{}, fmt.Errorf("broker rejected registration (%d): %s", resp.StatusCode, brokerErrMsg(msg))
		}
		return registerResult{}, fmt.Errorf("broker returned status %d", resp.StatusCode)
	}
	var rr registerResult
	// 64KB: the response now carries the effective offers (which can include a
	// time-of-use schedule), so allow more than the old band-only 4KB.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&rr)
	log.Printf("registered with broker %s as node %s", broker, reg.NodeID)
	return rr, nil
}

// brokerErrMsg extracts the human-readable reason from a broker error body. The
// broker replies {"error":{"message":"..."}} (jsonErr); we surface just the message
// so the share UX shows e.g. "station limit reached: ... take one off air" rather than
// the raw JSON envelope. Falls back to the raw bytes when it is not that shape.
func brokerErrMsg(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return string(body)
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

// ShareNodeID derives the broker node id for a share. It MUST be the single source
// of truth for both `roger share` (CLI) and the in-TUI [2] SHARE / h HIDE flows so
// that every model a host shares becomes a DISTINCT broker node.
//
// PRIVACY: the node id is PUBLIC - it is echoed verbatim in /discover and /market to
// every consumer. It MUST NOT leak anything sensitive about the host. The scheme is
// therefore `<station>-<model-slug>`, where `station` is a friendly, non-sensitive
// CALLSIGN the owner picks or that is auto-generated once and persisted (e.g.
// `brave-otter`), and `model-slug` is the model name (public, fine). NO hostname and
// NO upstream port ever appear in the node id.
//
// History (why this is the single chokepoint): the node id used to be the bare
// hostname, then `<hostname>-<model-slug>-<upstream-port>`. One `share` process serves
// one model, so running several bands/models on one host registered them all under the
// SAME node id. The broker keys nodes/tunnels/lastSeen/bridge-token by node id, so each
// register overwrote the prior sibling's token; the clobbered sibling's heartbeat then
// 401'd, its self-healing re-registrar fired and overwrote back - an infinite
// token-war / on-air "flapping" storm where only the last-registered band stayed
// visible. The per-model slug already makes DIFFERENT models on one host distinct
// nodes.
//
// instance disambiguates the RARE case of the SAME model shared twice from one station
// (e.g. two local servers): instance 0/1 yield the bare `<station>-<model-slug>`;
// instance 2,3,... append `-2`, `-3`. This is the per-process index, NOT the upstream
// port - no port ever leaks. The id is STABLE across a restart (persisted station +
// deterministic model slug + the same instance index), so a node re-registers as the
// same id (no orphan churn), and works with the per-band uniqueness from the
// multi-on-air work.
func ShareNodeID(station, model string, instance int) string {
	st := slugify(station)
	if st == "" {
		st = GenerateStation() // never emit a bare/hostnameless id; fall back to a fresh callsign
	}
	id := st
	if slug := slugify(model); slug != "" {
		id = st + "-" + slug
	}
	if instance >= 2 {
		id += "-" + strconv.Itoa(instance)
	}
	return id
}

// stationAdjectives / stationAnimals are the friendly, non-sensitive callsign
// vocabulary. A station name is one of each plus a small number (e.g. `brave-otter-37`),
// picked once with crypto/rand and persisted, so it is stable, readable, and reveals
// NOTHING about the host (no hostname, no network, no port). The number widens the combo
// space so independent installs rarely collide; collisions are harmless anyway (the
// broker keys on node id + owner pubkey) and an owner can always rename.
var (
	stationAdjectives = []string{
		"amber", "azure", "blithe", "bold", "brave", "bright", "brisk", "calm",
		"clever", "cosmic", "crimson", "dapper", "deft", "eager", "early", "easy",
		"electric", "fancy", "fleet", "fond", "gentle", "giant", "golden", "grand",
		"happy", "hardy", "hidden", "jolly", "keen", "kind", "lively", "lucky",
		"lunar", "merry", "mighty", "nimble", "noble", "polar", "prime", "proud",
		"quick", "quiet", "rapid", "royal", "ruby", "rustic", "sage", "scarlet",
		"sharp", "shy", "silent", "silver", "sleek", "snug", "solar", "spry",
		"steady", "stellar", "sunny", "swift", "tidy", "vivid", "warm", "witty",
	}
	stationAnimals = []string{
		"otter", "falcon", "lynx", "heron", "marten", "badger", "raven", "fox",
		"wolf", "bison", "moose", "elk", "hawk", "crane", "ibex", "puma",
		"jay", "wren", "robin", "finch", "owl", "kite", "tern", "swan",
		"seal", "orca", "narwhal", "walrus", "panda", "tapir", "civet", "genet",
		"koala", "lemur", "gibbon", "okapi", "quokka", "dingo", "ocelot", "serval",
		"caracal", "jaguar", "cougar", "marmot", "ermine", "stoat", "weasel", "mink",
		"beaver", "muskox", "gazelle", "impala", "kudu", "oryx", "addax", "saiga",
		"pika", "agouti", "coati", "kinkajou", "fennec", "jackal", "meerkat", "mongoose",
	}
)

// GenerateStation returns a fresh, friendly, NON-SENSITIVE station callsign like
// `brave-otter-37`, chosen with crypto/rand. It is meant to be called ONCE per install
// and persisted (see the CLI's loadOrCreateStation); the persisted value is then reused
// so the node re-registers as the same id across restarts. It reveals nothing about the
// host.
func GenerateStation() string {
	adj := stationAdjectives[randIndex(len(stationAdjectives))]
	animal := stationAnimals[randIndex(len(stationAnimals))]
	return adj + "-" + animal + "-" + strconv.Itoa(randIndex(90)+10)
}

// randIndex returns a uniform crypto/rand index in [0,n) (n>0). Falls back to 0 only if
// the system RNG fails, which never happens in practice.
func randIndex(n int) int {
	if n <= 0 {
		return 0
	}
	bn, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(bn.Int64())
}

// SlugStation normalizes a station callsign to the SAME broker-safe slug the node id
// uses (lowercased, non-alphanumerics collapsed to single dashes, trimmed). The CLI +
// TUI call this so what the owner types, what is persisted, and what appears in
// /discover all match. An input that slugs to nothing returns "" (callers then
// auto-generate), distinct from ShareNodeID which never returns a bare/empty id.
func SlugStation(s string) string { return slugify(s) }

// slugify lowercases s and collapses every run of non-alphanumeric characters to a
// single `-`, trimming leading/trailing `-`. It yields readable, broker-safe id
// fragments (e.g. "Qwen3-Coder/Next" -> "qwen3-coder-next").
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
