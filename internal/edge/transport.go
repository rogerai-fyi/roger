package edge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
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
// mDNS group, sending to 224.0.0.251:5353. It REJOINS: interfaces come and go (a container
// reconnected, a USB Ethernet adapter re-enumerating, a network stack restarted), and a socket on a
// vanished interface only ever answers "no such device" - so a send rescans, at most every
// rejoinEvery, and at once after a failed write.
type multicastTransport struct {
	group *net.UDPAddr

	list   func() []net.Interface
	listen func(*net.Interface) (*net.UDPConn, error)
	usable func(net.Interface) bool

	cmu      sync.Mutex
	conns    map[string]*net.UDPConn // by interface name#index: a re-created interface is a new one
	lastScan time.Time

	mu   sync.Mutex
	recv chan packet
	done chan struct{}
	once sync.Once
	dl   time.Time
}

// rejoinEvery bounds how often a send rescans the interfaces.
const rejoinEvery = 10 * time.Second

type packet struct {
	b    []byte
	from net.Addr
}

// NewMulticastTransport joins the mDNS group on every private, multicast-capable
// interface. It returns ErrNoLAN when there is none: a machine with only loopback has
// no LAN to discover on, and saying so once beats browsing nothing forever.
func NewMulticastTransport(ifaces []net.Interface) (Transport, error) {
	return NewRejoiningMulticastTransport(func() []net.Interface { return ifaces })
}

// NewRejoiningMulticastTransport is NewMulticastTransport over a live interface list, rejoining as
// interfaces come and go.
func NewRejoiningMulticastTransport(list func() []net.Interface) (Transport, error) {
	group := &net.UDPAddr{IP: net.ParseIP(mdnsGroupV4), Port: mdnsPort}
	listen := func(ifi *net.Interface) (*net.UDPConn, error) { return net.ListenMulticastUDP("udp4", ifi, group) }
	usable := func(ifi net.Interface) bool {
		return ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 && HasLAN(InterfaceAddrs([]net.Interface{ifi}))
	}
	return newRejoiningTransport(list, listen, usable, group)
}

func newRejoiningTransport(list func() []net.Interface, listen func(*net.Interface) (*net.UDPConn, error),
	usable func(net.Interface) bool, group *net.UDPAddr) (*multicastTransport, error) {
	t := &multicastTransport{
		group: group, list: list, listen: listen, usable: usable,
		conns: map[string]*net.UDPConn{},
		recv:  make(chan packet, 256),
		done:  make(chan struct{}),
	}
	t.cmu.Lock()
	t.rejoinLocked()
	n := len(t.conns)
	t.cmu.Unlock()
	if n == 0 {
		close(t.done)
		return nil, ErrNoLAN
	}
	return t, nil
}

// rejoinLocked joins every usable interface not yet joined and drops every socket whose interface is
// gone. An interface that will not join is skipped, not fatal. Callers hold cmu.
func (t *multicastTransport) rejoinLocked() {
	t.lastScan = time.Now()
	want := map[string]net.Interface{}
	for _, ifi := range t.list() {
		if t.usable(ifi) {
			want[fmt.Sprintf("%s#%d", ifi.Name, ifi.Index)] = ifi
		}
	}
	for key, c := range t.conns {
		if _, ok := want[key]; !ok {
			_ = c.Close()
			delete(t.conns, key)
		}
	}
	for key, ifi := range want {
		if _, ok := t.conns[key]; ok {
			continue
		}
		ifi := ifi
		c, err := t.listen(&ifi)
		if err != nil {
			continue
		}
		t.conns[key] = c
		go t.pump(c)
	}
}

