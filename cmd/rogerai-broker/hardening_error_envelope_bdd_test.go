package main

// hardening_error_envelope_bdd_test.go makes features/errors/error_envelope.feature EXECUTABLE
// (slice 6, contract §14.B6) against the REAL broker (sa6H, hardening_affinity_bdd_test.go): every
// refusal is produced the way production produces it (a real 429 from the rate limiter, a real
// upstream status from a station, a real band lookup, a real signature failure) and the body is
// read as {"error": {"code", "message", "type", "metadata"}}.
//
// Fixture notes (stated, not hidden):
//   - "goes off air between the pick and the dispatch": the station stays fresh in the registry
//     but its tunnel is gone, which is the single-instance form of that race.
//   - "has no free slot": the station's job channel is never read and its in_flight equals its
//     capacity; "no poller listening": the job channel is never read and nothing is in flight.
//     On a single instance both reach today's same "no poller free" path after the 3 s hand-off
//     wait; the contract gives them distinct codes.
//   - an expired web session is a correctly signed roger_session cookie whose expiry has passed,
//     sent from the allow-listed Origin.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type ee6State struct {
	*sa6H
	bodies [][]byte
}

func (s *ee6State) env() (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(s.lastBody, &m); err != nil {
		return nil, fmt.Errorf("the body is not JSON: %.300s", s.lastBody)
	}
	e, ok := m["error"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the body has no error object: %.300s", s.lastBody)
	}
	return e, nil
}

func (s *ee6State) meta() (map[string]any, error) {
	e, err := s.env()
	if err != nil {
		return nil, err
	}
	md, ok := e["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("error.metadata is not an object (%v): %.300s", e["metadata"], s.lastBody)
	}
	return md, nil
}

// --- the shape ---------------------------------------------------------------------------------

func (s *ee6State) shape() error {
	e, err := s.env()
	if err != nil {
		return err
	}
	for _, k := range []string{"code", "message", "type", "metadata"} {
		if _, ok := e[k]; !ok {
			return fmt.Errorf("error has no %q: %.300s", k, s.lastBody)
		}
	}
	return nil
}

func (s *ee6State) typeIs(want string) error {
	e, err := s.env()
	if err != nil {
		return err
	}
	if e["type"] != want {
		return fmt.Errorf("error.type %v, want %q (%.300s)", e["type"], want, s.lastBody)
	}
	return nil
}

func (s *ee6State) metaObject() error { _, err := s.meta(); return err }

func (s *ee6State) metaRequestID() error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	if h := s.lastHdr.Get("X-RogerAI-Request-Id"); h == "" || md["request_id"] != h {
		return fmt.Errorf("error.metadata.request_id %v, header %q", md["request_id"], h)
	}
	return nil
}

func (s *ee6State) codeAndType(status, code, typ string) error {
	if err := s.statusCode(status, code); err != nil {
		return err
	}
	return s.typeIs(typ)
}

// --- Givens -------------------------------------------------------------------------------------

func (s *ee6State) nothing() error { return nil }

func (s *ee6State) balanceOf(user, bal string) error {
	if err := s.newUser("__low", rs1f(bal)); err != nil {
		return err
	}
	s.users[user] = s.users["__low"]
	s.cur = "__low"
	s.as(user)
	return nil
}

func (s *ee6State) offAirBetween(name string) error {
	st := s.st(name)
	s.b.mu.Lock()
	delete(s.b.tunnels, st.id)
	s.b.mu.Unlock()
	return nil
}

func (s *ee6State) unread(name string) *fstation {
	st := s.st(name)
	s.b.mu.Lock()
	s.b.tunnels[st.id] = &nodeTunnel{jobs: make(chan protocol.Job), waiters: map[string]chan protocol.JobResult{}, token: st.tun.token}
	s.b.mu.Unlock()
	return st
}

func (s *ee6State) noFreeSlot(name string) error {
	st := s.unread(name)
	s.alone(name, st.model)
	s.b.metricsMu.Lock()
	s.b.inflight[st.id] = capacityOf(s.b.concurrentTPS[st.id], "")
	s.b.metricsMu.Unlock()
	return nil
}

func (s *ee6State) noPoller(name string) error { s.unread(name); return nil }

