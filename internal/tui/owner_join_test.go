package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/ownerjoin"
	towerpkg "rogerai.fm/roger/v6/internal/tower"
)

// ownerFake is the owner's side as the host would fill it, held in memory: the end-to-end
// scenarios run it against a real Tower (cmd/rogerai/join_owner_screens_bdd_test.go); these cases
// pin what the screen does with each answer.
type ownerFake struct {
	reqs     []towerpkg.PairRequest
	decided  []string
	decErr   error
	invErr   error
	outcome  string
	over     bool
	pinned   []ownerjoin.Pinned
	invCalls []string
}

func (f *ownerFake) hooks() *ownerjoin.Hooks {
	lab := ownerjoin.Tower{Dir: "/srv/lab", Label: "lab", Addr: "192.168.1.20:8787", FP: "9f3c-0000-1111-2222"}
	kit := ownerjoin.Tower{Dir: "/srv/kit", Label: "kit", Addr: "192.168.1.21:8787", FP: "aaaa-bbbb-cccc-dddd"}
	return &ownerjoin.Hooks{
		Towers:   func() []ownerjoin.Tower { return []ownerjoin.Tower{lab, kit} },
		Requests: func(t ownerjoin.Tower) ([]towerpkg.PairRequest, error) {
			if t.Dir == "/srv/kit" {
				return nil, errors.New("stopped")
			}
			return f.reqs, nil
		},
		Decide: func(dir, id string, approve bool) error {
			f.decided = append(f.decided, dir+"/"+id+map[bool]string{true: "/y", false: "/n"}[approve])
			return f.decErr
		},
		Invite: func(t ownerjoin.Tower) (ownerjoin.Invite, error) {
			f.invCalls = append(f.invCalls, t.Label)
			if f.invErr != nil {
				return ownerjoin.Invite{}, f.invErr
			}
			return ownerjoin.Invite{Dir: t.Dir, Tower: t.Label, ID: "inv1", Heading: "invite for one device, valid 15 min:",
				Next: "on the other machine run:", Line: "roger join roger://join?kind=tower-client&x=1",
				ExpiresAt: time.Now().Add(15 * time.Minute).UnixNano()}, nil
		},
		Outcome: func(dir, id string) (string, bool) { return f.outcome, f.over },
		Pinned:  func() []ownerjoin.Pinned { return f.pinned },
	}
}

// drive runs a Cmd and feeds its message back, the way Bubble Tea does (a tick is not waited on).
func drive(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if _, isRefresh := msg.(OwnerJoinRefresh); isRefresh {
		return m
	}
	tm, _ := m.Update(msg)
	return tm.(model)
}

func ownerModel(t *testing.T, f *ownerFake) model {
	t.Helper()
	m := NewWithHooks("", "", nil, Hooks{Owner: f.hooks()})
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	m = tm.(model)
	tm, cmd := m.Update(OwnerJoinRefresh{})
	return drive(t, tm.(model), cmd)
}

func ownerKey(m model, k string) (model, tea.Cmd) {
	tm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	return tm.(model), cmd
}

func TestOwnerJoinIsOffWithoutHooks(t *testing.T) {
	m := NewWithHooks("", "", nil, Hooks{})
	require.Nil(t, m.ownerJoinCmd())
	tm, cmd := m.Update(OwnerJoinRefresh{})
	require.Nil(t, cmd)
	m = tm.(model)
	require.Empty(t, m.ownerJoinBanner(100))
	require.Empty(t, m.ownerEdgeView(100))
}

func TestARequestIsACardOnEdgeAndTheDialAndANoticeElsewhere(t *testing.T) {
	f := &ownerFake{reqs: []towerpkg.PairRequest{{ID: "r1", Name: "laptop-x", Phrase: "amber-river-71"}, {ID: "r2", Name: "pi-2", Phrase: "a-b-c-01"}}}
	m := ownerModel(t, f)
	require.Len(t, m.own.reqs, 2, "a Tower whose requests cannot be read is skipped, not an error")

	m.mode = modeBrowse
	v := stripANSI(m.View())
	require.Contains(t, v, "laptop-x wants to join · amber-river-71 · y approves, n refuses")
	require.Contains(t, v, "approve only if laptop-x shows the same phrase")
	require.Contains(t, v, "and 1 more request after this one")

	m.mode = modeHelp
	v = stripANSI(m.View())
	require.Contains(t, v, "2 devices want to join: open [3] EDGE to answer")
	require.NotContains(t, v, "y approves")

	// y while typing (a chat, the command palette) is a letter, never an answer.
	m.mode = modeCommand
	_, cmd := ownerKey(m, "y")
	if cmd != nil {
		if _, ok := cmd().(ownerDecidedMsg); ok {
			t.Fatal("y in the chat answered a join request")
		}
	}
	require.Empty(t, f.decided)
}

