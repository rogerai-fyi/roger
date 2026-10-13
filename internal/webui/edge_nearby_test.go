package webui

// The NEARBY endpoints' refusals (features/edge/empty_edge_simple.feature §7): what the console is
// told when a build wired no hook, a device is not nearby, or the authority is not answering.

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/look"
	"rogerai.fm/roger/v6/internal/ownerjoin"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/tower"
)

var errFake = errors.New("the host said no")

func nearbyServer(t *testing.T, h EdgeHooks, owner *ownerjoin.Hooks) (*httptest.Server, string) {
	t.Helper()
	h.Fleet = edge.NewFleet(store.NewMem(), "acct-1")
	srv := New(nil, Options{Edge: h, Owner: owner})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv.Token()
}

func nearbyCall(t *testing.T, ts *httptest.Server, tok, method, path, body string) (int, string) {
	t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	req, err := http.NewRequest(method, ts.URL+path+sep+"t="+tok, bytes.NewBufferString(body))
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

func TestNearbyEndpointsRefuse(t *testing.T) {
	phone := store.EdgeNode{ID: "n_phone", Name: "gentle-ibex-14", Kind: "mobile", LastSeen: time.Now().Add(-20 * time.Minute).Unix()}
	desk := store.EdgeNode{ID: "n_desk", Name: "deskside", Kind: "host", Pin: strings.Repeat("ab", 32)}
	cands := func() []store.EdgeNode { return []store.EdgeNode{phone, desk} }

	// No hooks wired: Invite and Approve say so, and the snapshot says neither button works.
	ts, tok := nearbyServer(t, EdgeHooks{Candidates: cands}, nil)
	code, body := nearbyCall(t, ts, tok, "POST", "/api/edge/nearby/invite", `{"id":"n_phone"}`)
	require.Equal(t, http.StatusNotImplemented, code, body)
	code, body = nearbyCall(t, ts, tok, "POST", "/api/edge/nearby/approve", `{"id":"n_phone"}`)
	require.Equal(t, http.StatusNotImplemented, code, body)
	code, body = nearbyCall(t, ts, tok, "POST", "/api/edge/nearby/approve", `{"id":"n_desk"}`)
	require.Equal(t, http.StatusConflict, code)
	require.Contains(t, body, look.NearbyNotServing)
	code, body = nearbyCall(t, ts, tok, "GET", "/api/edge", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"invite":false`)
	require.Contains(t, body, `"stale":true`) // the phone was last heard 20 minutes ago
	require.Contains(t, body, "last seen 20m ago")

	// Approve is POST-only; asked is a read and changes nothing.
	code, _ = nearbyCall(t, ts, tok, "GET", "/api/edge/nearby/approve?id=n_phone", "")
	require.Equal(t, http.StatusMethodNotAllowed, code)
	code, body = nearbyCall(t, ts, tok, "GET", "/api/edge/nearby/asked?id=n_phone", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, look.NearbyNoCert)

	// Nothing names a device that is not nearby, and a malformed body is refused.
	ts, tok = nearbyServer(t, EdgeHooks{Candidates: cands,
		InviteFor: func(store.EdgeNode) (string, time.Time, error) { return "", time.Time{}, errFake },
		Adopt:     func(string, string) error { return errFake },
	}, &ownerjoin.Hooks{
		Edge: func() (ownerjoin.Tower, bool) {
			return ownerjoin.Tower{Dir: "edge:authority", Addr: "192.168.1.5:8791"}, true
		},
		Requests: func(ownerjoin.Tower) ([]tower.PairRequest, error) { return nil, errFake },
	})
	for _, path := range []string{"/api/edge/nearby/invite", "/api/edge/nearby/approve"} {
		code, _ = nearbyCall(t, ts, tok, "POST", path, `{"id":"n_elsewhere"}`)
		require.Equal(t, http.StatusNotFound, code, path)
		code, _ = nearbyCall(t, ts, tok, "POST", path, `{`)
		require.Equal(t, http.StatusBadRequest, code, path)
	}
	code, _ = nearbyCall(t, ts, tok, "POST", "/api/edge/nearby/invite", `{}`)
	require.Equal(t, http.StatusBadRequest, code)
	// A hook that fails says so; it is not a 200 with nothing in it.
	code, body = nearbyCall(t, ts, tok, "POST", "/api/edge/nearby/invite", `{"id":"n_phone"}`)
	require.Equal(t, http.StatusBadGateway, code)
	require.Contains(t, body, errFake.Error())
	code, _ = nearbyCall(t, ts, tok, "POST", "/api/edge/nearby/approve", `{"id":"n_phone"}`)
	require.Equal(t, http.StatusBadGateway, code)
	// A requests read that fails is "still waiting", never a guessed phrase.
	code, body = nearbyCall(t, ts, tok, "GET", "/api/edge/nearby/asked?id=n_desk", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"state":"waiting"`)
}

// Two waiting requests with the asked name match neither: the console never guesses which phrase.
func TestNearbyAskedNeverGuesses(t *testing.T) {
	desk := store.EdgeNode{ID: "n_desk", Name: "deskside", Kind: "host", Pin: strings.Repeat("ab", 32)}
	ts, tok := nearbyServer(t, EdgeHooks{Candidates: func() []store.EdgeNode { return []store.EdgeNode{desk} }},
		&ownerjoin.Hooks{
			Edge: func() (ownerjoin.Tower, bool) {
				return ownerjoin.Tower{Dir: "edge:authority", Addr: "192.168.1.5:8791"}, true
			},
			Requests: func(ownerjoin.Tower) ([]tower.PairRequest, error) {
				return []tower.PairRequest{{ID: "r1", Name: "deskside", Phrase: "amber-river-71"}, {ID: "r2", Name: "deskside", Phrase: "quiet-otter-14"}}, nil
			},
		})
	code, body := nearbyCall(t, ts, tok, "GET", "/api/edge/nearby/asked?id=n_desk", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"state":"waiting"`)
	require.NotContains(t, body, "amber-river-71")
}
