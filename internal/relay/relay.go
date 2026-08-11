// Package relay is the Tower's data plane: a TCP splice that carries a consumer's TLS
// session to a Station and never terminates it.
//
// # WHY IT IS NOT AN HTTP PROXY
//
// The Tower is somebody else's machine carrying somebody else's customers' traffic. Anything
// that terminates TLS sees plaintext, and no amount of policy makes an operator unable to
// read what their own process decrypted. So this never decrypts: it reads the SNI out of the
// ClientHello to learn which Station a connection is for, and from there it copies bytes.
//
// The session runs end to end between the CONSUMER and the STATION. What the Tower observes
// is a server name, two addresses, byte counts and timings - the routing metadata it needs to
// do its job and be paid for it, and nothing else.
//
// # WHY THE SNI IS READ WITH GO'S OWN TLS PARSER
//
// Hand-rolling a ClientHello parser is a well-known source of subtle bugs: extension
// ordering, session-ticket sizes, GREASE values, fragmented records. Instead the handshake is
// started with crypto/tls and abandoned the instant it reports the server name, over a
// connection that RECORDS every byte it hands out. Those recorded bytes are then replayed to
// the Station, so the Station sees the ClientHello exactly as the client sent it.
//
// The cost is that the relay must not accidentally complete a handshake, which is why the
// callback always returns an error: there is no configuration under which this process holds
// a private key for the name it is routing.
package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// Router says where a connection for a server name should go.
//
// An interface because a Tower's map of Stations changes as they attach and detach, and
// because the relay has no business knowing how that is decided.
type Router interface {
	// Upstream returns the address for this server name, and whether it is one we carry.
	Upstream(serverName string) (string, bool)
}

// Stats is what the Tower may honestly say it did. It is metadata by construction: there is
// no field here that could hold content, because the relay never has any.
type Stats struct {
	ServerName string
	Upstream   string
	// ToStation and ToClient are byte counts. They are the Tower's OWN claim about its work,
	// which is checkable against what the two ends report and is not trusted on its own.
	ToStation int64
	ToClient  int64
	Started   time.Time
	Ended     time.Time
	// Err is why the connection ended, when it ended badly.
	Err error
}

// Duration is how long the connection was open.
func (s Stats) Duration() time.Duration { return s.Ended.Sub(s.Started) }

// Relay splices connections through to Stations.
type Relay struct {
	Router Router
	// Dial reaches a Station. Replaceable so a test does not need a real network, and so a
	// Tower can reach Stations over whatever private path it has.
	Dial func(ctx context.Context, addr string) (net.Conn, error)
	// PeekTimeout bounds how long a connection may take to say who it wants. A connection
	// that opens and says nothing is otherwise a free slot held forever.
	PeekTimeout time.Duration
	// IdleTimeout closes a spliced connection that has gone quiet in both directions.
	IdleTimeout time.Duration
	// MaxBytes bounds one connection in each direction. Zero means unbounded.
	MaxBytes int64
	// OnClose reports what happened, for metering and for the operator's own logs.
	OnClose func(Stats)
}

// The refusals a caller can act on.
var (
	ErrNoServerName = errors.New("the connection named no server")
	ErrNotOurs      = errors.New("no Station here answers to that name")
)

const (
	defaultPeekTimeout = 10 * time.Second
	defaultIdleTimeout = 5 * time.Minute
)

func (r *Relay) peekTimeout() time.Duration {
	if r.PeekTimeout > 0 {
		return r.PeekTimeout
	}
	return defaultPeekTimeout
}

func (r *Relay) idleTimeout() time.Duration {
	if r.IdleTimeout > 0 {
		return r.IdleTimeout
	}
	return defaultIdleTimeout
}

