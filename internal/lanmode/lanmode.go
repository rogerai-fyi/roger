// Package lanmode is LAN mode's one source of truth (features/lan/): whether this process
// runs in LAN mode and why, what counts as a LOCAL address, and how a standalone Tower is
// recognised. Every surface (CLI, TUI, console, client) asks this package rather than keeping
// its own copy, because a mode two surfaces disagree about is the bug that makes the feature
// a lie.
package lanmode

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/netpriv"
)

// DefaultTowerPort is the standalone Tower's default consumer port (cmd/roger-tower-local).
const DefaultTowerPort = "8787"

// Source names where the mode came from, for `roger status`.
type Source string

const (
	SrcDefault Source = "default"
	SrcConfig  Source = "config"
	SrcEnv     Source = "env"
	SrcFlag    Source = "flag"
)

// Mode is the decided mode of one process.
type Mode struct {
	LAN    bool
	Source Source
}

// ParseEnv reads ROGER_LAN. Anything it does not recognise is an error, never a guess.
func ParseEnv(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("ROGER_LAN=%q is not understood: use 1, true, yes or on to turn LAN mode on, 0, false, no or off to turn it off", v)
}

// ConfigMode validates a `roger config set mode` value: "lan", or "default" (stored as unset).
func ConfigMode(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "lan":
		return "lan", nil
	case "default", "":
		return "", nil
	}
	return "", fmt.Errorf("unknown mode %q: use lan or default", v)
}

// Decide applies the one precedence: flag, then environment, then config.
func Decide(flagLAN, flagNoLAN bool, env string, envSet bool, cfgMode string) (Mode, error) {
	if flagLAN && flagNoLAN {
		return Mode{}, fmt.Errorf("--lan and --no-lan conflict: pass one")
	}
	if flagLAN {
		return Mode{LAN: true, Source: SrcFlag}, nil
	}
	if flagNoLAN {
		return Mode{LAN: false, Source: SrcFlag}, nil
	}
	if envSet {
		on, err := ParseEnv(env)
		if err != nil {
			return Mode{}, err
		}
		return Mode{LAN: on, Source: SrcEnv}, nil
	}
	m, err := ConfigMode(cfgMode)
	if err != nil {
		return Mode{}, fmt.Errorf("config: %w", err)
	}
	if m == "lan" {
		return Mode{LAN: true, Source: SrcConfig}, nil
	}
	return Mode{Source: SrcDefault}, nil
}

// IsLocalIP is LAN mode's local check: the one rule (internal/netpriv) with CGNAT excluded,
// which is not local unless the owner added it by hand (ruled 2026-10-06).
func IsLocalIP(ip net.IP) bool { return netpriv.IsLocal(ip, false) }

// Addr is a parsed Tower address: a host (a literal IP, possibly zoned, or a name) and a port.
type Addr struct {
	Host string
	Port string
	ip   netip.Addr
}

// Literal reports whether the host is an IP literal (never resolved).
func (a Addr) Literal() bool { return a.ip.IsValid() }

// String is host:port, with IPv6 bracketed.
func (a Addr) String() string { return net.JoinHostPort(a.Host, a.Port) }

// Base is the http base URL for the Tower's consumer plane.
func (a Addr) Base() string {
	u := url.URL{Scheme: "http", Host: a.String()}
	return u.String()
}

// ParseAddr reads "host", "host:port" or an http URL. A link-local IPv6 address must carry its
// zone, because without one the kernel cannot know which interface to use.
func ParseAddr(s string) (Addr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Addr{}, fmt.Errorf("an address is required, e.g. 192.168.1.20:%s", DefaultTowerPort)
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return Addr{}, fmt.Errorf("%q is not an address", s)
		}
		s = u.Host
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		host, port = strings.Trim(s, "[]"), DefaultTowerPort
	}
	if p, perr := netip.ParseAddrPort(net.JoinHostPort("0.0.0.0", port)); perr != nil || p.Port() == 0 {
		return Addr{}, fmt.Errorf("%q has no usable port", s)
	}
	a := Addr{Host: host, Port: port}
	if ip, perr := netip.ParseAddr(host); perr == nil {
		if ip.Is6() && !ip.Is4In6() && ip.IsLinkLocalUnicast() && ip.Zone() == "" {
			return Addr{}, fmt.Errorf("a link-local IPv6 address needs its interface, for example \"[%s%%eth0]:%s\"", host, port)
		}
		a.ip = ip
	} else if host == "" {
		return Addr{}, fmt.Errorf("%q has no host", s)
	}
	return a, nil
}

