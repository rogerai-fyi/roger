package main

// THE EDGE HOST: what makes a shipped `roger` show the operator's REAL fleet.
//
// internal/edge holds the fleet, the discovery engine and the LAN transport; internal/tui
// holds the [3] EDGE screen. Neither knows how to find the other, and this is the seam
// that joins them - the same one `roger edge` already stands on.
//
// ONE EDGE PER MACHINE. The CLI keeps the last-known Edge beside config.json (edge.json)
// and reads it through loadEdgeState; the TUI screen is given THAT fleet and THOSE
// candidates, and every write the screen makes goes back through the same save. There is
// no second source of truth to drift, so what the TUI adopts is what `roger edge list`
// prints in the next process.
//
// DISCOVERY IS NEVER ON THE PATH OF ANYTHING. It runs on its own goroutine, it is brought
// up there rather than at the call that starts it (so a wedged socket costs the launch
// nothing), a heartbeat is handed to the screen without ever blocking on it, and
// ROGERAI_EDGE_DISCOVERY=0 builds nothing at all - see the last section of
// features/edge/discovery.feature.

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
	"rogerai.fm/roger/v6/internal/tui"
)

const (
	// edgeBeatBuffer bounds the heartbeats waiting for the screen. A run with no TUI (the
	// browser console alone) drains nothing at all, so this is a ring the host drops from
	// rather than a queue that grows.
	edgeBeatBuffer = 64
	// edgeStopGrace is how long a quit waits for the discovery goroutine. Cancelling the
	// context is what ends it; this bound is for the case where it is stuck in a socket
	// syscall, because nobody's quit should hang on the network.
	edgeStopGrace = 2 * time.Second
)

// edgeHost is this process's Edge: the fleet, the candidate list, the discovery engine
// that keeps both current, and the heartbeats the screen animates on.
type edgeHost struct {
	self  string
	st    *edgeState
	beats chan string

	disc    *edge.Discovery
	every   time.Duration
	cancel  context.CancelFunc
	closing bool // set under mu when stop() begins, so a late registerInstance does not orphan a record
	done    chan struct{}
	once    sync.Once

	// face is this machine's LAN identity: the TLS describe listener a peer dials to
	// check who we are. It exists only once this machine has enrolled, because before
	// that there is nothing true to serve.
	//
	// auth is the LAN issuing service, on the ONE machine per Edge the owner
	// designated as its authority. Without it a plant network could form a root and
	// never enroll a second machine against it.
	//
	// BOTH are guarded by mu: the discovery goroutine brings them up (an enrollment can
	// happen while this process is already running) and a quit takes them down, and a
	// quit that timed out is a quit racing a pass that is still going.
	face   *http.Server
	faceLn net.Listener
	srv    *edge.Server
	// reg is this process's registration in the node's household (edgeinstance.go).
	reg     *edge.Registry
	regNode string
	cfg     config
	auth    *http.Server
	authLn  net.Listener
	issuer  *edgeauth.Issuer

	// The last discovery pass, for the empty screen's DISCOVERY fact: when it ran and
	// what it found. Guarded by mu like the candidates it produced.
	lastPass    time.Time
	lastFound   string
	unavailable bool
	armed       bool // the engine exists (ROGERAI_EDGE_DISCOVERY was not 0)
	// The status is polled - once a frame by the TUI, once a second by the console - and
	// reading it opens the authority's root and dials for the LAN address. It is cached
	// for edgeStatusTTL; the files it reads change on the owner's own commands, not
	// per second. statusNow is a clock seam for the cache's test.
	statusVal edge.SelfStatus
	statusAt  time.Time
	statusNow func() time.Time

	// mu guards the candidate list and the state-file write: the discovery goroutine
	// refreshes both while the UI goroutine reads them for a frame.
	mu sync.Mutex
}

// newEdgeHost loads this machine's Edge through the CLI's own loader, so the TUI and
// `roger edge` are looking at one file and one fleet.
func newEdgeHost(self string) (*edgeHost, error) {
	st, err := loadEdgeState()
	if err != nil {
		return nil, err
	}
	return &edgeHost{self: self, st: st, beats: make(chan string, edgeBeatBuffer), cfg: loadConfig()}, nil
}

