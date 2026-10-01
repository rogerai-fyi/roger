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
	"encoding/json"
	"net/http"
	"sort"
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
	// ConfirmActuate is the owner saying yes to a node that can change the physical world
	// (features/edge/node_identity.feature). The console opens no new authority with it: it is the
	// same host operation the terminal's `roger edge actuate` performs, and the HOST records who
	// confirmed - the browser is never asked and never believed about that. Nil = disabled.
	ConfirmActuate func(id string) error
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
	// Alerts is the rules firing right now by node id, each as its wording ("disk > 90") - the
	// console's share of health.feature §5 (alerts show on the fleet surfaces). Nil = unknown.
	Alerts func() map[string][]string
	// Rules is the alert rules in force, SetRule stores one from the owner's WORDS AS TYPED (the same
	// core `roger edge alert set` uses, so the browser and the terminal cannot derive a different rule
	// id or resolve one scope two ways) and answers with the host's own sentence, RuleOff turns a rule
	// off or back on, RemoveRule drops it. The console could show WHICH rules were firing and not say
	// what fires; this is that half (features/edge/health.feature, features/edge/manage_web.feature
	// §3). Nil leaves the list empty and the actions honestly disabled.
	Rules      func() ([]edge.Rule, error)
	SetRule    func(metric, op, value, scope string) (string, error)
	RuleOff    func(id string, off bool) error
	RemoveRule func(id string) error
	// Sinks is where an alert goes when the owner wants it to leave this machine, WITHOUT their
	// secrets: a webhook URL is a bearer credential and the browser is never told one, not even one
	// it supplied a moment ago. AddSink takes the URL the owner typed, RemoveSink drops one by id,
	// TestSink proves one works. Nil = the console says so and draws nothing.
	Sinks      func() ([]edge.Sink, error)
	AddSink    func(kind, url string) (edge.Sink, error)
	RemoveSink func(id string) (bool, error)
	TestSink   func(id string) (edge.Delivery, error)
	// Maintain performs ONE maintenance action on a member - start, stop or restart a persistent agent,
	// or read its redacted log tail - through the SAME invoke `roger edge maintain` and `roger edge
	// logs` take (features/edge/maintenance.feature, features/edge/logs.feature). Both locks still
	// apply and neither is re-implemented here: maintenance must be on at THAT machine, and the caller
	// must hold an authority-signed grant carrying maintain. The console opens no new path and adds not
	// one line of authorization of its own. Nil = disabled.
	Maintain func(node string, t edge.MaintainTask) (edge.MaintainResult, error)
	// ServeModel asks a member to serve a model id under a serve-control grant
	// (features/edge/remote_model.feature): the same request as `roger model serve <id> --on <node>`.
	// It returns the member's own answer. Nil leaves it disabled.
	ServeModel func(node, id string) (string, error)
	// Floor is the factory-floor view of the rack (health.feature §5 and the floor research): the
	// state log's samples, the standing trouble calls, the pile-up rate and the sign-off report;
	// Roger acknowledges a call, StandBy quiets one for a time. Nil = the console shows no floor.
	Floor func(now time.Time) EdgeFloor
	// Events is the merged timeline (features/edge/events.feature); nil means this build wired none and
	// the tab simply has no activity list.
	Events func(now time.Time, limit int) []EdgeEvent
	// Intent is what the owner asked of this Edge and what they left standing; nil means this build
	// wired neither, and the tab simply shows no such sections.
	Intent   func(now time.Time) EdgeFleetIntent
	Roger    func(ruleID, nodeID string) error
	StandBy  func(ruleID, nodeID string, d time.Duration) error
	Sessions *edge.Sessions
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
	ID string `json:"id"`
	// Name is the node's RECORD name and is what the rest of the payload keys off (self, via, the
	// rename form). Label is what to PRINT: a record whose name is its own id was never named by its
	// owner, and a 48-character id is not a name (edge.Label). The console shows Label and sends Name.
	Name       string                `json:"name"`
	Label      string                `json:"label,omitempty"`
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
	// Alerts are the rules firing for this node right now, by wording; decided by the host.
	Alerts []string `json:"alerts"`
	// Software is the inventory of each roger on the node, worded here so the browser draws the same
	// line the TUI does, with the newest roger it is behind (features/edge/inventory.feature).
	Software []edgeSoftwareJSON `json:"software"`
	// Tags are the owner's words for this unit (features/edge/tags.feature).
	Tags []string `json:"tags,omitempty"`
	// Maintain is true when the unit declares maintain - its owner turned maintenance on there, so a
	// member with a grant can act on it (features/edge/maintenance.feature). False says nothing.
	Maintain bool `json:"maintain,omitempty"`
	// Model is what the unit serves of its own and is fetching, worded by edge.ModelLine; ModelHave is
	// the model ids it reported on its disk; ServeControl is whether its owner lets the Edge change it.
	// State is the shared state word with its age (edge.UnitWord): ON AIR 3s, HEARD 9s · no control
	// path, TROUBLE, DARK 2d - the same line the terminal draws.
	State        string   `json:"state,omitempty"`
	Model        string   `json:"model,omitempty"`
	ModelHave    []string `json:"model_have,omitempty"`
	ServeControl bool     `json:"serve_control,omitempty"`
}

