//go:build linux

package detect

import (
	"encoding/binary"
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
func listeningPorts() []int {
	seen := map[int]bool{}
	var out []int
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
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
	case 4: // IPv4, little-endian
		v := binary.LittleEndian.Uint32(raw)
		return v == 0 || (byte(v) == 127) // 0.0.0.0 wildcard, or 127.x.x.x loopback
	case 16: // IPv6
		allZero := true
		for _, c := range raw {
			if c != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return true // :: wildcard
		}
		// ::1 loopback: /proc stores each 32-bit word little-endian; the loopback
		// bit lands in the last byte.
		for i := 0; i < 15; i++ {
			if raw[i] != 0 {
				return false
			}
		}
		return raw[15] == 1
	}
	return false
}
