package client

import (
	"encoding/json"
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
