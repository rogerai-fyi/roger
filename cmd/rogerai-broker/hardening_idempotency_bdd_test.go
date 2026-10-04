package main

// hardening_idempotency_bdd_test.go makes features/routing/idempotency.feature EXECUTABLE (slice
// 6, contract §14.B2) against the REAL broker, real store (Postgres when ROGERAI_TEST_DATABASE_URL
// is set), the miniredis shared store and real stations (sa6H, hardening_affinity_bdd_test.go).
// The @proxy scenarios run in internal/client/idempotency_proxy_bdd_test.go.
//
// Observation points:
//   - "the identical request" is the first request's exact spec re-sent: the same body bytes
//     (utPrompt is deterministic) and the same headers.
//   - "received exactly N jobs in total" is the station's upstream request count since it went on
//     air; "nothing for the retry" is the per-station footprint of the retry alone.
//   - money: hold / spend / earn ledger rows of the payer and the operator, never a header.
//   - "in flight" requests run in a goroutine against the same broker; the step waits until the
//     station's upstream was hit before the scenario continues.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/store"
)

type id6State struct {
	*sa6H
	wallet0 float64
}

func (s *id6State) window(n string) error { return os.Setenv("ROGERAI_IDEMPOTENCY_TTL", n+"m") }

func (s *id6State) ttlKnob(v string) error { return os.Setenv("ROGERAI_IDEMPOTENCY_TTL", v) }

func (s *id6State) firstOrErr() (*rs1Shot, error) {
	if s.first == nil {
		return nil, fmt.Errorf("no first request carrying an Idempotency-Key in this scenario")
	}
	return s.first, nil
}

// resend re-sends the first request's spec, optionally altered, as the named user.
func (s *id6State) resend(user string, alter func(q *sa6Spec)) error {
	if s.first == nil {
		return fmt.Errorf("no first request to retry")
	}
	q := s.firstQ
	q.user, q.expect = user, ""
	q.hdr = map[string]string{}
	for k, v := range s.firstQ.hdr {
		q.hdr[k] = v
	}
	if alter != nil {
		alter(&q)
	}
	b, _ := s.balance()
	s.wallet0 = b
	return s.send(q)
}

func (s *id6State) identical(user, key string) error {
	return s.resend(user, func(q *sa6Spec) { q.hdr["Idempotency-Key"] = key })
}

