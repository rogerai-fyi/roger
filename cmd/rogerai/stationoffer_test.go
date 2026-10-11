package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A station's output names a few refused models but counts many, and never lists what it skipped.
func TestStationOfferLinesStayShort(t *testing.T) {
	require.Equal(t, "the Tower does not accept: a, b", refusedLine([]string{"a", "b"}))
	many := refusedLine(strings.Split("a b c d e f g h i j k l m n o p q r s t u v w x y z aa bb cc dd ee ff gg", " "))
	require.Equal(t, "the Tower does not accept 33 of the offered models (its invite names the ones it takes)", many)
	require.Equal(t, "skipped 1 other model on this machine; name one with --models to offer it", skippedLine(1))
	require.Equal(t, "skipped 33 other models on this machine; name one with --models to offer it", skippedLine(33))
}
