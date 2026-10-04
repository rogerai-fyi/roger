package main

// routing_model_fallback_bdd_test.go makes features/routing/model_fallback_list.feature
// EXECUTABLE against the REAL relay (contract features/routing/ROUTING-EXPRESSION-CONTRACT.md
// §3, with the §1b money invariants and §7 X-RogerAI-Model). It rides the upstream_failover
// harness (foState: a real broker over the real store - the in-memory reference, or the real
// Postgres when ROGERAI_TEST_DATABASE_URL is set - a real shared store over miniredis, and N
// real stations, each with its own scripted httptest upstream behind the real /agent/result and
// /agent/stream ingress) and the pins harness (rpState) for the edge/Tower fabric. Nothing is
// mocked: every Then reads the response, the store's rows (holds, releases, spends, earns,
// receipts, strikes, grant usage), the broker's own state (cooldowns, quotes, health), the
// upstreams' request logs, /admin/live, or the captured log.
//
// WHAT THIS RUNNER ADDS TO THE HARNESS (all prefixed mf1):
//   - stations that offer MORE THAN ONE model, a declared or ESTIMATED window, a free window;
//   - a recorder in front of every upstream script, so "b1 received a body with model b" is
//     read from the bytes the station's upstream actually got, in arrival order;
//   - a request builder that sends `model` / `models` / `provider` / `roger` exactly as the
//     scenario words them (the foState builder only knows a single model);
//   - one deterministic pick order per model: the first station declared for a model keeps
//     Tier A, later stations of the same model are demoted to Tier B with a strictly decreasing
//     success average (the landOrder device), so "a1 then a2 then a3" is the plan order.
//
// FIXTURE RULE (learned in slice 0): on the Postgres store a wallet with no row cannot settle
// even a $0 relay (free/self relays skip ensureSeeded - a known open finding). Every free,
// self-use and anonymous caller here is therefore given a wallet row the way production does
// (db.BalanceOf on the wallet walletOf() resolves) so the runner behaves the same on both
// stores; no scenario in this file is about that gap.
//
// SCALED CLOCKS: the 90 s non-stream deadline, the 10 s attempt budget and the 20 s stream
// commit grace are production vars; the deadline scenarios scale them (2 s / 2/9 s / 200 ms)
// exactly as upstream_failover's s1Holds85s / s1SlowFirstChunk do, and assert ORDER and ratio.
//
// OBSERVATION NOTES:
//   - "the effective model list is [...]" is read black-box: the same body is relayed again
//     with every earlier model's stations answering 429, and X-RogerAI-Model must walk the
//     list in order (so order AND membership are observed, not a private field).
//   - "no pick ran" is the /admin/live routing_pref_* counters not moving (one increment per
//     routing pass, slice 0) together with no hold and no upstream call.
//   - Scenarios tagged @proxy (the local proxy) and @unit (the planner seed) are not run here;
//     the @unit one has a plain Go test at the bottom of this file.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// mf1Recv is one request a station's upstream received.
type mf1Recv struct {
	body []byte
	at   time.Time
}

// mf1Req is one consumer request as a scenario words it.
type mf1Req struct {
	model     *string         // nil = no "model" key
	models    json.RawMessage // nil = no "models" key (raw, so malformed values travel verbatim)
	stream    bool
	tokens    int
	maxTokens int
	hdr       map[string]string
	extra     map[string]any
	caller    string // "" funded consumer | "anon" | "grant" | "owner" | "other"
	broker    *broker
	padTo     int // pad the body to this many bytes (the body-limit scenario)
	cancelIn  time.Duration
}

// mf1Store wraps the real store to refuse exactly one operation for exactly one scenario
// (RekeyHold, or Finalize for one node); everything else is the real store.
type mf1Store struct {
	store.Store
	refuseRekey    bool
	refuseSettleOn string
}

func (w *mf1Store) RekeyHold(user, from, to string) error {
	if w.refuseRekey {
		return errors.New("mf1: store refuses RekeyHold")
	}
	return w.Store.RekeyHold(user, from, to)
}

func (w *mf1Store) Finalize(user, node string, held, cost, ownerShare float64, rec protocol.UsageReceipt) (float64, error) {
	if w.refuseSettleOn != "" && node == w.refuseSettleOn {
		return 0, errors.New("mf1: ledger rejects the settle")
	}
	return w.Store.Finalize(user, node, held, cost, ownerShare, rec)
}

type mf1State struct {
	*rpState

	mmu         sync.Mutex
	recv        map[string][]mf1Recv
	arrivals    []string
	inflight    int
	maxInflight int
	smodels     map[string][]string // station name -> offered models, declaration order
	decl        []string            // stations in declaration order

	// request / caller shaping
	caller       string
	ownerCaller  *fstation
	balanceSet   bool
	req          mf1Req // the last request sent (for re-sends)
	reqBody      []byte
	five         []string
	lateResult   string // station whose voided result is re-posted as a success after the relay
	instance2    bool
	confOnly     bool
	relayStartAt time.Time
	relayEndAt   time.Time
	codes2       []int

	// snapshots taken when a When starts
	before    map[string]int
	admin0    map[string]any
	logMark1  int
	holds0    int
	releases0 int
	spends0   int
	holdAmt0  float64
	balance0  float64
	grantTok0 int64
	holdFor   time.Duration // how long the slow station of a scaled-clock scenario holds

	// scenario-specific handles
	wrapped    *mf1Store
	modSrv     *httptest.Server
	scrn       *screener
	lockUntil  time.Time
	owner      *fstation // grant owner "O"
	towerRecv  map[string][][]byte
	towerMu    sync.Mutex
	genCode    int
	genBody    []byte
	fellOverID string
	health0    map[string]float64
	savedEnv   map[string]*string
	forgeStop  chan struct{}
	forgeWG    sync.WaitGroup
}

// --- lifecycle -------------------------------------------------------------------

func (s *mf1State) resetMF1() error {
	s.teardownMF1()
	if err := s.rpState.resetPins(); err != nil {
		return err
	}
	s.recv, s.arrivals, s.inflight, s.maxInflight = map[string][]mf1Recv{}, nil, 0, 0
	s.smodels, s.decl = map[string][]string{}, nil
	s.caller, s.ownerCaller, s.balanceSet = "", nil, false
	s.req, s.reqBody, s.five, s.lateResult, s.instance2, s.confOnly = mf1Req{}, nil, nil, "", false, false
	s.codes2 = nil
	s.before, s.admin0, s.logMark1, s.holds0, s.balance0, s.grantTok0 = map[string]int{}, nil, 0, 0, 0, 0
	s.releases0, s.spends0, s.holdAmt0, s.holdFor = 0, 0, 0, 0
	s.wrapped, s.modSrv, s.scrn, s.owner = nil, nil, nil, nil
	s.lockUntil = time.Time{}
	s.towerRecv = map[string][][]byte{}
	s.genCode, s.genBody, s.fellOverID = 0, nil, ""
	s.health0 = map[string]float64{}
	s.savedEnv = map[string]*string{}
	s.forgeStop = make(chan struct{})
	s.ordered = true // this runner applies its own per-model order (applyOrder)
	s.tokens = 50
	// The price lock runs for the PRODUCTION window (the -price-lock flag default, the spec's
	// lockWin 24 h); the shared test broker constructors use a shorter one.
	s.b.lockWin = 24 * time.Hour
	return nil
}

func (s *mf1State) teardownMF1() {
	if s.forgeStop != nil {
		close(s.forgeStop)
		s.forgeWG.Wait()
		s.forgeStop = nil
	}
	if s.scrn != nil {
		s.scrn.shutdown(2 * time.Second)
		s.scrn = nil
	}
	if s.modSrv != nil {
		s.modSrv.Close()
		s.modSrv = nil
	}
	for k, v := range s.savedEnv {
		if v == nil {
			_ = os.Unsetenv(k)
		} else {
			_ = os.Setenv(k, *v)
		}
	}
	s.savedEnv = nil
	if s.rpState != nil && s.rpState.foState != nil && s.b != nil {
		s.rpState.teardownPins()
	}
}

func (s *mf1State) setenv(k, v string) {
	if _, seen := s.savedEnv[k]; !seen {
		if old, ok := os.LookupEnv(k); ok {
			s.savedEnv[k] = &old
		} else {
			s.savedEnv[k] = nil
		}
	}
	_ = os.Setenv(k, v)
}

// --- stations ----------------------------------------------------------------------

var mf1TrailingDigits = regexp.MustCompile(`\d+$`)

// mf1DefaultModel is the naming convention of the feature: "a1" serves "a", "b2" serves "b".
func mf1DefaultModel(name string) string {
	return mf1TrailingDigits.ReplaceAllString(name, "")
}

type mf1StOpt struct {
	models    []string
	in, out   float64
	priced    bool
	ctx       int
	estimated bool
	owner     *fstation
	ownerPriv ed25519.PrivateKey
}

// stn stands up (or returns) a real station offering one or more models.
func (s *mf1State) stn(name string, o mf1StOpt) *fstation {
	if st, ok := s.stations[name]; ok {
		return st
	}
	if len(o.models) == 0 {
		o.models = []string{mf1DefaultModel(name)}
	}
	if !o.priced {
		o.in, o.out = 1, 1
	}
	if o.ctx == 0 {
		o.ctx = 32768
	}
	st := s.standUp(name, stationOpts{model: o.models[0], priceIn: o.in, priceOut: o.out, ctx: o.ctx, owner: o.owner, ownerPriv: o.ownerPriv})
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	reg.Offers = nil
	for _, m := range o.models {
		reg.Offers = append(reg.Offers, protocol.ModelOffer{Model: m, PriceIn: o.in, PriceOut: o.out, Ctx: o.ctx, CtxEstimated: o.estimated})
	}
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
	s.smodels[name] = o.models
	s.decl = append(s.decl, name)
	s.behave(name, mf1Real)
	return st
}

func (s *mf1State) offers(name string, f func(o *protocol.ModelOffer)) {
	st := s.st(name)
	s.b.mu.Lock()
	reg := s.b.nodes[st.id]
	for i := range reg.Offers {
		f(&reg.Offers[i])
	}
	s.b.nodes[st.id] = reg
	s.b.mu.Unlock()
}

// applyOrder makes declaration order the pick order WITHIN each model (see the file header).
func (s *mf1State) applyOrder() {
	seen := map[string]int{}
	s.b.metricsMu.Lock()
	for _, n := range s.decl {
		st, ok := s.stations[n]
		if !ok {
			continue
		}
		m := s.smodels[n][0]
		if i := seen[m]; i > 0 {
			if _, graded := s.b.success[st.id]; !graded {
				s.b.success[st.id] = 0.5 - 0.05*float64(i-1)
			}
		}
		seen[m]++
	}
	s.b.metricsMu.Unlock()
}

type mf1Beh func(n int, w http.ResponseWriter, r *http.Request)

// behave installs an upstream script behind the recorder.
func (s *mf1State) behave(name string, beh mf1Beh) {
	st := s.st(name)
	st.set(func(n int, w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mmu.Lock()
		s.recv[name] = append(s.recv[name], mf1Recv{body: body, at: time.Now()})
		s.arrivals = append(s.arrivals, name)
		s.inflight++
		if s.inflight > s.maxInflight {
			s.maxInflight = s.inflight
		}
		s.mmu.Unlock()
		defer func() {
			s.mmu.Lock()
			s.inflight--
			s.mmu.Unlock()
		}()
		beh(n, w, r)
	})
}

func mf1Real(_ int, w http.ResponseWriter, _ *http.Request) { utRealCompletion(w) }

func mf1Status(code int, body string, hdr map[string]string) mf1Beh {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func mf1429(retryAfter string) mf1Beh {
	hdr := map[string]string{}
	if retryAfter != "" {
		hdr["Retry-After"] = retryAfter
	}
	return mf1Status(429, utDefaultBody(429), hdr)
}

const mf1CtxBody = `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 20000 tokens.","code":"context_length_exceeded"}}`

func mf1Stream(name string, chunks int, die bool) mf1Beh {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		for i := 0; i < chunks; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chunk%d from %s \"}}]}\n\n", i+1, name)
			if f != nil {
				f.Flush()
			}
		}
		if die {
			panic(http.ErrAbortHandler) // the connection dies mid-stream
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5000,\"completion_tokens\":20}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func mf1Hold(d time.Duration, then mf1Beh) mf1Beh {
	return func(n int, w http.ResponseWriter, r *http.Request) {
		time.Sleep(d)
		then(n, w, r)
	}
}

// forging stands up a station whose loop signs a receipt for a request id of its OWN choosing
// (the harness's station loop always binds the receipt to the job it was handed).
func (s *mf1State) forging(name, model string, forgeID func(jobID string) string) *fstation {
	st := &fstation{name: name, id: name + "-" + s.nonce, model: model, priceIn: 1, priceOut: 1}
	pub, priv, _ := ed25519.GenerateKey(nil)
	st.priv, st.pubHex = priv, hex.EncodeToString(pub)
	_, st.ownerPriv, _ = ed25519.GenerateKey(nil)
	st.acct = hex.EncodeToString(st.ownerPriv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err := s.db.BindOwner(store.Owner{GitHubID: gid.Int64() + 10, Login: "op-" + name + "-" + s.nonce, Pubkey: st.acct, Email: name + "@example.com"}); err != nil {
		s.t.Fatalf("bind owner: %v", err)
	}
	st.up = httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	s.b.nodes[st.id] = protocol.NodeRegistration{NodeID: st.id, PubKey: st.pubHex,
		Offers: []protocol.ModelOffer{{Model: model, PriceIn: 1, PriceOut: 1, Ctx: 32768}}}
	s.b.lastSeen[st.id] = time.Now()
	st.tun = &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: "tok-" + st.id}
	s.b.tunnels[st.id] = st.tun
	if err := s.db.BindNode(st.id, st.acct); err != nil {
		s.t.Fatalf("bind node: %v", err)
	}
	s.stations[name] = st
	s.smodels[name] = []string{model}
	s.decl = append(s.decl, name)
	s.forgeWG.Add(1)
	stop := s.forgeStop
	go func() {
		defer s.forgeWG.Done()
		for {
			select {
			case <-stop:
				return
			case job := <-st.tun.jobs:
				st.mu.Lock()
				st.reqN++
				st.mu.Unlock()
				s.mmu.Lock()
				s.recv[name] = append(s.recv[name], mf1Recv{body: job.Body, at: time.Now()})
				s.arrivals = append(s.arrivals, name)
				s.mmu.Unlock()
				rec := protocol.UsageReceipt{RequestID: forgeID(job.ID), NodeID: st.id, User: job.User, Model: model,
					PromptTokens: 5000, CompletionTokens: 20, PriceIn: 1, PriceOut: 1, TS: time.Now().Unix(), LineageMethod: "p0-upstream-usage"}
				rec.SignNode(st.priv)
				res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte(utRealCompletionBody), Receipt: rec}
				wire, _ := json.Marshal(res)
				s.deliverRaw(st, wire)
			}
		}
	}()
	return st
}

// --- the generic station sentence ---------------------------------------------------
//
// Most Givens are one sentence naming stations and what each does:
//   "a1" at 1.00/1.00 429s, "b1" at 50.00/50.00 serves, "c1" at 1.50/1.50 serves
//   stations "a1", "a2", "a3", "a4" serve "a" and all their upstreams return 429
// stationSentence reads them clause by clause: consecutive station names share the clause that
// follows them; a station named twice is updated by its later clause.

var (
	mf1StationTok = regexp.MustCompile(`"((?:[a-z]+\d+)|mine)"`)
	mf1SepOnly    = regexp.MustCompile(`^\s*(,|and|, and)?\s*$`)
	mf1ServesRe   = regexp.MustCompile(`\bserv(?:es|e|ing)\s+(?:only\s+|both\s+)?("[^"]+"(?:(?:,\s*|\s+and\s+)"[^"]+")*)`)
	mf1QuotedRe   = regexp.MustCompile(`"([^"]+)"`)
	mf1PriceBoth  = regexp.MustCompile(`\b(?:at|priced)\s+(\d+(?:\.\d+)?)/(\d+(?:\.\d+)?)`)
	mf1PriceOut   = regexp.MustCompile(`\bat\s+(\d+(?:\.\d+)?)\s+out\b`)
	mf1PriceIn    = regexp.MustCompile(`\bat\s+(\d+(?:\.\d+)?)\s+in\b`)
	mf1CtxEst     = regexp.MustCompile(`ESTIMATED window of (\d+)|\(ctx (\d+), ESTIMATED\)`)
	mf1CtxDecl    = regexp.MustCompile(`DECLARED window of (\d+)|\(ctx (\d+) declared\)|\bdeclared (\d+)|\bwith (\d+)\b`)
	mf1HoldRe     = regexp.MustCompile(`holds the request for (\d+) s then returns (\d+)`)
	mf1ReturnsRe  = regexp.MustCompile(`\b(?:returns?|answers?)\s+(\d{3})(?:\s+with Retry-After (\d+))?`)
	mf1NNNs       = regexp.MustCompile(`\b(\d{3})s\b`)
	mf1StreamsRe  = regexp.MustCompile(`\bstreams\b`)
	mf1NoStation  = regexp.MustCompile(`\bno station serves .*$`)
)

type mf1Clause struct {
	names []string
	text  string
}

func mf1Clauses(sentence string) []mf1Clause {
	locs := mf1StationTok.FindAllStringSubmatchIndex(sentence, -1)
	var out []mf1Clause
	for i := 0; i < len(locs); {
		names := []string{sentence[locs[i][2]:locs[i][3]]}
		j := i
		for j+1 < len(locs) && mf1SepOnly.MatchString(sentence[locs[j][1]:locs[j+1][0]]) {
			j++
			names = append(names, sentence[locs[j][2]:locs[j][3]])
		}
		end := len(sentence)
		if j+1 < len(locs) {
			end = locs[j+1][0]
		}
		out = append(out, mf1Clause{names: names, text: sentence[locs[j][1]:end]})
		i = j + 1
	}
	return out
}

func mf1FirstNum(m []string) int {
	for _, g := range m[1:] {
		if g != "" {
			n, _ := strconv.Atoi(g)
			return n
		}
	}
	return 0
}

