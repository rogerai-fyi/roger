package tower

import "testing"

// Doctor judges a listener's loopback-ness by parsing the address with the one rule
// (internal/netpriv), not by matching a string prefix (one_binary.feature, doctor's loopback check).
func TestDoctorLoopbackParsesTheAddress(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8787", true},
		{"localhost:8787", true}, // loopback by definition (RFC 6761)
		{"127.1.2.3:8787", true},
		{"[::1]:8787", true},
		{"[0:0:0:0:0:0:0:1]:8787", true},  // ::1 written out: a prefix match missed it
		{"[::ffff:127.0.0.1]:8787", true}, // IPv4-mapped loopback
		{"127.example.net:8787", false},   // a NAME that starts with "127.": a prefix match called it loopback
		{"192.168.1.10:8787", false},
		{"0.0.0.0:8787", false},
	} {
		if got := isLoopback(tc.addr); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