func (s *id6State) identicalTimes(user, key, n string) error {
	for i := 0; i < rs1i(n); i++ {
		if err := s.identical(user, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *id6State) identicalOnB(user, key string) error {
	if s.b2 == nil {
		return fmt.Errorf("no instance B")
	}
	saved := s.b
	s.b = s.b2
	defer func() { s.b = saved }()
	return s.identical(user, key)
}

func (s *id6State) identicalStream(user, key string) error {
	return s.resend(user, func(q *sa6Spec) { q.hdr["Idempotency-Key"] = key; q.stream = true })
}

func (s *id6State) streamFalse(user, key string) error {
	return s.resend(user, func(q *sa6Spec) { q.hdr["Idempotency-Key"] = key; q.stream = false })
}

func (s *id6State) sameBodyHeader(user, h, v, key string) error {
	return s.resend(user, func(q *sa6Spec) { q.hdr["Idempotency-Key"] = key; q.hdr[h] = v })
}

func (s *id6State) whitespace(user, key string) error {
	first, err := s.firstOrErr()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, first.sent, "", "    "); err != nil {
		return err
	}
	return s.resend(user, func(q *sa6Spec) {
		q.hdr["Idempotency-Key"] = key
		q.raw = buf.String()
	})
}

func (s *id6State) differentPrompt(user, model, key string) error {
	q := sa6Spec{user: user, caller: "user", model: model, hdr: map[string]string{"Idempotency-Key": key}, promptTokens: 77}
	return s.send(q)
}

func (s *id6State) twiceNoKey(user string) error {
	q := sa6Spec{user: user, caller: "user", model: "m", hdr: map[string]string{}}
	if err := s.send(q); err != nil {
		return err
	}
	return s.send(q)
}

// --- the same-body checks ------------------------------------------------------------------------

func (s *id6State) sameBody() error {
	first, err := s.firstOrErr()
	if err != nil {
		return err
	}
	if err := s.responseIs(strconv.Itoa(first.code)); err != nil {
		return err
	}
	if !bytes.Equal(s.lastBody, first.body) {
		return fmt.Errorf("the retry's body differs from the first:\n first %.300s\n retry %.300s", first.body, s.lastBody)
	}
	return nil
}

func (s *id6State) sameReqID() error {
	first, err := s.firstOrErr()
	if err != nil {
		return err
	}
	a, b := first.hdr.Get("X-RogerAI-Request-Id"), s.lastHdr.Get("X-RogerAI-Request-Id")
	if a == "" || a != b {
		return fmt.Errorf("X-RogerAI-Request-Id %q on the retry, %q on the first", b, a)
	}
	return nil
}

func (s *id6State) reqIDDiffers() error {
	first, err := s.firstOrErr()
	if err != nil {
		return err
	}
	a, b := first.hdr.Get("X-RogerAI-Request-Id"), s.lastHdr.Get("X-RogerAI-Request-Id")
	if b == "" || a == b {
		return fmt.Errorf("X-RogerAI-Request-Id %q on both", b)
	}
	return nil
}

func (s *id6State) sameHeaders() error {
	first, err := s.firstOrErr()
	if err != nil {
		return err
	}
	for _, k := range []string{"X-RogerAI-Receipt", "X-RogerAI-Cost", "X-RogerAI-Provider", "X-RogerAI-Model", "X-RogerAI-Price"} {
		a, b := first.hdr.Get(k), s.lastHdr.Get(k)
		if a == "" || a != b {
			return fmt.Errorf("%s %q on the replay, %q on the first", k, b, a)
		}
	}
	return nil
}

func (s *id6State) jobsTotal(name, n string) error {
	if got := s.st(name).upstreamCount(); got != rs1i(n) {
		return fmt.Errorf("%q received %d job(s) in total, want %s", name, got, n)
	}
	return nil
}

func (s *id6State) nothingForRetry() error {
	for n, d := range s.delta {
		if d != 0 {
			return fmt.Errorf("%q received %d request(s) for the retry", n, d)
		}
	}
	return nil
}

func (s *id6State) ledger(holder string, kinds ...string) ([]store.LedgerRow, error) {
	return s.db.LedgerOf(holder, kinds, 5000)
}

func (s *id6State) oneSettleOneReceipt() error {
	first, err := s.firstOrErr()
	if err != nil {
		return err
	}
	id := first.hdr.Get("X-RogerAI-Request-Id")
	rows, err := s.ledger(s.wallet, store.KindSpend)
	if err != nil {
		return err
	}
	n := 0
	for _, r := range rows {
		if strings.HasPrefix(r.Ref, id) {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d spend row(s) for request %s, want exactly 1", n, id)
	}
	if _, _, err := s.storedReceipt(id + "-1"); err != nil {
		if _, _, err2 := s.storedReceipt(id); err2 != nil {
			return fmt.Errorf("no stored receipt for %s: %v", id, err)
		}
	}
	return nil
}

func (s *id6State) spendRows() (int, error) {
	rows, err := s.ledger(s.wallet, store.KindSpend)
	return len(rows), err
}

func (s *id6State) debitedOnce(string) error {
	n, err := s.spendRows()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%d spend row(s) on the wallet, want exactly 1", n)
	}
	return nil
}

func (s *id6State) moderationOnce() error {
	s.modMu.Lock()
	n := s.modCalls
	s.modMu.Unlock()
	if n != 1 {
		return fmt.Errorf("the classifier was called %d time(s) for the request and its retry, want 1", n)
	}
	return nil
}

func (s *id6State) flags() error { s.illegal = true; return nil }

func (s *id6State) slowerThanWindow(name string) error {
	nonStreamRelayWait = 400 * time.Millisecond
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		utRealCompletion(w)
	})
	return nil
}

