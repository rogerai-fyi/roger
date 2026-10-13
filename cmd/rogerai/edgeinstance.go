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
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
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
// maintain is whether the owner turned maintenance on HERE (features/edge/maintenance.feature). It is
// a PARAMETER rather than a read of the global config: the callers all hold the config already, and a
// helper that reaches for it silently depends on whatever config the process happens to see - which
// is how a test that never isolated its config started failing once maintenance existed.
func edgeInstanceCaps(bands []string, maintain bool) []store.EdgeCap {
	caps := []store.EdgeCap{{Name: string(edge.Operate), State: string(edge.Claimed)}}
	if len(bands) > 0 {
		caps = append(caps, store.EdgeCap{Name: string(edge.Serve), State: string(edge.Claimed)})
	}
	// Off by default, so the face refuses every maintenance invoke on a machine whose owner never
	// asked for it.
	if maintain {
		caps = append(caps, store.EdgeCap{Name: string(edge.Maintain), State: string(edge.Claimed)})
	}
	return caps
}

// edgeInstanceCapsFor is edgeInstanceCaps with every owner switch on THIS machine applied.
func edgeInstanceCapsFor(cfg config, bands []string) []store.EdgeCap {
	caps := edgeInstanceCaps(bands, edgeMaintenanceOn(cfg))
	if edgeServeControlOn(cfg) {
		caps = append(caps, store.EdgeCap{Name: string(edge.ServeControl), State: string(edge.Claimed)})
	}
	return caps
}

