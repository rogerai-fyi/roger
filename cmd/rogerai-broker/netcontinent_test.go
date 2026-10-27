package main

// netcontinent_test.go: the operator-supplied network-to-continent table and the region
// contradiction rule built on it (contract §14.B7 #21, founder ruling 2026-10-05).

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

func TestParseNetTable(t *testing.T) {
	good := "# comment\n\n10.0.0.0/8 NA\n10.1.0.0/16   eu\n2001:db8::/32\tAS\n"
	tbl, err := parseNetTable([]byte(good))
	require.NoError(t, err)
	for _, tc := range []struct{ addr, want string }{
		{"10.2.3.4", "NA"},
		{"10.1.2.3", "EU"}, // the longest prefix wins
		{"2001:db8::1", "AS"},
		{"192.0.2.1", ""}, // no match
		{"not-an-ip", ""},
		{"", ""},
		{"10.1.2.3:443", "EU"}, // an address with a port
	} {
		require.Equal(t, tc.want, tbl.continentOf(tc.addr), tc.addr)
	}
	var none *netTable
	require.Equal(t, "", none.continentOf("10.2.3.4"), "no table maps nothing")

	for _, bad := range []string{
		"10.0.0.0/8",              // no continent
		"10.0.0.0/8 NA extra",     // a third field
		"10.0.0.300/8 NA",         // not a prefix
		"10.0.0.0/8 ZZ",           // not a continent code
		"10.0.0.0/8 NA\nnonsense", // a bad line anywhere
	} {
		_, err := parseNetTable([]byte(bad))
		require.Error(t, err, "%q must be refused", bad)
	}
}

func TestLoadNetTable(t *testing.T) {
	tbl, err := loadNetTable("")
	require.NoError(t, err)
	require.Nil(t, tbl, "no file configured: no table")

	p := filepath.Join(t.TempDir(), "t.txt")
	require.NoError(t, os.WriteFile(p, []byte("10.0.0.0/8 NA\n"), 0o600))
	tbl, err = loadNetTable(p)
	require.NoError(t, err)
	require.Equal(t, "NA", tbl.continentOf("10.9.9.9"))

	_, err = loadNetTable(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)

	// slice-6 review 2026-10-06: the read is bounded; a table over the cap is refused.
	big := filepath.Join(t.TempDir(), "big.txt")
	require.NoError(t, os.WriteFile(big, []byte("# padding\n"+string(make([]byte, 64))), 0o600))
	prev := netTableMaxBytes
	netTableMaxBytes = 16
	_, err = loadNetTable(big)
	netTableMaxBytes = prev
	require.ErrorContains(t, err, "larger than")

	tbl, err = loadNetTable("testdata/net_continents.txt")
	require.NoError(t, err)
	require.Equal(t, "NA", tbl.continentOf("192.0.2.1"))
	require.Equal(t, "EU", tbl.continentOf("198.51.100.1"))
}

func TestRegionContinent(t *testing.T) {
	for _, tc := range []struct{ region, want string }{
		{"eu", "EU"}, {"EU", "EU"}, {"europe", "EU"}, {"eu-west", "EU"}, {" eu ", "EU"},
		{"us", "NA"}, {"us-east", "NA"}, {"northamerica", "NA"},
		{"asia", "AS"}, {"jp", "AS"},
		// slice-6 review 2026-10-06: an ambiguous token never maps (an honest station must never
		// be contradicted): ap-southeast-2 is Australia, "ap" alone names no continent.
		{"ap", ""}, {"ap-southeast", ""}, {"ap-southeast-2", ""}, {"ap-northeast-1", ""}, {"america", ""},
		{"br", "SA"}, {"southamerica", "SA"}, {"af", "AF"}, {"oc", "OC"}, {"au", "OC"},
		// slice-6 audit 2026-10-06: two-letter tokens that are also another place's country code
		// map to nothing (sa Saudi Arabia, na Namibia, in India or a word, ca a state or a country).
		{"sa", ""}, {"na", ""}, {"in", ""}, {"ca", ""},
		{"", ""}, {"openrouter", ""}, {"mars", ""},
	} {
		require.Equal(t, tc.want, regionContinent(tc.region), tc.region)
	}
}

func TestRegionContradicted(t *testing.T) {
	for _, tc := range []struct {
		name string
		reg  protocol.NodeRegistration
		want bool
	}{
		{"declared eu, network NA", protocol.NodeRegistration{Region: "eu", NetContinent: "NA"}, true},
		{"declared eu, network EU", protocol.NodeRegistration{Region: "eu", NetContinent: "EU"}, false},
		{"no network continent", protocol.NodeRegistration{Region: "eu"}, false},
		{"no declared region", protocol.NodeRegistration{NetContinent: "NA"}, false},
		{"region with no continent", protocol.NodeRegistration{Region: "openrouter", NetContinent: "NA"}, false},
		{"curated is never contradicted", protocol.NodeRegistration{Region: "eu", NetContinent: "NA", Curated: true}, false},
	} {
		require.Equal(t, tc.want, regionContradicted(tc.reg), tc.name)
	}
}

func TestNetContinentIsBrokerSet(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{NodeID: "n1", PubKey: hex.EncodeToString(pub), Region: "eu"}
	reg.SignRegistration(priv)
	reg.NetContinent = "NA"
	require.True(t, reg.VerifyRegistration(), "the broker's stamp never breaks the node's signature")
}

// slice-6 audit 2026-10-06: the region table's state is visible to the operator on /admin/live.
func TestRegionTableStatus(t *testing.T) {
	b := &broker{}
	require.Equal(t, map[string]any{"configured": false, "ranges": 0}, b.regionTableStatus())
	tbl, err := loadNetTable("testdata/net_continents.txt")
	require.NoError(t, err)
	b.netTable = tbl
	require.Equal(t, map[string]any{"configured": true, "ranges": 3}, b.regionTableStatus())
	b.netTableErr = "line 2: want \"CIDR CONTINENT\""
	require.Equal(t, "line 2: want \"CIDR CONTINENT\"", b.regionTableStatus()["error"])
}
