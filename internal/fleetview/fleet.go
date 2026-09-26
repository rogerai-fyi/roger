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

	f.drawFrame(fr)
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

// --- drawing: the rack ---------------------------------------------------------------------

func span(s string, c lm.Color) lm.Span { return lm.NewSpan(s, lm.Fg(c)) }
func bold(s string, c lm.Color) lm.Span { return lm.NewSpan(s, lm.Fg(c).Bold()) }

func (f *Fleet) drawFrame(fr *lm.Frame) {
	rows := lm.SplitVertical(fr.Area(), lm.Fixed(2), lm.Fill(), lm.Fixed(1))
	units, alerts := 0, 0
	for _, it := range f.snap.Items {
		if it.Kind == ItemHub || it.Kind == ItemMember {
			units++
		}
		alerts += len(it.Alerts)
	}
	right := clock(f.snap.At) + " · " + unitsWord(units)
	if alerts > 0 {
		right += fmt.Sprintf(" · ▲ %d", alerts)
	}
	tabs := []string{"fleet"}
	if f.health != nil {
		tabs = append(tabs, "health")
	}
	brandBand(fr, rows[0], tabs, 0, right, f.frame)
	if f.err != nil {
		fr.RenderWidget(lm.NewText(
			lm.NewLine(),
			lm.NewLine(span("  ", dim), bold("could not read the Edge", live)),
			lm.NewLine(span("  ", dim), span(f.err.Error(), ink)),
		).WithWrap(false), rows[1])
	} else {
		cols := lm.SplitHorizontal(rows[1], lm.Percentage(54), lm.Fixed(1), lm.Fill())
		f.drawRack(fr, cols[0])
		hairline(fr, cols[1])
		f.drawReadout(fr, cols[2])
	}
	actionBar(fr, rows[2], f.actions(), f.toast, f.toastErr, f.toastAt)
	if f.confirm {
		it := f.selected()
		confirmBox(fr, "forget "+it.Name+"?", "it leaves this Edge and its certificate is revoked", "y forget", "n keep it")
	}
}

// glyph is a unit's mark and its kind color.
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
			return "▣", ash
		}
		return "▣", kindColor(it.NodeKind)
	default:
		if (frame/3)%2 == 0 { // a discovered device asks: it blinks, softly
			return "◈", gold
		}
		return "◇", gold
	}
}

// rogerWords is what a roger on this machine is doing, in a few words.
func (it FleetItem) rogerWords() string {
	switch {
	case it.Dark:
		return "agent · offline"
	case it.Job != "":
		return "agent · " + it.Job
	case it.Kind == ItemAgent && len(it.Bands) > 0:
		return "agent · " + fewBands(it.Bands)
	case it.Kind == ItemAgent:
		return "agent · idle"
	case it.Self:
		return "this one"
	case len(it.Bands) > 0:
		return "serves " + fewBands(it.Bands)
	}
	return "roger"
}

// fewBands names the first two models and counts the rest, so a line stays a line.
func fewBands(b []string) string {
	if len(b) <= 2 {
		return strings.Join(b, " ")
	}
	return strings.Join(b[:2], " ") + fmt.Sprintf(" +%d", len(b)-2)
}

func alertChip(n int) []lm.Span {
	if n == 0 {
		return nil
	}
	return []lm.Span{span("  ", dim), bold(fmt.Sprintf("▲ %d", n), live)}
}

