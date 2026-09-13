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
	Sessions   *edge.Sessions
	// Status is what is true about THIS machine's place on its Edge (enrolled, authority,
	// discovery), for the empty state. Nil = unknown; the panel then names the LAN path only.
	Status func() edge.SelfStatus
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
}

type edgeContractJSON struct {
	Class  string   `json:"class,omitempty"`
	Labels []string `json:"labels,omitempty"`
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
	Contract edgeContractJSON `json:"contract,omitempty"`
	Count    int              `json:"count"`
	At       int64            `json:"at"`
	Age      string           `json:"age"`
}

// edgeFacts are the empty state's sentences, worded ONCE in internal/edge and printed
// verbatim by the browser, so the console says exactly what the terminal says.
type edgeFacts struct {
	Machine       string   `json:"machine"`
	Authority     []string `json:"authority"`
	Discovery     string   `json:"discovery"`
	EnrollAgainst string   `json:"enroll_against"`
}

type edgeSnap struct {
	Configured bool             `json:"configured"`
	SelfStatus *edge.SelfStatus `json:"self_status,omitempty"`
	Facts      *edgeFacts       `json:"facts,omitempty"`
	Self       string           `json:"self,omitempty"`
	Account    string           `json:"account,omitempty"`
	At         int64            `json:"at,omitempty"`
	Nodes      []edgeNodeJSON   `json:"nodes"`
	Candidates []edgeNodeJSON   `json:"candidates"`
	Sessions   []edgeSessJSON   `json:"sessions"`
	TooMany    bool             `json:"too_many"`
	GraphMax   int              `json:"graph_max"`
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
		Caps:       []edgeCapJSON{},
		Transports: append([]store.EdgeTransport{}, n.Transports...),
		History:    append([]store.EdgeEvent{}, n.History...),
		LAN:        edge.HasLANTransport(n),
		Dark:       n.Presence == string(edge.PresenceDark),
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
		Contract: edgeContractJSON{Class: x.Contract.Class, Labels: x.Contract.Labels},
		Count:    g.Count, At: x.At, Age: edge.Age(now.Sub(time.Unix(x.At, 0))),
	}
}

// edgeSnapshot is the one read. Everything the tab shows comes from this, taken once.
func (s *Server) edgeSnapshot() (edgeSnap, error) {
	h := s.opts.Edge
	snap := edgeSnap{Nodes: []edgeNodeJSON{}, Candidates: []edgeNodeJSON{}, Sessions: []edgeSessJSON{}, GraphMax: edgeGraphMax}
	if h.Fleet == nil {
		return snap, nil
	}
	now := s.edgeNow()
	list, err := h.Fleet.List()
	if err != nil {
		return snap, err
	}
	snap.Configured, snap.Self, snap.Account, snap.At = true, h.Self, h.Fleet.Account(), now.Unix()
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
			snap.Facts = &edgeFacts{Machine: st.MachineLine(), Authority: st.AuthorityLines(),
				Discovery: st.DiscoveryLine(now), EnrollAgainst: st.EnrollAgainstLine()}
		}
	}
	if h.Candidates != nil {
		for _, c := range h.Candidates() {
			snap.Candidates = append(snap.Candidates, edgeNodeOf(c, now))
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
