package tui

// Executable spec: features/edge/topology_view.feature - the Edge screen ("what is on my
// Edge, and how is it connected?").
//
// REAL dependencies, no mocks:
//   - a REAL internal/edge.Fleet over the REAL internal/store Mem backend. Nodes are
//     enrolled, probed, swept dark and forgotten through the production fleet API, so the
//     screen is drawn from the same records discovery writes.
//   - the REAL TUI model, driven through Update with real key messages, and the REAL
//     renderer at FIXED widths. Assertions are on the plain text of a frame, the way the
//     rest of these suites assert.
//
// The clock is injected (fleet.SetClock + m.edgeClock) rather than slept on: this repo has
// been bitten by wall-clock-racing tests, and a "last seen 6m ago" that depends on how long
// the suite took to run is exactly that bug.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// The glyph vocabulary the screen is specified in, written out LITERALLY here rather than
// imported from the implementation: a spec that reads its answer out of the code under test
// cannot fail when the code changes its mind.
const (
	tSolid  = "─" // a LAN-direct link this instance verified
	tDim    = "┈" // a path through a relay
	tBroken = "╌" // a node whose heartbeat aged out
	tPulse  = "●" // a heartbeat travelling the edge it arrived on
	tSelf   = "▣" // this instance
	tSel    = "›" // the selection carat
)

// ---- fixture -------------------------------------------------------------

type edgeBDD struct {
	t     *testing.T
	db    store.Store
	fleet *edge.Fleet
	cands []store.EdgeNode
	m     model
	now   time.Time
	ids   map[string]string             // owner name -> node id
	priv  map[string]ed25519.PrivateKey // the half that must never be drawn
	// render state
	w       int
	out     string
	frozen  bool // the mid-render scenario: do NOT re-snapshot before drawing
	lastCmd tea.Cmd
	outs    map[string]string // every screen rendered, for the hint sweep
	frameA  string
	frameB  string
	subject string // the node a scenario is talking about
	relay   string
}

func (s *edgeBDD) reset() {
	s.now = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	s.db = store.NewMem()
	s.fleet = edge.NewFleet(s.db, "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.ids = map[string]string{}
	s.priv = map[string]ed25519.PrivateKey{}
	s.cands = nil
	s.frozen = false
	s.outs = map[string]string{}
	s.subject, s.relay = "", ""
	s.w = 0
	s.build()
}

// build makes the model the way the host does: hooks carry the fleet, this instance's name,
// and the candidate list discovery keeps.
func (s *edgeBDD) build() {
	hooks := Hooks{
		EdgeSelf:       "roger-desk",
		EdgeFleet:      s.fleet,
		EdgeCandidates: func() []store.EdgeNode { return s.cands },
		// The real adoption path (what discovery.Adopt does): the owner's explicit action
		// is what turns a candidate into a member.
		EdgeAdopt: func(id, name string) error {
			for _, c := range s.cands {
				if c.ID != id {
					continue
				}
				c.Name, c.Presence = name, string(edge.PresenceVerified)
				_, err := s.fleet.Enroll(c)
				return err
			}
			return fmt.Errorf("no candidate %q was seen on this network", id)
		},
	}
	var tm tea.Model = NewWithHooks("http://broker.local", "tester", nil, hooks)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := tm.(model)
	m.edgeClock = func() time.Time { return s.now }
	s.m = m
}

func lanT(addr string) store.EdgeTransport {
	return store.EdgeTransport{Kind: "lan", Addr: addr, Fingerprint: strings.Repeat("ab", 32)}
}

func relayT(via string) store.EdgeTransport { return store.EdgeTransport{Kind: "relay", Addr: via} }

func (s *edgeBDD) enroll(name string, ts []store.EdgeTransport, caps ...edge.Capability) store.EdgeNode {
	s.t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		s.t.Fatalf("keygen: %v", err)
	}
	n := edge.NewNode(pub, name, edge.Host, caps...)
	n.Transports = ts
	got, err := s.fleet.Enroll(n)
	if err != nil {
		s.t.Fatalf("enroll %q: %v", name, err)
	}
	s.ids[name] = got.ID
	s.priv[name] = priv
	return got
}

// ---- render helpers ------------------------------------------------------

func (s *edgeBDD) open() {
	if s.m.mode != modeEdge {
		s.m.enterEdge()
	}
}

func (s *edgeBDD) render(w int) string {
	s.open()
	if !s.frozen {
		s.m.refreshEdge()
	}
	s.w = w
	s.out = stripANSI(s.m.edgeView(w))
	return s.out
}

func (s *edgeBDD) press(key string) {
	var msg tea.KeyMsg
	switch key {
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	var tm tea.Model = s.m
	tm, s.lastCmd = tm.Update(msg)
	s.m = asModel(tm)
}

func (s *edgeBDD) tick() {
	var tm tea.Model = s.m
	tm, s.lastCmd = tm.Update(edgeAnimMsg{})
	s.m = asModel(tm)
}

func (s *edgeBDD) heartbeat(name string) {
	var tm tea.Model = s.m
	tm, s.lastCmd = tm.Update(edgeHeartbeatMsg{node: s.ids[name]})
	s.m = asModel(tm)
}

// lineFor returns the (single) rendered line naming this node, and its index.
func lineFor(out, name string) (string, int, error) {
	var hit string
	idx, n := -1, 0
	for i, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, name) {
			hit, idx, n = ln, i, n+1
		}
	}
	if n == 0 {
		return "", -1, fmt.Errorf("%q is not drawn:\n%s", name, out)
	}
	if n > 1 {
		return "", -1, fmt.Errorf("%q is drawn %d times, want once:\n%s", name, n, out)
	}
	return hit, idx, nil
}

// nameOccurrences counts the lines naming this node (an elided name is not counted; the
// callers that use this pass short names).
func nameOccurrences(out, name string) int {
	n := 0
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, name) {
			n++
		}
	}
	return n
}

// edgeField is the drawing between the spine and the name: everything before the node's
// name on its row. It is where the solid / dim / broken texture and the pulse live.
func edgeField(line, name string) string {
	if i := strings.Index(line, name); i >= 0 {
		return line[:i]
	}
	return line
}

// layoutOf classifies a frame the way an operator would: a boxed self with a spine is the
// full graph, a spine with no box is the compact graph, and no spine at all is the list.
var spineRe = regexp.MustCompile(`[├└][` + tSolid + tDim + tBroken + ` ]`)

func layoutOf(out string) string {
	box := strings.Contains(out, "╭") && strings.Contains(out, "╮")
	spine := spineRe.MatchString(out)
	switch {
	case box && spine:
		return "graph"
	case spine:
		return "compact graph"
	default:
		return "list"
	}
}

// ---- 1. THE SLOT ---------------------------------------------------------