// drawRack is the left column: the hub as the transmitter, its rogers under it, the units
// patched in, the bench.
func (f *Fleet) drawRack(fr *lm.Frame, area lm.Rect) {
	var lines []lm.Line
	now := f.snap.At
	rail := func(i int, c lm.Color) lm.Span {
		if i == f.sel {
			return bold("▌", live)
		}
		return span("▌", c)
	}
	nameC := func(i int, dark bool) lm.Color {
		switch {
		case i == f.sel:
			return paper
		case dark:
			return ash
		}
		return ink
	}
	members, cands := 0, 0
	for _, it := range f.snap.Items {
		switch it.Kind {
		case ItemMember:
			members++
		case ItemCandidate:
			cands++
		}
	}
	seenMember, seenCand := false, false
	for i, it := range f.snap.Items {
		g, gc := it.glyph(f.frame)
		switch it.Kind {
		case ItemHub:
			lines = append(lines, lm.NewLine())
			lines = append(lines, lm.NewLine(span(" ", dim), rail(i, sky), span(" ", dim), bold(g+" ", gc), bold(it.Name, nameC(i, false)),
				span("   ", dim), span("this machine", muted)))
			lines = append(lines, lm.NewLine(span(" ", dim), rail(i, sky), span("   ", dim),
				span(strings.ToLower(f.snap.Self.Authority)+" · "+f.snap.Self.Prefer, muted)))
			l := lm.NewLine(span(" ", dim), rail(i, sky), span("   ", dim))
			lv := carrierLevel(now, false, now)
			l.Spans = append(l.Spans, meter(lv, true, -1, f.frame, true)...)
			l.Spans = append(l.Spans, span(" "+sUnit(lv)+"  ", muted), span("online", mint))
			if it.Health != nil && it.Health.Readings != nil && it.Health.Readings.UptimeS > 0 {
				l.Spans = append(l.Spans, span(" "+edge.Age(time.Duration(it.Health.Readings.UptimeS)*time.Second), muted))
			}
			l.Spans = append(l.Spans, alertChip(len(it.Alerts))...)
			lines = append(lines, l)
		case ItemInstance, ItemAgent:
			branch := "├"
			if i+1 >= len(f.snap.Items) || (f.snap.Items[i+1].Kind != ItemInstance && f.snap.Items[i+1].Kind != ItemAgent) {
				branch = "╰"
			}
			nc := nameC(i, it.Dark)
			if i == f.sel {
				gc = live
			}
			lines = append(lines, lm.NewLine(span(" ", dim), rail(i, sky), span("      "+branch+" ", dim), bold(g+" ", gc),
				bold(pad(it.Name, 20), nc), span(it.rogerWords(), muted)))
		case ItemMember:
			if !seenMember {
				seenMember = true
				lines = append(lines, lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow("on the rack", sky),
					span(fmt.Sprintf("  %d patched in", members), muted)), lm.NewLine())
			}
			l := lm.NewLine(span(" ", dim), rail(i, kindColor(it.NodeKind)), span(" ", dim), bold(g+" ", gc),
				bold(pad(it.Name, 20), nameC(i, it.Dark)), span(pad(kindLabel(it.NodeKind), 8), muted))
			l.Spans = append(l.Spans, meter(carrierLevel(it.LastSeen, it.Dark, now), true, -1, f.frame, !it.Dark)...)
			pc := mint
			if it.Dark {
				pc = ash
			}
			l.Spans = append(l.Spans, span(" ", dim), span(it.Presence, pc))
			l.Spans = append(l.Spans, alertChip(len(it.Alerts))...)
			lines = append(lines, l)
		case ItemCandidate:
			if !seenCand {
				seenCand = true
				lines = append(lines, lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow("on the bench", gold),
					span(fmt.Sprintf("  %d asking to be adopted", cands), muted)), lm.NewLine())
			}
			l := lm.NewLine(span(" ", dim), rail(i, gold), span(" ", dim), bold(g+" ", gc),
				bold(pad(it.Name, 20), nameC(i, false)), span(pad(kindLabel(it.NodeKind), 8), muted))
			l.Spans = append(l.Spans, meter(0, false, -1, 0, false)...)
			l.Spans = append(l.Spans, span(" found · a adopt", gold))
			lines = append(lines, l)
		}
	}
	if !seenMember {
		lines = append(lines, lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow("on the rack", sky), span("  nothing patched in yet", muted)))
		if f.snap.Self.Join != "" {
			lines = append(lines, lm.NewLine(span("   on another machine:  ", dim), span(f.snap.Self.Join, ink)))
		}
		lines = append(lines, lm.NewLine(span("   or scan the LAN:  ", dim), span("roger edge scan", ink)))
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
}

