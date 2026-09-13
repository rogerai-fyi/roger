package edge

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// Fleet is one account's Edge: the set of machines that owner owns.
//
// Everything on it is account-scoped by construction - the account is a field on the
// Fleet, not an argument a caller can forget - so there is no path where a surface of
// one account lists, describes or addresses another account's node.
type Fleet struct {
	db      store.Store
	account string
	// now is the clock, injectable so a test can assert on recorded times without
	// racing the wall clock.
	now func() time.Time
}

// NewFleet returns the fleet view for one account.
func NewFleet(db store.Store, account string) *Fleet {
	return &Fleet{db: db, account: account, now: time.Now}
}

// SetClock replaces the fleet's clock. Test seam.
func (f *Fleet) SetClock(now func() time.Time) { f.now = now }

// Account is the owner this fleet belongs to.
func (f *Fleet) Account() string { return f.account }

// EnrollError refuses a node that already belongs to another Edge.
//
// Existing names the account it already belongs to, for the surfaces entitled to know:
// the node itself, and its actual owner. Error() does NOT, because the reader of the
// message is the account whose enrollment just failed, and a refusal that names the
// other account turns "is this device already claimed?" into an account-enumeration
// oracle on any LAN.
type EnrollError struct{ Existing string }

func (e *EnrollError) Error() string { return "that node already belongs to another Edge" }

// ErrNoSuchNode is returned when this account holds no node with that id.
var ErrNoSuchNode = errors.New("no such node on this Edge")

// ErrNotVerified is returned when work is addressed to a capability the node has not
// had verified (or, for actuate, has not had confirmed).
var ErrNotVerified = errors.New("that node has no verified capability for this")

// Enroll adds a node to this Edge. The record's account is stamped here rather than
// taken from the caller, so a node can never be enrolled into an account by asking.
func (f *Fleet) Enroll(n store.EdgeNode) (store.EdgeNode, error) {
	if err := ValidName(n.Name); err != nil {
		return store.EdgeNode{}, err
	}
	if !Kind(n.Kind).Valid() {
		return store.EdgeNode{}, fmt.Errorf("%q is not a node kind", n.Kind)
	}
	for _, c := range n.Caps {
		if err := f.checkCap(Kind(n.Kind), Capability(c.Name)); err != nil {
			return store.EdgeNode{}, err
		}
	}
	n.Account = f.account
	n.Station = false
	sortTransports(n.Transports)
	if n.Pin == "" {
		// A LAN transport's fingerprint IS the pin: it is the certificate the owner
		// accepted for this node, and the thing a later dial is checked against.
		for _, t := range n.Transports {
			if t.Kind == "lan" && t.Fingerprint != "" {
				n.Pin = t.Fingerprint
				break
			}
		}
	}
	now := f.now().Unix()
	if n.LastSeen == 0 {
		n.LastSeen = now
	}
	if n.Presence == "" {
		n.Presence = string(PresenceVerified)
	}
	n.History = append(n.History, store.EdgeEvent{At: now, What: "enrolled", Detail: n.Name})
	got, err := f.db.EnrollEdgeNode(n)
	if err != nil {
		var elsewhere *store.EdgeEnrolledElsewhere
		if errors.As(err, &elsewhere) {
			return store.EdgeNode{}, &EnrollError{Existing: elsewhere.Existing}
		}
		return store.EdgeNode{}, err
	}
	return got, nil
}

// checkCap refuses a capability that is not in the vocabulary, or that this kind of
// thing cannot have.
func (f *Fleet) checkCap(k Kind, c Capability) error {
	if !c.Valid() {
		return fmt.Errorf("%q is not a capability", c)
	}
	if !k.MayDeclare(c) {
		return fmt.Errorf("a %s may not declare %q", k, c)
	}
	return nil
}