func (s *edgeBDD) aTUIWithAFleetOfNodes() error {
	// One ordinary member: a host on the LAN that also has a relay path, with one
	// VERIFIED and one CLAIMED capability - enough for the detail scenarios to have
	// something honest to show.
	n := s.enroll("desk-pi", []store.EdgeTransport{lanT("192.168.1.10"), relayT("relay.rogerai.fm")},
		edge.Serve, edge.Classify)
	if err := s.fleet.RecordProbe(n.ID, edge.Classify, true); err != nil {
		return err
	}
	return nil
}

func (s *edgeBDD) mainScreenRendered() error {
	s.out = stripANSI(s.m.View())
	return nil
}

func (s *edgeBDD) slotNamesEdge(slot string) error {
	if !strings.Contains(s.out, slot+" EDGE") {
		return fmt.Errorf("the preset bar does not offer %q EDGE:\n%s", slot, s.out)
	}
	return nil
}

func (s *edgeBDD) slotNamesConfig(slot string) error {
	if !strings.Contains(s.out, slot+" CONFIG") {
		return fmt.Errorf("the preset bar does not offer %q CONFIG:\n%s", slot, s.out)
	}
	return nil
}

func (s *edgeBDD) pressingOpensEdge(key string) error {
	s.build()
	s.press(key)
	if s.m.mode != modeEdge {
		return fmt.Errorf("pressing %q left the TUI in mode %v, want the Edge screen", key, s.m.mode)
	}
	return nil
}

func (s *edgeBDD) pressingOpensConfig(key, old string) error {
	s.build()
	s.press(key)
	if s.m.mode != modeLimits {
		return fmt.Errorf("pressing %q left the TUI in mode %v, want the spend-limit screen", key, s.m.mode)
	}
	// ...and the key that used to open it no longer does.
	s.build()
	s.press(old)
	if s.m.mode == modeLimits {
		return fmt.Errorf("%q still opens the spend-limit screen", old)
	}
	return nil
}

// renderEveryScreen paints every screen that carries a key hint, wide and narrow, expanded
// and compact. It is the only honest way to answer "no hint ANYWHERE".
func (s *edgeBDD) anyScreenRendersAHint() error {
	s.outs = map[string]string{}
	for _, w := range []int{120, 100, 80, 60} {
		for _, compact := range []bool{false, true} {
			for _, md := range []mode{
				modeBrowse, modeCommand, modeChat, modeHelp, modeShare, modeLimits,
				modeAgent, modeLogin, modeEdge, modeLog, modeQuitConfirm, modeOverLimit,
				modeConnectConfirm, modeShareSetup, modeShareEditor, modeBandDetail,
				modeFreqEntry, modePrivate, modeVoiceBooth, modeListeningPost,
			} {
				mm := browseSeed(w)
				mm.compact = compact
				// The screens that only exist with a channel open need one, or they are
				// not screens - they are a nil dereference.
				mm.connected = &offer{NodeID: "demo-node", Model: "gpt-oss-20b", Online: true}
				mm.endpoint = "http://127.0.0.1:8080/v1"
				if len(mm.bands) > 0 {
					// The confirm / over-limit screens describe a QUOTE; without one they
					// are not a screen, they are a nil dereference.
					mm.q = quote{b: mm.bands[0], limit: Limit{MaxOut: 0.1}, typical: 800}
				}
				mm.mode = md
				key := fmt.Sprintf("w=%d compact=%v mode=%v", w, compact, md)
				s.outs[key] = stripANSI(mm.View())
			}
			// The BAND CARD names the screens its sections came from, in words - the
			// one place outside the preset bar that spells "[3] CONFIG".
			mm := browseSeed(w)
			mm.compact = compact
			card, _ := mm.openBandConfig("gpt-oss-20b", modeShare)
			s.outs[fmt.Sprintf("w=%d compact=%v band card", w, compact)] = stripANSI(asModel(card).View())
		}
	}
	return nil
}

func (s *edgeBDD) hintSays(want string) error {
	named := 0
	for key, out := range s.outs {
		if !strings.Contains(out, "CONFIG") {
			continue
		}
		named++
		if !strings.Contains(out, want+" CONFIG") && strings.Contains(out, "] CONFIG") {
			return fmt.Errorf("%s names CONFIG without %q:\n%s", key, want, out)
		}
	}
	if named == 0 {
		return fmt.Errorf("no screen named the config screen at all - the sweep is not looking at anything")
	}
	return nil
}

func (s *edgeBDD) noHintStillSays(stale string) error {
	for key, out := range s.outs {
		if strings.Contains(out, stale) {
			return fmt.Errorf("%s still renders %q:\n%s", key, stale, out)
		}
	}
	return nil
}

func (s *edgeBDD) theEdgeScreenIsOpen() error {
	// Opened from SHARE, so "where the user came from" is a real place and not the default.
	s.m.mode = modeShare
	s.press("3")
	if s.m.mode != modeEdge {
		return fmt.Errorf("3 did not open the Edge screen from SHARE (mode %v)", s.m.mode)
	}
	return nil
}

func (s *edgeBDD) escReturnsWhereTheUserCameFrom() error {
	s.press("esc")
	if s.m.mode != modeShare {
		return fmt.Errorf("esc landed in mode %v, want the screen the user came from (SHARE)", s.m.mode)
	}
	// ...and from the browser it returns to the browser, not to SHARE.
	s.build()
	s.press("3")
	s.press("esc")
	if s.m.mode != modeBrowse {
		return fmt.Errorf("esc from an Edge opened in BROWSE landed in mode %v, want BROWSE", s.m.mode)
	}
	return nil
}

func (s *edgeBDD) leaveEdgeWith(key string) mode {
	s.build()
	s.m.mode = modeShare
	s.press("3")
	s.press(key)
	return s.m.mode
}

func (s *edgeBDD) qDoesWhatItDoesElsewhere() error {
	// On every other numbered screen q LEAVES THE SCREEN - it never quits RogerAI out
	// from under an operator who is two screens deep, and it lands where esc lands.
	viaQ, viaEsc := s.leaveEdgeWith("q"), s.leaveEdgeWith("esc")
	if viaQ == modeEdge {
		return fmt.Errorf("q did not leave the Edge screen")
	}
	if viaQ != viaEsc {
		return fmt.Errorf("q landed in mode %v but esc landed in %v - q is not the exit it is everywhere else", viaQ, viaEsc)
	}
	if s.m.mode == modeQuitConfirm {
		return fmt.Errorf("q started a quit from a numbered screen")
	}
	// The comparison screens, unchanged: q leaves SHARE and leaves CONFIG.
	for _, open := range []string{"2", "4"} {
		s.build()
		s.press(open)
		was := s.m.mode
		s.press("q")
		if s.m.mode == was {
			return fmt.Errorf("q no longer leaves the screen %q opens (mode %v)", open, was)
		}
	}
	return nil
}

