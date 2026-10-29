package buildinfo

import (
	"regexp"
	"testing"
)

// The unstamped fallback is the release this source is: shaped like a tag (no leading v), so a
// build nobody stamps still compares correctly against the newest release.
func TestFallbackIsAReleaseVersion(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`).MatchString(Version) {
		t.Fatalf("Version %q is not a release version", Version)
	}
}