// drawReadout is the selected unit in full: a headline, its meters, what it does, its facts.
func (f *Fleet) drawReadout(fr *lm.Frame, area lm.Rect) {
	it := f.selected()
	now := f.snap.At
	width := int(area.Width) - 13
	if width < 20 {
		width = 20
	}
	var lines []lm.Line
	row := func(k string, v ...lm.Span) {
		l := lm.NewLine(span("  ", dim), eyebrow(pad(k, 10), muted))
		l.Spans = append(l.Spans, v...)
		lines = append(lines, l)
	}
	text := func(k, v string, c lm.Color) {
		for i, part := range wrapWords(v, width) {
			if i > 0 {
				k = ""
			}
			row(k, span(part, c))
		}
	}
	head := func(name, role string, c lm.Color) {
		lines = append(lines, lm.NewLine(), lm.NewLine(span("  ", dim), bold(name, paper), span("   "+role, c)))
	}
	prose := func(s string) {
		for _, part := range wrapWords(s, width+10) {
			lines = append(lines, lm.NewLine(span("  ", dim), span(part, ink)))
		}
		lines = append(lines, lm.NewLine())
	}
	switch it.Kind {
	case ItemHub:
		root, _, _ := strings.Cut(f.snap.Self.Root, " · ") // the badge names the machine too; the headline already did
		head(it.Name, "this machine · "+strings.ToLower(root), muted)
		prose("the transmitter of this Edge: it holds the root key and signs every member's certificate")
		lv := carrierLevel(now, false, now)
		row("carrier", append(meter(lv, true, -1, f.frame, true), span(" "+sUnit(lv)+"   ", muted), span("turns go "+f.snap.Self.PreferValue+" first", teal))...)
	case ItemInstance, ItemAgent:
		role := "roger on this machine"
		if it.Kind == ItemAgent {
			role = "agent on this machine"
		}
		head(it.Name, role, muted)
		switch {
		case it.Dark:
			prose("offline: its process is not running. resume it from the TUI's [3], or forget it there")
		case it.Kind == ItemAgent:
			prose("an agent: it can act on other nodes of the Edge under a grant, and serve what it has on air")
		case len(it.Bands) > 0:
			prose("serves a model to the Edge")
		default:
			prose("on the Edge, not an agent, not serving")
		}
		if it.Job != "" {
			row("job", bold(it.Job, gold))
		}
	case ItemMember:
		head(it.Name, kindLabel(it.NodeKind)+" · patched in", muted)
		if it.Dark {
			prose("dark: the authority has not heard from it past the liveness window. it re-admits itself when it pings again")
		} else {
			prose("a member of this Edge, verified by the certificate this authority issued it")
		}
		lv := carrierLevel(it.LastSeen, it.Dark, now)
		cs := append(meter(lv, true, -1, f.frame, !it.Dark), span("   ", dim))
		if it.Dark {
			cs = append(cs, span(it.Presence, ash))
		} else {
			cs = append(cs, span(sUnit(lv), muted), span("   heard "+edge.Age(now.Sub(it.LastSeen))+" ago", ink))
		}
		row("carrier", cs...)
		if it.Via != "" {
			row("reached", span("through "+it.Via, ink))
		}
	case ItemCandidate:
		head(it.Name, kindLabel(it.NodeKind)+" · on the bench", gold)
		prose("seen on this network, not on your Edge. adopting it grants a claim; it fetches its certificate and patches in")
		row("status", bold("not on your Edge", gold), span(" · press a to adopt it", muted))
	}
	// Health meters, where the unit reports them.
	if h := it.Health; h != nil && h.Readings != nil {
		r := h.Readings
		if r.CPUTempC != nil {
			from := -1.0
			if th, ok := thresholdIn(it.Alerts, "cpu_temp"); ok {
				from = th / 110
			}
			row("temp", append(append(meterN(*r.CPUTempC/110, from, 0, false, meterCells), span(fmt.Sprintf(" %.1f°C", *r.CPUTempC), ink)), alarm(it.Alerts, "cpu_temp")...)...)
		}
		if l, ok := h.MemoryLevel(); ok {
			row("memory", append(meterN(l, -1, 0, false, meterCells), span(fmt.Sprintf(" %s of %s GB", fmtGB(r.RAMUsedGB), fmtGB(r.RAMTotalGB)), ink))...)
		}
		if l, ok := h.DiskLevel(); ok {
			from := -1.0
			if th, ok := thresholdIn(it.Alerts, "disk"); ok {
				from = th / 100
			}
			row("disk", append(append(meterN(l, from, 0, false, meterCells), span(fmt.Sprintf(" %.0f%%", 100*l), ink)), alarm(it.Alerts, "disk")...)...)
		}
	}
	if len(it.Bands) > 0 {
		row("on air", span(strings.Join(it.Bands, "  "), teal))
	}
	if len(it.Caps) > 0 {
		row("can do", chips(it.Caps)...)
	}
	// Alerts not already told on a meter row.
	for _, a := range it.Alerts {
		if strings.HasPrefix(a, "cpu_temp ") || strings.HasPrefix(a, "disk ") {
			continue
		}
		row("alarm", bold("▲ "+a, live))
	}
	if it.Kind == ItemHub {
		lines = append(lines, lm.NewLine())
		for i, ln := range f.snap.Self.AuthorityLines {
			k := ""
			if i == 0 {
				k = "authority"
			}
			text(k, ln, ink)
		}
		if f.snap.Self.Discovery != "" {
			text("discovery", f.snap.Self.Discovery, ink)
		}
		if f.snap.Self.Join != "" {
			text("join", f.snap.Self.Join, ink)
		}
	}
	if f.rename {
		lines = append(lines, lm.NewLine())
		lines = append(lines, lm.NewLine(span("  ", dim), span("new name  ", paper), bold(string(f.input)+"▏", paper),
			span("  (was "+it.Name+") · enter to rename · esc to cancel", muted)))
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
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

type action struct{ key, label, hint string }

// actions is what the selected unit offers, in order.
func (f *Fleet) actions() []action {
	it := f.selected()
	out := []action{{"↑↓", "select", ""}}
	switch it.Kind {
	case ItemCandidate:
		if f.Actions.Adopt != nil {
			out = append(out, action{"a", "adopt", ""})
		}
	case ItemMember:
		if f.Actions.Rename != nil {
			out = append(out, action{"n", "rename", ""})
		}
		if f.Actions.Forget != nil {
			out = append(out, action{"f", "forget", ""})
		}
	case ItemHub:
		if f.Actions.SetPrefer != nil {
			out = append(out, action{"p", "prefer", ""})
		}
	}
	if f.health != nil {
		out = append(out, action{"2", "health", ""})
	}
	return append(out, action{"r", "re-read", ""}, action{"q", "quit", ""})
}
