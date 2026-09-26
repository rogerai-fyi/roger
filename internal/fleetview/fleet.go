package fleetview

// The Edge FLEET screen: this machine as the hub, the rogers running on it, the members it has
// adopted and the devices asking to be - drawn as a map with a readout for whatever is selected,
// and the actions that manage them in place (adopt, rename, forget, where a turn goes first).
// Actions are functions handed in by the caller; the screen never reaches into the Edge itself.

import (
	"context"
	"fmt"
	"strings"
	"time"

	lm "github.com/thebanri/limoni"
	"github.com/thebanri/limoni/widgets"

	"rogerai.fm/roger/v6/internal/edge"
)

// ItemKind is what a line on the map is.
type ItemKind int

const (
	ItemHub       ItemKind = iota // this machine
	ItemInstance                  // a roger running here
	ItemAgent                     // an agent roger running here (or a persistent one, dark)
	ItemMember                    // another machine on this Edge
	ItemCandidate                 // a discovered device not yet on this Edge
)

// Cap is one capability as a node declares it: CLAIMED or VERIFIED.
type Cap struct{ Name, State string }

// FleetItem is one line of the map with everything its readout needs.
type FleetItem struct {
	Kind     ItemKind
	ID       string // node id (hub, member, candidate)
	Name     string
	NodeKind string // host | board | mobile
	Presence string // "online", "offline 1d", "discovered"
	Dark     bool
	Self     bool // the roger you are looking at
	Bands    []string
	Job      string
	Caps     []Cap
	Via      string
	LastSeen time.Time
	Health   *edge.NodeHealth
	Alerts   []string
}

// SelfFacts is this machine's place on its Edge, worded by the caller (the same lines the CLI
// and the console print).
type SelfFacts struct {
	Name, Root, Authority, Prefer, PreferValue, Machine, Discovery, Join string
	AuthorityLines                                                       []string // the readout's full account
	Enrolled                                                             bool
}

// FleetSnapshot is one read of the whole Edge.
type FleetSnapshot struct {
	Self  SelfFacts
	Items []FleetItem // draw order: hub, its rogers, members, candidates
	At    time.Time
}

// FleetSource reads a snapshot.
type FleetSource func() (FleetSnapshot, error)

// FleetActions are what the keys do. A nil action is not offered.
type FleetActions struct {
	Adopt     func(id, name string) (string, error) // returns the line to show
	Forget    func(id string) error
	Rename    func(id, name string) error
	SetPrefer func(want string) error
}

// Fleet is the screen.
type Fleet struct {
	Source  FleetSource
	Actions FleetActions
	Every   time.Duration
	health  *Health // the HEALTH tab, when given

	snap     FleetSnapshot
	err      error
	readAt   time.Time
	sel      int
	tab      int
	frame    int // advances each frame; the pulses breathe on it
	confirm  bool
	rename   bool
	input    []rune
	toast    string
	toastAt  time.Time
	toastErr bool
}

// NewFleet makes the screen over src, with health as its second tab (nil = no tab).
func NewFleet(src FleetSource, health *Health, acts FleetActions) *Fleet {
	if health != nil {
		health.BackHint = "1 fleet"
	}
	return &Fleet{Source: src, Actions: acts, Every: 2 * time.Second, health: health}
}

// Run opens the screen full-screen until q/esc or ctx ends.
func (f *Fleet) Run(ctx context.Context) error {
	return lm.RunWithContext(ctx, f.Draw, lm.WithTitle("roger edge"), lm.WithFPS(6))
}

// Draw is one frame.
func (f *Fleet) Draw(fr *lm.Frame, ev *lm.Event) bool {
	f.frame++
	if ev != nil && ev.Type == lm.EventKey {
		if !f.onKey(ev) {
			return false
		}
	}
	if f.tab == 1 && f.health != nil {
		return f.health.Draw(fr, nil)
	}
	f.refresh()
	f.clamp()

	rows := lm.SplitVertical(fr.Area(), lm.Fixed(3), lm.Fill(), lm.Fixed(1))
	f.drawHeader(fr, rows[0])
	if f.err != nil {
		fr.RenderWidget(lm.NewBlock().Rounded().WithTitle(" could not read the Edge ").WithBorderStyle(lm.Fg(live)).
			WithChild(lm.NewParagraph(f.err.Error()).WithStyle(lm.Fg(ink))).WithPadding(0, 1, 0, 1), rows[1])
	} else {
		cols := lm.SplitHorizontal(rows[1], lm.Percentage(56), lm.Percentage(44))
		f.drawMap(fr, cols[0])
		f.drawReadout(fr, cols[1])
	}
	fr.RenderWidget(f.actionBar(), rows[2])
	if f.confirm {
		f.drawConfirm(fr)
	}
	return true
}