func (s *id6State) takesSeconds(name, secs string) error {
	d := time.Duration(rs1i(secs)) * time.Second
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(d)
		utRealCompletion(w)
	})
	return nil
}

// inFlight starts the first request in a goroutine and waits until the station was hit.
func (s *id6State) inFlightOn(user, inst, model, key string, stream bool) error {
	s.as(user)
	if err := s.snapOnce(); err != nil {
		return err
	}
	b := s.b
	if strings.Contains(inst, "B") && s.b2 != nil {
		b = s.b2
	}
	q := sa6Spec{user: user, caller: "user", model: model, stream: stream, hdr: map[string]string{"Idempotency-Key": key}}
	req := s.rs1(q)
	sent, _ := s.buildBody(req)
	r := s.httpRequest(req, sent)
	before := 0
	for _, st := range s.stations {
		before += st.upstreamCount()
	}
	s.bg = make(chan rs1Shot, 1)
	go func() {
		w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
		b.relay(w, r)
		s.bg <- rs1Shot{code: w.Code, hdr: w.Header(), body: w.Body.Bytes(), sent: sent}
	}()
	sh := rs1Shot{sent: sent}
	s.first, s.firstQ = &sh, q
	for i := 0; i < 300; i++ {
		n := 0
		for _, st := range s.stations {
			n += st.upstreamCount()
		}
		if n > before {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("the first request never reached a station")
}

func (s *id6State) inFlight(user, model, key string) error {
	return s.inFlightOn(user, "", model, key, false)
}

func (s *id6State) inFlightA(user, model, key string) error {
	return s.inFlightOn(user, "A", model, key, false)
}

func (s *id6State) streamStarted(user, model, key string) error {
	return s.inFlightOn(user, "", model, key, true)
}

func (s *id6State) firstFinishes(code string) error {
	if s.bg == nil {
		return fmt.Errorf("nothing in flight")
	}
	select {
	case sh := <-s.bg:
		s.bg = nil
		if strconv.Itoa(sh.code) != code {
			return fmt.Errorf("the first request finished %d, want %s (%.300s)", sh.code, code, sh.body)
		}
		s.first = &sh
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("the first request never finished")
	}
}

func (s *id6State) retryAfterAtLeast(n string) error {
	v, err := strconv.Atoi(s.lastHdr.Get("Retry-After"))
	if err != nil || v < rs1i(n) {
		return fmt.Errorf("Retry-After %q, want at least %s", s.lastHdr.Get("Retry-After"), n)
	}
	return nil
}

// --- concurrency ------------------------------------------------------------------------------

func (s *id6State) twiceConcurrently(user, key string) error {
	s.as(user)
	if err := s.snapOnce(); err != nil {
		return err
	}
	q := sa6Spec{user: user, caller: "user", model: "m", hdr: map[string]string{"Idempotency-Key": key}}
	req := s.rs1(q)
	sent, _ := s.buildBody(req)
	reqs := []*http.Request{s.httpRequest(req, sent), s.httpRequest(req, sent)}
	var wg sync.WaitGroup
	shots := make([]rs1Shot, 2)
	for i := range reqs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
			s.b.relay(w, reqs[i])
			shots[i] = rs1Shot{code: w.Code, hdr: w.Header(), body: w.Body.Bytes()}
		}(i)
	}
	wg.Wait()
	s.shots = append(s.shots, shots...)
	return nil
}

func (s *id6State) exactlyOne200(name string) error {
	sh := s.shots[len(s.shots)-2:]
	ok := 0
	for _, x := range sh {
		if x.code == 200 && x.hdr.Get("X-RogerAI-Provider") == s.id(name) && x.hdr.Get("X-RogerAI-Idempotent-Replay") == "" {
			ok++
		}
	}
	if ok != 1 {
		return fmt.Errorf("%d fresh 200(s) from %q among %d and %d, want exactly one", ok, name, sh[0].code, sh[1].code)
	}
	return nil
}

