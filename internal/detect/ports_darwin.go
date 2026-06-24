//go:build darwin

package detect

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// listeningPorts enumerates the real TCP ports in LISTEN state on macOS via the
// system `lsof`, which lists open files including listening sockets without
// needing root for the current user's own processes. We parse the NAME column's
// trailing ":PORT" and keep only loopback / wildcard binds. Bounded + timed out
// so a slow lsof can never stall detection.
func listeningPorts() []int {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	// -nP: numeric host + port (no DNS / service-name lookups). -iTCP -sTCP:LISTEN:
	// only TCP sockets in LISTEN state. -Fn: machine-readable, one field per line.
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fn")
	outBytes, err := cmd.Output()
	if err != nil {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	for _, line := range strings.Split(string(outBytes), "\n") {
		if len(line) == 0 || line[0] != 'n' { // -Fn name records begin with 'n'
			continue
		}
		name := line[1:] // e.g. "127.0.0.1:11434" or "*:8080" or "[::1]:1234"
		host, portStr, ok := splitHostPortLast(name)
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
// "*:port" both work), returning the host (brackets stripped) and the port.
func splitHostPortLast(s string) (host, port string, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", false
	}
	host = strings.Trim(s[:i], "[]")
	port = s[i+1:]
	return host, port, port != ""
}

// localOrWildcardHost reports whether a host string is the wildcard ("*", empty,
// 0.0.0.0, ::) or loopback (127.x / ::1), i.e. reachable on localhost.
func localOrWildcardHost(host string) bool {
	switch host {
	case "*", "", "0.0.0.0", "::", "::1", "127.0.0.1":
		return true
	}
	return strings.HasPrefix(host, "127.")
}