// ---- 2. THE GRAPH --------------------------------------------------------

func (s *edgeBDD) theEdgeScreenRenders() error {
	w := s.w
	if w == 0 {
		w = 100
	}
	s.render(w)
	return nil
}

func (s *edgeBDD) selfIsANode() error {
	if !strings.Contains(s.out, "roger-desk") {
		return fmt.Errorf("this instance is not drawn:\n%s", s.out)
	}
	ln, _, err := lineFor(s.out, "roger-desk")
	if err != nil {
		return err
	}
	if !strings.Contains(s.out, "SELF") || !strings.Contains(ln+s.out, tSelf) {
		return fmt.Errorf("this instance is not marked as self:\n%s", s.out)
	}
	return nil
}

func (s *edgeBDD) everyNodeDrawnInRelationToSelf() error {
	list, err := s.fleet.List()
	if err != nil {
		return err
	}
	for _, n := range list {
		ln, _, err := lineFor(s.out, n.Name)
		if err != nil {
			return err
		}
		if !strings.ContainsAny(ln, "├└") {
			return fmt.Errorf("%q is drawn with no edge back to self:\n%s", n.Name, s.out)
		}
	}
	return nil
}

func (s *edgeBDD) nodeVerifiedLANDirect(name string) error {
	s.enroll(name, []store.EdgeTransport{lanT("192.168.1.20")}, edge.Serve)
	s.subject = name
	return nil
}

func (s *edgeBDD) nodeThroughRelay(name, via string) error {
	if _, ok := s.ids[via]; !ok {
		// The relay is itself a member, reached over its relay path (not on this LAN):
		// that is what makes its own edge to self a DIM one.
		s.enroll(via, []store.EdgeTransport{relayT("relay.rogerai.fm")}, edge.Relay)
	}
	s.enroll(name, []store.EdgeTransport{relayT(via)}, edge.Actuate)
	s.subject, s.relay = name, via
	return nil
}

func (s *edgeBDD) joinedToSelfBySolidEdge(name string) error {
	ln, _, err := lineFor(s.out, name)
	if err != nil {
		return err
	}
	field := edgeField(ln, name)
	if !strings.Contains(field, tSolid) {
		return fmt.Errorf("%q has no solid edge to self: %q", name, ln)
	}
	if strings.Contains(field, tDim) || strings.Contains(field, tBroken) {
		return fmt.Errorf("%q's edge is not purely solid: %q", name, ln)
	}
	return nil
}

func (s *edgeBDD) joinedThroughRelayByDimEdges(node, via, via2 string) error {
	nodeLn, nodeAt, err := lineFor(s.out, node)
	if err != nil {
		return err
	}
	relayLn, relayAt, err := lineFor(s.out, via)
	if err != nil {
		return err
	}
	for _, c := range []struct {
		name, line string
	}{{node, nodeLn}, {via2, relayLn}} {
		f := edgeField(c.line, c.name)
		if !strings.Contains(f, tDim) {
			return fmt.Errorf("%q is not joined by a dim edge: %q", c.name, c.line)
		}
		if strings.Contains(f, tSolid) {
			return fmt.Errorf("%q claims a solid edge it has not seen: %q", c.name, c.line)
		}
	}
	// The hop is DRAWN: the relayed node hangs off the relay's row, further from the
	// spine than the relay is.
	if nodeAt <= relayAt {
		return fmt.Errorf("%q is not drawn beneath its relay %q:\n%s", node, via, s.out)
	}
	if strings.IndexAny(nodeLn, "├└") <= strings.IndexAny(relayLn, "├└") {
		return fmt.Errorf("%q is not branched off %q - it hangs straight off the spine:\n%s", node, via, s.out)
	}
	return nil
}

func (s *edgeBDD) relayIsDrawnAsANode(via string) error {
	ln, _, err := lineFor(s.out, via)
	if err != nil {
		return err
	}
	if !strings.Contains(ln, "RELAY") {
		return fmt.Errorf("%q is drawn but not identified as the relay hop: %q", via, ln)
	}
	if !strings.ContainsAny(ln, "├└") {
		return fmt.Errorf("%q is not a node on the graph: %q", via, ln)
	}
	return nil
}

func (s *edgeBDD) nodeReachableBothWays() error {
	s.enroll("dual-pi", []store.EdgeTransport{lanT("192.168.1.30"), relayT("tower-1")}, edge.Sense)
	s.subject = "dual-pi"
	s.render(100)
	return nil
}

func (s *edgeBDD) appearsExactlyOnce() error {
	if n := nameOccurrences(s.out, s.subject); n != 1 {
		return fmt.Errorf("%q appears %d times, want once:\n%s", s.subject, n, s.out)
	}
	return nil
}

func (s *edgeBDD) itsEdgeIsTheSolidLANOne() error {
	return s.joinedToSelfBySolidEdge(s.subject)
}

func (s *edgeBDD) detailListsBothTransports() error {
	if err := s.openDetailOf(s.subject); err != nil {
		return err
	}
	lan, relay := strings.Index(s.out, "lan"), strings.Index(s.out, "relay")
	if lan < 0 || relay < 0 {
		return fmt.Errorf("the detail does not list both transports:\n%s", s.out)
	}
	if lan > relay {
		return fmt.Errorf("the detail lists the relay before the LAN - not preference order:\n%s", s.out)
	}
	return nil
}

func (s *edgeBDD) nodesDeclaringEveryCapability() error {
	n := s.enroll("cap-node", []store.EdgeTransport{lanT("192.168.1.40")},
		edge.Serve, edge.Classify, edge.Sense, edge.Actuate, edge.Relay)
	if err := s.fleet.RecordProbe(n.ID, edge.Serve, true); err != nil {
		return err
	}
	// A second node with the SAME capability VERIFIED, so "claimed vs verified" is a
	// comparison of the same mark in two states rather than of two different marks.
	v := s.enroll("cap-verified", []store.EdgeTransport{lanT("192.168.1.41")}, edge.Classify)
	return s.fleet.RecordProbe(v.ID, edge.Classify, true)
}

// marksOf pulls the bracketed capability cell off a node's row.
var marksRe = regexp.MustCompile(`\[([^\]]*)\]`)

func marksOf(out, name string) (string, error) {
	ln, _, err := lineFor(out, name)
	if err != nil {
		return "", err
	}
	mm := marksRe.FindStringSubmatch(ln)
	if mm == nil {
		return "", fmt.Errorf("%q carries no capability marks: %q", name, ln)
	}
	return mm[1], nil
}

func (s *edgeBDD) distinctMarkPerCapability() error {
	cell, err := marksOf(s.out, "cap-node")
	if err != nil {
		return err
	}
	marks := strings.Fields(cell)
	if len(marks) != 5 {
		return fmt.Errorf("cap-node declares 5 capabilities but carries %d marks (%q)", len(marks), cell)
	}
	seen := map[string]bool{}
	for _, mk := range marks {
		if seen[strings.ToLower(mk)] {
			return fmt.Errorf("two capabilities share the mark %q: %q", mk, cell)
		}
		seen[strings.ToLower(mk)] = true
	}
	return nil
}

