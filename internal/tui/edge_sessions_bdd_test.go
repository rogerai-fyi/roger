package tui

// Executable spec: features/edge/sessions.feature - "a node is a thing; a session is a
// thing happening".
//
// REAL dependencies, no mocks:
//   - a REAL internal/edge.Fleet over the REAL internal/store Mem backend, so the
//     participants a session runs between are the same records the topology screen draws.
//   - a REAL internal/edge.Sessions ledger, fed REAL protocol.UsageReceipt values shaped
//     exactly as cmd/rogerai-broker/tunnel.go writes them (including the SECOND receipt a
//     failover writes, and the $0 VoidReason receipt settleVoid records).
//   - the REAL TUI model and the REAL renderer at FIXED widths. Every assertion is on the
//     plain text of a frame.
//   - a counting decorator over the REAL store for the "no wallet is touched" scenario. It
//     delegates every call; it replaces nothing.
//
// The clock is injected (fleet.SetClock + sessions.SetClock + m.edgeClock) rather than
// slept on: a session that fades "after a while" must fade because the clock moved, not
// because the suite was slow.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// The session vocabulary the screen is specified in, written out LITERALLY here rather
// than imported from the implementation: a spec that reads its answer out of the code
// under test cannot fail when the code changes its mind.
const (
	sGlyphSession  = "▸" // traffic that happened
	sGlyphEscalate = "▲" // an escalation - the right call, drawn up the chain
	sGlyphRefused  = "✕" // nothing served it
	sGlyphWarn     = "⚠" // the warning style an escalation must NEVER wear
	sRightCall     = "right call"
)

// ---- the wallet watch ----------------------------------------------------

// walletWatch is a counting decorator over the REAL store. Every method is the real one
// (embedded interface); the three money entry points are counted on the way through, so
// "no wallet is touched" is an observation of the production store, not of a stand-in.
type walletWatch struct {
	store.Store
	mu      sync.Mutex
	touched int
}

func (w *walletWatch) bump() { w.mu.Lock(); w.touched++; w.mu.Unlock() }

func (w *walletWatch) count() int { w.mu.Lock(); defer w.mu.Unlock(); return w.touched }

func (w *walletWatch) HoldFor(user, requestID string, amount float64) (bool, error) {
	w.bump()
	return w.Store.HoldFor(user, requestID, amount)
}

func (w *walletWatch) ReleaseHoldFor(user, requestID string) (float64, error) {
	w.bump()
	return w.Store.ReleaseHoldFor(user, requestID)
}

func (w *walletWatch) Finalize(user, node string, held, cost, ownerShare float64, rec protocol.UsageReceipt) (float64, error) {
	w.bump()
	return w.Store.Finalize(user, node, held, cost, ownerShare, rec)
}

// ---- fixture -------------------------------------------------------------

type edgeSessBDD struct {
	t     *testing.T
	db    *walletWatch
	fleet *edge.Fleet
	sess  *edge.Sessions
	m     model
	now   time.Time
	ids   map[string]string

	w   int
	out string

	// what a scenario is talking about
	station  string // the station that serves the band under test
	sibling  string // the station a failover lands on
	relay    string // the relay a path goes through
	board    string // the classifying node
	band     string
	request  string
	last     edge.Session
	recs     []protocol.UsageReceipt
	rows     []string // one rendered session row per Examples row, for the shape sweep
	attrs    []string
	balance  float64
	nodesPre int
	err      error
}

func (s *edgeSessBDD) reset() {
	s.now = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	s.db = &walletWatch{Store: store.NewMem()}
	s.fleet = edge.NewFleet(s.db, "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.sess = edge.NewSessions("acct-1")
	s.sess.SetClock(func() time.Time { return s.now })
	s.ids = map[string]string{}
	s.station, s.sibling, s.relay, s.board = "", "", "", ""
	s.band, s.request = "gpt-oss-120b", ""
	s.recs, s.rows, s.attrs = nil, nil, nil
	s.last, s.err = edge.Session{}, nil
	s.w, s.out = 0, ""
	s.build()
}

func (s *edgeSessBDD) build() {
	hooks := Hooks{
		EdgeSelf:       "roger-desk",
		EdgeFleet:      s.fleet,
		EdgeSessions:   s.sess,
		EdgeCandidates: func() []store.EdgeNode { return nil },
	}
	var tm tea.Model = NewWithHooks("http://broker.local", "tester", &LimitStore{}, hooks)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := tm.(model)
	m.edgeClock = func() time.Time { return s.now }
	s.m = m
}

func (s *edgeSessBDD) enroll(name string, kind edge.Kind, caps ...edge.Capability) store.EdgeNode {
	s.t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		s.t.Fatalf("keygen: %v", err)
	}
	n := edge.NewNode(pub, name, kind, caps...)
	n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.10", Fingerprint: strings.Repeat("ab", 32)}}
	got, err := s.fleet.Enroll(n)
	if err != nil {
		s.t.Fatalf("enroll %q: %v", name, err)
	}
	s.ids[name] = got.ID
	return got
}

func (s *edgeSessBDD) render(w int) string {
	if s.m.mode != modeEdge {
		s.m.enterEdge()
	}
	s.m.refreshEdge()
	s.w = w
	s.out = stripANSI(s.m.edgeView(w))
	return s.out
}

// sessionRows are the drawn session rows: the lines in the SESSIONS block.
func sessionRows(out string) []string {
	var rows []string
	in := false
	for _, ln := range strings.Split(out, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "SESSIONS") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if t == "" {
			continue
		}
		if !strings.ContainsAny(t, sGlyphSession+sGlyphEscalate+sGlyphRefused) {
			break // out of the block
		}
		rows = append(rows, ln)
	}
	return rows
}

func (s *edgeSessBDD) theOneRow() (string, error) {
	rows := sessionRows(s.out)
	if len(rows) != 1 {
		return "", fmt.Errorf("want exactly one session row, got %d:\n%s", len(rows), s.out)
	}
	return rows[0], nil
}

// ---- receipt fixtures ----------------------------------------------------

// receiptFrom builds a receipt the way the relay writes one: bound to the attempt id the
// broker derives (attemptID: the request itself for attempt 1, request-N after that),
// naming the station that served and the model that was asked.
func receiptFor(request, station, model string, attempt int, ts int64) protocol.UsageReceipt {
	id := request
	if attempt > 1 {
		id = fmt.Sprintf("%s-%d", request, attempt)
	}
	return protocol.UsageReceipt{
		RequestID: id, NodeID: station, User: "acct-1", Model: model,
		PromptTokens: 120, CompletionTokens: 240, PriceIn: 0.1, PriceOut: 0.4, TS: ts,
	}
}

// voidReceiptFor is the $0 receipt settleVoid records so a request that produced nothing
// is still auditable: no tokens billed, and a VoidReason that says why.
func voidReceiptFor(request, station, model string, attempt int, reason string, ts int64) protocol.UsageReceipt {
	r := receiptFor(request, station, model, attempt, ts)
	r.CompletionTokens, r.PriceIn, r.PriceOut = 0, 0, 0
	r.VoidReason = reason
	return r
}

// serve records one completed session of the given shape and returns it.
func (s *edgeSessBDD) serve(t edge.Traffic) (edge.Session, error) {
	if t.Account == "" {
		t.Account = "acct-1"
	}
	if t.Band == "" {
		t.Band = s.band
	}
	if t.Request == "" {
		t.Request = "req-" + fmt.Sprint(len(s.sess.Live())+1)
	}
	s.request = t.Request
	got, err := s.sess.Record(t)
	if err == nil {
		s.last, s.recs = got, t.Receipts
	}
	return got, err
}

