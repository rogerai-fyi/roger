package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/routingrules"
)

// TestQuantUnknownIsOneValue pins the shared quant rule's "unknown" to the wire protocol's, so the
// relay, the catalog and the LAN ladder admit an unlabeled source for the same word.
func TestQuantUnknownIsOneValue(t *testing.T) {
	require.Equal(t, protocol.QuantUnknown, routingrules.QuantUnknown)
}
