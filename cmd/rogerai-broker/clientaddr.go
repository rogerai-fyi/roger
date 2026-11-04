package main

// Which address a request came from, and when a forwarding header is believed.
// The spec is features/security/client_address_trust.feature.
//
// The caller's address keys every per-address control (the anonymous and free-tier limits,
// API-key guessing, the email sign-in code flow, /report, the concierge, free registration and
// its network bucket, the region cross-check), and it is the address the abuse-preservation
// record keeps, where a forged one poisons a legal report (18 USC 2258A). A caller writes every
// header it sends, so a forwarding header is believed only when the TCP peer is a proxy the
// operator named, and then only the part that proxy added. With nothing named, no header is
// believed: a broker behind a proxy it was not told about keys every caller on that proxy,
// which is wrong but visible, where believing an unnamed peer would let it name any caller.

import (
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"

	"golang.org/x/net/http/httpguts"
)

// addrTrust is the two operator settings: which TCP peers may say who the caller is, and which
// header, if any, carries the caller's country. Startup sets it (tests too) and every request
// reads it, so it sits behind an atomic pointer.
type addrTrust struct {
	proxies       []netip.Prefix // ROGERAI_TRUSTED_PROXIES; empty trusts no peer
	countryHeader string         // ROGERAI_COUNTRY_HEADER as written; "" reads no country
}

var clientAddrTrust atomic.Pointer[addrTrust]

// noProxyTrusted is in force until startup installs the operator's settings.
var noProxyTrusted = &addrTrust{}

// currentAddrTrust is the setting in force.
func currentAddrTrust() *addrTrust {
	if t := clientAddrTrust.Load(); t != nil {
		return t
	}
	return noProxyTrusted
}

// singleAddressHeaders are never read for the caller, so none of them can be the country header
// either: naming one would turn a caller-written address into the tally's country.
var singleAddressHeaders = map[string]bool{
	"X-Forwarded-For": true, "Forwarded": true, "Cf-Connecting-Ip": true, "X-Real-Ip": true,
	"True-Client-Ip": true,
}

// parseAddrTrust reads the two settings. Blank or "none" trusts no peer; otherwise the proxies
// are a comma-separated list of addresses and CIDRs. Anything the broker cannot read exactly
// is an error, never a quiet fallback: a typo would otherwise leave it believing headers from a
// peer the operator meant to exclude, or keying every caller on the proxy.
func parseAddrTrust(proxies, countryHeader string) (*addrTrust, error) {
	t := &addrTrust{}
	if h := strings.TrimSpace(countryHeader); h != "" {
		if !httpguts.ValidHeaderFieldName(h) {
			return nil, fmt.Errorf("ROGERAI_COUNTRY_HEADER: %q is not a header name", h)
		}
		if singleAddressHeaders[http.CanonicalHeaderKey(h)] {
			return nil, fmt.Errorf("ROGERAI_COUNTRY_HEADER: %q carries an address, not a country", h)
		}
		t.countryHeader = h
	}
	setting := strings.TrimSpace(proxies)
	if setting == "" || strings.EqualFold(setting, "none") {
		return t, nil
	}
	for _, item := range strings.Split(setting, ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		p, err := parseProxyEntry(item)
		if err != nil {
			return nil, fmt.Errorf("ROGERAI_TRUSTED_PROXIES: %q is not a proxy address or CIDR: %v", item, err)
		}
		t.proxies = append(t.proxies, p)
	}
	if len(t.proxies) == 0 {
		return nil, fmt.Errorf("ROGERAI_TRUSTED_PROXIES: %q names no proxy", setting)
	}
	return t, nil
}

