// Package tui is the interactive `rogerai` experience - a two-way radio for GPUs.
// Stations (providers) go on air; you tune in to a channel and talk. Signal bars
// animate live; a gold call-sign ◆ marks lineage-verified. Built on Bubble Tea.
package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/detect"
)

// Hooks lets the host (cmd/rogerai) supply the few platform/auth bits the TUI
// can't compute itself, so the in-TUI /share, /login, /topup, /grant flows are
// REAL actions (not "run it elsewhere") without the tui package importing the
// host. All are optional; a nil hook degrades that flow to a labeled hint.
type Hooks struct {
	NodeID      string                                        // this node's id (hostname)
	HW          string                                        // hardware label for the offer
	GitHubID    string                                        // public GitHub OAuth client id (device flow)
	ShareModel  string                                        // saved onboarding model (default offer)
	SharePriceI float64                                       // saved input price (0 = free)
	SharePriceO float64                                       // saved output price (0 = free)
	Login       func(broker, clientID string) (string, error) // device-flow login -> github login
	TopupURL    func(broker, user string, usd float64) (string, error)
	GrantCreate func(broker, name string, free bool) (secret string, err error)
	GrantList   func(broker string) ([]GrantRow, error)
}

// GrantRow is a compact grant summary for the in-TUI /grant list.
type GrantRow struct {
	Name, Price, Status string
}

// quiet is true when output isn't an interactive color TTY (NO_COLOR set, or
// piped / redirected). lipgloss already strips color in that case; we also
// freeze the animation to a single representative frame so the on-air pulse
// and signal bars render as a clean static fallback instead of garbled glyph
// churn in a pipe. Honors DESIGN.md: "static fallback when NO_COLOR / non-TTY".
var quiet = func() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return true
	}
	return !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd())
}()

// anim returns the live frame counter, or a fixed frame when quiet so motion
// settles into a stable, well-formed snapshot.
func anim(frame int) int {
	if quiet {
		return 1
	}
	return frame
}

// ---- palette (the "white room, neon wiring" tokens) ----
var (
	cVolt  = lipgloss.Color("#5B5BFF") // brand / you / selection
	cLive  = lipgloss.Color("#00C781") // on air / health
	cEmber = lipgloss.Color("#FF8A3D") // money / cost
	cInk   = lipgloss.Color("#0B0D12")
	cMist  = lipgloss.Color("#6B7280")
	cGold  = lipgloss.Color("#E8B339") // lineage call-sign

	stBrand    = lipgloss.NewStyle().Foreground(cVolt).Bold(true)
	stTag      = lipgloss.NewStyle().Foreground(cEmber)
	stDim      = lipgloss.NewStyle().Foreground(cMist)
	stLive     = lipgloss.NewStyle().Foreground(cLive)
	stEmber    = lipgloss.NewStyle().Foreground(cEmber)
	stGold     = lipgloss.NewStyle().Foreground(cGold)
	stSelBar   = lipgloss.NewStyle().Foreground(cVolt)
	stSelText  = lipgloss.NewStyle().Foreground(cVolt).Bold(true)
	stHeadRule = lipgloss.NewStyle().Foreground(lipgloss.Color("#ECEDF1"))
	stPanel    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cVolt).Padding(0, 1)
	stKey      = lipgloss.NewStyle().Foreground(cEmber).Bold(true)
	stPrompt   = lipgloss.NewStyle().Foreground(cVolt).Bold(true) // the `rog ›` prompt lockup
	cRed       = lipgloss.Color("#FF3B3B")                        // the live-red on-air beacon (web --carrier)
	stRed      = lipgloss.NewStyle().Foreground(cRed).Bold(true)

	// k9s-grade selection: a full-width reverse-video (accent-bg) row so the cursor
	// is unmistakable at a glance, exactly like k9s's cursor row (it flips the row's
	// background to its accent so the selected resource pops). We use the brand volt
	// as the row background with ink text; under NO_COLOR lipgloss drops the bg and a
	// leading `>` carat carries the selection instead (see rowSel / selCarat).
	// k9s design refs (cited for the local design record): k9scli.io (cursor/accent
	// row, status columns, contextual key footer) and github.com/derailed/k9s
	// (skin table.cursorColor=aqua, reverse-video selected row, keyboard-first nav).
	stRowSel = lipgloss.NewStyle().Foreground(cInk).Background(cVolt).Bold(true)
)

// selCarat is the NO_COLOR / non-TTY selection marker: a bold `>` the eye still
// catches when the reverse-video background is stripped. A space keeps unselected
// rows aligned under the same gutter.
func selCarat(sel bool) string {
	if sel {
		return stSelText.Render(">")
	}
	return " "
}

// rowSel renders a table row body so the SELECTED row is k9s-style reverse-video
// (a full-width accent background bar) and unselected rows are plain. The `plain`
// text for a selected row should carry no per-cell color - one reverse-video style
// governs the whole row (mixing fg colors inside a bg run reads as noise). Under
// NO_COLOR the background is stripped automatically and the caller's leading
// selCarat carries the cursor instead.
func rowSel(sel bool, plain string, width int) string {
	if !sel {
		return plain
	}
	if w := lipgloss.Width(plain); w < width {
		plain += strings.Repeat(" ", width-w)
	}
	return stRowSel.Render(plain)
}

type offer struct {
	NodeID       string  `json:"node_id"`
	Region       string  `json:"region"`
	Model        string  `json:"model"`
	PriceIn      float64 `json:"price_in"`
	PriceOut     float64 `json:"price_out"`
	Ctx          int     `json:"ctx"`
	Online       bool    `json:"online"`
	Confidential bool    `json:"confidential"`
	FreeNow      bool    `json:"free_now"`
	TPS          float64 `json:"tps"`
}

// alertBox is a tiny thread-safe mailbox: the relay's failover callback (running
// in the proxy goroutine) drops a line in, and the Bubble Tea tick loop drains it
// onto the status line. Pointer-shared so the model copy on each Update sees it.
type alertBox struct {
	mu  sync.Mutex
	msg string
}

func (a *alertBox) set(s string) { a.mu.Lock(); a.msg = s; a.mu.Unlock() }
func (a *alertBox) take() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.msg
	a.msg = ""
	return s
}

type mode int

const (
	modeBrowse mode = iota
	modeCommand
	modeChat
	modeHelp
	modeConnectConfirm // 3.2 cost confirmation (default DENY)
	modeOverLimit      // 3.3 over-limit + inline edit-your-max
	modeLimits         // 3.4 per-model spend limits
	modeShare          // k9s-style provider table: list local models, toggle on/off-air
)

// Limit is the per-model spend ceiling (mirrors cmd/rogerai's config.Limit).
// Zero fields mean "no cap on that knob". Units match /discover.
type Limit struct {
	MaxIn  float64
	MaxOut float64
	MinTPS float64
}

// LimitStore is the TUI's view of the persisted spend limits: a per-model map, a
// Default for unpinned bands, the typical reply size for est-cost, and a Save
// callback so the host (cmd/rogerai) owns persistence. nil-safe: an empty store
// means no caps. Resolve picks per-model else Default.
type LimitStore struct {
	Models     map[string]Limit
	Default    Limit
	TypicalOut int
	Save       func(models map[string]Limit, def Limit) // persist (nil = no-op)
}

func (s *LimitStore) resolve(model string) Limit {
	if s == nil {
		return Limit{}
	}
	if l, ok := s.Models[model]; ok {
		return l
	}
	return s.Default
}

func (s *LimitStore) typical() int {
	if s == nil || s.TypicalOut <= 0 {
		return 800
	}
	return s.TypicalOut
}

func (s *LimitStore) set(model string, l Limit) {
	if s == nil {
		return
	}
	if s.Models == nil {
		s.Models = map[string]Limit{}
	}
	s.Models[model] = l
	if s.Save != nil {
		s.Save(s.Models, s.Default)
	}
}

func (s *LimitStore) clear(model string) {
	if s == nil || s.Models == nil {
		return
	}
	delete(s.Models, model)
	if s.Save != nil {
		s.Save(s.Models, s.Default)
	}
}

// band is one model grouped across stations, with its live cross-station
// out-price range (semantics A in the design doc).
type band struct {
	model    string
	stations int     // online stations serving it
	minOut   float64 // cheapest active out-price now
	maxOut   float64 // priciest active out-price now
	cheapest *offer  // the station at minOut (broker's default route)
	online   bool    // any station on air
	free     bool    // any station FREE now
	lineage  int     // count of confidential/lineage stations
	all      []offer // every station in this band (online first)
}

// quote is the resolved deal for a connect attempt: the band, the chosen
// station, the effective limit, and the est-cost numbers.
type quote struct {
	b         band
	limit     Limit
	estReply  float64 // credits per typical reply at the cheapest out-price
	typical   int
	overLimit bool
}

type model struct {
	broker, user     string
	offers           []offer
	cursor           int
	width, height    int
	frame            int
	mode             mode
	cmd              textinput.Model
	chatIn           textinput.Model
	transcript       []string
	connected        *offer
	endpoint         string
	apikey           string
	proxyUp          bool
	proxyAddr        string
	confidentialOnly bool
	balance          float64
	haveBal          bool
	status           string
	alert            *alertBox
	// pricing UX state
	limits     *LimitStore
	bands      []band // offers grouped by model (the band list, 3.1)
	q          quote  // the in-flight connect quote (confirm / over-limit)
	editBuf    string // inline numeric edit buffer (over-limit + limits edit)
	editField  int    // which field is focused in the limits editor (0=out,1=tps)
	limCursor  int    // cursor in the limits view
	limModels  []string
	watching   string    // band we are "wait & notify" watching (stub label)
	showDetail bool      // [d] expands the connect-confirm screen; default off (simple)
	relaying   bool      // a chat request is in flight (drives Ping's transmit line)
	relayStart time.Time // when the in-flight chat began (for the elapsed "transmitting Ns")
	scanErr    bool      // last band scan failed (broker unreachable) -> Ping "...static"
	scanned    bool      // at least one scan has come back (good or empty) -> Ping idle, not tx
	minimized  bool      // header toggle: thin one-line bar vs the full lockup
	// chat session state (CHANNEL mode)
	sysPrompt string  // /system prompt prepended to each turn
	sessCost  float64 // running session cost in dollars (sum of per-reply costs)
	// async, cached update check (non-blocking)
	updateLine string // "update available v<cur> -> v<new>" or "" (set by updateMsg)
	// in-TUI provider/account/money flows (TUI-V2-CRITIQUE D / audit C5)
	hooks     Hooks          // host-supplied platform/auth bits (nil-safe)
	share     *agent.Session // most-recently-shared in-process session (the panel's headline; nil = none)
	onAir     bool           // ON AIR indicator + panel (true while any share is live)
	ghLogin   string         // linked GitHub login once /login succeeds
	grantList []GrantRow     // last /grant list result
	// k9s-style SHARE / provider table (modeShare): one row per locally-detected
	// model, each independently flippable on/off air. shares holds the live session
	// per on-air model; shareRows is the rendered model list; shareCursor is the
	// highly-visible reverse-video selection cursor.
	shares      map[string]*agent.Session // model -> live in-process session (on air)
	shareRows   []shareRow                // the provider table rows (detected models)
	shareCursor int                       // selected row in the provider table
	shareUp     string                    // the local upstream chat URL backing the shares
}

// shareRow is one model in the k9s-style provider table: a locally-detected model
// plus its share status. Live metrics are read off the session when on air.
type shareRow struct {
	model string
	ctx   int
}

