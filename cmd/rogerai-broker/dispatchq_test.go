package main

// dispatchq_test.go: unit and integration tests for the dispatch queue (dispatchq.go) beyond
// the approved scenarios in features/multinode/dispatch_inbox.feature: the streamed relay,
// the honest failure answers, the rollout overlap in both directions, a node hanging up
// mid-handoff, and the topology / mode switches. Real miniredis, no mocks.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// newQBroker is newMIBroker in a given dispatch mode.
func newQBroker(t *testing.T, priv ed25519.PrivateKey, db store.Store, mr *miniredis.Miniredis, mode dispatchMode) *broker {
	t.Helper()
	b := newMIBroker(t, priv, db, mr)
	b.dispatchMode = mode
	return b
}

// waitAdvertised blocks until n polls of node sit idle on b.
func waitAdvertised(t *testing.T, b *broker, node string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for localIdle(b, node) < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d polls advertised idle", localIdle(b, node), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// postResult posts a node-signed result for job to inst.
func qPostResult(t *testing.T, inst *broker, node, token, model string, job protocol.Job, priv ed25519.PrivateKey) int {
	t.Helper()
	res := miSignedResult(job.ID, node, model, "served "+job.ID, priv, 200)
	rb, _ := json.Marshal(res)
	rr := httptest.NewRequest(http.MethodPost, "/agent/result?node="+node, bytes.NewReader(rb))
	rr.Header.Set("Authorization", miNodeBearer(token))
	rw := httptest.NewRecorder()
	inst.agentResult(rw, rr)
	return rw.Code
}

func TestQueueStreamRendezvous(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newQBroker(t, priv, db, mr, dispatchViaQueueOnly)
	bInst := newQBroker(t, priv, db, mr, dispatchViaQueueOnly)
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	offers := []protocol.ModelOffer{{Model: "free-m"}}
	miRegisterNode(a, "n1", hex.EncodeToString(nodePub), "tok", offers)
	miRegisterNode(bInst, "n1", hex.EncodeToString(nodePub), "tok", offers)

	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one \"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"two \"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"three\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		job, ok := pollOnce(t, bInst, "n1", "tok")
		if !ok {
			t.Errorf("instance B poll got no stream job")
			return
		}
		pr, pw := io.Pipe()
		go func() {
			for _, c := range chunks {
				_, _ = pw.Write([]byte(c))
			}
			_ = pw.Close()
		}()
		sr := httptest.NewRequest(http.MethodPost, "/agent/stream?node=n1&job="+job.ID, pr)
		sr.Header.Set("Authorization", miNodeBearer("tok"))
		bInst.agentStream(httptest.NewRecorder(), sr)
		qPostResult(t, bInst, "n1", "tok", "free-m", job, nodePriv)
	}()
	waitAdvertised(t, bInst, "n1", 1)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	r := miSignedRelayReq(t, userPriv, []byte(`{"model":"free-m","stream":true,"max_tokens":8}`), nil)
	w := httptest.NewRecorder()
	a.relay(w, r)
	wg.Wait()

	got := w.Body.String()
	i1, i2, i3 := strings.Index(got, "one "), strings.Index(got, "two "), strings.Index(got, "three")
	if i1 < 0 || i2 < 0 || i3 < 0 || !(i1 < i2 && i2 < i3) {
		t.Fatalf("queue-mode stream on A missing or out of order: %q", got)
	}
	if n := bInst.stats.dqHandoff.Load(); n != 1 {
		t.Errorf("handoffs on B = %d, want 1", n)
	}
}

