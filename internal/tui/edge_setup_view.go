package tui

// THE WIZARD'S DRAWING (features/edge/onboard.feature) - the ONE screen the founder asked to
// play like a game: a colourful ASCII cabinet with a radio-tower world, glowing choice cards, a
// call-sign plate, and a real carrier-handshake animation between two towers that locks and goes
// ON AIR. It draws on the palette's lamp roles and the Wave Spectrum (the website's wave hues),
// but colour is never load-bearing: every state also carries box-drawing, a glyph and words, so
// NO_COLOR, a pipe and a legacy console read the same board. Under NO_COLOR / reduced motion /
// narrow widths the animated scene degrades to plain phase lines.

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// edgeSetupContentWidth is the cabinet's inner width for the current terminal: wide enough to
// keep the long choice sentences on one line, capped so it never sprawls, floored so the frame
// still closes on a narrow terminal.
func edgeSetupContentWidth(w int) int {
	cw := w - 4
	if cw > 92 {
		cw = 92
	}
	if cw < 40 {
		cw = 40
	}
	return cw
}

// edgeSetupView draws the wizard for the current step. Each block is emitted through line(),
// which clips to the terminal, so nothing can overflow.
func (m model) edgeSetupView(w int, line func(string)) {
	s := m.edge.setup
	cw := edgeSetupContentWidth(w)
	emit := func(block string) {
		for _, ln := range strings.Split(block, "\n") {
			line("  " + ln)
		}
	}
	off := 0
	if s.step == setupRunning {
		off = s.frame // the cabinet transmits while the handshake is live
	}
	emit(edgeSetupBanner(cw, off))
	emit(edgeSetupTrack(s, cw))
	line("")
	switch s.step {
	case setupChoose:
		emit(m.setupChooseScene(cw))
	case setupName:
		emit(m.setupNameScene(cw))
	case setupAddr:
		emit(m.setupAddrScene(cw))
	case setupRunning:
		m.setupHandshakeScene(cw, w, line)
	case setupAllow:
		emit(m.setupAllowScene(cw))
	case setupDone:
		emit(m.setupDoneScene(cw))
	case setupFailed:
		emit(m.setupFailedScene(cw))
	}
	line("")
	line("  " + edgeSetupCenter(stDim.Render(edgeSetupHint(s)), cw))
}

// ── the cabinet's furniture ────────────────────────────────────────────────

// edgeSetupBanner is the wizard's marquee: the shared cabinet titled for setup.
func edgeSetupBanner(cw int, off int) string {
	return edgeCabinet(cw, "◉ SET UP YOUR EDGE ◉", off)
}

// edgeCabinet is the arcade marquee the whole [3] EDGE surface wears: a double-ruled bar with
// Wave-Spectrum gradient shoulders framing `title` and a bracketed call sign, so both the patch
// bay and the setup wizard read as one instrument (features/edge/patchbay.feature §8). `off`
// shifts the shoulder spectrum, so the marquee visibly transmits while something is really in
// flight and sits still otherwise. Under NO_COLOR it is box characters and words alone.
func edgeCabinet(cw int, title string, off int) string {
	titleR := lipgloss.NewStyle().Foreground(cLive).Bold(true).Render(title)
	shoulderL := edgeSetupSpectrumBar("▐▓▒░ ", off)
	shoulderR := edgeSetupSpectrumBar(" ░▒▓▌", off+4)
	row1 := shoulderL + titleR + shoulderR
	callsign := stKey.Render("[ R O G E R · E D G E ]") + stDim.Render("   · · ·  ON AIR  · · ·")
	inner := lipgloss.JoinVertical(lipgloss.Center, row1, callsign)
	return lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(cLive).
		Width(cw - 2).
		Align(lipgloss.Center).
		Render(inner)
}