// ---- background ----------------------------------------------------------

func (s *edgeSessBDD) anEdgeWithThisMachineAndAStation() error {
	s.station = "house-cb"
	n := s.enroll(s.station, edge.Host, edge.Serve)
	return s.fleet.RecordProbe(n.ID, edge.Serve, true)
}

// ---- 1. a session is traffic, not a capability ---------------------------

func (s *edgeSessBDD) noTrafficIsFlowing() error { return nil }

func (s *edgeSessBDD) theEdgeIsViewed() error {
	s.render(100)
	return nil
}

func (s *edgeSessBDD) noSessionIsDrawn() error {
	if rows := sessionRows(s.out); len(rows) > 0 {
		return fmt.Errorf("a session was drawn with no traffic:\n%s", strings.Join(rows, "\n"))
	}
	if strings.Contains(s.out, "SESSIONS") {
		return fmt.Errorf("an idle Edge still draws a SESSIONS block:\n%s", s.out)
	}
	return nil
}

func (s *edgeSessBDD) theGraphIsStillBecauseMovementMeansTraffic() error {
	for i := 0; i < 4; i++ {
		var tm tea.Model = s.m
		tm, _ = tm.Update(edgeAnimMsg{})
		s.m = asModel(tm)
	}
	after := stripANSI(s.m.edgeView(100))
	if after != s.out {
		return fmt.Errorf("the graph moved with no traffic:\nbefore:\n%s\nafter:\n%s", s.out, after)
	}
	if strings.Contains(after, string(edgeGlyphPulse)) {
		return fmt.Errorf("a pulse is drawn with no traffic:\n%s", after)
	}
	return nil
}

func (s *edgeSessBDD) aStationThatCouldServeButHasNotBeenAsked() error {
	// The Background's station is a real member declaring a VERIFIED serve, and it has
	// been asked nothing at all.
	s.render(100)
	return nil
}

func (s *edgeSessBDD) noSessionIsDrawnToIt() error {
	for _, row := range sessionRows(s.out) {
		if strings.Contains(row, s.station) {
			return fmt.Errorf("a session was drawn to a station nothing asked:\n%s", row)
		}
	}
	return nil
}

func (s *edgeSessBDD) aLinkThatMerelyCouldExistIsNeverDrawn() error {
	// The station IS on the graph (it is a member); what must be absent is any TRAFFIC
	// to it. Nothing was asked, so the ledger holds nothing at all.
	if n := len(s.sess.Live()); n != 0 {
		return fmt.Errorf("the ledger invented %d session(s) from a capability", n)
	}
	if !strings.Contains(s.out, s.station) {
		return fmt.Errorf("the station is not even on the graph, so the scenario proves nothing:\n%s", s.out)
	}
	return s.noSessionIsDrawn()
}

func (s *edgeSessBDD) aCompletedSession() error {
	s.station = "house-cb"
	_, err := s.serve(edge.Traffic{
		Kind: edge.FromUse, Request: "req-fade",
		Receipts: []protocol.UsageReceipt{receiptFor("req-fade", s.station, s.band, 1, s.now.Unix())},
	})
	if err != nil {
		return err
	}
	s.render(100)
	if len(sessionRows(s.out)) != 1 {
		return fmt.Errorf("the completed session was not drawn in the first place:\n%s", s.out)
	}
	s.nodesPre = len(s.m.edgeNodes())
	return nil
}

func (s *edgeSessBDD) timePassesBeyondTheVisibleLife() error {
	s.now = s.now.Add(edge.SessionLife + time.Second)
	s.render(100)
	return nil
}

func (s *edgeSessBDD) itIsNoLongerDrawn() error { return s.noSessionIsDrawn() }

func (s *edgeSessBDD) theFleetIsUnchangedByItsPassing() error {
	if got := len(s.m.edgeNodes()); got != s.nodesPre {
		return fmt.Errorf("the fleet went from %d nodes to %d when a session faded", s.nodesPre, got)
	}
	if n := s.sess.Len(); n != 0 {
		return fmt.Errorf("a faded session is still held (%d), so sessions accumulate", n)
	}
	return nil
}

func (s *edgeSessBDD) manySessionsBetweenTheSameTwoParticipants() error {
	for i := 0; i < 30; i++ {
		req := fmt.Sprintf("req-many-%d", i)
		if _, err := s.serve(edge.Traffic{
			Kind: edge.FromAgent, Request: req,
			Receipts: []protocol.UsageReceipt{receiptFor(req, s.station, s.band, 1, s.now.Unix())},
		}); err != nil {
			return err
		}
	}
	s.render(100)
	return nil
}

func (s *edgeSessBDD) theGraphStillHoldsExactlyThoseTwoParticipants() error {
	// self is the box; the station is the one node row. Nothing else.
	nodes := s.m.edgeNodes()
	if len(nodes) != 1 || nodes[0].Name != s.station {
		var names []string
		for _, n := range nodes {
			names = append(names, n.Name)
		}
		return fmt.Errorf("the graph holds %v, want just %q beside self:\n%s", names, s.station, s.out)
	}
	return nil
}

func (s *edgeSessBDD) theCountIsShownOnTheEdgeNotAsNewBoxes() error {
	rows := sessionRows(s.out)
	if len(rows) != 1 {
		return fmt.Errorf("30 sessions between one pair drew %d rows, want one carrying a count:\n%s", len(rows), s.out)
	}
	if !strings.Contains(rows[0], "×30") {
		return fmt.Errorf("the busy edge does not carry its count:\n%s", rows[0])
	}
	if strings.Count(s.out, "╭") > 1 {
		return fmt.Errorf("sessions grew new boxes:\n%s", s.out)
	}
	return nil
}

// ---- 2. every initiator is the same shape --------------------------------

// theInitiator maps the Examples column onto the REAL initiator vocabulary.
func (s *edgeSessBDD) theInitiator(desc string) error {
	s.station = "house-cb"
	switch desc {
	case "a `roger use` endpoint":
		s.last = edge.Session{Kind: edge.FromUse}
	case "the TUI's own agent":
		s.last = edge.Session{Kind: edge.FromAgent}
	case "a guest operator holding the mic":
		s.last = edge.Session{Kind: edge.FromGuest, Who: "opencode"}
	case "the web console":
		s.last = edge.Session{Kind: edge.FromConsole}
	case "a board that escalated a reading":
		s.board = "bench-pi"
		s.last = edge.Session{Kind: edge.FromDevice, Who: s.board, Escalate: true}
	default:
		return fmt.Errorf("the Examples table names an initiator this spec does not know: %q", desc)
	}
	return nil
}

func (s *edgeSessBDD) itOpensATurnAgainstABand() error {
	req := "req-" + string(s.last.Kind)
	got, err := s.serve(edge.Traffic{
		Kind: s.last.Kind, Who: s.last.Who, Escalate: s.last.Escalate,
		Request: req,
		Receipts: []protocol.UsageReceipt{
			receiptFor(req, s.station, s.band, 1, s.now.Unix()),
		},
	})
	if err != nil {
		return err
	}
	s.last = got
	s.render(100)
	return nil
}

func (s *edgeSessBDD) aSessionIsDrawnFromThisNodeToTheServingStation() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, s.station) {
		return fmt.Errorf("the session does not reach the serving station:\n%s", row)
	}
	return nil
}