// ---- messages ----
type offersMsg []offer
type balanceMsg float64
type chatMsg struct {
	reply, status string
	cost          float64
}
type chatErrMsg string // a chat turn failed - surfaced INLINE in the CHANNEL transcript
type errMsg string
type tickMsg struct{}

// in-TUI flow result messages
type loginMsg string                  // github login on success
type topupMsg string                  // checkout URL
type grantMsg struct{ secret string } // a newly created grant's secret (shown once)
type grantListMsg []GrantRow
type flowErrMsg string // a flow failed (login/topup/grant) - shown on the status line

func New(broker, user string) model {
	return NewWith(broker, user, nil)
}

// NewWith builds the model with a spend-limit store (nil = no caps / no persist).
func NewWith(broker, user string, limits *LimitStore) model {
	return NewWithHooks(broker, user, limits, Hooks{})
}

// NewWithHooks is NewWith plus the host-supplied hooks for the in-TUI provider /
// account / money flows.
func NewWithHooks(broker, user string, limits *LimitStore, hooks Hooks) model {
	m := newBase(broker, user, limits)
	m.hooks = hooks
	return m
}

func newBase(broker, user string, limits *LimitStore) model {
	ci := textinput.New()
	// We render the `rog ›` lockup ourselves in promptLine, so the input carries no
	// prompt of its own (avoids a doubled marker). Its View() still echoes live.
	ci.Prompt = ""
	ci.Placeholder = "search · connect · chat · share · login · topup · grant · limits · balance · help · quit"
	ch := textinput.New()
	ch.Prompt = ""
	ch.Placeholder = "type to talk on channel  ·  / for in-session commands"
	return model{broker: broker, user: user, cmd: ci, chatIn: ch, proxyAddr: "127.0.0.1:4141", status: "tuning in…", alert: &alertBox{}, limits: limits}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(fetchOffers(m.broker), fetchBalance(m.broker, m.user), tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.frame++
		if m.alert != nil {
			if a := m.alert.take(); a != "" {
				m.status = stEmber.Render("⚡ " + a)
			}
		}
		// Periodic band re-scan: the tick is 160ms; every ~rescanEveryFrames (~5s) we
		// pull a fresh /discover so the band table + the "is a station on air" check
		// stay live without the user pressing r. This keeps the consumer + share views
		// honest about who is actually on air (the broker ages a node out at ~35s).
		if m.frame%rescanEveryFrames == 0 {
			return m, tea.Batch(tick(), fetchOffers(m.broker))
		}
		return m, tick()
	case offersMsg:
		m.offers = []offer(msg)
		m.scanErr = false
		m.scanned = true // a scan returned (even empty) -> stop showing the loading pose
		m.bands = groupBands(m.offers, m.limits)
		if m.cursor >= len(m.bands) {
			m.cursor = 0
		}
		// "wait & notify" stub: if a watched band has dipped under the limit, say so.
		notified := false
		if m.watching != "" {
			for _, b := range m.bands {
				if b.model == m.watching && b.online {
					lim := m.limits.resolve(b.model)
					if lim.MaxOut == 0 || b.minOut <= lim.MaxOut {
						m.status = stLive.Render("⚡ " + b.model + " dipped under your limit (" + money(b.minOut) + " out) - tune in")
						m.watching = ""
						notified = true
					}
				}
			}
		}
		// Don't clobber a fresh dip-under notification, an in-flight relay, or a modal
		// sub-screen's own status with the periodic scan summary - it's a browse-mode
		// affordance only; in CHANNEL the transcript carries the signal.
		if !notified && !m.relaying && (m.mode == modeBrowse || m.mode == modeCommand) {
			m.status = fmt.Sprintf("%s · %s on air", plural(len(m.bands), "band"), plural(countOnline(m.offers), "station"))
		}
		return m, nil
	case balanceMsg:
		m.balance, m.haveBal = float64(msg), true
		return m, nil
	case chatMsg:
		m.relaying = false
		m.sessCost += msg.cost
		reply := msg.reply
		if strings.TrimSpace(reply) == "" {
			// The station answered but with no content (an all-reasoning turn, or an
			// empty completion). Never render a blank arrow - say so plainly so the turn
			// is not a silent no-response.
			reply = stDim.Render("(the station replied with no text)")
		} else {
			reply = stLive.Render("◂ ") + reply
		}
		m.transcript = append(m.transcript, reply, stDim.Render("   "+msg.status))
		// Refresh the wallet after a billed turn so the header balance stays true.
		return m, fetchBalance(m.broker, m.user)
	case chatErrMsg:
		// A chat turn FAILED. The fix for the founder's silent no-response: the failure
		// lands IN the CHANNEL transcript (red, inline) - not just the footer - so the
		// user always sees an outcome right where they were typing.
		m.relaying = false
		m.transcript = append(m.transcript, stRed.Render("✕ ")+stEmber.Render(string(msg)))
		m.status = stEmber.Render("! " + string(msg))
		return m, nil
	case errMsg:
		m.relaying = false
		if strings.HasPrefix(string(msg), "broker unreachable") {
			m.scanErr = true // the band scan dropped -> Ping goes "...static"
		}
		m.status = stEmber.Render("! " + string(msg))
		return m, nil
	case loginMsg:
		m.ghLogin = string(msg)
		m.status = stLive.Render("◆ logged in as @" + string(msg) + " - you can now earn as a provider")
		return m, nil
	case topupMsg:
		m.status = stEmber.Render("top up: ") + stKey.Render(string(msg)) + stDim.Render("  (open to pay)")
		return m, nil
	case grantMsg:
		m.status = stLive.Render("◆ grant created - secret (shown once): ") + stKey.Render(msg.secret)
		return m, nil
	case grantListMsg:
		m.grantList = []GrantRow(msg)
		if len(m.grantList) == 0 {
			m.status = stDim.Render("no grants yet - /grant create <name> mints a free key")
		} else {
			m.status = stLive.Render(plural(len(m.grantList), "grant") + " - see the panel")
		}
		return m, nil
	case flowErrMsg:
		m.status = stEmber.Render("! " + string(msg))
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	// route to the active text input
	var cmd tea.Cmd
	switch m.mode {
	case modeCommand:
		m.cmd, cmd = m.cmd.Update(msg)
	case modeChat:
		m.chatIn, cmd = m.chatIn.Update(msg)
	}
	return m, cmd
}

func (m model) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeCommand:
		switch k.String() {
		case "enter":
			cmd := strings.TrimSpace(m.cmd.Value())
			m.cmd.SetValue("")
			m.mode = modeBrowse
			return m.run(cmd)
		case "esc":
			m.cmd.SetValue("")
			m.mode = modeBrowse
			return m, nil
		}
		var c tea.Cmd
		m.cmd, c = m.cmd.Update(k)
		return m, c
	case modeChat:
		switch k.String() {
		case "esc", "tab":
			// leave CHANNEL for BROWSE; the channel + endpoint stay live.
			m.mode = modeBrowse
			m.chatIn.Blur()
			return m, nil
		case "enter":
			p := strings.TrimSpace(m.chatIn.Value())
			if p == "" || m.connected == nil {
				return m, nil
			}
			m.chatIn.SetValue("")
			// A leading / in-session is a slash command, not a chat turn.
			if strings.HasPrefix(p, "/") {
				return m.runSession(p)
			}
			turn := p
			if m.sysPrompt != "" {
				turn = m.sysPrompt + "\n\n" + p
			}
			m.transcript = append(m.transcript, stSelText.Render("▸ ")+p)
			// Pre-flight: if no station for this band is on air right now, say so in the
			// transcript immediately instead of firing a request the broker will bounce
			// with a 503 the user might never see. (Best-effort: a stale scan still falls
			// through to the real request + its inline error.)
			if !m.bandOnAir(m.connected.Model) {
				m.transcript = append(m.transcript, stRed.Render("✕ ")+stEmber.Render("no station on air for "+m.connected.Model+" right now - press r in BROWSE to re-scan, or /share to put one up"))
				return m, nil
			}
			m.relaying = true
			m.relayStart = time.Now()
			return m, sendChat(m.broker, m.user, m.connected.Model, turn, m.confidentialOnly)
		}
		var c tea.Cmd
		m.chatIn, c = m.chatIn.Update(k)
		return m, c
	case modeHelp:
		m.mode = modeBrowse
		return m, nil
	case modeConnectConfirm:
		switch k.String() {
		case "enter", "y", "Y":
			return m.openChannel()
		case "d", "D": // toggle the detail block (default screen stays minimal)
			m.showDetail = !m.showDetail
			return m, nil
		default: // esc, n, N, anything else - default DENY
			m.mode = modeBrowse
			m.status = stDim.Render("denied - no channel opened")
			return m, nil
		}
	case modeOverLimit:
		return m.onOverLimitKey(k)
	case modeLimits:
		return m.onLimitsKey(k)
	case modeShare:
		return m.onShareKey(k)
	default: // browse
		switch k.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "/", ":":
			m.mode = modeCommand
			m.cmd.Focus()
			return m, textinput.Blink
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.bands)-1 { // browse list is the bands (grouped), not raw offers
				m.cursor++
			}
		case "enter":
			return m.connect()
		case "c", "tab":
			if m.connected != nil {
				m.mode = modeChat
				m.chatIn.Focus()
				return m, textinput.Blink
			}
		case "m":
			if m.connected != nil {
				m.minimized = !m.minimized
			}
		case "?":
			m.mode = modeHelp
		case "r":
			m.status = "re-scanning the band…"
			m.scanErr, m.scanned = false, false // back to the loading pose while we retune
			return m, fetchOffers(m.broker)
		}
	}
	return m, nil
}

// runSession dispatches an in-CHANNEL slash command (the pi.dev-style session
// harness). It is a clean dispatch so deeper agentic tool-use can be added later;
// for now it covers re-tune, transcript, system prompt, cost, privacy, endpoint,
// help, and leave. Anything unrecognized is echoed as a hint, never sent as chat.
func (m model) runSession(line string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(line)
	cmd := strings.TrimPrefix(fields[0], "/")
	arg := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	sysLine := func(s string) {
		m.transcript = append(m.transcript, stDim.Render("· ")+stDim.Render(s))
	}
	switch cmd {
	case "model", "tune", "retune":
		// re-tune: drop back to the band browser to pick a new channel.
		m.mode = modeBrowse
		m.chatIn.Blur()
		m.status = stDim.Render("pick a band, enter to re-tune (the channel stays open until you do)")
		return m, nil
	case "clear":
		m.transcript = nil
		m.sessCost = 0
		sysLine("transcript cleared")
		return m, nil
	case "save":
		// save is a labeled local action: the transcript already lives in-memory;
		// we surface where it would write (no disk I/O from the TUI by design).
		sysLine("session has " + fmt.Sprintf("%d", len(m.transcript)) + " lines (kept in-memory this session)")
		return m, nil
	case "system":
		if arg == "" {
			if m.sysPrompt == "" {
				sysLine("no system prompt set · /system <prompt> to set one")
			} else {
				sysLine("system: " + m.sysPrompt)
			}
			return m, nil
		}
		m.sysPrompt = arg
		sysLine("system prompt set · prepended to each turn")
		return m, nil
	case "cost":
		sysLine("session cost so far: " + dollars(m.sessCost) + " · balance " + m.balDollars())
		return m, nil
	case "confidential", "conf":
		m.confidentialOnly = !m.confidentialOnly
		if m.confidentialOnly {
			sysLine("confidential-only ON · routing only to TEE-attested nodes")
		} else {
			sysLine("confidential-only off")
		}
		return m, nil
	case "endpoint", "ep":
		if m.endpoint == "" {
			sysLine("no endpoint yet")
			return m, nil
		}
		sysLine("endpoint " + m.endpoint + " · key " + m.apikey + " · model " + m.connected.Model)
		return m, nil
	case "help", "h":
		sysLine("/model /clear /save /system <p> /cost /confidential /endpoint /help /quit · tab leaves channel")
		return m, nil
	case "quit", "q":
		return m, tea.Quit
	default:
		sysLine("unknown: /" + cmd + " · /help for in-session commands")
		return m, nil
	}
}