// List is the fleet view: this account's enrolled nodes, plus its Stations.
//
// A Station is DERIVED, never shadowed. `roger share` registers into rogerai.nodes and
// binds the node to an account, and that path is untouched by Roger Edge - so rather
// than copy a Station into the Edge table (where it would drift, and where a bug could
// disturb a live registration), the fleet joins the two at read time. A Station that is
// ALSO enrolled keeps its Edge record and simply gains the verified `serve` mark.
func (f *Fleet) List() ([]store.EdgeNode, error) {
	enrolled, err := f.db.EdgeNodesOfAccount(f.account)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(enrolled))
	for i, n := range enrolled {
		byID[n.ID] = i
	}
	stations, err := f.db.NodesOfAccount(f.account)
	if err != nil {
		return nil, err
	}
	if len(stations) == 0 {
		return enrolled, nil
	}
	regs, err := f.db.AllNodes()
	if err != nil {
		return nil, err
	}
	reg := make(map[string]store.NodeRecord, len(regs))
	for _, r := range regs {
		reg[r.NodeID] = r
	}
	for _, id := range stations {
		r, registered := reg[id]
		if !registered {
			continue // bound but not registered: supply state, not a machine we can show
		}
		if i, ok := byID[id]; ok {
			enrolled[i] = withServe(enrolled[i])
			continue
		}
		enrolled = append(enrolled, withServe(store.EdgeNode{
			ID: id, Account: f.account, Name: id, Kind: string(Host),
			Presence: string(PresenceVerified), LastSeen: r.LastSeen,
			Transports: []store.EdgeTransport{{Kind: "relay"}},
			Station:    true,
		}))
	}
	sortEdgeNodes(enrolled)
	return enrolled, nil
}

// withServe marks a record as serving, VERIFIED: a registered Station has already
// passed the existing serving probe, which is exactly what `serve` means.
func withServe(n store.EdgeNode) store.EdgeNode {
	for i, c := range n.Caps {
		if c.Name == string(Serve) {
			n.Caps[i].State = string(Verified)
			return n
		}
	}
	n.Caps = append(n.Caps, store.EdgeCap{Name: string(Serve), State: string(Verified)})
	return n
}

// sortEdgeNodes orders a fleet listing by name then id, so the view is stable.
func sortEdgeNodes(ns []store.EdgeNode) {
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0 && less(ns[j], ns[j-1]); j-- {
			ns[j], ns[j-1] = ns[j-1], ns[j]
		}
	}
}

func less(a, b store.EdgeNode) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}

// Get returns one of this account's nodes (a derived Station included).
func (f *Fleet) Get(id string) (store.EdgeNode, bool, error) {
	list, err := f.List()
	if err != nil {
		return store.EdgeNode{}, false, err
	}
	for _, n := range list {
		if n.ID == id {
			return n, true, nil
		}
	}
	return store.EdgeNode{}, false, nil
}

// ByName resolves the owner's name for a node within this account.
func (f *Fleet) ByName(name string) (store.EdgeNode, bool, error) {
	list, err := f.List()
	if err != nil {
		return store.EdgeNode{}, false, err
	}
	for _, n := range list {
		if n.Name == name {
			return n, true, nil
		}
	}
	return store.EdgeNode{}, false, nil
}

// mutate loads this account's stored record, applies fn, and writes it back. It is the
// single write path, so account scoping and the history line are in one place instead
// of repeated at every caller.
func (f *Fleet) mutate(id string, fn func(*store.EdgeNode) error) error {
	n, ok, err := f.db.EdgeNodeByID(f.account, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoSuchNode
	}
	if err := fn(&n); err != nil {
		return err
	}
	sortTransports(n.Transports)
	if err := f.db.UpdateEdgeNode(n); err != nil {
		return err
	}
	return nil
}

// Rename changes the owner's name for a node. The ID is the key, so the history, the
// pin and any grant addressed to the node all survive untouched.
func (f *Fleet) Rename(id, name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	return f.mutate(id, func(n *store.EdgeNode) error {
		old := n.Name
		n.Name = name
		n.History = append(n.History, store.EdgeEvent{
			At: f.now().Unix(), What: "renamed", Detail: old + " -> " + name})
		return nil
	})
}

