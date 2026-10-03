package main

// Found preparing the Pi test (2026-10-02): nothing refreshed a revocation list after enrollment, so
// every machine read "stale" a day after it joined, and a member never learned of a device revoked
// later (enrollment.feature: "a peer that has the current revocation list"). A running roger now
// refreshes it on its regular pass.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
)

func TestTheAuthorityKeepsItsOwnRevocationListFresh(t *testing.T) {
	newFacedAuthority(t, "desk")
	st := edgeIdentityStore()
	now := time.Now()
	require.NoError(t, st.SaveTrust([]string{"s1"}, now.Add(-48*time.Hour)))
	require.Contains(t, edgeTrustNote(now), "stale")
	require.Empty(t, edgeRefreshTrustNow(now))
	require.Empty(t, edgeTrustNote(now), "the authority's own list is the source: fresh after a pass")
}

func TestAMemberRefreshesItsListFromItsAuthorityAndNeverLosesARevocation(t *testing.T) {
	newFacedAuthority(t, "desk")
	served := []string{"s2", "s3"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != enrollhttp.RevocationsPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(served)
	}))
	defer srv.Close()
	// This machine is a MEMBER of the Edge at srv, not its authority.
	edgeAsMemberOf(t, strings.TrimPrefix(srv.URL, "http://"))
	st := edgeIdentityStore()
	now := time.Now()
	require.NoError(t, st.SaveTrust([]string{"s1"}, now.Add(-48*time.Hour)))

	// This authority answers only the unsigned list (one from before signing): it is taken, add-only,
	// and the member says the list was unsigned (revocation.feature).
	require.Contains(t, edgeRefreshTrustNow(now), "unsigned")
	tr, err := st.LoadTrust()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"s1", "s2", "s3"}, tr.Revoked, "what the authority added is learned")
	require.Empty(t, edgeTrustNote(now))

	// An answer missing a serial (forged, or a broken authority) never un-revokes it.
	served = []string{}
	require.Contains(t, edgeRefreshTrustNow(now.Add(time.Hour)), "unsigned")
	tr, _ = st.LoadTrust()
	require.ElementsMatch(t, []string{"s1", "s2", "s3"}, tr.Revoked, "a revocation is never lost")

	// An authority that does not answer leaves the list and its age as they were, and says so.
	srv.Close()
	warn := edgeRefreshTrustNow(now.Add(30 * time.Hour))
	require.Contains(t, warn, "could not refresh the revocation list")
	require.Contains(t, edgeTrustNote(now.Add(30*time.Hour)), "stale", "the warning stands")
}

// The host refreshes at most every so often, not on every discovery pass.
func TestTheRefreshIsPacedNotPerPass(t *testing.T) {
	h := &edgeHost{}
	t0 := time.Now()
	require.True(t, h.trustDue(t0))
	h.trustAt = t0
	require.False(t, h.trustDue(t0.Add(time.Minute)))
	require.True(t, h.trustDue(t0.Add(edgeTrustEvery)))
}

var _ = edgeauth.FreshnessWindow

// edgeAsMemberOf makes this (enrolled) test machine a member of the Edge whose authority answers at
// hostport, rather than its authority: no root key here, and a descriptor naming where it enrolled.
func edgeAsMemberOf(t *testing.T, hostport string) {
	t.Helper()
	require.NoError(t, os.RemoveAll(filepath.Join(edgeAuthDir(), edgeauth.AuthorityDir)))
	d, _, err := edgeIdentityStore().Descriptor()
	require.NoError(t, err)
	d.Kind, d.Where = edgeauth.KindLocal, hostport
	require.NoError(t, edgeIdentityStore().SaveDescriptor(d))
}

// features/edge/revocation.feature: a member uses a signed list only when the root it enrolled under
// signed it; a list signed by any other root is refused whole, and the member's list stays as it was.
func TestAMemberUsesASignedListOnlyWhenItsOwnRootSignedIt(t *testing.T) {
	newFacedAuthority(t, "desk")
	own, isAuth, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, isAuth)
	other, err := edgeauth.Designate(t.TempDir(), "elsewhere")
	require.NoError(t, err)
	serveSigned := func(l *edgeauth.Local, serials ...string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != enrollhttp.SignedRevocationsPath {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			signed, err := l.SignRevocationList(serials, time.Now())
			require.NoError(t, err)
			_ = json.NewEncoder(w).Encode(signed)
		}))
	}
	ownSrv := serveSigned(own, "0a0a")
	defer ownSrv.Close()
	otherSrv := serveSigned(other, "0b0b")
	defer otherSrv.Close()

	edgeAsMemberOf(t, strings.TrimPrefix(ownSrv.URL, "http://"))
	st := edgeIdentityStore()
	now := time.Now()
	require.NoError(t, st.SaveTrust([]string{"s1"}, now.Add(-48*time.Hour)))
	require.Empty(t, edgeRefreshTrustNow(now), "a signed list from its own root, verified: nothing to say")
	tr, _ := st.LoadTrust()
	require.ElementsMatch(t, []string{"s1", "0a0a"}, tr.Revoked)

	// The same address now answers a list signed by a different root: refused whole.
	edgeAsMemberOf(t, strings.TrimPrefix(otherSrv.URL, "http://"))
	warn := edgeRefreshTrustNow(now.Add(time.Hour))
	require.Contains(t, warn, "signature did not verify")
	tr, _ = st.LoadTrust()
	require.ElementsMatch(t, []string{"s1", "0a0a"}, tr.Revoked, "not one serial of a forged list is used")
	require.Equal(t, now.Unix(), tr.RefreshedAt, "and its age is not reset by it")
}