// run handles a slash command.
func (m model) run(cmd string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return m, nil
	}
	switch fields[0] {
	case "search", "s":
		m.status = "re-scanning the band…"
		m.scanErr, m.scanned = false, false
		return m, fetchOffers(m.broker)
	case "connect", "tune":
		return m.connect()
	case "chat":
		if m.connected != nil {
			m.mode = modeChat
			m.chatIn.Focus()
			return m, textinput.Blink
		}
		m.status = "tune in to a station first (Enter)"
	case "balance", "bal":
		if m.haveBal && m.balance <= 0 {
			m.status = stEmber.Render("balance empty") + stDim.Render(" - ") + stKey.Render("/topup") + stDim.Render(" to add funds")
		}
		return m, fetchBalance(m.broker, m.user)
	case "limits", "limit":
		m.enterLimits()
		return m, nil
	case "config", "cfg":
		m.status = fmt.Sprintf("broker %s · user %s  (rogerai config set broker <url>)", m.broker, m.user)
	case "confidential", "conf":
		m.confidentialOnly = !m.confidentialOnly
		if m.confidentialOnly {
			m.status = stGold.Render("◆ confidential-only ON") + " - routing only to TEE-attested nodes"
		} else {
			m.status = "confidential-only off"
		}
	case "share":
		return m.doShare(fields[1:])
	case "login":
		return m.doLogin()
	case "topup", "add":
		return m.doTopup(fields[1:])
	case "grant":
		return m.doGrant(fields[1:])
	case "endpoint", "ep":
		if m.connected == nil {
			m.status = "tune in first to get an endpoint"
		}
	case "help", "h":
		m.mode = modeHelp
	case "quit", "q":
		return m, tea.Quit
	default:
		m.status = "unknown: /" + fields[0] + "  (try /help)"
	}
	return m, nil
}

// doShare opens the k9s-style provider table (modeShare) instead of silently
// auto-committing a share - the founder's "it just auto-selected and I couldn't
// tell which model" complaint. It detects the local models, lists them with an
// ON-AIR / OFF-AIR status + price + live metrics, and lets the user flip any model
// on/off air from a highly visible cursor. `/share off` still stops everything;
// `/share <model>` is a quick shortcut that flips one model on air directly.
func (m model) doShare(args []string) (tea.Model, tea.Cmd) {
	if len(args) > 0 && (args[0] == "off" || args[0] == "stop") {
		m.stopAllShares()
		m.status = stDim.Render("off air - you stopped sharing")
		return m, nil
	}
	found := detect.Detect()
	if len(found) == 0 {
		m.status = stEmber.Render("! no local LLM detected - start Ollama/LM Studio/llama.cpp/vLLM, then /share")
		return m, nil
	}
	m.loadShareRows(found)
	// `/share <model>` shortcut: flip that exact model on air, then show the table.
	if len(args) > 0 {
		want := args[0]
		for i, r := range m.shareRows {
			if r.model == want {
				m.shareCursor = i
				mm := &m
				mm.toggleShareAt(i)
				m = *mm
				break
			}
		}
	}
	m.mode = modeShare
	if len(m.shareRows) == 0 {
		m.status = stEmber.Render("! the local server reported no models - check it serves /v1/models")
	} else {
		m.status = stDim.Render("provider table - ↑↓ select, enter/a toggle ON-AIR, esc done")
	}
	return m, nil
}

// loadShareRows builds the provider table from detected servers: one row per
// served model id (de-duplicated), remembering the upstream chat URL to back the
// shares. The first reachable server is used as the upstream (the share path is
// in-process; multi-server fan-out is deferred).
func (m *model) loadShareRows(found []detect.Found) {
	if m.shares == nil {
		m.shares = map[string]*agent.Session{}
	}
	pick := found[0]
	m.shareUp = normalizeUpstream(pick.Chat)
	seen := map[string]bool{}
	rows := make([]shareRow, 0, len(pick.Models))
	for _, mdl := range pick.Models {
		if mdl == "" || seen[mdl] {
			continue
		}
		seen[mdl] = true
		ctxLen := pick.Ctx[mdl]
		if ctxLen <= 0 {
			ctxLen = 32768
		}
		rows = append(rows, shareRow{model: mdl, ctx: ctxLen})
	}
	// Put the saved onboarding model first so the obvious default is at the cursor.
	if def := m.hooks.ShareModel; def != "" {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].model == def && rows[j].model != def })
	}
	m.shareRows = rows
	if m.shareCursor >= len(rows) {
		m.shareCursor = 0
	}
}

// toggleShareAt flips the on-air state of the provider-table row at index i: a
// model that is off air goes ON AIR (starts an in-process agent.Session against
// the local upstream at the saved/free price), one that is on air goes off. It
// keeps m.share / m.onAir pointing at the headline (any-live) session so the
// existing ON-AIR panel + header indicator still work.
func (m *model) toggleShareAt(i int) {
	if i < 0 || i >= len(m.shareRows) {
		return
	}
	if m.shares == nil {
		m.shares = map[string]*agent.Session{}
	}
	row := m.shareRows[i]
	if sess, ok := m.shares[row.model]; ok && sess != nil {
		sess.Stop()
		delete(m.shares, row.model)
		m.refreshShareHeadline()
		m.status = stDim.Render("off air - stopped sharing ") + stKey.Render(row.model)
		return
	}
	node := m.hooks.NodeID
	if node == "" {
		node = "node"
	}
	// Free by default (visible + changeable in the table); the saved onboarding
	// price applies only to the saved model, so a different model shares FREE unless
	// the user set a global price. A priced share still requires `rogerai login`.
	priceIn, priceOut := 0.0, 0.0
	if row.model == m.hooks.ShareModel {
		priceIn, priceOut = m.hooks.SharePriceI, m.hooks.SharePriceO
	}
	sess, err := agent.Start(agent.Config{
		Broker: m.broker, Upstream: m.shareUp, NodeID: node,
		Region: "home", HW: m.hooks.HW, Model: row.model,
		PriceIn: priceIn, PriceOut: priceOut, Ctx: row.ctx, Parallel: 4,
	})
	if err != nil {
		m.status = stEmber.Render("! could not put " + row.model + " on air: " + err.Error())
		return
	}
	m.shares[row.model] = sess
	m.refreshShareHeadline()
	kind := "FREE"
	if priceIn > 0 || priceOut > 0 {
		kind = dollars(priceOut) + "/1M out"
	}
	m.status = stLive.Render("● ON AIR ") + stDim.Render("- sharing ") + stKey.Render(row.model) + stDim.Render(" ("+kind+")")
}

// refreshShareHeadline repoints m.share / m.onAir at any still-live session so the
// header ON-AIR badge and the onAirPanel reflect the current set after a toggle.
func (m *model) refreshShareHeadline() {
	m.share, m.onAir = nil, false
	for _, sess := range m.shares {
		if sess != nil {
			m.share, m.onAir = sess, true
			return
		}
	}
}

// stopAllShares takes every model off air (used by /share off and a clean exit).
func (m *model) stopAllShares() {
	for mdl, sess := range m.shares {
		if sess != nil {
			sess.Stop()
		}
		delete(m.shares, mdl)
	}
	m.share, m.onAir = nil, false
}

// onShareKey drives the k9s-style provider table: up/down (j/k) move the
// reverse-video cursor, enter/a/space toggle the selected model on/off air, r
// re-detects local models, esc/q leaves (shares keep running in the background).
func (m *model) onShareKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.mode = modeBrowse
		return m, nil
	case "up", "k":
		if m.shareCursor > 0 {
			m.shareCursor--
		}
	case "down", "j":
		if m.shareCursor < len(m.shareRows)-1 {
			m.shareCursor++
		}
	case "enter", "a", " ", "space":
		m.toggleShareAt(m.shareCursor)
	case "r":
		if found := detect.Detect(); len(found) > 0 {
			m.loadShareRows(found)
			m.status = stDim.Render("re-detected local models")
		} else {
			m.status = stEmber.Render("! no local LLM detected")
		}
	}
	return m, nil
}

// doLogin runs the GitHub device flow in-TUI (async; the result lands as a
// loginMsg / flowErrMsg).
func (m model) doLogin() (tea.Model, tea.Cmd) {
	if m.hooks.Login == nil {
		m.status = stDim.Render("login unavailable in this build - run `rogerai login`")
		return m, nil
	}
	m.status = stDim.Render("opening GitHub device login - follow the code shown in your terminal…")
	broker, clientID := m.broker, m.hooks.GitHubID
	login := m.hooks.Login
	return m, func() tea.Msg {
		l, err := login(broker, clientID)
		if err != nil {
			return flowErrMsg("login failed: " + err.Error())
		}
		return loginMsg(l)
	}
}

// doTopup opens checkout (async; the URL lands as a topupMsg).
func (m model) doTopup(args []string) (tea.Model, tea.Cmd) {
	if m.hooks.TopupURL == nil {
		m.status = stDim.Render("top-up unavailable in this build - run `rogerai balance --topup`")
		return m, nil
	}
	usd := 10.0
	if len(args) > 0 {
		if f, err := strconv.ParseFloat(args[0], 64); err == nil && f > 0 {
			usd = f
		}
	}
	broker, user, topup := m.broker, m.user, m.hooks.TopupURL
	m.status = stDim.Render("opening checkout…")
	return m, func() tea.Msg {
		url, err := topup(broker, user, usd)
		if err != nil {
			return flowErrMsg("top-up failed: " + err.Error())
		}
		return topupMsg(url)
	}
}

// doGrant creates or lists owner grant keys in-TUI. `/grant create <name>` mints a
// FREE key (shown once); `/grant` or `/grant list` lists them.
func (m model) doGrant(args []string) (tea.Model, tea.Cmd) {
	if len(args) >= 1 && (args[0] == "create" || args[0] == "new") {
		if m.hooks.GrantCreate == nil {
			m.status = stDim.Render("grants unavailable in this build - run `rogerai grant create`")
			return m, nil
		}
		name := "my-bots"
		if len(args) >= 2 {
			name = args[1]
		}
		broker, create := m.broker, m.hooks.GrantCreate
		m.status = stDim.Render("creating free grant " + name + "…")
		return m, func() tea.Msg {
			secret, err := create(broker, name, true)
			if err != nil {
				return flowErrMsg("grant create failed: " + err.Error())
			}
			return grantMsg{secret: secret}
		}
	}
	// default: list
	if m.hooks.GrantList == nil {
		m.status = stDim.Render("grants unavailable in this build - run `rogerai grant list`")
		return m, nil
	}
	broker, list := m.broker, m.hooks.GrantList
	return m, func() tea.Msg {
		rows, err := list(broker)
		if err != nil {
			return flowErrMsg("grant list failed: " + err.Error())
		}
		return grantListMsg(rows)
	}
}

