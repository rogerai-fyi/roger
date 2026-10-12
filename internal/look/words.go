package look

import (
	"fmt"
	"time"
)

// WelcomeOption is one line of the one-time welcome card
// (features/onboarding/welcome_card.feature): its key, its glyph, its label and what it does.
// The keys are the TUI's own keys, so the card teaches the keys the user keeps using.
type WelcomeOption struct{ Key, Glyph, Label, What string }

// WelcomeOptions are the card's six lines, in order. The console shows the same six
// (web/src/console: its own copy is checked against this list).
var WelcomeOptions = []WelcomeOption{
	{"1", "◉", "Chat with a model", "pick a model and talk"},
	{"0", "▸", "Run an agent", "let a model read, write, run code"},
	{"2", "((•))", "Share a model", "put a model on air (free or paid)"},
	{"3", "▟•▙", "Edge network", "join your own devices together"},
	{"w", "◇", "Web console", "open the same app in a browser"},
	{"l", "◈", "Log in", "starter credit for new accounts; earn by sharing"},
}

// WelcomeLoggedIn is the login line for a user who is already logged in: the l key opens the
// same [L] panel, which offers to log out.
func WelcomeLoggedIn(who string) WelcomeOption {
	if who == "" {
		who = "your account"
	} else {
		who = "@" + who
	}
	return WelcomeOption{"l", "◈", "Log out", "logged in as " + who}
}

// The card's words around its lines.
const (
	WelcomeHeading = "first time here"
	WelcomeSkip    = "pick one, or esc to skip"
	WelcomeAgain   = "you can always get here again with ?"
	// WelcomeKeys is the jump keys as the action bar shows them.
	WelcomeKeys = "1 0 2 3 w l"
)

// WelcomeActions is the card's action bar.
var WelcomeActions = []Action{{"↑↓", "move"}, {"enter", "open"}, {WelcomeKeys, "jump"}, {"esc", "skip"}}

// The empty [3] EDGE's words (features/edge/empty_edge_simple.feature), shared by the TUI, the
// console and `roger edge`.
const (
	EmptyEdgeBrand   = "▟•▙ ROGER · EDGE"
	EmptyEdgeTitle   = "your edge"
	EmptyEdgeState   = "not set up yet"
	EmptyEdgeSay     = "Roger Edge links your own devices (laptops, desktops, Pis) so they can use each other's models, on your network."
	EmptyEdgeStart   = "start an Edge here"
	EmptyEdgeStartIs = "your other devices will join this one"
	EmptyEdgeJoin    = "join an Edge you started elsewhere"
	EmptyEdgeJoinIs  = "paste an invite, or ask by address"
	// EmptyEdgeCompact is the whole empty state in the windowshade.
	EmptyEdgeCompact = "your Edge: not set up · e start · j join"
	// EmptyEdgeOffSwitch is the switch that turns discovery off.
	EmptyEdgeOffSwitch = "ROGERAI_EDGE_DISCOVERY=0"
)

// EmptyEdgeActions is the empty Edge's action bar.
var EmptyEdgeActions = []Action{{"e", "start an Edge here"}, {"j", "join"}, {"esc", "back"}}

// The j prompt (features/edge/empty_edge_simple.feature §4): what it asks, what an ask by
// address shows, and the one line each way a join can fail. The host maps real errors onto
// these; the TUI, the console and the tests only ever see the words.
const (
	EmptyEdgePromptAsk  = "paste an invite, or type the authority's address"
	EmptyEdgePhraseNote = "the owner of that Edge should see the same phrase"
	EmptyEdgeAskWait    = "waiting for the owner there to approve"
	JoinSaysUsed        = "that invite was already used: ask for a new one"
	JoinSaysExpired     = "that invite expired: ask for a new one"
	JoinSaysRefused     = "the owner refused"
	JoinSaysTimeout     = "no answer in 2 minutes: ask again"
	JoinSaysNoAnswer    = "nothing answered at that address"
	JoinSaysTower       = "that is a Tower invite: use it from the dial (roger join)"
	JoinSaysOtherEdge   = "that is not the Edge you joined before"
	JoinSaysNotInvite   = "that is not an invite roger knows: paste the whole roger:// line"
	JoinSaysNotLocal    = "that address is not on your network: a join reaches your own network only"
	JoinSaysNotAddress  = "that is not an address roger can reach: host:port on your network"
	// JoinSaysUsedOrExpired is what the host says when the authority holds no live code for the
	// invite: it does not tell a spent code from an expired one, so neither does this line.
	JoinSaysUsedOrExpired = "that invite was already used or has expired: ask for a new one"
)