func (s *ee6State) slowerThanWindow(name string) error {
	nonStreamRelayWait = 400 * time.Millisecond
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		utRealCompletion(w)
	})
	return nil
}

func (s *ee6State) exhausted(user string) error { s.as(user); return s.rateLimitExhausted(user) }

func (s *ee6State) grantRPM(_, rpm string) error {
	owner := s.st("s1")
	secret := "rog-grant_" + s.nonce
	sum := sha256.Sum256([]byte(secret))
	s.grantID, s.grantSecret = "grant_"+s.nonce, secret
	g := store.Grant{ID: s.grantID, SecretHash: hex.EncodeToString(sum[:]), Owner: owner.acct, Label: "ee6",
		Models: []string{"m"}, Free: true, RPM: rs1f(rpm), Burst: 1, CreatedAt: time.Now().Unix()}
	if err := s.db.CreateGrant(g); err != nil {
		return err
	}
	if err := rs1GrantWalletRow(s.db, g); err != nil {
		return err
	}
	if err := s.send(sa6Spec{caller: "grant", model: "m", hdr: map[string]string{}}); err != nil {
		return err
	}
	return s.statusIs("200")
}

func (s *ee6State) grantRelays(model string) error {
	return s.send(sa6Spec{caller: "grant", model: model, hdr: map[string]string{}})
}

func (s *ee6State) grantNoStation(string) error {
	if err := s.mint(s.st("s1"), []string{"m"}, 0); err != nil {
		return err
	}
	s.goOffAir("s1")
	return nil
}

func (s *ee6State) badSig(model string) error {
	q := sa6Spec{caller: "user", model: model, hdr: map[string]string{}}
	req := s.rs1(q)
	sent, _ := s.buildBody(req)
	r := s.httpRequest(req, sent)
	r.Header.Set(protocol.HeaderSig, strings.Repeat("0", 128))
	return s.raw(r)
}

func (s *ee6State) raw(r *http.Request) error {
	w := &recWriter{ResponseRecorder: httptest.NewRecorder()}
	s.b.relay(w, r)
	s.lastCode, s.lastBody, s.lastHdr = w.Code, w.Body.Bytes(), w.Header()
	s.bodies = append(s.bodies, s.lastBody)
	return nil
}

func (s *ee6State) unsignedUser() error {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Roger-User", "someone")
	return s.raw(r)
}

func (s *ee6State) expiredCookie(model string) error {
	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://rogerai.fm")
	exp := time.Now().Add(-time.Hour).Unix()
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.b.signSessionFull("buyer", 1, s.wallet, "", exp)})
	return s.raw(r)
}

func (s *ee6State) anonRelays(model, with string) error {
	q, err := s.spec("", model, with)
	if err != nil {
		return err
	}
	q.caller = "anon"
	return s.send(q)
}

func (s *ee6State) streams(user, model, with string) error {
	q, err := s.spec(user, model, with)
	if err != nil {
		return err
	}
	q.stream = true
	return s.send(q)
}

func (s *ee6State) flagsCategory(string) error { s.illegal = true; return nil }

func (s *ee6State) classifierDown() error {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	s.b.mod.url, s.b.mod.mode, s.b.mod.require = url, modeSync, true
	return nil
}

func (s *ee6State) bandOffAir(band, code string) error {
	if _, err := s.band(band, code, "p1", "m", nil); err != nil {
		return err
	}
	s.goOffAir("p1")
	return nil
}

func (s *ee6State) bandDenies(band, code, model string) error {
	_, err := s.band(band, code, "p1", "x-other", []string{"x-other"})
	return err
}

func (s *ee6State) threeSituations() error {
	if _, err := s.band("B1", "FREQ-1", "p1", "m", nil); err != nil {
		return err
	}
	s.goOffAir("p1")
	_, err := s.band("B2", "FREQ-2", "p2", "x-other", []string{"x-other"})
	return err
}

func (s *ee6State) triggersEach(user string) error {
	s.bodies = nil
	for _, code := range []string{"WRONG", "FREQ-1", "FREQ-2"} {
		if err := s.send(sa6Spec{user: user, caller: "user", model: "m", hdr: map[string]string{"X-Roger-Freq": code}}); err != nil {
			return err
		}
		s.bodies = append(s.bodies, s.lastBody)
	}
	return nil
}