// connect is two-phase: it builds the quote for the selected band and enters the
// cost-confirmation screen (or the over-limit screen if the cheapest station is
// above the user's max). The proxy is only bound on accept (openChannel).
func (m model) connect() (tea.Model, tea.Cmd) {
	if len(m.bands) == 0 || m.cursor >= len(m.bands) {
		return m, nil
	}
	bd := m.bands[m.cursor]
	if !bd.online || bd.cheapest == nil {
		m.status = stDim.Render("no station on air for " + bd.model + " - try /search")
		return m, nil
	}
	lim := m.limits.resolve(bd.model)
	typ := m.limits.typical()
	q := quote{b: bd, limit: lim, typical: typ, estReply: bd.minOut * float64(typ) / 1e6}
	if lim.MaxOut > 0 && bd.minOut > lim.MaxOut {
		q.overLimit = true
		m.q = q
		m.editBuf = money(bd.minOut) // pre-fill the smallest unblocking raise
		m.mode = modeOverLimit
		return m, nil
	}
	m.q = q
	m.showDetail = false // open simple; [d] expands
	m.mode = modeConnectConfirm
	return m, nil
}

// openChannel binds the local proxy (once) and marks the band connected, sending
// the resolved spend limits to the relay so routing stays within them. Called
// only after the user accepts the cost confirmation.
func (m model) openChannel() (tea.Model, tea.Cmd) {
	q := m.q
	o := *q.b.cheapest
	if !m.proxyUp {
		ln, err := net.Listen("tcp", m.proxyAddr)
		if err != nil {
			m.mode = modeBrowse
			m.status = stEmber.Render("! endpoint bind failed: " + err.Error())
			return m, nil
		}
		m.endpoint = "http://" + ln.Addr().String() + "/v1"
		m.proxyUp = true
		// Failover alerts from the relay land in a shared box the tick loop drains
		// onto the status line - bots keep hitting the same endpoint regardless.
		alert := m.alert
		opts := client.ProxyOptions{
			Broker: m.broker, User: m.user, Confidential: m.confidentialOnly,
			MaxPriceIn: q.limit.MaxIn, MaxPriceOut: q.limit.MaxOut, MinTPS: q.limit.MinTPS,
			Alert: func(s string) { alert.set(s) },
		}
		go http.Serve(ln, client.ProxyHandler(opts))
	}
	m.connected = &o
	m.apikey = "roger-local"
	// Connecting auto-switches to CHANNEL mode and compacts the header (the founder's
	// "compact-on-connect"). The endpoint stays live regardless of mode.
	m.mode = modeChat
	m.chatIn.Focus()
	if len(m.transcript) == 0 {
		m.transcript = append(m.transcript, stDim.Render("◂ ")+stLive.Render("roger that")+stDim.Render(" - channel open. type to talk, /help for in-session commands."))
	}
	m.status = stGold.Render("◆ ") + stLive.Render("on channel ") + o.NodeID + stDim.Render(" - endpoint live · roger that")
	return m, textinput.Blink
}

// onOverLimitKey drives the over-limit screen (3.3): inline numeric edit of your
// max, up/down nudge by 0.01, enter = save & re-check, esc/N = deny, w = wait.
func (m *model) onOverLimitKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "n", "N":
		m.mode = modeBrowse
		m.status = stDim.Render("denied - no channel opened")
		return m, nil
	case "w":
		// "wait & notify when it dips under" - stubbed as a labeled no-op: watch the
		// band, the offers tick drops a status line if it dips under (real notify P1).
		m.watching = m.q.b.model
		m.mode = modeBrowse
		m.status = stDim.Render("waiting - will flag " + m.q.b.model + " when it dips under " + money(m.q.limit.MaxOut))
		return m, nil
	case "up":
		m.editBuf = nudge(m.editBuf, +0.01)
		return m, nil
	case "down":
		m.editBuf = nudge(m.editBuf, -0.01)
		return m, nil
	case "backspace":
		if len(m.editBuf) > 0 {
			m.editBuf = m.editBuf[:len(m.editBuf)-1]
		}
		return m, nil
	case "enter":
		nv, err := strconv.ParseFloat(strings.TrimSpace(m.editBuf), 64)
		if err != nil || nv < m.q.b.minOut {
			// still below the band - keep blocked (validation), leave the user here.
			m.status = stEmber.Render("still below the band (" + money(m.q.b.minOut) + ") - raise it or esc")
			return m, nil
		}
		// persist the new per-model max, then re-run the connect check.
		lim := m.limits.resolve(m.q.b.model)
		lim.MaxOut = nv
		m.limits.set(m.q.b.model, lim)
		m.bands = groupBands(m.offers, m.limits)
		m.mode = modeBrowse
		return m.connect()
	default:
		if d := digitsDot(k.String()); d != "" {
			m.editBuf += d
		}
		return m, nil
	}
}

// enterLimits builds the model list for the limits view (3.4): every band with a
// set limit, unioned with the bands currently on air, sorted.
func (m *model) enterLimits() {
	seen := map[string]bool{}
	var models []string
	if m.limits != nil {
		for mdl := range m.limits.Models {
			if !seen[mdl] {
				seen[mdl] = true
				models = append(models, mdl)
			}
		}
	}
	for _, b := range m.bands {
		if !seen[b.model] {
			seen[b.model] = true
			models = append(models, b.model)
		}
	}
	sort.Strings(models)
	m.limModels = models
	if m.limCursor >= len(models) {
		m.limCursor = 0
	}
	m.editBuf = ""
	m.editField = -1 // not editing yet
	m.mode = modeLimits
}

// onLimitsKey drives the per-model limits view (3.4): up/down move, enter edits
// (Tab between out-price and min-tps), d clears, esc done.
func (m *model) onLimitsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	editing := m.editField >= 0
	if !editing {
		switch k.String() {
		case "esc", "q":
			m.mode = modeBrowse
			return m, nil
		case "up", "k":
			if m.limCursor > 0 {
				m.limCursor--
			}
		case "down", "j":
			if m.limCursor < len(m.limModels)-1 {
				m.limCursor++
			}
		case "d":
			if m.limCursor < len(m.limModels) {
				m.limits.clear(m.limModels[m.limCursor])
				m.enterLimits()
			}
		case "enter":
			if m.limCursor < len(m.limModels) {
				lim := m.limits.resolve(m.limModels[m.limCursor])
				m.editField = 0
				m.editBuf = trimZero(lim.MaxOut)
			}
		}
		return m, nil
	}
	// editing a field
	switch k.String() {
	case "esc":
		m.editField = -1
		return m, nil
	case "tab":
		m.commitLimitField()
		m.editField = (m.editField + 1) % 2
		lim := m.limits.resolve(m.limModels[m.limCursor])
		if m.editField == 0 {
			m.editBuf = trimZero(lim.MaxOut)
		} else {
			m.editBuf = trimZero(lim.MinTPS)
		}
		return m, nil
	case "enter":
		m.commitLimitField()
		m.editField = -1
		return m, nil
	case "backspace":
		if len(m.editBuf) > 0 {
			m.editBuf = m.editBuf[:len(m.editBuf)-1]
		}
		return m, nil
	default:
		if d := digitsDot(k.String()); d != "" {
			m.editBuf += d
		}
		return m, nil
	}
}

// commitLimitField writes the current edit buffer into the focused field of the
// selected model's limit and persists it.
func (m *model) commitLimitField() {
	if m.limCursor >= len(m.limModels) {
		return
	}
	mdl := m.limModels[m.limCursor]
	lim := m.limits.resolve(mdl)
	v, _ := strconv.ParseFloat(strings.TrimSpace(m.editBuf), 64)
	if m.editField == 0 {
		lim.MaxOut = v
	} else {
		lim.MinTPS = v
	}
	m.limits.set(mdl, lim)
}

// nudge adjusts a numeric edit buffer by delta, clamped at 0, 2dp.
func nudge(buf string, delta float64) string {
	v, _ := strconv.ParseFloat(strings.TrimSpace(buf), 64)
	v += delta
	if v < 0 {
		v = 0
	}
	return fmt.Sprintf("%.2f", v)
}

// digitsDot returns a single digit or dot keypress (for the inline numeric edit),
// or "" for anything else.
func digitsDot(s string) string {
	if len(s) == 1 && (s[0] >= '0' && s[0] <= '9' || s[0] == '.') {
		return s
	}
	return ""
}

// trimZero renders a float for editing, blank for 0 (so "no cap" shows empty).
func trimZero(v float64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%g", v)
}

// narrowCols is the width below which the TUI reflows to a single, slimmer column
// (drops the band table's signal/flags columns, two-line footer).
const narrowCols = 64

// effWidth returns the width to DRAW at. Width 0 is the unsized initial frame
// (before the first WindowSizeMsg) - balloon to 88 so the first paint isn't a
// 1-column sliver. A genuinely small terminal draws at its REAL width (floored at
// 40), so the rules + footer match the viewport instead of overflowing at 88.
// (TUI-V2-CRITIQUE A.)
func (m model) effWidth() int {
	if m.width == 0 {
		return 88
	}
	if m.width < 40 {
		return 40
	}
	return m.width
}

// narrow reports whether to use the single-column reflow (real width is small).
// At exactly narrowCols (64) the wide band grid (~67 cols) would still overflow,
// so the boundary is inclusive: width <= 64 reflows.
func (m model) narrow() bool { return m.width != 0 && m.width <= narrowCols }

// ---- view ----
func (m model) View() string {
	w := m.effWidth()
	var b strings.Builder
	b.WriteString(m.header(w) + "\n")
	switch m.mode {
	case modeHelp:
		b.WriteString(m.helpView())
	case modeChat:
		b.WriteString(m.chatView(w))
	case modeConnectConfirm:
		b.WriteString(m.confirmView(w))
	case modeOverLimit:
		b.WriteString(m.overLimitView(w))
	case modeLimits:
		b.WriteString(m.limitsView(w))
	case modeShare:
		b.WriteString(m.shareView(w))
	default:
		b.WriteString(m.browseView(w))
	}
	if m.connected != nil && m.mode != modeChat && m.mode != modeConnectConfirm && m.mode != modeOverLimit && m.mode != modeLimits && m.mode != modeShare {
		b.WriteString("\n" + m.endpointPanel(w))
	}
	// The ON AIR provider panel rides under the browse view whenever /share is live.
	if m.onAir && m.share != nil && (m.mode == modeBrowse || m.mode == modeCommand) {
		b.WriteString("\n" + m.onAirPanel(w))
	}
	// The command prompt is always present in browse/command mode so it is never a
	// mystery WHERE to type: a labeled `rog ›` line that echoes every keystroke
	// live (its textinput View() is re-rendered each Update). modeChat owns its own
	// always-live prompt inside chatView.
	if m.mode == modeBrowse || m.mode == modeCommand {
		b.WriteString("\n" + m.promptLine(w))
	}
	b.WriteString("\n" + m.footer(w))
	return b.String()
}

// promptLine renders the always-visible command prompt. It shows the live
// textinput View() (cursor + echoed text) when focused, or a calm hint to press
// `/` when idle, so the user always sees a clear, labeled place to type.
func (m model) promptLine(w int) string {
	if m.mode == modeCommand {
		return stPrompt.Render("  rog › ") + m.cmd.View()
	}
	hint := "press / to type a command  ·  enter to tune in"
	if m.narrow() {
		hint = "/ command · ⏎ tune in"
	}
	return stPrompt.Render("  rog › ") + stDim.Render(hint)
}

