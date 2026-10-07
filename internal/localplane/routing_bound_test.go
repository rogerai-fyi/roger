package localplane

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestModelsBoundComesBeforeEntriesAreBuilt: an over-long models[] is refused without first
// building every entry (each a large object here), as the broker bounds raw entries.
func TestModelsBoundComesBeforeEntriesAreBuilt(t *testing.T) {
	entry := `{` + strings.Repeat(`"k":[1,2,3,4,5,6,7,8],`, 200) + `"z":0}`
	body := []byte(`{"model":"m","models":[` + strings.TrimSuffix(strings.Repeat(entry+",", maxModelsEntries+1), ",") + `]}`)
	_, rerr := parseLocalRouting(body, false)
	require.NotNil(t, rerr)
	require.Contains(t, rerr.msg, "more than")
	allocs := testing.AllocsPerRun(5, func() { _, _ = parseLocalRouting(body, false) })
	require.Less(t, allocs, 1000.0, "the entries were built before the bound was checked")
}