func (s *ee6State) identicalBodies() error {
	var norm []string
	for i, b := range s.bodies {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		e, _ := m["error"].(map[string]any)
		md, _ := e["metadata"].(map[string]any)
		if md == nil || md["request_id"] == nil {
			return fmt.Errorf("refusal %d carries no error.metadata.request_id: %s", i+1, b)
		}
		delete(md, "request_id")
		out, _ := json.Marshal(m)
		norm = append(norm, string(out))
	}
	for i := 1; i < len(norm); i++ {
		if norm[i] != norm[0] {
			return fmt.Errorf("refusal bodies differ:\n %s\n %s", norm[0], norm[i])
		}
	}
	return nil
}

// --- upstream failures --------------------------------------------------------------------------

func (s *ee6State) statusBody(name, code, body string) error {
	st := s.st(name)
	st.scriptStatus(rs1i(code), body, nil)
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rs1i(code))
		_, _ = w.Write([]byte(body))
	})
	return nil
}

func (s *ee6State) status429RA(name, ra string) error {
	s.script429For(name, ra)
	return nil
}

func (s *ee6State) textBody(name, code, text string) error {
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(rs1i(code))
		_, _ = w.Write([]byte(text))
	})
	return nil
}

func (s *ee6State) bigBody(name, code, kib string) error {
	return s.textBody(name, code, strings.Repeat("y", rs1i(kib)<<10))
}

func (s *ee6State) answers(name, code string) error {
	return s.statusBody(name, code, utDefaultBody(rs1i(code)))
}

func (s *ee6State) both503(a, b, model string) error {
	s.onAir(b, model, 0.10, 0.30)
	for _, n := range []string{a, b} {
		if err := s.answers(n, "503"); err != nil {
			return err
		}
	}
	return nil
}

func (s *ee6State) bandStation500(band, code, name string) error {
	if _, err := s.band(band, code, name, "m", nil); err != nil {
		return err
	}
	return s.answers(name, "500")
}

func (s *ee6State) metaString(key, want string) error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	v := fmt.Sprint(md[key])
	if key == "station" {
		v = s.nameOfID(v)
	}
	if v != want {
		return fmt.Errorf("error.metadata.%s %v, want %q", key, md[key], want)
	}
	return nil
}

func (s *ee6State) metaNum(key, want string) error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	if f, ok := md[key].(float64); !ok || f != rs1f(want) {
		return fmt.Errorf("error.metadata.%s %v, want %s", key, md[key], want)
	}
	return nil
}

func (s *ee6State) rawContains(str string) error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	r, ok := md["raw"].(string)
	if !ok || !strings.Contains(r, str) {
		return fmt.Errorf("error.metadata.raw %v does not contain %q", md["raw"], str)
	}
	return nil
}

func (s *ee6State) rawIs(str string) error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	if r, ok := md["raw"].(string); !ok || r != str {
		return fmt.Errorf("error.metadata.raw %v, want the string %q", md["raw"], str)
	}
	return nil
}

func (s *ee6State) rawAtMost(n string) error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	r, ok := md["raw"].(string)
	if !ok {
		return fmt.Errorf("error.metadata.raw is absent")
	}
	if len(r) > rs1i(n) {
		return fmt.Errorf("error.metadata.raw is %d bytes, want at most %s", len(r), n)
	}
	return nil
}

func (s *ee6State) rawPresent() error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	if _, ok := md["raw"]; !ok {
		return fmt.Errorf("error.metadata.raw is absent: %.300s", s.lastBody)
	}
	return nil
}

func (s *ee6State) noStationNoModel() error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	for _, k := range []string{"station", "model"} {
		if _, ok := md[k]; ok {
			return fmt.Errorf("error.metadata carries %q", k)
		}
	}
	return nil
}

func (s *ee6State) noStation() error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	if _, ok := md["station"]; ok {
		return fmt.Errorf("error.metadata carries station %v", md["station"])
	}
	return nil
}

func (s *ee6State) filtersAre(list string) error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	got, _ := md["filters"].([]any)
	var g []string
	for _, v := range got {
		g = append(g, fmt.Sprint(v))
	}
	if want := mo6Quoted(list); strings.Join(g, ",") != strings.Join(want, ",") {
		return fmt.Errorf("error.metadata.filters %v, want %v", g, want)
	}
	return nil
}