// wire fills the Edge seams on the hooks the TUI is built from.
func (h *edgeHost) wire(hooks *tui.Hooks) {
	if h.self != "" {
		hooks.EdgeSelf = h.self
	}
	// An ENROLLED machine knows what it is called on its own graph: the name the owner
	// gave it when it joined. That beats the Station name, which is a supply-side label
	// and may not exist at all.
	if name := h.enrolledName(); name != "" {
		hooks.EdgeSelf = name
	}
	hooks.EdgeFleet = h.st.fleet
	hooks.EdgeSessions = h.st.sessions
	hooks.EdgeCandidates = h.candidates
	hooks.EdgeAdopt = h.adopt
	hooks.EdgeHeartbeats = h.beats
	hooks.EdgeStatus = h.status
	// The rogers running on THIS machine, read live from the local instance registry - so [3]
	// shows the owner's own instances and agents (mDNS cannot see them over loopback).
	hooks.EdgeHousehold = func() []edge.Instance { return edge.Household(edgeInstancesDir(), time.Now()) }
	hooks.EdgeName = h.nameNode
	hooks.EdgeForget = h.forgetNode
	hooks.EdgeSetPrefer = h.setPrefer
	hooks.EdgeGrants = h.grants
	hooks.EdgeAlerts = edgeFiringAlerts
	hooks.EdgeFloor = func(now time.Time) ([]edge.StateSample, []edge.TroubleCall, int, edge.SignOffReport) {
		l := edgeAlertLog()
		samples, _ := l.States(now.Add(-24 * time.Hour))
		calls, rate, so, _ := l.Report(now)
		return samples, calls, rate, so
	}
	hooks.EdgeReads = func() map[string]int64 { reads, _ := edgeAlertLog().Reads(); return reads }
	hooks.EdgeRoger = func(ruleID, nodeID string) error {
		return edgeAlertLog().Roger(ruleID, nodeID, edgeOperatorName(), time.Now())
	}
	hooks.EdgeStandBy = func(ruleID, nodeID string, d time.Duration) error {
		return edgeAlertLog().StandByAs(ruleID, nodeID, edgeOperatorName(), time.Now().Add(d))
	}
	hooks.EdgeHealthLive = true
	hooks.EdgeIssueGrant = h.issueGrant
	hooks.EdgeRevokeGrant = h.revokeGrant
	// The agents the owner marked persistent - a durable record, so one that has stopped still
	// draws (dark) with resume/remove (features/edge/agents.feature).
	hooks.EdgePersistentAgents = func() []edge.PersistentAgent { return edge.PersistentAgents(edgePersistDir()) }
	hooks.EdgeSelfInstance = h.selfInstanceName
	// The [3] control panel's in-place actions, so the owner acts from the screen (no leaving to
	// type `roger edge agent on`).
	hooks.EdgeAgentActive = func() bool { return edgeIsAgentRole(loadConfig()) }
	hooks.EdgeSetAgent = h.setAgentRoleLive
	hooks.EdgeAddAgent = h.addAgentLive
	hooks.EdgeRenameSelf = h.renameSelfLive
	hooks.EdgeResumeAgent = h.resumeAgentLive
	hooks.EdgeRemoveAgent = h.removeAgentLive
	hooks.EdgeAdopting = h.adopting
	hooks.EdgeAgentJobs = func() []edge.AgentJob { return edge.AgentJobs(edgeJobsDir()) }
	hooks.EdgeSetAgentJob = h.setAgentJobLive
	hooks.EdgeClearAgentJob = h.clearAgentJobLive
	// The onboarding wizard (features/edge/onboard.feature) drives the SAME enrollment the
	// `roger edge` commands do, through this seam - never its own copy of the logic.
	setup := edgeSetupHooks(h.cfg)
	hooks.EdgeSetup = &setup
	hooks.EdgeSetupOf = edgeSetupState
}

// status is what is true about THIS machine right now - enrolled, rooted where, scanning
// - read from the same files the CLI reads, plus what only the running host knows: the
// last pass and this machine's authority address.
func (h *edgeHost) status() edge.SelfStatus {
	now := time.Now
	if h.statusNow != nil {
		now = h.statusNow
	}
	h.mu.Lock()
	if !h.statusAt.IsZero() && now().Sub(h.statusAt) < edgeStatusTTL {
		v := h.statusVal
		h.mu.Unlock()
		return v
	}
	// st is read under the lock: adoptEnrollment replaces it when this machine joins
	// an Edge while running, and a status must be read from one state, not two.
	st := h.st
	facts := edgeDiscoveryFacts{
		Enabled: h.armed, Unavailable: h.unavailable, Interval: h.every,
		LastPass: h.lastPass, Found: h.lastFound, AuthorityAddr: edgeAuthorityURL(h.authLn),
	}
	h.mu.Unlock()
	// Enabled is whether the ENGINE exists (arm built one), not what the environment
	// says: a knob that reads "on" over an engine that never came up would be a claim.
	if facts.Interval == 0 {
		facts.Interval = edge.ConfigFromEnv(nil).Interval
	}
	v := edgeSelfStatus(st.fleet, facts)
	h.mu.Lock()
	h.statusVal, h.statusAt = v, now()
	h.mu.Unlock()
	return v
}

// edgeStatusTTL bounds how often the status is really read under a polling viewer.
const edgeStatusTTL = 5 * time.Second

// candidates is what discovery has SEEN and nobody has adopted. They are handed over as a
// copy of their own list, never merged into the fleet: this machine holds no Edge
// authority until enrollment ships, so an unadopted peer has had nothing about it
// verified and must never be drawn as a member.
func (h *edgeHost) candidates() []store.EdgeNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]store.EdgeNode(nil), h.st.candidates...)
}