// edgeSetupTrack is the progress spine drawn as a RADIO TUNING DIAL, not a form breadcrumb: an
// FM band framed 88…108, each step a station whose signal is LOCKED (▇, behind you), ACQUIRING
// (◕, where you are) or SILENT (○, ahead), joined by a carrier that lights up as you tune across.
// Every state carries its own glyph so NO_COLOR reads the same order; the frequency framing is
// dropped on a narrow terminal.
func edgeSetupTrack(s *edgeSetup, cw int) string {
	steps := []string{"mode", "name", "handshake", "on air"}
	if s.finish {
		steps = []string{"finish", "handshake", "on air"}
	}
	cur := edgeSetupRailIndex(s)
	var parts []string
	for i, name := range steps {
		if i > 0 {
			if i <= cur {
				parts = append(parts, spectrumStyle(i+2).Render("━━"))
			} else {
				parts = append(parts, stDim.Render("┅┅"))
			}
		}
		switch {
		case i < cur:
			parts = append(parts, spectrumStyle(i+3).Render("▇")+stDim.Render(" "+name))
		case i == cur:
			parts = append(parts, lampStyle(roleLive).Render("◕")+stKey.Render(" "+name))
		default:
			parts = append(parts, stDim.Render("○ "+name))
		}
	}
	chain := strings.Join(parts, " ")
	if cw >= 60 {
		chain = lampStyle(roleSignal).Render("fm 88") + stDim.Render(" ▏ ") + chain +
			stDim.Render(" ▏ ") + lampStyle(roleSignal).Render("108")
	}
	return edgeSetupCenter(chain, cw)
}

func edgeSetupRailIndex(s *edgeSetup) int {
	if s.finish {
		switch s.step {
		case setupRunning:
			return 1
		case setupDone, setupAllow, setupFailed:
			return 2
		default:
			return 0
		}
	}
	switch s.step {
	case setupChoose:
		return 0
	case setupName, setupAddr:
		return 1
	case setupRunning:
		return 2
	default:
		return 3
	}
}

// ── the choice: two glowing cards ──────────────────────────────────────────

func (m model) setupChooseScene(cw int) string {
	head := edgeSetupCenter(stDim.Render("how do you want to set this machine up?"), cw)
	newCard := edgeSetupCard(cw, m.edge.setup.choice == 0, edgeTowerEmblem(),
		"start a new Edge - this machine becomes the authority",
		[]string{"needs no internet, roots the fleet here"})
	joinCard := edgeSetupCard(cw, m.edge.setup.choice == 1, edgeAntennaEmblem(),
		"join an existing Edge",
		[]string{"needs the authority's address, and to be allowed on it"})
	return lipgloss.JoinVertical(lipgloss.Left, head, "", newCard, joinCard)
}

// edgeSetupCard is one choice: a bordered panel that GLOWS red with a ▶ carat when selected and
// sits dim otherwise. The emblem is a 1-column mast in the left gutter and the text is a
// fixed-width block, so a sentence that wraps on a narrow terminal hangs cleanly under its own
// title instead of colliding with the mast. The carat is the NO_COLOR tell; the glow is the game.
func edgeSetupCard(cw int, selected bool, mast []string, title string, body []string) string {
	border, carat, titleSt := cRule, "  ", stDim
	if selected {
		border, carat, titleSt = cLive, lampStyle(roleLive).Render("▶ "), stKey
	}
	inner := cw - 4 // border + padding
	textW := inner - 5
	if textW < 20 {
		textW = 20
	}
	lines := []string{carat + titleSt.Render(title)}
	for _, b := range body {
		lines = append(lines, stDim.Render(b))
	}
	text := lipgloss.NewStyle().Width(textW).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	mastCol := lipgloss.JoinVertical(lipgloss.Left, mast...)
	row := lipgloss.JoinHorizontal(lipgloss.Top, mastCol, "  ", text)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Width(cw-2).
		Padding(0, 1).
		Render(row)
}

// edgeTowerEmblem is a broadcast tower: beacon over a mast on a groundplane.
func edgeTowerEmblem() []string {
	return []string{
		lampStyle(roleLive).Render(" ▲ "),
		stDim.Render("╱█╲"),
		stDim.Render("═╩═"),
	}
}

// edgeAntennaEmblem is a receiving antenna: a lit dish over a mast on the same groundplane, so
// the two cards read as one family.
func edgeAntennaEmblem() []string {
	return []string{
		lampStyle(roleSignal).Render("(●)"),
		stDim.Render(" ▏ "),
		stDim.Render("═╩═"),
	}
}

// ── naming: a call-sign plate ──────────────────────────────────────────────

