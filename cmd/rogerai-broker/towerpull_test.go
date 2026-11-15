package main

// Unit tests for towerpull.go's edges (features/tower/tower_pull_dispatch.feature); the
// scenarios themselves run in tower_pull_dispatch_*_bdd_test.go.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/placement"
	"rogerai.fm/roger/v7/internal/towerhub"
)

func pullTower(t *testing.T) (*broker, linkTower, string, func(path string, body any) (int, string)) {
	t.Helper()
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "pull-unit")
	lt := enrolledTower(t, b, op.login)
	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{Network: link.PublicNetwork, Versions: []int{1},
		TowerID: lt.id, Capabilities: append(mandatoryCaps(), link.CapPull), RelayEndpoint: "203.0.113.7:8444"}), &acc)
	require.Equal(t, http.StatusOK, code, raw)
	post := func(path string, body any) (int, string) {
		return signedPostTo(lt, srv.URL, path, jsonOf(t, body), nil, 10*time.Second)
	}
	return b, lt, acc.SessionID, post
}

func TestAnAnswerIsCheckedForItsTowerBeforeItIsRead(t *testing.T) {
	b, lt, sess, post := pullTower(t)
	other := enrolledTower(t, b, signedInOperator(t, b, "other").login)
	body := towerhub.PullAnswer{TowerID: lt.id, SessionID: sess, ID: "pw-x", Status: 200}
	code, _ := post("/tower/work/answer", body) // no tower_id in the URL
	require.Equal(t, http.StatusForbidden, code)
	code, _ = post("/tower/work/answer?tower_id="+other.id, body) // the key is not that Tower's
	require.Equal(t, http.StatusForbidden, code)
}

func TestANonsenseAnswerStatusIsABadGateway(t *testing.T) {
	b, lt, sess, post := pullTower(t)
	got := make(chan *http.Response, 1)
	go func() {
		base, hc := b.pullClient(lt.id)
		req, _ := http.NewRequest(http.MethodPost, base+towerhub.PathSubmit, nil)
		resp, err := hc.Do(req)
		require.NoError(t, err)
		got <- resp
	}()
	var work []towerhub.PullWork
	require.Eventually(t, func() bool {
		var out struct {
			Work []towerhub.PullWork `json:"work"`
		}
		c, raw := post("/tower/work/pull", map[string]string{"tower_id": lt.id, "session_id": sess})
		if c == http.StatusOK {
			_ = json.Unmarshal([]byte(raw), &out)
			work = out.Work
		}
		return len(work) == 1
	}, 5*time.Second, 10*time.Millisecond)
	code, raw := post("/tower/work/answer?tower_id="+lt.id, towerhub.PullAnswer{TowerID: lt.id, SessionID: sess, ID: work[0].ID, Status: 999})
	require.Equal(t, http.StatusOK, code, raw)
	resp := <-got
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

func TestCoresOwnStoreFailingIsNeverTheTowersFault(t *testing.T) {
	b, lt, _, _ := pullTower(t)
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = vs.Close() })
	mr.Close() // the store is down
	b.shared = vs
	base, hc := b.pullClient(lt.id)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = (&towerhub.Client{BaseURL: base, HTTP: hc}).SubmitJob(ctx, []byte("g"), []byte("e"))
	require.True(t, errors.Is(err, errPullUnavailable), "got %v", err)
}

func TestAFinishedSenderStopsListeningButKeepsItsWithdrawal(t *testing.T) {
	b, lt, _, _ := pullTower(t)
	prev := pullPickupDeadline
	pullPickupDeadline = 50 * time.Millisecond
	defer func() { pullPickupDeadline = prev }()
	base, hc := b.pullClient(lt.id)
	_, err := (&towerhub.Client{BaseURL: base, HTTP: hc}).SubmitJob(context.Background(), []byte("g"), []byte("e"))
	require.ErrorIs(t, err, errPullNotTaken)
	b.pulls.mu.Lock()
	defer b.pulls.mu.Unlock()
	require.Empty(t, b.pulls.answers, "a finished sender stops listening")
	require.Len(t, b.pulls.claim, 1, "its withdrawn claim stays until it expires, so the work is never run late")
}