// adopting is the nodes the owner has adopted (a claim was granted) that have not yet claimed and
// checked in - shown as ADOPTING so a just-adopted phone is visible between adopt and membership
// (features/edge/claim.feature). Once a node claims and presents it is in the fleet, and drops off
// this list. Only an authority has grants.
func (h *edgeHost) adopting() []store.EdgeNode {
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !ok {
		return nil
	}
	grants, err := local.Claims()
	if err != nil || len(grants) == 0 {
		return nil
	}
	h.mu.Lock()
	fleet := h.st.fleet
	h.mu.Unlock()
	var out []store.EdgeNode
	for _, g := range grants {
		if fleet != nil {
			if _, known, _ := fleet.Get(g.NodeID); known {
				continue // it claimed and presented - a member now, not adopting
			}
		}
		name := g.Name
		if name == "" {
			name = g.NodeID
		}
		out = append(out, store.EdgeNode{ID: g.NodeID, Name: name})
	}
	return out
}

// adopt is the owner's explicit `a` on the Edge screen. It takes the SAME path as
// `roger edge adopt`: the certificate is checked before anything is enrolled, and the
// cache is written so the CLI prints what the screen just did.
func (h *edgeHost) adopt(id, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok, err := edgeResolve(h.st.candidates, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no candidate %q was seen on this network", id)
	}
	// A device that LEFT the Edge (a phone: "Leave this Edge") does NOT tell the authority, so it is
	// still a member here. Re-adopting it must not blindly mint a SECOND certificate while the first is
	// live; but a member that has effectively LEFT should be able to rejoin. So: a LIVE (verified)
	// member is refused (forget it first); a member that is NOT live falls through - a claim device is
	// RE-ADMITTED (old cert revoked, fresh claim; edgeAdoptByClaim gates a revoked device to the
	// explicit readmit), and a serving node is re-dialed, re-verified and re-pinned.
	n, known, err := h.st.fleet.Get(c.ID)
	if err != nil {
		return fmt.Errorf("could not check whether %s is already a member: %w", edgeShortID(c.ID), err)
	}
	if known && (c.Pin != "" || edge.Presence(n.Presence) == edge.PresenceVerified) {
		// Refuse re-adopting a known member on the SERVING path (Pin != "") on ANY presence: re-pinning
		// a member from an unauthenticated LAN advert could point it at an attacker's certificate and
		// take over that node's identity in the fleet. Refuse a LIVE member on any path too. Only a
		// NON-live member rejoining by CLAIM (Pin == "", where the device's node KEY proves its
		// identity and an advert cannot forge it) is re-admitted below.
		name := n.Name
		if name == "" {
			name = edgeShortID(c.ID)
		}
		return fmt.Errorf("%s is already a member of this Edge - forget it first to re-adopt", name)
	}
	// A candidate that advertises NO certificate (Pin empty) - a phone - cannot be dialed and
	// verified; it joins by CLAIM. Adopting it GRANTS the claim, and it becomes a member when it
	// claims its certificate and checks in (features/edge/claim.feature). A candidate that DOES
	// serve a certificate is dialed, verified, and taken into the fleet the original way - it is NOT
	// granted a claim (a grant on a member would outlive revoke/forget and let it re-enter).
	if c.Pin == "" {
		if err := edgeAdoptByClaim(c.ID, c.Name); err != nil {
			return err
		}
	} else {
		// A candidate that SERVES a certificate is dialed, verified, and enrolled directly - it does
		// NOT get a claim grant. A leftover grant on a member is a standing re-entry ticket that
		// survives revoke/forget, which is exactly the hole the audit found (2026-09-23).
		if _, err = edgeAdoptCandidate(h.st, c, name, name); err != nil {
			return err
		}
	}
	// AUTO-CLEAR: the instant it is adopted it leaves the DISCOVERED band - it is now adopting (a
	// claim was granted) or already a member, not something to adopt again. It will not be
	// re-offered on the next pass either (runPass drops granted candidates). (edgeAdoptCandidate
	// already removed a serving candidate; this also covers the claim path, idempotently.)
	h.st.candidates = edgeFilter(h.st.candidates, func(x store.EdgeNode) bool { return x.ID != c.ID })
	_ = h.st.save()
	return nil
}

// setPrefer chooses where a turn goes first on this machine - local, market, or local-only
// (features/edge/mode.feature) - the host-side of `roger edge prefer` and the console's Prefer control.
// It validates, persists, and updates the live config so the next turn honors it at once.
func (h *edgeHost) setPrefer(want string) error {
	if !edge.ValidPrefer(want) {
		return fmt.Errorf("prefer takes local, market or local-only; %q is none of them", want)
	}
	cfg := loadConfig()
	cfg.EdgePrefer = want
	if err := saveConfig(cfg); err != nil {
		return err
	}
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
	return nil
}

// grants lists the Edge grants this owner has issued - the console/TUI Grants view.
func (h *edgeHost) grants() ([]store.Grant, error) { return edgeGrantStore().List() }

