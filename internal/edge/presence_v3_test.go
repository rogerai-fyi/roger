package edge

// Presence v3 (ruled 2026-10-13): power, for ANY kind of node, signed. Pinned by golden vectors
// computed independently (testdata/gen_presence_v3_vectors.py, pure-Python RFC 8032).

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPresenceV3GoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/presence_v3_vectors.json")
	require.NoError(t, err)
	var file struct {
		Domain  string `json:"domain"`
		Vectors []struct {
			Name         string          `json:"name"`
			Seed         string          `json:"seed"`
			NodeID       string          `json:"node_id"`
			Request      json.RawMessage `json:"request"`
			Canonical    string          `json:"canonical"`
			CanonicalHex string          `json:"canonical_hex"`
			Sig          string          `json:"sig"`
		} `json:"vectors"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	require.Equal(t, presencePrefixV3, file.Domain)
	require.Len(t, file.Vectors, 6)
	for _, v := range file.Vectors {
		var p PresenceRequest
		require.NoError(t, json.Unmarshal(v.Request, &p), v.Name)
		seed, err := hex.DecodeString(v.Seed)
		require.NoError(t, err)
		key := ed25519.NewKeyFromSeed(seed)
		require.Equal(t, v.NodeID, NodeID(key.Public().(ed25519.PublicKey)), v.Name)
		require.Equal(t, v.Canonical, string(p.canonical()), v.Name)
		require.Equal(t, v.CanonicalHex, hex.EncodeToString(p.canonical()), v.Name)
		require.Equal(t, v.Sig, hex.EncodeToString(ed25519.Sign(key, p.canonical())), v.Name)
		require.Empty(t, p.vitalsRefusal(), v.Name)
	}
}

// Power travels for any kind; it is recorded; and every way it can be unsigned or nonsense is refused.
func TestPresenceV3CarriesPowerForAnyKind(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	h := PresenceHandler(f, a, nil)
	pct, yes, no, mins := 18, true, false, 95

	host := validPresence(id, certPEM)
	host.Kind, host.V = "host", 3
	host.Power = &PowerVitals{BatteryPct: &pct, Charging: &no, Source: "battery", LowPower: &yes, MinutesLeft: &mins}
	signPresence(&host, priv)
	require.Equal(t, http.StatusOK, doPresence(h, host).Code, "a host reports its power")
	n, ok, err := f.Get(id)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, n.Power)
	require.Equal(t, 18, *n.Power.BatteryPct)
	require.Equal(t, "battery", n.Power.Source)
	require.True(t, *n.Power.LowPower)
	require.Equal(t, 95, *n.Power.MinutesLeft)

	for name, bad := range map[string]func(*PresenceRequest){
		"power in a v2":                  func(p *PresenceRequest) { p.V = 2; p.Power = &PowerVitals{LowPower: &yes} },
		"power in a v1":                  func(p *PresenceRequest) { p.V = 0; p.Power = &PowerVitals{LowPower: &yes} },
		"a source that is not a word":    func(p *PresenceRequest) { p.Power = &PowerVitals{Source: "solar"} },
		"a battery over 100":             func(p *PresenceRequest) { b := 101; p.Power = &PowerVitals{BatteryPct: &b} },
		"a negative time left":           func(p *PresenceRequest) { m := -1; p.Power = &PowerVitals{MinutesLeft: &m} },
		"a v3 battery under vitals":      func(p *PresenceRequest) { p.Kind = "mobile"; p.Vitals = &PhoneVitals{BatteryPct: &pct} },
		"phone vitals from a host in v3": func(p *PresenceRequest) { p.Kind = "host"; p.Vitals = &PhoneVitals{Thermal: "fair"} },
	} {
		p := validPresence(id, certPEM)
		p.V = 3
		bad(&p)
		signPresence(&p, priv)
		require.Equal(t, http.StatusForbidden, doPresence(h, p).Code, name)
	}

	// A version this authority does not read is refused in the exact words a sender falls back on.
	p := validPresence(id, certPEM)
	p.V = 4
	signPresence(&p, priv)
	w := doPresence(h, p)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "that presence version is not one this authority reads", strings.TrimSpace(w.Body.String()))

	// A phone in v3: thermal and foreground under vitals, its battery under power, and both recorded.
	ph := validPresence(id, certPEM)
	ph.V = 3
	ph.Vitals = &PhoneVitals{Thermal: "nominal", Foreground: &no}
	ph.Power = &PowerVitals{BatteryPct: &pct, Charging: &yes, LowPower: &yes}
	signPresence(&ph, priv)
	require.Equal(t, http.StatusOK, doPresence(h, ph).Code)
	n, _, _ = f.Get(id)
	require.False(t, *n.Phone.Foreground)
	require.Equal(t, 18, *n.Phone.BatteryPct, "a phone's battery is on its phone line too")
	require.True(t, *n.Power.LowPower)
}
