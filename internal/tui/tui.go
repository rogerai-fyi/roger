// Package tui is the interactive `rogerai` experience - a two-way radio for GPUs,
// and the terminal twin of the website's "Live Operating Manual". Stations
// (providers) go on air; you tune in to a channel and talk. The look is the web's:
// ~95% monochrome + ONE red beacon, the shared instrument glyphs (◉ on air, ○ off
// air, ◆ verified, ▁▂▃▄▅▆▇█ signal bars), flat hairline structure, and a single
// carrier beat driving the beacon, the ((•)) spinner, and the signal-bar shimmer.
// Built on Bubble Tea + Lipgloss.
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
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-isatty"
	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// Hooks lets the host (cmd/rogerai) supply the few platform/auth bits the TUI
// can't compute itself, so the in-TUI /share, /login, /topup, /grant flows are
// REAL actions (not "run it elsewhere") without the tui package importing the
// host. All are optional; a nil hook degrades that flow to a labeled hint.
type Hooks struct {
	NodeID      string                                        // this node's id (hostname)
	HW          string                                        // hardware label for the offer
	GitHubID    string                                        // public GitHub OAuth client id (device flow)
	LinkedLogin string                                        // the locally-linked GitHub login at startup ("" = anonymous)
	ShareModel  string                                        // saved onboarding model (default offer)
	SharePriceI float64                                       // saved input price (0 = free)
	SharePriceO float64                                       // saved output price (0 = free)
	Login       func(broker, clientID string) (string, error) // device-flow login -> github login
	// LoginBegin starts the GitHub device flow and returns the URL + code to show
	// (no polling); LoginPoll then blocks until the user authorizes and returns the
	// linked login. Split so the TUI can render its own clean login panel + auto-open
	// the browser instead of relying on the CLI's stdout (hidden behind the TUI). When
	// nil the TUI falls back to the single-shot Login hook.
	LoginBegin func(broker, clientID string) (LoginDevice, error)
	LoginPoll  func(broker, clientID string, d LoginDevice) (string, error)
	// Logout forgets the local GitHub binding (the in-TUI logout). nil degrades the
	// logout panel to a labeled hint.
	Logout      func() error
	TopupURL    func(broker, user string, usd float64) (string, error)
	GrantCreate func(broker, name string, free bool) (secret string, err error)
	GrantList   func(broker string) ([]GrantRow, error)
	// SavePrice persists a per-model price + time-of-use schedule the in-TUI editor
	// produced, so the choice survives the session (nil = in-session only). The host
	// owns the config write; the TUI keeps no disk I/O.
	SavePrice func(model string, p Pricing)
	// SavedPrices seeds the editor with prices the user set in a previous session, so
	// the provider table shows them and on-air uses them (nil = none).
	SavedPrices map[string]Pricing
	// Compact seeds the "windowshade" compact mode at launch from the saved config, so
	// the [m] choice sticks across sessions (the host owns the disk read).
	Compact bool
	// SaveCompact persists the compact toggle when the user presses [m], so the calm
	// view is remembered next launch (nil = session-only; no disk I/O in the TUI).
	SaveCompact func(bool)
}

// GrantRow is a compact grant summary for the in-TUI /grant list.
type GrantRow struct {
	Name, Price, Status string
}

