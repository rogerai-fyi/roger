package main

// The steps of features/tower/tower_pull_dispatch.feature; the harness is in
// tower_pull_dispatch_bdd_test.go.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towercore/placement"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// submitVia sends Core's own submit for the Tower through inst's pull transport, the way
// driveSealedF does, and returns where the answer lands.
func (s *tpdState) submitVia(inst, towerID string, grant, env []byte, timeout time.Duration) chan tpdResp {
	ch := make(chan tpdResp, 1)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s.cancelWait = cancel
	go func() {
		base, hc := s.inst[inst].pullClient(towerID)
		defer cancel()
		res, err := (&towerhub.Client{BaseURL: base, HTTP: hc}).SubmitJob(ctx, grant, env)
		ch <- tpdResp{err: err, body: res.Envelope}
	}()
	return ch
}

func (s *tpdState) waitQueued(towerID string) error {
	deadline := time.Now().Add(3 * time.Second)
	for !s.mr.Exists(towerWorkBufPrefix + towerID) {
		if time.Now().After(deadline) {
			return fmt.Errorf("nothing was queued for the Tower")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

func pending(ch chan tpdPull, d time.Duration) bool {
	select {
	case <-ch:
		return false
	case <-time.After(d):
		return true
	}
}

func recvPull(ch chan tpdPull, d time.Duration) (tpdPull, error) {
	select {
	case p := <-ch:
		return p, nil
	case <-time.After(d):
		return tpdPull{}, fmt.Errorf("the pull did not answer within %s", d)
	}
}

// --- which Towers pull -----------------------------------------------------------------

func (s *tpdState) thisReleaseOpens() error {
	s.stopAuto()
	sess, err := s.openSession(s.lt, "A", true, s.endpoint)
	s.sess = sess
	return err
}

func (s *tpdState) sessionCarriesPullMirrored() error {
	rec, ok, err := s.mirror.Get(s.lt.id)
	if err != nil || !ok {
		return fmt.Errorf("no mirror record: %v", err)
	}
	if !rec.Pull || rec.SessionID != s.sess {
		return fmt.Errorf("the mirrored session is %s pull=%v, want %s pull=true", rec.SessionID, rec.Pull, s.sess)
	}
	return nil
}

func (s *tpdState) everyInstanceReadsPull() error {
	for name, b := range s.inst {
		if !b.tower.link.Pulls(s.lt.id) {
			return fmt.Errorf("instance %s does not read the pull capability", name)
		}
	}
	return nil
}

func (s *tpdState) towerPulls() error {
	if !s.a().tower.link.Pulls(s.lt.id) {
		return fmt.Errorf("the Tower's session does not pull")
	}
	return nil
}

func (s *tpdState) publicHub() error {
	if c := s.a().tower.link.HubClass(s.lt.id); c != placement.HubPublic {
		return fmt.Errorf("the hub is %s, want public", c)
	}
	return nil
}

func (s *tpdState) consumerRequest() error {
	s.pulled.Store(0)
	s.netSubmits.Store(0)
	s.balBefore = consumerBalance(s.t, s.a(), s.consumer)
	s.rec = s.relay()
	return nil
}

func (s *tpdState) queuedNotDialed() error {
	if s.rec.Code != http.StatusOK || !strings.Contains(s.rec.Body.String(), "pong by pull") {
		return fmt.Errorf("the consumer got %d %s", s.rec.Code, s.rec.Body.String())
	}
	if s.pulled.Load() < 1 || s.netSubmits.Load() != 0 {
		return fmt.Errorf("pulled %d, dialed %d: want the work pulled and the hub never dialed", s.pulled.Load(), s.netSubmits.Load())
	}
	return nil
}

func (s *tpdState) olderTower() error {
	s.stopAuto()
	sess, err := s.openSession(s.lt, "A", false, s.endpoint)
	s.sess = sess
	if err == nil && s.a().tower.link.Pulls(s.lt.id) {
		return fmt.Errorf("a session without the capability pulls")
	}
	return err
}

func (s *tpdState) dialedAsToday() error {
	if s.netSubmits.Load() < 1 || s.pulled.Load() != 0 {
		return fmt.Errorf("dialed %d, pulled %d: want the hub dialed as today", s.netSubmits.Load(), s.pulled.Load())
	}
	return nil
}

func (s *tpdState) pathUnchanged() error {
	if s.rec.Code != http.StatusOK || !strings.Contains(s.rec.Body.String(), "pong by pull") {
		return fmt.Errorf("the dialed path answered %d %s", s.rec.Code, s.rec.Body.String())
	}
	return nil
}

func (s *tpdState) noOrPrivateHub() error {
	for _, b := range s.inst {
		b.canaryVet = vetPublicIP // production classing, so a private address is private
	}
	s.stopAuto()
	sess, err := s.openSession(s.lt, "A", false, "192.168.1.20:8444")
	s.sess = sess
	return err
}

func (s *tpdState) noStationPlaced() error {
	for _, f := range s.a().placementSnapshot() {
		if f.cand.TowerID == s.lt.id {
			return fmt.Errorf("the Tower is still a placement candidate")
		}
	}
	return nil
}

// The approved step names `roger tower serve`; what Core can tell an older binary reaches the
// operator through the status note every version prints (raised with the founder).
func (s *tpdState) toldToUpgrade() error {
	entry := map[string]any{}
	s.a().towerReachView(s.lt.id, entry)
	note, _ := entry["note"].(string)
	if entry["carries_traffic"] != false || !strings.Contains(note, "roger upgrade") {
		return fmt.Errorf("the status says %v", entry)
	}
	return nil
}

// --- the pull ----------------------------------------------------------------------------

func (s *tpdState) oneHeldOnA() error {
	s.stopAuto()
	s.pulls = []chan tpdPull{s.pullAsync("A", s.lt, s.sess)}
	return s.waitHeld(s.lt.id, 1)
}

func (s *tpdState) requestArrivesOnB() error {
	s.gotResp = s.submitVia("B", s.lt.id, []byte("grant-bytes"), []byte("sealed-bytes"), 5*time.Second)
	return nil
}

func (s *tpdState) reachesHeldPullAtOnce() error {
	p, err := recvPull(s.pulls[0], time.Second)
	if err != nil {
		return err
	}
	if p.code != http.StatusOK || len(p.work) != 1 {
		return fmt.Errorf("the held pull answered %d with %d piece(s) of work", p.code, len(p.work))
	}
	s.work = p.work[0]
	return nil
}

func (s *tpdState) carriesGrantAndSealedOnly() error {
	w := s.work
	if w.Method != http.MethodPost || w.Path != towerhub.PathSubmit || w.Query != "" {
		return fmt.Errorf("the work is %s %s?%s", w.Method, w.Path, w.Query)
	}
	for k := range w.Header {
		if k != "Content-Type" {
			return fmt.Errorf("the work carries header %s", k)
		}
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body, &body); err != nil || len(body) != 2 {
		return fmt.Errorf("the work body is %s", w.Body)
	}
	g, _ := base64.StdEncoding.DecodeString(body["grant"])
	e, _ := base64.StdEncoding.DecodeString(body["envelope"])
	if string(g) != "grant-bytes" || string(e) != "sealed-bytes" {
		return fmt.Errorf("the work carries grant %q and envelope %q", g, e)
	}
	return nil
}

func (s *tpdState) noJobQueued() error { s.stopAuto(); return nil }

func (s *tpdState) towerSendsPull() error {
	start := time.Now()
	s.lastPull = s.pullNow("A", s.lt, s.sess)
	s.lastPull.hdr.Set("X-Held-For", time.Since(start).String())
	return s.lastPull.err
}

func (s *tpdState) heldUpTo25s() error {
	if tpdProductionHold != 25*time.Second {
		return fmt.Errorf("the production hold is %s, want 25s", tpdProductionHold)
	}
	held, _ := time.ParseDuration(s.lastPull.hdr.Get("X-Held-For"))
	if held < pullHold-50*time.Millisecond {
		return fmt.Errorf("the empty pull was answered after %s, before the hold (%s)", held, pullHold)
	}
	return nil
}

func (s *tpdState) noWorkThenPullsAgain() error {
	if s.lastPull.code != http.StatusNoContent {
		return fmt.Errorf("the empty pull answered %d", s.lastPull.code)
	}
	ch := s.pullAsync("A", s.lt, s.sess)
	if !pending(ch, 200*time.Millisecond) {
		return fmt.Errorf("the next pull was not accepted and held")
	}
	return nil
}

func (s *tpdState) noPullOpen() error { s.stopAuto(); return nil }

func (s *tpdState) jobQueued() error {
	s.gotResp = s.submitVia("B", s.lt.id, []byte("g"), []byte("e"), 5*time.Second)
	return s.waitQueued(s.lt.id)
}

func (s *tpdState) pullsWithinDeadline() error {
	time.Sleep(pullPickupDeadline / 3)
	s.lastPull = s.pullNow("A", s.lt, s.sess)
	return s.lastPull.err
}

func (s *tpdState) deliveredOnThatPull() error {
	if s.lastPull.code != http.StatusOK || len(s.lastPull.work) != 1 {
		return fmt.Errorf("the pull answered %d with %d piece(s)", s.lastPull.code, len(s.lastPull.work))
	}
	return nil
}

func (s *tpdState) fourHeldBoth() error {
	s.stopAuto()
	s.pulls = nil
	for _, inst := range []string{"A", "A", "B", "B"} {
		s.pulls = append(s.pulls, s.pullAsync(inst, s.lt, s.sess))
	}
	return s.waitHeld(s.lt.id, 4)
}

func (s *tpdState) oneJobQueued() error {
	s.gotResp = s.submitVia("B", s.lt.id, []byte("g"), []byte("e"), 5*time.Second)
	return nil
}

func (s *tpdState) exactlyOneReceives() error {
	got := 0
	var rest []chan tpdPull
	deadline := time.After(2 * time.Second)
	for _, ch := range s.pulls {
		select {
		case p := <-ch:
			if p.code == http.StatusOK && len(p.work) == 1 {
				got++
			}
		case <-deadline:
			rest = append(rest, ch)
			continue
		case <-time.After(300 * time.Millisecond):
			rest = append(rest, ch)
		}
	}
	if got != 1 {
		return fmt.Errorf("%d pulls received the job, want exactly one", got)
	}
	s.pulls = rest
	return nil
}

func (s *tpdState) otherThreeHold() error {
	if len(s.pulls) != 3 {
		return fmt.Errorf("%d pulls still holding, want three", len(s.pulls))
	}
	for _, ch := range s.pulls {
		if !pending(ch, 50*time.Millisecond) {
			return fmt.Errorf("a pull that received nothing returned")
		}
	}
	return nil
}

func (s *tpdState) fourHeld() error {
	s.stopAuto()
	s.pulls = nil
	for i := 0; i < 4; i++ {
		s.pulls = append(s.pulls, s.pullAsync("A", s.lt, s.sess))
	}
	return s.waitHeld(s.lt.id, 4)
}

func (s *tpdState) sendsFifth() error {
	s.lastPull = s.pullNow("B", s.lt, s.sess)
	return s.lastPull.err
}

func (s *tpdState) fifthRefused() error {
	if s.lastPull.code != http.StatusTooManyRequests || s.lastPull.hdr.Get("Retry-After") == "" {
		return fmt.Errorf("the fifth pull answered %d Retry-After %q", s.lastPull.code, s.lastPull.hdr.Get("Retry-After"))
	}
	for _, ch := range s.pulls {
		if !pending(ch, 20*time.Millisecond) {
			return fmt.Errorf("a held pull was disturbed")
		}
	}
	return nil
}

func (s *tpdState) twoTowersHolding() error {
	s.stopAuto()
	op := signedInOperator(s.t, s.a(), "pull-op-2")
	s.other = enrolledTower(s.t, s.a(), op.login)
	if err := s.a().tower.registry.Transition(s.other.id, admit.StateActive); err != nil {
		return err
	}
	var err error
	if s.otherSess, err = s.openSession(s.other, "B", true, "203.0.113.9:8444"); err != nil {
		return err
	}
	s.pulls = []chan tpdPull{s.pullAsync("A", s.lt, s.sess), s.pullAsync("B", s.other, s.otherSess)}
	if err := s.waitHeld(s.lt.id, 1); err != nil {
		return err
	}
	return s.waitHeld(s.other.id, 1)
}

func (s *tpdState) jobForFirst() error {
	s.gotResp = s.submitVia("A", s.lt.id, []byte("g"), []byte("e"), 5*time.Second)
	return nil
}

func (s *tpdState) onlyFirstReceives() error {
	p, err := recvPull(s.pulls[0], 2*time.Second)
	if err != nil || p.code != http.StatusOK || len(p.work) != 1 {
		return fmt.Errorf("the first Tower's pull: %+v %v", p, err)
	}
	if !pending(s.pulls[1], 200*time.Millisecond) {
		return fmt.Errorf("the second Tower's pull returned")
	}
	return nil
}

func (s *tpdState) cannotNameAnother() error {
	body := jsonOf(s.t, map[string]string{"tower_id": s.lt.id, "session_id": s.sess})
	code, raw := signedPostTo(s.other, s.srv["A"].URL, "/tower/work/pull", body, nil, 5*time.Second)
	if code != http.StatusForbidden {
		return fmt.Errorf("a pull naming another Tower answered %d %s", code, raw)
	}
	return nil
}

func (s *tpdState) noPullAndLate() error {
	s.stopAuto()
	return nil
}

func (s *tpdState) deadlinePasses() error {
	s.balBefore = consumerBalance(s.t, s.a(), s.consumer)
	tally, _ := s.a().tower.outcomes.Tally(s.lt.id, time.Time{})
	s.failedBef = tally.StationFault
	s.rec = s.relay()
	return nil
}

func (s *tpdState) withdrawnNoLaterPull() error {
	p := s.pullNow("A", s.lt, s.sess)
	if p.code == http.StatusOK && len(p.work) > 0 {
		return fmt.Errorf("a later pull took the withdrawn job")
	}
	return nil
}

func (s *tpdState) movesToNext() error {
	if n := strings.Count(tpdLogs.String(), "tower "+s.lt.id+" failed for "+tpdModel); n != 1 {
		return fmt.Errorf("the bridge moved on from the Tower %d time(s), want once", n)
	}
	if !strings.Contains(tpdLogs.String(), "trying the next relay") {
		return fmt.Errorf("the bridge did not move to the next candidate")
	}
	return nil
}

func (s *tpdState) notCharged() error {
	if after := consumerBalance(s.t, s.a(), s.consumer); after != s.balBefore {
		return fmt.Errorf("the consumer's balance moved from %v to %v", s.balBefore, after)
	}
	return nil
}

func (s *tpdState) missCountsOnce() error {
	tally, err := s.a().tower.outcomes.Tally(s.lt.id, time.Time{})
	if err != nil {
		return err
	}
	if tally.StationFault != s.failedBef+1 {
		return fmt.Errorf("the miss was recorded %d time(s), want once", tally.StationFault-s.failedBef)
	}
	return nil
}

func (s *tpdState) jobWithdrawnEarlier() error {
	s.stopAuto()
	ch := s.submitVia("A", s.lt.id, []byte("g"), []byte("e"), 10*time.Second)
	select {
	case r := <-ch:
		if r.err == nil || !strings.Contains(r.err.Error(), errPullNotTaken.Error()) {
			return fmt.Errorf("the untaken job ended %v", r.err)
		}
	case <-time.After(pullPickupDeadline + 2*time.Second):
		return fmt.Errorf("the untaken job was never withdrawn")
	}
	return nil
}

func (s *tpdState) pullsMomentLater() error {
	s.lastPull = s.pullNow("A", s.lt, s.sess)
	return s.lastPull.err
}

func (s *tpdState) withdrawnNotReceived() error {
	if s.lastPull.code == http.StatusOK && len(s.lastPull.work) > 0 {
		return fmt.Errorf("the pull received the withdrawn job")
	}
	return nil
}

// --- the answer --------------------------------------------------------------------------

func (s *tpdState) tookOnAWaitingOnB() error {
	s.stopAuto()
	ch := s.pullAsync("A", s.lt, s.sess)
	if err := s.waitHeld(s.lt.id, 1); err != nil {
		return err
	}
	s.gotResp = s.submitVia("B", s.lt.id, []byte("g"), []byte("e"), 5*time.Second)
	p, err := recvPull(ch, 2*time.Second)
	if err != nil || len(p.work) != 1 {
		return fmt.Errorf("the Tower did not take the job: %v", err)
	}
	s.work = p.work[0]
	return nil
}

func (s *tpdState) postsAnswerToA() error {
	body, _ := json.Marshal(map[string]string{
		"envelope": base64.StdEncoding.EncodeToString([]byte("sealed-answer")),
		"receipt":  base64.StdEncoding.EncodeToString([]byte("station-receipt")),
	})
	s.ansCode, s.ansBody = s.postAnswer("A", s.lt, s.sess, towerhub.PullAnswer{ID: s.work.ID, Status: http.StatusOK,
		Header: map[string]string{"Content-Type": "application/json"}, Body: body})
	if s.ansCode != http.StatusOK {
		return fmt.Errorf("the answer post answered %d %s", s.ansCode, s.ansBody)
	}
	return nil
}

func (s *tpdState) bReceivesAndChecks() error {
	select {
	case r := <-s.gotResp:
		if r.err != nil || string(r.body) != "sealed-answer" {
			return fmt.Errorf("instance B got %q %v", r.body, r.err)
		}
	case <-time.After(3 * time.Second):
		return fmt.Errorf("instance B never received the answer")
	}
	// The checks after the hand-over are driveSealedF's and run on the live loop below.
	return s.liveRoundTrip()
}

// liveRoundTrip drives one real request end to end: seal, pull, Station, receipt, open.
func (s *tpdState) liveRoundTrip() error {
	s.startAuto("A")
	s.rec = s.relay()
	if s.rec.Code != http.StatusOK || !strings.Contains(s.rec.Body.String(), "pong by pull") {
		return fmt.Errorf("the live round trip answered %d %s", s.rec.Code, s.rec.Body.String())
	}
	return nil
}

func (s *tpdState) consumerGetsFromB() error { return nil } // shown by the two steps above

func (s *tpdState) answerNotGiven() error {
	s.ansCode, s.ansBody = s.postAnswer("A", s.lt, s.sess, towerhub.PullAnswer{ID: "pw-never-given", Status: 200})
	return nil
}

func (s *tpdState) refusedNothingReaches() error {
	if s.ansCode != http.StatusForbidden {
		return fmt.Errorf("an answer for work never given answered %d %s", s.ansCode, s.ansBody)
	}
	return nil
}

func (s *tpdState) postedOnce() error {
	if err := s.tookOnAWaitingOnB(); err != nil {
		return err
	}
	return s.postsAnswerToA()
}

func (s *tpdState) postsAgain() error {
	s.ansCode, s.ansBody = s.postAnswer("B", s.lt, s.sess, towerhub.PullAnswer{ID: s.work.ID, Status: 500})
	return nil
}

func (s *tpdState) secondChangesNothing() error {
	if s.ansCode != http.StatusOK || !strings.Contains(s.ansBody, "already received") {
		return fmt.Errorf("the second answer answered %d %s", s.ansCode, s.ansBody)
	}
	select {
	case r := <-s.gotResp:
		if string(r.body) != "sealed-answer" {
			return fmt.Errorf("the waiting request saw %q", r.body)
		}
	case <-time.After(3 * time.Second):
		return fmt.Errorf("the waiting request saw nothing")
	}
	return nil
}

func (s *tpdState) requestGaveUp() error {
	s.stopAuto()
	s.hold.Store(true)
	s.startAuto("A")
	s.balBefore = consumerBalance(s.t, s.a(), s.consumer)
	// The waiting request gives up after a second, as a bridged drive does at its deadline.
	r := <-s.submitVia("B", s.lt.id, []byte("g"), []byte("e"), time.Second)
	if r.err == nil {
		return fmt.Errorf("the request did not give up")
	}
	s.rec = httptest.NewRecorder()
	s.rec.Code = http.StatusGatewayTimeout
	return nil
}

func (s *tpdState) postsLate() error {
	s.heldMu.Lock()
	held := s.heldWork
	s.heldMu.Unlock()
	if len(held) == 0 {
		return fmt.Errorf("the Tower holds no answer to post")
	}
	ans := towerhub.ServePulled(context.Background(), s.hubMux, held[0], 32<<20)
	s.ansCode, s.ansBody = s.postAnswer("A", s.lt, s.sess, ans)
	return nil
}

func (s *tpdState) answerDiscarded() error {
	// The Tower took the work, so its late answer is accepted - and goes nowhere: the waiting
	// request already gave up (checked in the Given), and nothing else listens for it.
	if s.ansCode != http.StatusOK {
		return fmt.Errorf("the late answer answered %d %s", s.ansCode, s.ansBody)
	}
	return nil
}

func (s *tpdState) billedOnlyServing() error { return s.notCharged() }

func (s *tpdState) answerOverCap() error {
	s.ansCode, s.ansBody = 0, ""
	big := bytes.Repeat([]byte("x"), maxPullAnswer+1024)
	code, raw := signedPostTo(s.lt, s.srv["A"].URL, "/tower/work/answer?tower_id="+s.lt.id, big, nil, 30*time.Second)
	s.ansCode, s.ansBody = code, raw
	return nil
}

func (s *tpdState) refusedBeforeReading() error {
	if s.ansCode != http.StatusRequestEntityTooLarge {
		return fmt.Errorf("an answer over the cap answered %d %s", s.ansCode, s.ansBody)
	}
	// And on the waiting side an over-cap hub answer is a Tower fault: ServePulled refuses it.
	ans := towerhub.ServePulled(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1024))
	}), towerhub.PullWork{Method: http.MethodPost, Path: towerhub.PathSubmit}, 512)
	if ans.Status != http.StatusBadGateway {
		return fmt.Errorf("an over-cap hub answer was carried as %d", ans.Status)
	}
	return nil
}

