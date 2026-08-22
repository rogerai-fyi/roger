package update

import (
	"strings"
	"testing"
)

func TestNoticeAndAvailability(t *testing.T) {
	// up to date -> no banner
	if n := (CheckResult{Current: "0.1.0", Latest: "0.1.0"}).Notice(); n != "" {
		t.Errorf("up-to-date should produce no notice, got %q", n)
	}
	// newer available -> a banner naming both versions + the upgrade command
	n := (CheckResult{Current: "0.1.0", Latest: "0.2.0", Available: true}).Notice()
	for _, want := range []string{"v0.1.0", "v0.2.0", "roger upgrade"} {
		if !strings.Contains(n, want) {
			t.Errorf("notice %q missing %q", n, want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		cur, lat string
		want     bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.9", "0.2.0", true},
		{"0.2.0", "0.1.9", false}, // dev build ahead of latest must NOT advertise a downgrade
		{"0.1.0", "", false},
		{"1.0", "1.0.1", true},
		{"1.2.3", "1.2.3", false},

		// MAJOR BOUNDARIES. Every user on a 5.x build has to be told that 6.0.0 exists,
		// and the comparison is left-to-right, so the major decides before the minor is
		// ever read - which is exactly the case a table of 0.x and 1.x examples never
		// exercised. The downgrade direction matters just as much: 6.x must never be
		// offered 5.7.1 as an "update", or a release that lands late in the tag list
		// walks people backwards across a module-path change.
		{"5.7.1", "6.0.0", true},
		{"6.0.0", "5.7.1", false},
		{"5.9.9", "6.0.0", true}, // the minor rolling over must not out-rank the major
		{"6.0.0", "6.0.1", true},
		{"6.0.0", "6.0.0", false},
	}
	for _, c := range cases {
		if got := isNewer(c.cur, c.lat); got != c.want {
			t.Errorf("isNewer(%q,%q)=%v, want %v", c.cur, c.lat, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	if normalize("v1.2.3") != "1.2.3" || normalize(" 1.2.3 ") != "1.2.3" {
		t.Errorf("normalize failed: %q %q", normalize("v1.2.3"), normalize(" 1.2.3 "))
	}
}

func TestCachedNoticeOptOut(t *testing.T) {
	t.Setenv("ROGERAI_NO_UPDATE_CHECK", "1")
	if n := CachedNotice("0.1.0"); n != "" {
		t.Errorf("opt-out should return no notice, got %q", n)
	}
}

func TestAssetNameAndCachePath(t *testing.T) {
	if assetName() == "" {
		t.Error("assetName empty")
	}
	// The release assets are `roger-<os>-<arch>` (renamed from `rogerai-` in v4.7.0); an
	// `rogerai-` prefix here silently breaks `roger upgrade` on every platform.
	if !strings.HasPrefix(assetName(), "roger-") {
		t.Errorf("assetName() = %q, want the published 'roger-' release prefix", assetName())
	}
	if strings.HasPrefix(assetName(), "rogerai-") {
		t.Errorf("assetName() = %q uses the OLD 'rogerai-' prefix - no such release asset exists", assetName())
	}
	if cachePath() == "" {
		t.Error("cachePath empty")
	}
}
