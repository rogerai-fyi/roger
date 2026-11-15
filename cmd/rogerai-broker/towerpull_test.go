package main

// Unit tests for towerpull.go's edges (features/tower/tower_pull_dispatch.feature); the
// scenarios themselves run in tower_pull_dispatch_*_bdd_test.go.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/towercore/link"
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
	require.False(t, b.towerKeyMatches(lt.id, "zz"))
	require.False(t, b.towerKeyMatches("tw-unknown", hexOf(lt.priv.Public().(ed25519.PublicKey))))
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

func TestTheLocalClaimsGoWithTheSender(t *testing.T) {
	b, lt, _, _ := pullTower(t)
	prev := pullPickupDeadline
	pullPickupDeadline = 50 * time.Millisecond
	defer func() { pullPickupDeadline = prev }()
	base, hc := b.pullClient(lt.id)
	_, err := (&towerhub.Client{BaseURL: base, HTTP: hc}).SubmitJob(context.Background(), []byte("g"), []byte("e"))
	require.ErrorIs(t, err, errPullNotTaken)
	b.pulls.mu.Lock()
	defer b.pulls.mu.Unlock()
	require.Empty(t, b.pulls.claim, "a finished sender leaves no claim behind")
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
		got := b.pullTake(lt.id)
		require.Len(t, got, 1)
		require.Empty(t, b.pullTake(lt.id), "taken work is claimed")
		b.pullUntake(lt.id, got)
		again := b.pullTake(lt.id)
		require.Len(t, again, 1, "shared=%v: the given-back work is taken by the next pull", shared)
		require.Equal(t, "pw-back", again[0].ID)
		// Another Tower's give-back cannot release this Tower's claim.
		b.pullUntake("tw-other", again)
		require.Empty(t, b.pullTake(lt.id))
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
