package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProfileModelsSurviveTheBandRewrite: a profile's models [a, b] resolved on the tuned
// proxy for band b still offers a once the band rewrite sets model to b (the CLI sends the
// whole list beside the band model the same way).
func TestProfileModelsSurviveTheBandRewrite(t *testing.T) {
	ps, err := ParseProfiles([]byte(`{"profiles":{"two":{"models":["a","b"]}}}`))
	require.NoError(t, err)
	out, did, err := ResolveProfileBody([]byte(`{"model":"@profile/two","messages":[]}`), ps)
	require.NoError(t, err)
	require.True(t, did)
	rewritten, model, ok := rewriteModel(keepPrimaryBeforeRewrite(out, "b"), "b", true)
	require.True(t, ok)
	require.Equal(t, "b", model)
	var m struct {
		Models []string `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rewritten, &m))
	require.Equal(t, []string{"a", "b"}, m.Models, "the profile's first model is still tried")

	// A primary that IS the band, or a body with no model, is left alone.
	same := []byte(`{"model":"b:free","models":["c"]}`)
	require.Equal(t, string(same), string(keepPrimaryBeforeRewrite(same, "b")))
	require.Equal(t, `{"x":1}`, string(keepPrimaryBeforeRewrite([]byte(`{"x":1}`), "b")))
	require.Equal(t, `{"model":"a"}`, string(keepPrimaryBeforeRewrite([]byte(`{"model":"a"}`), "")))
	bad := []byte(`{"model":"a","models":"nope"}`)
	require.Equal(t, string(bad), string(keepPrimaryBeforeRewrite(bad, "b")))
}

// TestFreqStripKeepsLargeIntegers: removing roger.freq never rounds the request's other
// integers (a seed above 2^53).
func TestFreqStripKeepsLargeIntegers(t *testing.T) {
	out, f := takeFreq([]byte(`{"seed":9007199254740993,"roger":{"freq":"abc"}}`))
	require.Equal(t, "abc", f)
	require.Contains(t, string(out), "9007199254740993")
	out = dropGuestFreq([]byte(`{"model":"@profile/p","seed":9007199254740993,"roger":{"freq":"abc"}}`))
	require.NotContains(t, string(out), "freq")
	require.Contains(t, string(out), "9007199254740993")
}

// TestProfileFreeVariantOfTheBandSurvivesTheRewrite: a profile whose model is the band's
// :free variant, with no other routing key, still asks for free once resolved on the proxy.
func TestProfileFreeVariantOfTheBandSurvivesTheRewrite(t *testing.T) {
	ps, err := ParseProfiles([]byte(`{"profiles":{"freebie":{"model":"b:free"}}}`))
	require.NoError(t, err)
	out, did, err := ResolveProfileBody([]byte(`{"model":"@profile/freebie","messages":[]}`), ps)
	require.NoError(t, err)
	require.True(t, did)
	_, model, ok := rewriteModel(keepPrimaryBeforeRewrite(out, "b"), "b", true)
	require.True(t, ok)
	require.Equal(t, "b:free", model, "the profile's :free is kept")
}

// TestValidatorEnforcesTheBrokerBounds: the client refuses what the broker refuses on size:
// more than 32 require or region entries, a quant label over 40 characters, and a min_ctx or
// max_ttft_ms above 2^31-1.
func TestValidatorEnforcesTheBrokerBounds(t *testing.T) {
	many := func(v string, n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
	for name, b := range map[string]map[string]any{
		"33 require":       {"roger": map[string]any{"require": many("tools", 33)}},
		"33 region":        {"roger": map[string]any{"region": many("eu", 33)}},
		"41-char label":    {"provider": map[string]any{"quantizations": []any{strings.Repeat("q", 41)}}},
		"min_ctx 2^31":     {"roger": map[string]any{"min_ctx": 2147483648.0}},
		"max_ttft_ms 2^31": {"roger": map[string]any{"max_ttft_ms": 2147483648.0}},
	} {
		require.Error(t, ValidateRoutingBody(b), name)
	}
	require.NoError(t, ValidateRoutingBody(map[string]any{"provider": map[string]any{"quantizations": []any{strings.Repeat("q", 40)}},
		"roger": map[string]any{"min_ctx": 2147483647.0, "region": many("eu", 32)}}))
}

// TestFiveModelLimitCountsTheModel: the effective list is [model] ++ models (contract §3), so
// a model plus five other entries is six models, over the limit.
func TestFiveModelLimitCountsTheModel(t *testing.T) {
	require.Error(t, ValidateRoutingBody(map[string]any{"model": "a", "models": []any{"b", "c", "d", "e", "f"}}))
	require.NoError(t, ValidateRoutingBody(map[string]any{"model": "a", "models": []any{"a", "b", "c", "d", "e"}}))
}

// TestValidatorBoundsAModelID: a model id over 256 characters, in model or models[], is
// refused as the broker refuses it; exactly 256 is accepted.
func TestValidatorBoundsAModelID(t *testing.T) {
	long := strings.Repeat("m", ModelIDMax+1)
	require.Error(t, ValidateRoutingBody(map[string]any{"model": long}))
	require.Error(t, ValidateRoutingBody(map[string]any{"models": []any{long}}))
	require.NoError(t, ValidateRoutingBody(map[string]any{"model": strings.Repeat("m", ModelIDMax)}))
}

// TestMaxPriceRefusalIsDeterministic: with several faulty max_price keys the refusal names the
// same one every time.
func TestMaxPriceRefusalIsDeterministic(t *testing.T) {
	for i := 0; i < 40; i++ {
		err := ValidateRoutingBody(map[string]any{"provider": map[string]any{"max_price": map[string]any{"zz": 1.0, "aa": 1.0, "mm": 1.0}}})
		require.EqualError(t, err, "provider.max_price.aa is not supported")
	}
}