func (s *ee6State) towerServes(name, model string) error {
	_, err := s.tower(name, model, 1, 0)
	return err
}

// --- headers -------------------------------------------------------------------------------------

func (s *ee6State) requestIDPresent() error {
	if s.lastHdr.Get("X-RogerAI-Request-Id") == "" {
		return fmt.Errorf("no X-RogerAI-Request-Id (%d %.200s)", s.lastCode, s.lastBody)
	}
	return nil
}

func (s *ee6State) retryAfterIs(n string) error { return s.headerIs("Retry-After", n) }

func (s *ee6State) raMatches() error {
	md, err := s.meta()
	if err != nil {
		return err
	}
	if fmt.Sprint(md["retry_after_s"]) != s.lastHdr.Get("Retry-After") || s.lastHdr.Get("Retry-After") == "" {
		return fmt.Errorf("error.metadata.retry_after_s %v, Retry-After %q", md["retry_after_s"], s.lastHdr.Get("Retry-After"))
	}
	return nil
}

// --- streams ------------------------------------------------------------------------------------

func (s *ee6State) plainJSONEnvelope() error {
	if err := s.statusIs("503"); err != nil {
		return err
	}
	if ct := s.lastHdr.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return fmt.Errorf("Content-Type %q", ct)
	}
	return s.shape()
}

func (s *ee6State) frameThenFail(name string) error {
	s.hitScript(name, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"part\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
	})
	return nil
}

func (s *ee6State) frames() []string { return sseFrames(s.lastBody) }

