package tui

// THE EDGE SETUP WIZARD ([3] EDGE, features/edge/onboard.feature) - set up this machine's
// Edge interactively, like a game, instead of reading CLI hints.
//
// It is a sub-state of the Edge screen, entered with `e` from the empty state (which keeps
// all its honest facts and named commands - the wizard is additive). It never re-implements
// enrollment: it drives the host's EdgeSetup hooks, which are the SAME code `roger edge`
// runs.
//
// THE HANDSHAKE is a real operation in flight, so its animation is a legitimate exception to
// the fleet's "motion only from real events" rule (topology_view.feature): it runs in THIS
// wizard's own loop (edgeSetupTickMsg), never the graph's pulse loop, starts when the enroll
// is dispatched, and stops the instant it returns. It is the radio metaphor made literal - a
// carrier sweep listening for the authority, a lock when the certificate is issued.
//
// Everything degrades: under NO_COLOR / compact (reduced motion) / a pipe / narrow widths the
// sweep becomes plain phase lines, and nothing here carries colour of its own that a glyph
// does not also carry.

import (
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/edge"
)

// EdgeSetup is the onboarding backend the wizard drives (filled by the host, cmd/rogerai/
// edgesetup.go). Each op does the real work and returns an error the wizard shows.
type EdgeSetup struct {
	DefaultName func() string
	UserKeyHex  func() string
	NewEdge     func(name string) error // this machine becomes the authority + enrolls
	Join        func(name, authorityAddr string) error
	EnrollSelf  func() error // finish: designated here but not enrolled
}

// EdgeSetupState is the half-done state the wizard reads to start at the right step.
type EdgeSetupState struct {
	Enrolled      bool
	AuthorityHere bool
	AuthorityAddr string
}

// ErrNotAllowedYet is what Join returns when the authority does not yet allow this machine:
// the wizard then shows this machine's key and the exact `allow` command. Any other error is
// shown in the backend's own words.
var ErrNotAllowedYet = errors.New("this machine is not allowed on that Edge yet")

type edgeSetupStep int

const (
	setupChoose  edgeSetupStep = iota // new vs join
	setupName                         // name this machine
	setupAddr                         // (join) the authority address
	setupRunning                      // the handshake is in flight
	setupAllow                        // (join) not allowed yet: show the key + command
	setupDone                         // success
	setupFailed                       // an error other than not-allowed
)

// edgeSetup is the wizard's whole state. Zero value = closed.
type edgeSetup struct {
	open    bool
	step    edgeSetupStep
	join    bool   // the chosen path
	choice  int    // 0 = new, 1 = join (at setupChoose)
	name    string // the name buffer
	addr    string // the authority-address buffer
	field   int    // which text field is active at a step (name/addr)
	finish  bool   // entered straight at "enroll this machine" (a missed step)
	err     string // the failure text at setupFailed
	userKey string // this machine's key, shown at setupAllow

	// the handshake animation: its own clock, bound to the operation.
	anim    bool
	frame   int
	startAt time.Time
}

// edgeSetupMinShow is how long the handshake is shown at least, so a fast (local) enroll
// does not flash by before it reads as a handshake.
const edgeSetupMinShow = 900 * time.Millisecond

// edgeSetupTickEvery paces the sweep. It is the wizard's OWN tick; it exists only while a
// handshake is animating and is never the graph's edgeAnimCmd.
const edgeSetupTickEvery = 90 * time.Millisecond

// edgeSetupDoneMsg carries an enroll op's result back to the model.
type edgeSetupDoneMsg struct {
	err  error
	kind edgeSetupStep // setupDone, setupAllow, or setupFailed
}

type edgeSetupTickMsg struct{}

func edgeSetupTick() tea.Cmd {
	return tea.Tick(edgeSetupTickEvery, func(time.Time) tea.Msg { return edgeSetupTickMsg{} })
}

// canSetup reports whether this build wired the setup backend.
func (m model) canSetup() bool { return m.hooks.EdgeSetup != nil }

