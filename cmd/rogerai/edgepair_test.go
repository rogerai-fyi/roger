package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/ownerjoin"
	"rogerai.fm/roger/v6/internal/tower"
)

// servingAuthority is this machine as an Edge authority "shed", serving enrollment and pairing.
func servingAuthority(t *testing.T) (*edgeHost, string) {
	t.Helper()
	offline(t)
	t.Setenv("ROGERAI_EDGE_DISCOVERY", "0")
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	h, err := newEdgeHost("shed")
	require.NoError(t, err)
	h.startAuthority()
	t.Cleanup(h.closeListeners)
	_, port, err := net.SplitHostPort(h.authorityAddr())
	require.NoError(t, err)
	return h, "127.0.0.1:" + port
}

// askEdge runs `roger join <addr> workshop` as a second machine until it shows its phrase.
func askEdge(t *testing.T, addr string) (*liveBuf, chan error) {
	t.Helper()
	asSecondMachine(t)
	out, done := &liveBuf{}, make(chan error, 1)
	go func() { done <- joinPairAt(out, addr, "workshop", false) }()
	require.Eventually(t, func() bool { return clientPhrase(out.String()) != "" }, 5*time.Second, 10*time.Millisecond, out.String())
	return out, done
}

// A device in the middle answers with its own root: the two phrases differ, and if the owner
// approves without comparing, the device still keeps nothing, because it enrolls only under the
// root its phrase covered.
func TestAMiddleRootMakesThePhrasesDifferAndTheDeviceKeepsNothing(t *testing.T) {
	h, addr := servingAuthority(t)
	middle, err := edgeauth.Designate(t.TempDir(), "middle")
	require.NoError(t, err)
	real, err := url.Parse("http://" + addr)
	require.NoError(t, err)
	fwd := httputil.NewSingleHostReverseProxy(real)
	mitm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == enrollhttp.RootPath {
			_, _ = w.Write([]byte(middle.RootPEM()))
			return
		}
		fwd.ServeHTTP(w, r)
	}))
	defer mitm.Close()

	out, done := askEdge(t, strings.TrimPrefix(mitm.URL, "http://"))
	reqs := h.pairing().PairRequests()
	require.Len(t, reqs, 1)
	require.NotEqual(t, reqs[0].Phrase, clientPhrase(out.String()), "the owner's phrase is over the real root")

	require.NoError(t, h.pairing().ApprovePair(reqs[0].ID))
	select {
	case err := <-done:
		require.Error(t, err, "an enrollment under a root the phrase did not cover is refused")
	case <-time.After(10 * time.Second):
		t.Fatal("roger join never finished")
	}
	_, _, kept, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.False(t, kept, "nothing is kept")
}

func TestARefusedEdgeRequestEnrollsNothing(t *testing.T) {
	h, addr := servingAuthority(t)
	_, done := askEdge(t, addr)
	o := h.ownerJoin(&ownerjoin.Hooks{})
	e, ok := o.Edge()
	require.True(t, ok)
	require.Equal(t, "shed", e.Label)
	reqs, err := o.Requests(e)
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	require.NoError(t, o.Decide(e.Dir, reqs[0].ID, false))
	err = <-done
	require.ErrorContains(t, err, "shed refused the request")
	_, _, kept, _ := edgeIdentityStore().LoadIdentity()
	require.False(t, kept)
}

func TestTheOwnersTowersAreStillAnsweredBesideTheEdge(t *testing.T) {
	var asked, decided []string
	o := &ownerjoin.Hooks{
		Requests: func(t ownerjoin.Tower) ([]tower.PairRequest, error) { asked = append(asked, t.Dir); return nil, nil },
		Decide:   func(dir, id string, approve bool) error { decided = append(decided, dir+"/"+id); return nil },
	}
	h := &edgeHost{}
	w := h.ownerJoin(o)
	_, ok := w.Edge()
	require.False(t, ok, "no Edge authority is serving here")
	_, err := w.Requests(ownerjoin.Tower{Dir: "/srv/lab"})
	require.NoError(t, err)
	require.NoError(t, w.Decide("/srv/lab", "r1", true))
	require.Equal(t, []string{"/srv/lab"}, asked)
	require.Equal(t, []string{"/srv/lab/r1"}, decided)
	_, err = w.Requests(ownerjoin.Tower{Dir: edgeOwnerDir})
	require.ErrorIs(t, err, errEdgeNotServing)
	require.ErrorIs(t, w.Decide(edgeOwnerDir, "r1", true), errEdgeNotServing)
}

func TestAStationCannotAskAnEdge(t *testing.T) {
	_, addr := servingAuthority(t)
	err := joinPairAt(&liveBuf{}, addr, "kitchen-pi", true)
	require.ErrorContains(t, err, "a station joins a Tower")
	require.Equal(t, 2, exitCode(err))
}