// confirmView is the connect-time cost confirmation (3.2): the deal + an explicit
// accept/deny with the SAFE default on DENY.
func (m model) confirmView(w int) string {
	q := m.q
	bd := q.b
	st := bd.cheapest
	var b strings.Builder

	// Header: model, the station you'd lock, throughput, lineage.
	verified := ""
	if bd.lineage > 0 {
		verified = stDim.Render("   ") + stGold.Render("◆ verified")
	}
	b.WriteString("\n" + stGold.Render("  ◆ ") + stSelText.Render(bd.model) + stDim.Render("   via @") + st.NodeID + stDim.Render("   ") + tpsCell(st.TPS, st.Online) + verified + "\n\n")

	// One glanceable line: what you pay, that it's under your cap, est cost.
	cap := ""
	if q.limit.MaxOut > 0 {
		cap = stDim.Render("   ·   ") + stLive.Render("under your "+money(q.limit.MaxOut)+" cap")
	}
	b.WriteString("    " + stEmber.Render(money(bd.minOut)) + stDim.Render(" $/1M out") + cap +
		stDim.Render("   ·   ~"+dollars(q.estReply)+" / reply") + "\n")

	// Everything else is behind [d] - keep the default screen simple.
	if m.showDetail {
		b.WriteString("\n")
		if bd.stations > 1 {
			b.WriteString(stDim.Render("    live range   ") + stEmber.Render(rangeStr(bd)) + stDim.Render(" $/1M out  ("+fmt.Sprintf("%d", bd.stations)+" on air)") + "\n")
		}
		b.WriteString(stDim.Render("    input price  ") + stEmber.Render(money(st.PriceIn)) + stDim.Render(" $/1M in") + "\n")
		if m.haveBal {
			reps := 0.0
			if q.estReply > 0 {
				reps = m.balance / q.estReply
			}
			b.WriteString(stDim.Render(fmt.Sprintf("    balance      %s   (~%.0f replies)", dollars(m.balance), reps)) + "\n")
		}
		b.WriteString(stDim.Render("    locked       each reply price-locks at send; a hold pre-auths the session") + "\n")
	}

	b.WriteString("\n")
	b.WriteString("       " + stLive.Render("accept · open channel") + "     " + stDim.Render("deny · back") + "     " + stDim.Render("more detail") + "\n")
	b.WriteString("       " + stKey.Render("[ enter / y ]") + "         " + stDim.Render("[ esc / n ]") + "     " + stKey.Render("[ d ]") + "\n")
	return b.String()
}

// overLimitView is the over-limit + inline edit-your-max screen (3.3).
func (m model) overLimitView(w int) string {
	q := m.q
	bd := q.b
	st := bd.cheapest
	gap := bd.minOut - q.limit.MaxOut
	pct := 0.0
	if q.limit.MaxOut > 0 {
		pct = gap / q.limit.MaxOut * 100
	}
	var b strings.Builder
	b.WriteString("\n" + stEmber.Render("  ⚠ the band is above your limit") + "       " + stSelText.Render(bd.model) + "\n\n")
	b.WriteString(stDim.Render("    cheapest on air   ") + stEmber.Render(money(bd.minOut)) + stDim.Render(" $/1M out   @"+st.NodeID+"  "+st.Region+"  ") + tpsCell(st.TPS, st.Online) + "\n")
	b.WriteString(stDim.Render("    your max          ") + stEmber.Render(money(q.limit.MaxOut)) + stDim.Render(" $/1M out") + "\n")
	b.WriteString(stDim.Render(fmt.Sprintf("    gap               +%.2f  (%.0f%% over)   you would pay ", gap, pct)+dollars(bd.minOut*float64(q.typical)/1e6)+" / reply") + "\n\n")
	// the inline edit row
	editShown := m.editBuf
	hint := stDim.Render("min " + money(bd.minOut))
	if v, err := strconv.ParseFloat(strings.TrimSpace(m.editBuf), 64); err == nil && v >= bd.minOut {
		hint = stLive.Render("▸ enough to tune in now")
	} else {
		hint = stEmber.Render("still below the band (" + money(bd.minOut) + ")")
	}
	b.WriteString(stDim.Render("    raise your max for "+bd.model+"   (was "+money(q.limit.MaxOut)+")") + "\n")
	b.WriteString("      $/1M out   " + stSelText.Render("▏"+editShown+"▏") + "   " + hint + "\n\n")
	b.WriteString("    " + stKey.Render("⏎ save & re-check") + stDim.Render("   ↑ +0.01   ↓ -0.01   ") + stDim.Render("w wait & notify   esc deny") + "\n")
	return b.String()
}

// limitsView is the per-model spend-limits editor (3.4).
func (m model) limitsView(w int) string {
	var b strings.Builder
	b.WriteString("\n" + stBrand.Render("  spend limits") + stDim.Render("    what you are willing to pay, per band") + "\n\n")
	b.WriteString(stDim.Render(fmt.Sprintf("    %-22s %-13s %-10s %-15s %s", "band", "max $/1M out", "min t/s", "live now", "status")) + "\n")
	if len(m.limModels) == 0 {
		b.WriteString(stDim.Render("    (none yet - press a / set one in `rogerai config set-limit`)") + "\n")
	}
	for i, mdl := range m.limModels {
		cur := " "
		nameStyle := lipgloss.NewStyle().Foreground(cInk)
		if i == m.limCursor {
			cur = stSelBar.Render("▌")
			nameStyle = stSelText
		}
		lim := m.limits.resolve(mdl)
		maxOut := "-"
		if lim.MaxOut > 0 {
			maxOut = money(lim.MaxOut)
		}
		mtps := "-"
		if lim.MinTPS > 0 {
			mtps = fmt.Sprintf("%g", lim.MinTPS)
		}
		live, status := "-", stDim.Render("·")
		for _, bd := range m.bands {
			if bd.model == mdl && bd.online {
				live = rangeStr(bd)
				if lim.MaxOut > 0 && bd.minOut > lim.MaxOut {
					status = stEmber.Render(fmt.Sprintf("⚠ over by %.2f", bd.minOut-lim.MaxOut))
				} else {
					status = stLive.Render("✓ within")
				}
				break
			}
		}
		b.WriteString(fmt.Sprintf("%s   %s %s %s %s %s\n",
			cur, nameStyle.Render(pad(mdl, 22)), stEmber.Render(pad(maxOut, 13)), stDim.Render(pad(mtps, 10)), stDim.Render(pad(live, 15)), status))
	}
	if m.editField >= 0 && m.limCursor < len(m.limModels) {
		field := "max $/1M out"
		if m.editField == 1 {
			field = "min t/s"
		}
		b.WriteString("\n  " + stPanel.Render(stDim.Render("edit "+m.limModels[m.limCursor]+"   "+field+"  ")+stSelText.Render("▏"+m.editBuf+"▏")+stDim.Render("   ⏎ save   tab next field   esc cancel")) + "\n")
	}
	b.WriteString("\n    " + stDim.Render("↑↓ move   ⏎ edit   tab next field   d clear   esc done") + "\n")
	return b.String()
}

// tpsCell renders a station's signal: a live dot + measured tok/s, or offline.
func tpsCell(tps float64, online bool) string {
	dot := stDim.Render("○")
	if online {
		dot = stLive.Render("●")
	}
	if tps > 0 {
		return dot + stLive.Render(fmt.Sprintf("  %.0f t/s", tps))
	}
	return dot + stDim.Render("  - t/s")
}

// onAirPulse returns the breathing ON-AIR beacon in a FIXED-width cell so the
// header's right edge never jitters as the arcs grow/shrink. The eye is the
// live-red (#FF3B3B) on-air beacon matching the web --carrier; the arcs are
// live-green. Cadence is gated on a slow phase so it reads as a calm breath, not
// a flicker. eyeStyle lets callers pick the red brand beacon vs Ping's green eye.
func onAirPulse(frame int) string { return pulseWith(frame, stRed) }

func pulseWith(frame int, eyeStyle lipgloss.Style) string {
	// arc widths 1..3..1, on a 9-cell stage; the eye sits dead center. Under quiet
	// (NO_COLOR / pipe) anim() freezes the frame so a pipe sees a stable beacon.
	//
	// Animation craft (cited for the local design record): motion is glyph
	// substitution in a fixed monospace grid - the arcs breathe the "broadcast"
	// ripple and the eye does a tiny phosphor-decay (full • on the bright phase,
	// a faint · on the decay phase), the CRT-afterglow trick. Same approach as
	// GitHub Copilot CLI's animated banner; static under NO_COLOR / non-TTY.
	// https://github.blog/engineering/from-pixels-to-characters-the-engineering-behind-github-copilot-clis-animated-ascii-banner/
	f := anim(frame)
	arcs := []int{1, 2, 3, 2}[f/2%4]
	if quiet {
		// Freeze to the canonical two-arc ((•)) brand beacon (brand-ascii.txt §2)
		// rather than the collapsed single arc a frozen frame happens to land on,
		// so a pipe / NO_COLOR sees the recognizable on-air motif.
		arcs = 2
	}
	open := strings.Repeat("(", arcs)
	clos := strings.Repeat(")", arcs)
	// phosphor decay: the eye glows full on the breath peak, fades to a faint dot
	// on the trough. Frozen to the bright eye under quiet (no churn in a pipe).
	eye := eyeStyle.Render("•")
	if !quiet && f%4 == 0 {
		eye = stDim.Render("·")
	}
	body := stLive.Render(open) + " " + eye + " " + stLive.Render(clos)
	const stage = 9 // width of "((( • )))"
	return lipgloss.PlaceHorizontal(stage, lipgloss.Center, body)
}

// modeName returns the current mode's short label for the indicator, so the
// header badge names the actual screen (not a stale BROWSE) while you are in a
// confirm / over-limit / limits sub-screen.
func (m model) modeName() string {
	switch m.mode {
	case modeChat:
		return "CHANNEL"
	case modeConnectConfirm:
		return "CONFIRM"
	case modeOverLimit:
		return "OVER LIMIT"
	case modeLimits:
		return "LIMITS"
	case modeShare:
		return "SHARE"
	default:
		return "BROWSE"
	}
}

