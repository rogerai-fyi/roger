package client

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutingOverlay(t *testing.T) {
	var b map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"models":["a"],"provider":{"only":["n1"],"order":["n1"],"ignore":["n9"],
		"allow_fallbacks":false,"require_parameters":true,"sort":"price","quantizations":["Q8_0"],
		"max_price":{"prompt":0.2,"completion":2,"request":0.05}},
		"roger":{"require":["tools"],"params_b":[7,70],"min_ctx":32768,"max_ttft_ms":900,"min_tps":5,"trust_min":"verified",
		"self_hosted_only":true,"confidential":true,"region":["eu"]}}`), &b))
	got := Routing{Pref: "fast", Ignore: []string{"n8"}}.Overlay(b)
	require.Equal(t, Routing{Sort: "price", Models: []string{"a"}, Only: []string{"n1"}, Prefer: []string{"n1"},
		Ignore: []string{"n8", "n9"}, NoFallbacks: true, RequireParams: true, Quantizations: []string{"Q8_0"},
		MaxIn: 0.2, MaxOut: 2, MaxReq: 0.05, Require: []string{"tools"}, ParamsB: []float64{7, 70}, MinCtx: 32768,
		MaxTTFT: 900, MinTPS: 5, TrustMin: "verified", SelfHostedOnly: true, Confidential: true, Region: []string{"eu"}}, got)

	got = Routing{Sort: "price"}.Overlay(map[string]any{"roger": map[string]any{"pref": "cheap"}})
	require.Equal(t, Routing{Pref: "cheap"}, got, "a profile's pref replaces the rule's sort")
	require.Equal(t, Routing{MinTPS: 3}, Routing{MinTPS: 3}.Overlay(nil))
	_, ok := bodyPath(map[string]any{"a": "x"}, "a.b")
	require.False(t, ok)
}

func TestFreeOnlyAddsTheSugarOnce(t *testing.T) {
	out, err := Routing{FreeOnly: true}.Apply([]byte(`{"model":"m"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"m:free"}`, string(out))
	out, err = Routing{FreeOnly: true}.Apply([]byte(`{"model":"m:free"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"m:free"}`, string(out))
}
