package protocol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDuplicateRoutingKey names the first duplicate key inside provider, roger, or
// provider.max_price, and nothing for a body without one.
func TestDuplicateRoutingKey(t *testing.T) {
	for in, want := range map[string]string{
		`{"provider":{"only":["a"],"only":["b"]}}`:                 "provider.only",
		`{"roger":{"pref":"cheap","pref":"fast"}}`:                 "roger.pref",
		`{"provider":{"max_price":{"request":1,"request":2}}}`:     "provider.max_price.request",
		`{"provider":{"max_price":{"request":1e400,"request":2}}}`: "provider.max_price.request",
		`{"provider":{"only":["a"]},"roger":{"pref":"cheap"}}`:     "",
		`{"provider":"openai"}`:                                    "",
		`{"model":"a","model":"b"}`:                                "", // top-level duplicates are not a routing object's
		`{"provider":{"order":["a"]},"provider":{"order":["b"]}}`:  "",
	} {
		require.Equal(t, want, DuplicateRoutingKey([]byte(in)), in)
	}
}