// Forget removes the node and its pin from this Edge, and nothing else. Receipts and
// ledger rows are money, not fleet state, and are never touched; clearing the pin is
// what makes the node adoptable again later.
func (f *Fleet) Forget(id string) error {
	ok, err := f.db.ForgetEdgeNode(f.account, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoSuchNode
	}
	return nil
}

// RepinNode records the certificate a node now presents, for a node whose IDENTITY has
// not changed - a renewal. The id, the name, the history and the node's place in the
// fleet are untouched, because none of them is a property of the paper.
//
// It is deliberately not a general "set the pin": the only caller is the renewal path,
// which has already established that the KEY is the same one.
func (f *Fleet) RepinNode(id, fingerprint string) error {
	return f.mutate(id, func(n *store.EdgeNode) error {
		if n.Pin == fingerprint {
			return nil
		}
		n.Pin = fingerprint
		n.History = append(n.History, store.EdgeEvent{
			At: f.now().Unix(), What: "renewed", Detail: fingerprint})
		return nil
	})
}

// RequireReenrollment marks every member of this Edge as needing to re-enroll, with the
// reason, and returns how many were marked.
//
// Nothing is deleted. Changing an Edge's authority invalidates every certificate at
// once, and the honest way to show that is a fleet full of nodes saying WHY they stopped
// verifying - not an empty screen the owner has to work out for themselves.
func (f *Fleet) RequireReenrollment(reason string) (int, error) {
	list, err := f.db.EdgeNodesOfAccount(f.account)
	if err != nil {
		return 0, err
	}
	marked := 0
	for _, n := range list {
		if err := f.mutate(n.ID, func(m *store.EdgeNode) error {
			m.Presence = string(PresenceReenroll)
			m.History = append(m.History, store.EdgeEvent{
				At: f.now().Unix(), What: "re-enrollment required", Detail: reason})
			return nil
		}); err != nil {
			return marked, err
		}
		marked++
	}
	return marked, nil
}

// Declare replaces the node's declared capability set. Identity does not move: the id,
// the name and the history are the node's, and what it can do is merely current state.
func (f *Fleet) Declare(id string, caps []Capability) error {
	return f.mutate(id, func(n *store.EdgeNode) error {
		for _, c := range caps {
			if err := f.checkCap(Kind(n.Kind), c); err != nil {
				return err
			}
		}
		prev := map[string]store.EdgeCap{}
		for _, c := range n.Caps {
			prev[c.Name] = c
		}
		var next []store.EdgeCap
		var added []string
		for _, c := range caps {
			if old, ok := prev[string(c)]; ok {
				next = append(next, old)
				delete(prev, string(c))
				continue
			}
			next = append(next, store.EdgeCap{Name: string(c), State: string(initialState(c))})
			added = append(added, "+"+string(c))
		}
		var dropped []string
		for name := range prev {
			dropped = append(dropped, "-"+name)
		}
		n.Caps = next
		detail := strings.Join(append(added, dropped...), " ")
		n.History = append(n.History, store.EdgeEvent{
			At: f.now().Unix(), What: "capabilities", Detail: detail})
		return nil
	})
}

// RecordProbe records the OUTCOME of verifying one declared capability: a pass promotes
// it to VERIFIED, a failure leaves it CLAIMED so nothing routes on the strength of the
// claim alone.
//
// It refuses `actuate` outright. Actuate is never probed - there is no read-only way to
// ask a machine whether it can move something - so the only path to a verified actuate
// is ConfirmActuate, and closing this door is what stops behavior from being promoted
// into permission.
func (f *Fleet) RecordProbe(id string, c Capability, passed bool) error {
	if c == Actuate {
		return fmt.Errorf("actuate is never probed; it is confirmed by the owner")
	}
	return f.mutate(id, func(n *store.EdgeNode) error {
		for i, d := range n.Caps {
			if d.Name != string(c) {
				continue
			}
			state := Claimed
			if passed {
				state = Verified
			}
			n.Caps[i].State = string(state)
			n.History = append(n.History, store.EdgeEvent{
				At: f.now().Unix(), What: "probe", Detail: string(c) + " -> " + string(state)})
			return nil
		}
		return fmt.Errorf("this node does not declare %q", c)
	})
}

