//go:build darwin

package detect

import "testing"

// TestSplitHostPortLast covers the lsof NAME-column host:port split: it must split
// on the LAST colon so bracketed IPv6 and "*:port" both parse, stripping brackets.
func TestSplitHostPortLast(t *testing.T) {
	cases := []struct {
		in         string
		host, port string
		ok         bool
	}{
		{"127.0.0.1:11434", "127.0.0.1", "11434", true},
		{"*:8080", "*", "8080", true},
		{"[::1]:1234", "::1", "1234", true},
		{"[::]:443", "::", "443", true},
		{"0.0.0.0:9000", "0.0.0.0", "9000", true},
		{"nocolon", "", "", false},
		{"trailing:", "trailing", "", false},
	}
	for _, c := range cases {
		h, p, ok := splitHostPortLast(c.in)
		if ok != c.ok || (ok && (h != c.host || p != c.port)) {
			t.Errorf("splitHostPortLast(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, h, p, ok, c.host, c.port, c.ok)
		}
	}
}

// TestLocalOrWildcardHost: only loopback/wildcard hosts count as locally reachable;
// a LAN/public host is rejected so detection never probes a remote peer.
func TestLocalOrWildcardHost(t *testing.T) {
	local := []string{"*", "", "0.0.0.0", "::", "::1", "127.0.0.1", "127.0.0.53"}
	for _, h := range local {
		if !localOrWildcardHost(h) {
			t.Errorf("localOrWildcardHost(%q) = false, want true", h)
		}
	}
	remote := []string{"192.168.1.10", "10.0.0.2", "8.8.8.8", "2001:db8::1", "example.com"}
	for _, h := range remote {
		if localOrWildcardHost(h) {
			t.Errorf("localOrWildcardHost(%q) = true, want false", h)
		}
	}
}