func (s *edgeBDD) claimedIsDistinctFromVerified() error {
	claimed, err := marksOf(s.out, "cap-node")
	if err != nil {
		return err
	}
	verified, err := marksOf(s.out, "cap-verified")
	if err != nil {
		return err
	}
	// classify is CLAIMED on one and VERIFIED on the other: the two renderings of the
	// SAME capability must not be the same glyph.
	var c, v string
	for _, mk := range strings.Fields(claimed) {
		if strings.EqualFold(mk, strings.Fields(verified)[0]) {
			c = mk
		}
	}
	v = strings.Fields(verified)[0]
	if c == "" {
		return fmt.Errorf("classify is not marked on cap-node (%q vs %q)", claimed, verified)
	}
	if c == v {
		return fmt.Errorf("CLAIMED and VERIFIED classify render identically as %q - nothing tells them apart", c)
	}
	return nil
}

func (s *edgeBDD) nodeAgedOut() error {
	s.enroll("attic-board", []store.EdgeTransport{lanT("192.168.1.50")}, edge.Sense)
	s.now = s.now.Add(6 * time.Minute)
	n, err := s.fleet.Sweep(2 * time.Minute)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("the sweep darkened nothing")
	}
	s.subject = "attic-board"
	return nil
}

func (s *edgeBDD) drawnDarkWithLastSeenAge() error {
	ln, _, err := lineFor(s.out, "attic-board")
	if err != nil {
		return err
	}
	if !strings.Contains(ln, "DARK") {
		return fmt.Errorf("a node that aged out is not drawn DARK: %q", ln)
	}
	if !regexp.MustCompile(`\d+[smhd]`).MatchString(ln) {
		return fmt.Errorf("a dark node does not say how long ago it was last seen: %q", ln)
	}
	return nil
}

func (s *edgeBDD) stillInTheGraph() error {
	if nameOccurrences(s.out, "attic-board") != 1 {
		return fmt.Errorf("the dark node was dropped from the graph:\n%s", s.out)
	}
	return nil
}

func (s *edgeBDD) edgeIsBroken() error {
	ln, _, err := lineFor(s.out, "attic-board")
	if err != nil {
		return err
	}
	f := edgeField(ln, "attic-board")
	if !strings.Contains(f, tBroken) {
		return fmt.Errorf("a dark node's edge is not drawn broken: %q", ln)
	}
	if strings.Contains(f, tSolid) {
		return fmt.Errorf("a dark node still claims a solid link: %q", ln)
	}
	return nil
}

func (s *edgeBDD) anUnenrolledPeer() error {
	s.cands = []store.EdgeNode{{
		ID: "n_" + strings.Repeat("c0", 24), Name: "new-pi5", Kind: string(edge.Host),
		Presence: string(edge.PresenceCandidate), LastSeen: s.now.Unix(),
		Transports: []store.EdgeTransport{lanT("192.168.1.44")},
	}}
	return nil
}

func (s *edgeBDD) candidateInItsOwnArea() error {
	at := strings.Index(s.out, "CANDIDATES")
	if at < 0 {
		return fmt.Errorf("there is no CANDIDATES area:\n%s", s.out)
	}
	peer := strings.Index(s.out, "new-pi5")
	if peer < at {
		return fmt.Errorf("the candidate is drawn inside the fleet, above the CANDIDATES heading:\n%s", s.out)
	}
	return nil
}

func (s *edgeBDD) candidateHasNoEdgeToSelf() error {
	ln, _, err := lineFor(s.out, "new-pi5")
	if err != nil {
		return err
	}
	if strings.ContainsAny(ln, "├└"+tSolid+tDim+tBroken) {
		return fmt.Errorf("a candidate is drawn connected to self: %q", ln)
	}
	return nil
}

func (s *edgeBDD) saysHowToAdopt() error {
	if !strings.Contains(strings.ToLower(s.out), "adopt") {
		return fmt.Errorf("the screen does not say how to adopt a candidate:\n%s", s.out)
	}
	return nil
}

