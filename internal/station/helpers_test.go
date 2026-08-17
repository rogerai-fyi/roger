package station

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// initStation is the shared test fixture: a Station identity in a temp dir.
func initStation(t *testing.T) *Station {
	t.Helper()
	s, err := Init(t.TempDir())
	require.NoError(t, err)
	return s
}
