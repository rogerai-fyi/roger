package main

// routing_node_preference_bdd_test.go makes the WHOLE of
// features/routing/node_preference.feature EXECUTABLE against the REAL broker (slice 1 of
// the routing-expression set): provider.only / ignore / order / allow_fallbacks / sort and
// roger.pref, their header equivalents, the Tower relay ids in node lists, the carrier
// strip, telemetry and the anti-abuse pins. The scenarios that can only be driven from
// another runner carry a @cli / @tui / @harness / @docs tag and are excluded here.
//
// It rides the slice-0 runner's fixtures (s0State over rpState over foState: a real
// relayBroker + store + miniredis, real stations with scripted upstreams behind real
// tunnels) and REUSES its step functions for the 57 @slice0 scenarios: every relay step
// goes through one dispatcher (np1Relays) that first tries the slice-0 step table, then a
// clause parser for the slice-1 shapes (only, sort, pref, grants, towers, streams). Every
// relay is a real relay through relay(); attempt order is read at the upstreams; money is
// read from the ledger; counters from /admin/live; logs from the captured broker log.
//
// Observation notes (where a sentence is judged by a stated proxy):
//   - "picked more often than under <pref>" is judged by the real scoring function: 2000
//     seeded pickFor draws under each pref over the same registry (deterministic, no
//     sampling noise), PLUS the batch having really run under the pref (every relay 200
//     and the /admin/live routing_pref_<value> counter).
//   - "in score order" (two-station no-fallback plan) is observed as "exactly those two
//     stations, once each": the score order of a seeded two-member draw is not separately
//     visible from outside the broker.
//   - "planned after" / "not in the plan" are observed by re-firing the same request with
//     every other station answering 429 and reading the upstream hit order.
//   - "ran the same constant-work path" is structural: same status, same header set, same
//     number of broker log lines, no upstream call, no hold - never a timing measurement.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	mrand "math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
)

type np1KV struct{ k, v string }

type np1Snap struct {
	holds, releases, spends int
	receipts                int
	balance                 float64
}

type np1State struct {
	*s0State

	// np1TTFTOnly: the scenario stated TTFT figures, so every relay starts with no total
	// latency measured (the stub answers in microseconds, and the broker's own measurement of a
	// served relay would otherwise outrank the stated figures from the second relay on).
	np1TTFTOnly bool

	// request shaping beyond what the slice-0 runner carries
	np1Grant   bool               // relay as the grant holder (bearer token, no signature)
	np1PrefHdr string             // X-Roger-Pref
	np1Caller  ed25519.PrivateKey // "consumer B"
	np1Stream  bool
	np1Pref    string // the pref the batch ran under (body over header)

	// what was sent / observed
	np1Base, np1Sent []byte     // the body before the routing fragment, and as sent
	np1Attempts      []int      // upstream hits each relay of the current batch made
	np1AttemptHits   [][]string // the stations each relay of the current batch hit, in order
	np1Prev          []rpResult // the batch before the current one
	np1All           []rpResult // every response of the scenario, in order
	np1LogSpans      [][2]int   // captured-log offsets [before, after] of each relay
	np1Snapped       bool
	np1Before        np1Snap
	np1LogMark       int

	// fixtures
	np1RA        map[string]string  // station -> the Retry-After its 429 carries
	np1TPS       map[string]float64 // station -> the tps the scenario measured
	np1BannedOps map[string]bool
	np1Later     []rpResult // "relays and later relays again": the later relays
	np1BResults  []rpResult // consumer B's relays
	np1Collision string     // the station name a Tower row poses as
	np1PoseTower string     // that Tower's id

	// np1Shares is the seeded pick share of every station under each pref, taken at the
	// scenario's GIVEN state - before the batch runs. A real relay re-measures the station
	// that served it (tps, success, exploration counts), so shares read after a batch would
	// judge the batch's own side effects instead of the scenario's fixture.
	np1Shares map[string]map[string]float64
}

func (s *np1State) np1Reset() error {
	if err := s.resetSlice0(); err != nil {
		return err
	}
	s.np1Grant, s.np1PrefHdr, s.np1Caller, s.np1Stream, s.np1Pref = false, "", nil, false, ""
	s.np1Base, s.np1Sent, s.np1Attempts, s.np1Prev, s.np1All, s.np1LogSpans = nil, nil, nil, nil, nil, nil
	s.np1AttemptHits = nil
	s.np1Snapped, s.np1Before, s.np1LogMark = false, np1Snap{}, 0
	s.np1RA, s.np1TPS, s.np1BannedOps = map[string]string{}, map[string]float64{}, map[string]bool{}
	s.np1Later, s.np1BResults, s.np1Collision, s.np1PoseTower = nil, nil, "", ""
	s.np1Shares, s.np1TTFTOnly = nil, false
	return nil
}

// --- the relay dispatcher ----------------------------------------------------------

var np1SubjectRe = regexp.MustCompile(`^(a funded consumer|an anonymous consumer|a signed-in consumer|the grant holder|consumer A|consumer B|\d+ funded consumers|\d+ anonymous consumers|\d+ signed-in consumers|\d+ grant) relays? (.*)$`)

type np1Step struct {
	re   *regexp.Regexp
	call func(a []string) error
}

// np1Slice0Table is the slice-0 runner's relay step table, verbatim: the same expressions
// bound to the same step functions, so the @slice0 scenarios run exactly as they do there.
func (s *np1State) np1Slice0Table() []np1Step {
	mk := func(expr string, call func(a []string) error) np1Step {
		return np1Step{re: regexp.MustCompile(expr), call: call}
	}
	return []np1Step{
		mk(`^(\d+) funded consumers relay with provider\.ignore (\[.*\])$`, func(a []string) error { return s.relaysIgnore(a[0], a[1]) }),
		mk(`^a funded consumer relays with provider\.ignore (\[.*\]) and the pick lands on "([^"]*)"$`, func(a []string) error { return s.relayIgnoreLandsOn(a[0], a[1]) }),
		mk(`^a funded consumer relays with provider\.ignore (\[[^\]]*\]|"[^"]*"|\d+|33 distinct station ids)$`, func(a []string) error { return s.relayIgnore(a[0]) }),
		mk(`^a funded consumer relays with X-Roger-Exclude-Nodes "([^"]*)" and provider\.ignore (\[.*\])$`, func(a []string) error { return s.relayExcludeAndIgnore(a[0], a[1]) }),
		mk(`^a funded consumer relays with roger\.freq for band "([^"]*)" and provider\.ignore (\[.*\])$`, func(a []string) error { return s.relayFreqIgnore(a[0], a[1]) }),
		mk(`^a funded consumer relays with roger\.freq for band "([^"]*)" and provider\.order (\[.*\])$`, func(a []string) error { return s.relayFreqOrder(a[0], a[1]) }),
		mk(`^(\d+) funded consumers relay with provider\.order (\[.*\])$`, func(a []string) error { return s.relaysOrder(a[0], a[1]) }),
		mk(`^a funded consumer relays with provider\.order (\[.*\]) and provider\.ignore (\[.*\])$`, func(a []string) error { return s.relayOrderIgnore(a[0], a[1]) }),
		mk(`^a funded consumer relays with provider\.order (\[.*\]) and no max_price$`, func(a []string) error { return s.relayOrderNoMaxPrice(a[0]) }),
		mk(`^a funded consumer relays with provider\.order (\[.*\]) and provider\.allow_fallbacks (true|false)$`, func(a []string) error { return s.relayOrderFallbacks(a[0], a[1]) }),
		mk(`^a funded consumer relays with provider\.order (\[.*\]), provider\.allow_fallbacks false, and ROGERAI_RELAY_ATTEMPTS (\d+)$`, func(a []string) error { return s.relayOrderNoFallbackAttempts(a[0], a[1]) }),
		mk(`^a funded consumer relays with provider\.order (\[[^\]]*\]|"[^"]*"|\d+|33 distinct station ids)$`, func(a []string) error { return s.relayOrder(a[0]) }),
		mk(`^a funded consumer relays with X-Roger-Node "([^"]*)" and provider\.order (\[.*\])$`, func(a []string) error { return s.relayPinAndOrder(a[0], a[1]) }),
		mk(`^a funded consumer relays with X-Roger-Node "([^"]*)", provider\.order (\[.*\]), and provider\.allow_fallbacks false$`, func(a []string) error { return s.relayPinOrderNoFallbacks(a[0], a[1]) }),
		mk(`^a funded consumer relays with X-Roger-Node "([^"]*)" and provider\.allow_fallbacks true$`, func(a []string) error { return s.relayPinFallbacksTrue(a[0]) }),
		mk(`^a funded consumer relays with X-Roger-Node "([^"]*)"$`, func(a []string) error { return s.relayPin(a[0]) }),
		mk(`^a funded consumer relays with roger\.confidential true and provider\.order (\[.*\])$`, func(a []string) error { return s.relayConfidentialOrder(a[0]) }),
		mk(`^an anonymous consumer relays with provider\.order (\[.*\])$`, func(a []string) error { return s.anonRelayOrder(a[0]) }),
		mk(`^a funded consumer relays with provider\.allow_fallbacks false and the pick lands on "([^"]*)"$`, func(a []string) error { return s.relayNoFallbackLandsOn(a[0]) }),
		mk(`^a funded consumer relays with provider\.allow_fallbacks false$`, func(a []string) error { return s.relayNoFallback() }),
		mk(`^a funded consumer relays with provider\.allow_fallbacks ("[^"]*"|\d+|null|true)$`, func(a []string) error { return s.relayFallbacksRaw(a[0]) }),
	}
}

// np1Relays is the ONE relay step: the slice-0 table first, the slice-1 clause parser after.
func (s *np1State) np1Relays(text string) error {
	if err := s.np1Snapshot(); err != nil {
		return err
	}
	for _, e := range s.np1Slice0Table() {
		if m := e.re.FindStringSubmatch(text); m != nil {
			logBefore := len(s.logs.String())
			if err := e.call(m[1:]); err != nil {
				return err
			}
			s.np1All = append(s.np1All, rpResult{code: s.lastCode, hdr: s.lastHdr, body: s.lastBody})
			s.np1LogSpans = append(s.np1LogSpans, [2]int{logBefore, len(s.logs.String())})
			return nil
		}
	}
	return s.np1Generic(text)
}

// np1Split cuts a clause list on the top-level separators (", and ", " and ", ", "),
// leaving JSON arrays, objects and quoted strings whole.
func np1Split(in string) []string {
	var out []string
	depth, inQ, start := 0, false, 0
	for i := 0; i < len(in); {
		c := in[i]
		switch {
		case inQ:
			if c == '\\' {
				i += 2
				continue
			}
			if c == '"' {
				inQ = false
			}
		case c == '"':
			inQ = true
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case depth == 0:
			cut := 0
			for _, sep := range []string{", and ", " and ", ", "} {
				if strings.HasPrefix(in[i:], sep) {
					cut = len(sep)
					break
				}
			}
			if cut > 0 {
				out = append(out, strings.TrimSpace(in[start:i]))
				i += cut
				start = i
				continue
			}
		}
		i++
	}
	return append(out, strings.TrimSpace(in[start:]))
}

var (
	np1ProviderRe = regexp.MustCompile(`^provider\.(only|ignore|order|sort|allow_fallbacks) (.+)$`)
	np1MaxOutRe   = regexp.MustCompile(`^provider\.max_price\.completion ([0-9.]+)$`)
	np1RogerRe    = regexp.MustCompile(`^roger\.(pref|min_tps|max_ttft_ms|confidential) (.+)$`)
	np1FreqRe     = regexp.MustCompile(`^roger\.freq for band "([^"]*)"$`)
	np1HdrRe      = regexp.MustCompile(`^X-Roger-(Node|Exclude-Nodes|Pref):? "?([^"]*)"?$`)
	np1LandRe     = regexp.MustCompile(`^the pick lands on "([^"]*)"$`)
	np1AttemptsRe = regexp.MustCompile(`^ROGERAI_RELAY_ATTEMPTS (\d+)$`)
	np1Of32Re     = regexp.MustCompile(`^of 32 distinct ids including "([^"]*)"$`)
	np1ForRe      = regexp.MustCompile(`^for "([^"]*)" with (.*)$`)
	np1NoRe       = regexp.MustCompile(`^no (max_price|X-Roger-Freq|body|body pref|body routing object|routing object)$`)
)