// openEdgeSetup enters the wizard. If this machine has an authority but is not enrolled, it
// starts straight at enrolling this machine (the one real "finish this" gap); otherwise at
// the choice.
func (m *model) openEdgeSetup() {
	if !m.canSetup() {
		m.status = stEmber.Render("this build cannot set up an Edge here - use the commands shown")
		return
	}
	s := edgeSetup{open: true, step: setupChoose}
	if m.hooks.EdgeSetup.DefaultName != nil {
		s.name = m.hooks.EdgeSetup.DefaultName()
	}
	// Decide the finish path from the frame's own status snapshot (already read), not a fresh
	// file hook, so opening the wizard is instant on keypress.
	if st := m.edge.status; st != nil && st.AuthorityHere && !st.Enrolled {
		s.finish = true
		s.step = setupName
		s.join = false
	}
	m.edge.setup = &s
}

func (m model) edgeSetupState() EdgeSetupState {
	if m.hooks.EdgeSetupOf != nil {
		return m.hooks.EdgeSetupOf()
	}
	return EdgeSetupState{}
}

// dispatchEdgeSetup runs the chosen op off the UI thread and starts the handshake.
func (m *model) dispatchEdgeSetup() tea.Cmd {
	s := m.edge.setup
	s.step, s.anim, s.startAt, s.frame = setupRunning, true, m.edgeNow(), 0
	hooks := m.hooks.EdgeSetup
	name, addr, join, finish := s.name, s.addr, s.join, s.finish
	run := func() tea.Msg {
		var err error
		switch {
		case finish:
			err = hooks.EnrollSelf()
		case join:
			err = hooks.Join(name, addr)
		default:
			err = hooks.NewEdge(name)
		}
		switch {
		case err == nil:
			return edgeSetupDoneMsg{kind: setupDone}
		case errors.Is(err, ErrNotAllowedYet):
			return edgeSetupDoneMsg{kind: setupAllow, err: err}
		default:
			return edgeSetupDoneMsg{kind: setupFailed, err: err}
		}
	}
	return tea.Batch(edgeSetupTick(), func() tea.Msg { return run() })
}

// onEdgeSetupTick advances the sweep, and only while a handshake is in flight - the loop
// stops itself the moment the operation is no longer running.
func (m model) onEdgeSetupTick() (tea.Model, tea.Cmd) {
	s := m.edge.setup
	if s == nil || !s.anim || s.step != setupRunning {
		return m, nil // nothing in flight: do not keep ticking (topology_view.feature rule)
	}
	s.frame++
	return m, edgeSetupTick()
}

// onEdgeSetupDone lands an op's result. A success or a not-allowed is held until the minimum
// show has elapsed, so the handshake always reads as one.
func (m model) onEdgeSetupDone(msg edgeSetupDoneMsg) (tea.Model, tea.Cmd) {
	s := m.edge.setup
	if s == nil {
		return m, nil
	}
	settle := func() {
		s.anim = false
		switch msg.kind {
		case setupAllow:
			s.step, s.userKey = setupAllow, ""
			if m.hooks.EdgeSetup != nil && m.hooks.EdgeSetup.UserKeyHex != nil {
				s.userKey = m.hooks.EdgeSetup.UserKeyHex()
			}
		case setupFailed:
			s.step = setupFailed
			s.err = ""
			if msg.err != nil {
				s.err = msg.err.Error()
			}
		default:
			s.step = setupDone
			m.refreshEdge() // the fleet now has this machine; redraw behind the done panel
		}
	}
	if wait := edgeSetupMinShow - m.edgeNow().Sub(s.startAt); wait > 0 {
		return m, tea.Tick(wait, func(time.Time) tea.Msg { return msg })
	}
	settle()
	return m, nil
}

