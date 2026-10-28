package modelview

// The console reads these shapes by their JSON names (internal/webui/assets/console.js), so a
// renamed field would silently blank a row. The names are pinned here.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTheNamesTheConsoleReadsArePinned(t *testing.T) {
	v := View{Budget: 1, Pool: "system memory", Runtime: "r", CanServe: true, Dir: "/d", DiskUsed: 2,
		Models:  []Row{{ID: "a", Band: "b", Serving: true, Verified: true}},
		Serving: &Serving{Band: "b", ID: "a", Ctx: 8192, Since: time.Unix(0, 0)},
		Also:    []Serving{{Band: "c"}},
		Job:     &FetchJob{Repo: "r", File: "f", RemainingS: 3, State: "fetching", Fix: "x"},
	}
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	for _, k := range []string{"budget", "pool", "runtime", "can_serve", "models", "disk_used", "dir", "serving", "also", "job"} {
		require.Contains(t, got, k)
	}
	require.Equal(t, "fetching", got["job"].(map[string]any)["state"])
	require.EqualValues(t, 3, got["job"].(map[string]any)["remaining_s"])

	c, err := json.Marshal(FileChoice{Label: "Q4", Fit: "fits", Short: "s", Line: "l", Pace: "p", PaceLine: "pl", Room: "rm", Suggested: true, Have: true})
	require.NoError(t, err)
	var fc map[string]any
	require.NoError(t, json.Unmarshal(c, &fc))
	for _, k := range []string{"label", "fit", "short", "line", "pace", "pace_line", "room", "suggested", "have"} {
		require.Contains(t, fc, k)
	}

	// Empty optional facts are left out rather than sent as blanks the page would print.
	e, _ := json.Marshal(View{})
	require.NotContains(t, string(e), `"serving"`)
	require.NotContains(t, string(e), `"also"`)
}
