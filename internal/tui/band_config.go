package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v6/internal/protocol"
)

// ONE CARD PER BAND.
//
// FOUNDER 2026-08-21: "let's make it easier to setup and config different things for a
// band in a more easier way" -> "yes, one card per band".
//
// THE DIAGNOSIS. Everything about a single model was scattered across four screens, and no
// screen anywhere could answer "how is this band set up?":
//
//	on air / off air ............ [2] SHARE            (a / space)
//	public or private band ...... [2] SHARE            (h)
//	what you EARN + windows ..... [2] SHARE -> p       (modeShareEditor)
//	what you PAY ................ [3] CONFIG           (modeLimits)
//	dial · move · new code ...... BASE STATION [p]     (the band card)
//	can I reach it .............. [1] TUNE IN
//
// Six surfaces for one model. An operator asking a single question had to visit four of
// them and hold the answer in their head, which is exactly how the founder ended up with
// two bands on one node and no idea which was live.
//
// THE CARD is the detail view that was missing: one band, every setting that APPLIES to
// it, each row naming the key that changes it. Sections are conditional - a market band you
// do not serve shows only what you pay; a local model with no band shows no dial - so the
// card is short when the truth is short.
//
// IT OWNS NO EDITOR. Every row routes into the EXISTING editor (modeShareEditor for
// pricing, the limits buffer for spend caps, the band actions for move/rotate/revoke) and
// returns here. Forking those would give the product two implementations of each edit that
// would drift, and the drift would be about money.
//
// The right-hand column names the screen each section came from ([2] SHARE, [3] CONFIG).
// That is deliberate: the card teaches the map instead of replacing it silently, so an
// operator who knows the old route keeps it and one who does not learns it here.

// bandConfigRow is one setting on the card: what it is, what it currently says, and the key
// that changes it. A row with no key is a fact, not a control.
type bandConfigRow struct {
	label, value, key, hint string
}

// openBandConfig opens the card for a model, remembering where to return to.
func (m model) openBandConfig(model string, back mode) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(model) == "" {
		return m, nil
	}
	m.cfgModel = model
	m.cfgReturn, m.cfgReturnSet = back, true
	m.mode = modeBandConfig
	m.status = stDim.Render("everything about ") + stKey.Render(model) + stDim.Render(" in one place")
	return m, m.rescanPrivate()
}

// closeBandConfig returns to whichever list opened the card.
func (m model) closeBandConfig() (tea.Model, tea.Cmd) {
	m.mode = modeBrowse
	if m.cfgReturnSet {
		m.mode = m.cfgReturn
		m.cfgReturn, m.cfgReturnSet = 0, false
	}
	m.cfgModel = ""
	return m, nil
}

// cfgShareRow is the share-table row for the card's model, if this machine serves it.
func (m model) cfgShareRow() (shareRow, bool) {
	for _, r := range m.shareRows {
		if r.model == m.cfgModel {
			return r, true
		}
	}
	return shareRow{}, false
}

// cfgBand is the private band bound to this model on THIS station, if any. Only a LIVE
// band counts: a revoked row is history, and offering its dial here would suggest a code
// that no longer resolves.
func (m model) cfgBand() (BandRow, bool) {
	for _, r := range m.privRows() {
		if r.model == m.cfgModel && r.band.Status == "active" {
			return r.band, true
		}
	}
	return BandRow{}, false
}

func (m model) cfgOnAir() bool { return m.shares[m.cfgModel] != nil }

// bandConfigView renders the card.
func (m model) bandConfigView(w int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString("  " + truncVisible(s, w-2) + "\n") }

	sr, served := m.cfgShareRow()
	bd, banded := m.cfgBand()
	private := m.sharePrivate[m.cfgModel]
	onAir := m.cfgOnAir()

	// HEADER: the model, and the one-phrase answer to "what is this to me right now".
	state := stDim.Render("on the open market")
	switch {
	case served && onAir && private:
		state = stRed.Render(glyphOnAir+" PRIVATE") + stDim.Render(" · on air from this machine")
	case served && onAir:
		state = stRed.Render(glyphOnAir+" ON AIR") + stDim.Render(" · shared from this machine")
	case served:
		state = stDim.Render("on this machine · off air")
	}
	b.WriteString("  " + stSelBar.Render("▌") + " " + stBrand.Render(m.cfgModel) +
		stDim.Render("   ") + state + "\n")
	if !m.shortTerminal() {
		b.WriteString("\n")
	}

	// THIS MACHINE - only when we actually serve it. Claiming a provider section for a
	// band we merely consume would invite an operator to price something they do not own.
	if served {
		m.cfgSection(&b, w, "THIS MACHINE", "[2] SHARE", m.cfgProviderRows(sr, bd, banded, private, onAir))
	}
	// WHAT YOU PAY - only when the band is reachable as a consumer. A purely local model
	// has no price to cap: nothing is metered, and a spend limit on it would be theatre.
	lim := m.limits.resolve(m.cfgModel)
	_, onMarket := m.bandForModel(m.cfgModel)
	if onMarket || !served || lim.MaxOut > 0 || lim.MinTPS > 0 {
		m.cfgSection(&b, w, "WHAT YOU PAY", "[3] CONFIG", m.cfgConsumerRows())
	}
	if !served {
		line(stDim.Render("this machine does not serve "+m.cfgModel+" - put a model on air in ") +
			stKey.Render("[2]") + stDim.Render(" SHARE"))
		b.WriteString("\n")
	}
	return b.String()
}

