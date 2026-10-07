package main

import (
	"errors"
	"net"
	"strconv"
	"sync"

	"rogerai.fm/roger/v6/internal/localplane"
)

// edgeInterfaceAddrs, edgeIsLoopback and edgeIsPrivate are this machine's interface addresses and
// the address rules (seams so the bind rule is tested on real sockets).
var (
	edgeInterfaceAddrs = net.InterfaceAddrs
	edgeIsLoopback     = func(ip net.IP) bool { return ip.IsLoopback() }
	edgeIsPrivate      = localplane.IsPrivateAddr
)

// edgeListen opens an Edge listener (the face or the authority). An explicit host in bind is
// honoured as given. A wildcard bind (":port") listens on loopback and each private interface
// address only, all on one port, so an Edge never answers on a public interface
// (features/lan/regressions.feature). Link-local addresses are skipped: they need a zone.
func edgeListen(bind string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return nil, err
	}
	if host != "" {
		return net.Listen("tcp", bind)
	}
	addrs, err := edgeInterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		if edgeIsLoopback(ipn.IP) || edgeIsPrivate(ipn.IP) {
			ips = append(ips, ipn.IP)
		}
	}
	m := &multiListener{conns: make(chan net.Conn), done: make(chan struct{})}
	for _, ip := range ips {
		ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), port))
		if err != nil {
			continue // that address cannot take the port: the others still serve
		}
		if len(m.lns) == 0 {
			port = strconv.Itoa(ln.Addr().(*net.TCPAddr).Port) // every address shares the first port
		}
		m.lns = append(m.lns, ln)
	}
	if len(m.lns) == 0 {
		return nil, errors.New("no loopback or private interface could be bound on port " + port)
	}
	for _, ln := range m.lns {
		go m.accept(ln)
	}
	return m, nil
}

// multiListener is one listener over several addresses on the same port.
type multiListener struct {
	lns   []net.Listener
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (m *multiListener) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		select {
		case m.conns <- c:
		case <-m.done:
			_ = c.Close()
			return
		}
	}
}

func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case <-m.done:
		return nil, net.ErrClosed
	}
}

func (m *multiListener) Close() error {
	m.once.Do(func() {
		close(m.done)
		for _, ln := range m.lns {
			_ = ln.Close()
		}
	})
	return nil
}

func (m *multiListener) Addr() net.Addr { return m.lns[0].Addr() }
