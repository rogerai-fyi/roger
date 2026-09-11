package edge

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// Environment knobs, with the spec's defaults.
const (
	EnvDiscovery = "ROGERAI_EDGE_DISCOVERY"          // 1|0, default 1
	EnvService   = "ROGERAI_EDGE_MDNS_SERVICE"       // default _rogerai._tcp
	EnvInterval  = "ROGERAI_EDGE_DISCOVERY_INTERVAL" // default 30s
	EnvTTL       = "ROGERAI_EDGE_CANDIDATE_TTL"      // default 5m
	EnvCeiling   = "ROGERAI_EDGE_CANDIDATE_CEILING"  // default 256
)

// Defaults.
const (
	DefaultInterval = 30 * time.Second
	DefaultTTL      = 5 * time.Minute
	DefaultCeiling  = 256
	DefaultWindow   = 2 * time.Second
)

// Config is the knob set.
type Config struct {
	Enabled bool
	Service string
	// Interval is how often a browse happens.
	Interval time.Duration
	// CandidateTTL is both how long an unadopted candidate is remembered and how long
	// a member may go unheard before it is shown DARK.
	CandidateTTL time.Duration
	// Ceiling bounds how many records ONE interval may retain. A hostile LAN can
	// generate advertisements faster than any host can dial them; this is what turns
	// that into a bounded map and a counter.
	Ceiling int
	// Window is how long one browse listens.
	Window time.Duration
	// ManualPasses leaves the browse schedule to the caller: Start brings the
	// advertising half up and returns, and each pass happens when RunOnce is called.
	// That is what a one-shot `roger edge scan` wants, and what a long-running node
	// does NOT want - so the zero value is the daemon behavior.
	ManualPasses bool
}

// ConfigFromEnv reads the knobs. getenv is a seam (nil means os.Getenv) so a test can
// set them without racing another test's environment.
func ConfigFromEnv(getenv func(string) string) Config {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := Config{
		Enabled:      getenv(EnvDiscovery) != "0",
		Service:      DefaultService,
		Interval:     DefaultInterval,
		CandidateTTL: DefaultTTL,
		Ceiling:      DefaultCeiling,
		Window:       DefaultWindow,
	}
	if s := strings.TrimSpace(getenv(EnvService)); s != "" {
		c.Service = s
	}
	if d, err := time.ParseDuration(getenv(EnvInterval)); err == nil && d > 0 {
		c.Interval = d
	}
	if d, err := time.ParseDuration(getenv(EnvTTL)); err == nil && d > 0 {
		c.CandidateTTL = d
	}
	if n, err := strconv.Atoi(getenv(EnvCeiling)); err == nil && n > 0 {
		c.Ceiling = n
	}
	if c.Window > c.Interval {
		c.Window = c.Interval
	}
	return c
}

// Refusal is one advertisement that did not survive the check, in the words the owner
// reads. Advertised and Observed carry both fingerprints, because a mismatch on a home
// or plant network is either a misconfiguration or an attack, and the owner cannot tell
// which from a bare "refused".
type Refusal struct {
	Addr       string
	NodeID     string
	Reason     string
	Advertised string
	Observed   string
}

// Report is what one pass of discovery has to say.
type Report struct {
	// Notes are the plain-language lines: "no peers found", "discovery unavailable".
	Notes []string
	// Warnings are the lines an owner must actually see - one per refusal.
	Warnings []string
	Refusals []Refusal
	// Seen / Dropped / Dials bound the cost of a pass: how many records arrived, how
	// many were refused storage at the ceiling, and how many dials that turned into.
	Seen    int
	Dropped int
	Dials   int
}

// Has reports whether the report contains a refusal with this reason.
func (r Report) Has(reason string) bool {
	for _, f := range r.Refusals {
		if f.Reason == reason {
			return true
		}
	}
	return false
}

// Find returns the first refusal with this reason.
func (r Report) Find(reason string) (Refusal, bool) {
	for _, f := range r.Refusals {
		if f.Reason == reason {
			return f, true
		}
	}
	return Refusal{}, false
}

