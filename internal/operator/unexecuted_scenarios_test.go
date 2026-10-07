package operator

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pendingElsewhere is every @tui / @broker scenario in these two feature files: their runners
// exclude those tags and no other runner loads these files, so none of them executes yet. They
// are approved behaviour that is not built (no broker code logs an unknown OpenRouter slug,
// the desk has no profile-aware handoff frame or return summary). Listing them keeps them from
// being skipped in silence: adding or removing such a scenario fails this test until the list
// (and the PR that changes it) says so. It does not detect a scenario that starts running.
var pendingElsewhere = []string{
	"cli/profiles.feature: The TUI, CLI and proxy resolve the same profile to the same body object",
	"operator/guest_routing.feature: An OpenRouter `models` list with slugs no station serves is skipped model by model",
	"operator/guest_routing.feature: Detection and the desk strip are unaffected by profiles",
	"operator/guest_routing.feature: OpenRouter provider slugs in order match no node id and fall back to normal scoring",
	"operator/guest_routing.feature: OpenRouter provider slugs with allow_fallbacks:false is an honest 503 no_match",
	"operator/guest_routing.feature: OpenRouter's `route: \"fallback\"` is passed through and refused as unknown by the broker",
	"operator/guest_routing.feature: OpenRouter's `transforms` and `plugins` are ordinary passthrough keys",
	"operator/guest_routing.feature: PROPOSED - r is a plate key only, at the ask prompt it just types",
	"operator/guest_routing.feature: PROPOSED - the plate lets the DJ pick a profile before handing the mic",
	"operator/guest_routing.feature: The handoff frame names the served model and node from the chunk, never guest content",
	"operator/guest_routing.feature: The summary on return names the profile the guest ran under",
}

// TestNoTagExcludedScenarioIsSilentlyUnexecuted lists the @tui / @broker scenarios of the
// files whose runners exclude those tags, and holds them to pendingElsewhere.
func TestNoTagExcludedScenarioIsSilentlyUnexecuted(t *testing.T) {
	tagged := regexp.MustCompile(`(?m)^\s*@(?:tui|broker)\s*\n\s*Scenario(?: Outline)?: (.+)$`)
	var got []string
	for _, f := range []string{"cli/profiles.feature", "operator/guest_routing.feature"} {
		b, err := os.ReadFile("../../features/" + f)
		require.NoError(t, err)
		for _, m := range tagged.FindAllStringSubmatch(string(b), -1) {
			got = append(got, f+": "+strings.TrimSpace(m[1]))
		}
	}
	sort.Strings(got)
	want := append([]string(nil), pendingElsewhere...)
	sort.Strings(want)
	require.Equal(t, want, got)
}
