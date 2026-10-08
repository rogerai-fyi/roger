package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegistrationProbeMinIsSigned pins the operator-declared minimum probe interval
// (probe_min_s) as a SIGNED registration field, ridden exactly like Curated: the possession
// proof covers it, so nobody but the node's key can add, strip, or change it in flight, and
// omitempty keeps a node that never declares it byte-identical to an old binary's signature.
func TestRegistrationProbeMinIsSigned(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	base := func() NodeRegistration {
		return NodeRegistration{NodeID: "n1", PubKey: hex.EncodeToString(pub), TS: 123,
			Offers: []ModelOffer{{Model: "m", PriceIn: 1, PriceOut: 2}}}
	}

	t.Run("round trip verifies and survives JSON", func(t *testing.T) {
		reg := base()
		reg.ProbeMinSeconds = 6 * 3600
		reg.SignRegistration(priv)
		require.True(t, reg.VerifyRegistration())

		raw, err := json.Marshal(reg)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"probe_min_s":21600`)
		var back NodeRegistration
		require.NoError(t, json.Unmarshal(raw, &back))
		require.Equal(t, 6*3600, back.ProbeMinSeconds)
		require.True(t, back.VerifyRegistration(), "the declared interval must survive the wire with its signature intact")
	})

	for _, tc := range []struct {
		name   string
		signed int
		sent   int
	}{
		{"stripped in flight", 21600, 0},
		{"lowered in flight", 21600, 60},
		{"raised in flight", 21600, 86400},
		{"added in flight", 0, 21600},
	} {
		t.Run(tc.name+" fails verification", func(t *testing.T) {
			reg := base()
			reg.ProbeMinSeconds = tc.signed
			reg.SignRegistration(priv)
			reg.ProbeMinSeconds = tc.sent
			require.False(t, reg.VerifyRegistration())
		})
	}

	t.Run("zero is omitted so old signatures stay byte-identical", func(t *testing.T) {
		// An absent key is exactly the JSON a binary that predates the field signed, so its
		// signature still verifies on a broker that knows the field.
		reg := base()
		require.NotContains(t, string(reg.regSigningBytes()), "probe_min_s")
	})
}