func (s *edgeBDD) emptyFleet() error {
	s.db = store.NewMem()
	s.fleet = edge.NewFleet(s.db, "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.ids = map[string]string{}
	s.cands = nil
	s.build()
	return nil
}

func (s *edgeBDD) saysThisMachineIsTheOnlyNode() error {
	if !strings.Contains(s.out, "the only node on the Edge") {
		return fmt.Errorf("an empty Edge does not explain itself:\n%s", s.out)
	}
	if strings.ContainsAny(s.out, "╭╮╰╯├└│") {
		return fmt.Errorf("an empty Edge still draws an empty box:\n%s", s.out)
	}
	return nil
}

func (s *edgeBDD) namesTheOneAction() error {
	if !strings.Contains(s.out, "ADD A NODE") {
		return fmt.Errorf("an empty Edge does not name the action that would add one:\n%s", s.out)
	}
	return nil
}

// ---- 3. THE ANIMATION ----------------------------------------------------

func (s *edgeBDD) aNodeSendingHeartbeats() error {
	s.enroll("bench-pi", []store.EdgeTransport{lanT("192.168.1.20")}, edge.Serve)
	s.subject = "bench-pi"
	s.render(100)
	s.heartbeat("bench-pi")
	return nil
}

func (s *edgeBDD) twoFramesBetweenHeartbeats() error {
	s.frameA = s.render(100)
	s.tick()
	s.frameB = s.render(100)
	return nil
}

func pulseColumn(out, name string) int {
	ln, _, err := lineFor(out, name)
	if err != nil {
		return -1
	}
	return strings.Index(edgeField(ln, name), tPulse)
}

func (s *edgeBDD) pulseAdvances() error {
	a, b := pulseColumn(s.frameA, s.subject), pulseColumn(s.frameB, s.subject)
	if a < 0 || b < 0 {
		return fmt.Errorf("no pulse on %q's edge (frame A col %d, frame B col %d):\n%s\n---\n%s",
			s.subject, a, b, s.frameA, s.frameB)
	}
	if b >= a {
		return fmt.Errorf("the pulse did not advance toward self: column %d then %d", a, b)
	}
	return nil
}

func (s *edgeBDD) noOtherEdgeChanges() error {
	al, bl := strings.Split(s.frameA, "\n"), strings.Split(s.frameB, "\n")
	if len(al) != len(bl) {
		return fmt.Errorf("the frame changed shape: %d lines then %d", len(al), len(bl))
	}
	for i := range al {
		if strings.Contains(al[i], s.subject) {
			continue
		}
		if al[i] != bl[i] {
			return fmt.Errorf("line %d changed with no traffic on it:\n%q\n%q", i, al[i], bl[i])
		}
	}
	return nil
}

func (s *edgeBDD) fleetSilentForAMinute() error {
	s.enroll("bench-pi", []store.EdgeTransport{lanT("192.168.1.20")}, edge.Serve)
	s.render(100)
	s.now = s.now.Add(time.Minute)
	return nil
}

func (s *edgeBDD) framesAreRendered() error {
	s.frameA = s.render(100)
	for i := 0; i < 5; i++ {
		s.tick()
	}
	s.frameB = s.render(100)
	return nil
}

func (s *edgeBDD) noPulseMoves() error {
	if strings.Contains(s.frameA, tPulse) || strings.Contains(s.frameB, tPulse) {
		return fmt.Errorf("a pulse is drawn with no traffic to carry it:\n%s", s.frameB)
	}
	return nil
}

func (s *edgeBDD) theGraphIsStill() error {
	if s.frameA != s.frameB {
		return fmt.Errorf("the graph moved with no traffic:\n%s\n---\n%s", s.frameA, s.frameB)
	}
	if s.lastCmd != nil {
		return fmt.Errorf("an animation frame was requested with nothing to animate")
	}
	return nil
}

func (s *edgeBDD) aRelayedNode() error {
	return s.nodeThroughRelay("cabinet-jetson", "tower-1")
}

// heartbeatArrives records, frame by frame, which row the pulse is on.
func (s *edgeBDD) heartbeatArrives() error {
	s.render(100)
	s.heartbeat(s.subject)
	var order []string
	for i := 0; i < 24; i++ {
		out := s.render(100)
		if pulseColumn(out, s.subject) >= 0 {
			order = append(order, "node")
		} else if pulseColumn(out, s.relay) >= 0 {
			order = append(order, "relay")
		}
		s.tick()
	}
	s.frameA = strings.Join(order, ",")
	return nil
}

func (s *edgeBDD) pulseTravelsNodeRelaySelf() error {
	seq := strings.Split(s.frameA, ",")
	firstRelay, lastNode := -1, -1
	for i, at := range seq {
		if at == "relay" && firstRelay < 0 {
			firstRelay = i
		}
		if at == "node" {
			lastNode = i
		}
	}
	if lastNode < 0 {
		return fmt.Errorf("the pulse never appeared on the relayed node's own edge (%q)", s.frameA)
	}
	if firstRelay < 0 {
		return fmt.Errorf("the pulse never crossed the relay's edge to self (%q)", s.frameA)
	}
	if lastNode > firstRelay {
		return fmt.Errorf("the pulse reached the relay before it left the node (%q)", s.frameA)
	}
	return nil
}

func (s *edgeBDD) userLeavesTheEdgeScreen() error {
	s.enroll("bench-pi", []store.EdgeTransport{lanT("192.168.1.20")}, edge.Serve)
	s.render(100)
	s.heartbeat("bench-pi")
	s.tick()
	s.tick()
	s.frameA = s.render(100) // the state at the moment of leaving
	s.press("esc")
	return nil
}

func (s *edgeBDD) noFurtherFramesRequested() error {
	before := pulseColumn(s.frameA, "bench-pi")
	s.tick()
	if s.lastCmd != nil {
		return fmt.Errorf("the Edge screen is not visible but still asked for another animation frame")
	}
	s.press("3")
	after := pulseColumn(s.render(100), "bench-pi")
	if before != after {
		return fmt.Errorf("the animation advanced while the screen was not visible: column %d then %d", before, after)
	}
	return nil
}

func (s *edgeBDD) returningResumesFromCurrentState() error {
	at := pulseColumn(s.render(100), "bench-pi")
	if at < 0 {
		return fmt.Errorf("the pulse was lost on the way back to the screen:\n%s", s.out)
	}
	field := edgeField(mustLine(s.out, "bench-pi"), "bench-pi")
	if at == strings.LastIndexAny(field, tSolid)+1 || at == len(field)-1 {
		return fmt.Errorf("the animation replayed from the start instead of resuming: column %d of %q", at, field)
	}
	s.tick()
	next := pulseColumn(s.render(100), "bench-pi")
	if next >= at {
		return fmt.Errorf("the animation did not resume on return: column %d then %d", at, next)
	}
	return nil
}

func mustLine(out, name string) string {
	ln, _, _ := lineFor(out, name)
	return ln
}

func (s *edgeBDD) noColorIsSet() error {
	s.t.Setenv("NO_COLOR", "1")
	s.enroll("attic-board", []store.EdgeTransport{lanT("192.168.1.50")}, edge.Sense)
	s.now = s.now.Add(6 * time.Minute)
	// Enrolled AFTER the clock moved, so the sweep takes only the node that aged out.
	s.enroll("bench-pi", []store.EdgeTransport{lanT("192.168.1.20")}, edge.Serve)
	if err := s.nodeThroughRelay("cabinet-jetson", "tower-1"); err != nil {
		return err
	}
	if _, err := s.fleet.Sweep(2 * time.Minute); err != nil {
		return err
	}
	s.build()
	return nil
}

func (s *edgeBDD) noANSIEscapes() error {
	s.open()
	s.m.refreshEdge()
	raw := s.m.edgeView(100)
	if strings.Contains(raw, "\x1b") {
		return fmt.Errorf("the Edge screen emitted ANSI under NO_COLOR:\n%q", raw)
	}
	s.out = raw
	return nil
}

func (s *edgeBDD) edgesDistinguishableByGlyph() error {
	for _, c := range []struct{ name, glyph, what string }{
		{"bench-pi", tSolid, "solid"},
		{"cabinet-jetson", tDim, "dim"},
		{"attic-board", tBroken, "broken"},
	} {
		ln, _, err := lineFor(s.out, c.name)
		if err != nil {
			return err
		}
		if !strings.Contains(edgeField(ln, c.name), c.glyph) {
			return fmt.Errorf("the %s edge is not drawn with %q: %q", c.what, c.glyph, ln)
		}
	}
	if tSolid == tDim || tDim == tBroken || tSolid == tBroken {
		return fmt.Errorf("the three edge textures are not distinct glyphs")
	}
	return nil
}

// ---- 4. GEOMETRY ---------------------------------------------------------

func (s *edgeBDD) aTerminalNColumnsWide(cols int) error {
	var tm tea.Model = s.m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: cols, Height: 40})
	s.m = tm.(model)
	s.m.edgeClock = func() time.Time { return s.now }
	s.w = cols
	return nil
}

func (s *edgeBDD) theEdgeScreenRendersAtWidth() error {
	s.render(s.w)
	return nil
}

func (s *edgeBDD) noLineExceeds(cols int) error {
	for i, ln := range strings.Split(s.out, "\n") {
		if got := lipgloss.Width(ln); got > cols {
			return fmt.Errorf("line %d is %d columns wide at a %d-column terminal: %q", i, got, cols, ln)
		}
	}
	return nil
}

