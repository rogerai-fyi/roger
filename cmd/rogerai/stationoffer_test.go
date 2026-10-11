package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/detect"
)

// A station's output names a few refused models but counts many, and never lists what it skipped.
func TestStationOfferLinesStayShort(t *testing.T) {
	require.Equal(t, "the Tower does not accept: a, b", refusedLine([]string{"a", "b"}))
	many := refusedLine(strings.Split("a b c d e f g h i j k l m n o p q r s t u v w x y z aa bb cc dd ee ff gg", " "))
	require.Equal(t, "the Tower does not accept 33 of the offered models (its invite names the ones it takes)", many)
	require.Equal(t, "skipped 1 other model on this machine; name one with --models to offer it", skippedLine(1))
	require.Equal(t, "skipped 33 other models on this machine; name one with --models to offer it", skippedLine(33))
}

// A model several local servers list is offered in the pick list once, and counts as an account
// proxy if any server listing it is one.
func TestStationPickListNamesEachModelOnce(t *testing.T) {
	prevServed, prevTTY, prevIn := rogerServedBases, stationInteractive, stationStdin
	rogerServedBases = func() []string { return nil }
	stationInteractive = func() bool { return true }
	stationStdin = strings.NewReader("\n")
	t.Cleanup(func() { rogerServedBases, stationInteractive, stationStdin = prevServed, prevTTY, prevIn })
	found := []detect.Found{
		{BaseURL: "http://127.0.0.1:8001/v1", Models: []string{"voice", "qwen3-8b"}},
		{BaseURL: "http://127.0.0.1:8002/v1", Models: []string{"voice"}},
		{BaseURL: "http://127.0.0.1:8003/v1", Models: []string{"voice", "claude"}},
	}
	var out strings.Builder
	stationChoose(found, nil, &out)
	require.Equal(t, 1, strings.Count(out.String(), ") voice"), out.String())
	require.Contains(t, out.String(), ") voice (account proxy", "a model any proxy lists is treated as a proxy's")
	require.Equal(t, 1, strings.Count(out.String(), ") qwen3-8b\n"), out.String())
}
