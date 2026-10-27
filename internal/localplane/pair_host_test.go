package localplane

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/tower"
)

// An Edge authority approves a device the same way (approve.feature:49): the same pairing, over
// the key the Edge presents (its root), and an approval does what the Edge says it does.
func TestPairingForAnEdgeCoversItsKeyAndAdmitsOnlyOnApproval(t *testing.T) {
	var admitted []string
	fail := false
	srv := NewPairing("shed", func() (string, error) { return "the-edge-root", nil },
		func(r tower.PairRequest, keyHash, pubHex string) (JoinAnswer, error) {
			if fail {
				return JoinAnswer{}, errors.New("the allow-list could not be written")
			}
			admitted = append(admitted, r.Name+" "+pubHex)
			return JoinAnswer{Joined: "edge", Tower: "shed"}, nil
		})
	srv.SetPairPoll(50 * time.Millisecond)

	c := newPairClient(t)
	require.Equal(t, http.StatusOK, c.begin(t, srv, "client", "workshop", nil, "").Code)
	require.Equal(t, "shed", c.start.Tower)
	require.Equal(t, http.StatusOK, c.reveal(srv, c.nonce).Code)
	reqs := srv.PairRequests()
	require.Len(t, reqs, 1)
	require.Equal(t, c.phrase("the-edge-root"), reqs[0].Phrase, "the phrase covers the key the Edge presents")
	require.Empty(t, admitted, "nothing is admitted before the owner approves")

	fail = true
	require.ErrorContains(t, srv.ApprovePair(reqs[0].ID), "allow-list")
	require.Len(t, srv.PairRequests(), 1, "an approval that could not be kept leaves the request waiting")
	require.Equal(t, "pending", c.wait(srv).Status)

	fail = false
	require.NoError(t, srv.ApprovePair(reqs[0].ID))
	a := c.wait(srv)
	require.Equal(t, "approved", a.Status)
	require.Equal(t, JoinAnswer{Joined: "edge", Tower: "shed"}, a.Answer)
	require.Equal(t, []string{"workshop " + c.pub()}, admitted)
}

func TestPairingForAnEdgeServesNothingButThePairing(t *testing.T) {
	srv := NewPairing("shed", func() (string, error) { return "k", nil },
		func(tower.PairRequest, string, string) (JoinAnswer, error) { return JoinAnswer{}, nil })
	for _, p := range []string{"/discover", "/v1/models", "/v1/chat/completions", "/local/poll", "/local/join"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, p)
	}
	// A refusal admits nothing.
	c := newPairClient(t)
	c.begin(t, srv, "client", "workshop", nil, "")
	c.reveal(srv, c.nonce)
	require.NoError(t, srv.RefusePair(srv.PairRequests()[0].ID))
}

func TestPairingForAnEdgeWhoseKeyCannotBeReadShowsNothing(t *testing.T) {
	srv := NewPairing("shed", func() (string, error) { return "", errors.New("no root") },
		func(tower.PairRequest, string, string) (JoinAnswer, error) { return JoinAnswer{}, nil })
	c := newPairClient(t)
	c.begin(t, srv, "client", "workshop", nil, "")
	require.Equal(t, http.StatusUnauthorized, c.reveal(srv, c.nonce).Code)
	require.Empty(t, srv.PairRequests())
}
