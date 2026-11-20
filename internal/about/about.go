// Package about words a model's ABOUT block (features/details): the lines the TUI station log
// and `roger models <id>` print, so the terminal app and the CLI say the same thing in the
// same order. Pure: same model in, same lines out. The web surfaces mirror these words in
// their own scripts and are held to them by web/test.
package about

import (
	"fmt"
	"strings"

	"rogerai.fm/roger/v7/internal/protocol"
)

// Model is what one band of stations says about its model.
type Model struct {
	ID                         string
	Modality                   string // chat (or ""), tts, stt
	Vision                     bool   // a station declared the vision capability
	Tuning, BaseModel, License string
	Details                    protocol.Details
	Siblings                   []string // other on-air models on the same base, sorted
}

// Line is one labelled row; an empty Label is a sentence of its own (the summary).
type Line struct{ Label, Value string }

// LabelWidth is the column the values start at.
const LabelWidth = 10

// maxSiblings is how many same-base models are named before "+N more".
const maxSiblings = 4

// Heading names whose words these are, "" when there are none.
func Heading(d protocol.Details) string {
	switch {
	case d.Empty():
		return ""
	case d.Source == protocol.SourceRogerAI:
		return "RogerAI's description of this model"
	}
	n := "stations"
	if d.Stations == 1 {
		n = "station"
	}
	return fmt.Sprintf("the station's own words · %d of %d %s", d.DeclaredBy, d.Stations, n)
}

// Kind is what goes in and out, and how the weights were made.
func Kind(modality string, vision bool, tuning, base string) string {
	k := "text in, text out"
	switch {
	case modality == protocol.ModalityTTS:
		k = "text in, speech out"
	case modality == protocol.ModalitySTT:
		k = "speech in, text out"
	case vision:
		k = "text and image in, text out"
	}
	switch {
	case tuning == "adapter" && base != "":
		k += "  ·  adapter on " + base
	case tuning != "":
		k += "  ·  " + tuning
	}
	return k
}

// Lines is the ABOUT block below its heading: summary, best at, not for, kind, base, same
// base, license, page. A line with nothing to say is left out. page turns a note's site path
// into what this surface shows ("" leaves the page line out).
func Lines(m Model, page func(string) string) []Line {
	d := m.Details
	var out []Line
	add := func(label, v string) {
		if v != "" {
			out = append(out, Line{label, v})
		}
	}
	add("", d.Summary)
	add("best at", d.BestAt)
	add("not for", d.NotFor)
	add("kind", Kind(m.Modality, m.Vision, m.Tuning, m.BaseModel))
	if !(m.Tuning == "adapter" && m.BaseModel != "") {
		add("base", m.BaseModel) // an adapter's base is already on the kind line
	}
	if n := len(m.Siblings); n > 0 {
		s := m.Siblings
		more := ""
		if n > maxSiblings {
			s, more = s[:maxSiblings], fmt.Sprintf(" +%d more", n-maxSiblings)
		}
		add("same base", strings.Join(s, ", ")+more)
	}
	add("license", m.License)
	if page != nil && d.Page != "" {
		add("page", page(d.Page))
	}
	return out
}

// Pad renders a labelled line's label at LabelWidth ("best at   ").
func Pad(label string) string {
	if label == "" {
		return ""
	}
	if strings.TrimSpace(label) == "" { // a continuation row: the value column, nothing else
		return strings.Repeat(" ", LabelWidth)
	}
	if len(label) >= LabelWidth {
		return label + " "
	}
	return label + strings.Repeat(" ", LabelWidth-len(label))
}

// Facts picks a band's tuning, base model and license: those of the first station whose text
// is the folded text (so the facts and the words come from one author), else of the first
// station that states each.
func Facts(ballots []protocol.Ballot, d protocol.Details) (tuning, base, license string) {
	for _, b := range ballots {
		f := protocol.FoldDetails([]protocol.Ballot{{T: b.T, Online: true}})
		if !d.Empty() && f.Summary == d.Summary && f.BestAt == d.BestAt && f.NotFor == d.NotFor && f.Source == d.Source {
			return tuningOf(b.T), protocol.DetailsText(b.T.BaseModel, 128), protocol.DetailsText(b.T.License, 64)
		}
	}
	for _, b := range ballots {
		if tuning == "" {
			tuning = tuningOf(b.T)
		}
		if base == "" {
			base = protocol.DetailsText(b.T.BaseModel, 128)
		}
		if license == "" {
			license = protocol.DetailsText(b.T.License, 64)
		}
	}
	return tuning, base, license
}

// tuningOf is a station's tuning if it is a known one, else "": it is printed into a terminal.
func tuningOf(t protocol.Taxonomy) string {
	return protocol.Taxonomy{Tuning: t.Tuning}.Canonical("").Tuning
}

// SameBase is a canonical base-model key: siblings match on it.
func SameBase(base string) string { return strings.ToLower(protocol.DetailsText(base, 128)) }

// Block is one model's details as data: /v1/models rogerai.details and `roger models <id>
// --json`. origin resolves a curated page; declared text says how many stations agree.
func Block(d protocol.Details, ballots []protocol.Ballot, origin string) map[string]any {
	out := map[string]any{"source": d.Source}
	put := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	put("summary", d.Summary)
	put("best_at", d.BestAt)
	put("not_for", d.NotFor)
	if d.Page != "" {
		put("page", strings.TrimRight(origin, "/")+d.Page)
	}
	tuning, base, license := Facts(ballots, d)
	put("base_model", base)
	put("tuning", tuning)
	put("license", license)
	if d.Source == protocol.SourceDeclared {
		out["declared_by"], out["stations"] = d.DeclaredBy, d.Stations
	}
	return out
}