// Work a Tower never received (its pull hung up as the work was handed over) goes back on the
// queue unclaimed, and the next pull takes it - on one instance and over the shared store.
func TestWorkAHungUpPullNeverReceivedIsGivenBack(t *testing.T) {
	for _, shared := range []bool{false, true} {
		b, lt, _, _ := pullTower(t)
		if shared {
			vs, _ := testValkeyShared(t)
			b.shared = vs
		}
		w := pullWork{ID: "pw-back", Method: http.MethodPost, Path: towerhub.PathSubmit,
			Exp: time.Now().Add(time.Minute).UnixMilli()}
		require.NoError(t, b.pullQueue(lt.id, w))
		got := take(b, lt.id)
		require.Len(t, got, 1)
		require.Empty(t, take(b, lt.id), "taken work is claimed")
		b.pullUntake(lt.id, got)
		again := take(b, lt.id)
		require.Len(t, again, 1, "shared=%v: the given-back work is taken by the next pull", shared)
		require.Equal(t, "pw-back", again[0].ID)
		// Another Tower's give-back cannot release this Tower's claim.
		b.pullUntake("tw-other", again)
		require.Empty(t, take(b, lt.id))
	}
}

// Over the shared store an answer is parked under a key and only a notice is published (a
// subscriber is cut off past Valkey's output-buffer limit, which would lose the largest answers);
// the waiting instance reads it and removes it.
func TestASharedAnswerIsParkedNotPublished(t *testing.T) {
	b, lt, sess, post := pullTower(t)
	vs, mr := testValkeyShared(t)
	b.shared = vs
	got := make(chan []byte, 1)
	go func() {
		base, hc := b.pullClient(lt.id)
		req, _ := http.NewRequest(http.MethodPost, base+towerhub.PathSubmit, nil)
		resp, err := hc.Do(req)
		if err != nil {
			got <- nil
			return
		}
		body, _ := io.ReadAll(resp.Body)
		got <- body
	}()
	var work []towerhub.PullWork
	require.Eventually(t, func() bool {
		c, raw := post("/tower/work/pull", map[string]string{"tower_id": lt.id, "session_id": sess})
		if c == http.StatusOK {
			var out struct {
				Work []towerhub.PullWork `json:"work"`
			}
			_ = json.Unmarshal([]byte(raw), &out)
			work = out.Work
		}
		return len(work) == 1
	}, 5*time.Second, 10*time.Millisecond)
	big := bytes.Repeat([]byte("a"), 1<<20)
	code, raw := post("/tower/work/answer?tower_id="+lt.id, towerhub.PullAnswer{TowerID: lt.id, SessionID: sess,
		ID: work[0].ID, Status: 200, Body: big})
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, big, <-got)
	require.False(t, mr.Exists(cacheKeyPrefix+pullAnswerKey(work[0].ID)), "the parked answer was not removed")
}