// cfgSection prints one titled block with its rows, aligned, each naming its key.
func (m model) cfgSection(b *strings.Builder, w int, title, from string, rows []bandConfigRow) {
	head := "  " + stKey.Render(title)
	// The "from [2] SHARE" signpost is the first thing to go on a short terminal. It
	// teaches the map, which is worth a row when there is one to spare and is not worth
	// pushing the frame past the window - a frame taller than the terminal scrolls the alt
	// buffer and strands the previous frame's header (the stacked-logos failure).
	if !m.narrow() && !m.shortTerminal() {
		head += stDim.Render("   from " + from)
	}
	b.WriteString(truncVisible(head, w) + "\n")
	for _, r := range rows {
		key := "  "
		if r.key != "" {
			key = stKey.Render(r.key)
		}
		row := "    " + stDim.Render(pad(r.label, 14)) + pad(r.value, 30) + key
		if r.hint != "" && !m.narrow() {
			row += stDim.Render("  " + r.hint)
		}
		b.WriteString("  " + truncVisible(row, w-2) + "\n")
	}
	if !m.shortTerminal() {
		b.WriteString("\n")
	}
}

// cfgProviderRows is the "what this machine does with it" half.
func (m model) cfgProviderRows(sr shareRow, bd BandRow, banded, private, onAir bool) []bandConfigRow {
	air := stDim.Render("no")
	if onAir && private {
		air = stRed.Render(glyphOnAir) + stLive.Render(" yes, privately")
	} else if onAir {
		air = stLive.Render("yes, on the open market")
	}
	rows := []bandConfigRow{
		{label: "on air", value: air, key: "a", hint: "toggle"},
		{label: "visibility", value: cfgVisibility(private), key: "h", hint: cfgVisibilityHint(private)},
	}
	if banded {
		// The DIAL only - never the code. Only its hash is stored, so there is nothing to
		// show, and a placeholder would read as the real thing.
		rows = append(rows,
			bandConfigRow{label: "band", value: stKey.Render(bandDial(bd)), key: "n", hint: "new code"},
			bandConfigRow{label: "name", value: cfgLabel(bd), key: "l", hint: "name this band"},
		)
	}
	rows = append(rows,
		bandConfigRow{label: "served by", value: stDim.Render(cfgUpstream(sr))},
		bandConfigRow{label: "variant", value: cfgVariant(sr)},
		bandConfigRow{label: "you earn", value: cfgEarn(m.pricingFor(m.cfgModel)), key: "p", hint: "set price + windows"},
	)
	return rows
}

// cfgVariant renders what detection read off this machine for THIS row's model - the
// compression label, who produced the weights, and the flavor. It is the operator's only
// view of what the market will see them as, which is why it states its own absence rather
// than hiding: a missing row cannot tell "this model published no metadata" apart from
// "detection is broken". Nothing here is ever inferred from the model NAME alone beyond
// what detect already vouches for - an empty field renders as absent, never as a guess.
func cfgVariant(sr shareRow) string {
	parts := []string{}
	if sr.quant != "" {
		parts = append(parts, stKey.Render(sr.quant))
	}
	if sr.weights != "" {
		parts = append(parts, stDim.Render("by ")+sr.weights)
	}
	if sr.variant != "" {
		parts = append(parts, sr.variant)
	}
	if len(parts) == 0 {
		if strings.TrimSpace(sr.upstream) == "" {
			return stDim.Render("—")
		}
		return stDim.Render("nothing detected · shares as the plain model id")
	}
	return strings.Join(parts, stDim.Render(" · "))
}

