package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOfferMeetsBody(t *testing.T) {
	base := Offer{NodeID: "n1", Region: "eu", Capabilities: []string{"TOOLS"}, Quant: "Q8_0", Verified: true}
	cases := []struct {
		name string
		o    Offer
		c    Criteria
		want bool
	}{
		{"no body filters", base, Criteria{}, true},
		{"required capability present (case-insensitive)", base, Criteria{Require: []string{"tools"}}, true},
		{"required capability missing", base, Criteria{Require: []string{"vision"}}, false},
		{"self-hosted only refuses curated", Offer{Curated: true}, Criteria{SelfHostedOnly: true}, false},
		{"self-hosted only admits self-hosted", base, Criteria{SelfHostedOnly: true}, true},
		{"quant listed", base, Criteria{Quantizations: []string{"q8_0"}}, true},
		{"quant not listed", base, Criteria{Quantizations: []string{"BF16"}}, false},
		{"unlabeled offer admitted by unknown", Offer{}, Criteria{Quantizations: []string{QuantUnknown}}, true},
		{"unlabeled offer refused without unknown", Offer{}, Criteria{Quantizations: []string{"BF16"}}, false},
		{"only admits", base, Criteria{Only: []string{"n1"}}, true},
		{"only refuses", base, Criteria{Only: []string{"n2"}}, false},
		{"region admits", base, Criteria{Region: []string{"eu"}}, true},
		{"region refuses", base, Criteria{Region: []string{"us"}}, false},
		{"verified floor met", base, Criteria{TrustMin: "verified"}, true},
		{"verified floor missed", Offer{}, Criteria{TrustMin: "verified"}, false},
		{"confidential floor missed", base, Criteria{TrustMin: "confidential"}, false},
		{"confidential floor met", Offer{Confidential: true}, Criteria{TrustMin: "confidential"}, true},
	}
	for _, c := range cases {
		require.Equal(t, c.want, offerMeetsBody(c.o, c.c), c.name)
	}
}

func TestCallerRoutingCriteria(t *testing.T) {
	var c Criteria
	require.False(t, callerRoutingCriteria([]byte(`nope`), &c))
	require.Equal(t, Criteria{}, c)

	c = Criteria{}
	no := callerRoutingCriteria([]byte(`{"tools":[{}],
		"roger":{"require":["vision"],"self_hosted_only":true,"confidential":true,"region":["eu"],"trust_min":"verified"},
		"provider":{"quantizations":["Q8_0"],"only":["n1"],"allow_fallbacks":false}}`), &c)
	require.True(t, no)
	require.Equal(t, Criteria{Require: []string{"vision", "tools"}, SelfHostedOnly: true, Confidential: true,
		Quantizations: []string{"Q8_0"}, Only: []string{"n1"}, Region: []string{"eu"}, TrustMin: "verified"}, c)

	c = Criteria{}
	require.False(t, callerRoutingCriteria([]byte(`{"provider":{"allow_fallbacks":true},"tools":[]}`), &c))
	require.Empty(t, c.Require)
}

func TestErrorCodeOf(t *testing.T) {
	require.Equal(t, "no_match", errorCodeOf([]byte(`{"error":{"code":"no_match"}}`)))
	require.Equal(t, "", errorCodeOf([]byte(`{"error":"plain"}`)))
	require.Equal(t, "", errorCodeOf([]byte(`nope`)))
}