func (s *id6State) otherIs409OrReplay(code string) error {
	sh := s.shots[len(s.shots)-2:]
	for _, x := range sh {
		if x.code == 200 && x.hdr.Get("X-RogerAI-Idempotent-Replay") == "" {
			continue
		}
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(x.body, &env)
		if (x.code == 409 && env.Error.Code == code) || x.hdr.Get("X-RogerAI-Idempotent-Replay") == "true" {
			return nil
		}
		return fmt.Errorf("the other response is %d %.200s, want 409 %q or a replay", x.code, x.body, code)
	}
	return fmt.Errorf("both responses were fresh 200s")
}

// --- scope --------------------------------------------------------------------------------------

func (s *id6State) reqIDDiffersFromUser(string) error { return s.reqIDDiffers() }

func (s *id6State) grantIssued(_, _ string) error {
	return s.mint(s.st("s1"), []string{"m"}, 0)
}

func (s *id6State) grantServed(_, key string) error {
	q := sa6Spec{caller: "grant", model: "m", hdr: map[string]string{"Idempotency-Key": key}}
	if err := s.send(q); err != nil {
		return err
	}
	return s.statusIs("200")
}

func (s *id6State) signedAs(user, _, key string) error {
	return s.resend(user, func(q *sa6Spec) { q.caller = "user"; q.hdr["Idempotency-Key"] = key })
}

func (s *id6State) freshNotReplay() error {
	if err := s.statusIs("200"); err != nil {
		return err
	}
	if v := s.lastHdr.Get("X-RogerAI-Idempotent-Replay"); v != "" {
		return fmt.Errorf("X-RogerAI-Idempotent-Replay %q on a fresh relay", v)
	}
	return s.reqIDDiffers()
}

func (s *id6State) anonFree(key string) error {
	s.onAir("f1", "m", 0, 0)
	s.goOffAir("s1") // "a free station": the anonymous caller's only supply
	q := sa6Spec{caller: "anon", model: "m", hdr: map[string]string{"Idempotency-Key": key, "CF-Connecting-IP": "198.51.100.7"}}
	if err := s.send(q); err != nil {
		return err
	}
	return s.statusIs("200")
}

func (s *id6State) otherAnon(key string) error {
	_, s.anonPriv, _ = ed25519.GenerateKey(nil)
	return s.resend("", func(q *sa6Spec) {
		q.hdr["Idempotency-Key"] = key
		q.hdr["CF-Connecting-IP"] = "203.0.113.9"
	})
}

// --- the window --------------------------------------------------------------------------------

func (s *id6State) passes(n, unit string) error {
	d := time.Duration(rs1i(n)) * time.Minute
	if strings.HasPrefix(unit, "second") {
		d = time.Duration(rs1i(n)) * time.Second
	}
	s.advance(d)
	return nil
}

func (s *id6State) isReplay() error {
	if v := s.lastHdr.Get("X-RogerAI-Idempotent-Replay"); v != "true" {
		return fmt.Errorf("X-RogerAI-Idempotent-Replay %q, want a replay (%d %.200s)", v, s.lastCode, s.lastBody)
	}
	return s.sameReqID()
}

func (s *id6State) isFresh() error { return s.freshNotReplay() }

// --- streams -----------------------------------------------------------------------------------

func (s *id6State) streamCompleted(user, model, key string) error {
	q := sa6Spec{user: user, caller: "user", model: model, stream: true, hdr: map[string]string{"Idempotency-Key": key}}
	if err := s.send(q); err != nil {
		return err
	}
	if s.lastCode != 200 || !bytes.Contains(s.lastBody, []byte("[DONE]")) {
		return fmt.Errorf("the stream did not complete: %d %.300s", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *id6State) noStationFor(model string) error {
	for n, st := range s.stations {
		if st.model == model {
			s.goOffAir(n)
		}
	}
	return nil
}

func (s *id6State) streamNoMatch(user, model, key string) error {
	q := sa6Spec{user: user, caller: "user", model: model, stream: true, hdr: map[string]string{"Idempotency-Key": key}}
	if err := s.send(q); err != nil {
		return err
	}
	return s.statusCode("503", "no_match")
}

func (s *id6State) streamsSlowly(name, secs string) error {
	d := time.Duration(rs1i(secs)) * time.Second
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"slow\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(d)
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	})
	return nil
}

