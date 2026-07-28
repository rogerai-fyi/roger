package harness

// fetch.go is the ONE guarded network path for model-supplied URLs (web_fetch). The URL
// is chosen by the MODEL, which may be steered by a hostile page or search snippet, so it
// is treated as attacker-controlled: the address is vetted BEFORE any dial, the dial is
// pinned to the vetted IP, every redirect hop is re-vetted, and only readable text comes
// back. Spec: features/answers/fetch_hardening.feature.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// maxFetchBytes caps a web_fetch body read.
const maxFetchBytes = 256 << 10

// maxRedirects bounds a redirect chain; each hop is vetted like a fresh URL.
const maxRedirects = 5

// fetchTimeout bounds a whole web_fetch (DNS, all hops, the body read) so a slow URL can't
// hang the turn. A var (the shellTimeout precedent) only so a test can shorten it;
// production is unchanged.
var fetchTimeout = 20 * time.Second

// resolveHost is the DNS seam: it turns a hostname into the addresses that will be vetted.
// A var so a test can exercise DNS-based SSRF and rebinding without real DNS. Production
// never reassigns it. It takes the fetch's ctx so a hostile domain whose nameserver never
// answers is bounded by fetchTimeout like everything else.
var resolveHost = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// fetchVetIP is the address-vetting seam, defaulting to the real vetIP. A var so a test
// that needs a reachable stand-in for a "public" server (every local listener is loopback)
// can permit 127.0.0.1 while delegating everything else to the real policy. Production
// never reassigns it.
var fetchVetIP = vetIP

// blockedNets is the deny-list applied ON TOP of the "must be global unicast" rule. Go's
// IsPrivate/IsLoopback/IsLinkLocal predicates miss several ranges that are very much
// internal in practice, and several IPv6 forms EMBED an IPv4 address that would otherwise
// skip the v4 predicates entirely:
//
//	100.64.0.0/10     carrier-grade NAT - also where Tailscale tailnets and some
//	                  managed-Kubernetes node ranges live, so this is the highest-value miss
//	64:ff9b::/96      NAT64 well-known prefix: on an IPv6-only network this reaches
//	                  169.254.169.254 (cloud metadata) as 64:ff9b::a9fe:a9fe
//	2002::/16         6to4, which embeds an arbitrary IPv4 address
//	::/96             deprecated IPv4-compatible ::a.b.c.d (To4 only normalizes ::ffff:)
//	::ffff:0:0:0/96   the SIIT IPv4-translated form of the same trick
//	2001::/32         Teredo tunneling
//	192.0.0.0/24      IETF protocol assignments · 198.18.0.0/15 benchmarking
//	240.0.0.0/4       reserved · fec0::/10 deprecated site-local
//	the TEST-NET / documentation ranges, which no real fetch should target
var blockedNets = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

// vetIP decides whether a fetch may reach an address. The posture is ALLOW-LIST first: an
// address must be global unicast (which excludes loopback, link-local, unspecified,
// multicast, and broadcast in one rule), and must then not fall in any blockedNets range.
// A deny-list alone kept missing tunnel and shared-address-space forms.
func vetIP(ip net.IP) error {
	if ip == nil {
		return errors.New("blocked address: unparsable")
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("blocked address %s (unparsable)", ip)
	}
	addr = addr.Unmap() // ::ffff:a.b.c.d is vetted as the v4 address it carries
	switch {
	case addr.IsLoopback():
		return fmt.Errorf("blocked address %s (loopback is not fetchable)", ip)
	case addr.IsPrivate():
		return fmt.Errorf("blocked address %s (private ranges are not fetchable)", ip)
	case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
		return fmt.Errorf("blocked address %s (link-local / metadata space is not fetchable)", ip)
	case !addr.IsGlobalUnicast():
		// Unspecified, multicast, and 255.255.255.255 all land here.
		return fmt.Errorf("blocked address %s (not a global unicast address)", ip)
	}
	for _, n := range blockedNets {
		if n.Contains(addr) {
			return fmt.Errorf("blocked address %s (reserved or internal range %s)", ip, n)
		}
	}
	return nil
}

