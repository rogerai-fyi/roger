package harness

import "testing"

func TestJoinExcludesTrimsDedupsAndSorts(t *testing.T) {
	for name, tc := range map[string]struct {
		in   []string
		want string
	}{
		"empty":        {nil, ""},
		"blanks only":  {[]string{"", "  "}, ""},
		"dedup + sort": {[]string{"b", " a ", "b", "", "a"}, "a,b"},
		"single":       {[]string{"node-1"}, "node-1"},
	} {
		if got := joinExcludes(tc.in); got != tc.want {
			t.Errorf("%s: joinExcludes(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}
