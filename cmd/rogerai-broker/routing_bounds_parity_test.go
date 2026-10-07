package main

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/client"
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