func (s *ee6State) errorFrame() error {
	for _, f := range s.frames() {
		var m map[string]any
		if json.Unmarshal([]byte(f), &m) != nil {
			continue
		}
		if e, ok := m["error"].(map[string]any); ok {
			for _, k := range []string{"code", "message", "type", "metadata"} {
				if _, ok := e[k]; !ok {
					return fmt.Errorf("the error frame lacks %q: %s", k, f)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("no error data frame in the stream: %.400s", s.lastBody)
}

func (s *ee6State) usageAfterError() error {
	seenErr := false
	for _, f := range s.frames() {
		var m map[string]any
		if json.Unmarshal([]byte(f), &m) != nil {
			continue
		}
		if _, ok := m["error"]; ok {
			seenErr = true
			continue
		}
		if seenErr {
			if u, ok := m["usage"].(map[string]any); ok && u["rogerai"] != nil {
				return nil
			}
		}
	}
	return fmt.Errorf("no broker usage chunk after the error frame: %.400s", s.lastBody)
}

func (s *ee6State) doneLast() error {
	fr := s.frames()
	if len(fr) == 0 || fr[len(fr)-1] != "[DONE]" {
		return fmt.Errorf("the last frame is not [DONE]: %v", fr)
	}
	return nil
}

// --- discovery -------------------------------------------------------------------------------------

func (s *ee6State) consumerGets(path string) error {
	code, body, hdr := s.get(s.b, path)
	s.lastCode, s.lastBody, s.lastHdr = code, body, hdr
	return nil
}

func (s *ee6State) userGets(user, path string) error {
	s.as(user)
	return s.consumerGets(path)
}

func (s *ee6State) typeAndMeta() error {
	e, err := s.env()
	if err != nil {
		return err
	}
	if _, ok := e["type"]; !ok {
		return fmt.Errorf("no error.type: %.200s", s.lastBody)
	}
	_, err = s.meta()
	return err
}

func (s *ee6State) codeAndEnvelope(status, code string) error {
	if err := s.statusCode(status, code); err != nil {
		return err
	}
	return s.shape()
}

// --- privacy ----------------------------------------------------------------------------------------

func (s *ee6State) everyPath() error {
	s.bodies = nil
	_, _ = s.band("B1", "FREQ-1", "p1", "m", nil)
	steps := []func() error{
		func() error { return s.relays("u-1", "", "", "m", ` with roger.bogus 1`) },
		func() error { return s.relays("u-1", "", "", "m", ` with roger.region ["ap"]`) },
		func() error { return s.relays("u-1", "", "", "m", ` with X-Roger-Freq "FREQ-1" and roger.bogus 1`) },
		func() error { return s.badSig("m") },
		func() error { return s.unsignedUser() },
		func() error { return s.anonRelays("m", "") },
	}
	for _, f := range steps {
		if err := f(); err != nil {
			return err
		}
		s.bodies = append(s.bodies, s.lastBody)
	}
	return nil
}

var ee6IP = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

func (s *ee6State) noPII() error {
	code := s.codes["FREQ-1"]
	for _, b := range s.bodies {
		str := string(b)
		if strings.Contains(str, "u_gh_") || strings.Contains(str, "u_email_") || ee6IP.MatchString(str) ||
			strings.Contains(str, "FREQ-1") || (code != "" && strings.Contains(str, code)) {
			return fmt.Errorf("an error body leaks an id, IP or band code: %s", str)
		}
	}
	if len(s.bodies) == 0 {
		return fmt.Errorf("no refusal bodies collected")
	}
	return nil
}

func (s *ee6State) badBearer(model, auth string) error {
	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", auth)
	return s.raw(r)
}

func (s *ee6State) bodyLacks(str string) error {
	if strings.Contains(string(s.lastBody), str) {
		return fmt.Errorf("the error body contains %q", str)
	}
	return nil
}

func TestErrorEnvelopeBDD(t *testing.T) {
	sa6Run(t, "../../features/errors/error_envelope.feature", func(sc *godog.ScenarioContext, h *sa6H) {
		s := &ee6State{sa6H: h}
		sc.Step(`^the body is \{"error": \{"code", "message", "type", "metadata"\}\}$`, s.shape)
		sc.Step(`^error\.type is "([^"]+)"$`, s.typeIs)
		sc.Step(`^error\.metadata is an object$`, s.metaObject)
		sc.Step(`^error\.metadata\.request_id equals X-RogerAI-Request-Id$`, s.metaRequestID)
		sc.Step(`^nothing$`, s.nothing)
		sc.Step(`^"([^"]+)" has a balance of \$([0-9.]+)$`, s.balanceOf)
		sc.Step(`^"([^"]+)" goes off air between the pick and the dispatch$`, s.offAirBetween)
		sc.Step(`^the response is (\d+) with error code "([^"]+)" and type "([^"]+)"$`, s.codeAndType)
		sc.Step(`^"([^"]+)" is the only station and has no free slot$`, s.noFreeSlot)
		sc.Step(`^"([^"]+)" has no poller listening on any instance$`, s.noPoller)
		sc.Step(`^"([^"]+)" takes longer than the non-stream relay window$`, s.slowerThanWindow)
		sc.Step(`^"([^"]+)" has exhausted its relay (?:rate )?bucket$`, s.exhausted)
		sc.Step(`^error\.metadata\.retry_after_s equals the Retry-After header$`, s.raMatches)
		sc.Step(`^a grant "([^"]+)" with rpm (\d+) that was just used$`, s.grantRPM)
		sc.Step(`^the grant holder relays for "([^"]+)"$`, s.grantRelays)
		sc.Step(`^a caller relays for "([^"]+)" with a signature that does not verify$`, s.badSig)
		sc.Step(`^an unsigned caller with an X-Roger-User header relays for a paid station$`, s.unsignedUser)
		sc.Step(`^a browser with an expired session cookie relays for "([^"]+)"$`, s.expiredCookie)
		sc.Step(`^an anonymous caller relays for "([^"]+)"( with .+|)$`, s.anonRelays)
		sc.Step(`^a grant "([^"]+)" whose owner has no station on air$`, s.grantNoStation)
		sc.Step(`^sync moderation flags the prompt(?: as category "([^"]+)")?$`, s.flagsCategory)
		sc.Step(`^the body does not contain "([^"]+)"$`, s.bodyLacks)
		sc.Step(`^sync moderation is required and the classifier is unreachable$`, s.classifierDown)
		sc.Step(`^no band has code "([^"]+)"$`, func(string) error { return nil })
		sc.Step(`^band "([^"]+)" with code "([^"]+)" is off air$`, s.bandOffAir)
		sc.Step(`^band "([^"]+)" with code "([^"]+)" denies model "([^"]+)"$`, s.bandDenies)
		sc.Step(`^the message is "([^"]+)"$`, s.errorMessageIs)
		sc.Step(`^error\.metadata has no station and no model$`, s.noStationNoModel)
		sc.Step(`^error\.metadata has no station$`, s.noStation)
		sc.Step(`^the three private-band refusal situations above$`, s.threeSituations)
		sc.Step(`^"([^"]+)" triggers each once$`, s.triggersEach)
		sc.Step(`^the three bodies are identical after removing error\.metadata\.request_id$`, s.identicalBodies)
		sc.Step(`^"([^"]+)" answers with status (\d+) and body (\{.+\})$`, s.statusBody)
		sc.Step(`^"([^"]+)" answers with status 429 and Retry-After (\d+)$`, s.status429RA)
		sc.Step(`^"([^"]+)" answers with status (\d+) and the text body "([^"]+)"$`, s.textBody)
		sc.Step(`^"([^"]+)" answers with status (\d+) and a (\d+) KiB body$`, s.bigBody)
		sc.Step(`^"([^"]+)" answers (\d+)$`, s.answers)
		sc.Step(`^error\.metadata\.raw contains "([^"]+)"$`, s.rawContains)
		sc.Step(`^error\.metadata\.(station|model) is "([^"]+)"$`, s.metaString)
		sc.Step(`^error\.metadata\.(retry_after_s|attempts) is (\d+)$`, s.metaNum)
		sc.Step(`^error\.metadata\.raw is the string "([^"]+)"$`, s.rawIs)
		sc.Step(`^error\.metadata\.raw is at most (\d+) bytes$`, s.rawAtMost)
		sc.Step(`^error\.metadata\.raw is present$`, s.rawPresent)
		sc.Step(`^"([^"]+)" and "([^"]+)" are on air for "([^"]+)" and both answer 503$`, s.both503)
		sc.Step(`^a private band "([^"]+)" with code "([^"]+)" whose station "([^"]+)" answers 500$`, s.bandStation500)
		sc.Step(`^an approved Tower "([^"]+)" serves "([^"]+)"$`, s.towerServes)
		sc.Step(`^error\.metadata\.filters is \[(.*)\]$`, s.filtersAre)
		sc.Step(`^X-RogerAI-Request-Id is present$`, s.requestIDPresent)
		sc.Step(`^Retry-After is "(\d+)"$`, s.retryAfterIs)
		sc.Step(`^"([^"]+)" streams for "([^"]+)"( with .+|)$`, s.streams)
		sc.Step(`^the response is 503 with Content-Type application/json and the envelope$`, s.plainJSONEnvelope)
		sc.Step(`^"([^"]+)" sends one content frame then fails$`, s.frameThenFail)
		sc.Step(`^one data frame is \{"error": \{"code", "message", "type", "metadata"\}\}$`, s.errorFrame)
		sc.Step(`^the broker's usage chunk follows it$`, s.usageAfterError)
		sc.Step(`^"\[DONE\]" is last$`, s.doneLast)
		sc.Step(`^a consumer GETs (\S+)$`, s.consumerGets)
		sc.Step(`^the body has error\.type and error\.metadata$`, s.typeAndMeta)
		sc.Step(`^"([^"]+)" GETs (\S+)$`, s.userGets)
		sc.Step(`^the response is (\d+) with error code "([^"]+)" and the envelope$`, s.codeAndEnvelope)
		sc.Step(`^every refusal path above has been triggered once, including with band code "FREQ-1"$`, s.everyPath)
		sc.Step(`^no error body contains a "u_gh_" or "u_email_" id, an IP address, or "FREQ-1"$`, s.noPII)
		sc.Step(`^a caller relays for "([^"]+)" with Authorization "([^"]+)" that is invalid$`, s.badBearer)
		sc.Step(`^the error body does not contain "([^"]+)"$`, s.bodyLacks)
	})
}

var _ = ed25519.GenerateKey

// sameApartFromRequestID compares two refusal bodies the way the no-oracle rules need since the
// envelope (§14.B6): every error carries its own metadata.request_id, every other byte must match.
func sameApartFromRequestID(a, b []byte) bool {
	return bytes.Equal(envRequestID.ReplaceAll(bytes.TrimSpace(a), nil), envRequestID.ReplaceAll(bytes.TrimSpace(b), nil))
}

var envRequestID = regexp.MustCompile(`"request_id":"[^"]*"`)