// ConfirmActuate is the owner saying yes to a node that can change the physical world.
// It records WHO confirmed and WHEN, because that is the only audit trail a physical
// action has before it happens.
func (f *Fleet) ConfirmActuate(id, who string) error {
	if who == "" {
		return fmt.Errorf("a confirmation records who gave it")
	}
	return f.mutate(id, func(n *store.EdgeNode) error {
		for i, d := range n.Caps {
			if d.Name != string(Actuate) {
				continue
			}
			at := f.now().Unix()
			n.Caps[i].State = string(Verified)
			n.Caps[i].ConfirmedBy = who
			n.Caps[i].ConfirmedAt = at
			n.History = append(n.History, store.EdgeEvent{
				At: at, What: "actuate-confirmed", Detail: who})
			return nil
		}
		return fmt.Errorf("this node does not declare actuate")
	})
}

// RecordEvent appends one line to a node's history. Behavior is recorded honestly and
// separately from capability: a node that accepted world-changing commands has that in
// its history, and STILL does not gain `actuate` from it.
func (f *Fleet) RecordEvent(id, what, detail string) error {
	return f.mutate(id, func(n *store.EdgeNode) error {
		n.History = append(n.History, store.EdgeEvent{At: f.now().Unix(), What: what, Detail: detail})
		return nil
	})
}

// VerificationMethod answers "how would the fleet verify this?" for one of a node's
// declared capabilities - the line a fleet view shows beside a CLAIMED mark.
func (f *Fleet) VerificationMethod(id string, c Capability) (string, error) {
	n, ok, err := f.Get(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNoSuchNode
	}
	if !Declares(n, c) {
		return "", fmt.Errorf("this node does not declare %q", c)
	}
	return c.VerificationMethod(), nil
}

// AuthorizeInvoke is the gate in front of every action addressed to a node. It answers
// one question - may THIS grant do THIS on THIS node, right now - and it is the reason
// forgetting a node is enough to revoke what was addressed to it: a node that is not on
// the Edge is not a node an invoke can name.
//
// need is the capability the action requires; "" means the caller needs membership and
// a valid grant but no particular capability.
func (f *Fleet) AuthorizeInvoke(g store.Grant, nodeID string, need Capability) error {
	n, ok, err := f.Get(nodeID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoSuchNode
	}
	if g.Owner != f.account {
		return fmt.Errorf("that grant was not issued by this Edge's owner")
	}
	if g.Revoked {
		return fmt.Errorf("that grant is revoked")
	}
	if g.Expired(f.now()) {
		return fmt.Errorf("that grant has expired")
	}
	if len(g.Nodes) > 0 {
		scoped := false
		for _, id := range g.Nodes {
			if id == nodeID {
				scoped = true
				break
			}
		}
		if !scoped {
			return fmt.Errorf("that grant does not address this node")
		}
	}
	if need == "" {
		return nil
	}
	if !Routable(n, need) {
		return fmt.Errorf("%w: %s", ErrNotVerified, need)
	}
	return nil
}

// Observation is what a VERIFIED dial learned about a node: its own account of itself,
// over its own certificate, plus where it was reached and the certificate it served.
type Observation struct {
	Name        string // used only when the node is new to this Edge
	Kind        string
	Caps        []Capability
	Addr        string
	Fingerprint string
}

