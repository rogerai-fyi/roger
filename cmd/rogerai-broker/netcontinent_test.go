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

	tbl, err = loadNetTable("testdata/net_continents.txt")
	require.NoError(t, err)
	require.Equal(t, "NA", tbl.continentOf("3.208.0.1"))
	require.Equal(t, "EU", tbl.continentOf("52.28.0.1"))
}

func TestRegionContinent(t *testing.T) {
	for _, tc := range []struct{ region, want string }{
		{"eu", "EU"}, {"EU", "EU"}, {"europe", "EU"}, {"eu-west", "EU"}, {" eu ", "EU"},
		{"us", "NA"}, {"na", "NA"}, {"us-east", "NA"}, {"ca", "NA"},
		{"asia", "AS"}, {"ap-southeast", "AS"}, {"jp", "AS"},
		{"sa", "SA"}, {"br", "SA"}, {"af", "AF"}, {"oc", "OC"}, {"au", "OC"},
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