// np1Generic parses "<subject> relays <tail>" for the slice-1 shapes and fires it.
func (s *np1State) np1Generic(text string) error {
	m := np1SubjectRe.FindStringSubmatch(text)
	if m == nil {
		return fmt.Errorf("unparsed relay step %q", text)
	}
	subject, tail := m[1], m[2]
	count := 1
	s.np1Grant, s.np1Caller, s.anon = false, nil, false
	switch subject {
	case "a funded consumer", "a signed-in consumer", "consumer A":
	case "an anonymous consumer":
		s.anon = true
	case "the grant holder":
		s.np1Grant = true
	case "consumer B":
		priv, err := s.np1SecondConsumer()
		if err != nil {
			return err
		}
		s.np1Caller = priv
	default:
		f := strings.Fields(subject)
		count, _ = strconv.Atoi(f[0])
		switch f[1] {
		case "anonymous":
			s.anon = true
		case "grant":
			s.np1Grant = true
		}
	}

	if tail == "and later relays again with no routing object" {
		return s.np1RelayThenLater()
	}
	tail = strings.Replace(tail, " with the 90 second relay deadline", " and the 90 second relay deadline", 1)
	switch {
	case strings.HasPrefix(tail, "with "):
		tail = strings.TrimPrefix(tail, "with ")
	case strings.HasPrefix(tail, "an 8000-token prompt with "):
		s.tokens = 8000
		tail = strings.TrimPrefix(tail, "an 8000-token prompt with ")
	default:
		fm := np1ForRe.FindStringSubmatch(tail)
		if fm == nil {
			return fmt.Errorf("unparsed relay tail %q", tail)
		}
		s.model, tail = fm[1], fm[2]
	}

	var provider, roger []np1KV
	s.hdrPin, s.hdrExclude, s.np1PrefHdr, s.np1Stream, s.np1Pref = "", "", "", false, ""
	land := ""
	for _, c := range np1Split(tail) {
		c = strings.TrimPrefix(strings.TrimPrefix(c, "header "), "body ")
		switch {
		case np1NoRe.MatchString(c), c == "the 90 second relay deadline":
			// nothing to add: the request simply does not carry it / the deadline is the
			// (scaled) production deadline the Given installed
		case c == `"stream": true`:
			s.np1Stream = true
		case c == "tools":
			s.bodyShape = "tools"
		case np1LandRe.MatchString(c):
			land = np1LandRe.FindStringSubmatch(c)[1]
		case np1AttemptsRe.MatchString(c):
			if err := os.Setenv("ROGERAI_RELAY_ATTEMPTS", np1AttemptsRe.FindStringSubmatch(c)[1]); err != nil {
				return err
			}
		case np1MaxOutRe.MatchString(c):
			provider = append(provider, np1KV{"max_price", `{"completion": ` + np1MaxOutRe.FindStringSubmatch(c)[1] + `}`})
		case np1FreqRe.MatchString(c):
			band := np1FreqRe.FindStringSubmatch(c)[1]
			if s.bands[band] == "" { // a scenario that names a band without a Given: one station p1
				if err := s.privateBand(band, `"p1"`); err != nil {
					return err
				}
			}
			roger = append(roger, np1KV{"freq", strconv.Quote(s.bands[band])})
		case np1ProviderRe.MatchString(c):
			pm := np1ProviderRe.FindStringSubmatch(c)
			key, val := pm[1], pm[2]
			if key == "only" || key == "ignore" || key == "order" {
				if om := np1Of32Re.FindStringSubmatch(val); om != nil {
					val = s.np1ThirtyTwoIncluding(om[1])
				} else {
					val = s.listValue(val)
				}
			}
			provider = append(provider, np1KV{key, val})
		case np1RogerRe.MatchString(c):
			rm := np1RogerRe.FindStringSubmatch(c)
			roger = append(roger, np1KV{rm[1], rm[2]})
			if rm[1] == "pref" {
				s.np1Pref = strings.Trim(rm[2], `"`)
			}
		case np1HdrRe.MatchString(c):
			hm := np1HdrRe.FindStringSubmatch(c)
			switch hm[1] {
			case "Node":
				s.hdrPin = s.idOf(hm[2])
			case "Exclude-Nodes":
				s.hdrExclude = s.idOf(hm[2])
			case "Pref":
				s.np1PrefHdr = hm[2]
				if s.np1Pref == "" {
					s.np1Pref = hm[2]
				}
			}
		default:
			return fmt.Errorf("unparsed relay clause %q in %q", c, text)
		}
	}
	// a body pref named after the header clause still wins
	for _, kv := range roger {
		if kv.k == "pref" {
			s.np1Pref = strings.Trim(kv.v, `"`)
		}
	}
	s.bodyFrag = np1Frag(provider, roger)
	if land != "" {
		s.landOn(land)
	}
	if len(s.batch) > 0 {
		s.np1Prev = s.batch
	}
	s.batch, s.np1Attempts, s.np1AttemptHits = nil, nil, nil
	if count >= 20 && s.np1Shares == nil {
		s.np1Shares = map[string]map[string]float64{}
		for _, pref := range []string{"", "balanced", "cheap", "fast", "reliable"} {
			s.np1Shares[pref] = s.np1Share(pref, nil)
		}
	}
	for i := 0; i < count; i++ {
		if err := s.np1Fire(); err != nil {
			return err
		}
		if subject == "consumer B" {
			s.np1BResults = append(s.np1BResults, s.batch[len(s.batch)-1])
		}
	}
	return nil
}

func np1Obj(name string, kvs []np1KV) string {
	if len(kvs) == 0 {
		return ""
	}
	parts := make([]string, len(kvs))
	for i, kv := range kvs {
		parts[i] = strconv.Quote(kv.k) + ": " + kv.v
	}
	return strconv.Quote(name) + ": {" + strings.Join(parts, ", ") + "}"
}

// np1Frag renders the routing members verbatim (a malformed table value reaches the broker
// exactly as written). The provider object comes first so the slice-0 Then steps that read
// s.bodyFrag keep working.
func np1Frag(provider, roger []np1KV) string {
	p, r := np1Obj("provider", provider), np1Obj("roger", roger)
	switch {
	case p != "" && r != "":
		return p + ", " + r
	case p != "":
		return p
	}
	return r
}

func (s *np1State) np1ThirtyTwoIncluding(name string) string {
	ids := make([]string, 0, 32)
	for i := 0; i < 31; i++ {
		ids = append(ids, fmt.Sprintf("x%d-%s", i, s.nonce))
	}
	ids = append(ids, s.idOf(name))
	b, _ := json.Marshal(ids)
	return string(b)
}

// np1Snapshot records the scenario's pre-relay state once: the slice-0 snapshot (holds,
// upstream counts, hits) plus receipts, ledger rows, balance and the log offset.
func (s *np1State) np1Snapshot() error {
	if s.np1Snapped {
		return nil
	}
	if err := s.ensureFunded(); err != nil {
		return err
	}
	if err := s.snapshotNowOnce(); err != nil {
		return err
	}
	s.np1Snapped = true
	h, rel, sp, _, err := s.holdRows()
	if err != nil {
		return err
	}
	rc, err := s.receiptCount()
	if err != nil {
		return err
	}
	bal, err := s.balance()
	if err != nil {
		return err
	}
	s.np1Before = np1Snap{holds: h, releases: rel, spends: sp, receipts: rc, balance: bal}
	s.np1LogMark = len(s.logs.String())
	return nil
}

// np1Fire is one REAL relay with the current shaping: the slice-0 fire plus the grant bearer,
// the X-Roger-Pref header, a second consumer and streaming.
func (s *np1State) np1Fire() error {
	if s.np1TTFTOnly {
		s.b.metricsMu.Lock()
		clear(s.b.totalLat)
		s.b.metricsMu.Unlock()
	}
	if err := s.np1Snapshot(); err != nil {
		return err
	}
	// The stated tok/s hold across the batch: the stub upstream answers in microseconds, so
	// the broker's own measurement of a served relay would otherwise overwrite them after the
	// first serve (a band pick, §14.4, is sensitive to exactly that).
	for n, v := range s.np1TPS {
		if st, ok := s.stations[n]; ok {
			s.setTPS(st.id, v)
		}
	}
	var priv ed25519.PrivateKey
	switch {
	case s.np1Grant:
	case s.anon:
		priv = s.anonPriv
	case s.np1Caller != nil:
		priv = s.np1Caller
	default:
		priv = s.consumerPriv
	}
	base := s.requestBody(s.np1Stream)
	body := append([]byte(nil), base...)
	if s.bodyFrag != "" {
		body = append(append([]byte(nil), bytes.TrimSuffix(bytes.TrimSpace(base), []byte("}"))...), []byte(","+s.bodyFrag+"}")...)
	}
	s.np1Base, s.np1Sent = base, body
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if s.np1Grant {
		r.Header.Set("Authorization", "Bearer "+s.grantToken)
	} else {
		signReq(r, priv, body)
	}
	if s.hdrPin != "" {
		r.Header.Set("X-Roger-Node", s.hdrPin)
	}
	if s.hdrExclude != "" {
		r.Header.Set("X-Roger-Exclude-Nodes", s.hdrExclude)
	}
	if s.minTPS != "" {
		r.Header.Set("X-Roger-Min-TPS", s.minTPS)
	}
	if s.np1PrefHdr != "" {
		r.Header.Set("X-Roger-Pref", s.np1PrefHdr)
	}
	hitsBefore, jobsBefore, logBefore := len(s.hitList()), len(s.jobs()), len(s.logs.String())
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.relayStart = time.Now()
	s.b.relay(w, r)
	s.relayEnd = time.Now()
	res := rpResult{code: w.Code, hdr: w.Header(), body: w.Body.Bytes()}
	s.batch = append(s.batch, res)
	s.np1All = append(s.np1All, res)
	// Attempts are counted from the jobs this broker dispatched during the relay (exact), not
	// from upstream hits, which can also include stray traffic from a leaked goroutine of an
	// earlier test whose old upstream port the OS reused for one of this scenario's stations.
	jobs := s.jobs()
	var mine []string
	if jobsBefore <= len(jobs) {
		mine = jobs[jobsBefore:]
	}
	s.np1Attempts = append(s.np1Attempts, len(mine))
	s.np1AttemptHits = append(s.np1AttemptHits, mine)
	if hits := len(s.hitList()) - hitsBefore; hits != len(mine) {
		s.t.Logf("np1: relay %d: %d upstream hit(s) for %d dispatched job(s) %v: stray upstream traffic", len(s.batch), hits, len(mine), mine)
	}
	s.np1LogSpans = append(s.np1LogSpans, [2]int{logBefore, len(s.logs.String())})
	s.lastCode, s.lastBody, s.lastHdr, s.lastRec = w.Code, w.Body.Bytes(), w.Header(), w
	return nil
}

// np1SecondConsumer is "consumer B": another signed, logged-in, funded account.
func (s *np1State) np1SecondConsumer() (ed25519.PrivateKey, error) {
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	gid, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err := s.db.BindOwner(store.Owner{GitHubID: gid.Int64() + 10, Login: "buyer-b-" + s.nonce, Pubkey: pub}); err != nil {
		return nil, err
	}
	if _, err := s.db.AddCredits("u_gh_"+strconv.FormatInt(gid.Int64()+10, 10), 10); err != nil {
		return nil, err
	}
	return priv, nil
}

// np1RelayThenLater: relay with no routing object until the echoing station has served once
// (so its echoed body really came back through the broker), then relay 30 more times.
func (s *np1State) np1RelayThenLater() error {
	s.bodyFrag, s.batch, s.np1Attempts, s.np1AttemptHits = "", nil, nil, nil
	served := false
	for i := 0; i < 40 && !served; i++ {
		if err := s.np1Fire(); err != nil {
			return err
		}
		served = s.lastCode == 200 && s.nameOfID(s.provider()) == "s1"
	}
	if !served {
		return fmt.Errorf("the echoing station never served in 40 relays (last %d %s)", s.lastCode, s.lastBody)
	}
	s.batch, s.np1Attempts, s.np1AttemptHits = nil, nil, nil
	for i := 0; i < 30; i++ {
		if err := s.np1Fire(); err != nil {
			return err
		}
	}
	s.np1Later = s.batch
	return nil
}

// --- Given: stations ----------------------------------------------------------------

// np1Cool cools a station through the real path WITHOUT the harness's stand-up-order tier
// demotion (foState.landOrder), so the other stations keep their Tier A standing.
func (s *np1State) np1Cool(name string, secs int) error {
	s.ordered = true
	return s.cool(name, secs)
}

func (s *np1State) np1CoolingFor(name, secs string) error {
	n, _ := strconv.Atoi(secs)
	return s.np1Cool(name, n)
}

func (s *np1State) np1OperatorBanned(op string) error { s.np1BannedOps[op] = true; return nil }

func (s *np1State) np1OwnedByOperator(name, op string) error {
	st := s.stand(name, 1, 1)
	if s.np1BannedOps[op] {
		s.b.metricsMu.Lock()
		if s.b.bannedOwners == nil {
			s.b.bannedOwners = map[string]bool{}
		}
		s.b.bannedOwners[st.acct] = true
		s.b.metricsMu.Unlock()
	}
	return nil
}

