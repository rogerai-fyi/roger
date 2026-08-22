package tui

import (
	"strconv"
	"strings"

	"rogerai.fm/roger/v6/internal/glyphs"
)

// toolrun.go - TOOL CALLS AS DATA.
//
// The transcript used to store tool machinery as pre-rendered strings, and everything
// that later needed a fact about a call had to read it back out of its own formatting.
// That produced foldToolName, which sniffed a tool name out of a line we had formatted
// minutes earlier by looking for ⚙ and ✓ glyphs - and duly mistook the glyph itself for
// the name, and then mistook words out of a fetched Reddit page for three more names.
// Both bugs were unreachable from the type system because there was no type.
//
// So a call is a RECORD now. The transcript keeps ordering by holding a reference to it
// (toolRefMark + index); the record holds the facts; rendering happens at display time
// from the facts. Three things follow, in increasing order of how much they matter:
//
//   1. foldToolName is gone. Names come from the field called Name.
//   2. Every call has a stable IDENTITY (its index). That is the precondition for
//      running calls in parallel: interleaved pre-rendered strings cannot be told
//      apart, indexed records can.
//   3. The browser console can consume the same records rather than reimplementing
//      how a tool call looks. One definition of a call, two surfaces.

// toolStatus is where a call has got to. A call is born running and settles once.
type toolStatus int

const (
	toolRunning toolStatus = iota
	toolOK
	toolFailed
	toolDenied
)

// toolRun is one tool call and its outcome. Pure data: no styling, no glyphs, nothing
// that assumes a terminal - which is what lets the console render the same record.
type toolRun struct {
	Name     string // the tool, e.g. "web_fetch"
	Arg      string // the human arg summary, e.g. the path or URL
	Status   toolStatus
	Detail   string   // the settled tail: "ok · 132 bytes", or the error's first line
	Approved bool     // the operator confirmed a side-effecting call
	Preview  []string // the result preview, shown under `d`
}

// Done reports whether the call has settled, which is what separates "2 tool calls" as
// a count of RUNS from a count of transcript rows.
func (t toolRun) Done() bool { return t.Status != toolRunning }

// toolRefMark tags a transcript line that is a REFERENCE to a toolRun rather than text.
// Same C0-byte discipline as the other marks: it survives ansi.Strip, so every path
// that leaves the TUI has to resolve or strip it explicitly rather than leaking it.
const toolRefMark = "\x1f"

func toolRef(i int) string { return toolRefMark + strconv.Itoa(i) }

// toolRefIndex resolves a reference line to its index, or -1 if the line is not one.
func toolRefIndex(line string) int {
	if !strings.HasPrefix(line, toolRefMark) {
		return -1
	}
	i, err := strconv.Atoi(line[len(toolRefMark):])
	if err != nil || i < 0 {
		return -1
	}
	return i
}

// render paints one settled or running call as its transcript card. The ONLY place a
// tool call becomes glyphs, so the shape can change here without anything downstream
// having to re-parse it.
func (t toolRun) render() string {
	var mark, tail string
	switch t.Status {
	case toolDenied:
		mark, tail = stRed.Render("  ✕ "), stEmber.Render("denied")
	case toolFailed:
		mark, tail = stRed.Render("  ✕ "), stEmber.Render(t.Detail)
	case toolOK:
		mark, tail = stLive.Render("  ✓ "), stDim.Render(t.Detail)
	default:
		gear := "⚙"
		if glyphs.ASCII() {
			gear = "*"
		}
		mark, tail = "  "+lampStyle(roleDial).Render("◐")+stDim.Render(" "+gear+" "), stDim.Render("running")
	}
	card := mark + stDim.Render(t.Name)
	if t.Arg != "" {
		card += stDim.Render("   " + t.Arg)
	}
	if t.Approved && t.Status != toolDenied {
		card += stDim.Render(" · approved")
	}
	return card + stDim.Render(" · ") + tail
}
