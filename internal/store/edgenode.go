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
	// Instances names the running instances that PROVIDE this capability on the node
	// (features/edge/instances.feature): a node's capabilities are the union of its
	// instances', and this is the provenance. Empty on a record older than instances.
	Instances []string `json:"instances,omitempty"`
}

// EdgeInstance is one running `roger` on a node: a process, named by the owner, with the
// capabilities and bands IT provides. It holds no certificate of its own - it is inside
// its node's trust boundary - and it exists only while the process runs.
type EdgeInstance struct {
	Name  string    `json:"name"`
	Caps  []EdgeCap `json:"caps,omitempty"`
	Bands []string  `json:"bands,omitempty"` // models this instance has on air
	// Voices are the bands in Bands that are VOICES (text-to-speech) here, so a member knows which
	// bands to ask for speech (features/lan/voice.feature). Never wider than Bands.
	Voices []string `json:"voices,omitempty"`
	// Agent is true when this instance runs as an AGENT on the Edge - it operates on other nodes,
	// and the surfaces draw it as ◆ (features/edge/agents.feature). Distinct from merely claiming
	// the operate capability, which any instance may do; Agent is the owner's declaration - a
	// command, a config role, or the ROGER_EDGE_AGENT launch env.
	Agent   bool  `json:"agent,omitempty"`
	Started int64 `json:"started,omitempty"`
	Port    int   `json:"port,omitempty"` // its own LAN face, when it has one
	// PID is the process that wrote this registration, on the machine that wrote it. It is written by
	// the process about ITSELF, so it is never a claim about anybody else, and it is what lets the
	// owner stop or restart a roger from another machine (features/edge/maintenance.feature). A
	// registration from an older roger carries none, and a stop refuses rather than guessing.
	PID       int            `json:"pid,omitempty"`
	Resources *EdgeResources `json:"resources,omitempty"`
	// ServeProven are the bands the serving probe actually answered for on this instance
	// (features/edge/serving_probe.feature). A VERIFIED serve means EVERY band in this list was
	// proven, so any subset of it is proven too and only a band outside it needs a fresh probe.
	// Empty on a record written before the probe existed: such a record falls back to Bands.
	ServeProven []string `json:"serve_proven,omitempty"`
	// Software is what this roger IS: its version and the machine it runs on
	// (features/edge/inventory.feature). Read, never invented; nil on an older face.
	Software *EdgeSoftware `json:"software,omitempty"`
	// Model is what this roger serves by itself and what it is fetching (features/edge/
	// remote_model.feature), as it said in its own last report. nil on a roger that serves nothing
	// of its own, and on an older face.
	Model *EdgeModel `json:"model,omitempty"`
}

// EdgeModel is a roger's own serving, in full, and the fetch in flight.
type EdgeModel struct {
	Band  string `json:"band,omitempty"` // empty: nothing served
	ID    string `json:"id,omitempty"`
	Quant string `json:"quant,omitempty"`
	Ctx   int    `json:"ctx,omitempty"`
	Since int64  `json:"since,omitempty"`
	// File and SHA256 are which exact file the band is, as the member says it serves it
	// (features/models/short_names_nearby.feature). Empty on an older member: no mapping from it.
	File        string     `json:"file,omitempty"`
	SHA256      string     `json:"sha256,omitempty"`
	Bytes       int64      `json:"bytes,omitempty"` // the file's size, as the member says
	Fetch       *EdgeFetch `json:"fetch,omitempty"`
	CannotServe string     `json:"cannot_serve,omitempty"` // why this machine has no runtime, in one sentence
	ServeFailed string     `json:"serve_failed,omitempty"` // the last serve's own last words, when it never came on the air
	Stopped     string     `json:"stopped,omitempty"`      // why roger last stopped serving on its own (an idle timeout), in one line
	Build       string     `json:"build,omitempty"`        // what its model runs on: cpu, vulkan, cuda, metal
	TokPerSec   float64    `json:"tok_s,omitempty"`        // the speed its runtime measured, once it has answered
	// Also is what it serves beside Band, in the order they went on the air (two at once,
	// local_models.feature §5). Band is the latest: the one a switch replaces.
	Also []EdgeServed `json:"also,omitempty"`
	// Have is the ids of the models on its disk (bounded), so an owner on another machine can pick
	// one to serve without walking to it. Ids only: never a path.
	Have []string `json:"have,omitempty"`
}

