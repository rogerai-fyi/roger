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
	"sync"
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

// Mode is the decided mode of one process. EdgeOnly is LAN mode's strict subset
// (features/lan/edge_only.feature): Edge members and this machine, never a Tower. It is only
// ever true with LAN.
type Mode struct {
	LAN      bool
	EdgeOnly bool
	Source   Source
}

// onOff reads a boolean environment value. Anything it does not recognise is an error, never a
// guess; name is the variable the error names.
func onOff(name, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s=%q is not understood: use 1, true, yes or on to turn it on, 0, false, no or off to turn it off", name, v)
}

// ParseEnv reads ROGER_LAN.
func ParseEnv(v string) (bool, error) { return onOff("ROGER_LAN", v) }

// ParseEdgeOnlyEnv reads ROGER_LAN_EDGE_ONLY.
func ParseEdgeOnlyEnv(v string) (bool, error) { return onOff("ROGER_LAN_EDGE_ONLY", v) }

// ConfigMode validates a `roger config set mode` value: "lan", "lan-edge-only", or "default"
// (stored as unset). One key with three values cannot contradict itself (ruled 2026-10-07).
func ConfigMode(v string) (string, error) {
	switch m := strings.ToLower(strings.TrimSpace(v)); m {
	case "lan", "lan-edge-only":
		return m, nil
	case "default", "":
		return "", nil
	}
	return "", fmt.Errorf("unknown mode %q: use lan, lan-edge-only or default", v)
}

// Inputs is everything one process's mode is decided from.
type Inputs struct {
	FlagLAN, FlagNoLAN, FlagEdgeOnly bool
	EnvLAN                           string
	EnvLANSet                        bool
	EnvEdgeOnly                      string
	EnvEdgeOnlySet                   bool
	Config                           string // the config's "mode"
}

// Decide applies the one precedence, flag, then environment, then config, to LAN mode and
// edge-only together. --edge-only implies --lan; --lan alone (or ROGER_LAN=1) is LAN without
// edge-only; ROGER_LAN_EDGE_ONLY=0 turns edge-only off and leaves LAN as the rest decides it.
func Decide(in Inputs) (Mode, error) {
	switch {
	case in.FlagEdgeOnly && in.FlagNoLAN:
		return Mode{}, fmt.Errorf("--edge-only and --no-lan conflict: pass one")
	case in.FlagLAN && in.FlagNoLAN:
		return Mode{}, fmt.Errorf("--lan and --no-lan conflict: pass one")
	case in.FlagEdgeOnly:
		return Mode{LAN: true, EdgeOnly: true, Source: SrcFlag}, nil
	case in.FlagLAN:
		return Mode{LAN: true, Source: SrcFlag}, nil
	case in.FlagNoLAN:
		return Mode{Source: SrcFlag}, nil
	}
	// The config is read only when it decides: a flag or the environment never consults it.
	config := func() (string, error) {
		m, err := ConfigMode(in.Config)
		if err != nil {
			return "", fmt.Errorf("config: %w", err)
		}
		return m, nil
	}
	var lanEnv, lanEnvOn bool
	if in.EnvLANSet {
		on, err := ParseEnv(in.EnvLAN)
		if err != nil {
			return Mode{}, err
		}
		lanEnv, lanEnvOn = true, on
	}
	if in.EnvEdgeOnlySet {
		on, err := ParseEdgeOnlyEnv(in.EnvEdgeOnly)
		if err != nil {
			return Mode{}, err
		}
		if on {
			if lanEnv && !lanEnvOn {
				return Mode{}, fmt.Errorf("ROGER_LAN_EDGE_ONLY=1 and ROGER_LAN=0 conflict: unset one")
			}
			return Mode{LAN: true, EdgeOnly: true, Source: SrcEnv}, nil
		}
		if lanEnv {
			return Mode{LAN: lanEnvOn, Source: SrcEnv}, nil
		}
		cfg, err := config()
		if err != nil {
			return Mode{}, err
		}
		return Mode{LAN: cfg != "", Source: SrcEnv}, nil
	}
	if lanEnv {
		return Mode{LAN: lanEnvOn, Source: SrcEnv}, nil
	}
	cfg, err := config()
	if err != nil {
		return Mode{}, err
	}
	switch cfg {
	case "lan-edge-only":
		return Mode{LAN: true, EdgeOnly: true, Source: SrcConfig}, nil
	case "lan":
		return Mode{LAN: true, Source: SrcConfig}, nil
	}
	return Mode{Source: SrcDefault}, nil
}

// WantsNonce is THE nonce decision (features/packaging/one_binary.feature section 7.6): whether
// a signed request carries a replay nonce. Every standalone Tower gets one, loopback included
// (ruled 2026-10-07):
//   - plaintext http to a non-loopback address the one rule (internal/netpriv) calls local:
//     RFC 1918, ULA and link-local, zoned IPv6 included; or
//   - plaintext http to a Tower in known (host:port, a bare host, or an http URL): one the owner
//     added (`roger lan add`, how a CGNAT, loopback or named Tower is known) or the Tower a share
//     station was pointed at; or one this process has confirmed (Check, NoteTower).
//
// A loopback address is not assumed to be a Tower: other services there (a Core-joined Tower's
// hub, a test broker) verify signatures without a nonce. https (the public broker) and any
// public host never get one. Both signers ask this: the client's signWithKey and the share
// station's signLocal.
func WantsNonce(u *url.URL, known []string) bool {
	if u == nil || u.Scheme != "http" {
		return false
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "80"
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		switch netpriv.ClassifyAddr(ip) {
		case netpriv.Private, netpriv.ULA, netpriv.LinkLocal:
			return true
		}
	}
	if towerSeenAt(host, port) {
		return true
	}
	for _, a := range known {
		if t, err := ParseAddr(a); err == nil && strings.EqualFold(t.Host, host) && t.Port == port {
			return true
		}
	}
	return false
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
	_, v := Resolve(ctx, a, resolve)
	return v
}