func (s *edgeSessBDD) itIsAttributedTo(attr string) error {
	want := attr
	switch attr {
	case "the guest's name":
		want = "opencode"
	case "the board's name":
		want = s.board
	}
	if got := s.last.Attribution(); got != want {
		return fmt.Errorf("the session is attributed to %q, want %q", got, want)
	}
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, want) {
		return fmt.Errorf("the drawn row does not carry the attribution %q:\n%s", want, row)
	}
	s.rows = append(s.rows, row)
	s.attrs = append(s.attrs, want)
	return nil
}

// shapeOf is the row with its ONE variable field - the attribution - taken out, so what
// is left is the row's shape: the glyph, the columns, the widths and the tail.
func shapeOf(row, attr string) (string, error) {
	i := strings.Index(row, attr)
	if i < 0 {
		return "", fmt.Errorf("the attribution %q is not in the row %q", attr, row)
	}
	return row[:i] + strings.Repeat("~", len([]rune(attr))) + row[i+len(attr):], nil
}

func (s *edgeSessBDD) itsShapeIsIdenticalToEveryOtherRow() error {
	// Every row this Examples table has produced so far, with the attribution blanked,
	// must be the same string: same glyph column, same column widths, same tail.
	var want string
	for i, row := range s.rows {
		got, err := shapeOf(row, s.attrs[i])
		if err != nil {
			return err
		}
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			return fmt.Errorf("row shapes differ:\n  %q (%s)\n  %q (%s)", want, s.attrs[0], got, s.attrs[i])
		}
	}
	if lipgloss.Width(s.rows[len(s.rows)-1]) > 100 {
		return fmt.Errorf("a session row overflows the terminal: %q", s.rows[len(s.rows)-1])
	}
	return nil
}

func (s *edgeSessBDD) anySessionIsOpenedAgainstABand() error {
	s.station = "house-cb"
	s.band = "wave-nano"
	_, err := s.serve(edge.Traffic{
		Kind: edge.FromUse, Request: "req-band",
		Receipts: []protocol.UsageReceipt{receiptFor("req-band", s.station, s.band, 1, s.now.Unix())},
	})
	if err != nil {
		return err
	}
	s.render(100)
	return nil
}

func (s *edgeSessBDD) theSessionNamesTheBand() error {
	if s.last.Band != s.band {
		return fmt.Errorf("the session names band %q, want %q", s.last.Band, s.band)
	}
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, s.band) {
		return fmt.Errorf("the drawn session does not name the band it is asking:\n%s", row)
	}
	return nil
}

func (s *edgeSessBDD) aSessionCompletes() error {
	s.station = "house-cb"
	rec := receiptFor("req-served", s.station, s.band, 1, s.now.Unix())
	_, err := s.serve(edge.Traffic{Kind: edge.FromUse, Request: "req-served", Receipts: []protocol.UsageReceipt{rec}})
	if err != nil {
		return err
	}
	s.render(100)
	return nil
}

func (s *edgeSessBDD) itNamesTheStationThatServed() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, s.station) {
		return fmt.Errorf("the completed session does not name the station that served:\n%s", row)
	}
	return nil
}

func (s *edgeSessBDD) thatIsTheStationItsReceiptNames() error {
	if len(s.recs) == 0 {
		return errors.New("the scenario recorded no receipt, so there is nothing to check against")
	}
	if s.last.Station != s.recs[len(s.recs)-1].NodeID {
		return fmt.Errorf("the session names station %q; its receipt names %q",
			s.last.Station, s.recs[len(s.recs)-1].NodeID)
	}
	return nil
}

func (s *edgeSessBDD) aSessionWhoseFirstStationFailedWithNoOutput() error {
	s.station, s.sibling = "cold-cb", "house-cb"
	// Exactly what the failover writes: attempt 1 VOIDED at $0 with its reason, then
	// attempt 2's real receipt from the sibling that served.
	s.recs = []protocol.UsageReceipt{
		voidReceiptFor("req-fo", s.station, s.band, 1, protocol.VoidEmptyOutput, s.now.Unix()),
	}
	return nil
}

func (s *edgeSessBDD) itIsServedByASibling() error {
	s.recs = append(s.recs, receiptFor("req-fo", s.sibling, s.band, 2, s.now.Unix()))
	_, err := s.serve(edge.Traffic{Kind: edge.FromUse, Request: "req-fo", Receipts: s.recs})
	if err != nil {
		return err
	}
	s.render(100)
	return nil
}

func (s *edgeSessBDD) itShowsTheStationLeftAndTheOneThatServed() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, s.station) {
		return fmt.Errorf("the failed-over session hides the station it left:\n%s", row)
	}
	if !strings.Contains(row, s.sibling) {
		return fmt.Errorf("the failed-over session does not name the station that served:\n%s", row)
	}
	if strings.Index(row, s.station) > strings.Index(row, s.sibling) {
		return fmt.Errorf("the station it left is drawn AFTER the one that served:\n%s", row)
	}
	if s.last.Left != s.station || s.last.Station != s.sibling {
		return fmt.Errorf("the session records left=%q served=%q, want left=%q served=%q",
			s.last.Left, s.last.Station, s.station, s.sibling)
	}
	return nil
}

func (s *edgeSessBDD) bothReceiptsExistAsTheFailoverWritesThem() error {
	if len(s.recs) != 2 {
		return fmt.Errorf("the failover produced %d receipts, want 2", len(s.recs))
	}
	if s.recs[0].VoidReason == "" {
		return errors.New("the abandoned attempt's receipt is not a $0 void, so the lineage is incomplete")
	}
	if s.recs[0].RequestID != "req-fo" || s.recs[1].RequestID != "req-fo-2" {
		return fmt.Errorf("the two receipts are not the broker's attempt ids: %q, %q",
			s.recs[0].RequestID, s.recs[1].RequestID)
	}
	if got := s.last.Receipts; len(got) != 2 {
		return fmt.Errorf("the session carries %d receipts, want both", len(got))
	}
	for _, rec := range s.recs {
		if !edge.ReceiptBelongs(rec, s.last.Request) {
			return fmt.Errorf("receipt %q does not bind to request %q", rec.RequestID, s.last.Request)
		}
	}
	return nil
}

// ---- 3. the escalation chain ---------------------------------------------

func (s *edgeSessBDD) aNodeDeclaringWithItsContract(cap string) error {
	s.board = "bench-pi"
	s.station = "house-cb"
	s.band = "wave-nano"
	n := s.enroll(s.board, edge.Board, edge.Capability(cap))
	if err := s.fleet.SetContract(n.ID, edge.Contract{
		Class:   "alarm_triage",
		Framing: edge.ProductionFraming("alarm_triage"),
		Labels:  []string{"ok", "nuisance", "chattering", "escalate"},
	}); err != nil {
		return err
	}
	return nil
}

func (s *edgeSessBDD) itMeetsAReadingItCannotName() error { return nil }

func (s *edgeSessBDD) itEscalatesToABand() error {
	n, ok, err := s.fleet.ByName(s.board)
	if err != nil || !ok {
		return fmt.Errorf("the board is not on the fleet: %v", err)
	}
	c := n.Contract
	_, err = s.serve(edge.Traffic{
		Kind: edge.FromDevice, Who: s.board, Escalate: true,
		Contract: c, Request: "req-esc",
		Receipts: []protocol.UsageReceipt{receiptFor("req-esc", s.station, s.band, 1, s.now.Unix())},
	})
	if err != nil {
		return err
	}
	s.render(100)
	return nil
}

