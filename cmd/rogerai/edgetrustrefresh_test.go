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

	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/edgeauth/enrollhttp"
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

// revocationServer answers like an authority: the signed list at the signed path, signed by l at
// *at with *serials, and (when plain is set) an unsigned list at the plain path.
func revocationServer(t *testing.T, l *edgeauth.Local, at *time.Time, serials *[]string, plain bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == enrollhttp.SignedRevocationsPath && l != nil:
			signed, err := l.SignRevocationList(*serials, *at)
			require.NoError(t, err)
			_ = json.NewEncoder(w).Encode(signed)
		case r.URL.Path == enrollhttp.RevocationsPath && plain:
			_ = json.NewEncoder(w).Encode(*serials)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAMemberRefreshesItsListFromItsAuthorityAndNeverLosesARevocation(t *testing.T) {
	newFacedAuthority(t, "desk")
	own, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	now := time.Now().Truncate(time.Second)
	at, served := now, []string{"s2", "s3"}
	srv := revocationServer(t, own, &at, &served, false)
	edgeAsMemberOf(t, strings.TrimPrefix(srv.URL, "http://"))
	st := edgeIdentityStore()
	require.NoError(t, st.SaveTrust([]string{"s1"}, now.Add(-48*time.Hour)))

	require.Empty(t, edgeRefreshTrustNow(now))
	tr, err := st.LoadTrust()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"s1", "s2", "s3"}, tr.Revoked, "what the authority added is learned")
	require.Empty(t, edgeTrustNote(now))

	// A signed answer missing a serial (a broken authority) never un-revokes it.
	at, served = now.Add(time.Hour), []string{}
	require.Empty(t, edgeRefreshTrustNow(now.Add(time.Hour)))
	tr, _ = st.LoadTrust()
	require.ElementsMatch(t, []string{"s1", "s2", "s3"}, tr.Revoked, "a revocation is never lost")

	// An authority that does not answer leaves the list and its age as they were, and says so.
	srv.Close()
	warn := edgeRefreshTrustNow(now.Add(30 * time.Hour))
	require.Contains(t, warn, "could not refresh the revocation list")
	require.Contains(t, edgeTrustNote(now.Add(30*time.Hour)), "stale", "the warning stands")
}

// revocation.feature (amended 2026-10-26): "a member never uses an unsigned revocation list". A 404 on
// the signed list used to drop to the plain list, so whoever answered in the authority's place could
// revoke every good device.
func TestAMemberNeverUsesAnUnsignedList(t *testing.T) {
	newFacedAuthority(t, "desk")
	now := time.Now().Truncate(time.Second)
	at, served := now, []string{"s2", "s3"}
	srv := revocationServer(t, nil, &at, &served, true)
	edgeAsMemberOf(t, strings.TrimPrefix(srv.URL, "http://"))
	st := edgeIdentityStore()
	require.NoError(t, st.SaveTrust([]string{"s1"}, now.Add(-time.Hour)))

	warn := edgeRefreshTrustNow(now)
	require.Contains(t, warn, "could not be checked")
	require.Contains(t, warn, "may be stale")
	tr, _ := st.LoadTrust()
	require.Equal(t, []string{"s1"}, tr.Revoked, "not one serial of an unsigned list is used")
	require.Equal(t, now.Add(-time.Hour).Unix(), tr.RefreshedAt, "and its age is not reset")

	// The same holds where a list is first fetched, at enrollment.
	_, err := edgeRefreshTrust(st, srv.URL, nil, false, now)
	require.NoError(t, err)
	tr, _ = st.LoadTrust()
	require.Equal(t, []string{"s1"}, tr.Revoked, "enrollment takes no unsigned list either")
}

// revocation.feature: "a list signed earlier than the last one the member took is refused", and "a
// member's list is as fresh as when it was signed, not when it was fetched".
func TestAMemberRefusesAnOlderSignedListAndCountsFreshnessFromSigning(t *testing.T) {
	newFacedAuthority(t, "desk")
	own, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	ten := time.Now().Truncate(time.Second).Add(-20 * time.Minute)
	at, served := ten, []string{"0a0a"}
	srv := revocationServer(t, own, &at, &served, false)
	edgeAsMemberOf(t, strings.TrimPrefix(srv.URL, "http://"))
	st := edgeIdentityStore()
	now := ten.Add(20 * time.Minute)

	require.Empty(t, edgeRefreshTrustNow(now))
	tr, _ := st.LoadTrust()
	require.Equal(t, []string{"0a0a"}, tr.Revoked)
	require.Equal(t, 20*time.Minute, tr.Age(now), "fresh from the signing, not the fetch")
	require.Equal(t, ten.Unix(), tr.SignedAt)

	// A captured list, validly signed an hour earlier, replayed with a serial of its own.
	at, served = ten.Add(-time.Hour), []string{"0a0a", "0c0c"}
	warn := edgeRefreshTrustNow(now.Add(time.Minute))
	require.Contains(t, warn, "older")
	tr, _ = st.LoadTrust()
	require.Equal(t, []string{"0a0a"}, tr.Revoked, "not one serial of an older list is used")
	require.Equal(t, ten.Unix(), tr.RefreshedAt, "its freshness stays as it was")

	// The same list again is taken, and changes nothing.
	at, served = ten, []string{"0a0a"}
	require.Empty(t, edgeRefreshTrustNow(now.Add(2*time.Minute)))
	tr, _ = st.LoadTrust()
	require.Equal(t, []string{"0a0a"}, tr.Revoked)
	require.Equal(t, ten.Unix(), tr.SignedAt)

	// A local revoke keeps the signing time it was refreshed at.
	require.NoError(t, st.Revoke("0d0d", now))
	tr, _ = st.LoadTrust()
	require.Equal(t, ten.Unix(), tr.SignedAt, "a local revoke does not forget the last signed list")
}

// revocation.feature: "a list signed in the future is refused".
func TestAMemberRefusesAListSignedInTheFuture(t *testing.T) {
	newFacedAuthority(t, "desk")
	own, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	now := time.Now().Truncate(time.Second)
	at, served := now.Add(10*time.Minute), []string{"0a0a"}
	srv := revocationServer(t, own, &at, &served, false)
	edgeAsMemberOf(t, strings.TrimPrefix(srv.URL, "http://"))
	st := edgeIdentityStore()
	require.NoError(t, st.SaveTrust([]string{"s1"}, now.Add(-time.Hour)))

	warn := edgeRefreshTrustNow(now)
	require.Contains(t, warn, "clock")
	tr, _ := st.LoadTrust()
	require.Equal(t, []string{"s1"}, tr.Revoked)

	// Inside the allowance (a few seconds of clock difference) it is taken.
	at = now.Add(30 * time.Second)
	require.Empty(t, edgeRefreshTrustNow(now))
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
	signedAt := time.Now().Truncate(time.Second)
	serveSigned := func(l *edgeauth.Local, serials ...string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != enrollhttp.SignedRevocationsPath {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			signed, err := l.SignRevocationList(serials, signedAt)
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
	require.Equal(t, signedAt.Unix(), tr.RefreshedAt, "and its age is not reset by it")
}
