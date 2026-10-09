package routingrules

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassName(t *testing.T) {
	for _, tc := range []struct {
		id, want string
		ok       bool
	}{
		{"@class/small", "small", true},
		{"@class/large", "large", true},
		{"@class/coding-30b", "coding-30b", true},
		{"@class/Small", "Small", false},
		{"@class/small/x", "small/x", false},
		{"@class/", "", false},
	} {
		got, ok := ClassName(tc.id)
		require.Equal(t, tc.ok, ok, tc.id)
		require.Equal(t, tc.want, got, tc.id)
	}
	require.Equal(t, []string{"coding-30b", "large", "small"}, ClassNames())
}

func TestExpandClass(t *testing.T) {
	src := []ClassCandidate{
		{Model: "qwen3-8b", ParamsB: 8, ParamsKnown: true, Cost: 2},
		{Model: "qwen3-8b", ParamsB: 8, ParamsKnown: true, Cost: 1}, // its cheapest source counts
		{Model: "llama-3b", ParamsB: 3, ParamsKnown: true, Cost: 1},
		{Model: "mystery", ParamsKnown: false},                     // no known size: never in a size class
		{Model: "gpt-oss-20b", ParamsB: 20, ParamsKnown: true},     // outside small
		{Model: "qwen3-coder-30b", ParamsB: 30, ParamsKnown: true}, // coding-30b needs tools
		{Model: "devstral-24b", ParamsB: 24, ParamsKnown: true, Tools: true},
	}
	require.Equal(t, []string{"llama-3b", "qwen3-8b"}, ExpandClass("small", src, 0), "ties on cost break by id")
	require.Equal(t, []string{"llama-3b"}, ExpandClass("small", src, 1), "max caps the expansion")
	require.Equal(t, []string{"devstral-24b"}, ExpandClass("coding-30b", src, 0), "a tools class takes only tools-verified models")
	require.Empty(t, ExpandClass("large", src, 0))
	require.Nil(t, ExpandClass("nope", src, 0))
}

func TestQuantAdmits(t *testing.T) {
	require.Nil(t, QuantSet(nil))
	set := QuantSet([]string{" Q8_0 ", "bf16"})
	for _, tc := range []struct {
		set   map[string]bool
		quant string
		want  bool
	}{
		{nil, "", true}, {nil, "Q4_K_M", true},
		{set, "Q8_0", true}, {set, "q8_0", true}, {set, "BF16", true},
		{set, "Q4_K_M", false}, {set, "", false},
		{QuantSet([]string{QuantUnknown}), "", true},
		{QuantSet([]string{QuantUnknown}), "Q8_0", false},
	} {
		require.Equal(t, tc.want, QuantAdmits(tc.set, tc.quant), "%v %q", tc.set, tc.quant)
	}
}