// stationSentence stands up / updates every station a sentence names.
func (s *mf1State) stationSentence(sentence string) error {
	for _, c := range mf1Clauses(sentence) {
		var o mf1StOpt
		c.text = mf1NoStation.ReplaceAllString(c.text, "") // "... and no station serves "a"" names no station
		if m := mf1ServesRe.FindStringSubmatch(c.text); m != nil {
			for _, q := range mf1QuotedRe.FindAllStringSubmatch(m[1], -1) {
				o.models = append(o.models, q[1])
			}
		}
		if m := mf1PriceBoth.FindStringSubmatch(c.text); m != nil {
			o.in, _ = strconv.ParseFloat(m[1], 64)
			o.out, _ = strconv.ParseFloat(m[2], 64)
			o.priced = true
		} else if m := mf1PriceOut.FindStringSubmatch(c.text); m != nil {
			o.in = 1
			o.out, _ = strconv.ParseFloat(m[1], 64)
			o.priced = true
		} else if m := mf1PriceIn.FindStringSubmatch(c.text); m != nil {
			o.in, _ = strconv.ParseFloat(m[1], 64)
			o.out = 1
			o.priced = true
		}
		if m := mf1CtxEst.FindStringSubmatch(c.text); m != nil {
			o.ctx, o.estimated = mf1FirstNum(m), true
		} else if m := mf1CtxDecl.FindStringSubmatch(c.text); m != nil {
			o.ctx = mf1FirstNum(m)
		} else if strings.Contains(c.text, "wider window") {
			o.ctx = 131072
		}
		for _, name := range c.names {
			if _, exists := s.stations[name]; !exists {
				if len(o.models) == 0 {
					switch mf1DefaultModel(name) {
					case "a", "b", "c", "d", "e":
					default:
						return fmt.Errorf("fixture: station %q has no model in %q", name, sentence)
					}
				}
				s.stn(name, o)
			} else if o.priced {
				s.offers(name, func(of *protocol.ModelOffer) { of.PriceIn, of.PriceOut = o.in, o.out })
				s.st(name).priceIn, s.st(name).priceOut = o.in, o.out
			}
			if beh := mf1BehaviourOf(name, c.text); beh != nil {
				s.behave(name, beh)
			}
		}
	}
	return nil
}

// mf1BehaviourOf reads what a clause says the station's upstream does (nil = unchanged).
func mf1BehaviourOf(name, text string) mf1Beh {
	switch {
	case mf1HoldRe.MatchString(text):
		return nil // the scaled-clock scenarios install their own hold
	case strings.Contains(text, "streams two chunks then dies mid-stream"):
		return mf1Stream(name, 2, true)
	case strings.Contains(text, "streams one frame then 429s mid-stream"):
		return mf1Stream(name, 1, true)
	case mf1StreamsRe.MatchString(text):
		return mf1Stream(name, 3, false)
	case strings.Contains(text, "context-length"):
		return mf1Status(400, mf1CtxBody, nil)
	case strings.Contains(text, "returns 200 with an empty completion"):
		return mf1Status(200, `{"choices":[],"usage":{"prompt_tokens":5000,"completion_tokens":0}}`, nil)
	case strings.Contains(text, "over-claims its tokens"):
		return mf1Status(200, `{"choices":[{"message":{"role":"assistant","content":"The answer is 4."}}],"usage":{"prompt_tokens":5000,"completion_tokens":1000000}}`, nil)
	}
	if m := mf1ReturnsRe.FindStringSubmatch(text); m != nil {
		code, _ := strconv.Atoi(m[1])
		hdr := map[string]string{}
		if m[2] != "" {
			hdr["Retry-After"] = m[2]
		}
		return mf1Status(code, utDefaultBody(code), hdr)
	}
	if m := mf1NNNs.FindStringSubmatch(text); m != nil {
		code, _ := strconv.Atoi(m[1])
		return mf1Status(code, utDefaultBody(code), nil)
	}
	if strings.Contains(text, "both return 429") || strings.Contains(text, "upstreams return 429") {
		return mf1429("")
	}
	return nil
}

// --- the consumer -------------------------------------------------------------------

func (s *mf1State) consumerUser() string {
	return protocol.UserIDFromPubkey(hex.EncodeToString(s.consumerPriv.Public().(ed25519.PublicKey)))
}

// walletRowFor gives a non-funded caller the wallet row production creates on first /balance
// (the fixture rule in the file header).
func (s *mf1State) walletRowFor(pubHex string) error {
	wallet := protocol.UserIDFromPubkey(pubHex)
	if o, ok, err := s.db.OwnerByPubkey(pubHex); err == nil && ok {
		if w, ok := accountWalletForOwner(o); ok {
			wallet = w
		}
	}
	_, err := s.db.BalanceOf(wallet, s.b.seedFunds)
	return err
}

// fundOwnerCaller makes the station owner who is this scenario's CONSUMER the Background's
// "funded consumer with balance $10.00": a wallet row (the fixture rule above) holding $10, so
// a paid station ahead of the owner's own one in the list can be held for and tried.
func (s *mf1State) fundOwnerCaller() error {
	if err := s.walletRowFor(s.ownerCaller.acct); err != nil {
		return err
	}
	w := s.payerWallet()
	bal, err := s.db.PeekBalance(w)
	if err != nil {
		return err
	}
	if bal < 10 {
		_, err = s.db.AddCredits(w, 10-bal)
	}
	return err
}

func (s *mf1State) payerWallet() string {
	switch s.caller {
	case "anon":
		return protocol.UserIDFromPubkey(hex.EncodeToString(s.anonPriv.Public().(ed25519.PublicKey)))
	case "owner":
		pub := s.ownerCaller.acct
		if o, ok, err := s.db.OwnerByPubkey(pub); err == nil && ok {
			if w, ok := accountWalletForOwner(o); ok {
				return w
			}
		}
		return protocol.UserIDFromPubkey(pub)
	}
	return s.wallet
}

func (s *mf1State) requestBytes(q mf1Req) []byte {
	tokens := q.tokens
	if tokens == 0 {
		tokens = 50
	}
	m := map[string]any{"messages": []map[string]any{{"role": "user", "content": utPrompt(tokens)}}}
	if q.model != nil {
		m["model"] = *q.model
	}
	if q.models != nil {
		m["models"] = q.models
	}
	if q.stream {
		m["stream"] = true
	}
	if q.maxTokens > 0 {
		m["max_tokens"] = q.maxTokens
	}
	for k, v := range q.extra {
		m[k] = v
	}
	if q.padTo > 0 {
		b, _ := json.Marshal(m)
		if pad := q.padTo - len(b); pad > 0 {
			m["messages"] = []map[string]any{{"role": "user", "content": strings.Repeat("x", pad+len(utPrompt(tokens)))}}
		}
	}
	b, _ := json.Marshal(m)
	return b
}

// send fires ONE real relay and records the outcome on the harness fields.
func (s *mf1State) send(q mf1Req) error {
	if q.caller == "" {
		q.caller = s.caller
	}
	if s.confOnly {
		if q.hdr == nil {
			q.hdr = map[string]string{}
		}
		q.hdr["X-Roger-Confidential"] = "1"
	}
	body := s.requestBytes(q)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	switch q.caller {
	case "grant":
		r.Header.Set("Authorization", "Bearer "+s.grantToken)
	case "anon":
		if err := s.walletRowFor(hex.EncodeToString(s.anonPriv.Public().(ed25519.PublicKey))); err != nil {
			return err
		}
		signReq(r, s.anonPriv, body)
	case "owner":
		if err := s.fundOwnerCaller(); err != nil {
			return err
		}
		signReq(r, s.ownerCaller.ownerPriv, body)
	default:
		if !s.balanceSet {
			if err := s.ensureFunded(); err != nil {
				return err
			}
		}
		signReq(r, s.consumerPriv, body)
	}
	if s.freq != "" {
		r.Header.Set("X-Roger-Freq", s.freq)
	}
	for k, v := range q.hdr {
		r.Header.Set(k, v)
	}
	var cancel context.CancelFunc
	if q.cancelIn > 0 {
		ctx, c := context.WithCancel(r.Context())
		cancel = c
		r = r.WithContext(ctx)
		time.AfterFunc(q.cancelIn, c)
	}
	b := s.b
	if q.broker != nil {
		b = q.broker
	} else if s.instance2 && s.b2 != nil {
		b = s.b2
	}
	s.applyOrder()
	s.heartbeat()
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.relayStartAt = time.Now()
	s.relayStart = s.relayStartAt
	b.relay(w, r)
	s.relayEndAt = time.Now()
	s.relayEnd = s.relayEndAt
	if cancel != nil {
		cancel()
	}
	s.req, s.reqBody = q, body
	s.lastRec = w
	s.lastCode, s.lastBody, s.lastHdr = w.Code, w.Body.Bytes(), w.Header()
	s.codes2 = append(s.codes2, w.Code)
	return nil
}

// begin snapshots what the Thens compare against, once per When.
func (s *mf1State) begin() error {
	s.before = map[string]int{}
	for n, st := range s.stations {
		s.before[n] = st.upstreamCount()
	}
	s.mmu.Lock()
	s.recv, s.arrivals, s.maxInflight = map[string][]mf1Recv{}, nil, 0
	s.mmu.Unlock()
	s.logMark1 = len(s.logs.String())
	if err := s.readAdminLive(); err != nil {
		return err
	}
	s.admin0 = s.adminResp
	if !s.balanceSet && s.caller == "" {
		if err := s.ensureFunded(); err != nil {
			return err
		}
	}
	if s.caller == "owner" && s.ownerCaller != nil {
		if err := s.fundOwnerCaller(); err != nil { // before the snapshot below, so the deltas are the relay's
			return err
		}
	}
	h, rel, sp, amt, err := s.rows()
	if err != nil {
		return err
	}
	s.holds0, s.releases0, s.spends0, s.holdAmt0 = h, rel, sp, amt
	if bal, err := s.db.PeekBalance(s.payerWallet()); err == nil {
		s.balance0 = bal
	}
	if s.grantID != "" {
		if u, err := s.db.GrantUsageOf(s.grantID, time.Now()); err == nil {
			s.grantTok0 = u.DayTokens
		}
	}
	s.b.metricsMu.Lock()
	s.health0 = map[string]float64{}
	for n, st := range s.stations {
		if v, ok := s.b.success[st.id]; ok {
			s.health0[n] = v
		} else {
			s.health0[n] = -1
		}
	}
	s.b.metricsMu.Unlock()
	return nil
}

// --- the generic relay sentence -----------------------------------------------------

var (
	mf1ModelRe   = regexp.MustCompile(`"model": "([^"]*)"`)
	mf1ModelsRe  = regexp.MustCompile(`"models": `)
	mf1AcrossRe  = regexp.MustCompile(`across (\[[^\]]*\])`)
	mf1PinRe     = regexp.MustCompile(`X-Roger-Node "([^"]+)"`)
	mf1ExclRe    = regexp.MustCompile(`X-Roger-Exclude-Nodes "([^"]+)"`)
	mf1MaxOutRe  = regexp.MustCompile(`max_price\.completion (\d+(?:\.\d+)?)`)
	mf1MaxInRe   = regexp.MustCompile(`max_price\.prompt (\d+(?:\.\d+)?)`)
	mf1SugarTail = regexp.MustCompile(`:(free|floor|nitro)$`)
)

