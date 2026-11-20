package tui

// The ABOUT block at the top of the [1] TUNE IN station log (features/details/tui_about.feature):
// what the model is for, in RogerAI's words for its own models or the stations' own words,
// before the table of who serves it. The lines are internal/about's, so `roger models <id>`
// prints the same ones.

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v7/internal/about"
	"rogerai.fm/roger/v7/internal/catalog"
	"rogerai.fm/roger/v7/internal/protocol"
)

const (
	aboutMaxRows   = 10 // the block never pushes the station table off a normal screen
	aboutFieldRows = 3  // a long field shows this many rows, then "…"
	aboutNarrow    = 38 // a text width below a 40-column terminal's: only the summary is shown
	noDescription  = "no description yet - the station has not said what this model is for"
	notVerifiedRow = "weights not verified - any station can use this model name"
)

// foldAbout fills a band's details and facts from its stations (protocol.FoldDetails, the
// /market rule), re-canonicalizing every string it reads.
func foldAbout(all []offer) (protocol.Details, string, string, string) {
	ballots := make([]protocol.Ballot, len(all))
	for i, o := range all {
		ballots[i] = protocol.Ballot{T: o.Taxonomy, Online: o.Online, Curated: o.Curated}
	}
	d := protocol.FoldDetails(ballots)
	tuning, base, license := about.Facts(ballots, d)
	return d, tuning, base, license
}

// siblings are the other on-air models on the dial built on the same base.
func (m model) siblings(bd band) []string {
	key := about.SameBase(bd.base)
	if key == "" {
		return nil
	}
	seen := map[string]bool{bd.model: true}
	var out []string
	for _, b := range m.bands {
		if b.online && !seen[b.model] && about.SameBase(b.base) == key {
			seen[b.model] = true
			out = append(out, b.model)
		}
	}
	sort.Strings(out)
	return out
}

// aboutRow is one row of the block: a label (padded) and a value, or a sentence.
type aboutRow struct {
	label, value string
	heading      bool
}

func (r aboutRow) plain() string { return about.Pad(r.label) + r.value }

// wrapText breaks s into rows of at most width display columns at spaces (wordWrap),
// hard-breaking a word wider than a row first. maxRows > 0 caps the rows, the last ending in "…".
func wrapText(s string, width, maxRows int) []string {
	width = max(width, 1)
	if lipgloss.Width(s) <= width { // fits: kept exactly, its spacing included
		return []string{s}
	}
	var words []string
	for _, w := range strings.Fields(s) {
		for lipgloss.Width(w) > width {
			r, n := []rune(w), 0
			for n < len(r) && lipgloss.Width(string(r[:n+1])) <= width {
				n++
			}
			n = max(n, 1) // a rune wider than the row still moves the loop on
			words, w = append(words, string(r[:n])), string(r[n:])
		}
		words = append(words, w)
	}
	rows := wordWrap(strings.Join(words, " "), width)
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
		last := []rune(rows[maxRows-1])
		for len(last) > 0 && lipgloss.Width(string(last))+1 > width {
			last = last[:len(last)-1]
		}
		rows[maxRows-1] = strings.TrimRight(string(last), " ") + "…"
	}
	return rows
}

// aboutBlock is the ABOUT block for a band at width w (the block's own text width), at most
// maxRows rows; nil when there is nothing to say.
func (m model) aboutBlock(bd band, w, maxRows int) []aboutRow {
	d := bd.about
	tax := bandTaxonomyDetail(bd)
	if d.Empty() && len(tax) == 0 && bd.tuning == "" && bd.base == "" && bd.license == "" {
		return nil
	}
	head := "ABOUT"
	if h := about.Heading(d); h != "" {
		head += "  " + h
	}
	if d.Source == protocol.SourceRogerAI && bd.lineage > 0 {
		head += "  " + glyphConf + " verified lineage"
	}
	lines := about.Lines(about.Model{ID: bd.model, Modality: bd.modality, Vision: bd.vision,
		Tuning: bd.tuning, BaseModel: bd.base, License: bd.license, Details: d, Siblings: m.siblings(bd)}, catalog.PageDisplay)
	build := func(fieldRows int) []aboutRow {
		rows := []aboutRow{{value: head, heading: true}}
		if w < aboutNarrow { // a narrow terminal: the sentence, and where the rest is
			for _, r := range wrapText(d.Summary, w, fieldRows) {
				rows = append(rows, aboutRow{value: r})
			}
			return append(rows, aboutRow{value: "more at wider width"})
		}
		if d.Source == protocol.SourceRogerAI && bd.lineage == 0 {
			rows = append(rows, aboutRow{value: notVerifiedRow})
		}
		for _, l := range lines {
			vw := w - about.LabelWidth
			if l.Label == "" {
				vw = w
			}
			for i, r := range wrapText(l.Value, vw, fieldRows) {
				label := l.Label
				if i > 0 && label != "" {
					label = strings.Repeat(" ", about.LabelWidth) // continuation sits under the value
				}
				rows = append(rows, aboutRow{label: label, value: r})
			}
		}
		if d.Empty() {
			rows = append(rows, aboutRow{value: noDescription})
		}
		for _, t := range tax {
			for _, r := range wrapText(t, w, fieldRows) {
				rows = append(rows, aboutRow{value: r})
			}
		}
		return rows
	}
	rows := build(aboutFieldRows)
	for fr := aboutFieldRows - 1; len(rows) > maxRows && fr >= 1; fr-- {
		rows = build(fr)
	}
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

// aboutRows is the block as plain text, one string per row.
func (m model) aboutRows(bd band, w int) []string {
	var out []string
	for _, r := range m.aboutBlock(bd, w, aboutMaxRows) {
		out = append(out, r.plain())
	}
	return out
}

// renderAbout styles the block: the heading in the brand color, labels dim, values plain.
func renderAbout(rows []aboutRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		switch {
		case r.heading:
			out[i] = stBrand.Render("ABOUT") + stDim.Render(strings.TrimPrefix(r.value, "ABOUT"))
		case r.label == "":
			out[i] = r.value
		default:
			out[i] = stDim.Render(about.Pad(r.label)) + r.value
		}
	}
	return out
}
