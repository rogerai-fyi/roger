package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// errNotPublic marks an endpoint that is UNREACHABLE BY DESIGN - loopback, LAN, or
// link-local - as opposed to one that is broken. The distinction is a Tower's
// reputation: a home-lab Tower advertising its LAN name is exactly what it says it is,
// and recording its canary as a FAILURE (which repeated, suspends) would punish the
// configuration this network explicitly supports.
var errNotPublic = errors.New("endpoint is not publicly routable")

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
		return fmt.Errorf("%w: %s is loopback", errNotPublic, ip)
	case ip.IsPrivate():
		return fmt.Errorf("%w: %s is a private (LAN) range", errNotPublic, ip)
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return fmt.Errorf("%w: %s is link-local (the metadata service lives here)", errNotPublic, ip)
	case ip.IsUnspecified():
		return fmt.Errorf("%w: %s is unspecified", errNotPublic, ip)
	case ip.IsMulticast():
		return fmt.Errorf("%w: %s is multicast", errNotPublic, ip)
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

// endpointNotPublic answers whether an advertised endpoint is unreachable by design,
// resolving a hostname the way the dialer will. A name that does not resolve is NOT
// "not public" - it may be broken, and broken is the canary's business to discover.
func endpointNotPublic(ctx context.Context, endpoint string, vet func(net.IP) error) error {
	if vet == nil {
		return nil // the test seam: no vet, no design skips
	}
	host := hostOf(endpoint)
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return vet(ip)
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(rctx, "ip", host)
	if err != nil {
		return nil // unresolvable = possibly broken, the drive's problem, not a design skip
	}
	for _, ip := range ips {
		if verr := vet(ip); verr != nil {
			return fmt.Errorf("%w: %s resolves to a non-public address: %v", errNotPublic, host, verr)
		}
	}
	return nil
}