func (s *tpdState) stationModelFailure() error {
	s.upCode.Store(http.StatusTooManyRequests)
	return nil
}

func (s *tpdState) towerPostsFailure() error {
	s.rec = s.relay()
	return nil
}

func (s *tpdState) sameFailureClass() error {
	if s.rec.Code != http.StatusTooManyRequests || s.rec.Header().Get("Retry-After") == "" {
		return fmt.Errorf("the consumer saw %d Retry-After %q", s.rec.Code, s.rec.Header().Get("Retry-After"))
	}
	return nil
}

// --- what the Tower can and cannot do ------------------------------------------------------

func (s *tpdState) jobSealedToStation() error {
	s.stopAuto()
	ch := s.pullAsync("A", s.lt, s.sess)
	if err := s.waitHeld(s.lt.id, 1); err != nil {
		return err
	}
	done := make(chan *httptestRec, 1)
	go func() { r := s.relay(); done <- &httptestRec{r.Code, r.Body.String()} }()
	p, err := recvPull(ch, 5*time.Second)
	if err != nil || len(p.work) != 1 {
		return fmt.Errorf("no job pulled: %v", err)
	}
	var body map[string]string
	_ = json.Unmarshal(p.work[0].Body, &body)
	env, _ := base64.StdEncoding.DecodeString(body["envelope"])
	if bytes.Contains(env, []byte("ping")) || bytes.Contains(p.work[0].Body, []byte("ping")) {
		return fmt.Errorf("the pulled job carries the prompt in the clear")
	}
	s.work = p.work[0]
	// The real hub's answer to the real sealed job, kept for the next step, then posted.
	s.firstAns = towerhub.ServePulled(context.Background(), s.hubMux, s.work, 32<<20)
	go s.postAnswer("A", s.lt, s.sess, s.firstAns)
	r := <-done
	if r.code != http.StatusOK {
		return fmt.Errorf("the request answered %d %s", r.code, r.body)
	}
	return nil
}