// requestOf reads a When sentence into a request.
func (s *mf1State) requestOf(text string) (mf1Req, error) {
	q := mf1Req{tokens: 50}
	if m := mf1ModelRe.FindStringSubmatch(text); m != nil && !strings.Contains(text, `no "model"`) {
		v := m[1]
		q.model = &v
	}
	if loc := mf1ModelsRe.FindStringIndex(text); loc != nil {
		dec := json.NewDecoder(strings.NewReader(text[loc[1]:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return q, fmt.Errorf("fixture: cannot read the models value in %q: %v", text, err)
		}
		q.models = raw
	}
	if m := mf1AcrossRe.FindStringSubmatch(text); m != nil {
		var list []string
		if err := json.Unmarshal([]byte(m[1]), &list); err != nil || len(list) == 0 {
			return q, fmt.Errorf("fixture: cannot read the across list in %q", text)
		}
		q.model = &list[0]
		if len(list) > 1 {
			q.models, _ = json.Marshal(list[1:])
		}
	}
	if strings.Contains(text, "all five") {
		if len(s.five) != 5 {
			return q, fmt.Errorf("fixture: no five-model fixture for %q", text)
		}
		q.model = &s.five[0]
		q.models, _ = json.Marshal(s.five[1:])
	}
	q.stream = strings.Contains(text, `"stream": true`)
	if strings.Contains(text, "20000") {
		q.tokens = 20000
	}
	if strings.Contains(text, "large max_tokens") {
		q.maxTokens = 100000
	}
	q.hdr = map[string]string{}
	if m := mf1PinRe.FindStringSubmatch(text); m != nil {
		q.hdr["X-Roger-Node"] = s.st(m[1]).id
	}
	if m := mf1ExclRe.FindStringSubmatch(text); m != nil {
		q.hdr["X-Roger-Exclude-Nodes"] = s.st(m[1]).id
	}
	q.extra = map[string]any{}
	provider := map[string]any{}
	maxPrice := map[string]any{}
	if m := mf1MaxOutRe.FindStringSubmatch(text); m != nil {
		f, _ := strconv.ParseFloat(m[1], 64)
		maxPrice["completion"] = f
	}
	if m := mf1MaxInRe.FindStringSubmatch(text); m != nil {
		f, _ := strconv.ParseFloat(m[1], 64)
		maxPrice["prompt"] = f
	}
	if len(maxPrice) > 0 {
		provider["max_price"] = maxPrice
	}
	if strings.Contains(text, "provider.allow_fallbacks false") {
		provider["allow_fallbacks"] = false
	}
	if strings.Contains(text, `a "provider" object`) {
		provider["ignore"] = []string{"n-nobody"}
	}
	if len(provider) > 0 {
		q.extra["provider"] = provider
	}
	if strings.Contains(text, `a "roger" object`) {
		q.extra["roger"] = map[string]any{"pref": "balanced"}
	}
	switch {
	case strings.HasPrefix(text, "the grant relays"):
		q.caller = "grant"
	case strings.HasPrefix(text, "a logged-in consumer"):
		q.caller = "funded"
	}
	return q, nil
}

// relaySentence is the When for every "... relays with ..." wording.
func (s *mf1State) relaySentence(text string) error {
	q, err := s.requestOf(text)
	if err != nil {
		return err
	}
	if err := s.begin(); err != nil {
		return err
	}
	if q.caller == "funded" {
		q.caller = "default"
	}
	if err := s.send(q); err != nil {
		return err
	}
	if s.lateResult != "" {
		s.replayLate(s.lateResult)
	}
	return nil
}

// replayLate re-posts a station's voided result as a 200 for the SAME job id, after the
// request was served elsewhere.
func (s *mf1State) replayLate(name string) {
	st := s.st(name)
	res := st.postedResults()
	if len(res) == 0 {
		return
	}
	m := res[0]
	m["status"] = 200
	m["body"] = []byte(utRealCompletionBody)
	wire, _ := json.Marshal(m)
	s.deliverRaw(st, wire)
}

// --- reads ---------------------------------------------------------------------------

// rows is foState.holdRows for whoever pays in this scenario.
func (s *mf1State) rows() (holds, releases, spends int, holdAmt float64, err error) {
	w := s.wallet
	s.wallet = s.payerWallet()
	defer func() { s.wallet = w }()
	return s.holdRows()
}

func (s *mf1State) entriesFor(name string) ([]store.Entry, error) {
	w := s.wallet
	s.wallet = s.payerWallet()
	defer func() { s.wallet = w }()
	return s.entriesOf(name)
}

func (s *mf1State) recvOf(name string) []mf1Recv {
	s.mmu.Lock()
	defer s.mmu.Unlock()
	return append([]mf1Recv(nil), s.recv[name]...)
}

func (s *mf1State) arrivalsCopy() []string {
	s.mmu.Lock()
	defer s.mmu.Unlock()
	return append([]string(nil), s.arrivals...)
}

func (s *mf1State) received(name string) int {
	return s.st(name).upstreamCount() - s.before[name]
}

func (s *mf1State) errObj() (code, msg string) {
	var d struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(s.lastBody, &d) != nil {
		return "", string(s.lastBody)
	}
	var o struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(d.Error, &o) == nil && (o.Code != "" || o.Message != "") {
		return o.Code, o.Message
	}
	var str string
	_ = json.Unmarshal(d.Error, &str)
	return "", str
}

func (s *mf1State) offerOf(name, model string) protocol.ModelOffer {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for _, o := range s.b.nodes[s.st(name).id].Offers {
		if o.Model == model || model == "" {
			return o
		}
	}
	return protocol.ModelOffer{}
}

// maxCostAt is the hold ceiling the broker computes for one (price, window) pair on the
// request that was sent: estimateMaxCost over a single-model body with the same prompt and
// max_tokens (the carriers never reach the estimate - they are stripped first).
func (s *mf1State) maxCostAt(in, out float64, ctx int, estimated bool) float64 {
	if estimated && ctx > 32768 {
		ctx = 32768
	}
	m := "m"
	q := mf1Req{model: &m, tokens: s.req.tokens, maxTokens: s.req.maxTokens}
	return estimateMaxCost(s.requestBytes(q), in, out, ctx)
}

func (s *mf1State) maxCostOf(name string) float64 {
	o := s.offerOf(name, "")
	return s.maxCostAt(o.PriceIn, o.PriceOut, o.Ctx, o.CtxEstimated)
}

func mf1Near(got, want, tol float64) bool {
	return math.Abs(got-want) <= tol*math.Max(math.Abs(want), 1e-9)
}

func (s *mf1State) counter(m map[string]any, key string) float64 {
	if routing, _ := m["routing"].(map[string]any); routing != nil {
		if v, ok := routing[key].(float64); ok {
			return v
		}
	}
	v, _ := m[key].(float64)
	return v
}

func (s *mf1State) hasCounter(m map[string]any, key string) bool {
	if routing, _ := m["routing"].(map[string]any); routing != nil {
		if _, ok := routing[key]; ok {
			return true
		}
	}
	_, ok := m[key]
	return ok
}

func (s *mf1State) quoteFor(name, model string) (priceQuote, bool) {
	suffix := "|" + s.st(name).id + "|" + model
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for k, q := range s.b.quotes {
		if strings.HasSuffix(k, suffix) {
			return q, true
		}
	}
	return priceQuote{}, false
}

func (s *mf1State) newLogs() string {
	all := s.logs.String()
	if s.logMark1 > len(all) {
		return all
	}
	return all[s.logMark1:]
}

// --- Then primitives --------------------------------------------------------------------

type mf1A func(s *mf1State) error

func mf1All(as ...mf1A) func(*mf1State) error {
	return func(s *mf1State) error {
		for _, a := range as {
			if err := a(s); err != nil {
				return err
			}
		}
		return nil
	}
}

func aCode(want int) mf1A {
	return func(s *mf1State) error {
		if s.lastCode != want {
			code, msg := s.errObj()
			return fmt.Errorf("response %d (code=%q %.200s), want %d", s.lastCode, code, msg, want)
		}
		return nil
	}
}

func aFrom(name string) mf1A {
	return func(s *mf1State) error {
		if err := aCode(200)(s); err != nil {
			return err
		}
		if p := s.lastHdr.Get("X-RogerAI-Provider"); p != s.st(name).id {
			return fmt.Errorf("X-RogerAI-Provider=%q, want %s (%s)", p, s.st(name).id, name)
		}
		if s.received(name) < 1 {
			return fmt.Errorf("%s is named as the provider but its upstream received nothing", name)
		}
		return nil
	}
}

func aHdr(k, want string) mf1A {
	return func(s *mf1State) error {
		if got := s.lastHdr.Get(k); got != want {
			return fmt.Errorf("%s=%q, want %q (status %d %.160s)", k, got, want, s.lastCode, s.lastBody)
		}
		return nil
	}
}

func aNoHdr(k string) mf1A {
	return func(s *mf1State) error {
		if got := s.lastHdr.Get(k); got != "" {
			return fmt.Errorf("%s=%q, want absent", k, got)
		}
		return nil
	}
}

func aXModel(m string) mf1A { return aHdr("X-RogerAI-Model", m) }

func aHasRA() mf1A {
	return func(s *mf1State) error {
		if s.lastHdr.Get("Retry-After") == "" {
			return fmt.Errorf("no Retry-After on the %d response (%.160s)", s.lastCode, s.lastBody)
		}
		return nil
	}
}

func aErrCode(want string) mf1A {
	return func(s *mf1State) error {
		code, msg := s.errObj()
		if code != want {
			return fmt.Errorf("error code %q, want %q (status %d, message %.200q)", code, want, s.lastCode, msg)
		}
		return nil
	}
}

func aMsgHas(subs ...string) mf1A {
	return func(s *mf1State) error {
		_, msg := s.errObj()
		for _, sub := range subs {
			if !strings.Contains(msg, sub) {
				return fmt.Errorf("message %.240q does not contain %q (status %d)", msg, sub, s.lastCode)
			}
		}
		return nil
	}
}

func aNothing(names ...string) mf1A {
	return func(s *mf1State) error {
		for _, n := range names {
			if got := s.received(n); got != 0 {
				return fmt.Errorf("%s received %d request(s), want none", n, got)
			}
		}
		return nil
	}
}

func aNoStation() mf1A {
	return func(s *mf1State) error {
		for n := range s.stations {
			if got := s.received(n); got != 0 {
				return fmt.Errorf("%s received %d request(s), want no station contacted", n, got)
			}
		}
		return nil
	}
}

func aRecvN(name string, n int) mf1A {
	return func(s *mf1State) error {
		if got := s.received(name); got != n {
			return fmt.Errorf("%s received %d request(s), want exactly %d", name, got, n)
		}
		return nil
	}
}

func (s *mf1State) lastBodyOf(name string) (map[string]json.RawMessage, error) {
	rs := s.recvOf(name)
	if len(rs) == 0 {
		return nil, fmt.Errorf("%s received no request (response was %d %.160s)", name, s.lastCode, s.lastBody)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rs[len(rs)-1].body, &m); err != nil {
		return nil, fmt.Errorf("%s received a non-JSON body: %.120s", name, rs[len(rs)-1].body)
	}
	return m, nil
}

func aBodyModel(name, model string) mf1A {
	return func(s *mf1State) error {
		m, err := s.lastBodyOf(name)
		if err != nil {
			return err
		}
		var got string
		_ = json.Unmarshal(m["model"], &got)
		if got != model {
			return fmt.Errorf("%s received body model %q, want %q", name, got, model)
		}
		return nil
	}
}

func aNoCarriers(name string) mf1A {
	return func(s *mf1State) error {
		m, err := s.lastBodyOf(name)
		if err != nil {
			return err
		}
		for _, k := range []string{"models", "provider", "roger"} {
			if _, ok := m[k]; ok {
				return fmt.Errorf("%s received the routing carrier %q", name, k)
			}
		}
		return nil
	}
}

func aNoHold() mf1A {
	return func(s *mf1State) error {
		h, _, _, _, err := s.rows()
		if err != nil {
			return err
		}
		if h != s.holds0 {
			return fmt.Errorf("%d hold row(s) were placed, want none", h-s.holds0)
		}
		return nil
	}
}

func aChargedZero() mf1A {
	return func(s *mf1State) error {
		bal, err := s.db.PeekBalance(s.payerWallet())
		if err != nil {
			return err
		}
		if !mf1Near(bal, s.balance0, 1e-9) {
			return fmt.Errorf("consumer charged: balance %.6f, was %.6f", bal, s.balance0)
		}
		return nil
	}
}

// aHoldReleased: nothing is left reserved and nothing was charged.
func aHoldReleased() mf1A {
	return func(s *mf1State) error {
		h, rel, sp, _, err := s.rows()
		if err != nil {
			return err
		}
		if h-s.holds0 != rel-s.releases0 {
			return fmt.Errorf("holds=%d releases=%d since the request: a hold is still pending or was refunded twice", h-s.holds0, rel-s.releases0)
		}
		if sp != s.spends0 {
			return fmt.Errorf("%d spend row(s) were written, want the hold returned in full", sp-s.spends0)
		}
		return aChargedZero()(s)
	}
}

func aOneHold() mf1A {
	return func(s *mf1State) error {
		h, _, _, _, err := s.rows()
		if err != nil {
			return err
		}
		if h-s.holds0 != 1 {
			return fmt.Errorf("%d hold row(s) placed, want exactly 1 (status %d %.160s)", h-s.holds0, s.lastCode, s.lastBody)
		}
		return nil
	}
}

func (s *mf1State) heldAmount() (float64, error) {
	_, _, _, amt, err := s.rows()
	return amt - s.holdAmt0, err
}

// aHoldAtLeast: exactly one hold, at least the max cost at in/out on a 32768 window.
func aHoldAtLeast(in, out float64) mf1A {
	return func(s *mf1State) error {
		if err := aOneHold()(s); err != nil {
			return err
		}
		amt, err := s.heldAmount()
		if err != nil {
			return err
		}
		if want := s.maxCostAt(in, out, 32768, false); amt < want*0.98 {
			return fmt.Errorf("hold %.6f < max cost at %.2f/%.2f %.6f", amt, in, out, want)
		}
		return nil
	}
}

// aHoldSized: exactly one hold, sized (within 3%) for in/out on the given window.
func aHoldSized(in, out float64, ctx int) mf1A {
	return func(s *mf1State) error {
		if err := aOneHold()(s); err != nil {
			return err
		}
		amt, err := s.heldAmount()
		if err != nil {
			return err
		}
		if want := s.maxCostAt(in, out, ctx, false); !mf1Near(amt, want, 0.03) {
			return fmt.Errorf("hold %.6f, want the max cost at %.2f/%.2f on a %d window = %.6f", amt, in, out, ctx, want)
		}
		return nil
	}
}

func aHoldEquals(name string) mf1A {
	return func(s *mf1State) error {
		if err := aOneHold()(s); err != nil {
			return err
		}
		amt, err := s.heldAmount()
		if err != nil {
			return err
		}
		if want := s.maxCostOf(name); !mf1Near(amt, want, 0.03) {
			return fmt.Errorf("hold %.6f, want %s's own max cost %.6f", amt, name, want)
		}
		return nil
	}
}

func (s *mf1State) voidReason(name string) (string, protocol.UsageReceipt, error) {
	es, err := s.entriesFor(name)
	if err != nil {
		return "", protocol.UsageReceipt{}, err
	}
	if len(es) == 0 {
		return "", protocol.UsageReceipt{}, fmt.Errorf("no receipt for %s (response %d %.160s)", name, s.lastCode, s.lastBody)
	}
	rec, keys, err := s.storedReceipt(es[0].RequestID)
	if err != nil {
		return "", rec, err
	}
	vr, _ := keys["void_reason"].(string)
	return vr, rec, nil
}

func aVoided(name, reason string) mf1A {
	return func(s *mf1State) error {
		vr, _, err := s.voidReason(name)
		if err != nil {
			return err
		}
		if vr == "" {
			return fmt.Errorf("%s's receipt is not voided", name)
		}
		if reason != "" && vr != reason {
			return fmt.Errorf("%s's void_reason %q, want %q", name, vr, reason)
		}
		return nil
	}
}

func aSettled(name string) mf1A {
	return func(s *mf1State) error {
		es, err := s.entriesFor(name)
		if err != nil {
			return err
		}
		if len(es) != 1 || es[0].Cost <= 0 {
			return fmt.Errorf("%s has no settled receipt: %+v (response %d %.160s)", name, es, s.lastCode, s.lastBody)
		}
		return nil
	}
}

func (s *mf1State) costOf(name string) (float64, error) {
	es, err := s.entriesFor(name)
	if err != nil {
		return 0, err
	}
	if len(es) != 1 {
		return 0, fmt.Errorf("%s has %d receipts, want exactly 1 (response %d %.160s)", name, len(es), s.lastCode, s.lastBody)
	}
	return es[0].Cost, nil
}

func aBalanceMinus(name string) mf1A {
	return func(s *mf1State) error {
		cost, err := s.costOf(name)
		if err != nil {
			return err
		}
		if cost <= 0 {
			return fmt.Errorf("%s's attempt settled at %.6f, want a real charge", name, cost)
		}
		bal, err := s.db.PeekBalance(s.payerWallet())
		if err != nil {
			return err
		}
		if !mf1Near(bal, s.balance0-cost, 1e-6) {
			return fmt.Errorf("balance %.6f, want start %.6f minus %s's cost %.6f", bal, s.balance0, name, cost)
		}
		return nil
	}
}

func aLedger(holds, releases, spends int) mf1A {
	return func(s *mf1State) error {
		h, rel, sp, _, err := s.rows()
		if err != nil {
			return err
		}
		if h-s.holds0 != holds || rel-s.releases0 != releases || sp-s.spends0 != spends {
			return fmt.Errorf("ledger since the request: holds=%d releases=%d spends=%d, want %d/%d/%d (status %d %.160s)",
				h-s.holds0, rel-s.releases0, sp-s.spends0, holds, releases, spends, s.lastCode, s.lastBody)
		}
		return nil
	}
}

func aNoStrike(name string) mf1A {
	return func(s *mf1State) error { return s.ownerNoStrikes(name) }
}

func aStruck(name string) mf1A {
	return func(s *mf1State) error {
		st, err := s.strikesOf(name)
		if err != nil {
			return err
		}
		if len(st) == 0 {
			return fmt.Errorf("%s's owner has no strike", name)
		}
		return nil
	}
}

func (s *mf1State) earns(name string) ([]store.LedgerRow, error) {
	return s.db.LedgerOf(s.st(name).acct, []string{store.KindEarn}, 100)
}

func aNoEarn(name string) mf1A {
	return func(s *mf1State) error {
		rows, err := s.earns(name)
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			return fmt.Errorf("%s's owner has %d earn row(s), want none", name, len(rows))
		}
		return nil
	}
}

func aOneEarn(name string, share float64) mf1A {
	return func(s *mf1State) error {
		rows, err := s.earns(name)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("%s's owner has %d earn row(s), want exactly 1 (response %d %.160s)", name, len(rows), s.lastCode, s.lastBody)
		}
		if share > 0 {
			cost, err := s.costOf(name)
			if err != nil {
				return err
			}
			if !mf1Near(rows[0].Amount, cost*share, 1e-6) {
				return fmt.Errorf("%s's earn %.6f, want %.0f%% of its cost %.6f", name, rows[0].Amount, share*100, cost)
			}
		}
		return nil
	}
}

func aArrivals(want ...string) mf1A {
	return func(s *mf1State) error {
		got := s.arrivalsCopy()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			return fmt.Errorf("upstream attempts %v, want %v (response %d %.160s)", got, want, s.lastCode, s.lastBody)
		}
		return nil
	}
}

func aNoLock(name, model string) mf1A {
	return func(s *mf1State) error {
		if q, ok := s.quoteFor(name, model); ok {
			return fmt.Errorf("a price lock exists for (%s, %s) until %s", name, model, q.until.Format(time.RFC3339))
		}
		return nil
	}
}

func aReceiptHdr(name, model string) mf1A {
	return func(s *mf1State) error {
		enc := s.lastHdr.Get("X-RogerAI-Receipt")
		if enc == "" {
			return fmt.Errorf("no X-RogerAI-Receipt on the %d response (%.160s)", s.lastCode, s.lastBody)
		}
		rec, err := protocol.DecodeReceipt(enc)
		if err != nil {
			return err
		}
		if rec.NodeID != s.st(name).id || rec.Model != model {
			return fmt.Errorf("receipt names node %q model %q, want %s / %q", rec.NodeID, rec.Model, s.st(name).id, model)
		}
		return nil
	}
}

func aBilledAt(in, out float64) mf1A {
	return func(s *mf1State) error {
		enc := s.lastHdr.Get("X-RogerAI-Receipt")
		if enc == "" {
			return fmt.Errorf("no X-RogerAI-Receipt on the %d response (%.160s)", s.lastCode, s.lastBody)
		}
		rec, err := protocol.DecodeReceipt(enc)
		if err != nil {
			return err
		}
		if !mf1Near(rec.PriceIn, in, 1e-9) || !mf1Near(rec.PriceOut, out, 1e-9) {
			return fmt.Errorf("served pair billed at %.4f/%.4f, want %.4f/%.4f", rec.PriceIn, rec.PriceOut, in, out)
		}
		return nil
	}
}

func aCounters(failovers, fallbacks int) mf1A {
	return func(s *mf1State) error {
		if err := s.readAdminLive(); err != nil {
			return err
		}
		if !s.hasCounter(s.adminResp, "model_fallbacks") && fallbacks != 0 {
			return fmt.Errorf("/admin/live carries no model_fallbacks counter")
		}
		gotF := int(s.counter(s.adminResp, "relay_failovers") - s.counter(s.admin0, "relay_failovers"))
		gotM := int(s.counter(s.adminResp, "model_fallbacks") - s.counter(s.admin0, "model_fallbacks"))
		if gotF != failovers || gotM != fallbacks {
			return fmt.Errorf("relay_failovers +%d model_fallbacks +%d, want +%d / +%d (response %d %.120s)", gotF, gotM, failovers, fallbacks, s.lastCode, s.lastBody)
		}
		return nil
	}
}

func aNoPickRan() mf1A {
	return func(s *mf1State) error {
		if err := s.readAdminLive(); err != nil {
			return err
		}
		for _, p := range []string{"cheap", "balanced", "fast", "reliable"} {
			k := "routing_pref_" + p
			if d := s.counter(s.adminResp, k) - s.counter(s.admin0, k); d != 0 {
				return fmt.Errorf("a routing pass ran (%s +%v) for a request that must be refused before any pick", k, d)
			}
		}
		return nil
	}
}

func aSSEOnly(name string, never ...string) mf1A {
	return func(s *mf1State) error {
		if err := aCode(200)(s); err != nil {
			return err
		}
		if !bytes.Contains(s.lastBody, []byte("from "+name)) {
			return fmt.Errorf("the stream lacks %s's chunks: %.300s", name, s.lastBody)
		}
		for _, n := range never {
			if bytes.Contains(s.lastBody, []byte("from "+n)) {
				return fmt.Errorf("the stream carries %s's chunks: %.300s", n, s.lastBody)
			}
		}
		if bytes.Contains(s.lastBody, []byte("rate limit")) || bytes.Contains(s.lastBody, []byte("context length")) {
			return fmt.Errorf("the stream carries a failed attempt's error bytes: %.300s", s.lastBody)
		}
		return nil
	}
}

func aBodyIsCompletion() mf1A {
	return func(s *mf1State) error {
		return utCompletionAsBilled(s.lastBody, utRealCompletionBody, s.lastHdr)
	}
}

// effectiveList observes the effective model list black-box (see the file header).
func aEffectiveList(want ...string) mf1A {
	return func(s *mf1State) error {
		if s.lastCode == http.StatusBadRequest {
			code, msg := s.errObj()
			return fmt.Errorf("the request was refused: 400 %s %.200s", code, msg)
		}
		for _, m := range want {
			served := false
			for _, ms := range s.smodels {
				for _, x := range ms {
					served = served || x == m
				}
			}
			if !served {
				s.stn(m+"9", mf1StOpt{models: []string{m}})
			}
		}
		for i, m := range want {
			for n, ms := range s.smodels {
				for _, x := range ms {
					if i > 0 && x == want[i-1] {
						s.behave(n, mf1429(""))
					}
				}
			}
			q := s.req
			if err := s.send(q); err != nil {
				return err
			}
			if s.lastCode != 200 {
				code, msg := s.errObj()
				return fmt.Errorf("with every model before %q failing, the response is %d %s %.160s, want 200 served as %q (effective list %v)", m, s.lastCode, code, msg, m, want)
			}
			if got := s.lastHdr.Get("X-RogerAI-Model"); got != m {
				return fmt.Errorf("position %d of the effective list served X-RogerAI-Model %q, want %q (list %v)", i, got, m, want)
			}
		}
		return nil
	}
}

// --- Given: fixtures the station sentence cannot say ---------------------------------

func (s *mf1State) wrap() *mf1Store {
	if s.wrapped == nil {
		s.wrapped = &mf1Store{Store: s.b.db}
		s.b.db = s.wrapped
	}
	return s.wrapped
}

func (s *mf1State) scaleDeadline(wait time.Duration) {
	nonStreamRelayWait = wait
	relayMinAttemptBudget = wait / 9 // the production 10 s of 90 s
}

func (s *mf1State) mkGrant(owner *fstation, g store.Grant) error {
	secret := "rog-grant_" + s.nonce
	sum := sha256.Sum256([]byte(secret))
	s.grantID, s.grantToken = "grant_"+s.nonce, secret
	g.ID, g.SecretHash, g.Owner, g.CreatedAt = s.grantID, hex.EncodeToString(sum[:]), owner.acct, time.Now().Unix()
	if g.Label == "" {
		g.Label = "mf1"
	}
	s.owner = owner
	s.caller = "grant"
	if err := s.db.CreateGrant(g); err != nil {
		return err
	}
	// The grant's own wallet ("g_<id>") gets a row the way the file header's fixture rule gives
	// every $0 payer one: on the Postgres store a free grant's settle finds no row otherwise
	// (the open free-settle finding), and this runner must read the same on both stores.
	return rs1GrantWalletRow(s.db, g)
}

func (s *mf1State) mkBand(name string, models []string) error {
	st := s.st(name)
	s.b.private[st.id] = true
	code := "147.520 MHz · ABCD-" + strings.ToUpper(s.nonce[:4])
	s.freq = code
	return s.db.CreateBand(store.Band{ID: "band_" + s.nonce, CodeHash: protocol.BandCodeHash(code), CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: st.acct, NodeID: st.id, Models: models, CreatedAt: time.Now().Unix()})
}

func (s *mf1State) moderationServer(flagged bool) {
	s.modSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if flagged {
			_, _ = w.Write([]byte(`{"results":[{"flagged":true,"categories":{"S1":true}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"flagged":false}]}`))
	}))
}