// Options builds a Discovery.
type Options struct {
	Fleet     *Fleet
	Authority *cert.Authority
	// Self is this node's own advertisement. Its NodeID is skipped when browsing.
	Self   Advert
	Config Config
	// Interfaces reports the machine's network interfaces (nil means net.Interfaces).
	Interfaces func() []net.Interface
	// Plane builds a packet plane. It is called TWICE - once for the responder and
	// once for the browser - so that an answer we send never eats a query we are
	// waiting for. Two sockets on one multicast group is ordinary; one socket with two
	// readers is a race. Nil means the real multicast plane.
	Plane func() (Transport, error)
	// AdvertiseIPs overrides the addresses the A records answer with. Nil derives them
	// from Interfaces, which is what a node does when nobody has told it otherwise.
	AdvertiseIPs []net.IP
	// Dial is how a peer is checked (nil means DialTLS).
	Dial DialFunc
	Now  func() time.Time
	// Log receives the lines a human reads. Nil discards them.
	Log func(string)
}

// Discovery is the LAN half of Roger Edge: it advertises this node, browses for the
// owner's others, and turns the ones that survive a certificate check into fleet
// members. It NEVER sits on the request path - it runs on its own goroutine, holds no
// lock a relay takes, and a browse that takes five seconds costs a concurrent relay
// nothing.
type Discovery struct {
	opts Options
	cfg  Config
	tp   Transport // the browse plane
	ap   Transport // the advertise plane
	resp *Responder
	now  func() time.Time

	mu          sync.Mutex
	report      Report
	candidates  map[string]store.EdgeNode
	saidUnavail bool
	advertising bool
	browsing    bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds discovery. It does not touch the network until Start.
func New(o Options) *Discovery {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Dial == nil {
		o.Dial = DialTLS
	}
	if o.Interfaces == nil {
		o.Interfaces = func() []net.Interface {
			ifaces, err := net.Interfaces()
			if err != nil {
				return nil
			}
			return ifaces
		}
	}
	if o.Plane == nil {
		o.Plane = func() (Transport, error) { return NewMulticastTransport(o.Interfaces()) }
	}
	if o.AdvertiseIPs == nil {
		o.AdvertiseIPs = AdvertiseAddrs(InterfaceAddrs(o.Interfaces()))
	}
	if o.Config.Service == "" {
		o.Config = ConfigFromEnv(nil)
	}
	return &Discovery{opts: o, cfg: o.Config, now: o.Now,
		candidates: map[string]store.EdgeNode{}}
}

// Advertising / Browsing report whether Start actually turned each half on.
func (d *Discovery) Advertising() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.advertising }
func (d *Discovery) Browsing() bool    { d.mu.Lock(); defer d.mu.Unlock(); return d.browsing }

// Responder is this node's advertising half (nil until Start, or when disabled).
func (d *Discovery) Responder() *Responder { return d.resp }

// Report returns the latest pass's report.
func (d *Discovery) Report() Report {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.report
}

// Candidates are the unenrolled peers seen on the LAN. They are NOT members: they have
// no capabilities, they receive no traffic, and they are kept here rather than in the
// fleet store precisely so that nothing can mistake one for a node the owner owns.
func (d *Discovery) Candidates() []store.EdgeNode {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]store.EdgeNode, 0, len(d.candidates))
	for _, c := range d.candidates {
		out = append(out, c)
	}
	sortEdgeNodes(out)
	return out
}

// Adopt is the owner's explicit action that turns a candidate into a member. Nothing
// else does: seeing a machine on your network is not deciding it is yours.
func (d *Discovery) Adopt(id, name string) (store.EdgeNode, error) {
	d.mu.Lock()
	c, ok := d.candidates[id]
	d.mu.Unlock()
	if !ok {
		return store.EdgeNode{}, fmt.Errorf("no candidate %q was seen on this network", id)
	}
	c.Name = name
	c.Presence = string(PresenceVerified)
	n, err := d.opts.Fleet.Enroll(c)
	if err != nil {
		return store.EdgeNode{}, err
	}
	d.mu.Lock()
	delete(d.candidates, id)
	d.mu.Unlock()
	return n, nil
}