func (m model) setupNameScene(cw int) string {
	s := m.edge.setup
	var prompt string
	switch {
	case s.finish:
		prompt = "finish setting up: enroll this machine onto the Edge it already roots."
	case s.join:
		prompt = "a name for THIS machine on the Edge you are joining:"
	default:
		prompt = "a name for your new Edge's first machine (this one):"
	}
	plate := edgeSetupPlate(cw, "CALL SIGN", s.name, true)
	blocks := []string{edgeSetupCenter(stDim.Render(prompt), cw), "", plate}
	if s.err != "" {
		blocks = append(blocks, "", edgeSetupCenter(lampStyle(roleLive).Render("✕ ")+stEmber.Render(s.err), cw))
	}
	return lipgloss.JoinVertical(lipgloss.Left, blocks...)
}

func (m model) setupAddrScene(cw int) string {
	s := m.edge.setup
	blocks := []string{
		edgeSetupCenter(stDim.Render("the authority's address on your network:"), cw),
		"",
		edgeSetupPlate(cw, "THIS MACHINE", s.name, false),
		edgeSetupPlate(cw, "AUTHORITY", s.addr, true),
	}
	if s.err != "" {
		blocks = append(blocks, "", edgeSetupCenter(lampStyle(roleLive).Render("✕ ")+stEmber.Render(s.err), cw))
	}
	blocks = append(blocks, "", edgeSetupCenter(stDim.Render("shaped like http://192.168.1.10:8791 - the address the authority machine shows."), cw))
	return lipgloss.JoinVertical(lipgloss.Left, blocks...)
}

// edgeSetupPlate is a labelled input plate. The active one glows and carries a blinking-style
// carat; the resting one shows its value quietly.
func edgeSetupPlate(cw int, label, val string, active bool) string {
	border, labelSt := cRule, stDim
	cursor := ""
	if active {
		border, labelSt, cursor = cLive, stKey, lampStyle(roleLive).Render("▏")
	}
	if val == "" && !active {
		val = stDim.Render("(none)")
	} else {
		val = stEmber.Render(val)
	}
	row := labelSt.Render(pad(label, 13)) + " " + val + cursor
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Width(cw-2).
		Padding(0, 1).
		Render(row)
}

// ── the handshake: a carrier crossing between two towers ───────────────────

func (m model) setupHandshakeScene(cw, w int, line func(string)) {
	s := m.edge.setup
	phase := "contacting the authority..."
	if !s.join {
		phase = "raising this machine's Edge..."
	}
	if s.finish {
		phase = "enrolling this machine..."
	}
	// Plain fallback: NO_COLOR, reduced motion or narrow -> phase lines, no travelling scene.
	if m.compact || w < 56 || edgeSetupPlain() {
		line("  " + stKey.Render("HANDSHAKE"))
		line("  " + stDim.Render(phase))
		return
	}
	for _, ln := range strings.Split(m.edgeSetupHandshakeArt(cw, phase), "\n") {
		line("  " + ln)
	}
}