// tower stands up a live sealed fabric (rpState.standUpTower's recipe) whose upstream RECORDS
// the bodies it receives, so "the Tower received body model b" is read off the wire.
func (s *mf1State) tower(name, model string, priceOut float64) error {
	if err := s.ensureTower(); err != nil {
		return err
	}
	t, b, srv := s.t, s.b, s.towerSrv
	tw := &rpTower{name: name}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := io.ReadAll(r.Body)
		s.towerMu.Lock()
		s.towerRecv[name] = append(s.towerRecv[name], reqBody)
		s.towerMu.Unlock()
		if bytes.Contains(reqBody, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"streamed\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong from `+name+`"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}))
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
		_ = agent.ServeTower(ctx, agent.Config{NodeID: shareNodeID, Broker: srv.URL, Model: model, Modality: "chat",
			PriceIn: 0, PriceOut: 0, Upstream: upstream.URL, Parallel: 1}, nodeOp.priv, t.TempDir(), io.Discard, nil)
	}()
	deadline := time.Now().Add(10 * time.Second)
	var stationID string
	for {
		ats, aerr := b.tower.stations.ByTower(lt.id)
		if aerr == nil && len(ats) > 0 {
			hubServer.RegisterNode(ats[0].StationID, hubAuthOf(t, ats[0]))
			stationID = ats[0].StationID
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tower %s: the share node never attached", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if priceOut > 0 {
		routableEdgePriced(t, b, lt.id, stationID, model, liveEndpointOf(t, b, lt.id), int64(priceOut*1e6), int64(priceOut*1e6))
	}
	rows, err := b.tower.routable.ByTower(lt.id, time.Now())
	if err != nil || len(rows) == 0 {
		return fmt.Errorf("tower %s: no routable row (%v)", name, err)
	}
	tw.nodeID = rows[0].NodeID
	s.towers[name] = tw
	return nil
}

func (s *mf1State) towerGot() [][]byte {
	s.towerMu.Lock()
	defer s.towerMu.Unlock()
	var out [][]byte
	for _, bs := range s.towerRecv {
		out = append(out, bs...)
	}
	return out
}

type mf1Step struct {
	text string
	fn   func(*mf1State) error
}

// gen is "read the stations out of the sentence".
func gen(text string) mf1Step { return mf1Step{text: text} }

// genThen is the sentence plus one thing it cannot say.
func genThen(text string, extra func(*mf1State) error) mf1Step {
	return mf1Step{text: text, fn: func(s *mf1State) error {
		if err := s.stationSentence(text); err != nil {
			return err
		}
		return extra(s)
	}}
}

func mf1Givens() []mf1Step {
	return []mf1Step{
		// --- Background ---
		{`a broker with a real store and a shared store`, func(*mf1State) error { return nil }},
		{`relay failover is on with ROGERAI_RELAY_ATTEMPTS "3"`, func(s *mf1State) error {
			s.setenv("ROGERAI_RELAY_FAILOVER", "1")
			s.setenv("ROGERAI_RELAY_ATTEMPTS", "3")
			return nil
		}},
		{`scripted upstreams behind real stations`, func(*mf1State) error { return nil }},
		{`a funded consumer with balance $10.00 unless a scenario says otherwise`, func(*mf1State) error { return nil }},

		// --- knobs ---
		{`ROGERAI_RELAY_ATTEMPTS is "1"`, func(s *mf1State) error { s.setenv("ROGERAI_RELAY_ATTEMPTS", "1"); return nil }},
		{`ROGERAI_RELAY_FAILOVER is "0"`, func(s *mf1State) error { s.setenv("ROGERAI_RELAY_FAILOVER", "0"); return nil }},
		{`the fee rate is 30%`, func(s *mf1State) error { s.b.feeRate = 0.30; return nil }},

		// --- plain station sentences ---
		gen(`"a1" 429s and "b1" 429s`),
		gen(`"a1" 429s and "b1" serves`),
		gen(`"a1" 429s and "b1" serves "b"`),
		gen(`"a1" 429s and "b1" serves under load`),
		gen(`"a1" 429s, "a2" 500s, "b1" serves`),
		gen(`"a1" 429s, "b1" 500s, "c1" serves`),
		gen(`"a1" and "a2" serve "a" and "a1" 429s`),
		gen(`"a1" and "a2" serve "a"; "a1" returns 429 and "a2" returns a completion`),
		gen(`"a1" at 1.00/1.00 429s and "b1" at 2.00/2.00 serves`),
		gen(`"a1" at 1.00/1.00 429s, "b1" at 50.00/50.00 serves, "c1" at 1.50/1.50 serves`),
		gen(`"a1" at 1.00/1.00 serves; "b1" at 9.00/9.00`),
		gen(`"a1" at 2.00/2.00 429s, "a2" at 1.00/1.00 500s, "b1" at 4.00/4.00 serves`),
		gen(`"a1" at 3.00/3.00 429s and "b1" at 1.00/1.00 serves but over-claims its tokens`),
		gen(`"a1" at 5.00/5.00 and "b1" at 0.10/0.10 serve`),
		gen(`"a1" at 5.00/5.00 and "b1" at 4.00/4.00 serve`),
		gen(`"a1" returns 200 with an empty completion, "b1" serves`),
		gen(`"a1" returns 400 with a context-length body and "b1" streams`),
		gen(`"a1" returns 429 and "b1" returns 500`),
		gen(`"a1" returns 429 and "b1" returns a completion`),
		gen(`"a1" returns 429 and "b1" serves "b"`),
		gen(`"a1" returns 429 and "b1" streams`),
		gen(`"a1" returns 429 and "b1" streams a completion`),
		gen(`"a1" returns 429 with Retry-After 30 and "b1" serves`),
		gen(`"a1" returns 429 with Retry-After 7 and "b1" returns 503 with Retry-After 20`),
		gen(`"a1" returns 429, "a2" returns 500, "b1" returns a completion`),
		gen(`"a1" returns 500 and "b1" serves`),
		gen(`"a1" returns a context-length 400 and "b1" serves`),
		gen(`"a1" returns a context-length 400 to a 20-token prompt; "b1" serves`),
		gen(`"a1" returns a context-length 400 to every request including canaries; "b1" serves`),
		gen(`"a1" serves "a"`),
		gen(`"a1" serves "a" (ctx 131072, ESTIMATED) at 1.00/1.00 and 429s; "b1" serves "b" (ctx 8192 declared) at 1.00/1.00`),
		gen(`"a1" serves "a" and "b1" serves "b"`),
		gen(`"a1" serves "a" and its upstream returns 400 with a context-length error body`),
		gen(`"a1" serves "a" and returns 429 with Retry-After 7; "b1" serves "b" and returns 503`),
		gen(`"a1" serves "a" and returns 429; "b1" serves "b"`),
		gen(`"a1" serves "a" at 1.00/1.00 and "b1" serves "b" at 2.00/2.00`),
		gen(`"a1" serves "a" at 1.00/1.00 and "f1" serves "b" at 0/0`),
		gen(`"a1" serves "a" at 1.00/1.00 and returns 429; "b1" serves "b" at 3.00/3.00 and serves`),
		gen(`"a1" serves "a" at 12.00 out and "b1" serves "b" at 9.00 out`),
		gen(`"a1" serves "a" at 5.00 in and "b1" serves "b" at 0.10 in`),
		gen(`"a1" serves "a" at 5.00 out and "b1" serves "b" at 0.50 out`),
		gen(`"a1" serves "a" at 5.00 out and "b1" serves "b" at 6.00 out`),
		gen(`"a1" serves "a" declared 8192 and "b1" serves "b" declared 16384`),
		gen(`"a1" serves "a" declared 8192 and no station serves "b"`),
		gen(`"a1" serves "a" with a DECLARED window of 8192 and "b1" serves "b" with 131072`),
		gen(`"a1" serves "a" with an ESTIMATED window of 8192 and "b1" serves "b"`),
		gen(`"a1" streams one frame then 429s mid-stream, "b1" streams`),
		gen(`"a1" streams two chunks then dies mid-stream, "b1" is healthy`),
		gen(`"a1", "a2" serve "a" and "a1" returns 429; "b1" serves "b"`),
		gen(`"b1" is an old station serving only "b" and no station serves "a"`),
		gen(`"b1" now lists "b" at 2.00/2.00, and "a1" 429s`),
		gen(`"b1" serves "b"`),
		gen(`"b1" serves "b" and is healthy`),
		gen(`"b1" serves "b" and no station serves "a"`),
		gen(`"b1" serves "b" and returns a completion`),
		gen(`"b1" serves "b" with a wider window and returns a completion`),
		gen(`"b1" streams "b" and no station serves "a"`),
		gen(`"f1" serves "a" at 0/0 and returns 429; "p1" serves "b" at 1.00/1.00; "f2" serves "c" at 0/0`),
		gen(`"p1" serves "a" at 1.00/1.00 and returns 429; "f1" serves "b" at 0/0`),
		gen(`"s1" serves "a" and "b" and returns 429 for "a"; "b2" serves "b"`),
		gen(`"s1" serves "a" and "b"; "b2" serves "b"`),
		gen(`"s1" serves only "b"; "a1" serves "a"`),
		gen(`"s1"'s upstream returns 429`),
		gen(`no station serves "a" and "b1" serves "b"`),
		gen(`no station serves "a" and station "b1" serves "b"`),
		gen(`no station serves "a", "b" or "c"`),
		gen(`station "b1" serves "b" and returns a completion`),
		gen(`station "b2" serves "b" and returns a completion`),
		gen(`station "s1" serves "Qwen3-32B"`),
		gen(`station "s1" serves "a"`),
		gen(`station "s1" serves "a" and "b" and returns a completion`),
		gen(`station "s1" serves "a" and its upstream returns 429`),
		gen(`station "s1" serves "a" priced 1.00/1.00 and station "s2" serves "b" priced 1.00/1.00`),
		gen(`station "s1" serves "b" priced 0/0 right now`),
		gen(`station "s1" serves both "a" and "b" and its upstream returns 429`),
		gen(`stations "a1" serves "a" and "b1" serves "b", both healthy`),
		gen(`stations "a1", "a2" serve "a" and both return 429, and "b1" serves "b"`),
		gen(`stations "a1", "a2", "a3", "a4" serve "a" and all their upstreams return 429`),
		gen(`the consumer has no lock on ("a1", "a") and "a1" 429s, "b1" serves`),
		gen(`the consumer has no lock on ("b1", "b") and "a1" 429s`),

		// --- sentences with one thing more ---
		genThen(`"a1" 429s and "b1" serves "b" in a published free window right now`, func(s *mf1State) error {
			now := time.Now().UTC()
			s.offers("b1", func(o *protocol.ModelOffer) {
				o.Schedule = []protocol.PriceWindow{{Start: now.Add(-2 * time.Hour).Format("15:04"), End: now.Add(2 * time.Hour).Format("15:04"), Free: true}}
			})
			return nil
		}),
		genThen(`"a1" 429s and "b1" serves but the ledger rejects the settle`, func(s *mf1State) error {
			s.wrap().refuseSettleOn = s.st("b1").id
			return nil
		}),
		genThen(`"a1" 429s, "b1" serves, and the store refuses RekeyHold`, func(s *mf1State) error {
			s.wrap().refuseRekey = true
			return nil
		}),
		genThen(`"a1" is voided (429) and then posts a second, successful result for the same job id after "b1" served`, func(s *mf1State) error {
			s.behave("a1", mf1429(""))
			s.lateResult = "a1"
			return nil
		}),
		genThen(`"a1" returns 500 with body {"error":"secret-upstream-detail"} and "b1" serves`, func(s *mf1State) error {
			s.behave("a1", mf1Status(500, `{"error":"secret-upstream-detail"}`, nil))
			return nil
		}),
		genThen(`"a1" serves "a" and returns 400 {"error":"invalid tool schema"}`, func(s *mf1State) error {
			s.behave("a1", mf1Status(400, `{"error":"invalid tool schema"}`, nil))
			return nil
		}),
		genThen(`"a1" holds the request for 25 s then returns 429, and "b1" streams`, func(s *mf1State) error {
			// the 20 s commit grace and the 25 s hold, scaled: 200 ms grace, 800 ms hold
			streamCommitGrace = 200 * time.Millisecond
			s.holdFor = 800 * time.Millisecond
			s.behave("a1", mf1Hold(s.holdFor, mf1429("")))
			return nil
		}),
		genThen(`"a1" serves "a" and holds the request for 85 s then returns 500`, func(s *mf1State) error {
			s.scaleDeadline(2 * time.Second) // 85 of 90 s -> 1.9 of 2 s
			s.holdFor = 1900 * time.Millisecond
			s.behave("a1", mf1Hold(s.holdFor, mf1Status(500, utDefaultBody(500), nil)))
			return nil
		}),
		genThen(`"a1" serves "a" and never answers within nonStreamRelayWait, "b1" serves "b"`, func(s *mf1State) error {
			s.scaleDeadline(time.Second)
			s.behave("a1", mf1Hold(3*time.Second, mf1Real))
			return nil
		}),
		genThen(`"a1" serves "a" but no poller is listening on it, and "b1" serves "b"`, func(s *mf1State) error {
			st := s.st("a1")
			s.b.mu.Lock()
			s.b.tunnels[st.id] = &nodeTunnel{jobs: make(chan protocol.Job), waiters: map[string]chan protocol.JobResult{}, token: st.tun.token}
			s.b.mu.Unlock()
			return nil
		}),
		genThen(`"a1" serves "a" slowly and "b1" serves "b"`, func(s *mf1State) error {
			s.holdFor = 800 * time.Millisecond
			s.behave("a1", mf1Hold(s.holdFor, mf1Real))
			return nil
		}),
		genThen(`"b1" goes off air after the plan is built and while "a1" is answering 429`, func(s *mf1State) error {
			b1 := s.st("b1")
			s.behave("a1", func(_ int, w http.ResponseWriter, _ *http.Request) {
				s.b.mu.Lock()
				delete(s.b.tunnels, b1.id)
				delete(s.b.nodes, b1.id)
				s.b.mu.Unlock()
				w.WriteHeader(429)
				_, _ = w.Write([]byte(utDefaultBody(429)))
			})
			return nil
		}),
		genThen(`"b1" serves "b" and echoes "model": "b" in its completion body`, func(s *mf1State) error {
			s.behave("b1", mf1Status(200, `{"model":"b","choices":[{"message":{"role":"assistant","content":"The answer is 4."}}],"usage":{"prompt_tokens":5000,"completion_tokens":20}}`, nil))
			return nil
		}),
		genThen(`"b1" serves "b" and echoes "model": "something-else" in its body`, func(s *mf1State) error {
			s.behave("b1", mf1Status(200, `{"model":"something-else","choices":[{"message":{"role":"assistant","content":"The answer is 4."}}],"usage":{"prompt_tokens":5000,"completion_tokens":20}}`, nil))
			return nil
		}),
		genThen(`"b1" serves "b" but returns a receipt claiming model "a"; "a1" 429s`, func(s *mf1State) error {
			s.st("b1").model = "a" // the station loop signs its receipt with this claim
			return nil
		}),
		genThen(`the backstop sweep reclaims the consumer's hold while "a1" is serving`, func(s *mf1State) error {
			s.behave("a1", func(_ int, w http.ResponseWriter, _ *http.Request) {
				if _, err := s.db.ReleaseStaleHolds(time.Now().Add(time.Hour)); err != nil {
					panic(err)
				}
				w.WriteHeader(429)
				_, _ = w.Write([]byte(utDefaultBody(429)))
			})
			return nil
		}),

		// --- forged receipts ---
		{`"a1" 429s and "b1" returns a receipt whose request id is attempt 1's id`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			s.behave("a1", mf1429(""))
			s.forging("b1", "b", func(jobID string) string {
				if i := strings.LastIndex(jobID, "-"); i > 0 {
					if _, err := strconv.Atoi(jobID[i+1:]); err == nil {
						return jobID[:i] // "<req>-2" -> "<req>", the id attempt 1 ran under
					}
				}
				return jobID + "-other"
			})
			return nil
		}},
		{`"a1" returns a completion with a receipt that does not bind to the job`, func(s *mf1State) error {
			s.forging("a1", "a", func(jobID string) string { return "unbound-" + jobID })
			return nil
		}},

		// --- lists without station names ---
		{`stations serve "a", "b" and "c"`, func(s *mf1State) error {
			for _, m := range []string{"a", "b", "c"} {
				s.stn(m+"1", mf1StOpt{})
			}
			return nil
		}},
		{`stations serve "b" and "c"`, func(s *mf1State) error {
			for _, m := range []string{"b", "c"} {
				s.stn(m+"1", mf1StOpt{})
			}
			return nil
		}},
		{`five models each with one healthy station`, func(s *mf1State) error {
			s.five = []string{"a", "b", "c", "d", "e"}
			for _, m := range s.five {
				s.stn(m+"1", mf1StOpt{})
			}
			return nil
		}},
		{`five models each with three stations whose upstreams each take 8 s to answer 500`, func(s *mf1State) error {
			s.five = []string{"a", "b", "c", "d", "e"}
			s.scaleDeadline(2 * time.Second) // 90 s -> 2 s; 8 s -> 178 ms
			s.holdFor = 178 * time.Millisecond
			for _, m := range s.five {
				for i := 1; i <= 3; i++ {
					n := m + strconv.Itoa(i)
					s.stn(n, mf1StOpt{})
					s.behave(n, mf1Hold(s.holdFor, mf1Status(500, utDefaultBody(500), nil)))
				}
			}
			return nil
		}},

		// --- cooldowns (through the REAL path: a pinned relay answered 429) ---
		{`every station of "a" cools for 30 s and every station of "b" cools for 12 s`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			s.stn("b1", mf1StOpt{})
			s.model = "a"
			if err := s.cool("a1", 30); err != nil {
				return err
			}
			s.model = "b"
			return s.cool("b1", 12)
		}},
		{`every station of "a" is in a 429 cooldown and "b1" serves "b"`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			s.stn("b1", mf1StOpt{})
			s.model = "a"
			return s.cool("a1", 60)
		}},

		// --- confidential ---
		{`confidential "a1" 429s; "b1" is confidential; "b2" is not`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			s.behave("a1", mf1429(""))
			s.stn("b2", mf1StOpt{}) // declared first: the non-confidential station would win the pick
			s.stn("b1", mf1StOpt{})
			s.b.confidential[s.st("a1").id] = true
			s.b.confidential[s.st("b1").id] = true
			s.confOnly = true
			return nil
		}},

		// --- moderation ---
		{`moderation mode is "sync" and the prompt is one the screener rejects`, func(s *mf1State) error {
			s.moderationServer(true)
			s.b.mod = moderation{provider: "url", url: s.modSrv.URL, client: s.modSrv.Client(), csamCats: loadCSAMCategories(""), mode: modeSync}
			return nil
		}},
		{`moderation mode is "sync" and the screener is unreachable (fail-closed)`, func(s *mf1State) error {
			s.moderationServer(true)
			url := s.modSrv.URL
			s.modSrv.Close() // connection refused
			s.b.mod = moderation{provider: "url", url: url, client: &http.Client{Timeout: 2 * time.Second}, csamCats: loadCSAMCategories(""), mode: modeSync, require: true}
			s.stn("a1", mf1StOpt{})
			s.stn("b1", mf1StOpt{})
			return nil
		}},
		{`moderation mode is "async"`, func(s *mf1State) error {
			s.moderationServer(true)
			s.b.mod = moderation{provider: "url", url: s.modSrv.URL, client: s.modSrv.Client(), csamCats: loadCSAMCategories(""), mode: modeAsync}
			s.scrn = newScreener(s.b, defaultScreenerConfig())
			s.b.scr = s.scrn
			s.scrn.start(1)
			return nil
		}},

		// --- anonymous / self-use ---
		{`an anonymous (not logged in) consumer`, func(s *mf1State) error {
			// An anonymous wallet is seeded exactly as production seeds first use (free credits),
			// so the ~$0 free-offer hold can land; it is still NOT logged in and cannot spend
			// (upstream_failover's freeAndPaid does the same).
			s.caller = "anon"
			s.b.seedFunds = 0.5
			return nil
		}},
		{`an anonymous consumer and paid stations for "a" and "b"`, func(s *mf1State) error {
			s.caller = "anon"
			s.b.seedFunds = 0.5
			s.stn("a1", mf1StOpt{})
			s.stn("b1", mf1StOpt{})
			return nil
		}},
		{`the consumer owns "mine" which serves "b"; "a1" serves "a" and 429s`, func(s *mf1State) error {
			s.ownerCaller = s.stn("mine", mf1StOpt{models: []string{"b"}, in: 0.10, out: 0.30, priced: true})
			s.caller = "owner"
			s.stn("a1", mf1StOpt{})
			s.behave("a1", mf1429(""))
			return nil
		}},
		{`the consumer owns "mine" serving "b" and a billed Tower also serves "b"; "a1" 429s`, func(s *mf1State) error {
			s.ownerCaller = s.stn("mine", mf1StOpt{models: []string{"b"}, in: 0.10, out: 0.30, priced: true})
			s.stn("a1", mf1StOpt{})
			s.behave("a1", mf1429(""))
			if err := s.tower("t1", "b", 1.00); err != nil {
				return err
			}
			s.caller = "owner"
			return nil
		}},

		// --- grants ---
		{`a grant allowing models ["b"] on owner O's nodes, and O's "o1" serves "a" and "b"`, func(s *mf1State) error {
			o1 := s.stn("o1", mf1StOpt{models: []string{"a", "b"}})
			return s.mkGrant(o1, store.Grant{Free: true, Models: []string{"b"}})
		}},
		{`a grant allowing models ["z"]`, func(s *mf1State) error {
			o1 := s.stn("o1", mf1StOpt{models: []string{"z"}})
			return s.mkGrant(o1, store.Grant{Free: true, Models: []string{"z"}})
		}},
		{`a grant with no model restriction on owner O's nodes serving "a" and "b"`, func(s *mf1State) error {
			o1 := s.stn("o1", mf1StOpt{models: []string{"a", "b"}})
			return s.mkGrant(o1, store.Grant{Free: true})
		}},
		{`a grant on owner O; O's "o1" serves "a" and 429s; a public "x1" serves "b"`, func(s *mf1State) error {
			o1 := s.stn("o1", mf1StOpt{models: []string{"a"}})
			s.behave("o1", mf1429(""))
			s.stn("x1", mf1StOpt{models: []string{"b"}})
			return s.mkGrant(o1, store.Grant{Free: true})
		}},
		{`a grant with a daily token cap; O's "o1" serves "a" and 429s, O's "o2" serves "b"`, func(s *mf1State) error {
			o1 := s.stn("o1", mf1StOpt{models: []string{"a"}})
			s.behave("o1", mf1429(""))
			s.stn("o2", mf1StOpt{models: []string{"b"}, owner: o1})
			return s.mkGrant(o1, store.Grant{Free: true, DailyCap: 1_000_000})
		}},
		{`a sponsored grant with price_out 1.00; O's "o1" serves "a" at 2.00 out and O's "o2" serves "b" at 0.50 out`, func(s *mf1State) error {
			return s.mf1SponsoredGrant(false)
		}},
		{`a sponsored grant with price_out 1.00; O's "o1" serves "a" at 2.00 out and 429s, O's "o2" serves "b" at 0.50 out`, func(s *mf1State) error {
			return s.mf1SponsoredGrant(true)
		}},
		{`station "long1" serves a 256-character model id`, func(s *mf1State) error {
			s.stn("long1", mf1StOpt{models: []string{mf1LongModel(256)}})
			return nil
		}},
		{`a grant allowing models ["a"] on O's nodes; a public "x1" serves "b"`, func(s *mf1State) error {
			o1 := s.stn("o1", mf1StOpt{models: []string{"c"}}) // O is on air, but not for "a"
			s.stn("x1", mf1StOpt{models: []string{"b"}})
			return s.mkGrant(o1, store.Grant{Free: true, Models: []string{"a"}})
		}},

		// --- private bands ---
		{`private band B on "s1" allows models ["b"] and "s1" serves "a" and "b"`, func(s *mf1State) error {
			s.stn("s1", mf1StOpt{models: []string{"a", "b"}})
			return s.mkBand("s1", []string{"b"})
		}},
		{`private band B on "s1" allows models ["z"]`, func(s *mf1State) error {
			s.stn("s1", mf1StOpt{models: []string{"a", "b"}})
			return s.mkBand("s1", []string{"z"})
		}},
		{`"s1" is the only station on band B, serves "a" and 429s; a public "x1" serves "b"`, func(s *mf1State) error {
			s.stn("s1", mf1StOpt{models: []string{"a"}})
			s.behave("s1", mf1429(""))
			s.stn("x1", mf1StOpt{models: []string{"b"}})
			return s.mkBand("s1", nil)
		}},

		// --- the edge fabric ---
		{`no direct node serves "a" or "b" and an approved Tower serves "b"`, func(s *mf1State) error { return s.tower("t1", "b", 0) }},
		{`a Tower is the only server of "b" and "a1" 429s`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			s.behave("a1", mf1429(""))
			return s.tower("t1", "b", 0)
		}},
		{`a Tower serves "b" at 5.00 out and "b1" serves "b" at 0.50 out; "a1" 429s`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			s.behave("a1", mf1429(""))
			s.stn("b1", mf1StOpt{in: 0.5, out: 0.5, priced: true})
			return s.tower("t1", "b", 5.00)
		}},
		{`a Tower serves "c" (not in the list) and "a1" serves "a"`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			return s.tower("t1", "c", 0)
		}},

		// --- balances, caps, locks ---
		{`the consumer's balance is $0.0001`, func(s *mf1State) error { s.balanceSet = true; return s.fund(0.0001) }},
		{`the consumer's balance covers 1.00/1.00 max cost but not 9.00/9.00`, func(s *mf1State) error {
			s.balanceSet = true // max cost at 1.00/1.00 ~0.033, at 9.00/9.00 ~0.295
			return s.fund(0.15)
		}},
		{`the consumer's balance covers 1.50/1.50 but not 50.00/50.00`, func(s *mf1State) error {
			s.balanceSet = true // max cost at 1.50/1.50 ~0.049, at 50.00/50.00 ~1.64
			return s.fund(0.5)
		}},
		{`the consumer's monthly cap fits "a1"'s max cost but not "b1"'s`, func(s *mf1State) error {
			if err := s.ensureFunded(); err != nil {
				return err
			}
			s.req = mf1Req{tokens: 50}
			return s.db.SetMonthlyCap(s.wallet, (s.maxCostOf("a1")+s.maxCostOf("b1"))/2)
		}},
		{`the consumer's monthly cap fits "b1" but not "a1"`, func(s *mf1State) error {
			if err := s.ensureFunded(); err != nil {
				return err
			}
			s.req = mf1Req{tokens: 50}
			return s.db.SetMonthlyCap(s.wallet, (s.maxCostOf("a1")+s.maxCostOf("b1"))/2)
		}},
		{`the consumer's monthly cap fits both`, func(s *mf1State) error {
			if err := s.ensureFunded(); err != nil {
				return err
			}
			return s.db.SetMonthlyCap(s.wallet, 5)
		}},
		{`the consumer has a 24 h lock on ("b1", "b") at 1.00/1.00 from yesterday`, func(s *mf1State) error {
			b1 := s.stn("b1", mf1StOpt{})
			s.lockUntil = time.Now().Add(2 * time.Hour).Truncate(time.Second)
			s.b.mu.Lock()
			s.b.quotes[s.consumerUser()+"|"+b1.id+"|b"] = priceQuote{in: 1, out: 1, until: s.lockUntil}
			s.b.mu.Unlock()
			return nil
		}},
		{`two broker instances share a store and instance 1 locked ("b1", "b") at 1.00/1.00`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			s.behave("a1", mf1429(""))
			b1 := s.stn("b1", mf1StOpt{})
			s.b.multiInstance = true
			s.b.quotedPrice(s.consumerUser(), b1.id, "b", 1, 1, true) // instance 1 mints the shared quote
			s.b.multiInstance = false                                 // ...and keeps delivering station results to local waiters
			s.offers("b1", func(o *protocol.ModelOffer) { o.PriceIn, o.PriceOut = 2, 2 })
			b1.priceIn, b1.priceOut = 2, 2
			b2 := s.instanceB()
			// Instance 2 dispatches LOCALLY (the harness stations sit on in-process tunnels; a
			// multi-instance dispatch needs a long-polling provider), and turns multi-instance
			// on while b1 is answering - before the settle consults the price lock - so the
			// lock read on instance 2 is the production SHARED-quote read.
			s.behave("b1", func(_ int, w http.ResponseWriter, _ *http.Request) {
				b2.multiInstance = true
				utRealCompletion(w)
			})
			s.instance2 = true
			return nil
		}},

		// --- a request already made ---
		{`a request that fell over from "a" to "b"`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			s.behave("a1", mf1429(""))
			s.stn("b1", mf1StOpt{})
			if err := s.relaySentence(`a consumer relays across ["a", "b"]`); err != nil {
				return err
			}
			s.fellOverID = s.requestID()
			return nil
		}},
	}
}

