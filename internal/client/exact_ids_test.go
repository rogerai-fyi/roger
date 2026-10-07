package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStationsAndRegionsCompareExactly: station ids and regions compare byte for byte, as the
// broker compares them; only quant labels compare without case.
func TestStationsAndRegionsCompareExactly(t *testing.T) {
	refused := func(r Routing, body string) {
		t.Helper()
		_, err := r.Apply([]byte(body))
		var rr *RoutingRefusal
		require.ErrorAs(t, err, &rr, body)
	}
	refused(Routing{Region: []string{"eu"}}, `{"model":"m","roger":{"region":["EU"]}}`)
	refused(Routing{Only: []string{"n1"}}, `{"model":"m","provider":{"only":["N1"]}}`)
	refused(Routing{Only: []string{"n1"}}, `{"model":"m","provider":{"order":["N1"]}}`)
	refused(Routing{Prefer: []string{"n1"}, NoFallbacks: true}, `{"model":"m","provider":{"order":["N1"]}}`)
	_, err := Routing{Quantizations: []string{"Q8_0"}}.Apply([]byte(`{"model":"m","provider":{"quantizations":["q8_0"]}}`))
	require.NoError(t, err, "quant labels still compare without case")

	offers := []Offer{{NodeID: "n1", Model: "m", Online: true, Region: "eu"}}
	_, ok := pickAlternative(offers, Criteria{Model: "m", Only: []string{"N1"}}, nil)
	require.False(t, ok, "only compares station ids exactly")
	_, ok = pickAlternative(offers, Criteria{Model: "m", Region: []string{"EU"}}, nil)
	require.False(t, ok, "region compares exactly")
	_, ok = pickAlternative(offers, Criteria{Model: "m", Exclude: []string{"N1"}}, nil)
	require.True(t, ok, "an exclusion names one station exactly, as the broker's ignore does")
}

// TestPaddedGuestModelsEntryIsRefused: a guest's models[] entry with surrounding whitespace is
// refused, as the broker refuses it, never silently dropped by the owner's filter.
func TestPaddedGuestModelsEntryIsRefused(t *testing.T) {
	_, err := Routing{Models: []string{"a", "b"}}.Apply([]byte(`{"model":"a","models":["b"," b"]}`))
	var rr *RoutingRefusal
	require.ErrorAs(t, err, &rr)
	require.Equal(t, "models must be a list of model ids", rr.Msg)
}