// header is the PERSISTENT status bar, always visible: the brand lockup with the
// live-red on-air eye + the current state. It COMPACTS to a thin one-line bar
// once a channel is open (so you never lose "what am I on + my balance"), and the
// [m] key toggles minimized vs expanded.
func (m model) header(w int) string {
	tower := stBrand.Render("▟█▙")
	name := stBrand.Render(" R O G E R") + stTag.Render(" · A I")
	eye := onAirPulse(m.frame)
	rule := stHeadRule.Render(strings.Repeat("─", w))

	// COMPACT: once connected (or the user minimized), a single thin bar carrying
	// channel + model + out-price + balance + a tiny live signal.
	if m.connected != nil && (m.minimized || m.mode == modeChat) {
		o := m.connected
		bar := stGold.Render("◆") + " " + eye + stLive.Render(" on channel ") + stSelText.Render(o.NodeID) +
			stDim.Render(" · ") + stKey.Render(o.Model) +
			stDim.Render(" · ") + stEmber.Render(dollars(o.PriceOut)+"/1M") +
			stDim.Render(" · bal ") + stEmber.Render(m.balDollars()) +
			"  " + tintSignal(signalBarsRaw(m.frame, o.TPS, true), o.TPS, true)
		return bar + "\n" + rule
	}

	// EXPANDED: brand lockup + eye on the left; the mode badge on the right. When
	// /share is live, a single ON AIR mark leads the badge (the one on-air indicator).
	left := tower + name + "  " + eye
	badge := stDim.Render("mode ") + stSelText.Render(m.modeName())
	if m.onAir {
		badge = stRed.Render("● ON AIR") + stDim.Render("  ·  ") + badge
	}
	var top string
	if m.narrow() {
		// Single column: stack the badge under the lockup so neither overflows the
		// real (narrow) width.
		top = left + "\n" + badge
	} else {
		gap := w - lipgloss.Width(left) - lipgloss.Width(badge)
		if gap < 1 {
			gap = 1
		}
		top = left + strings.Repeat(" ", gap) + badge
	}

	// the state line: while browsing, "scanning the band · N on air · balance $X";
	// once connected (expanded, not minimized) it names the channel.
	var state string
	if m.connected != nil {
		state = stGold.Render("  ◆ ") + stLive.Render("on channel ") + stSelText.Render(m.connected.NodeID) +
			stDim.Render(" · ") + stKey.Render(m.connected.Model) +
			stDim.Render(" · bal ") + stEmber.Render(m.balDollars()) + stDim.Render("  ([m] minimize)")
	} else {
		on := countOnline(m.offers)
		summary := "scanning the band…"
		if m.scanned {
			summary = fmt.Sprintf("%d on air", on)
		}
		// The beacon in the lockup above already carries the (( • )) motif, so the
		// state line drops its literal ((•)) prefix - exactly one on-air mark in the
		// header (TUI-V2-CRITIQUE C).
		state = stDim.Render("  ") + stDim.Render(summary) +
			stDim.Render(" · balance ") + stEmber.Render(m.balDollars())
	}
	return top + "\n" + state + "\n" + rule
}

// bandOnAir reports whether the latest scan shows any online station for model.
// It also counts the user's own in-process /share when it serves that model, so a
// solo founder sharing + chatting their own node is never told "no station" on a
// stale scan (the share registered but a fresh /discover hasn't come back yet).
func (m model) bandOnAir(model string) bool {
	for _, b := range m.bands {
		if b.model == model && b.online {
			return true
		}
	}
	if m.share != nil && m.share.Model() == model {
		return true
	}
	for mdl, s := range m.shares {
		if mdl == model && s != nil {
			return true
		}
	}
	return false
}

// balDollars renders the wallet balance in dollars, or "-" before it loads.
func (m model) balDollars() string {
	if !m.haveBal {
		return "-"
	}
	return dollars(m.balance)
}

func (m model) browseView(w int) string {
	if len(m.bands) == 0 {
		// Three empty cases, all filled with Ping in the dead space (never over
		// real content): the broker dropped -> Ping "...static"; still scanning
		// (no fetch back yet) -> Ping transmitting; scanned but quiet -> Ping idle.
		switch {
		case m.scanErr:
			return "\n" + pingPose(pingStatic, m.frame, w, "…static. the broker went off air - press r to retune") + "\n"
		case !m.scanned:
			return "\n" + pingPose(pingTx, m.frame, w, "tuning in… reaching for stations on air") + "\n"
		default:
			return "\n" + pingPose(pingIdle, m.frame, w, "the band is quiet - go ahead, press r to listen again…") + "\n"
		}
	}
	var b strings.Builder
	// Section heading, manual-style: a thin tab + a count, like the web's §-markers.
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("THE BAND") +
		stDim.Render(fmt.Sprintf("   %d models on air", len(m.bands))) + "\n")
	// Narrow (< 64 col): a slim three-column table (band · on air · price), dropping
	// the signal + flags columns so nothing overflows the real width. Wide: the full
	// fixed grid (band · on air · range · signal · flags). (TUI-V2-CRITIQUE A.)
	nameW := 20
	if m.narrow() {
		nameW = 14
		b.WriteString("  " + stDim.Render(fmt.Sprintf("%-14s  %-7s  %s", "band", "on air", "$/1M out")) + "\n")
	} else {
		// Column header, tabular. Widths match the body cells exactly so price + signal
		// columns line up under a fixed grid (lipgloss width, not eyeballed spacing).
		b.WriteString("  " + stDim.Render(fmt.Sprintf("%-20s  %-7s  %-17s  %-8s  %s",
			"band", "on air", "$/1M out (range)", "signal", "flags")) + "\n")
	}
	// Table width for the k9s reverse-video selection bar (spans the whole row).
	tableW := w - 2
	if tableW < 20 {
		tableW = 20
	}
	for i, bd := range m.bands {
		sel := i == m.cursor
		stationsLbl := "-"
		if bd.online {
			stationsLbl = fmt.Sprintf("%d on", bd.stations)
		}
		if m.narrow() {
			free := ""
			if bd.free {
				free = "  FREE"
			}
			// PLAIN row for the reverse-video bar; the selected row is one accent bar.
			plain := fmt.Sprintf("%s  %s  %s%s", pad(bd.model, nameW), pad(stationsLbl, 7), rangeStr(bd), free)
			if sel {
				b.WriteString(selCarat(true) + " " + rowSel(true, plain, tableW) + "\n")
				continue
			}
			// Unselected: dim band, tinted price + FREE tag.
			freeTag := ""
			if bd.free {
				freeTag = "  " + stLive.Render("FREE")
			}
			b.WriteString(selCarat(false) + " " + stDim.Render(pad(bd.model, nameW)) + "  " +
				stDim.Render(pad(stationsLbl, 7)) + "  " + stEmber.Render(rangeStr(bd)) + freeTag + "\n")
			continue
		}
		// Signal from the cheapest station's measured tps (fixed 5-cell equalizer).
		var sigTPS float64
		online := bd.online
		if bd.cheapest != nil {
			sigTPS = bd.cheapest.TPS
		}
		if sel {
			// k9s-style: the cursor row is one unmistakable reverse-video bar. We use
			// the raw (uncolored) signal glyphs so the single accent style governs the
			// whole row (a colored cell inside an accent bg reads as noise).
			rawSig := pad(signalBarsRaw(m.frame, sigTPS, online), 8)
			plain := fmt.Sprintf("%s  %s  %s  %s  %s",
				pad(bd.model, nameW), pad(stationsLbl, 7), pad(rangeStr(bd), 17), rawSig, plainBandBadge(bd, m.limits))
			b.WriteString(selCarat(true) + " " + rowSel(true, plain, tableW) + "\n")
			continue
		}
		rng := stEmber.Render(pad(rangeStr(bd), 17))
		sig := tintSignal(pad(signalBarsRaw(m.frame, sigTPS, online), 8), sigTPS, online)
		b.WriteString(selCarat(false) + " " + stDim.Render(pad(bd.model, nameW)) + "  " +
			stDim.Render(pad(stationsLbl, 7)) + "  " + rng + "  " + sig + "  " + bandBadge(bd, m.limits) + "\n")
	}
	return b.String()
}

// plainBandBadge is bandBadge without color, for the reverse-video selected row
// (one accent style governs the whole row; an embedded fg color reads as noise).
func plainBandBadge(bd band, limits *LimitStore) string {
	parts := []string{}
	if bd.lineage > 0 {
		parts = append(parts, fmt.Sprintf("◆ %d", bd.lineage))
	}
	if bd.free {
		parts = append(parts, "FREE")
	}
	if bandOverLimit(bd, limits) {
		parts = append(parts, "above limit")
	}
	if len(parts) == 0 {
		return "·"
	}
	return strings.Join(parts, " ")
}

// bandBadge renders the right-hand flag cell: the gold ◆ lineage call-sign (with
// the verified hop count), a live FREE tag, and the ember above-limit warning.
func bandBadge(bd band, limits *LimitStore) string {
	parts := []string{}
	if bd.lineage > 0 {
		parts = append(parts, stGold.Render(fmt.Sprintf("◆ %d", bd.lineage)))
	}
	if bd.free {
		parts = append(parts, stLive.Render("FREE"))
	}
	if bandOverLimit(bd, limits) {
		parts = append(parts, stEmber.Render("above limit"))
	}
	if len(parts) == 0 {
		return stDim.Render("·")
	}
	return strings.Join(parts, " ")
}

// groupBands groups offers by model into bands, computing each band's live
// cross-station out-price range (min..max of out-price across ONLINE stations),
// the cheapest station, and flags. Bands are sorted cheapest-first, with any band
// whose cheapest station is over the user's limit sorted last (it still shows,
// flagged "above limit" per the design). Offline-only bands sort after online.
func groupBands(offers []offer, limits *LimitStore) []band {
	byModel := map[string]*band{}
	order := []string{}
	for _, o := range offers {
		b, ok := byModel[o.Model]
		if !ok {
			b = &band{model: o.Model}
			byModel[o.Model] = b
			order = append(order, o.Model)
		}
		oc := o
		b.all = append(b.all, oc)
		if o.Confidential {
			b.lineage++
		}
		if !o.Online {
			continue
		}
		if o.FreeNow {
			b.free = true
		}
		if b.stations == 0 || o.PriceOut < b.minOut {
			b.minOut = o.PriceOut
			b.cheapest = &b.all[len(b.all)-1]
		}
		if b.stations == 0 || o.PriceOut > b.maxOut {
			b.maxOut = o.PriceOut
		}
		b.stations++
		b.online = true
	}
	out := make([]band, 0, len(order))
	for _, m := range order {
		out = append(out, *byModel[m])
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi := bandOverLimit(out[i], limits)
		oj := bandOverLimit(out[j], limits)
		if out[i].online != out[j].online {
			return out[i].online // online first
		}
		if oi != oj {
			return !oi // within-limit before above-limit
		}
		return out[i].minOut < out[j].minOut // then cheapest first
	})
	return out
}

// bandOverLimit reports whether a band's cheapest online station is over the
// user's per-model out-price max (so it sorts last and is flagged).
func bandOverLimit(b band, limits *LimitStore) bool {
	if !b.online {
		return false
	}
	lim := limits.resolve(b.model)
	return lim.MaxOut > 0 && b.minOut > lim.MaxOut
}

// money renders a price as a fixed 2-dp string (the per-1M band prices).
func money(v float64) string { return fmt.Sprintf("%.2f", v) }