// Verdict is the result of classifying an address.
type Verdict struct {
	Local  bool
	CGNAT  bool   // in 100.64.0.0/10: not local unless the owner added it by hand
	Reason string // why it is not local; empty when it is
}

// LocalRanges is the human list of local ranges, for refusals.
const LocalRanges = "loopback, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7 and link-local"

func classifyIP(a netip.Addr) Verdict {
	if a.Unmap() == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return Verdict{Reason: "a broadcast or multicast address is never a Tower"}
	}
	switch netpriv.ClassifyAddr(a) {
	case netpriv.Unspecified:
		return Verdict{Reason: "an unspecified address is never a Tower"}
	case netpriv.Multicast:
		return Verdict{Reason: "a broadcast or multicast address is never a Tower"}
	case netpriv.Loopback, netpriv.Private, netpriv.ULA, netpriv.LinkLocal:
		return Verdict{Local: true}
	case netpriv.CGNAT:
		return Verdict{CGNAT: true, Reason: "a carrier/overlay (CGNAT) address is not local unless added by hand"}
	}
	return Verdict{Reason: "a public address: LAN mode only uses " + LocalRanges}
}

// ClassifyLiteral classifies an address string whose host is an IP literal; a hostname or an
// unparsable address is reported as not local (Classify resolves names).
func ClassifyLiteral(s string) Verdict {
	a, err := ParseAddr(s)
	if err != nil {
		return Verdict{Reason: err.Error()}
	}
	if !a.Literal() {
		return Verdict{Reason: "a hostname must be resolved to be classified"}
	}
	return classifyIP(a.ip)
}

// Resolver looks a hostname up. net.DefaultResolver.LookupIP-shaped.
type Resolver func(ctx context.Context, host string) ([]net.IP, error)

// DefaultResolver resolves with the system resolver.
func DefaultResolver(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// Classify classifies an address; a hostname is local only when EVERY address it resolves to
// is local. Callers classify again at every connection: a name is never cached as local.
func Classify(ctx context.Context, a Addr, resolve Resolver) Verdict {
	if a.Literal() {
		return classifyIP(a.ip)
	}
	if resolve == nil {
		resolve = DefaultResolver
	}
	ips, err := resolve(ctx, a.Host)
	if err != nil {
		return Verdict{Reason: fmt.Sprintf("%s does not resolve: %v", a.Host, err)}
	}
	if len(ips) == 0 {
		return Verdict{Reason: a.Host + " resolves to no address"}
	}
	for _, ip := range ips {
		na, ok := netip.AddrFromSlice(ip)
		if !ok || !classifyIP(na).Local {
			return Verdict{Reason: a.Host + " resolves to a public address (" + ip.String() + ")"}
		}
	}
	return Verdict{Local: true}
}

// ProbeTower reports whether base answers as a standalone Tower: an unsigned POST to
// /local/poll gets the plane's UNIFORM 401, {"error":"unauthorized"}. Matching that exact
// answer (not merely "not a 404") tells a Tower apart from a public broker (no such route) and
// from a dev server that catch-alls a 200. hc nil uses a 3 s client.
func ProbeTower(ctx context.Context, hc *http.Client, base string) bool {
	return Probe(ctx, hc, base) == ProbeTowerYes
}

// ProbeResult is what an address is, as far as /local/poll can tell.
type ProbeResult int

const (
	ProbeNoAnswer ProbeResult = iota // nothing answered (refused, timed out, bad URL)
	ProbeNotTower                    // something answered, but not a standalone Tower
	ProbeTowerYes                    // the standalone Tower's uniform 401
)

// Probe classifies base by its answer to an unsigned /local/poll.
func Probe(ctx context.Context, hc *http.Client, base string) ProbeResult {
	if hc == nil {
		hc = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/local/poll", nil)
	if err != nil {
		return ProbeNoAnswer
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return ProbeNoAnswer
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode == http.StatusUnauthorized && strings.Contains(string(body), `"unauthorized"`) {
		return ProbeTowerYes
	}
	return ProbeNotTower
}
