package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseUnits(t *testing.T) {
	for in, want := range map[string][]float64{
		"7-70": {7, 70}, "7b-70b": {7, 70}, "0.5-3": {0.5, 3}, "30": {30, 30}, "30-": {30, ParamsOpenMax}, "-8": {0, 8},
	} {
		got, err := ParseParams(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"70-7", "-", "0-0", "7-nan", "seven", "0", "-0", "0-", "x-7"} {
		_, err := ParseParams(bad)
		require.Error(t, err, bad)
	}
	for in, want := range map[string]int{"32k": 32768, "32K": 32768, "8192": 8192, "128k": 131072, "1k": 1024} {
		got, err := ParseCtx(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"0", "-1", "32kb", "", "0k"} {
		_, err := ParseCtx(bad)
		require.Error(t, err, bad)
	}
	for in, want := range map[string]int{"1500ms": 1500, "1.5s": 1500, "2s": 2000, "800": 800} {
		got, err := ParseTTFT(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"0", "-5ms", "soon", "0.5ms", "-3"} {
		_, err := ParseTTFT(bad)
		require.Error(t, err, bad)
	}

	_, err := ParseParams("70-7")
	require.EqualError(t, err, "min must be ≤ max")
	for _, r := range []string{"eu", "apac"} {
		got, err := NormRegion(r)
		require.NoError(t, err)
		require.Equal(t, r, got)
	}
	// The broker's token rule (^[a-z]{2,8}$, request_shape.feature): no digits or hyphens.
	for _, r := range []string{"us-west", "eu-1", "europe-west1", "e"} {
		_, err := NormRegion(r)
		require.Error(t, err, r)
	}
	for _, r := range []string{"EU", "e", "europe-west1"} {
		_, err := NormRegion(r)
		require.Error(t, err, r)
	}
	got, err := NormRequire("TOOLS")
	require.NoError(t, err)
	require.Equal(t, "tools", got)
	_, err = NormRequire("json")
	require.Error(t, err)
}
