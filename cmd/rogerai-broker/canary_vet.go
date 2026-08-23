package main

import (
	"fmt"
	"net"
)

// vetPublicIP is the canary's answer to "may Roger Core dial this?". Everything that is
// not publicly routable is refused: loopback, RFC1918, link-local (which includes the
// cloud metadata service), unspecified, multicast, and their IPv6 and v4-mapped forms.
// The stdlib predicates cover each range; the job here is refusing on ANY of them and
// naming which one, so a skipped canary is explicable from one log line.
func vetPublicIP(ip net.IP) error {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLoopback():
		return fmt.Errorf("%s is loopback", ip)
	case ip.IsPrivate():
		return fmt.Errorf("%s is a private range", ip)
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return fmt.Errorf("%s is link-local (the metadata service lives here)", ip)
	case ip.IsUnspecified():
		return fmt.Errorf("%s is unspecified", ip)
	case ip.IsMulticast():
		return fmt.Errorf("%s is multicast", ip)
	}
	return nil
}

// hostOf is SplitHostPort that answers just the host, and the empty string for input it
// cannot read - the caller treats that as "not a literal IP" and lets the dial-time vet
// judge the resolved addresses instead.
func hostOf(endpoint string) string {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return ""
	}
	return host
}