// dollars renders a money value with Groq-style adaptive precision: balances and
// "big" amounts at 2dp ($12.34), but tiny per-reply / per-token costs keep enough
// significant digits to never collapse to $0.00 (e.g. $0.000123). 1 credit = $1,
// so this is a pure display relabel of the credit unit. Display only - settlement
// math is untouched.
func dollars(v float64) string {
	if v < 0 {
		// Defensive: real money is never negative here (balances/costs are >= 0);
		// a negative slipping through renders as a plain dash rather than "$-…".
		return "-"
	}
	if v == 0 {
		return "$0.00"
	}
	if v >= 0.01 {
		return "$" + fmt.Sprintf("%.2f", v)
	}
	// sub-cent: ~3 significant figures, in plain decimal (no exponent, no trailing
	// zeros) so a real cost never reads as $0.00 (e.g. $0.000123).
	s := strconv.FormatFloat(v, 'g', 3, 64)
	if strings.ContainsAny(s, "eE") {
		// FormatFloat may pick scientific notation for very small values; expand it.
		s = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return "$" + s
}

// rangeStr renders a band's cross-station out-price spread as "min ~ max", or a
// single point when there is only one station (never fake a spread, per design).
func rangeStr(b band) string {
	if !b.online {
		return "-"
	}
	if b.stations <= 1 || b.minOut == b.maxOut {
		return money(b.minOut)
	}
	return money(b.minOut) + " ~ " + money(b.maxOut)
}

// pad truncates (with an ellipsis) or right-pads s to n display runes.
func pad(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s + strings.Repeat(" ", n-len(r))
}

func (m model) chatView(w int) string {
	var b strings.Builder
	sys := ""
	if m.sysPrompt != "" {
		sys = stDim.Render(" · system set")
	}
	b.WriteString(stGold.Render("  ◆ ") + stDim.Render("CHANNEL · "+m.connected.NodeID+" · "+m.connected.Model) +
		stDim.Render(" · cost ") + stEmber.Render(dollars(m.sessCost)) + sys + "\n")
	// Scrollable transcript: keep the tail that fits the pane (you ▸ / them ◂).
	lines := m.transcript
	max := m.height - 8
	if max < 6 {
		max = 12
	}
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	for _, l := range lines {
		b.WriteString("  " + l + "\n")
	}
	// While a reply is in flight, Ping relays it: a subtle one-line transmit with an
	// elapsed-seconds readout so a slow CPU inference reads as progress, not a hang.
	// It sits just under the last message and never displaces the transcript.
	if m.relaying {
		elapsed := 0
		if !m.relayStart.IsZero() {
			elapsed = int(time.Since(m.relayStart).Seconds())
		}
		b.WriteString("  " + transmitLine(m.frame, elapsed) + "\n")
	}
	// The always-live channel prompt: `you ›` + the textinput View() (cursor +
	// echoed text), updated every keystroke. Same live-echo contract as promptLine.
	b.WriteString("\n  " + stPrompt.Render("you › ") + m.chatIn.View() + "\n")
	b.WriteString("  " + stDim.Render("/help for in-session commands  ·  tab leaves channel  ·  enter sends") + "\n")
	return b.String()
}

// transmitLine is Ping's inline relay indicator: the on-air motif breathing with
// a short radio caption + an elapsed-seconds readout. Single line, so it never
// obstructs the chat transcript. The elapsed counter reassures on slow inference
// (CPU MoE replies can take a minute) that the request is alive, not hung.
func transmitLine(frame, elapsedSec int) string {
	caption := "relaying… ping carries your tokens to the station"
	if elapsedSec >= 2 {
		caption = fmt.Sprintf("relaying… %ds  (slow stations can take a minute - holding the channel)", elapsedSec)
	}
	return pulseWith(frame, stPingEye) + " " + stLive.Render("◂ ") + stDim.Render(caption)
}

func (m model) endpointPanel(w int) string {
	lineage := stDim.Render("·")
	if m.connected != nil && m.connected.Confidential {
		lineage = stGold.Render("◆ verified")
	}
	head := stGold.Render("◆ ") + stLive.Render("channel open") + "  " +
		stDim.Render("point your bots here") + "  " + lineage
	model := stDim.Render("-")
	if m.connected != nil {
		model = stKey.Render(m.connected.Model)
	}
	body := head + "\n" +
		stDim.Render("  base url  ") + stKey.Render(m.endpoint) + "\n" +
		stDim.Render("  api key   ") + stKey.Render(m.apikey) + "\n" +
		stDim.Render("  model     ") + model + "\n" +
		stDim.Render("  drop-in, openai-compatible. ") + stLive.Render("roger that.") + stDim.Render("  ·  /chat to test")
	return stPanel.Render(body)
}

// onAirPanel renders the live ON AIR provider instrument: model, price,
// connections served, and running earnings in $, with an off-air hint.
func (m model) onAirPanel(w int) string {
	s := m.share
	in, out := s.Price()
	reqs, toks := s.Served()
	price := stLive.Render("FREE")
	if in > 0 || out > 0 {
		price = stEmber.Render(dollars(out) + "/1M out  " + dollars(in) + "/1M in")
	}
	head := stRed.Render("● ON AIR") + "  " + stDim.Render("you are sharing") + "  " + stKey.Render(s.Model())
	body := head + "\n" +
		stDim.Render("  node       ") + stSelText.Render(s.Node()) + "\n" +
		stDim.Render("  price      ") + price + "\n" +
		stDim.Render("  served     ") + stLive.Render(fmt.Sprintf("%d", reqs)) + stDim.Render(fmt.Sprintf(" requests · %d out tokens", toks)) + "\n" +
		stDim.Render("  earnings   ") + stEmber.Render(dollars(s.Earnings())) + stDim.Render("  (settles on the broker)") + "\n" +
		stDim.Render("  ") + stKey.Render("/share off") + stDim.Render(" to go off air")
	return stPanel.Render(body)
}

// sharesOnAir counts how many local models are currently on air.
func (m model) sharesOnAir() int {
	n := 0
	for _, s := range m.shares {
		if s != nil {
			n++
		}
	}
	return n
}

// sharePrice returns the price a row WOULD share at (FREE unless it's the saved
// model with a saved price), or the live session's price when it's on air.
func (m model) sharePrice(row shareRow, live *agent.Session) (in, out float64) {
	if live != nil {
		return live.Price()
	}
	if row.model == m.hooks.ShareModel {
		return m.hooks.SharePriceI, m.hooks.SharePriceO
	}
	return 0, 0
}

// shareView is the k9s-style provider table: one row per locally-detected model
// with an unmistakable reverse-video selection cursor, a clear ON-AIR / OFF-AIR
// status column, the price (FREE or $/1M out), and the live earning metrics
// (requests served, out tokens, earnings $) for any model that is on air. The
// founder can glance and instantly see what is shared vs not, and flip any model
// on/off air with one key. This replaces the old silent auto-share.
//
// k9s patterns applied (cited for the local design record): a highly visible
// cursor row (k9s flips the selected row to its accent background; we use the
// brand-volt reverse-video bar, with a `>` carat under NO_COLOR), status columns
// per resource, and a contextual key footer - k9scli.io + github.com/derailed/k9s.
func (m model) shareView(w int) string {
	var b strings.Builder
	// compact drops the metrics columns (SERVED/OUT TOK/EARNINGS): the full grid is
	// ~88 cols, so anything narrower uses the 3-column model·status·price layout to
	// stay width-safe (the band grid uses the same idea at its own threshold).
	compact := w < 88
	head := stSelBar.Render("▌") + " " + stBrand.Render("SHARE")
	if compact {
		b.WriteString("  " + head + stDim.Render(fmt.Sprintf("   %d on air / %d", m.sharesOnAir(), len(m.shareRows))) + "\n")
	} else {
		b.WriteString("  " + head +
			stDim.Render(fmt.Sprintf("   your GPU as a station   %s detected · %d on air",
				plural(len(m.shareRows), "model"), m.sharesOnAir())) + "\n")
	}

	if len(m.shareRows) == 0 {
		return b.String() + "\n  " + stEmber.Render("no local models detected") +
			stDim.Render(" - start a local LLM and press r to re-detect") + "\n"
	}

	// Column geometry. compact drops the metrics columns so nothing overflows.
	nameW := 24
	if compact {
		nameW = 14
	}
	// Header (k9s-style ALL-CAPS column labels).
	if compact {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("  %-14s  %-8s  %s", "MODEL", "STATUS", "PRICE")) + "\n")
	} else {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("  %-24s  %-9s  %-12s  %-9s  %-10s  %s",
			"MODEL", "STATUS", "PRICE", "SERVED", "OUT TOK", "EARNINGS")) + "\n")
	}

	// Table width for the reverse-video bar (the highlight spans the whole row).
	tableW := w - 4
	if tableW < 20 {
		tableW = 20
	}

	for i, row := range m.shareRows {
		sel := i == m.shareCursor
		live := m.shares[row.model]
		on := live != nil
		// Status cell text (plain, so the reverse-video bar governs a selected row).
		statusTxt := "OFF-AIR"
		if on {
			statusTxt = "ON-AIR"
		}
		in, out := m.sharePrice(row, live)
		priceTxt := "FREE"
		if in > 0 || out > 0 {
			priceTxt = dollars(out) + "/1M out"
		}

		// Build the row body as PLAIN text first (cells padded), then color it: a
		// selected row is one reverse-video bar; an unselected row tints the status
		// + price cells. This keeps the k9s "the cursor row is obvious" contract.
		var plain string
		if compact {
			plain = fmt.Sprintf("  %-14s  %-8s  %s", pad(row.model, 14), statusTxt, priceTxt)
		} else {
			served, outTok, earn := "-", "-", "-"
			if on {
				reqs, toks := live.Served()
				served = fmt.Sprintf("%d", reqs)
				outTok = fmt.Sprintf("%d", toks)
				earn = dollars(live.Earnings())
			}
			plain = fmt.Sprintf("  %-24s  %-9s  %-12s  %-9s  %-10s  %s",
				pad(row.model, nameW), statusTxt, priceTxt, served, outTok, earn)
		}

		if sel {
			// Reverse-video accent bar across the whole row - unmistakable cursor.
			b.WriteString(selCarat(true) + rowSel(true, plain, tableW) + "\n")
			continue
		}
		// Unselected: a dot/blank gutter, dim model, colored status + price cells.
		st := stDim.Render(pad(statusTxt, 9))
		if on {
			st = stLive.Render(pad("● "+statusTxt, 9))
		}
		if compact {
			stN := stDim.Render(pad(statusTxt, 8))
			if on {
				stN = stLive.Render(pad("●"+statusTxt, 8))
			}
			b.WriteString(selCarat(false) + "  " + stDim.Render(pad(row.model, 14)) + "  " + stN + "  " + sharePriceCell(priceTxt) + "\n")
			continue
		}
		served, outTok, earn := stDim.Render(pad("-", 9)), stDim.Render(pad("-", 10)), stDim.Render("-")
		if on {
			reqs, toks := live.Served()
			served = stLive.Render(pad(fmt.Sprintf("%d", reqs), 9))
			outTok = stDim.Render(pad(fmt.Sprintf("%d", toks), 10))
			earn = stEmber.Render(dollars(live.Earnings()))
		}
		b.WriteString(selCarat(false) + "  " + stDim.Render(pad(row.model, nameW)) + "  " + st + "  " +
			sharePriceCell(pad(priceTxt, 12)) + "  " + served + "  " + outTok + "  " + earn + "\n")
	}

	if compact {
		b.WriteString("\n  " + stDim.Render("free by default · ") + stKey.Render("enter") + stDim.Render("/") + stKey.Render("a") + stDim.Render(" toggles") + "\n")
	} else {
		b.WriteString("\n  " + stDim.Render("free by default · the selected model toggles with ") +
			stKey.Render("enter") + stDim.Render(" or ") + stKey.Render("a") +
			stDim.Render(" · shares keep serving when you leave this view") + "\n")
	}
	return b.String()
}

// sharePriceCell tints a price cell: FREE live-green, a priced cell ember.
func sharePriceCell(txt string) string {
	if strings.HasPrefix(strings.TrimSpace(txt), "FREE") {
		return stLive.Render(txt)
	}
	return stEmber.Render(txt)
}

// modalFooter renders a modal sub-screen's own footer (its keys + the balance),
// width-safe: it stacks under a narrow width and drops the right half when it
// can't fit. status rides under the rule like the main footer.
func modalFooter(w int, left, right, status string) string {
	rule := stHeadRule.Render(strings.Repeat("─", w))
	st := ""
	if status != "" {
		st = "\n" + stDim.Render("  ") + status
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return rule + "\n" + left + st // drop the right half; keys are what matter here
	}
	return rule + "\n" + left + strings.Repeat(" ", gap) + right + st
}