// Resolve classifies a with ONE lookup and returns the host:port to connect to: the literal
// itself, or the first address that lookup returned (every one of them judged local). A caller
// connects to what Resolve returns and never to the name again, so a name cannot flip to a
// public address between the check and the connection. dial is "" when a is not local.
func Resolve(ctx context.Context, a Addr, resolve Resolver) (dial string, v Verdict) {
	if a.Literal() {
		v = classifyIP(a.ip)
		if v.Local || v.CGNAT {
			dial = a.String()
		}
		return dial, v
	}
	if resolve == nil {
		resolve = DefaultResolver
	}
	ips, err := resolve(ctx, a.Host)
	if err != nil {
		return "", Verdict{Reason: fmt.Sprintf("%s does not resolve: %v", a.Host, err)}
	}
	if len(ips) == 0 {
		return "", Verdict{Reason: a.Host + " resolves to no address"}
	}
	for _, ip := range ips {
		na, ok := netip.AddrFromSlice(ip)
		if !ok || !classifyIP(na).Local {
			return "", Verdict{Reason: a.Host + " resolves to a public address (" + ip.String() + ")"}
		}
	}
	return net.JoinHostPort(ips[0].String(), a.Port), Verdict{Local: true}
}

// ProbeResult is what an address is, as far as /local/poll can tell.
type ProbeResult int

const (
	ProbeNoAnswer ProbeResult = iota // nothing answered (refused, timed out, bad URL)
	ProbeNotTower                    // something answered, but not a standalone Tower
	ProbeTowerYes                    // the standalone Tower's uniform 401
)

// probe classifies base by its answer to an unsigned /local/poll: the plane's UNIFORM 401,
// {"error":"unauthorized"}. Matching that exact answer (not merely "not a 404") tells a Tower
// apart from a public broker (no such route) and from a dev server that catch-alls a 200.
func probe(ctx context.Context, hc *http.Client, base string) ProbeResult {
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

// TowerCacheTTL is how long one probe's answer stands for every caller (the TUI renders SHARE
// every frame; `roger lan list` and the dial ask too).
const TowerCacheTTL = 5 * time.Second

type towerSeen struct {
	res ProbeResult
	at  time.Time
}

var (
	towerMu    sync.Mutex
	towerCache = map[string]towerSeen{}
	// towersSeen is every standalone Tower this process has confirmed, by host:port: a request
	// signed to one carries the replay nonce (WantsNonce) without waiting on a probe.
	towersSeen sync.Map
)

// Check is THE standalone-Tower probe (features/packaging/one_binary.feature section 7.2):
// whether base answers /local/poll as a standalone Tower does. One answer stands for
// TowerCacheTTL for every caller. hc nil uses a 3 s client. A Tower it confirms is noted for the
// nonce decision.
func Check(ctx context.Context, hc *http.Client, base string) ProbeResult {
	key := strings.TrimRight(base, "/")
	towerMu.Lock()
	if seen, ok := towerCache[key]; ok && time.Since(seen.at) < TowerCacheTTL {
		towerMu.Unlock()
		return seen.res
	}
	towerMu.Unlock()
	res := probe(ctx, hc, key)
	towerMu.Lock()
	towerCache[key] = towerSeen{res: res, at: time.Now()}
	towerMu.Unlock()
	if res == ProbeTowerYes {
		NoteTower(key)
	}
	return res
}

// IsTower reports whether base is a standalone Tower's consumer plane: plaintext http at a local
// address (or "localhost", how most people type the loopback host; no other name is resolved
// here), answering Check as a Tower. The public https broker is never probed.
func IsTower(ctx context.Context, hc *http.Client, base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" {
		return false
	}
	if !strings.EqualFold(u.Hostname(), "localhost") && !ClassifyLiteral(u.Host).Local {
		return false
	}
	return Check(ctx, hc, base) == ProbeTowerYes
}

// NoteTower records a standalone Tower this process has seen answer (a probe, or a LAN dial read
// of its /discover), so requests signed to it carry the replay nonce.
func NoteTower(addr string) {
	if a, err := ParseAddr(addr); err == nil {
		towersSeen.Store(strings.ToLower(a.String()), struct{}{})
	}
}

func towerSeenAt(host, port string) bool {
	_, ok := towersSeen.Load(strings.ToLower(net.JoinHostPort(host, port)))
	return ok
}

// ResetTowers forgets every cached probe answer and noted Tower (tests start each scenario fresh).
func ResetTowers() {
	towerMu.Lock()
	towerCache = map[string]towerSeen{}
	towerMu.Unlock()
	towersSeen.Range(func(k, _ any) bool { towersSeen.Delete(k); return true })
}