// LoginDevice is the display-ready view of a started GitHub device flow the TUI
// renders in its login panel: the URL to open + the short code to type. Handle is
// the opaque continuation the host's LoginPoll uses to resume polling.
type LoginDevice struct {
	VerificationURI string
	UserCode        string
	Handle          any
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

// frozenFrame is the fixed, well-formed frame the compact "windowshade" mode feeds
// every animation function (beacon arcs, signal shimmer, Ping pose) so motion
// settles to a stable snapshot - the same canonical frame quiet/anim() picks. Used
// by the compact render paths to treat compact as an explicit prefers-reduced-motion.
const frozenFrame = 1

// ---- palette: the web's "Live Operating Manual" tokens ----
//
// ~95% monochrome + ONE red beacon. This mirrors the website exactly (see
// docs-internal/design/direction-foundation.md §3.2): a near-monochrome ink/dim/
// bright ramp plus a SINGLE accent red used only as glints - the on-air beacon,
// the verified ◆, the selection cursor, the pressed preset, and headline accents.
// Everything else is ink-on-paper. The old indigo "volt", green "live", orange
// "ember", and gold "lineage" accents are RETIRED - they collapse into the mono
// ramp (so the binary reads as a terminal twin of the site, not a different app).
//
// lipgloss.AdaptiveColor flips light/dark with the terminal background, matching
// the web's "white room" / "ink room" pair: live red is #E0231C on light, lifted
// to #FF4438 on dark for AA; the ink ramp warms toward the page neutrals.
var (
	// The one accent: the live-red on-air beacon (web --live). Light #E0231C / dark
	// #FF4438. Used ONLY as a signal glint, never as a surface fill behind text.
	cRed = lipgloss.AdaptiveColor{Light: "#E0231C", Dark: "#FF4438"}

	// The monochrome ink ramp (warm near-black on paper / warm off-white on ink),
	// tracking the web's --ink-900 / --ink-500 / --ink-400 / --hairline tokens.
	cInk   = lipgloss.AdaptiveColor{Light: "#15140F", Dark: "#F3F1EA"} // headlines / primary
	cBody  = lipgloss.AdaptiveColor{Light: "#33312B", Dark: "#CFCCC4"} // body / values
	cDim   = lipgloss.AdaptiveColor{Light: "#6B685F", Dark: "#9A968B"} // secondary / labels
	cFaint = lipgloss.AdaptiveColor{Light: "#9A968B", Dark: "#6F6C64"} // off-bars / disabled
	cRule  = lipgloss.AdaptiveColor{Light: "#D8D7D2", Dark: "#2A2720"} // the single hairline
	cInkBg = lipgloss.AdaptiveColor{Light: "#FBFBFA", Dark: "#0E0D0B"} // paper (selection text)

	// One voice, five roles - but now they all draw from the SAME mono+red system,
	// so the names are kept (minimal churn across the file) while the COLOR is unified:
	//   stBrand  - the headline / faceplate lettering (bright ink, bold).
	//   stTag    - a quiet brand-tail / secondary (dim).
	//   stDim    - labels, captions, structure (dim).
	//   stLive   - on-air / good / values that were "green" -> now ink (no green).
	//   stEmber  - prices / money that were "orange" -> now ink mono (weight, not hue).
	//   stGold   - lineage ◆ that was "gold" -> now the ONE red (verified is a glint).
	//   stKey    - the load-bearing value (command / endpoint / model) -> bright ink.
	//   stSelText- the selection / focus glint -> red.
	stBrand    = lipgloss.NewStyle().Foreground(cInk).Bold(true)
	stTag      = lipgloss.NewStyle().Foreground(cDim)
	stDim      = lipgloss.NewStyle().Foreground(cDim)
	stLive     = lipgloss.NewStyle().Foreground(cBody)
	stEmber    = lipgloss.NewStyle().Foreground(cBody)
	stGold     = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	stSelBar   = lipgloss.NewStyle().Foreground(cRed)
	stSelText  = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	stHeadRule = lipgloss.NewStyle().Foreground(cRule)
	stPanel    = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(cRule).Padding(0, 1)
	stKey      = lipgloss.NewStyle().Foreground(cInk).Bold(true)
	stPrompt   = lipgloss.NewStyle().Foreground(cInk).Bold(true) // the `rog ›` prompt lockup
	stRed      = lipgloss.NewStyle().Foreground(cRed).Bold(true)

	// k9s-grade selection: a full-width reverse-video (accent-bg) row so the cursor
	// is unmistakable at a glance, exactly like k9s's cursor row (it flips the row's
	// background to its accent so the selected resource pops). The web's one accent is
	// red, so the cursor row is the red bar with paper text; under NO_COLOR lipgloss
	// drops the bg and a leading `>` carat carries the selection instead (see rowSel /
	// selCarat). k9s design refs (cited for the local design record): k9scli.io
	// (cursor/accent row, status columns, contextual key footer) and
	// github.com/derailed/k9s (skin table.cursorColor, reverse-video selected row).
	stRowSel = lipgloss.NewStyle().Foreground(cInkBg).Background(cRed).Bold(true)
)

// Shared iconography (the web's instrument glyphs), used consistently across
// search / share / channel so every surface reads as one designed system:
//
//	glyphOnAir  ◉  on air / online / a live carrier
//	glyphOffAir ○  off air / offline / over-margin
//	glyphVerify ◆  lineage-verified (rendered in the one red as a glint)
//	signalGlyphs ▁▂▃▄▅▆▇█  the signal-strength tower
//
// These degrade to plain runes under NO_COLOR (lipgloss strips the color, the
// glyph itself is still a recognizable Unicode mark) and stay fixed-width.
const (
	glyphOnAir  = "◉"
	glyphOffAir = "○"
	glyphVerify = "◆"
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

// detectShares is the indirection over local-LLM detection used by the SHARE
// flows, so tests can make it deterministic (the real Detect scans the host's open
// ports). Production uses detect.DetectWith.
var detectShares = func(extra ...string) []detect.Found { return detect.DetectWith(extra...) }

// detectSharesCmd runs detectShares in a goroutine (a tea.Cmd) so the SHARE flows
// detect local models WITHOUT blocking the Bubble Tea event loop - probing a busy
// host's open ports can take a few seconds, which would otherwise freeze every
// keystroke with no feedback. The result comes back as a sharesDetectedMsg the
// Update handler folds into the provider table. detectShares stays injectable so
// tests can make this deterministic (a test can also feed sharesDetectedMsg
// directly to exercise the handler).
func detectSharesCmd(extra string) tea.Cmd {
	return func() tea.Msg { return sharesDetectedMsg{found: detectShares(extra)} }
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
	modeConnecting     // staged scan/lock/handshake/CHANNEL-OPEN sequence (the web's tune-in)
	modeOverLimit      // 3.3 over-limit + inline edit-your-max
	modeLimits         // 3.4 per-model spend limits
	modeShare          // k9s-style provider table: list local models, toggle on/off-air
	modeShareEditor    // per-model pricing + time-of-use schedule editor (login-gated)
	modeShareSetup     // guided fallback: no local model detected, pick a tool / paste a URL
	modeQuitConfirm    // on-air quit-guard: confirm before going off air on quit
	modeAgent          // [0] AGENT: the embedded tool-capable agent harness (dj.md persona)
	modeLogin          // [L] confirmable login/logout panel (never an instant action)
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
	broker, user  string
	offers        []offer
	cursor        int
	width, height int
	frame         int
	mode          mode
	cmd           textinput.Model
	chatIn        textinput.Model
	transcript    []string
	connected     *offer
	endpoint      string
	apikey        string
	// lastConnected is the band we most recently TUNED IN to (a "sticky" recent
	// station). It is kept across band re-scans so a band you connected to never
	// vanishes from the browse list when its node ages out of /discover - it stays as
	// an available, tunable station you can re-tune. Set on connect, kept on disconnect
	// (you disconnected on purpose, so you most want to reconnect), cleared only when a
	// fresh /discover lists the band on air again (the live offer takes over). See the
	// offersMsg handler (sticky-band merge) + disconnect().
	lastConnected *offer
	// recentBands records every model we have tuned in to this session, so a re-connect
	// to one is FAST: the staged scan/lock/handshake animation plays only on the FIRST
	// (cold) tune-in to a band; a band in this set drops straight into the open channel
	// (warm reconnect). Cleared by nothing this session (a band stays "warm" once tuned).
	recentBands map[string]bool
	// staged tune-in sequence (modeConnecting): connectStage is the step the
	// animation has reached (0..connectStageDone); connectStartFrame anchors the
	// per-step dwell to m.frame so the steps advance on the one carrier beat. Under
	// quiet (NO_COLOR / non-TTY / reduced-motion) the sequence renders fully resolved
	// in a single frame (no churn in a pipe).
	connectStage      int
	connectStartFrame int
	proxyUp           bool
	proxyAddr         string
	confidentialOnly  bool
	balance           float64
	haveBal           bool
	status            string
	alert             *alertBox
	// pricing UX state
	limits *LimitStore
	bands  []band // offers grouped by model (the band list, 3.1)
	// SCALE: the band browser is built for hundreds/thousands of stations, so the
	// list is FILTERED + SORTED into a derived view (visibleBands) and only the
	// VISIBLE window is rendered each frame (virtualized). m.cursor indexes the
	// VISIBLE set, never the raw m.bands. browseTop is the index of the first row
	// drawn in the window (it scrolls to keep the cursor in view). See visibleBands,
	// windowFor, and browseView. NOTE: the broker /discover returns the FULL on-air
	// set (no broker-side pagination) - client windowing + filter covers realistic
	// scale now; broker-side pagination + load-on-scroll is the next step IF on-air
	// counts ever exceed a few hundred. See fetchOffers.
	filterMode    bool            // the live filter input line is open (f)
	filterIn      textinput.Model // the live name filter buffer
	filterApplied string          // the applied name substring (kept after enter; lowercased compare)
	sortMode      int             // band sort cycle (see sort* consts) - mirrors the /bands web page
	fFree         bool            // toggle: only bands with a FREE-now station
	fConf         bool            // toggle: only confidential / verified (lineage) bands
	fOn           bool            // toggle: only bands with a station on air
	browseTop     int             // first visible row index in the virtualized window
	loadedOnce    bool            // a /discover scan has come back at least once (drives the initial ((•)) scanning pose)
	q             quote           // the in-flight connect quote (confirm / over-limit)
	editBuf       string          // inline numeric edit buffer (over-limit + limits edit)
	editField     int             // which field is focused in the limits editor (0=out,1=tps)
	limCursor     int             // cursor in the limits view
	limModels     []string
	watching      string    // band we are "wait & notify" watching (stub label)
	showDetail    bool      // [d] expands the connect-confirm screen; default off (simple)
	relaying      bool      // a chat request is in flight (drives Ping's transmit line)
	relayStart    time.Time // when the in-flight chat began (for the elapsed "transmitting Ns")
	scanErr       bool      // last band scan failed (broker unreachable) -> Ping "...static"
	scanned       bool      // at least one scan has come back (good or empty) -> Ping idle, not tx
	minimized     bool      // header toggle: thin one-line bar vs the full lockup
	// compact is the "windowshade" mode (XMMS/Winamp collapse): a calm, dense,
	// animation-free alternate view toggled by [m] in every non-text-entry context.
	// When set the header drops to one strip, all motion freezes (carrier beat, Ping,
	// the ((•)) spinner), rows tighten, and the frame tick idles when nothing is in
	// flight - an explicit prefers-reduced-motion within the app. Persisted via the
	// host SaveCompact hook (nil = session-only).
	compact bool
	// chat session state (CHANNEL mode)
	sysPrompt string  // /system prompt prepended to each turn
	sessCost  float64 // running session cost in dollars (sum of per-reply costs)
	// [0] AGENT state (modeAgent): the embedded tool-capable harness. agent holds the
	// session-only loop (dj.md persona + bounded tools); agentIn is the prompt; the
	// transcript carries the streamed turn (assistant text, tool calls, results,
	// answer). agentBusy is true while a turn runs in the background goroutine; the
	// confirm sub-state (agentPendingConfirm) pauses the turn for a y/N on a mutating
	// tool. agentCost is the running session cost. See agent.go for the wiring.
	agent               *agentRuntime // nil until first entered; built lazily
	agentIn             textinput.Model
	agentLines          []string      // the rendered AGENT transcript (you ▸ / tool ◉ / answer ◂)
	agentBusy           bool          // a turn is in flight (drives the working line)
	agentStart          time.Time     // when the in-flight turn began (elapsed readout)
	agentPendingConfirm *agentConfirm // non-nil while a mutating tool awaits y/N
	agentCost           float64       // running AGENT session cost in dollars
	// /model selection state. agentPicked marks that the user chose the model
	// explicitly (so auto-resolution does not snap it back). agentPicker is the modal
	// list (open with 2+ candidates); agentPickerRows is the candidate models and
	// agentPickerCursor the selected row. See agent.go (openAgentModelPicker / the
	// picker key + view).
	agentPicked       bool     // the model was chosen via /model (sticky over auto-resolve)
	agentPicker       bool     // the /model picker modal is open
	agentPickerRows   []string // candidate models in the open picker
	agentPickerCursor int      // selected row in the picker
	// async, cached update check (non-blocking)
	updateLine string // "update available v<cur> -> v<new>" or "" (set by updateMsg)
	// in-TUI provider/account/money flows (TUI-V2-CRITIQUE D / audit C5)
	hooks     Hooks          // host-supplied platform/auth bits (nil-safe)
	share     *agent.Session // most-recently-shared in-process session (the panel's headline; nil = none)
	onAir     bool           // ON AIR indicator + panel (true while any share is live)
	ghLogin   string         // linked GitHub login (set at startup if linked, or once /login succeeds); "" = anonymous
	loggedIn  bool           // true when the broker confirms a real account wallet (gates the balance display)
	grantList []GrantRow     // last /grant list result
	// [L] confirmable login/logout panel (modeLogin). The panel never acts on arrival -
	// only y (logout) / enter (start login) inside it does - so arrow-nav can land on it
	// without surprises. loginReturn is the mode to restore when the panel is dismissed.
	loginReturn  mode        // mode to return to when the login/logout panel is dismissed
	loginDevice  LoginDevice // the started device flow (URL + code) while waiting for auth
	loginWaiting bool        // true once the device flow started and we are polling for auth
	loginNote    string      // a one-line panel note (e.g. "opened in your browser")
	// k9s-style SHARE / provider table (modeShare): one row per locally-detected
	// model, each independently flippable on/off air. shares holds the live session
	// per on-air model; shareRows is the rendered model list; shareCursor is the
	// highly-visible reverse-video selection cursor.
	shares      map[string]*agent.Session // model -> live in-process session (on air)
	shareRows   []shareRow                // the provider table rows (detected models)
	shareCursor int                       // selected row in the provider table
	shareUp     string                    // the local upstream chat URL backing the shares
	quitReturn  mode                      // the mode to restore if the on-air quit-guard is declined
	// async SHARE detection: probing the host's open ports for local LLMs can take a
	// few seconds on a busy box (120+ listening ports). shareLoading marks the
	// provider table as "scanning the band…" while detection runs OFF the Bubble Tea
	// event loop (a tea.Cmd goroutine returning sharesDetectedMsg), so pressing
	// [2]/SHARE/r never freezes the UI. sharePending holds the optional `/share
	// <model>` shortcut model to flip on air once detection lands. setupOnEmpty
	// chooses whether an empty detect drops into the guided setup wizard (the initial
	// open) or stays on the table with a "still nothing" note (the in-table r
	// re-detect, which must not yank the user into the wizard mid-table).
	shareLoading bool
	sharePending string
	setupOnEmpty bool
	shareRescan  bool   // the in-flight detect is a retry (re-scan), not a first open
	setupHint    string // the note to show in the wizard if the in-flight rescan finds nothing
	// per-model pricing + time-of-use schedule editor (modeShareEditor). prices the
	// row at shareCursor; persisted via the host SavePrice hook (nil = in-session only).
	edPriceIn  string             // $/1M in edit buffer
	edPriceOut string             // $/1M out edit buffer
	edWindows  []SchedWindow      // time-of-use windows being edited
	edField    int                // focused field (see edField* consts)
	edWinSub   int                // focused sub-field within a window (see winSub* consts)
	edWinBuf   string             // in-progress digit buffer for the focused window price sub-field
	edModel    string             // the model this editor is pricing
	prices     map[string]Pricing // per-model saved pricing (in/out + schedule)
	// guided-fallback share setup wizard (modeShareSetup): pick a tool for a
	// one-liner, or paste a URL we verify with detect.Probe.
	setupCursor int    // selected option in the setup wizard
	setupPaste  string // the pasted-URL buffer (when the "Other" option is chosen)
	setupErr    string // last paste-verify error
}

// edField identifies the focused field in the pricing/schedule editor.
const (
	edFieldIn       = iota // $/1M input price
	edFieldOut             // $/1M output price
	edFieldAddWin          // the "add a time-of-use window" affordance
	edFieldFirstWin        // first window row (each window is one field below this)
)

// winSub identifies the focused sub-field WITHIN a time-of-use window row, cycled
// with left/right so a window can edit its Start, End, and in/out prices (not just
// Start) - otherwise a window publishes with In=Out=0 unintentionally.
const (
	winSubStart = iota // "HH:MM" window start
	winSubEnd          // "HH:MM" window end
	winSubIn           // $/1M in inside the window
	winSubOut          // $/1M out inside the window
	winSubCount        // number of sub-fields (for modulo cycling)
)

// SchedWindow is the TUI's editable view of a time-of-use price window (mirrors
// protocol.PriceWindow). Times are "HH:MM" UTC; Free zeroes the price in-window.
type SchedWindow struct {
	Start, End string
	In, Out    float64
	Free       bool
}

// Pricing is the per-model saved price + schedule the editor produces. The host
// persists it (and feeds it back as Hooks.SavedPricing); on-air it is applied
// when a model goes live.
type Pricing struct {
	In, Out float64
	Windows []SchedWindow
}

// shareRow is one model in the k9s-style provider table: a locally-detected model
// plus its share status. Live metrics are read off the session when on air. Each
// row carries its OWN upstream (the detected server's chat URL) so a multi-endpoint
// box (e.g. :8060 gpt-oss-20b + :8080 gpt-oss-120b + :8081 qwen3-vl-8b) shares each
// model against the server that actually serves it - not a single shared upstream.
type shareRow struct {
	model    string
	ctx      int
	upstream string // the normalized chat-completions URL backing THIS row's model
}

// ---- messages ----
type offersMsg []offer

// sharesDetectedMsg carries the result of an ASYNC local-LLM detection scan run off
// the event loop (see detectSharesCmd). The Update handler turns it into provider
// rows + clears the loading flag, so the SHARE table never blocks the UI while the
// host's open ports are probed.
type sharesDetectedMsg struct{ found []detect.Found }

// balanceMsg carries the wallet read: the balance plus whether the broker says the
// caller is logged in (has a real account wallet). Balance is shown only when in.
type balanceMsg struct {
	balance  float64
	loggedIn bool
}
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

// loginStartedMsg carries the started device flow back to the Update loop so the
// panel can render the URL + code and we can auto-open the browser, THEN begin
// polling (the poll is a second Cmd that lands as a loginMsg / flowErrMsg).
type loginStartedMsg LoginDevice

// logoutMsg signals the local GitHub binding was forgotten (the in-TUI logout).
type logoutMsg struct{}

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
	// Reflect the locally-linked login at startup so the header shows the right state
	// before the first /balance comes back. The broker's logged_in flag (from the
	// signed balance read) is the source of truth and confirms it.
	m.ghLogin = hooks.LinkedLogin
	// Seed the windowshade compact mode from the saved config so the [m] choice sticks.
	m.compact = hooks.Compact
	// Seed per-model pricing the user set in a previous session.
	if len(hooks.SavedPrices) > 0 {
		m.prices = map[string]Pricing{}
		for mdl, p := range hooks.SavedPrices {
			m.prices[mdl] = p
		}
	}
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
	ag := textinput.New()
	ag.Prompt = ""
	ag.Placeholder = "ask the agent to do something"
	fi := textinput.New()
	fi.Prompt = ""
	fi.Placeholder = "type to filter bands by name"
	return model{broker: broker, user: user, cmd: ci, chatIn: ch, agentIn: ag, filterIn: fi, proxyAddr: "127.0.0.1:4141", status: "tuning in…", alert: &alertBox{}, limits: limits}
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
		// While the staged tune-in is playing, advance it on the carrier beat (it owns
		// the tick until it drops into CHANNEL). It never fires a /discover re-scan mid
		// lock, so the sequence stays smooth.
		if m.mode == modeConnecting {
			return m.advanceConnect()
		}
		// COMPACT (windowshade): treat compact like prefers-reduced-motion. When nothing
		// is in flight the fast 160ms animation tick idles - we drop to a slow rescan tick
		// (a fresh /discover every ~5s, no animation frames) so the view is genuinely calm
		// yet still updates on real events (offers, balance, chat replies arrive via their
		// own Cmds). A relay / staged tune-in / SHARE detection still needs the live beat,
		// so those keep the fast tick even in compact.
		if m.compact && !m.relaying && m.mode != modeConnecting && !m.shareLoading {
			return m, tea.Batch(slowTick(), fetchOffers(m.broker))
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
		m.scanned = true    // a scan returned (even empty) -> stop showing the loading pose
		m.loadedOnce = true // the first scan has come back: never re-enter the initial loading pose
		m.bands = m.mergeStickyBand(groupBands(m.offers, m.limits))
		// Clamp the cursor + window into the FILTERED view (the list the user actually
		// navigates), so a re-scan that shrinks the matches never strands the cursor.
		m.clampBrowse()
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
	case sharesDetectedMsg:
		return m.onSharesDetected(msg.found)
	case balanceMsg:
		m.loggedIn = msg.loggedIn
		if msg.loggedIn {
			m.balance, m.haveBal = msg.balance, true
		} else {
			// Anonymous: no wallet/balance to show.
			m.balance, m.haveBal = 0, false
		}
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
		// The same actionable surface the AGENT uses: a tight short cause + a [1] tune
		// in / [2] share next step, INLINE in the transcript (not just the footer) so a
		// 5xx / timeout / no-station is never a dead end.
		m.transcript = append(m.transcript, failureHint(string(msg), m.narrow())...)
		m.status = stEmber.Render("! " + shortFailure(string(msg)))
		return m, nil
	case errMsg:
		m.relaying = false
		if strings.HasPrefix(string(msg), "broker unreachable") {
			m.scanErr = true // the band scan dropped -> Ping goes "...static"
		}
		m.status = stEmber.Render("! " + string(msg))
		return m, nil
	case loginStartedMsg:
		// The device flow started: stash the URL + code so the login panel renders
		// them, auto-open the browser (fire-and-forget; SSH/headless just reads the
		// code), then kick off polling for the authorization.
		m.loginDevice = LoginDevice(msg)
		m.loginWaiting = true
		m.loginNote = "opened in your browser (or copy the link above)"
		m.status = stDim.Render("waiting for GitHub authorization…")
		openURL(m.loginDevice.VerificationURI)
		return m, m.pollLoginCmd()
	case loginMsg:
		m.ghLogin = string(msg)
		m.loggedIn = true
		m.loginWaiting = false
		m.loginDevice = LoginDevice{}
		// Leave the login panel back to where the user was.
		if m.mode == modeLogin {
			m.mode = m.loginReturn
		}
		m.status = stLive.Render("◆ logged in as @" + string(msg) + " - wallet ready, you can now earn as a provider")
		// Refresh the wallet so the header flips to @login · $balance right away.
		return m, fetchBalance(m.broker, m.user)
	case logoutMsg:
		m.ghLogin = ""
		m.loggedIn = false
		m.haveBal = false
		m.balance = 0
		m.loginWaiting = false
		m.loginDevice = LoginDevice{}
		if m.mode == modeLogin {
			m.mode = m.loginReturn
		}
		m.status = stDim.Render("logged out - now anonymous (free models + grant keys); [L] to log back in")
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
	case agentEventMsg:
		return m.onAgentEvent(msg)
	case agentConfirmMsg:
		// A side-effecting tool wants to run: pause the turn for an on-screen y/N (default
		// DENY). The loop goroutine is blocked on the confirm's resp channel meanwhile.
		c := agentConfirm(msg)
		m.agentPendingConfirm = &c
		m.agentLines = append(m.agentLines, "  "+stEmber.Render("? ")+stKey.Render(c.summary())+stDim.Render("   run it? [y/N]"))
		m.status = stEmber.Render("! " + c.tool + " - y/n")
		return m, nil
	case agentCostMsg:
		m.agentCost += float64(msg)
		return m, nil
	case agentDoneMsg:
		m.agentBusy = false
		m.status = stDim.Render("AGENT ready - ask it to do something")
		return m, fetchBalance(m.broker, m.user)
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
	// The quit-confirm modal owns every key while open (answer the on-air guard).
	if m.mode == modeQuitConfirm {
		switch k.String() {
		case "y", "Y", "enter":
			return m.quitNow()
		default: // n/N/esc/anything else - stay on air, return to where we were
			m.mode = m.quitReturn
			m.status = stDim.Render("still on air - kept sharing")
			return m, nil
		}
	}
	// Ctrl+C is a global quit, intercepted everywhere so the on-air guard can fire
	// (otherwise a text-input mode would swallow it). q/esc stay mode-specific below.
	if k.String() == "ctrl+c" {
		return m.requestQuit()
	}
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
		case "esc":
			// esc DISCONNECTS: drop the channel and return to the band browser. This is
			// "leave this channel", NOT "quit RogerAI" - quitting is a deliberate q from
			// BROWSE (or the on-air guard). tab is the non-destructive peek (below).
			return m.disconnect()
		case "tab":
			// tab is a NON-destructive switch to BROWSE - the channel + endpoint stay
			// live so you can tab back. (esc disconnects; this just looks away.)
			m.mode = modeBrowse
			m.chatIn.Blur()
			m.status = stDim.Render("peeking at the band - the channel stays open · tab/c to return · esc here disconnects")
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
				m.transcript = append(m.transcript,
					stRed.Render("✕ ")+stEmber.Render("no station on air for "+m.connected.Model+" right now"),
					hintTuneOrShare(m.narrow()))
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
		// A preset key jumps straight to its mode; any other key returns to browse.
		if k.String() != "?" {
			if nm, cmd, ok := m.presetForKey(k.String()); ok {
				return nm, cmd
			}
		}
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
	case modeConnecting:
		// The staged tune-in is brief and self-completing; a key lets an impatient
		// operator skip straight to the channel (enter/space) or back out (esc).
		switch k.String() {
		case "esc", "n", "N":
			m.mode = modeBrowse
			m.status = stDim.Render("cancelled - the endpoint stays bound, no channel opened")
			return m, nil
		default:
			return m.finishConnect()
		}
	case modeOverLimit:
		return m.onOverLimitKey(k)
	case modeLimits:
		return m.onLimitsKey(k)
	case modeShare:
		return m.onShareKey(k)
	case modeShareEditor:
		return m.onShareEditorKey(k)
	case modeShareSetup:
		return m.onShareSetupKey(k)
	case modeAgent:
		return m.onAgentKey(k)
	case modeLogin:
		return m.onLoginKey(k)
	default: // browse
		// FILTER ENTRY owns every key while open: typing edits the live name filter, esc
		// clears + closes, enter keeps it applied and returns to the list. Handled BEFORE
		// presetForKey + the browse keys so f, m, l, 0, etc. are NEVER stolen mid-filter
		// (the founder's "guard f so it isn't stolen elsewhere"). The filter is also never
		// reachable from the command palette / chat / editors, which own their own keys
		// and don't fall through to this browse default.
		if m.filterMode {
			switch k.String() {
			case "esc":
				// esc clears + closes the filter (back to the full list).
				m.filterMode = false
				m.filterIn.Blur()
				m.filterIn.SetValue("")
				m.filterApplied = ""
				m.clampBrowse()
				m.status = stDim.Render("filter cleared")
				return m, nil
			case "enter":
				// enter keeps the filter applied and returns to the list (cursor navigable).
				m.filterMode = false
				m.filterIn.Blur()
				m.filterApplied = strings.TrimSpace(m.filterIn.Value())
				m.clampBrowse()
				return m, nil
			}
			// Any other key edits the buffer; the filter applies LIVE as you type.
			var c tea.Cmd
			m.filterIn, c = m.filterIn.Update(k)
			m.filterApplied = strings.TrimSpace(m.filterIn.Value())
			m.clampBrowse()
			return m, c
		}
		// The preset bank: 1 TUNE IN · 2 SHARE · 3 CONFIG · L LOGIN · ? HELP. Handled
		// first so the always-visible top bar's buttons jump straight to their mode.
		if nm, cmd, ok := m.presetForKey(k.String()); ok {
			return nm, cmd
		}
		switch k.String() {
		case "q":
			return m.requestQuit()
		case "/", ":":
			m.mode = modeCommand
			m.cmd.Focus()
			return m, textinput.Blink
		case "f":
			// f opens the live name filter (the headline scale fix). It seeds from any
			// already-applied filter so f re-opens to edit, not to clear.
			m.filterMode = true
			m.filterIn.SetValue(m.filterApplied)
			m.filterIn.CursorEnd()
			m.filterIn.Focus()
			return m, textinput.Blink
		case "S":
			// S cycles the sort dial (strongest / cheapest / fastest / most-stations),
			// mirroring the /bands web page so CLI + web match. Re-sorting can move the
			// selected band, so re-clamp the window.
			m.sortMode = (m.sortMode + 1) % sortCount
			m.clampBrowse()
			m.status = stDim.Render("sort: " + sortLabel(m.sortMode))
			return m, nil
		case "F":
			// quick toggle: only bands with a FREE-now station.
			m.fFree = !m.fFree
			m.clampBrowse()
			return m, nil
		case "C":
			// quick toggle: only confidential / verified (lineage) bands.
			m.fConf = !m.fConf
			m.clampBrowse()
			return m, nil
		case "O":
			// quick toggle: only bands with a station on air.
			m.fOn = !m.fOn
			m.clampBrowse()
			return m, nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.scrollBrowse()
		case "down", "j":
			if m.cursor < len(m.visibleBands())-1 { // navigate the FILTERED + SORTED view
				m.cursor++
			}
			m.scrollBrowse()
		case "enter":
			// Enter on the band you are ALREADY connected to jumps straight into the open
			// channel (no re-tune, no staged sequence) - the connected row is a toggle:
			// Enter opens it, d (below) disconnects it. Enter on any other band tunes in.
			if m.connected != nil && m.cursorOnConnected() {
				m.mode = modeChat
				m.chatIn.Focus()
				m.status = stGold.Render(glyphVerify+" ") + stLive.Render("back on channel ") + m.connected.NodeID
				return m, textinput.Blink
			}
			return m.connect()
		case "d":
			// Disconnect FROM THE LIST: if connected, d drops the channel right here so the
			// user can see + toggle what is connected without entering it first (the
			// founder's "disconnect should be doable from the tune-in list"). The band stays
			// in the list as a tunable station (sticky), so Enter re-tunes it.
			if m.connected != nil {
				return m.disconnect()
			}
			m.status = stDim.Render("nothing connected to disconnect - enter tunes in")
			return m, nil
		case "c", "tab":
			if m.connected != nil {
				m.mode = modeChat
				m.chatIn.Focus()
				return m, textinput.Blink
			}
		case "s":
			// The one obvious section toggle: jump to SHARE (provide). esc/s returns.
			return m.toggleSection()
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
		sysLine("/model /clear /save /system <p> /cost /confidential /endpoint /disconnect /quit")
		sysLine("esc or /disconnect leaves this channel · /quit exits RogerAI · tab peeks at the band")
		return m, nil
	case "disconnect", "leave", "dc":
		// Explicit "leave this channel" - same as esc. Returns to the band browser.
		return m.disconnect()
	case "quit", "q":
		// /quit in a CHANNEL means leave the CHANNEL (disconnect), not quit the whole
		// app - quitting RogerAI is a deliberate q from BROWSE / the on-air guard. If a
		// share is live, fall through to the quit path so the on-air guard can fire.
		if m.onAirCount() > 0 {
			return m.requestQuit()
		}
		return m.disconnect()
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
		if !m.loggedInState() {
			m.status = stDim.Render("not logged in - ") + stKey.Render("type /login") + stDim.Render(" to use your wallet")
			return m, nil
		}
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
	case "login", "logout":
		// Both open the same confirmable [L] panel: logged out it offers the login
		// prompt, logged in it offers the logout confirm. Neither acts on its own.
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
		return m.requestQuit()
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
	// ASYNC: enter the provider table in a LOADING pose IMMEDIATELY and fire detection
	// off the event loop. detectShares used to run synchronously here and block every
	// keystroke for seconds on a busy host (120+ open ports to probe); now the user
	// sees the scanning indicator at once and the sharesDetectedMsg lands the rows.
	m.mode = modeShare
	m.shareLoading = true
	m.setupOnEmpty = true // the initial open: an empty scan drops into the guided wizard
	m.shareRescan = false
	m.setupHint = ""
	m.sharePending = ""
	if len(args) > 0 {
		m.sharePending = args[0] // `/share <model>` shortcut: flip it on air after detect
	}
	m.status = stDim.Render("scanning the band for local models…")
	return m, detectSharesCmd(m.shareUp)
}

// onSharesDetected folds an async detection result into the provider table: it
// clears the loading pose, builds the rows, applies a pending `/share <model>`
// shortcut, and - only on the initial open (setupOnEmpty) - drops into the guided
// setup wizard when nothing was found. An empty re-detect from inside the table
// (setupOnEmpty=false) stays on the table with a clear note rather than yanking the
// user into the wizard mid-list.
func (m model) onSharesDetected(found []detect.Found) (tea.Model, tea.Cmd) {
	m.shareLoading = false
	if len(found) == 0 {
		if m.setupOnEmpty {
			// GUIDED FALLBACK: nothing detected -> the in-TUI setup wizard (pick a tool for
			// a one-liner, or paste a URL we verify), not a dead-end status line. If we were
			// already in the wizard (a re-scan / named-tool pick that found nothing), keep
			// the wizard but flag the empty result inline.
			nm := m.enterShareSetup()
			if m.shareRescan {
				note := m.setupHint
				if note == "" {
					note = "still nothing on the defaults / your open ports - give it a moment, or paste the URL below"
				}
				nm.setupErr = note
			}
			return nm, nil
		}
		m.status = stEmber.Render("! still nothing on the defaults / your open ports - press r to re-scan, or start a local LLM")
		return m, nil
	}
	m.loadShareRows(found)
	// `/share <model>` shortcut: flip that exact model on air, then show the table.
	if m.sharePending != "" {
		want := m.sharePending
		m.sharePending = ""
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

// loadShareRows builds the provider table by FLATTENING every detected server x
// its served models into one row list (de-duplicated by model id), with EACH row
// carrying its own upstream chat URL. On a multi-endpoint box this lists all real
// local models - e.g. :8060 gpt-oss-20b, :8080 gpt-oss-120b, :8081 qwen3-vl-8b, and
// a shim's many models on :8788 - not just the first server's. The first detected
// server's chat URL is kept as m.shareUp for back-compat (the headline default),
// but on-air uses each row's own upstream so a model goes live against the server
// that actually serves it. The first server's models keep priority on a dup id.
func (m *model) loadShareRows(found []detect.Found) {
	if m.shares == nil {
		m.shares = map[string]*agent.Session{}
	}
	if len(found) > 0 {
		m.shareUp = normalizeUpstream(found[0].Chat)
	}
	seen := map[string]bool{}
	rows := make([]shareRow, 0)
	for _, srv := range found {
		up := normalizeUpstream(srv.Chat)
		for _, mdl := range srv.Models {
			if mdl == "" || seen[mdl] {
				continue
			}
			seen[mdl] = true
			ctxLen := srv.Ctx[mdl]
			if ctxLen <= 0 {
				ctxLen = 32768
			}
			rows = append(rows, shareRow{model: mdl, ctx: ctxLen, upstream: up})
		}
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
	// Free by default (visible + changeable in the editor). The price + time-of-use
	// schedule come from pricingFor (an edited price, the saved onboarding price for
	// the default model, else free).
	p := m.pricingFor(row.model)
	priceIn, priceOut := p.In, p.Out
	// Share-to-EARN needs an account: a priced share requires `rogerai login` (the
	// broker 403s a priced node from an unlinked owner). Flash a clear login prompt
	// instead of a failed start; free sharing stays open to anyone, no login.
	if (priceIn > 0 || priceOut > 0 || len(p.Windows) > 0) && !m.loggedInState() {
		m.status = stEmber.Render("log in to earn - run ") + stKey.Render("/login") + stDim.Render(" (free sharing works without an account)")
		return
	}
	// Each row goes on air against the server that actually serves it (its own
	// upstream), falling back to the headline shareUp for a row that predates the
	// per-row upstream (e.g. a legacy/synthetic row).
	up := row.upstream
	if up == "" {
		up = m.shareUp
	}
	sess, err := agent.Start(agent.Config{
		Broker: m.broker, Upstream: up, NodeID: node,
		Region: "home", HW: m.hooks.HW, Model: row.model,
		PriceIn: priceIn, PriceOut: priceOut, Ctx: row.ctx, Parallel: 4,
		Schedule: schedToProtocol(p.Windows),
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
	m.status = stRed.Render(glyphOnAir+" ON AIR ") + stDim.Render("- sharing ") + stKey.Render(row.model) + stDim.Render(" ("+kind+")")
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

// onAirCount is how many models are currently ON AIR (live shares). Drives the
// quit-guard: quitting while > 0 must confirm going off air first.
func (m model) onAirCount() int {
	n := m.sharesOnAir()
	if n == 0 && m.onAir && m.share != nil {
		n = 1 // a legacy single-share session not tracked in the shares map
	}
	return n
}

// requestQuit is the single quit entry point. While ON AIR (sharing as a provider)
// it does NOT quit immediately: it opens a confirm so the user knows quitting takes
// them off air. Off air, quit is immediate. Returns the (model, cmd) to apply.
func (m model) requestQuit() (tea.Model, tea.Cmd) {
	if m.onAirCount() > 0 {
		m.quitReturn = m.mode
		m.mode = modeQuitConfirm
		return m, nil
	}
	return m, tea.Quit
}

// quitNow goes cleanly off air (releasing every share) and quits. Used when the
// on-air quit-guard is confirmed.
func (m *model) quitNow() (tea.Model, tea.Cmd) {
	m.stopAllShares()
	return m, tea.Quit
}

// onShareKey drives the k9s-style provider table: up/down (j/k) move the
// reverse-video cursor, enter/a/space toggle the selected model on/off air, p
// opens the per-model price + schedule editor (login-gated), r re-detects, esc/q
// leaves (shares keep running in the background), s returns to TUNE IN.
func (m *model) onShareKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Preset bank: 1 TUNE IN · 3 CONFIG · L LOGIN · ? HELP jump straight out of the
	// table. (2 SHARE is the current screen, so it is a no-op pressed-state and falls
	// through to the table keys below; `a`/`enter` toggle on-air as before.)
	if k.String() != "2" {
		if nm, cmd, ok := m.presetForKey(k.String()); ok {
			return nm, cmd
		}
	}
	switch k.String() {
	case "esc", "q", "s":
		m.mode = modeBrowse
		m.status = stDim.Render("TUNE IN - browse the band, enter to tune in")
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
	case "p", "e":
		// Open the price + time-of-use schedule editor for the selected model. This is
		// EARNING, so it is login-gated: anonymous users get a clear /login prompt
		// (free sharing stays open to anyone).
		return m.enterShareEditor()
	case "r":
		// ASYNC re-detect: stay on the table in the loading pose and probe off the event
		// loop (a busy host's port scan must never freeze the table). An empty result
		// keeps us on the table with a note (setupOnEmpty stays false) rather than yanking
		// into the wizard mid-list.
		m.shareLoading = true
		m.setupOnEmpty = false
		m.shareRescan = true
		m.setupHint = ""
		m.sharePending = ""
		m.status = stDim.Render("re-scanning the band for local models…")
		return m, detectSharesCmd(m.shareUp)
	}
	return m, nil
}

// enterShareSetup opens the in-TUI guided fallback when no local model was
// detected: a small wizard to pick a tool (for a start one-liner) or paste an
// endpoint we verify with detect.Probe. Mirrors the CLI guidedUpstream flow.
func (m model) enterShareSetup() model {
	m.mode = modeShareSetup
	m.setupCursor = 0
	m.setupPaste = ""
	m.setupErr = ""
	m.status = stDim.Render("no local model found - pick what you're running, or paste a URL")
	return m
}

// setupOptions are the guided-fallback choices: a tool (with a start one-liner) or
// the paste-a-URL path. Order is the on-screen order.
var setupOptions = []struct{ key, label, oneLiner string }{
	{"ollama", "Ollama", "ollama serve   then:  ollama run llama3.2   (→ :11434)"},
	{"lm-studio", "LM Studio", "LM Studio → Developer → Start Server   (→ :1234)"},
	{"vllm", "vLLM", "vllm serve <model> --port 8000   (→ :8000)"},
	{"llamacpp", "llama.cpp", "llama-server -m <model>.gguf --port 8080   (→ :8080)"},
	{"other", "Other - paste a URL", ""},
}

// onShareSetupKey drives the guided fallback: up/down move, enter picks; a named
// tool shows its one-liner + offers a re-scan; the "Other" row turns the row into
// a URL input we verify on enter. esc/s leaves.
func (m *model) onShareSetupKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	pasting := m.setupCursor == len(setupOptions)-1
	// Preset bank jumps - but NOT while pasting a URL (those keystrokes are the URL),
	// and not for `2`/SHARE which is the current section.
	if !pasting && k.String() != "2" {
		if nm, cmd, ok := m.presetForKey(k.String()); ok {
			return nm, cmd
		}
	}
	switch k.String() {
	case "esc", "s":
		m.mode = modeBrowse
		m.status = stDim.Render("TUNE IN - browse the band")
		return m, nil
	case "up", "k":
		if m.setupCursor > 0 {
			m.setupCursor--
		}
		m.setupErr = ""
		return m, nil
	case "down", "j":
		if m.setupCursor < len(setupOptions)-1 {
			m.setupCursor++
		}
		m.setupErr = ""
		return m, nil
	case "r":
		// Re-scan (after the user started their tool in another terminal). ASYNC: enter
		// the loading table and probe off the event loop; an empty result returns to the
		// wizard with a note (setupOnEmpty=true), a found result lands the table.
		m.mode = modeShare
		m.shareLoading = true
		m.setupOnEmpty = true
		m.shareRescan = true
		m.setupHint = ""
		m.sharePending = ""
		m.setupErr = ""
		m.status = stDim.Render("re-scanning the band for local models…")
		return m, detectSharesCmd(m.shareUp)
	case "enter":
		if pasting {
			url := strings.TrimSpace(m.setupPaste)
			if url == "" {
				m.setupErr = "paste your endpoint, e.g. http://127.0.0.1:8081"
				return m, nil
			}
			if f, ok := detect.Probe(url); ok {
				m.shareUp = normalizeUpstream(f.Chat)
				m.loadShareRows([]detect.Found{f})
				m.mode = modeShare
				m.status = stLive.Render("verified " + f.BaseURL + " - " + plural(len(m.shareRows), "model") + " ready")
				return m, nil
			}
			m.setupErr = "no OpenAI-compatible server at " + url + " (no /v1/models) - check it and try again"
			return m, nil
		}
		// A named tool: ASYNC re-detect (maybe it's already up). If nothing comes back we
		// return to the wizard with this tool's start one-liner; a found result lands the
		// table. Detection runs off the event loop so the pick never freezes the wizard.
		m.mode = modeShare
		m.shareLoading = true
		m.setupOnEmpty = true
		m.shareRescan = true
		m.sharePending = ""
		m.setupHint = "start it, then press r to re-scan:  " + setupOptions[m.setupCursor].oneLiner
		m.status = stDim.Render("checking for " + setupOptions[m.setupCursor].label + "…")
		return m, detectSharesCmd(m.shareUp)
	case "backspace":
		if pasting && m.setupPaste != "" {
			m.setupPaste = m.setupPaste[:len(m.setupPaste)-1]
		}
		return m, nil
	default:
		if pasting {
			if s := k.String(); len(s) == 1 {
				m.setupPaste += s
			}
		}
		return m, nil
	}
}

// enterShareEditor opens the per-model price + time-of-use schedule editor for the
// row at the cursor. EARNING requires an account, so this is login-gated: an
// anonymous user is shown "log in to earn - run /login" instead of being allowed
// to set a price that could never pay out. Free sharing stays open to anyone, so
// the table itself (and toggling FREE on/off air) never needs login.
func (m model) enterShareEditor() (tea.Model, tea.Cmd) {
	if len(m.shareRows) == 0 {
		return m, nil
	}
	if !m.loggedInState() {
		m.status = stEmber.Render("log in to earn - run ") + stKey.Render("/login") + stDim.Render("  (free sharing works without an account)")
		return m, nil
	}
	row := m.shareRows[m.shareCursor]
	m.edModel = row.model
	p := m.pricingFor(row.model)
	m.edPriceIn = trimZero(p.In)
	m.edPriceOut = trimZero(p.Out)
	m.edWindows = append([]SchedWindow(nil), p.Windows...)
	m.edField = edFieldOut // out-price is the headline knob
	m.edWinSub = winSubStart
	m.mode = modeShareEditor
	m.status = stDim.Render("tab field · ←→ window start/end/in/out · a add · d del · f free · ⏎ save · esc")
	return m, nil
}

// onShareEditorKey drives the pricing + schedule editor. tab/↑↓ move between
// fields (in, out, add-window, each window), digits edit the focused price, a adds
// a window, d deletes the focused window, f flips a window FREE, enter saves +
// returns to the provider table, esc cancels.
func (m *model) onShareEditorKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	nFields := edFieldFirstWin + len(m.edWindows)
	switch k.String() {
	case "esc":
		m.mode = modeShare
		m.status = stDim.Render("cancelled - price unchanged")
		return m, nil
	case "enter":
		m.commitShareEditor()
		m.mode = modeShare
		return m, nil
	case "tab", "down":
		m.edField = (m.edField + 1) % nFields
		m.edWinSub = winSubStart // each row starts on its Start sub-field
		m.syncWinBuf()
		return m, nil
	case "shift+tab", "up":
		m.edField = (m.edField - 1 + nFields) % nFields
		m.edWinSub = winSubStart
		m.syncWinBuf()
		return m, nil
	case "right", "left":
		// Cycle the sub-field WITHIN the focused window (Start/End/In/Out) so all of
		// its values are editable. No-op outside a window row.
		if m.edField >= edFieldFirstWin {
			if k.String() == "right" {
				m.edWinSub = (m.edWinSub + 1) % winSubCount
			} else {
				m.edWinSub = (m.edWinSub - 1 + winSubCount) % winSubCount
			}
			m.syncWinBuf()
		}
		return m, nil
	case "a":
		// Add a time-of-use window (ChargePoint-style): a default evening peak the
		// user then edits. Focus jumps to the new window.
		m.edWindows = append(m.edWindows, SchedWindow{Start: "18:00", End: "22:00", In: 0, Out: 0})
		m.edField = edFieldFirstWin + len(m.edWindows) - 1
		m.edWinSub = winSubStart
		m.syncWinBuf()
		return m, nil
	case "d":
		if m.edField >= edFieldFirstWin {
			i := m.edField - edFieldFirstWin
			if i >= 0 && i < len(m.edWindows) {
				m.edWindows = append(m.edWindows[:i], m.edWindows[i+1:]...)
				if m.edField >= edFieldFirstWin+len(m.edWindows) {
					m.edField = edFieldOut
				}
			}
		}
		return m, nil
	case "f":
		if m.edField >= edFieldFirstWin {
			i := m.edField - edFieldFirstWin
			if i >= 0 && i < len(m.edWindows) {
				m.edWindows[i].Free = !m.edWindows[i].Free
			}
		}
		return m, nil
	case "backspace":
		m.editShareField(func(s string) string {
			if len(s) > 0 {
				return s[:len(s)-1]
			}
			return s
		})
		return m, nil
	default:
		ch := k.String()
		// Price fields take digits/dot; window fields take digits + ':' (HH:MM).
		if d := digitsDot(ch); d != "" || ch == ":" {
			add := d
			if ch == ":" {
				add = ":"
			}
			m.editShareField(func(s string) string { return s + add })
		}
		return m, nil
	}
}

// editShareField applies edit fn to the buffer of the focused editor field. Price
// fields (in/out) edit the price buffers; a window field edits its focused sub-field
// (Start/End time, or in/out price - cycled with left/right) so a window can set all
// of its values, not just Start.
func (m *model) editShareField(fn func(string) string) {
	switch m.edField {
	case edFieldIn:
		m.edPriceIn = fn(m.edPriceIn)
	case edFieldOut:
		m.edPriceOut = fn(m.edPriceOut)
	case edFieldAddWin:
		// nothing to type on the add-window affordance
	default:
		i := m.edField - edFieldFirstWin
		if i < 0 || i >= len(m.edWindows) {
			return
		}
		w := &m.edWindows[i]
		switch m.edWinSub {
		case winSubEnd:
			w.End = fn(w.End)
		case winSubIn:
			// Edit a persistent string buffer (so a typed "0." survives a keystroke that
			// would parse to 0), then reflect it into the window's float price.
			m.edWinBuf = fn(m.edWinBuf)
			w.In, _ = strconv.ParseFloat(strings.TrimSpace(m.edWinBuf), 64)
		case winSubOut:
			m.edWinBuf = fn(m.edWinBuf)
			w.Out, _ = strconv.ParseFloat(strings.TrimSpace(m.edWinBuf), 64)
		default: // winSubStart
			w.Start = fn(w.Start)
		}
	}
}

// syncWinBuf loads edWinBuf from the focused window's price sub-field (so editing
// continues from the current value), and clears it otherwise. Called whenever the
// focused field or sub-field changes.
func (m *model) syncWinBuf() {
	m.edWinBuf = ""
	if m.edField < edFieldFirstWin {
		return
	}
	i := m.edField - edFieldFirstWin
	if i < 0 || i >= len(m.edWindows) {
		return
	}
	switch m.edWinSub {
	case winSubIn:
		m.edWinBuf = trimZero(m.edWindows[i].In)
	case winSubOut:
		m.edWinBuf = trimZero(m.edWindows[i].Out)
	}
}

// commitShareEditor writes the edited price + schedule into m.prices, persists it
// via the host SavePrice hook (if any), and re-prices a live share so an on-air
// model reflects the new base price immediately.
func (m *model) commitShareEditor() {
	if m.prices == nil {
		m.prices = map[string]Pricing{}
	}
	in, _ := strconv.ParseFloat(strings.TrimSpace(m.edPriceIn), 64)
	out, _ := strconv.ParseFloat(strings.TrimSpace(m.edPriceOut), 64)
	p := Pricing{In: in, Out: out, Windows: append([]SchedWindow(nil), m.edWindows...)}
	m.prices[m.edModel] = p
	if m.hooks.SavePrice != nil {
		m.hooks.SavePrice(m.edModel, p)
	}
	kind := "FREE"
	if in > 0 || out > 0 {
		kind = dollars(out) + "/1M out · " + dollars(in) + "/1M in"
	}
	win := ""
	if len(p.Windows) > 0 {
		win = stDim.Render(" · " + plural(len(p.Windows), "window"))
	}
	m.status = stLive.Render("saved ") + stKey.Render(m.edModel) + stDim.Render(" at ") + stEmber.Render(kind) + win
}

// pricingFor returns the saved (edited) pricing for a model, falling back to the
// host's saved onboarding price for the default model, else free.
func (m model) pricingFor(model string) Pricing {
	if p, ok := m.prices[model]; ok {
		return p
	}
	if model == m.hooks.ShareModel {
		return Pricing{In: m.hooks.SharePriceI, Out: m.hooks.SharePriceO}
	}
	return Pricing{}
}

// schedToProtocol converts the TUI's editable windows into the wire
// protocol.PriceWindow the agent publishes (times "HH:MM" UTC; Free zeroes the
// in-window price). Empty in -> no schedule.
func schedToProtocol(ws []SchedWindow) []protocol.PriceWindow {
	if len(ws) == 0 {
		return nil
	}
	out := make([]protocol.PriceWindow, 0, len(ws))
	for _, w := range ws {
		out = append(out, protocol.PriceWindow{Start: w.Start, End: w.End, In: w.In, Out: w.Out, Free: w.Free})
	}
	return out
}

// doLogin opens the confirmable [L] panel - it NEVER acts on its own, because
// arrow-nav across the preset bank can land on [L]. Logged in it offers a log-out
// confirm; logged out it offers a press-enter-to-log-in prompt. The device flow
// only starts on an explicit ENTER inside the panel (startLogin), and logout only
// on an explicit y (see onLoginKey). The panel returns to the mode it was opened
// from on dismiss.
func (m model) doLogin() (tea.Model, tea.Cmd) {
	if m.mode != modeLogin {
		m.loginReturn = m.mode
	}
	m.mode = modeLogin
	m.loginNote = ""
	// Re-arming the panel never carries over a stale in-flight device flow.
	m.loginWaiting = false
	m.loginDevice = LoginDevice{}
	if m.loggedInState() {
		m.status = stDim.Render("log out? y confirms · n / esc keeps you logged in")
	} else {
		m.status = stDim.Render("log in with GitHub - press enter · esc cancels")
	}
	return m, nil
}

// startLogin begins the GitHub device flow (called only from an explicit ENTER in
// the login panel). It prefers the begin/poll hook pair so the TUI renders its own
// clean panel + auto-opens the browser; it falls back to the single-shot Login hook
// (terminal-printed codes) when only that is wired.
func (m model) startLogin() (tea.Model, tea.Cmd) {
	broker, clientID := m.broker, m.hooks.GitHubID
	if m.hooks.LoginBegin != nil {
		begin := m.hooks.LoginBegin
		m.status = stDim.Render("starting GitHub device login…")
		return m, func() tea.Msg {
			d, err := begin(broker, clientID)
			if err != nil {
				return flowErrMsg("login failed: " + err.Error())
			}
			return loginStartedMsg(d)
		}
	}
	if m.hooks.Login != nil {
		// Legacy single-shot hook: it prints the code to the terminal and blocks.
		m.loginWaiting = true
		m.loginNote = "follow the code shown in your terminal"
		m.status = stDim.Render("opening GitHub device login…")
		login := m.hooks.Login
		return m, func() tea.Msg {
			l, err := login(broker, clientID)
			if err != nil {
				return flowErrMsg("login failed: " + err.Error())
			}
			return loginMsg(l)
		}
	}
	m.status = stDim.Render("login unavailable in this build - run `rogerai login`")
	return m, nil
}

// pollLoginCmd waits (off the event loop) for the user to authorize the started
// device flow, landing a loginMsg on success or a flowErrMsg on failure/timeout.
func (m model) pollLoginCmd() tea.Cmd {
	if m.hooks.LoginPoll == nil {
		return nil
	}
	broker, clientID := m.broker, m.hooks.GitHubID
	poll := m.hooks.LoginPoll
	dev := m.loginDevice
	return func() tea.Msg {
		l, err := poll(broker, clientID, dev)
		if err != nil {
			return flowErrMsg("login failed: " + err.Error())
		}
		return loginMsg(l)
	}
}

// startLogout clears the local GitHub binding (called only from an explicit y in
// the logout confirm panel).
func (m model) startLogout() (tea.Model, tea.Cmd) {
	if m.hooks.Logout == nil {
		m.status = stDim.Render("logout unavailable in this build - run `rogerai logout`")
		m.mode = m.loginReturn
		return m, nil
	}
	logout := m.hooks.Logout
	return m, func() tea.Msg {
		if err := logout(); err != nil {
			return flowErrMsg("logout failed: " + err.Error())
		}
		return logoutMsg{}
	}
}

// onLoginKey owns every key while the [L] login/logout panel is open, so the
// y / n / enter here are NEVER stolen by the preset bank or the arrow-cycle. The
// panel is always dismissible (esc / n / arrowing away keep the current session).
func (m model) onLoginKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While the device flow is in flight, only allow dismissing the panel (the poll
	// keeps running in the background and still lands its loginMsg). No key restarts
	// the flow, so there is never a surprise second code.
	switch k.String() {
	case "esc", "left", "right":
		// Dismiss: keep the current login state exactly as it is. Arrowing away (the
		// preset cycle keys) must NOT start a flow or log anyone out - it just leaves.
		m.mode = m.loginReturn
		m.status = stDim.Render("")
		return m, nil
	}
	if m.loggedInState() {
		// LOGGED IN -> a logout confirm. y logs out; everything else keeps the session.
		switch k.String() {
		case "y", "Y":
			return m.startLogout()
		case "n", "N":
			m.mode = m.loginReturn
			m.status = stDim.Render("still logged in")
			return m, nil
		}
		return m, nil
	}
	// LOGGED OUT -> press enter to start the device flow (+ auto-open browser).
	if !m.loginWaiting {
		switch k.String() {
		case "enter":
			return m.startLogin()
		}
	}
	return m, nil
}

// loginView renders the confirmable [L] panel: the clean GitHub device-flow panel
// while waiting for authorization (#2), the log-out confirm when logged in (#5),
// or the press-enter login prompt when logged out (#5). All forms are left-aligned,
// the device code is rendered in the mono key style, and the panel is width /
// NO_COLOR / narrow safe (it wraps no fixed-width art; the bordered plate degrades
// to plain text when color is stripped).
func (m model) loginView(w int) string {
	pulse := "(( • ))"

	// IN FLIGHT: the device flow started - the tidy left-aligned panel (#2/#3).
	if m.loginWaiting && m.loginDevice.UserCode != "" {
		note := m.loginNote
		if note == "" {
			note = "opened in your browser (or copy the link above)"
		}
		body := stKey.Render("GITHUB LOGIN") + "\n\n" +
			stDim.Render("  1 · open   ") + stLive.Render(m.loginDevice.VerificationURI) + "\n" +
			stDim.Render("  2 · code   ") + stKey.Render(m.loginDevice.UserCode) + "\n\n" +
			stGold.Render("  "+pulse) + stDim.Render(" waiting for authorization...") + "\n" +
			stDim.Render("  "+note) + "\n\n" +
			stDim.Render("  esc backs out (you can /login again any time)")
		return "\n" + stPanel.Render(body) + "\n"
	}

	// LOGGED IN -> the log-out confirm (#5). Never auto-logs-out.
	if m.loggedInState() {
		who := "@" + m.ghLogin
		if m.ghLogin == "" {
			who = "your account"
		}
		body := stKey.Render("ACCOUNT") + "\n\n" +
			stGold.Render("  "+glyphVerify+" ") + stDim.Render("logged in as ") + stSelText.Render(who)
		if m.haveBal {
			body += stDim.Render(" · ") + stEmber.Render(dollars(m.balance))
		}
		body += "\n\n" +
			"  " + stDim.Render("log out? ") + stEmber.Render("[y/N]") + "\n\n" +
			stDim.Render("  y logs out (clears this session) · n / esc keeps you logged in")
		return "\n" + stPanel.Render(body) + "\n"
	}

	// LOGGED OUT -> press enter to start the GitHub device flow (#5).
	body := stKey.Render("GITHUB LOGIN") + "\n\n" +
		stDim.Render("  log in with GitHub to use your wallet + earn as a provider") + "\n\n" +
		"  " + stDim.Render("press ") + stKey.Render("enter") + stDim.Render(" to start (opens your browser) · esc cancels")
	return "\n" + stPanel.Render(body) + "\n"
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
	bd, ok := m.selectedBand() // the cursor against the filtered + sorted view
	if !ok {
		return m, nil
	}
	if !bd.online || bd.cheapest == nil {
		// An offline band (incl. the sticky recent station whose node aged out of
		// /discover): Enter re-scans the band to find it back on air, rather than a
		// dead-end - the natural "bring it back" action so a recent station is always
		// re-tunable from here.
		m.status = stDim.Render("no station on air for ") + stKey.Render(bd.model) + stDim.Render(" right now - re-scanning the band…")
		m.scanErr, m.scanned = false, false
		return m, fetchOffers(m.broker)
	}
	// Anonymous = free models only. Tuning a PRICED band needs an account wallet:
	// flash a clear inline login prompt instead of opening a confirm the broker would
	// reject. A FREE band (minOut 0, or a free-now window) stays open to anyone.
	if !m.loggedInState() && bd.minOut > 0 && !bd.free {
		m.status = stEmber.Render("this band is paid - ") + stKey.Render("type /login") + stDim.Render(" to use your wallet (free bands work without an account)")
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
	// Remember this station as the "sticky" recent band so it never vanishes from the
	// browse list if its node ages out of /discover while we are on the channel (the
	// founder's vanishing-band bug). mergeStickyBand re-includes it on every re-scan.
	sticky := o
	m.lastConnected = &sticky
	// WARM RECONNECT: a band we have tuned in to before this session skips the staged
	// scan/lock/handshake animation and drops straight into the open channel - only a
	// FIRST (cold) tune-in plays the full sequence. The endpoint is already bound, so a
	// reconnect is genuinely instant.
	warm := m.recentBands[o.Model]
	if m.recentBands == nil {
		m.recentBands = map[string]bool{}
	}
	m.recentBands[o.Model] = true
	if warm {
		m.mode = modeConnecting
		m.connectStage = connectStageDone
		return m.finishConnect()
	}
	// Rather than snapping straight to the channel, run the web's staged tune-in:
	//   ◉ scanning stations … ok
	//   ◉ locking strongest @x · NN t/s · 0.NN $/M … ok
	//   ◉ lineage handshake ◆ weights·shard·token … ok
	//   ◉ CHANNEL OPEN <model> via @x ◆ verified
	// then the clean BASE URL / API KEY / MODEL plate + "roger that." This replaces
	// the old blank wait with a legible "what's happening" sequence that matches the
	// site's tune-in animation. The endpoint is already bound (above); the channel
	// itself opens when the sequence completes (advanceConnect). Under quiet the
	// sequence is rendered fully resolved in a single frame.
	m.mode = modeConnecting
	m.connectStage = 0
	m.connectStartFrame = m.frame
	m.status = stRed.Render(glyphOnAir+" ") + stLive.Render("tuning in to ") + stSelText.Render(o.NodeID) + stDim.Render(" …")
	if quiet || m.compact {
		// No animation in a pipe / NO_COLOR, or in the windowshade compact mode (an
		// explicit reduced-motion): jump straight to the resolved channel, no staged
		// tune-in churn.
		return m.finishConnect()
	}
	return m, tick()
}

// connectStages is the number of staged steps in the tune-in sequence (scan, lock,
// handshake, CHANNEL OPEN). connectStageDone is the terminal stage (all steps "ok"
// and the channel held open, ready to drop into CHANNEL on the next beat).
const (
	connectStages    = 4
	connectStageDone = connectStages
	// connectDwellFrames is how many ticks each staged step holds before the next
	// reveals - ~3 frames at the 160ms tick (~0.5s/step) so the sequence reads as a
	// deliberate lock, not a flicker, and completes in ~2s.
	connectDwellFrames = 3
)

// advanceConnect steps the staged tune-in on each tick: every connectDwellFrames
// it reveals the next step; once every step is "ok" it drops into the live CHANNEL.
// Called from the tick handler while in modeConnecting.
func (m model) advanceConnect() (tea.Model, tea.Cmd) {
	if m.mode != modeConnecting {
		return m, tick()
	}
	elapsed := m.frame - m.connectStartFrame
	stage := elapsed / connectDwellFrames
	if stage > connectStageDone {
		stage = connectStageDone
	}
	m.connectStage = stage
	if stage >= connectStageDone {
		return m.finishConnect()
	}
	return m, tick()
}

// finishConnect drops the completed tune-in sequence into the live CHANNEL: it
// auto-switches to CHANNEL mode and compacts the header (the founder's
// "compact-on-connect"). The endpoint stays live regardless of mode.
func (m model) finishConnect() (tea.Model, tea.Cmd) {
	o := m.connected
	m.mode = modeChat
	m.connectStage = connectStageDone
	m.chatIn.Focus()
	if len(m.transcript) == 0 {
		m.transcript = append(m.transcript, stDim.Render("◂ ")+stLive.Render("roger that")+stDim.Render(" - channel open. type to talk, /help for in-session commands."))
	}
	m.status = stGold.Render(glyphVerify+" ") + stLive.Render("on channel ") + o.NodeID + stDim.Render(" - endpoint live · roger that")
	return m, textinput.Blink
}

// disconnect leaves the current CHANNEL: it drops the connected band and returns
// to the band browser. This is "leave this channel", a distinct action from
// quitting RogerAI (q from BROWSE / the on-air guard). The local proxy endpoint is
// left bound (cheap, and bots may still hold it) but the conversation is cleared
// so re-tuning starts fresh. A no-op when not connected.
func (m model) disconnect() (tea.Model, tea.Cmd) {
	if m.connected == nil {
		m.mode = modeBrowse
		return m, nil
	}
	was := m.connected.Model
	m.connected = nil
	m.transcript = nil
	m.sessCost = 0
	m.sysPrompt = ""
	m.minimized = false
	m.chatIn.Blur()
	m.chatIn.SetValue("")
	m.mode = modeBrowse
	m.status = stDim.Render("disconnected from ") + stKey.Render(was) + stDim.Render(" - back on the band · enter to tune in, q to quit RogerAI")
	return m, nil
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
		m.bands = m.mergeStickyBand(groupBands(m.offers, m.limits))
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
		// Preset bank jumps (only when NOT editing a numeric field, so a typed digit in
		// the editor is never stolen). 3 CONFIG is the current screen -> no-op.
		if k.String() != "3" {
			if nm, cmd, ok := m.presetForKey(k.String()); ok {
				return nm, cmd
			}
		}
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

// presetKey is one button on the always-visible preset-station bar: a radio
// preset that lights up when its mode is active and jumps to it when pressed.
type presetKey struct {
	key, label string
	active     bool
}

// presetButtons returns the preset bank for the current mode, with exactly one
// preset lit (the section/screen the user is in). TUNE IN covers browse/command/
// chat/connect; SHARE covers the provider table / editor / setup; CONFIG maps to
// the limits screen (the in-TUI config surface). LOGIN + HELP are always-available
// actions (lit only while their screen shows).
func (m model) presetButtons() []presetKey {
	tuneActive := !m.inShareSection() && m.mode != modeLimits && m.mode != modeHelp && m.mode != modeAgent && m.mode != modeLogin
	// [L] flips its label by state: LOGOUT when an account is linked, LOGIN otherwise.
	// It is a resting-capable mode now (the confirmable panel), so it lights while open.
	loginLabel := "LOGIN"
	if m.loggedInState() {
		loginLabel = "LOGOUT"
	}
	return []presetKey{
		{"0", "AGENT", m.mode == modeAgent},
		{"1", "TUNE IN", tuneActive},
		{"2", "SHARE", m.inShareSection()},
		{"3", "CONFIG", m.mode == modeLimits},
		{"L", loginLabel, m.mode == modeLogin},
		{"?", "HELP", m.mode == modeHelp},
	}
}

// stPreset / stPresetOn render a preset button: a lit (current) preset is a
// pressed, reverse-video red glint (like a depressed station button); the rest are
// dim. Under NO_COLOR the reverse-video is stripped and a leading dot marks the lit
// preset so the active mode is still unmistakable.
var (
	stPreset   = lipgloss.NewStyle().Foreground(cDim)
	stPresetOn = lipgloss.NewStyle().Foreground(cInkBg).Background(cRed).Bold(true)
)

// presetBar renders the always-visible "preset bank" of radio-station buttons:
// [1] TUNE IN  [2] SHARE  [3] CONFIG  [L] LOGIN  [?] HELP, with the CURRENT mode
// lit like a pressed preset. It replaces the buried single "s share" hint and makes
// the two modes unmistakable. Compact + NO_COLOR-safe: under a narrow width it drops
// to just key glyphs ([1][2][3][L][?]) so it never overflows.
func (m model) presetBar(w int) string {
	btns := m.presetButtons()
	narrow := m.narrow()
	parts := make([]string, 0, len(btns))
	for _, b := range btns {
		var cell string
		if narrow {
			// Narrow: just the key, lit preset reverse-video (or `>key` under NO_COLOR).
			if b.active {
				cell = stPresetOn.Render(" " + b.key + " ")
			} else {
				cell = stPreset.Render("[" + b.key + "]")
			}
		} else {
			label := "[" + b.key + "] " + b.label
			if b.active {
				// A leading dot survives NO_COLOR (where the bg glint is stripped) so the
				// lit preset reads as pressed even with no color.
				cell = stPresetOn.Render(" •" + label + " ")
			} else {
				cell = stPreset.Render(" " + label + " ")
			}
		}
		parts = append(parts, cell)
	}
	bar := strings.Join(parts, stPreset.Render(" "))
	return "  " + bar
}

// presetForKey maps a top-level key press to its preset action, returning the new
// model + cmd and true when the key was a preset jump (so onKey can short-circuit).
// It is the keyboard half of the preset bank: 1 -> TUNE IN, 2 -> SHARE, 3 -> CONFIG
// (limits), L -> LOGIN, ? -> HELP. It is only consulted from non-text-entry modes
// (browse / a SHARE sub-screen / limits / help) so it never steals a typed digit in
// the command palette, the chat input, or a numeric price/limit editor.
// toggleCompact flips the windowshade compact mode and persists the choice via the
// host SaveCompact hook (nil = session-only). It also clears the connected-header
// `minimized` sub-toggle so the two header collapses never fight: expanding out of
// compact returns to the full header, and compact subsumes the thin-bar minimize.
func (m model) toggleCompact() model {
	m.compact = !m.compact
	if m.compact {
		m.status = stDim.Render("compact - calm, dense, animation-free · m expands")
	} else {
		m.minimized = false
		m.status = stDim.Render("expanded - the full operating manual · m compacts")
	}
	if m.hooks.SaveCompact != nil {
		m.hooks.SaveCompact(m.compact)
	}
	return m
}

// cyclePreset steps the preset bank one button in dir (+1 next / -1 previous),
// wrapping around the ends, and fires the destination's jump - so left/right behave
// exactly like pressing that preset's number/letter. The "current" preset is the lit
// one in presetButtons() (exactly one is lit in every context cyclePreset is reached
// from: AGENT / TUNE IN / SHARE / CONFIG / HELP); LOGIN is never a resting mode, so a
// missing lit preset just falls back to the TUNE IN slot. The new key is dispatched
// back through presetForKey so the jump action is identical to the keypress.
func (m model) cyclePreset(dir int) (tea.Model, tea.Cmd, bool) {
	btns := m.presetButtons()
	cur := 1 // default to TUNE IN if nothing is lit (LOGIN has no resting mode)
	for i, b := range btns {
		if b.active {
			cur = i
			break
		}
	}
	n := len(btns)
	next := ((cur+dir)%n + n) % n
	return m.presetForKey(btns[next].key)
}

func (m model) presetForKey(key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "right":
		// Sequential tab navigation across the preset bank: step to the NEXT preset
		// (0 -> 1 -> 2 -> 3 -> L -> ? -> wrap to 0) and fire its jump, so left/right
		// behave exactly like pressing the number/letter. presetForKey is only ever
		// consulted from non-text-entry contexts (browse / a SHARE sub-screen not pasting
		// / limits-not-editing / help), so left/right inherit that exact guard and never
		// steal a cursor move in the schedule editor's window sub-fields, the command
		// palette, chat, the AGENT prompt, the `f` filter, or a numeric field.
		return m.cyclePreset(+1)
	case "left":
		// Previous preset (wraps the other way: 0 -> ? -> L -> 3 -> 2 -> 1 -> 0).
		return m.cyclePreset(-1)
	case "m":
		// COMPACT (the "windowshade"): toggle the calm, dense, animation-free view. Lives
		// alongside the preset jumps so it works in every non-text-entry context (browse /
		// the SHARE table / limits-not-editing / help) and is NEVER stolen while typing in
		// chat, the command palette, or a numeric price/limit/schedule editor (those modes
		// own their keys and don't consult presetForKey). Persisted via SaveCompact so the
		// choice sticks across launches (nil = session-only).
		return m.toggleCompact(), nil, true
	case "0":
		// AGENT: open the embedded tool-capable harness (dj.md persona). It runs on the
		// open channel's model, else the last band tuned in this session; /model switches.
		nm, cmd := m.enterAgent()
		return nm, cmd, true
	case "1":
		// TUNE IN: leave any SHARE/limits screen, back to the band browser. A live
		// channel stays open (tab/c returns to it).
		if m.inShareSection() || m.mode == modeLimits {
			m.mode = modeBrowse
			m.status = stDim.Render("TUNE IN - browse the band, enter to tune in")
		}
		return m, nil, true
	case "2":
		// SHARE: open the provider table (or the guided fallback). doShare returns the
		// (model, cmd) so we surface it as-is.
		nm, cmd := m.doShare(nil)
		return nm, cmd, true
	case "3":
		// CONFIG: the in-TUI per-model spend-limits screen.
		m.enterLimits()
		return m, nil, true
	case "l", "L":
		nm, cmd := m.doLogin()
		return nm, cmd, true
	case "?":
		m.mode = modeHelp
		return m, nil, true
	}
	return m, nil, false
}

// ---- view ----
func (m model) View() string {
	w := m.effWidth()
	var b strings.Builder
	// COMPACT (the "windowshade"): no expanded preset bar and no spacer - the dense
	// one-line header carries the section + counts + account + the `m:expand` hint, so
	// the whole top collapses to a single strip + a hairline rule.
	if m.compact {
		b.WriteString(m.compactHeader(w) + "\n")
	} else {
		// A blank spacer line sets the preset bar apart from the brand lockup below it, so
		// the [1] TUNE IN ... bar and the ▟█▙ R O G E R · A I ((•)) logo read as two
		// distinct rows instead of one cramped block. A single line keeps it tight on a
		// short terminal; an empty line is inherently NO_COLOR / narrow-safe.
		b.WriteString(m.presetBar(w) + "\n\n")
		b.WriteString(m.header(w) + "\n")
	}
	switch m.mode {
	case modeHelp:
		b.WriteString(m.helpView())
	case modeChat:
		b.WriteString(m.chatView(w))
	case modeConnectConfirm:
		b.WriteString(m.confirmView(w))
	case modeConnecting:
		b.WriteString(m.connectingView(w))
	case modeOverLimit:
		b.WriteString(m.overLimitView(w))
	case modeLimits:
		b.WriteString(m.limitsView(w))
	case modeShare:
		b.WriteString(m.shareView(w))
	case modeShareEditor:
		b.WriteString(m.shareEditorView(w))
	case modeShareSetup:
		b.WriteString(m.shareSetupView(w))
	case modeQuitConfirm:
		b.WriteString(m.quitConfirmView(w))
	case modeAgent:
		b.WriteString(m.agentView(w))
	case modeLogin:
		b.WriteString(m.loginView(w))
	default:
		b.WriteString(m.browseView(w))
	}
	if m.connected != nil && m.mode != modeChat && m.mode != modeConnectConfirm && m.mode != modeConnecting && m.mode != modeOverLimit && m.mode != modeLimits && m.mode != modeAgent && m.mode != modeLogin && !m.inShareSection() {
		// COMPACT drops the bordered endpoint plate (a "compact-on-connect extra") to a
		// single terse status line - the load-bearing endpoint stays one /endpoint away.
		if m.compact {
			b.WriteString("\n" + truncVisible("  "+stRed.Render(glyphOnAir+" ")+stLive.Render("channel open")+stDim.Render(" · ")+stKey.Render(m.endpoint)+stDim.Render(" · /chat"), w))
		} else {
			b.WriteString("\n" + m.endpointPanel(w))
		}
	}
	// The ON AIR provider panel rides under the browse view whenever /share is live.
	// COMPACT drops the bordered panel to a one-line status (density + width-safety).
	if m.onAir && m.share != nil && (m.mode == modeBrowse || m.mode == modeCommand) {
		if m.compact {
			b.WriteString("\n" + truncVisible("  "+stRed.Render(glyphOnAir+" ON AIR")+stDim.Render(" · ")+stKey.Render(m.share.Model())+stDim.Render(" · /share off"), w))
		} else {
			b.WriteString("\n" + m.onAirPanel(w))
		}
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

// quitConfirmView is the on-air quit-guard: a clear "you are ON AIR - quit and go
// off air?" prompt with the SAFE default on NO (keep sharing). Shown only while at
// least one model is live (requestQuit gates entry).
func (m model) quitConfirmView(w int) string {
	n := m.onAirCount()
	body := stRed.Render(glyphOnAir+" ON AIR") + stDim.Render(" - you are sharing ") +
		stKey.Render(fmt.Sprintf("%d model(s)", n)) + "\n\n" +
		"  You are ON AIR sharing " + stKey.Render(fmt.Sprintf("%d model(s)", n)) +
		stDim.Render(" - quit and go off air? ") + stEmber.Render("[y/N]") + "\n\n" +
		stDim.Render("  y quits + goes off air cleanly · n / esc keeps you on air")
	return "\n" + stPanel.Render(body) + "\n"
}

// confirmView is the connect-time cost confirmation (3.2): the deal + an explicit
// accept/deny with the SAFE default on DENY.
func (m model) confirmView(w int) string {
	q := m.q
	bd := q.b
	st := bd.cheapest
	var b strings.Builder

	// Section-tab heading, matching the SHARE / CHANNEL look so the connect-confirm
	// reads as part of the same designed system, not an older screen.
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("TUNE IN") +
		stDim.Render("   confirm the channel before it opens") + "\n\n")

	// A k9s-style aligned one-row table: the station you'd lock, padded under the
	// same column-header style the share table uses (reverse-video cursor row + carat).
	b.WriteString("  " + stDim.Render(fmt.Sprintf("  %-22s  %-12s  %-10s  %s", "BAND", "STATION", "SIGNAL", "FLAGS")) + "\n")
	b.WriteString("  " + selCarat(true) + rowSel(true,
		fmt.Sprintf("  %-22s  %-12s  %-10s  %s",
			pad(bd.model, 22), pad("@"+st.NodeID, 12), pad(tpsPlain(st.TPS, st.Online), 10), plainBandBadge(bd, m.limits, false)),
		w-4) + "\n\n")

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

// connectStep renders one line of the staged tune-in: a leading ◉ on-air glyph,
// the step label, and - once the step is reached - a trailing "ok". A step not yet
// reached is dim and shows the working "…"; the reached step glints the on-air red.
// state: 0 = pending, 1 = working (current), 2 = done.
func connectStep(state int, label, detail string) string {
	switch state {
	case 0: // pending - not yet revealed (dim, hollow)
		return "  " + stDim.Render(glyphOffAir+" "+label)
	case 1: // working - the live carrier glint + an animated ellipsis-feel "…"
		line := "  " + stRed.Render(glyphOnAir) + " " + stLive.Render(label)
		if detail != "" {
			line += stDim.Render(" · ") + stDim.Render(detail)
		}
		return line + stDim.Render(" …")
	default: // done
		line := "  " + stRed.Render(glyphOnAir) + " " + stDim.Render(label)
		if detail != "" {
			line += stDim.Render(" · ") + stDim.Render(detail)
		}
		return line + stDim.Render(" … ") + stLive.Render("ok")
	}
}

// connectingView renders the staged tune-in sequence (modeConnecting): the web's
// scan -> lock -> lineage handshake -> CHANNEL OPEN animation, finishing on the
// aligned BASE URL / API KEY / MODEL plate + "roger that." The steps reveal one at
// a time on the carrier beat (m.connectStage); under quiet the whole sequence is
// shown resolved at once. Each step uses the shared ◉ on-air glyph and the verified
// ◆; the only color is the one red glint on ◉ / ◆ and the selection.
func (m model) connectingView(w int) string {
	o := m.connected
	if o == nil {
		return ""
	}
	st := m.connectStage // 0..connectStageDone; a step at index i is "done" once stage>i
	stateOf := func(i int) int {
		switch {
		case st > i+1 || st >= connectStageDone:
			return 2 // done
		case st == i+1:
			return 2 // the step that just completed
		case st == i:
			return 1 // working (current)
		default:
			return 0 // pending
		}
	}
	narrow := m.narrow()
	var b strings.Builder
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("TUNE IN") +
		stDim.Render("   locking the channel") + "\n\n")

	// The lock detail (station · t/s · price) is the widest line; drop it to just the
	// callsign when narrow so the step still reads but never overflows.
	lockDetail := "@" + o.NodeID + " · " + tpsPlain(o.TPS, o.Online) + " · " + money(o.PriceOut) + " $/M"
	if narrow {
		lockDetail = "@" + o.NodeID
	}
	b.WriteString(connectStep(stateOf(0), "scanning stations", "") + "\n")
	b.WriteString(connectStep(stateOf(1), "locking strongest", lockDetail) + "\n")
	// The lineage-handshake step carries the verified ◆ + the signed triplet (the
	// triplet is dropped when narrow).
	hs := stateOf(2)
	triplet := " weights·shard·token"
	if narrow {
		triplet = ""
	}
	hsTriplet := stGold.Render(glyphVerify) + stDim.Render(triplet)
	switch hs {
	case 0:
		b.WriteString("  " + stDim.Render(glyphOffAir+" lineage handshake") + "\n")
	case 1:
		b.WriteString("  " + stRed.Render(glyphOnAir) + " " + stLive.Render("lineage handshake") + "  " + hsTriplet + stDim.Render(" …") + "\n")
	default:
		b.WriteString("  " + stRed.Render(glyphOnAir) + " " + stDim.Render("lineage handshake") + "  " + hsTriplet + stDim.Render(" … ") + stLive.Render("ok") + "\n")
	}
	// The terminal CHANNEL OPEN line: revealed once every prior step is done.
	if st >= connectStageDone {
		open := "  " + stRed.Render(glyphOnAir) + " " + stBrand.Render("CHANNEL OPEN") + "  " + stKey.Render(o.Model)
		if !narrow {
			open += stDim.Render(" via @") + stSelText.Render(o.NodeID) + "  " + stGold.Render(glyphVerify+" verified")
		}
		b.WriteString(open + "\n")
		// The clean endpoint plate + the drop-in line (a shorter line when narrow).
		b.WriteString("\n" + m.endpointBlock(w) + "\n")
		dropIn := "drop-in, OpenAI-compatible - point any OpenAI tool here. "
		if narrow {
			dropIn = "drop-in. "
		}
		b.WriteString("  " + stDim.Render(dropIn) + stLive.Render("roger that.") + "\n")
	} else {
		b.WriteString("  " + stDim.Render(glyphOffAir+" CHANNEL OPEN") + "\n")
	}
	return b.String()
}

// endpointBlock renders the clean, aligned BASE URL / API KEY / MODEL spec plate -
// dim mono labels, bright mono values, lined up like the web's endpoint plate. It
// is the shared surface used by both the staged tune-in finale and the persistent
// endpoint panel, so the binary shows the same "spec plate" the site does.
func (m model) endpointBlock(w int) string {
	model := "-"
	if m.connected != nil {
		model = m.connected.Model
	}
	// A small fixed-width label column so the values align in one mono gutter.
	row := func(label, value string) string {
		return "  " + stDim.Render(pad(label, 9)) + stKey.Render(value)
	}
	return row("BASE URL", m.endpoint) + "\n" +
		row("API KEY", m.apikey) + "\n" +
		row("MODEL", model)
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

// tpsCell renders a station's signal: the shared ◉ on-air glyph (the one red
// glint) + measured tok/s, or the hollow ○ off-air glyph, in mono. Same
// iconography the band table, share table, and channel header all use.
func tpsCell(tps float64, online bool) string {
	dot := stDim.Render(glyphOffAir)
	if online {
		dot = stRed.Render(glyphOnAir)
	}
	if tps > 0 {
		return dot + stLive.Render(fmt.Sprintf("  %.0f t/s", tps))
	}
	return dot + stDim.Render("  - t/s")
}

// tpsPlain is tpsCell without color (for a reverse-video selected row, where one
// accent style must govern the whole row). Same ◉/○ shared glyphs, no color.
func tpsPlain(tps float64, online bool) string {
	dot := glyphOffAir
	if online {
		dot = glyphOnAir
	}
	if tps > 0 {
		return fmt.Sprintf("%s %.0f t/s", dot, tps)
	}
	return dot + " - t/s"
}

// onAirPulse returns the breathing ON-AIR beacon in a FIXED-width cell so the
// header's right edge never jitters as the arcs grow/shrink. The eye is the one
// live-red on-air beacon (cRed: #E0231C light / #FF4438 dark) matching the web's
// --live carrier; the arcs are mono ink. Cadence is gated on a slow phase so it
// reads as a calm breath, not a flicker. eyeStyle lets callers pass the beacon
// style (the beacon and Ping's eye now share the same one red).
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

// inShareSection reports whether the current screen is part of the SHARE (provide)
// section vs the TUNE IN (consume) section. The header names the section so it is
// never ambiguous that RogerAI does both.
func (m model) inShareSection() bool {
	switch m.mode {
	case modeShare, modeShareEditor, modeShareSetup:
		return true
	}
	return false
}

// sectionName is the two-mode top-level indicator: TUNE IN (consume: browse /
// connect / chat) vs SHARE (provide: your models / earnings / on air).
func (m model) sectionName() string {
	if m.inShareSection() {
		return "SHARE"
	}
	return "TUNE IN"
}

// sectionBadge renders the section indicator with the inactive section shown dim
// beside it, so the header reads "TUNE IN | share" (or "tune in | SHARE") and the
// `s` toggle is self-evident. SHARE is ember (provide = money), TUNE IN is volt
// (consume). At narrow widths it collapses to just the ACTIVE section so it never
// overflows the (already stacked) header line.
func (m model) sectionBadge() string {
	if m.narrow() {
		if m.inShareSection() {
			return stEmber.Bold(true).Render("SHARE") + stDim.Render(" [s]")
		}
		return stSelText.Render("TUNE IN") + stDim.Render(" [s]")
	}
	tune, share := stDim.Render("tune in"), stDim.Render("share")
	if m.inShareSection() {
		share = stEmber.Bold(true).Render("SHARE")
	} else {
		tune = stSelText.Render("TUNE IN")
	}
	return tune + stDim.Render(" │ ") + share + stDim.Render(" ([s])")
}

// toggleSection flips between the TUNE IN and SHARE sections - the one obvious key
// (s) that makes "I can both consume and provide" unmistakable. Entering SHARE
// runs detection (opening the provider table or the guided fallback); leaving SHARE
// returns to the band browser. A live CHANNEL is left intact (tab back to it).
func (m model) toggleSection() (tea.Model, tea.Cmd) {
	if m.inShareSection() {
		m.mode = modeBrowse
		m.status = stDim.Render("TUNE IN - browse the band, enter to tune in")
		return m, nil
	}
	return m.doShare(nil)
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
	case modeConnecting:
		return "LOCKING"
	case modeOverLimit:
		return "OVER LIMIT"
	case modeLimits:
		return "LIMITS"
	case modeShare:
		return "PROVIDER TABLE"
	case modeShareEditor:
		return "PRICE + SCHEDULE"
	case modeShareSetup:
		return "SET UP A MODEL"
	default:
		return "BROWSE"
	}
}

// compactHeader is the windowshade-mode header: the whole brand lockup + preset bar
// collapses to ONE dense, animation-free strip carrying the live state + account +
// the `m:expand` hint, with a single hairline rule under it. No big banner, no arcs.
// The static `(•)` beacon stands in for the breathing pulse (frozen, per the
// reduced-motion contract). Width-safe: the strip is built as labeled segments and
// truncated to the real width before the rule, so it never overflows at 40 cols.
//
// Shapes (illustrative):
//
//	browsing: (•) ROGER·AI · TUNE IN · 3 on air · ◆ @bownux $42.17   m:expand
//	on air:   (•) ROGER·AI · ◆ on @nyx · gpt-oss-20b · $0.30/1M · $42.17   m:expand
func (m model) compactHeader(w int) string {
	dot := stRed.Render("(•)")
	brand := stBrand.Render("ROGER") + stTag.Render("·AI")
	sep := stDim.Render(" · ")
	hint := stDim.Render("m:expand")

	var mid string
	if m.connected != nil {
		// Channel context: the load-bearing "what am I on + price + balance".
		o := m.connected
		mid = stGold.Render(glyphVerify) + stLive.Render(" on ") + stSelText.Render("@"+o.NodeID) +
			sep + stKey.Render(o.Model) +
			sep + stEmber.Render(dollars(o.PriceOut)+"/1M")
	} else {
		// Browsing: the section + how many stations are on air.
		on := countOnline(m.offers)
		summary := "scanning…"
		if m.scanned {
			summary = fmt.Sprintf("%d on air", on)
		}
		section := "TUNE IN"
		if m.inShareSection() {
			section = "SHARE"
		}
		state := stKey.Render(section) + sep + stDim.Render(summary)
		if m.onAir {
			state = stRed.Render(glyphOnAir+" ON AIR") + sep + state
		}
		mid = state
	}

	// The account tag carries the wallet, the other load-bearing bit. The compact form
	// is terse - ◆ @login · $bal collapses to just $bal (or /login when anonymous) - so
	// the dense strip stays short and the m:expand hint never gets crowded out.
	acct := m.accountTag(true)
	if m.loggedInState() && m.ghLogin != "" {
		// Logged in: keep the callsign + balance (the identity is worth the few cols).
		acct = stGold.Render(glyphVerify) + stDim.Render(" @") + stSelText.Render(m.ghLogin)
		if m.haveBal {
			acct += stDim.Render(" ") + stEmber.Render(dollars(m.balance))
		}
	}

	left := dot + " " + brand + sep + mid + sep + acct
	// Right-align the hint when there's room; otherwise it trails inline. We measure on
	// the visible (ANSI-stripped) width so color never throws off the geometry.
	leftVis := lipgloss.Width(left)
	hintVis := lipgloss.Width(hint)
	rule := stHeadRule.Render(strings.Repeat("-", w))
	if leftVis+2+hintVis <= w {
		gap := w - leftVis - hintVis
		return left + strings.Repeat(" ", gap) + hint + "\n" + rule
	}
	// Too narrow for the gap: trim the left strip to fit "… m:expand" on one line so it
	// never overflows. truncVisible cuts on display width, ANSI-safe.
	budget := w - hintVis - 1
	if budget < 0 {
		budget = 0
	}
	return truncVisible(left, budget) + " " + hint + "\n" + rule
}

// truncVisible cuts s to at most n display columns, preserving ANSI styling and never
// splitting an escape sequence. It is the compact strip's width clamp (ansi.Truncate
// is display-width aware and ANSI-safe, so a colored segment is cut cleanly rather
// than leaking a half escape).
func truncVisible(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "")
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
			stDim.Render(" · ") + m.accountTag(true) +
			"  " + tintSignal(signalBarsRaw(m.frame, o.TPS, true), o.TPS, true)
		return bar + "\n" + rule
	}

	// EXPANDED: brand lockup + eye on the left; the SECTION + screen badge on the
	// right. The section (TUNE IN vs SHARE) is the load-bearing "which half of the app
	// am I in" indicator, always shown so it is never ambiguous that you can both
	// consume and provide; the screen mode is the secondary detail. When /share is
	// live, a single ON AIR mark leads the badge (the one on-air indicator).
	left := tower + name + "  " + eye
	// Narrow: just the section + ON AIR (the screen "mode X" detail is dropped so the
	// stacked badge line fits the real width). Wide: section + screen mode.
	badge := m.sectionBadge()
	if !m.narrow() {
		badge += stDim.Render("  ·  ") + stDim.Render("mode ") + stSelText.Render(m.modeName())
	}
	if m.onAir {
		badge = stRed.Render(glyphOnAir+" ON AIR") + stDim.Render("  ·  ") + badge
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
	// once connected AND back on the band (channel held, expanded, not minimized) it
	// names the channel. A connect-time sub-screen (confirm / the staged LOCKING
	// sequence) does NOT show this line - those views carry the channel context
	// themselves - so the header stays compact and width-safe through the tune-in.
	holdingChannel := m.connected != nil && (m.mode == modeBrowse || m.mode == modeCommand)
	var state string
	if holdingChannel {
		// Narrow: drop the "([m] compact)" hint so the line fits the real width.
		hint := stDim.Render("  ([m] compact)")
		if m.narrow() {
			hint = ""
		}
		state = stGold.Render("  "+glyphVerify+" ") + stLive.Render("on channel ") + stSelText.Render(m.connected.NodeID) +
			stDim.Render(" · ") + stKey.Render(m.connected.Model) +
			stDim.Render(" · ") + m.accountTag(true) + hint
	} else {
		on := countOnline(m.offers)
		summary := "scanning the band…"
		if m.scanned {
			summary = fmt.Sprintf("%d on air", on)
		}
		// The beacon in the lockup above already carries the (( • )) motif, so the
		// state line drops its literal ((•)) prefix - exactly one on-air mark in the
		// header (TUI-V2-CRITIQUE C). The account lockup carries login state + balance;
		// the balance only appears when logged in.
		state = stDim.Render("  ") + stDim.Render(summary) +
			stDim.Render(" · ") + m.accountTag(m.narrow())
	}
	return top + "\n" + state + "\n" + rule
}

// bandOnAir reports whether the latest scan shows any online station for model.
// It also counts the user's own in-process /share when it serves that model, so a
// solo founder sharing + chatting their own node is never told "no station" on a
// stale scan (the share registered but a fresh /discover hasn't come back yet).
// connectedModel returns the model id of the currently-open channel, or "" when
// not connected. Used to MARK the connected band in the browse list (the lit
// "◉ connected" row) and to drive the from-the-list disconnect shortcut.
func (m model) connectedModel() string {
	if m.connected == nil {
		return ""
	}
	return m.connected.Model
}

// selectedBand resolves the cursor against the FILTERED + SORTED view (the same
// list the browse window renders + navigates), returning the band under the cursor.
// Every band action (connect, cursorOnConnected) goes through this so the cursor
// never desyncs from what the user sees when a filter / sort is applied. ok is
// false when the visible list is empty.
func (m model) selectedBand() (band, bool) {
	vis := m.visibleBands()
	if len(vis) == 0 {
		return band{}, false
	}
	i := m.cursor
	if i < 0 {
		i = 0
	}
	if i >= len(vis) {
		i = len(vis) - 1
	}
	return vis[i], true
}

// clampBrowse keeps m.cursor + m.browseTop valid against the current FILTERED view.
// Called after anything that can change the visible-set size (a re-scan, a filter
// edit, a toggle, a sort) so the cursor never points past the list and the window
// never strands rows. Pointer receiver: it mutates the model in place.
func (m *model) clampBrowse() {
	n := len(m.visibleBands())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.browseTop > m.cursor {
		m.browseTop = m.cursor
	}
	if m.browseTop < 0 {
		m.browseTop = 0
	}
}

// scrollBrowse clamps the cursor and then scrolls the virtualized window so the
// cursor stays visible (used on every up/down nav). It persists browseTop so the
// remembered scroll position survives between frames; browseView recomputes the
// same window each render, so the view stays correct even without this, but
// storing it keeps the "remembered top" honest when the cursor jumps via a re-scan.
func (m *model) scrollBrowse() {
	m.clampBrowse()
	rows := m.browseRows()
	m.browseTop, _ = windowFor(m.browseTop, m.cursor, rows, len(m.visibleBands()))
}

// cursorOnConnected reports whether the browse cursor is on the band we are
// currently connected to (used so Enter toggles into the open channel rather than
// re-running the connect flow).
func (m model) cursorOnConnected() bool {
	cm := m.connectedModel()
	if cm == "" {
		return false
	}
	bd, ok := m.selectedBand()
	return ok && bd.model == cm
}

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

// sigFrame is the frame the view feeds every animation function (the signal-bar
// shimmer, the beacon pulse, Ping, the working spinner). In compact ("windowshade")
// mode it returns a fixed frozen frame so motion settles to a static snapshot - the
// app's own prefers-reduced-motion. Otherwise it is the live carrier beat (m.frame).
func (m model) sigFrame() int {
	if m.compact {
		return frozenFrame
	}
	return m.frame
}

// balDollars renders the wallet balance in dollars, or "-" before it loads.
func (m model) balDollars() string {
	if !m.haveBal {
		return "-"
	}
	return dollars(m.balance)
}

// loggedInState reports whether the user has a real account wallet: the broker's
// logged_in flag, or (before the first balance comes back) a locally-linked login.
func (m model) loggedInState() bool { return m.loggedIn || m.ghLogin != "" }

// accountTag renders the header/footer account lockup: logged in shows
// "◆ @login · $balance"; anonymous shows a calm, steady "not logged in · /login to
// use your wallet" prompt (no balance number is ever shown when anonymous). When
// `compact` is set it drops to a terser form for the thin bar / narrow widths.
func (m model) accountTag(compact bool) string {
	if !m.loggedInState() {
		if compact {
			return stKey.Render("/login")
		}
		return stDim.Render("not logged in · ") + stKey.Render("/login") + stDim.Render(" to use your wallet")
	}
	// Compact (thin bar / narrow footer): just the balance ($), the load-bearing bit.
	if compact {
		if !m.haveBal {
			return stGold.Render("◆")
		}
		return stEmber.Render(dollars(m.balance))
	}
	who := stGold.Render("◆") + stDim.Render(" logged in")
	if m.ghLogin != "" {
		who = stGold.Render("◆") + stDim.Render(" @") + stSelText.Render(m.ghLogin)
	}
	if !m.haveBal {
		return who
	}
	return who + stDim.Render(" · ") + stEmber.Render(dollars(m.balance))
}

// Band sort cycle - mirrors the /bands web page's sort <select> so the CLI and
// the web read the same dial (strongest signal / cheapest / fastest / most
// stations). sortSignal is the default (live-first, then strongest signal).
const (
	sortSignal   = iota // strongest signal (live first, then signal desc) - the default
	sortCheapest        // cheapest $/1M out (ascending)
	sortFastest         // fastest measured tok/s (descending)
	sortStations        // most stations on air (descending)
	sortCount           // number of sort modes (for the S cycle)
)

// sortLabel is the short word shown in the footer / filter line for a sort mode.
func sortLabel(mode int) string {
	switch mode {
	case sortCheapest:
		return "cheapest"
	case sortFastest:
		return "fastest"
	case sortStations:
		return "most-stations"
	default:
		return "strongest"
	}
}

// bandSignal is the same 0-ish proxy the signal tower uses (cheapest station's
// measured tok/s), so the "strongest signal" sort orders by what the meter shows.
func bandSignal(b band) float64 {
	if b.cheapest != nil {
		return b.cheapest.TPS
	}
	return 0
}

// visibleBands is the DERIVED browse list: m.bands run through the active name
// filter + quick toggles (free-now / confidential / on-air) and the sort cycle.
// The cursor + the virtualized window both index THIS slice, never the raw
// m.bands, so filtering and scaling never desync from navigation. It mirrors the
// /bands web page's applyFilters (same predicates + sort keys) so CLI and web
// match. Cheap to recompute each frame (a filter + a stable sort over the grouped
// bands, not the raw offers); at thousands of bands this is the only full pass and
// it is O(n log n) once, while RENDER stays O(window).
func (m model) visibleBands() []band {
	q := strings.ToLower(strings.TrimSpace(m.filterApplied))
	out := make([]band, 0, len(m.bands))
	for _, b := range m.bands {
		if q != "" && !strings.Contains(strings.ToLower(b.model), q) {
			continue
		}
		if m.fFree && !b.free {
			continue
		}
		if m.fConf && b.lineage == 0 { // confidential == lineage in /discover
			continue
		}
		if m.fOn && !b.online {
			continue
		}
		out = append(out, b)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch m.sortMode {
		case sortCheapest:
			// offline bands (no live price) sort last; then cheapest out-price first.
			if a.online != b.online {
				return a.online
			}
			return a.minOut < b.minOut
		case sortFastest:
			return bandSignal(a) > bandSignal(b)
		case sortStations:
			return a.stations > b.stations
		default: // sortSignal: live first, then strongest signal
			if a.online != b.online {
				return a.online
			}
			return bandSignal(a) > bandSignal(b)
		}
	})
	return out
}

// filtersActive reports whether any name filter or quick toggle is narrowing the
// list (used to show the "filter: ... (n/total)" line + the clear hint).
func (m model) filtersActive() bool {
	return strings.TrimSpace(m.filterApplied) != "" || m.fFree || m.fConf || m.fOn
}

// browseRows is how many band rows the virtualized window may draw at the current
// terminal height. It reserves the fixed chrome (preset bar, header, section tab +
// column header, prompt, footer, any endpoint/on-air panel) so the window scrolls
// instead of pushing the footer off-screen on a short terminal. Floored so a tiny
// terminal still shows a few rows + the position indicator.
func (m model) browseRows() int {
	h := m.height
	if h <= 0 {
		h = 30 // unsized first frame: a sensible default window
	}
	// Fixed chrome above/below the list: preset bar (~2) + header (~1) + section tab
	// (1) + column header (1) + filter line when open (1) + prompt (1) + footer
	// (2-3) + the two "more" hint lines + the position line. Compact trims the header.
	chrome := 12
	if m.compact {
		chrome = 9
	}
	if m.filterMode || m.filtersActive() {
		chrome++
	}
	if m.connected != nil {
		chrome += 4 // the endpoint panel rides under the list
	}
	if m.onAir && m.share != nil {
		chrome += 4 // the ON AIR panel too
	}
	rows := h - chrome
	if rows < 3 {
		rows = 3
	}
	return rows
}

// windowFor computes the virtualized slice [top, end) over a list of length n,
// given the cursor and how many rows fit. It scrolls the window so the cursor is
// always visible (clamped at both edges), starting from the caller's current top.
// Returns the new top and the exclusive end. Correct with the cursor at 0, at n-1,
// with a window larger than the list (whole list, no scroll), and with n == 0.
func windowFor(top, cursor, rows, n int) (int, int) {
	if rows < 1 {
		rows = 1
	}
	if n <= rows {
		return 0, n // everything fits: no scroll
	}
	if cursor < top {
		top = cursor // scrolled above the window: pull the top up to the cursor
	}
	if cursor >= top+rows {
		top = cursor - rows + 1 // below the window: pull the top down
	}
	if top > n-rows {
		top = n - rows // never leave a blank tail
	}
	if top < 0 {
		top = 0
	}
	return top, top + rows
}

func (m model) browseView(w int) string {
	if len(m.bands) == 0 {
		// ASYNC LOADING: the initial /discover (and any r re-scan) runs off the Bubble
		// Tea event loop, so until the first offers land we show the SAME ((•)) scanning
		// indicator the SHARE provider table uses - a clear "scanning the band…" pose, not
		// a frozen empty list. loadedOnce flips true on the first offersMsg; scanned tracks
		// every scan so a manual r re-scan (which resets scanned) shows it again too.
		loading := !m.scanned && !m.scanErr
		// COMPACT: no Ping art (it animates and eats rows) - a single static status
		// line in the calm windowshade voice.
		if m.compact {
			switch {
			case m.scanErr:
				return "  " + stEmber.Render("(○) ...static") + stDim.Render(" - broker off air · r to retune") + "\n"
			case loading:
				return "  " + m.transmitLineFor(0) + stDim.Render("  scanning the band…") + "\n"
			default:
				return "  " + stDim.Render("(•) no stations on air right now · r to re-scan") + "\n"
			}
		}
		// Three empty cases, all filled with Ping in the dead space (never over
		// real content): the broker dropped -> Ping "...static"; still scanning
		// (no fetch back yet) -> the ((•)) scanning indicator (mirrors SHARE); scanned
		// but quiet -> Ping idle with a rotating "scanning the band" hint so the empty
		// band feels like a DJ working the dial, not dead space.
		switch {
		case m.scanErr:
			return "\n" + pingPose(pingStatic, m.frame, w, "…static. the broker went off air - press r to retune") + "\n"
		case loading:
			return "\n  " + m.transmitLineFor(0) + "\n  " + stDim.Render("scanning the band…") + "\n"
		default:
			return "\n" + pingPose(pingIdle, m.frame, w, idleHint(m.frame)) + "\n"
		}
	}
	var b strings.Builder
	// SCALE: render the FILTERED + SORTED view, not raw m.bands, and only the visible
	// window of it (virtualized). vis is the derived list the cursor + window index.
	vis := m.visibleBands()
	total := len(m.bands)
	matched := len(vis)
	// Section heading, manual-style: a thin tab + a count, like the web's §-markers.
	// COMPACT drops the prose count to a terse "N" and (below) the column-header row,
	// so more bands fit per screen - the windowshade density. The sort label rides in
	// the heading so the active dial (strongest / cheapest / fastest / most-stations)
	// is always visible (S cycles it; mirrors the /bands web page).
	sortTag := stDim.Render(" · sort " + sortLabel(m.sortMode))
	if m.narrow() {
		// Narrow: drop the sort tag from the heading (it would overflow the slim width);
		// the footer still teaches S, and the filter line carries the active state.
		sortTag = ""
	}
	if m.compact {
		b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("BAND") +
			stDim.Render(fmt.Sprintf("  %d", matched)) + sortTag + "\n")
	} else {
		b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("THE BAND") +
			stDim.Render(fmt.Sprintf("   %d models on air", total)) + sortTag + "\n")
	}
	// FILTER line: shown while the live filter input is open (f) OR when a filter /
	// toggle is applied. It carries the active name filter, the quick toggles, and the
	// match count (e.g. "filter: qwen  (3/240)") so it's always clear what is narrowing
	// the list. esc clears + closes, enter keeps it applied and returns to the list.
	if m.filterMode || m.filtersActive() {
		b.WriteString(m.filterLine(matched, total) + "\n")
	}
	// No band matches the active filter / toggles: a clear note (not a blank list),
	// with the keys to widen back out. Mirrors the /bands web page's empty state.
	if matched == 0 {
		return b.String() + "  " + stEmber.Render("no bands match") +
			stDim.Render(" - esc clears the filter, S re-sorts, the toggles widen it") + "\n"
	}
	// Narrow (< 64 col): a slim three-column table (band · on air · price), dropping
	// the signal + flags columns so nothing overflows the real width. Wide: the full
	// fixed grid (band · on air · range · signal · flags). (TUI-V2-CRITIQUE A.)
	nameW := 20
	if m.narrow() {
		nameW = 14
		if !m.compact {
			b.WriteString("  " + stDim.Render(fmt.Sprintf("%-14s  %-9s  %s", "band", "on air", "$/1M out")) + "\n")
		}
	} else {
		// Column header, tabular. Widths match the body cells exactly so price + signal
		// columns line up under a fixed grid (lipgloss width, not eyeballed spacing).
		// COMPACT omits the header row entirely (denser; the cells stay self-evident).
		if !m.compact {
			b.WriteString("  " + stDim.Render(fmt.Sprintf("%-20s  %-9s  %-17s  %-8s  %s",
				"band", "on air", "$/1M out (range)", "signal", "flags")) + "\n")
		}
	}
	// Table width for the k9s reverse-video selection bar (spans the whole row).
	tableW := w - 2
	if tableW < 20 {
		tableW = 20
	}
	connModel := m.connectedModel()
	// VIRTUALIZE: render only the window of rows that fit the terminal height. The
	// cursor is clamped into vis, the window scrolls to keep it in view, and a
	// position indicator (e.g. "12-24 of 340") + top/bottom "more" hints orient the
	// user. We deliberately iterate ONLY [top:end), never the whole list, so the
	// frame cost is O(window) at thousands of bands. browseTop is recomputed each
	// frame from the (already-clamped) cursor, so it stays correct at both edges,
	// with a filter applied (window over the filtered set), and for the sticky band.
	cur := m.cursor
	if cur >= matched {
		cur = matched - 1
	}
	if cur < 0 {
		cur = 0
	}
	rows := m.browseRows()
	top, end := windowFor(m.browseTop, cur, rows, matched)
	// Top "more" hint: rows scrolled off above.
	if top > 0 {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("↑ %d more above", top)) + "\n")
	}
	for i := top; i < end; i++ {
		bd := vis[i]
		sel := i == cur
		connected := connModel != "" && bd.model == connModel
		stationsLbl := "-"
		if bd.online {
			stationsLbl = fmt.Sprintf("%d on", bd.stations)
		}
		// The band you are on the channel with reads "connected" in the on-air column
		// (a lit row), so the open channel's station is obvious at a glance even when
		// its node has briefly aged out of /discover (the sticky offline band).
		if connected {
			stationsLbl = "connected"
		}
		if m.narrow() {
			free := ""
			if bd.free {
				free = "  FREE"
			}
			// PLAIN row for the reverse-video bar; the selected row is one accent bar.
			plain := fmt.Sprintf("%s  %s  %s%s", pad(bd.model, nameW), pad(stationsLbl, 9), rangeStr(bd), free)
			if connected {
				plain = glyphOnAir + " " + fmt.Sprintf("%s  %s  %s", pad(bd.model, nameW-2), pad(stationsLbl, 9), rangeStr(bd))
			}
			if sel {
				b.WriteString(selCarat(true) + " " + rowSel(true, plain, tableW) + "\n")
				continue
			}
			// Unselected: dim band, tinted price + FREE tag. A connected row leads with the
			// lit ◉ marker and a red "connected" label so it stands out in the list.
			if connected {
				b.WriteString(selCarat(false) + " " + stRed.Render(glyphOnAir) + " " + stKey.Render(pad(bd.model, nameW-2)) + "  " +
					stRed.Render(pad(stationsLbl, 9)) + "  " + stEmber.Render(rangeStr(bd)) + "\n")
				continue
			}
			freeTag := ""
			if bd.free {
				freeTag = "  " + stLive.Render("FREE")
			}
			b.WriteString(selCarat(false) + " " + stDim.Render(pad(bd.model, nameW)) + "  " +
				stDim.Render(pad(stationsLbl, 9)) + "  " + stEmber.Render(rangeStr(bd)) + freeTag + "\n")
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
			rawSig := pad(signalBarsRaw(m.sigFrame(), sigTPS, online, bd.stations), 8)
			plain := fmt.Sprintf("%s  %s  %s  %s  %s",
				pad(bd.model, nameW), pad(stationsLbl, 9), pad(rangeStr(bd), 17), rawSig, plainBandBadge(bd, m.limits, connected))
			b.WriteString(selCarat(true) + " " + rowSel(true, plain, tableW) + "\n")
			continue
		}
		rng := stEmber.Render(pad(rangeStr(bd), 17))
		sig := tintSignal(pad(signalBarsRaw(m.sigFrame(), sigTPS, online, bd.stations), 8), sigTPS, online)
		nameCell := stDim.Render(pad(bd.model, nameW))
		statCell := stDim.Render(pad(stationsLbl, 9))
		if connected {
			// The connected band's name + on-air cell light up so the open channel is
			// obvious in the list (the "◉ connected" badge is in the flags cell too).
			nameCell = stKey.Render(pad(bd.model, nameW))
			statCell = stRed.Render(pad(stationsLbl, 9))
		}
		b.WriteString(selCarat(false) + " " + nameCell + "  " +
			statCell + "  " + rng + "  " + sig + "  " + bandBadge(bd, m.limits, connected) + "\n")
	}
	// Bottom "more" hint: rows scrolled off below.
	if end < matched {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("↓ %d more below", matched-end)) + "\n")
	}
	// Position indicator: which slice of the (filtered) list is on screen, e.g.
	// "12-24 of 340". Only shown when the list does not all fit (windowing is live),
	// so a short list stays uncluttered.
	if matched > rows {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("%d-%d of %d", top+1, end, matched)) + "\n")
	}
	return b.String()
}