// requestID is the consumer-facing request id of the last relay: attempt 1's receipt id (the
// lineage base), read from the store.
func (s *mf1State) requestID() string {
	w := s.wallet
	s.wallet = s.payerWallet()
	defer func() { s.wallet = w }()
	es, err := s.entries()
	if err != nil || len(es) == 0 {
		return ""
	}
	ids := make([]string, 0, len(es))
	for _, e := range es {
		ids = append(ids, e.RequestID)
	}
	sort.Slice(ids, func(i, j int) bool { return len(ids[i]) < len(ids[j]) })
	return ids[0]
}

// --- When ---------------------------------------------------------------------------

func (s *mf1State) readGeneration(id string, priv ed25519.PrivateKey) {
	r := httptest.NewRequest(http.MethodGet, "/generation?id="+id, nil)
	signReq(r, priv, nil)
	w := httptest.NewRecorder()
	s.b.routes().ServeHTTP(w, r)
	s.genCode, s.genBody = w.Code, w.Body.Bytes()
}

// mf1RelayWhens are the Whens the relay sentence reader handles as written.
var mf1RelayWhens = []string{
	`a band-B relay is made with "model": "a" and "models": ["b"]`,
	`a confidential-only relay is made across ["a", "b"]`,
	`a consumer relays (non-stream, 90 s deadline) with "model": "a" and "models": ["b"]`,
	`a consumer relays a prompt the broker measures at ~20000 tokens with "model": "a" and "models": ["b"]`,
	`a consumer relays a ~20000-token prompt with "model": "a" and "models": ["b"]`,
	`a consumer relays across ["a", "b", "c"]`,
	`a consumer relays across ["a", "b"]`,
	`a consumer relays across ["a", "b:floor"]`,
	`a consumer relays across all five`,
	`a consumer relays with "model": "a" (no station) and "models": ["b:free"]`,
	`a consumer relays with "model": "a" and no list`,
	`a consumer relays with "model": "a" only`,
	`a consumer relays with "model": "a", "models": ["b", "c"] and a "provider" object`,
	`a consumer relays with "model": "a", "models": ["b"] and a "roger" object`,
	`a consumer relays with "model": "x" and "models": ["qwen3-32b"]`,
	`a consumer relays with "stream": true across ["a", "b"]`,
	`a consumer relays with "stream": true, "model": "a" and "models": ["b"]`,
	`a consumer relays with X-Roger-Exclude-Nodes "s1" across ["a", "b"]`,
	`a consumer relays with X-Roger-Node "a1", "model": "a" and "models": ["b"]`,
	`a consumer relays with X-Roger-Node "s1", "model": "a" and "models": ["b"]`,
	`a consumer relays with a large max_tokens across ["a", "b"]`,
	`a consumer relays with all five models`,
	`a consumer relays with max_price.completion 1.00 across ["a", "b"]`,
	`a consumer relays with max_price.completion 1.00 and "model": "a" and "models": ["b"]`,
	`a consumer relays with max_price.prompt 1.00 across ["a", "b"]`,
	`a consumer relays with no "model" and "models": ["b", "c"]`,
	`a consumer relays with no "model" and no "models"`,
	`a consumer relays with no price cap across ["a", "b"]`,
	`a consumer relays with provider.allow_fallbacks false across ["a", "b"]`,
	`a logged-in consumer relays with "model": "a" and "models": ["b", "c"]`,
	`a logged-in consumer relays with "model": "a" and "models": ["b"]`,
	`instance 2 relays with "model": "a" (429s) and "models": ["b"]`,
	`the consumer relays with "model": "a" and "models": ["b"]`,
	`the grant relays with "model": "a" (O has no "a" on air) and "models": ["b"]`,
	`the grant relays with "model": "a" and "models": ["b", "c"]`,
	`the grant relays with "model": "a" and "models": ["b"]`,
	`the grant relays with max_price.completion 1.00 and "model": "a" and "models": ["b"]`,
}

// mf1LongModel is a model id of exactly n characters (the 256-character limit scenarios).
func mf1LongModel(n int) string { return strings.Repeat("m", n) }

// mf1SponsoredGrant is the priced-grant fixture: O's "o1" serves "a" at 2.00 out (optionally
// 429ing), O's "o2" serves "b" at 0.50 out, and the grant bills at 1.00/1.00 from the
// sponsor's unified wallet.
func (s *mf1State) mf1SponsoredGrant(o1Throttled bool) error {
	o1 := s.stn("o1", mf1StOpt{models: []string{"a"}, in: 1, out: 2, priced: true})
	if o1Throttled {
		s.behave("o1", mf1429(""))
	}
	s.stn("o2", mf1StOpt{models: []string{"b"}, in: 0.5, out: 0.5, priced: true, owner: o1})
	if o, ok, err := s.db.OwnerByPubkey(o1.acct); err == nil && ok { // the sponsor's unified wallet pays
		if w, ok := accountWalletForOwner(o); ok {
			if _, err := s.db.AddCredits(w, 10); err != nil {
				return err
			}
		}
	}
	return s.mkGrant(o1, store.Grant{PriceIn: 1.00, PriceOut: 1.00})
}

func mf1Whens() []mf1Step {
	return []mf1Step{
		{`a consumer relays with "model": "a" and "models" containing a 257-character id`, func(s *mf1State) error {
			if err := s.begin(); err != nil {
				return err
			}
			a := "a"
			raw, _ := json.Marshal([]string{mf1LongModel(257)})
			return s.send(mf1Req{model: &a, models: raw, tokens: 50})
		}},
		{`a consumer relays with a 257-character "model"`, func(s *mf1State) error {
			if err := s.begin(); err != nil {
				return err
			}
			m := mf1LongModel(257)
			return s.send(mf1Req{model: &m, tokens: 50})
		}},
		{`a consumer relays with that 256-character id as "model"`, func(s *mf1State) error {
			if err := s.begin(); err != nil {
				return err
			}
			m := mf1LongModel(256)
			return s.send(mf1Req{model: &m, tokens: 50})
		}},
		{`a consumer relays with "model": "a" and that 256-character id in "models"`, func(s *mf1State) error {
			if err := s.begin(); err != nil {
				return err
			}
			a := "a"
			raw, _ := json.Marshal([]string{mf1LongModel(256)})
			return s.send(mf1Req{model: &a, models: raw, tokens: 50})
		}},
		{`a consumer relays with a body at the limit plus a "models" array`, func(s *mf1State) error {
			if err := s.begin(); err != nil {
				return err
			}
			a := "a"
			return s.send(mf1Req{model: &a, models: json.RawMessage(`["b"]`), tokens: 50, padTo: 4<<20 + 1024})
		}},
		{`a consumer relays across ["a", "b"] and then another consumer relays for "b" alone`, func(s *mf1State) error {
			if err := s.relaySentence(`a consumer relays across ["a", "b"]`); err != nil {
				return err
			}
			b := "b"
			return s.send(mf1Req{model: &b, tokens: 50})
		}},
		{`a consumer relays across ["a", "b"] and then reads /generation?id=<request id>`, func(s *mf1State) error {
			if err := s.relaySentence(`a consumer relays across ["a", "b"]`); err != nil {
				return err
			}
			s.readGeneration(s.requestID(), s.consumerPriv)
			return nil
		}},
		{`another identity reads /generation?id=<that request id>`, func(s *mf1State) error {
			s.readGeneration(s.fellOverID, s.anonPriv)
			return nil
		}},
		{`ten consumers relay across ["a", "b"]`, func(s *mf1State) error {
			q, err := s.requestOf(`a consumer relays across ["a", "b"]`)
			if err != nil {
				return err
			}
			if err := s.begin(); err != nil {
				return err
			}
			for i := 0; i < 10; i++ {
				if err := s.send(q); err != nil {
					return err
				}
			}
			return nil
		}},
		{`the consumer disconnects during the attempt on "a1" with "models": ["b"] set`, func(s *mf1State) error {
			if err := s.begin(); err != nil {
				return err
			}
			a := "a"
			return s.send(mf1Req{model: &a, models: json.RawMessage(`["b"]`), tokens: 50, cancelIn: 200 * time.Millisecond})
		}},
		{`/admin/live is read on a single instance after a fallback`, func(s *mf1State) error {
			s.stn("a1", mf1StOpt{})
			s.behave("a1", mf1429(""))
			s.stn("b1", mf1StOpt{})
			if err := s.relaySentence(`a consumer relays across ["a", "b"]`); err != nil {
				return err
			}
			return s.readAdminLive()
		}},
	}
}

// --- Then ---------------------------------------------------------------------------

func (s *mf1State) modelsOnAir(model string) []string {
	var out []string
	for _, n := range s.decl {
		for _, m := range s.smodels[n] {
			if m == model {
				out = append(out, n)
			}
		}
	}
	return out
}

