package tui

// MODELS AS ROWS OF SHARE (features/serving/local_models.feature §9, founder ruling 2026-10-01: "no
// new tab ... something under [2] SHARE that is visually obvious ... as a row"). After the rows of
// what is detected, SHARE lists what roger can do itself: models on this disk that nothing serves, a
// download in progress, and - always last - a row to get a model that fits this machine. Each row's
// action is the MODELS screen's own (models.go): the serve question, the fetch, the search.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v6/internal/modelview"
)

type shareExtraKind int

const (
	extraDisk  shareExtraKind = iota // on this disk, served by nothing
	extraFetch                       // a download in progress (or one that failed)
	extraGet                         // + get a model that fits this machine
)

type shareExtra struct {
	kind shareExtraKind
	row  modelview.Row // extraDisk
	idx  int           // its index in the models view, for the serve question
}

// shareExtras are the rows SHARE adds after the detected ones. None when this build cannot serve
// models itself.
func (m model) shareExtras() []shareExtra {
	if m.hooks.Models.View == nil {
		return nil
	}
	var out []shareExtra
	v := m.mdl.view
	for i, r := range v.Models {
		if !r.Serving { // a served one is already a detected row of its own
			out = append(out, shareExtra{kind: extraDisk, row: r, idx: i})
		}
	}
	if j := v.Job; j != nil && j.State != "done" {
		out = append(out, shareExtra{kind: extraFetch})
	}
	return append(out, shareExtra{kind: extraGet})
}

// shareRowCount is every row the SHARE cursor can rest on.
func (m model) shareRowCount() int { return len(m.shareRows) + len(m.shareExtras()) }

// shareExtraAt is the extra row under the cursor, if the cursor is past the detected rows.
func (m model) shareExtraAt(cursor int) (shareExtra, bool) {
	i := cursor - len(m.shareRows)
	ex := m.shareExtras()
	if i < 0 || i >= len(ex) {
		return shareExtra{}, false
	}
	return ex[i], true
}

// onShareExtraEnter is Enter on one of roger's own rows.
func (m *model) onShareExtraEnter(e shareExtra) (tea.Model, tea.Cmd) {
	switch e.kind {
	case extraDisk:
		// The serve question, asked here in SHARE; the answer serves it in the background.
		m.mdl.pane, m.mdl.sel = modelsDisk, e.idx
		return m, m.modelsEnter()
	case extraFetch:
		nm, cmd := m.enterModels()
		return nm, cmd
	}
	// + get a model: the search and the fit view, inside SHARE, typing at once.
	nm, cmd := m.enterModels()
	nm.mdl.typing, nm.mdl.query = true, ""
	nm.status = stDim.Render("SHARE › GET A MODEL - type what you are looking for, ⏎ to search")
	return nm, cmd
}

// shareExtrasView draws roger's own rows under the detected ones, in the table's own grammar: the
// cursor bar, a lamp-width gutter, the model cell, a status word.
func (m model) shareExtrasView(w, nameW, tableW int) string {
	ex := m.shareExtras()
	if len(ex) == 0 {
		return ""
	}
	var b strings.Builder
	v := m.mdl.view
	for i, e := range ex {
		sel := len(m.shareRows)+i == m.shareCursor
		var plain string
		switch e.kind {
		case extraDisk:
			src := "roger · " + modelsDash(e.row.Quant) + " · ⏎ serve here"
			if !v.CanServe {
				src = "roger cannot serve here"
			}
			plain = fmt.Sprintf("  %-*s  %-9s  %s", nameW, truncVisible(e.row.Band, nameW), "ON DISK", src)
		case extraFetch:
			j := v.Job
			bar := modelsBar(j.Done, j.Total, 10)
			info := modelsBytes(j.Done) + "/" + modelsBytes(j.Total)
			switch j.State {
			case "fetching":
				if j.Rate > 0 {
					info += " · " + modelsBytes(int64(j.Rate)) + "/s"
				}
				if j.RemainingS > 0 && j.Done*50 >= j.Total {
					info += " · " + modelsSecs(j.RemainingS)
				}
			case "failed":
				info = "failed: " + j.Err
			case "interrupted":
				info += " · interrupted, fetch again to resume"
			}
			plain = fmt.Sprintf("↓ %-*s  %s %s", nameW, truncVisible(strings.TrimSuffix(j.File, ".gguf"), nameW), bar, info)
		case extraGet:
			mem := "search HuggingFace"
			if v.Budget > 0 {
				mem = modelsBytes(v.Budget) + " " + v.Pool + " · search HuggingFace"
			}
			plain = "+ Get a model that fits this machine   " + mem
		}
		if sel {
			b.WriteString(selCarat(true) + rowSel(true, truncVisible(plain, tableW), tableW) + "\n")
			continue
		}
		style := stDim
		if e.kind == extraGet {
			style = stKey // the way to get a model is always visible, never a hidden key
		}
		b.WriteString(selCarat(false) + style.Render(truncVisible(plain, tableW)) + "\n")
	}
	return b.String()
}

// shareExtraDetail is the detail banner for one of roger's own rows.
func (m model) shareExtraDetail(e shareExtra) string {
	switch e.kind {
	case extraDisk:
		if !m.mdl.view.CanServe {
			return m.mdl.view.Runtime
		}
		return e.row.ID + " is on this disk and nothing serves it - ⏎ serves it here with roger itself, in the background"
	case extraFetch:
		return "a model is being fetched - ⏎ shows it in full; it keeps going while you do anything else"
	}
	return "find a model that fits this machine and serve it with nothing else installed - ⏎ to search"
}

// servedByRoger reports whether a detected row is a model roger itself serves, and how to say so.
func (m model) servedByRoger(row shareRow) (string, bool) {
	if m.mdl.view.Serving == nil {
		return "", false
	}
	for _, sv := range append(append([]modelview.Serving{}, m.mdl.view.Also...), *m.mdl.view.Serving) {
		if sv.Band == row.model && strings.HasPrefix(row.upstream, strings.TrimSuffix(sv.Address, "/v1")) {
			return fmt.Sprintf("served by roger itself · %s · %s · %d context", sv.ID, modelsDash(sv.Quant), sv.Ctx), true
		}
	}
	return "", false
}

// shareAskView is a serve question asked from a SHARE row: y answers, anything else is no.
func (m model) shareAskView(w int) string {
	a := m.mdl.ask
	if a == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n  " + stKey.Render(strings.ToUpper(a.title[:1])+a.title[1:]) + "\n")
	for _, l := range a.lines {
		b.WriteString("    " + truncVisible(l, w-6) + "\n")
	}
	b.WriteString("  " + a.answers() + "\n")
	return b.String()
}