// onKey handles one key; false ends the screen.
func (f *Fleet) onKey(ev *lm.Event) bool {
	k := ev.Key
	switch {
	case f.rename:
		switch {
		case k.Type == lm.KeyEsc:
			f.rename = false
		case k.Type == lm.KeyEnter:
			name := strings.TrimSpace(string(f.input))
			it := f.selected()
			f.rename = false
			if name != "" && f.Actions.Rename != nil {
				f.report(f.Actions.Rename(it.ID, name), "renamed to "+name)
			}
		case k.Type == lm.KeyBackspace:
			if len(f.input) > 0 {
				f.input = f.input[:len(f.input)-1]
			}
		case k.Ch != 0:
			f.input = append(f.input, k.Ch)
		}
		return true
	case f.confirm:
		if k.Ch == 'y' || k.Ch == 'Y' {
			it := f.selected()
			f.confirm = false
			if f.Actions.Forget != nil {
				f.report(f.Actions.Forget(it.ID), "forgot "+it.Name)
			}
		} else {
			f.confirm = false
			f.say("left alone", false)
		}
		return true
	}
	if f.tab == 1 && f.health != nil {
		switch {
		case k.Ch == '1' || k.Type == lm.KeyTab:
			f.tab = 0
			return true
		case k.Ch == 'q' || k.Ch == 'Q' || k.Type == lm.KeyEsc:
			return false
		}
		return f.health.Draw(nil, ev) || true
	}
	switch {
	case k.Ch == 'q' || k.Ch == 'Q' || k.Type == lm.KeyEsc:
		return false
	case k.Ch == '2' || k.Type == lm.KeyTab:
		if f.health != nil {
			f.tab = 1
		}
	case k.Ch == 'r':
		f.readAt = time.Time{}
	case k.Type == lm.KeyDown || k.Ch == 'j':
		f.sel++
	case k.Type == lm.KeyUp || k.Ch == 'k':
		f.sel--
	case k.Ch == 'a':
		if it := f.selected(); it.Kind == ItemCandidate && f.Actions.Adopt != nil {
			msg, err := f.Actions.Adopt(it.ID, it.Name)
			if err != nil {
				f.say(err.Error(), true)
			} else {
				f.say(msg, false)
			}
			f.readAt = time.Time{}
		}
	case k.Ch == 'f':
		if it := f.selected(); it.Kind == ItemMember && f.Actions.Forget != nil {
			f.confirm = true
		}
	case k.Ch == 'n':
		if it := f.selected(); it.Kind == ItemMember && f.Actions.Rename != nil {
			f.rename, f.input = true, nil // typed fresh; the old name stays in view as the hint
		}
	case k.Ch == 'p':
		if f.Actions.SetPrefer != nil {
			next := nextPrefer(f.snap.Self.PreferValue)
			if err := f.Actions.SetPrefer(next); err != nil {
				f.say(err.Error(), true)
			} else {
				f.snap.Self.PreferValue, f.snap.Self.Prefer = next, "prefers "+next
				f.say("turns go "+next+" first", false)
			}
		}
	}
	return true
}

func nextPrefer(cur string) string {
	switch cur {
	case "market":
		return "local-only"
	case "local-only":
		return "local"
	default:
		return "market"
	}
}

func (f *Fleet) report(err error, ok string) {
	if err != nil {
		f.say(err.Error(), true)
	} else {
		f.say(ok, false)
	}
	f.readAt = time.Time{}
}

func (f *Fleet) say(s string, isErr bool) { f.toast, f.toastAt, f.toastErr = s, time.Now(), isErr }

