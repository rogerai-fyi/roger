package edge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// ErrNoLAN is what discovery reports when there is no local network to discover on.
var ErrNoLAN = errors.New("discovery is unavailable: no private LAN interface")

// Transport is the packet plane discovery speaks on: send one packet to everybody,
// receive whatever everybody sent. That is the whole of what mDNS needs from the
// network, and naming it makes the multicast group a detail rather than a dependency.
type Transport interface {
	// Send writes one packet to the whole plane.
	Send(b []byte) error
	// Recv reads the next packet. It returns a net.Error with Timeout() true when the
	// read deadline passes, which is the ordinary way a browse window ends.
	Recv(b []byte) (int, net.Addr, error)
	// SetReadDeadline bounds Recv.
	SetReadDeadline(t time.Time) error
	// Close releases the sockets.
	Close() error
}

// multicastTransport is the real thing: one socket per usable interface, joined to the
// mDNS group, sending to 224.0.0.251:5353.
type multicastTransport struct {
	conns []*net.UDPConn
	group *net.UDPAddr

	mu   sync.Mutex
	recv chan packet
	done chan struct{}
	once sync.Once
	dl   time.Time
}

type packet struct {
	b    []byte
	from net.Addr
}

// NewMulticastTransport joins the mDNS group on every private, multicast-capable
// interface. It returns ErrNoLAN when there is none: a machine with only loopback has
// no LAN to discover on, and saying so once beats browsing nothing forever.
func NewMulticastTransport(ifaces []net.Interface) (Transport, error) {
	group := &net.UDPAddr{IP: net.ParseIP(mdnsGroupV4), Port: mdnsPort}
	t := &multicastTransport{
		group: group,
		recv:  make(chan packet, 256),
		done:  make(chan struct{}),
	}
	for i := range ifaces {
		ifi := ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		if !HasLAN(InterfaceAddrs([]net.Interface{ifi})) {
			continue
		}
		c, err := net.ListenMulticastUDP("udp4", &ifi, group)
		if err != nil {
			continue // an interface that will not join is skipped, not fatal
		}
		t.conns = append(t.conns, c)
		go t.pump(c)
	}
	if len(t.conns) == 0 {
		close(t.done)
		return nil, ErrNoLAN
	}
	return t, nil
}

// pump moves one socket's packets onto the shared channel, so Recv is a single read
// regardless of how many interfaces joined.
func (t *multicastTransport) pump(c *net.UDPConn) {
	buf := make([]byte, maxMsgSize)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			return
		}
		select {
		case t.recv <- packet{b: append([]byte(nil), buf[:n]...), from: from}:
		case <-t.done:
			return
		default: // the reader is behind: drop, never block the network
		}
	}
}

func (t *multicastTransport) Send(b []byte) error {
	var last error
	for _, c := range t.conns {
		if _, err := c.WriteTo(b, t.group); err != nil {
			last = err
		}
	}
	return last
}

func (t *multicastTransport) SetReadDeadline(dl time.Time) error {
	t.mu.Lock()
	t.dl = dl
	t.mu.Unlock()
	return nil
}

func (t *multicastTransport) Recv(b []byte) (int, net.Addr, error) {
	t.mu.Lock()
	dl := t.dl
	t.mu.Unlock()
	var timer <-chan time.Time
	if !dl.IsZero() {
		tm := time.NewTimer(time.Until(dl))
		defer tm.Stop()
		timer = tm.C
	}
	select {
	case p := <-t.recv:
		return copy(b, p.b), p.from, nil
	case <-timer:
		return 0, nil, timeoutError{}
	case <-t.done:
		return 0, nil, net.ErrClosed
	}
}

func (t *multicastTransport) Close() error {
	t.once.Do(func() { close(t.done) })
	for _, c := range t.conns {
		_ = c.Close()
	}
	return nil
}

// timeoutError is the deadline signal, in the shape callers already test for.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// isTimeout reports whether err is an expired read deadline - the ordinary end of a
// browse window, not a failure.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// --- responder ------------------------------------------------------------

// Responder answers browse queries for one node. It answers ONLY the service it was
// given, and it answers with the same fields every time: there is no state here a peer
// can drive.
type Responder struct {
	tp      Transport
	service string
	host    string
	ips     []net.IP

	mu sync.Mutex
	ad Advert
}

// NewResponder builds the advertising half. host is the mDNS host label (a node's id is
// used, so the label is as unique as the identity it names).
func NewResponder(tp Transport, service string, ad Advert, ips []net.IP) *Responder {
	return &Responder{tp: tp, service: service, host: ad.NodeID, ips: ips, ad: ad}
}

// Packet renders the advertisement exactly as it goes on the wire. Exported so the
// advertising rules can be asserted against real bytes rather than against a struct.
func (r *Responder) Packet() ([]byte, error) {
	r.mu.Lock()
	ad := r.ad
	r.mu.Unlock()
	return buildResponse(r.service, ad.NodeID, r.host, uint16(ad.Port), ad.TXT(), r.ips)
}

