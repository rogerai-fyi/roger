package look

import (
	"fmt"
	"strings"
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

// NEARBY (empty_edge_simple.feature §7, founder ruling 2026-10-12): a device that pings is a row
// the owner acts on from the sighting. i mints an invite bound to that device, a approves it when
// it asks (a claim-only device, a phone, is granted its claim instead). No address is typed.
const (
	NearbyEyebrow    = "nearby"
	NearbyActions    = "i invite · a approve"
	NearbyInvited    = "invited · waiting"
	NearbyNoCert     = "no certificate yet · joins by claim"
	NearbyComeBack   = "it must come back on the network to use it"
	NearbyInviteNext = "scan the QR with the device, or paste the line on it: it joins once, and only this device can"
	NearbyNoEdgeYet  = "start an Edge here first (e), then i invites it"
	NearbyNotServing = "this machine's authority is not answering on the network yet"
	NearbyCLIHead    = "nearby (pinging on this network, not on an Edge yet):"
	NearbyCLINone    = "no device is pinging on this network right now."
	// AdoptDeprecated is the one line `roger edge adopt` prints while it stays as an alias.
	AdoptDeprecated = "adopt is now: roger edge invite --for <id>, or roger edge approve <id>"
	// JoinSaysForOther is a bound invite presented by a device it was not minted for.
	JoinSaysForOther = "that invite is for another device: ask for one for this one"
)

// NearbyActionsBar is the nearby row's actions as an action bar.
var NearbyActionsBar = []Action{{"i", "invite"}, {"a", "approve"}}

// NearbySeen words how long ago a device was last heard: "seen 12s ago", or, once it has been quiet
// for NearbyStaleAfter, "last seen 20m ago" so the owner knows it is not here now.
func NearbySeen(since time.Duration) string {
	if since >= NearbyStaleAfter {
		return "last seen " + shortAge(since) + " ago"
	}
	return "seen " + shortAge(since) + " ago"
}

// NearbyStaleAfter is how long a device may be quiet before its row is marked stale.
const NearbyStaleAfter = 5 * time.Minute

// NearbyInviteHead is the invite panel's heading for one device.
func NearbyInviteHead(name string) string { return "invite for " + name }

// NearbyWaitAsk is what the screen says after a on a device that joins by asking.
func NearbyWaitAsk(name string) string { return "waiting for " + name + " to ask" }

// NearbyRunLine names what that device runs, against this authority's address.
func NearbyRunLine(addr string) string { return "on it, run: roger join " + addr }

// NearbyCLIInvite and NearbyCLIApprove are the two lines `roger edge nearby` prints under a row.
func NearbyCLIInvite(id string) string  { return "invite it: roger edge invite --for " + id }
func NearbyCLIApprove(id string) string { return "approve it when it asks: roger edge approve " + id }

// NearbyKey is how a nearby row names a device's key: the fingerprint of the certificate it serves
// ("fp"), or for a device with none (a phone) its node id, which derives from its node key ("key").
// Twelve hex characters, never clipped, are what tell two same-named devices apart.
func NearbyKey(pin, id string) (label, short string) {
	if pin != "" {
		return "fp", NearbyShort(pin)
	}
	return "key", NearbyShort(strings.TrimPrefix(id, "n_"))
}

// NearbyShort is the first twelve characters of a fingerprint or an id.
func NearbyShort(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

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