// cfgConsumerRows is the "what you are willing to pay" half - the [3] CONFIG fields.
func (m model) cfgConsumerRows() []bandConfigRow {
	lim := m.limits.resolve(m.cfgModel)
	return []bandConfigRow{
		{label: "max $/1M out", value: cfgLimit(lim.MaxOut), key: "e", hint: "cap what a turn may cost"},
		{label: "min t/s", value: cfgLimit(lim.MinTPS), key: "t", hint: "refuse stations slower than this"},
		{label: "quants", value: cfgQuants(lim.Quants), key: "Q", hint: "only these weights, everywhere"},
	}
}

// cfgQuants renders the accepted-quant rule. "any" is the default and is stated as a WORD:
// a blank cell here would read as "nothing allowed" on the one row where that would be a
// catastrophic misreading.
func cfgQuants(qs []string) string {
	if len(qs) == 0 {
		return stDim.Render("any")
	}
	return stKey.Render(strings.Join(qs, " "))
}

func cfgVisibility(private bool) string {
	if private {
		return stRed.Render("private")
	}
	return stDim.Render("public")
}

// cfgVisibilityHint carries the CONSEQUENCE, which is the half that matters and the half
// that used to be clipped out of the value column.
func cfgVisibilityHint(private bool) string {
	if private {
		return "hidden · only its code can tune it"
	}
	return "listed on the open market"
}

// cfgHost trims an upstream to host:port. The full chat-completions URL is the least
// interesting 30 characters on the card and it was pushing everything else off the row;
// what an operator wants here is "which server", not the path.
func cfgHost(raw string) string {
	s := raw
	for _, p := range []string{"http://", "https://"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexByte(s, '/'); i > 0 {
		s = s[:i]
	}
	if s == "" {
		return raw
	}
	return s
}

func cfgLabel(bd BandRow) string {
	if s := strings.TrimSpace(bd.Label); s != "" {
		return stKey.Render(s)
	}
	return stDim.Render("(unnamed)")
}

func cfgUpstream(sr shareRow) string {
	if strings.TrimSpace(sr.upstream) == "" {
		return "no local server detected"
	}
	return cfgHost(sr.upstream)
}

// cfgEarn renders the provider price. FREE is stated as a word, never as $0.00 - a printed
// zero reads as a measured charge rather than the absence of one.
func cfgEarn(p Pricing) string {
	if p.In <= 0 && p.Out <= 0 {
		if len(p.Windows) > 0 {
			return stLive.Render("free") + stDim.Render(" · with time-of-use windows")
		}
		return stLive.Render("free")
	}
	return stEmber.Render("↑"+money(p.In)+"  ↓"+money(p.Out)) + stDim.Render(" /1M")
}

// cfgLimit renders a spend cap. An UNSET cap is "-", never 0: zero would read as "refuse
// everything", which is the opposite of what no cap means.
func cfgLimit(v float64) string {
	if v <= 0 {
		return stDim.Render("-") + stDim.Render("  (no cap)")
	}
	return stKey.Render(trimZero(v))
}

// onBandConfigKey drives the card. EVERY branch routes into the editor that already owns
// that setting and comes back here - the card is a hub, not a second implementation.
func (m model) onBandConfigKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	sr, served := m.cfgShareRow()
	_ = sr
	switch k.String() {
	case "esc", "q", "left":
		return m.closeBandConfig()
	case "r":
		return m, m.rescanPrivate()
	case "enter":
		// USE IT. The card is where an operator ends up asking "so can I talk to it?", and
		// the answer must be one key from here rather than a trip back to the dial.
		return m.cfgUse()
	case "a", " ", "space":
		if !served {
			m.status = stDim.Render("this machine does not serve " + m.cfgModel + " - nothing to put on air")
			return m, nil
		}
		return m.cfgToggleOnAir()
	case "h", "H":
		if !served {
			m.status = stDim.Render("only a model on THIS machine can be hidden onto a private band")
			return m, nil
		}
		return m.cfgTogglePrivate()
	case "p", "P":
		if !served {
			m.status = stDim.Render("you can only price a model you serve")
			return m, nil
		}
		return m.cfgOpenPricing()
	case "e", "E":
		return m.cfgEditLimit(0)
	case "t", "T":
		return m.cfgEditLimit(1)
	case "Q":
		// UPPERCASE, because lowercase q already closes the card - and because it matches
		// the dial's Q, which filters by quant. Same letter, same subject, one is a view
		// and the other is the rule.
		//
		// The STANDING quant rule for this band. It lives on the card because the card is
		// where everything about a band lives - and beside the spend caps because it is
		// the same kind of statement: what this operator will accept being routed to.
		m.cfgLabelIn.SetValue(strings.Join(m.limits.resolve(m.cfgModel).Quants, " "))
		m.cfgLabelIn.CursorEnd()
		m.cfgLabelIn.Focus()
		m.mode = modeBandQuants
		m.status = stDim.Render("accepted quants · space-separated · empty = any")
		return m, textinput.Blink
	case "n", "N":
		bd, ok := m.cfgBand()
		if !ok {
			m.status = stDim.Render("no private band on this model yet - press ") + stKey.Render("h") +
				stDim.Render(" to hide it onto one")
			return m, nil
		}
		return m.openBandRotateConfirm(bd), nil
	case "l", "L":
		bd, ok := m.cfgBand()
		if !ok {
			m.status = stDim.Render("only a private band can be named - press ") + stKey.Render("h") + stDim.Render(" first")
			return m, nil
		}
		m.bandManageID, m.bandManageDisp = bd.ID, bd.Display
		m.cfgLabelIn.SetValue(bd.Label)
		m.cfgLabelIn.CursorEnd()
		m.cfgLabelIn.Focus()
		m.mode = modeBandLabel
		m.status = stDim.Render("name this band · ⏎ save · esc cancel")
		return m, textinput.Blink
	}
	return m, nil
}