// edgeIsAgentRole reports whether THIS roger is declared an agent on its Edge, by ANY of the
// three ways the founder ruled (features/edge/agents.feature): the ROGER_EDGE_AGENT launch env
// (a scripted/automatic agent job, wins per-run), or the persisted config role (set by
// `roger edge agent on`). Claiming the operate capability is not itself this declaration.
func edgeIsAgentRole(cfg config) bool {
	if v, ok := os.LookupEnv("ROGER_EDGE_AGENT"); ok {
		if on, valid := edgeParseOnOff(v); valid {
			return on // on/off/yes/no/true/false/1/0 all understood, incl. an explicit off
		}
		return strings.TrimSpace(v) != "" // any other non-empty value reads as on
	}
	return cfg.EdgeAgent
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
	inst := edge.Instance{Name: edgeInstanceName(cfg, h.enrolledName(), taken), Caps: edgeInstanceCapsFor(cfg, bands), Model: edgeModelNow(),
		Bands: bands, Voices: voicesOf(bands), Agent: edgeIsAgentRole(cfg), Resources: edgeReadResources(now), Software: edgeReadSoftware(),
		PID:  os.Getpid(),  // its own pid, about itself: what lets the owner stop it from another machine
		Port: h.facePort()} // its OWN face, so a caller can address this roger rather than the machine
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
	if h.closing {
		h.mu.Unlock()
		_ = reg.Deregister() // the host is quitting; do not leave an orphan registration
		return
	}
	h.reg, h.regNode = reg, id.NodeID
	h.mu.Unlock()
	// The name this process answers to, for the one-runner-per-machine rule the schedules use.
	edgeMyInstanceName = func() string {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.reg == nil {
			return ""
		}
		return h.reg.Instance().Name
	}
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
	agent := edgeIsAgentRole(cfg)
	_ = reg.Update(node, func(i *edge.Instance) {
		i.Bands, i.Caps, i.Resources = bands, edgeInstanceCapsFor(cfg, bands), edgeReadResources(now)
		i.Voices = voicesOf(bands)
		i.Model = edgeModelNow() // what it serves of its own and is fetching, so the fleet can see it
		i.Agent = agent          // a change via `roger edge agent on` is picked up on the next pass
		// The software record is refreshed every beat, exactly like the resources: it is what the
		// machine says NOW. Refreshing only when absent left a stale record standing for the life of
		// the process, so a kernel upgrade - or a fix to the reader itself - showed up on a restart
		// and nowhere else.
		i.Software = edgeReadSoftware()
		i.PID = os.Getpid()   // refreshed with the rest, so a record written by an older roger gains it
		i.Port = h.facePort() // and its own face port, which is only known once the listener is up
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
	var out []string
	if edgeOnAir != nil {
		out = append(out, edgeOnAir()...)
	}
	// A band a SEPARATE headless share on this machine has on air is this machine's too: the face
	// serves it to members from the share's local record (lanShareUpstream).
	for _, rec := range onair.Shares() {
		if !slices.Contains(out, rec.Band) {
			out = append(out, rec.Band)
		}
	}
	// SORTED: the order a process happens to list its models in is not a fact about the Edge, and an
	// unstable order rewrote the registration and the fleet record on every pass.
	sort.Strings(out)
	return out
}

// lanShareUpstream resolves band to a separate headless share's upstream on this machine, from its
// owner-only record. The upstream and its key never leave this machine: only the band is advertised.
func lanShareUpstream(band string) (chatURL, key string, ok bool) {
	for _, rec := range onair.Shares() {
		if rec.Band == band {
			return rec.ChatURL, rec.Key, true
		}
	}
	return "", "", false
}

// edgeReadResources reads what it can and invents nothing: RAM from the kernel on Linux, a
// GPU from nvidia-smi when the tool is present and answers within a second. Anything that
// could not be read is absent from the record, never zero.
func edgeReadResources(now time.Time) *store.EdgeResources { return edgeReadResourcesFrom("/", now) }

// edgeReadResourcesFrom reads the health facts under root (features/edge/health.feature §1): memory
// used/total, 1-minute load, uptime, CPU temperature, every mounted real filesystem's used/total,
// and a GPU when nvidia-smi answers. Every value is a REAL reading stamped with the moment it was
// taken; a metric the platform does not expose is left absent - never zero. Rooted so a test can
// stand up a fake /proc and /sys.
func edgeReadResourcesFrom(root string, now time.Time) *store.EdgeResources {
	r := &store.EdgeResources{}
	readFile := func(rel string) (string, bool) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	if body, ok := readFile("proc/meminfo"); ok {
		var total, avail float64
		for _, line := range strings.Split(body, "\n") {
			fs := strings.Fields(line)
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
		if total > 0 {
			r.RAMTotalGB, r.RAMUsedGB = total, total-avail
		}
	}
	if body, ok := readFile("proc/loadavg"); ok {
		if fs := strings.Fields(body); len(fs) > 0 {
			if v, err := strconv.ParseFloat(fs[0], 64); err == nil {
				r.CPULoad1 = &v
			}
		}
	}
	if body, ok := readFile("proc/uptime"); ok {
		if fs := strings.Fields(body); len(fs) > 0 {
			if v, err := strconv.ParseFloat(fs[0], 64); err == nil && v > 0 {
				r.UptimeS = int64(v)
			}
		}
	}
	// CPU temperature: the first thermal zone that names the CPU/package/SoC, in millidegrees. A
	// board with no such sensor gets NO field.
	if zones, err := filepath.Glob(filepath.Join(root, "sys/class/thermal/thermal_zone*")); err == nil {
		for _, z := range zones {
			typ, _ := os.ReadFile(filepath.Join(z, "type"))
			t := strings.ToLower(strings.TrimSpace(string(typ)))
			if !(strings.Contains(t, "cpu") || strings.Contains(t, "pkg") || strings.Contains(t, "soc") || strings.Contains(t, "acpitz") || strings.Contains(t, "core")) {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(z, "temp"))
			if err != nil {
				continue
			}
			if milli, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64); err == nil {
				c := milli / 1000
				r.CPUTempC = &c
				break
			}
		}
	}
	// Many machines expose the CPU sensor only through hwmon (AMD k10temp/zenpower, Intel coretemp,
	// Pi cpu_thermal), with no thermal zone at all - so fall back to it before giving up.
	if r.CPUTempC == nil {
		if mons, err := filepath.Glob(filepath.Join(root, "sys/class/hwmon/hwmon*")); err == nil {
			for _, m := range mons {
				nameB, _ := os.ReadFile(filepath.Join(m, "name"))
				name := strings.ToLower(strings.TrimSpace(string(nameB)))
				if !(name == "k10temp" || name == "coretemp" || name == "zenpower" || name == "cpu_thermal" || strings.Contains(name, "cpu")) {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(m, "temp1_input"))
				if err != nil {
					continue
				}
				if milli, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64); err == nil {
					c := milli / 1000
					r.CPUTempC = &c
					break
				}
			}
		}
	}
	// Disks: every mounted REAL filesystem (a /dev device on a real fs type), never proc/tmpfs
	// pseudo-mounts, and each DEVICE once - btrfs subvolumes (/, /home, /.snapshots) are one
	// filesystem mounted several times, not three disks. Used/total via statfs, which a test can fake.
	if body, ok := readFile("proc/mounts"); ok {
		seen := map[string]bool{}
		for _, line := range strings.Split(body, "\n") {
			fs := strings.Fields(line)
			if len(fs) < 3 || !strings.HasPrefix(fs[0], "/dev/") || !edgeRealFS(fs[2]) || seen[fs[0]] {
				continue
			}
			seen[fs[0]] = true
			if used, total, ok := edgeStatfs(fs[1]); ok && total > 0 {
				r.Disks = append(r.Disks, store.EdgeDisk{Mount: fs[1], UsedGB: used, TotalGB: total})
			}
		}
	}
	// A Raspberry Pi's power word: the firmware publishes it on the platform bus (the Pi 4 and the Pi
	// 5 name the bus differently, hence the glob). Hex, with or without its 0x.
	if ws, _ := filepath.Glob(filepath.Join(root, "sys/devices/platform/soc*/*firmware/get_throttled")); len(ws) > 0 {
		if raw, err := os.ReadFile(ws[0]); err == nil {
			txt := strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x")
			if v, err := strconv.ParseUint(txt, 16, 32); err == nil {
				w := uint32(v)
				r.PiPower = &w
			}
		}
	}
	// Card and eMMC wear, from the device's own life_time register: 0x01 is 0-10% used ... 0x0A is
	// 90-100%, 0x0B past its life. The upper bound is reported, because the hopeful end of a wear
	// estimate is how a card dies unannounced. 0x00 means the card does not say.
	if ls, _ := filepath.Glob(filepath.Join(root, "sys/block/mmcblk*/device/life_time")); len(ls) > 0 {
		if raw, err := os.ReadFile(ls[0]); err == nil {
			if fs := strings.Fields(string(raw)); len(fs) > 0 {
				if v, err := strconv.ParseUint(strings.TrimPrefix(fs[0], "0x"), 16, 8); err == nil && v > 0 {
					pct := float64(min(v, 10)) * 10
					r.SDWearPct = &pct
				}
			}
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
	if r.RAMTotalGB == 0 && r.GPU == "" && r.CPULoad1 == nil && r.UptimeS == 0 && r.CPUTempC == nil && len(r.Disks) == 0 &&
		r.PiPower == nil && r.SDWearPct == nil {
		return nil // nothing could be read: no record, rather than a record of zeros
	}
	r.Power = edgeReadPower(root, now)
	r.ReadAt = now.Unix()
	return r
}

// edgeRealFS reports whether a mount's filesystem type holds real data (a disk), as opposed to a
// kernel pseudo-filesystem whose "space" means nothing to an owner.
func edgeRealFS(t string) bool {
	switch t {
	case "ext2", "ext3", "ext4", "xfs", "btrfs", "f2fs", "zfs", "vfat", "exfat", "ntfs", "ntfs3", "apfs", "hfsplus", "jfs", "reiserfs":
		return true
	}
	return false
}

// edgeSelfDeclares reports whether THIS process will accept an invoke for the given capability, from
// what it actually IS right now - not the self node's fleet record, which is never probed and carries
// no caps. OPERATE (the delegate arrow) is accepted only when the owner has made this roger an AGENT
// (roger edge agent on / the EdgeAgent role): every roger CAN operate, but only one the owner opted
// in should run other nodes' tasks. Other capabilities check the live instance caps, requiring
// VERIFIED for the probe-verified ones; the runner refuses non-operate device caps anyway.
func edgeSelfDeclares(need edge.Capability) bool {
	if need == edge.Operate {
		return edgeIsAgentRole(loadConfig())
	}
	if need == edge.Maintain {
		// The owner's switch on THIS machine is the whole declaration: there is no probe for it, and
		// a grant alone never reaches a machine whose owner did not ask (features/edge/maintenance.feature).
		return edgeMaintenanceOn(loadConfig())
	}
	if need == edge.ServeControl {
		return edgeServeControlOn(loadConfig()) // the same rule (features/edge/remote_model.feature)
	}
	for _, c := range edgeInstanceCaps(edgeBandsOnAir(), edgeMaintenanceOn(loadConfig())) {
		if c.Name == string(need) {
			return c.State == string(edge.Verified)
		}
	}
	return false
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
	troubleLog := edgeAlertLog()
	return edge.Serving{
		Trust: auth,
		// The trouble feed (trouble_feed.feature): members read this machine's alert log and answer
		// under their certificates; a member's id is worded by the name the fleet knows it by.
		Trouble: &troubleLog,
		Names: func(id string) string {
			h.mu.Lock()
			defer h.mu.Unlock()
			if n, ok, err := h.st.fleet.Get(id); err == nil && ok {
				return n.Name
			}
			return ""
		},
		// Re-read trust per request so a node revoked while this face runs is refused at once.
		TrustNow: func() *cert.Authority {
			fresh, _, err := edgeIdentityStore().Trust(time.Now())
			if err != nil {
				return nil
			}
			return fresh
		},
		Member: func(id string) bool {
			if id == nodeID {
				return true
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			_, ok, err := h.st.fleet.Get(id)
			return err == nil && ok
		},
		Upstream: func(band string) (string, string, string, bool) {
			var u, k string
			ok := false
			if edgeUpstreamOf != nil {
				u, k, ok = edgeUpstreamOf(band)
			}
			if !ok {
				u, k, ok = lanShareUpstream(band) // a separate headless share on this machine
			}
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
		Speech: func(band string) (string, string, string, bool) {
			u, k, ok := localSpeechOf(band)
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
		// Authorize is the callee-side gate for /edge/invoke (features/edge/control.feature): the grant
		// must be signed by THIS Edge's authority, name this caller (or be caller-open), ALLOW the
		// capability, and AuthorizeInvoke it on this node. Composed here from the pieces each already
		// tested: VerifyGrant + the caller match + the capability gate + Fleet.AuthorizeInvoke.
		Authorize: func(g store.Grant, callerID string, need edge.Capability) error {
			fresh, _, err := edgeIdentityStore().Trust(time.Now())
			if err != nil || fresh == nil {
				return fmt.Errorf("this node cannot verify grants right now")
			}
			if err := edge.VerifyGrant(g, fresh.Root()); err != nil {
				return err
			}
			// REVOCATION IS NEVER TRUSTED FROM THE WIRE. Revoked is outside the signed canonical bytes
			// (internal/edge/grant.go), so a caller holding a revoked grant could simply send
			// Revoked:false and the signature would still verify. The callee therefore ignores the wire
			// value entirely and applies the standing from a source it TRUSTS: this machine's authority
			// grant store. A grant this node did not issue has no local standing here - cross-node
			// per-grant revocation rides a distributed grant-CRL that is a later slice; until then a
			// caller is still cut off at the node level by cert revocation (internal/edge/verify.go),
			// which the mutual-TLS handshake already enforces before this runs.
			g, err = edgeTrustedRevocation(edgeGrantStore(), g)
			if err != nil {
				return err // fail closed: an unreadable grant store refuses the invoke
			}
			if g.Caller != "" && g.Caller != callerID {
				return fmt.Errorf("that grant is for a different caller")
			}
			if need != "" {
				allowed := false
				for _, c := range g.Caps {
					if c == string(need) {
						allowed = true
						break
					}
				}
				if !allowed {
					return fmt.Errorf("that grant does not allow %s", need)
				}
			}
			h.mu.Lock()
			fleet := h.st.fleet
			h.mu.Unlock()
			// The grant/scope checks (owner, revoked, expiry, node scope) run against the fleet record.
			// The CAPABILITY check does NOT: the callee is THIS process, and its own fleet record is not
			// probed and carries no caps (edgeRecordSelf enrols a bare node; discovery skips self). So
			// authorize the capability against what THIS process actually declares LIVE - which is what
			// will run the task - not the stale self record.
			if err := fleet.AuthorizeInvoke(g, nodeID, ""); err != nil {
				return err
			}
			if need != "" && !edgeSelfDeclares(need) {
				if need == edge.Maintain {
					// The owner's switch is off here. Say THAT, and how to turn it on: "no verified
					// capability for this" is true and tells the operator nothing they can act on.
					return fmt.Errorf("%s", edge.MaintainOffReason)
				}
				if need == edge.ServeControl {
					return fmt.Errorf("%s", edge.ServeControlOffReason)
				}
				return fmt.Errorf("%w: %s", edge.ErrNotVerified, need)
			}
			return nil
		},
		// Invoke is the task RUNNER (features/edge/control.feature, the delegate arrow): once the face
		// has authorized an invoke, this runs the delegated task in THIS roger's own headless harness -
		// its own tools, its own permissions, no human to open a confirm gate - and returns the result.
		// Device capabilities (sense/actuate) are refused here by name: this binary runs an agent, not
		// a board. See cmd/rogerai/edgerunner.go.
		// The name this process REGISTERED, read live: it is what may never be the target of a
		// maintenance action, and a guess from the config named the next roger instead.
		Invoke: edgeInvokeHandler(edgeLiveTurnDeps(func() string {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.reg == nil {
				return "" // not registered: this process is nobody's target
			}
			return h.reg.Instance().Name
		})),
	}, true
}

// selfInstanceName is the name THIS running roger goes by in its own household, so the TUI can
// mark it among the several rogers on this machine. Empty until this instance has registered.
func (h *edgeHost) selfInstanceName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reg == nil {
		return ""
	}
	return h.reg.Instance().Name
}

// setAgentRoleLive makes THIS roger an agent (or stops), in place: it persists the role, marks or
// unmarks the durable record, and re-registers at once so [3] shows the change immediately - the
// screen action behind `roger edge agent on`, without leaving the TUI.
func (h *edgeHost) setAgentRoleLive(on bool) error {
	cfg := loadConfig()
	cfg.EdgeAgent = on
	if err := saveConfig(cfg); err != nil {
		return err
	}
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
	name := edgeThisAgentName(cfg)
	if on {
		_ = edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), name, time.Now())
	} else {
		_, _ = edge.UnmarkPersistent(edgePersistDir(), name)
	}
	h.beatInstance(cfg) // stamp the Agent flag on this instance's registration now
	return nil
}

// addAgentLive launches a NEW agent roger on this machine - a plain, detached roger with the agent
// role and a fresh default name - so "Add Agent" on [3] spawns one without leaving the screen.
func (h *edgeHost) addAgentLive() error {
	taken := []string{}
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		taken = append(taken, in.Name)
	}
	name := edge.DefaultInstanceName(h.enrolledName(), taken)
	return edgeSpawnRoger(map[string]string{"ROGER_EDGE_AGENT": "1", "ROGER_EDGE_INSTANCE": name})
}

// renameSelfLive renames THIS roger among the rogers here, at once.
func (h *edgeHost) renameSelfLive(name string) error {
	if err := edge.ValidInstanceName(name); err != nil {
		return err
	}
	h.mu.Lock()
	reg, node := h.reg, h.regNode
	h.mu.Unlock()
	if reg == nil {
		return nil
	}
	return reg.Rename(node, name, time.Now())
}

// resumeAgentLive relaunches a DARK persistent agent from the [3] panel - the same detached
// plain-roger spawn `roger edge agent resume` uses, but silent (the TUI owns the screen). It
// declines when the name is not a known persistent agent, or when it is already running.
func (h *edgeHost) resumeAgentLive(name string) error {
	if !edge.IsPersistent(edgePersistDir(), name) {
		return fmt.Errorf("no persistent agent named %q", name)
	}
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		if in.Name == name {
			return nil // already running - nothing to do
		}
	}
	return edgeSpawnRoger(map[string]string{"ROGER_EDGE_AGENT": "1", "ROGER_EDGE_INSTANCE": name})
}

// removeAgentLive forgets a persistent agent from the [3] panel - drops the durable record only,
// never touching a live process (remove ends the persistence, it does not kill a run).
func (h *edgeHost) removeAgentLive(name string) error {
	_, err := edge.UnmarkPersistent(edgePersistDir(), name)
	return err
}

// setAgentJobLive binds a model/job to an agent from the [3] panel - the durable assignment the
// `roger edge model`/`roger edge job` commands write, but silent (the TUI owns the screen).
func (h *edgeHost) setAgentJobLive(j edge.AgentJob) error {
	if j.Node == "" {
		j.Node = edgeThisNodeID()
	}
	return edge.SetAgentJob(edgeJobsDir(), j, time.Now())
}

// clearAgentJobLive drops an agent's assignment from the [3] panel.
func (h *edgeHost) clearAgentJobLive(name string) error {
	_, err := edge.ClearAgentJob(edgeJobsDir(), name)
	return err
}
