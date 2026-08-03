package tower

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// THE PHASE 1 GATE.
//
// The claim that closes Phase 1 is: "a default standalone Tower makes no RogerAI network
// connection and exposes no public advertisement or RogerAI settlement route." A design
// that merely omits the code to dial RogerAI is not a proof - the proof has to be that an
// attempt to reach anything outside the declared private allowlist is REFUSED, and that
// running a full local flow performs no outbound dial at all.
//
// Contract: features/tower/modes.feature, the standalone-isolation scenarios.

func TestAllowlistPermitsOnlyPrivateDestinations(t *testing.T) {
	g := NewEgressGuard(nil) // default: loopback + RFC1918 + link-local-free
	for _, ok := range []string{
		"127.0.0.1:5432", "[::1]:6379", "10.1.2.3:5432", "172.16.5.4:5432", "192.168.1.9:5432",
	} {
		require.NoError(t, g.Allow(ok), "%s is a private destination", ok)
	}
}

// The destinations that matter: RogerAI itself, the public Internet, and cloud
// instance-metadata - the classic SSRF pivot.
func TestAllowlistRefusesEveryPublicDestination(t *testing.T) {
	g := NewEgressGuard(nil)
	for name, addr := range map[string]string{
		"RogerAI broker":    "broker.rogerai.fm:443",
		"public IPv4":       "93.184.216.34:443",
		"public IPv6":       "[2606:2800:220:1:248:1893:25c8:1946]:443",
		"instance metadata": "169.254.169.254:80",
		"metadata by name":  "metadata.google.internal:80",
		"public hostname":   "example.com:443",
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, g.Allow(addr), "%s must be refused", addr)
		})
	}
}

// A hostname is refused outright rather than resolved. Resolving it would already be a
// DNS lookup - the exact thing the gate says must not happen - and would open the door
// to rebinding, where a name resolves to an allowed IP once and a forbidden one later.
func TestHostnamesAreRefusedWithoutResolving(t *testing.T) {
	g := NewEgressGuard(nil)
	for _, host := range []string{
		"broker.rogerai.fm:443", "localhost:5432", "db.internal:5432", "anything.invalid:1",
	} {
		err := g.Allow(host)
		require.Error(t, err, "%s must be refused without a DNS lookup", host)
		require.Contains(t, err.Error(), "literal IP")
	}
}

func TestAnOperatorCanDeclareTheirOwnPrivateCIDR(t *testing.T) {
	_, cidr, err := net.ParseCIDR("100.64.0.0/10") // a CGNAT / tailnet range
	require.NoError(t, err)
	g := NewEgressGuard([]*net.IPNet{cidr})

	require.NoError(t, g.Allow("100.64.1.2:5432"), "a declared range is allowed")
	require.Error(t, g.Allow("10.0.0.1:5432"), "declaring a range REPLACES the default, it does not add to it")
	require.Error(t, g.Allow("93.184.216.34:443"))
}

func TestMalformedDestinationsAreRefused(t *testing.T) {
	g := NewEgressGuard(nil)
	for _, bad := range []string{"", "notanaddress", "10.0.0.1", ":443", "10.0.0.1:notaport"} {
		require.Error(t, g.Allow(bad), "%q must be refused", bad)
	}
}

// v1 never fetches a target supplied by a request. A Tower that fetched caller-supplied
// URLs would be an egress proxy no matter how good its allowlist was, because the caller
// chooses the destination.
func TestRequestSuppliedTargetsAreNeverFetched(t *testing.T) {
	g := NewEgressGuard(nil)
	for _, target := range []string{
		"https://broker.rogerai.fm/v1/models",
		"http://example.com/",
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:7071/admin",
		"http://10.0.0.5/",
	} {
		err := g.AllowRequestTarget(target)
		require.Error(t, err, "%s must not be fetched", target)
		require.Contains(t, err.Error(), "does not fetch")
	}
}

// --- the gate itself -------------------------------------------------------

// TestStandaloneHasNoOutboundNetworkCallAtAll is the Phase 1 gate.
//
// It is a SOURCE-level assertion rather than a runtime one, deliberately. A runtime
// "did we dial?" test can only observe calls that go through a seam this package
// controls, so it would pass happily while someone added a raw net.Dial or http.Get
// beside it. Reading the source catches that: if any non-test file in this package
// acquires the ability to reach the network, this fails.
//
// egress.go itself is exempt because it is the allowlist, and it makes no call - it
// only parses and compares addresses.
func TestStandaloneHasNoOutboundNetworkCallAtAll(t *testing.T) {
	forbidden := []string{
		"net.Dial", "net.DialTimeout", "net.Listen",
		"http.Get", "http.Post", "http.Head", "http.NewRequest", "http.Client",
		"net.LookupHost", "net.LookupIP", "net.LookupAddr", "net.Resolver",
		"exec.Command",
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "egress.go" {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		src := string(b)
		for _, bad := range forbidden {
			require.NotContains(t, src, bad,
				"%s uses %s: a standalone Tower must make no outbound network call, "+
					"and any new outbound path needs its own approved spec first", f, bad)
		}
		checked++
	}
	require.Greater(t, checked, 3, "the scan must actually have read the package")
}

// The runtime half of the same claim: a full local flow - init, invite, admit, attach,
// route - completes end to end. Combined with the source scan above, that is the proof
// the gate asks for: the whole standalone story works, and nothing in it can reach out.
func TestStandaloneCompletesAFullLocalFlow(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)

	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err)

	_, err = st.AttachStation("st-1", "station-key", []string{"llama-8b"})
	require.NoError(t, err)

	receipt, err := st.Route(testClientKey, "llama-8b")
	require.NoError(t, err)
	require.Equal(t, "st-1", receipt.StationID)
	require.Equal(t, st.LocalNetworkID, receipt.NetworkID)
}

// The same guarantee stated the other way: a default standalone configuration has no
// public authority to dial and cannot advertise, so there is nothing to reach RogerAI
// with even if something tried.
func TestDefaultStandaloneConfigHasNothingToDial(t *testing.T) {
	c, err := ParseConfig([]byte(minimalStandalone))
	require.NoError(t, err)
	require.Empty(t, c.PublicAuthority())
	require.False(t, c.AdvertisesPublicly())
	require.False(t, Doctor(c).ReachesPublicNetwork)
}