// issueGrant mints an authority-signed grant from the console/TUI (features/edge/control.feature). It
// is issued only where the root key lives; caller "" or "self" is a Self grant. callee is a node id.
func (h *edgeHost) issueGrant(caller, capability, callee, forDur string) (store.Grant, error) {
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return store.Grant{}, err
	}
	if !ok {
		return store.Grant{}, fmt.Errorf("this machine is not the authority of a locally-rooted Edge; grants are issued where the root key lives")
	}
	callerID := caller
	if caller == "self" || caller == "-" {
		callerID = ""
	}
	if forDur == "" {
		forDur = "30d"
	}
	exp, err := parseExpires(forDur)
	if err != nil {
		// parseExpires is shared with `roger grant` and words its error for --expires; the console's
		// field is "for", so re-word it rather than name a flag the console has no field for.
		return store.Grant{}, fmt.Errorf("bad grant duration %q, want e.g. 7d or 2026-12-31", forDur)
	}
	// A callee must be a node on this Edge, so a typo cannot mint a grant for a node that does not
	// exist; a named caller must be too (a Self grant, callerID=="", is any of the owner's instances).
	if _, ok, err := h.st.fleet.Get(callee); err != nil {
		return store.Grant{}, err
	} else if !ok {
		return store.Grant{}, fmt.Errorf("no such node on this Edge to grant against")
	}
	if callerID != "" {
		if _, ok, err := h.st.fleet.Get(callerID); err != nil {
			return store.Grant{}, err
		} else if !ok {
			return store.Grant{}, fmt.Errorf("the caller is not a node on this Edge")
		}
	}
	return edgeIssueGrant(edgeGrantStore(), local.Account(), local.Authority().RootKey(),
		edgeGrantSpec{caller: callerID, callee: callee, capability: capability, expiresAt: exp}, time.Now())
}

// revokeGrant marks a grant revoked in this authority's store.
func (h *edgeHost) revokeGrant(id string) error {
	found, err := edgeGrantStore().Revoke(id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no such grant %q", id)
	}
	return nil
}

// observeSelf records THIS machine into its OWN fleet record every pass - the household running here
// (each instance's caps, bands and live resources) and a fresh last-seen. Discovery deliberately
// skips self and edgeRecordSelf enrols a bare row once, so without this the authority's own row
// went DARK ("offline 5d") and carried no health, and the console/TUI drew the one machine that is
// certainly up as the deadest node on the Edge. It goes through the same Observe every other node
// goes through: capability states are NOT trusted from the report - they start at their initial
// state and only a probe or the owner's confirmation moves them - so self cannot grant itself
// VERIFIED (the audit's earlier finding stays closed). Requires the face to be up, so the address
// and fingerprint recorded are the ones a peer really dials.
// recordOnNode puts one event on a node's row in THIS host's state, and saves it. A schedule run goes
// through here rather than through a fresh read of the state: the host saves its own copy at the end of
// the same pass, so a separate write is overwritten and the event is lost.
func (h *edgeHost) recordOnNode(nodeID, what, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.st.fleet.RecordEvent(nodeID, what, detail); err != nil {
		return
	}
	_ = h.st.save()
}

// facePort is the port THIS process's LAN face listens on, or 0 before it is up. It goes on the
// process's own registration so a caller can address this roger - node/instance - rather than the
// machine, whose recorded face is whichever roger observed itself last
// (features/edge/maintenance.feature).
func (h *edgeHost) facePort() int {
	h.mu.Lock()
	ln := h.faceLn
	h.mu.Unlock()
	if ln == nil {
		return 0
	}
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

func (h *edgeHost) observeSelf(now time.Time) {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return
	}
	h.mu.Lock()
	ln, srv := h.faceLn, h.srv
	h.mu.Unlock()
	if ln == nil || srv == nil {
		return
	}
	obs := edge.Observation{
		Name: h.enrolledName(), Kind: string(edge.Host),
		Addr: ln.Addr().String(), Fingerprint: srv.Fingerprint(),
		Instances: edge.Household(edgeInstancesDir(), now),
	}
	proven := h.probeOwnServe(id.NodeID, srv, obs.Instances) // before the lock: the probe reads h.mu itself
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.st.fleet.Observe(id.NodeID, obs); err != nil {
		return
	}
	for name, bands := range proven {
		_ = h.st.fleet.RecordServeProbe(id.NodeID, name, bands)
	}
	_ = h.st.save()
}

// probeOwnServe runs the serving probe (features/edge/serving_probe.feature) on this machine's own
// household, through its own face's upstream map - the same check a peer's discovery makes over the
// wire, minus the wire. It returns the instances whose serve was proven on at least one band. An
// instance already VERIFIED for its current bands is not asked again; one whose band this face cannot
// route stays CLAIMED, because a peer's turn could not reach it either. It never holds h.mu: the
// face's Upstream takes that lock.
func (h *edgeHost) probeOwnServe(nodeID string, srv *edge.Server, insts []edge.Instance) map[string][]string {
	h.mu.Lock()
	cur, _, _ := h.st.fleet.Get(nodeID)
	h.mu.Unlock()
	// Which instances are already proven, and for which bands. The bands are compared as a SET: their
	// order is whatever the process listed its models in, and a joined-string key made every pass
	// look like a change, so the probe ran and re-recorded itself forever (caught by the events
	// timeline, which filled with "serve -> VERIFIED" every 30 seconds).
	already := map[string][]string{}
	for _, in := range cur.Instances {
		for _, c := range in.Caps {
			if c.Name == string(edge.Serve) && c.State == string(edge.Verified) {
				if len(in.ServeProven) > 0 {
					already[in.Name] = in.ServeProven
				} else {
					already[in.Name] = in.Bands // a record from before the probe kept its bands
				}
			}
		}
	}
	proven := map[string][]string{}
	for _, in := range insts {
		claims := false
		for _, c := range in.Caps {
			claims = claims || c.Name == string(edge.Serve)
		}
		if !claims || len(in.Bands) == 0 {
			continue
		}
		if was, ok := already[in.Name]; ok && edge.BandsWithin(in.Bands, was) {
			continue // already proven for these bands, or for a set that contains them
		}
		// EVERY band must answer. Proving one and marking the instance would let a roger that
		// advertises two models be routed the one it cannot serve - and it is what makes a SHRINKING
		// band set keep its verdict without a re-probe (features/edge/serving_probe.feature).
		all := true
		for _, b := range in.Bands {
			if got, status, _ := srv.ProbeBand(context.Background(), b); status != 0 || got != in.Name {
				all = false
				break
			}
		}
		if all {
			proven[in.Name] = append([]string(nil), in.Bands...)
		}
	}
	return proven
}

