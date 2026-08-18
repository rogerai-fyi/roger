package towerhub

// The hub client's redirect policy.
//
// Every broker call a node makes passes protocol.NoDowngradeRedirect; this Client set no
// CheckRedirect at all, so Go's default applied. That was a worse gap here than on a broker call:
// the party answering is explicitly untrusted - the sealed envelope exists because the tower is
// not to be believed - and a tower that answered a poll with a 302 could name where the node
// sent its credentials next.
//
// THE TOKEN IS GONE AND THE POLICY IS NOT REDUNDANT. A request signature binds the method, the
// target and the body; it does not bind the HOST. So a followed redirect would hand a chosen
// third party a live, unexpired signature to present to the real hub - the capture the whole
// scheme exists to prevent, delivered rather than intercepted.
//
// The policy here is stricter than the broker one: a redirect is refused outright rather than
// merely checked for a plaintext downgrade, because a hub has no legitimate reason to redirect
// (Core hands the node its endpoint) and "not a downgrade" still lets the relay name any https
// host it likes.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
)

// A hub that tries to bounce a node's authenticated poll somewhere else does not get to.
func TestPollDoesNotFollowAHubsRedirect(t *testing.T) {
	var elsewhereSaw atomic.Int64
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(protocol.HeaderSig) != "" || r.Header.Get("Authorization") != "" {
			elsewhereSaw.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer elsewhere.Close()

	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+PathPoll, http.StatusFound)
	}))
	defer hub.Close()

	c := newTestNode(t).client(hub.URL, 0)
	_, ok, err := c.PollJob(context.Background(), "st-1")
	require.Error(t, err, "the client followed a hostile hub's redirect")
	require.False(t, ok)
	require.Zero(t, elsewhereSaw.Load(), "the node's signed request was carried to a host the hub chose")
}

// The policy is installed even when the caller supplied its own http.Client (which every real
// caller does, for the long-poll timeout) - and without mutating that client, since the poll
// workers and the audit loop share one.
func TestTheCallersClientStillGetsTheRedirectPolicy(t *testing.T) {
	supplied := &http.Client{}
	c := &Client{BaseURL: "http://127.0.0.1:1", HTTP: supplied}
	require.NotNil(t, c.httpClient().CheckRedirect)
	require.Nil(t, supplied.CheckRedirect, "the shared client was mutated - that is a data race between workers")
	require.Same(t, c.httpClient(), c.httpClient(), "the effective client is built once")
}