func (s *edgeSessBDD) theEscalationIsDrawnFromTheBoardToTheStation() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, sGlyphEscalate) {
		return fmt.Errorf("the escalation is not drawn as one:\n%s", row)
	}
	bi, si := strings.Index(row, s.board), strings.Index(row, s.station)
	if bi < 0 || si < 0 || bi > si {
		return fmt.Errorf("the escalation does not travel board -> station:\n%s", row)
	}
	return nil
}

func (s *edgeSessBDD) anEscalationIsDrawn() error {
	if err := s.aNodeDeclaringWithItsContract("classify"); err != nil {
		return err
	}
	return s.itEscalatesToABand()
}

func (s *edgeSessBDD) itRendersInThePositiveStyle() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, sGlyphEscalate) {
		return fmt.Errorf("the escalation does not wear the positive escalation glyph:\n%s", row)
	}
	if strings.Contains(row, sGlyphRefused) {
		return fmt.Errorf("the escalation wears the refusal glyph:\n%s", row)
	}
	return nil
}

func (s *edgeSessBDD) itNeverRendersInTheWarningStyle() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if strings.Contains(row, sGlyphWarn) {
		return fmt.Errorf("the escalation is drawn as a warning:\n%s", row)
	}
	for _, bad := range []string{"WARN", "warning", "fault", "FAULT", "failed", "FAILED", "error", "ERROR", "REFUSED"} {
		if strings.Contains(row, bad) {
			return fmt.Errorf("the escalation reads as a fault (%q):\n%s", bad, row)
		}
	}
	return nil
}

func (s *edgeSessBDD) itsLabelReadsAsTheRightCall() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, sRightCall) {
		return fmt.Errorf("the escalation's label does not read as the right call:\n%s", row)
	}
	return nil
}

func (s *edgeSessBDD) aClassifyingNodeWhoseContractCarriesAFixedFraming() error {
	return s.aNodeDeclaringWithItsContract("classify")
}

func (s *edgeSessBDD) itEscalates() error { return s.itEscalatesToABand() }

func (s *edgeSessBDD) theFramingIsPartOfWhatWasSent() error {
	n, ok, err := s.fleet.ByName(s.board)
	if err != nil || !ok {
		return fmt.Errorf("the board is not on the fleet: %v", err)
	}
	c := n.Contract
	if c.Framing == "" {
		return errors.New("the device carries no framing, so nothing could travel with the escalation")
	}
	if s.last.Contract.Framing != c.Framing {
		return fmt.Errorf("the escalation carries a DIFFERENT framing from the device:\n  device: %q\n  sent:   %q",
			c.Framing, s.last.Contract.Framing)
	}
	if s.last.Contract.Class != c.Class {
		return fmt.Errorf("the escalation carries class %q, the device's is %q", s.last.Contract.Class, c.Class)
	}
	return nil
}

func (s *edgeSessBDD) theFramingShownIsTheRealProductionFraming() error {
	want := edge.ProductionFraming("alarm_triage")
	if want == "" {
		return errors.New("there is no production framing for alarm_triage")
	}
	// Not a paraphrase: the SAME text the shipped Playbox device-prompt map carries.
	js, err := os.ReadFile("../../web/src/js/playbox.js")
	if err != nil {
		return fmt.Errorf("the production framing map could not be read: %w", err)
	}
	if !strings.Contains(string(js), want) {
		return fmt.Errorf("the framing is not the production one in web/src/js/playbox.js:\n%q", want)
	}
	// And it is SHOWN, verbatim, on the device it is part of.
	s.m.edge.sel = s.ids[s.board]
	s.m.edge.detail = true
	shown := stripANSI(s.m.edgeView(100))
	s.m.edge.detail = false
	flat := strings.Join(strings.Fields(shown), " ")
	if !strings.Contains(flat, strings.Join(strings.Fields(want), " ")) {
		return fmt.Errorf("the device's framing is not shown verbatim:\n%s", shown)
	}
	return nil
}

func (s *edgeSessBDD) aCompletedEscalationWithAModelAnswer() error {
	if err := s.aNodeDeclaringWithItsContract("classify"); err != nil {
		return err
	}
	n, _, _ := s.fleet.ByName(s.board)
	c := n.Contract
	got, err := s.serve(edge.Traffic{
		Kind: edge.FromDevice, Who: s.board, Escalate: true,
		Contract: c, Request: "req-route", Answer: `{"verdict":"chattering","confidence":0.81}`,
		Route:    "log",
		Receipts: []protocol.UsageReceipt{receiptFor("req-route", s.station, s.band, 1, s.now.Unix())},
	})
	if err != nil {
		return err
	}
	s.last = got
	return nil
}

func (s *edgeSessBDD) theOwnerChangesWhereFindingsAreRouted() error {
	got, err := s.sess.Route(s.last.Request, "human-review")
	if err != nil {
		return err
	}
	s.last = got
	return nil
}

func (s *edgeSessBDD) theModelAnswerIsUnchanged() error {
	want := `{"verdict":"chattering","confidence":0.81}`
	if s.last.Answer != want {
		return fmt.Errorf("routing rewrote the finding:\n  was:  %q\n  now:  %q", want, s.last.Answer)
	}
	return nil
}

func (s *edgeSessBDD) onlyTheDestinationChanges() error {
	if s.last.Route != "human-review" {
		return fmt.Errorf("the destination is %q, want %q", s.last.Route, "human-review")
	}
	// Everything else about the session is the one it was.
	before, ok := s.sess.Get(s.last.Request)
	if !ok {
		return errors.New("the session is gone")
	}
	before.Route = "log"
	want := s.last
	want.Route = "log"
	if fmt.Sprint(before) != fmt.Sprint(want) {
		return fmt.Errorf("routing changed more than the destination:\n  %v\n  %v", want, before)
	}
	return nil
}

func (s *edgeSessBDD) aClassifyingNodeThatNamesTheReadingItself() error {
	if err := s.aNodeDeclaringWithItsContract("classify"); err != nil {
		return err
	}
	// It answered locally. Nothing was relayed, so nothing is recorded: the Edge has no
	// way to hear about work that never left the device, and that is the point.
	s.render(100)
	return nil
}

func (s *edgeSessBDD) noEscalationIsDrawn() error {
	for _, row := range sessionRows(s.out) {
		if strings.Contains(row, sGlyphEscalate) {
			return fmt.Errorf("an escalation was drawn for a reading the device named itself:\n%s", row)
		}
	}
	return nil
}

func (s *edgeSessBDD) nothingWasAskedOfAnyBand() error {
	if n := s.sess.Len(); n != 0 {
		return fmt.Errorf("%d session(s) exist though nothing left the device", n)
	}
	return s.noSessionIsDrawn()
}

func (s *edgeSessBDD) everyStationForTheBandIsCoolingOrAbsent() error {
	s.band = "wave-nano"
	s.station = ""
	return s.aNodeDeclaringWithItsContract("classify")
}