// EmptyEdgePromptActions is the j prompt's action bar, and EmptyEdgeAskActions the wait's.
var (
	EmptyEdgePromptActions = []Action{{"enter", "join"}, {"esc", "back"}}
	EmptyEdgeAskActions    = []Action{{"esc", "cancel"}}
)

// The e setup's authority step (ruled 2026-10-11: inside e, local by default).
const (
	SetupAuthorityAsk   = "where does this Edge's authority live?"
	SetupAuthorityLocal = "on this network only (works with no internet)"
	SetupAuthorityCore  = "through RogerAI (needs a login; reach it from anywhere)"
	SetupNeedsLogin     = "rooting an Edge through RogerAI needs a login"
	SetupLoginOffer     = "log in now"
	// SetupNoBuildLine is what e says on a build that cannot run setup.
	SetupNoBuildLine = "this build cannot set up an Edge here: run roger edge setup to start one, or roger join <invite or address> to join one"
)

// The CLI's empty Edge (§7): the head line and the two next steps.
const (
	EmptyEdgeCLIHead  = "your Edge: not set up yet"
	EmptyEdgeCLIStart = "roger edge setup"
	EmptyEdgeCLIJoin  = "roger join <invite or address>"
)

// EdgeScan is what the empty Edge's scan line knows: whether discovery is off, whether a pass
// has completed, what it saw, and how often it runs.
type EdgeScan struct {
	Off      bool
	PassDone bool
	Seen     int // other roger machines seen nearby (this machine excluded)
	InEdge   int // of those, already in an Edge
	Every    time.Duration
}

// Eye is the scan line's magic eye: closed with discovery off, waiting before the first pass,
// seen once one has completed.
func (s EdgeScan) Eye() string {
	switch {
	case s.Off:
		return EyeOff
	case s.PassDone:
		return EyeSeen
	}
	return EyeWait
}

// Lines are the two scan lines in words: when it scans (or that it is off, naming the
// switch), then what the last completed pass saw. Before a pass completes nothing is counted:
// it never says "0 roger machines" from no evidence.
func (s EdgeScan) Lines() (string, string) {
	if s.Off {
		return "scanning is off (" + EmptyEdgeOffSwitch + ")", "the two ways in still work: e start, j join"
	}
	first := "scanning this network while this screen is open (every " + shortDur(s.Every) + ")"
	if !s.PassDone {
		return first, "no pass has completed yet"
	}
	return first, NearbyLine(s.Seen, s.InEdge)
}

// NearbyLine words a completed pass's count.
func NearbyLine(seen, inEdge int) string {
	if seen <= 0 {
		return "no other roger machines seen nearby"
	}
	noun := "roger machines"
	if seen == 1 {
		noun = "roger machine"
	}
	tail := "none in an Edge yet"
	if inEdge > 0 {
		tail = fmt.Sprintf("%d in an Edge", inEdge)
	}
	return fmt.Sprintf("%d %s seen nearby · %s", seen, noun, tail)
}

// shortDur is an interval as the scan line says it: 30s, 2m, 1h. Zero (not reported) reads
// as the default 30s pass.
func shortDur(d time.Duration) string {
	switch {
	case d <= 0:
		return "30s"
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
}