// parseProxyEntry reads one trusted-proxy entry: an address (its own /32 or /128) or a CIDR. An
// IPv4-mapped address is read as the IPv4 address it carries, as every peer is. Three CIDR forms
// are refused because each is almost always a mistake: a mapped CIDR (no unmapped peer falls
// inside it), a /0 (every peer, so every caller names itself), and host bits past the prefix
// ("10.0.0.1/24" for 10.0.0.0/24, or for the single address 10.0.0.1).
func parseProxyEntry(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		switch {
		case err != nil:
			return netip.Prefix{}, err
		case p.Addr().Is4In6():
			return netip.Prefix{}, fmt.Errorf("an IPv4-mapped CIDR; write it as IPv4")
		case p.Bits() == 0:
			return netip.Prefix{}, fmt.Errorf("a /0 trusts every peer")
		case p != p.Masked():
			return netip.Prefix{}, fmt.Errorf("host bits set; the network is %s", p.Masked())
		}
		return p, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap().WithZone("")
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// trusts reports whether a request delivered by peer a (already unmapped) may say who its
// caller is.
func (t *addrTrust) trusts(a netip.Addr) bool {
	for _, p := range t.proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// String is the startup log line: what the broker believes, and from whom.
func (t *addrTrust) String() string {
	who := "no proxy trusted, every request is keyed on its TCP peer (behind a proxy, name it in ROGERAI_TRUSTED_PROXIES)"
	if len(t.proxies) > 0 {
		ps := make([]string, len(t.proxies))
		for i, p := range t.proxies {
			ps[i] = p.String()
		}
		who = "forwarding headers believed from " + strings.Join(ps, ", ")
	}
	if t.countryHeader == "" {
		return who + "; no country header"
	}
	return who + "; country from " + t.countryHeader
}

// loadClientAddrTrust installs ROGERAI_TRUSTED_PROXIES and ROGERAI_COUNTRY_HEADER and says what
// it installed. runServe calls it first, so a setting the broker cannot read stops it before it
// serves a request, and the setting in force is left as it was.
func loadClientAddrTrust(getenv func(string) string) error {
	t, err := parseAddrTrust(getenv("ROGERAI_TRUSTED_PROXIES"), getenv("ROGERAI_COUNTRY_HEADER"))
	if err != nil {
		return err
	}
	clientAddrTrust.Store(t)
	log.Printf("client address: %s", t)
	return nil
}

// clientIP is the address a request came from. A direct caller is the address it connected
// from, whatever it sends. Behind a trusted proxy the caller is the right-most X-Forwarded-For
// entry that is not itself a trusted proxy, because each proxy appends the address it saw; the
// entries to its left were written by the caller and are never read. Every trusted peer is
// taken to be such a proxy, so whatever a trusted host writes is believed: name only the
// proxies. Single-address headers (CF-Connecting-IP, X-Real-IP) are not read at all: whether
// something in front overwrote them is a fact about the deployment the broker cannot check, and
// common proxies pass them through. A RemoteAddr that is not an IP (a local socket) is returned
// as it is and is never a proxy.
func clientIP(r *http.Request) string {
	peer, ok := parseHostAddr(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	t := currentAddrTrust()
	if !t.trusts(peer) {
		return peer.String()
	}
	if a, ok := forwardedCaller(r.Header.Values("X-Forwarded-For"), t); ok {
		return a.String()
	}
	return peer.String()
}

// forwardedCaller walks X-Forwarded-For from the right, across every header line in order, and
// returns the first entry that is not a trusted proxy. When every entry is one (a caller among
// the trusted proxies) it returns the right-most, the address the nearest proxy saw. An entry
// that is not an address ends the walk: what came before it stands, and with nothing before it
// there is no answer and the request is keyed on the proxy. Only the entries right of the
// caller's are ever parsed, however much the caller wrote.
func forwardedCaller(lines []string, t *addrTrust) (netip.Addr, bool) {
	var nearest netip.Addr
	for i := len(lines) - 1; i >= 0; i-- {
		rest := lines[i]
		for {
			item, more := rest, false
			if j := strings.LastIndexByte(rest, ','); j >= 0 {
				item, rest, more = rest[j+1:], rest[:j], true
			}
			a, ok := parseHostAddr(item)
			if !ok {
				return nearest, nearest.IsValid()
			}
			if !t.trusts(a) {
				return a, true
			}
			if !nearest.IsValid() {
				nearest = a
			}
			if !more {
				break
			}
		}
	}
	return nearest, nearest.IsValid()
}

// parseHostAddr reads an address the way a peer or a proxy writes one: bare, bracketed IPv6, or
// with a port ("203.0.113.7:51000", "[2001:db8::7]:51000"). It comes back in the one form every
// key uses: an IPv4-mapped address as IPv4, no zone, IPv6 compressed and lower-case.
func parseHostAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if len(s) > 2 && s[0] == '[' && s[len(s)-1] == ']' {
		s = s[1 : len(s)-1]
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		ap, perr := netip.ParseAddrPort(s)
		if perr != nil {
			return netip.Addr{}, false
		}
		a = ap.Addr()
	}
	return a.Unmap().WithZone(""), true
}

// clientCountry is the coarse origin of a request for the Tower origin tally: the value of the
// header the operator names in ROGERAI_COUNTRY_HEADER (an edge proxy's country header, such as
// CF-IPCountry), read only when a trusted proxy delivered the request and the header has exactly
// one line. With no header named there is no country and the tally counts the request as
// unknown, since a caller can write any value into a header the front does not set; two lines
// mean the front added one beside the caller's, and which is whose cannot be told. origin.Record
// keeps only a 2-character code. No address is read here.
func clientCountry(r *http.Request) string {
	t := currentAddrTrust()
	if t.countryHeader == "" {
		return ""
	}
	peer, ok := parseHostAddr(r.RemoteAddr)
	if !ok || !t.trusts(peer) {
		return ""
	}
	if v := r.Header.Values(t.countryHeader); len(v) == 1 {
		return strings.TrimSpace(v[0])
	}
	return ""
}
