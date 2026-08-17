package tower

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Phase 1.1 of the approved Tower plan: the mode and configuration core.
// Contract: features/tower/modes.feature.
//
// The single most important property here is that STANDALONE is structurally isolated
// from the public network - not isolated by a boolean someone can flip, but by the
// configuration for reaching RogerAI being un-expressible in standalone mode at all.

func TestModeAcceptsExactlyTwoValues(t *testing.T) {
	for _, in := range []string{"joined", "standalone"} {
		m, err := ParseMode(in)
		require.NoError(t, err)
		require.Equal(t, Mode(in), m)
	}
}

func TestModeRejectsEverythingElse(t *testing.T) {
	for _, in := range []string{"", "public", "private", "hybrid", "joined,standalone", "unknown", "JOINED", " joined"} {
		_, err := ParseMode(in)
		require.Error(t, err, "mode %q must be rejected", in)
	}
}

const minimalStandalone = `
apiVersion: tower.rogerai.fm/v1alpha1
kind: Tower
mode: standalone
`

const minimalJoined = `
apiVersion: tower.rogerai.fm/v1alpha1
kind: Tower
mode: joined
joined:
  authority: https://broker.rogerai.fm
  enrollmentTokenFile: /run/secrets/enrollment-token
`

func TestLoadsAMinimalConfigInEitherMode(t *testing.T) {
	for name, y := range map[string]string{"standalone": minimalStandalone, "joined": minimalJoined} {
		t.Run(name, func(t *testing.T) {
			c, err := ParseConfig([]byte(y))
			require.NoError(t, err)
			require.Equal(t, Mode(name), c.Mode)
		})
	}
}

func TestUnknownFieldFailsClosed(t *testing.T) {
	y := minimalStandalone + "\nnotAField: 1\n"
	_, err := ParseConfig([]byte(y))
	require.Error(t, err, "an unknown field must fail validation, not be ignored")
	require.Contains(t, strings.ToLower(err.Error()), "notafield")
}

func TestWrongApiVersionOrKindFailsClosed(t *testing.T) {
	for _, y := range []string{
		"apiVersion: other/v1\nkind: Tower\nmode: standalone\n",
		"apiVersion: tower.rogerai.fm/v1alpha1\nkind: Broker\nmode: standalone\n",
		"kind: Tower\nmode: standalone\n",
		"apiVersion: tower.rogerai.fm/v1alpha1\nmode: standalone\n",
	} {
		_, err := ParseConfig([]byte(y))
		require.Error(t, err, "config %q must be rejected", y)
	}
}

// The heart of the standalone isolation guarantee: every field that could point a
// standalone Tower at the public network is rejected outright.
func TestStandaloneRejectsEveryPublicNetworkField(t *testing.T) {
	for name, frag := range map[string]string{
		"public authority URL": "joined:\n  authority: https://broker.rogerai.fm\n",
		"enrollment token":     "joined:\n  enrollmentTokenFile: /run/secrets/tok\n",
		"joined certificate":   "joined:\n  certificateFile: /etc/tower/tls.crt\n",
		"public advertisement": "publicAdvertisement: true\n",
		"RogerAI payout":       "payout:\n  wallet: u_gh_1\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig([]byte(minimalStandalone + frag))
			require.Error(t, err, "standalone must not accept %s", name)
		})
	}
}

// The symmetric guarantee: a joined Tower cannot be configured with the local trust
// root and settlement authority that only a standalone network has.
func TestJoinedRejectsEveryStandaloneAuthorityField(t *testing.T) {
	for name, frag := range map[string]string{
		"local settlement signer": "standalone:\n  settlementSignerFile: /etc/tower/settle.key\n",
		"offline root":            "standalone:\n  offlineRootFile: /etc/tower/root.key\n",
		"local trust publication": "standalone:\n  trustPublicationFile: /etc/tower/trust.json\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig([]byte(minimalJoined + frag))
			require.Error(t, err, "joined must not accept %s", name)
		})
	}
}

func TestJoinedRequiresItsAuthorityAndToken(t *testing.T) {
	_, err := ParseConfig([]byte("apiVersion: tower.rogerai.fm/v1alpha1\nkind: Tower\nmode: joined\n"))
	require.Error(t, err, "joined mode without an authority must not validate")
}

// Secrets are supplied by owner-only FILE, never as a scalar that lands in shell
// history, a process listing, or a config backup.
func TestSecretSuppliedAsAScalarIsRejected(t *testing.T) {
	for name, frag := range map[string]string{
		"enrollment token":  "joined:\n  enrollmentToken: rog-secret-value\n",
		"identity key":      "identity:\n  key: PRIVATE-KEY-MATERIAL\n",
		"database password": "storage:\n  url: postgres://u:p@h/db\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig([]byte(minimalJoined + frag))
			require.Error(t, err, "a %s supplied as a scalar must be rejected", name)
			require.NotContains(t, err.Error(), "rog-secret-value", "the rejection must not echo the secret")
			require.NotContains(t, err.Error(), "PRIVATE-KEY-MATERIAL")
			require.NotContains(t, err.Error(), "postgres://u:p@h/db")
		})
	}
}

