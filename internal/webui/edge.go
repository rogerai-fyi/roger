package webui

// THE EDGE TAB - "what is on my Edge, and how is it connected?" in a browser.
//
// This is the console's window on the SAME Edge the TUI's [3] EDGE screen draws: the same
// fleet, the same candidate list, the same adopt path and the same session ledger, handed
// in through EdgeHooks exactly as the TUI's Hooks receive them. The arrangement (which
// relay a node is drawn under) and the session words come from internal/edge/view.go, so
// the terminal and the browser cannot disagree about a path.
//
// THE RULE THE WHOLE TAB OBEYS: it never claims a link it has not seen. The read below is
// ONE snapshot - fleet, candidates and sessions read once, stamped with one clock - so a
// frame can never show an edge to a node it does not list. The browser starts a pulse
// only when a node's last-seen advanced between two of these reads, which is the only
// heartbeat a browser can see. The tab can look; it cannot dial, and it cannot spend.
//
// Spec: features/edge/console_view.feature.

import (
	"net/http"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// EdgeHooks are the console's seams onto this machine's Edge - the same five the TUI is
// given (tui.Hooks.Edge*). Nil Fleet = this build has no Edge host; the tab says so rather
// than drawing a fleet of none. Nil Adopt = the tab can look but not adopt. Sessions is
// READ here and WRITTEN by the console's own relayed turns (chat.go), because the console
// is an initiator like any other and its traffic belongs on the Edge it came from.
type EdgeHooks struct {
	Self       string
	Fleet      *edge.Fleet
	Candidates func() []store.EdgeNode
	Adopt      func(id, name string) error
	// Name renames a node and Forget removes it - the same host operations the TUI and CLI perform,
	// so the console manages the fleet, it does not just watch it (features/edge/manage_web.feature).
	// Nil leaves that action honestly disabled in the console.
	Name   func(id, name string) error
	Forget func(id string) error
	// SetAgent turns THIS roger's agent role on or off, AddAgent launches a new agent roger on this
	// machine, and SetPrefer chooses where a turn goes first - the same self-management the TUI's [3]
	// panel does (features/edge/agents.feature, mode.feature). AgentActive reports the current role.
	SetAgent    func(on bool) error
	AddAgent    func() error
	AgentActive func() bool
	SetPrefer   func(want string) error
	// SetAgentJob binds a model and/or a job to an agent and ClearAgentJob drops the assignment - the
	// TUI's Bind Model / job actions (features/edge/jobs.feature). Nil leaves them disabled.
	SetAgentJob   func(j edge.AgentJob) error
	ClearAgentJob func(name string) error
	// Grants is the authority-signed grants this owner issued, IssueGrant mints one, RevokeGrant revokes
	// by id - the invoke fabric's grant surface (features/edge/control.feature). Nil = disabled.
	Grants      func() ([]store.Grant, error)
	IssueGrant  func(caller, capability, callee, forDur string) (store.Grant, error)
	RevokeGrant func(id string) error
	Sessions    *edge.Sessions
	// Status is what is true about THIS machine's place on its Edge (enrolled, authority,
	// discovery), for the empty state. Nil = unknown; the panel then names the LAN path only.
	Status func() edge.SelfStatus
	// Household is this machine's running instances (nil = unknown).
	Household func() []edge.Instance
	// Adopting is the nodes the owner has adopted that have not yet claimed a certificate and checked
	// in (shown so a just-adopted phone does not vanish between adopt and membership). PersistentAgents
	// are the agents marked durable (one not in the live household is drawn offline), and AgentJobs are
	// the durable assignments - each agent's bound model and its job. All read-only; nil = unknown.
	Adopting         func() []store.EdgeNode
	PersistentAgents func() []edge.PersistentAgent
	AgentJobs        func() []edge.AgentJob
	// Now is the snapshot clock. Injectable so a test can assert on an age without
	// racing the wall clock; nil means time.Now.
	Now func() time.Time
}

// edgeGraphMax mirrors the TUI's bound: past it the browser draws a list that says so,
// rather than a picture that has stopped being one.
const edgeGraphMax = 24

type edgeCapJSON struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Verify      string `json:"verify,omitempty"` // how an UNVERIFIED capability will be verified
	ConfirmedBy string `json:"confirmed_by,omitempty"`
	ConfirmedAt int64  `json:"confirmed_at,omitempty"`
}