// joined is the interfaces joined now, for a test to read.
func (t *multicastTransport) joined() []string {
	t.cmu.Lock()
	defer t.cmu.Unlock()
	out := make([]string, 0, len(t.conns))
	for k := range t.conns {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// closeAll closes every socket without forgetting them - a test's vanished devices.
func (t *multicastTransport) closeAll() {
	t.cmu.Lock()
	defer t.cmu.Unlock()
	for _, c := range t.conns {
		_ = c.Close()
	}
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
	select {
	case <-t.done:
		return net.ErrClosed // closed is closed: nothing to rejoin
	default:
	}
	t.cmu.Lock()
	defer t.cmu.Unlock()
	if time.Since(t.lastScan) >= rejoinEvery {
		t.rejoinLocked()
	}
	if err := t.sendLocked(b); err == nil {
		return nil
	}
	// A failed write is usually a vanished interface: rejoin now and try once more. A socket that
	// failed is replaced even when its interface still reads the same (its device was re-created).
	for key, c := range t.conns {
		_ = c.Close()
		delete(t.conns, key)
	}
	t.rejoinLocked()
	return t.sendLocked(b)
}

// sendLocked writes to every joined interface; it fails only when no write succeeded.
func (t *multicastTransport) sendLocked(b []byte) error {
	var last error
	sent := false
	for _, c := range t.conns {
		if _, err := c.WriteTo(b, t.group); err != nil {
			last = err
			continue
		}
		sent = true
	}
	if sent {
		return nil
	}
	if last == nil {
		last = ErrNoLAN
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
	// A side that only listens (the responder) never sends until it hears a query, and cannot hear
	// one on a vanished interface: it rejoins here too, once the rescan is due.
	select {
	case <-t.done:
	default:
		t.cmu.Lock()
		if time.Since(t.lastScan) >= rejoinEvery {
			t.rejoinLocked()
		}
		t.cmu.Unlock()
	}
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
	t.cmu.Lock()
	defer t.cmu.Unlock()
	for key, c := range t.conns {
		_ = c.Close()
		delete(t.conns, key)
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
	liveIPs func() []net.IP // when set, the addresses are read fresh for every answer

	mu sync.Mutex
	ad Advert
}

// NewResponder builds the advertising half. host is the mDNS host label (a node's id is
// used, so the label is as unique as the identity it names).
func NewResponder(tp Transport, service string, ad Advert, ips []net.IP) *Responder {
	return &Responder{tp: tp, service: service, host: ad.NodeID, ips: ips, ad: ad}
}

// NewLiveResponder is NewResponder with the addresses read fresh for every answer: a member whose
// network came back with a new address says the new one, not the one it had at start.
func NewLiveResponder(tp Transport, service string, ad Advert, ips func() []net.IP) *Responder {
	return &Responder{tp: tp, service: service, host: ad.NodeID, liveIPs: ips, ad: ad}
}

// Packet renders the advertisement exactly as it goes on the wire. Exported so the
// advertising rules can be asserted against real bytes rather than against a struct.
func (r *Responder) Packet() ([]byte, error) {
	r.mu.Lock()
	ad := r.ad
	r.mu.Unlock()
	ips := r.ips
	if r.liveIPs != nil {
		ips = r.liveIPs()
	}
	return buildResponse(r.service, ad.NodeID, r.host, uint16(ad.Port), ad.TXT(), ips)
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
		s, hasSRV := srv[inst]
		ad := advertFromTXT(txt[inst])
		idFromTXT := ad.NodeID != "" // a real self-identification, not the instance-name fallback
		if hasSRV {
			ad.Host = trimRoot(s.target)
			if ad.Port == 0 {
				ad.Port = int(s.port)
			}
			if ip, ok := hosts[trimRoot(s.target)]; ok {
				ad.IP = ip
			}
		}
		if ad.IP == nil {
			if ip := addrIP(from); ip != nil {
				// No address record: fall back to where the packet came from. Still only a
				// hint, and the certificate check is unchanged either way.
				ad.IP = ip
			}
		}
		if ad.NodeID == "" {
			ad.NodeID = strings.TrimSuffix(inst, "."+svc)
		}
		// A CANDIDATE (no account) joins by adoption/claim and is NEVER dialed from here, so it
		// need not carry a certificate fingerprint or a servable SRV/address - only an identity.
		// This is the whole point of a phone: it advertises itself with no cert yet. A MEMBER (an
		// account set) IS dialed and verified, so it must be fully usable (SRV + fingerprint + addr).
		if ad.Account == "" {
			// Keep it only if it actually named itself (id= in its TXT); a bare PTR with no TXT is
			// noise, not a device.
			if idFromTXT {
				out = append(out, ad)
			}
			continue
		}
		if !hasSRV {
			continue
		}
		if err := ad.usable(); err != nil {
			continue
		}
		out = append(out, ad)
	}
	return out
}