// cfgUse opens a channel on this band - direct when it runs here, and the ordinary tune-in
// otherwise. It reuses the PRIVATE tab's opener so the two cannot disagree about whether a
// band is reachable.
func (m model) cfgUse() (tea.Model, tea.Cmd) {
	for _, r := range m.privRows() {
		if r.model == m.cfgModel && r.band.Status == "active" {
			mm, cmd, _ := m.tuneInPrivateRow(r)
			return mm, cmd
		}
	}
	// No private band: it is an ordinary market band, so hand it to the dial rather than
	// inventing a second connect path here.
	if b, ok := m.bandForModel(m.cfgModel); ok {
		m.cfgModel, m.cfgReturn, m.cfgReturnSet = "", 0, false
		m.mode = modeBrowse
		m.tuneTab = tabOpenMarket
		for i, vb := range m.visibleBands() {
			if vb.model == b.model {
				m.cursor = i
				break
			}
		}
		return m.connect()
	}
	m.status = stDim.Render("nothing is serving " + m.cfgModel + " right now")
	return m, nil
}

func (m model) cfgToggleOnAir() (tea.Model, tea.Cmd) {
	for i, r := range m.shareRows {
		if r.model != m.cfgModel {
			continue
		}
		mm := &m
		mm.toggleShareAt(i) // the SAME call [2] SHARE makes - one behaviour, two doors
		m = *mm
		return m, nil
	}
	return m, nil
}

func (m model) cfgTogglePrivate() (tea.Model, tea.Cmd) {
	for i, r := range m.shareRows {
		if r.model != m.cfgModel {
			continue
		}
		mm := &m
		mm.togglePrivateAt(i)
		m = *mm
		// togglePrivateAt routes a fresh mint to the one-time code card. Remember to come
		// back HERE afterwards rather than dropping the operator on the share table they
		// never opened.
		if m.mode == modeBandCard {
			m.bandCardReturn, m.bandCardReturnSet = modeBandConfig, true
		}
		return m, nil
	}
	return m, nil
}

// cfgOpenPricing hands off to the real pricing + windows editor (modeShareEditor), with the
// share cursor parked on this model so the editor prices the right row.
func (m model) cfgOpenPricing() (tea.Model, tea.Cmd) {
	for i, r := range m.shareRows {
		if r.model != m.cfgModel {
			continue
		}
		m.shareCursor = i
		return m.enterShareEditor()
	}
	return m, nil
}