type edgeNodeJSON struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Kind       string                `json:"kind"`
	Caps       []edgeCapJSON         `json:"caps"`
	Transports []store.EdgeTransport `json:"transports"`
	Presence   string                `json:"presence"`
	LastSeen   int64                 `json:"last_seen,omitempty"`
	Age        string                `json:"age,omitempty"`
	Pin        string                `json:"pin,omitempty"`
	History    []store.EdgeEvent     `json:"history"`
	Contract   store.EdgeContract    `json:"contract,omitempty"`
	Station    bool                  `json:"station,omitempty"`
	// The drawing facts, decided here and not in the browser: the browser paints what
	// it is told and infers nothing.
	LAN   bool   `json:"lan"`
	Dark  bool   `json:"dark"`
	Via   string `json:"via,omitempty"`
	Relay bool   `json:"relay"`
	Child bool   `json:"child"`
	// Instances are the rogers running on the node as its face last reported them.
	Instances          []edge.Instance `json:"instances"`
	InstancesTruncated bool            `json:"instances_truncated,omitempty"`
}

type edgeContractJSON struct {
	Class  string   `json:"class,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

type edgeGrantJSON struct {
	ID        string `json:"id"`
	Caller    string `json:"caller"` // node id, or "" for a Self grant (any of the owner's instances)
	Callee    string `json:"callee"` // the node id the grant addresses
	Caps      string `json:"caps"`   // comma-joined capabilities
	ExpiresAt int64  `json:"expires_at"`
	State     string `json:"state"` // live | expired | REVOKED
}

type edgeSessJSON struct {
	Request  string           `json:"request"`
	Kind     string           `json:"kind"`
	Who      string           `json:"who"`
	Band     string           `json:"band"`
	Station  string           `json:"station,omitempty"`
	Left     string           `json:"left,omitempty"`
	Via      string           `json:"via,omitempty"`
	Escalate bool             `json:"escalate"`
	Outcome  string           `json:"outcome"`
	Route    string           `json:"route,omitempty"` // local | market, from the evidence
	Contract edgeContractJSON `json:"contract,omitempty"`
	Count    int              `json:"count"`
	At       int64            `json:"at"`
	Age      string           `json:"age"`
}

// edgeFacts are the empty state's sentences, worded ONCE in internal/edge and printed
// verbatim by the browser, so the console says exactly what the terminal says.
type edgeFacts struct {
	// Root and Prefer are the header's two badges, worded once (mode.feature).
	Root          string   `json:"root"`
	Prefer        string   `json:"prefer"`
	Machine       string   `json:"machine"`
	Authority     []string `json:"authority"`
	Discovery     string   `json:"discovery"`
	EnrollAgainst string   `json:"enroll_against"`
	// Setup points at the terminal wizard when this machine is not yet a member. Empty once
	// enrolled, so the console shows it exactly when the terminal offers `e` (onboard.feature).
	Setup string `json:"setup,omitempty"`
}

type edgeSnap struct {
	Configured bool `json:"configured"`
	// SelfInstances is THIS machine's household: the rogers running here, this one among
	// them. Self is not a row in its own fleet, so its instances ride beside it.
	SelfInstances []edge.Instance  `json:"self_instances"`
	SelfStatus    *edge.SelfStatus `json:"self_status,omitempty"`
	Facts         *edgeFacts       `json:"facts,omitempty"`
	Self          string           `json:"self,omitempty"`
	Account       string           `json:"account,omitempty"`
	At            int64            `json:"at,omitempty"`
	Nodes         []edgeNodeJSON   `json:"nodes"`
	Candidates    []edgeNodeJSON   `json:"candidates"`
	Sessions      []edgeSessJSON   `json:"sessions"`
	TooMany       bool             `json:"too_many"`
	GraphMax      int              `json:"graph_max"`
	// Manage is true when this build wired the fleet-management hooks (Name + Forget), so the console
	// shows those actions on a member exactly when they would work, and never when they would 501.
	Manage bool `json:"manage"`
	// AgentActive is whether THIS roger is running as an agent, and SelfManage whether the self-controls
	// (agent on/off, add agent, prefer) are wired, so the console shows them exactly when they work.
	AgentActive bool `json:"agent_active"`
	SelfManage  bool `json:"self_manage"`
	JobsManage  bool `json:"jobs_manage"`
	// The rest of the fleet state the TUI shows, so the console shows the same: nodes adopted but not
	// yet checked in, the durable agents, and each agent's assignment (bound model + job).
	Adopting         []edgeNodeJSON         `json:"adopting"`
	PersistentAgents []edge.PersistentAgent `json:"persistent_agents"`
	AgentJobs        []edge.AgentJob        `json:"agent_jobs"`
	Grants           []edgeGrantJSON        `json:"grants"`
	GrantsManage     bool                   `json:"grants_manage"`
}

func (s *Server) edgeNow() time.Time {
	if s.opts.Edge.Now != nil {
		return s.opts.Edge.Now()
	}
	return time.Now()
}

func edgeNodeOf(n store.EdgeNode, now time.Time) edgeNodeJSON {
	out := edgeNodeJSON{
		ID: n.ID, Name: n.Name, Kind: n.Kind, Presence: n.Presence, LastSeen: n.LastSeen,
		Pin: n.Pin, Contract: n.Contract, Station: n.Station,
		Caps:               []edgeCapJSON{},
		Transports:         append([]store.EdgeTransport{}, n.Transports...),
		History:            append([]store.EdgeEvent{}, n.History...),
		LAN:                edge.HasLANTransport(n),
		Dark:               n.Presence == string(edge.PresenceDark),
		Instances:          append([]edge.Instance{}, n.Instances...),
		InstancesTruncated: n.InstancesTruncated,
	}
	if n.LastSeen > 0 {
		out.Age = edge.Age(now.Sub(time.Unix(n.LastSeen, 0)))
	}
	for _, c := range n.Caps {
		j := edgeCapJSON{Name: c.Name, State: c.State, ConfirmedBy: c.ConfirmedBy, ConfirmedAt: c.ConfirmedAt}
		if c.State != string(edge.Verified) {
			j.Verify = edge.Capability(c.Name).VerificationMethod()
		}
		out.Caps = append(out.Caps, j)
	}
	return out
}

func edgeSessOf(g edge.SessionGroup, now time.Time) edgeSessJSON {
	x := g.Session
	return edgeSessJSON{
		Request: x.Request, Kind: string(x.Kind), Who: x.Attribution(), Band: x.Band,
		Station: x.Station, Left: x.Left, Via: x.Via, Escalate: x.Escalate,
		Outcome:  x.OutcomeLabel(),
		Route:    x.Where,
		Contract: edgeContractJSON{Class: x.Contract.Class, Labels: x.Contract.Labels},
		Count:    g.Count, At: x.At, Age: edge.Age(now.Sub(time.Unix(x.At, 0))),
	}
}

// edgeGrantOf renders a stored grant for the console - its authorizing facts and current standing,
// never the signature or any secret.
func edgeGrantOf(g store.Grant, now time.Time) edgeGrantJSON {
	callee := ""
	if len(g.Nodes) > 0 {
		callee = g.Nodes[0]
	}
	state := "live"
	switch {
	case g.Revoked:
		state = "REVOKED"
	case g.Expired(now):
		state = "expired"
	}
	return edgeGrantJSON{
		ID: g.ID, Caller: g.Caller, Callee: callee,
		Caps: strings.Join(g.Caps, ","), ExpiresAt: g.ExpiresAt, State: state,
	}
}

// edgeSnapshot is the one read. Everything the tab shows comes from this, taken once.
func (s *Server) edgeSnapshot() (edgeSnap, error) {
	h := s.opts.Edge
	snap := edgeSnap{Nodes: []edgeNodeJSON{}, Candidates: []edgeNodeJSON{}, Sessions: []edgeSessJSON{}, GraphMax: edgeGraphMax, SelfInstances: []edge.Instance{}}
	if h.Fleet == nil {
		return snap, nil
	}
	now := s.edgeNow()
	list, err := h.Fleet.List()
	if err != nil {
		return snap, err
	}
	snap.Configured, snap.Self, snap.Account, snap.At = true, h.Self, h.Fleet.Account(), now.Unix()
	snap.Manage = h.Name != nil && h.Forget != nil
	snap.SelfManage = h.SetAgent != nil && h.AddAgent != nil && h.SetPrefer != nil
	snap.JobsManage = h.SetAgentJob != nil && h.ClearAgentJob != nil
	if h.AgentActive != nil {
		snap.AgentActive = h.AgentActive()
	}
	snap.SelfInstances = []edge.Instance{}
	if h.Household != nil {
		snap.SelfInstances = append(snap.SelfInstances, h.Household()...)
	}
	for _, r := range edge.Arrange(list) {
		n := edgeNodeOf(r.Node, now)
		n.Via, n.Relay, n.Child = r.Via, r.Relay, r.Child
		snap.Nodes = append(snap.Nodes, n)
	}
	snap.TooMany = len(snap.Nodes) > edgeGraphMax
	if h.Status != nil {
		st := h.Status()
		snap.SelfStatus = &st
		if st.Err == "" {
			snap.Facts = &edgeFacts{Root: st.RootBadge(), Prefer: st.PreferBadge(), Machine: st.MachineLine(), Authority: st.AuthorityLines(),
				Discovery: st.DiscoveryLine(now), EnrollAgainst: st.EnrollAgainstLine()}
			if !st.Enrolled {
				snap.Facts.Setup = st.SetupConsoleLine()
			}
		}
	}
	if h.Candidates != nil {
		for _, c := range h.Candidates() {
			snap.Candidates = append(snap.Candidates, edgeNodeOf(c, now))
		}
	}
	snap.Adopting = []edgeNodeJSON{}
	if h.Adopting != nil {
		for _, a := range h.Adopting() {
			snap.Adopting = append(snap.Adopting, edgeNodeOf(a, now))
		}
	}
	snap.PersistentAgents = []edge.PersistentAgent{}
	if h.PersistentAgents != nil {
		snap.PersistentAgents = append(snap.PersistentAgents, h.PersistentAgents()...)
	}
	snap.AgentJobs = []edge.AgentJob{}
	if h.AgentJobs != nil {
		snap.AgentJobs = append(snap.AgentJobs, h.AgentJobs()...)
	}
	snap.Grants = []edgeGrantJSON{}
	snap.GrantsManage = h.IssueGrant != nil && h.RevokeGrant != nil
	if h.Grants != nil {
		gs, gerr := h.Grants()
		if gerr != nil {
			return snap, gerr
		}
		for _, g := range gs {
			snap.Grants = append(snap.Grants, edgeGrantOf(g, now))
		}
	}
	if h.Sessions != nil {
		for _, g := range edge.GroupSessions(h.Sessions.Live()) {
			snap.Sessions = append(snap.Sessions, edgeSessOf(g, now))
		}
	}
	return snap, nil
}

func (s *Server) writeEdge(w http.ResponseWriter) {
	snap, err := s.edgeSnapshot()
	if err != nil {
		// The store's own words, as an error: an Edge that could not be READ is not an
		// empty Edge, and the panel must never draw "the only node" over a failure.
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, snap)
}

// handleEdge is the read (GET only): the snapshot the tab polls while it is shown.
func (s *Server) handleEdge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.writeEdge(w)
}

// handleEdgeAdopt is the owner's click on a candidate. POST-only and token-gated via
// s.action; it takes the SAME path as the TUI's `a` and `roger edge adopt` (the hook
// checks the certificate before anything is enrolled), and returns the fresh snapshot so
// the tab shows what it just did.
func (s *Server) handleEdgeAdopt(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.Fleet == nil || h.Adopt == nil {
		http.Error(w, "this build cannot adopt - no fleet is wired to the Edge tab", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decode(r, &req) || req.ID == "" {
		http.Error(w, "candidate id required", http.StatusBadRequest)
		return
	}
	var cand store.EdgeNode
	if h.Candidates != nil {
		for _, c := range h.Candidates() {
			if c.ID == req.ID {
				cand = c
			}
		}
	}
	if cand.ID == "" {
		http.Error(w, "not a candidate on this network", http.StatusNotFound)
		return
	}
	if err := h.Adopt(cand.ID, cand.Name); err != nil {
		http.Error(w, "adopt "+cand.Name+": "+err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgeName renames a node through the host, POST-only and token-gated - the console's Name
// action. It is not a browser-only label: the new name goes through the same Rename the TUI and CLI
// use, with the same per-account uniqueness, and the fresh snapshot returns so the tab shows it.
func (s *Server) handleEdgeName(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.Fleet == nil || h.Name == nil {
		http.Error(w, "this build cannot rename - no fleet is wired to the Edge tab", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !decode(r, &req) || req.ID == "" || req.Name == "" {
		http.Error(w, "node id and name required", http.StatusBadRequest)
		return
	}
	if err := h.Name(req.ID, req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgeForget removes a node through the host, POST-only and token-gated - the console's Forget
// action. It is the SAME destructive path as the CLI (revoke the certificate, evict the key, drop the
// node), never a silent delete; the browser confirms before it calls this (manage_web.feature).
func (s *Server) handleEdgeForget(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.Fleet == nil || h.Forget == nil {
		http.Error(w, "this build cannot forget - no fleet is wired to the Edge tab", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decode(r, &req) || req.ID == "" {
		http.Error(w, "node id required", http.StatusBadRequest)
		return
	}
	if err := h.Forget(req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgeAgent turns THIS roger's agent role on or off through the host - POST-only, token-gated -
// the console's mirror of the TUI's Stop/Start Agent (features/edge/agents.feature).
func (s *Server) handleEdgeAgent(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.SetAgent == nil {
		http.Error(w, "this build cannot change the agent role from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if !decode(r, &req) {
		http.Error(w, "on (bool) required", http.StatusBadRequest)
		return
	}
	if err := h.SetAgent(req.On); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgeAddAgent launches a new agent roger on this machine through the host - the console's Add
// Agent (features/edge/agents.feature). POST-only and token-gated.
func (s *Server) handleEdgeAddAgent(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.AddAgent == nil {
		http.Error(w, "this build cannot add an agent from the console", http.StatusNotImplemented)
		return
	}
	if err := h.AddAgent(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgePrefer chooses where a turn goes first (local | market | local-only) through the host -
// the console's mirror of `roger edge prefer` (features/edge/mode.feature). POST-only and token-gated.
func (s *Server) handleEdgePrefer(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.SetPrefer == nil {
		http.Error(w, "this build cannot set the routing preference from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		Prefer string `json:"prefer"`
	}
	if !decode(r, &req) || req.Prefer == "" {
		http.Error(w, "prefer (local|market|local-only) required", http.StatusBadRequest)
		return
	}
	if err := h.SetPrefer(req.Prefer); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeEdge(w)
}

// handleEdgeJob binds a model and/or a job to an agent through the host - the console's Bind Model /
// job action (features/edge/jobs.feature). POST-only and token-gated. The node defaults to this
// machine host-side, so the browser sends only the agent name and the assignment.
func (s *Server) handleEdgeJob(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.SetAgentJob == nil {
		http.Error(w, "this build cannot assign a job from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		Name    string   `json:"name"`
		Node    string   `json:"node"`
		Model   string   `json:"model"`
		Use     bool     `json:"use"`
		Share   bool     `json:"share"`
		Job     string   `json:"job"`
		Targets []string `json:"targets"`
	}
	if !decode(r, &req) || req.Name == "" {
		http.Error(w, "agent name required", http.StatusBadRequest)
		return
	}
	if err := h.SetAgentJob(edge.AgentJob{
		Name: req.Name, Node: req.Node, Model: req.Model,
		Use: req.Use, Share: req.Share, Job: req.Job, Targets: req.Targets,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgeJobClear drops an agent's assignment through the host. POST-only and token-gated.
func (s *Server) handleEdgeJobClear(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.ClearAgentJob == nil {
		http.Error(w, "this build cannot clear a job from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(r, &req) || req.Name == "" {
		http.Error(w, "agent name required", http.StatusBadRequest)
		return
	}
	if err := h.ClearAgentJob(req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgeGrantIssue mints an authority-signed grant from the console - POST-only, token-gated - the
// invoke fabric's grant surface (features/edge/control.feature). caller may be "self".
func (s *Server) handleEdgeGrantIssue(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.IssueGrant == nil {
		http.Error(w, "this build cannot issue grants from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		Caller     string `json:"caller"`
		Capability string `json:"capability"`
		Callee     string `json:"callee"`
		For        string `json:"for"`
	}
	if !decode(r, &req) || req.Capability == "" || req.Callee == "" {
		http.Error(w, "capability and callee required", http.StatusBadRequest)
		return
	}
	if _, err := h.IssueGrant(req.Caller, req.Capability, req.Callee, req.For); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeEdge(w)
}

// handleEdgeGrantRevoke revokes a grant by id from the console. POST-only and token-gated.
func (s *Server) handleEdgeGrantRevoke(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.RevokeGrant == nil {
		http.Error(w, "this build cannot revoke grants from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decode(r, &req) || req.ID == "" {
		http.Error(w, "grant id required", http.StatusBadRequest)
		return
	}
	if err := h.RevokeGrant(req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// recordConsoleSession puts the console's own relayed turn on the Edge it came from, as
// the receipt describes it. The console is an initiator like `roger use` or a guest:
// leaving its traffic off the Edge would draw a quiet fleet over a busy one. A turn with
// no receipt records nothing - a session is a request the relay receipted, never a claim.
func (s *Server) recordConsoleSession(rec protocol.UsageReceipt) {
	led := s.opts.Edge.Sessions
	if led == nil || rec.RequestID == "" {
		return
	}
	_, _ = led.Record(edge.Traffic{
		Account: led.Account(), Kind: edge.FromConsole, Request: rec.RequestID,
		Receipts: []protocol.UsageReceipt{rec},
	})
}
