package harness

import "testing"

// The audit's exact over-match cases, plus the shapes that must keep working.
func TestGlobMatchAnchorsOnSegmentBoundaries(t *testing.T) {
	for _, c := range []struct {
		pat, rel string
		want     bool
	}{
		{"cmd/**", "cmd/roger/main.go", true},
		{"cmd/**", "internal/cmdx/f.go", false}, // "cmd" inside a segment is not cmd/
		{"cmd/**", "mycmd/f.go", false},
		{"src/**", "notsrc/deep/f.go", false},
		{"**/*.go", "a.go", true},
		{"**/*.go", "sub/deep/c.go", true},
		{"**/*.go", "sub/deep/c.md", false},
		{"*.go", "sub/deep/c.go", true}, // bare name matches the basename
		{"cmd/*/main.go", "cmd/roger/main.go", true},
		{"cmd/*/main.go", "cmd/a/b/main.go", false},
		{"a/**/b/*.go", "a/x/y/b/f.go", true},
		{"a/**/b/*.go", "a/x/y/nb/f.go", false},
	} {
		if got := globMatch(c.pat, c.rel); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pat, c.rel, got, c.want)
		}
	}
}