// nameNode renames a node on this Edge, the host-side of the console's Name action and the CLI's
// `roger edge name` (features/edge/manage_web.feature). It enforces the same per-account name
// uniqueness the CLI does - a name that already belongs to another node is refused, so a rename never
// silently collides - and persists through h.st, so the live fleet the TUI and console read updates
// at once.
func (h *edgeHost) nameNode(id, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok, err := h.st.fleet.Get(id); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no such node on this Edge")
	}
	nodes, err := h.st.list()
	if err != nil {
		return err
	}
	for _, other := range nodes {
		if other.Name == name && other.ID != id {
			return fmt.Errorf("the name %q is already taken on this Edge", name)
		}
	}
	if err := h.st.fleet.Rename(id, name); err != nil {
		return err
	}
	return h.st.save()
}

// forgetNode removes a node from this Edge, the host-side of the console's Forget action and the
// CLI's `roger edge forget` (features/edge/manage_web.feature: "never a silent delete"). It takes the
// SAME destructive path as the CLI: when this machine is the authority it revokes the node's
// certificate (so a LAN peer with the list refuses it, offline, after a restart) and evicts its
// enrollment key, then drops it from the fleet. Money (receipts, ledger) is never touched.
func (h *edgeHost) forgetNode(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok, err := h.st.fleet.Get(id); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no such node on this Edge")
	}
	if _, _, err := edgeRevokeOnForget(id); err != nil {
		return err
	}
	if err := h.st.fleet.Forget(id); err != nil {
		return err
	}
	return h.st.save()
}

// enrolledName is what this machine is called on its own Edge, or "" if it has not
// joined one.
func (h *edgeHost) enrolledName() string {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return ""
	}
	if n, found, err := h.st.fleet.Get(id.NodeID); err == nil && found && n.Name != "" {
		return n.Name
	}
	return id.NodeID
}

// arm builds the discovery engine from the environment's knobs, and reports whether there
// is one. ROGERAI_EDGE_DISCOVERY=0 builds NOTHING: no engine, no socket, no goroutine.
// edge.New touches no network, so this is safe on the launch path.
func (h *edgeHost) arm() bool {
	opts := edgeDiscoveryOptions(h.st.fleet)
	if !opts.Config.Enabled {
		return false
	}
	// THIS IS THE PAYOFF OF ENROLLMENT. Until a machine had a certificate it could only
	// browse, because an advertisement it could not back up is noise. Now it serves its
	// own describe endpoint over its own certificate and advertises the fingerprint of
	// it, so the owner's other machines can dial, check, and see it as a member.
	// Start the authority FIRST, so its enrollment/claim port is known and can be advertised on the
	// self record (auth=): a phone that gets adopted then finds where to send its claim from the
	// advertisement alone, no address read to it (features/edge/claim.feature).
	h.startAuthority()
	if self, ok := h.startFace(); ok {
		if h.authLn != nil {
			if tcp, ok := h.authLn.Addr().(*net.TCPAddr); ok {
				self.AuthPort = tcp.Port
			}
		}
		opts.Self = self
	}
	h.registerInstance(h.cfg)
	// The daemon paces its own passes here (see serve), because the host needs each
	// pass's report - to beat the screen, refresh the candidates and write the cache -
	// and the engine's own loop reports to nobody.
	opts.Config.ManualPasses = true
	if opts.Log == nil {
		opts.Log = func(line string) { log.Println(line) }
	}
	h.disc, h.every = edge.New(opts), opts.Config.Interval
	h.mu.Lock()
	h.armed = true
	h.mu.Unlock()
	return true
}

