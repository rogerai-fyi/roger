//go:build windows

package detect

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// listeningPorts enumerates the real TCP ports in LISTENING state on Windows via
// the system `netstat -ano -p TCP`, parsing the local-address column. We keep only
// loopback / wildcard binds (127.x, 0.0.0.0, ::, ::1). Bounded + timed out so a
// slow netstat can never stall detection.
func listeningPorts() []int {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "netstat", "-ano", "-p", "TCP")
	outBytes, err := cmd.Output()
	if err != nil {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	for _, line := range strings.Split(string(outBytes), "\n") {
		f := strings.Fields(line)
		// Layout: Proto  Local-Address  Foreign-Address  State  PID
		if len(f) < 4 || !strings.EqualFold(f[0], "TCP") {
			continue
		}
		if !strings.EqualFold(f[3], "LISTENING") {
			continue
		}
		host, portStr, ok := splitHostPortLast(f[1])
		if !ok || !localOrWildcardHost(host) {
			continue
		}
		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 || p > 65535 {
			continue
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
			if len(out) >= maxEnumPorts {
				return out
			}
		}
	}
	return out
}

// splitHostPortLast splits "host:port" on the LAST colon (so bracketed IPv6 and
// "0.0.0.0:port" both work), returning the host (brackets stripped) and the port.
func splitHostPortLast(s string) (host, port string, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", false
	}
	host = strings.Trim(s[:i], "[]")
	port = s[i+1:]
	return host, port, port != ""
}

// localOrWildcardHost reports whether a host string is the wildcard or loopback,
// i.e. reachable on localhost.
func localOrWildcardHost(host string) bool {
	switch host {
	case "*", "", "0.0.0.0", "::", "::1", "127.0.0.1":
		return true
	}
	return strings.HasPrefix(host, "127.")
}