// filterLine renders the active filter strip under the band heading: the live
// name-filter input (while open), the applied substring + match count (e.g.
// "filter: qwen  (3/240)"), and the lit quick toggles (free / conf / on-air). It
// is the band browser's mirror of the /bands web tuner chips so the CLI + web
// narrow the same way. matched/total drive the "(n/total)" count.
func (m model) filterLine(matched, total int) string {
	var parts []string
	if m.filterMode {
		// The live input: typing filters as you go. The label + the textinput View()
		// (cursor + echoed text) so it is obvious WHERE the filter text lands.
		parts = append(parts, stKey.Render("filter ▸ ")+m.filterIn.View())
	} else if q := strings.TrimSpace(m.filterApplied); q != "" {
		parts = append(parts, stDim.Render("filter: ")+stKey.Render(q))
	}
	// Lit quick toggles (only the on ones, to stay tight).
	var toggles []string
	if m.fFree {
		toggles = append(toggles, stLive.Render("free-now"))
	}
	if m.fConf {
		toggles = append(toggles, stGold.Render("conf"))
	}
	if m.fOn {
		toggles = append(toggles, stRed.Render("on-air"))
	}
	if len(toggles) > 0 {
		parts = append(parts, stDim.Render("["+strings.Join(toggles, " ")+"]"))
	}
	// The match count, always, so it is clear how much the filter narrowed the list.
	parts = append(parts, stDim.Render(fmt.Sprintf("(%d/%d)", matched, total)))
	return "  " + strings.Join(parts, "  ")
}