func (s *edgeSessBDD) aBoardEscalates() error {
	n, _, _ := s.fleet.ByName(s.board)
	c := n.Contract
	// Nothing served it, so the only receipt is the $0 one that says why - the same
	// void shape settleVoid records, with no station to name.
	rec := voidReceiptFor("req-cold", "", s.band, 1, edge.RefusedNoStation, s.now.Unix())
	_, err := s.serve(edge.Traffic{
		Kind: edge.FromDevice, Who: s.board, Escalate: true,
		Contract: c, Request: "req-cold",
		Receipts: []protocol.UsageReceipt{rec},
	})
	if err != nil {
		return err
	}
	s.render(100)
	return nil
}

func (s *edgeSessBDD) theSessionIsDrawnAsRefusedWithTheReason() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, sGlyphRefused) {
		return fmt.Errorf("the refused escalation is not drawn as refused:\n%s", row)
	}
	if !strings.Contains(row, edge.RefusedNoStation) {
		return fmt.Errorf("the refusal does not say why:\n%s", row)
	}
	if s.last.Outcome != edge.OutcomeRefused {
		return fmt.Errorf("the session's outcome is %q, want REFUSED", s.last.Outcome)
	}
	return nil
}

func (s *edgeSessBDD) itIsNotDrawnAsThoughAModelAnswered() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if strings.Contains(row, sRightCall) || strings.Contains(row, "served") {
		return fmt.Errorf("a refusal is drawn as though a model answered:\n%s", row)
	}
	if s.last.Answer != "" {
		return fmt.Errorf("a refused session carries an answer: %q", s.last.Answer)
	}
	if s.last.Station != "" {
		return fmt.Errorf("a refused session names a serving station: %q", s.last.Station)
	}
	return nil
}

// ---- 4. authority is unchanged -------------------------------------------

// anySessionDrawnOnTheEdge records one of EVERY shape the layer can draw, so "any" is
// answered by a sweep rather than by one convenient example.
func (s *edgeSessBDD) anySessionDrawnOnTheEdge() error {
	s.station, s.sibling, s.board = "house-cb", "sib-cb", "bench-pi"
	if err := s.aNodeDeclaringWithItsContract("classify"); err != nil {
		return err
	}
	s.station = "house-cb"
	kinds := []struct {
		k   edge.Initiator
		who string
	}{
		{edge.FromUse, ""}, {edge.FromAgent, ""}, {edge.FromGuest, "opencode"},
		{edge.FromConsole, ""}, {edge.FromDevice, s.board},
	}
	for i, kd := range kinds {
		req := fmt.Sprintf("req-sweep-%d", i)
		if _, err := s.serve(edge.Traffic{
			Kind: kd.k, Who: kd.who, Escalate: kd.k == edge.FromDevice, Request: req,
			Receipts: []protocol.UsageReceipt{receiptFor(req, s.station, s.band, 1, s.now.Unix())},
		}); err != nil {
			return err
		}
	}
	// ...a failed-over one...
	if _, err := s.serve(edge.Traffic{
		Kind: edge.FromUse, Request: "req-sweep-fo",
		Receipts: []protocol.UsageReceipt{
			voidReceiptFor("req-sweep-fo", "cold-cb", s.band, 1, protocol.VoidEmptyOutput, s.now.Unix()),
			receiptFor("req-sweep-fo", s.sibling, s.band, 2, s.now.Unix()),
		},
	}); err != nil {
		return err
	}
	// ...and a refused one.
	if _, err := s.serve(edge.Traffic{
		Kind: edge.FromDevice, Who: s.board, Escalate: true, Request: "req-sweep-cold",
		Receipts: []protocol.UsageReceipt{
			voidReceiptFor("req-sweep-cold", "", s.band, 1, edge.RefusedNoStation, s.now.Unix()),
		},
	}); err != nil {
		return err
	}
	s.render(100)
	return nil
}

func (s *edgeSessBDD) aReceiptExistsForIt() error {
	live := s.sess.Live()
	if len(live) < 7 {
		return fmt.Errorf("the sweep drew %d sessions, want every shape", len(live))
	}
	for _, ss := range live {
		if ss.Request == "" {
			return fmt.Errorf("a session with no request is drawn: %+v", ss)
		}
		if len(ss.Receipts) == 0 {
			return fmt.Errorf("a session with no receipt is drawn: %+v", ss)
		}
		for _, r := range ss.Receipts {
			if !edge.ReceiptBelongs(r, ss.Request) {
				return fmt.Errorf("session %q is drawn from receipt %q, which is not its own", ss.Request, r.RequestID)
			}
		}
	}
	// And the ledger REFUSES to draw one that was never receipted.
	if _, err := s.sess.Record(edge.Traffic{Account: "acct-1", Kind: edge.FromUse, Request: "req-bare", Band: s.band}); !errors.Is(err, edge.ErrNoReceipt) {
		return fmt.Errorf("the ledger accepted a session with no receipt (err=%v)", err)
	}
	return nil
}

func (s *edgeSessBDD) theEdgeViewCreatedNoTrafficOfItsOwn() error {
	before := s.sess.Len()
	touched := s.db.count()
	for _, w := range []int{100, 80, 60} {
		s.render(w)
		s.m.edgeView(w)
	}
	if got := s.sess.Len(); got != before {
		return fmt.Errorf("looking at the Edge created %d session(s)", got-before)
	}
	if got := s.db.count(); got != touched {
		return fmt.Errorf("looking at the Edge touched the wallet %d time(s)", got-touched)
	}
	return nil
}

func (s *edgeSessBDD) theOwnerLooksAtTheEdge() error {
	s.station = "house-cb"
	if _, err := s.serve(edge.Traffic{
		Kind: edge.FromUse, Request: "req-look",
		Receipts: []protocol.UsageReceipt{receiptFor("req-look", s.station, s.band, 1, s.now.Unix())},
	}); err != nil {
		return err
	}
	s.balance, _ = s.db.PeekBalance("acct-1")
	s.nodesPre = s.sess.Len()
	// The whole surface: enter it, move on it, open a detail, re-read it, leave it.
	for _, key := range []string{"3", "down", "enter", "esc", "r", "esc"} {
		var msg tea.KeyMsg
		switch key {
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		var tm tea.Model = s.m
		tm, _ = tm.Update(msg)
		s.m = asModel(tm)
		s.out = stripANSI(s.m.View())
	}
	return nil
}

func (s *edgeSessBDD) nothingIsDispatched() error {
	if got := s.sess.Len(); got != s.nodesPre {
		return fmt.Errorf("looking at the Edge dispatched %d turn(s)", got-s.nodesPre)
	}
	return nil
}

func (s *edgeSessBDD) noWalletIsTouched() error {
	if n := s.db.count(); n != 0 {
		return fmt.Errorf("the Edge view touched the wallet %d time(s)", n)
	}
	got, err := s.db.PeekBalance("acct-1")
	if err != nil {
		return err
	}
	if got != s.balance {
		return fmt.Errorf("the balance moved from %v to %v", s.balance, got)
	}
	return nil
}

func (s *edgeSessBDD) aConsumerSessionAgainstAPaidBand() error {
	s.station, s.band = "house-cb", "gpt-oss-120b"
	s.m.limits.set(s.band, Limit{MaxOut: 0.20})
	return nil
}

func (s *edgeSessBDD) itIsSubjectToTheSameSpendLimits() error {
	// The SAME predicate the dial uses before it will tune a band - not a second one.
	under := band{model: s.band, online: true, minOut: 0.10}
	if bandOverLimit(under, s.m.limits) {
		return errors.New("a band inside the cap was refused")
	}
	over := band{model: s.band, online: true, minOut: 0.90}
	if !bandOverLimit(over, s.m.limits) {
		return errors.New("a band over the cap was not refused - the session is not bound by the existing limits")
	}
	return nil
}

func (s *edgeSessBDD) exceedingThemRefusesTheSession() error {
	over := band{model: s.band, online: true, minOut: 0.90}
	if !bandOverLimit(over, s.m.limits) {
		return errors.New("the existing limit did not refuse")
	}
	// A refused session is drawn as the refusal it was, never as a served turn.
	rec := voidReceiptFor("req-cap", "", s.band, 1, edge.RefusedOverLimit, s.now.Unix())
	got, err := s.serve(edge.Traffic{
		Kind: edge.FromUse, Request: "req-cap", Receipts: []protocol.UsageReceipt{rec},
	})
	if err != nil {
		return err
	}
	if got.Outcome != edge.OutcomeRefused {
		return fmt.Errorf("an over-limit session is drawn as %q, want REFUSED", got.Outcome)
	}
	s.render(100)
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, edge.RefusedOverLimit) {
		return fmt.Errorf("the refusal does not name the limit:\n%s", row)
	}
	return nil
}

