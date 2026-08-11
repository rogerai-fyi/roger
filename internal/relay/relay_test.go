package relay

// relay_test.go is the data plane's contract, and the property under test is negative: the
// Tower CARRIES the traffic and CANNOT READ IT.
//
// Negative properties are easy to fake, so these tests do not ask the relay whether it
// decrypted anything. They stand a real TLS server on one side, a real TLS client on the
// other, put a secret through, and inspect the bytes the relay actually touched.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stationTLS stands up a real HTTPS server with a certificate for one name - a Station, as
// far as anything else here can tell.
func stationTLS(t *testing.T, name string, handler http.Handler) (addr string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              []string{name},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	pool = x509.NewCertPool()
	pool.AddCert(leaf)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
	})
	require.NoError(t, err)
	srv := &http.Server{Handler: handler}
	// A Station that keeps connections alive is realistic, but it means the splice only ends
	// when something else closes it - and these tests are about what the relay reports when a
	// connection FINISHES. Disabling keep-alive makes each request its own connection, which
	// is the shape the assertions are written against.
	srv.SetKeepAlivesEnabled(false)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), pool
}

// relayOn starts a relay in front of one Station and returns its address plus a place the
// per-connection stats land.
func relayOn(t *testing.T, r *Relay) (addr string, stats *statsLog) {
	t.Helper()
	if r.IdleTimeout == 0 {
		// Short, so a test that goes wrong fails in seconds rather than sitting on the
		// production five-minute window.
		r.IdleTimeout = 2 * time.Second
	}
	log := &statsLog{}
	r.OnClose = log.add
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = r.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); _ = ln.Close() })
	return ln.Addr().String(), log
}

type statsLog struct {
	mu  sync.Mutex
	all []Stats
}

func (s *statsLog) add(st Stats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all = append(s.all, st)
}

func (s *statsLog) get() []Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Stats(nil), s.all...)
}

// clientVia builds an HTTPS client that dials the RELAY but speaks TLS to the Station name.
func clientVia(relayAddr, name string, pool *x509.CertPool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", relayAddr)
			},
			TLSClientConfig: &tls.Config{ServerName: name, RootCAs: pool},
		},
		Timeout: 10 * time.Second,
	}
}