func (s *np1State) np1DeadProbe(name string) error {
	return s.stationState(name, "past the dead-probe streak")
}

func (s *np1State) np1OnlyAttested(name string) error {
	s.b.mu.Lock()
	s.b.confidential[s.st(name).id] = true
	s.b.mu.Unlock()
	return nil
}

func (s *np1State) np1OnAirOtherModelOnly(name, model string) error {
	st := s.stand(name, 1, 1)
	s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Model = model })
	return nil
}

func (s *np1State) np1DeclaresCtx(name, ctx string) error {
	n, _ := strconv.Atoi(ctx)
	s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) { o.Ctx, o.CtxEstimated = n, false })
	return nil
}

func (s *np1State) np1FreeOne(name string) error {
	s.stand(name, 0, 0)
	s.b.seedFunds = 0.5 // an anonymous wallet is seeded as production seeds first use
	return nil
}

func (s *np1State) np1FreeAndPaid(free, _ string) error { return s.np1FreeOne(free) }

func (s *np1State) np1FreeWithTPS(a, ta, b, tb string) error {
	for n, t := range map[string]string{a: ta, b: tb} {
		if err := s.np1FreeOne(n); err != nil {
			return err
		}
		v, _ := strconv.ParseFloat(t, 64)
		s.setTPS(s.st(n).id, v)
		s.np1TPS[n] = v
	}
	return nil
}

func (s *np1State) np1EqualScore(a, b string) error {
	for _, n := range []string{a, b} {
		s.setPrice(n, 2, 2)
		s.b.metricsMu.Lock()
		delete(s.b.success, s.st(n).id)
		delete(s.b.tps, s.st(n).id)
		s.b.inflight[s.st(n).id] = 0
		s.b.metricsMu.Unlock()
	}
	return nil
}

func (s *np1State) np1FiveFirstThree429() error {
	for _, n := range []string{"s1", "s2", "s3", "s4", "s5"} {
		s.stand(n, 1, 1)
	}
	for _, n := range []string{"s1", "s2", "s3"} {
		s.script429For(n, "")
	}
	return nil
}

// np1Takes85Then429 is the production 85 s of a 90 s deadline at test scale (the same 2 s
// window and 10-of-90 attempt budget the approved upstream_failover runner uses).
func (s *np1State) np1Takes85Then429(name string) error {
	nonStreamRelayWait = 2 * time.Second
	relayMinAttemptBudget = nonStreamRelayWait / 9
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1900 * time.Millisecond)
		w.WriteHeader(429)
		_, _ = w.Write([]byte(utDefaultBody(429)))
	})
	return nil
}

func (s *np1State) np1Both429RA(a, ra, b, rb string) error {
	s.script429For(a, ra)
	s.script429For(b, rb)
	s.np1RA[a], s.np1RA[b] = ra, rb
	return nil
}

func (s *np1State) np1Is429(name string) error { s.script429For(name, ""); return nil }

func (s *np1State) np1PricesInOut(_, _, out, n1, in1, n2, in2 string) error {
	o, _ := strconv.ParseFloat(out, 64)
	i1, _ := strconv.ParseFloat(in1, 64)
	i2, _ := strconv.ParseFloat(in2, 64)
	s.setPrice(n1, i1, o)
	s.setPrice(n2, i2, o)
	return nil
}

func (s *np1State) np1Reliability(better string, others ...string) {
	s.setSuccess(s.st(better).id, 0.99)
	for _, n := range others {
		if n != better {
			s.setSuccess(s.st(n).id, 0.75) // still Tier A (>= 0.55), just less reliable
		}
	}
}

func (s *np1State) np1PriceIdenticallyBetter(a, b, better string) error {
	s.setPrice(a, 1, 1)
	s.setPrice(b, 1, 1)
	s.np1Reliability(better, a, b)
	return nil
}

func (s *np1State) np1ScheduledWindow(name, out string) error {
	p, _ := strconv.ParseFloat(out, 64)
	now := time.Now().UTC()
	win := protocol.PriceWindow{
		Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"),
		In: p / 10, Out: p,
	}
	s.setOffer(s.st(name).id, func(o *protocol.ModelOffer) { o.Schedule = []protocol.PriceWindow{win} })
	if _, o, _, sched := s.offerOf(name).ActivePrice(time.Now()); !sched || math.Abs(o-p) > 1e-9 {
		return fmt.Errorf("fixture: %s's window is not active now (out %v, scheduled %v)", name, o, sched)
	}
	return nil
}

func (s *np1State) np1TwoPricedOutOneCheaper(a, b, pab, c, pc string) error {
	for _, n := range []string{a, b} {
		if err := s.pricesOut(n, pab); err != nil {
			return err
		}
	}
	return s.pricesOut(c, pc)
}

func (s *np1State) np1TierB(name, rate string) error {
	r, _ := strconv.ParseFloat(rate, 64)
	s.setSuccess(s.st(name).id, r)
	return nil
}

func (s *np1State) np1AllTierB() error {
	for _, n := range []string{"s1", "s2", "s3"} {
		s.setSuccess(s.st(n).id, 0.30)
	}
	return nil
}

var (
	np1MeasureRe  = regexp.MustCompile(`"([^"]+)" (\d+)(ms)?(?:[,\s]|$)|"([^"]+)" unmeasured`)
	np1Also429Re  = regexp.MustCompile(`"([^"]+)" 429s`)
	np1BetterRe   = regexp.MustCompile(`"([^"]+)" has the better reliability`)
	np1PricesRe   = regexp.MustCompile(`"([^"]+)" prices out ([0-9.]+)`)
	np1BandPrice  = regexp.MustCompile(` \(out ([0-9.]+)\)`)
	np1BandMember = regexp.MustCompile(`"([^"]+)" \(out ([0-9.]+)\)`)
)

func (s *np1State) np1SetTTFT(name string, ms float64) {
	s.np1TTFTOnly = true
	s.b.mu.Lock()
	tq := s.b.trust[s.st(name).id]
	tq.ttftMs = ms
	s.b.trust[s.st(name).id] = tq
	s.b.mu.Unlock()
}

func (s *np1State) np1UnmeasureTPS(name string) {
	s.b.metricsMu.Lock()
	delete(s.b.tps, s.st(name).id)
	s.b.metricsMu.Unlock()
	delete(s.np1TPS, name)
}

// np1Measured parses every "measured tps|ttft ..." Given: the per-station measurements, then
// the optional riders (a 429ing station, a better reliability, a price).
func (s *np1State) np1Measured(metric, rest string) error {
	var named []string
	for _, m := range np1MeasureRe.FindAllStringSubmatch(rest, -1) {
		if m[4] != "" { // unmeasured
			named = append(named, m[4])
			if metric == "tps" {
				s.np1UnmeasureTPS(m[4])
			} else {
				s.np1SetTTFT(m[4], 0)
			}
			continue
		}
		v, _ := strconv.ParseFloat(m[2], 64)
		named = append(named, m[1])
		if metric == "tps" {
			s.setTPS(s.st(m[1]).id, v)
			s.np1TPS[m[1]] = v
		} else {
			s.np1SetTTFT(m[1], v)
		}
	}
	if len(named) == 0 {
		return fmt.Errorf("no measurement parsed from %q", rest)
	}
	if metric == "ttft" {
		// A station the Given does not name has NO ttft measurement: the harness stands every
		// station up with a 200 ms placeholder, which would outrank the stated values.
		for n := range s.stations {
			if !slices.Contains(named, n) {
				s.np1SetTTFT(n, 0)
			}
		}
	}
	if m := np1Also429Re.FindStringSubmatch(rest); m != nil {
		s.script429For(m[1], "")
	}
	if m := np1BetterRe.FindStringSubmatch(rest); m != nil {
		s.np1Reliability(m[1], named...)
	}
	if m := np1PricesRe.FindStringSubmatch(rest); m != nil {
		return s.pricesOut(m[1], m[2])
	}
	return nil
}

func (s *np1State) np1NoTPSAnywhere() error {
	for n := range s.stations {
		s.np1UnmeasureTPS(n)
	}
	return nil
}

func (s *np1State) np1StrictlyBest(name, metric string) {
	rank := []string{name}
	for _, n := range []string{"s1", "s2", "s3"} {
		if n != name {
			rank = append(rank, n)
		}
	}
	switch metric {
	case "price": // the Background already prices s1 < s2 < s3; restate it for any best
		for i, n := range rank {
			s.setPrice(n, float64(i+1), float64(i+1))
		}
	case "throughput":
		for i, n := range rank {
			v := []float64{90, 40, 20}[i]
			s.setTPS(s.st(n).id, v)
			s.np1TPS[n] = v
		}
	case "latency":
		for i, n := range rank {
			s.np1SetTTFT(n, []float64{100, 400, 800}[i])
		}
	}
}

func (s *np1State) np1BestAndIdle(name, metric string) error {
	s.np1StrictlyBest(name, metric)
	s.b.metricsMu.Lock()
	for _, st := range s.stations {
		s.b.inflight[st.id] = 0
	}
	s.b.metricsMu.Unlock()
	return nil
}

func (s *np1State) np1BestBut(name, metric, state string) error {
	s.np1StrictlyBest(name, metric)
	state = strings.TrimSpace(state)
	if state == "cooling" {
		return s.np1Cool(name, 15)
	}
	if state == "stale" {
		return s.stale(name)
	}
	return s.stationState(name, state)
}

func (s *np1State) np1FastestButTierB(name string) error {
	s.np1StrictlyBest(name, "throughput")
	s.setTPS(s.st("s2").id, 60)
	s.setTPS(s.st("s3").id, 40)
	s.np1TPS["s2"], s.np1TPS["s3"] = 60, 40
	s.setSuccess(s.st(name).id, 0.30)
	return nil
}

func (s *np1State) np1LacksTools(name string) error {
	for n, st := range s.stations {
		if n != name {
			s.b.recordToolProbe(st.id, s.model, true, false, true)
		}
	}
	return nil
}

func (s *np1State) np1BestReliability(name string) error {
	s.np1Reliability(name, "s1", "s2", "s3")
	return nil
}

func (s *np1State) np1CheapestIdle(name string) error { return s.np1BestAndIdle(name, "price") }
func (s *np1State) np1CheapestCooling(name string) error {
	s.np1StrictlyBest(name, "price")
	return s.np1Cool(name, 15)
}

func (s *np1State) np1AtCapacity(name string) error {
	s.b.metricsMu.Lock()
	s.b.inflight[s.st(name).id] = 64
	s.b.metricsMu.Unlock()
	return nil
}