func (s *mf1State) genAttempts() ([]map[string]any, map[string]any, error) {
	if s.genCode != 200 {
		return nil, nil, fmt.Errorf("GET /generation = %d %.200s (the relay answered %d)", s.genCode, s.genBody, s.lastCode)
	}
	var rec map[string]any
	if err := json.Unmarshal(s.genBody, &rec); err != nil {
		return nil, nil, err
	}
	raw, _ := rec["attempts"].([]any)
	var out []map[string]any
	for _, a := range raw {
		if m, ok := a.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, rec, nil
}

func (s *mf1State) flags() ([]store.ModerationFlag, error) {
	if s.scrn != nil {
		s.scrn.flagWG.Wait()
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		fs, err := s.db.ModerationFlagsByPseudonym(s.b.pseudonym(s.consumerUser(), "relay"), 0, 50)
		if err != nil || len(fs) > 0 || time.Now().After(deadline) {
			return fs, err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func mf1Thens() []mf1Step {
	T := func(text string, as ...mf1A) mf1Step { return mf1Step{text: text, fn: mf1All(as...)} }
	is429RA := []mf1A{aCode(429), aHasRA()}
	return []mf1Step{
		// --- who was (not) contacted ---
		T(`"a1" is attempted first`, func(s *mf1State) error {
			if got := s.arrivalsCopy(); len(got) == 0 || got[0] != "a1" {
				return fmt.Errorf("upstream attempts %v, want a1 first (response %d %.160s)", got, s.lastCode, s.lastBody)
			}
			return nil
		}),
		T(`"a1" received nothing`, aNothing("a1")),
		T(`"a1" received nothing and no 401 was answered`, aNothing("a1"), func(s *mf1State) error {
			if s.lastCode == 401 {
				return fmt.Errorf("a 401 was answered: %.160s", s.lastBody)
			}
			return nil
		}),
		T(`"a1" received nothing and no receipt exists for "a"`, aNothing("a1"), func(s *mf1State) error {
			es, err := s.entriesFor("a1")
			if err != nil {
				return err
			}
			if len(es) != 0 {
				return fmt.Errorf("%d receipt(s) exist for a1, want none (never dispatched)", len(es))
			}
			return nil
		}),
		T(`"a2" is never tried`, aNothing("a2")),
		T(`"a4" received nothing`, aNothing("a4")),
		T(`"b1" is never tried`, aNothing("b1")),
		T(`"b1" received nothing`, aNothing("b1")),
		T(`"s2" received nothing`, aNothing("s2")),
		T(`"x1" received nothing`, aNothing("x1")),
		T(`the departed "b1" is skipped (as a departed sibling is today)`, aNothing("b1")),
		T(`no attempt on "b1" is started`, aRecvN("a1", 1), aNothing("b1"), func(s *mf1State) error {
			if code, _ := s.errObj(); code == "unsupported_routing_key" || code == "invalid_routing_value" {
				return fmt.Errorf("the request never reached a1's plan: it was refused 400 %s", code)
			}
			return nil
		}),
		T(`the failover to "b1" does not happen (headers are committed)`, aNothing("b1")),
		T(`no attempt on "b1" starts and X-RogerAI-Model is "a"`, aNothing("b1"), aXModel("a")),
		T(`"b1" received nothing and the hold is released in full`, aNothing("b1"), aHoldReleased()),
		T(`"x1" received nothing and the response is 429 with a Retry-After`, aNothing("x1"), aCode(429), aHasRA()),
		T(`"x1" received nothing and the response is 503 with error code "no_match"`, aNothing("x1"), aCode(503), aErrCode("no_match")),
		T(`"s1" received exactly one request`, aRecvN("s1", 1)),
		T(`"s1" received exactly one request (for "a") and is not re-tried for "b"`, aRecvN("s1", 1), aBodyModel("s1", "a")),
		T(`no station received anything and no hold was placed`, aNoStation(), aNoHold()),
		T(`no hold was placed and no station received anything`, aNoHold(), aNoStation()),
		T(`no station received anything and no hold row remains`, aNoStation(), aHoldReleased()),
		T(`no pick ran, no hold was placed, and no station received anything`, aNoPickRan(), aNoHold(), aNoStation()),
		T(`no dispatch into a cooling "a" station happened`, func(s *mf1State) error {
			return aNothing(s.modelsOnAir("a")...)(s)
		}),
		T(`exactly one "a" station received the request, then "b1"`, func(s *mf1State) error {
			got := s.arrivalsCopy()
			if len(got) != 2 || mf1DefaultModel(got[0]) != "a" || got[1] != "b1" {
				return fmt.Errorf("upstream attempts %v, want one \"a\" station then b1 (response %d %.160s)", got, s.lastCode, s.lastBody)
			}
			return nil
		}),
		T(`exactly three "a" stations received the request, then "b1"`, func(s *mf1State) error {
			got := s.arrivalsCopy()
			if len(got) != 4 || got[3] != "b1" {
				return fmt.Errorf("upstream attempts %v, want three \"a\" stations then b1 (response %d %.160s)", got, s.lastCode, s.lastBody)
			}
			seen := map[string]bool{}
			for _, n := range got[:3] {
				if mf1DefaultModel(n) != "a" || seen[n] {
					return fmt.Errorf("upstream attempts %v, want three DISTINCT \"a\" stations first", got)
				}
				seen[n] = true
			}
			return nil
		}),
		T(`exactly two attempts were made, "a1" then "b1"`, aArrivals("a1", "b1")),
		T(`the plan is ["f1", "p1"] and "f2" is never tried`, aArrivals("f1", "p1"), aNothing("f2")),
		T(`the response is 200 from "p1" and the hold covered "p1"`, aFrom("p1"), aHoldEquals("p1")),
		T(`the finished attempt on "a1" settles: one spend row, the hold captured for it and the remainder released`, aLedger(1, 1, 1), aSettled("a1")),
		T(`"a1"'s receipt is settled, billed on the recounted tokens`, func(s *mf1State) error {
			vr, rec, err := s.voidReason("a1")
			if err != nil {
				return err
			}
			if vr != "" {
				return fmt.Errorf("a1's receipt is voided (%s); a reply that claims tokens is usable output", vr)
			}
			// The node claimed 3 or 40 completion tokens over whitespace/empty text; the broker
			// bills min(claim, recount), so the settled count never exceeds the claim.
			if rec.CompletionTokens > 40 {
				return fmt.Errorf("settled completion tokens %d exceed the node's claim", rec.CompletionTokens)
			}
			return nil
		}),
		T(`exactly one station is in flight at any moment for this request`, func(s *mf1State) error {
			s.mmu.Lock()
			max, n := s.maxInflight, len(s.arrivals)
			s.mmu.Unlock()
			if n == 0 {
				return fmt.Errorf("no station was contacted at all (response %d %.160s)", s.lastCode, s.lastBody)
			}
			if max != 1 {
				return fmt.Errorf("%d stations were in flight at once, want exactly 1", max)
			}
			return nil
		}),

		// --- what the station saw ---
		T(`"a1" received a body with model "a" and no "models", "provider" or "roger" key`, aBodyModel("a1", "a"), aNoCarriers("a1")),
		T(`"b1" received a body with model "b"`, aBodyModel("b1", "b")),
		T(`"o1" received a request with body model "b"`, aBodyModel("o1", "b")),
		T(`"b1" received model "b" and served it; the receipt is for "b"`, aBodyModel("b1", "b"), aFrom("b1"), aReceiptHdr("b1", "b")),
		T(`a station serving "b" received the request with body model "b"`, func(s *mf1State) error {
			for _, n := range s.modelsOnAir("b") {
				if len(s.recvOf(n)) > 0 {
					return aBodyModel(n, "b")(s)
				}
			}
			return fmt.Errorf("no station serving \"b\" received the request (response %d %.160s)", s.lastCode, s.lastBody)
		}),
		T(`"b1"'s job User is the pseudonym for (user, "b1"), the messages are byte-identical to the consumer's, and max_tokens is the default output budget`, func(s *mf1State) error {
			got, err := s.lastBodyOf("b1")
			if err != nil {
				return err
			}
			var sent map[string]json.RawMessage
			_ = json.Unmarshal(s.reqBody, &sent)
			if !bytes.Equal(got["messages"], sent["messages"]) {
				return fmt.Errorf("b1 received messages %.80s, the consumer sent %.80s", got["messages"], sent["messages"])
			}
			if sent["max_tokens"] != nil {
				return fmt.Errorf("the scenario's consumer must state no max_tokens")
			}
			prompt := approxPromptTokens(s.reqBody)
			s.b.mu.Lock()
			reg := s.b.nodes[s.st("b1").id]
			s.b.mu.Unlock()
			if len(reg.Offers) == 0 {
				return fmt.Errorf("b1 has no registered offer")
			}
			if want := fmt.Sprint(expectedOutput(0, prompt-1, holdWindow(reg.Offers[0]))); string(got["max_tokens"]) != want {
				return fmt.Errorf("b1 received max_tokens %s, want the default output budget %s", got["max_tokens"], want)
			}
			_, rec, err := s.voidReason("b1")
			if err != nil {
				return err
			}
			if want := s.b.pseudonym(s.consumerUser(), s.st("b1").id); rec.User != want {
				return fmt.Errorf("b1's job user %q, want the (user, b1) pseudonym %q", rec.User, want)
			}
			return nil
		}),

		// --- 200s ---
		T(`the response is 200 from "a1"`, aFrom("a1")),
		T(`the response is 200 from "b1"`, aFrom("b1")),
		T(`the response is 200 from "b2"`, aFrom("b2")),
		T(`the response is 200 from "b1" (list resolution is not failover)`, aFrom("b1")),
		T(`the request is served by "s1"`, aFrom("s1")),
		T(`the request is served by "s1" exactly as a single-model request`, aFrom("s1"), aBodyIsCompletion(), aXModel("a"), aLedger(1, 1, 1)),
		T(`the request is served for "a" on O's node`, aFrom("o1"), aBodyModel("o1", "a"), aXModel("a")),
		T(`the response is 200 from "a1" with X-RogerAI-Model "a"`, aFrom("a1"), aXModel("a")),
		T(`the response is 200 from "a2" with X-RogerAI-Model "a"`, aFrom("a2"), aXModel("a")),
		T(`the response is 200 from "b1" with X-RogerAI-Model "b"`, aFrom("b1"), aXModel("b")),
		T(`the response is 200 from "c1" with X-RogerAI-Model "c"`, aFrom("c1"), aXModel("c")),
		T(`the response is 200 from "f1" with X-RogerAI-Model "b"`, aFrom("f1"), aXModel("b")),
		T(`the response is 200 from "s1" with X-RogerAI-Model "a"`, aFrom("s1"), aXModel("a")),
		T(`the response is 200 from "s1" with X-RogerAI-Model "b"`, aFrom("s1"), aXModel("b")),
		T(`the response is 200 from "b1" with no Retry-After`, aFrom("b1"), aNoHdr("Retry-After")),
		T(`the response is 200 from "f1" with X-RogerAI-Cost "0"`, aFrom("f1"), aHdr("X-RogerAI-Cost", "0")),
		T(`the response is 200 from "mine" with X-RogerAI-Cost "0" and the hold is returned`, aFrom("mine"), aHdr("X-RogerAI-Cost", "0"), aHoldReleased()),
		T(`the response is 200 from "mine" at $0 and the Tower received nothing`, aFrom("mine"), aHdr("X-RogerAI-Cost", "0"), func(s *mf1State) error {
			if n := len(s.towerGot()); n != 0 {
				return fmt.Errorf("the Tower received %d request(s), want none", n)
			}
			return nil
		}),
		T(`"a1" is never planned and the response is 200 from "b1"`, aNothing("a1"), aFrom("b1")),
		T(`"o1" is never planned and the response is 200 from "o2"`, aNothing("o1"), aFrom("o2")),
		T(`"b1" is tried next and the response is 200 from "b1"`, aArrivals("a1", "b1"), aFrom("b1")),
		T(`the response is 200 from "b1" and "a1"'s void_reason is "context-window"`, aFrom("b1"), aVoided("a1", "context-window")),
		T(`the response is 200 with "b1"'s body and no billing headers`, aCode(200), aBodyIsCompletion(), aNoHdr("X-RogerAI-Cost"), aNoHdr("X-RogerAI-Receipt")),
		T(`the 200 body is exactly "b1"'s completion and contains no "secret-upstream-detail"`, aFrom("b1"), aBodyIsCompletion()),
		T(`the request is accepted (five entries)`, func(s *mf1State) error {
			if s.lastCode == http.StatusBadRequest {
				code, msg := s.errObj()
				return fmt.Errorf("five entries were refused: 400 %s %.200s", code, msg)
			}
			return aErrCode("no_match")(s) // nothing serves them: accepted, then no_match
		}),
		T(`the effective model list is ["a", "b", "c"]`, aEffectiveList("a", "b", "c")),
		T(`the effective model list is ["b", "c"]`, aEffectiveList("b", "c")),
		T(`the effective model list is ["a", "b", "c", "d"] and the request is accepted`, aEffectiveList("a", "b", "c", "d")),

		// --- headers ---
		T(`X-RogerAI-Model is "a"`, aXModel("a")),
		T(`X-RogerAI-Model is "b"`, aXModel("b")),
		T(`X-RogerAI-Model is "b" (no sugar)`, aXModel("b")),
		T(`X-RogerAI-Model is "b" regardless of the body's claim`, aCode(200), aXModel("b")),
		T(`X-RogerAI-Provider is "b1" and X-RogerAI-Model is "b"`, func(s *mf1State) error { return aHdr("X-RogerAI-Provider", s.st("b1").id)(s) }, aXModel("b")),
		T(`the response headers carry X-RogerAI-Model "b" and X-RogerAI-Provider "b1"`, aXModel("b"), func(s *mf1State) error { return aHdr("X-RogerAI-Provider", s.st("b1").id)(s) }),
		T(`X-RogerAI-Cost is "0" and no receipt header is present`, aHdr("X-RogerAI-Cost", "0"), aNoHdr("X-RogerAI-Receipt")),
		T(`the 429 response has no X-RogerAI-Model and X-RogerAI-Cost "0"`, aCode(429), aNoHdr("X-RogerAI-Model"), aHdr("X-RogerAI-Cost", "0"), aRecvN("b1", 1)),
		T(`the decoded X-RogerAI-Receipt has model "b" and node "b1"`, aReceiptHdr("b1", "b")),
		T(`the body's model is "b" (the broker does not rewrite station bodies)`, aCode(200), func(s *mf1State) error {
			var d struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(s.lastBody, &d)
			if d.Model != "b" {
				return fmt.Errorf("body model %q, want \"b\" as the station wrote it", d.Model)
			}
			return nil
		}),
		T(`X-RogerAI-Price carries locked_until of the existing lock`, aFrom("b1"), func(s *mf1State) error {
			want := fmt.Sprintf("locked_until=%d", s.lockUntil.Unix())
			if got := s.lastHdr.Get("X-RogerAI-Price"); !strings.Contains(got, want) {
				return fmt.Errorf("X-RogerAI-Price=%q, want it to carry %s", got, want)
			}
			return nil
		}),
		T(`X-RogerAI-Price is "in=2.0000;out=2.0000;locked_until=<b1 lock>"`, aFrom("b1"), func(s *mf1State) error {
			q, ok := s.quoteFor("b1", "b")
			if !ok {
				return fmt.Errorf("no lock exists for (b1, b)")
			}
			want := fmt.Sprintf("in=2.0000;out=2.0000;locked_until=%d", q.until.Unix())
			return aHdr("X-RogerAI-Price", want)(s)
		}),

		// --- refusals ---
		T(`the response is 429 with a Retry-After`, is429RA...),
		T(`the response is 429 (one attempt) with a Retry-After`, append(is429RA, aArrivals("a1"))...),
		T(`the response is 429 with a Retry-After (no failover of any kind)`, append(is429RA, aArrivals("a1"))...),
		T(`the response is 429 with Retry-After and "b1" received nothing`, append(is429RA, aNothing("b1"))...),
		T(`"b" contributes no candidate (no free offer) and the response is 429 with a Retry-After`, is429RA...),
		T(`the response is 500 with no Retry-After`, aCode(500), aNoHdr("Retry-After"), aRecvN("b1", 1)),
		T(`the response is 503 with error code "no_match"`, aCode(503), aErrCode("no_match")),
		T(`the response is 503 with error code "no_match" and no hold was placed`, aCode(503), aErrCode("no_match"), aNoHold()),
		T(`the response is 503 with error code "band_cooling" and Retry-After "12"`, aCode(503), aErrCode("band_cooling"), aHdr("Retry-After", "12")),
		T(`the response is 503 with Retry-After "20" and "b1"'s body`, aCode(503), aHdr("Retry-After", "20"), func(s *mf1State) error {
			if !bytes.Equal(bytes.TrimSpace(s.lastBody), []byte(utDefaultBody(503))) {
				return fmt.Errorf("body %.200s, want b1's upstream body", s.lastBody)
			}
			return aRecvN("b1", 1)(s)
		}),
		// b1's 503 carries no Retry-After of its own, so the consumer gets the broker's default
		// hint - the existing rule for a final 429/503 (setRetryAfter: "always tells the consumer
		// when to come back") - and never the EARLIER attempt's value (a1's 7).
		T(`the response is 503 with the last upstream's Retry-After when present`, aCode(503), func(s *mf1State) error {
			want := strconv.Itoa(s.b.retryAfterHint(protocol.JobResult{}))
			if got := s.lastHdr.Get("Retry-After"); got != want {
				return fmt.Errorf("Retry-After=%q, want the default hint %s (b1 sent none; a1's 7 must not leak)", got, want)
			}
			return nil
		}),
		T(`the response is 503 "no node of this grant's owner is serving" with error code "no_match"`, aCode(503), aMsgHas("no node of this grant's owner is serving"), aErrCode("no_match")),
		T(`the response is 503 "no station on that frequency (it may be off air) - check the code"`, aCode(503), aMsgHas("no station on that frequency (it may be off air) - check the code")),
		T(`the body carries no error code that distinguishes model-denied from off-air`, func(s *mf1State) error {
			if code, _ := s.errObj(); code != "" {
				return fmt.Errorf("the band refusal carries error code %q", code)
			}
			return nil
		}),
		T(`the response is 403 with error code "grant_model_denied"`, aCode(403), aErrCode("grant_model_denied")),
		T(`the message names the denied models and no station received anything`, aMsgHas("a", "b", "c"), aErrCode("grant_model_denied"), aNoStation()),
		T(`the message names "a, b, c"`, aMsgHas("a, b, c")),
		T(`the response is 402 "insufficient balance - add funds"`, aCode(402), aMsgHas("insufficient balance - add funds")),
		T(`the response is 401 "log in to spend on paid models" and no station received anything`, aCode(401), aMsgHas("log in to spend on paid models"), aNoStation()),
		T(`no hold was placed and X-RogerAI-Cost is "0"`, aNoHold(), aHdr("X-RogerAI-Cost", "0")),
		T(`no hold was placed`, aNoHold()),
		T(`the response is 400 with error code "invalid_routing_value" and the message contains "model is required"`, aCode(400), aErrCode("invalid_routing_value"), aMsgHas("model is required")),
		T(`the response is 400 with error code "invalid_routing_value" and the message names the 256-character model id limit`, aCode(400), aErrCode("invalid_routing_value"), aMsgHas("256")),
		T(`the response is 200 from "o1", billed at the grant's 1.00/1.00, and "o2" received nothing`, aFrom("o1"), aBilledAt(1, 1), aNothing("o2")),
		T(`the response is 200 from "o2", billed at the grant's 1.00/1.00, not the station's 0.50`, aFrom("o2"), aBilledAt(1, 1)),
		T(`the response is 200 from "long1"`, aFrom("long1")),
		T(`the response is 400 "request exceeds the context window"`, aCode(400), aMsgHas("exceeds the context window")),
		T(`the response is 400 "request exceeds the context window" naming 8192`, aCode(400), aMsgHas("exceeds the context window", "8192")),
		T(`the message names the widest window across the list (16384)`, aMsgHas("16384")),
		T(`the off-air model contributed nothing to the answer (§3: skipped silently)`, func(s *mf1State) error {
			if _, msg := s.errObj(); strings.Contains(msg, "no node offers") {
				return fmt.Errorf("the off-air model shaped the answer: %.200s", msg)
			}
			return aNoStation()(s)
		}),
		T(`the response is 400 from "a1" and "b1" received nothing`, aCode(400), aRecvN("a1", 1), aNothing("b1"), func(s *mf1State) error {
			if !bytes.Contains(s.lastBody, []byte("invalid tool schema")) {
				return fmt.Errorf("body %.200s, want a1's upstream 400", s.lastBody)
			}
			return nil
		}),
		T(`the response is the existing "node busy" 503 (dispatch outcome), not a model fallback`, aCode(503), aMsgHas("node busy"), aNothing("b1")),
		T(`the response is the existing 504 "node timed out" and "b1" received nothing`, aCode(504), aMsgHas("node timed out"), aNothing("b1")),
		T(`the response is 401 "invalid request signature" and no station received anything`, aCode(401), aMsgHas("invalid request signature"), aNoStation()),
		T(`the response is the existing monthly-cap refusal for the first pick`, aCode(402), aMsgHas("monthly"), aNoStation()),
		T(`the response is "a1"'s status and body as today, "a1" is struck for the unbound receipt`, aCode(200), aBodyIsCompletion(), aStruck("a1"), aRecvN("a1", 1)),
		T(`the response is the screener's status (451 or 503) before any pick`, func(s *mf1State) error {
			if s.lastCode != 451 && s.lastCode != 503 {
				code, msg := s.errObj()
				return fmt.Errorf("response %d (%s %.200s), want the screener's 451 or 503", s.lastCode, code, msg)
			}
			if code, _ := s.errObj(); code == "no_match" {
				return fmt.Errorf("the refusal is a routing no_match, not the screener's verdict")
			}
			return aNoPickRan()(s)
		}),
		T(`the response is 503 with the moderation message, not "no_match", and no station was contacted`, aCode(503), func(s *mf1State) error {
			code, msg := s.errObj()
			if code == "no_match" || strings.Contains(msg, "no node offers") {
				return fmt.Errorf("the 503 is a routing refusal (%s %.200s), want the moderation fail-closed message", code, msg)
			}
			return aNoStation()(s)
		}),
		T(`the response is 404 with no hint that the id exists`, func(s *mf1State) error {
			if s.fellOverID == "" {
				return fmt.Errorf("the earlier request never fell over to \"b\" (no request id to look up)")
			}
			if s.genCode != 404 {
				return fmt.Errorf("GET /generation as another identity = %d %.200s, want 404", s.genCode, s.genBody)
			}
			foreign := append([]byte(nil), s.genBody...)
			s.readGeneration("0123456789abcdef", s.anonPriv) // well-formed but unknown (a malformed id is a 400 by generation_lookup.feature)
			if !bytes.Equal(foreign, s.genBody) {
				return fmt.Errorf("the 404 for a foreign id (%.120s) differs from the 404 for an unknown id (%.120s)", foreign, s.genBody)
			}
			s.readGeneration(s.fellOverID, s.consumerPriv)
			if s.genCode != 200 {
				return fmt.Errorf("the request's own identity reads /generation = %d, want 200 (the endpoint must exist for the 404 to mean anything)", s.genCode)
			}
			return nil
		}),

		// --- money ---
		T(`the hold is released in full`, aHoldReleased()),
		T(`the hold placed for "p1" is returned in full`, aLedger(1, 1, 0), aChargedZero()),
		T(`the remainder of the hold is returned`, aLedger(1, 1, 1), aBalanceMinus("b1")),
		T(`the consumer was charged 0 and no hold row remains`, aChargedZero(), aLedger(1, 1, 0)),
		T(`the consumer was charged 0 and both receipts are voided`, aChargedZero(), aVoided("a1", ""), aVoided("b1", "")),
		T(`exactly one hold row exists, at least the max cost at 3.00/3.00`, aHoldAtLeast(3, 3)),
		T(`one hold_release and one spend for "b1"'s cost exist`, aLedger(1, 1, 1), aSettled("b1")),
		T(`the consumer's balance equals start minus "b1"'s cost`, aBalanceMinus("b1")),
		T(`the single hold is sized for 4.00/4.00`, aHoldSized(4, 4, 32768)),
		T(`the hold was sized for 1.50/1.50`, aHoldSized(1.5, 1.5, 32768)),
		T(`the hold equals "a1"'s max cost and the response is 200 from "a1"`, aHoldEquals("a1"), aFrom("a1")),
		T(`the hold covers "a1" alone and the response is 200 from "a1"`, aHoldEquals("a1"), aFrom("a1")),
		T(`the hold covers 2.00/2.00 and the response is 200 from "b1" with the ordinary monthly headers`, aHoldSized(2, 2, 32768), aFrom("b1"), func(s *mf1State) error {
			if s.lastHdr.Get("X-RogerAI-Monthly-Cap") == "" || s.lastHdr.Get("X-RogerAI-Monthly-Spend") == "" {
				return fmt.Errorf("the served response lacks the ordinary X-RogerAI-Monthly-* headers: %v", s.lastHdr)
			}
			return nil
		}),
		T(`no "monthly limit reached" notice and no cap email`, func(s *mf1State) error {
			if n := s.lastHdr.Get("X-RogerAI-Monthly-Notice"); strings.Contains(n, "monthly limit reached") {
				return fmt.Errorf("the served response carries the limit-reached notice %q", n)
			}
			s.mailMu.Lock()
			defer s.mailMu.Unlock()
			if len(s.mails) != 0 {
				return fmt.Errorf("%d cap email(s) were sent for a refused CEILING", len(s.mails))
			}
			return nil
		}),
		T(`exactly one hold was placed and it covered the pricier of ("a","a1") and ("b","b1")`, aOneHold(), func(s *mf1State) error {
			amt, err := s.heldAmount()
			if err != nil {
				return err
			}
			if want := math.Max(s.maxCostOf("a1"), s.maxCostOf("b1")); amt < want*0.98 {
				return fmt.Errorf("hold %.6f < the pricier pair's max cost %.6f", amt, want)
			}
			return nil
		}),
		T(`the hold was sized with the estimated window clamped to 32768 for "a1" and 8192 for "b1"`, aOneHold(), func(s *mf1State) error {
			amt, err := s.heldAmount()
			if err != nil {
				return err
			}
			want := math.Max(s.maxCostAt(1, 1, 131072, true), s.maxCostAt(1, 1, 8192, false))
			if !mf1Near(amt, want, 0.03) {
				return fmt.Errorf("hold %.6f, want %.6f (a1's ESTIMATED 131072 clamped to 32768; b1's declared 8192)", amt, want)
			}
			return nil
		}),
		T(`the settle for "b1" is clamped to "b1"'s own ceiling`, aFrom("b1"), func(s *mf1State) error {
			cost, err := s.costOf("b1")
			if err != nil {
				return err
			}
			if ceil := s.maxCostAt(1, 1, 8192, false); cost > ceil*1.0001 {
				return fmt.Errorf("b1 settled %.6f above its own ceiling %.6f", cost, ceil)
			}
			return nil
		}),
		T(`the consumer is charged at most "b1"'s own max cost at 1.00/1.00`, aFrom("b1"), func(s *mf1State) error {
			cost, err := s.costOf("b1")
			if err != nil {
				return err
			}
			if ceil := s.maxCostAt(1, 1, 32768, false); cost <= 0 || cost > ceil*1.0001 {
				return fmt.Errorf("b1 settled %.6f, want a charge of at most its own max cost %.6f (never the 3.00/3.00 plan ceiling)", cost, ceil)
			}
			return nil
		}),
		T(`the consumer is charged 0 for "b1", no lock is created, and the hold is released in full`, aFrom("b1"), aChargedZero(), aNoLock("b1", "b"), aHoldReleased()),
		T(`exactly one earn row exists, for "b1"'s owner, at 70% of "b1"'s cost`, aOneEarn("b1", 0.70)),
		T(`no earn row exists for "a1"'s owner`, aNoEarn("a1")),
		T(`"b1"'s operator gets no earn row`, aNoEarn("b1")),
		T(`"b1"'s operator is not struck`, aNoStrike("b1")),
		T(`"a1"'s owner has no earn row and no strike`, aNoEarn("a1"), aNoStrike("a1")),
		T(`"b1"'s owner has one pending earn row`, aOneEarn("b1", 0)),
		T(`"a1"'s owner has no strike`, aNoStrike("a1")),
		T(`exactly one spend row exists and the consumer's balance dropped by exactly "b1"'s cost`, aLedger(1, 1, 1), aBalanceMinus("b1")),
		T(`the ledger holds one hold, one hold_release, one spend, and no other money rows for this request`, aLedger(1, 1, 1), aBalanceMinus("b1")),
		T(`the late result is discarded, no second spend exists, and "a1" earns nothing`, aFrom("b1"), aLedger(1, 1, 1), aNoEarn("a1"), aVoided("a1", "")),
		T(`the receipt does not bind and "b1" is struck for an unbound receipt, nothing settles, the hold is refunded`, aRecvN("b1", 1), aStruck("b1"), aLedger(1, 1, 0), aChargedZero()),
		T(`the served pair is billed at the locked 1.00/1.00`, aFrom("b1"), aBilledAt(1, 1)),
		T(`instance 2 bills the served pair at the shared 1.00/1.00 quote`, aFrom("b1"), aBilledAt(1, 1)),
		T(`a lock exists for ("b1", "b") until now + 24 h`, func(s *mf1State) error {
			q, ok := s.quoteFor("b1", "b")
			if !ok {
				return fmt.Errorf("no lock exists for (b1, b) (response %d %.160s)", s.lastCode, s.lastBody)
			}
			if d := time.Until(q.until); d < 23*time.Hour+50*time.Minute || d > 24*time.Hour+time.Minute {
				return fmt.Errorf("the lock runs for %s, want ~24 h", d)
			}
			return nil
		}),
		T(`no lock was created for ("a1", "a")`, aNoLock("a1", "a")),
		T(`no lock exists for ("a1", "a") after the request`, aRecvN("a1", 1), aNoLock("a1", "a")),
		T(`the grant's cap is debited once, for "o2"'s served tokens only`, aFrom("o2"), func(s *mf1State) error {
			u, err := s.db.GrantUsageOf(s.grantID, time.Now())
			if err != nil {
				return err
			}
			if got := u.DayTokens - s.grantTok0; got != 5020 {
				return fmt.Errorf("the grant was debited %d tokens, want 5020 (o2's served attempt only)", got)
			}
			return nil
		}),

		// --- receipts, lineage, strikes, health ---
		T(`"a1"'s voided receipt names model "a", "b1"'s names "b", "c1"'s settled receipt names "c"`, func(s *mf1State) error {
			for _, w := range []struct{ n, m string }{{"a1", "a"}, {"b1", "b"}, {"c1", "c"}} {
				vr, rec, err := s.voidReason(w.n)
				if err != nil {
					return err
				}
				if rec.Model != w.m {
					return fmt.Errorf("%s's receipt names model %q, want %q", w.n, rec.Model, w.m)
				}
				if (w.n == "c1") == (vr != "") {
					return fmt.Errorf("%s's receipt void_reason=%q (c1 must be settled, the others voided)", w.n, vr)
				}
			}
			return nil
		}),
		T(`each is chained to its station's prev_hash`, func(s *mf1State) error {
			for _, n := range []string{"a1", "b1", "c1"} {
				_, rec, err := s.voidReason(n)
				if err != nil {
					return err
				}
				head, err := s.db.ChainHead(s.st(n).id)
				if err != nil {
					return err
				}
				if head != rec.Hash() {
					return fmt.Errorf("%s chain head %s != its receipt hash %s (not chained)", n, head, rec.Hash())
				}
			}
			return nil
		}),
		T(`receipts exist for attempts 1 ("a1", voided), 2 ("a2", voided), 3 ("b1", settled)`, aVoided("a1", ""), aVoided("a2", ""), aSettled("b1")),
		T(`all three carry the consumer's request id lineage in order`, func(s *mf1State) error {
			ids := map[string]string{}
			for _, n := range []string{"a1", "a2", "b1"} {
				es, err := s.entriesFor(n)
				if err != nil {
					return err
				}
				if len(es) != 1 {
					return fmt.Errorf("%s has %d receipts, want 1", n, len(es))
				}
				ids[n] = es[0].RequestID
			}
			base := ids["a1"]
			if ids["a2"] != base+"-2" || ids["b1"] != base+"-3" {
				return fmt.Errorf("attempt ids %v, want %s, %s-2, %s-3", ids, base, base, base)
			}
			return nil
		}),
		T(`the settled row is keyed on model "b" and the recount uses "b"'s tokenizer`, aFrom("b1"), func(s *mf1State) error {
			es, err := s.entriesFor("b1")
			if err != nil {
				return err
			}
			if len(es) != 1 || es[0].Model != "b" {
				return fmt.Errorf("b1's settled row %+v, want one row keyed on model \"b\"", es)
			}
			if !strings.Contains(s.newLogs(), "model=b") {
				return fmt.Errorf("no settle/recount log line names model=b for the served attempt")
			}
			return nil
		}),
		T(`"a1"'s owner has no strike and "a1"'s health is not graded down for it`, aVoided("a1", "context-window"), aNoStrike("a1"), func(s *mf1State) error {
			s.b.metricsMu.Lock()
			v, ok := s.b.success[s.st("a1").id]
			s.b.metricsMu.Unlock()
			if ok && v < 1 && (s.health0["a1"] < 0 || v < s.health0["a1"]) {
				return fmt.Errorf("a1's success average was graded down to %.3f for a context-window 400", v)
			}
			return nil
		}),
		T(`"a1" is not cooling and its success EWMA is graded down as today`, aFrom("b1"), func(s *mf1State) error {
			s.model = "a"
			if s.isCooling("a1") {
				return fmt.Errorf("a1 is cooling after a 5xx")
			}
			s.b.metricsMu.Lock()
			v, ok := s.b.success[s.st("a1").id]
			s.b.metricsMu.Unlock()
			if !ok || v >= 1 {
				return fmt.Errorf("a1's success average is not graded down (%v, present=%v)", v, ok)
			}
			return nil
		}),
		T(`"a1" is cooling for 30 s for model "a"`, aFrom("b1"), func(s *mf1State) error {
			s.model = "a"
			// Read the cooldown's expiry itself rather than walking the clock past it: a lapsed
			// cooldown is DELETED on read (coolingUntilLocked), so advancing past the expiry and
			// back would leave a1 not cooling for the next step, which needs it cooling.
			until, cooling := s.b.coolingUntil(s.st("a1").id)
			if !cooling {
				return fmt.Errorf("a1 is not cooling after its 429")
			}
			if d := until.Sub(s.b.now()); d <= 29*time.Second || d > 30*time.Second+time.Second {
				return fmt.Errorf("a1 cools for %s, want its 30 s Retry-After", d)
			}
			return nil
		}),
		T(`the next single-model request for "a" skips "a1" while a sibling exists`, func(s *mf1State) error {
			s.stn("a2", mf1StOpt{})
			if err := s.begin(); err != nil {
				return err
			}
			a := "a"
			if err := s.send(mf1Req{model: &a, tokens: 50}); err != nil {
				return err
			}
			return mf1All(aFrom("a2"), aNothing("a1"))(s)
		}),
		T(`the second request does not dispatch into a cooling "s1" while "b2" exists`, func(s *mf1State) error {
			if len(s.codes2) != 2 || s.codes2[0] != 200 || s.codes2[1] != 200 {
				return fmt.Errorf("the two relays answered %v, want both served (the first by falling over to \"b\")", s.codes2)
			}
			return nil
		}, aRecvN("s1", 1), aBodyModel("s1", "a"), aFrom("b2")),
		T(`"b1"'s successCount, TPS and capacity evidence are updated and "a1"'s are not`, aFrom("b1"), func(s *mf1State) error {
			a1, b1 := s.st("a1").id, s.st("b1").id
			s.b.metricsMu.Lock()
			defer s.b.metricsMu.Unlock()
			if s.b.successCount[b1] < 1 {
				return fmt.Errorf("b1's successCount = %d, want >= 1", s.b.successCount[b1])
			}
			if s.b.tps[b1] <= 0 {
				return fmt.Errorf("b1 has no measured TPS after serving")
			}
			if s.b.successCount[a1] != 0 || s.b.tps[a1] != 0 {
				return fmt.Errorf("a1 (429) gained serving evidence: successCount=%d tps=%v", s.b.successCount[a1], s.b.tps[a1])
			}
			return nil
		}),
		T(`"a1" earns nothing on any of them`, func(s *mf1State) error {
			for i, c := range s.codes2 {
				if c != 200 {
					return fmt.Errorf("relay %d of 10 = %d, want every one served by the next model", i+1, c)
				}
			}
			e, err := s.db.EarningsOf(s.st("a1").id)
			if err != nil {
				return err
			}
			if e != 0 {
				return fmt.Errorf("a1 earned %.6f", e)
			}
			return aNoEarn("a1")(s)
		}),
		T(`"a1" is quarantined by the probe streak as a station that completes nothing`, func(s *mf1State) error {
			st := s.st("a1")
			for i := 0; i < probeDeadStreak+1; i++ {
				s.b.probeNode(s.b.nodes[st.id], "a", nextCanary(uint64(i)))
			}
			s.model = "a"
			if _, ok := s.pickOK(st.id, nil, nil, s.b); ok {
				return fmt.Errorf("a1 is still pickable after %d failed canaries", probeDeadStreak+1)
			}
			return aNoStrike("a1")(s)
		}),
		T(`"a1"'s probe canary fails on a small prompt and it drops out of Tier-A`, func(s *mf1State) error {
			st := s.st("a1")
			s.b.probeNode(s.b.nodes[st.id], "a", nextCanary(1))
			s.b.mu.Lock()
			tq := s.b.trust[st.id]
			s.b.mu.Unlock()
			if !tq.probed || tq.probeOK || tq.probeFails < 1 {
				return fmt.Errorf("a1's canary did not fail (probed=%v ok=%v fails=%d)", tq.probed, tq.probeOK, tq.probeFails)
			}
			return nil
		}),
		T(`no error, no voided receipt and no strike exists for "a"`, aCode(200), func(s *mf1State) error {
			w := s.wallet
			s.wallet = s.payerWallet()
			es, err := s.entries()
			s.wallet = w
			if err != nil {
				return err
			}
			for _, e := range es {
				if e.Model == "a" {
					return fmt.Errorf("a receipt exists for the off-air model \"a\": %+v", e)
				}
			}
			return nil
		}),

		// --- streaming ---
		T(`the SSE stream carries only "b1"'s chunks`, aSSEOnly("b1", "a1")),
		T(`the consumer receives exactly one 200 response with no leaked 429`, func(s *mf1State) error {
			s.lastRec.mu.Lock()
			st := append([]int(nil), s.lastRec.statuses...)
			s.lastRec.mu.Unlock()
			if len(st) != 1 || st[0] != 200 {
				return fmt.Errorf("statuses written %v (final %d %.160s), want exactly one 200", st, s.lastCode, s.lastBody)
			}
			return aSSEOnly("b1", "a1")(s)
		}),
		T(`the SSE headers go out at streamCommitGrace (20 s) with no content`, aCode(200), func(s *mf1State) error {
			s.lastRec.mu.Lock()
			at := s.lastRec.headerAt
			s.lastRec.mu.Unlock()
			if at.IsZero() {
				return fmt.Errorf("no SSE headers were written")
			}
			d := at.Sub(s.relayStartAt)
			if d < streamCommitGrace*8/10 || d >= s.holdFor {
				return fmt.Errorf("SSE headers went out after %s, want at the commit grace %s and before a1 answered (%s)", d, streamCommitGrace, s.holdFor)
			}
			if bytes.Contains(s.lastBody, []byte(`"content"`)) {
				return fmt.Errorf("the committed stream carries content: %.200s", s.lastBody)
			}
			return nil
		}),
		T(`the stream ends with "a1"'s failure per today's committed-stream rule`, aCode(200), aVoided("a1", ""), func(s *mf1State) error {
			if bytes.Contains(s.lastBody, []byte("from b1")) {
				return fmt.Errorf("the committed stream carries b1's chunks: %.200s", s.lastBody)
			}
			return nil
		}),
		T(`the stream ends as today (partial output, settled per today's mid-stream rule)`, aCode(200), func(s *mf1State) error {
			if !bytes.Contains(s.lastBody, []byte("chunk2 from a1")) {
				return fmt.Errorf("the partial stream was not delivered: %.300s", s.lastBody)
			}
			vr, _, err := s.voidReason("a1")
			if err != nil {
				return err
			}
			if vr != "" {
				return fmt.Errorf("the partial stream was VOIDED (%s); today's rule settles it", vr)
			}
			return aLedger(1, 1, 0)(s)
		}),
		T(`the final SSE chunk before [DONE] carries usage.rogerai.model "b" and usage.rogerai.node "b1"`, func(s *mf1State) error {
			chunk, err := s.usageChunk()
			if err != nil {
				return err
			}
			usage, _ := chunk["usage"].(map[string]any)
			rog, _ := usage["rogerai"].(map[string]any)
			if rog["model"] != "b" || rog["node"] != s.st("b1").id {
				return fmt.Errorf("usage.rogerai names model %v node %v, want b / %s", rog["model"], rog["node"], s.st("b1").id)
			}
			return nil
		}),
		T(`usage.cost equals the settled cost of "b1"'s attempt`, func(s *mf1State) error {
			cost, err := s.costOf("b1")
			if err != nil {
				return err
			}
			return s.usageCostEquals(cost)
		}),

		// --- deadline ---
		T(`no attempt on "b" starts with less than 10 s of deadline remaining`, aRecvN("a1", 1), aNothing("b1")),
		T(`the response is the first-failure outcome as today, voided`, aCode(500), aVoided("a1", "")),
		T(`attempts stop when less than 10 s remain on the 90 s deadline`, func(s *mf1State) error {
			s.mmu.Lock()
			defer s.mmu.Unlock()
			n := len(s.arrivals)
			if n < 2 || n >= 15 {
				return fmt.Errorf("%d attempts were made, want the walk cut short by the deadline (2..14 of 15)", n)
			}
			var last time.Time
			for _, rs := range s.recv {
				for _, r := range rs {
					if r.at.After(last) {
						last = r.at
					}
				}
			}
			if left := s.relayStartAt.Add(nonStreamRelayWait).Sub(last); left < relayMinAttemptBudget-50*time.Millisecond {
				return fmt.Errorf("the last attempt started with %s left, less than the attempt budget %s", left, relayMinAttemptBudget)
			}
			return nil
		}),
		T(`the response is the last failure with the consumer charged 0`, aCode(500), aChargedZero()),
		T(`no attempt started after the deadline`, func(s *mf1State) error {
			s.mmu.Lock()
			defer s.mmu.Unlock()
			end := s.relayStartAt.Add(nonStreamRelayWait)
			for n, rs := range s.recv {
				for _, r := range rs {
					if r.at.After(end) {
						return fmt.Errorf("%s was dispatched %s after the deadline", n, r.at.Sub(end))
					}
				}
			}
			return nil
		}),

		// --- moderation ---
		T(`the screening job's served node is "b1"`, aFrom("b1"), func(s *mf1State) error {
			fs, err := s.flags()
			if err != nil {
				return err
			}
			if len(fs) == 0 {
				return fmt.Errorf("no moderation verdict was recorded")
			}
			if fs[0].Node != s.st("b1").id {
				return fmt.Errorf("the verdict names node %q, want b1 (%s)", fs[0].Node, s.st("b1").id)
			}
			return nil
		}),
		T(`the verdict is recorded once for the request, not per attempt`, func(s *mf1State) error {
			fs, err := s.flags()
			if err != nil {
				return err
			}
			if len(fs) != 1 {
				return fmt.Errorf("%d verdicts recorded, want exactly 1", len(fs))
			}
			return nil
		}),

		// --- the edge fabric ---
		T(`the bridge serves "b" and X-RogerAI-Model is "b" with X-RogerAI-Relay naming the Tower`, aCode(200), aXModel("b"), func(s *mf1State) error {
			return aHdr("X-RogerAI-Relay", s.towers["t1"].id)(s)
		}),
		T(`the Tower received nothing`, func(s *mf1State) error {
			if n := len(s.towerGot()); n != 0 {
				return fmt.Errorf("the Tower received %d request(s), want none", n)
			}
			return aRecvN("a1", 1)(s)
		}),
		T(`the Tower is never tried and the response is 200 from "b1"`, func(s *mf1State) error {
			if n := len(s.towerGot()); n != 0 {
				return fmt.Errorf("the Tower received %d request(s), want none", n)
			}
			return aFrom("b1")(s)
		}),
		T(`the Tower received body model "b" and no "models", "provider" or "roger" key`, func(s *mf1State) error {
			got := s.towerGot()
			if len(got) == 0 {
				return fmt.Errorf("the Tower received nothing (response %d %.160s)", s.lastCode, s.lastBody)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(got[len(got)-1], &m); err != nil {
				return err
			}
			var model string
			_ = json.Unmarshal(m["model"], &model)
			if model != "b" {
				return fmt.Errorf("the Tower received body model %q, want \"b\"", model)
			}
			for _, k := range []string{"models", "provider", "roger"} {
				if _, ok := m[k]; ok {
					return fmt.Errorf("the Tower received the routing carrier %q", k)
				}
			}
			return nil
		}),

		// --- telemetry and logs ---
		T(`/admin/live relay_failovers increments by 2 and model_fallbacks increments by 1`, aCounters(2, 1)),
		T(`model_fallbacks does not increment and relay_failovers does not increment`, aFrom("b1"), func(s *mf1State) error {
			if err := s.readAdminLive(); err != nil {
				return err
			}
			if !s.hasCounter(s.adminResp, "model_fallbacks") {
				return fmt.Errorf("/admin/live carries no model_fallbacks counter")
			}
			return aCounters(0, 0)(s)
		}),
		T(`the existing keys are unchanged and model_fallbacks is present`, func(s *mf1State) error {
			if !s.hasCounter(s.adminResp, "model_fallbacks") {
				return fmt.Errorf("/admin/live carries no model_fallbacks counter")
			}
			for k := range s.admin0 {
				if _, ok := s.adminResp[k]; !ok {
					return fmt.Errorf("/admin/live lost the key %q", k)
				}
			}
			r0, _ := s.admin0["routing"].(map[string]any)
			r1, _ := s.adminResp["routing"].(map[string]any)
			for k := range r0 {
				if _, ok := r1[k]; !ok {
					return fmt.Errorf("/admin/live routing lost the key %q", k)
				}
			}
			return nil
		}),
		T(`exactly one log line names requested=[a,b] served=b@b1`, func(s *mf1State) error {
			want, n := "served=b@"+s.st("b1").id, 0
			for _, line := range strings.Split(s.newLogs(), "\n") {
				if strings.Contains(line, "requested=[a,b]") && strings.Contains(line, want) {
					n++
				}
			}
			if n != 1 {
				return fmt.Errorf("%d log lines name requested=[a,b] %s, want exactly 1", n, want)
			}
			return nil
		}),
		T(`no log line contains the prompt`, func(s *mf1State) error {
			if strings.Contains(s.newLogs(), "word word word") {
				return fmt.Errorf("a log line carries the prompt")
			}
			return nil
		}),

		// --- /generation ---
		T(`attempts are [{1,a1,a,429},{2,a2,a,500},{3,b1,b,200}]`, func(s *mf1State) error {
			at, _, err := s.genAttempts()
			if err != nil {
				return err
			}
			want := []struct {
				n           float64
				node, model string
				status      float64
			}{{1, s.st("a1").id, "a", 429}, {2, s.st("a2").id, "a", 500}, {3, s.st("b1").id, "b", 200}}
			if len(at) != len(want) {
				return fmt.Errorf("%d attempts in /generation, want 3: %v", len(at), at)
			}
			for i, w := range want {
				if at[i]["n"] != w.n || at[i]["node"] != w.node || at[i]["model"] != w.model || at[i]["status"] != w.status {
					return fmt.Errorf("attempt %d = %v, want {%v %s %s %v}", i+1, at[i], w.n, w.node, w.model, w.status)
				}
			}
			return nil
		}),
		T(`served is {node: b1, model: b} and models is ["a", "b"]`, func(s *mf1State) error {
			_, rec, err := s.genAttempts()
			if err != nil {
				return err
			}
			served, _ := rec["served"].(map[string]any)
			if served["node"] != s.st("b1").id || served["model"] != "b" {
				return fmt.Errorf("served = %v, want node b1 model b", served)
			}
			models, _ := json.Marshal(rec["models"])
			if string(models) != `["a","b"]` {
				return fmt.Errorf("models = %s, want [\"a\",\"b\"]", models)
			}
			return nil
		}),

		// --- compatibility ---
		T(`the response, headers, receipts and ledger rows match the pre-feature single-model failover`, aFrom("a2"), aBodyIsCompletion(), aVoided("a1", "upstream-throttled"), aSettled("a2"), aLedger(1, 1, 1), aArrivals("a1", "a2"), func(s *mf1State) error {
			return s.nonStreamHeaders()
		}),
	}
}

// --- registration --------------------------------------------------------------------

func (s *mf1State) register(sc *godog.ScenarioContext) {
	lit := func(text string, fn func() error) { sc.Step("^"+regexp.QuoteMeta(text)+"$", fn) }
	for _, g := range mf1Givens() {
		g := g
		if g.fn == nil {
			lit(g.text, func() error { return s.stationSentence(g.text) })
		} else {
			lit(g.text, func() error { return g.fn(s) })
		}
	}
	// Outline Givens (disjoint by construction: a bare status, "with body {..}", "<status> with {..}").
	sc.Step(`^"a1" serves "a" and its upstream (returns (?:429|500|502|503|504)|is unreachable \(station posts 502\)|returns 200 with an empty completion|returns 200 with whitespace only|returns 200 claiming tokens, no text)$`, func(failure string) error {
		st := s.stn("a1", mf1StOpt{})
		switch {
		case strings.HasPrefix(failure, "is unreachable"):
			st.mu.Lock()
			st.unreachable = true
			st.mu.Unlock()
		case failure == "returns 200 with an empty completion":
			s.behave("a1", mf1Status(200, `{"choices":[],"usage":{"prompt_tokens":5000,"completion_tokens":0}}`, nil))
		case failure == "returns 200 with whitespace only":
			s.behave("a1", mf1Status(200, `{"choices":[{"message":{"role":"assistant","content":"   \n "}}],"usage":{"prompt_tokens":5000,"completion_tokens":3}}`, nil))
		case failure == "returns 200 claiming tokens, no text":
			s.behave("a1", mf1Status(200, `{"choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":5000,"completion_tokens":40}}`, nil))
		default:
			code, _ := strconv.Atoi(strings.TrimPrefix(failure, "returns "))
			s.behave("a1", mf1Status(code, utDefaultBody(code), nil))
		}
		return nil
	})
	sc.Step(`^"a1" serves "a" and its upstream returns 400 with body (\{.*\})$`, func(body string) error {
		s.stn("a1", mf1StOpt{})
		s.behave("a1", mf1Status(400, body, nil))
		return nil
	})
	sc.Step(`^"a1" serves "a" and its upstream returns (\d{3}) with (\{"error":"[^"]*"\})$`, func(status int, body string) error {
		s.stn("a1", mf1StOpt{})
		s.behave("a1", mf1Status(status, body, nil))
		return nil
	})

	// Whens: the outline row first (a raw "models" value), then every other wording.
	outline := regexp.MustCompile(`^a consumer relays with "model": "a" and "models": (.+)$`)
	sc.Step(outline.String(), func(value string) error {
		text := `a consumer relays with "model": "a" and "models": ` + value
		if err := s.begin(); err != nil {
			return err
		}
		a := "a"
		q := mf1Req{model: &a, models: json.RawMessage(value), tokens: 50}
		if json.Valid([]byte(value)) {
			var err error
			if q, err = s.requestOf(text); err != nil {
				return err
			}
		}
		return s.send(q)
	})
	for _, w := range mf1RelayWhens {
		w := w
		if outline.MatchString(w) {
			continue
		}
		lit(w, func() error {
			s.instance2 = s.instance2 || strings.HasPrefix(w, "instance 2")
			return s.relaySentence(w)
		})
	}
	for _, w := range mf1Whens() {
		w := w
		lit(w.text, func() error { return w.fn(s) })
	}

	// Thens: the parametrised ones first, then the table.
	voidRe := regexp.MustCompile(`^"([a-z0-9]+)"'s receipt is voided with void_reason "([a-z-]+)"$`)
	sc.Step(voidRe.String(), func(name, reason string) error { return aVoided(name, reason)(s) })
	codeRe := regexp.MustCompile(`^the response is 400 with error code "([a-z_]+)" naming "models"$`)
	sc.Step(codeRe.String(), func(code string) error {
		return mf1All(aCode(400), aErrCode(code), aMsgHas("models"))(s)
	})
	sc.Step(`^the response is (\d{3}) with the upstream body \(one attempt, voided as today\)$`, func(status int) error {
		if err := aCode(status)(s); err != nil {
			return err
		}
		rs := s.recvOf("a1")
		if len(rs) != 1 {
			return fmt.Errorf("a1 received %d requests, want exactly one attempt", len(rs))
		}
		if !bytes.Contains(s.lastBody, []byte(`"error"`)) {
			return fmt.Errorf("body %.200s is not the upstream's error body", s.lastBody)
		}
		return aVoided("a1", "")(s)
	})
	for _, t := range mf1Thens() {
		t := t
		if voidRe.MatchString(t.text) || codeRe.MatchString(t.text) {
			continue
		}
		lit(t.text, func() error { return t.fn(s) })
	}
}

func TestRoutingModelFallbackBDD(t *testing.T) {
	st := &mf1State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardownMF1()
	})
	suite := godog.TestSuite{
		Name: "model_fallback_list",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.resetMF1()
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.teardownMF1()
				return ctx, nil
			})
			st.register(sc)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/routing/model_fallback_list.feature"},
			Tags: "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later && ~@unit && ~@part-c && ~@slice3", TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("model_fallback_list.feature has failing scenarios")
	}
}

// TestMF1PlannerSeedIsReproducible is the plain Go test for the @unit scenario "the per-attempt
// seed makes the station choice within a model reproducible". Request ids are minted per
// request, so the seed cannot be injected through the relay. No list-level planner seam exists
// yet (the plan is built inline in relay()); until slice 1 extracts one, this exercises the
// seam that does exist - pickFor with an injected rng, as router_test.go does - for the model
// the list resolves to: no station serves "a" (nothing is picked, the list moves on), and the
// same seed over the same two equal "b" stations yields the same station every time, while
// different seeds reach both (so the equality is the seed's doing, not a fixed winner).
func TestMF1PlannerSeedIsReproducible(t *testing.T) {
	st := &mf1State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
	if err := st.resetMF1(); err != nil {
		t.Fatal(err)
	}
	defer st.teardownMF1()
	st.stn("b1", mf1StOpt{})
	st.stn("b2", mf1StOpt{})
	pick := func(model, seed string) (string, bool) {
		st.b.mu.Lock()
		defer st.b.mu.Unlock()
		n, _, ok := st.b.pickFor(model, false, 0, 0, 0, "", nil, nil, nil, pickReq{rng: seededRand(seed)})
		return n.NodeID, ok
	}
	if _, ok := pick("a", "seed-1"); ok {
		t.Fatalf("a station was picked for \"a\", which no station serves")
	}
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		seed := "seed-" + strconv.Itoa(i)
		first, ok := pick("b", seed)
		if !ok {
			t.Fatalf("no station picked for \"b\" with %s", seed)
		}
		if again, _ := pick("b", seed); again != first {
			t.Fatalf("seed %s picked %s then %s for the same candidate set", seed, first, again)
		}
		seen[first] = true
	}
	if len(seen) != 2 {
		t.Fatalf("64 seeds reached %d of the 2 equal stations; the pick is not seed-driven", len(seen))
	}
}
