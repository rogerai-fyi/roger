// Package tui is the interactive `rogerai` experience - a two-way radio for GPUs.
// Stations (providers) go on air; you tune in to a channel and talk. Signal bars
// animate live; a gold call-sign ◆ marks lineage-verified. Built on Bubble Tea.
package tui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bownux/rogerai/internal/client"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

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
)

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
	limits    *LimitStore
	bands     []band // offers grouped by model (the band list, 3.1)
	q         quote  // the in-flight connect quote (confirm / over-limit)
	editBuf   string // inline numeric edit buffer (over-limit + limits edit)
	editField int    // which field is focused in the limits editor (0=out,1=tps)
	limCursor int    // cursor in the limits view
	limModels []string
	watching  string // band we are "wait & notify" watching (stub label)
}

// ---- messages ----
type offersMsg []offer
type balanceMsg float64
type chatMsg struct{ reply, status string }
type errMsg string
type tickMsg struct{}

func New(broker, user string) model {
	return NewWith(broker, user, nil)
}

// NewWith builds the model with a spend-limit store (nil = no caps / no persist).
func NewWith(broker, user string, limits *LimitStore) model {
	ci := textinput.New()
	ci.Prompt = lipgloss.NewStyle().Foreground(cVolt).Render("/ ")
	ci.Placeholder = "search · connect · chat · limits · config · share · endpoint · balance · help · quit"
	ch := textinput.New()
	ch.Prompt = stSelText.Render("you ▸ ")
	ch.Placeholder = "say something on channel…"
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
		return m, tick()
	case offersMsg:
		m.offers = []offer(msg)
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
		// Don't clobber a fresh dip-under notification with the scan summary.
		if !notified {
			m.status = fmt.Sprintf("%d bands · %d stations on air", len(m.bands), countOnline(m.offers))
		}
		return m, nil
	case balanceMsg:
		m.balance, m.haveBal = float64(msg), true
		return m, nil
	case chatMsg:
		m.transcript = append(m.transcript, stLive.Render("◂ ")+msg.reply, stDim.Render("   "+msg.status))
		return m, nil
	case errMsg:
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
		case "esc":
			m.mode = modeBrowse
			m.chatIn.Blur()
			return m, nil
		case "enter":
			p := strings.TrimSpace(m.chatIn.Value())
			if p == "" || m.connected == nil {
				return m, nil
			}
			m.chatIn.SetValue("")
			m.transcript = append(m.transcript, stSelText.Render("▸ ")+p)
			return m, sendChat(m.broker, m.user, m.connected.Model, p, m.confidentialOnly)
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
		default: // esc, n, N, anything else - default DENY
			m.mode = modeBrowse
			m.status = stDim.Render("denied - no channel opened")
			return m, nil
		}
	case modeOverLimit:
		return m.onOverLimitKey(k)
	case modeLimits:
		return m.onLimitsKey(k)
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
			if m.cursor < len(m.offers)-1 {
				m.cursor++
			}
		case "enter":
			return m.connect()
		case "c":
			if m.connected != nil {
				m.mode = modeChat
				m.chatIn.Focus()
				return m, textinput.Blink
			}
		case "?":
			m.mode = modeHelp
		case "r":
			m.status = "re-scanning the band…"
			return m, fetchOffers(m.broker)
		}
	}
	return m, nil
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
		m.status = "go on air: run `rogerai share` in another terminal (auto-detects your local model)"
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
	m.mode = modeBrowse
	m.status = stGold.Render("◆ ") + stLive.Render("on channel ") + o.NodeID + stDim.Render(" - endpoint live · roger that")
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

// ---- view ----
func (m model) View() string {
	w := m.width
	if w < 64 {
		w = 88
	}
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
	default:
		b.WriteString(m.browseView(w))
	}
	if m.connected != nil && m.mode != modeChat && m.mode != modeConnectConfirm && m.mode != modeOverLimit && m.mode != modeLimits {
		b.WriteString("\n" + m.endpointPanel(w))
	}
	b.WriteString("\n" + m.footer(w))
	return b.String()
}