func (s *edgeSessBDD) aDeviceActionWithNoValidGrant() error {
	s.board = "bench-pi"
	n := s.enroll("relay-arm", edge.Board, edge.Actuate)
	// The REAL authority check, with an empty grant.
	s.err = s.fleet.AuthorizeInvoke(store.Grant{}, n.ID, edge.Actuate)
	s.ids["relay-arm"] = n.ID
	return nil
}

func (s *edgeSessBDD) itIsRefused() error {
	if s.err == nil {
		return errors.New("an action with no grant was authorized")
	}
	return nil
}

func (s *edgeSessBDD) theEdgeDrawsTheRefusalNotAnAction() error {
	rec := voidReceiptFor("req-grant", "", "relay-arm.actuate", 1, edge.RefusedNoGrant, s.now.Unix())
	got, err := s.serve(edge.Traffic{
		Kind: edge.FromAgent, Request: "req-grant", Receipts: []protocol.UsageReceipt{rec},
	})
	if err != nil {
		return err
	}
	if got.Outcome != edge.OutcomeRefused {
		return fmt.Errorf("an ungranted action is drawn as %q, want REFUSED", got.Outcome)
	}
	s.render(100)
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	if !strings.Contains(row, sGlyphRefused) || !strings.Contains(row, edge.RefusedNoGrant) {
		return fmt.Errorf("the refusal is not drawn as one:\n%s", row)
	}
	if strings.Contains(row, "served") {
		return fmt.Errorf("an ungranted action is drawn as an action:\n%s", row)
	}
	return nil
}

func (s *edgeSessBDD) trafficBelongingToADifferentAccount() error {
	s.station = "someone-elses-cb"
	_, s.err = s.sess.Record(edge.Traffic{
		Account: "acct-2", Kind: edge.FromGuest, Who: "their-guest", Request: "req-theirs", Band: "their-band",
		Receipts: []protocol.UsageReceipt{receiptFor("req-theirs", s.station, "their-band", 1, s.now.Unix())},
	})
	s.render(100)
	return nil
}

func (s *edgeSessBDD) itDoesNotAppearOnThisEdge() error {
	if !errors.Is(s.err, edge.ErrOtherAccount) {
		return fmt.Errorf("another account's traffic was accepted onto this Edge (err=%v)", s.err)
	}
	if n := s.sess.Len(); n != 0 {
		return fmt.Errorf("%d foreign session(s) are held", n)
	}
	return s.noSessionIsDrawn()
}

func (s *edgeSessBDD) nothingAboutItIsInferable() error {
	for _, leak := range []string{"acct-2", "their-guest", "req-theirs", "their-band", s.station} {
		if strings.Contains(s.out, leak) {
			return fmt.Errorf("the frame leaks %q from another account:\n%s", leak, s.out)
		}
	}
	// Not even a count, a placeholder or a gap.
	for _, hint := range []string{"1 session", "hidden", "elsewhere", "other account"} {
		if strings.Contains(s.out, hint) {
			return fmt.Errorf("the frame hints at foreign traffic (%q):\n%s", hint, s.out)
		}
	}
	return nil
}

// ---- 5. the view holds up ------------------------------------------------