func (s *edgeBDD) theLayoutIs(want string) error {
	if got := layoutOf(s.out); got != want {
		return fmt.Errorf("at %d columns the layout is %q, want %q:\n%s", s.w, got, want, s.out)
	}
	return nil
}

func (s *edgeBDD) aFleetOfNNodes(n int) error {
	s.db = store.NewMem()
	s.fleet = edge.NewFleet(s.db, "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.ids = map[string]string{}
	for i := 0; i < n; i++ {
		s.enroll(fmt.Sprintf("node-%03d", i), []store.EdgeTransport{lanT(fmt.Sprintf("192.168.1.%d", i%250))}, edge.Sense)
	}
	s.build()
	return nil
}

func (s *edgeBDD) rendersAtColumns(cols int) error {
	var tm tea.Model = s.m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: cols, Height: 40})
	s.m = tm.(model)
	s.m.edgeClock = func() time.Time { return s.now }
	s.render(cols)
	s.w = cols
	return nil
}

func (s *edgeBDD) theLayoutIsAList() error { return s.theLayoutIs("list") }

func (s *edgeBDD) saysHowManyOfHowMany() error {
	mm := regexp.MustCompile(`showing (\d+) of (\d+) nodes`).FindStringSubmatch(s.out)
	if mm == nil {
		return fmt.Errorf("the list does not say how many of how many it is showing:\n%s", s.out)
	}
	if mm[2] != "200" {
		return fmt.Errorf("the list says the fleet holds %s nodes, want 200", mm[2])
	}
	if mm[1] == mm[2] {
		return fmt.Errorf("the list claims to be showing all 200 nodes at 100 columns")
	}
	return nil
}

func (s *edgeBDD) noLineTruncatedMidGlyph() error {
	for i, ln := range strings.Split(s.out, "\n") {
		if !utf8.ValidString(ln) {
			return fmt.Errorf("line %d was cut mid-glyph: %q", i, ln)
		}
		if lipgloss.Width(ln) > s.w {
			return fmt.Errorf("line %d overflows %d columns: %q", i, s.w, ln)
		}
	}
	return nil
}

func (s *edgeBDD) aMaximumLengthName() error {
	name := strings.Repeat("n", edge.MaxNameLen)
	if err := edge.ValidName(name); err != nil {
		return err
	}
	s.enroll(name, []store.EdgeTransport{lanT("192.168.1.60")}, edge.Sense)
	s.subject = name
	return nil
}

func (s *edgeBDD) nameIsElided() error {
	if strings.Contains(s.out, s.subject) {
		return fmt.Errorf("a %d-character name was drawn in full at 80 columns:\n%s", len(s.subject), s.out)
	}
	if !strings.Contains(s.out, "…") && !strings.Contains(s.out, "...") {
		return fmt.Errorf("the long name was cut with no elision marker:\n%s", s.out)
	}
	return nil
}

// boxAlignmentPreserved: the self box is square, and every node row puts its capability
// marks in the SAME column - which is what "the drawing did not break" means here.
func (s *edgeBDD) boxAlignmentPreserved() error {
	var box []int
	col := -1
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.ContainsAny(ln, "╭╰") || (strings.Count(ln, "│") == 2) {
			box = append(box, lipgloss.Width(ln))
		}
		if at := strings.Index(ln, "["); at >= 0 && strings.ContainsAny(ln, "├└") {
			if col < 0 {
				col = at
			} else if at != col {
				return fmt.Errorf("a node row's marks column moved from %d to %d: %q", col, at, ln)
			}
		}
	}
	if len(box) < 3 {
		return fmt.Errorf("the self box is not drawn as a box:\n%s", s.out)
	}
	for _, w := range box[1:] {
		if w != box[0] {
			return fmt.Errorf("the self box is skewed: widths %v", box)
		}
	}
	return nil
}

func (s *edgeBDD) fleetChangesDuringARender() error {
	s.enroll("bench-pi", []store.EdgeTransport{lanT("192.168.1.20")}, edge.Serve)
	s.enroll("going-away", []store.EdgeTransport{lanT("192.168.1.21")}, edge.Sense)
	s.open()
	s.m.refreshEdge() // the frame's snapshot is taken HERE
	s.frozen = true
	// ...and the fleet moves under it while the frame is being drawn.
	if err := s.fleet.Forget(s.ids["going-away"]); err != nil {
		return err
	}
	s.enroll("just-arrived", []store.EdgeTransport{lanT("192.168.1.22")}, edge.Sense)
	s.render(100)
	return nil
}

func (s *edgeBDD) frameIsInternallyConsistent() error {
	// The frame is the snapshot: the node that left is still drawn, the one that arrived
	// is not - and neither is half-drawn.
	if nameOccurrences(s.out, "going-away") != 1 {
		return fmt.Errorf("the frame lost a node mid-draw:\n%s", s.out)
	}
	if nameOccurrences(s.out, "just-arrived") != 0 {
		return fmt.Errorf("the frame drew a node that arrived after its snapshot:\n%s", s.out)
	}
	// The next frame catches up honestly.
	s.frozen = false
	next := s.render(100)
	if nameOccurrences(next, "just-arrived") != 1 || nameOccurrences(next, "going-away") != 0 {
		return fmt.Errorf("the next frame did not catch up with the fleet:\n%s", next)
	}
	return nil
}

func (s *edgeBDD) noEdgeToANodeNotDrawn() error {
	rows, names := 0, 0
	drawn := map[string]bool{}
	for _, n := range s.m.edgeNodes() {
		drawn[n.Name] = true
	}
	for _, ln := range strings.Split(s.out, "\n") {
		if !spineRe.MatchString(ln) {
			continue
		}
		rows++
		for name := range drawn {
			if strings.Contains(ln, name) {
				names++
			}
		}
	}
	if rows == 0 {
		return fmt.Errorf("no edges were drawn at all:\n%s", s.out)
	}
	if rows != names {
		return fmt.Errorf("%d edges were drawn but only %d of them reach a node the frame draws:\n%s",
			rows, names, s.out)
	}
	return nil
}

// ---- 5. SELECTION AND DETAIL ---------------------------------------------

func (s *edgeBDD) openDetailOf(name string) error {
	s.open()
	s.m.refreshEdge()
	for i := 0; i < 64; i++ {
		s.press("up") // rewind: the selection walks, it does not jump
	}
	for i := 0; i < 64; i++ {
		if s.m.edge.sel == s.ids[name] {
			break
		}
		s.press("down")
	}
	if s.m.edge.sel != s.ids[name] {
		return fmt.Errorf("the selection never reached %q", name)
	}
	s.press("enter")
	s.out = stripANSI(s.m.edgeView(100))
	return nil
}