func (f *Fleet) refresh() {
	if !f.readAt.IsZero() && time.Since(f.readAt) < f.Every {
		return
	}
	snap, err := f.Source()
	f.readAt, f.err = time.Now(), err
	if err == nil {
		f.snap = snap
	}
}

func (f *Fleet) clamp() {
	if f.sel >= len(f.snap.Items) {
		f.sel = len(f.snap.Items) - 1
	}
	if f.sel < 0 {
		f.sel = 0
	}
}

func (f *Fleet) selected() FleetItem {
	if len(f.snap.Items) == 0 {
		return FleetItem{}
	}
	return f.snap.Items[f.sel]
}

// --- drawing ---------------------------------------------------------------------------------

func span(s string, c lm.Color) lm.Span { return lm.NewSpan(s, lm.Fg(c)) }
func bold(s string, c lm.Color) lm.Span { return lm.NewSpan(s, lm.Fg(c).Bold()) }

func (f *Fleet) drawHeader(fr *lm.Frame, area lm.Rect) {
	rows := lm.SplitVertical(area, lm.Fixed(1), lm.Fixed(1), lm.Fixed(1))
	title := lm.NewLine(span(" ▟•▙ ", live), bold(" R O G E R", paper), span(" · ", live), bold("E D G E ", paper),
		span("  ◉ ON AIR", live))
	if f.health != nil {
		tabs := []lm.Span{span("     ", dim)}
		for i, t := range []string{"1 FLEET", "2 HEALTH"} {
			if i == f.tab {
				tabs = append(tabs, bold(" "+t+" ", live))
			} else {
				tabs = append(tabs, span("  "+t+"  ", dim))
			}
		}
		title.Spans = append(title.Spans, tabs...)
	}
	fr.RenderWidget(lm.NewText(title).WithWrap(false), rows[0])

	facts := lm.NewLine(span(" EDGE · ", muted), bold(f.snap.Self.Root, gold), span(" · ", muted), span(f.snap.Self.Prefer, teal))
	if n := len(f.snap.Items); n > 0 {
		members, cands, alerts := 0, 0, 0
		for _, it := range f.snap.Items {
			switch it.Kind {
			case ItemMember:
				members++
			case ItemCandidate:
				cands++
			}
			alerts += len(it.Alerts)
		}
		facts.Spans = append(facts.Spans, span(fmt.Sprintf(" · %d member%s", members, plural(members)), muted))
		if cands > 0 {
			facts.Spans = append(facts.Spans, span(fmt.Sprintf(" · %d to adopt", cands), gold))
		}
		if alerts > 0 {
			facts.Spans = append(facts.Spans, bold(fmt.Sprintf(" · ⚠ %d alert%s", alerts, plural(alerts)), live))
		}
	}
	fr.RenderWidget(lm.NewText(facts).WithWrap(false), rows[1])

	// A warm rule under the head: red fading to gold to the dark - or, while an action's
	// outcome is fresh, that outcome, where the eye already is.
	if f.toast != "" && time.Since(f.toastAt) < 8*time.Second {
		c := mint
		if f.toastErr {
			c = live
		}
		fr.RenderWidget(lm.NewText(lm.NewLine(span(" ▸ ", c), bold(f.toast, c))).WithWrap(false), rows[2])
		return
	}
	w := int(area.Width)
	rule := lm.NewLine()
	for _, seg := range []struct {
		n int
		c lm.Color
	}{{w / 6, live}, {w / 6, ember}, {w / 6, gold}, {w / 2, dim}} {
		if seg.n > 0 {
			rule.Spans = append(rule.Spans, span(strings.Repeat("─", seg.n), seg.c))
		}
	}
	fr.RenderWidget(lm.NewText(rule).WithWrap(false), rows[2])
}

// glyph is an item's mark and its color.
func (it FleetItem) glyph(frame int) (string, lm.Color) {
	switch it.Kind {
	case ItemHub:
		return "◉", live
	case ItemInstance:
		return "◆", ember
	case ItemAgent:
		if it.Dark {
			return "◇", ash
		}
		return "◆", gold
	case ItemMember:
		if it.Dark {
			return "▯", ash
		}
		return "▣", kindColor(it.NodeKind)
	default:
		if frame/3%2 == 0 { // a discovered device asks: it blinks, softly
			return "◈", gold
		}
		return "◇", gold
	}
}