// vetAndPin resolves and vets rawurl, returning the exact "ip:port" to dial. Pinning the
// dial to the vetted IP is what closes DNS rebinding: a re-resolution between the check
// and the connection cannot move the target.
func vetAndPin(ctx context.Context, rawurl string) (string, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return "", fmt.Errorf("unfetchable URL %q: %w", rawurl, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("only http(s) URLs are supported: %q", rawurl)
	}
	host := u.Hostname()
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("unfetchable URL %q: no host", rawurl)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}

	var ips []net.IP
	if ip := parseLooseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolved, err := resolveHost(ctx, host)
		if err != nil {
			return "", fmt.Errorf("cannot resolve %q: %w", host, err)
		}
		ips = resolved
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("cannot resolve %q: no addresses", host)
	}
	// EVERY answer must vet clean: a name that resolves to one public and one private
	// address must not be fetchable via the public one. The dial then pins ips[0] - we
	// deliberately do NOT fall back to a later answer, since "try the next address" is
	// exactly the retry loop that would make the vetting racy.
	for _, ip := range ips {
		if err := fetchVetIP(ip); err != nil {
			return "", err
		}
	}
	return net.JoinHostPort(ips[0].String(), port), nil
}

// parseLooseIP parses the numeric host forms the C resolver (inet_aton) accepts but
// net.ParseIP does not: 32-bit decimal (2130706433), hex (0x7f000001), octal
// (017700000001), and short dotted forms (127.1). Without this, "http://2130706433/"
// would skip the IP check, resolve through the system resolver, and reach 127.0.0.1.
// Returns nil for anything that is not purely numeric - real hostnames go to DNS, whose
// answers are vetted anyway, so declining here is always safe.
func parseLooseIP(host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	parts := strings.Split(host, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return nil
	}
	vals := make([]uint64, 0, len(parts))
	for _, p := range parts {
		v, ok := parseLooseNum(p)
		if !ok {
			return nil
		}
		vals = append(vals, v)
	}
	// The last part absorbs the remaining bytes (a.b => a.0.0.b's low 24 bits, etc).
	var addr uint64
	last := vals[len(vals)-1]
	lead := vals[:len(vals)-1]
	for _, v := range lead {
		if v > 0xff {
			return nil
		}
	}
	maxLast := uint64(1) << (8 * (4 - uint(len(lead))))
	if last >= maxLast {
		return nil
	}
	for i, v := range lead {
		addr |= v << (8 * (3 - uint(i)))
	}
	addr |= last
	return net.IPv4(byte(addr>>24), byte(addr>>16), byte(addr>>8), byte(addr))
}

