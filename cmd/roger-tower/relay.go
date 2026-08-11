package main

// relay.go runs the Tower's DATA PLANE: the port consumers connect to.
//
// This is the half that makes a Tower worth paying for. Work dispatched through the control
// plane still crosses Roger Core; traffic that arrives here does not - the consumer's TLS
// session runs end to end to the Station and this process only splices bytes. Core handles
// two small control messages instead of the whole payload.
//
// THE OPERATOR CANNOT READ IT. Nothing here terminates TLS, and there is no configuration
// that would let it: the Tower holds no certificate for the names it routes. What it can see
// is the server name, the addresses, the byte counts and the timings - which is exactly what
// it needs to be paid.

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/relay"
)

// relayRoutes maps a Station's public name to where this Tower reaches it.
type relayRoutes map[string]string

// parseRelayRoutes reads the repeatable --relay-station flag.
//
// The key is the STATION ID rather than a full hostname, because the hostname is Core's to
// choose - a Station is reachable at a name under Core's relay domain, and an operator
// should not have to keep that spelling in step by hand.
func parseRelayRoutes(vals []string) (relayRoutes, error) {
	out := relayRoutes{}
	for _, v := range vals {
		id, addr, ok := cutOne(v, "=")
		if !ok || id == "" || addr == "" {
			return nil, fmt.Errorf("--relay-station wants ID=HOST:PORT, got %q", v)
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, fmt.Errorf("--relay-station %q needs a host:port, not %q", id, addr)
		}
		out[id] = addr
	}
	return out, nil
}

// Upstream resolves a server name to a Station this Tower carries.
//
// The Station ID is the leftmost label: `st-abc123.relay.example` is Station `st-abc123`. A
// name for a Station this Tower does not hold is refused rather than guessed at - a relay
// that resolved unknown names would be an open proxy wearing a Tower's clothes.
func (r relayRoutes) Upstream(serverName string) (string, bool) {
	label, _, found := strings.Cut(serverName, ".")
	if !found || label == "" {
		return "", false
	}
	addr, ok := r[label]
	return addr, ok
}

// runRelay serves the data plane until stopped.
func runRelay(addr string, routes relayRoutes, out io.Writer, stop <-chan struct{}) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "relaying consumer traffic on %s for %d Station(s)\n", ln.Addr(), len(routes))
	fmt.Fprint(out, "this Tower cannot read what it relays: the TLS session runs end to end\n"+
		"between the consumer and the Station, and no certificate for those names is held here.\n")

	r := &relay.Relay{
		Router: routes,
		// Bounded so one connection cannot hold a slot, or a month of egress, on its own.
		PeekTimeout: 10 * time.Second,
		IdleTimeout: 5 * time.Minute,
		MaxBytes:    relayMaxBytesPerDirection,
		OnClose: func(s relay.Stats) {
			// METADATA ONLY, by construction - there is no content here to log even by
			// accident. These counts are what the operator is paid on, and they are checkable
			// against what the two ends independently report.
			if s.Err != nil {
				fmt.Fprintf(out, "relay %s: %v (%s, up %d B, down %d B)\n",
					s.ServerName, s.Err, s.Duration().Round(time.Millisecond), s.ToStation, s.ToClient)
				return
			}
			fmt.Fprintf(out, "relay %s: %s, up %d B, down %d B\n",
				s.ServerName, s.Duration().Round(time.Millisecond), s.ToStation, s.ToClient)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stop
		cancel()
		_ = ln.Close()
	}()
	return r.Serve(ctx, ln)
}

// relayMaxBytesPerDirection bounds one connection. Generous for a long completion, and far
// below what an abusive one would want.
const relayMaxBytesPerDirection = 256 << 20

// runRelayInBackground starts the data plane and returns a wait function.
func runRelayInBackground(addr string, routes relayRoutes, out io.Writer, stop <-chan struct{}) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := runRelay(addr, routes, out, stop); err != nil {
			fmt.Fprintf(out, "the relay stopped: %v\n", err)
		}
	}()
	return func() { <-done }
}
