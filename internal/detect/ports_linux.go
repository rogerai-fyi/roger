//go:build linux

package detect

import (
	"encoding/hex"
	"os"
	"strconv"
	"strings"
)

// listeningPorts enumerates the real TCP ports in LISTEN state on this host by
// reading the kernel's /proc/net/tcp (+ tcp6), with NO external process. We keep
// only ports bound to loopback or the wildcard address (0.0.0.0 / ::) - a model
// server you can reach on localhost - so detection never probes a remote peer it
// happened to see. The result is de-duplicated and bounded.
// procTCPPaths are the /proc files the enumerator reads. A package var so tests can
// point it at synthetic fixtures (the host's real open ports must not leak in).
var procTCPPaths = []string{"/proc/net/tcp", "/proc/net/tcp6"}

func listeningPorts() []int {
	seen := map[int]bool{}
	var out []int
	for _, path := range procTCPPaths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue // header / blank
			}
			f := strings.Fields(line)
			if len(f) < 4 {
				continue
			}
			// f[1] = local_address as HEXIP:HEXPORT, f[3] = connection state.
			if f[3] != "0A" { // 0x0A = TCP_LISTEN
				continue
			}
			ipHex, portHex, ok := strings.Cut(f[1], ":")
			if !ok {
				continue
			}
			if !localOrWildcardHex(ipHex) {
				continue
			}
			p, err := strconv.ParseInt(portHex, 16, 32)
			if err != nil || p <= 0 || p > 65535 {
				continue
			}
			if !seen[int(p)] {
				seen[int(p)] = true
				out = append(out, int(p))
				if len(out) >= maxEnumPorts {
					return out
				}
			}
		}
	}
	return out
}

// localOrWildcardHex reports whether a /proc/net local-address hex IP is loopback
// (127.0.0.1 / ::1) or the wildcard (0.0.0.0 / ::), i.e. reachable on localhost.
// /proc stores the IPv4 address as a little-endian 32-bit hex; IPv6 as 16 bytes.
func localOrWildcardHex(ipHex string) bool {
	raw, err := hex.DecodeString(ipHex)
	if err != nil {
		return false
	}
	switch len(raw) {
	case 4: // IPv4: /proc stores the address as a host (little-endian) uint32, so the
		// hex "0100007F" decodes to bytes {01,00,00,7F} and the FIRST network octet
		// (127 for loopback) lands in raw[3]. The wildcard 0.0.0.0 is all-zero.
		if raw[0] == 0 && raw[1] == 0 && raw[2] == 0 && raw[3] == 0 {
			return true // 0.0.0.0 wildcard
		}
		return raw[3] == 127 // 127.x.x.x loopback
	case 16: // IPv6
		// /proc/net/tcp6 stores the 16-byte address as FOUR 32-bit words, each in
		// HOST (little-endian) byte order. So for ::1 - whose network-order bytes are
		// 15 zeros then 0x01 - the low word (bytes 0xc..0xf) is the network-order
		// high-half 0x00000001, byte-swapped to little-endian => bytes 12,13,14,15 =
		// 01 00 00 00. The naive raw[15]==1 test therefore never matches the real
		// proc form. We restore network order by reversing each 4-byte word, then
		// match against the canonical addresses.
		net16 := make([]byte, 16)
		for w := 0; w < 4; w++ {
			net16[w*4+0] = raw[w*4+3]
			net16[w*4+1] = raw[w*4+2]
			net16[w*4+2] = raw[w*4+1]
			net16[w*4+3] = raw[w*4+0]
		}
		return ipv6IsLocalOrWildcard(net16)
	}
	return false
}

// ipv6IsLocalOrWildcard reports whether a 16-byte IPv6 address in NETWORK order is
// the wildcard (::), the loopback (::1), or an IPv4-mapped loopback (::ffff:127.x).
func ipv6IsLocalOrWildcard(ip []byte) bool {
	if len(ip) != 16 {
		return false
	}
	allZero := true
	for _, c := range ip {
		if c != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return true // :: wildcard
	}
	// ::1 loopback: 15 zero bytes then 0x01.
	loop := true
	for i := 0; i < 15; i++ {
		if ip[i] != 0 {
			loop = false
			break
		}
	}
	if loop && ip[15] == 1 {
		return true
	}
	// IPv4-mapped (::ffff:a.b.c.d): bytes 0..9 zero, 10,11 == 0xff, then the v4 in
	// 12..15. Loopback when the first v4 octet is 127 (127.0.0.0/8).
	mapped := true
	for i := 0; i < 10; i++ {
		if ip[i] != 0 {
			mapped = false
			break
		}
	}
	if mapped && ip[10] == 0xff && ip[11] == 0xff && ip[12] == 127 {
		return true
	}
	return false
}
