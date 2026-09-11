package store

import (
	"errors"
	"sort"
	"sync"
)

// The Roger Edge node record: the account-scoped, capability-typed thing an OWNER owns
// (features/edge/node_identity.feature). It sits beside wallets, grants and the Station
// registry rather than inside them, because a node does not have to host a model to
// belong: a sensor that serves nothing and a relay board that serves nothing are members.
//
// It deliberately does NOT touch rogerai.nodes. A Station is a node with the `serve`
// capability, and the Station registration path stays byte-identical; the fleet view
// DERIVES a Station's row (see edge.Fleet.List) instead of shadowing it here.
//
// The vocabulary (which capabilities exist, which kinds may declare which) lives in
// internal/edge; this layer stores strings so the store never depends on it.

// EdgeCap is one declared capability and how far its verification has got.
// State is "CLAIMED", "VERIFIED" or "PENDING CONFIRMATION".
type EdgeCap struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	ConfirmedBy string `json:"confirmed_by,omitempty"` // actuate only: who said yes
	ConfirmedAt int64  `json:"confirmed_at,omitempty"` // unix seconds
}

// EdgeTransport is one way a node can be reached. Kind is "lan" or "relay"; a LAN entry
// carries the pinned certificate fingerprint, because a LAN dial is only safe pinned.
type EdgeTransport struct {
	Kind        string `json:"kind"`
	Addr        string `json:"addr,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// EdgeEvent is one line of a node's history. Renaming keeps it; forgetting drops it with
// the record (money is elsewhere and is never touched).
type EdgeEvent struct {
	At     int64  `json:"at"`
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
}

// EdgeNode is the record itself.
type EdgeNode struct {
	ID         string          `json:"id"`      // derived from the node's own public key
	Account    string          `json:"account"` // the owner; every node on one Edge shares it
	Name       string          `json:"name"`    // owner-chosen, unique within the account
	Kind       string          `json:"kind"`    // host | board | mobile
	Caps       []EdgeCap       `json:"caps,omitempty"`
	Transports []EdgeTransport `json:"transports,omitempty"`
	Presence   string          `json:"presence,omitempty"` // VERIFIED | DARK | CANDIDATE
	LastSeen   int64           `json:"last_seen,omitempty"`
	Pin        string          `json:"pin,omitempty"` // pinned certificate SHA-256 (hex)
	History    []EdgeEvent     `json:"history,omitempty"`
	// Station marks a row DERIVED from the existing Station registry rather than stored
	// here. It is never persisted: it is recomputed on every fleet listing.
	Station bool `json:"station,omitempty"`
}

// ErrEdgeNameTaken is returned when a name is already used by another node in the SAME
// account. Names are per-account, so the same name in another account is not a conflict.
var ErrEdgeNameTaken = errors.New("that name is already used by another node on this Edge")

// ErrEdgeNoSuchNode is returned when the account holds no node with that id.
var ErrEdgeNoSuchNode = errors.New("no such node on this Edge")

// EdgeEnrolledElsewhere reports that a node id is already enrolled to ANOTHER account: a
// node belongs to exactly one Edge at a time.
//
// Existing carries the owning account for the surfaces entitled to it (the node itself,
// and its real owner). Error() deliberately does NOT, because the party that sees this
// message is the account that failed to enroll, and it must learn nothing about the one
// that succeeded.
type EdgeEnrolledElsewhere struct{ Existing string }

func (e *EdgeEnrolledElsewhere) Error() string {
	return "that node already belongs to another Edge"
}

// --- Mem ------------------------------------------------------------------
//
// A small map set on Mem with its own mutex, mirroring grantStore: fleet ops never
// contend with the wallet/ledger lock.

type edgeStore struct {
	mu    sync.Mutex
	nodes map[string]EdgeNode // node id -> record (ONE Edge per node, so the id is global)
}

func newEdgeStore() *edgeStore { return &edgeStore{nodes: map[string]EdgeNode{}} }

// enrollLocked is the shared insert guard: one Edge per node, one name per account.
func (e *edgeStore) enrollLocked(n EdgeNode) error {
	if cur, ok := e.nodes[n.ID]; ok {
		if cur.Account != n.Account {
			return &EdgeEnrolledElsewhere{Existing: cur.Account}
		}
	}
	for id, other := range e.nodes {
		if id != n.ID && other.Account == n.Account && other.Name == n.Name {
			return ErrEdgeNameTaken
		}
	}
	return nil
}

func (m *Mem) EnrollEdgeNode(n EdgeNode) (EdgeNode, error) {
	m.es.mu.Lock()
	defer m.es.mu.Unlock()
	if err := m.es.enrollLocked(n); err != nil {
		return EdgeNode{}, err
	}
	n.Station = false
	m.es.nodes[n.ID] = n
	return n, nil
}

func (m *Mem) UpdateEdgeNode(n EdgeNode) error {
	m.es.mu.Lock()
	defer m.es.mu.Unlock()
	cur, ok := m.es.nodes[n.ID]
	if !ok || cur.Account != n.Account {
		return ErrEdgeNoSuchNode
	}
	if err := m.es.enrollLocked(n); err != nil {
		return err
	}
	n.Station = false
	m.es.nodes[n.ID] = n
	return nil
}

func (m *Mem) EdgeNodeByID(account, id string) (EdgeNode, bool, error) {
	m.es.mu.Lock()
	defer m.es.mu.Unlock()
	n, ok := m.es.nodes[id]
	if !ok || n.Account != account {
		return EdgeNode{}, false, nil
	}
	return n, true, nil
}

func (m *Mem) EdgeNodesOfAccount(account string) ([]EdgeNode, error) {
	m.es.mu.Lock()
	defer m.es.mu.Unlock()
	var out []EdgeNode
	for _, n := range m.es.nodes {
		if n.Account == account {
			out = append(out, n)
		}
	}
	sortEdgeNodes(out)
	return out, nil
}

func (m *Mem) ForgetEdgeNode(account, id string) (bool, error) {
	m.es.mu.Lock()
	defer m.es.mu.Unlock()
	n, ok := m.es.nodes[id]
	if !ok || n.Account != account {
		return false, nil
	}
	delete(m.es.nodes, id)
	return true, nil
}

// sortEdgeNodes gives both backends the same order (by name, then id), so a parity test
// compares content rather than map iteration luck.
func sortEdgeNodes(ns []EdgeNode) {
	sort.Slice(ns, func(i, j int) bool {
		if ns[i].Name != ns[j].Name {
			return ns[i].Name < ns[j].Name
		}
		return ns[i].ID < ns[j].ID
	})
}