// edgeSetupHandshakeArt draws one frame of the handshake as a radio scene: your tower on the
// left, the authority's on the right (or the horizon, for a new Edge), and a carrier packet
// sweeping the channel between them, trailing Wave-Spectrum rings. It is bound to a real op in
// flight, so its motion is honest.
func (m model) edgeSetupHandshakeArt(cw int, phase string) string {
	s := m.edge.setup
	left := "you"
	right := "authority"
	if !s.join {
		right = "the fleet"
	}
	towerTop := lampStyle(roleLive).Render("▲")
	towerMid := stDim.Render("╱█╲")
	towerBot := stDim.Render("═╩═")

	// The channel is what is left after the two 3-wide towers and a little margin. The carrier
	// is a comet: a bright head with a fading Wave-Spectrum wake behind it and a wavefront just
	// ahead, riding a faint shimmering baseline so the band is alive end to end, never an empty
	// field of dots.
	chW := cw - 12
	if chW < 8 {
		chW = 8
	}
	head := s.frame % chW
	var ch strings.Builder
	for i := 0; i < chW; i++ {
		d := i - head
		switch {
		case d == 0:
			ch.WriteString(spectrumStyle(s.frame).Render("◈")) // packet head
		case d == 1:
			ch.WriteString(spectrumStyle(s.frame + 1).Render("»")) // leading spark
		case d == 2:
			ch.WriteString(spectrumStyle(s.frame + 2).Render("›"))
		case d == -1:
			ch.WriteString(spectrumStyle(s.frame - 1).Render("◉")) // hot trail
		case d == -2:
			ch.WriteString(spectrumStyle(s.frame - 2).Render("○"))
		case d == -3:
			ch.WriteString(spectrumStyle(s.frame - 3).Render("∘")) // fading wake
		default:
			// carrier static: a fine shimmer so the band is alive end to end, never dead dots.
			if (i+s.frame)&1 == 0 {
				ch.WriteString(stDim.Render("·"))
			} else {
				ch.WriteString(stDim.Render("˙"))
			}
		}
	}

	// The base carries a standing carrier wave that scrolls, and - on a join - a fainter ACK
	// spark travels the OTHER way, authority -> you, so the handshake reads as a two-way
	// negotiation rather than one machine shouting. Its glyphs point left (‹«) against the
	// comet's right (»›), so the direction survives NO_COLOR. A new Edge has no remote peer,
	// so it just broadcasts (no ACK).
	waveRunes := []string{"∿", "⌇"}
	wlen := chW - 2
	ackHead := wlen - 1 - (s.frame % max(1, wlen))
	var wave strings.Builder
	for i := 0; i < wlen; i++ {
		switch d := i - ackHead; {
		case s.join && d == 0:
			wave.WriteString(lampStyle(roleSignal).Render("○"))
		case s.join && d == 1:
			wave.WriteString(lampStyle(roleSignal).Render("«"))
		case s.join && d == 2:
			wave.WriteString(lampStyle(roleSignal).Render("‹"))
		default:
			wave.WriteString(spectrumStyle(s.frame + i).Render(waveRunes[(i+s.frame)%2]))
		}
	}
	ring := lampStyle(roleSignal).Render("((") + wave.String() + lampStyle(roleSignal).Render("))")

	labels := edgeSetupCenter(stDim.Render(left)+strings.Repeat(" ", max(1, chW-len(left)-len(right)+6))+stDim.Render(right), cw)
	scene := []string{
		labels,
		"  " + towerTop + strings.Repeat(" ", chW+2) + towerTop,
		" " + towerMid + " " + ch.String() + " " + towerMid,
		" " + towerBot + " " + ring + " " + towerBot,
		"",
		edgeSetupCenter(spectrumStyle(s.frame).Render("• ")+stDim.Render(phase), cw),
	}
	return lipgloss.JoinVertical(lipgloss.Left, scene...)
}

// ── results: allow / done / failed panels ──────────────────────────────────

func (m model) setupAllowScene(cw int) string {
	s := m.edge.setup
	body := []string{
		lampStyle(roleDialGlow).Render("◍ ") + stKey.Render("almost - this machine is not allowed on that Edge yet."),
		"",
		stDim.Render("on the AUTHORITY machine, run:"),
		lampStyle(roleLive).Render("  roger edge authority allow ") + stKey.Render(edgeSetupClip(s.userKey, cw-32)),
		"",
		stDim.Render("that is this machine's user key. once it is allowed, retry below."),
	}
	return edgeSetupPanel(cw, cDialGlow, body)
}

func (m model) setupDoneScene(cw int) string {
	s := m.edge.setup
	st := m.edgeSetupState()
	crown := edgeSetupCenter(edgeSetupSpectrumBar("◉  O N   A I R  ◉", 0), cw-6)
	right := "the fleet"
	if s.join {
		right = "authority"
	}
	lock := edgeSetupLockStrip(cw-6, right)
	if s.join {
		return edgeSetupPanel(cw, cLive, []string{
			crown, "", lock, "",
			lampStyle(roleLive).Render("◉ ") + stKey.Render("on air. this machine is on your Edge."),
			stDim.Render("you joined the Edge. it draws on this screen now."),
		})
	}
	addr := st.AuthorityAddr
	if addr == "" {
		addr = "http://<this machine's LAN address>"
	}
	return edgeSetupPanel(cw, cLive, []string{
		crown, "", lock, "",
		lampStyle(roleLive).Render("◉ ") + stKey.Render("on air. this machine is on your Edge."),
		stDim.Render("your Edge is live, rooted here, mode LOCAL, formed with no internet."),
		"",
		stKey.Render("add a device or agent") + stDim.Render(" - on the other machine, either:"),
		stEmber.Render("  roger edge enroll <name> --authority " + addr),
		stDim.Render("  or run  ") + stEmber.Render("roger edge setup") + stDim.Render("  there and join this Edge at "+addr),
	})
}