// startFace stands this machine's LAN identity up: a TLS listener whose certificate IS
// its identity, and the advertisement that commits it to that certificate before any
// peer dials. It reports the advertisement, and false when this machine has not
// enrolled and so has nothing true to advertise.
func (h *edgeHost) startFace() (edge.Advert, bool) {
	if h.serving() {
		return edge.Advert{}, false
	}
	id, key, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return edge.Advert{}, false
	}
	srv := edge.NewServer(edge.Describe{
		NodeID: id.NodeID, Account: id.Account, Kind: string(edge.Host),
	}, tls.Certificate{Certificate: [][]byte{id.Cert.Raw}, PrivateKey: key})
	ln, err := net.Listen("tcp", edgeFaceBind())
	if err != nil {
		log.Println("edge: could not open this machine's LAN face:", err)
		return edge.Advert{}, false
	}
	srv.SetHousehold(h.household) // the instances, as of each describe
	if sv, ok := h.peerServing(key, id.NodeID); ok {
		srv.SetServing(sv) // peers may ask this node for a turn, over mutual TLS
	}
	face := &http.Server{Handler: srv.Handler(), TLSConfig: srv.ListenerTLS(),
		ReadHeaderTimeout: 10 * time.Second}
	h.mu.Lock()
	h.faceLn, h.face, h.srv = ln, face, srv
	h.mu.Unlock()
	go func() { _ = face.ServeTLS(ln, "", "") }()
	return srv.Advert(ln.Addr().(*net.TCPAddr).Port), true
}

// serving reports whether this machine's LAN face is already up.
func (h *edgeHost) serving() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.face != nil
}

// edgeAuthAccessLog logs each authority request (method, path, remote) to a FILE - a diagnostic for a
// device that cannot join. Enabled by ROGERAI_EDGE_AUTHLOG=1 (path from ROGERAI_EDGE_AUTHLOG_FILE, or
// a default under the edge dir). It writes to the file directly, NOT the std logger, because the TUI
// captures log output into an in-memory ring where it cannot be read from outside the process.
func edgeAuthAccessLog(next http.Handler) http.Handler {
	path := os.Getenv("ROGERAI_EDGE_AUTHLOG_FILE")
	if path == "" {
		path = filepath.Join(edgeAuthDir(), "access.log")
	}
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			fmt.Fprintf(f, "%s  %s %s from %s\n", time.Now().Format(time.RFC3339), r.Method, r.URL.Path, r.RemoteAddr)
			f.Close()
		}
		mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// startAuthority serves enrollment for the ONE machine the owner designated. It is not
// started anywhere else: a machine that holds no root has nothing to issue with.
func (h *edgeHost) startAuthority() {
	h.mu.Lock()
	already := h.auth != nil
	h.mu.Unlock()
	if already {
		return // re-arming discovery must not bind a second socket
	}
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil || !ok {
		return
	}
	ln, err := net.Listen("tcp", edgeAuthorityBind())
	fellBack := false
	if err != nil {
		// The remembered (or env-pinned) port is momentarily taken - typically the process we just
		// replaced still releasing it. Fall back to an ephemeral port so this run still serves, but do
		// NOT persist it: overwriting the saved port with a transient one would orphan every enrolled
		// device. The next restart tries the saved port again, so the port converges back to stable.
		log.Printf("edge: authority port %s unavailable (%v); using an ephemeral port for this run only", edgeAuthorityBind(), err)
		ln, err = net.Listen("tcp", ":0")
		if err != nil {
			log.Println("edge: could not open the Edge authority's LAN service:", err)
			return
		}
		fellBack = true
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok && !fellBack {
		// Persist ONLY the port we actually preferred (the saved/env one, or the first-ever ephemeral
		// when nothing was saved), so a restart binds the same port a device enrolled against. A
		// fallback port is never persisted (see above).
		edgeSaveAuthorityPort(tcp.Port)
	}
	// The authority server answers BOTH enrollment (issue a certificate) and PRESENCE (a member
	// that dials out to say it is here, so a phone that cannot serve an inbound face still appears -
	// features/edge/presence.feature). Presence writes to the same fleet discovery does.
	authMux := http.NewServeMux()
	// Presence reads the authority FRESH per request (a reload picks up revoked.json a CLI forget
	// wrote beside us), so a forgotten node stops verifying at once, not at the next restart.
	authDir := edgeAuthDir()
	authMux.Handle(edge.PresencePath, edge.PresenceHandlerFunc(h.st.fleet, func() *cert.Authority {
		fresh, ok, err := edgeauth.OpenLocal(authDir)
		if err != nil || !ok {
			return nil
		}
		return fresh.Authority()
	}, nil))
	authMux.Handle("/", enrollhttp.Handler(local))
	// Access log for the authority (claim/enroll/present), gated on ROGERAI_EDGE_AUTHLOG=1. It records
	// each request's method, path and remote address so a device that cannot join can be diagnosed:
	// a claim that never arrives (a reachability problem) looks different from one that arrives and is
	// refused. Off by default - it is a diagnostic, not steady-state noise.
	var handler http.Handler = authMux
	if os.Getenv("ROGERAI_EDGE_AUTHLOG") == "1" {
		handler = edgeAuthAccessLog(authMux)
	}
	auth := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	h.mu.Lock()
	h.authLn, h.issuer, h.auth = ln, local.Issuer(), auth
	h.mu.Unlock()
	go func() { _ = auth.Serve(ln) }()
}

// EnvFaceBind and EnvAuthorityBind let an operator pin the ports. Both default to an
// ephemeral port on every interface: the advertisement carries the port, so nothing has
// to agree on a number in advance.
const (
	envFaceBind      = "ROGERAI_EDGE_BIND"
	envAuthorityBind = "ROGERAI_EDGE_AUTHORITY_BIND"
)

func edgeFaceBind() string {
	if v := os.Getenv(envFaceBind); v != "" {
		return v
	}
	return ":0"
}

func edgeAuthorityBind() string {
	if v := os.Getenv(envAuthorityBind); v != "" {
		return v
	}
	// A device presents to the authority at the URL it cached when it enrolled, and it does not
	// re-resolve that URL. So the authority's port MUST be stable across restarts, or every restart
	// silently cuts off every enrolled phone. Reuse the port this Edge last bound; only fall back to an
	// ephemeral one on the very first run (or if that port is taken), and persist whatever we get.
	if p := edgeSavedAuthorityPort(); p > 0 {
		return ":" + strconv.Itoa(p)
	}
	return ":0"
}

// edgeAuthorityPortFile is where this machine's authority port is remembered, beside its root, so a
// restart binds the same port a device enrolled against.
func edgeAuthorityPortFile() string { return filepath.Join(edgeAuthDir(), "authority-port") }

// edgeSavedAuthorityPort reads the remembered authority port, or 0 if none/unreadable.
func edgeSavedAuthorityPort() int {
	b, err := os.ReadFile(edgeAuthorityPortFile())
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || p <= 0 || p > 65535 {
		return 0
	}
	return p
}

// edgeSaveAuthorityPort remembers the authority port so the next restart reuses it. Best-effort: a
// write failure only means the port may change on the next restart, which the fallback already handles.
func edgeSaveAuthorityPort(port int) {
	_ = os.MkdirAll(edgeAuthDir(), 0o755)
	_ = os.WriteFile(edgeAuthorityPortFile(), []byte(strconv.Itoa(port)), 0o644)
}

// authorityAddr is where this machine's Edge authority answers, or "" if it is not one.
func (h *edgeHost) authorityAddr() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.authLn == nil {
		return ""
	}
	return h.authLn.Addr().String()
}

