package tui

// SERVE MODEL on a member (features/edge/remote_model.feature): pick what a machine on the Edge
// should serve, without leaving the screen or walking to it. The member judges, fetches and proves;
// this picker only names the model - from the ids the member reported it has, or an exact
// owner/repo:QUANT it will fetch from its own source - and asks first.
//
// On THIS machine the same key opens MODELS, which is the same flow done here.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v7/internal/edge"
)

type edgeServeModelState struct {
	open   bool
	node   string // the member's id
	name   string // what the owner calls it
	have   []string
	sel    int    // index into have; len(have) is the "type an id" row
	buf    string // the exact id being typed
	asking string // the id awaiting y/N
}

type edgeServeModelMsg struct {
	name, says string
	err        error
}

// edgeStartServeModel opens the picker for the selected unit.
func (m *model) edgeStartServeModel() (tea.Model, tea.Cmd) {
	it, _ := m.edgeSelectedNet()
	if it.machine || it.self {
		nm, cmd := m.enterModels()
		return nm, cmd
	}
	if !it.member || it.node == nil {
		m.status = stDim.Render("Serve Model acts on a machine - select one first")
		return m, nil
	}
	if m.hooks.EdgeServeModel == nil {
		m.status = stEmber.Render("this build cannot ask a member to serve - run `roger model serve <id> --on " + it.name + "`")
		return m, nil
	}
	if !edge.Controllable(*it.node) {
		m.status = stEmber.Render(it.name + " has no face this machine can dial, so it cannot be asked to serve - it can only be heard")
		return m, nil
	}
	if !edge.Declares(*it.node, edge.ServeControl) {
		// Never a dead end: say which switch, and where it lives.
		m.status = stEmber.Render(it.name + " has not turned on remote serving control - run `roger edge serve-control on` THERE")
		return m, nil
	}
	s := edgeServeModelState{open: true, node: it.id, name: it.name}
	if mm := edge.ModelOf(*it.node); mm != nil {
		s.have = append(s.have, mm.Have...)
	}
	m.edge.serveModel = s
	return m, nil
}

func (m *model) onEdgeServeModelKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.edge.serveModel
	if s.asking != "" {
		id := s.asking
		s.asking = ""
		if k.String() != "y" && k.String() != "Y" {
			m.status = stDim.Render("nothing changed on " + s.name)
			return m, nil
		}
		s.open = false
		serve, node, name := m.hooks.EdgeServeModel, s.node, s.name
		m.status = stDim.Render("asking " + name + " to serve " + id + "…")
		return m, func() tea.Msg {
			says, err := serve(node, id)
			return edgeServeModelMsg{name: name, says: says, err: err}
		}
	}
	typing := s.sel == len(s.have)
	switch k.Type {
	case tea.KeyEsc:
		s.open = false
		return m, nil
	case tea.KeyUp:
		if s.sel > 0 {
			s.sel--
		}
		return m, nil
	case tea.KeyDown:
		if s.sel < len(s.have) {
			s.sel++
		}
		return m, nil
	case tea.KeyEnter:
		id := ""
		if typing {
			id = strings.TrimSpace(s.buf)
			if _, _, ok := edge.ExactIndexID(id); !ok {
				m.status = stEmber.Render("name it exactly as owner/repo:QUANT - a member searches nowhere for a model")
				return m, nil
			}
		} else {
			id = s.have[s.sel]
		}
		s.asking = id
		return m, nil
	case tea.KeyBackspace, tea.KeyDelete:
		if typing && s.buf != "" {
			s.buf = s.buf[:len(s.buf)-1]
		}
		return m, nil
	case tea.KeyRunes:
		if typing && len(s.buf) < 200 {
			s.buf += string(k.Runes)
		}
		return m, nil
	}
	return m, nil
}

func (m model) edgeServeModelPanel(line func(string)) {
	s := m.edge.serveModel
	if s.asking != "" {
		line("  " + stKey.Render("ASK "+strings.ToUpper(s.name)+" TO SERVE "+s.asking+"?"))
		line("  " + stDim.Render(s.name+" judges it against its own memory, fetches it if it must, and keeps serving"))
		line("  " + stDim.Render("what it serves now until the new one answers."))
		line("  " + lampStyle(roleLive).Render("y") + " ask   " + stKey.Render("any other key") + " no " + stDim.Render("(⏎ alone is no)"))
		return
	}
	line("  " + stKey.Render("WHAT SHOULD "+strings.ToUpper(s.name)+" SERVE?") + stDim.Render("  ↑↓ pick · ⏎ choose · esc cancel"))
	if len(s.have) == 0 {
		line("  " + stDim.Render("it reports no models on its disk yet"))
	}
	for i, id := range s.have {
		cur := "  "
		if i == s.sel {
			cur = lampStyle(roleLive).Render("▶ ")
		}
		line("  " + cur + id + stDim.Render("  on its disk"))
	}
	cur := "  "
	if s.sel == len(s.have) {
		cur = lampStyle(roleLive).Render("▶ ")
	}
	buf := s.buf
	if s.sel == len(s.have) {
		buf += "_"
	}
	line("  " + cur + stDim.Render("fetch exactly: ") + stEmber.Render(buf) + stDim.Render("  owner/repo:QUANT, from its own source"))
}

// onEdgeServeModelMsg reports the member's answer in its own words.
func (m model) onEdgeServeModelMsg(msg edgeServeModelMsg) model {
	if msg.err != nil {
		m.status = stEmber.Render(msg.err.Error())
		return m
	}
	m.status = stLive.Render(msg.name + ": " + msg.says)
	m.refreshEdge()
	return m
}