// edgeSetupLockStrip is the arrival payoff: the carrier that was sweeping now stands SOLID
// between the two towers with the packet seated at the far mast, and a seal reads CARRIER
// LOCKED. It is the still frame the whole handshake was tuning toward.
func edgeSetupLockStrip(w int, right string) string {
	beam := w - 10
	if beam < 8 {
		beam = 8
	}
	half := beam / 2
	line := spectrumStyle(2).Render(strings.Repeat("═", half)) +
		lampStyle(roleLive).Render("◈") +
		spectrumStyle(5).Render(strings.Repeat("═", beam-half-1))
	tower := stDim.Render("▲")
	labels := edgeSetupCenter(stDim.Render("you")+strings.Repeat(" ", max(1, beam-len(right)))+stDim.Render(right), w)
	strip := edgeSetupCenter(tower+" "+line+" "+tower, w)
	seal := edgeSetupCenter(lampStyle(roleSignal).Render("◖▣▣▣▣▣◗ ")+stKey.Render("CARRIER LOCKED")+stDim.Render(" · certificate seated"), w)
	return lipgloss.JoinVertical(lipgloss.Left, labels, strip, seal)
}

func (m model) setupFailedScene(cw int) string {
	s := m.edge.setup
	body := []string{lampStyle(roleLive).Render("✕ ") + stKey.Render("that did not go through:")}
	for _, ln := range strings.Split(s.err, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		body = append(body, "  "+stEmber.Render(edgeSetupClip(strings.TrimSpace(ln), cw-6)))
	}
	body = append(body, "", stDim.Render("this machine is not a member yet - try again, or use the commands shown."))
	return edgeSetupPanel(cw, cRule, body)
}

// edgeSetupPanel frames a result in a rounded box in the given accent.
func edgeSetupPanel(cw int, accent lipgloss.TerminalColor, body []string) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Width(cw-2).
		Padding(0, 1).
		Render(lipgloss.JoinVertical(lipgloss.Left, body...))
}

// ── small helpers ──────────────────────────────────────────────────────────

// edgeSetupCenter centres a (possibly styled) string within width, measuring visible width so
// ANSI never throws the maths off.
func edgeSetupCenter(s string, width int) string {
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, s)
}

// edgeSetupSpectrumBar tints a short motif across the Wave Spectrum from an offset, so the
// marquee shimmers in the fleet's own hues.
func edgeSetupSpectrumBar(motif string, off int) string {
	var b strings.Builder
	for i, r := range motif {
		if r == ' ' {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(spectrumStyle(off + i).Render(string(r)))
	}
	return b.String()
}

func edgeSetupClip(key string, room int) string {
	if room < 16 {
		room = 16
	}
	if len(key) > room {
		return key[:room-1] + "…"
	}
	return key
}

func edgeSetupHint(s *edgeSetup) string {
	switch s.step {
	case setupChoose:
		return "↑↓ choose  ·  ⏎ continue  ·  esc leave"
	case setupName, setupAddr:
		return "type  ·  ⏎ continue  ·  esc back"
	case setupRunning:
		return "working..."
	case setupAllow:
		return "⏎/r retry once allowed  ·  esc leave"
	case setupDone:
		return "⏎ done"
	case setupFailed:
		return "⏎/r try again  ·  esc leave"
	}
	return "esc leave"
}

// edgeSetupPlain reports NO_COLOR, read LIVE (not the init-time `quiet`) so a test can drive it
// with t.Setenv, matching how the empty/mode suites flip NO_COLOR. Under it the animated scene
// becomes phase lines: motion the eye cannot colour-track is noise, so we state the phase.
func edgeSetupPlain() bool {
	_, ok := os.LookupEnv("NO_COLOR")
	return ok
}