// A live node with no poller anywhere: the job waits its queue wait, then an honest,
// retryable "station busy" - not the old instant "no poller free".
func TestQueueLiveNodeNoPollerIsBusyAfterWait(t *testing.T) {
	defer func(d time.Duration) { queueWaitChat = d }(queueWaitChat)
	queueWaitChat = 300 * time.Millisecond
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	a := newQBroker(t, priv, store.NewMem(), mr, dispatchViaQueueOnly)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	miRegisterNode(a, "n1", hex.EncodeToString(nodePub), "tok", []protocol.ModelOffer{{Model: "free-m"}})

	_, userPriv, _ := ed25519.GenerateKey(nil)
	r := miSignedRelayReq(t, userPriv, []byte(`{"model":"free-m","max_tokens":8}`), nil)
	w := httptest.NewRecorder()
	start := time.Now()
	a.relay(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "station busy") {
		t.Fatalf("relay = %d %q, want 503 station busy", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want 1", w.Header().Get("Retry-After"))
	}
	if el := time.Since(start); el < 250*time.Millisecond || el > 2*time.Second {
		t.Errorf("answered after %s, want after the 300 ms queue wait", el)
	}
	// The withdrawn job is gone from the node's list.
	if mr.Exists(dqListPrefix + "n1") {
		if l, _ := mr.List(dqListPrefix + "n1"); len(l) != 0 {
			t.Errorf("the withdrawn job is still queued: %v", l)
		}
	}
}

func TestQueueOffAirFailsFast(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	a := newQBroker(t, priv, store.NewMem(), mr, dispatchViaQueueOnly)
	tk, err := a.dispatchRemote(context.Background(), "ghost", protocol.Job{ID: "j1"}, false)
	if err != errOffAir || tk != nil {
		t.Fatalf("dispatch to a never-seen node = %v, %v; want errOffAir", tk, err)
	}
	w := httptest.NewRecorder()
	a.writeDispatchFailure(w, a.dispatchErrOutcome(err))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "station off air") {
		t.Errorf("off-air answer = %d %q", w.Code, w.Body.String())
	}
	if a.stats.dqOffAir.Load() != 1 {
		t.Errorf("dqOffAir = %d, want 1", a.stats.dqOffAir.Load())
	}
}

// A dead Valkey in queue mode fails a PAID relay cleanly and refunds the hold.
func TestQueueDeadBusFailsCleanAndRefunds(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newQBroker(t, priv, db, mr, dispatchViaQueueOnly)
	_, userPriv, _ := ed25519.GenerateKey(nil)
	_ = db.BindOwner(store.Owner{GitHubID: 9, Login: "q-octo", Pubkey: hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))})
	startBal, _ := db.BalanceOf("u_gh_9", 50)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	miRegisterNode(a, "n1", hex.EncodeToString(nodePub), "tok", []protocol.ModelOffer{{Model: "paid-m", PriceOut: 0.5}})
	mr.Close()

	w := httptest.NewRecorder()
	a.relay(w, miSignedRelayReq(t, userPriv, []byte(`{"model":"paid-m","max_tokens":8}`), nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("dead-bus relay = %d, want 503", w.Code)
	}
	if end, _ := db.BalanceOf("u_gh_9", 0); end != startBal {
		t.Errorf("balance %v -> %v across a failed relay: the hold was not refunded", startBal, end)
	}
}

