//go:build windows

package detect

import "testing"

// TestSplitHostPortLast covers the netstat local-address host:port split: split on
// the LAST colon (bracketed IPv6 + "0.0.0.0:port" both parse), brackets stripped.
func TestSplitHostPortLast(t *testing.T) {
	cases := []struct {
		in         string
		host, port string
		ok         bool
	}{
		{"127.0.0.1:11434", "127.0.0.1", "11434", true},
		{"0.0.0.0:8080", "0.0.0.0", "8080", true},
		{"[::1]:1234", "::1", "1234", true},
		{"[::]:443", "::", "443", true},
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

// TestLocalOrWildcardHost: only loopback/wildcard hosts are locally reachable.
func TestLocalOrWildcardHost(t *testing.T) {
	local := []string{"*", "", "0.0.0.0", "::", "::1", "127.0.0.1", "127.0.0.53"}
	for _, h := range local {
		if !localOrWildcardHost(h) {
			t.Errorf("localOrWildcardHost(%q) = false, want true", h)
		}
	}
	remote := []string{"192.168.1.10", "10.0.0.2", "8.8.8.8", "2001:db8::1"}
	for _, h := range remote {
		if localOrWildcardHost(h) {
			t.Errorf("localOrWildcardHost(%q) = true, want false", h)
		}
	}
}