func (r *Relay) dial(ctx context.Context, addr string) (net.Conn, error) {
	if r.Dial != nil {
		return r.Dial(ctx, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

// Serve accepts connections until the listener closes.
func (r *Relay) Serve(ctx context.Context, ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// A closed listener is an ordinary shutdown, not a failure to report.
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		go r.Handle(ctx, conn)
	}
}

// Handle carries one connection.
func (r *Relay) Handle(ctx context.Context, client net.Conn) {
	stats := Stats{Started: time.Now()}
	defer func() {
		stats.Ended = time.Now()
		if r.OnClose != nil {
			r.OnClose(stats)
		}
		_ = client.Close()
	}()

	name, prefix, err := peekServerName(client, r.peekTimeout())
	if err != nil {
		stats.Err = err
		return
	}
	stats.ServerName = name

	addr, ok := r.Router.Upstream(name)
	if !ok {
		// Refused without dialling anything. A relay that connected first would be a way to
		// make somebody else's Tower open connections to arbitrary addresses.
		stats.Err = ErrNotOurs
		return
	}
	stats.Upstream = addr

	station, err := r.dial(ctx, addr)
	if err != nil {
		stats.Err = err
		return
	}
	defer station.Close()

	// The ClientHello the client already sent, replayed byte for byte so the Station sees the
	// handshake exactly as it was made. Anything else and the session would not complete.
	if _, err := station.Write(prefix); err != nil {
		stats.Err = err
		return
	}
	stats.ToStation = int64(len(prefix))

	r.splice(client, station, &stats)
}

// splice copies in both directions until either side closes or goes idle.
func (r *Relay) splice(client, station net.Conn, stats *Stats) {
	idle := r.idleTimeout()
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		fail    error
		winding bool
	)
	// note records the FIRST real failure.
	//
	// Once one direction has finished, the other is deliberately unblocked - and on a
	// connection whose type cannot half-close, that unblocking IS a read deadline, which
	// surfaces as "i/o timeout". Reporting that as the reason the connection ended would put
	// a fake error on most perfectly ordinary requests, and an operator's metering is built
	// from these.
	note := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if err == nil || fail != nil {
			return
		}
		if winding && errors.Is(err, os.ErrDeadlineExceeded) {
			return
		}
		fail = err
	}

	copyOne := func(dst, src net.Conn, count *int64) {
		defer wg.Done()
		n, err := copyIdle(dst, src, idle, r.MaxBytes)
		mu.Lock()
		*count += n
		mu.Unlock()
		note(err)

		mu.Lock()
		winding = true
		mu.Unlock()
		// Closing the WRITE half tells the other end this direction is finished, so a Station
		// waiting for the rest of a request stops waiting rather than hanging until the idle
		// timeout. Not every connection type can half-close - a wrapper that only implements
		// net.Conn cannot - so the fallback unblocks the other copy with a deadline instead.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
			return
		}
		_ = dst.SetReadDeadline(time.Now())
	}

	wg.Add(2)
	go copyOne(station, client, &stats.ToStation)
	go copyOne(client, station, &stats.ToClient)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if stats.Err == nil {
		stats.Err = fail
	}
}

// ErrTooLarge is returned when one direction exceeds MaxBytes.
var ErrTooLarge = errors.New("the connection carried more than its limit")

// copyIdle copies with an idle deadline refreshed on every read, and an optional ceiling.
//
// The deadline bounds SILENCE rather than total time: a long streamed completion that keeps
// producing tokens is not idle, and cutting it off would break exactly the requests a Station
// is most useful for.
func copyIdle(dst, src net.Conn, idle time.Duration, max int64) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		if err := src.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return total, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if max > 0 && total+int64(n) > max {
				return total, ErrTooLarge
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if rerr != nil {
			if isNormalClose(rerr) {
				return total, nil
			}
			return total, rerr
		}
	}
}

// isNormalClose reports whether a read ended because the peer went away rather than because
// anything went wrong.
//
// A client that closes abruptly resets the connection, and a browser tab closing mid-stream
// does it constantly. Recording that as a relay FAULT would make an operator's error rate a
// measure of how often their customers change their minds - and these numbers are what they
// are paid and judged on.
func isNormalClose(err error) bool {
	switch {
	case errors.Is(err, io.EOF),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.EPIPE):
		return true
	}
	return false
}

// peekServerName reads the ClientHello far enough to learn the SNI, and returns the bytes it
// consumed so they can be replayed.
func peekServerName(conn net.Conn, timeout time.Duration) (string, []byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", nil, err
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	rec := &recorder{Conn: conn}
	var name string
	// errStopHandshake is returned from the callback ALWAYS. There is no path here that
	// completes a handshake: this process holds no key for the name it is routing, and if it
	// ever did, it would be decrypting somebody's traffic.
	err := tls.Server(rec, &tls.Config{
		GetConfigForClient: func(hi *tls.ClientHelloInfo) (*tls.Config, error) {
			name = hi.ServerName
			return nil, errStopHandshake
		},
	}).Handshake()

	if name == "" {
		if err == nil || errors.Is(err, errStopHandshake) {
			// A TLS connection that named nobody. Common for a raw IP client, and there is
			// nothing to route it to.
			return "", nil, ErrNoServerName
		}
		return "", nil, err
	}
	return name, rec.seen(), nil
}

var errStopHandshake = errors.New("relay: stopping at the server name")

// recorder hands reads through while keeping a copy, so the handshake bytes crypto/tls
// consumed can be replayed to the Station.
//
// Writes are DISCARDED. The relay must never send anything of its own to the client: the only
// thing it could send at this point is an alert from a handshake it has no business
// completing, and the client would take it as coming from the Station.
type recorder struct {
	// EMBEDDED, so deadlines and addresses pass straight through and only the two methods
	// that matter are written here. Spelling out the whole net.Conn surface by hand would be
	// six more places to get a forwarding wrong, for no behaviour.
	net.Conn
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *recorder) Read(p []byte) (int, error) {
	n, err := r.Conn.Read(p)
	if n > 0 {
		r.mu.Lock()
		r.buf.Write(p[:n])
		r.mu.Unlock()
	}
	return n, err
}

func (r *recorder) seen() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.buf.Bytes()...)
}

// Write is DISCARDED, and Close with it. The only thing this process could send the client
// here is an alert from a handshake it has no business completing - and the client would take
// it as coming from the Station. Everything else (deadlines, addresses) is the embedded
// connection's, which is why they are not written out here.
func (r *recorder) Write(p []byte) (int, error) { return len(p), nil }

// Close is a no-op: the caller owns the underlying connection and closes it itself.
func (r *recorder) Close() error { return nil }
