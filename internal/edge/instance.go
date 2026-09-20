package edge

// INSTANCES - a running `roger` is a member of its node's Edge.
//
// A node is a machine. A machine does not serve a model or run an agent: a PROCESS does. So
// the Edge has a level under the node: the INSTANCE, a running roger with a name the owner
// chooses, the capabilities and bands IT provides, and a life exactly as long as the process.
// Several instances run on one node; the node's capabilities are the union of theirs.
//
// AN INSTANCE HOLDS NO CERTIFICATE. It is inside its node's trust boundary - the same machine
// under the same owner - so anything that could forge one could already be the node. The
// node's LAN face reports its household over the node's own certificate, and that report is
// the only account of the instances anybody believes (the same rule describe already follows
// for capabilities).
//
// REGISTRATION IS NOT A CEREMONY. A process registers itself by writing one private file
// under the node's config dir (the same per-process pattern as the session mirror, already
// proven there), keeps it fresh while it runs, and removes it on exit. Siblings read the
// directory to learn the household; a file nobody has touched for a whole liveness window is
// a dead process and is ignored and removed. No locks, no daemon, no shared memory.
//
// Spec: features/edge/instances.feature.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// Instance is what a running roger says about itself. It is the store's record, kept as one
// type so the registry file, the describe answer and the fleet record cannot disagree.
type Instance = store.EdgeInstance

const (
	// MaxInstanceName bounds an instance name: it has to fit a describe answer beside a
	// node name, an mDNS-sized label and a strip on the screen.
	MaxInstanceName = 32
	// MaxInstances bounds how many instances a node's face may report and a peer will
	// record. Past it the list is truncated and the record says so.
	MaxInstances = 32
	// InstanceLife is how long an instance file may go untouched before it is a dead
	// process. A running instance beats well inside it (each discovery pass).
	InstanceLife = 2 * time.Minute
)

var (
	// ErrNotEnrolled: a roger on a machine that has not enrolled registers nothing - there
	// is no node for it to be an instance of.
	ErrNotEnrolled = errors.New("this machine is not enrolled on an Edge, so a roger on it is not a member: roger edge enroll <name> first")
	// ErrInstanceNameTaken: two instances on one node cannot share a name.
	ErrInstanceNameTaken = errors.New("that name is taken by another running instance on this node")
	// ErrAmbiguousInstance: a bare name that names instances on more than one node.
	ErrAmbiguousInstance = errors.New("that name is on more than one node; say node/name")
	// ErrNoSuchInstance: no instance by that address.
	ErrNoSuchInstance = errors.New("no such instance on this Edge")
)

// ValidInstanceName is stricter than a node name: lowercase, digits and hyphens only,
// because it becomes half of an address (node/instance) and a label on a strip.
func ValidInstanceName(name string) error {
	switch {
	case name == "":
		return errors.New("an instance needs a name")
	case len(name) > MaxInstanceName:
		return fmt.Errorf("an instance name is at most %d characters", MaxInstanceName)
	case strings.Contains(name, "/"):
		return errors.New("an instance name cannot contain '/': it is written node/instance")
	case strings.Contains(name, ".."):
		return fmt.Errorf("%q is not a usable instance name", name)
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		ok := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || (i > 0 && i < len(name)-1 && ch == '-')
		if !ok {
			return fmt.Errorf("%q is not a usable instance name: lowercase letters, digits and inner hyphens only", name)
		}
	}
	return nil
}

// DefaultInstanceName is the name a roger gets when the owner chose none: the node's own
// name with a small ordinal, the first one free in the household. Deterministic, never
// random, so the same machine's second roger is always "<node>-2".
func DefaultInstanceName(node string, taken []string) string {
	base := strings.ToLower(node)
	if ValidInstanceName(base) != nil || base == "" {
		base = "roger"
	}
	if len(base) > MaxInstanceName-3 {
		base = base[:MaxInstanceName-3]
	}
	used := map[string]bool{}
	for _, t := range taken {
		used[t] = true
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s-%d", base, i)
		if !used[c] {
			return c
		}
	}
}

// SplitAddress splits "node/instance" into its halves; a bare name returns ("", name).
func SplitAddress(addr string) (node, inst string) {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i], addr[i+1:]
	}
	return "", addr
}

// Registry is THIS process's registration: one file it owns under the household dir.
type Registry struct {
	dir  string
	path string
	inst Instance
}

var instanceSeq atomic.Int64

type householdFile struct {
	Node     string   `json:"node"`
	Instance Instance `json:"instance"`
}