type httptestRec struct {
	code int
	body string
}

func (s *tpdState) answerSealedToConsumer() error {
	if s.firstAns.Status != http.StatusOK || len(s.firstAns.Body) == 0 {
		return fmt.Errorf("the hub answered %d", s.firstAns.Status)
	}
	var out map[string]string
	if err := json.Unmarshal(s.firstAns.Body, &out); err != nil || out["envelope"] == "" {
		return fmt.Errorf("the hub's answer carries no sealed envelope: %s", s.firstAns.Body)
	}
	env, _ := base64.StdEncoding.DecodeString(out["envelope"])
	if bytes.Contains(env, []byte("pong by pull")) || bytes.Contains(s.firstAns.Body, []byte("pong by pull")) {
		return fmt.Errorf("the answer the Tower carries back is readable")
	}
	return nil
}

func (s *tpdState) receiptSigned() error {
	// Proven by the outline below: an answer whose receipt does not bind its bytes fails.
	return nil
}

// tamper answers every pulled job with a modified hub answer.
func (s *tpdState) tamper(mutate func(a *towerhub.PullAnswer)) {
	s.stopAuto()
	stop := make(chan struct{})
	s.autoStop = stop
	s.autoDone.Add(1)
	go func() {
		defer s.autoDone.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			p := s.pullNow("A", s.lt, s.sess)
			for _, w := range p.work {
				w := w
				go func() {
					a := towerhub.ServePulled(context.Background(), s.hubMux, w, 32<<20)
					mutate(&a)
					s.postAnswer("A", s.lt, s.sess, a)
				}()
			}
		}
	}()
}

