// Package egressguard is the standalone Tower's runtime guard (features/packaging/
// one_binary.feature section 4): in standalone mode the process may connect only to local
// addresses (internal/netpriv: loopback, RFC 1918, ULA, link-local; CGNAT is not local). Install
// makes the guard the process's default HTTP transport and DNS resolver, so a forgotten client
// cannot bypass it; a name is judged by the address it resolves to, never by the name.
//
// It does not reach a direct net.Dial: Go has no process-wide hook for it. The standalone serve
// package's own source gate forbids it (internal/structural.NoOutbound).
package egressguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"

	"rogerai.fm/roger/v6/internal/netpriv"
)

// ErrRefused is the guard's refusal of a non-local connection.
var ErrRefused = errors.New("standalone: non-local connection refused")

var (
	mu   sync.Mutex
	logw io.Writer = io.Discard
	// resolve looks a name up; by default through the (guarded, once installed) default resolver.
	resolve = defaultResolve
	dialer  net.Dialer
)

func defaultResolve(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// SetResolver points name lookups at fn (a seam: a test resolves a name to a chosen address);
// nil restores the default resolver.
func SetResolver(fn func(ctx context.Context, host string) ([]net.IP, error)) {
	mu.Lock()
	defer mu.Unlock()
	if fn == nil {
		fn = defaultResolve
	}
	resolve = fn
}

func refuse(dest string) error {
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintf(logw, "%v: %s\n", ErrRefused, dest)
	return fmt.Errorf("%w: %s", ErrRefused, dest)
}

// DialContext connects to addr only when it is local. A literal is judged as written (a zone is
// kept for the dial); a name is resolved and judged by the address it resolves to, and the
// connection goes to that address, so a later lookup cannot move it.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if a, perr := netip.ParseAddr(host); perr == nil {
		if !netpriv.IsLocal(net.IP(a.Unmap().WithZone("").AsSlice()), false) {
			return nil, refuse(addr)
		}
		return dialer.DialContext(ctx, network, addr)
	}
	mu.Lock()
	lookup := resolve
	mu.Unlock()
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s resolves to no address", host)
	}
	for _, ip := range ips {
		if !netpriv.IsLocal(ip, false) {
			return nil, refuse(net.JoinHostPort(ip.String(), port) + " (" + host + ")")
		}
	}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

// Install makes the guard the process's default HTTP transport (and so http.DefaultClient's) and
// its default DNS resolver, logging every refusal to w, and returns the function that puts the
// previous ones back (tests; a standalone serve never uninstalls). A nil w discards the log.
func Install(w io.Writer) (uninstall func()) {
	if w == nil {
		w = io.Discard
	}
	mu.Lock()
	prevLog := logw
	logw = w
	mu.Unlock()
	prevTransport, prevResolver := http.DefaultTransport, net.DefaultResolver
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = DialContext
	t.Proxy = nil // a configured proxy is a destination too; no detour around the guard
	http.DefaultTransport = t
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: DialContext}
	return func() {
		http.DefaultTransport, net.DefaultResolver = prevTransport, prevResolver
		mu.Lock()
		logw = prevLog
		mu.Unlock()
	}
}