// Observe records a verified sighting. It is the ONLY way discovery writes to the
// fleet, and it happens strictly after VerifyPeer has passed - an advertisement on its
// own never reaches this function.
//
// A node already on the Edge is UPDATED, never re-added: the id is the key, so its
// name, its history and its place in the fleet survive going dark and coming back, and
// no duplicate row is created. Its other transports (a relay path) are kept, with the
// LAN entry refreshed and sorted back to the front.
func (f *Fleet) Observe(id string, obs Observation) (store.EdgeNode, error) {
	now := f.now().Unix()
	cur, ok, err := f.db.EdgeNodeByID(f.account, id)
	if err != nil {
		return store.EdgeNode{}, err
	}
	if !ok {
		n := store.EdgeNode{ID: id, Name: obs.Name, Kind: obs.Kind, Pin: obs.Fingerprint}
		if n.Kind == "" {
			n.Kind = string(Host)
		}
		for _, c := range obs.Caps {
			n.Caps = append(n.Caps, store.EdgeCap{Name: string(c), State: string(initialState(c))})
		}
		n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: obs.Addr, Fingerprint: obs.Fingerprint}}
		n.Presence = string(PresenceVerified)
		n.LastSeen = now
		return f.Enroll(n)
	}
	err = f.mutate(id, func(n *store.EdgeNode) error {
		// The capabilities of record are the ones the certificate-backed describe
		// reported, not the ones the advertisement claimed. A state already earned
		// (a passed probe, an owner's actuate confirmation) is carried across.
		prev := map[string]store.EdgeCap{}
		for _, c := range n.Caps {
			prev[c.Name] = c
		}
		var next []store.EdgeCap
		for _, c := range obs.Caps {
			if old, ok := prev[string(c)]; ok {
				next = append(next, old)
				continue
			}
			next = append(next, store.EdgeCap{Name: string(c), State: string(initialState(c))})
		}
		n.Caps = next
		n.Presence = string(PresenceVerified)
		n.LastSeen = now
		if n.Pin == "" {
			n.Pin = obs.Fingerprint
		}
		lan := false
		for i, t := range n.Transports {
			if t.Kind == "lan" {
				n.Transports[i].Addr = obs.Addr
				n.Transports[i].Fingerprint = obs.Fingerprint
				lan = true
				break
			}
		}
		if !lan {
			n.Transports = append(n.Transports,
				store.EdgeTransport{Kind: "lan", Addr: obs.Addr, Fingerprint: obs.Fingerprint})
		}
		if cur.Presence != string(PresenceVerified) {
			n.History = append(n.History, store.EdgeEvent{At: now, What: "seen", Detail: obs.Addr})
		}
		return nil
	})
	if err != nil {
		return store.EdgeNode{}, err
	}
	got, _, err := f.db.EdgeNodeByID(f.account, id)
	return got, err
}

// Sweep marks every node not heard from within window as DARK, and KEEPS it. A fleet
// that silently drops members hides the problem it exists to show: a dark box with a
// last-seen time is information, an absent box is a lie by omission.
//
// It returns how many nodes went dark on this pass.
func (f *Fleet) Sweep(window time.Duration) (int, error) {
	list, err := f.db.EdgeNodesOfAccount(f.account)
	if err != nil {
		return 0, err
	}
	cutoff := f.now().Add(-window).Unix()
	darkened := 0
	for _, n := range list {
		if n.Presence != string(PresenceVerified) || n.LastSeen == 0 || n.LastSeen > cutoff {
			continue
		}
		if err := f.mutate(n.ID, func(m *store.EdgeNode) error {
			m.Presence = string(PresenceDark)
			m.History = append(m.History, store.EdgeEvent{
				At: f.now().Unix(), What: "dark", Detail: "last seen " + time.Unix(m.LastSeen, 0).UTC().Format(time.RFC3339)})
			return nil
		}); err != nil {
			return darkened, err
		}
		darkened++
	}
	return darkened, nil
}

// LANAddr is the address a node was last reached at on the LAN, or "" if it has no LAN
// transport.
func LANAddr(n store.EdgeNode) string {
	for _, t := range n.Transports {
		if t.Kind == "lan" {
			return t.Addr
		}
	}
	return ""
}