// EdgeServed is one more model a member serves beside its latest.
type EdgeServed struct {
	Band   string `json:"band"`
	ID     string `json:"id,omitempty"`
	Quant  string `json:"quant,omitempty"`
	Ctx    int    `json:"ctx,omitempty"`
	Since  int64  `json:"since,omitempty"`
	File   string `json:"file,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

// EdgeFetch is a fetch on a member, as it last reported it.
type EdgeFetch struct {
	Repo  string  `json:"repo"`
	File  string  `json:"file"`
	Done  int64   `json:"done"`
	Total int64   `json:"total"`
	Rate  float64 `json:"rate,omitempty"`
	State string  `json:"state"` // fetching | done | failed | interrupted
	Err   string  `json:"err,omitempty"`
}

// EdgeSoftware is the software inventory of one running roger. Every field is what the machine
// said, or empty when it could not be read.
type EdgeSoftware struct {
	Roger    string `json:"roger,omitempty"`    // the roger version, e.g. 6.11.2
	OS       string `json:"os,omitempty"`       // the OS pretty name, e.g. Debian GNU/Linux 12
	Kernel   string `json:"kernel,omitempty"`   // the kernel release
	Arch     string `json:"arch,omitempty"`     // amd64, arm64, ...
	Hostname string `json:"hostname,omitempty"` // the machine's own name
	Machine  string `json:"machine,omitempty"`  // the hardware model: a Jetson, a Pi, a DMI product
}

// EdgeResources are the local resource facts an instance could READ - never invented: a
// field that could not be read is absent, not zero. Advisory only; nothing routes on them.
type EdgeResources struct {
	GPU        string  `json:"gpu,omitempty"`
	GPUMemUsed float64 `json:"gpu_mem_used,omitempty"` // fraction 0..1
	RAMUsedGB  float64 `json:"ram_used_gb,omitempty"`
	RAMTotalGB float64 `json:"ram_total_gb,omitempty"`
	// The health facts (features/edge/health.feature). Pointers where zero is a real reading
	// (a temperature, a load), so "not exposed" is nil - absent, never 0.
	CPUTempC  *float64   `json:"cpu_temp_c,omitempty"`
	CPULoad1  *float64   `json:"cpu_load1,omitempty"` // 1-minute load average
	UptimeS   int64      `json:"uptime_s,omitempty"`
	Disks     []EdgeDisk `json:"disks,omitempty"` // every mounted real filesystem
	SDWearPct *float64   `json:"sd_wear_pct,omitempty"`
	// PiPower is a Raspberry Pi firmware's get_throttled word, verbatim: its low bits say what is
	// happening NOW (under-voltage, capped, throttled, soft limit), bits 16-19 what has happened
	// since boot. nil on anything that is not a Pi; 0 is a real, healthy reading.
	PiPower *uint32 `json:"pi_power,omitempty"`
	ReadAt  int64   `json:"read_at,omitempty"`
}

// EdgeDisk is one mounted filesystem's used and total, in GB.
type EdgeDisk struct {
	Mount   string  `json:"mount"`
	UsedGB  float64 `json:"used_gb"`
	TotalGB float64 `json:"total_gb"`
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
	// By is WHO asked for it, when someone other than the owner at this machine did: the caller's
	// authenticated node id. It is the audit record (a name can change or be reused); the Detail
	// sentence names the caller and never carries the id.
	By string `json:"by,omitempty"`
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
	// Tags are the OWNER's words for what this unit is (features/edge/tags.feature): a group a rule
	// can be scoped to. Never read from the node itself, and never a permission.
	Tags     []string    `json:"tags,omitempty"`
	LastSeen int64       `json:"last_seen,omitempty"`
	Pin      string      `json:"pin,omitempty"` // pinned certificate SHA-256 (hex)
	History  []EdgeEvent `json:"history,omitempty"`
	// Contract is what a CLASSIFYING node is framed with. Wave models are contract
	// models - the device prompt is PART of the device - so the framing lives on the
	// node record and travels with every escalation it raises. Empty for a node that
	// declares no classify contract. The record is kept here; the MEANING (what a valid
	// contract is, and what the production framings are) lives in internal/edge, the
	// same split as EdgeCap and edge.Capability.
	Contract EdgeContract `json:"contract,omitempty"`
	// Station marks a row DERIVED from the existing Station registry rather than stored
	// here. It is never persisted: it is recomputed on every fleet listing.
	Station bool `json:"station,omitempty"`
	// Instances are the running rogers on this node as its LAN face last reported them,
	// bounded; InstancesTruncated says the face reported more than the bound.
	Instances          []EdgeInstance `json:"instances,omitempty"`
	InstancesTruncated bool           `json:"instances_truncated,omitempty"`
	// Phone is what a phone last reported about itself in a signed presence (features/edge/
	// fleet_window.feature): battery, charging, thermal state, whether the app was in front. nil
	// for anything that is not a phone, and for a phone whose app does not report them yet.
	Phone *EdgePhone `json:"phone,omitempty"`
}

// EdgePhone is a phone's own vitals. Absent is nil or empty, never zero: iOS gives no processor
// load or temperature figure, and nothing here pretends it does.
type EdgePhone struct {
	BatteryPct *int   `json:"battery_pct,omitempty"`
	Charging   *bool  `json:"charging,omitempty"`
	Thermal    string `json:"thermal,omitempty"` // nominal | fair | serious | critical
	Foreground *bool  `json:"foreground,omitempty"`
	ReadAt     int64  `json:"read_at,omitempty"`
}

// EdgeContract is the fixed framing a classifying device carries: its task class, the
// system framing it is always given (verbatim - never a paraphrase and never a summary),
// and the closed label set it may answer with. A reading outside that set is one the
// device cannot name, which is what an escalation is.
type EdgeContract struct {
	Class   string   `json:"class,omitempty"`
	Framing string   `json:"framing,omitempty"`
	Labels  []string `json:"labels,omitempty"`
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