// parseLooseNum parses one inet_aton component: 0x/0X hex, leading-0 octal, else decimal.
func parseLooseNum(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	base := 10
	switch {
	case len(s) > 2 && (strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X")):
		base, s = 16, s[2:]
	case len(s) > 1 && s[0] == '0':
		base, s = 8, s[1:]
	}
	v, err := strconv.ParseUint(s, base, 64)
	if err != nil || v > 0xffffffff {
		return 0, false
	}
	return v, true
}

// webFetch GETs a model-supplied URL through the guard and returns readable text. It
// follows redirects MANUALLY (http.Client's own follower would dial the next hop before
// we could vet it), re-vetting and re-pinning every hop.
//
// Known, deliberate omission: there is no port allowlist. A redirect to a non-web port on
// a genuinely public host is vetted clean and dialed. GET-only cross-protocol smuggling is
// weak, and a port allowlist would break legitimate services on odd ports.
func webFetch(ctx context.Context, rawurl string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// The turn's context is the PARENT: fetchTimeout bounds a slow host, and cancelling
	// the turn (esc) abandons the request in flight rather than waiting it out.
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	cur := strings.TrimSpace(rawurl)
	for hop := 0; ; hop++ {
		if hop > maxRedirects {
			return "", fmt.Errorf("too many redirects (over %d hops) starting at %q", maxRedirects, rawurl)
		}
		dial, err := vetAndPin(ctx, cur)
		if err != nil {
			return "", err
		}
		resp, err := fetchOnce(ctx, cur, dial)
		if err != nil {
			return "", err
		}
		if loc := redirectTarget(resp); loc != "" {
			resp.Body.Close()
			next, err := url.Parse(loc)
			if err != nil {
				return "", fmt.Errorf("bad redirect target %q: %w", loc, err)
			}
			base, _ := url.Parse(cur)
			cur = base.ResolveReference(next).String()
			continue
		}
		ctype := resp.Header.Get("Content-Type")
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
		resp.Body.Close()

		text, err := extractText(ctype, body)
		if err != nil {
			return "", err
		}
		if resp.StatusCode >= 400 {
			return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, clip(text)), nil
		}
		if strings.TrimSpace(text) == "" {
			return fmt.Sprintf("HTTP %d (empty body)", resp.StatusCode), nil
		}
		// A page we really read: wrap it here, where the FINAL url after redirects is known
		// and already normalized. Doing this in the loop instead would cite the model's raw
		// argument, so "https://x " and "https://x" would read as two different sources and a
		// redirect would cite the entry URL rather than the page actually quoted.
		// Clip the WRAPPED result so the marker counts against the tool-output cap too: the
		// model's context budget does not care that some of the bytes are metadata.
		return clip(wrapRetrieved(cur, text)), nil
	}
}

// fetchOnce performs ONE request with the dial pinned to the vetted address. The URL is
// unchanged, so the Host header and the TLS server name still carry the original hostname.
func fetchOnce(ctx context.Context, rawurl, dialAddr string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RogerAI/web_fetch")
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// Ignore the caller-supplied address: only the vetted, pinned one is dialed.
			return (&net.Dialer{}).DialContext(ctx, network, dialAddr)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		// Never auto-follow: webFetch vets each hop itself.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client.Do(req)
}

// redirectTarget returns the Location of a redirect response, or "".
func redirectTarget(resp *http.Response) string {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return resp.Header.Get("Location")
	}
	return ""
}

// extractText turns a fetched body into readable UTF-8 text: HTML is reduced to its text
// (script/style dropped), text-ish types pass through, binary is refused rather than
// spent as context, and control bytes are stripped.
func extractText(ctype string, body []byte) (string, error) {
	mediaType, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		mediaType = strings.TrimSpace(strings.ToLower(strings.SplitN(ctype, ";", 2)[0]))
		params = map[string]string{}
	}
	if mediaType == "" {
		mediaType = "text/plain"
	}
	if !textual(mediaType) {
		return "", fmt.Errorf("unsupported content type %q (web_fetch returns text only)", mediaType)
	}
	// Sniff the RAW bytes: decodeCharset's latin-1 fallback would happily manufacture
	// valid UTF-8 out of arbitrary binary, so "it decoded" is not evidence of text.
	if err := sniffBinary(mediaType, body); err != nil {
		return "", err
	}
	text := decodeCharset(body, params["charset"])
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		text = htmlToText(text)
	}
	return stripControls(text), nil
}

// textual reports whether a media type carries text we can hand to a model.
func textual(mediaType string) bool {
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/xhtml+xml", "application/javascript":
		return true
	}
	return strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