func (s *np1State) np1EchoesOrder(name string) error {
	id := s.st(name).id
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"The answer is 4."}}],"usage":{"prompt_tokens":5000,"completion_tokens":20},"provider":{"order":[%q]}}`, id)
	})
	return nil
}

// np1PrivateBand wraps the slice-0 band fixture for the priced form
// `"p1" (out 2.00) and "p2" (out 1.00)`.
func (s *np1State) np1PrivateBand(band, names string) error {
	priced := np1BandMember.FindAllStringSubmatch(names, -1)
	if err := s.privateBand(band, np1BandPrice.ReplaceAllString(names, "")); err != nil {
		return err
	}
	for _, m := range priced {
		p, _ := strconv.ParseFloat(m[2], 64)
		s.setPrice(m[1], p, p)
	}
	return nil
}

// --- Given: grants -------------------------------------------------------------------

// np1Rehome puts station `name` under `owner`'s account. A node->account binding is TOFU in
// the store, so the station comes back on air under a fresh node id owned by `owner`; the
// scenario keeps calling it by its name.
func (s *np1State) np1Rehome(name string, owner *fstation) {
	old := s.st(name)
	if old.acct == owner.acct {
		return
	}
	s.b.mu.Lock()
	delete(s.b.nodes, old.id)
	delete(s.b.tunnels, old.id)
	delete(s.b.lastSeen, old.id)
	s.b.mu.Unlock()
	delete(s.stations, name)
	alias := name + "g"
	st := s.standUp(alias, stationOpts{model: s.model, priceIn: old.priceIn, priceOut: old.priceOut, owner: owner})
	delete(s.stations, alias)
	st.name = name
	s.stations[name] = st
	order := s.order[:0]
	for _, n := range s.order {
		if n != alias {
			order = append(order, n)
		}
	}
	s.order = order
	s.b.mu.Lock()
	s.b.trust[st.id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	s.b.mu.Unlock()
	s.scriptServes(name)
}

func (s *np1State) np1GrantScoped(list string) error {
	var names []string
	if err := json.Unmarshal([]byte(list), &names); err != nil || len(names) == 0 {
		return fmt.Errorf("bad grant node list %q", list)
	}
	owner := s.st(names[0])
	ids := []string{owner.id}
	for _, n := range names[1:] {
		s.np1Rehome(n, owner)
		ids = append(ids, s.st(n).id)
	}
	secret := "rog-grant_np1" + s.nonce
	sum := sha256.Sum256([]byte(secret))
	s.grantID, s.grantToken = "grant_np1_"+s.nonce, secret
	g := store.Grant{ID: s.grantID, SecretHash: hex.EncodeToString(sum[:]), Owner: owner.acct,
		Label: "np1", Free: true, Nodes: ids, DailyCap: 1_000_000, CreatedAt: time.Now().Unix()}
	if err := s.db.CreateGrant(g); err != nil {
		return err
	}
	return rs1GrantWalletRow(s.db, g) // see there: a free grant's wallet needs a row to settle on Postgres
}

func (s *np1State) np1GrantScopedAnd429(list, name string) error {
	if err := s.np1GrantScoped(list); err != nil {
		return err
	}
	s.script429For(name, "")
	return nil
}

// --- Given: towers -------------------------------------------------------------------

func (s *np1State) np1TowerHosts(name string) error {
	s.model = "m"
	_, err := s.standUpTower(name, "m", 0, 0, 0)
	s.heartbeat()
	return err
}

func (s *np1State) np1TowerHostsNoDirect(name string) error {
	if err := s.np1TowerHosts(name); err != nil {
		return err
	}
	for _, st := range s.stations {
		s.setOffer(st.id, func(o *protocol.ModelOffer) { o.Model = "other-model" })
	}
	return nil
}

// np1TowerPosesAs stands up an approved Tower whose routable row carries the DIRECT
// station's node id as its relay name (a Station id itself must be st-<hex>, so the row's
// node id is where a Tower can collide with the direct namespace). Its data plane is dead:
// only the admission of the row is under test, never its service.
func (s *np1State) np1TowerPosesAs(name string) error {
	if err := s.ensureTower(); err != nil {
		return err
	}
	direct := s.st(name)
	op := signedInOperator(s.t, s.b, "pose-"+name+"-op-"+s.nonce)
	lt := enrolledTower(s.t, s.b, op.login)
	if err := s.b.tower.registry.Transition(lt.id, admit.StateActive); err != nil {
		return err
	}
	stationID := "st-" + hex.EncodeToString([]byte("pose|"+name+"|"+s.nonce))
	attachStation(s.t, s.b, stationID, lt.id, op.login)
	if err := s.b.tower.routable.Replace(lt.id, []fleet.Station{{
		TowerID: lt.id, StationID: stationID, OfferID: "self-" + stationID,
		Model: s.model, Modality: "text",
		Expires: time.Now().Add(time.Hour), Endpoint: "127.0.0.1:1",
		NodeID: direct.id, PriceIn: 1_000_000, PriceOut: 1_000_000,
	}}); err != nil {
		return err
	}
	s.np1Collision, s.np1PoseTower = name, lt.id
	s.heartbeat()
	return nil
}

// --- Given: telemetry ----------------------------------------------------------------

func (s *np1State) np1TelemetryRelays(orders, sorts, refused string) error {
	fire := func(n, frag string) error {
		count, _ := strconv.Atoi(n)
		s.bodyFrag = frag
		for i := 0; i < count; i++ {
			if err := s.np1Fire(); err != nil {
				return err
			}
		}
		return nil
	}
	if err := fire(orders, `"provider": {"order": [`+strconv.Quote(s.idOf("s1"))+`]}`); err != nil {
		return err
	}
	if err := fire(sorts, `"provider": {"sort": "price"}`); err != nil {
		return err
	}
	return fire(refused, `"provider": {"order": ["s9"], "allow_fallbacks": false}`)
}

func (s *np1State) np1ReadsAdminLive() error { return s.readAdminLive() }

func (s *np1State) np1OperatorRegisters(_ string, out string) error {
	p, _ := strconv.ParseFloat(out, 64)
	return s.nodeRegistersAt(s.model, p)
}

// --- Then: helpers -------------------------------------------------------------------

func (s *np1State) np1Counter(key string) (float64, bool) {
	if err := s.readAdminLive(); err != nil {
		return 0, false
	}
	v, ok := s.adminResp[key].(float64)
	return v, ok
}

func (s *np1State) np1AllOK(batch []rpResult) error {
	if len(batch) == 0 {
		return fmt.Errorf("no relay fired")
	}
	for i, r := range batch {
		if r.code != 200 {
			return fmt.Errorf("relay %d of %d: status %d (%s)", i+1, len(batch), r.code, r.body)
		}
	}
	return nil
}

func (s *np1State) np1Hist(batch []rpResult) map[string]int {
	h := map[string]int{}
	for _, r := range batch {
		if r.code == 200 {
			h[s.nameOfID(r.hdr.Get("X-RogerAI-Provider"))]++
		}
	}
	return h
}

// np1Share is the pick share of every station over 2000 seeded draws of the REAL pickFor
// under `pref` (and an optional allow-list), same registry, same seeds for every pref.
func (s *np1State) np1Share(pref string, allow map[string]bool) map[string]float64 {
	const n = 2000
	counts := map[string]int{}
	tokens := approxPromptTokens(s.requestBody(false))
	for i := 0; i < n; i++ {
		s.b.mu.Lock()
		node, _, ok := s.b.pickFor(s.model, false, 0, 0, effectiveRelayMaxOut(0), "", nil, allow, nil,
			pickReq{pref: parsePref(pref), promptTokens: tokens, rng: mrand.New(mrand.NewSource(int64(i + 1)))})
		s.b.mu.Unlock()
		if ok {
			counts[s.nameOfID(node.NodeID)]++
		}
	}
	out := map[string]float64{}
	for k, v := range counts {
		out[k] = float64(v) / n
	}
	return out
}

func (s *np1State) np1LogsSince(mark int) string {
	all := s.logs.String()
	if mark > len(all) {
		mark = 0
	}
	return all[mark:]
}

func (s *np1State) np1BridgeEntered() error {
	for i, r := range s.batch {
		if relay := r.hdr.Get("X-RogerAI-Relay"); relay != "" {
			return fmt.Errorf("relay %d rode Tower %q", i+1, relay)
		}
	}
	if logs := s.np1LogsSince(s.np1LogMark); strings.Contains(logs, "edge bridge:") || strings.Contains(logs, "edge pick") {
		return fmt.Errorf("the bridge was entered: %s", np1Tail(logs, 600))
	}
	return nil
}

func np1Tail(text string, n int) string {
	if len(text) > n {
		return text[len(text)-n:]
	}
	return text
}

func (s *np1State) np1Tower(name string) (*rpTower, error) {
	tw, ok := s.towers[name]
	if !ok {
		return nil, fmt.Errorf("no Tower %q in this scenario", name)
	}
	return tw, nil
}

// np1Refire sends the current request once more (same shaping) after resetting the upstream
// hit record, so a Then step can read the plan the broker walks.
func (s *np1State) np1Refire() error {
	if err := s.snapshotNow(); err != nil {
		return err
	}
	return s.np1Fire()
}

// --- Then ----------------------------------------------------------------------------

func (s *np1State) np1ProviderNames(name string) error {
	if got := s.provider(); got != s.idOf(name) {
		return fmt.Errorf("X-RogerAI-Provider %q, want %q's id %q (status %d %s)", got, name, s.idOf(name), s.lastCode, s.lastBody)
	}
	return nil
}

var np1NameInMsg = regexp.MustCompile(`\b(s[0-9]|p[0-9]|f[0-9]|t[0-9])\b`)

func (s *np1State) np1ResponseCodeMessage(status, code, msg string) error {
	if err := s.responseIsWithCode(status, code); err != nil {
		return err
	}
	_, got := s.errBody()
	withIDs := np1NameInMsg.ReplaceAllStringFunc(msg, func(n string) string { return s.idOf(n) })
	if got != msg && got != withIDs {
		return fmt.Errorf("error.message %q, want %q", got, msg)
	}
	return nil
}

func (s *np1State) np1CostZeroNoReceipt() error {
	if got := s.lastHdr.Get("X-RogerAI-Cost"); got != "0" {
		return fmt.Errorf("X-RogerAI-Cost %q, want \"0\" (status %d %s)", got, s.lastCode, s.lastBody)
	}
	if rec := s.lastHdr.Get("X-RogerAI-Receipt"); rec != "" {
		return fmt.Errorf("the response carries a receipt")
	}
	rc, err := s.receiptCount()
	if err != nil {
		return err
	}
	if rc != s.np1Before.receipts {
		return fmt.Errorf("%d receipt(s) were written, want none", rc-s.np1Before.receipts)
	}
	return nil
}

func (s *np1State) np1HoldReleasedInFull() error {
	h, _, sp, _, err := s.holdRows()
	if err != nil {
		return err
	}
	if h-s.np1Before.holds < 1 {
		return fmt.Errorf("no hold was placed, so nothing was released (status %d %s)", s.lastCode, s.lastBody)
	}
	if sp != s.np1Before.spends {
		return fmt.Errorf("%d charge row(s) were written; the hold was not released in full", sp-s.np1Before.spends)
	}
	return s.np1BalanceUnchanged()
}

func (s *np1State) np1BalanceUnchanged() error {
	bal, err := s.balance()
	if err != nil {
		return err
	}
	if math.Abs(bal-s.np1Before.balance) > 1e-9 {
		return fmt.Errorf("balance %v, want the pre-relay %v", bal, s.np1Before.balance)
	}
	return nil
}

func (s *np1State) np1NoReveal(name string) error {
	body := strings.ToLower(string(s.lastBody))
	for _, leak := range []string{strings.ToLower(s.idOf(name)), "private", "band", "frequency"} {
		if strings.Contains(body, leak) {
			return fmt.Errorf("the refusal reveals %q: %s", leak, s.lastBody)
		}
	}
	return nil
}

func (s *np1State) np1GrantOwnerMessage() error {
	_, msg := s.errBody()
	if !strings.Contains(msg, "grant") || !strings.Contains(msg, "owner") {
		return fmt.Errorf("message %q does not say no node of this grant's owner matches", msg)
	}
	return nil
}

func (s *np1State) np1ConfidentialMessage() error {
	if _, msg := s.errBody(); !strings.Contains(msg, "confidential") {
		return fmt.Errorf("message %q does not say no confidential station matches", msg)
	}
	return nil
}

func (s *np1State) np1RelayOnlyIsNoMatch(list string) error {
	s.bodyFrag = `"provider": {"only": ` + s.listValue(list) + `}`
	if err := s.np1Fire(); err != nil {
		return err
	}
	return s.responseIsWithCode("503", "no_match")
}

func (s *np1State) np1CtxRefusal(text, window string) error {
	if err := s.responseIs("400"); err != nil {
		return err
	}
	_, msg := s.errBody()
	if msg == "" {
		msg = string(s.lastBody)
	}
	if !strings.Contains(msg, strings.TrimPrefix(text, "request ")) || !strings.Contains(msg, window) {
		return fmt.Errorf("message %q does not say %q naming %s", msg, text, window)
	}
	return nil
}

func (s *np1State) np1NothingAndNoHold(name string) error {
	if err := s.receivedNothing(name); err != nil {
		return err
	}
	return s.noHold()
}

func (s *np1State) np1NotStruck(name string) error {
	strikes, err := s.strikesOf(name)
	if err != nil {
		return err
	}
	if len(strikes) != 0 {
		return fmt.Errorf("%q's operator carries %d strike(s), want none", name, len(strikes))
	}
	return nil
}

func (s *np1State) np1Response401(msg string) error {
	if err := s.responseIs("401"); err != nil {
		return err
	}
	if !strings.Contains(string(s.lastBody), msg) {
		return fmt.Errorf("body %s does not say %q", s.lastBody, msg)
	}
	return nil
}

func (s *np1State) np1AnonNoHoldCostZero() error {
	if err := s.noHold(); err != nil {
		return err
	}
	anon := protocol.UserIDFromPubkey(hex.EncodeToString(s.anonPriv.Public().(ed25519.PublicKey)))
	rows, err := s.db.LedgerOf(anon, []string{store.KindHold}, 100)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return fmt.Errorf("the anonymous wallet carries %d hold row(s), want none", len(rows))
	}
	if got := s.lastHdr.Get("X-RogerAI-Cost"); got != "0" {
		return fmt.Errorf("X-RogerAI-Cost %q, want \"0\"", got)
	}
	return nil
}

func (s *np1State) np1BothPicked(a, b string) error {
	if err := s.np1AllOK(s.batch); err != nil {
		return err
	}
	h := s.np1Hist(s.batch)
	if h[a] == 0 || h[b] == 0 {
		return fmt.Errorf("picks %v: want both %q and %q picked (P2C spreads the remainder)", h, a, b)
	}
	return nil
}

func (s *np1State) np1NoAttemptTo(name string) error { return s.receivedNothing(name) }

func (s *np1State) np1AttemptsToBoth(a, b string) error {
	hits := s.hitList()
	if len(hits) != 2 || hits[0] == hits[1] || (hits[0] != a && hits[0] != b) || (hits[1] != a && hits[1] != b) {
		return fmt.Errorf("attempts %v, want exactly %q and %q once each (status %d %s)", hits, a, b, s.lastCode, s.lastBody)
	}
	return nil
}

func (s *np1State) np1LastUpstreamRetryAfter() error {
	if err := s.responseIs("429"); err != nil {
		return err
	}
	hits := s.hitList()
	if len(hits) == 0 {
		return fmt.Errorf("no upstream was hit")
	}
	want := s.np1RA[hits[len(hits)-1]]
	if got := s.lastHdr.Get("Retry-After"); got != want {
		return fmt.Errorf("Retry-After %q, want the last upstream's (%s) %q", got, hits[len(hits)-1], want)
	}
	return nil
}

func (s *np1State) np1VoidedReceipt(name string) error {
	reason, reqID, err := s.voidReasonOf(name)
	if err != nil {
		return fmt.Errorf("no voided receipt in %q's chain: %w", name, err)
	}
	if reason == "" {
		return fmt.Errorf("receipt %s in %q's chain carries no void_reason", reqID, name)
	}
	es, err := s.entriesOf(name)
	if err != nil {
		return err
	}
	if es[0].Cost != 0 {
		return fmt.Errorf("receipt %s cost %v, want a $0 void", reqID, es[0].Cost)
	}
	return nil
}

func (s *np1State) np1GrantOneAttempt() error {
	u, err := s.db.GrantUsageOf(s.grantID, time.Now())
	if err != nil {
		return err
	}
	if hits := s.hitList(); len(hits) != 1 {
		return fmt.Errorf("attempts %v, want exactly one under the grant", hits)
	}
	// One attempt that produced no output debits nothing; two attempts with a served second
	// would have debited the served attempt's tokens.
	if u.DayTokens != 0 {
		return fmt.Errorf("grant day tokens %d after one voided attempt, want 0", u.DayTokens)
	}
	return nil
}

func (s *np1State) np1StreamRefusedNoSSE() error {
	if err := s.responseHasRetryAfter("429"); err != nil {
		return err
	}
	if ct := s.lastHdr.Get("Content-Type"); strings.Contains(ct, "event-stream") {
		return fmt.Errorf("SSE headers were committed (Content-Type %q)", ct)
	}
	s.lastRec.mu.Lock()
	statuses := append([]int(nil), s.lastRec.statuses...)
	s.lastRec.mu.Unlock()
	if len(statuses) != 1 || statuses[0] != 429 {
		return fmt.Errorf("statuses written %v, want exactly [429]", statuses)
	}
	return nil
}

// np1PlanOrder re-fires the current request with every station answering 429 and returns the
// order the broker walked them in.
func (s *np1State) np1PlanOrder() ([]string, error) {
	for n := range s.stations {
		s.script429For(n, "")
	}
	if err := s.np1Refire(); err != nil {
		return nil, err
	}
	return s.hitList(), nil
}

func (s *np1State) np1PlannedAfter(later, earlier string) error {
	plan, err := s.np1PlanOrder()
	if err != nil {
		return err
	}
	pos := map[string]int{}
	for i, n := range plan {
		if _, seen := pos[n]; !seen {
			pos[n] = i + 1
		}
	}
	if pos[earlier] == 0 || pos[later] == 0 || pos[later] < pos[earlier] {
		return fmt.Errorf("plan %v: want %q after %q (status %d %s)", plan, later, earlier, s.lastCode, s.lastBody)
	}
	return nil
}

func (s *np1State) np1NotInPlan(name string) error {
	if err := s.np1AllOK(s.batch); err != nil {
		return err
	}
	plan, err := s.np1PlanOrder()
	if err != nil {
		return err
	}
	for _, n := range plan {
		if n == name {
			return fmt.Errorf("plan %v contains %q", plan, name)
		}
	}
	if len(plan) == 0 {
		return fmt.Errorf("the plan is empty (status %d %s)", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *np1State) np1PicksFollowScore() error {
	if err := s.np1AllOK(s.batch); err != nil {
		return err
	}
	// The score order the batch was routed under: taken at the Given state, BEFORE the batch
	// (like every other share in this runner) - the batch's own relays re-measure the station
	// that served and shrink its exploration lift, which is not what the picks were made on.
	share := s.np1Shares["balanced"]
	if share == nil {
		share = s.np1Share("balanced", nil)
	}
	best, top := "", -1.0
	for n, v := range share {
		if v > top {
			best, top = n, v
		}
	}
	h := s.np1Hist(s.batch)
	if len(h) != 1 || h[best] != len(s.batch) {
		return fmt.Errorf("picks %v, want every pick on the top-scored station %q (a strict sort with no measurement falls back to score)", h, best)
	}
	return nil
}

func (s *np1State) np1PickIsNot(name string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d, want 200 (%s)", s.lastCode, s.lastBody)
	}
	if s.provider() == s.idOf(name) {
		return fmt.Errorf("the pick is %q", name)
	}
	return nil
}

func (s *np1State) np1EveryPickFaster(a, b string) error {
	faster := a
	if s.np1TPS[b] > s.np1TPS[a] {
		faster = b
	}
	return s.everyPick(faster)
}

func (s *np1State) np1NoOfferOverCeiling() error {
	ceil := maxPriceOutCeiling()
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for id, reg := range s.b.nodes {
		for _, o := range reg.Offers {
			if o.PriceOut > ceil {
				return fmt.Errorf("%s offers out %v above the %v ceiling", id, o.PriceOut, ceil)
			}
		}
	}
	return nil
}

func (s *np1State) np1SortPriceCapPicks(name string) error {
	s.bodyFrag = `"provider": {"sort": "price", "max_price": {"completion": 100}}`
	if err := s.np1Fire(); err != nil {
		return err
	}
	return s.pickIs(name)
}

func (s *np1State) np1DistributionsMatch() error {
	if err := s.np1AllOK(s.np1Prev); err != nil {
		return fmt.Errorf("first batch: %w", err)
	}
	if err := s.np1AllOK(s.batch); err != nil {
		return fmt.Errorf("second batch: %w", err)
	}
	// The two batches cannot be compared pick-for-pick: they run one after the other, and every
	// served relay re-measures its station's tps, success and load, so the second batch scores a
	// different fleet than the first (an empirical share gap here measures that drift, not the
	// pref). The claim is proved exactly instead, in two parts:
	//  1. on the REAL relay path, every relay of BOTH batches ran the balanced profile (the
	//     broker's per-pass counter: the no-routing-object batch is counted as balanced, and no
	//     other profile ran at all);
	//  2. the scoring function under "no pref" and under "balanced" is the same function: 2000
	//     seeded pickFor draws, same seeds, at the frozen Given state, give identical shares.
	total := len(s.np1Prev) + len(s.batch)
	if v, ok := s.np1Counter("routing_pref_balanced"); !ok || int(v) != total {
		return fmt.Errorf("/admin/live routing_pref_balanced = %v (present %v), want exactly %d: both batches must run the balanced profile", v, ok, total)
	}
	for _, other := range []string{"cheap", "fast", "reliable"} {
		if v, ok := s.np1Counter("routing_pref_" + other); ok && v != 0 {
			return fmt.Errorf("/admin/live routing_pref_%s = %v, want 0: a relay with no routing object ran another profile", other, v)
		}
	}
	none, bal := s.np1Shares[""], s.np1Shares["balanced"]
	for n := range s.stations {
		if math.Abs(none[n]-bal[n]) > 1e-9 {
			return fmt.Errorf("%q seeded share %.4f with no pref vs %.4f under balanced", n, none[n], bal[n])
		}
	}
	return nil
}

func (s *np1State) np1RanUnder(pref string, atLeast int) error {
	v, ok := s.np1Counter("routing_pref_" + pref)
	if !ok || int(v) < atLeast {
		return fmt.Errorf("/admin/live routing_pref_%s = %v (present %v), want >= %d: the batch did not run under %q", pref, v, ok, atLeast, pref)
	}
	return nil
}

func (s *np1State) np1PickedMoreThanUnder(name, baseline string) error {
	if err := s.np1AllOK(s.batch); err != nil {
		return err
	}
	pref := s.np1Pref
	if pref == "" {
		return fmt.Errorf("the batch carried no pref")
	}
	if err := s.np1RanUnder(pref, len(s.batch)); err != nil {
		return err
	}
	if other, ok := s.np1Counter("routing_pref_" + baseline); pref != baseline && baseline != "balanced" && ok && other > 0 {
		return fmt.Errorf("routing_pref_%s = %v, but every relay asked for %q", baseline, other, pref)
	}
	if s.np1Shares == nil {
		return fmt.Errorf("no pre-batch pick shares were taken")
	}
	with, base := s.np1Shares[pref], s.np1Shares[baseline]
	if with[name] <= base[name] {
		return fmt.Errorf("%q seeded pick share %.4f under %q, %.4f under %q (2000 draws at the Given state): not picked more often", name, with[name], pref, base[name], baseline)
	}
	return nil
}

func (s *np1State) np1NotPickedNTimes(name, n string) error {
	if err := s.np1AllOK(s.batch); err != nil {
		return err
	}
	want, _ := strconv.Atoi(n)
	if got := s.np1Hist(s.batch)[name]; got >= want {
		return fmt.Errorf("%q was picked %d of %d times: a weighted pref must keep the power-of-two-choices spread", name, got, len(s.batch))
	}
	return nil
}

func (s *np1State) np1ServedUnderBalanced() error {
	if err := s.responseIs("200"); err != nil {
		return err
	}
	if err := s.np1RanUnder("balanced", 1); err != nil {
		return err
	}
	for _, other := range []string{"cheap", "fast", "reliable"} {
		if v, ok := s.np1Counter("routing_pref_" + other); ok && v > 0 {
			return fmt.Errorf("routing_pref_%s = %v after a request with an unknown header pref", other, v)
		}
	}
	return nil
}

func (s *np1State) np1PrefHeaderUnknownCounter() error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	for _, key := range []string{"pref_header_unknown", "routing_pref_header_unknown"} {
		if v, ok := s.adminResp[key].(float64); ok && v >= 1 {
			return nil
		}
	}
	if routing, _ := s.adminResp["routing"].(map[string]any); routing != nil {
		if v, ok := routing["pref_header_unknown"].(float64); ok && v >= 1 {
			return nil
		}
	}
	return fmt.Errorf("/admin/live carries no pref_header_unknown counter >= 1")
}

func (s *np1State) np1Response400Code(code string) error { return s.responseIsWithCode("400", code) }

func (s *np1State) np1SecondAttemptUnderCheap(first string) error {
	hits := s.hitList()
	if len(hits) != 2 || hits[0] != first {
		return fmt.Errorf("attempts %v, want [%s, <remainder>] (status %d %s)", hits, first, s.lastCode, s.lastBody)
	}
	if err := s.np1RanUnder("cheap", 1); err != nil {
		return err
	}
	// The remainder is a two-member pool at equal load, which power-of-two-choices decides by
	// score: under cheap that is the cheaper of the two.
	rest := []string{}
	for _, n := range []string{"s1", "s2", "s3"} {
		if n != first {
			rest = append(rest, n)
		}
	}
	cheaper := rest[0]
	if s.offerOf(rest[1]).PriceOut < s.offerOf(rest[0]).PriceOut {
		cheaper = rest[1]
	}
	if hits[1] != cheaper {
		return fmt.Errorf("attempt 2 hit %q, want the cheaper remainder %q under pref cheap", hits[1], cheaper)
	}
	return nil
}

func (s *np1State) np1PickedMoreThan(a, b string) error {
	if err := s.np1AllOK(s.batch); err != nil {
		return err
	}
	h := s.np1Hist(s.batch)
	if h[a] <= h[b] {
		return fmt.Errorf("picks %v: want %q more often than %q", h, a, b)
	}
	if h[a]+h[b] != len(s.batch) {
		return fmt.Errorf("picks %v: a station outside the only set served", h)
	}
	if s.np1Pref != "" && s.np1Pref != "balanced" {
		return s.np1RanUnder(s.np1Pref, len(s.batch))
	}
	return nil
}

func (s *np1State) np1EveryRelayOneAttempt() error {
	if len(s.np1Attempts) == 0 {
		return fmt.Errorf("no relay fired")
	}
	for i, n := range s.np1Attempts {
		if n != 1 {
			r := s.batch[i]
			var who []string
			if i < len(s.np1AttemptHits) {
				who = s.np1AttemptHits[i]
			}
			return fmt.Errorf("relay %d made %d attempt(s), want exactly one (status %d %s; jobs %v)", i+1, n, r.code, r.body, who)
		}
	}
	return nil
}

func (s *np1State) np1BridgeServesThrough(name string) error {
	tw, err := s.np1Tower(name)
	if err != nil {
		return err
	}
	if s.lastCode != 200 {
		return fmt.Errorf("status %d, want 200 through Tower %s (%s)", s.lastCode, name, s.lastBody)
	}
	if got := s.lastHdr.Get("X-RogerAI-Relay"); got != tw.id {
		return fmt.Errorf("X-RogerAI-Relay %q, want Tower %s (%s)", got, name, tw.id)
	}
	return nil
}

func (s *np1State) np1EveryRelayThrough(name string) error {
	tw, err := s.np1Tower(name)
	if err != nil {
		return err
	}
	if err := s.np1AllOK(s.batch); err != nil {
		return err
	}
	for i, r := range s.batch {
		if got := r.hdr.Get("X-RogerAI-Relay"); got != tw.id {
			return fmt.Errorf("relay %d: X-RogerAI-Relay %q (provider %q), want Tower %s", i+1, got, s.nameOfID(r.hdr.Get("X-RogerAI-Provider")), name)
		}
	}
	return nil
}

func (s *np1State) np1EveryPickNoBridge(name string) error {
	if err := s.everyPick(name); err != nil {
		return err
	}
	return s.np1BridgeEntered()
}

func (s *np1State) np1PickIsDirect(name string) error {
	if err := s.pickIs(name); err != nil {
		return err
	}
	if relay := s.lastHdr.Get("X-RogerAI-Relay"); relay != "" {
		return fmt.Errorf("served through Tower %q, want the direct station %q", relay, name)
	}
	return nil
}

func (s *np1State) np1EitherDirectOrTowerRow(direct, _ string) error {
	if err := s.np1AllOK(s.batch); err != nil {
		return err
	}
	for i, r := range s.batch {
		relay, prov := r.hdr.Get("X-RogerAI-Relay"), r.hdr.Get("X-RogerAI-Provider")
		if relay == "" && prov == s.idOf(direct) {
			continue
		}
		if relay != "" && relay == s.np1PoseTower {
			continue
		}
		return fmt.Errorf("relay %d was served by provider %q relay %q: neither the direct %q nor the Tower row named %q", i+1, s.nameOfID(prov), relay, direct, direct)
	}
	return nil
}

func (s *np1State) np1CollisionWarnedOnce() error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	raw, _ := json.Marshal(s.adminResp)
	id := s.idOf(s.np1Collision)
	for _, key := range []string{"routing_id_collisions", "id_collisions"} {
		if v, ok := s.adminResp[key].(float64); ok {
			if v != 1 {
				return fmt.Errorf("/admin/live %s = %v, want exactly 1", key, v)
			}
			return nil
		}
	}
	if n := strings.Count(string(raw), id); strings.Contains(string(raw), "collision") && n == 1 {
		return nil
	}
	return fmt.Errorf("/admin/live carries no single id-collision warning for %q", id)
}

func (s *np1State) np1EffectiveSelection(effective string) error {
	switch strings.TrimSpace(effective) {
	case "order s3, fallbacks on":
		if err := s.pickIs("s3"); err != nil {
			return err
		}
		return s.fallbacksAllowed()
	case "s1 first, fallbacks on":
		if err := s.pickIs("s1"); err != nil {
			return err
		}
		return s.fallbacksAllowed()
	case "pin s1 kept (it is in only)":
		if err := s.pickIs("s1"); err != nil {
			return err
		}
		// the header's implied no-fallback is kept too: with s1 refusing, one attempt, no other
		s.script429For("s1", "")
		if err := s.np1Refire(); err != nil {
			return err
		}
		if hits := s.hitList(); s.lastCode != 429 || len(hits) != 1 || hits[0] != "s1" {
			return fmt.Errorf("with s1 429ing: status %d attempts %v, want a single 429 from the kept pin", s.lastCode, hits)
		}
		return nil
	case "ignore s1 and s2 (union)":
		return s.pickIs("s3")
	case "fast":
		if err := s.responseIs("200"); err != nil {
			return err
		}
		if err := s.np1RanUnder("fast", 1); err != nil {
			return err
		}
		if v, ok := s.np1Counter("routing_pref_cheap"); ok && v > 0 {
			return fmt.Errorf("routing_pref_cheap = %v: the header pref was used although the body named fast", v)
		}
		return nil
	case "sort latency":
		if err := s.responseIs("200"); err != nil {
			return err
		}
		if v, ok := s.np1Counter("routing_strict_sort"); !ok || v < 1 {
			return fmt.Errorf("/admin/live routing_strict_sort = %v (present %v), want >= 1", v, ok)
		}
		if v, ok := s.np1Counter("routing_pref_cheap"); ok && v > 0 {
			return fmt.Errorf("routing_pref_cheap = %v: the header pref was applied beside a body sort", v)
		}
		return nil
	}
	return fmt.Errorf("unknown effective selection %q", effective)
}

// np1NoErrorReturned judges the scenario's OWN request (the first response); the effective-
// selection step may have re-fired it against a refusing station since.
func (s *np1State) np1NoErrorReturned() error {
	if len(s.np1All) == 0 {
		return fmt.Errorf("no relay fired")
	}
	if first := s.np1All[0]; first.code != 200 {
		return fmt.Errorf("the request returned %d: %s", first.code, first.body)
	}
	return nil
}

func (s *np1State) np1StationBody(name string) ([]byte, error) {
	s.bodiesMu.Lock()
	defer s.bodiesMu.Unlock()
	b := s.bodies[name]
	if b == nil {
		return nil, fmt.Errorf("%q received no body (status %d %s)", name, s.lastCode, s.lastBody)
	}
	return b, nil
}

func (s *np1State) np1BodyHasNoCarriers(name string) error {
	body, err := s.np1StationBody(name)
	if err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return fmt.Errorf("station body is not a JSON object: %v", err)
	}
	for _, k := range []string{"provider", "roger", "models"} {
		if _, has := m[k]; has {
			return fmt.Errorf("%q received carrier %q: %s", name, k, body)
		}
	}
	return nil
}

func (s *np1State) np1BodyOtherwiseIdentical(name string) error {
	body, err := s.np1StationBody(name)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace(s.np1Base)) {
		return fmt.Errorf("%q received\n%s\nwant the consumer's body without the carriers\n%s", name, body, s.np1Base)
	}
	return nil
}

// np1BodyIdenticalButMax: the station's body minus the default max_tokens the broker adds to a
// body that states no output limit (contract §14.11) is the consumer's body without carriers.
func (s *np1State) np1BodyIdenticalButMax(name string) error {
	body, err := s.np1StationBody(name)
	if err != nil {
		return err
	}
	if !bytes.Contains(body, []byte(`"max_tokens"`)) {
		return fmt.Errorf("%q received no default max_tokens: %s", name, body)
	}
	got := rewriteBodyDrop(body, map[string]bool{"max_tokens": true}, nil)
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(s.np1Base)) {
		return fmt.Errorf("%q received\n%s\nwant the consumer's body without the carriers (plus max_tokens)\n%s", name, body, s.np1Base)
	}
	return nil
}

func (s *np1State) np1ShowsCounters(order, srt, refused string) error {
	for key, want := range map[string]string{"routing_strict_order": order, "routing_strict_sort": srt, "routing_nofallback_refused": refused} {
		w, _ := strconv.Atoi(want)
		v, ok := s.adminResp[key].(float64)
		if !ok || int(v) != w {
			return fmt.Errorf("/admin/live %s = %v (present %v), want %d", key, s.adminResp[key], ok, w)
		}
	}
	return nil
}

func (s *np1State) np1OneLogLine(reason, name string) error {
	if s.lastCode != 200 {
		return fmt.Errorf("status %d, want 200 (%s)", s.lastCode, s.lastBody)
	}
	req := s.lastHdr.Get("X-RogerAI-Request-Id") // the line names the request, never an attempt
	if req == "" {
		return fmt.Errorf("the response names no request id")
	}
	id, n := s.idOf(name), 0
	for _, line := range strings.Split(s.np1LogsSince(s.np1LogMark), "\n") {
		if strings.Contains(line, req) && strings.Contains(line, reason) && strings.Contains(line, id) {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d log line(s) name request %s, %q and %q, want exactly one", n, req, reason, name)
	}
	return nil
}

func (s *np1State) np1NoBandCodeInLogs() error {
	logs := s.logs.String()
	for band, code := range s.bands {
		if code == "" {
			continue
		}
		secret := code[strings.LastIndex(code, " ")+1:]
		if strings.Contains(logs, code) || strings.Contains(logs, secret) {
			return fmt.Errorf("a log line contains band %q's code", band)
		}
	}
	if len(s.bands) == 0 {
		return fmt.Errorf("no band was tuned in this scenario")
	}
	// and the relay really ran: a refused-as-unsupported request never reaches a log site
	if s.lastCode != 200 {
		return fmt.Errorf("the band relay was %d, want it served so its log lines exist (%s)", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *np1State) np1NoHoldReceiptStrike() error {
	if err := s.noHold(); err != nil {
		return err
	}
	rc, err := s.receiptCount()
	if err != nil {
		return err
	}
	if rc != s.np1Before.receipts {
		return fmt.Errorf("%d receipt(s) written, want none", rc-s.np1Before.receipts)
	}
	for n := range s.stations {
		if err := s.np1NotStruck(n); err != nil {
			return err
		}
	}
	return nil
}

func (s *np1State) np1BUnaffected() error {
	if len(s.np1BResults) == 0 {
		return fmt.Errorf("consumer B never relayed")
	}
	// B relays twenty more times with no routing object. A strict order is deterministic:
	// a B steered by A's order lands on s3 EVERY time; a B routed by score spreads (or sits
	// on the cheapest), so "by score" is observed as "not pinned to the station A ordered",
	// with B's pool unchanged (every Background station still a candidate for B).
	s.bodyFrag = ""
	for i := 0; i < 20; i++ {
		if err := s.np1Fire(); err != nil {
			return err
		}
		s.np1BResults = append(s.np1BResults, s.batch[len(s.batch)-1])
	}
	onS3 := 0
	for i, r := range s.np1BResults {
		if r.code != 200 {
			return fmt.Errorf("B's relay %d: status %d (%s)", i+1, r.code, r.body)
		}
		if r.hdr.Get("X-RogerAI-Provider") == s.idOf("s3") {
			onS3++
		}
	}
	if onS3 == len(s.np1BResults) {
		return fmt.Errorf("all %d of B's relays landed on s3, the station A ordered", onS3)
	}
	for _, n := range []string{"s1", "s2", "s3"} {
		if !s.candidateUnder(s.st(n).id, nil) {
			return fmt.Errorf("%q is no longer a candidate for B", n)
		}
	}
	// when the strict-order counter exists, only A's relay may have counted
	if v, ok := s.np1Counter("routing_strict_order"); ok && v != 1 {
		return fmt.Errorf("/admin/live routing_strict_order = %v, want exactly 1 (A's relay only)", v)
	}
	return nil
}

func (s *np1State) np1SecondPickByScore() error {
	if err := s.np1AllOK(s.np1Later); err != nil {
		return err
	}
	h := s.np1Hist(s.np1Later)
	if h["s1"] == len(s.np1Later) {
		return fmt.Errorf("all %d later picks are s1: the echoed order steered them (%v)", len(s.np1Later), h)
	}
	if v, ok := s.np1Counter("routing_strict_order"); ok && v > 0 {
		return fmt.Errorf("/admin/live routing_strict_order = %v although no consumer sent an order", v)
	}
	return nil
}

func (s *np1State) np1EveryDispatchedTo(name string) error { return s.everyPick(name) }

func (s *np1State) np1LoadFactorReported(name string) error {
	if err := s.readAdminLive(); err != nil {
		return err
	}
	id := s.idOf(name)
	var find func(v any) bool
	find = func(v any) bool {
		switch t := v.(type) {
		case map[string]any:
			_, hasLoad := t["load_factor"]
			if nid, _ := t["node_id"].(string); hasLoad && (nid == id || t["node"] == id) {
				return true
			}
			if sub, ok := t[id].(map[string]any); ok {
				if _, has := sub["load_factor"]; has {
					return true
				}
			}
			for _, c := range t {
				if find(c) {
					return true
				}
			}
		case []any:
			for _, c := range t {
				if find(c) {
					return true
				}
			}
		}
		return false
	}
	if !find(s.adminResp) {
		return fmt.Errorf("/admin/live reports no load_factor for %q", name)
	}
	return nil
}

func (s *np1State) np1BothNoMatchIdentical() error {
	if len(s.np1All) < 2 {
		return fmt.Errorf("%d response(s), want two", len(s.np1All))
	}
	a, b := s.np1All[len(s.np1All)-2], s.np1All[len(s.np1All)-1]
	for i, r := range []rpResult{a, b} {
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(r.body, &env)
		if r.code != 503 || env.Error.Code != "no_match" {
			return fmt.Errorf("response %d: %d %s, want 503 no_match", i+1, r.code, r.body)
		}
	}
	if !sameApartFromRequestID(a.body, b.body) {
		return fmt.Errorf("bodies differ:\n%s\n%s", a.body, b.body)
	}
	return nil
}

func (s *np1State) np1SameConstantWork() error {
	if len(s.np1All) < 2 || len(s.np1LogSpans) < 2 {
		return fmt.Errorf("two refusals are needed")
	}
	a, b := s.np1All[len(s.np1All)-2], s.np1All[len(s.np1All)-1]
	keys := func(h http.Header) string {
		var ks []string
		for k := range h {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	if a.code != b.code || keys(a.hdr) != keys(b.hdr) {
		return fmt.Errorf("the refusals differ in shape: %d [%s] vs %d [%s]", a.code, keys(a.hdr), b.code, keys(b.hdr))
	}
	all := s.logs.String()
	lines := func(span [2]int) int { return strings.Count(all[span[0]:span[1]], "\n") }
	sa, sb := s.np1LogSpans[len(s.np1LogSpans)-2], s.np1LogSpans[len(s.np1LogSpans)-1]
	if lines(sa) != lines(sb) {
		return fmt.Errorf("the private lookup left %d log line(s), the unknown id %d: not the same path", lines(sa), lines(sb))
	}
	if err := s.noStationReceivedAnything(); err != nil {
		return err
	}
	return s.noHold()
}

// np1HoldCovers: the slice-0 assertion for a single relay; for a batch, every hold placed
// since the snapshot must be `name`'s max cost.
func (s *np1State) np1HoldCovers(name, price string) error {
	n, err := s.holdsPlaced()
	if err != nil {
		return err
	}
	if n <= 1 {
		return s.holdCovers(name, price)
	}
	p, _ := strconv.ParseFloat(price, 64)
	if o := s.offerOf(name); math.Abs(o.PriceOut-p) > 1e-9 {
		return fmt.Errorf("%q is priced %v, the scenario says %v", name, o.PriceOut, p)
	}
	rows, err := s.db.LedgerOf(s.wallet, []string{store.KindHold}, 5000)
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	want := s.expectedHold(name)
	for i := 0; i < n && i < len(rows); i++ {
		if got := -rows[i].Amount; math.Abs(got-want) > 1e-9 {
			return fmt.Errorf("hold %.9f, want %.9f (%s's max cost at %v)", got, want, name, p)
		}
	}
	return nil
}

func TestRoutingNodePreferenceBDD(t *testing.T) {
	// These scenarios measure ranking over many free relays from one client IP; the free-traffic
	// limits (§14.3) are pinned in fairness_and_abuse.feature, so they are off here.
	for _, k := range []string{"ROGERAI_FREE_RATE_RPM", "ROGERAI_FREE_STATION_RPM", "ROGERAI_FREE_PIN_RPM"} {
		t.Setenv(k, "0")
	}
	st := &np1State{s0State: &s0State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}}
	prev := log.Writer()
	log.SetOutput(st.logs)
	t.Cleanup(func() {
		log.SetOutput(prev)
		st.teardownPins()
	})
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, st.np1Reset()
			})
			sc.After(func(ctx context.Context, scn *godog.Scenario, err error) (context.Context, error) {
				if err != nil {
					t.Logf("broker log tail for %q:\n%s\nlast response %d %s", scn.Name, np1Tail(st.logs.String(), 3000), st.lastCode, st.lastBody)
				}
				st.teardownPins()
				return ctx, nil
			})

			// --- Background (slice-0 functions) ---
			sc.Step(`^a broker with an empty in-memory node registry$`, st.emptyRegistry)
			sc.Step(`^the fee rate is 30%$`, st.feeRate30)
			sc.Step(`^stations "s1", "s2", "s3" serve "m", all on air, healthy \(Tier A\), seen just now$`, st.threeStations)
			sc.Step(`^"s1" prices out 1\.00, "s2" prices out 2\.00, "s3" prices out 3\.00 per 1M$`, st.threePrices)
			sc.Step(`^a funded consumer$`, st.fundedConsumer)

			// --- Given: slice-0 functions ---
			sc.Step(`^"([^"]*)" 429s and "([^"]*)" serves$`, st.is429sAndServes)
			sc.Step(`^"([^"]*)" 429s with Retry-After (\d+) and "([^"]*)" serves$`, st.is429sRetryAfterAndServes)
			sc.Step(`^"([^"]*)" 429s, "([^"]*)" 429s, and "([^"]*)" serves$`, st.threeWay429)
			sc.Step(`^"([^"]*)" 429s and "([^"]*)" and "([^"]*)" serve$`, st.a429sBandCServe)
			sc.Step(`^"([^"]*)" 429s, "([^"]*)" 429s with Retry-After (\d+), and "([^"]*)" serves$`, st.a429sB429sRAcServes)
			sc.Step(`^"([^"]*)" 429s twice in a row and "([^"]*)" serves$`, st.twiceThenServes)
			sc.Step(`^"([^"]*)" is cooling for (\d+) more seconds and "([^"]*)" serves$`, st.coolingAndServes)
			sc.Step(`^"([^"]*)" is cooling for (\d+) more seconds and "([^"]*)" is cooling for (\d+) more seconds$`, st.bothCooling)
			sc.Step(`^"([^"]*)" is cooling for (\d+) more seconds and "([^"]*)" is banned$`, st.coolingAndBanned)
			sc.Step(`^"([^"]*)" is banned and "([^"]*)" is cooling$`, st.bannedAndCooling)
			sc.Step(`^"([^"]*)" is stale$`, st.stale)
			sc.Step(`^"([^"]*)" was last seen 2\*nodeTTL ago$`, st.stale)
			sc.Step(`^"([^"]*)" is (banned|owned by a banned operator|stale \(last seen 2\*nodeTTL ago\)|past the dead-probe streak|cooling|private \(band-only\) and no code is presented|not offering "m"|priced out 12\.00 \(over the \$10 default cap\)|declaring ctx 4096 for an 8000-token prompt|below the request's min_tps|lacking a required capability|not TEE-attested on a confidential request|priced over the default out-cap)$`, st.stationState)
			sc.Step(`^"([^"]*)" prices out ([0-9.]+) per 1M$`, st.pricesOut)
			sc.Step(`^only "([^"]*)" and "([^"]*)" are TEE-attested$`, st.onlyAttested)
			sc.Step(`^"([^"]*)" and "([^"]*)" serve "m" free \(0/0\)$`, st.freeStations)
			sc.Step(`^the consumer's balance covers "([^"]*)" but not "([^"]*)"$`, st.balanceCovers)
			sc.Step(`^stations "s1"\.\."s5" serve "m" and all 429$`, st.fiveAll429)
			sc.Step(`^"([^"]*)" and "([^"]*)" are idle and "([^"]*)" is at capacity$`, st.idleAndAtCapacity)
			sc.Step(`^"([^"]*)" is a private \(band-only\) station for "([^"]*)"$`, st.privateStationNoCode)

			// --- Given: slice 1 ---
			sc.Step(`^a private band "([^"]*)" with stations? (.+) for "m"$`, st.np1PrivateBand)
			sc.Step(`^"([^"]*)" is cooling for (\d+) more seconds$`, st.np1CoolingFor)
			sc.Step(`^operator "([^"]*)" is banned$`, st.np1OperatorBanned)
			sc.Step(`^"([^"]*)" is owned by "([^"]*)", on air for "m", seen just now$`, st.np1OwnedByOperator)
			sc.Step(`^"([^"]*)" has failed probeDeadStreak liveness probes in a row$`, st.np1DeadProbe)
			sc.Step(`^only "([^"]*)" is TEE-attested$`, st.np1OnlyAttested)
			sc.Step(`^"([^"]*)" is on air for "([^"]*)" only$`, st.np1OnAirOtherModelOnly)
			sc.Step(`^"([^"]*)" declares ctx (\d+) for "m"$`, st.np1DeclaresCtx)
			sc.Step(`^"([^"]*)" serves "m" free \(0/0\) and "([^"]*)" is paid$`, st.np1FreeAndPaid)
			sc.Step(`^"([^"]*)" serves "m" free \(0/0\)$`, st.np1FreeOne)
			sc.Step(`^"([^"]*)" \(tps (\d+)\) and "([^"]*)" \(tps (\d+)\) serve "m" free$`, st.np1FreeWithTPS)
			sc.Step(`^"([^"]*)" and "([^"]*)" are equal in every score term$`, st.np1EqualScore)
			sc.Step(`^stations "s1"\.\."s5" serve "m" and "s1", "s2", "s3" 429$`, st.np1FiveFirstThree429)
			sc.Step(`^"([^"]*)" takes 85 seconds then 429s$`, st.np1Takes85Then429)
			sc.Step(`^"([^"]*)" 429s with Retry-After (\d+) and "([^"]*)" 429s with Retry-After (\d+)$`, st.np1Both429RA)
			sc.Step(`^"([^"]*)" 429s$`, st.np1Is429)
			sc.Step(`^"([^"]*)" 429s before any chunk$`, st.np1Is429)
			sc.Step(`^"([^"]*)" and "([^"]*)" both price out ([0-9.]+), "([^"]*)" prices in ([0-9.]+), "([^"]*)" prices in ([0-9.]+)$`, st.np1PricesInOut)
			sc.Step(`^"([^"]*)" and "([^"]*)" price identically and "([^"]*)" has the better reliability$`, st.np1PriceIdenticallyBetter)
			sc.Step(`^"([^"]*)" has a scheduled window pricing out ([0-9.]+) that is active now$`, st.np1ScheduledWindow)
			sc.Step(`^"([^"]*)" and "([^"]*)" price out ([0-9.]+) and "([^"]*)" prices out ([0-9.]+)$`, st.np1TwoPricedOutOneCheaper)
			sc.Step(`^"([^"]*)" is Tier B \(success rate ([0-9.]+)\) and "s2", "s3" are Tier A$`, st.np1TierB)
			sc.Step(`^"s1", "s2", "s3" are all Tier B$`, st.np1AllTierB)
			sc.Step(`^measured (tps|ttft) (.+)$`, st.np1Measured)
			sc.Step(`^no station has a tps measurement$`, st.np1NoTPSAnywhere)
			sc.Step(`^"([^"]*)" is strictly best under (price|throughput|latency) and every station is idle$`, st.np1BestAndIdle)
			sc.Step(`^"([^"]*)" is strictly best under (price|throughput|latency) but is (.+)$`, st.np1BestBut)
			sc.Step(`^"([^"]*)" is strictly fastest but Tier B, and "s2", "s3" are Tier A$`, st.np1FastestButTierB)
			sc.Step(`^"([^"]*)" lacks the "tools" capability$`, st.np1LacksTools)
			sc.Step(`^"([^"]*)" has the best reliability spine$`, st.np1BestReliability)
			sc.Step(`^"([^"]*)" is strictly cheapest and every station is idle$`, st.np1CheapestIdle)
			sc.Step(`^"([^"]*)" is strictly cheapest but cooling$`, st.np1CheapestCooling)
			sc.Step(`^"([^"]*)" is at capacity$`, st.np1AtCapacity)
			sc.Step(`^"([^"]*)" answers with a body containing \{"provider":\{"order":\["s1"\]\}\}$`, st.np1EchoesOrder)
			sc.Step(`^a grant (?:from the owner of .+ )?scoped to nodes (\[[^\]]*\])$`, st.np1GrantScoped)
			sc.Step(`^a grant scoped to nodes (\[[^\]]*\]) and "([^"]*)" 429s$`, st.np1GrantScopedAnd429)
			sc.Step(`^a Tower "([^"]*)" hosts "m"$`, st.np1TowerHosts)
			sc.Step(`^a Tower "([^"]*)" hosts "m" and no direct station is in the only set$`, st.np1TowerHosts)
			sc.Step(`^a Tower "([^"]*)" hosts "m" and no direct station serves "m"$`, st.np1TowerHostsNoDirect)
			sc.Step(`^a Tower registers a station whose relay name equals "([^"]*)"$`, st.np1TowerPosesAs)
			sc.Step(`^(\d+) relays used provider\.order, (\d+) used provider\.sort, and (\d+) no-fallback relays were refused no_match$`, st.np1TelemetryRelays)

			// --- When ---
			sc.Step(`^((?:a funded consumer|an anonymous consumer|a signed-in consumer|the grant holder|consumer [AB]|\d+ (?:funded|anonymous|signed-in) consumers|\d+ grant) relays? .*)$`, st.np1Relays)
			sc.Step(`^an operator registers "([^"]*)" for "m" at out ([0-9.]+) per 1M$`, st.np1OperatorRegisters)
			sc.Step(`^the founder reads /admin/live$`, st.np1ReadsAdminLive)

			// --- Then: slice-0 functions ---
			sc.Step(`^the pick is "([^"]*)"$`, st.pickIs)
			sc.Step(`^the pick is "([^"]*)" or "([^"]*)" by score$`, st.pickIsOneOf2)
			sc.Step(`^the pick is "([^"]*)", "([^"]*)" or "([^"]*)" by score$`, st.pickIsOneOf3)
			sc.Step(`^every pick is "([^"]*)"$`, st.everyPickIs)
			sc.Step(`^every pick is "([^"]*)" or "([^"]*)"$`, st.everyPickIsOr)
			sc.Step(`^"([^"]*)" is picked at least once$`, st.pickedAtLeastOnce)
			sc.Step(`^the pick distribution matches (\d+) relays with no routing object$`, st.distributionMatches)
			sc.Step(`^"([^"]*)" received nothing$`, st.receivedNothing)
			sc.Step(`^"([^"]*)" and "([^"]*)" received nothing$`, st.bothReceivedNothing)
			sc.Step(`^no station received anything$`, st.noStationReceivedAnything)
			sc.Step(`^"([^"]*)" received exactly one attempt$`, st.receivedExactlyOne)
			sc.Step(`^the response is (\d+)$`, st.responseIs)
			sc.Step(`^the response is (\d+) \{"error":\{"code":"([^"]*)"\}\}$`, st.responseIsWithCode)
			sc.Step(`^the response is (\d+) \{"error":\{"code":"([^"]*)"\}\} with Retry-After: (\d+)$`, st.responseIsWithCodeRetryAfter)
			sc.Step(`^the response is 400 with error\.code "([^"]*)" naming "([^"]*)"$`, st.response400Naming)
			sc.Step(`^the response is 503 "([^"]*)"$`, st.response503Message)
			sc.Step(`^the body carries only the generic band code band_unavailable \(§14\.B6.*\)$`, func() error { return st.errorCodeIs("band_unavailable") })
			sc.Step(`^the failover goes to "([^"]*)", never "([^"]*)"$`, st.failoverGoesToNever)
			sc.Step(`^the failover goes to "([^"]*)"$`, st.failoverGoesTo)
			sc.Step(`^attempt 1 hit "([^"]*)", attempt 2 hit "([^"]*)", attempt 3 hit "([^"]*)"$`, st.attemptsHit)
			sc.Step(`^attempt 1 hit "([^"]*)" and attempt 2 hit "([^"]*)" or "([^"]*)"$`, st.attempt1And2Either)
			sc.Step(`^attempt 1 hit "([^"]*)" and attempt 2 hit "([^"]*)"$`, st.attempt1And2)
			sc.Step(`^the consumer sees 200 with X-RogerAI-Provider "([^"]*)"$`, st.consumerSees200Provider)
			sc.Step(`^the consumer sees (\d+)$`, st.consumerSees)
			sc.Step(`^exactly one attempt was made, to "([^"]*)"$`, st.exactlyOneAttemptTo)
			sc.Step(`^exactly one attempt was made$`, st.exactlyOneAttempt)
			sc.Step(`^exactly (\d+) attempts were made, to "([^"]*)", "([^"]*)", "([^"]*)"$`, st.exactlyNAttemptsTo)
			sc.Step(`^the response is (\d+) with Retry-After: (\d+)$`, st.responseWithRetryAfter)
			sc.Step(`^the response is (\d+) with Retry-After$`, st.responseHasRetryAfter)
			sc.Step(`^no Retry-After header is present$`, st.noRetryAfter)
			sc.Step(`^the response carries no warning about "([^"]*)"$`, st.noWarningAbout)
			sc.Step(`^the header is ignored without error$`, st.headerIgnoredWithoutError)
			sc.Step(`^fallbacks are allowed \(the body's default\), not disabled by the header$`, st.fallbacksAllowed)
			sc.Step(`^no hold was placed$`, st.noHold)
			sc.Step(`^exactly one hold was placed$`, st.exactlyOneHold)
			sc.Step(`^the hold covers "([^"]*)" at ([0-9.]+), not "([^"]*)" at ([0-9.]+)$`, st.holdCoversNot)
			sc.Step(`^the hold covered exactly the picked station, not the priciest of three$`, st.holdCoveredPicked)
			sc.Step(`^on success at "([^"]*)" the consumer is charged at ([0-9.]+) and the remainder released$`, st.chargedAtAndReleased)

			// --- Then: slice 1 ---
			sc.Step(`^the hold covers "([^"]*)" at ([0-9.]+)$`, st.np1HoldCovers)
			sc.Step(`^X-RogerAI-Provider names "([^"]*)"$`, st.np1ProviderNames)
			sc.Step(`^the response is (\d+) \{"error":\{"code":"([^"]*)","message":"([^"]*)"\}\}$`, st.np1ResponseCodeMessage)
			sc.Step(`^X-RogerAI-Cost is 0 and there is no receipt$`, st.np1CostZeroNoReceipt)
			sc.Step(`^the hold is released in full$`, st.np1HoldReleasedInFull)
			sc.Step(`^the error message does not reveal that "([^"]*)" exists or is private$`, st.np1NoReveal)
			sc.Step(`^the message says no node of this grant's owner matches$`, st.np1GrantOwnerMessage)
			sc.Step(`^the message says no confidential station matches$`, st.np1ConfidentialMessage)
			sc.Step(`^the registration is rejected$`, st.registrationRejected)
			sc.Step(`^a relay with provider\.only (\[.*\]) is 503 \{"error":\{"code":"no_match"\}\}$`, st.np1RelayOnlyIsNoMatch)
			sc.Step(`^the response is 400 "([^"]*)" naming (\d+)$`, st.np1CtxRefusal)
			sc.Step(`^"([^"]*)" received nothing and no hold was placed$`, st.np1NothingAndNoHold)
			sc.Step(`^"([^"]*)"'s operator is not struck$`, st.np1NotStruck)
			sc.Step(`^the response is 401 "([^"]*)"$`, st.np1Response401)
			sc.Step(`^no hold was placed and X-RogerAI-Cost is "0"$`, st.np1AnonNoHoldCostZero)
			sc.Step(`^both "([^"]*)" and "([^"]*)" are picked$`, st.np1BothPicked)
			sc.Step(`^no attempt is made to "([^"]*)" \(less than 10 seconds left\)$`, st.np1NoAttemptTo)
			sc.Step(`^attempts were made to "([^"]*)" and "([^"]*)" in score order$`, st.np1AttemptsToBoth)
			sc.Step(`^the response is 429 with the last upstream Retry-After$`, st.np1LastUpstreamRetryAfter)
			sc.Step(`^a \$0 voided receipt is written in "([^"]*)"'s chain$`, st.np1VoidedReceipt)
			sc.Step(`^the consumer's balance is unchanged$`, st.np1BalanceUnchanged)
			sc.Step(`^the grant's caps were charged for one attempt, not two$`, st.np1GrantOneAttempt)
			sc.Step(`^the response is 429 with Retry-After and no SSE headers were committed$`, st.np1StreamRefusedNoSSE)
			sc.Step(`^"([^"]*)" is planned after "([^"]*)"$`, st.np1PlannedAfter)
			sc.Step(`^the picks follow the score order$`, st.np1PicksFollowScore)
			sc.Step(`^"([^"]*)" is not in the plan$`, st.np1NotInPlan)
			sc.Step(`^the pick is not "([^"]*)"$`, st.np1PickIsNot)
			sc.Step(`^every pick is the faster of "([^"]*)" and "([^"]*)"$`, st.np1EveryPickFaster)
			sc.Step(`^no offer over the register ceiling is on air$`, st.np1NoOfferOverCeiling)
			sc.Step(`^a relay with provider\.sort "price" and provider\.max_price\.completion 100 picks "([^"]*)"$`, st.np1SortPriceCapPicks)
			sc.Step(`^the two pick distributions are statistically indistinguishable$`, st.np1DistributionsMatch)
			sc.Step(`^"([^"]*)" is picked more often than under (balanced|cheap)$`, st.np1PickedMoreThanUnder)
			sc.Step(`^"([^"]*)" is not picked (\d+) times$`, st.np1NotPickedNTimes)
			sc.Step(`^the request is served under balanced$`, st.np1ServedUnderBalanced)
			sc.Step(`^a warning counter pref_header_unknown increments$`, st.np1PrefHeaderUnknownCounter)
			sc.Step(`^the response is 400 with error\.code "([^"]*)"$`, st.np1Response400Code)
			sc.Step(`^attempt 1 hit "([^"]*)" and attempt 2 is scored under cheap$`, st.np1SecondAttemptUnderCheap)
			sc.Step(`^"([^"]*)" is picked more often than "([^"]*)"$`, st.np1PickedMoreThan)
			sc.Step(`^every relay made exactly one attempt$`, st.np1EveryRelayOneAttempt)
			sc.Step(`^the bridge serves through "([^"]*)"$`, st.np1BridgeServesThrough)
			sc.Step(`^X-RogerAI-Relay names "([^"]*)"$`, st.np1BridgeServesThrough)
			sc.Step(`^every relay is served through "([^"]*)"$`, st.np1EveryRelayThrough)
			sc.Step(`^every pick is "([^"]*)" and the bridge is never entered$`, st.np1EveryPickNoBridge)
			sc.Step(`^the pick is the direct station "([^"]*)"$`, st.np1PickIsDirect)
			sc.Step(`^the bridge is never entered$`, st.np1BridgeEntered)
			sc.Step(`^every pick is either the direct "([^"]*)" or the Tower row "([^"]*)" and no other server$`, st.np1EitherDirectOrTowerRow)
			sc.Step(`^/admin/live warns exactly once about the id collision$`, st.np1CollisionWarnedOnce)
			sc.Step(`^the effective selection is (.+)$`, st.np1EffectiveSelection)
			sc.Step(`^no error is returned$`, st.np1NoErrorReturned)
			sc.Step(`^the body "([^"]*)" received has no "provider", "roger" or "models" key$`, st.np1BodyHasNoCarriers)
			sc.Step(`^the body "([^"]*)" received is otherwise byte-identical to what the consumer sent$`, st.np1BodyOtherwiseIdentical)
			sc.Step(`^the body "([^"]*)" received is otherwise byte-identical to what the consumer sent, apart from the default max_tokens$`, st.np1BodyIdenticalButMax)
			sc.Step(`^it shows routing_strict_order (\d+), routing_strict_sort (\d+), routing_nofallback_refused (\d+)$`, st.np1ShowsCounters)
			sc.Step(`^exactly one log line names the request, "([^"]*)", and "([^"]*)"$`, st.np1OneLogLine)
			sc.Step(`^no log line contains the band code$`, st.np1NoBandCodeInLogs)
			sc.Step(`^no hold was placed, no receipt was written, and no operator was struck$`, st.np1NoHoldReceiptStrike)
			sc.Step(`^B's pick is by score and unaffected by A's order$`, st.np1BUnaffected)
			sc.Step(`^the second pick is by score$`, st.np1SecondPickByScore)
			sc.Step(`^every relay is dispatched to "([^"]*)" \(the consumer chose it\)$`, st.np1EveryDispatchedTo)
			sc.Step(`^the capacity-aware load factor is reported on /admin/live for "([^"]*)"$`, st.np1LoadFactorReported)
			sc.Step(`^both responses are byte-identical 503 \{"error":\{"code":"no_match"\}\} bodies apart from the request id$`, st.np1BothNoMatchIdentical)
			sc.Step(`^both refusals ran the same constant-work path \(the private lookup is performed whether or not the id exists\)$`, st.np1SameConstantWork)
		},
		Options: &godog.Options{
			Format: "pretty", Strict: true, TestingT: t,
			Paths: []string{"../../features/routing/node_preference.feature"},
			Tags:  "~@cli && ~@tui && ~@proxy && ~@harness && ~@docs && ~@later && ~@part-b && ~@part-c && ~@slice2 && ~@slice3 && ~@slice4",
		},
	}
	if suite.Run() != 0 {
		t.Fatal("features/routing/node_preference.feature has failing scenarios")
	}
}