// Serve answers browse queries until ctx is done.
func (r *Responder) Serve(ctx context.Context) error {
	buf := make([]byte, maxMsgSize)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = r.tp.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := r.tp.Recv(buf)
		if err != nil {
			if isTimeout(err) {
				continue
			}
			return err
		}
		m, err := parseMessage(buf[:n])
		if err != nil || m.response {
			continue
		}
		if !r.asksForUs(m) {
			continue
		}
		pkt, err := r.Packet()
		if err != nil {
			continue
		}
		if err := r.tp.Send(pkt); err != nil {
			continue
		}
	}
}

// asksForUs reports whether the message contains a PTR question for our service.
func (r *Responder) asksForUs(m *message) bool {
	want := r.service + "." + mdnsDomain
	for _, q := range m.questions {
		if q.typ == typePTR && equalName(q.name, want) {
			return true
		}
	}
	return false
}

// equalName compares DNS names case-insensitively, without the trailing root.
func equalName(a, b string) bool {
	a, b = trimRoot(a), trimRoot(b)
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 32
		}
		if 'A' <= y && y <= 'Z' {
			y += 32
		}
		if x != y {
			return false
		}
	}
	return true
}

func trimRoot(s string) string {
	for len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}

// --- browser --------------------------------------------------------------

// BrowseResult is one pass of browsing: the records that were kept, how many arrived,
// and how many were dropped once the ceiling was reached.
type BrowseResult struct {
	Adverts []Advert
	Seen    int // distinct node ids that arrived this pass
	Dropped int // arrived, refused storage because the ceiling was reached
}

// Browse sends one query and collects answers until window expires.
//
// The ceiling is the point of the shape of this function. A hostile LAN can put as many
// advertisements on the group as it can generate; the bound on what that costs us is
// applied HERE, before anything is stored and before a single dial is attempted, so a
// flood buys the flooder a bounded map and a counter.
func Browse(ctx context.Context, tp Transport, service string, window time.Duration, ceiling int) (BrowseResult, error) {
	var res BrowseResult
	q, err := buildQuery(service)
	if err != nil {
		return res, err
	}
	if err := tp.Send(q); err != nil {
		return res, fmt.Errorf("discovery query: %w", err)
	}
	deadline := time.Now().Add(window)
	buf := make([]byte, maxMsgSize)
	kept := map[string]Advert{}
	seen := map[string]bool{}
	for {
		if ctx.Err() != nil {
			break
		}
		now := time.Now()
		if !now.Before(deadline) {
			break
		}
		step := deadline.Sub(now)
		if step > 200*time.Millisecond {
			step = 200 * time.Millisecond
		}
		_ = tp.SetReadDeadline(now.Add(step))
		n, from, err := tp.Recv(buf)
		if err != nil {
			if isTimeout(err) {
				continue
			}
			break
		}
		m, err := parseMessage(buf[:n])
		if err != nil || !m.response {
			continue
		}
		for _, ad := range advertsFrom(m, service, from) {
			if seen[ad.NodeID] {
				continue
			}
			seen[ad.NodeID] = true
			if ceiling > 0 && len(kept) >= ceiling {
				res.Dropped++
				continue
			}
			kept[ad.NodeID] = ad
		}
	}
	res.Seen = len(seen)
	for _, ad := range kept {
		res.Adverts = append(res.Adverts, ad)
	}
	return res, nil
}

// advertsFrom assembles the PTR/SRV/TXT/A records of one response into advertisements.
// A record set that does not add up to something dialable is dropped here: there is
// nothing to check, so there is nothing to keep.
func advertsFrom(m *message, service string, from net.Addr) []Advert {
	svc := service + "." + mdnsDomain
	instances := map[string]bool{}
	srv := map[string]record{}
	txt := map[string][]string{}
	hosts := map[string]net.IP{}
	for _, r := range m.answers {
		switch r.typ {
		case typePTR:
			if equalName(r.name, svc) {
				instances[trimRoot(r.ptr)] = true
			}
		case typeSRV:
			srv[trimRoot(r.name)] = r
		case typeTXT:
			txt[trimRoot(r.name)] = r.txt
		case typeA, typeAAAA:
			if _, ok := hosts[trimRoot(r.name)]; !ok {
				hosts[trimRoot(r.name)] = r.ip
			}
		}
	}
	var out []Advert
	for inst := range instances {
		s, ok := srv[inst]
		if !ok {
			continue
		}
		ad := advertFromTXT(txt[inst])
		ad.Host = trimRoot(s.target)
		if ad.Port == 0 {
			ad.Port = int(s.port)
		}
		if ip, ok := hosts[trimRoot(s.target)]; ok {
			ad.IP = ip
		} else if ip := addrIP(from); ip != nil {
			// No address record: fall back to where the packet came from. It is still
			// only a hint, and the certificate check is unchanged either way.
			ad.IP = ip
		}
		if ad.NodeID == "" {
			ad.NodeID = strings.TrimSuffix(inst, "."+svc)
		}
		if err := ad.usable(); err != nil {
			continue
		}
		out = append(out, ad)
	}
	return out
}
