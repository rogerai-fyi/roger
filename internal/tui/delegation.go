package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"rogerai.fm/roger/v6/internal/glyphs"
	"rogerai.fm/roger/v6/internal/harness"
)

// delegation.go - WATCHING THE SUBAGENTS.
//
// FOUNDER 2026-08-21: "show delegation and monitoring/status of it neatly and
// beautifully under the ask footer". A delegation used to be a `delegate` card that sat
// there with no sign of life for as long as the child took - the operator could not
// tell working from hung, which is the same complaint that produced the working line.
//
// THE DESIGN CONSTRAINT, and why the strip looks like this. The readout under the
// composer is a FIXED two-row slot; that fixed height is the whole reason the composer
// stopped moving between turns. A delegation panel that grew a row per child would put
// the movement straight back. So the strip does not add rows - while children are
// running it REPLACES the carrier sweep, which is the honest trade: both rows are
// proof-of-life, and naming what each child is doing says strictly more than a sweep
// that only says "something is happening".
//
// It reads as a channel strip on a desk - one lamp per child, its number, and the verb
// it is on - because that is what it is: several stations working one question, which
// is the same picture the mesh deck draws.

// delegateState is one live subagent as the transcript surface sees it.
type delegateState struct {
	Label string // "#1"
	Doing string // the verb: "reading", "searching", "thinking"
	Steps int
	Done  bool
}

// noteDelegateEvent folds one forwarded child event into the live view. Called for
// every event carrying an Agent label.
func (m *model) noteDelegateEvent(label string, e agentEventMsg) {
	if m.agentDelegates == nil {
		m.agentDelegates = map[string]*delegateState{}
	}
	d, ok := m.agentDelegates[label]
	if !ok {
		d = &delegateState{Label: label, Doing: "starting"}
		m.agentDelegates[label] = d
	}
	if e.AgentDone {
		d.Done = true
		return
	}
	if e.Step > 0 {
		d.Steps = e.Step
	}
	switch e.Kind {
	case harness.EventToolCall:
		d.Doing = delegateVerb(e.Tool)
	case harness.EventToolResult:
		// Between tools the child is thinking about what it just read. Saying so beats
		// leaving the last tool's verb up, which would read as still running.
		d.Doing = "thinking"
	case harness.EventAssistant, harness.EventFinal:
		d.Doing = "reporting"
	}
}

// delegateVerb turns a tool name into what the child is DOING, because "read_file" is
// the machine's word and a status line is read by a person.
func delegateVerb(tool string) string {
	switch tool {
	case "read_file":
		return "reading"
	case "list_dir":
		return "listing"
	case "web_search":
		return "searching"
	case "web_fetch":
		return "fetching"
	default:
		if tool == "" {
			return "working"
		}
		return tool
	}
}

// liveDelegates returns the children still running, in label order so the strip does
// not reshuffle itself while the operator is reading it.
func (m model) liveDelegates() []*delegateState {
	out := make([]*delegateState, 0, len(m.agentDelegates))
	for _, d := range m.agentDelegates {
		if !d.Done {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// delegationStrip renders the live children as ONE row, or "" when none are running
// (the carrier keeps the row then). Width-aware: it drops each child's verb before it
// drops a child, because knowing THREE are running matters more than knowing what the
// third one is doing, and it never wraps - a wrapped strip would take the row the pin
// depends on.
func (m model) delegationStrip(w int) string {
	live := m.liveDelegates()
	if len(live) == 0 {
		return ""
	}
	lamp := lampStyle(roleDial).Render(glyphs.Fold("◉"))
	head := stDim.Render("  delegated ")
	if len(live) > 1 {
		head = stDim.Render(fmt.Sprintf("  %d delegated ", len(live)))
	}
	full := make([]string, 0, len(live))
	terse := make([]string, 0, len(live))
	for _, d := range live {
		full = append(full, lamp+stKey.Render(d.Label)+stDim.Render(" "+d.Doing))
		terse = append(terse, lamp+stKey.Render(d.Label))
	}
	sep := stDim.Render(" · ")
	for _, cells := range [][]string{full, terse} {
		line := head + strings.Join(cells, sep)
		if lipgloss.Width(line) <= w {
			return line
		}
	}
	// Even the terse form does not fit: say how many and stop, rather than truncating
	// mid-label into something that reads like a different agent.
	return truncVisible(head+stDim.Render("…"), w)
}

// delegationReceiptLine summarises what this turn's subagents cost, for the transcript,
// or "" when the turn delegated to nobody.
//
// It is the closing half of the live strip: the strip says who is working, this says
// what they did. Without it the per-agent receipts the harness keeps would never reach
// a human - and attribution nobody can see is bookkeeping for its own sake.
//
// The numbers come from the HARNESS's receipt, not from what the surface happened to
// observe. A surface that counted the events it saw would undercount a child whose
// events were dropped while the operator was scrolled away, and would then disagree
// with the bill.
func (m model) delegationReceiptLine() string {
	if m.agent == nil || m.agent.loop == nil {
		return ""
	}
	rc := m.agent.loop.TurnReceipt()
	if len(rc.Children) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rc.Children))
	for _, c := range rc.Children {
		cell := c.Agent + " " + plural(c.Steps, "step")
		if !c.Complete {
			// A child that did not finish is named as such, here, rather than folded
			// silently into a total that would then read as final.
			cell += " · unfinished"
		}
		parts = append(parts, cell)
	}
	head := fmt.Sprintf("%d delegated", len(rc.Children))
	tail := ""
	if rc.Searches > 0 || rc.Fetches > 0 {
		tail = stDim.Render("  ·  " + plural(rc.Searches, "search") + ", " +
			plural(rc.Fetches, "fetch") + " this turn")
	}
	return stDim.Render("  ⋮ "+head+" · ") + stDim.Render(strings.Join(parts, " · ")) + tail
}