// edgeSoftwareJSON is one roger's inventory line on a node card.
type edgeSoftwareJSON struct {
	Instance string `json:"instance"`
	Words    string `json:"words"`
	Behind   string `json:"behind,omitempty"`
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

// edgeRuleJSON is one alert rule as the console lists it: the words the terminal prints, the scope
// in the owner's own terms, and whether it is off. The identity is kept so an action names the rule
// the host knows, never a rebuilt one.
type edgeRuleJSON struct {
	ID      string `json:"id"`
	Words   string `json:"words"` // "cpu_temp > 85", worded once by edge.Rule.Key
	Of      string `json:"of"`    // every node | every node tagged production | shed
	Off     bool   `json:"off,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// edgeScopeWords says a rule's scope the way the owner wrote it: the whole fleet, a group of their
// own words, or one node BY THE NAME THEY GAVE IT - never a 48-character id.
func edgeScopeWords(scope string, names map[string]string) string {
	if scope == "" || scope == "fleet" {
		return "every node"
	}
	if tag, ok := edge.ScopeTag(scope); ok {
		return "every node tagged " + tag
	}
	if n := names[scope]; n != "" {
		return n
	}
	return scope
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
	// Rules is what raises an alarm on this Edge, each in the words the terminal prints, with its
	// scope said in the owner's terms. RulesManage and MaintainManage say whether this build wired
	// those actions, so the browser offers them exactly when they work and never draws a dead button.
	Rules            []edgeRuleJSON `json:"rules"`
	RulesManage      bool           `json:"rules_manage"`
	MaintainManage   bool           `json:"maintain_manage"`
	ServeModelManage bool           `json:"serve_model_manage"`
	// ActuateManage says this build can perform the actuate ceremony. A console that cannot must
	// draw no button for it rather than one that answers 501.
	ActuateManage bool `json:"actuate_manage"`
	// Sinks is where alerts go, each named by kind, host and id and NEVER by URL. SinkManage says
	// this build can change them.
	Sinks      []edgeSinkJSON `json:"sinks,omitempty"`
	SinkManage bool           `json:"sink_manage"`
	// Floor is the ANDON and TROUBLE facts the TUI shows; FloorManage whether roger/stand by are wired.
	Floor *edgeFloorJSON `json:"floor,omitempty"`
	// Events is the timeline the terminal's EVENTS tab shows (features/edge/events.feature), worded on
	// this side so the browser draws what it is told.
	Events []edgeEventJSON `json:"events"`
	// Expect and Drift are what the owner asked of this Edge and where it does not hold
	// (features/edge/drift.feature); Schedule is what they left standing (schedule.feature).
	Expect      []edgeExpectJSON   `json:"expect"`
	Drift       []edgeDriftJSON    `json:"drift"`
	Schedule    []edgeScheduleJSON `json:"schedule"`
	FloorManage bool               `json:"floor_manage"`
}

// EdgeFloor is what the host hands the console for the floor: raw facts, worded here.
type EdgeFloor struct {
	Samples []edge.StateSample
	Calls   []edge.TroubleCall
	Rate    int
	SignOff edge.SignOffReport
	Reads   map[string]int64 // node id -> when its Roger app last read the trouble feed
}

// edgeExpectJSON is one thing the owner expects, and how many units differ from it.
type edgeExpectJSON struct {
	Expected string `json:"expected"`
	Of       string `json:"of"`
	Differ   int    `json:"differ,omitempty"`
}

// edgeDriftJSON is one unit measured against one expectation.
type edgeDriftJSON struct {
	Node     string `json:"node"`
	Expected string `json:"expected"`
	Found    string `json:"found"`
	Unknown  bool   `json:"unknown,omitempty"` // the unit has not reported the fact this needs
	Dark     bool   `json:"dark,omitempty"`
}

// edgeScheduleJSON is one standing job and its newest outcomes.
type edgeScheduleJSON struct {
	ID      string   `json:"id"`
	What    string   `json:"what"`
	Of      string   `json:"of"`
	Cadence string   `json:"cadence"`
	Last    string   `json:"last"`
	Next    string   `json:"next"`
	Runs    []string `json:"runs,omitempty"`
}

// EdgeFleetIntent is what the host hands over for those, already worded: the browser draws what it is
// told, as it does for everything else on this tab.
type EdgeFleetIntent struct {
	Expect   []EdgeExpectLine
	Drift    []EdgeDriftLine
	Schedule []EdgeScheduleLine
}

// EdgeExpectLine is one expectation and how many units differ from it.
type EdgeExpectLine struct {
	Expected string
	Of       string
	Differ   int
}

// EdgeDriftLine is one unit measured against one expectation.
type EdgeDriftLine struct {
	Node     string
	Expected string
	Found    string
	Unknown  bool
	Dark     bool
}

// EdgeScheduleLine is one standing job.
type EdgeScheduleLine struct {
	ID      string
	What    string
	Of      string
	Cadence string
	Last    string
	Next    string
	Runs    []string
}

// edgeEventJSON is one line of the timeline: already worded, with its age.
type edgeEventJSON struct {
	Age    string `json:"age"`
	Node   string `json:"node"`
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
	Alarm  bool   `json:"alarm,omitempty"` // a crossing in the alert log, not a node event
}

// EdgeEvent is one timeline line as the host hands it over, already worded.
type EdgeEvent struct {
	Age    string
	Node   string
	What   string
	Detail string
	Alarm  bool
}

// edgeConsoleEvents is how many lines the tab shows: enough to read what happened, few enough that
// the snapshot stays small on every poll.
const edgeConsoleEvents = 30

type edgeFloorJSON struct {
	Units   []edgeUnitJSON `json:"units"`
	Calls   []edgeCallJSON `json:"calls"`
	Rate    int            `json:"rate"`
	SignOff edgeSignOff    `json:"sign_off"`
}

// edgeUnitJSON is one unit as the andon tile draws it: the state in words, how long, airtime over
// the last hour, the log strip (60 cells, oldest first, "" = not judged), what fires on it.
type edgeUnitJSON struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Label    string   `json:"label,omitempty"` // what to print; see edgeNodeJSON.Label
	Kind     string   `json:"kind"`
	State    string   `json:"state"` // ON AIR | TROUBLE | DARK
	SinceS   int64    `json:"since_s"`
	Capped   bool     `json:"capped,omitempty"` // the log began inside the run: since is a floor
	Airtime  int      `json:"airtime"`
	NoLog    bool     `json:"no_log,omitempty"` // nothing judged yet: airtime means nothing
	Log      []string `json:"log"`
	Alerts   []string `json:"alerts"`
	LastSeen int64    `json:"last_seen,omitempty"`
	LastRead int64    `json:"last_read,omitempty"` // when its Roger app last read the feed: it hears the Edge
}

type edgeCallJSON struct {
	Rule     string `json:"rule"`
	NodeID   string `json:"node_id"`
	Node     string `json:"node"`
	Metric   string `json:"metric"`
	When     string `json:"when"`
	Value    string `json:"value"`
	Priority string `json:"priority"`
	SinceS   int64  `json:"since_s"`
	Stale    bool   `json:"stale,omitempty"`
	Acked    bool   `json:"acked"`
	AckBy    string `json:"ack_by,omitempty"`
	Shelved  bool   `json:"shelved,omitempty"`
	UntilAt  int64  `json:"until_at,omitempty"`
	StandBy  string `json:"standby_by,omitempty"` // who quieted it
	Why      string `json:"why"`
	Do       string `json:"do"`
}

type edgeSignOff struct {
	Raised       int           `json:"raised"`
	Cleared      int           `json:"cleared"`
	Loudest      string        `json:"loudest,omitempty"`
	LoudestCount int           `json:"loudest_count,omitempty"`
	DeadAir      []edgeDeadAir `json:"dead_air"`
}

type edgeDeadAir struct {
	Node string `json:"node"`
	ForS int64  `json:"for_s"`
}

func (s *Server) edgeNow() time.Time {
	if s.opts.Edge.Now != nil {
		return s.opts.Edge.Now()
	}
	return time.Now()
}

// edgeLabelIfDifferent is the printable label ONLY when it differs from the record name. An
// ordinary node needs no second copy of its name in every payload, and the browser already falls
// back to the name when the label is absent.
func edgeLabelIfDifferent(n store.EdgeNode, all []store.EdgeNode) string {
	if l := edge.UnitName(n, all); l != n.Name {
		return l
	}
	return ""
}

func edgeNodeOf(n store.EdgeNode, now time.Time, all []store.EdgeNode) edgeNodeJSON {
	out := edgeNodeJSON{
		ID: n.ID, Name: n.Name, Label: edgeLabelIfDifferent(n, all), Kind: n.Kind, Presence: n.Presence, LastSeen: n.LastSeen,
		Pin: n.Pin, Contract: n.Contract, Station: n.Station, Tags: append([]string(nil), n.Tags...),
		Caps:               []edgeCapJSON{},
		Transports:         append([]store.EdgeTransport{}, n.Transports...),
		History:            append([]store.EdgeEvent{}, n.History...),
		LAN:                edge.HasLANTransport(n),
		Dark:               n.Presence == string(edge.PresenceDark),
		Instances:          append([]edge.Instance{}, n.Instances...),
		InstancesTruncated: n.InstancesTruncated,
		Alerts:             []string{},
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
	snap.ActuateManage = h.ConfirmActuate != nil
	snap.SinkManage = h.AddSink != nil && h.RemoveSink != nil
	if h.Sinks != nil {
		if list, err := h.Sinks(); err == nil {
			snap.Sinks = []edgeSinkJSON{}
			for _, sk := range list {
				// Kind, host and id. NOT the URL: the field does not even exist on this struct, so
				// there is no line anybody can add later that leaks it by forgetting.
				snap.Sinks = append(snap.Sinks, edgeSinkJSON{ID: sk.ID, Kind: string(sk.Kind), Host: sk.Host, Words: sk.Words()})
			}
		}
	}
	snap.SelfManage = h.SetAgent != nil && h.AddAgent != nil && h.SetPrefer != nil
	snap.JobsManage = h.SetAgentJob != nil && h.ClearAgentJob != nil
	if h.AgentActive != nil {
		snap.AgentActive = h.AgentActive()
	}
	snap.SelfInstances = []edge.Instance{}
	if h.Household != nil {
		snap.SelfInstances = append(snap.SelfInstances, h.Household()...)
	}
	var alerts map[string][]string
	if h.Alerts != nil {
		alerts = h.Alerts()
	}
	newest := edge.NewestRoger(list)
	for _, r := range edge.Arrange(list) {
		n := edgeNodeOf(r.Node, now, list)
		n.Via, n.Relay, n.Child = r.Via, r.Relay, r.Child
		n.Alerts = append(n.Alerts, alerts[n.ID]...)
		n.Maintain = edge.Declares(r.Node, edge.Maintain)
		n.ServeControl = edge.Declares(r.Node, edge.ServeControl)
		_, n.State = edge.UnitWord(r.Node, len(n.Alerts), now)
		if mm := edge.ModelOf(r.Node); mm != nil {
			n.Model, n.ModelHave = edge.ModelLine(mm), mm.Have
		}
		n.Software = []edgeSoftwareJSON{}
		for _, in := range r.Node.Instances {
			if w := edge.SoftwareWords(in.Software); w != "" {
				n.Software = append(n.Software, edgeSoftwareJSON{Instance: in.Name, Words: w, Behind: edge.Behind(in, newest)})
			}
		}
		snap.Nodes = append(snap.Nodes, n)
	}
	snap.Events = []edgeEventJSON{}
	if h.Events != nil {
		for _, e := range h.Events(now, edgeConsoleEvents) {
			snap.Events = append(snap.Events, edgeEventJSON{Age: e.Age, Node: e.Node, What: e.What,
				Detail: e.Detail, Alarm: e.Alarm})
		}
	}
	snap.Expect, snap.Drift, snap.Schedule = []edgeExpectJSON{}, []edgeDriftJSON{}, []edgeScheduleJSON{}
	if h.Intent != nil {
		in := h.Intent(now)
		for _, e := range in.Expect {
			snap.Expect = append(snap.Expect, edgeExpectJSON{Expected: e.Expected, Of: e.Of, Differ: e.Differ})
		}
		for _, d := range in.Drift {
			snap.Drift = append(snap.Drift, edgeDriftJSON{Node: d.Node, Expected: d.Expected, Found: d.Found,
				Unknown: d.Unknown, Dark: d.Dark})
		}
		for _, j := range in.Schedule {
			snap.Schedule = append(snap.Schedule, edgeScheduleJSON{ID: j.ID, What: j.What, Of: j.Of,
				Cadence: j.Cadence, Last: j.Last, Next: j.Next, Runs: j.Runs})
		}
	}
	if h.Floor != nil {
		snap.Floor = edgeFloorOf(h.Floor(now), list, alerts, now)
		snap.FloorManage = h.Roger != nil && h.StandBy != nil
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
			snap.Candidates = append(snap.Candidates, edgeNodeOf(c, now, append(append([]store.EdgeNode(nil), list...), c)))
		}
	}
	snap.Adopting = []edgeNodeJSON{}
	if h.Adopting != nil {
		for _, a := range h.Adopting() {
			snap.Adopting = append(snap.Adopting, edgeNodeOf(a, now, append(append([]store.EdgeNode(nil), list...), a)))
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
		// A grants read error must not blank the whole Edge tab - the fleet, sessions and controls still
		// matter. Degrade: leave grants empty (the section simply shows none) and draw everything else.
		if gs, gerr := h.Grants(); gerr == nil {
			for _, g := range gs {
				snap.Grants = append(snap.Grants, edgeGrantOf(g, now))
			}
		}
	}
	snap.Rules = []edgeRuleJSON{}
	snap.RulesManage = h.SetRule != nil && h.RuleOff != nil && h.RemoveRule != nil
	snap.MaintainManage = h.Maintain != nil
	snap.ServeModelManage = h.ServeModel != nil
	if h.Rules != nil {
		// A rule read that fails must not blank the tab, exactly as a grants read must not: the fleet,
		// the floor and the sessions still matter. Degrade to no rules and draw everything else.
		if rules, rerr := h.Rules(); rerr == nil {
			names := map[string]string{}
			for id, nm := range edge.UnitNames(list) {
				names[id] = nm
			}
			for _, r := range rules {
				snap.Rules = append(snap.Rules, edgeRuleJSON{ID: r.ID, Words: r.Key(),
					Of: edgeScopeWords(r.Scope, names), Off: r.Off, Default: r.Default})
			}
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

// edgeSinkJSON is one destination as the browser is allowed to know it. THERE IS NO URL FIELD, and
// that is the point: the secret cannot leak through a payload that has nowhere to put it.
type edgeSinkJSON struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Host  string `json:"host,omitempty"`
	Words string `json:"words"`
}

// handleEdgeSink adds, removes or tests a destination for alerts. POST-only and token-gated like
// every other mutation. The URL travels ONE WAY: the browser may send one, and no read ever gives
// one back, so a console that has been left open cannot be walked through to recover a credential.
func (s *Server) handleEdgeSink(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.AddSink == nil || h.RemoveSink == nil || h.TestSink == nil {
		http.Error(w, "this build cannot change where alerts go - run `roger edge sink`", http.StatusNotImplemented)
		return
	}
	var req struct {
		Do   string `json:"do"` // add | rm | test
		Kind string `json:"kind"`
		URL  string `json:"url"`
		ID   string `json:"id"`
	}
	if !decode(r, &req) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch req.Do {
	case "add":
		sk, err := h.AddSink(req.Kind, req.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = sk
	case "rm":
		if req.ID == "" {
			http.Error(w, "sink id required", http.StatusBadRequest)
			return
		}
		had, err := h.RemoveSink(req.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if !had {
			http.Error(w, "no sink has that id", http.StatusNotFound)
			return
		}
	case "test":
		if req.ID == "" {
			http.Error(w, "sink id required", http.StatusBadRequest)
			return
		}
		d, err := h.TestSink(req.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if !d.OK {
			// The outcome is safe to show: a Delivery names the sink by id and host, never by URL.
			http.Error(w, d.Words+" did not take it: "+d.Says, http.StatusBadGateway)
			return
		}
	default:
		http.Error(w, "sink takes add, rm or test", http.StatusBadRequest)
		return
	}
	s.writeEdge(w)
}

// handleEdgeActuate is THE ACTUATE CEREMONY through the host, POST-only and token-gated like every
// other mutation here. Confirming actuate licenses a machine to CHANGE THE PHYSICAL WORLD, so:
//
//   - the browser confirms before it calls this, the way it does for forget;
//   - the request carries the node and NOTHING ELSE - in particular it does not carry who is
//     confirming, because a confirmation the requester can name themselves for is worth nothing.
//     The host takes the operator identity it already holds;
//   - a build with no ceremony wired answers 501 rather than pretending.
//
// handleEdgeServeModel asks a member to serve a model id (features/edge/remote_model.feature). The
// body carries a node, a model id and the owner's yes - nothing that could be an address - and the
// member judges the request itself. POST-only and token-gated like every other mutation.
func (s *Server) handleEdgeServeModel(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.ServeModel == nil {
		http.Error(w, "this build cannot ask a member to serve - run `roger model serve <id> --on <node>`", http.StatusNotImplemented)
		return
	}
	var req struct {
		confirmed
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	if !decode(r, &req) || req.ID == "" || req.Model == "" {
		http.Error(w, "node id and model id required", http.StatusBadRequest)
		return
	}
	if !req.Confirm {
		http.Error(w, "changing what a member serves needs the owner's yes", http.StatusPreconditionRequired)
		return
	}
	says, err := h.ServeModel(req.ID, req.Model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"says": says})
}

func (s *Server) handleEdgeActuate(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.Fleet == nil || h.ConfirmActuate == nil {
		http.Error(w, "this build cannot confirm actuate - run `roger edge actuate <node>`", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decode(r, &req) || req.ID == "" {
		http.Error(w, "node id required", http.StatusBadRequest)
		return
	}
	if err := h.ConfirmActuate(req.ID); err != nil {
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
		On *bool `json:"on"`
	}
	if !decode(r, &req) || req.On == nil { // an empty body must not read as "off"
		http.Error(w, "on (bool) required", http.StatusBadRequest)
		return
	}
	if err := h.SetAgent(*req.On); err != nil {
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
		Name: req.Name, Model: req.Model, // the node is this machine's: the host fills it in, never the browser
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

// handleEdgeTags is the console's label action (features/edge/tags.feature): +word adds, -word
// removes, a bare list replaces, clear empties. POST-only and token-gated. It goes through the SAME
// Fleet the TUI and CLI edit, so the word validation, the twelve-tag bound, the sort and the history
// line are one implementation, not a second one written in a browser. A tag names and groups: it is
// never a capability, never a permission and never a grant, so this needs no hook beyond the fleet.
func (s *Server) handleEdgeTags(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.Fleet == nil {
		http.Error(w, "this build has no fleet to tag - no Edge is wired to the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID     string   `json:"id"`
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
		Set    []string `json:"set"`
		Clear  bool     `json:"clear"`
	}
	if !decode(r, &req) || req.ID == "" {
		http.Error(w, "node id required", http.StatusBadRequest)
		return
	}
	edits, replaces := len(req.Add)+len(req.Remove) > 0, len(req.Set) > 0 || req.Clear
	switch {
	case edits && replaces:
		// "gpu -bench" could mean either, so it is refused rather than guessed at, as the CLI refuses it.
		http.Error(w, "say either what to add and remove, or the whole list to keep - not both", http.StatusBadRequest)
		return
	case !edits && !replaces:
		http.Error(w, "say what to add, what to remove, the list to keep, or clear", http.StatusBadRequest)
		return
	case req.Clear && len(req.Set) > 0:
		http.Error(w, "clear takes no tags; it empties them", http.StatusBadRequest)
		return
	}
	var err error
	if replaces {
		err = h.Fleet.SetTags(req.ID, req.Set) // clear is the empty list
	} else {
		err = h.Fleet.EditTags(req.ID, req.Add, req.Remove)
	}
	if err != nil {
		// The fleet's own words, and its own refusal: an invalid word changes nothing.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeEdge(w)
}

// handleEdgeRule stores an alert rule from the owner's words AS TYPED - POST-only and token-gated.
// The metric, the comparison, the value and the scope go to the host unchanged, because the rule id
// and the scope resolution are derived once, where `roger edge alert set` derives them; a second
// derivation here is how the browser and the terminal would come to hold two different rules.
func (s *Server) handleEdgeRule(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.SetRule == nil {
		http.Error(w, "this build cannot set an alert rule from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		Metric string `json:"metric"`
		Op     string `json:"op"`
		Value  string `json:"value"`
		Scope  string `json:"scope"`
	}
	if !decode(r, &req) || req.Metric == "" || req.Op == "" || req.Value == "" {
		http.Error(w, "a rule is a metric, a comparison (gt|lt) and a value", http.StatusBadRequest)
		return
	}
	says, err := h.SetRule(req.Metric, req.Op, req.Value, req.Scope)
	if err != nil {
		// A rule that cannot mean what it says is refused where it is made, in the host's words.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	snap, serr := s.edgeSnapshot()
	if serr != nil {
		http.Error(w, serr.Error(), http.StatusBadGateway)
		return
	}
	// The host's own sentence rides back with the fresh snapshot, so the browser says what the
	// terminal says instead of composing a second version of it.
	writeJSON(w, struct {
		Says string   `json:"says"`
		Edge edgeSnap `json:"edge"`
	}{says, snap})
}

// handleEdgeRuleOff turns one rule off or back on through the host. POST-only and token-gated.
func (s *Server) handleEdgeRuleOff(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.RuleOff == nil {
		http.Error(w, "this build cannot turn an alert rule off from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID  string `json:"id"`
		Off *bool  `json:"off"`
	}
	if !decode(r, &req) || req.ID == "" || req.Off == nil { // an empty body must not read as "on"
		http.Error(w, "rule id and off (bool) required", http.StatusBadRequest)
		return
	}
	if err := h.RuleOff(req.ID, *req.Off); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgeRuleRemove drops a rule by id through the host. POST-only and token-gated.
func (s *Server) handleEdgeRuleRemove(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.RemoveRule == nil {
		http.Error(w, "this build cannot remove an alert rule from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decode(r, &req) || req.ID == "" {
		http.Error(w, "rule id required", http.StatusBadRequest)
		return
	}
	if err := h.RemoveRule(req.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgeMaintain performs one maintenance action on a member - start, stop or restart a
// persistent agent, or read its redacted log tail - POST-only and token-gated.
//
// It adds NO authorization of its own. The task is read by edge.ParseMaintainTask, the same parser
// the callee uses, so an action that is not built is refused BY NAME and an action that acts on an
// agent must name a usable one before anything leaves this machine; everything after that - the
// owner's maintenance switch on that machine, the authority-signed grant carrying maintain, the
// mutual TLS, the revocation, the replay cache - is the invoke path's, unchanged.
func (s *Server) handleEdgeMaintain(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.Maintain == nil {
		http.Error(w, "this build cannot perform a maintenance action from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		Node   string `json:"node"`
		Action string `json:"action"`
		Target string `json:"target"`
		Lines  int    `json:"lines"`
	}
	if !decode(r, &req) || req.Node == "" {
		http.Error(w, "the node to maintain is required", http.StatusBadRequest)
		return
	}
	raw, err := json.Marshal(edge.MaintainTask{Action: edge.MaintainAction(req.Action), Target: req.Target, Lines: req.Lines})
	if err != nil {
		http.Error(w, "that maintenance task could not be read", http.StatusBadRequest)
		return
	}
	task, why := edge.ParseMaintainTask(raw)
	if why != "" {
		http.Error(w, why, http.StatusBadRequest)
		return
	}
	res, err := h.Maintain(req.Node, task)
	if err != nil {
		// The fleet's own words: the missing grant, the owner's switch, a node with no LAN face.
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, res)
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

// edgeFloorOf words the floor: the state and time in state per unit from the log (a dark unit's
// last-seen knows better than a log that began after it went dark), airtime as availability
// over the last hour, calls priority-then-oldest with why and do, the sign-off line.
func edgeFloorOf(f EdgeFloor, nodes []store.EdgeNode, alerts map[string][]string, now time.Time) *edgeFloorJSON {
	out := &edgeFloorJSON{Units: []edgeUnitJSON{}, Calls: []edgeCallJSON{}, SignOff: edgeSignOff{DeadAir: []edgeDeadAir{}}}
	names := map[string]string{}
	for _, n := range nodes {
		names[n.ID] = edge.UnitName(n, nodes)
		cells, since, capped := edge.LogOf(f.Samples, n.ID, now)
		u := edgeUnitJSON{ID: n.ID, Name: n.Name, Label: edgeLabelIfDifferent(n, nodes), Kind: n.Kind,
			Log: make([]string, len(cells)), Alerts: alerts[n.ID], LastSeen: n.LastSeen}
		if u.Alerts == nil {
			u.Alerts = []string{}
		}
		air, known := 0, 0
		for i, c := range cells {
			u.Log[i] = string(c)
			if c != edge.StateUnknown {
				known++
				if c != edge.StateDark {
					air++
				}
			}
		}
		if known > 0 {
			u.Airtime = int(float64(air)/float64(known)*100 + 0.5)
		} else {
			u.NoLog = true
		}
		dark := n.Presence == string(edge.PresenceDark)
		last := edge.StateUnknown
		if len(cells) > 0 {
			last = cells[len(cells)-1]
		}
		switch {
		case dark || last == edge.StateDark:
			u.State = "DARK"
		case len(u.Alerts) > 0 || last == edge.StateTrouble:
			u.State = "TROUBLE"
		default:
			u.State = "ON AIR"
		}
		if dark && n.LastSeen > 0 && now.Sub(time.Unix(n.LastSeen, 0)) > since {
			since, capped = now.Sub(time.Unix(n.LastSeen, 0)), false
		}
		u.SinceS, u.Capped = int64(since.Seconds()), capped
		u.LastRead = f.Reads[n.ID]
		out.Units = append(out.Units, u)
	}
	calls := append([]edge.TroubleCall(nil), f.Calls...)
	edge.SortCalls(calls)
	for _, c := range calls {
		node := names[c.NodeID]
		if node == "" {
			node = c.Node
		}
		why, do := edge.WhyAndDo(c.Metric)
		j := edgeCallJSON{Rule: c.RuleID, NodeID: c.NodeID, Node: node, Metric: string(c.Metric), When: c.When,
			Value: edge.FormatValue(c.Metric, c.Value), Priority: edge.Priority(c.Metric), SinceS: int64(c.Since.Seconds()), Stale: c.Stale,
			Acked: c.Acked, AckBy: edge.Handle(c.AckBy), Shelved: c.Shelved, Why: why, Do: do}
		if c.Shelved {
			j.UntilAt, j.StandBy = c.StandByUntil.Unix(), edge.Handle(c.StandByBy)
		}
		out.Calls = append(out.Calls, j)
	}
	out.Rate = f.Rate
	out.SignOff = edgeSignOff{Raised: f.SignOff.Raised, Cleared: f.SignOff.Cleared, Loudest: f.SignOff.Loudest, LoudestCount: f.SignOff.LoudestCount, DeadAir: []edgeDeadAir{}}
	for id, d := range f.SignOff.DeadAir {
		name := names[id]
		if name == "" {
			name = id
		}
		out.SignOff.DeadAir = append(out.SignOff.DeadAir, edgeDeadAir{Node: name, ForS: int64(d.Seconds())})
	}
	sort.Slice(out.SignOff.DeadAir, func(i, j int) bool { return out.SignOff.DeadAir[i].ForS > out.SignOff.DeadAir[j].ForS })
	return out
}

// handleEdgeRoger acknowledges a standing trouble call through the host.
func (s *Server) handleEdgeRoger(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.Roger == nil {
		http.Error(w, "this build cannot acknowledge calls from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		Rule string `json:"rule"`
		Node string `json:"node"`
	}
	if !decode(r, &req) || req.Rule == "" || req.Node == "" {
		http.Error(w, "rule and node required", http.StatusBadRequest)
		return
	}
	if err := h.Roger(req.Rule, req.Node); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}

// handleEdgeStandBy quiets a standing call for a number of hours (one by default, eight at most).
func (s *Server) handleEdgeStandBy(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Edge
	if h.StandBy == nil {
		http.Error(w, "this build cannot put calls on stand by from the console", http.StatusNotImplemented)
		return
	}
	var req struct {
		Rule  string  `json:"rule"`
		Node  string  `json:"node"`
		Hours float64 `json:"hours"`
	}
	if !decode(r, &req) || req.Rule == "" || req.Node == "" {
		http.Error(w, "rule and node required", http.StatusBadRequest)
		return
	}
	if req.Hours <= 0 {
		req.Hours = 1
	}
	if req.Hours > 8 {
		req.Hours = 8
	}
	if err := h.StandBy(req.Rule, req.Node, time.Duration(req.Hours*float64(time.Hour))); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeEdge(w)
}