// sub is the item's one-line description under its name on the map.
func (it FleetItem) sub(self SelfFacts) string {
	tag := alertTag(it.Alerts)
	switch it.Kind {
	case ItemHub:
		s := self.Authority
		if tag != "" {
			s += " · " + tag
		}
		return s
	case ItemInstance:
		if it.Self {
			return "‹ this one"
		}
		if len(it.Bands) > 0 {
			return "serves " + fewBands(it.Bands)
		}
		return "roger"
	case ItemAgent:
		switch {
		case it.Dark:
			return "agent · offline"
		case it.Job != "":
			return it.Job
		case len(it.Bands) > 0:
			return "agent · serves " + fewBands(it.Bands)
		}
		return "agent · idle"
	case ItemMember:
		s := "member · " + it.Presence
		if tag != "" {
			s += " · " + tag
		}
		return s
	default:
		return "adopt with a"
	}
}

// fewBands names the first two models and counts the rest, so a card line stays a line.
func fewBands(b []string) string {
	if len(b) <= 2 {
		return strings.Join(b, " ")
	}
	return strings.Join(b[:2], " ") + fmt.Sprintf(" +%d", len(b)-2)
}

func alertTag(a []string) string {
	switch len(a) {
	case 0:
		return ""
	case 1:
		return "1 alert"
	}
	return fmt.Sprintf("%d alerts", len(a))
}

func (f *Fleet) drawMap(fr *lm.Frame, area lm.Rect) {
	var lines []lm.Line
	band := func(title string, c lm.Color) {
		lines = append(lines, lm.NewLine(), lm.NewLine(span("  ", dim), bold(title, c), span("  "+strings.Repeat("┈", 40), dim)))
	}
	seen := map[ItemKind]bool{}
	for i, it := range f.snap.Items {
		switch {
		case it.Kind == ItemMember && !seen[ItemMember]:
			band("ON THE EDGE", sky)
			lines = append(lines, lm.NewLine(span("  other machines you have adopted", dim)))
		case it.Kind == ItemCandidate && !seen[ItemCandidate]:
			band("DISCOVERED", gold)
			lines = append(lines, lm.NewLine(span("  on this network, not yet on your Edge", dim)))
		}
		seen[it.Kind] = true
		g, c := it.glyph(f.frame)
		cursor, nameC := "  ", ink
		if i == f.sel {
			cursor, nameC = "▶ ", paper
		}
		indent := ""
		switch it.Kind {
		case ItemInstance, ItemAgent:
			indent = "   ├─ "
			if i+1 >= len(f.snap.Items) || (f.snap.Items[i+1].Kind != ItemInstance && f.snap.Items[i+1].Kind != ItemAgent) {
				indent = "   ╰─ "
			}
		case ItemMember, ItemCandidate:
			indent = "   "
		}
		name := it.Name
		if it.Dark {
			nameC = ash
		}
		l := lm.NewLine(span(cursor, live), span(indent, dim), bold(g+" ", c), bold(pad(name, 22), nameC))
		// The description: presence in its color, a live pulse for what is online.
		subC := muted
		if it.Kind == ItemMember && !it.Dark || it.Kind == ItemHub {
			subC = mint
			if f.frame/4%2 == 0 {
				subC = teal
			}
		}
		if it.Kind == ItemCandidate {
			subC = gold
		}
		l.Spans = append(l.Spans, span(it.sub(f.snap.Self), subC))
		if len(it.Alerts) > 0 {
			l.Spans = append(l.Spans, bold("  ⚠", live))
		}
		lines = append(lines, l)
		if it.Kind == ItemHub {
			lines = append(lines, lm.NewLine(span("     │", dim)))
		}
	}
	if len(f.snap.Items) <= 1 {
		lines = append(lines, lm.NewLine(), lm.NewLine(span("   ┈┈ no rogers running here yet ┈┈", dim)))
	}
	title := " YOUR EDGE NETWORK "
	fr.RenderWidget(lm.NewBlock().Rounded().WithTitle(title).WithBorderStyle(lm.Fg(muted)).
		WithChild(lm.NewText(lines...).WithWrap(false)).WithPadding(0, 1, 0, 1), area)
}

