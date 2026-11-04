package main

// Unit tests for clientaddr.go. The behaviour is specified, example by example, in
// features/security/client_address_trust.feature (client_address_bdd_test.go); these pin the
// pieces the spec leans on and give the rest of the suite one way to say who a caller is.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// setCallerIP makes r arrive straight from ip, with no proxy in between. It is how a test says
// who the caller is: a forwarding header from a direct caller is never believed.
func setCallerIP(r *http.Request, ip string) { r.RemoteAddr = net.JoinHostPort(ip, "40000") }

// isolateAddrTrust keeps a test that loads settings from the environment (runServe does) from
// picking up the developer's own and from leaking what it installs into later tests.
func isolateAddrTrust(t *testing.T) {
	t.Helper()
	found := clientAddrTrust.Load()
	t.Setenv("ROGERAI_TRUSTED_PROXIES", "")
	t.Setenv("ROGERAI_COUNTRY_HEADER", "")
	t.Cleanup(func() { clientAddrTrust.Store(found) })
}

// withAddrTrust puts a trust setting in force for one test and restores the one it found.
func withAddrTrust(t *testing.T, proxies, country string) {
	t.Helper()
	tr, err := parseAddrTrust(proxies, country)
	require.NoError(t, err)
	found := clientAddrTrust.Load()
	clientAddrTrust.Store(tr)
	t.Cleanup(func() { clientAddrTrust.Store(found) })
}

// Until a proxy is named no peer is trusted, the loopback and private ones included, both
// before startup installs a setting and after it installs a blank one.
func TestNoPeerIsTrustedUntilAProxyIsNamed(t *testing.T) {
	isolateAddrTrust(t)
	clientAddrTrust.Store(nil)
	blank, err := parseAddrTrust("", "")
	require.NoError(t, err)
	for _, tr := range []*addrTrust{currentAddrTrust(), blank} {
		for _, a := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.0.1",
			"fd00::1", "169.254.169.254", "100.64.0.1", "203.0.113.1", "2001:db8::1"} {
			require.False(t, tr.trusts(netip.MustParseAddr(a)), a)
		}
		require.Empty(t, tr.countryHeader)
	}
}

// Only the entries right of the caller's are ever parsed, so a caller that writes a megabyte of
// junk in front of the proxy's entry changes nothing.
func TestForwardedCallerReadsOnlyFromTheRight(t *testing.T) {
	withAddrTrust(t, "10.0.0.1", "")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:40000"
	r.Header.Set("X-Forwarded-For", strings.Repeat("junk, ", 1<<17)+"203.0.113.7")
	require.Equal(t, "203.0.113.7", clientIP(r))
}

// A trust setting the broker cannot read stops runServe before it serves anything, and the
// setting already in force is left alone.
func TestRunServeRefusesAnUnreadableTrustSetting(t *testing.T) {
	withAddrTrust(t, "10.0.0.1", "")
	before := clientAddrTrust.Load()
	t.Setenv("ROGERAI_TRUSTED_PROXIES", "10.0.0.1, proxy.internal")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	err = runServe(ln, defaultFeeRate, 0, time.Hour, nil)
	require.ErrorContains(t, err, "proxy.internal")
	require.Same(t, before, clientAddrTrust.Load(), "a refused setting must not replace the one in force")
}
