package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/protocol"
)

// Regression (slice-4 review): the CATEGORY column pushed SHARE rows past the terminal at
// widths between the dense cut-off (88) and ~110, so unselected rows wrapped and broke the
// table. No line of the SHARE tab may be wider than the terminal, at any width.
func TestShareTableNeverOverflowsTheTerminal(t *testing.T) {
	for _, w := range []int{88, 100, 110} {
		m := browseSeed(w)
		m.ctrl.SetRows([]node.ShareRow{
			{Model: "nomic-embed-text-v1.5-very-long-model-name", Upstream: "http://127.0.0.1:1/v1/chat/completions",
				Inferred: protocol.Taxonomy{Category: "retrieval", Subcategory: "embeddings", TaxonomySource: protocol.SourceInferred}},
			{Model: "qwen3-vl-8b", Upstream: "http://127.0.0.1:1/v1/chat/completions"},
		})
		m.syncShareCache()
		m.mode = modeShare
		for _, compact := range []bool{false, true} {
			m.compact = compact
			for _, line := range strings.Split(m.shareView(w), "\n") {
				if n := lipgloss.Width(line); n > w {
					t.Errorf("width %d (compact %v): a SHARE line is %d cells wide: %q", w, compact, n, stripANSI(line))
				}
			}
		}
	}
}
