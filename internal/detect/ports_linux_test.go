//go:build linux

package detect

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLocalOrWildcardHex is the P1-1 regression: the /proc/net loopback hex matcher
// must correctly recognize the REAL proc forms - including ::1, whose word-swapped
// little-endian encoding puts the 0x01 at byte index 12 (not 15, which the old
// raw[15]==1 test wrongly checked) - plus IPv4 loopback/wildcard and IPv4-mapped
// loopback, while rejecting real non-local addresses.
func TestLocalOrWildcardHex(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		want bool
	}{
		// IPv4 (little-endian 32-bit). 0100007F = 127.0.0.1; 00000000 = 0.0.0.0.
		{"ipv4 loopback 127.0.0.1", "0100007F", true},
		{"ipv4 loopback 127.1.2.3", "0302017F", true}, // 127.x.x.x
		{"ipv4 wildcard 0.0.0.0", "00000000", true},
		{"ipv4 LAN 192.168.1.10", "0A01A8C0", false},
		{"ipv4 public 1.2.3.4", "04030201", false},
		// IPv6 (four 32-bit words, each host/little-endian byte order).
		// ::1 real proc form: 15 zero bytes + 0x01, word-swapped => 0x01 at index 12.
		{"ipv6 ::1 loopback (real proc form)", "00000000000000000000000001000000", true},
		// :: wildcard: all zero.
		{"ipv6 :: wildcard", "00000000000000000000000000000000", true},
		// ::ffff:127.0.0.1 (IPv4-mapped loopback): per-word little-endian storage of
		// net bytes 00..00 ffff 7f000001 => word2 "ffff0000", word3 "0100007f".
		{"ipv6 ::ffff:127.0.0.1 mapped loopback", "0000000000000000ffff00000100007f", true},
		// A real global IPv6 (2001:db8::1) must be rejected.
		{"ipv6 global 2001:db8::1", "B80D0120000000000000000001000000", false},
		// ::ffff:8.8.8.8 (mapped, NOT loopback) must be rejected.
		{"ipv6 ::ffff:8.8.8.8 mapped public", "0000000000000000ffff000008080808", false},
		// Garbage hex -> false (not panic).
		{"bad hex", "zzzz", false},
	}
	for _, c := range cases {
		if got := localOrWildcardHex(c.hex); got != c.want {
			t.Errorf("%s: localOrWildcardHex(%q) = %v, want %v", c.name, c.hex, got, c.want)
		}
	}
}

// TestListeningPortsProcFixture parses a synthetic /proc/net/tcp(6) behind the
// injectable procTCPPaths var: only LISTEN (0A) sockets bound to loopback/wildcard
// are returned; an ESTABLISHED socket and a LAN-bound listener are skipped.
func TestListeningPortsProcFixture(t *testing.T) {
	dir := t.TempDir()
	// columns: sl local_address rem_address st ... (we use the first four).
	// 0100007F:1F90 = 127.0.0.1:8080 LISTEN(0A) -> keep.
	// 00000000:23F0 = 0.0.0.0:9200 LISTEN -> keep (wildcard).
	// 0A01A8C0:0050 = 192.168.1.10:80 LISTEN -> skip (LAN).
	// 0100007F:D431 = 127.0.0.1:54321 ESTABLISHED(01) -> skip (not listening).
	tcp4 := "  sl  local_address rem_address   st tx_queue rx_queue\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000\n" +
		"   1: 00000000:23F0 00000000:0000 0A 00000000:00000000\n" +
		"   2: 0A01A8C0:0050 00000000:0000 0A 00000000:00000000\n" +
		"   3: 0100007F:D431 0100007F:1F90 01 00000000:00000000\n"
	// tcp6: ::1:1234 LISTEN -> keep (the P1-1 form).
	tcp6 := "  sl  local_address                         remote_address                        st\n" +
		"   0: 00000000000000000000000001000000:04D2 00000000000000000000000000000000:0000 0A\n"

	p4 := filepath.Join(dir, "tcp")
	p6 := filepath.Join(dir, "tcp6")
	if err := os.WriteFile(p4, []byte(tcp4), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p6, []byte(tcp6), 0o644); err != nil {
		t.Fatal(err)
	}
	old := procTCPPaths
	procTCPPaths = []string{p4, p6}
	defer func() { procTCPPaths = old }()

	got := listeningPorts()
	want := map[int]bool{8080: true, 9200: true, 0x04D2: true} // 0x04D2 == 1234
	if len(got) != len(want) {
		t.Fatalf("ports = %v, want exactly %v", got, []int{8080, 9200, 1234})
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected port %d (LAN / non-listening must be skipped)", p)
		}
	}
}