// cfgEditLimit opens the [3] CONFIG spend-limit editor on this band's row, field 0 (max
// $/1M out) or 1 (min t/s), so the edit uses the same buffer and the same save path.
func (m model) cfgEditLimit(field int) (tea.Model, tea.Cmd) {
	mm := &m
	mm.enterLimits() // the SAME builder [3] CONFIG uses, so the row set can never differ
	m = *mm
	for i, mdl := range m.limModels {
		if mdl != m.cfgModel {
			continue
		}
		m.limCursor = i
		m.editField = field
		m.editBuf = ""
		if lim := m.limits.resolve(m.cfgModel); field == 0 && lim.MaxOut > 0 {
			m.editBuf = trimZero(lim.MaxOut)
		} else if field == 1 && lim.MinTPS > 0 {
			m.editBuf = trimZero(lim.MinTPS)
		}
		m.mode = modeLimits
		m.limReturn, m.limReturnSet = modeBandConfig, true
		m.status = stDim.Render("type a number · ⏎ save · esc cancel")
		return m, nil
	}
	m.status = stDim.Render("that band has no spend row yet - it appears once it is on the dial")
	return m, nil
}

// onBandLabelKey drives the name-this-band input.
func (m model) onBandLabelKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.cfgLabelIn.Blur()
		m.mode = modeBandConfig
		return m, nil
	case "enter":
		label := strings.TrimSpace(m.cfgLabelIn.Value())
		m.cfgLabelIn.Blur()
		m.mode = modeBandConfig
		return m, m.labelBand(m.bandManageID, label)
	}
	var c tea.Cmd
	m.cfgLabelIn, c = m.cfgLabelIn.Update(k)
	return m, c
}

// bandLabelView is the small naming input, rendered over the card.
func (m model) bandLabelView(w int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString("  " + truncVisible(s, w-2) + "\n") }
	b.WriteString("\n" + stHeadRule.Render(strings.Repeat("─", w)) + "\n")
	line(stKey.Render("NAME THIS BAND") + stDim.Render("   "+m.bandManageDisp))
	b.WriteString("\n")
	line(stDim.Render("a band's own name - what it is FOR, so the list stops identifying it"))
	line(stDim.Render("by its id. \"home gpu\", \"friends\", \"the laptop\"."))
	b.WriteString("\n")
	line("  " + m.cfgLabelIn.View())
	b.WriteString("\n")
	line(stDim.Render("⏎ save · esc cancel · an empty name clears it"))
	return b.String()
}

// onBandQuantsKey drives the accepted-quants input.
func (m model) onBandQuantsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.cfgLabelIn.Blur()
		m.mode = modeBandConfig
		return m, nil
	case "enter":
		qs := parseQuantList(m.cfgLabelIn.Value())
		m.cfgLabelIn.Blur()
		m.mode = modeBandConfig
		lim := m.limits.resolve(m.cfgModel)
		lim.Quants = qs
		m.limits.Set(m.cfgModel, lim)
		if len(qs) == 0 {
			m.status = stDim.Render("any quant accepted for ") + stKey.Render(m.cfgModel)
			return m, nil
		}
		m.status = stLive.Render("rule set") + stDim.Render(" - ") + stKey.Render(m.cfgModel) +
			stDim.Render(" will only be served at ") + stKey.Render(strings.Join(qs, " "))
		return m, nil
	}
	var c tea.Cmd
	m.cfgLabelIn, c = m.cfgLabelIn.Update(k)
	return m, c
}

// parseQuantList turns what the operator typed into the rule.
//
// Upper-cased and deduped because a quant is a NAME: someone typing "q4_k_m q4_k_m" means
// one thing once, and it has to match the label a station advertises, which Normalize also
// upper-cases. Splitting on both spaces and commas is deliberate - people write lists both
// ways and neither is wrong enough to reject.
func parseQuantList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t'
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		// The SAME canonicaliser the offer's label goes through. Upper-casing here would
		// store an MLX rule as "4BIT" while the feed carries "4bit", so a rule the
		// operator typed exactly as published would match nothing.
		f = protocol.CanonicalQuant(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// bandQuantsView is the small input for the accepted-quant rule.
func (m model) bandQuantsView(w int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString("  " + truncVisible(s, w-2) + "\n") }
	b.WriteString("\n" + stHeadRule.Render(strings.Repeat("─", w)) + "\n")
	line(stKey.Render("ACCEPTED QUANTS") + stDim.Render("   "+m.cfgModel))
	b.WriteString("\n")
	line(stDim.Render("only serve this band at these weights - a RULE, not a filter: it binds"))
	line(stDim.Render("the agent and roger use too, not just what you are looking at."))
	b.WriteString("\n")
	line("  " + m.cfgLabelIn.View())
	b.WriteString("\n")
	if qs := m.quantsOnAir(); len(qs) > 0 {
		line(stDim.Render("on the dial now: ") + stKey.Render(strings.Join(qs, " ")))
	}
	line(stDim.Render("⏎ save · esc cancel · EMPTY accepts any quant"))
	return b.String()
}