// onEdgeSetupKey drives the wizard. Returns handled=false when the key is not the wizard's,
// so onEdgeKey can fall through (it never does while the wizard is open, but the guard keeps
// the two handlers honest).
func (m *model) onEdgeSetupKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	s := m.edge.setup
	if s == nil || !s.open {
		return m, nil, false
	}
	switch s.step {
	case setupChoose:
		return m.onSetupChooseKey(k)
	case setupName:
		return m.onSetupTextKey(k, &s.name, m.setupAfterName)
	case setupAddr:
		return m.onSetupTextKey(k, &s.addr, m.setupAfterAddr)
	case setupRunning:
		return m, nil, true // no input while the handshake is in flight
	case setupAllow:
		return m.onSetupAllowKey(k)
	case setupDone:
		// Dismiss on enter/space/esc/q. Key BY TYPE first (some terminals deliver enter as a
		// type with a non-"enter" String()), so ⏎ on the done screen always closes to [3].
		switch k.Type {
		case tea.KeyEnter, tea.KeyEsc, tea.KeySpace:
			m.closeEdgeSetup()
			return m, nil, true
		}
		if s := k.String(); s == "enter" || s == "esc" || s == "q" || s == " " {
			m.closeEdgeSetup()
		}
		return m, nil, true
	case setupFailed:
		return m.onSetupFailedKey(k)
	}
	return m, nil, true
}

func (m *model) onSetupChooseKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	s := m.edge.setup
	switch k.String() {
	case "esc", "q":
		m.closeEdgeSetup()
	case "up", "k":
		s.choice = 0
	case "down", "j":
		s.choice = 1
	case "left", "right", "tab":
		s.choice ^= 1
	case "enter":
		s.join = s.choice == 1
		s.step = setupName
	}
	return m, nil, true
}

func (m *model) onSetupTextKey(k tea.KeyMsg, buf *string, commit func() (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd, bool) {
	s := m.edge.setup
	switch k.Type {
	case tea.KeyEsc:
		m.setupBack()
	case tea.KeyEnter:
		mm, cmd := commit()
		return mm, cmd, true
	case tea.KeyBackspace, tea.KeyDelete:
		if len(*buf) > 0 {
			*buf = (*buf)[:len(*buf)-1]
			s.err = ""
		}
	case tea.KeyRunes, tea.KeySpace:
		// A space arrives as KeySpace carrying Runes{' '}, so appending the runes is the whole
		// insert - do not add a second space (that doubled every space keystroke).
		*buf += string(k.Runes)
		s.err = ""
	}
	return m, nil, true
}

func (m *model) setupAfterName() (tea.Model, tea.Cmd) {
	s := m.edge.setup
	if err := edge.ValidName(s.name); err != nil {
		s.err = err.Error()
		return m, nil
	}
	if s.join {
		s.step = setupAddr
		return m, nil
	}
	return m, m.dispatchEdgeSetup()
}

func (m *model) setupAfterAddr() (tea.Model, tea.Cmd) {
	s := m.edge.setup
	if s.addr == "" {
		s.err = "an address is needed, shaped like http://host:port"
		return m, nil
	}
	return m, m.dispatchEdgeSetup()
}

func (m *model) onSetupAllowKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch k.String() {
	case "esc", "q":
		m.closeEdgeSetup()
	case "enter", "r":
		return m, m.dispatchEdgeSetup(), true // retry the join
	}
	return m, nil, true
}

func (m *model) onSetupFailedKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	s := m.edge.setup
	switch k.String() {
	case "esc", "q":
		m.closeEdgeSetup()
	case "enter", "r":
		s.step = setupName // try again from the name, keeping what was typed
		if s.join {
			// go straight back to the address the owner most likely needs to fix
			s.step = setupAddr
		}
		s.err = ""
	}
	return m, nil, true
}

// setupBack goes up one step; from the first step it leaves the wizard.
func (m *model) setupBack() {
	s := m.edge.setup
	switch s.step {
	case setupAddr:
		s.step = setupName
	case setupName:
		if s.finish {
			m.closeEdgeSetup() // the finish path has no choice step to return to
			return
		}
		s.step = setupChoose
	default:
		m.closeEdgeSetup()
	}
}

func (m *model) closeEdgeSetup() {
	m.edge.setup = nil
}
