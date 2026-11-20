package towerhub

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// features/tower/tower_pull_dispatch.feature: only the two requests Core makes of a hub can be
// pulled, only allow-listed headers cross, and nothing a pull carries can crash the Tower.
func TestOnlyTheTwoHubRequestsArePulled(t *testing.T) {
	for _, tc := range []struct {
		w  PullWork
		ok bool
	}{
		{PullWork{Method: http.MethodPost, Path: PathSubmit}, true},
		{PullWork{Method: http.MethodPost, Path: PathSubmit, Query: "x=1"}, false},
		{PullWork{Method: http.MethodGet, Path: PathPoll, Query: "nonce=ab"}, true},
		{PullWork{Method: http.MethodGet, Path: PathPoll, Query: "nonce=ab&station=s"}, false},
		{PullWork{Method: http.MethodGet, Path: PathPoll, Query: "nonce=ab", Body: []byte("x")}, false},
		{PullWork{Method: http.MethodGet, Path: PathPoll, Query: "%zz"}, false},
		{PullWork{Method: http.MethodPost, Path: PathComplete}, false},
		{PullWork{Method: http.MethodGet, Path: PathSubmit}, false},
	} {
		require.Equal(t, tc.ok, PullAllowed(tc.w), "%+v", tc.w)
	}
}

func TestServePulledRunsAgainstTheHubAndCapsTheAnswer(t *testing.T) {
	hub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "pulled", r.RemoteAddr)
		require.Empty(t, r.Header.Get("X-Other"), "only allow-listed headers cross")
		w.Header().Set(HubProofHeader, "p")
		w.Header().Set("X-Secret", "never crosses")
		_, _ = w.Write([]byte("answer"))
	})
	a := ServePulled(context.Background(), hub, PullWork{ID: "1", Method: http.MethodPost, Path: PathSubmit,
		Header: map[string]string{"Content-Type": "application/json", "X-Other": "no"}}, 1<<10)
	require.Equal(t, http.StatusOK, a.Status)
	require.Equal(t, "answer", string(a.Body))
	require.Equal(t, "p", a.Header[HubProofHeader])
	require.NotContains(t, a.Header, "X-Secret", "only allow-listed headers cross")
	a = ServePulled(context.Background(), hub, PullWork{Method: http.MethodPost, Path: PathSubmit}, 3)
	require.Equal(t, http.StatusBadGateway, a.Status, "an answer over the cap is a failure, not a truncation")
	a = ServePulled(context.Background(), hub, PullWork{Method: http.MethodPost, Path: PathComplete}, 1<<10)
	require.Equal(t, http.StatusForbidden, a.Status)
	// A method that does not parse is answered here and never reaches the hub - no panic.
	a = ServePulled(context.Background(), hub, PullWork{Method: "BAD METHOD", Path: PathSubmit}, 1<<10)
	require.NotEqual(t, http.StatusOK, a.Status)
	require.Equal(t, http.Header{"Content-Type": {"x"}}, HeaderOf(map[string]string{"Content-Type": "x", "X-No": "y"}))
}