// A configured listener must be loopback by default: a fresh config that names no address
// must not silently listen on every interface.
//
// THIS TEST USED TO ASSERT THE OPPOSITE MISTAKE. It required ListenAddresses to be
// non-empty for a config that declares no listener at all, which passed only because the
// three addresses it returned were the station, admin and metrics ports - none of which
// this build binds. It was checking that we had invented some listeners, not that the real
// ones were safe.
func TestAConfiguredDataPlaneDefaultsToLoopback(t *testing.T) {
	c, err := ParseConfig([]byte(minimalStandalone))
	require.NoError(t, err)
	require.Empty(t, c.ListenAddresses(),
		"a Tower that configures no data plane binds nothing, and must not claim otherwise")

	// The HUB is what this build binds; a leftover relay.address binds nothing and must not
	// be reported as a listener (doctor would assess a port that does not exist).
	c, err = ParseConfig([]byte(minimalStandalone + "hub:\n  address: 127.0.0.1:8444\n"))
	require.NoError(t, err)
	require.Equal(t, []string{"127.0.0.1:8444"}, c.ListenAddresses())

	c, err = ParseConfig([]byte(minimalStandalone + "relay:\n  address: 127.0.0.1:8443\n"))
	require.NoError(t, err)
	require.Empty(t, c.ListenAddresses(), "a dead relay address is not a listener")
	require.Contains(t, strings.Join(c.Unenforced(), "\n"), "relay.address",
		"and the operator is told it does nothing")
}

// Every field this build decodes but does not act on must be NAMED. A control that is
// accepted and then ignored is worse than one that is missing: the operator believes the
// limit is in force and stops thinking about it.
func TestConfiguredControlsThisBuildIgnoresAreNamed(t *testing.T) {
	c, err := ParseConfig([]byte(minimalStandalone))
	require.NoError(t, err)
	require.Empty(t, c.Unenforced(), "defaults are not a promise the operator asked for")

	y := minimalStandalone + "limits:\n  maxStations: 4\n  maxInflight: 8\n" +
		"observability:\n  metricsAddress: 0.0.0.0:9090\n"
	c, err = ParseConfig([]byte(y))
	require.NoError(t, err)
	joined := strings.Join(c.Unenforced(), "\n")
	require.Contains(t, joined, "limits.maxStations")
	// The retired relay keys: still parsed so an upgrade does not hard-fail a running
	// Tower's config, but named as doing nothing and pointed at their replacement.
	cr, rerr := ParseConfig([]byte(minimalStandalone +
		"relay:\n  address: 127.0.0.1:8443\n  stations:\n    st-1: 127.0.0.1:9000\n"))
	require.NoError(t, rerr)
	relayIgnored := strings.Join(cr.Unenforced(), "\n")
	require.Contains(t, relayIgnored, "relay.address")
	require.Contains(t, relayIgnored, "relay.stations")
	require.Contains(t, relayIgnored, "hub.address", "the replacement is named")
	require.Contains(t, joined, "limits.maxInflight")
	require.Contains(t, joined, "observability.metricsAddress")
}

func TestStandaloneHasNoPublicNetworkReachability(t *testing.T) {
	c, err := ParseConfig([]byte(minimalStandalone))
	require.NoError(t, err)
	require.Empty(t, c.PublicAuthority(), "a standalone Tower must have no public authority to dial")
	require.False(t, c.AdvertisesPublicly())
}

func TestJoinedReportsItsAuthority(t *testing.T) {
	c, err := ParseConfig([]byte(minimalJoined))
	require.NoError(t, err)
	require.Equal(t, "https://broker.rogerai.fm", c.PublicAuthority())
}

// Redacted printing must show every effective value INCLUDING defaults, so an operator
// can see what the Tower will actually do, while never reading a secret file.
func TestRedactedPrintShowsEffectiveValuesButNoSecrets(t *testing.T) {
	c, err := ParseConfig([]byte(minimalJoined))
	require.NoError(t, err)
	out := c.PrintRedacted()

	require.Contains(t, out, "mode: joined")
	require.Contains(t, out, "https://broker.rogerai.fm")
	require.Contains(t, out, "/run/secrets/enrollment-token", "the secret PATH is shown")
	require.Contains(t, out, "127.0.0.1:", "defaults are shown, not omitted")
	require.NotContains(t, strings.ToLower(out), "begin private key")
}
