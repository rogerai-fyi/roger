package main

// rc_inbox_bdd_test.go makes features/multinode/rc_inbox.feature AND rc_cross_instance.feature
// (X1-X4, documentation-only until now) EXECUTABLE. Real broker instances over real HTTP, the
// real client (host poll/events with the host token, viewer join/send/stream with attach
// tokens), one shared session store, real miniredis. No mocks.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"rogerai.fm/roger/v6/internal/bddtest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type rciInst struct {
	b   *broker
	srv *httptest.Server
}

type rciViewer struct {
	inst   string
	att    string
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	frames []protocol.RCFrame
}

func (v *rciViewer) got() []protocol.RCFrame {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]protocol.RCFrame(nil), v.frames...)
}

type rciState struct {
	t       *testing.T
	mr      *miniredis.Miniredis
	mem     *store.Mem
	inst    map[string]*rciInst
	sid     string
	hostTok string
	viewers map[string]*rciViewer

	hostMu   sync.Mutex
	hostGot  []protocol.RCInbound
	hostStop context.CancelFunc
	pollCode int
	enabled  bool
	attachOK bool
}

func (s *rciState) reset(t *testing.T) {
	s.t = t
	s.inst = map[string]*rciInst{}
	s.viewers = map[string]*rciViewer{}
	s.hostGot = nil
	s.hostStop = nil
	s.pollCode = 0
	s.mem = store.NewMem()
	if err := s.mem.BindOwner(store.Owner{GitHubID: 11, Login: "rc", Pubkey: client.UserPubHex()}); err != nil {
		t.Fatal(err)
	}
	s.mr = miniredis.RunT(t)
}

func (s *rciState) cleanup() {
	if s.hostStop != nil {
		s.hostStop()
	}
	for _, v := range s.viewers {
		v.cancel()
	}
	for _, i := range s.inst {
		i.srv.Close()
		if vs, ok := i.b.shared.(*valkeyStore); ok {
			_ = vs.Close()
		}
	}
}

func (s *rciState) addInstance(name string) *rciInst {
	vs, err := newValkeyStore("redis://" + s.mr.Addr() + "?pool_size=10")
	if err != nil {
		s.t.Fatal(err)
	}
	b := &broker{db: s.mem, pubOfUser: map[string]string{}, shared: vs, multiInstance: true,
		instanceID: newInstanceID(), dispatchMode: dispatchViaQueueOnly}
	m := http.NewServeMux()
	m.HandleFunc("/rc/enable", b.rcEnable)
	m.HandleFunc("/rc/sessions", b.rcSessions)
	m.HandleFunc("/rc/attach", b.rcAttach)
	m.HandleFunc("/rc/revoke-all", b.rcRevokeAll)
	m.HandleFunc("/rc/", b.rcSubtree)
	in := &rciInst{b: b, srv: httptest.NewServer(m)}
	s.inst[name] = in
	return in
}

func (s *rciState) twoInstances() error {
	s.addInstance("A")
	s.addInstance("B")
	return nil
}

func (s *rciState) enableOn(inst string) error {
	_, res, err := client.EnableRC(s.inst[inst].srv.URL, "rc-test")
	if err != nil {
		return err
	}
	s.sid, s.hostTok = res.SessionID, res.HostToken
	s.enabled = true
	return nil
}

// pollOnce is one host long-poll on inst: the inbound, or nil on 204. The code is kept.
func (s *rciState) pollOnce(ctx context.Context, inst string) *protocol.RCInbound {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.inst[inst].srv.URL+"/rc/"+s.sid+"/poll", nil)
	req.Header.Set("Authorization", "Bearer "+s.hostTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		time.Sleep(20 * time.Millisecond) // a closed server: don't spin
		return nil
	}
	defer resp.Body.Close()
	s.hostMu.Lock()
	s.pollCode = resp.StatusCode
	s.hostMu.Unlock()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var in protocol.RCInbound
	if json.NewDecoder(resp.Body).Decode(&in) != nil {
		return nil
	}
	return &in
}