// plainBandBadge is bandBadge without color, for the reverse-video selected row
// (one accent style governs the whole row; an embedded fg color reads as noise).
// connected leads the cell with the "◉ connected" marker so the open channel's
// band is unmistakable even on the cursor row / under NO_COLOR.
func plainBandBadge(bd band, limits *LimitStore, connected bool) string {
	parts := []string{}
	if connected {
		parts = append(parts, glyphOnAir+" connected")
	}
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

// bandBadge renders the right-hand flag cell: a lit "◉ connected" marker for the
// open channel's band, the gold ◆ lineage call-sign (with the verified hop count),
// a live FREE tag, and the ember above-limit warning.
func bandBadge(bd band, limits *LimitStore, connected bool) string {
	parts := []string{}
	if connected {
		parts = append(parts, stRed.Render(glyphOnAir+" connected"))
	}
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

// mergeStickyBand keeps a band you recently TUNED IN to in the browse list even
// when the broker's latest /discover no longer carries it (the founder's
// vanishing-band bug: a node you were on ages out of /discover at ~35s, so the
// next periodic re-scan dropped it from m.bands and r could not bring it back).
// If m.lastConnected is set and the fresh band list already contains that model,
// the live offer wins and the sticky placeholder is cleared (it is on air again).
// Otherwise we append a synthetic OFFLINE band carrying the remembered station, so
// the row stays present, marked offline/available, and is still selectable to
// re-tune. nil-safe: with no sticky band the input list passes through unchanged.
func (m *model) mergeStickyBand(bands []band) []band {
	if m.lastConnected == nil {
		return bands
	}
	want := m.lastConnected.Model
	for _, b := range bands {
		if b.model == want {
			// The band is back in /discover (on air or listed) - the live offer is the
			// source of truth now; drop the stale sticky placeholder.
			m.lastConnected = nil
			return bands
		}
	}
	// Not in the fresh scan: keep it as an offline, tunable station so it never
	// vanishes. minOut/cheapest from the remembered offer let Enter re-tune it.
	o := *m.lastConnected
	sticky := band{
		model:    o.Model,
		stations: 0,
		minOut:   o.PriceOut,
		maxOut:   o.PriceOut,
		cheapest: nil, // offline: no on-air station to lock right now
		online:   false,
		free:     o.FreeNow || (o.PriceOut == 0 && o.PriceIn == 0),
		all:      []offer{o},
	}
	if o.Confidential {
		sticky.lineage = 1
	}
	return append(bands, sticky)
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
	// Section-tab heading, matching the SHARE table's "▌ SECTION  context" look so
	// the channel reads as part of the same designed system. COMPACT collapses it to a
	// single terse status (callsign · model · cost), dropping the prose label.
	if m.compact {
		head := "  " + stSelBar.Render("▌") + " " + stBrand.Render("CHAN") +
			stDim.Render("  ") + stGold.Render(glyphVerify) + stDim.Render(" "+m.connected.NodeID+" · ") + stKey.Render(m.connected.Model) +
			stDim.Render(" · ") + stEmber.Render(dollars(m.sessCost)) + sys
		b.WriteString(truncVisible(head, w) + "\n")
	} else {
		b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("CHANNEL") +
			stDim.Render("   ") + stGold.Render(glyphVerify) + stDim.Render(" "+m.connected.NodeID+" · ") + stKey.Render(m.connected.Model) +
			stDim.Render("   cost ") + stEmber.Render(dollars(m.sessCost)) + sys + "\n")
	}
	// Scrollable transcript: keep the tail that fits the pane (you ▸ / them ◂). COMPACT
	// reclaims two rows (no preset bar / spacer above) so more of the transcript shows.
	lines := m.transcript
	max := m.height - 8
	if m.compact {
		max = m.height - 6
	}
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
		// COMPACT freezes the ((•)) working spinner to a static (•) glyph + phrase (no
		// ring animation), per the reduced-motion contract.
		b.WriteString("  " + m.transmitLineFor(elapsed) + "\n")
	}
	// The always-live channel prompt: `you ›` + the textinput View() (cursor +
	// echoed text), updated every keystroke. Same live-echo contract as promptLine.
	b.WriteString("\n  " + stPrompt.Render("you › ") + m.chatIn.View() + "\n")
	// COMPACT omits this verbose in-view key line - the terse compactFooter teaches the
	// keys (esc disconnect · tab peek), keeping the channel dense and width-safe.
	if !m.compact {
		b.WriteString("  " + stDim.Render("enter sends  ·  ") + stKey.Render("esc") + stDim.Render(" disconnects (leave this channel)  ·  ") + stKey.Render("tab") + stDim.Render(" peek at the band  ·  /help") + "\n")
	}
	return b.String()
}