// confirmView is the connect-time cost confirmation (3.2): the deal + an explicit
// accept/deny with the SAFE default on DENY.
func (m model) confirmView(w int) string {
	q := m.q
	bd := q.b
	var b strings.Builder
	b.WriteString("\n" + stGold.Render("  ◆ ") + stSelText.Render("tune in to  ") + stSelText.Render(bd.model) + "\n\n")
	st := bd.cheapest
	b.WriteString(stDim.Render("    station       ") + "@" + st.NodeID + "  " + stDim.Render(st.Region) + "  " + tpsCell(st.TPS, st.Online) + stDim.Render("  the strongest match") + "\n")
	if bd.stations > 1 {
		b.WriteString(stDim.Render("    live range    ") + stEmber.Render(rangeStr(bd)) + stDim.Render(" $/1M out   ("+fmt.Sprintf("%d", bd.stations)+" on air)") + "\n")
	}
	b.WriteString(stDim.Render("    price now     ") + stEmber.Render(money(bd.minOut)) + stDim.Render(" $/1M out   ·   ") + stEmber.Render(money(st.PriceOut)) + " " + stDim.Render("(cheapest, in "+money(st.PriceIn)+")") + "\n")
	if q.limit.MaxOut > 0 {
		b.WriteString(stDim.Render("    your max      ") + stEmber.Render(money(q.limit.MaxOut)) + stDim.Render(" $/1M out   ") + stLive.Render("within your limit ✓") + "\n")
	}
	b.WriteString(stDim.Render("    locked        each reply price-locks at send; a hold pre-auths your session") + "\n\n")
	b.WriteString(stDim.Render(fmt.Sprintf("    est. cost     ~ %.6f cr / typical reply  (~%d out tokens)", q.estReply, q.typical)) + "\n")
	if m.haveBal {
		b.WriteString(stDim.Render(fmt.Sprintf("                  ~ %.6f cr / 100 replies        balance %.4f cr", q.estReply*100, m.balance)) + "\n")
	}
	b.WriteString("\n")
	b.WriteString("       " + stLive.Render("accept · open channel") + "          " + stDim.Render("deny · back to the band") + "\n")
	b.WriteString("       " + stKey.Render("[ enter / y ]") + "                  " + stDim.Render("[ esc / n ]  (default)") + "\n")
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
	b.WriteString(stDim.Render(fmt.Sprintf("    gap               +%.2f  (%.0f%% over)   you would pay %.6f cr / reply", gap, pct, bd.minOut*float64(q.typical)/1e6)) + "\n\n")
	// the inline edit row
	editShown := m.editBuf
	hint := stDim.Render("min " + money(bd.minOut))
	if v, err := strconv.ParseFloat(strings.TrimSpace(m.editBuf), 64); err == nil && v >= bd.minOut {
		hint = stLive.Render("▸ enough to tune in now")
	} else {
		hint = stEmber.Render("still below the band (" + money(bd.minOut) + ")")
	}
	b.WriteString(stDim.Render("    raise your max for "+bd.model) + "\n")
	b.WriteString("      $/1M out   " + stSelText.Render("▏"+editShown+"▏") + "   " + hint + "\n")
	b.WriteString(stDim.Render(fmt.Sprintf("                 min %s   ·   suggested %s   ·   was %s", money(bd.minOut), money(bd.minOut), money(q.limit.MaxOut))) + "\n\n")
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

func (m model) header(w int) string {
	tower := stBrand.Render("▟█▙")
	name := stBrand.Render(" R O G E R") + stTag.Render(" · A I ")
	pulse := []string{"( • )", "(( • ))", "((( • )))", "(( • ))"}[anim(m.frame)%4]
	right := stLive.Render(pulse)
	if m.connected != nil {
		right = stGold.Render("◆ ") + stLive.Render("on channel "+m.connected.NodeID) + " " + stLive.Render(pulse)
	}
	left := tower + name + right
	tag := stDim.Render("borrow a GPU, pay by the token")
	gap := w - lipgloss.Width(left) - lipgloss.Width(tag)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + tag + "\n" + stHeadRule.Render(strings.Repeat("─", w))
}

func (m model) browseView(w int) string {
	if len(m.bands) == 0 {
		return "\n" + stDim.Render("   scanning the band for stations on air…  (r to rescan)") + "\n"
	}
	var b strings.Builder
	b.WriteString(stDim.Render(fmt.Sprintf("  the band - %d models on air", len(m.bands))) + "\n")
	b.WriteString(stDim.Render(fmt.Sprintf("  %-20s %-8s %-15s %-9s %s", "band", "stations", "$/1M out (range)", "signal", "flags")) + "\n")
	for i, bd := range m.bands {
		nameStyle := lipgloss.NewStyle().Foreground(cInk)
		cur := " "
		if i == m.cursor {
			cur = stSelBar.Render("▌")
			nameStyle = stSelText
		}
		name := nameStyle.Render(pad(bd.model, 20))
		stationsLbl := "-"
		if bd.online {
			stationsLbl = fmt.Sprintf("%d on", bd.stations)
		}
		stations := stDim.Render(pad(stationsLbl, 8))
		// price range, tinted: cheap end live, spread dim
		rng := stEmber.Render(pad(rangeStr(bd), 15))
		// per-band spark "weather" (omitted under NO_COLOR / single station)
		spark := ""
		if s := priceSpark(bd); s != "" {
			spark = " " + stDim.Render(s)
		}
		// signal from the cheapest station's measured tps
		var sigTPS float64
		online := bd.online
		if bd.cheapest != nil {
			sigTPS = bd.cheapest.TPS
		}
		sig := signalBars(m.frame, sigTPS, online)
		badge := stDim.Render("·")
		if bd.lineage > 0 {
			badge = stGold.Render(fmt.Sprintf("◆ %d", bd.lineage))
		}
		if bd.free {
			badge += " " + stLive.Render("FREE")
		}
		if bandOverLimit(bd, m.limits) {
			badge += " " + stEmber.Render("above limit")
		}
		b.WriteString(fmt.Sprintf("%s %s %s %s%s %s %s\n",
			cur, name, stations, rng, spark, sig, badge))
	}
	return b.String()
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

// money renders a price as a fixed 2-dp string.
func money(v float64) string { return fmt.Sprintf("%.2f", v) }

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

// priceSpark renders a tiny block-ramp "weather" glyph for a band's spread using
// the existing signal-bar ramp. With no real history (P0) it is a static shape
// derived from the spread width, guarded for NO_COLOR / non-UTF8 (returns "" so
// the caller omits it, matching the design's fallback).
func priceSpark(b band) string {
	if quiet || !b.online || b.stations <= 1 || b.maxOut <= b.minOut {
		return ""
	}
	glyphs := []rune("▁▂▃▄▅▆▇█")
	// A small fixed bowl whose depth scales with the spread - a visual hint that a
	// wider spread means more to shop around. Not real history (that is P1).
	shape := []int{2, 3, 5, 4, 3, 2, 1}
	var sb strings.Builder
	for _, lvl := range shape {
		if lvl >= len(glyphs) {
			lvl = len(glyphs) - 1
		}
		sb.WriteRune(glyphs[lvl])
	}
	return sb.String()
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
	b.WriteString(stGold.Render("  ◆ ") + stDim.Render(fmt.Sprintf("on channel · %s · esc to leave", m.connected.NodeID)) + "\n")
	lines := m.transcript
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	for _, l := range lines {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("\n  " + m.chatIn.View() + "\n")
	return b.String()
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

func (m model) footer(w int) string {
	bal := "-"
	if m.haveBal {
		bal = fmt.Sprintf("%.4f cr", m.balance)
	}
	left := stDim.Render("↑↓ tune  ·  enter on-air  ·  c chat  ·  / cmd  ·  ? help  ·  q quit")
	confMode := ""
	if m.confidentialOnly {
		confMode = stGold.Render("◆conf-only") + "  "
	}
	right := confMode + stEmber.Render("balance "+bal) + "  " + stDim.Render(m.broker)
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	st := ""
	if m.status != "" {
		st = "\n" + stDim.Render("  ") + m.status
	}
	return stHeadRule.Render(strings.Repeat("─", w)) + "\n" + left + strings.Repeat(" ", gap) + right + st
}

func (m model) helpView() string {
	cmds := [][2]string{
		{"/search", "re-scan the band for stations"},
		{"/connect (enter)", "tune in to the selected station"},
		{"/chat (c)", "test the connected model in-CLI"},
		{"/limits", "see + edit your per-model spend maxes"},
		{"/endpoint", "show the local OpenAI endpoint + key"},
		{"/confidential", "toggle: route only to TEE-attested nodes"},
		{"/config", "broker + identity (federation: switch brokers)"},
		{"/share", "go on air - share your own local model"},
		{"/balance", "wallet credits"},
		{"/help  /quit", "this · exit"},
	}
	var b strings.Builder
	b.WriteString("\n" + stBrand.Render("  commands") + stDim.Render("  (a two-way radio for GPUs)\n\n"))
	for _, c := range cmds {
		b.WriteString("  " + stKey.Render(fmt.Sprintf("%-18s", c[0])) + stDim.Render(c[1]) + "\n")
	}
	b.WriteString("\n" + stDim.Render("  press any key to go back") + "\n")
	return b.String()
}

// ---- helpers / cmds ----
// signalBars renders measured throughput as an animated equalizer. Bar height is
// set by the node's measured tok/s; unmeasured (0) shows a dim flat signal.
func signalBars(frame int, tps float64, online bool) string {
	glyphs := []rune("▁▂▃▄▅▆▇█")
	if !online {
		return stDim.Render("▁▁▁▁▁")
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
		return stDim.Render("▁▁▁▁▁") // online but not yet measured
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
	return stLive.Render(sb.String())
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
		json.NewDecoder(resp.Body).Decode(&d)
		sort.Slice(d.Offers, func(i, j int) bool { return d.Offers[i].PriceIn < d.Offers[j].PriceIn })
		return offersMsg(d.Offers)
	}
}

func fetchBalance(broker, user string) tea.Cmd {
	return func() tea.Msg {
		req, _ := http.NewRequest(http.MethodGet, broker+"/balance", nil)
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
		reply, status, err := client.Chat(broker, user, mdl, prompt, confidential)
		if err != nil {
			return errMsg(err.Error())
		}
		return chatMsg{reply: reply, status: status}
	}
}

// Run launches the TUI with no spend limits (back-compat).
func Run(broker, user string) error {
	return RunWith(broker, user, nil)
}

// RunWith launches the TUI with a spend-limit store (the pricing UX: per-model
// maxes, connect confirmation, over-limit edit). nil = no caps / no persistence.
func RunWith(broker, user string, limits *LimitStore) error {
	_, err := tea.NewProgram(NewWith(broker, user, limits), tea.WithAltScreen()).Run()
	return err
}