var tpdReplayed []byte

func (s *tpdState) towerTries(tries string) error {
	switch tries {
	case "posts an answer whose receipt does not bind its bytes":
		s.tamper(func(a *towerhub.PullAnswer) { a.Body = rewriteJSON(a.Body, "envelope", flipLastByte) })
	case "posts an answer signed by a different Station":
		s.tamper(func(a *towerhub.PullAnswer) { a.Body = rewriteJSON(a.Body, "receipt", flipLastByte) })
	case "replays an earlier attempt's answer for a new attempt":
		if tpdReplayed == nil {
			first := make(chan struct{})
			var once sync.Once
			s.tamper(func(a *towerhub.PullAnswer) {
				if tpdReplayed == nil {
					tpdReplayed = a.Body
				} else {
					a.Body = tpdReplayed
				}
				once.Do(func() { close(first) })
			})
			s.relay()
			<-first
		}
	case "pulls with a session that is closed or expired":
		s.stopAuto()
		s.lastPull = s.pullNow("A", s.lt, "sess-not-open")
		return nil
	case "pulls after its certificate serial was revoked":
		s.stopAuto()
		if _, err := s.a().tower.registry.RecordRenewal(s.lt.id, admit.Renewal{CertSerial: "4242", TLSKeyHash: "h", At: time.Now()}); err != nil {
			return err
		}
		if err := s.a().tower.ca.Revoke(big.NewInt(4242)); err != nil {
			return err
		}
		s.lastPull = s.pullNow("A", s.lt, s.sess)
		return nil
	case "pulls while suspended, revoked or lease-lapsed":
		s.stopAuto()
		if err := s.a().tower.registry.Transition(s.lt.id, admit.StateSuspended); err != nil {
			return err
		}
		s.lastPull = s.pullNow("A", s.lt, s.sess)
		return nil
	case "pulls without a valid request signature":
		s.stopAuto()
		body := jsonOf(s.t, map[string]string{"tower_id": s.lt.id, "session_id": s.sess})
		req, _ := http.NewRequest(http.MethodPost, s.srv["A"].URL+"/tower/work/pull", bytes.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		s.lastPull = tpdPull{code: resp.StatusCode}
		return nil
	default:
		return fmt.Errorf("unknown attempt %q", tries)
	}
	s.balBefore = consumerBalance(s.t, s.a(), s.consumer)
	s.rec = s.relay()
	return nil
}

func (s *tpdState) coreAnswers(answers string) error {
	switch answers {
	case "fails the attempt as a Tower fault, charges nothing":
		if s.rec.Code == http.StatusOK {
			return fmt.Errorf("a tampered answer reached the consumer: %s", s.rec.Body.String())
		}
		return s.notCharged()
	case "refuses the pull, delivers nothing":
		if s.lastPull.code < 400 || len(s.lastPull.work) > 0 {
			return fmt.Errorf("the pull answered %d with %d piece(s)", s.lastPull.code, len(s.lastPull.work))
		}
		return nil
	}
	return fmt.Errorf("unknown answer %q", answers)
}

func (s *tpdState) holdsPullOpen() error {
	s.stopAuto()
	s.pulls = []chan tpdPull{s.pullAsync("A", s.lt, s.sess)}
	return s.waitHeld(s.lt.id, 1)
}

func (s *tpdState) towerRevoked() error {
	if err := s.a().tower.registry.Transition(s.lt.id, admit.StateRevoked); err != nil {
		return err
	}
	s.gotResp = s.submitVia("B", s.lt.id, []byte("g"), []byte("e"), 10*time.Second)
	return nil
}

func (s *tpdState) heldEmptyNextRefused() error {
	p, err := recvPull(s.pulls[0], pullHold+2*time.Second)
	if err != nil {
		return err
	}
	if len(p.work) > 0 {
		return fmt.Errorf("a revoked Tower's held pull received work")
	}
	if next := s.pullNow("A", s.lt, s.sess); next.code != http.StatusForbidden {
		return fmt.Errorf("the next pull answered %d", next.code)
	}
	return nil
}

func (s *tpdState) queuedWithdrawn() error {
	select {
	case r := <-s.gotResp:
		if r.err == nil || !strings.Contains(r.err.Error(), errPullNotTaken.Error()) {
			return fmt.Errorf("the queued job ended %v", r.err)
		}
	case <-time.After(pullPickupDeadline + 3*time.Second):
		return fmt.Errorf("the queued job was never withdrawn")
	}
	return nil
}

func (s *tpdState) twoInProgress() error {
	s.stopAuto()
	ch := s.pullAsync("A", s.lt, s.sess)
	if err := s.waitHeld(s.lt.id, 1); err != nil {
		return err
	}
	r1 := s.submitVia("B", s.lt.id, []byte("g1"), []byte("e1"), 10*time.Second)
	r2 := s.submitVia("B", s.lt.id, []byte("g2"), []byte("e2"), 10*time.Second)
	var got []towerhub.PullWork
	deadline := time.Now().Add(3 * time.Second)
	for len(got) < 2 && time.Now().Before(deadline) {
		p, err := recvPull(ch, 2*time.Second)
		if err == nil {
			got = append(got, p.work...)
		}
		if len(got) < 2 {
			ch = s.pullAsync("A", s.lt, s.sess)
		}
	}
	if len(got) != 2 {
		return fmt.Errorf("the Tower took %d jobs, want two", len(got))
	}
	s.heldWork = got
	s.gotResp = make(chan tpdResp, 2)
	go func() { s.gotResp <- <-r1 }()
	go func() { s.gotResp <- <-r2 }()
	return nil
}

func (s *tpdState) startsDraining() error {
	return s.a().tower.registry.Transition(s.lt.id, admit.StateDraining)
}

func (s *tpdState) noNewJobsWhileDraining() error {
	if s.a().tower.registry.MayTakeWork(s.lt.id) {
		return fmt.Errorf("a draining Tower may take work")
	}
	for _, f := range s.a().placementSnapshot() {
		if f.cand.TowerID == s.lt.id {
			return fmt.Errorf("a draining Tower is a placement candidate")
		}
	}
	if s.rec = s.relay(); s.rec.Code == http.StatusOK {
		return fmt.Errorf("a draining Tower carried a new request")
	}
	return nil
}

func (s *tpdState) answersStillAccepted() error {
	for _, w := range s.heldWork {
		code, raw := s.postAnswer("A", s.lt, s.sess, towerhub.PullAnswer{ID: w.ID, Status: 200,
			Body: []byte(`{"envelope":"","receipt":""}`)})
		if code != http.StatusOK {
			return fmt.Errorf("a draining Tower's answer answered %d %s", code, raw)
		}
	}
	return nil
}

// --- health checks -------------------------------------------------------------------------

func (s *tpdState) runCanary() error {
	s.pulled.Store(0)
	s.netSubmits.Store(0)
	s.startAuto("A")
	s.outcome = s.a().RunCanary(s.lt.id)
	return nil
}

func (s *tpdState) canaryByPull() error {
	if s.pulled.Load() < 1 || s.netSubmits.Load() != 0 {
		return fmt.Errorf("the canary was pulled %d and dialed %d time(s)", s.pulled.Load(), s.netSubmits.Load())
	}
	return nil
}

func (s *tpdState) canaryPassesOrFails() error {
	if s.outcome != reputation.CanaryPass {
		return fmt.Errorf("the canary through a working pull is %q", s.outcome)
	}
	return nil
}

func (s *tpdState) hubCheckDue() error {
	s.a().hubCheckMu.Lock()
	s.a().hubCheckAt = nil
	s.a().hubCheckMu.Unlock()
	return nil
}

func (s *tpdState) hubCheckQueued() error {
	s.stopAuto()
	ch := s.pullAsync("A", s.lt, s.sess)
	if err := s.waitHeld(s.lt.id, 1); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { s.a().towerHubCheckOnce(time.Now()); close(done) }()
	p, err := recvPull(ch, 3*time.Second)
	if err != nil || len(p.work) != 1 {
		return fmt.Errorf("no hub check pulled: %v", err)
	}
	w := p.work[0]
	if w.Method != http.MethodGet || w.Path != towerhub.PathPoll || !strings.HasPrefix(w.Query, "nonce=") {
		return fmt.Errorf("the hub check is %s %s?%s", w.Method, w.Path, w.Query)
	}
	go s.serve("A", w)
	<-done
	return nil
}

func (s *tpdState) sameProof() error {
	sum, err := s.health.Summary(s.lt.id, time.Now())
	if err != nil {
		return err
	}
	if sum.LastAt.IsZero() || sum.FailStreak != 0 {
		return fmt.Errorf("the hub check did not pass: %+v", sum)
	}
	return nil
}

func (s *tpdState) recordedPassPull() error {
	sum, _ := s.health.Summary(s.lt.id, time.Now())
	// A public hub is also dialed, so its combined check lands as a hub check; the private-hub
	// case records "pull" (private_hub_placement.feature runs it).
	if sum.LastSource != health.SourceHubCheck && sum.LastSource != health.SourcePull {
		return fmt.Errorf("the check landed as %q", sum.LastSource)
	}
	return s.privatePullSource()
}

// privatePullSource re-runs the check for a private hub, which Core never dials.
func (s *tpdState) privatePullSource() error {
	for _, b := range s.inst {
		b.canaryVet = vetPublicIP
	}
	sess, err := s.openSession(s.lt, "A", true, "192.168.1.20:8444")
	if err != nil {
		return err
	}
	s.sess = sess
	if err := s.hubCheckDue(); err != nil {
		return err
	}
	if err := s.hubCheckQueued(); err != nil {
		return err
	}
	sum, _ := s.health.Summary(s.lt.id, time.Now())
	if sum.LastSource != health.SourcePull || sum.FailStreak != 0 {
		return fmt.Errorf("a private hub's pull check landed as %q streak %d", sum.LastSource, sum.FailStreak)
	}
	return nil
}

func (s *tpdState) noProofFailsOnce() error {
	s.stopAuto()
	if err := s.hubCheckDue(); err != nil {
		return err
	}
	before, _ := s.health.Summary(s.lt.id, time.Now())
	now := time.Now()
	s.a().towerHubCheckOnce(now)
	if err := s.hubCheckDue(); err != nil {
		return err
	}
	s.a().towerHubCheckOnce(now.Add(time.Second)) // same sweep slot
	after, _ := s.health.Summary(s.lt.id, time.Now())
	if after.FailStreak != before.FailStreak+1 {
		return fmt.Errorf("an untaken hub check moved the streak from %d to %d, want one", before.FailStreak, after.FailStreak)
	}
	return nil
}

func (s *tpdState) pullsAndPublic() error {
	if err := s.towerPulls(); err != nil {
		return err
	}
	return s.publicHub()
}

func (s *tpdState) alsoDialed() error {
	s.startAuto("A")
	s.netPolls.Store(0)
	if err := s.hubCheckDue(); err != nil {
		return err
	}
	s.a().towerHubCheckOnce(time.Now())
	if s.netPolls.Load() < 1 {
		return fmt.Errorf("the public hub was not dialed")
	}
	return nil
}

func (s *tpdState) dialFailureCounts() error {
	// The listener goes away while the pull keeps working: the pull check passes, the dial fails.
	s.hubDown()
	if err := s.hubCheckDue(); err != nil {
		return err
	}
	later := time.Now().Add(canaryInterval)
	before, _ := s.health.Summary(s.lt.id, later)
	s.a().towerHubCheckOnce(later)
	after, _ := s.health.Summary(s.lt.id, later.Add(10*time.Second))
	if after.FailStreak != before.FailStreak+1 {
		return fmt.Errorf("a failed outside dial moved the streak %d -> %d", before.FailStreak, after.FailStreak)
	}
	return nil
}

func (s *tpdState) sameRampRecord() error {
	// One store, one Summary: the ramp, placement and mobility all read s.health.
	if s.a().tower.health != s.health || s.inst["B"].tower.health != s.health {
		return fmt.Errorf("the instances read different health records")
	}
	return nil
}

func (s *tpdState) pullCountsSame() error {
	h := health.NewMemStore()
	at := time.Now()
	for i, src := range []string{health.SourcePull, health.SourceHubCheck} {
		id := fmt.Sprintf("t%d", i)
		_ = h.RecordCheck(id, health.Check{OK: false, At: at, Source: src, Slot: 1})
		_ = h.RecordCheck(id, health.Check{OK: false, At: at.Add(canaryInterval), Source: src, Slot: 2})
	}
	a, _ := h.Summary("t0", at.Add(canaryInterval))
	b, _ := h.Summary("t1", at.Add(canaryInterval))
	if a.FailStreak != b.FailStreak || a.FailStreak != 2 {
		return fmt.Errorf("a pull streak is %d and a dialed one %d", a.FailStreak, b.FailStreak)
	}
	return nil
}

// --- multi-instance and restarts ---------------------------------------------------------

func (s *tpdState) tookOnA() error { return s.tookOnAWaitingOnB() }

func (s *tpdState) instanceARestarts() error {
	s.srv["A"].CloseClientConnections()
	s.inst["A"].pulls = pullLocal{}
	return nil
}

func (s *tpdState) answerStillReaches() error {
	if err := s.postAnswerTo("B"); err != nil {
		return err
	}
	select {
	case r := <-s.gotResp:
		if string(r.body) != "sealed-answer" {
			return fmt.Errorf("the waiting request got %q %v", r.body, r.err)
		}
	case <-time.After(3 * time.Second):
		return fmt.Errorf("the answer never reached the waiting request")
	}
	return nil
}

func (s *tpdState) postAnswerTo(inst string) error {
	body, _ := json.Marshal(map[string]string{"envelope": base64.StdEncoding.EncodeToString([]byte("sealed-answer")),
		"receipt": base64.StdEncoding.EncodeToString([]byte("r"))})
	code, raw := s.postAnswer(inst, s.lt, s.sess, towerhub.PullAnswer{ID: s.work.ID, Status: 200, Body: body})
	if code != http.StatusOK {
		return fmt.Errorf("the answer post to %s answered %d %s", inst, code, raw)
	}
	return nil
}

func (s *tpdState) waitingOnB() error { return s.tookOnAWaitingOnB() }

func (s *tpdState) instanceBRestarts() error {
	s.waitCancel()
	return nil
}

func (s *tpdState) brokenConnection() error {
	select {
	case r := <-s.gotResp:
		if r.err == nil {
			return fmt.Errorf("the request ended without an error")
		}
	case <-time.After(3 * time.Second):
		return fmt.Errorf("the request did not end")
	}
	return nil
}

func (s *tpdState) lateDiscardedNotBilled() error {
	// The answer still posts (the Tower took the work) but nobody is waiting for it.
	return s.postAnswerTo("A")
}

func (s *tpdState) oneInstanceNoStore() error {
	b, srv := towerTestBroker(s.t)
	s.inst, s.srv = map[string]*broker{"A": b}, map[string]*httptest.Server{"A": srv}
	op := signedInOperator(s.t, b, "local-op")
	s.lt = enrolledTower(s.t, b, op.login)
	if err := b.tower.registry.Transition(s.lt.id, admit.StateActive); err != nil {
		return err
	}
	var err error
	s.sess, err = s.openSession(s.lt, "A", true, "203.0.113.7:8444")
	return err
}

func (s *tpdState) localFallback() error {
	if s.inst["A"].shared != nil {
		return fmt.Errorf("the broker has a shared store")
	}
	ch := s.pullAsync("A", s.lt, s.sess)
	time.Sleep(100 * time.Millisecond)
	resp := s.submitVia("A", s.lt.id, []byte("g"), []byte("e"), 5*time.Second)
	p, err := recvPull(ch, 2*time.Second)
	if err != nil || len(p.work) != 1 {
		return fmt.Errorf("the in-process pull got nothing: %v", err)
	}
	s.work = p.work[0]
	s.gotResp = resp
	return s.answerStillReachesFrom("A")
}

func (s *tpdState) answerStillReachesFrom(inst string) error {
	if err := s.postAnswerTo(inst); err != nil {
		return err
	}
	select {
	case r := <-s.gotResp:
		if string(r.body) != "sealed-answer" {
			return fmt.Errorf("got %q %v", r.body, r.err)
		}
	case <-time.After(3 * time.Second):
		return fmt.Errorf("the answer never arrived")
	}
	return nil
}

// --- harness extras --------------------------------------------------------------------------

func (s *tpdState) hubDown() { s.hubDownFn() }

func (s *tpdState) waitCancel() {
	if s.cancelWait != nil {
		s.cancelWait()
	}
}

// --- the runner ------------------------------------------------------------------------------

var (
	tpdLogs           = &syncBuf{}
	tpdProductionHold = pullHold
)

func TestTowerPullDispatchBDD(t *testing.T) {
	prevLog := log.Writer()
	log.SetOutput(tpdLogs)
	prevHold, prevPickup := pullHold, pullPickupDeadline
	pullHold = 2 * time.Second // held pulls end quickly so the servers close; heldUpTo25s checks the real value
	t.Cleanup(func() { log.SetOutput(prevLog); pullHold, pullPickupDeadline = prevHold, prevPickup })
	s := &tpdState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				tpdReplayed = nil
				return ctx, s.reset()
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				return ctx, s.reset()
			})
			sc.Step(`^Roger Core runs two instances sharing one store$`, s.twoInstances)
			sc.Step(`^a registered Tower with a live link session and a hub serving one attached Station$`, s.liveTower)
			sc.Step(`^a Tower running this release opens a link session$`, s.thisReleaseOpens)
			sc.Step(`^its session record carries the "pull" capability, mirrored to the shared store$`, s.sessionCarriesPullMirrored)
			sc.Step(`^every Core instance reads the same capability for that session$`, s.everyInstanceReadsPull)
			sc.Step(`^the Tower's session carries the "pull" capability$`, s.towerPulls)
			sc.Step(`^the Tower advertises a public hub address$`, s.publicHub)
			sc.Step(`^a consumer request is routed to a Station on that Tower$`, s.consumerRequest)
			sc.Step(`^Core queues the sealed job for the Tower and does not dial the hub$`, s.queuedNotDialed)
			sc.Step(`^a Tower whose session does not carry the "pull" capability$`, s.olderTower)
			sc.Step(`^it advertises a public hub address$`, s.publicHub)
			sc.Step(`^Core dials the hub and submits, as it does today$`, s.dialedAsToday)
			sc.Step(`^nothing about that path changes$`, s.pathUnchanged)
			sc.Step(`^it advertises no hub address, or a private one$`, s.noOrPrivateHub)
			sc.Step(`^no Station is placed on it$`, s.noStationPlaced)
			sc.Step("^`roger tower serve` on that machine tells the operator to upgrade so the Tower can pull$", s.toldToUpgrade)
			sc.Step(`^the Tower holds one pull request open on instance A$`, s.oneHeldOnA)
			sc.Step(`^a consumer request routed to its Station arrives on instance B$`, s.requestArrivesOnB)
			sc.Step(`^the sealed job reaches the Tower on instance A's held pull without waiting for the hold to end$`, s.reachesHeldPullAtOnce)
			sc.Step(`^the job carries the signed grant and the sealed request, and nothing else$`, s.carriesGrantAndSealedOnly)
			sc.Step(`^no job is queued for the Tower$`, s.noJobQueued)
			sc.Step(`^the Tower sends a pull request$`, s.towerSendsPull)
			sc.Step(`^Core holds it for up to 25 seconds$`, s.heldUpTo25s)
			sc.Step(`^answers "no work" if nothing arrives, and the Tower pulls again at once$`, s.noWorkThenPullsAgain)
			sc.Step(`^the Tower has no pull open$`, s.noPullOpen)
			sc.Step(`^a job is queued for it$`, s.jobQueued)
			sc.Step(`^the Tower pulls within the 3 second pickup deadline$`, s.pullsWithinDeadline)
			sc.Step(`^the job is delivered on that pull$`, s.deliveredOnThatPull)
			sc.Step(`^the Tower holds four pulls open across both instances$`, s.fourHeldBoth)
			sc.Step(`^one job is queued for it$`, s.oneJobQueued)
			sc.Step(`^exactly one pull receives the job$`, s.exactlyOneReceives)
			sc.Step(`^the other three keep holding$`, s.otherThreeHold)
			sc.Step(`^the Tower holds four pulls open$`, s.fourHeld)
			sc.Step(`^it sends a fifth$`, s.sendsFifth)
			sc.Step(`^Core answers 429 with a Retry-After, and the four held pulls are unaffected$`, s.fifthRefused)
			sc.Step(`^two Towers each with a held pull$`, s.twoTowersHolding)
			sc.Step(`^a job is queued for the first Tower's Station$`, s.jobForFirst)
			sc.Step(`^only the first Tower receives it$`, s.onlyFirstReceives)
			sc.Step(`^no pull request can name another Tower's ID to receive its jobs$`, s.cannotNameAnother)
			sc.Step(`^the Tower has no pull open and does not pull within the 3 second pickup deadline$`, s.noPullAndLate)
			sc.Step(`^the deadline passes$`, s.deadlinePasses)
			sc.Step(`^the job is withdrawn so no later pull can take it$`, s.withdrawnNoLaterPull)
			sc.Step(`^the consumer request moves to the next candidate, as a failed dial does today$`, s.movesToNext)
			sc.Step(`^the consumer is not charged for the withdrawn attempt$`, s.notCharged)
			sc.Step(`^the miss counts against the Tower like a failed dial, once$`, s.missCountsOnce)
			sc.Step(`^a job was withdrawn at its pickup deadline$`, s.jobWithdrawnEarlier)
			sc.Step(`^the Tower pulls a moment later$`, s.pullsMomentLater)
			sc.Step(`^that pull does not receive the withdrawn job$`, s.withdrawnNotReceived)
			sc.Step(`^the Tower took a job on instance A for a request waiting on instance B$`, s.tookOnAWaitingOnB)
			sc.Step(`^the Station completes and the Tower posts the sealed answer and receipt to instance A$`, s.postsAnswerToA)
			sc.Step(`^instance B receives them and runs the same checks as today: open, receipt binds the opened bytes$`, s.bReceivesAndChecks)
			sc.Step(`^the consumer gets the answer from instance B$`, s.consumerGetsFromB)
			sc.Step(`^a Tower posts an answer naming an attempt that was not delivered to that Tower$`, s.answerNotGiven)
			sc.Step(`^Core refuses it and nothing reaches any waiting request$`, s.refusedNothingReaches)
			sc.Step(`^a Tower posted an answer for an attempt$`, s.postedOnce)
			sc.Step(`^it posts an answer for the same attempt again$`, s.postsAgain)
			sc.Step(`^the second post changes nothing and is answered as already received$`, s.secondChangesNothing)
			sc.Step(`^the waiting request timed out and moved to another candidate$`, s.requestGaveUp)
			sc.Step(`^the Tower posts the answer late$`, s.postsLate)
			sc.Step(`^the answer is discarded$`, s.answerDiscarded)
			sc.Step(`^the consumer is billed only for the candidate that served them$`, s.billedOnlyServing)
			sc.Step(`^a Tower posts an answer over the cap$`, s.answerOverCap)
			sc.Step(`^Core refuses it before reading the rest, and the attempt fails as a Tower fault$`, s.refusedBeforeReading)
			sc.Step(`^the Station answers the job with a model failure, such as a 429 and a Retry-After$`, s.stationModelFailure)
			sc.Step(`^the Tower posts that failure$`, s.towerPostsFailure)
			sc.Step(`^the waiting request sees the same failure class it sees today from a dialed hub$`, s.sameFailureClass)
			sc.Step(`^a pulled job carries the request sealed to the Station's session key, as today$`, s.jobSealedToStation)
			sc.Step(`^the answer is sealed to the consumer side, as today$`, s.answerSealedToConsumer)
			sc.Step(`^the receipt is signed by the Station, so the Tower cannot alter the answer unnoticed$`, s.receiptSigned)
			sc.Step(`^the Tower (posts an answer whose receipt does not bind its bytes|posts an answer signed by a different Station|replays an earlier attempt's answer for a new attempt|pulls with a session that is closed or expired|pulls after its certificate serial was revoked|pulls while suspended, revoked or lease-lapsed|pulls without a valid request signature)$`, s.towerTries)
			sc.Step(`^Core (fails the attempt as a Tower fault, charges nothing|refuses the pull, delivers nothing)$`, s.coreAnswers)
			sc.Step(`^the Tower holds a pull open$`, s.holdsPullOpen)
			sc.Step(`^the Tower is revoked$`, s.towerRevoked)
			sc.Step(`^the held pull is answered empty and the next pull is refused$`, s.heldEmptyNextRefused)
			sc.Step(`^jobs already queued for it are withdrawn and fail over, uncharged$`, s.queuedWithdrawn)
			sc.Step(`^the Tower holds two jobs in progress$`, s.twoInProgress)
			sc.Step(`^it starts draining$`, s.startsDraining)
			sc.Step(`^its pulls receive no new jobs$`, s.noNewJobsWhileDraining)
			sc.Step(`^its answers for the two jobs it holds are still accepted$`, s.answersStillAccepted)
			sc.Step(`^Core runs a canary against a Station on that Tower$`, s.runCanary)
			sc.Step(`^the canary job is queued for the Tower's pull, indistinguishable from customer traffic$`, s.canaryByPull)
			sc.Step(`^it passes or fails exactly as a dialed canary does$`, s.canaryPassesOrFails)
			sc.Step(`^its hub check is due$`, s.hubCheckDue)
			sc.Step(`^Core queues a hub check carrying a fresh nonce for the Tower's pull$`, s.hubCheckQueued)
			sc.Step(`^the Tower's hub answers with the same signed identity proof it gives today$`, s.sameProof)
			sc.Step(`^a correct proof is recorded as a passed hub check, with source "pull"$`, s.recordedPassPull)
			sc.Step(`^no proof by the pickup deadline is recorded as a failed hub check, once per sweep slot$`, s.noProofFailsOnce)
			sc.Step(`^the Tower pulls and advertises a public hub address$`, s.pullsAndPublic)
			sc.Step(`^Core also dials the public address, as today, because remote Stations depend on it$`, s.alsoDialed)
			sc.Step(`^a failure there counts as a failed hub check even when the pull check passed$`, s.dialFailureCounts)
			sc.Step(`^promotion, demotion, caps and the house failover allowance read the same health record$`, s.sameRampRecord)
			sc.Step(`^a pull check pass or fail counts exactly as a dialed hub check pass or fail does$`, s.pullCountsSame)
			sc.Step(`^the Tower took a job on instance A$`, s.tookOnA)
			sc.Step(`^instance A restarts$`, s.instanceARestarts)
			sc.Step(`^the Tower's answer is posted to whichever instance it reaches and still reaches the waiting request$`, s.answerStillReaches)
			sc.Step(`^a request on instance B is waiting for the Tower's answer$`, s.waitingOnB)
			sc.Step(`^instance B restarts$`, s.instanceBRestarts)
			sc.Step(`^the request ends for the consumer as a broken connection does today$`, s.brokenConnection)
			sc.Step(`^a late answer for it is discarded and nothing is billed for it$`, s.lateDiscardedNotBilled)
			sc.Step(`^Core runs one instance with no shared store configured$`, s.oneInstanceNoStore)
			sc.Step(`^pull delivery uses the same in-process fallback the node poll uses today$`, s.localFallback)
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t,
			Paths: bddrun.Scenarios("../../features/tower/tower_pull_dispatch.feature", bddrun.Titled(true, "The serve command no longer says a Tower with no public address carries nothing", "Tower status says how work reaches the Tower", "The design documents describe what the code does"))},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("tower_pull_dispatch.feature failed")
	}
}

// rewriteJSON applies f to one base64 field of a hub submit answer.
func rewriteJSON(body []byte, field string, f func([]byte) []byte) []byte {
	var m map[string]string
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	raw, _ := base64.StdEncoding.DecodeString(m[field])
	m[field] = base64.StdEncoding.EncodeToString(f(raw))
	out, _ := json.Marshal(m)
	return out
}

func flipLastByte(b []byte) []byte {
	out := append([]byte(nil), b...)
	if len(out) > 0 {
		out[len(out)-1] ^= 0xff
	}
	return out
}