// idleHints rotate in the empty-band ("no stations on air") view so the dead space
// reads as a DJ scanning the band, not a blank screen. They cycle the two-way-radio
// affordances (tune in / go on air / config) the preset bar also exposes.
var idleHints = []string{
	"No stations on air right now…",
	"Press [2] to go on air and share your GPU",
	"Press [1] to tune in",
	"Press [3] for config",
}

// idleHint returns the empty-band hint for a frame, advancing every ~4.5s (28
// frames at the 160ms tick) so each line reads before the next. quiet (NO_COLOR /
// non-TTY) freezes to the first hint so a pipe sees one stable line.
func idleHint(frame int) string {
	if quiet {
		return idleHints[0]
	}
	return idleHints[(frame/28)%len(idleHints)]
}

// workingPhrases is the rotating radio voice of the working spinner - one coherent
// DJ persona (the same one the future dj.md will use). While a request is in flight
// the beacon pulses and the phrase advances, so the wait reads as a live broadcast
// being tuned, not a frozen hang.
var workingPhrases = []string{
	"Tuning in…",
	"Modulating…",
	"Carrier locked…",
	"Working the dial…",
	"Receiving…",
	"Squelch open…",
	"Riding the airwaves…",
}

// workingPhrase returns the radio phrase for a frame: it advances roughly every
// ~1.3s (8 frames at the 160ms tick) so the words read, not flicker. Under quiet
// (NO_COLOR / non-TTY) it freezes to the first phrase so a pipe sees a stable line.
func workingPhrase(frame int) string {
	if quiet {
		return workingPhrases[0]
	}
	return workingPhrases[(frame/8)%len(workingPhrases)]
}