// sniffBinary rejects a body that is binary whatever its declared type: a NUL byte is
// decisive, and so is a high proportion of C0 control bytes in the leading window.
func sniffBinary(mediaType string, body []byte) error {
	window := body
	if len(window) > 1024 {
		window = window[:1024]
	}
	controls := 0
	for i, c := range window {
		if c == 0 {
			return fmt.Errorf("refusing binary body declared as %q (NUL byte at offset %d)", mediaType, i)
		}
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' && c != '\f' {
			controls++
		}
	}
	// 20%: a real page carrying a few ANSI escapes stays well under this (those get
	// stripped, not refused); a body that is a fifth control bytes is not text.
	if len(window) > 0 && controls*100/len(window) > 20 {
		return fmt.Errorf("refusing binary body declared as %q (%d%% control bytes)", mediaType, controls*100/len(window))
	}
	return nil
}

// stripControls removes C0 control bytes and DEL, keeping only newline and tab. A fetched
// page is untrusted text that lands in the TUI transcript: raw ANSI escapes would repaint
// or retitle the user's terminal, and \x1e is the transcript's own tool-output marker.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// decodeCharset converts a declared legacy charset to UTF-8. Already-valid UTF-8 (and
// anything unrecognized that happens to be valid UTF-8) passes through untouched;
// otherwise a latin-1 reading beats emitting invalid UTF-8 at the model.
func decodeCharset(body []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin-1", "latin1", "iso8859-1":
		out, err := charmap.ISO8859_1.NewDecoder().Bytes(body)
		if err == nil {
			return string(out)
		}
	case "windows-1252", "cp1252":
		out, err := charmap.Windows1252.NewDecoder().Bytes(body)
		if err == nil {
			return string(out)
		}
	}
	if utf8.Valid(body) {
		return string(body)
	}
	out, err := charmap.ISO8859_1.NewDecoder().Bytes(body)
	if err != nil {
		return strings.ToValidUTF8(string(body), "")
	}
	return string(out)
}

// htmlToText reduces an HTML document to readable text: script/style/comment content is
// dropped entirely (it is noise the model would pay for, and a place to hide text), tags
// become line breaks, entities are unescaped, and runs of blank space collapse.
func htmlToText(src string) string {
	var b strings.Builder
	b.Grow(len(src) / 2)
	for i := 0; i < len(src); {
		c := src[i]
		if c != '<' {
			b.WriteByte(c)
			i++
			continue
		}
		if strings.HasPrefix(src[i:], "<!--") {
			if end := strings.Index(src[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
				continue
			}
			break
		}
		if skipTo, ok := skipElement(src, i, "script"); ok {
			i = skipTo
			b.WriteByte('\n')
			continue
		}
		if skipTo, ok := skipElement(src, i, "style"); ok {
			i = skipTo
			b.WriteByte('\n')
			continue
		}
		end := strings.IndexByte(src[i:], '>')
		if end < 0 {
			break
		}
		i += end + 1
		b.WriteByte('\n')
	}
	return collapse(html.UnescapeString(b.String()))
}

// skipElement reports whether src at i opens <name ...> and, if so, returns the offset
// just past its closing tag (or the end of input for an unterminated element).
func skipElement(src string, i int, name string) (int, bool) {
	if !strings.HasPrefix(strings.ToLower(src[i:min(i+len(name)+2, len(src))]), "<"+name) {
		return 0, false
	}
	rest := src[i+len(name)+1:]
	if rest != "" && !isTagBoundary(rest[0]) {
		return 0, false // <scriptfoo> is a different element
	}
	closeTag := "</" + name
	if end := strings.Index(strings.ToLower(src[i:]), closeTag); end >= 0 {
		if gt := strings.IndexByte(src[i+end:], '>'); gt >= 0 {
			return i + end + gt + 1, true
		}
	}
	return len(src), true
}

// isTagBoundary reports whether c ends a tag name.
func isTagBoundary(c byte) bool {
	return c == '>' || c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '/'
}

// collapse trims each line and drops empty ones, so tag-derived breaks don't leave a page
// of whitespace.
func collapse(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(strings.ReplaceAll(ln, "\r", ""))
		if ln != "" {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}
