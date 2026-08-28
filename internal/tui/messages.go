// Package tui is the interactive `rogerai` experience - a two-way radio for Local Models,
// and the terminal twin of the website's "Live Operating Manual". Stations
// (providers) go on air; you tune in to a channel and talk. The look is the web's:
// ~95% monochrome + ONE red beacon, the shared instrument glyphs (◉ on air, ○ off
// air, ◆ verified, ▁▂▃▄▅▆▇█ signal bars), flat hairline structure, and a single
// carrier beat driving the beacon, the ((•)) spinner, and the signal-bar shimmer.
// Built on Bubble Tea + Lipgloss.
package tui

import (
	"fmt"
	"time"

	"rogerai.fm/roger/v6/internal/detect"
)

// ---- messages ----
type offersMsg []offer

// freqResolvedMsg carries the result of a /freq private-band resolve (run off the
// event loop). ok=false means the broker's uniform "no station on that frequency"
// reply (wrong / revoked / expired / off air - indistinguishable, by design).
type freqResolvedMsg struct {
	freq   string  // the code typed (kept so the relay can route via X-Roger-Freq)
	label  string  // cosmetic display for the header (e.g. "147.520 MHz · ...")
	offers []offer // the band's live offers (already TUI-shaped)
	ok     bool
}

// sharesDetectedMsg carries the result of an ASYNC local-LLM detection scan run off
// the event loop (see detectSharesCmd). The Update handler turns it into provider
// rows + clears the loading flag, so the SHARE table never blocks the UI while the
// host's open ports are probed.
// privateRescanMsg carries a detection scan fired from a PRIVATE-band screen. It exists
// SOLELY so the result does not travel through onSharesDetected, which ends by setting
// mode = modeShare - a teleport away from the band the operator was looking at.
type privateRescanMsg struct{ found []detect.Found }

// autoStartDetectedMsg carries the LAUNCH detect - the one nobody asked for.
//
// It is deliberately NOT a sharesDetectedMsg: that handler ends on the SHARE table, which
// is right when the operator pressed a key to get there and wrong when they did not. A rig
// putting its models back on air at startup must leave the operator wherever they were.
type autoStartDetectedMsg struct {
	found []detect.Found
}

// autoStartRetryMsg re-arms the launch detect. roger routinely starts before the local
// model server does, and one scan at t=0 finds nothing on exactly the rigs the feature
// exists for.
type autoStartRetryMsg struct{}

type sharesDetectedMsg struct {
	found   []detect.Found
	needKey []string // base URLs present but key-protected (401/403), for the guided prompt
}

// balanceMsg carries the wallet read: the balance plus whether the broker says the
// caller is logged in (has a real account wallet). Balance is shown only when in.
type balanceMsg struct {
	balance      float64
	loggedIn     bool
	monthlyCap   float64 // per-account monthly spend cap ($); 0 = unlimited
	monthlySpend float64 // month-to-date captured spend ($)
}

type chatMsg struct {
	reply, status string
	cost          float64
	// Per-turn metrics for the rich reply footer (0/empty = broker didn't report it; the
	// renderer omits missing fields and falls back to `status`). See sendChat / replyFooter.
	provider            string
	tokensIn, tokensOut int
	tps                 float64
	priceIn, priceOut   float64
	latency             time.Duration
	// local marks a turn that ran DIRECT on this machine. It is not "cost 0": it is "there
	// is no cost", and the footer says so in words rather than printing a dollar figure.
	local bool
}

type chatErrMsg string

type errMsg string

type tickMsg struct{ gen int }

// in-TUI flow result messages
type loginMsg string

type topupMsg string

type grantMsg struct{ secret string }

type grantListMsg []GrantRow

type flowErrMsg string

// loginStartedMsg carries the started device flow back to the Update loop so the
// panel can render the URL + code and we can auto-open the browser, THEN begin
// polling (the poll is a second Cmd that lands as a loginMsg / flowErrMsg).
type loginStartedMsg LoginDevice

// logoutMsg signals the local GitHub binding was forgotten (the in-TUI logout).
type logoutMsg struct{}

// payoutStatusMsg carries the lazily-fetched Connect/KYC + payable snapshot back to
// the Update loop (best-effort; a fetch failure lands as a not-loaded snapshot and is
// simply not surfaced - the SHARE view still renders).
type payoutStatusMsg payoutSnapshot

// autoTuneMsg asks the model to run the auto-tune decision now (bands are already
// scanned). The cold path fetches /discover first (fetchOffers -> offersMsg), whose
// handler runs the decision when m.autoTuning is set.
type autoTuneMsg struct{}

// onAirLimitMsg is the clear blocked-at-the-soft-limit message the SHARE selector
// shows when the user tries to put one more band on air past share.max_on_air.
func (m model) onAirLimitMsg() string {
	max := m.maxOnAir()
	return stEmber.Render(fmt.Sprintf("%d/%d on air", max, max)) +
		stDim.Render(fmt.Sprintf(" - take one off air first, or raise share.max_on_air in config and restart"))
}
