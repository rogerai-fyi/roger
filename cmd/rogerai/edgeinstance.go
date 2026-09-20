package main

// THIS ROGER AS AN INSTANCE OF ITS NODE (features/edge/instances.feature).
//
// A node is the machine; this process is one of the rogers running on it. When the machine
// is enrolled, this process registers itself in the node's household (one private file
// under the config dir), keeps the registration fresh on every discovery pass with the bands
// it has on air and the resources it could read, reports the whole household on the node's
// LAN face, and deregisters on exit. No ceremony: joining the Edge is per machine, being on
// it is per process.

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// edgeInstancesDir is the node's household: every running roger's registration.
func edgeInstancesDir() string { return filepath.Join(filepath.Dir(configPath()), "edge-instances") }

// edgeOnAir reports the models THIS process has on air. Set once the node controller
// exists (edgeAttachOnAir); nil until then, and nil for a roger that shares nothing.
var edgeOnAir func() []string

// edgeAttachOnAir hands the host the live source of what this process is broadcasting.
func edgeAttachOnAir(fn func() []string) { edgeOnAir = fn }

// edgeInstanceName is the name this process registers under: the owner's choice from the
// config, or the first free default derived from the node's name.
func edgeInstanceName(cfg config, node string, taken []string) string {
	// ROGER_EDGE_INSTANCE is a PER-PROCESS override, so two rogers sharing one config dir
	// (two instances of one node - "2 or 3 rogers on this PC") each get the name the owner
	// chose without fighting over the one saved EdgeInstance. The env wins over the config,
	// the config over a derived default.
	if v := strings.TrimSpace(os.Getenv("ROGER_EDGE_INSTANCE")); v != "" && edge.ValidInstanceName(v) == nil {
		return v
	}
	if cfg.EdgeInstance != "" && edge.ValidInstanceName(cfg.EdgeInstance) == nil {
		return cfg.EdgeInstance
	}
	return edge.DefaultInstanceName(node, taken)
}

// edgeInstanceCaps is what this process can be asked for: it operates (it runs an agent),
// and it serves when it has a model on air.
func edgeInstanceCaps(bands []string) []store.EdgeCap {
	caps := []store.EdgeCap{{Name: string(edge.Operate), State: string(edge.Claimed)}}
	if len(bands) > 0 {
		caps = append(caps, store.EdgeCap{Name: string(edge.Serve), State: string(edge.Claimed)})
	}
	return caps
}

// registerInstance joins the household. A machine that has not enrolled registers
// nothing, and that is a state, not an error.
func (h *edgeHost) registerInstance(cfg config) {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return
	}
	now := time.Now()
	dir := edgeInstancesDir()
	taken := []string{}
	for _, in := range edge.Household(dir, now) {
		taken = append(taken, in.Name)
	}
	bands := edgeBandsOnAir()
	inst := edge.Instance{Name: edgeInstanceName(cfg, h.enrolledName(), taken), Caps: edgeInstanceCaps(bands),
		Bands: bands, Resources: edgeReadResources(now)}
	reg, err := edge.Register(dir, id.NodeID, inst, now)
	if err != nil {
		// A taken name is the one refusal a default cannot avoid on a race: fall back to
		// the next free default rather than leaving this roger off its own Edge.
		inst.Name = edge.DefaultInstanceName(h.enrolledName(), append(taken, inst.Name))
		if reg, err = edge.Register(dir, id.NodeID, inst, now); err != nil {
			return
		}
	}
	h.mu.Lock()
	h.reg, h.regNode = reg, id.NodeID
	h.mu.Unlock()
}

// beatInstance keeps the registration fresh and true: the bands on air right now, the
// resources as of now, and the name the owner may have changed in the config meanwhile.
func (h *edgeHost) beatInstance(cfg config) {
	h.mu.Lock()
	reg, node := h.reg, h.regNode
	h.mu.Unlock()
	if reg == nil {
		return
	}
	now := time.Now()
	bands := edgeBandsOnAir()
	_ = reg.Update(node, func(i *edge.Instance) {
		i.Bands, i.Caps, i.Resources = bands, edgeInstanceCaps(bands), edgeReadResources(now)
	})
	// An env-named process keeps its env name; only a config choice (and only when no env
	// override is set) renames a running instance to follow the owner's `roger edge name .`.
	if os.Getenv("ROGER_EDGE_INSTANCE") == "" {
		if want := cfg.EdgeInstance; want != "" && want != reg.Instance().Name {
			_ = reg.Rename(node, want, now)
		}
	}
	_ = reg.Beat(now)
}