// Audit finding: work given back after its pickup deadline cannot be retaken, so its sender is
// told now and fails over, instead of waiting out its whole drive.
func TestLateGivenBackWorkFailsTheSenderOverAtOnce(t *testing.T) {
	b, lt, _, _ := pullTower(t)
	prev := pullPickupDeadline
	pullPickupDeadline = 200 * time.Millisecond
	defer func() { pullPickupDeadline = prev }()
	done := make(chan error, 1)
	go func() {
		base, hc := b.pullClient(lt.id)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := (&towerhub.Client{BaseURL: base, HTTP: hc}).SubmitJob(ctx, []byte("g"), []byte("e"))
		done <- err
	}()
	var taken []pullWork
	require.Eventually(t, func() bool { taken = take(b, lt.id); return len(taken) == 1 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // past the deadline; the Tower has the claim
	start := time.Now()
	b.pullUntake(lt.id, taken)
	select {
	case err := <-done:
		require.ErrorIs(t, err, errPullNotTaken)
		require.Less(t, time.Since(start), 2*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("the sender was never told")
	}
}

// Audit finding: on one instance, a withdrawn claim outlives its sender, so buffered work the
// sender gave up on is never run.
func TestAWithdrawnClaimOutlivesItsSender(t *testing.T) {
	b, lt, _, _ := pullTower(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		base, hc := b.pullClient(lt.id)
		_, _ = (&towerhub.Client{BaseURL: base, HTTP: hc}).SubmitJob(ctx, []byte("g"), []byte("e"))
	}()
	require.Eventually(t, func() bool {
		b.pulls.mu.Lock()
		defer b.pulls.mu.Unlock()
		return len(b.pulls.buf[lt.id]) == 1
	}, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done
	require.Empty(t, take(b, lt.id), "work its sender withdrew was handed to a pull")
	b.pulls.mu.Lock()
	b.pulls.pruneLocked(time.Now().Add(pullClaimTTL + time.Minute))
	n := len(b.pulls.claim)
	b.pulls.mu.Unlock()
	require.Zero(t, n, "claims are pruned once they cannot matter")
}

// Audit finding: a session adopted from a record written before hub classes existed has a
// location but no class; the sweep measures it again rather than leaving it unplaceable.
func TestAnUnclassedHubIsMeasuredAgain(t *testing.T) {
	b, lt, sess, _ := pullTower(t)
	require.Eventually(t, func() bool { return b.tower.link.HubClass(lt.id) != placement.HubNone }, 3*time.Second, 5*time.Millisecond)
	require.True(t, b.tower.link.SetHubClass(lt.id, sess, placement.HubNone)) // as an old record reads
	b.remeasureTowerLocality(lt.id)
	require.Equal(t, placement.HubPublic, b.tower.link.HubClass(lt.id))
}

// flakyStore is the real shared store with chosen operations failing, to stand in for a
// Valkey that answers some calls and times out on others.
type flakyStore struct {
	*valkeyStore
	failCacheSet atomic.Int64 // fail this many cacheSet calls
	failClaim    atomic.Int64 // fail this many setIfAbsent calls
	failSub      bool         // every result subscription fails
	dropNotices  bool         // result notices are lost, as while a subscriber reconnects
	claimLies    bool         // setIfAbsent writes, then reports a timeout
	drains       atomic.Int64 // towerWorkDrain calls
}

func (f *flakyStore) towerWorkDrain(tower string) ([][]byte, error) {
	f.drains.Add(1)
	return f.valkeyStore.towerWorkDrain(tower)
}

func (f *flakyStore) setIfAbsent(k, v string, ttl time.Duration) (bool, error) {
	if f.failClaim.Add(-1) >= 0 {
		return false, errors.New("i/o timeout")
	}
	if f.claimLies {
		_, _ = f.valkeyStore.setIfAbsent(k, v, ttl)
		return false, errors.New("i/o timeout")
	}
	return f.valkeyStore.setIfAbsent(k, v, ttl)
}

func (f *flakyStore) busPublishResult(id string, raw []byte) error {
	if f.dropNotices {
		return nil
	}
	return f.valkeyStore.busPublishResult(id, raw)
}

func (f *flakyStore) cacheSet(k string, v []byte, ttl time.Duration) error {
	if f.failCacheSet.Add(-1) >= 0 {
		return errors.New("i/o timeout")
	}
	return f.valkeyStore.cacheSet(k, v, ttl)
}

func (f *flakyStore) busSubscribeResult(ctx context.Context, id string) (<-chan []byte, func(), error) {
	if f.failSub {
		return nil, func() {}, errors.New("i/o timeout")
	}
	return f.valkeyStore.busSubscribeResult(ctx, id)
}

func (f *flakyStore) healthy() bool { return true }

// Audit finding: an answer Core accepted but could not hand on is not "received"; the Tower's
// retry is taken and delivered.
func TestAnAnswerCoreCouldNotDeliverIsTakenOnRetry(t *testing.T) {
	b, lt, sess, post := pullTower(t)
	vs, _ := testValkeyShared(t)
	fs := &flakyStore{valkeyStore: vs}
	b.shared = fs
	got := make(chan []byte, 1)
	go func() {
		base, hc := b.pullClient(lt.id)
		req, _ := http.NewRequest(http.MethodPost, base+towerhub.PathSubmit, nil)
		resp, err := hc.Do(req)
		if err != nil {
			got <- nil
			return
		}
		body, _ := io.ReadAll(resp.Body)
		got <- body
	}()
	var work []towerhub.PullWork
	require.Eventually(t, func() bool {
		c, raw := post("/tower/work/pull", map[string]string{"tower_id": lt.id, "session_id": sess})
		if c == http.StatusOK {
			var out struct {
				Work []towerhub.PullWork `json:"work"`
			}
			_ = json.Unmarshal([]byte(raw), &out)
			work = out.Work
		}
		return len(work) == 1
	}, 5*time.Second, 10*time.Millisecond)
	fs.failCacheSet.Store(1)
	ans := towerhub.PullAnswer{TowerID: lt.id, SessionID: sess, ID: work[0].ID, Status: 200, Body: []byte("the answer")}
	code, raw := post("/tower/work/answer?tower_id="+lt.id, ans)
	require.Equal(t, http.StatusServiceUnavailable, code, raw)
	code, raw = post("/tower/work/answer?tower_id="+lt.id, ans)
	require.Equal(t, http.StatusOK, code, raw)
	require.NotContains(t, raw, "already received", "the retry was refused as a duplicate")
	require.Equal(t, []byte("the answer"), <-got)
}

// Audit finding: a store failure in the middle of a pull hub check is Core's, never recorded
// against the Tower.
func TestAStoreFailureMidHubCheckRecordsNothing(t *testing.T) {
	b, lt, _, _ := pullTower(t)
	vs, _ := testValkeyShared(t)
	b.shared = &flakyStore{valkeyStore: vs, failSub: true}
	b.towerHubCheckOnce(time.Now())
	sum, _ := b.tower.health.Summary(lt.id, time.Time{})
	require.True(t, sum.LastAt.IsZero(), "a store failure was recorded as a hub check: %+v", sum)
}

// Audit finding: work nobody pulls cannot pile up - expired work is dropped on one instance, and
// the shared list keeps only the newest towerWorkMax.
func TestQueuedWorkStaysBounded(t *testing.T) {
	b, lt, _, _ := pullTower(t)
	old := pullWork{ID: "pw-old", Exp: time.Now().Add(-time.Second).UnixMilli()}
	require.NoError(t, b.pullQueue(lt.id, old))
	require.NoError(t, b.pullQueue(lt.id, pullWork{ID: "pw-new", Exp: time.Now().Add(time.Minute).UnixMilli()}))
	b.pulls.mu.Lock()
	require.Len(t, b.pulls.buf[lt.id], 1, "expired work was kept")
	b.pulls.mu.Unlock()

	vs, mr := testValkeyShared(t)
	b.shared = vs
	for i := 0; i < towerWorkMax+20; i++ {
		require.NoError(t, b.pullQueue(lt.id, pullWork{ID: fmt.Sprintf("pw-%d", i), Exp: time.Now().Add(time.Minute).UnixMilli()}))
	}
	items, err := mr.List(towerWorkBufPrefix + lt.id)
	require.NoError(t, err)
	require.Len(t, items, towerWorkMax)
	require.True(t, streamRoutes["/tower/work/pull"] && streamRoutes["/tower/work/answer"],
		"the held pull and the large answer bound themselves, not the 30 s response deadline")
}

// countingBody counts how much of an answer Core read.
type countingBody struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// Review finding: the answer route's pre-read check compared only a public key, which a Tower's
// own hub hands out, so anyone could make Core buffer ~42 MiB per connection. Now the caller's
// signature (over the body's digest) and its claim on the named work are checked before a byte is
// read, and an answer that declares itself over the cap is refused unread.
func TestAnAnswerIsAuthenticatedBeforeAByteIsRead(t *testing.T) {
	b, lt, sess, _ := pullTower(t)
	require.NoError(t, b.pullQueue(lt.id, pullWork{ID: "pw-mine", Exp: time.Now().Add(time.Minute).UnixMilli()}))
	require.Len(t, take(b, lt.id), 1)
	ans := jsonOf(t, towerhub.PullAnswer{TowerID: lt.id, SessionID: sess, ID: "pw-mine", Status: 200, Body: bytes.Repeat([]byte("a"), 1<<20)})

	send := func(query string, body []byte, sign func(r *http.Request)) (int, int64) {
		cb := &countingBody{r: bytes.NewReader(body)}
		req := httptest.NewRequest(http.MethodPost, "/tower/work/answer?"+query, cb)
		sign(req)
		rec := httptest.NewRecorder()
		b.towerWorkAnswer(rec, req)
		return rec.Code, cb.n.Load()
	}
	signDigest := func(digest string) func(r *http.Request) {
		return func(r *http.Request) {
			pub, ts, sig := protocol.SignRequest(lt.priv, http.MethodPost, "/tower/work/answer", []byte(digest))
			r.Header.Set(towerhub.AnswerDigestHeader, digest)
			r.Header.Set("X-Roger-Pubkey", pub)
			r.Header.Set("X-Roger-TS", itoa64(ts))
			r.Header.Set("X-Roger-Sig", sig)
		}
	}
	q := "tower_id=" + lt.id + "&work=pw-mine"

	// The Tower's public key with no valid signature: refused unread.
	code, read := send(q, ans, func(r *http.Request) {
		r.Header.Set("X-Roger-Pubkey", hexOf(lt.priv.Public().(ed25519.PublicKey)))
		r.Header.Set("X-Roger-TS", itoa64(time.Now().Unix()))
		r.Header.Set("X-Roger-Sig", strings.Repeat("00", ed25519.SignatureSize))
		r.Header.Set(towerhub.AnswerDigestHeader, towerhub.AnswerDigest(ans))
	})
	require.Equal(t, http.StatusForbidden, code)
	require.Zero(t, read, "an unauthenticated answer was read")

	// Signed, but for work this Tower does not hold: refused unread.
	code, read = send("tower_id="+lt.id+"&work=pw-not-mine", ans, signDigest(towerhub.AnswerDigest(ans)))
	require.Equal(t, http.StatusForbidden, code)
	require.Zero(t, read, "an answer for work the Tower does not hold was read")

	// Signed and held, but declaring more than the cap: refused unread.
	code, read = send(q, ans, func(r *http.Request) {
		signDigest(towerhub.AnswerDigest(ans))(r)
		r.ContentLength = maxPullAnswer + 1
	})
	require.Equal(t, http.StatusRequestEntityTooLarge, code)
	require.Zero(t, read, "an answer declaring itself over the cap was read")

	// Signed over one digest, carrying other bytes: refused.
	code, _ = send(q, ans, signDigest(towerhub.AnswerDigest([]byte("something else"))))
	require.Equal(t, http.StatusForbidden, code)

	// The work named in the URL must be the work answered.
	other := jsonOf(t, towerhub.PullAnswer{TowerID: lt.id, SessionID: sess, ID: "pw-other", Status: 200})
	code, _ = send(q, other, signDigest(towerhub.AnswerDigest(other)))
	require.Equal(t, http.StatusForbidden, code)

	// All of it right: accepted.
	code, _ = send(q, ans, signDigest(towerhub.AnswerDigest(ans)))
	require.Equal(t, http.StatusOK, code)
}

// Review finding: an answer parked while its notice was lost (a subscriber reconnecting) was never
// read; the sender waited out its drive and the Tower was blamed. The sender also looks for the
// parked answer on its own.
func TestAParkedAnswerIsFoundWithoutItsNotice(t *testing.T) {
	prev := pullAnswerPoll
	pullAnswerPoll = 20 * time.Millisecond
	defer func() { pullAnswerPoll = prev }()
	b, lt, _, _ := pullTower(t)
	vs, _ := testValkeyShared(t)
	b.shared = &flakyStore{valkeyStore: vs, dropNotices: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	answers, stop, err := b.pullAwait(ctx, "pw-quiet")
	require.NoError(t, err)
	defer stop()
	require.NoError(t, b.deliverAnswer(pullAnswer{TowerID: lt.id, ID: "pw-quiet", Status: 200}))
	select {
	case a := <-answers:
		require.Equal(t, "pw-quiet", a.ID)
	case <-ctx.Done():
		t.Fatal("a parked answer whose notice was lost was never read")
	}
}

// Review finding: a store error while claiming drained work dropped that work, and the sender's
// withdrawal then counted it as the Tower's miss. Work Core could not claim goes back on the queue.
func TestWorkCoreCouldNotClaimGoesBack(t *testing.T) {
	b, lt, _, _ := pullTower(t)
	vs, _ := testValkeyShared(t)
	fs := &flakyStore{valkeyStore: vs}
	b.shared = fs
	require.NoError(t, b.pullQueue(lt.id, pullWork{ID: "pw-c", Exp: time.Now().Add(time.Minute).UnixMilli()}))
	fs.failClaim.Store(1)
	require.Empty(t, take(b, lt.id))
	got := take(b, lt.id)
	require.Len(t, got, 1, "work Core could not claim was dropped")
	require.Equal(t, "pw-c", got[0].ID)
}

// Review finding: work given back just before its deadline could lose its sender (the sender saw
// the Tower's claim and kept waiting; the requeued work then expired unseen). Expired work that
// nobody holds tells its sender, so it fails over; work its sender withdrew stays quiet.
func TestExpiredWorkNobodyHoldsTellsItsSender(t *testing.T) {
	for _, shared := range []bool{false, true} {
		b, lt, _, _ := pullTower(t)
		if shared {
			vs, _ := testValkeyShared(t)
			b.shared = vs
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		answers, stop, err := b.pullAwait(ctx, "pw-late")
		require.NoError(t, err)
		require.NoError(t, b.pullQueue(lt.id, pullWork{ID: "pw-late", Exp: time.Now().Add(-time.Millisecond).UnixMilli()}))
		require.Empty(t, take(b, lt.id))
		select {
		case a := <-answers:
			require.Equal(t, 0, a.Status, "shared=%v", shared)
			require.Equal(t, lt.id, a.TowerID)
		case <-ctx.Done():
			t.Fatalf("shared=%v: the sender of expired, unheld work was never told", shared)
		}
		stop()
		cancel()

		won, err := b.pullClaim("pw-gone", pullWithdrawn)
		require.NoError(t, err)
		require.True(t, won)
		ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
		answers, stop, err = b.pullAwait(ctx, "pw-gone")
		require.NoError(t, err)
		require.NoError(t, b.pullQueue(lt.id, pullWork{ID: "pw-gone", Exp: time.Now().Add(-time.Millisecond).UnixMilli()}))
		require.Empty(t, take(b, lt.id))
		select {
		case a, ok := <-answers:
			require.False(t, ok, "shared=%v: work its sender withdrew sent %+v", shared, a)
		case <-ctx.Done():
		}
		stop()
		cancel()
	}
}

// Review finding: work requeued after a store error woke the same held pull, which drained and
// failed again in a tight loop against the failing store. A pull pauses before taking again.
func TestAFailingStoreDoesNotSpinAHeldPull(t *testing.T) {
	prev := pullHold
	pullHold = 600 * time.Millisecond
	defer func() { pullHold = prev }()
	b, lt, sess, post := pullTower(t)
	vs, _ := testValkeyShared(t)
	fs := &flakyStore{valkeyStore: vs}
	b.shared = fs
	require.NoError(t, b.pullQueue(lt.id, pullWork{ID: "pw-spin", Exp: time.Now().Add(time.Minute).UnixMilli()}))
	fs.failClaim.Store(1 << 30)
	code, raw := post("/tower/work/pull", map[string]string{"tower_id": lt.id, "session_id": sess})
	require.Equal(t, http.StatusNoContent, code, raw)
	require.Less(t, fs.drains.Load(), int64(20), "a held pull spun against a failing store")
}

// Review finding: a claim the store wrote but reported as failed was requeued, then dropped by the
// next take as already claimed. Core asks who holds it; if it is this Tower, the work is handed over.
func TestAClaimWrittenButReportedFailedIsKept(t *testing.T) {
	b, lt, _, _ := pullTower(t)
	vs, _ := testValkeyShared(t)
	b.shared = &flakyStore{valkeyStore: vs, claimLies: true}
	require.NoError(t, b.pullQueue(lt.id, pullWork{ID: "pw-lie", Exp: time.Now().Add(time.Minute).UnixMilli()}))
	got, _ := b.pullTake(lt.id)
	require.Len(t, got, 1)
}

// take is pullTake's work, for tests that do not look at store failures.
func take(b *broker, tower string) []pullWork {
	w, _ := b.pullTake(tower)
	return w
}