// --- bounds -----------------------------------------------------------------------------------

func (s *id6State) bigCompletion(name, mib string) error {
	content := strings.Repeat("x", rs1i(mib)<<20)
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": content}}},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 10}})
		_, _ = w.Write(b)
	})
	return nil
}

func (s *id6State) liveKeys(user, n string) error {
	s.as(user)
	for i := 0; i < rs1i(n); i++ {
		q := sa6Spec{user: user, caller: "user", model: "m", hdr: map[string]string{"Idempotency-Key": fmt.Sprintf("bulk-%d", i)}}
		if err := s.send(q); err != nil {
			return err
		}
		if s.lastCode != 200 {
			return fmt.Errorf("bulk request %d: %d %.200s", i, s.lastCode, s.lastBody)
		}
	}
	return nil
}

func (s *id6State) oldestGone(user string) error {
	q := sa6Spec{user: user, caller: "user", model: "m", hdr: map[string]string{"Idempotency-Key": "bulk-0"}}
	if err := s.send(q); err != nil {
		return err
	}
	if s.lastCode != 200 || s.lastHdr.Get("X-RogerAI-Idempotent-Replay") != "" {
		return fmt.Errorf("the oldest key is still replayable: %d replay=%q", s.lastCode, s.lastHdr.Get("X-RogerAI-Idempotent-Replay"))
	}
	return nil
}

// --- money --------------------------------------------------------------------------------------

func (s *id6State) oneHold(string) error {
	n, _ := s.holdRowsOf(s.wallet)
	if d := n - s.holdBase[s.wallet]; d != 1 {
		return fmt.Errorf("%d hold row(s) placed across the request and its retries, want exactly 1", d)
	}
	return nil
}

func (s *id6State) oneSettleOneEarn() error {
	if err := s.debitedOnce(""); err != nil {
		return err
	}
	return s.earnedOnce("s1")
}

func (s *id6State) earnedOnce(name string) error {
	rows, err := s.ledger(s.st(name).acct, store.KindEarn)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return fmt.Errorf("%d earn row(s) for the operator of %q, want exactly 1", len(rows), name)
	}
	return nil
}

func (s *id6State) balanceUnchanged(string) error {
	b, err := s.balance()
	if err != nil {
		return err
	}
	if b != s.wallet0 {
		return fmt.Errorf("balance %v after the replay, %v before", b, s.wallet0)
	}
	return nil
}

func (s *id6State) costEqualsFirst() error {
	first, err := s.firstOrErr()
	if err != nil {
		return err
	}
	if a, b := first.hdr.Get("X-RogerAI-Cost"), s.lastHdr.Get("X-RogerAI-Cost"); a == "" || a != b {
		return fmt.Errorf("X-RogerAI-Cost %q on the replay, %q on the first", b, a)
	}
	return nil
}

func (s *id6State) lastToken(string) error {
	s.b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 0.0001, burst: 1}
	return nil
}

func (s *id6State) replayNot429() error {
	if s.lastCode == 429 {
		return fmt.Errorf("the replay was rate-limited: %.200s", s.lastBody)
	}
	return s.isReplay()
}

// --- attempts -----------------------------------------------------------------------------------

func (s *id6State) s1Serves() error { s.scriptServes("s1"); return nil }

func (s *id6State) s1429s2Serves() error {
	s.onAir("s2", "m", 0.10, 0.30)
	s.b.mu.Lock()
	tq := s.b.trust[s.st("s1").id]
	s.b.mu.Unlock()
	s.setTPS(s.st("s1").id, 200) // s1 is the clear head of the plan
	s.b.mu.Lock()
	s.b.trust[s.st("s1").id] = tq
	s.b.mu.Unlock()
	s.script429For("s1", "")
	return nil
}