// Rollout, deploy A: an instance still dispatching by publish reaches pollers on an instance
// already running the queue code (they hear both), and a lost claim no longer costs them
// their place once they run a queue mode.
func TestQueueRolloutOverlap(t *testing.T) {
	for _, tc := range []struct {
		name               string
		dispatch, pollMode dispatchMode
	}{
		{"old dispatcher, queue pollers", dispatchViaBus, dispatchViaQueue},
		{"queue dispatcher, deploy-A pollers", dispatchViaQueue, dispatchViaBus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			_, priv, _ := ed25519.GenerateKey(nil)
			db := store.NewMem()
			disp := newQBroker(t, priv, db, mr, tc.dispatch)
			poll := newQBroker(t, priv, db, mr, tc.pollMode)
			nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
			offers := []protocol.ModelOffer{{Model: "free-m"}}
			miRegisterNode(disp, "n1", hex.EncodeToString(nodePub), "tok", offers)
			miRegisterNode(poll, "n1", hex.EncodeToString(nodePub), "tok", offers)

			got := make(chan protocol.Job, 4)
			for i := 0; i < 2; i++ {
				go func() {
					if job, ok := pollOnce(t, poll, "n1", "tok"); ok {
						got <- job
						qPostResult(t, poll, "n1", "tok", "free-m", job, nodePriv)
					}
				}()
			}
			waitAdvertised(t, poll, "n1", 2)
			time.Sleep(100 * time.Millisecond) // the legacy SUBSCRIBEs are confirmed too

			tk, err := disp.dispatchRemote(context.Background(), "n1", protocol.Job{ID: "roll-1", Body: []byte(`{}`)}, false)
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			defer tk.close()
			raw, err := tk.awaitResult(time.Now().Add(5 * time.Second))
			if err != nil {
				t.Fatalf("result: %v", err)
			}
			var res protocol.JobResult
			if json.Unmarshal(raw, &res) != nil || res.ID != "roll-1" {
				t.Fatalf("result = %s", raw)
			}
			time.Sleep(200 * time.Millisecond)
			if n := len(got); n != 1 {
				t.Errorf("the job was served %d times, want exactly 1", n)
			}
		})
	}
}

// A job handed to a poll in memory just as its node hung up is put back and served by the
// next poll: never lost, never doubled.
func TestQueueHandoffToHungUpPollIsRequeued(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newQBroker(t, priv, db, mr, dispatchViaQueueOnly)
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	miRegisterNode(a, "n1", hex.EncodeToString(nodePub), "tok", []protocol.ModelOffer{{Model: "free-m"}})

	// A poll whose node has already hung up (its context is cancelled) but which is still
	// in the idle set when the job arrives.
	q := a.dqueue()
	dead := q.newWaiter("n1")
	q.advertise(dead)
	tk, err := a.dispatchRemote(context.Background(), "n1", protocol.Job{ID: "hang-1", Body: []byte(`{}`)}, false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	defer tk.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, "/agent/poll?node=n1", nil).WithContext(ctx)
	e := <-dead.wake
	if e == nil || !q.handOver(httptest.NewRecorder(), r, "n1", e) {
		t.Fatalf("the hung-up poll did not finish on the handoff")
	}
	q.retire(dead)

	// The next poll gets the job.
	job, ok := pollOnce(t, a, "n1", "tok")
	if !ok || job.ID != "hang-1" {
		t.Fatalf("next poll = %v %v, want hang-1", job.ID, ok)
	}
	qPostResult(t, a, "n1", "tok", "free-m", job, nodePriv)
	if _, err := tk.awaitResult(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("result: %v", err)
	}
}

// The inbox reader never blocks on one slow stream consumer: overflow fails that ticket.
func TestQueueSlowStreamConsumerFailsItsTicketOnly(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	a := newQBroker(t, priv, store.NewMem(), mr, dispatchViaQueueOnly)
	q := a.dqueue()
	tk := newTicket(q, "slow-1", "n1")
	q.mu.Lock()
	q.tickets["slow-1"] = tk
	q.mu.Unlock()
	for i := 0; i <= dqFrameBuf; i++ {
		q.handle(dqMsg{kind: "chunk", job: "slow-1", data: []byte("x")})
	}
	select {
	case <-tk.done:
		if tk.Err() != errStreamLag {
			t.Errorf("err = %v, want errStreamLag", tk.Err())
		}
	default:
		t.Fatal("an overflowing stream did not fail its ticket")
	}
	// A message for a finished job is absorbed.
	q.handle(dqMsg{kind: "res", job: "gone", data: []byte(`{}`)})
}

func TestDispatchModeFromEnv(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want dispatchMode
	}{{"", dispatchViaBus}, {"bus", dispatchViaBus}, {"queue", dispatchViaQueue}, {" Queue ", dispatchViaQueue}, {"queue-only", dispatchViaQueueOnly}, {"nonsense", dispatchViaBus}} {
		t.Setenv("ROGERAI_DISPATCH", tc.env)
		if got := dispatchModeFromEnv(); got != tc.want {
			t.Errorf("ROGERAI_DISPATCH=%q -> %v, want %v", tc.env, got, tc.want)
		}
	}
}

func TestValkeyTopologyFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name, url, ring, cluster string
		ok, isCluster            bool
		urls                     int
	}{
		{"none", "", "", "", false, false, 0},
		{"single", "redis://a:1", "", "", true, false, 1},
		{"ring wins", "redis://a:1", "redis://b:1, redis://c:1,", "", true, false, 2},
		{"cluster", "redis://a:1", "", "redis://d:1,redis://e:1", true, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ROGERAI_REDIS_URL", tc.url)
			t.Setenv("ROGERAI_REDIS_RING", tc.ring)
			t.Setenv("ROGERAI_REDIS_CLUSTER", tc.cluster)
			tp, ok := valkeyTopologyFromEnv()
			if ok != tc.ok || tp.cluster != tc.isCluster || len(tp.urls) != tc.urls {
				t.Errorf("got %+v %v", tp, ok)
			}
		})
	}
}

func TestValkeyTopologyBuildsEachClientKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		tp   valkeyTopology
		kind string
	}{
		{"single", valkeyTopology{urls: []string{"redis://127.0.0.1:1"}}, "*redis.Client"},
		{"ring", valkeyTopology{urls: []string{"redis://127.0.0.1:1", "redis://:pw@127.0.0.1:2/3"}}, "*redis.Ring"},
		{"cluster", valkeyTopology{urls: []string{"redis://127.0.0.1:1", "redis://127.0.0.1:2"}, cluster: true}, "*redis.ClusterClient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, pool, err := tc.tp.build(time.Second, 7)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			var kind string
			switch c.(type) {
			case *redis.Client:
				kind = "*redis.Client"
			case *redis.Ring:
				kind = "*redis.Ring"
			case *redis.ClusterClient:
				kind = "*redis.ClusterClient"
			}
			if kind != tc.kind || pool != 7 {
				t.Errorf("built %s pool %d, want %s pool 7", kind, pool, tc.kind)
			}
		})
	}
	if _, _, err := (valkeyTopology{}).build(time.Second, 0); err == nil {
		t.Error("an empty topology built a client")
	}
	if _, _, err := (valkeyTopology{urls: []string{"::bad"}}).build(time.Second, 0); err == nil {
		t.Error("a bad URL built a client")
	}
}

func TestWriteDispatchFailureWording(t *testing.T) {
	for _, tc := range []struct {
		multi bool
		mode  dispatchMode
		out   dispatchOutcome
		body  string
		retry bool
	}{
		{true, dispatchViaQueueOnly, dispatchBusy, "station busy", true},
		{true, dispatchViaQueueOnly, dispatchOffAir, "station off air", true}, // §14.8: every dispatch failure carries a Retry-After
		{true, dispatchViaQueueOnly, dispatchLost, "station handoff failed", true},
		{true, dispatchViaQueueOnly, dispatchBusErr, "dispatch bus unavailable", true},
		{true, dispatchViaBus, dispatchBusy, "node busy (no poller free)", true},
		{false, dispatchViaBus, dispatchBusy, "node busy (no poller free)", true},
	} {
		b := &broker{multiInstance: tc.multi, dispatchMode: tc.mode}
		w := httptest.NewRecorder()
		b.writeDispatchFailure(w, tc.out)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), tc.body) || (w.Header().Get("Retry-After") != "") != tc.retry {
			t.Errorf("%+v -> %d %q retry=%q", tc, w.Code, w.Body.String(), w.Header().Get("Retry-After"))
		}
		code := `"code":"station_busy"`
		if tc.out == dispatchOffAir {
			code = `"code":"station_off_air"`
		}
		if !strings.Contains(w.Body.String(), code) || w.Header().Get("X-RogerAI-Cost") != "0" {
			t.Errorf("%+v -> %q cost=%q, want %s and $0", tc, w.Body.String(), w.Header().Get("X-RogerAI-Cost"), code)
		}
	}
	for _, tc := range []struct {
		last          dispatchOutcome
		tried, offAir int
		want          dispatchOutcome
	}{
		{dispatchOffAir, 2, 2, dispatchOffAir}, {dispatchOffAir, 2, 1, dispatchBusy}, {dispatchLost, 1, 0, dispatchLost},
	} {
		if got := planDispatchOutcome(tc.last, tc.tried, tc.offAir); got != tc.want {
			t.Errorf("planDispatchOutcome%+v = %v", tc, got)
		}
	}
	for err, want := range map[error]dispatchOutcome{
		errNoPoller: dispatchNoPoller, errStationBusy: dispatchBusy, errOffAir: dispatchOffAir,
		errHandoffLost: dispatchLost, context.DeadlineExceeded: dispatchTimeout, errBadResult: dispatchBusErr,
	} {
		if got := (&broker{}).dispatchErrOutcome(err); got != want {
			t.Errorf("%v -> %v, want %v", err, got, want)
		}
	}
}

