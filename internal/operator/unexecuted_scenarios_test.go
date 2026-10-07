package operator

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pendingElsewhere is every scenario, in the files below, whose tag the file's runners
// exclude and no other runner executes: approved behaviour that is not built here (no broker
// code logs an unknown OpenRouter slug, the desk has no profile-aware handoff frame or return
// summary, the OpenAPI document and the harness strip are other work). Listing them keeps them
// from being skipped in silence: adding or removing such a scenario fails this test until the
// list (and the PR that changes it) says so. It does not detect a scenario that starts running.
var pendingElsewhere = []string{
	"cli/profiles.feature: The TUI, CLI and proxy resolve the same profile to the same body object",
	"routing/open_network_filters.feature: OpenAPI documents every filter with its unknown-attribute rule",
	"routing/open_network_filters.feature: The quant filter replaces the TUI's exclude-list simulation and binds in standalone roger use",
	"routing/request_shape.feature: The strip happens on the agent-harness relay path",
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

// excludedBy is each scanned file and the tags its runners skip that no other runner runs.
var excludedBy = map[string][]string{
	"cli/profiles.feature":                 {"tui"},
	"operator/guest_routing.feature":       {"tui", "broker"},
	"routing/open_network_filters.feature": {"cli", "proxy", "harness", "docs"},
	"routing/request_shape.feature":        {"cli", "proxy", "harness", "docs"},
}

// TestNoTagExcludedScenarioIsSilentlyUnexecuted holds the excluded scenarios of these files to
// pendingElsewhere.
func TestNoTagExcludedScenarioIsSilentlyUnexecuted(t *testing.T) {
	tagged := regexp.MustCompile(`(?m)^\s*((?:@[\w-]+\s*)+)\n\s*Scenario(?: Outline)?: (.+)$`)
	var got []string
	for f, tags := range excludedBy {
		b, err := os.ReadFile("../../features/" + f)
		require.NoError(t, err)
		for _, m := range tagged.FindAllStringSubmatch(string(b), -1) {
			for _, tg := range strings.Fields(m[1]) {
				if slices.Contains(tags, strings.TrimPrefix(tg, "@")) {
					got = append(got, f+": "+strings.TrimSpace(m[2]))
					break
				}
			}
		}
	}
	sort.Strings(got)
	want := append([]string(nil), pendingElsewhere...)
	sort.Strings(want)
	require.Equal(t, want, got)
}