// authorityIssuer is the issuer behind that service, for the surfaces that report on it.
func (h *edgeHost) authorityIssuer() *edgeauth.Issuer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.issuer
}

// running reports whether this host has a discovery engine at all.
func (h *edgeHost) running() bool { return h.disc != nil }

// start puts discovery on its own goroutine and RETURNS. Everything that can be slow -
// opening the multicast plane, the first browse - happens over there, so a machine with a
// hostile network launches exactly as fast as one with none.
func (h *edgeHost) start(ctx context.Context) bool {
	if !h.arm() {
		return false
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	h.mu.Lock()
	h.cancel, h.done = cancel, done
	h.mu.Unlock()
	go func() { defer close(done); h.serve(ctx) }()
	return true
}

// serve brings the engine up and paces the passes. A pass that fails is a pass: it is
// retried on the NEXT interval, never in a tight loop.
func (h *edgeHost) serve(ctx context.Context) {
	// Start never blocks and never fails: an unusable network is one log line from the
	// engine itself, not an error, and the pass is simply retried on the next interval.
	_ = h.disc.Start(ctx)
	defer h.disc.Stop()
	t := time.NewTicker(h.every)
	defer t.Stop()
	for {
		h.runPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runPass is one discovery pass and everything the host does with it: the engine updates
// the fleet itself, the candidates it saw are merged into the list the CLI reads, the
// result is written to the cache, and every VERIFIED sighting becomes a heartbeat.
func (h *edgeHost) runPass(ctx context.Context) edge.Report {
	h.adoptEnrollment(ctx)
	// Snapshot reg/cfg under the lock: registerInstance and deregisterInstance write h.reg while
	// running (a quit can null it), so reading it bare here is a data race (audit 2026-09-24).
	h.mu.Lock()
	reg, cfg := h.reg, h.cfg
	h.mu.Unlock()
	if reg == nil {
		h.registerInstance(cfg) // an enrollment that happened while running
	}
	h.beatInstance(loadConfig())
	h.observeSelf(time.Now()) // this machine's own row: live household, fresh last-seen
	h.judgeHealth(time.Now()) // the authority judges the rules, so a dead node still reports
	// Anything the owner left standing and that is due now (features/edge/schedule.feature). It runs
	// through the ordinary maintenance path, so the target's switch and the grant still decide; a
	// machine with no schedules does nothing at all here.
	edgeRunSchedule(cfg, time.Now(), h.recordOnNode)
	rep := h.disc.RunOnce(ctx)
	h.mu.Lock()
	h.st.adoptExternalEdits() // before merging discovery in: a candidate adopted elsewhere is gone
	h.st.candidates = edgeMergeCandidates(h.st.candidates, h.disc.Candidates())
	h.st.candidates = edgeDropGranted(h.st.candidates)             // a candidate we already adopted is adopting, not to-adopt
	h.st.candidates = edgeDropMembers(h.st.candidates, h.st.fleet) // a member re-advertising as a candidate is not to-adopt
	h.lastPass, h.unavailable = time.Now(), rep.Unavailable()
	h.lastFound = edge.PassSummary(len(rep.Verified), len(h.st.candidates))
	err := h.st.save()
	h.mu.Unlock()
	if err != nil {
		log.Println("edge: could not write the last-known Edge:", err)
	}
	// A VERIFIED sighting is a node this instance really heard from, over its own
	// certificate. Nothing else beats: a candidate was not dialed, and a refusal is not
	// a heartbeat.
	for _, s := range rep.Verified {
		h.beat(s.NodeID)
	}
	return rep
}

// adoptEnrollment picks up an enrollment that happened while this process was ALREADY
// running - `roger edge enroll` in another terminal with the TUI open. Without it a
// machine would join its Edge and then stay silent until the next launch, which is
// precisely the "nothing advertises" state enrollment exists to end. It costs one cheap
// file check per pass and does nothing at all on a machine that has not enrolled.
func (h *edgeHost) adoptEnrollment(ctx context.Context) {
	if h.disc == nil || h.serving() {
		return
	}
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return
	}
	// Joining an Edge can move which fleet this machine belongs to - an Edge rooted at
	// a designated machine has an account of its own, not the Core login's. Discovery
	// filters on that account, so it has to be reloaded here or every peer of the Edge
	// this machine just joined would be silently discarded as somebody else's.
	if id.Account != "" && id.Account != h.st.account {
		if st, err := loadEdgeState(); err == nil {
			h.mu.Lock()
			h.st = st
			h.mu.Unlock()
			log.Println("edge: this machine enrolled into", id.Account+
				"; the EDGE screen draws that Edge from the next launch")
		}
	}
	h.disc.Stop()
	if !h.arm() {
		return
	}
	_ = h.disc.Start(ctx)
}

// beat hands the screen one real heartbeat and NEVER blocks. A run with no TUI drains
// nothing; a busy frame drains late. Neither may stall discovery, and a dropped pulse
// costs an animation frame, not a fact.
func (h *edgeHost) beat(id string) {
	select {
	case h.beats <- id:
	default:
	}
}

// stop ends discovery. It is idempotent, and it does not wait forever: a quit that hangs
// on a socket is worse than a pass that never finished.
func (h *edgeHost) stop() {
	h.once.Do(func() {
		// Read cancel/done under the lock (start sets them under it) and mark closing in the same
		// critical section, so a registerInstance in the serve goroutine sees it (audit 2026-09-24).
		h.mu.Lock()
		cancel, done := h.cancel, h.done
		if cancel == nil {
			h.mu.Unlock()
			return // nothing was ever armed, so no serve goroutine can be registering
		}
		h.closing = true
		h.mu.Unlock()
		cancel()
		select {
		case <-done:
			close(h.beats) // no sender is left, so the screen's drain ends cleanly
		case <-time.After(edgeStopGrace):
		}
		h.deregisterInstance()
		h.closeListeners()
	})
}

// closeListeners takes the LAN face and the authority service down. A quit that leaves
// a socket open leaves a machine advertising an identity nothing is behind.
func (h *edgeHost) closeListeners() {
	h.mu.Lock()
	face, auth := h.face, h.auth
	h.face, h.auth = nil, nil
	h.mu.Unlock()
	for _, srv := range []*http.Server{face, auth} {
		if srv != nil {
			_ = srv.Close()
		}
	}
}

// startEdge gives the TUI this machine's ONE Edge - the same fleet, cache and candidates
// `roger edge` reads - and starts LAN discovery behind it. It returns the stop the launch
// path defers.
func startEdge(hooks *tui.Hooks) func() {
	// The log ring is teed on before anything else, so the first lines of the run are in it too
	// (features/edge/logs.feature). It is a TEE: the lines still reach wherever they went before.
	untee := edgeTeeLog()
	h, err := newEdgeHost(hooks.Station)
	if err != nil {
		// An Edge that could not be READ is not an empty Edge. Say so once and leave the
		// screen honestly unwired rather than drawing a fleet of none.
		log.Println("edge: could not read this machine's Edge:", err, "- the EDGE screen is unwired this run")
		return untee
	}
	h.wire(hooks)
	_ = h.start(context.Background())
	return func() {
		h.stop()
		untee()
	}
}

// judgeHealth evaluates the alert rules against the fleet each pass and records the outcome.
// Only the AUTHORITY judges (health.feature §4): an offline alert is fired from the MISSING
// heartbeat here, never by the node that stopped sending - and not by every member either, or
// one crossing would be N alerts the moment a sink is configured.
func (h *edgeHost) judgeHealth(now time.Time) {
	h.mu.Lock()
	authority := h.authLn != nil
	var hs []edge.NodeHealth
	var err error
	if authority {
		hs, err = edge.FleetHealth(h.st.fleet, now)
	}
	h.mu.Unlock()
	if !authority || err != nil {
		return
	}
	if _, alerts, err := edgeEvaluateAlerts(hs, now); err != nil {
		log.Println("edge: could not record the fleet's alerts:", err)
	} else {
		for _, a := range alerts {
			log.Println("edge alert:", a)
		}
	}
}