// deregisterInstance leaves the household. Idempotent.
func (h *edgeHost) deregisterInstance() {
	h.mu.Lock()
	reg := h.reg
	h.reg = nil
	h.mu.Unlock()
	if reg != nil {
		_ = reg.Deregister()
	}
}

// household is what this node's face reports: every live registration here.
func (h *edgeHost) household() ([]edge.Instance, bool) {
	return edge.DescribeInstances(edgeInstancesDir(), time.Now())
}

func edgeBandsOnAir() []string {
	if edgeOnAir == nil {
		return nil
	}
	return edgeOnAir()
}

// edgeReadResources reads what it can and invents nothing: RAM from the kernel on Linux, a
// GPU from nvidia-smi when the tool is present and answers within a second. Anything that
// could not be read is absent from the record, never zero.
func edgeReadResources(now time.Time) *store.EdgeResources {
	r := &store.EdgeResources{}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		var total, avail float64
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 2 {
				continue
			}
			kb, _ := strconv.ParseFloat(fs[1], 64)
			switch fs[0] {
			case "MemTotal:":
				total = kb / 1024 / 1024
			case "MemAvailable:":
				avail = kb / 1024 / 1024
			}
		}
		f.Close()
		if total > 0 {
			r.RAMTotalGB, r.RAMUsedGB = total, total-avail
		}
	}
	if path, err := exec.LookPath("nvidia-smi"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, path, "--query-gpu=name,memory.used,memory.total", "--format=csv,noheader,nounits").Output()
		if err == nil {
			if fs := strings.Split(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), ","); len(fs) == 3 {
				used, _ := strconv.ParseFloat(strings.TrimSpace(fs[1]), 64)
				total, _ := strconv.ParseFloat(strings.TrimSpace(fs[2]), 64)
				if total > 0 {
					r.GPU, r.GPUMemUsed = strings.TrimSpace(fs[0]), used/total
				}
			}
		}
	}
	if r.RAMTotalGB == 0 && r.GPU == "" {
		return nil // nothing could be read: no record, rather than a record of zeros
	}
	r.ReadAt = now.Unix()
	return r
}

// edgeUpstreamOf resolves a band to the local server that has it on air in THIS process.
// Set once the node controller exists (edgeAttachUpstream); nil for a roger that shares
// nothing, in which case the face serves describe only.
var edgeUpstreamOf func(band string) (chatURL, key string, ok bool)

// edgeAttachUpstream hands the host the live map of what this process serves and where.
func edgeAttachUpstream(fn func(band string) (chatURL, key string, ok bool)) { edgeUpstreamOf = fn }

// serving is what this node's face needs to answer peers (features/edge/local_inference.
// feature): the verify-only trust to authenticate them, membership in THIS fleet to admit
// them, the upstream that has the band, and the node key to receipt with. Absent when this
// machine holds no trust (not enrolled) - the face then answers describe only.
func (h *edgeHost) peerServing(key ed25519.PrivateKey, nodeID string) (edge.Serving, bool) {
	auth, _, err := edgeIdentityStore().Trust(time.Now())
	if err != nil || auth == nil {
		return edge.Serving{}, false
	}
	return edge.Serving{
		Trust: auth,
		Member: func(id string) bool {
			if id == nodeID {
				return true
			}
			_, ok, err := h.st.fleet.Get(id)
			return err == nil && ok
		},
		Upstream: func(band string) (string, string, string, bool) {
			if edgeUpstreamOf == nil {
				return "", "", "", false
			}
			u, k, ok := edgeUpstreamOf(band)
			if !ok {
				return "", "", "", false
			}
			inst := ""
			h.mu.Lock()
			if h.reg != nil {
				inst = h.reg.Instance().Name
			}
			h.mu.Unlock()
			return u, k, inst, true
		},
		NodeID: nodeID, Key: key,
	}, true
}
