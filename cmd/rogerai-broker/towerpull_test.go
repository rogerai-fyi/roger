package main

// Unit tests for towerpull.go's edges (features/tower/tower_pull_dispatch.feature); the
// scenarios themselves run in tower_pull_dispatch_*_bdd_test.go.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
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