// ttsQueuePair builds two queue-only instances sharing one Valkey and one store, with a TTS
// station "v1" registered on both and a funded consumer. It returns the consumer-facing
// instance, the node-facing instance, the node key and a signed speech request builder.
func ttsQueuePair(t *testing.T) (consumerSide, nodeSide *broker, nodePriv ed25519.PrivateKey, speech func() *http.Request) {
	t.Helper()
	mr := miniredis.RunT(t)
	mem := store.NewMem()
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	mk := func() *broker {
		b := relayBroker(mem)
		vs, err := newValkeyStore("redis://" + mr.Addr())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = vs.Close() })
		b.shared, b.multiInstance, b.instanceID, b.dispatchMode = vs, true, newInstanceID(), dispatchViaQueueOnly
		b.nodes["v1"] = protocol.NodeRegistration{
			NodeID: "v1", PubKey: hex.EncodeToString(nodePub), Station: "op", BridgeToken: "vtok",
			Offers: []protocol.ModelOffer{{Model: "voice-raw", Modality: protocol.ModalityTTS, Name: "voice-a", PriceIn: 10}},
		}
		b.lastSeen["v1"] = time.Now()
		b.tunnels["v1"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}, token: "vtok"}
		return b
	}
	consumerSide, nodeSide = mk(), mk()
	if err := mem.BindNode("v1", "oppub"); err != nil {
		t.Fatal(err)
	}
	if err := mem.BindOwner(store.Owner{GitHubID: 42, Login: "op", Pubkey: "oppub"}); err != nil {
		t.Fatal(err)
	}
	userPub, userPriv, _ := ed25519.GenerateKey(nil)
	if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: hex.EncodeToString(userPub)}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddCredits("u_gh_7", 5); err != nil {
		t.Fatal(err)
	}
	speech = func() *http.Request {
		body := []byte(`{"model":"voice-raw","input":"hello there","response_format":"mp3"}`)
		r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", bytes.NewReader(body))
		signReq(r, userPriv, body)
		return r
	}
	return consumerSide, nodeSide, nodePriv, speech
}