func (s *edgeSessBDD) nSessionsAcrossMParticipants(n, parts int) error {
	s.station = "house-cb"
	for i := 0; i < parts; i++ {
		who := fmt.Sprintf("participant-%d", i)
		s.enroll(who, edge.Board, edge.Classify)
	}
	for i := 0; i < n; i++ {
		who := fmt.Sprintf("participant-%d", i%parts)
		req := fmt.Sprintf("req-busy-%d", i)
		if _, err := s.serve(edge.Traffic{
			Kind: edge.FromDevice, Who: who, Escalate: true, Request: req,
			Receipts: []protocol.UsageReceipt{receiptFor(req, s.station, s.band, 1, s.now.Unix())},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *edgeSessBDD) theEdgeIsViewedAtColumns(w int) error {
	s.render(w)
	return nil
}

func (s *edgeSessBDD) noLineExceedsTheWidth() error {
	for i, ln := range strings.Split(s.out, "\n") {
		if got := lipgloss.Width(ln); got > s.w {
			return fmt.Errorf("line %d is %d columns wide at width %d: %q", i+1, got, s.w, ln)
		}
	}
	return nil
}

func (s *edgeSessBDD) theBusiestEdgeShowsACount() error {
	rows := sessionRows(s.out)
	if len(rows) == 0 {
		return fmt.Errorf("no sessions were drawn at all:\n%s", s.out)
	}
	if len(rows) > 12 {
		return fmt.Errorf("%d session rows were drawn - the view stopped being legible:\n%s", len(rows), s.out)
	}
	found := false
	for _, r := range rows {
		if strings.Contains(r, "×") {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no edge carries a count, so 50 sessions were drawn as marks:\n%s", strings.Join(rows, "\n"))
	}
	if strings.Count(s.out, sGlyphEscalate) > 12 {
		return fmt.Errorf("the busiest edge is drawn as overlapping marks:\n%s", s.out)
	}
	return nil
}

func (s *edgeSessBDD) aSessionThroughARelay() error {
	s.relay, s.station = "relay-box", "house-cb"
	s.enroll(s.relay, edge.Host, edge.Relay)
	_, err := s.serve(edge.Traffic{
		Kind: edge.FromUse, Via: s.relay, Request: "req-relayed",
		Receipts: []protocol.UsageReceipt{receiptFor("req-relayed", s.station, s.band, 1, s.now.Unix())},
	})
	if err != nil {
		return err
	}
	s.render(100)
	return nil
}

func (s *edgeSessBDD) itIsDrawnParticipantRelayStation() error {
	row, err := s.theOneRow()
	if err != nil {
		return err
	}
	ri, si := strings.Index(row, s.relay), strings.Index(row, s.station)
	if ri < 0 {
		return fmt.Errorf("the relay hop is not drawn:\n%s", row)
	}
	if si < 0 {
		return fmt.Errorf("the station is not drawn:\n%s", row)
	}
	if ri > si {
		return fmt.Errorf("the hop is drawn out of order (station before relay):\n%s", row)
	}
	if s.last.Via != s.relay {
		return fmt.Errorf("the session records via=%q, want %q", s.last.Via, s.relay)
	}
	return nil
}

func (s *edgeSessBDD) theRelayIsDrawnAsItsOwnNode() error {
	for _, n := range s.m.edgeNodes() {
		if n.Name == s.relay {
			return nil
		}
	}
	return fmt.Errorf("%q is not a node on the graph, so the hop is implied rather than visible:\n%s", s.relay, s.out)
}

func (s *edgeSessBDD) noColorIsSetForSessions() error {
	s.t.Setenv("NO_COLOR", "1")
	s.build()
	return nil
}

func (s *edgeSessBDD) sessionEscalationAndRefusalStayDistinct() error {
	s.station, s.board, s.band = "house-cb", "bench-pi", "wave-nano"
	if err := s.aNodeDeclaringWithItsContract("classify"); err != nil {
		return err
	}
	s.station = "house-cb"
	if _, err := s.serve(edge.Traffic{
		Kind: edge.FromUse, Request: "req-plain",
		Receipts: []protocol.UsageReceipt{receiptFor("req-plain", s.station, s.band, 1, s.now.Unix())},
	}); err != nil {
		return err
	}
	n, _, _ := s.fleet.ByName(s.board)
	c := n.Contract
	if _, err := s.serve(edge.Traffic{
		Kind: edge.FromDevice, Who: s.board, Escalate: true, Contract: c, Request: "req-up",
		Receipts: []protocol.UsageReceipt{receiptFor("req-up", s.station, s.band, 1, s.now.Unix())},
	}); err != nil {
		return err
	}
	if _, err := s.serve(edge.Traffic{
		Kind: edge.FromUse, Request: "req-no",
		Receipts: []protocol.UsageReceipt{voidReceiptFor("req-no", "", s.band, 1, edge.RefusedNoStation, s.now.Unix())},
	}); err != nil {
		return err
	}
	out := s.render(120)
	if strings.Contains(out, "\x1b[") {
		return fmt.Errorf("the frame carries ANSI escapes under NO_COLOR:\n%q", out)
	}
	rows := sessionRows(out)
	if len(rows) != 3 {
		return fmt.Errorf("want a session, an escalation and a refusal; got %d rows:\n%s", len(rows), out)
	}
	seen := map[string]int{}
	for _, r := range rows {
		for _, g := range []string{sGlyphSession, sGlyphEscalate, sGlyphRefused} {
			if strings.Contains(r, g) {
				seen[g]++
			}
		}
	}
	for _, g := range []string{sGlyphSession, sGlyphEscalate, sGlyphRefused} {
		if seen[g] != 1 {
			return fmt.Errorf("glyph %q marks %d rows, want exactly one - the three are not distinguishable:\n%s",
				g, seen[g], strings.Join(rows, "\n"))
		}
	}
	return nil
}

func (s *edgeSessBDD) sessionsAreBeingDrawn() error {
	s.station = "house-cb"
	for i := 0; i < 12; i++ {
		req := fmt.Sprintf("req-load-%d", i)
		if _, err := s.serve(edge.Traffic{
			Kind: edge.FromAgent, Request: req,
			Receipts: []protocol.UsageReceipt{receiptFor(req, s.station, s.band, 1, s.now.Unix())},
		}); err != nil {
			return err
		}
	}
	s.render(100)
	return nil
}

// aConsumerRelaysARequest hammers the ledger from "relay" goroutines while a "view"
// goroutine renders, which is the only shape in which the two could ever contend.
func (s *edgeSessBDD) aConsumerRelaysARequest() error {
	const relays = 2000
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the view, drawing as fast as it can
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = s.sess.Live()
			}
		}
	}()
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		for i := 0; i < relays; i++ {
			req := fmt.Sprintf("req-relay-%d", i)
			_, _ = s.sess.Record(edge.Traffic{
				Account: "acct-1", Kind: edge.FromUse, Request: req, Band: s.band,
				Receipts: []protocol.UsageReceipt{receiptFor(req, s.station, s.band, 1, s.now.Unix())},
			})
		}
		done <- time.Since(start)
	}()
	select {
	case d := <-done:
		close(stop)
		wg.Wait()
		s.balance = d.Seconds()
	case <-time.After(30 * time.Second):
		close(stop)
		wg.Wait()
		return errors.New("the relay path BLOCKED on the view: 2000 recordings did not finish in 30s")
	}
	return nil
}

func (s *edgeSessBDD) theRelayCompletesAtNormalSpeed() error {
	// A bound, not a stopwatch race: the point is that recording is O(1) and never waits
	// on a reader. 2000 recordings against a bounded ring is microseconds of work.
	if s.balance > 10 {
		return fmt.Errorf("2000 recordings took %.1fs - the relay is paying for the view", s.balance)
	}
	// The ring is BOUNDED, so a busy relay can never grow the ledger without limit.
	if n := s.sess.Len(); n > edge.SessionsMax {
		return fmt.Errorf("the ledger grew to %d, past its %d bound", n, edge.SessionsMax)
	}
	return nil
}

func (s *edgeSessBDD) nothingAboutTheRelayWaitedOnTheView() error {
	// The proof is structural: recording with NO view in existence produces exactly the
	// session recording with one does. The view is a reader; it is not a participant.
	withView, ok := s.sess.Get("req-relay-1999")
	if !ok {
		return errors.New("the last relay was not recorded")
	}
	lone := edge.NewSessions("acct-1")
	lone.SetClock(func() time.Time { return s.now })
	req := "req-relay-1999"
	noView, err := lone.Record(edge.Traffic{
		Account: "acct-1", Kind: edge.FromUse, Request: req, Band: s.band,
		Receipts: []protocol.UsageReceipt{receiptFor(req, s.station, s.band, 1, s.now.Unix())},
	})
	if err != nil {
		return err
	}
	if fmt.Sprint(withView) != fmt.Sprint(noView) {
		return fmt.Errorf("the view changed what the relay recorded:\n  with: %v\n  without: %v", withView, noView)
	}
	return nil
}

// ---- the suite -----------------------------------------------------------

