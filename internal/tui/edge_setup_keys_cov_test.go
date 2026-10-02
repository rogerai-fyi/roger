package tui

// THE [3] SET-UP WIZARD'S KEYS, step by step: choosing new vs join, typing and correcting a name and
// an address, going back a step, a join the authority has not allowed yet, a failure and its retry.
// The backend is the EdgeSetup hook set; each records what it was asked and answers as told.

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
)

type setupCalls struct {
	newEdge []string
	join    []string
	err     error
}

func setupWizard(t *testing.T) (model, *setupCalls) {
	t.Helper()
	self := edge.SelfStatus{Root: edge.RootLocal, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	c := &setupCalls{}
	m.hooks.EdgeSetup = &EdgeSetup{
		DefaultName: func() string { return "desk" },
		UserKeyHex:  func() string { return "c0ffee" },
		NewEdge:     func(name string) error { c.newEdge = append(c.newEdge, name); return c.err },
		Join:        func(name, addr string) error { c.join = append(c.join, name+"@"+addr); return c.err },
	}
	m = edgePress(m, "e")
	require.NotNil(t, m.edge.setup, "e opens the wizard on a machine that is not set up")
	return m, c
}

// settleSetup runs the dispatched handshake and lands its result, with the screen's clock moved past
// the minimum show time so the result settles at once.
func settleSetup(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	require.NotNil(t, cmd)
	batch, ok := cmd().(tea.BatchMsg)
	require.True(t, ok, "the handshake is dispatched with its animation tick")
	var done *edgeSetupDoneMsg
	for _, c := range batch {
		if msg, ok := c().(edgeSetupDoneMsg); ok {
			done = &msg
		}
	}
	require.NotNil(t, done)
	later := m.edge.setup.startAt.Add(edgeSetupMinShow + time.Second)
	m.edgeClock = func() time.Time { return later }
	tm, _ := m.Update(*done)
	return asModel(tm)
}

func setupKey(m model, k tea.KeyMsg) (model, tea.Cmd) {
	tm, cmd := m.Update(k)
	return asModel(tm), cmd
}

func TestTheWizardChoosesNewOrJoin(t *testing.T) {
	m, _ := setupWizard(t)
	s := m.edge.setup
	require.Equal(t, setupChoose, s.step)
	m = edgePress(m, "j")
	require.Equal(t, 1, s.choice)
	m = edgePress(m, "k")
	require.Equal(t, 0, s.choice)
	m, _ = setupKey(m, kt(tea.KeyTab))
	require.Equal(t, 1, s.choice, "tab flips the choice")
	m, _ = setupKey(m, kt(tea.KeyRight))
	require.Equal(t, 0, s.choice)
	m, _ = setupKey(m, kt(tea.KeyEsc))
	require.Nil(t, m.edge.setup, "esc at the first step leaves the wizard")
}

func TestTheWizardStartsANewEdgeAndRecoversFromAFailure(t *testing.T) {
	m, calls := setupWizard(t)
	m, _ = setupKey(m, kt(tea.KeyEnter)) // new
	s := m.edge.setup
	require.Equal(t, setupName, s.step)
	require.Equal(t, "desk", s.name, "seeded with the default name")

	// Edit the name: a bad one is refused in place, a corrected one goes.
	m, _ = setupKey(m, kt(tea.KeyBackspace))
	m = edgePress(m, "!")
	m, cmd := setupKey(m, kt(tea.KeyEnter))
	require.Nil(t, cmd)
	require.NotEmpty(t, s.err, "an invalid name is refused before anything runs")
	m, _ = setupKey(m, kt(tea.KeyBackspace))
	require.Empty(t, s.err, "editing clears the complaint")
	m, _ = setupKey(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	require.Equal(t, "des ", s.name, "one space per space keystroke")
	m, _ = setupKey(m, kt(tea.KeyBackspace))
	m = edgePress(m, "k")

	calls.err = errors.New("disk full")
	m, cmd = setupKey(m, kt(tea.KeyEnter))
	require.Equal(t, setupRunning, s.step)
	m, none := setupKey(m, keyRunes("x"))
	require.Nil(t, none, "no input while the handshake is in flight")
	m = settleSetup(t, m, cmd)
	s = m.edge.setup
	require.Equal(t, []string{"desk"}, calls.newEdge)
	require.Equal(t, setupFailed, s.step)
	require.Equal(t, "disk full", s.err)

	// r tries again from the name, keeping what was typed.
	m = edgePress(m, "r")
	require.Equal(t, setupName, s.step)
	require.Equal(t, "desk", s.name)
	require.Empty(t, s.err)

	calls.err = nil
	m, cmd = setupKey(m, kt(tea.KeyEnter))
	m = settleSetup(t, m, cmd)
	require.Equal(t, setupDone, m.edge.setup.step)
	m, _ = setupKey(m, kt(tea.KeyEnter))
	require.Nil(t, m.edge.setup, "enter on the done screen closes to [3]")
}

func TestTheWizardJoinsAndShowsTheKeyWhenNotAllowedYet(t *testing.T) {
	m, calls := setupWizard(t)
	m = edgePress(m, "j")
	m, _ = setupKey(m, kt(tea.KeyEnter))
	s := m.edge.setup
	require.True(t, s.join)
	m, _ = setupKey(m, kt(tea.KeyEnter)) // the default name
	require.Equal(t, setupAddr, s.step)

	m, cmd := setupKey(m, kt(tea.KeyEnter))
	require.Nil(t, cmd)
	require.Contains(t, s.err, "an address is needed")

	// esc goes back one step, and back again to the choice.
	m, _ = setupKey(m, kt(tea.KeyEsc))
	require.Equal(t, setupName, s.step)
	m, _ = setupKey(m, kt(tea.KeyEnter))
	m = edgePress(m, "http://10.0.0.2:8791")

	calls.err = ErrNotAllowedYet
	m, cmd = setupKey(m, kt(tea.KeyEnter))
	m = settleSetup(t, m, cmd)
	s = m.edge.setup
	require.Equal(t, []string{"desk@http://10.0.0.2:8791"}, calls.join)
	require.Equal(t, setupAllow, s.step)
	require.Equal(t, "c0ffee", s.userKey, "the owner is shown the key the authority must allow")

	// Retry after the owner allowed it.
	calls.err = nil
	m, cmd = setupKey(m, keyRunes("r"))
	m = settleSetup(t, m, cmd)
	require.Equal(t, setupDone, m.edge.setup.step)
	require.Len(t, calls.join, 2)
}

func TestAFailedJoinRetriesAtTheAddress(t *testing.T) {
	m, calls := setupWizard(t)
	m = edgePress(m, "j")
	m, _ = setupKey(m, kt(tea.KeyEnter))
	m, _ = setupKey(m, kt(tea.KeyEnter))
	m = edgePress(m, "http://nowhere:1")
	calls.err = errors.New("connection refused")
	m, cmd := setupKey(m, kt(tea.KeyEnter))
	m = settleSetup(t, m, cmd)
	require.Equal(t, setupFailed, m.edge.setup.step)
	m, _ = setupKey(m, kt(tea.KeyEnter))
	require.Equal(t, setupAddr, m.edge.setup.step, "a failed join goes back to the address, the likeliest fix")

	// q at a failure, and esc at the not-allowed screen, both leave.
	m.edge.setup.step = setupFailed
	m = edgePress(m, "q")
	require.Nil(t, m.edge.setup)
	m, _ = setupWizard(t)
	m.edge.setup.step = setupAllow
	m, _ = setupKey(m, kt(tea.KeyEsc))
	require.Nil(t, m.edge.setup)
}

func TestTheFinishPathHasNoChoiceToGoBackTo(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Name: "desk", AuthorityHere: true, Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	m.hooks.EdgeSetup = &EdgeSetup{EnrollSelf: func() error { return nil }}
	m.openEdgeSetup()
	require.True(t, m.edge.setup.finish)
	require.Equal(t, setupName, m.edge.setup.step)
	m, _ = setupKey(m, kt(tea.KeyEsc))
	require.Nil(t, m.edge.setup, "esc on the finish path leaves rather than showing a choice it skipped")
}