// wrapWords breaks s on spaces into lines no wider than w (a single word wider than w stands alone).
func wrapWords(s string, w int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" || len(out) == 0 {
		out = append(out, line)
	}
	return out
}

func pad(s string, n int) string {
	if len([]rune(s)) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len([]rune(s)))
}

// drawReadout is the selected item in full, with what can be done to it.
func (f *Fleet) drawReadout(fr *lm.Frame, area lm.Rect) {
	it := f.selected()
	var lines []lm.Line
	kv := func(k string, v ...lm.Span) {
		l := lm.NewLine(span(pad(k, 11), muted))
		l.Spans = append(l.Spans, v...)
		lines = append(lines, l)
	}
	width := int(area.Width) - 4 - 11 // inside the border and padding, after the label
	if width < 20 {
		width = 20
	}
	// kvText is kv for one plain value, wrapped on words so nothing is cut at the pane edge.
	kvText := func(k, v string, c lm.Color) {
		for i, part := range wrapWords(v, width) {
			if i > 0 {
				k = ""
			}
			kv(k, span(part, c))
		}
	}
	title, tc := it.Name, ink
	switch it.Kind {
	case ItemHub:
		title, tc = it.Name+" · this machine", live
		kvText("ROLE", "the authority hub · "+f.snap.Self.Root, gold)
		if f.snap.Self.Machine != "" {
			kvText("MACHINE", f.snap.Self.Machine, ink)
		}
		if len(f.snap.Self.AuthorityLines) == 0 {
			kvText("AUTHORITY", f.snap.Self.Authority, ink)
		}
		for i, ln := range f.snap.Self.AuthorityLines {
			k := ""
			if i == 0 {
				k = "AUTHORITY"
			}
			kvText(k, ln, ink)
		}
		kv("TURNS", span(f.snap.Self.Prefer, teal), span("  (p to change)", dim))
		if f.snap.Self.Discovery != "" {
			kvText("DISCOVERY", f.snap.Self.Discovery, ink)
		}
		if f.snap.Self.Join != "" {
			kvText("JOIN", "another machine runs  "+f.snap.Self.Join, ink)
		}
	case ItemInstance, ItemAgent:
		title, tc = it.Name+" · roger", ember
		if it.Kind == ItemAgent {
			title, tc = it.Name+" · agent", gold
		}
		switch {
		case it.Dark:
			kv("DOES", span("offline - its process is not running", ash))
		case it.Kind == ItemAgent:
			kv("DOES", span("agent · can act on other nodes on the Edge", ink))
		case len(it.Bands) > 0:
			kv("DOES", span("serves a model to the Edge", ink))
		default:
			kv("DOES", span("on the Edge · not an agent, not serving", ink))
		}
		if it.Job != "" {
			kv("JOB", bold(it.Job, gold))
		}
		if it.Self {
			kv("YOU", span("this is the roger you are looking at", ink))
		}
	case ItemMember:
		title, tc = it.Name+" · "+kindLabel(it.NodeKind), kindColor(it.NodeKind)
		pc := mint
		if it.Dark {
			pc = ash
		}
		kv("PRESENCE", bold(it.Presence, pc))
		if !it.LastSeen.IsZero() {
			kv("LAST SEEN", span(edge.Age(time.Since(it.LastSeen))+" ago", ink))
		}
		if it.Via != "" {
			kv("REACHED", span("through "+it.Via, ink))
		}
	case ItemCandidate:
		title, tc = it.Name+" · "+kindLabel(it.NodeKind), gold
		kv("STATUS", bold("not on your Edge", gold), span(" · press a to adopt it", dim))
	}
	if len(it.Bands) > 0 {
		kv("ON AIR", span(strings.Join(it.Bands, "  "), teal))
	}
	if len(it.Caps) > 0 {
		chips := []lm.Span{}
		for _, c := range it.Caps {
			if c.State == "VERIFIED" {
				chips = append(chips, bold(" "+strings.ToUpper(c.Name)+" ", capColor(c.Name)), span(" ", dim))
			} else {
				chips = append(chips, span(" "+c.Name+" ", capColor(c.Name)), span("· ", dim))
			}
		}
		kv("CAN DO", chips...)
	}
	if it.Health != nil && it.Health.Readings != nil {
		r := it.Health.Readings
		var hs []lm.Span
		if r.CPUTempC != nil {
			c := mint
			if *r.CPUTempC > 85 {
				c = live
			}
			hs = append(hs, bold(fmt.Sprintf("%.1f°C", *r.CPUTempC), c), span("  ", dim))
		}
		if r.RAMTotalGB > 0 {
			hs = append(hs, span(fmt.Sprintf("mem %.0f/%.0f GB", r.RAMUsedGB, r.RAMTotalGB), ink), span("  ", dim))
		}
		if l, ok := it.Health.DiskLevel(); ok {
			c := ink
			if l > 0.9 {
				c = live
			}
			hs = append(hs, span(fmt.Sprintf("disk %.0f%%", 100*l), c))
		}
		if len(hs) > 0 {
			kv("HEALTH", hs...)
		}
	}
	for i, a := range it.Alerts {
		k := ""
		if i == 0 {
			k = "ALERT"
		}
		kv(k, bold("⚠ firing · "+a, live))
	}
	if f.rename {
		lines = append(lines, lm.NewLine())
		kv("new name", bold(string(f.input)+"▏", paper), span("  (was "+f.selected().Name+") · enter to rename · esc to cancel", dim))
	}
	lines = append(lines, lm.NewLine())
	for _, a := range f.actions() {
		kv("", bold(" "+a.key+" ", live), span(a.label, ink), span("  "+a.hint, dim))
	}
	fr.RenderWidget(lm.NewBlock().Rounded().WithTitle(" "+title+" ").WithBorderStyle(lm.Fg(tc)).
		WithChild(lm.NewText(lines...).WithWrap(false)).WithPadding(0, 1, 0, 1), area)
}