// hostPolls runs the host's poll loop on inst, recording every turn it receives.
func (s *rciState) hostPolls(inst string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.hostStop = cancel
	go func() {
		for ctx.Err() == nil {
			if in := s.pollOnce(ctx, inst); in != nil && in.Kind == protocol.RCInTurn {
				s.hostMu.Lock()
				s.hostGot = append(s.hostGot, *in)
				s.hostMu.Unlock()
			}
			s.hostMu.Lock()
			refused := s.pollCode == http.StatusUnauthorized
			s.hostMu.Unlock()
			if refused {
				return
			}
		}
	}()
	s.waitHostAdvertised(inst)
}

func (s *rciState) waitHostAdvertised(inst string) {
	if s.inst[inst].b.rcInbox() == nil {
		time.Sleep(100 * time.Millisecond) // no route to watch: give the poll a moment
		return
	}
	field := "h:" + s.inst[inst].b.instanceID
	deadline := time.Now().Add(3 * time.Second)
	for s.mr.HGet(rcKey(s.sid, "route"), field) == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *rciState) sessionHostOnA() error {
	if err := s.enableOn("A"); err != nil {
		return err
	}
	s.hostPolls("A")
	return nil
}

func (s *rciState) postFrames(inst string, frames []protocol.RCFrame) error {
	body, _ := json.Marshal(frames)
	req, _ := http.NewRequest(http.MethodPost, s.inst[inst].srv.URL+"/rc/"+s.sid+"/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.hostTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("events on %s = %d", inst, resp.StatusCode)
	}
	return nil
}

func assistant(text string) protocol.RCFrame {
	return protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: text}
}