func TestTheOwnerAnswersTheFirstRequestAndItLeavesTheScreen(t *testing.T) {
	f := &ownerFake{reqs: []towerpkg.PairRequest{{ID: "r1", Name: "laptop-x", Phrase: "amber-river-71"}}}
	m := ownerModel(t, f)
	m.mode = modeEdge
	m, cmd := ownerKey(m, "n")
	m = drive(t, m, cmd)
	require.Equal(t, []string{"/srv/lab/r1/n"}, f.decided)
	require.Empty(t, m.own.reqs)
	require.Contains(t, stripANSI(m.status), "refused laptop-x")

	f.decErr = errors.New("that request expired")
	m.own.reqs = []ownerjoin.Request{ownerjoin.RequestFor(ownerjoin.Tower{Dir: "/srv/lab", Label: "lab"}, f.reqs[0])}
	m, cmd = ownerKey(m, "y")
	m = drive(t, m, cmd)
	require.Contains(t, stripANSI(m.status), "could not answer laptop-x: that request expired")
}

func TestTheInvitePanelShowsTheLineAndWhoJoined(t *testing.T) {
	f := &ownerFake{}
	m := ownerModel(t, f)
	m.mode = modeEdge
	v := stripANSI(m.ownerEdgeView(120))
	require.Contains(t, v, "Tower lab · 192.168.1.20:8787 · 9f3c-0000-1111-2222")
	require.Contains(t, v, "I invites a device")

	m, cmd := ownerKey(m, "I")
	m = drive(t, m, cmd)
	require.Equal(t, []string{"lab"}, f.invCalls)
	v = stripANSI(m.ownerEdgeView(40))
	require.Contains(t, v, "c copies the line · esc closes")
	require.Contains(t, v, "15 min left")
	require.Contains(t, strings.Join(strings.Fields(v), ""), "rogerjoinroger://join?kind=tower-client&x=1", "the whole line is on screen")

	// The panel owns the keys while open: y is not an answer here, and c copies.
	m, cmd = ownerKey(m, "c")
	require.NotNil(t, cmd)
	require.Contains(t, stripANSI(m.status), "Copied to clipboard")

	// Used: who joined, in place of the QR.
	f.outcome, f.over = "laptop-x joined (key ab...cd); undo with: roger tower revoke-client laptop-x", true
	tm, cmd := m.Update(OwnerJoinRefresh{})
	m = drive(t, tm.(model), cmd)
	v = stripANSI(m.ownerEdgeView(120))
	require.Contains(t, v, "laptop-x joined")
	require.NotContains(t, v, "min left")

	m, _ = ownerKey(m, "esc")
	require.Nil(t, m.own.invite)

	// The next invite is for the next Tower served.
	m, cmd = ownerKey(m, "I")
	_ = drive(t, m, cmd)
	require.Equal(t, []string{"lab", "kit"}, f.invCalls)
}

func TestAnInviteThatCannotBeMadeSaysWhy(t *testing.T) {
	f := &ownerFake{invErr: errors.New("the Tower stopped")}
	m := ownerModel(t, f)
	m.mode = modeEdge
	m, cmd := ownerKey(m, "I")
	m = drive(t, m, cmd)
	require.Nil(t, m.own.invite)
	require.Contains(t, stripANSI(m.status), "could not make an invite: the Tower stopped")
}

func TestTheTowersThisMachineJoinedAreListedWithTheirPins(t *testing.T) {
	f := &ownerFake{pinned: []ownerjoin.Pinned{{Addr: "192.168.1.30:8787", FP: "1234-5678-9abc-def0"}}}
	m := ownerModel(t, f)
	v := stripANSI(m.ownerEdgeView(120))
	require.Contains(t, v, "TOWERS YOU JOINED")
	require.Contains(t, v, "192.168.1.30:8787 · pinned 1234-5678-9abc-def0")
}

func TestARefreshWhilePollingDoesNotStartAnother(t *testing.T) {
	m := NewWithHooks("", "", nil, Hooks{Owner: (&ownerFake{}).hooks()})
	tm, cmd := m.Update(OwnerJoinRefresh{})
	require.NotNil(t, cmd)
	_, cmd = tm.(model).Update(OwnerJoinRefresh{})
	require.Nil(t, cmd, "one poll at a time")
}
