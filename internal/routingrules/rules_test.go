package routingrules

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func obj(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

// TestValues pins the per-key value rules every plane shares (contract §1a), ported with the
// rules from internal/client.
func TestValues(t *testing.T) {
	cases := []struct{ body, err string }{
		{`{}`, ""},
		{`{"provider":1}`, "provider must be an object"},
		{`{"roger":1}`, "roger must be an object"},
		{`{"provider":{"order":[]}}`, "provider.order must be a non-empty list of station ids"},
		{`{"provider":{"only":"x"}}`, "provider.only must be a non-empty list of station ids"},
		{`{"provider":{"ignore":[]}}`, ""},
		{`{"provider":{"ignore":[""]}}`, "provider.ignore must be a non-empty list of station ids"},
		{`{"provider":{"order":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q","r","s","t","u","v","w","x","y","z","A","B","C","D","E","F","G"]}}`, "provider.order has more than 32 entries"},
		{`{"provider":{"order":["a","b"],"only":["a"]}}`, "provider.order names b, outside provider.only"},
		{`{"provider":{"order":["a"],"only":["a","b"]}}`, ""},
		{`{"provider":{"allow_fallbacks":"no"}}`, "provider.allow_fallbacks must be a boolean"},
		{`{"provider":{"require_parameters":1}}`, "provider.require_parameters must be a boolean"},
		{`{"provider":{"sort":"cheap"}}`, "provider.sort must be price, throughput or latency"},
		{`{"provider":{"sort":"latency","allow_fallbacks":true,"require_parameters":false}}`, ""},
		{`{"provider":{"quantizations":[1]}}`, "provider.quantizations must be a list of labels"},
		{`{"provider":{"max_price":3}}`, "provider.max_price must be an object"},
		{`{"provider":{"max_price":{"image":1}}}`, "provider.max_price.image is not supported"},
		{`{"provider":{"max_price":{"prompt":-1}}}`, "provider.max_price.prompt must be a price >= 0"},
		{`{"provider":{"max_price":{"request":"1"}}}`, "provider.max_price.request must be a price >= 0"},
		{`{"provider":{"max_price":{"prompt":null,"completion":0,"request":0.5}}}`, ""},
		{`{"roger":{"pref":"warp"}}`, "roger.pref must be one of " + strings.Join(Prefs, ", ")},
		{`{"roger":{"pref":""}}`, "roger.pref must be one of " + strings.Join(Prefs, ", ")},
		{`{"roger":{"trust_min":"gold"}}`, "roger.trust_min must be any, verified or confidential"},
		{`{"roger":{"require":"tools"}}`, "roger.require must be a list"},
		{`{"roger":{"require":["audio"]}}`, "roger.require: unknown capability audio"},
		{`{"roger":{"require":["tools","vision"]}}`, ""},
		{`{"roger":{"region":[]}}`, "roger.region must be a list of lowercase regions"},
		{`{"roger":{"region":["EU"]}}`, "roger.region: EU is not a lowercase region"},
		{`{"roger":{"region":["eu","us"]}}`, ""},
		{`{"roger":{"region":["eu","us-west"]}}`, "roger.region: us-west is not a lowercase region"},
		{`{"roger":{"params_b":[1]}}`, "roger.params_b must be [min, max]"},
		{`{"roger":{"params_b":"7"}}`, "roger.params_b must be [min, max]"},
		{`{"roger":{"params_b":[0,0]}}`, "roger.params_b must be [min, max] with 0 <= min <= max and max > 0"},
		{`{"roger":{"params_b":[9,7]}}`, "roger.params_b must be [min, max] with 0 <= min <= max and max > 0"},
		{`{"roger":{"params_b":[7,"x"]}}`, "roger.params_b must be [min, max] with 0 <= min <= max and max > 0"},
		{`{"roger":{"params_b":[7,70]}}`, ""},
		{`{"roger":{"min_ctx":0}}`, "roger.min_ctx must be a positive integer"},
		{`{"roger":{"max_ttft_ms":1.5}}`, "roger.max_ttft_ms must be a positive integer"},
		{`{"roger":{"min_ctx":"8k"}}`, "roger.min_ctx must be a positive integer"},
		{`{"roger":{"min_tps":-1}}`, "roger.min_tps must be >= 0"},
		{`{"roger":{"min_tps":"fast"}}`, "roger.min_tps must be >= 0"},
		{`{"roger":{"self_hosted_only":"y"}}`, "roger.self_hosted_only must be a boolean"},
		{`{"roger":{"confidential":1}}`, "roger.confidential must be a boolean"},
		{`{"roger":{"freq":5}}`, "roger.freq must be a string"},
		{`{"roger":{"freq":"AAAA","confidential":true,"self_hosted_only":false,"min_ctx":8192,"max_ttft_ms":900,"min_tps":0,"trust_min":"verified"}}`, ""},
		{`{"provider":null,"roger":null}`, ""},
	}
	for _, c := range cases {
		err := Values(obj(t, c.body))
		if c.err == "" {
			require.NoError(t, err, c.body)
		} else {
			require.EqualError(t, err, c.err, c.body)
		}
	}
}

func TestIDListAcceptsStringSlices(t *testing.T) {
	ids, err := idList([]string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, ids)
	_, err = idList([]string{"a", " "})
	require.Error(t, err)
	_, err = idList(3)
	require.Error(t, err)
}

// params_b accepts 0 as the lower bound ("no floor"), as the broker does.
func TestParamsBAcceptsAZeroFloor(t *testing.T) {
	for body, ok := range map[string]bool{`{"params_b":[0,8]}`: true, `{"params_b":[0,0]}`: false, `{"params_b":[8,0]}`: false, `{"params_b":[-1,8]}`: false} {
		err := validateRoger(obj(t, body))
		if ok {
			require.NoError(t, err, body)
		} else {
			require.Error(t, err, body)
		}
	}
}

func TestValuesEnforceTheBrokerBounds(t *testing.T) {
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
		require.Error(t, Values(b), name)
	}
	require.NoError(t, Values(map[string]any{"provider": map[string]any{"quantizations": []any{strings.Repeat("q", 40)}},
		"roger": map[string]any{"min_ctx": 2147483647.0, "region": many("eu", 32)}}))
}

func TestMaxPriceRefusalIsDeterministic(t *testing.T) {
	for i := 0; i < 40; i++ {
		err := Values(map[string]any{"provider": map[string]any{"max_price": map[string]any{"zz": 1.0, "aa": 1.0, "mm": 1.0}}})
		require.EqualError(t, err, "provider.max_price.aa is not supported")
	}
}

func TestValidPref(t *testing.T) {
	require.True(t, ValidPref(""))
	for _, p := range Prefs {
		require.True(t, ValidPref(p))
	}
	require.False(t, ValidPref("warp"))
}