// viewer attaches (owner join) and streams on inst from lastSeq.
func (s *rciState) viewer(name, inst string, lastSeq uint64) (*rciViewer, error) {
	att, err := client.JoinRC(s.inst[inst].srv.URL, s.sid)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	v := &rciViewer{inst: inst, att: att, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(v.done)
		_ = client.StreamRC(ctx, s.inst[inst].srv.URL, s.sid, att, lastSeq, func(f protocol.RCFrame) {
			v.mu.Lock()
			v.frames = append(v.frames, f)
			v.mu.Unlock()
		})
	}()
	s.viewers[name] = v
	// Streaming means this instance advertises viewers for the session.
	field := "v:" + s.inst[inst].b.instanceID
	deadline := time.Now().Add(3 * time.Second)
	for s.mr.HGet(rcKey(s.sid, "route"), field) == "" {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the viewer on %s never started streaming", inst)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // past the seq read in rcStreamInbox
	return v, nil
}

func (s *rciState) send(inst, att, text string) error {
	return client.SendRC(s.inst[inst].srv.URL, s.sid, att, protocol.RCInbound{Kind: protocol.RCInTurn, Text: text})
}

func (s *rciState) turns() []string {
	s.hostMu.Lock()
	defer s.hostMu.Unlock()
	var out []string
	for _, in := range s.hostGot {
		out = append(out, in.Text)
	}
	return out
}

func (s *rciState) waitTurns(want []string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := s.turns()
		if len(got) >= len(want) {
			time.Sleep(200 * time.Millisecond) // a duplicate would land here
			got = s.turns()
			if strings.Join(got, "|") != strings.Join(want, "|") {
				return fmt.Errorf("the host received %q, want %q exactly once each, in order", got, want)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the host received %q, want %q", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assistantTexts is the viewer's assistant/user frames (not backfill requests), with seqs.
func assistantTexts(fs []protocol.RCFrame) ([]string, []uint64) {
	var t []string
	var q []uint64
	for _, f := range fs {
		if f.Kind == protocol.RCKindAssistant || f.Kind == protocol.RCKindUser {
			t = append(t, f.Text)
			q = append(q, f.Seq)
		}
	}
	return t, q
}

func (s *rciState) waitViewer(v *rciViewer, n int) ([]string, []uint64, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		t, q := assistantTexts(v.got())
		if len(t) >= n {
			time.Sleep(200 * time.Millisecond)
			t, q = assistantTexts(v.got())
			return t, q, nil
		}
		if time.Now().After(deadline) {
			return t, q, fmt.Errorf("the viewer on %s received %d frames, want %d", v.inst, len(t), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- R2 -------------------------------------------------------------------------------------

func (s *rciState) viewerOnB() error {
	_, err := s.viewer("vb", "B", 0)
	return err
}

func (s *rciState) twoTurnsFast() error {
	att := s.viewers["vb"].att
	start := time.Now()
	if err := s.send("B", att, "one"); err != nil {
		return err
	}
	if err := s.send("B", att, "two"); err != nil {
		return err
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		return fmt.Errorf("the two sends took %s", d)
	}
	return nil
}

func (s *rciState) hostGetsOneTwo() error { return s.waitTurns([]string{"one", "two"}) }
func (s *rciState) eachOnce() error       { return nil } // asserted by waitTurns (no duplicates)

func (s *rciState) hostBetweenPolls() error {
	s.hostStop()
	s.hostStop = nil
	time.Sleep(100 * time.Millisecond)
	return nil
}

func (s *rciState) viewerSendsLate() error {
	att, err := client.JoinRC(s.inst["B"].srv.URL, s.sid)
	if err != nil {
		return err
	}
	return s.send("B", att, "late")
}

func (s *rciState) hostPollsB() error {
	s.hostPolls("B")
	return nil
}

func (s *rciState) hostGetsLate() error { return s.waitTurns([]string{"late"}) }

func (s *rciState) twentyTurns() error {
	att, err := client.JoinRC(s.inst["B"].srv.URL, s.sid)
	if err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		if err := s.send("B", att, fmt.Sprintf("t%02d", i)); err != nil {
			return err
		}
	}
	return nil
}

func (s *rciState) all20InOrder() error {
	var want []string
	for i := 0; i < 20; i++ {
		want = append(want, fmt.Sprintf("t%02d", i))
	}
	return s.waitTurns(want)
}

func (s *rciState) hostStopsPolling() error { return s.hostBetweenPolls() }

func (s *rciState) viewerSendsATurn() error { return s.viewerSendsLate() }

func (s *rciState) turnExpires() error {
	if !s.mr.Exists(rcKey(s.sid, "inq")) {
		return fmt.Errorf("the turn was never queued - the expiry check would be vacuous")
	}
	s.mr.FastForward(rcInboundBufTTL + time.Second)
	if s.mr.Exists(rcKey(s.sid, "inq")) {
		return fmt.Errorf("the unpolled turn is still in the store after the inbound expiry")
	}
	return nil
}

// ---- R3 -------------------------------------------------------------------------------------

func (s *rciState) viewersOnAandB() error {
	if _, err := s.viewer("va", "A", 0); err != nil {
		return err
	}
	_, err := s.viewer("vb", "B", 0)
	return err
}

func (s *rciState) fiftyConcurrent() error {
	var a, b []protocol.RCFrame
	for i := 0; i < 25; i++ {
		a = append(a, assistant(fmt.Sprintf("a%02d", i)))
		b = append(b, assistant(fmt.Sprintf("b%02d", i)))
	}
	errs := make(chan error, 2)
	go func() { errs <- s.postFrames("A", a) }()
	go func() { errs <- s.postFrames("B", b) }()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			return err
		}
	}
	return nil
}

func (s *rciState) each50InOrder() error {
	for _, name := range []string{"va", "vb"} {
		texts, seqs, err := s.waitViewer(s.viewers[name], 50)
		if err != nil {
			return err
		}
		if len(texts) != 50 {
			return fmt.Errorf("viewer %s received %d frames, want exactly 50", name, len(texts))
		}
		for i := 1; i < len(seqs); i++ {
			if seqs[i] <= seqs[i-1] {
				return fmt.Errorf("viewer %s received seq %d after %d", name, seqs[i], seqs[i-1])
			}
		}
		seen := map[string]bool{}
		for _, t := range texts {
			if seen[t] {
				return fmt.Errorf("viewer %s received %q twice", name, t)
			}
			seen[t] = true
		}
	}
	return nil
}

func (s *rciState) viewerBSendsHi() error { return s.send("B", s.viewers["vb"].att, "hi") }

func (s *rciState) bothGetHi() error {
	for _, name := range []string{"va", "vb"} {
		texts, _, err := s.waitViewer(s.viewers[name], 1)
		if err != nil {
			return err
		}
		if len(texts) == 0 || texts[0] != "hi" {
			return fmt.Errorf("viewer %s did not receive the user frame hi (%v)", name, texts)
		}
	}
	return nil
}

func (s *rciState) viewersOnlyOnA() error {
	_, err := s.viewer("va", "A", 0)
	return err
}

func (s *rciState) hostPosts(n int) error {
	var fs []protocol.RCFrame
	for i := 0; i < n; i++ {
		fs = append(fs, assistant(fmt.Sprintf("f%03d", i)))
	}
	return s.postFrames("A", fs)
}

func (s *rciState) bInboxNone() error {
	if _, _, err := s.waitViewer(s.viewers["va"], 10); err != nil {
		return err
	}
	if n := s.inst["B"].b.stats.rcFrames.Load(); n != 0 {
		return fmt.Errorf("instance B received %d frames for a session it has no viewers of", n)
	}
	return nil
}

func (s *rciState) hostPosted5() error { return s.hostPosts(5) }

func (s *rciState) attachOnBNoID() error {
	_, err := s.viewer("vb", "B", 0)
	return err
}

func (s *rciState) onlyAfterAttach() error {
	if err := s.postFrames("A", []protocol.RCFrame{assistant("after")}); err != nil {
		return err
	}
	texts, _, err := s.waitViewer(s.viewers["vb"], 1)
	if err != nil {
		return err
	}
	if len(texts) != 1 || texts[0] != "after" {
		return fmt.Errorf("the late viewer received %v, want only the frame posted after it attached", texts)
	}
	return nil
}

// ---- R4 -------------------------------------------------------------------------------------

func (s *rciState) viewerAUpTo10() error {
	v, err := s.viewer("va", "A", 0)
	if err != nil {
		return err
	}
	if err := s.hostPosts(10); err != nil {
		return err
	}
	if _, _, err := s.waitViewer(v, 10); err != nil {
		return err
	}
	v.cancel()
	<-v.done
	return nil
}

func (s *rciState) hostPostedUpTo15() error {
	var fs []protocol.RCFrame
	for i := 11; i <= 15; i++ {
		fs = append(fs, assistant(fmt.Sprintf("s%d", i)))
	}
	return s.postFrames("A", fs)
}

func (s *rciState) reconnectOnBFrom10() error {
	_, err := s.viewer("vb", "B", 10)
	return err
}

func (s *rciState) gets11to15ThenLive() error {
	if err := s.postFrames("A", []protocol.RCFrame{assistant("live")}); err != nil {
		return err
	}
	texts, seqs, err := s.waitViewer(s.viewers["vb"], 6)
	if err != nil {
		return err
	}
	want := "s11,s12,s13,s14,s15,live"
	if strings.Join(texts, ",") != want || seqs[0] != 11 {
		return fmt.Errorf("the reconnected viewer received %v (seqs %v), want %s from seq 11", texts, seqs, want)
	}
	return nil
}

func (s *rciState) hostPosted300() error { return s.hostPosts(300) }

func (s *rciState) reconnectFrom1() error {
	_, err := s.viewer("vr", "B", 1)
	return err
}

func (s *rciState) gets200InOrder() error {
	_, seqs, err := s.waitViewer(s.viewers["vr"], 200)
	if err != nil {
		return err
	}
	if len(seqs) != 200 || seqs[0] != 101 || seqs[199] != 300 {
		return fmt.Errorf("replay from seq 1 gave %d frames (%v..%v), want seq 101..300", len(seqs), firstSeq(seqs), lastSeq(seqs))
	}
	return nil
}

func firstSeq(q []uint64) any {
	if len(q) == 0 {
		return nil
	}
	return q[0]
}

func lastSeq(q []uint64) any {
	if len(q) == 0 {
		return nil
	}
	return q[len(q)-1]
}

// ---- lifecycle ------------------------------------------------------------------------------

func (s *rciState) ownerDisablesOnB() error { return client.RevokeRC(s.inst["B"].srv.URL, s.sid) }

func (s *rciState) streamsEndWithEnded() error {
	for _, name := range []string{"va", "vb"} {
		v := s.viewers[name]
		select {
		case <-v.done:
		case <-time.After(5 * time.Second):
			return fmt.Errorf("viewer %s's stream did not end", name)
		}
		fs := v.got()
		if len(fs) == 0 || fs[len(fs)-1].Kind != protocol.RCKindEnded {
			return fmt.Errorf("viewer %s's stream did not end with an ended frame", name)
		}
	}
	return nil
}

func (s *rciState) nextPollRefused() error {
	s.pollOnce(context.Background(), "A")
	s.hostMu.Lock()
	code := s.pollCode
	s.hostMu.Unlock()
	if code != http.StatusUnauthorized {
		return fmt.Errorf("the host's poll after revocation = %d, want 401", code)
	}
	return nil
}

func (s *rciState) viewerDisconnects() error {
	v := s.viewers["vb"]
	v.cancel()
	<-v.done
	return nil
}

func (s *rciState) bLeavesRoute() error {
	field := "v:" + s.inst["B"].b.instanceID
	deadline := time.Now().Add(rcRouteRefresh)
	for s.mr.HGet(rcKey(s.sid, "route"), field) != "" {
		if time.Now().After(deadline) {
			return fmt.Errorf("instance B still advertises viewers after its only viewer left")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// ---- R5 -------------------------------------------------------------------------------------

func (s *rciState) fiftySessions() error {
	for i := 0; i < 50; i++ {
		sid := rcRandID()
		tok := rcRandToken("rc_host_")
		if err := s.mem.CreateRCSession(store.RCSession{ID: sid, OwnerWallet: "u_gh_11", Name: "load",
			HostTokenHash: rcHash(tok), LastHostSeen: time.Now().Unix()}); err != nil {
			return err
		}
		s.sid, s.hostTok = sid, tok
		ctx, cancel := context.WithCancel(context.Background())
		prev := s.hostStop
		s.hostStop = func() {
			cancel()
			if prev != nil {
				prev()
			}
		}
		inst := []string{"A", "B"}[i%2]
		go func(sid, tok string) {
			for ctx.Err() == nil {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.inst[inst].srv.URL+"/rc/"+sid+"/poll", nil)
				req.Header.Set("Authorization", "Bearer "+tok)
				if resp, err := http.DefaultClient.Do(req); err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		}(sid, tok)
		for j := 0; j < 3; j++ {
			if _, err := s.viewer(fmt.Sprintf("s%d-v%d", i, j), []string{"A", "B"}[(i+j)%2], 0); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *rciState) connectionBudget() error {
	pool := s.inst["A"].b.shared.(*valkeyStore).poolSize
	limit := 2 * (pool + 1)
	if got := s.mr.CurrentConnectionCount(); got > limit {
		return fmt.Errorf("the brokers hold %d store connections for 50 sessions with 150 viewers, want at most %d", got, limit)
	}
	return nil
}

// ---- R1 / R6 --------------------------------------------------------------------------------

func (s *rciState) crossInstancePasses() error {
	suite := godog.TestSuite{
		ScenarioInitializer: rcCrossInitScenarios(s.t),
		Options: &godog.Options{Format: "progress", Paths: []string{"../../features/multinode/rc_cross_instance.feature"},
			Strict: true, Output: io.Discard},
	}
	if bddtest.Run(s.t, &suite) != 0 {
		return fmt.Errorf("rc_cross_instance.feature failed on the inbox plane")
	}
	return nil
}

func (s *rciState) singleInstance() error {
	if s.hostStop != nil {
		s.hostStop() // the multi-instance host from the Background leaves first
		s.hostStop = nil
	}
	for _, i := range s.inst {
		i.srv.Close()
		_ = i.b.shared.(*valkeyStore).Close()
	}
	s.inst = map[string]*rciInst{}
	b := &broker{db: s.mem, pubOfUser: map[string]string{}}
	m := http.NewServeMux()
	m.HandleFunc("/rc/enable", b.rcEnable)
	m.HandleFunc("/rc/", b.rcSubtree)
	s.inst["A"] = &rciInst{b: b, srv: httptest.NewServer(m)}
	return s.enableOn("A")
}

func (s *rciState) singleTurnAndFrame() error {
	s.hostPolls("A")
	time.Sleep(100 * time.Millisecond)
	v, err := s.viewer1("A")
	if err != nil {
		return err
	}
	if err := s.send("A", v.att, "solo"); err != nil {
		return err
	}
	return s.postFrames("A", []protocol.RCFrame{assistant("reply")})
}

// viewer1 is viewer() without the route wait (single-instance has no route).
func (s *rciState) viewer1(inst string) (*rciViewer, error) {
	att, err := client.JoinRC(s.inst[inst].srv.URL, s.sid)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	v := &rciViewer{inst: inst, att: att, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(v.done)
		_ = client.StreamRC(ctx, s.inst[inst].srv.URL, s.sid, att, 0, func(f protocol.RCFrame) {
			v.mu.Lock()
			v.frames = append(v.frames, f)
			v.mu.Unlock()
		})
	}()
	s.viewers["solo"] = v
	time.Sleep(150 * time.Millisecond)
	return v, nil
}

func (s *rciState) singleReceived() error {
	if err := s.waitTurns([]string{"solo"}); err != nil {
		return err
	}
	texts, _, err := s.waitViewer(s.viewers["solo"], 2)
	if err != nil {
		return err
	}
	if !strings.Contains(strings.Join(texts, ","), "reply") {
		return fmt.Errorf("the single-instance viewer received %v, want the host's reply", texts)
	}
	return nil
}

func rcInitScenarios(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		s := &rciState{}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			s.reset(t)
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			s.cleanup()
			return ctx, nil
		})
		sc.Step(`^a multi-instance broker of two instances sharing one store$`, s.twoInstances)
		sc.Step(`^a remote-control session whose host polls instance A$`, s.sessionHostOnA)
		sc.Step(`^a viewer streaming on instance B$`, s.viewerOnB)
		sc.Step(`^the viewer sends turn "one" and then turn "two" within 10 ms$`, s.twoTurnsFast)
		sc.Step(`^the host receives "one" and then "two"$`, s.hostGetsOneTwo)
		sc.Step(`^each exactly once$`, s.eachOnce)
		sc.Step(`^the host is between polls$`, s.hostBetweenPolls)
		sc.Step(`^a viewer on instance B sends turn "late"$`, s.viewerSendsLate)
		sc.Step(`^the host polls instance B$`, s.hostPollsB)
		sc.Step(`^the host receives "late" exactly once$`, s.hostGetsLate)
		sc.Step(`^a viewer on instance B sends 20 turns in a row$`, s.twentyTurns)
		sc.Step(`^the host receives all 20 in send order, each exactly once$`, s.all20InOrder)
		sc.Step(`^the host stops polling$`, s.hostStopsPolling)
		sc.Step(`^a viewer sends a turn$`, s.viewerSendsATurn)
		sc.Step(`^the turn is gone from the store after the inbound expiry$`, s.turnExpires)
		sc.Step(`^viewers streaming on instance A and instance B$`, s.viewersOnAandB)
		sc.Step(`^the host posts 50 frames in two concurrent batches through different instances$`, s.fiftyConcurrent)
		sc.Step(`^each viewer receives all 50 frames in increasing seq order, each exactly once$`, s.each50InOrder)
		sc.Step(`^the viewer on B sends turn "hi"$`, s.viewerBSendsHi)
		sc.Step(`^both viewers receive the user frame "hi"$`, s.bothGetHi)
		sc.Step(`^viewers streaming only on instance A$`, s.viewersOnlyOnA)
		sc.Step(`^the host posts (\d+) frames$`, s.hostPosts)
		sc.Step(`^instance B's inbox carries none of them$`, s.bInboxNone)
		sc.Step(`^the host has posted 5 frames$`, s.hostPosted5)
		sc.Step(`^a viewer attaches on instance B without Last-Event-ID$`, s.attachOnBNoID)
		sc.Step(`^it receives only frames posted after it attached, plus the host's backfill$`, s.onlyAfterAttach)
		sc.Step(`^a viewer on instance A has received frames up to seq 10$`, s.viewerAUpTo10)
		sc.Step(`^the host has posted frames up to seq 15$`, s.hostPostedUpTo15)
		sc.Step(`^the viewer reconnects on instance B with Last-Event-ID 10$`, s.reconnectOnBFrom10)
		sc.Step(`^it receives seq 11 to 15, then live frames$`, s.gets11to15ThenLive)
		sc.Step(`^the host has posted 300 frames$`, s.hostPosted300)
		sc.Step(`^a viewer reconnects with Last-Event-ID 1$`, s.reconnectFrom1)
		sc.Step(`^it receives the last 200 frames in order$`, s.gets200InOrder)
		sc.Step(`^the owner disables the session on instance B$`, s.ownerDisablesOnB)
		sc.Step(`^both viewer streams end with an ended frame$`, s.streamsEndWithEnded)
		sc.Step(`^the host's next poll is refused$`, s.nextPollRefused)
		sc.Step(`^the viewer disconnects$`, s.viewerDisconnects)
		sc.Step(`^instance B leaves the session's viewer route within one refresh$`, s.bLeavesRoute)
		sc.Step(`^50 sessions, each with a polling host and 3 viewers spread over both instances$`, s.fiftySessions)
		sc.Step(`^the store reports at most 2 x \(pool size \+ 1\) client connections from the brokers$`, s.connectionBudget)
		sc.Step(`^every scenario of rc_cross_instance.feature passes$`, s.crossInstancePasses)
		sc.Step(`^the broker runs in single-instance mode$`, s.singleInstance)
		sc.Step(`^a viewer sends a turn and the host posts a frame$`, s.singleTurnAndFrame)
		sc.Step(`^the host receives the turn and the viewer receives the frame$`, s.singleReceived)
	}
}

// ---- rc_cross_instance.feature (X1-X4) on the inbox plane ------------------------------------

func rcCrossInitScenarios(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		s := &rciState{}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			s.reset(t)
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			s.cleanup()
			return ctx, nil
		})
		sc.Step(`^a two-instance broker pair "A" and "B" sharing a store and bus$`, s.twoInstances)
		sc.Step(`^a host enables remote control on instance A$`, func() error { return s.enableOn("A") })
		sc.Step(`^the owner attaches on instance B with the link code$`, func() error {
			// Re-enable to capture the one-time code (EnableRC's result carries it).
			_, res, err := client.EnableRC(s.inst["A"].srv.URL, "x1")
			if err != nil {
				return err
			}
			s.sid = res.SessionID
			_, aerr := client.AttachRC(s.inst["B"].srv.URL, res.Code)
			s.attachOK = aerr == nil
			return nil
		})
		sc.Step(`^the attach succeeds on B$`, func() error {
			if !s.attachOK {
				return fmt.Errorf("attaching on B with the link code failed")
			}
			return nil
		})
		sc.Step(`^a host polling instance A with remote control enabled$`, s.sessionHostOnA)
		sc.Step(`^a viewer on instance B sends a turn$`, func() error {
			att, err := client.JoinRC(s.inst["B"].srv.URL, s.sid)
			if err != nil {
				return err
			}
			return s.send("B", att, "x2")
		})
		sc.Step(`^the host on A receives the turn exactly once$`, func() error { return s.waitTurns([]string{"x2"}) })
		sc.Step(`^a host posting events on instance A$`, func() error { return s.enableOn("A") })
		sc.Step(`^viewers streaming on instances A and B$`, s.viewersOnAandB)
		sc.Step(`^the host posts an assistant frame$`, func() error {
			return s.postFrames("A", []protocol.RCFrame{assistant("x3")})
		})
		sc.Step(`^both viewers receive it$`, func() error {
			for _, name := range []string{"va", "vb"} {
				texts, _, err := s.waitViewer(s.viewers[name], 1)
				if err != nil {
					return err
				}
				if len(texts) != 1 || texts[0] != "x3" {
					return fmt.Errorf("viewer %s received %v", name, texts)
				}
			}
			return nil
		})
		sc.Step(`^a session enabled on A$`, func() error { return s.enableOn("A") })
		sc.Step(`^the owner revokes it on instance B$`, func() error { return client.RevokeRC(s.inst["B"].srv.URL, s.sid) })
		sc.Step(`^the host's next poll on A sees the session ended$`, s.nextPollRefused)
	}
}

func TestRCInboxBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: rcInitScenarios(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/rc_inbox.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if bddtest.Run(t, &suite) != 0 {
		t.Fatal("multinode/rc_inbox scenarios failed (see godog output above)")
	}
}

func TestRCCrossInstanceBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: rcCrossInitScenarios(t),
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/multinode/rc_cross_instance.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if bddtest.Run(t, &suite) != 0 {
		t.Fatal("multinode/rc_cross_instance scenarios failed (see godog output above)")
	}
}