func (s *edgeBDD) userMovesSelectionAndOpensANode() error {
	s.enroll("bench-pi", []store.EdgeTransport{lanT("192.168.1.20"), relayT("relay.rogerai.fm")}, edge.Serve, edge.Classify)
	if err := s.fleet.RecordProbe(s.ids["bench-pi"], edge.Classify, true); err != nil {
		return err
	}
	s.open()
	s.m.refreshEdge()
	first := s.m.edge.sel
	s.press("down")
	if s.m.edge.sel == first {
		return fmt.Errorf("the selection did not move")
	}
	s.subject = "bench-pi"
	return s.openDetailOf("bench-pi")
}

func (s *edgeBDD) detailShowsEverything() error {
	n, ok, err := s.fleet.ByName("bench-pi")
	if err != nil || !ok {
		return fmt.Errorf("the fixture node is missing: %v", err)
	}
	for _, want := range []string{n.ID, "bench-pi", "host", "serve", "CLAIMED", "classify", "VERIFIED", "192.168.1.20"} {
		if !strings.Contains(s.out, want) {
			return fmt.Errorf("the detail does not show %q:\n%s", want, s.out)
		}
	}
	if !regexp.MustCompile(`(?i)last seen`).MatchString(s.out) {
		return fmt.Errorf("the detail does not say when the node was last seen:\n%s", s.out)
	}
	lan, relay := strings.Index(s.out, "lan"), strings.Index(s.out, "relay")
	if lan < 0 || relay < 0 || lan > relay {
		return fmt.Errorf("the detail does not list transports in preference order (lan then relay):\n%s", s.out)
	}
	if n.Pin == "" {
		return fmt.Errorf("the fixture LAN peer has no pinned fingerprint")
	}
	if !strings.Contains(s.out, n.Pin[:16]) {
		return fmt.Errorf("the detail does not show the pinned fingerprint of a LAN peer:\n%s", s.out)
	}
	return nil
}

func (s *edgeBDD) noSecretIsShown() error {
	priv := hex.EncodeToString(s.priv["bench-pi"].Seed())
	if strings.Contains(strings.ToLower(s.out), priv[:16]) {
		return fmt.Errorf("the detail leaked private key material:\n%s", s.out)
	}
	for _, bad := range []string{"secret", "private key", "bearer", "password", "token", "BEGIN "} {
		if strings.Contains(strings.ToLower(s.out), strings.ToLower(bad)) {
			return fmt.Errorf("the detail shows something that reads as a secret (%q):\n%s", bad, s.out)
		}
	}
	return nil
}

func (s *edgeBDD) aNodeOnlyThroughRelay(via string) error {
	return s.nodeThroughRelay("cabinet-jetson", via)
}

func (s *edgeBDD) itsDetailIsOpened() error { return s.openDetailOf("cabinet-jetson") }

func (s *edgeBDD) namesTheRelayAsThePath(via string) error {
	if !strings.Contains(s.out, via) {
		return fmt.Errorf("the detail of a relayed node does not name %q as its path:\n%s", via, s.out)
	}
	return nil
}

func (s *edgeBDD) aSelectedNode() error {
	s.enroll("bench-pi", []store.EdgeTransport{lanT("192.168.1.20")}, edge.Serve)
	s.enroll("attic-board", []store.EdgeTransport{lanT("192.168.1.50")}, edge.Sense)
	s.open()
	s.m.refreshEdge()
	for i := 0; i < 64; i++ {
		s.press("up")
	}
	for i := 0; i < 64 && s.m.edge.sel != s.ids["bench-pi"]; i++ {
		s.press("down")
	}
	if s.m.edge.sel != s.ids["bench-pi"] {
		return fmt.Errorf("could not select bench-pi")
	}
	s.subject = "bench-pi"
	return nil
}

func (s *edgeBDD) anotherNodeGoesDark() error {
	s.now = s.now.Add(6 * time.Minute)
	// Only the OTHER node ages out: bench-pi is heard from just before the sweep.
	if _, err := s.fleet.Observe(s.ids["bench-pi"], edge.Observation{
		Name: "bench-pi", Kind: string(edge.Host), Caps: []edge.Capability{edge.Serve},
		Addr: "192.168.1.20", Fingerprint: strings.Repeat("ab", 32),
	}); err != nil {
		return err
	}
	n, err := s.fleet.Sweep(2 * time.Minute)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("nothing went dark")
	}
	other, ok, err := s.fleet.ByName("attic-board")
	if err != nil || !ok || other.Presence != string(edge.PresenceDark) {
		return fmt.Errorf("the other node did not go dark (%v)", other.Presence)
	}
	sel, _, err := s.fleet.ByName("bench-pi")
	if err != nil {
		return err
	}
	if sel.Presence == string(edge.PresenceDark) {
		return fmt.Errorf("the SELECTED node went dark - this scenario is about another one")
	}
	s.m.refreshEdge()
	return nil
}

func (s *edgeBDD) sameNodeStillSelected() error {
	if s.m.edge.sel != s.ids["bench-pi"] {
		return fmt.Errorf("the selection moved when the fleet changed")
	}
	out := stripANSI(s.m.edgeView(100))
	ln, _, err := lineFor(out, "bench-pi")
	if err != nil {
		return err
	}
	if !strings.Contains(ln, tSel) {
		return fmt.Errorf("the selected node is not marked as selected: %q", ln)
	}
	return nil
}

// ---- the suite -----------------------------------------------------------