func (s *id6State) attemptsIs(n string) error { return s.headerIs("X-RogerAI-Attempts", n) }

func TestIdempotencyBDD(t *testing.T) {
	sa6Run(t, "../../features/routing/idempotency.feature", func(sc *godog.ScenarioContext, h *sa6H) {
		s := &id6State{sa6H: h}
		sc.Step(`^the idempotency window is (\d+) minutes$`, s.window)
		sc.Step(`^ROGERAI_IDEMPOTENCY_TTL is "([^"]+)"$`, s.ttlKnob)
		sc.Step(`^"([^"]+)" sends the identical request with Idempotency-Key "([^"]+)"$`, s.identical)
		sc.Step(`^"([^"]+)" sends the identical request with Idempotency-Key "([^"]+)" (\d+) more times$`, s.identicalTimes)
		sc.Step(`^"([^"]+)" sends the identical request to instance B with Idempotency-Key "([^"]+)"$`, s.identicalOnB)
		sc.Step(`^"([^"]+)" sends the identical stream request with Idempotency-Key "([^"]+)"$`, s.identicalStream)
		sc.Step(`^"([^"]+)" sends the same request with stream false and Idempotency-Key "([^"]+)"$`, s.streamFalse)
		sc.Step(`^"([^"]+)" sends the same body with (X-Roger-[A-Za-z-]+) "([^"]+)" and Idempotency-Key "([^"]+)"$`, s.sameBodyHeader)
		sc.Step(`^"([^"]+)" sends the same JSON with different whitespace and Idempotency-Key "([^"]+)"$`, s.whitespace)
		sc.Step(`^"([^"]+)" sends the same request with Idempotency-Key "([^"]+)" twice concurrently$`, s.twiceConcurrently)
		sc.Step(`^"([^"]+)" sends the same request twice with no Idempotency-Key$`, s.twiceNoKey)
		sc.Step(`^"([^"]+)" sends the identical request signed as "([^"]+)" with Idempotency-Key "([^"]+)"$`, s.signedAs)
		sc.Step(`^the response is 200 with the same body bytes as the first$`, s.sameBody)
		sc.Step(`^X-RogerAI-Request-Id equals the first request's id$`, s.sameReqID)
		sc.Step(`^X-RogerAI-Request-Id differs from the first request's id$`, s.reqIDDiffers)
		sc.Step(`^X-RogerAI-Request-Id differs from "([^"]+)"'s request$`, s.reqIDDiffersFromUser)
		sc.Step(`^the two X-RogerAI-Request-Id values differ$`, func() error {
			sh := s.shots[len(s.shots)-2:]
			if a, b := sh[0].hdr.Get("X-RogerAI-Request-Id"), sh[1].hdr.Get("X-RogerAI-Request-Id"); a == "" || a == b {
				return fmt.Errorf("request ids %q and %q", a, b)
			}
			return nil
		})
		sc.Step(`^X-RogerAI-Idempotent-Replay is "([^"]+)"$`, func(v string) error { return s.headerIs("X-RogerAI-Idempotent-Replay", v) })
		sc.Step(`^X-RogerAI-Receipt, X-RogerAI-Cost, X-RogerAI-Provider, X-RogerAI-Model and X-RogerAI-Price equal the first response's$`, s.sameHeaders)
		sc.Step(`^"([^"]+)" received exactly (\d+) jobs? in total$`, s.jobsTotal)
		sc.Step(`^exactly 1 settle and 1 receipt exist for that request id$`, s.oneSettleOneReceipt)
		sc.Step(`^the wallet of "([^"]+)" was debited once$`, s.debitedOnce)
		sc.Step(`^no station received anything for the (?:retry|second request)$`, s.nothingForRetry)
		sc.Step(`^the moderation classifier was called exactly once for both$`, s.moderationOnce)
		sc.Step(`^sync moderation flags the prompt$`, s.flags)
		sc.Step(`^no second moderation record was written$`, s.moderationOnce)
		sc.Step(`^"([^"]+)" takes longer than the non-stream relay window$`, s.slowerThanWindow)
		sc.Step(`^"([^"]+)" takes (\d+) seconds to answer$`, s.takesSeconds)
		sc.Step(`^"([^"]+)" has relayed for "([^"]+)" with Idempotency-Key "([^"]+)" and the request is still in flight$`, s.inFlight)
		sc.Step(`^"([^"]+)" has relayed on instance A for "([^"]+)" with Idempotency-Key "([^"]+)" and it is in flight$`, s.inFlightA)
		sc.Step(`^the first request finishes with (\d+)$`, s.firstFinishes)
		sc.Step(`^Retry-After is at least (\d+)$`, s.retryAfterAtLeast)
		sc.Step(`^exactly one response is 200 from "([^"]+)"$`, s.exactlyOne200)
		sc.Step(`^the other is 409 "([^"]+)" or a replay of the first$`, s.otherIs409OrReplay)
		sc.Step(`^a grant "([^"]+)" issued to "([^"]+)"'s owner$`, s.grantIssued)
		sc.Step(`^a request with grant "([^"]+)" and Idempotency-Key "([^"]+)" was served$`, s.grantServed)
		sc.Step(`^the response is a fresh relay, not a replay$`, s.freshNotReplay)
		sc.Step(`^an anonymous caller relayed for a free station with Idempotency-Key "([^"]+)"$`, s.anonFree)
		sc.Step(`^a different anonymous IP sends the identical request with Idempotency-Key "([^"]+)"$`, s.otherAnon)
		sc.Step(`^(\d+) (seconds|minutes) pass$`, s.passes)
		sc.Step(`^the response is a replay$`, s.isReplay)
		sc.Step(`^the response is a fresh relay$`, s.isFresh)
		sc.Step(`^the response is a replay with the first request's id$`, s.isReplay)
		sc.Step(`^the response is a replay, not a 429$`, s.replayNot429)
		sc.Step(`^"([^"]+)" streams for "([^"]+)" with Idempotency-Key "([^"]+)" and the stream completed$`, s.streamCompleted)
		sc.Step(`^no station is on air for "([^"]+)"$`, s.noStationFor)
		sc.Step(`^"([^"]+)" streams for "([^"]+)" with Idempotency-Key "([^"]+)" and the response is 503 no_match$`, s.streamNoMatch)
		sc.Step(`^"([^"]+)" streams slowly for (\d+) seconds$`, s.streamsSlowly)
		sc.Step(`^"([^"]+)" has started a stream for "([^"]+)" with Idempotency-Key "([^"]+)"$`, s.streamStarted)
		sc.Step(`^"([^"]+)" answers with a (\d+) MiB completion$`, s.bigCompletion)
		sc.Step(`^"([^"]+)" has (\d+) live idempotency keys$`, s.liveKeys)
		sc.Step(`^the oldest key of "([^"]+)" is no longer replayable$`, s.oldestGone)
		sc.Step(`^exactly 1 hold was placed for key "([^"]+)"$`, s.oneHold)
		sc.Step(`^exactly 1 settle row and 1 earnings credit exist for it$`, s.oneSettleOneEarn)
		sc.Step(`^the operator of "([^"]+)" earned once$`, s.earnedOnce)
		sc.Step(`^the wallet balance of "([^"]+)" is unchanged by the replay$`, s.balanceUnchanged)
		sc.Step(`^X-RogerAI-Cost on the replay equals the first response's cost$`, s.costEqualsFirst)
		sc.Step(`^"([^"]+)" is at its last relay rate-limit token$`, s.lastToken)
		sc.Step(`^"s1" serves$`, s.s1Serves)
		sc.Step(`^"s1" 429s and "s2" serves$`, s.s1429s2Serves)
		sc.Step(`^X-RogerAI-Attempts is "(\d+)"$`, s.attemptsIs)
	})
}