// OpenRegistry prepares a registration under dir for the node with id nodeID. Nothing is
// written until Register.
func OpenRegistry(dir, nodeID string) *Registry {
	return &Registry{dir: dir, path: filepath.Join(dir, fmt.Sprintf("%d-%d.json", os.Getpid(), instanceSeq.Add(1)))}
}

// Path is this registration's own file ("" before Register).
func (r *Registry) Path() string { return r.path }

// Register makes this process a member of its node. It refuses a machine that has not
// enrolled (no node to be an instance of), an unusable name, and a name another running
// instance on this node already holds. It issues nothing and dials nothing.
func Register(dir, nodeID string, inst Instance, now time.Time) (*Registry, error) {
	if nodeID == "" {
		return nil, ErrNotEnrolled
	}
	if err := ValidInstanceName(inst.Name); err != nil {
		return nil, err
	}
	r := OpenRegistry(dir, nodeID)
	for _, h := range Household(dir, now) {
		if h.Name == inst.Name {
			return nil, ErrInstanceNameTaken
		}
	}
	if inst.Started == 0 {
		inst.Started = now.Unix()
	}
	r.inst = inst
	if err := r.write(nodeID); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) write(nodeID string) error {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(householdFile{Node: nodeID, Instance: r.inst})
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// Instance is what this process registered.
func (r *Registry) Instance() Instance { return r.inst }

// Update rewrites the registration with new facts (bands on air, resources) and beats it.
func (r *Registry) Update(nodeID string, fn func(*Instance)) error {
	fn(&r.inst)
	return r.write(nodeID)
}

// Rename gives this instance a new name, refusing one a sibling holds.
func (r *Registry) Rename(nodeID, name string, now time.Time) error {
	if err := ValidInstanceName(name); err != nil {
		return err
	}
	for _, h := range Household(r.dir, now) {
		if h.Name == name && h.Name != r.inst.Name {
			return ErrInstanceNameTaken
		}
	}
	r.inst.Name = name
	return r.write(nodeID)
}

// Beat marks the registration fresh: a running instance is one whose file keeps moving.
func (r *Registry) Beat(now time.Time) error { return os.Chtimes(r.path, now, now) }

// Deregister removes this process's registration. Idempotent.
func (r *Registry) Deregister() error {
	err := os.Remove(r.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Household is every LIVE instance registered under dir, this process's included, sorted
// by name. A file untouched for a whole InstanceLife is a dead process: skipped and
// removed. A file that is not a registration is nobody's instance.
func Household(dir string, now time.Time) []Instance {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	// Non-nil even when empty: an EMPTY household is a fact a face reports ("nothing is
	// running here"), distinct from a face too old to report one (nil).
	out := []Instance{}
	for _, p := range files {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if now.Sub(fi.ModTime()) > InstanceLife {
			_ = os.Remove(p)
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var h householdFile
		if json.Unmarshal(b, &h) != nil || h.Instance.Name == "" {
			continue
		}
		out = append(out, h.Instance)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DescribeInstances is the household as a node's face reports it: bounded, and the
// truncation is said rather than hidden.
func DescribeInstances(dir string, now time.Time) ([]Instance, bool) {
	h := Household(dir, now)
	if len(h) > MaxInstances {
		return h[:MaxInstances], true
	}
	return h, false
}

// unionCaps folds instance capabilities into the node's: one entry per capability, the
// BEST state among its providers, and the providers named. States already earned on the
// node (an owner's actuate confirmation) are carried by name.
func unionCaps(prev []store.EdgeCap, insts []Instance) []store.EdgeCap {
	kept := map[string]store.EdgeCap{}
	for _, c := range prev {
		kept[c.Name] = c
	}
	var order []string
	by := map[string]*store.EdgeCap{}
	for _, in := range insts {
		for _, c := range in.Caps {
			e, ok := by[c.Name]
			if !ok {
				order = append(order, c.Name)
				nc := store.EdgeCap{Name: c.Name, State: c.State}
				if p, had := kept[c.Name]; had {
					nc.ConfirmedBy, nc.ConfirmedAt = p.ConfirmedBy, p.ConfirmedAt
					if p.State == string(Verified) && c.Name == string(Actuate) {
						nc.State = p.State // the owner's yes is the node's, not a process's
					}
				}
				e = &nc
				by[c.Name] = e
			}
			if rank(c.State) > rank(e.State) {
				e.State = c.State
			}
			e.Instances = append(e.Instances, in.Name)
		}
	}
	out := make([]store.EdgeCap, 0, len(order))
	for _, c := range Capabilities() { // the design's order
		if e, ok := by[string(c)]; ok {
			out = append(out, *e)
		}
	}
	return out
}

func rank(state string) int {
	switch state {
	case string(Verified):
		return 2
	case string(PendingConfirmation):
		return 1
	}
	return 0
}