func TestEdgeTopologyViewFeature(t *testing.T) {
	st := &edgeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			// 1. the slot
			sc.Step(`^a TUI with a fleet of nodes$`, st.aTUIWithAFleetOfNodes)
			sc.Step(`^the main screen is rendered$`, st.mainScreenRendered)
			sc.Step(`^"([^"]*)" names Edge$`, st.slotNamesEdge)
			sc.Step(`^"([^"]*)" names CONFIG$`, st.slotNamesConfig)
			sc.Step(`^pressing "([^"]*)" opens the Edge screen$`, st.pressingOpensEdge)
			sc.Step(`^pressing "([^"]*)" opens the spend-limit config screen that "([^"]*)" used to open$`, st.pressingOpensConfig)
			sc.Step(`^any screen renders a hint naming the config screen$`, st.anyScreenRendersAHint)
			sc.Step(`^it says "([^"]*)"$`, st.hintSays)
			sc.Step(`^no rendered hint anywhere still says "([^"]*)"$`, st.noHintStillSays)
			sc.Step(`^the Edge screen is open$`, st.theEdgeScreenIsOpen)
			sc.Step(`^"esc" returns to where the user came from$`, st.escReturnsWhereTheUserCameFrom)
			sc.Step(`^"q" does what it does on every other numbered screen$`, st.qDoesWhatItDoesElsewhere)
			// 2. the graph
			sc.Step(`^the Edge screen renders$`, st.theEdgeScreenRenders)
			sc.Step(`^this instance appears as a node marked as self$`, st.selfIsANode)
			sc.Step(`^every other node is drawn in relation to it$`, st.everyNodeDrawnInRelationToSelf)
			sc.Step(`^a node "([^"]*)" verified LAN-direct$`, st.nodeVerifiedLANDirect)
			sc.Step(`^a node "([^"]*)" reachable only through relay "([^"]*)"$`, st.nodeThroughRelay)
			sc.Step(`^"([^"]*)" is joined to self by a solid edge$`, st.joinedToSelfBySolidEdge)
			sc.Step(`^"([^"]*)" is joined to "([^"]*)", and "([^"]*)" to self, by dim edges$`, st.joinedThroughRelayByDimEdges)
			sc.Step(`^"([^"]*)" is drawn as a node, so the hop is visible$`, st.relayIsDrawnAsANode)
			sc.Step(`^a node reachable LAN-direct and through a relay$`, st.nodeReachableBothWays)
			sc.Step(`^it appears exactly once$`, st.appearsExactlyOnce)
			sc.Step(`^its edge is the solid LAN one$`, st.itsEdgeIsTheSolidLANOne)
			sc.Step(`^its detail still lists both transports$`, st.detailListsBothTransports)
			sc.Step(`^nodes declaring serve, classify, sense, actuate and relay$`, st.nodesDeclaringEveryCapability)
			sc.Step(`^each node carries a distinct mark per declared capability$`, st.distinctMarkPerCapability)
			sc.Step(`^a CLAIMED but unverified capability is visually distinct from a VERIFIED one$`, st.claimedIsDistinctFromVerified)
			sc.Step(`^a node whose last heartbeat aged out$`, st.nodeAgedOut)
			sc.Step(`^it is drawn DARK with its last-seen age$`, st.drawnDarkWithLastSeenAge)
			sc.Step(`^it is still in the graph$`, st.stillInTheGraph)
			sc.Step(`^its edge is drawn as broken rather than solid$`, st.edgeIsBroken)
			sc.Step(`^an unenrolled peer discovered on the LAN$`, st.anUnenrolledPeer)
			sc.Step(`^it appears in a CANDIDATES area, visually separate from the fleet$`, st.candidateInItsOwnArea)
			sc.Step(`^it has no edge to self, because nothing is connected yet$`, st.candidateHasNoEdgeToSelf)
			sc.Step(`^the screen says how to adopt it$`, st.saysHowToAdopt)
			sc.Step(`^a fleet with no nodes and no candidates$`, st.emptyFleet)
			sc.Step(`^it says this machine is the only node on the Edge$`, st.saysThisMachineIsTheOnlyNode)
			sc.Step(`^it names the one action that would add another$`, st.namesTheOneAction)
			// 3. the animation
			sc.Step(`^a node sending heartbeats$`, st.aNodeSendingHeartbeats)
			sc.Step(`^two frames are rendered between heartbeats$`, st.twoFramesBetweenHeartbeats)
			sc.Step(`^the pulse advances along that node's edge$`, st.pulseAdvances)
			sc.Step(`^no other node's edge changes$`, st.noOtherEdgeChanges)
			sc.Step(`^a fleet where no node has sent anything for a minute$`, st.fleetSilentForAMinute)
			sc.Step(`^frames are rendered$`, st.framesAreRendered)
			sc.Step(`^no pulse moves on any edge$`, st.noPulseMoves)
			sc.Step(`^the graph is still, because a moving graph must mean traffic$`, st.theGraphIsStill)
			sc.Step(`^a node reachable only through a relay$`, st.aRelayedNode)
			sc.Step(`^its heartbeat arrives$`, st.heartbeatArrives)
			sc.Step(`^the pulse travels node to relay to self, in that order$`, st.pulseTravelsNodeRelaySelf)
			sc.Step(`^the user leaves the Edge screen$`, st.userLeavesTheEdgeScreen)
			sc.Step(`^no further animation frames are requested$`, st.noFurtherFramesRequested)
			sc.Step(`^returning to the screen resumes from the current state, not a replay$`, st.returningResumesFromCurrentState)
			sc.Step(`^NO_COLOR is set$`, st.noColorIsSet)
			sc.Step(`^the output contains no ANSI escapes$`, st.noANSIEscapes)
			sc.Step(`^solid, dim and broken edges remain distinguishable by glyph alone$`, st.edgesDistinguishableByGlyph)
			// 4. geometry
			sc.Step(`^a terminal (\d+) columns wide$`, st.aTerminalNColumnsWide)
			sc.Step(`^no line exceeds (\d+) columns$`, st.noLineExceeds)
			sc.Step(`^the layout is "([^"]*)"$`, st.theLayoutIs)
			sc.Step(`^a fleet of (\d+) nodes$`, st.aFleetOfNNodes)
			sc.Step(`^the Edge screen renders at (\d+) columns$`, st.rendersAtColumns)
			sc.Step(`^the layout is a list$`, st.theLayoutIsAList)
			sc.Step(`^it says how many nodes it is showing out of how many$`, st.saysHowManyOfHowMany)
			sc.Step(`^no line is truncated mid-glyph$`, st.noLineTruncatedMidGlyph)
			sc.Step(`^a node whose name is the maximum allowed length$`, st.aMaximumLengthName)
			sc.Step(`^the name is elided with a marker rather than overflowing$`, st.nameIsElided)
			sc.Step(`^the graph's box alignment is preserved$`, st.boxAlignmentPreserved)
			sc.Step(`^nodes appearing and disappearing during a render$`, st.fleetChangesDuringARender)
			sc.Step(`^the frame drawn is internally consistent$`, st.frameIsInternallyConsistent)
			sc.Step(`^no frame shows an edge to a node it does not draw$`, st.noEdgeToANodeNotDrawn)
			// 5. selection and detail
			sc.Step(`^the user moves the selection and opens a node$`, st.userMovesSelectionAndOpensANode)
			sc.Step(`^its detail shows id, name, kind, capabilities with verification state, transports in preference order, last seen, and the pinned fingerprint for a LAN peer$`, st.detailShowsEverything)
			sc.Step(`^no secret is shown$`, st.noSecretIsShown)
			sc.Step(`^a node reachable only through relay "([^"]*)"$`, st.aNodeOnlyThroughRelay)
			sc.Step(`^its detail is opened$`, st.itsDetailIsOpened)
			sc.Step(`^it names "([^"]*)" as the path$`, st.namesTheRelayAsThePath)
			sc.Step(`^a selected node$`, st.aSelectedNode)
			sc.Step(`^another node goes dark$`, st.anotherNodeGoesDark)
			sc.Step(`^the same node is still selected$`, st.sameNodeStillSelected)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/edge/topology_view.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the Edge topology scenarios failed")
	}
}
