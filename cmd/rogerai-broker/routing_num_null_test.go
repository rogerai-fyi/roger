package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRoutingNumRefusesNull: null is not a number, so no caller can read it as 0 (an object
// key that is null was already dropped as absent; a null inside a list is invalid).
func TestRoutingNumRefusesNull(t *testing.T) {
	_, ok := routingNum(json.RawMessage(" null "))
	require.False(t, ok)
	v, ok := routingNum(json.RawMessage("0"))
	require.True(t, ok)
	require.Zero(t, v)
}