func (m model) footer(w int) string {
	// Keybindings adapt to the mode so the footer always teaches the right keys. At
	// narrow widths a terse key line replaces the full one so it fits.
	var left string
	// Modal sub-screens get their OWN footer keys (TUI-V2-CRITIQUE B) - the browse
	// "↑↓ tune · / cmd" keys do nothing here and mislead.
	switch m.mode {
	case modeConnectConfirm:
		left = stDim.Render("enter/y accept  ·  esc/n deny  ·  d detail")
		if m.narrow() {
			left = stDim.Render("⏎/y accept · esc/n deny · d detail")
		}
		right := stEmber.Render("bal " + m.balDollars())
		return modalFooter(m.effWidth(), left, right, m.status)
	case modeOverLimit:
		left = stDim.Render("⏎ save & re-check  ·  ↑↓ nudge  ·  w wait  ·  esc deny")
		if m.narrow() {
			left = stDim.Render("⏎ save · ↑↓ nudge · w wait · esc")
		}
		return modalFooter(m.effWidth(), left, stEmber.Render("bal "+m.balDollars()), m.status)
	case modeLimits:
		left = stDim.Render("↑↓ move  ·  ⏎ edit  ·  tab field  ·  d clear  ·  esc done")
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎ edit · tab · d · esc")
		}
		return modalFooter(m.effWidth(), left, stEmber.Render("bal "+m.balDollars()), m.status)
	case modeShare:
		left = stDim.Render("↑↓/jk move  ·  ⏎/a toggle on-air  ·  r re-detect  ·  esc done")
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎/a on-air · r · esc")
		}
		right := stRed.Render(fmt.Sprintf("%d on air", m.sharesOnAir()))
		return modalFooter(m.effWidth(), left, right, m.status)
	}
	if m.mode == modeChat {
		if m.narrow() {
			left = stDim.Render("talk · / cmds · tab · esc · ⌃c")
		} else {
			left = stDim.Render("type to talk  ·  / in-session cmds  ·  tab browse  ·  esc leave  ·  ⌃c quit")
		}
	} else if m.narrow() {
		left = stDim.Render("↑↓ · ⏎ on-air · / · ? · q")
	} else {
		chatKey := ""
		if m.connected != nil {
			chatKey = "  ·  tab/c channel  ·  m minimize"
		}
		left = stDim.Render("↑↓ tune  ·  enter on-air" + chatKey + "  ·  / cmd  ·  ? help  ·  q quit")
	}
	confMode := ""
	if m.confidentialOnly {
		confMode = stGold.Render("◆conf-only") + "  "
	}
	right := confMode + stEmber.Render("bal "+m.balDollars()) + "  " + stDim.Render(m.broker)
	st := ""
	if m.status != "" {
		st = "\n" + stDim.Render("  ") + m.status
	}
	// A subtle non-blocking update notice rides in the status area when available.
	if m.updateLine != "" {
		st += "\n" + stDim.Render("  ") + stEmber.Render(m.updateLine)
	}
	rule := stHeadRule.Render(strings.Repeat("─", w))
	// Narrow: stack the keys above the bal/broker line (a two-line status bar) so
	// neither half is forced to overflow the real width. (TUI-V2-CRITIQUE A §5.)
	if m.narrow() {
		return rule + "\n" + left + "\n" + right + st
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// Wide-ish but the broker URL pushes past the edge: drop it (keep the
		// balance, which is the load-bearing half) so the line fits.
		right = confMode + stEmber.Render("bal "+m.balDollars())
		gap = w - lipgloss.Width(left) - lipgloss.Width(right)
		if gap < 1 {
			return rule + "\n" + left + "\n" + right + st // last resort: stack
		}
	}
	return rule + "\n" + left + strings.Repeat(" ", gap) + right + st
}

func (m model) helpView() string {
	cmds := [][2]string{
		{"/search", "re-scan the band for stations"},
		{"/connect (enter)", "tune in to the selected station"},
		{"/chat (c · tab)", "open the CHANNEL session with the connected model"},
		{"/limits", "see + edit your per-model spend maxes"},
		{"/balance", "wallet balance ($)"},
		{"/topup [usd]", "add credits (opens checkout)"},
		{"/share [off]", "open the provider table - flip your local models on/off air"},
		{"/login", "link GitHub (device flow) - to earn"},
		{"/grant [create <name>]", "private free keys for your bots/family"},
		{"/confidential", "toggle: route only to TEE-attested nodes"},
		{"/endpoint  /config", "endpoint + key · broker/identity"},
		{"/help  /quit", "this · exit"},
	}
	var b strings.Builder
	// Ping rests here, on air and standing by - an intentional home for the mascot
	// (not just empty/error states). Body volt, the eye the one live-red glyph.
	ping := renderPing(pingIdleFrames[anim(m.frame)%len(pingIdleFrames)], "•")
	b.WriteString("\n" + indentBlock(ping, "    ") + "\n")
	b.WriteString("    " + stPingDim.Render("Ping · on air, go ahead") + "\n\n")
	b.WriteString(stBrand.Render("  commands") + stDim.Render("  (a two-way radio for GPUs)") + "\n\n")
	for _, c := range cmds {
		b.WriteString("  " + stKey.Render(fmt.Sprintf("%-18s", c[0])) + stDim.Render(c[1]) + "\n")
	}
	b.WriteString("\n  " + stDim.Render("in CHANNEL: /model /clear /save /system <p> /cost /confidential /endpoint /help /quit") + "\n")
	b.WriteString("  " + stDim.Render("modes: tab switches BROWSE ⇄ CHANNEL · m minimizes the header") + "\n")
	b.WriteString("\n  " + stDim.Render("rogerai "+helpVersion+" · press any key to go back") + "\n")
	return b.String()
}

// helpVersion is the client version shown in help; set by the host via SetVersion.
var helpVersion = "v0.2.1"

// SetVersion lets the host (cmd/rogerai) inject the build version so the help /
// about surfaces match `rogerai version`.
func SetVersion(v string) {
	if v == "" {
		return
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	helpVersion = v
}

// indentBlock prefixes every line of a multi-line block with pad (for placing
// art without disturbing its internal alignment).
func indentBlock(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// ---- helpers / cmds ----
// signalBarsRaw returns the 5-cell equalizer glyphs WITHOUT color, so callers can
// pad/align on the true display width before tinting. Bar height is set by the
// node's measured tok/s; offline or unmeasured shows a flat low signal.
func signalBarsRaw(frame int, tps float64, online bool) string {
	glyphs := []rune("▁▂▃▄▅▆▇█")
	if !online {
		return "▁▁▁▁▁"
	}
	base := 0
	switch {
	case tps >= 600:
		base = 6
	case tps >= 300:
		base = 5
	case tps >= 150:
		base = 4
	case tps >= 60:
		base = 3
	case tps >= 20:
		base = 2
	case tps > 0:
		base = 1
	}
	if base == 0 {
		return "▁▁▁▁▁" // online but not yet measured
	}
	var sb strings.Builder
	frame = anim(frame)
	for i := 0; i < 5; i++ {
		lvl := base - (i % 2) + (frame+i)%2 // gentle shimmer around the measured level
		if lvl < 0 {
			lvl = 0
		}
		if lvl >= len(glyphs) {
			lvl = len(glyphs) - 1
		}
		sb.WriteRune(glyphs[lvl])
	}
	return sb.String()
}

// tintSignal colors a raw equalizer: live-green when the station is up and
// measured, dim otherwise. Any alignment padding in raw is tinted too, but
// spaces have no visible color so the column stays clean.
func tintSignal(raw string, tps float64, online bool) string {
	if online && tps > 0 {
		return stLive.Render(raw)
	}
	return stDim.Render(raw)
}

// normalizeUpstream turns a detected base/chat URL into the chat-completions URL
// the agent POSTs to (mirrors cmd/rogerai's helper; kept local so the TUI's
// in-process /share has no host dependency).
func normalizeUpstream(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	switch {
	case u == "":
		return u
	case strings.HasSuffix(u, "/chat/completions"):
		return u
	case strings.HasSuffix(u, "/v1"):
		return u + "/chat/completions"
	default:
		return u + "/v1/chat/completions"
	}
}

// plural renders "1 band" / "3 bands": a count with its noun, +s unless n == 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func countOnline(o []offer) int {
	n := 0
	for _, x := range o {
		if x.Online {
			n++
		}
	}
	return n
}

// rescanEveryFrames sets the live band re-scan cadence: at the 160ms tick, ~31
// frames is ~5s, comfortably under the broker's ~35s on-air TTL so a node that
// just went on/off air is reflected within one cadence.
const rescanEveryFrames = 31

func tick() tea.Cmd {
	return tea.Tick(160*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func fetchOffers(broker string) tea.Cmd {
	return func() tea.Msg {
		resp, err := http.Get(broker + "/discover")
		if err != nil {
			return errMsg("broker unreachable: " + broker)
		}
		defer resp.Body.Close()
		var d struct {
			Offers []offer `json:"offers"`
		}
		// A valid 200 with an empty body is a legitimate "no offers" scan (io.EOF),
		// not a drop; only a genuinely malformed body is treated as a broker drop.
		if err := json.NewDecoder(resp.Body).Decode(&d); err != nil && !errors.Is(err, io.EOF) {
			return errMsg("broker unreachable: " + broker)
		}
		sort.Slice(d.Offers, func(i, j int) bool { return d.Offers[i].PriceIn < d.Offers[j].PriceIn })
		return offersMsg(d.Offers)
	}
}

func fetchBalance(broker, user string) tea.Cmd {
	return func() tea.Msg {
		req, _ := http.NewRequest(http.MethodGet, broker+"/balance", nil)
		client.SignRequest(req, nil)
		req.Header.Set("X-Roger-User", user)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return errMsg("")
		}
		defer resp.Body.Close()
		var b struct {
			Balance float64 `json:"balance"`
		}
		json.NewDecoder(resp.Body).Decode(&b)
		return balanceMsg(b.Balance)
	}
}

func sendChat(broker, user, mdl, prompt string, confidential bool) tea.Cmd {
	return func() tea.Msg {
		reply, status, cost, err := client.Chat(broker, user, mdl, prompt, confidential)
		if err != nil {
			// A chat failure is surfaced INLINE in the transcript (chatErrMsg), not on
			// the footer status line - that was the silent-no-response bug: the user
			// typed, the spinner vanished, and nothing appeared where they were looking.
			return chatErrMsg(err.Error())
		}
		return chatMsg{reply: reply, status: status, cost: cost}
	}
}

// Run launches the TUI with no spend limits (back-compat).
func Run(broker, user string) error {
	return RunWith(broker, user, nil)
}

// RunWith launches the TUI with a spend-limit store (the pricing UX: per-model
// maxes, connect confirmation, over-limit edit). nil = no caps / no persistence.
func RunWith(broker, user string, limits *LimitStore) error {
	return RunWithNotice(broker, user, limits, "")
}

// RunWithNotice is RunWith plus a subtle, pre-computed "update available" line
// (empty = none) surfaced in the status area. The host owns the (cached, async,
// non-blocking) update check so the TUI never does network at startup.
func RunWithNotice(broker, user string, limits *LimitStore, notice string) error {
	return RunWithHooks(broker, user, limits, notice, Hooks{})
}

// RunWithHooks is RunWithNotice plus the host-supplied hooks that make the in-TUI
// /share, /login, /topup, /grant flows real actions.
func RunWithHooks(broker, user string, limits *LimitStore, notice string, hooks Hooks) error {
	m := NewWithHooks(broker, user, limits, hooks)
	m.updateLine = notice
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
