package main

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/harness"
)

// TestClientMirrorsTheBrokerRoutingBounds: the client validator mirrors the broker's size
// bounds (the broker's live in package main and cannot be imported); this pins them equal.
func TestClientMirrorsTheBrokerRoutingBounds(t *testing.T) {
	require.Equal(t, routingListMax, client.RoutingListMax)
	require.Equal(t, quantLabelMax, client.QuantLabelMax)
	require.Equal(t, math.MaxInt32, client.RoutingIntMax)
	require.Equal(t, routingModelsMax, client.ModelsMax)
	require.Equal(t, routingModelIDMax, client.ModelIDMax)
}

// TestHarnessKnowsEveryNoMatchFilterName: the agent harness keeps a no_match's words when it
// names a routing filter, so its copy of the broker's filter names must be this exact list.
func TestHarnessKnowsEveryNoMatchFilterName(t *testing.T) {
	require.Equal(t, noMatchFilterOrder, harness.NoMatchFilterNames)
}