// workingSpinner is our answer to Claude Code's ✻ working spinner, in RogerAI's own
// radio idiom: the animated on-air beacon ((•)) (pulsing carrier rings, via
// pulseWith) next to a rotating radio phrase. It is the one coherent "we're on it"
// motif for any in-flight request/turn. quiet freezes both the rings and the phrase.
func workingSpinner(frame int) string {
	return pulseWith(frame, stPingEye) + " " + stLive.Render(workingPhrase(frame))
}

// staticSpinner is the compact ("windowshade") working spinner: a frozen (•) glyph
// (no pulsing carrier rings) next to a fixed phrase, so an in-flight request reads as
// "we're on it" without any motion - the reduced-motion form of workingSpinner.
func staticSpinner() string {
	return stPingEye.Render("(•)") + " " + stLive.Render(workingPhrases[0])
}

// transmitLineFor is transmitLine but honors compact: a static spinner under compact
// (no ring animation), the live animated one otherwise. The elapsed-seconds readout
// is kept in both so a slow station still reads as alive, not hung.
func (m model) transmitLineFor(elapsedSec int) string {
	if m.compact {
		line := staticSpinner()
		if elapsedSec >= 2 {
			line += stDim.Render(fmt.Sprintf("  %ds (holding the channel)", elapsedSec))
		}
		return line
	}
	return transmitLine(m.frame, elapsedSec)
}

