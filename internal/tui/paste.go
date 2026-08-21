package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// paste.go - LARGE PASTES BECOME A PLACEHOLDER.
//
// FOUNDER 2026-08-21: "when pasting a large amount of text into the tui, it breaks the
// text box". It did: the composer is a soft-wrapping textarea capped at six rows, and a
// 300-line paste is 300 rows of content trying to live in it. The input stopped being
// legible and the operator could no longer see what they were about to send.
//
// So a big paste is HELD, not shown. The composer gets one line naming what arrived -
// `[Pasted text #1 +247 lines]` - and the real content is expanded back at submit time,
// so the model receives exactly what was pasted and the operator keeps a usable input.
//
// SMALL pastes stay inline. A URL, a path, a two-line snippet are all things you want
// to SEE before sending, and hiding them behind a placeholder would be strictly worse
// than the bug this fixes.

const (
	// pasteMinLines / pasteMinBytes are where a paste stops being something you read in
	// the box and starts being cargo. Four lines is about where a wrapped composer
	// begins to crowd out the transcript above it; 400 bytes catches the single
	// enormous line (a JSON blob, a base64 key) that no line count would.
	pasteMinLines = 4
	pasteMinBytes = 400
)

// pasteRef matches a placeholder so submit can expand it. Anchored on the exact shape
// this file writes, and the NUMBER is what carries meaning - typing something that
// looks like one expands nothing, because it has no stored content behind it.
var pasteRef = regexp.MustCompile(`\[Pasted text #(\d+)[^\]]*\]`)

// bigPaste reports whether pasted text should be held rather than shown inline.
func bigPaste(s string) bool {
	return strings.Count(s, "\n")+1 >= pasteMinLines || len(s) >= pasteMinBytes
}

// holdPaste stores the text and returns the placeholder to show in its place. The
// number is 1-based and stable for the session, so two pastes read as #1 and #2 rather
// than both claiming to be the first.
func (m *model) holdPaste(text string) string {
	m.agentPastes = append(m.agentPastes, text)
	n := len(m.agentPastes)
	// Count CONTENT lines: a paste that ends in a newline has a trailing empty line
	// that is not a line of anything, and "+248" for 247 lines is the kind of small
	// wrongness that makes a reader stop trusting the rest of the number.
	lines := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
	if lines >= pasteMinLines {
		return fmt.Sprintf("[Pasted text #%d +%d lines]", n, lines)
	}
	// A single enormous line: lines would read "+1", which says nothing. Size does.
	return fmt.Sprintf("[Pasted text #%d %s]", n, humanSize(len(text)))
}

// expandPastes puts the held text back before the prompt is sent, so the model receives
// what was actually pasted. A placeholder with no stored content behind it (the user
// typed something that looks like one, or edited the number) is left exactly as typed -
// substituting nothing there would silently delete what they wrote.
func (m model) expandPastes(s string) string {
	if len(m.agentPastes) == 0 {
		return s
	}
	return pasteRef.ReplaceAllStringFunc(s, func(ref string) string {
		g := pasteRef.FindStringSubmatch(ref)
		if len(g) < 2 {
			return ref
		}
		n, err := strconv.Atoi(g[1])
		if err != nil || n < 1 || n > len(m.agentPastes) {
			return ref
		}
		return m.agentPastes[n-1]
	})
}

// humanSize renders a byte count for the placeholder.
func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