func TestEdgeSessionsFeature(t *testing.T) {
	st := &edgeSessBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.Step(`^an Edge with this machine and at least one serving station$`, st.anEdgeWithThisMachineAndAStation)
			// 1. a session is traffic, not a capability
			sc.Step(`^no traffic is flowing$`, st.noTrafficIsFlowing)
			sc.Step(`^the Edge is viewed$`, st.theEdgeIsViewed)
			sc.Step(`^no session is drawn$`, st.noSessionIsDrawn)
			sc.Step(`^the graph is still, because a moving graph must mean traffic$`, st.theGraphIsStillBecauseMovementMeansTraffic)
			sc.Step(`^a station that could serve this band but has not been asked$`, st.aStationThatCouldServeButHasNotBeenAsked)
			sc.Step(`^no session is drawn to it$`, st.noSessionIsDrawnToIt)
			sc.Step(`^a link that merely could exist is never drawn as one$`, st.aLinkThatMerelyCouldExistIsNeverDrawn)
			sc.Step(`^a completed session$`, st.aCompletedSession)
			sc.Step(`^time passes beyond the session's visible life$`, st.timePassesBeyondTheVisibleLife)
			sc.Step(`^it is no longer drawn$`, st.itIsNoLongerDrawn)
			sc.Step(`^the fleet's nodes are unchanged by its passing$`, st.theFleetIsUnchangedByItsPassing)
			sc.Step(`^many sessions between the same two participants$`, st.manySessionsBetweenTheSameTwoParticipants)
			sc.Step(`^the graph still holds exactly those two participants$`, st.theGraphStillHoldsExactlyThoseTwoParticipants)
			sc.Step(`^the session count is shown on the edge between them, not as new boxes$`, st.theCountIsShownOnTheEdgeNotAsNewBoxes)
			// 2. every initiator is the same shape
			sc.Step(`^(a \x60roger use\x60 endpoint|the TUI's own agent|a guest operator holding the mic|the web console|a board that escalated a reading)$`, st.theInitiator)
			sc.Step(`^it opens a turn against a band$`, st.itOpensATurnAgainstABand)
			sc.Step(`^a session is drawn from this node to the serving station$`, st.aSessionIsDrawnFromThisNodeToTheServingStation)
			sc.Step(`^it is attributed to "([^"]*)"$`, st.itIsAttributedTo)
			sc.Step(`^its shape is identical to every other row in this table$`, st.itsShapeIsIdenticalToEveryOtherRow)
			sc.Step(`^any session is opened against a band$`, st.anySessionIsOpenedAgainstABand)
			sc.Step(`^the session names the band it is asking$`, st.theSessionNamesTheBand)
			sc.Step(`^a session completes$`, st.aSessionCompletes)
			sc.Step(`^it names the station that served$`, st.itNamesTheStationThatServed)
			sc.Step(`^that is the station its receipt names$`, st.thatIsTheStationItsReceiptNames)
			sc.Step(`^a session whose first station returned a no-output failure$`, st.aSessionWhoseFirstStationFailedWithNoOutput)
			sc.Step(`^it is served by a sibling$`, st.itIsServedByASibling)
			sc.Step(`^the session shows the station it left and the station that served$`, st.itShowsTheStationLeftAndTheOneThatServed)
			sc.Step(`^both receipts exist, exactly as the failover already writes them$`, st.bothReceiptsExistAsTheFailoverWritesThem)
			// 3. the escalation chain
			sc.Step(`^a node declaring "([^"]*)" with its contract$`, st.aNodeDeclaringWithItsContract)
			sc.Step(`^it meets a reading it cannot name$`, st.itMeetsAReadingItCannotName)
			sc.Step(`^it escalates to a band$`, st.itEscalatesToABand)
			sc.Step(`^the escalation is drawn as a session from the board, travelling to the station$`, st.theEscalationIsDrawnFromTheBoardToTheStation)
			sc.Step(`^an escalation is drawn$`, st.anEscalationIsDrawn)
			sc.Step(`^it renders in the positive style$`, st.itRendersInThePositiveStyle)
			sc.Step(`^it never renders in the warning style$`, st.itNeverRendersInTheWarningStyle)
			sc.Step(`^its label reads as the right call$`, st.itsLabelReadsAsTheRightCall)
			sc.Step(`^a classifying node whose contract carries a fixed framing$`, st.aClassifyingNodeWhoseContractCarriesAFixedFraming)
			sc.Step(`^it escalates$`, st.itEscalates)
			sc.Step(`^the framing is part of what was sent, because model and prompt ship as one unit$`, st.theFramingIsPartOfWhatWasSent)
			sc.Step(`^the framing shown is the device's real production framing, not a paraphrase$`, st.theFramingShownIsTheRealProductionFraming)
			sc.Step(`^a completed escalation with a model answer$`, st.aCompletedEscalationWithAModelAnswer)
			sc.Step(`^the owner changes where findings are routed$`, st.theOwnerChangesWhereFindingsAreRouted)
			sc.Step(`^the model answer is unchanged$`, st.theModelAnswerIsUnchanged)
			sc.Step(`^only the destination of the finding changes$`, st.onlyTheDestinationChanges)
			sc.Step(`^a classifying node that names the reading itself$`, st.aClassifyingNodeThatNamesTheReadingItself)
			sc.Step(`^no escalation is drawn$`, st.noEscalationIsDrawn)
			sc.Step(`^nothing was asked of any band$`, st.nothingWasAskedOfAnyBand)
			sc.Step(`^every station for the band is cooling or absent$`, st.everyStationForTheBandIsCoolingOrAbsent)
			sc.Step(`^a board escalates$`, st.aBoardEscalates)
			sc.Step(`^the session is drawn as refused, with the reason$`, st.theSessionIsDrawnAsRefusedWithTheReason)
			sc.Step(`^it is not drawn as though a model answered$`, st.itIsNotDrawnAsThoughAModelAnswered)
			// 4. authority is unchanged
			sc.Step(`^any session drawn on the Edge$`, st.anySessionDrawnOnTheEdge)
			sc.Step(`^a receipt exists for it$`, st.aReceiptExistsForIt)
			sc.Step(`^the Edge view created no traffic of its own$`, st.theEdgeViewCreatedNoTrafficOfItsOwn)
			sc.Step(`^the owner looks at the Edge$`, st.theOwnerLooksAtTheEdge)
			sc.Step(`^nothing is dispatched$`, st.nothingIsDispatched)
			sc.Step(`^no wallet is touched$`, st.noWalletIsTouched)
			sc.Step(`^a consumer session against a paid band$`, st.aConsumerSessionAgainstAPaidBand)
			sc.Step(`^it is subject to the same spend limits as any relay$`, st.itIsSubjectToTheSameSpendLimits)
			sc.Step(`^exceeding them refuses the session, exactly as it does today$`, st.exceedingThemRefusesTheSession)
			sc.Step(`^a device action with no valid grant$`, st.aDeviceActionWithNoValidGrant)
			sc.Step(`^it is refused$`, st.itIsRefused)
			sc.Step(`^the Edge draws the refusal, not an action$`, st.theEdgeDrawsTheRefusalNotAnAction)
			sc.Step(`^traffic belonging to a different account$`, st.trafficBelongingToADifferentAccount)
			sc.Step(`^it does not appear on this Edge$`, st.itDoesNotAppearOnThisEdge)
			sc.Step(`^nothing about it is inferable from what is drawn$`, st.nothingAboutItIsInferable)
			// 5. the view holds up
			sc.Step(`^(\d+) sessions in flight across (\d+) participants$`, st.nSessionsAcrossMParticipants)
			sc.Step(`^the Edge is viewed at (\d+) columns$`, st.theEdgeIsViewedAtColumns)
			sc.Step(`^no line exceeds the width$`, st.noLineExceedsTheWidth)
			sc.Step(`^the busiest edge shows a count rather than 50 overlapping marks$`, st.theBusiestEdgeShowsACount)
			sc.Step(`^a session to a station reachable only through a relay$`, st.aSessionThroughARelay)
			sc.Step(`^it is drawn travelling participant to relay to station$`, st.itIsDrawnParticipantRelayStation)
			sc.Step(`^the relay is drawn as its own node, so the hop is visible$`, st.theRelayIsDrawnAsItsOwnNode)
			sc.Step(`^NO_COLOR is set$`, st.noColorIsSetForSessions)
			sc.Step(`^a session, an escalation and a refusal remain distinguishable by glyph alone$`, st.sessionEscalationAndRefusalStayDistinct)
			sc.Step(`^sessions are being drawn$`, st.sessionsAreBeingDrawn)
			sc.Step(`^a consumer relays a request$`, st.aConsumerRelaysARequest)
			sc.Step(`^the relay completes at its normal speed$`, st.theRelayCompletesAtNormalSpeed)
			sc.Step(`^nothing about the relay waited on the view$`, st.nothingAboutTheRelayWaitedOnTheView)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/edge/sessions.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the Edge sessions scenarios failed")
	}
}
