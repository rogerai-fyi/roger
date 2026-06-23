// Package tui is the interactive `rogerai` experience - a two-way radio for GPUs.
// Stations (providers) go on air; you tune in to a channel and talk. Signal bars
// animate live; a gold call-sign ◆ marks lineage-verified. Built on Bubble Tea.
package tui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bownux/rogerai/internal/client"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

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

type mode int

const (
	modeBrowse mode = iota
	modeCommand
	modeChat
	modeHelp
)

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
}

// ---- messages ----
type offersMsg []offer
type balanceMsg float64
type chatMsg struct{ reply, status string }
type errMsg string
type tickMsg struct{}

func New(broker, user string) model {
	ci := textinput.New()
	ci.Prompt = lipgloss.NewStyle().Foreground(cVolt).Render("/ ")
	ci.Placeholder = "search · connect · chat · config · share · endpoint · balance · help · quit"
	ch := textinput.New()
	ch.Prompt = stEmber.Render("you ▸ ")
	ch.Placeholder = "message the model…"
	return model{broker: broker, user: user, cmd: ci, chatIn: ch, proxyAddr: "127.0.0.1:4141", status: "tuning in…"}
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
		return m, tick()
	case offersMsg:
		m.offers = []offer(msg)
		if m.cursor >= len(m.offers) {
			m.cursor = 0
		}
		m.status = fmt.Sprintf("%d stations on air", countOnline(m.offers))
		return m, nil
	case balanceMsg:
		m.balance, m.haveBal = float64(msg), true
		return m, nil
	case chatMsg:
		m.transcript = append(m.transcript, stLive.Render("◆ ")+msg.reply, stDim.Render("   "+msg.status))
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
			m.transcript = append(m.transcript, stEmber.Render("▸ ")+p)
			return m, sendChat(m.broker, m.user, m.connected.Model, p, m.confidentialOnly)
		}
		var c tea.Cmd
		m.chatIn, c = m.chatIn.Update(k)
		return m, c
	case modeHelp:
		m.mode = modeBrowse
		return m, nil
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

func (m model) connect() (tea.Model, tea.Cmd) {
	if len(m.offers) == 0 {
		return m, nil
	}
	o := m.offers[m.cursor]
	if !m.proxyUp {
		ln, err := net.Listen("tcp", m.proxyAddr)
		if err != nil {
			m.status = stEmber.Render("! endpoint bind failed: " + err.Error())
			return m, nil
		}
		m.endpoint = "http://" + ln.Addr().String() + "/v1"
		m.proxyUp = true
		go http.Serve(ln, client.ProxyHandler(m.broker, m.user, m.confidentialOnly))
	}
	m.connected = &o
	m.apikey = "roger-local"
	m.status = stLive.Render("◆ on channel ") + o.NodeID + " - endpoint live"
	return m, nil
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
	default:
		b.WriteString(m.browseView(w))
	}
	if m.connected != nil && m.mode != modeChat {
		b.WriteString("\n" + m.endpointPanel(w))
	}
	b.WriteString("\n" + m.footer(w))
	return b.String()
}

func (m model) header(w int) string {
	tower := stBrand.Render("▟█▙")
	name := stBrand.Render(" R O G E R") + stTag.Render(" · A I ")
	pulse := []string{"( • )", "(( • ))", "((( • )))", "(( • ))"}[m.frame%4]
	right := stLive.Render(pulse)
	if m.connected != nil {
		right = stLive.Render("◆ ON CHANNEL "+m.connected.NodeID) + " " + stLive.Render(pulse)
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
	if len(m.offers) == 0 {
		return stDim.Render("\n   scanning the band for stations on air…  (r to rescan)\n")
	}
	var b strings.Builder
	b.WriteString(stDim.Render(fmt.Sprintf("  %-14s %-20s %-12s %-8s %-7s %s\n", "STATION", "MODEL", "$/1M in·out", "REGION", "SIGNAL", "")))
	for i, o := range m.offers {
		sel := "  "
		nm := o.NodeID
		md := o.Model
		if i == m.cursor {
			sel = stSelBar.Render("▎ ")
			nm = stSelText.Render(o.NodeID)
			md = stSelText.Render(o.Model)
		} else {
			nm = lipgloss.NewStyle().Foreground(cInk).Render(o.NodeID)
		}
		price := stEmber.Render(fmt.Sprintf("%.2f·%.2f", o.PriceIn, o.PriceOut))
		sig := signalBars(m.frame, o.TPS, o.Online)
		tps := stDim.Render("   -")
		if o.TPS > 0 {
			tps = stLive.Render(fmt.Sprintf("%4.0f", o.TPS))
		}
		dot := stDim.Render("○")
		if o.Online {
			dot = stLive.Render("●")
		}
		badge := stDim.Render("·")
		if o.Confidential {
			badge = stGold.Render("◆conf")
		}
		if o.FreeNow {
			badge += " " + stLive.Render("FREE")
		}
		b.WriteString(fmt.Sprintf("%s%-22s %-20s %-22s %-8s %s%s t/s %s %s\n",
			sel, nm, md, price, stDim.Render(o.Region), sig, tps, dot, badge))
	}
	return b.String()
}

func (m model) chatView(w int) string {
	var b strings.Builder
	b.WriteString(stDim.Render(fmt.Sprintf("  test channel · %s · esc to leave\n", m.connected.NodeID)))
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
	body := stLive.Render("◆ tuned in - point any OpenAI tool here") + "\n" +
		"  endpoint  " + stKey.Render(m.endpoint) + "\n" +
		"  api key   " + stKey.Render(m.apikey) + "\n" +
		stDim.Render("  point your tools here: OPENAI_API_BASE="+m.endpoint+"  ·  /chat to test")
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
	b.WriteString(stDim.Render("\n  press any key to go back\n"))
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

// Run launches the TUI.
func Run(broker, user string) error {
	_, err := tea.NewProgram(New(broker, user), tea.WithAltScreen()).Run()
	return err
}