// Start brings discovery up and returns IMMEDIATELY. Nothing it does is allowed to
// delay a startup: a machine with no network gets one line and a running program.
func (d *Discovery) Start(ctx context.Context) error {
	if !d.cfg.Enabled {
		return nil
	}
	ap, err := d.opts.Plane()
	if err != nil {
		d.unavailable(err.Error())
		return nil
	}
	bp, err := d.opts.Plane()
	if err != nil {
		_ = ap.Close()
		d.unavailable(err.Error())
		return nil
	}
	d.ap, d.tp = ap, bp
	d.resp = NewResponder(d.ap, d.cfg.Service, d.opts.Self, d.opts.AdvertiseIPs)
	ctx, d.cancel = context.WithCancel(ctx)
	d.mu.Lock()
	d.advertising, d.browsing = true, true
	d.mu.Unlock()
	d.wg.Add(1)
	go func() { defer d.wg.Done(); _ = d.resp.Serve(ctx) }()
	if !d.cfg.ManualPasses {
		d.wg.Add(1)
		go func() { defer d.wg.Done(); d.loop(ctx) }()
	}
	return nil
}

// Stop shuts discovery down and waits for its goroutines.
func (d *Discovery) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	for _, tp := range []Transport{d.ap, d.tp} {
		if tp != nil {
			_ = tp.Close()
		}
	}
	d.ap, d.tp = nil, nil
	d.mu.Lock()
	d.advertising, d.browsing = false, false
	d.mu.Unlock()
}

