package towerjoin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// The Tower's two pull calls (features/tower/tower_pull_dispatch.feature): a pull names the Tower
// and its session and reads the work handed over; an answer names the Tower in the URL (so Core
// can check the key before it buffers the body) and is signed over the bare path Core verifies.
func TestPullAndAnswerAreSignedAndNamed(t *testing.T) {
	st := registeredTower(t)
	var answerQuery, answerPath string
	var answered towerhub.PullAnswer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts := r.Header.Get(protocol.HeaderTS)
		var tsv int64
		tsv, _ = strconv.ParseInt(ts, 10, 64)
		signed := body
		if r.URL.Path == "/tower/work/answer" {
			// An answer is signed over its digest, which Core checks before it reads the body.
			require.Equal(t, towerhub.AnswerDigest(body), r.Header.Get(towerhub.AnswerDigestHeader))
			signed = []byte(r.Header.Get(towerhub.AnswerDigestHeader))
		}
		_, ok := protocol.VerifyRequest(r.Header.Get(protocol.HeaderPubkey), r.Header.Get(protocol.HeaderSig), tsv, r.Method, r.URL.Path, signed)
		require.True(t, ok, "%s is signed over the path Core verifies", r.URL.Path)
		switch r.URL.Path {
		case "/tower/work/pull":
			var req map[string]string
			_ = json.Unmarshal(body, &req)
			require.Equal(t, "sess-1", req["session_id"])
			_ = json.NewEncoder(w).Encode(map[string]any{"work": []towerhub.PullWork{{ID: "pw-1"}}})
		case "/tower/work/answer":
			answerQuery, answerPath = r.URL.RawQuery, r.URL.Path
			_ = json.Unmarshal(body, &answered)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer srv.Close()
	t.Setenv("ROGER_BROKER", srv.URL)

	work, err := PullWork(context.Background(), st, "sess-1")
	require.NoError(t, err)
	require.Len(t, work, 1)
	require.NoError(t, AnswerWork(context.Background(), st, "sess-1", towerhub.PullAnswer{ID: "pw-1", Status: 200}))
	towerID, _ := CoreTowerID(st)
	require.Equal(t, "/tower/work/answer", answerPath)
	require.Equal(t, "tower_id="+towerID+"&work=pw-1", answerQuery)
	require.Equal(t, towerID, answered.TowerID)
	require.Equal(t, "sess-1", answered.SessionID)
}

func TestPullAndAnswerNeedARegisteredTower(t *testing.T) {
	st := joinedTower(t)
	_, err := PullWork(context.Background(), st, "s")
	require.Error(t, err)
	require.Error(t, AnswerWork(context.Background(), st, "s", towerhub.PullAnswer{}))
}

// Audit finding: Core's own trouble answering (a 5xx) is retried by the Tower like a lost post.
func TestAnAnswerCoreCouldNotTakeIsRetryable(t *testing.T) {
	st := registeredTower(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"work delivery unavailable"}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	t.Setenv("ROGER_BROKER", srv.URL)
	require.ErrorIs(t, AnswerWork(context.Background(), st, "s", towerhub.PullAnswer{ID: "pw"}), ErrUnreachable)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"this tower was not given that work"}}`, http.StatusForbidden)
	}))
	defer srv2.Close()
	t.Setenv("ROGER_BROKER", srv2.URL)
	err := AnswerWork(context.Background(), st, "s", towerhub.PullAnswer{ID: "pw"})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnreachable, "a refusal is final")
}

// Review finding: a link session Core no longer holds (409) is its own error, so the Tower waits
// for the re-opened link instead of dropping the answer or backing off as if Core had refused.
// An answer post also stops with the Tower's context rather than its 45 s client timeout.
func TestASessionCoreNoLongerHoldsIsSaidAndAnAnswerStopsWithItsContext(t *testing.T) {
	st := registeredTower(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"link session is not open"}`, http.StatusConflict)
	}))
	defer srv.Close()
	t.Setenv("ROGER_BROKER", srv.URL)
	_, err := PullWork(context.Background(), st, "s")
	require.ErrorIs(t, err, ErrSessionGone)
	require.ErrorIs(t, AnswerWork(context.Background(), st, "s", towerhub.PullAnswer{ID: "pw"}), ErrSessionGone)

	hang := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-hang }))
	defer func() { close(hang); slow.Close() }()
	t.Setenv("ROGER_BROKER", slow.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- AnswerWork(ctx, st, "s", towerhub.PullAnswer{ID: "pw"}) }()
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("an answer post outlived the Tower's context")
	}
}