// transmitLine is Ping's inline relay indicator: the working spinner (on-air beacon
// + rotating radio phrase) plus an elapsed-seconds readout once a reply is slow.
// Single line, so it never obstructs the chat transcript. The elapsed counter
// reassures on slow inference (CPU MoE replies can take a minute) that the request
// is alive, not hung.
func transmitLine(frame, elapsedSec int) string {
	line := workingSpinner(frame)
	if elapsedSec >= 2 {
		line += stDim.Render(fmt.Sprintf("  %ds  (slow stations can take a minute - holding the channel)", elapsedSec))
	}
	return line
}

// endpointPanel is the persistent channel-open plate shown under the browse view
// while a channel is held: the ◉ on-air glyph + (when confidential) the verified
// ◆, then the shared aligned BASE URL / API KEY / MODEL block + the drop-in line.
// It is the same spec plate the staged tune-in finishes on (endpointBlock), inside
// a flat single-hairline border (no heavy/double box).
func (m model) endpointPanel(w int) string {
	lineage := stDim.Render("·")
	if m.connected != nil && m.connected.Confidential {
		lineage = stGold.Render(glyphVerify + " verified")
	}
	head := stRed.Render(glyphOnAir+" ") + stLive.Render("channel open") + "  " +
		stDim.Render("point your bots here") + "  " + lineage
	body := head + "\n" +
		m.endpointBlock(w) + "\n" +
		stDim.Render("  drop-in, OpenAI-compatible - point any OpenAI tool here. ") + stLive.Render("roger that.") + stDim.Render("  ·  /chat to test")
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
	head := stRed.Render(glyphOnAir+" ON AIR") + "  " + stDim.Render("you are sharing") + "  " + stKey.Render(s.Model())
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

// sharePrice returns the price a row WOULD share at (its saved/edited price, FREE
// by default), or the live session's price when it's on air.
func (m model) sharePrice(row shareRow, live *agent.Session) (in, out float64) {
	if live != nil {
		return live.Price()
	}
	p := m.pricingFor(row.model)
	return p.In, p.Out
}

// hasSchedule reports whether a row has a time-of-use schedule set (so the table
// can flag it), live session schedules are not surfaced per-window here.
func (m model) hasSchedule(row shareRow) bool {
	return len(m.pricingFor(row.model).Windows) > 0
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
	// dense drops the metrics columns (SERVED/OUT TOK/EARNINGS): the full grid is
	// ~88 cols, so anything narrower uses the 3-column model·status·price layout to
	// stay width-safe (the band grid uses the same idea at its own threshold). The
	// windowshade compact mode forces the dense layout regardless of width.
	dense := w < 88 || m.compact
	head := stSelBar.Render("▌") + " " + stBrand.Render("SHARE")
	if dense {
		b.WriteString("  " + head + stDim.Render(fmt.Sprintf("   %d on air / %d", m.sharesOnAir(), len(m.shareRows))) + "\n")
	} else {
		b.WriteString("  " + head +
			stDim.Render(fmt.Sprintf("   your GPU as a station   %s detected · %d on air",
				plural(len(m.shareRows), "model"), m.sharesOnAir())) + "\n")
	}

	// LOADING: detection runs off the event loop, so while it's in flight we show a
	// clear indicator instead of a frozen UI. The ((•)) working spinner pulses with the
	// tick; quiet (NO_COLOR / non-TTY) and compact (windowshade) both freeze it to a
	// static (•) glyph + phrase via transmitLineFor.
	if m.shareLoading {
		spin := m.transmitLineFor(0)
		return b.String() + "\n  " + spin + "\n  " +
			stDim.Render("scanning the band for local models…") + "\n"
	}

	if len(m.shareRows) == 0 {
		return b.String() + "\n  " + stEmber.Render("no local models detected") +
			stDim.Render(" - start a local LLM and press r to re-detect") + "\n"
	}

	// Column geometry. dense drops the metrics columns so nothing overflows.
	nameW := 24
	if dense {
		nameW = 14
	}
	// Header (k9s-style ALL-CAPS column labels). Windowshade compact omits the header
	// row entirely for density (the cells stay self-evident).
	switch {
	case m.compact:
		// no column-header row
	case dense:
		b.WriteString("  " + stDim.Render(fmt.Sprintf("  %-14s  %-8s  %s", "MODEL", "STATUS", "PRICE")) + "\n")
	default:
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
		// A time-of-use schedule is flagged with a clock so the table shows it at a
		// glance (the per-window detail lives in the editor).
		if !on && m.hasSchedule(row) {
			priceTxt += " ~tou"
		}

		// Build the row body as PLAIN text first (cells padded), then color it: a
		// selected row is one reverse-video bar; an unselected row tints the status
		// + price cells. This keeps the k9s "the cursor row is obvious" contract.
		var plain string
		if dense {
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
			st = stRed.Render(pad(glyphOnAir+" "+statusTxt, 9))
		}
		if dense {
			stN := stDim.Render(pad(statusTxt, 8))
			if on {
				stN = stRed.Render(pad(glyphOnAir+statusTxt, 8))
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

	// Pricing affordance: logged in -> the per-model editor; anonymous -> the clear
	// "log in to earn" gate (free sharing still works without an account).
	if dense {
		ph := stKey.Render("p") + stDim.Render(" price")
		if !m.loggedInState() {
			ph = stDim.Render("log in to earn")
		}
		b.WriteString("\n  " + stDim.Render("free · ") + stKey.Render("⏎") + stDim.Render("/") + stKey.Render("a") + stDim.Render(" toggle · ") + ph + "\n")
	} else {
		ph := stKey.Render("p") + stDim.Render(" set price + schedule")
		if !m.loggedInState() {
			ph = stDim.Render("log in to earn (") + stKey.Render("/login") + stDim.Render(")")
		}
		b.WriteString("\n  " + stDim.Render("free by default · ") +
			stKey.Render("enter") + stDim.Render("/") + stKey.Render("a") + stDim.Render(" toggles on/off air · ") + ph + "\n")
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

// shareEditorView is the per-model price + time-of-use schedule editor (the
// ChargePoint-style earning surface), reached with `p` from the provider table and
// login-gated (enterShareEditor flashes the /login prompt for anonymous users, so
// this view only renders for a logged-in owner). It carries the same designed
// look as the share table: a section tab heading, a focused-field cursor, and a
// contextual key footer.
func (m model) shareEditorView(w int) string {
	var b strings.Builder
	narrow := m.narrow()
	headTail := stDim.Render("   what you earn per 1M tokens")
	if narrow {
		headTail = ""
	}
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("PRICE + SCHEDULE") +
		stDim.Render("   ") + stKey.Render(m.edModel) + headTail + "\n\n")

	field := func(idx int, label, val, unit string) string {
		cur := "  "
		nameSt := stDim
		valSt := stEmber
		if m.edField == idx {
			cur = stSelText.Render("▌ ")
			nameSt = stSelText
		}
		shown := val
		if shown == "" {
			shown = "0"
		}
		box := "▏" + shown + "▏"
		if m.edField == idx {
			box = stSelText.Render("▏" + shown + "▏")
		} else {
			box = valSt.Render(box)
		}
		tail := stDim.Render("  " + unit)
		if narrow {
			tail = ""
		}
		return cur + nameSt.Render(pad(label, 16)) + box + tail + "\n"
	}
	b.WriteString(field(edFieldIn, "$/1M input", m.edPriceIn, "$ per 1,000,000 input tokens"))
	b.WriteString(field(edFieldOut, "$/1M output", m.edPriceOut, "$ per 1M output  (the headline price)"))

	// The add-window affordance.
	addCur := "  "
	addSt := stDim
	if m.edField == edFieldAddWin {
		addCur = stSelText.Render("▌ ")
		addSt = stSelText
	}
	winTail := stDim.Render("   ") + stKey.Render("a") + stDim.Render(" add a window · ChargePoint-style")
	if narrow {
		winTail = stDim.Render(" · ") + stKey.Render("a") + stDim.Render(" add")
	}
	b.WriteString("\n" + addCur + addSt.Render("time-of-use windows") + winTail + "\n")

	if len(m.edWindows) == 0 {
		empty := stDim.Render("    (none - flat price all day · ") + stKey.Render("a") + stDim.Render(" adds a peak)")
		if narrow {
			empty = stDim.Render("    (none · ") + stKey.Render("a") + stDim.Render(" adds one)")
		}
		b.WriteString(empty + "\n")
	}
	for i, win := range m.edWindows {
		idx := edFieldFirstWin + i
		focused := m.edField == idx
		cur := "    "
		nameSt := stDim
		if focused {
			cur = "  " + stSelText.Render("▌ ")
			nameSt = stSelText
		}
		// Each sub-field renders its value; the focused one (in the focused row) is
		// highlighted (reverse-video, no literal brackets) so the user sees which
		// Start/End/In/Out they're editing without changing the layout width.
		sub := func(s int, v string) string {
			if focused && m.edWinSub == s {
				return stSelText.Render(v)
			}
			return nameSt.Render(v)
		}
		hours := sub(winSubStart, win.Start) + nameSt.Render("-") + sub(winSubEnd, win.End)
		// Pad to the visible (ANSI-stripped) width of the hours label so the price
		// column lines up regardless of the focus highlight.
		plainHours := win.Start + "-" + win.End + " UTC"
		if vis := len([]rune(plainHours)); vis < 18 {
			hours += nameSt.Render(" UTC" + strings.Repeat(" ", 18-vis))
		} else {
			hours += nameSt.Render(" UTC ")
		}
		var price string
		if win.Free {
			price = stLive.Render("FREE")
		} else {
			outVal, inVal := dollars(win.Out), dollars(win.In)
			// While editing a price sub-field, show the raw in-progress buffer (so a
			// half-typed "0." is visible, not the rounded float).
			if focused && m.edWinSub == winSubOut {
				outVal = "$" + m.edWinBuf
			}
			if focused && m.edWinSub == winSubIn {
				inVal = "$" + m.edWinBuf
			}
			price = stEmber.Render(sub(winSubOut, outVal) + "/1M out")
			if !narrow {
				price += stDim.Render(" · ") + stEmber.Render(sub(winSubIn, inVal)+"/1M in")
			}
		}
		b.WriteString(cur + hours + price + "\n")
	}

	if !narrow {
		b.WriteString("\n  " + stDim.Render("a window's price applies in its hours; the base price applies outside them.") + "\n")
	}
	return b.String()
}

// shareSetupView is the in-TUI guided fallback when no local model was detected: a
// k9s-styled option list (pick a tool for a start one-liner, or paste a URL we
// verify). It carries the same selection-cursor + contextual-footer feel as the
// provider table so the SHARE section reads as one designed system.
func (m model) shareSetupView(w int) string {
	var b strings.Builder
	narrow := m.narrow()
	headTail := stDim.Render("   no running model found - what are you using?")
	if narrow {
		headTail = ""
	}
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render("SET UP A MODEL") + headTail + "\n")
	if narrow {
		b.WriteString("  " + stDim.Render("what are you running?") + "\n")
	}
	b.WriteString("\n")

	nameW := 24
	if narrow {
		nameW = 18
	}
	for i, opt := range setupOptions {
		sel := i == m.setupCursor
		label := opt.label
		row := selCarat(sel) + " "
		if sel {
			row += rowSel(true, "  "+pad(label, nameW), w-4)
		} else {
			row += "  " + stDim.Render(pad(label, nameW))
		}
		b.WriteString(row + "\n")
		// Under the selected named tool, show its start one-liner inline (truncated to
		// the terminal width so it never overflows).
		if sel && opt.key != "other" && opt.oneLiner != "" {
			line := "      " + "start it: " + opt.oneLiner
			b.WriteString(stDim.Render(pad(line, w-2)) + "\n")
		}
	}

	// The paste row turns into a live input when the "Other" option is selected.
	if m.setupCursor == len(setupOptions)-1 {
		tail := stDim.Render("   e.g. http://127.0.0.1:8081  ·  ⏎ verifies /v1/models")
		if narrow {
			tail = ""
		}
		b.WriteString("\n  " + stPrompt.Render("url › ") + stSelText.Render(m.setupPaste+"▏") + tail + "\n")
	} else {
		hint := stDim.Render("started your tool? press ") + stKey.Render("r") + stDim.Render(" to re-scan")
		b.WriteString("\n  " + hint + "\n")
	}
	if m.setupErr != "" {
		b.WriteString("\n  " + stEmber.Render(pad("! "+m.setupErr, w-2)) + "\n")
	}
	return b.String()
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

// compactFooter is the windowshade single-line key-hint footer: a hairline rule, a
// terse per-mode hint, then the account tag and the `m expand` reminder. Width-safe:
// the hint is trimmed to fit before the rule, and a fresh status note (if any) rides
// one line under it so an action still surfaces an outcome.
func (m model) compactFooter(w int) string {
	rule := stHeadRule.Render(strings.Repeat("-", w))
	var keys string
	switch m.mode {
	case modeChat:
		keys = "talk · esc disconnect · tab peek"
	case modeShare:
		keys = "↑↓ · ⏎/a air · p price · r"
	case modeLimits:
		keys = "↑↓ · ⏎ edit · d clear · esc"
	case modeShareEditor:
		keys = "tab field · ⏎ save · esc"
	case modeShareSetup:
		keys = "↑↓ · ⏎ · r · esc"
	case modeConnectConfirm:
		keys = "⏎/y accept · esc deny"
	case modeOverLimit:
		keys = "⏎ save · ↑↓ · w wait · esc"
	default:
		keys = "↑↓ · ⏎ tune in · s share · / · ?"
	}
	hint := stDim.Render(keys) + stDim.Render("  ·  ") + stKey.Render("m") + stDim.Render(" expand") +
		stDim.Render("  ·  ") + m.accountTag(true)
	line := truncVisible("  "+hint, w)
	st := ""
	if m.status != "" {
		st = "\n" + truncVisible("  "+m.status, w)
	}
	return rule + "\n" + line + st
}

func (m model) footer(w int) string {
	// COMPACT (windowshade): a single, terse key-hint footer under one hairline rule -
	// no sprawling bal/broker/status block. It still adapts the leading hint to the
	// mode so the right keys are taught, and always carries the `m expand` reminder.
	if m.compact {
		return m.compactFooter(w)
	}
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
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeConnecting:
		left = stDim.Render("locking the channel  ·  ⏎ skip to channel  ·  esc cancel")
		if m.narrow() {
			left = stDim.Render("locking · ⏎ skip · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeOverLimit:
		left = stDim.Render("⏎ save & re-check  ·  ↑↓ nudge  ·  w wait  ·  esc deny")
		if m.narrow() {
			left = stDim.Render("⏎ save · ↑↓ nudge · w wait · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeLimits:
		left = stDim.Render("↑↓ move  ·  ⏎ edit  ·  tab field  ·  d clear  ·  esc done")
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎ edit · tab · d · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeShare:
		left = stDim.Render("↑↓/jk move  ·  ⏎/a on-air  ·  p price+schedule  ·  r re-detect  ·  s/esc tune in")
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎/a air · p · r · esc")
		}
		right := stRed.Render(fmt.Sprintf("%d on air", m.sharesOnAir()))
		return modalFooter(m.effWidth(), left, right, m.status)
	case modeShareEditor:
		left = stDim.Render("tab/↑↓ field  ·  type to set $  ·  a add window  ·  f free  ·  d delete  ·  ⏎ save  ·  esc cancel")
		if m.narrow() {
			left = stDim.Render("tab field · a/f/d · ⏎ save · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeShareSetup:
		left = stDim.Render("↑↓ pick  ·  ⏎ select/verify  ·  r re-scan  ·  s/esc tune in")
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎ · r · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeQuitConfirm:
		left = stDim.Render("y quit + go off air  ·  n/esc stay on air")
		if m.narrow() {
			left = stDim.Render("y quit · n/esc stay")
		}
		right := stRed.Render(fmt.Sprintf("%d on air", m.onAirCount()))
		return modalFooter(m.effWidth(), left, right, m.status)
	case modeAgent:
		switch {
		case m.agentPicker:
			left = stDim.Render("↑↓ pick a model  ·  ⏎ select  ·  esc keep current")
			if m.narrow() {
				left = stDim.Render("↑↓ pick · ⏎ select · esc keep")
			}
		case m.agentPendingConfirm != nil:
			left = stDim.Render("y run the tool  ·  n/esc deny (default DENY)")
			if m.narrow() {
				left = stDim.Render("y run · n/esc deny")
			}
		default:
			left = stDim.Render("type to ask  ·  /model  ·  /clear  ·  /persona  ·  esc exits AGENT")
			if m.narrow() {
				left = stDim.Render("ask · /model · esc exit")
			}
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	}
	if m.mode == modeChat {
		if m.narrow() {
			left = stDim.Render("talk · esc disconnect · tab peek · ⌃c quit")
		} else {
			left = stDim.Render("type to talk  ·  esc disconnect  ·  tab peek at the band  ·  /quit leaves channel  ·  ⌃c quit app")
		}
	} else if m.filterMode {
		// FILTER ENTRY: teach the live-filter keys (type / esc / enter), not the browse keys.
		if m.narrow() {
			left = stDim.Render("type to filter · esc clear · ⏎ apply")
		} else {
			left = stDim.Render("type to filter the band by name  ·  esc clears + closes  ·  ⏎ keeps it applied")
		}
	} else if m.narrow() {
		discKey := ""
		if m.connected != nil {
			discKey = " · d"
		}
		left = stDim.Render("↑↓ ⏎" + discKey + " · f filter · ←→ section · s · ?")
	} else if m.connected != nil {
		// Connected: lead with the channel + disconnect hints (load-bearing here); the
		// filter/sort keys still ride along but the toggles drop to keep the line tight.
		left = stDim.Render("↑↓ pick · enter tune in · d disconnect · tab/c channel · ←/→ section · s share")
	} else {
		left = stDim.Render("↑↓ pick · enter tune in · f filter · S sort · F/C/O · ←/→ section · s share")
	}
	confMode := ""
	if m.confidentialOnly {
		confMode = stGold.Render("◆conf-only") + "  "
	}
	right := confMode + m.accountTag(true) + "  " + stDim.Render(m.broker)
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
		right = confMode + m.accountTag(true)
		gap = w - lipgloss.Width(left) - lipgloss.Width(right)
		if gap < 1 {
			return rule + "\n" + left + "\n" + right + st // last resort: stack
		}
	}
	return rule + "\n" + left + strings.Repeat(" ", gap) + right + st
}

func (m model) helpView() string {
	// Lead with the few things a new user needs - the two-way radio in one breath.
	start := [][2]string{
		{"0", "AGENT: a small tool-capable agent (dj.md persona) - reads files, runs commands (you confirm)"},
		{"←/→", "switch section: cycle the [0] AGENT … [?] HELP bar (same as pressing its number)"},
		{"↑↓ then enter", "TUNE IN: pick a band, open a channel, chat"},
		{"f", "FILTER the band by name (live) - esc clears, enter keeps it applied"},
		{"S · F/C/O", "SORT cycle (strongest/cheapest/fastest/most-stations) · toggles free-now / confidential / on-air"},
		{"s", "switch to SHARE: put your own GPU on air (earn or free)"},
		{"m", "COMPACT: a calm, dense, animation-free windowshade view"},
		{"esc (in a channel)", "disconnect - leave the channel, back to the band"},
		{"q (browsing)", "quit RogerAI"},
	}
	cmds := [][2]string{
		{"/search", "re-scan the band for stations (CLI: rogerai search)"},
		{"/connect (enter)", "tune in to the selected station (CLI: rogerai use)"},
		{"/chat (c · tab)", "open the CHANNEL session with the connected model"},
		{"/share [off]", "SHARE: the provider table - flip your models on/off air"},
		{"/login", "link GitHub - only needed to EARN (CLI: rogerai login)"},
		{"/balance · /topup", "your wallet balance · add funds (CLI: rogerai balance)"},
		{"/limits", "see + edit your per-model spend maxes"},
		{"/grant [create <name>]", "private free keys for your bots/family"},
		{"/confidential", "toggle: route only to TEE-attested nodes"},
		{"/endpoint · /config", "endpoint + key · broker/identity"},
		{"/help · /quit", "this · quit RogerAI"},
	}
	var b strings.Builder
	// Ping rests here, on air and standing by - an intentional home for the mascot
	// (not just empty/error states). Body volt, the eye the one live-red glyph. COMPACT
	// freezes Ping to the canonical standing-by pose (no bob) per reduced-motion.
	pf := anim(m.frame)
	if m.compact {
		pf = frozenFrame
	}
	ping := renderPing(pingIdleFrames[pf%len(pingIdleFrames)], "•")
	b.WriteString("\n" + indentBlock(ping, "    ") + "\n")
	b.WriteString("    " + stPingDim.Render("Ping · on air, go ahead") + "\n\n")
	b.WriteString(stBrand.Render("  start here") + stDim.Render("  (a two-way radio for GPUs)") + "\n\n")
	for _, c := range start {
		b.WriteString("  " + stKey.Render(fmt.Sprintf("%-20s", c[0])) + stDim.Render(c[1]) + "\n")
	}
	b.WriteString("\n" + stBrand.Render("  all commands") + stDim.Render("  (each is also a `rogerai <cmd>` you can script)") + "\n\n")
	for _, c := range cmds {
		b.WriteString("  " + stKey.Render(fmt.Sprintf("%-22s", c[0])) + stDim.Render(c[1]) + "\n")
	}
	b.WriteString("\n  " + stDim.Render("in CHANNEL: /model /clear /save /system <p> /cost /endpoint /disconnect /quit") + "\n")
	b.WriteString("  " + stDim.Render("sections: ") + stKey.Render("←/→") + stDim.Render(" switch section (cycle the [0]…[?] bar) · ") +
		stKey.Render("s") + stDim.Render(" toggles TUNE IN ⇄ SHARE · ") +
		stKey.Render("tab") + stDim.Render(" peeks at the band from a channel") + "\n")
	b.WriteString("  " + stDim.Render("view: ") + stKey.Render("m") +
		stDim.Render(" toggles COMPACT - the calm, dense, animation-free windowshade") + "\n")
	b.WriteString("\n  " + stDim.Render("rogerai "+helpVersion+" · press any key to go back") + "\n")
	return b.String()
}

// helpVersion is the client version shown in help; set by the host via SetVersion.
var helpVersion = "v4.2.4"

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
// node's measured tok/s AND (optionally) the number of stations on the band - the
// web's "more stations, stronger signal" rule: each extra station after the first
// lifts the floor a notch (capped), so a busy band reads taller. Pass the station
// count as the optional 4th arg; the per-station header bar passes none (no boost).
// Offline or unmeasured shows a flat low signal. The shimmer rides m.frame (the one
// carrier beat) so the bars breathe with the beacon; quiet freezes the frame.
func signalBarsRaw(frame int, tps float64, online bool, stations ...int) string {
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
	// More stations on the band -> a stronger carrier: +1 notch per extra station
	// beyond the first, capped at +2 so a single fast node and a crowded band stay
	// distinguishable without pinning everything to the top.
	if len(stations) > 0 && stations[0] > 1 {
		boost := stations[0] - 1
		if boost > 2 {
			boost = 2
		}
		base += boost
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

// signalPeak is the glyph level at and above which a signal cell glints red - the
// "data-as-decoration" grade (like Serie / regex-tui): the tower is mono ink, but
// its tallest bars (a strong carrier) tip into the one accent red at the peak. The
// glyph ramp is ▁▂▃▄▅▆▇█ (indices 0..7); ▇/█ (>= 6) read as "peaking".
const signalPeak = 6

// tintSignal grades a raw equalizer cell-by-cell so the bar carries meaning, not
// just a flat color: an online, measured tower is mono ink with its PEAK cells
// (the tallest bars) glinting the one accent red - a subtle dim->red gradient
// driven by tok/s. Offline / unmeasured is flat dim. Padding spaces stay bare
// (no visible color), so column alignment is unaffected. Under NO_COLOR lipgloss
// strips every color and the ▁..█ glyphs alone still read the signal.
func tintSignal(raw string, tps float64, online bool) string {
	if !(online && tps > 0) {
		return stDim.Render(raw)
	}
	ramp := []rune("▁▂▃▄▅▆▇█")
	lvlOf := func(r rune) int {
		for i, g := range ramp {
			if g == r {
				return i
			}
		}
		return -1
	}
	var b strings.Builder
	for _, r := range raw {
		lvl := lvlOf(r)
		switch {
		case lvl < 0: // a space / non-bar rune (alignment padding) - leave bare
			b.WriteRune(r)
		case lvl >= signalPeak: // peaking - the one red glint
			b.WriteString(stRed.Render(string(r)))
		default: // body of the tower - mono ink
			b.WriteString(stLive.Render(string(r)))
		}
	}
	return b.String()
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

// slowTick is the compact ("windowshade") cadence: a calm ~5s beat that only drives
// the periodic band re-scan, never animation. It keeps the band/share tables live
// without the rapid 160ms churn, so compact + idle is genuinely quiet. The instant
// the user un-compacts, relays, or starts a staged tune-in, the tickMsg handler
// switches back to the fast tick().
func slowTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// fetchOffers pulls the FULL on-air set from the broker /discover (the broker does
// NOT paginate - one response carries every live offer). The TUI scales this with
// CLIENT-SIDE windowing (browseView renders only the visible window) + name/sort/
// toggle filters (visibleBands), which covers realistic scale. NEXT STEP, if on-air
// counts ever exceed a few hundred: add broker-side pagination + load-on-scroll
// here (a cursor/offset on /discover, fetching the next page as the window nears the
// bottom) so the client never holds the whole list in memory.
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
			Balance  float64 `json:"balance"`
			LoggedIn bool    `json:"logged_in"`
		}
		json.NewDecoder(resp.Body).Decode(&b)
		return balanceMsg{balance: b.Balance, loggedIn: b.LoggedIn}
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