// loop browses every interval. A pass that fails is a pass: it is retried on the NEXT
// interval, never in a tight loop, because a peer that advertises and refuses the
// connection must not be able to spend our CPU by doing so.
func (d *Discovery) loop(ctx context.Context) {
	t := time.NewTicker(d.cfg.Interval)
	defer t.Stop()
	for {
		d.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce is one full pass: browse, check what came back, and age out what stopped
// answering. It is exported so the loop is testable without waiting on a ticker.
func (d *Discovery) RunOnce(ctx context.Context) Report {
	var rep Report
	if !d.cfg.Enabled || d.tp == nil {
		d.unavailable("discovery is off")
		return d.Report()
	}
	res, err := Browse(ctx, d.tp, d.cfg.Service, d.cfg.Window, d.cfg.Ceiling)
	if err != nil {
		d.unavailable(err.Error())
	}
	rep.Seen, rep.Dropped = res.Seen, res.Dropped
	for _, ad := range res.Adverts {
		if ad.NodeID == d.opts.Self.NodeID {
			continue // ourselves, heard back off the group
		}
		d.consider(ctx, ad, &rep)
	}
	if len(res.Adverts) == 0 {
		rep.Notes = append(rep.Notes, "no peers found")
	}
	if _, err := d.opts.Fleet.Sweep(d.cfg.CandidateTTL); err != nil {
		rep.Notes = append(rep.Notes, "liveness sweep failed: "+err.Error())
	}
	d.pruneCandidates()
	d.mu.Lock()
	if d.saidUnavail {
		rep.Notes = append(rep.Notes, noteUnavailable)
	}
	d.report = rep
	d.mu.Unlock()
	return rep
}

// noteUnavailable is the one line a machine with no working multicast ever gets.
const noteUnavailable = "discovery is unavailable"

// consider turns one advertisement into a member, a candidate, a refusal, or nothing.
func (d *Discovery) consider(ctx context.Context, ad Advert, rep *Report) {
	f := d.opts.Fleet

	// A peer enrolled to ANOTHER account is not ours, however truthful its record. It
	// is not a member, it is not offered as a candidate, and nothing about it is
	// written anywhere: a machine that belongs to somebody else is not fleet state.
	if ad.Account != "" && ad.Account != f.Account() {
		return
	}

	// An unenrolled peer is a CANDIDATE. It is not dialed and it is not stored in the
	// fleet: until the owner adopts it, it has no capabilities and receives no traffic.
	if ad.Account == "" {
		d.remember(ad)
		return
	}

	existing, known, err := f.Get(ad.NodeID)
	if err != nil {
		rep.Notes = append(rep.Notes, "fleet read failed: "+err.Error())
		return
	}
	pin := ""
	if known {
		pin = existing.Pin
	}

	rep.Dials++
	peer, derr := d.opts.Dial(ctx, ad.Addr())
	if derr != nil {
		d.refuse(rep, ad, existing, known, ReasonUnreachable, "")
		return
	}
	if reason := VerifyPeer(ad, peer, d.opts.Authority, pin, d.now()); reason != "" {
		d.refuse(rep, ad, existing, known, reason, peer.Fingerprint)
		return
	}

	caps := make([]Capability, 0, len(peer.Describe.Caps))
	for _, c := range peer.Describe.Caps {
		if cap := Capability(c); cap.Valid() {
			caps = append(caps, cap)
		}
	}
	name := ad.NodeID
	if peer.Describe.Kind == "" {
		peer.Describe.Kind = ad.Kind
	}
	if _, err := f.Observe(ad.NodeID, Observation{
		Name: name, Kind: peer.Describe.Kind, Caps: caps,
		Addr: ad.Addr(), Fingerprint: peer.Fingerprint,
	}); err != nil {
		rep.Notes = append(rep.Notes, "fleet write failed: "+err.Error())
	}
}

// refuse records a refusal and says it out loud, once, with everything the owner needs
// to tell a misconfiguration from an attack.
//
// The one reason that is not simply passed through is IDENTITY COLLISION: a peer that
// fails verification while claiming the id of a node we currently hold VERIFIED at a
// DIFFERENT address is not a broken node, it is something standing in the road wearing
// that node's name, and the report has to say so.
func (d *Discovery) refuse(rep *Report, ad Advert, existing store.EdgeNode, known bool, reason, observed string) {
	if known && existing.Presence == string(PresenceVerified) &&
		LANAddr(existing) != "" && LANAddr(existing) != ad.Addr() {
		reason = ReasonIdentityCollision
	}
	rep.Refusals = append(rep.Refusals, Refusal{
		Addr: ad.Addr(), NodeID: ad.NodeID, Reason: reason,
		Advertised: ad.Fingerprint, Observed: observed,
	})
	line := fmt.Sprintf("edge discovery: refused %s at %s: %s (advertised %s, observed %s)",
		ad.NodeID, ad.Addr(), reason, orNone(ad.Fingerprint), orNone(observed))
	if reason == ReasonIdentityMismatch && known {
		line += "; if this really is the same node, clear its pin deliberately with `roger edge forget " + existing.Name + "`"
	}
	rep.Warnings = append(rep.Warnings, line)
	d.log(line)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// remember keeps an unenrolled peer as a candidate, under the ceiling.
func (d *Discovery) remember(ad Advert) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.candidates[ad.NodeID]; !ok && d.cfg.Ceiling > 0 && len(d.candidates) >= d.cfg.Ceiling {
		return
	}
	kind := ad.Kind
	if kind == "" {
		kind = string(Host)
	}
	d.candidates[ad.NodeID] = store.EdgeNode{
		ID: ad.NodeID, Name: ad.NodeID, Kind: kind,
		Presence: string(PresenceCandidate), LastSeen: d.now().Unix(),
		Pin:        ad.Fingerprint,
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: ad.Addr(), Fingerprint: ad.Fingerprint}},
	}
}

// pruneCandidates forgets candidates nobody adopted within the TTL.
func (d *Discovery) pruneCandidates() {
	cutoff := d.now().Add(-d.cfg.CandidateTTL).Unix()
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, c := range d.candidates {
		if c.LastSeen < cutoff {
			delete(d.candidates, id)
		}
	}
}

// unavailable says, exactly ONCE, that discovery is not working. Once and not per
// interval: a machine with no multicast would otherwise write a line every thirty
// seconds forever, which trains everybody to ignore the log that matters.
func (d *Discovery) unavailable(why string) {
	d.mu.Lock()
	first := !d.saidUnavail
	d.saidUnavail = true
	if first {
		d.report.Notes = append(d.report.Notes, noteUnavailable)
	}
	d.mu.Unlock()
	if first {
		d.log(noteUnavailable + ": " + why + " (serving and relaying are unaffected)")
	}
}

func (d *Discovery) log(line string) {
	if d.opts.Log != nil {
		d.opts.Log(line)
	}
}
