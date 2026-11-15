package towerjoin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

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
		_, ok := protocol.VerifyRequest(r.Header.Get(protocol.HeaderPubkey), r.Header.Get(protocol.HeaderSig), tsv, r.Method, r.URL.Path, body)
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
	require.NoError(t, AnswerWork(st, "sess-1", towerhub.PullAnswer{ID: "pw-1", Status: 200}))
	towerID, _ := CoreTowerID(st)
	require.Equal(t, "/tower/work/answer", answerPath)
	require.Equal(t, "tower_id="+towerID, answerQuery)
	require.Equal(t, towerID, answered.TowerID)
	require.Equal(t, "sess-1", answered.SessionID)
}

func TestPullAndAnswerNeedARegisteredTower(t *testing.T) {
	st := joinedTower(t)
	_, err := PullWork(context.Background(), st, "s")
	require.Error(t, err)
	require.Error(t, AnswerWork(st, "s", towerhub.PullAnswer{}))
}