// A voice app's shape: a burst of sentences to a voice station whose polls sit on the OTHER
// instance is spoken in full, each once.
func TestQueueAudioBurstAcrossInstances(t *testing.T) {
	cons, node, nodePriv, speech := ttsQueuePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	served := map[string]int{}
	for i := 0; i < 4; i++ {
		go func() {
			for ctx.Err() == nil {
				r := httptest.NewRequest(http.MethodGet, "/agent/poll?node=v1", nil).WithContext(ctx)
				r.Header.Set("Authorization", miNodeBearer("vtok"))
				w := httptest.NewRecorder()
				node.agentPoll(w, r)
				var job protocol.Job
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &job) != nil {
					continue
				}
				mu.Lock()
				served[job.ID]++
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: "v1", Model: "voice-raw", TS: time.Now().Unix()}
				rec.SignNode(nodePriv)
				rb, _ := json.Marshal(protocol.JobResult{ID: job.ID, Status: 200, Body: []byte("ID3-audio"), Receipt: rec})
				rr := httptest.NewRequest(http.MethodPost, "/agent/result?node=v1", bytes.NewReader(rb))
				rr.Header.Set("Authorization", miNodeBearer("vtok"))
				node.agentResult(httptest.NewRecorder(), rr)
			}
		}()
	}
	waitAdvertised(t, node, "v1", 4)

	codes := make(chan int, 4)
	for i := 0; i < 4; i++ {
		go func() {
			w := httptest.NewRecorder()
			cons.audioRelay(w, speech())
			codes <- w.Code
		}()
	}
	for i := 0; i < 4; i++ {
		if c := <-codes; c != http.StatusOK {
			t.Errorf("sentence %d answered %d, want 200", i, c)
		}
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(served) != 4 {
		t.Errorf("%d distinct sentences served, want 4", len(served))
	}
	for id, n := range served {
		if n != 1 {
			t.Errorf("sentence %s served %d times", id, n)
		}
	}
}

// A voice station with every poller busy: the sentence waits the audio queue wait, then an
// honest retryable "station busy", and the hold is refunded.
func TestQueueAudioBusyAfterQueueWait(t *testing.T) {
	defer func(d time.Duration) { queueWaitAudio = d }(queueWaitAudio)
	queueWaitAudio = 300 * time.Millisecond
	cons, _, _, speech := ttsQueuePair(t)
	before, _ := cons.db.(*store.Mem).PeekBalance("u_gh_7")
	w := httptest.NewRecorder()
	cons.audioRelay(w, speech())
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "station busy") || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("busy voice relay = %d %q (Retry-After %q)", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
	}
	if after, _ := cons.db.(*store.Mem).PeekBalance("u_gh_7"); after != before {
		t.Errorf("balance %v -> %v: the hold for an unserved sentence was not refunded", before, after)
	}
}

// failingWriter is a poll response whose connection is already gone.
type failingWriter struct{ h http.Header }

func (f *failingWriter) Header() http.Header       { return f.h }
func (f *failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (f *failingWriter) WriteHeader(int)           {}

// A job whose write to an ack-capable poll fails is re-delivered, not failed: the node never
// got it, and a node that acks dedupes by job id anyway.
func TestQueueWriteFailureToAckingPollIsRedelivered(t *testing.T) {
	defer dqTakenGrace.set(dqTakenGrace.get())
	dqTakenGrace.set(200 * time.Millisecond)
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	a := newQBroker(t, priv, store.NewMem(), mr, dispatchViaQueueOnly)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	miRegisterNode(a, "n1", hex.EncodeToString(nodePub), "tok", []protocol.ModelOffer{{Model: "free-m"}})
	tk, err := a.dispatchRemote(context.Background(), "n1", protocol.Job{ID: "wf-1", Body: []byte(`{}`)}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer tk.close()
	q := a.dqueue()
	e := q.pop("n1")
	if e == nil {
		t.Fatal("the job was not queued")
	}
	r := httptest.NewRequest(http.MethodGet, "/agent/poll?node=n1", nil)
	r.Header.Set(ackHeader, "1")
	if !q.handOver(&failingWriter{h: http.Header{}}, r, "n1", e) {
		t.Fatal("a failed write did not finish the poll")
	}
	deadline := time.Now().Add(3 * time.Second)
	for a.stats.dqRedeliver.Load() == 0 {
		select {
		case <-tk.done:
			t.Fatalf("the job failed (%v) instead of being re-delivered", tk.Err())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the job was never re-delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if again := q.pop("n1"); again == nil {
		t.Fatal("the re-delivered job is not back in the node's queue")
	}
}