// THE WHOLE POINT. A consumer talks to a Station through the Tower, and the Tower cannot read
// a byte of it.
func TestTheRelayCarriesTheSessionAndCannotReadIt(t *testing.T) {
	const name = "st-abc123.relay.example"
	const prompt = "the patient's diagnosis is confidential"
	const answer = "and so is this completion"

	station, pool := stationTLS(t, name, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.Contains(t, string(body), prompt, "the Station received the real request")
		_, _ = w.Write([]byte(answer))
	}))

	// EVERY BYTE THE RELAY TOUCHES, captured as it passes.
	var seen [][]byte
	var mu sync.Mutex
	r := &Relay{
		Router: routerFunc(func(sni string) (string, bool) { return station, sni == name }),
		Dial: func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			c, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			return &tapConn{Conn: c, onBytes: func(b []byte) {
				mu.Lock()
				seen = append(seen, append([]byte(nil), b...))
				mu.Unlock()
			}}, nil
		},
	}
	relayAddr, log := relayOn(t, r)

	client := clientVia(relayAddr, name, pool)
	resp, err := client.Post(
		"https://"+name+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"messages":[{"content":"`+prompt+`"}]}`))
	require.NoError(t, err)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, answer, string(got), "the consumer got their answer through the relay")
	// Keep-alive would otherwise hold the splice open, and the stats land when it closes.
	client.CloseIdleConnections()

	// SNAPSHOT, then release. Holding this across the wait below deadlocks the relay: its
	// copy goroutine takes the same lock to record bytes, so the connection could never
	// finish and the stats could never arrive.
	mu.Lock()
	captured := append([][]byte(nil), seen...)
	mu.Unlock()

	require.NotEmpty(t, captured, "the relay did carry the traffic")
	for _, b := range captured {
		require.NotContains(t, string(b), prompt, "the relay could read the prompt")
		require.NotContains(t, string(b), answer, "the relay could read the completion")
		require.NotContains(t, string(b), "messages", "the request's shape leaked to the relay")
	}

	// What it MAY see is the routing metadata it needs to do its job and be paid for it.
	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 5*time.Second, 10*time.Millisecond)
	st := log.get()[0]
	require.Equal(t, name, st.ServerName)
	require.Equal(t, station, st.Upstream)
	require.Positive(t, st.ToStation, "it can count what it carried")
	require.Positive(t, st.ToClient)
	require.Positive(t, st.Duration())
	require.NoError(t, st.Err)
}

// tapConn reports every byte written to the Station - which is exactly what a Tower operator
// running tcpdump on their own machine would see.
type tapConn struct {
	net.Conn
	onBytes func([]byte)
}

func (c *tapConn) Write(p []byte) (int, error) {
	c.onBytes(p)
	return c.Conn.Write(p)
}

// CloseWrite is forwarded so the relay can half-close through this wrapper, exactly as it
// would through a bare TCP connection. A wrapper that swallowed it would make the test
// exercise the fallback path instead of the one production uses.
func (c *tapConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *tapConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.onBytes(p[:n])
	}
	return n, err
}

// A name this Tower does not carry is refused WITHOUT dialling anything. A relay that
// connected first would be a way to make somebody else's machine open connections to
// addresses of your choosing.
func TestAnUnknownNameIsRefusedWithoutDialling(t *testing.T) {
	const name = "st-known.relay.example"
	station, pool := stationTLS(t, name, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	var dialled int
	var mu sync.Mutex
	r := &Relay{
		Router: routerFunc(func(sni string) (string, bool) { return station, sni == name }),
		Dial: func(ctx context.Context, addr string) (net.Conn, error) {
			mu.Lock()
			dialled++
			mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		},
	}
	relayAddr, log := relayOn(t, r)

	client := clientVia(relayAddr, "st-nobody.relay.example", pool)
	_, err := client.Get("https://st-nobody.relay.example/")
	require.Error(t, err, "a name this Tower does not carry gets nothing")
	client.CloseIdleConnections()

	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 5*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, log.get()[0].Err, ErrNotOurs)

	mu.Lock()
	require.Zero(t, dialled, "it must not open a connection for a name it does not carry")
	mu.Unlock()
}

// A connection that names nobody is refused. Common enough - anything connecting by raw IP -
// and there is nothing to route it to.
func TestAConnectionThatNamesNobodyIsRefused(t *testing.T) {
	r := &Relay{
		Router:      routerFunc(func(string) (string, bool) { return "", false }),
		PeekTimeout: 2 * time.Second,
	}
	relayAddr, log := relayOn(t, r)

	// A TLS client with no server name at all.
	conn, err := net.Dial("tcp", relayAddr)
	require.NoError(t, err)
	defer conn.Close()
	_ = tls.Client(conn, &tls.Config{InsecureSkipVerify: true}).Handshake()

	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 5*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, log.get()[0].Err, ErrNoServerName)
}

// Bytes that are not TLS at all are refused rather than forwarded. Forwarding them would make
// the Tower a general-purpose proxy for whatever an attacker wanted to reach.
func TestSomethingThatIsNotTLSIsRefused(t *testing.T) {
	var dialled int
	r := &Relay{
		Router: routerFunc(func(string) (string, bool) { return "127.0.0.1:1", true }),
		Dial: func(context.Context, string) (net.Conn, error) {
			dialled++
			return nil, errors.New("should not happen")
		},
		PeekTimeout: 2 * time.Second,
	}
	relayAddr, log := relayOn(t, r)

	conn, err := net.Dial("tcp", relayAddr)
	require.NoError(t, err)
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))
	_ = conn.Close()

	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 5*time.Second, 10*time.Millisecond)
	require.Error(t, log.get()[0].Err)
	require.Zero(t, dialled)
}

// A connection that opens and then says nothing is closed. Otherwise it is a free slot held
// for as long as the other side likes, which is the cheapest denial of service there is.
func TestASilentConnectionIsClosed(t *testing.T) {
	r := &Relay{
		Router:      routerFunc(func(string) (string, bool) { return "", false }),
		PeekTimeout: 150 * time.Millisecond,
	}
	relayAddr, log := relayOn(t, r)

	conn, err := net.Dial("tcp", relayAddr)
	require.NoError(t, err)
	defer conn.Close()

	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 5*time.Second, 10*time.Millisecond)
	require.Error(t, log.get()[0].Err, "a connection that never spoke is not carried")
}

// A Station that cannot be reached is reported, and the consumer's connection ends rather
// than hanging.
func TestAnUnreachableStationEndsTheConnection(t *testing.T) {
	const name = "st-gone.relay.example"
	_, pool := stationTLS(t, name, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	r := &Relay{Router: routerFunc(func(string) (string, bool) { return "127.0.0.1:1", true })}
	relayAddr, log := relayOn(t, r)

	client := clientVia(relayAddr, name, pool)
	_, err := client.Get("https://" + name + "/")
	require.Error(t, err)
	client.CloseIdleConnections()

	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 5*time.Second, 10*time.Millisecond)
	require.Error(t, log.get()[0].Err)
	require.Equal(t, name, log.get()[0].ServerName, "and it still says which Station it was for")
}

// THE COUNTS ARE THE METERING, so they have to be real. A large body must be reported as a
// large body - an operator is paid on this, and a Tower that under-reports its own work is
// only cheating itself, but one that cannot count at all cannot be paid at all.
func TestTheByteCountsReflectWhatWasCarried(t *testing.T) {
	const name = "st-counts.relay.example"
	big := strings.Repeat("x", 300*1024)
	station, pool := stationTLS(t, name, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(big))
	}))

	r := &Relay{Router: routerFunc(func(sni string) (string, bool) { return station, sni == name })}
	relayAddr, log := relayOn(t, r)

	client := clientVia(relayAddr, name, pool)
	resp, err := client.Post("https://"+name+"/", "text/plain", strings.NewReader(big))
	require.NoError(t, err)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.Len(t, got, len(big))
	client.CloseIdleConnections()

	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 10*time.Second, 10*time.Millisecond)
	st := log.get()[0]
	require.Greater(t, st.ToStation, int64(len(big)), "the request it carried, plus the handshake")
	require.Greater(t, st.ToClient, int64(len(big)), "and the answer")
}

// A ceiling is enforced, so one connection cannot use a Tower's whole month of egress.
func TestAConnectionCannotExceedItsCeiling(t *testing.T) {
	const name = "st-limit.relay.example"
	station, pool := stationTLS(t, name, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(strings.Repeat("y", 200*1024)))
	}))

	r := &Relay{
		Router:   routerFunc(func(sni string) (string, bool) { return station, sni == name }),
		MaxBytes: 32 * 1024,
	}
	relayAddr, log := relayOn(t, r)

	client := clientVia(relayAddr, name, pool)
	resp, err := client.Post("https://"+name+"/",
		"text/plain", strings.NewReader(strings.Repeat("z", 200*1024)))
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	require.Error(t, err, "a connection over its ceiling does not complete")
	client.CloseIdleConnections()

	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 10*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, log.get()[0].Err, ErrTooLarge)
}

// Serve returns cleanly when its listener closes, rather than reporting a shutdown as a
// failure - a Tower stopping is an ordinary thing.
func TestServeEndsCleanlyOnShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	r := &Relay{Router: routerFunc(func(string) (string, bool) { return "", false })}

	done := make(chan error, 1)
	go func() { done <- r.Serve(context.Background(), ln) }()
	require.NoError(t, ln.Close())

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return when its listener closed")
	}
}

// Two connections at once are carried independently, which is the ordinary case for a Tower
// with more than one customer.
func TestConcurrentConnectionsAreCarriedIndependently(t *testing.T) {
	const name = "st-busy.relay.example"
	station, pool := stationTLS(t, name, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = fmt.Fprintf(w, "echo:%s", body)
	}))

	r := &Relay{Router: routerFunc(func(sni string) (string, bool) { return station, sni == name })}
	relayAddr, log := relayOn(t, r)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			payload := fmt.Sprintf("request-%d", n)
			client := clientVia(relayAddr, name, pool)
			defer client.CloseIdleConnections()
			resp, err := client.Post("https://"+name+"/", "text/plain", strings.NewReader(payload))
			if !assertNoErr(t, err) {
				return
			}
			got, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !assertNoErr(t, err) {
				return
			}
			// Each connection got ITS OWN answer, not somebody else's.
			require.Equal(t, "echo:"+payload, string(got))
		}(i)
	}
	wg.Wait()

	require.Eventually(t, func() bool { return len(log.get()) >= 8 }, 10*time.Second, 10*time.Millisecond)
}

func assertNoErr(t *testing.T, err error) bool {
	t.Helper()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return false
	}
	return true
}

// --- the parts a full connection does not reach ------------------------------

// The defaults are bounded. A relay built with a zero config must not wait forever for a
// connection to speak, or hold a quiet splice open indefinitely.
func TestTheDefaultsAreBounded(t *testing.T) {
	r := &Relay{}
	require.Equal(t, defaultPeekTimeout, r.peekTimeout())
	require.Equal(t, defaultIdleTimeout, r.idleTimeout())
	require.Positive(t, r.peekTimeout())
	require.Positive(t, r.idleTimeout())

	set := &Relay{PeekTimeout: time.Second, IdleTimeout: 2 * time.Second}
	require.Equal(t, time.Second, set.peekTimeout())
	require.Equal(t, 2*time.Second, set.idleTimeout())
}

// A CONNECTION THAT CANNOT HALF-CLOSE still finishes, and still reports no fault. Not every
// connection a Tower carries is a bare TCP socket - anything wrapped loses CloseWrite - and
// the fallback that unblocks the other direction must not look like an error afterwards.
func TestAConnectionWithoutHalfCloseStillEndsCleanly(t *testing.T) {
	const name = "st-nohalfclose.relay.example"
	station, pool := stationTLS(t, name, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	}))

	r := &Relay{
		Router: routerFunc(func(sni string) (string, bool) { return station, sni == name }),
		Dial: func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			c, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			// Deliberately hides CloseWrite, so the relay takes the deadline fallback.
			return &plainConn{Conn: c}, nil
		},
	}
	relayAddr, log := relayOn(t, r)

	client := clientVia(relayAddr, name, pool)
	resp, err := client.Get("https://" + name + "/")
	require.NoError(t, err)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, "ok", string(got))
	client.CloseIdleConnections()

	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 10*time.Second, 10*time.Millisecond)
	st := log.get()[0]
	require.NoError(t, st.Err,
		"unblocking the other direction is how a splice ends, not something that went wrong")
	require.Positive(t, st.ToClient)
}

// plainConn is a net.Conn and nothing more - no CloseWrite to find.
type plainConn struct{ net.Conn }

// A peer going away is a normal end; anything else is a fault worth reporting. These numbers
// are what an operator is judged on, so the line between them matters.
func TestWhatCountsAsANormalClose(t *testing.T) {
	for _, err := range []error{io.EOF, net.ErrClosed, syscall.ECONNRESET, syscall.EPIPE} {
		require.True(t, isNormalClose(err), "%v is a peer going away", err)
	}
	for _, err := range []error{
		errors.New("something else"), os.ErrDeadlineExceeded, ErrTooLarge, syscall.ECONNREFUSED,
	} {
		require.False(t, isNormalClose(err), "%v is worth reporting", err)
	}
}

// A Station that accepts the connection and then drops it before the handshake completes is
// reported, rather than surfacing as a silent hang.
func TestAStationThatVanishesMidHandshakeIsReported(t *testing.T) {
	const name = "st-flaky.relay.example"
	_, pool := stationTLS(t, name, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	// A listener that accepts and immediately closes.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() {
		for {
			c, aerr := dead.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = dead.Close() })

	r := &Relay{Router: routerFunc(func(string) (string, bool) { return dead.Addr().String(), true })}
	relayAddr, log := relayOn(t, r)

	client := clientVia(relayAddr, name, pool)
	_, err = client.Get("https://" + name + "/")
	require.Error(t, err, "the consumer is not left hanging")
	client.CloseIdleConnections()

	require.Eventually(t, func() bool { return len(log.get()) > 0 }, 10*time.Second, 10*time.Millisecond)
	require.Equal(t, name, log.get()[0].ServerName)
}

// routerFunc adapts a function to Router. It lives here rather than in the package because
// the only production router is a map, and an exported adapter nothing uses is surface that
// has to be kept working for no one.
type routerFunc func(string) (string, bool)

func (f routerFunc) Upstream(name string) (string, bool) { return f(name) }