type action struct{ key, label, hint string }

// actions is what the selected item offers, in order.
func (f *Fleet) actions() []action {
	it := f.selected()
	var out []action
	switch it.Kind {
	case ItemCandidate:
		if f.Actions.Adopt != nil {
			out = append(out, action{"a", "adopt", "bring it onto your Edge"})
		}
	case ItemMember:
		if f.Actions.Rename != nil {
			out = append(out, action{"n", "rename", "give it a name you choose"})
		}
		if f.Actions.Forget != nil {
			out = append(out, action{"f", "forget", "off the Edge · cert revoked"})
		}
	case ItemHub:
		if f.Actions.SetPrefer != nil {
			out = append(out, action{"p", "prefer", "cycle where a turn goes first"})
		}
	}
	if f.health != nil {
		out = append(out, action{"2", "health", "temperatures, disks, alerts"})
	}
	return out
}

func (f *Fleet) actionBar() lm.Widget {
	l := lm.NewLine(span(" ", dim))
	add := func(k, v string) {
		l.Spans = append(l.Spans, bold(" "+k+" ", live), span(v+"   ", muted))
	}
	add("↑↓", "move")
	for _, a := range f.actions() {
		add(a.key, a.label)
	}
	add("r", "re-read")
	add("q", "quit")
	return lm.NewText(l).WithWrap(false)
}

// drawConfirm is the forget question, centered over the map.
func (f *Fleet) drawConfirm(fr *lm.Frame) {
	it := f.selected()
	a := fr.Area()
	w, h := uint16(64), uint16(7)
	if a.Width < w+2 {
		w = a.Width - 2
	}
	box := lm.Rect{X: a.X + (a.Width-w)/2, Y: a.Y + (a.Height-h)/2, Width: w, Height: h}
	d := widgets.Dialog{ID: "forget", Title: " forget " + it.Name + "? ",
		Message:    "it leaves this Edge and its certificate is revoked",
		SubMessage: "y forget · n keep it",
		Buttons:    []widgets.DialogButton{{Text: "y forget", Style: lm.Fg(live)}, {Text: "n keep", Style: lm.Fg(ink)}},
		Style:      lm.Fg(ink), HeaderStyle: lm.Fg(live).Bold(), BorderStyle: lm.Fg(live), Shadow: true}
	fr.RenderWidget(d, box)
}
