package fleetview

// The HEALTH screen (features/edge/health.feature §2, §5, §6) on the rack: a strip of units,
// each with its temperature, memory, disk and carrier as meters; below it the selected unit's
// instrument stack with wide meters and the history of its readings; then the rules in force
// as sentences. A metric a unit does not report is a dim rail, never a zero.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	lm "github.com/thebanri/limoni"

	"rogerai.fm/roger/v6/internal/edge"
)

// HealthSnapshot is one read of everything the screen draws, taken together so one frame is
// internally consistent (a unit's row, its stack and its alerts come from the same moment).
type HealthSnapshot struct {
	Nodes  []edge.NodeHealth
	Firing map[string][]string // node id -> the rule wordings firing for it
	Rules  []RuleLine
	Sinks  string // where alerts go; "" = none configured
	At     time.Time
}

// RuleLine is one alert rule as the rules pane lists it.
type RuleLine struct{ ID, When, Scope, Kind, State string }

// HealthSource reads a snapshot. It is called on the first frame and again every Every.
type HealthSource func() (HealthSnapshot, error)

// HealthActions edit the rules in place: the CLI's `roger edge alert set|off|on|rm` as functions.
type HealthActions struct {
	SetRule    func(metric, op, value, scope string) (string, error)
	RuleOff    func(id string, off bool) error
	RemoveRule func(id string) error
}

const historyLen = 60

// history is the last readings of one unit, kept in this process, for the sparklines.
type history struct{ temp, load []float64 }

// Health is the screen's state: the last snapshot, when it was read, the selection.
type Health struct {
	Source HealthSource
	Every  time.Duration // how often the source is re-read; 0 = every frame
	// BackHint names the way back when this screen is a tab of another ("1 fleet"); Tabs/Tab is
	// then the parent's strip, so the band reads the same on every tab.
	BackHint string
	Tabs     []string
	Tab      int
	Actions  HealthActions

	snap    HealthSnapshot
	rsel    int // the chosen rule, walked with [ and ]
	adding  bool
	input   []rune
	last    string // the last edit's outcome
	lastErr bool
	saidAt  time.Time
	err     error
	readAt  time.Time
	sel     int
	frame   int
	hist    map[string]*history
	lastAt  map[string]int64 // the ReadAt last folded into hist, per unit
}

// NewHealth makes the screen over src, re-reading every 2s.
func NewHealth(src HealthSource) *Health {
	return &Health{Source: src, Every: 2 * time.Second, hist: map[string]*history{}, lastAt: map[string]int64{}}
}

// Run opens the screen full-screen until q/esc or ctx ends.
func (h *Health) Run(ctx context.Context) error {
	return lm.RunWithContext(ctx, h.Draw, lm.WithTitle("roger edge health"), lm.WithFPS(6))
}

// Draw is one frame: handle the event, refresh if due, lay the screen out. It is the function
// uitest drives, so what the test sees is exactly what the terminal gets.
func (h *Health) Draw(f *lm.Frame, ev *lm.Event) bool {
	if f == nil { // keys only (a parent tab forwards them without a frame)
		return h.onKey(ev)
	}
	if !h.onKey(ev) {
		return false
	}
	h.frame++
	h.refresh()
	if h.sel >= len(h.snap.Nodes) {
		h.sel = len(h.snap.Nodes) - 1
	}
	if h.sel < 0 {
		h.sel = 0
	}
	rows := lm.SplitVertical(f.Area(), lm.Fixed(2), lm.Fill(), lm.Fixed(1))
	firing := 0
	for _, a := range h.snap.Firing {
		firing += len(a)
	}
	right := clock(h.snap.At) + " · " + unitsWord(len(h.snap.Nodes))
	tabs, tab := []string{"health"}, 0
	if len(h.Tabs) > 0 {
		tabs, tab = h.Tabs, h.Tab
	}
	brandBand(f, rows[0], tabs, tab, right, firing, firing > 0, h.frame)
	if h.err != nil {
		f.RenderWidget(lm.NewText(
			lm.NewLine(),
			lm.NewLine(span("  ", dim), bold("could not read the fleet", live)),
			lm.NewLine(span("  ", dim), span(h.err.Error(), ink)),
		).WithWrap(false), rows[1])
	} else {
		h.drawBody(f, rows[1])
	}
	acts := []action{{"↑↓", "unit"}}
	if h.Actions.SetRule != nil {
		acts = append(acts, action{"a", "add rule"})
	}
	if h.Actions.RuleOff != nil || h.Actions.RemoveRule != nil {
		acts = append(acts, action{"[ ]", "rule"})
	}
	if h.Actions.RuleOff != nil {
		acts = append(acts, action{"o", "off/on"})
	}
	if h.Actions.RemoveRule != nil {
		acts = append(acts, action{"x", "remove"})
	}
	acts = append(acts, action{"r", "re-read"})
	if h.BackHint != "" {
		acts = append(acts, action{"1", "fleet"})
	}
	acts = append(acts, action{"q", "quit"})
	actionBar(f, rows[2], acts, h.last, h.lastErr, h.saidAt)
	return true
}

func (h *Health) say(s string, isErr bool) { h.last, h.lastErr, h.saidAt = s, isErr, time.Now() }

// chosenRule is the rule under the [ ] cursor, if any.
func (h *Health) chosenRule() (RuleLine, bool) {
	if h.rsel < 0 || h.rsel >= len(h.snap.Rules) {
		return RuleLine{}, false
	}
	return h.snap.Rules[h.rsel], true
}

// onKey handles one key; false ends the screen.
func (h *Health) onKey(ev *lm.Event) bool {
	if ev == nil || ev.Type != lm.EventKey {
		return true
	}
	k := ev.Key
	if h.adding {
		switch {
		case k.Type == lm.KeyEsc:
			h.adding = false
		case k.Type == lm.KeyEnter:
			h.adding = false
			w := strings.Fields(string(h.input))
			if len(w) < 3 {
				h.say("a rule is a metric, gt or lt, and a value: cpu_temp gt 80 [fleet|<unit>]", true)
				return true
			}
			scope := "fleet"
			if len(w) > 3 {
				scope = w[3]
			}
			if h.Actions.SetRule != nil {
				msg, err := h.Actions.SetRule(w[0], w[1], w[2], scope)
				if err != nil {
					h.say(err.Error(), true)
				} else {
					h.say(msg, false)
				}
				h.readAt = time.Time{}
			}
		case k.Type == lm.KeyBackspace:
			if len(h.input) > 0 {
				h.input = h.input[:len(h.input)-1]
			}
		case k.Type == lm.KeySpace:
			h.input = append(h.input, ' ')
		case k.Ch != 0:
			h.input = append(h.input, k.Ch)
		}
		return true
	}
	switch {
	case k.Ch == 'q' || k.Ch == 'Q' || k.Type == lm.KeyEsc:
		return false
	case k.Ch == 'r':
		h.readAt = time.Time{} // re-read now
	case k.Type == lm.KeyDown || k.Ch == 'j':
		h.sel++
	case k.Type == lm.KeyUp || k.Ch == 'k':
		h.sel--
	case k.Ch == ']':
		h.rsel++
	case k.Ch == '[':
		h.rsel--
	case k.Ch == 'a':
		if h.Actions.SetRule != nil {
			h.adding, h.input = true, nil
		}
	case k.Ch == 'o':
		if r, ok := h.chosenRule(); ok && h.Actions.RuleOff != nil {
			off := !strings.HasPrefix(r.State, "off")
			if err := h.Actions.RuleOff(r.ID, off); err != nil {
				h.say(err.Error(), true)
			} else if off {
				h.say("rule "+r.ID+" is off", false)
			} else {
				h.say("rule "+r.ID+" is on", false)
			}
			h.readAt = time.Time{}
		}
	case k.Ch == 'x':
		if r, ok := h.chosenRule(); ok && h.Actions.RemoveRule != nil {
			if r.Kind == "default" {
				h.say("a default is turned off, not removed (o)", true)
				return true
			}
			if err := h.Actions.RemoveRule(r.ID); err != nil {
				h.say(err.Error(), true)
			} else {
				h.say("rule "+r.ID+" removed", false)
			}
			h.readAt = time.Time{}
		}
	}
	return true
}

func (h *Health) refresh() {
	if !h.readAt.IsZero() && time.Since(h.readAt) < h.Every {
		return
	}
	snap, err := h.Source()
	h.readAt = time.Now()
	h.err = err
	if err != nil {
		return
	}
	h.snap = snap
	// Fold each NEW reading into the unit's history (a re-read of the same reading is not a
	// new point).
	for _, n := range snap.Nodes {
		r := n.Readings
		if r == nil || r.ReadAt == 0 || h.lastAt[n.ID] == r.ReadAt {
			continue
		}
		h.lastAt[n.ID] = r.ReadAt
		hs := h.hist[n.ID]
		if hs == nil {
			hs = &history{}
			h.hist[n.ID] = hs
		}
		if r.CPUTempC != nil {
			hs.temp = appendCap(hs.temp, *r.CPUTempC)
		}
		if r.CPULoad1 != nil {
			hs.load = appendCap(hs.load, *r.CPULoad1)
		}
	}
}

func appendCap(xs []float64, v float64) []float64 {
	xs = append(xs, v)
	if len(xs) > historyLen {
		xs = xs[len(xs)-historyLen:]
	}
	return xs
}

func (h *Health) drawBody(f *lm.Frame, area lm.Rect) {
	var lines []lm.Line
	now := h.snap.At
	narrow := area.Width < 120
	nameW, kindW := 18, 7
	if narrow {
		nameW, kindW = 16, 0
	}
	// The strip: one row per unit; at 100 columns the carrier meter goes (the eye tells presence).
	head := lm.NewLine(span("   ", dim), eyebrow(pad("unit", nameW+kindW+4), muted), eyebrow(pad("temp", 18), muted),
		eyebrow(pad("memory", 18), muted), eyebrow(pad("disk", 18), muted))
	if !narrow {
		head.Spans = append(head.Spans, eyebrow("carrier", muted))
	}
	lines = append(lines, lm.NewLine(), head)
	for i, n := range h.snap.Nodes {
		firing := h.snap.Firing[n.ID]
		railC := kindColor(n.Kind)
		if n.Dark {
			railC = ash
		}
		rail := span("▌", railC)
		nc := ink
		if i == h.sel {
			rail, nc = bold("▌", live), paper
		}
		if n.Dark && i != h.sel {
			nc = ash
		}
		l := lm.NewLine(span(" ", dim), rail, span(" ", dim))
		l.Spans = append(l.Spans, eye(carrierLevel(n.LastHeard, n.Dark, now), n.Dark, false, len(firing) > 0, h.frame, i)...)
		l.Spans = append(l.Spans, span(" ", dim), span(pad(elide(n.Name, nameW), nameW), nc))
		if kindW > 0 {
			l.Spans = append(l.Spans, span(pad(kindLabel(n.Kind), kindW), dim))
		}
		l.Spans = append(l.Spans, cellTemp(n, firing, h.snap.Rules)...)
		l.Spans = append(l.Spans, cellMem(n)...)
		l.Spans = append(l.Spans, cellDisk(n, firing, h.snap.Rules)...)
		lv := carrierLevel(n.LastHeard, n.Dark, now)
		if !narrow {
			l.Spans = append(l.Spans, meter(lv, !n.LastHeard.IsZero(), -1, h.frame, !n.Dark)...)
		}
		switch {
		case n.Dark:
			l.Spans = append(l.Spans, span(" DARK "+edge.Age(n.DarkFor), ash))
		case len(firing) > 0:
			l.Spans = append(l.Spans, span(" TROUBLE", live))
		default:
			l.Spans = append(l.Spans, span(" ON AIR", signal))
		}
		if !n.Dark && n.Readings != nil && n.Readings.UptimeS > 0 {
			l.Spans = append(l.Spans, span(" · up "+edge.Age(time.Duration(n.Readings.UptimeS)*time.Second), muted))
		}
		l.Spans = append(l.Spans, alertChip(len(firing))...)
		lines = append(lines, l)
	}
	if len(h.snap.Nodes) == 0 {
		lines = append(lines, lm.NewLine(span("   nothing on this Edge reports yet", muted)))
	}
	// The selected unit's instrument stack.
	if len(h.snap.Nodes) > 0 {
		n := h.snap.Nodes[h.sel]
		firing := h.snap.Firing[n.ID]
		lines = append(lines, lm.NewLine())
		headline := lm.NewLine(span(" ", dim))
		headline.Spans = append(headline.Spans, eye(carrierLevel(n.LastHeard, n.Dark, now), n.Dark, false, len(firing) > 0, h.frame, 99)...)
		headline.Spans = append(headline.Spans, span(" ", dim), bold(n.Name, ink), span("   "+kindLabel(n.Kind)+" · ", dim))
		switch {
		case n.Dark:
			headline.Spans = append(headline.Spans, span("DARK "+edge.Age(n.DarkFor), ash))
		case len(firing) > 0:
			headline.Spans = append(headline.Spans, span("TROUBLE", live))
		default:
			headline.Spans = append(headline.Spans, span("ON AIR", signal))
		}
		if !n.LastHeard.IsZero() {
			headline.Spans = append(headline.Spans, span(" · last heard "+edge.Age(now.Sub(n.LastHeard))+" ago", muted))
		}
		if n.Readings != nil {
			headline.Spans = append(headline.Spans, span(" · readings "+edge.Age(n.ReadAge)+" ago", muted))
		}
		lines = append(lines, headline)
		// The wide meters leave room for the value and an alarm sentence at the right.
		wide := int(area.Width) - 58
		if wide < 16 {
			wide = 16
		}
		if wide > 40 { // a brick five rows deep is not a meter
			wide = 40
		}
		row := func(k string, v ...lm.Span) {
			l := lm.NewLine(span("  ", dim), eyebrow(pad(k, 8), muted))
			l.Spans = append(l.Spans, v...)
			lines = append(lines, l)
		}
		r := n.Readings
		hs := h.hist[n.ID]
		line := func(metric string, scale float64) float64 {
			if th, ok := ruleLine(h.snap.Rules, n.Name, metric); ok {
				return th / scale
			}
			if th, ok := thresholdIn(firing, metric); ok {
				return th / scale
			}
			return -1
		}
		switch {
		case r == nil:
			lines = append(lines, lm.NewLine(span("  this node reports no health readings", muted)))
		default:
			if r.CPUTempC != nil {
				th, hasTh := thresholdIn(firing, "cpu_temp")
				v := []lm.Span{bold(fmt.Sprintf("%.1f°C  ", *r.CPUTempC), ink)}
				if hs != nil && len(hs.temp) > 1 {
					v = append(v, sparkline(hs.temp, th, hasTh)...)
					v = append(v, span(fmt.Sprintf("  last %d reads", len(hs.temp)), dim))
				}
				v = append(v, alarm(firing, "cpu_temp")...)
				row("temp", v...)
			}
			if r.CPULoad1 != nil {
				v := []lm.Span{bold(fmt.Sprintf("%.2f   ", *r.CPULoad1), ink)}
				if hs != nil && len(hs.load) > 1 {
					v = append(v, sparkline(hs.load, 0, false)...)
				}
				row("load", v...)
			}
			if l, ok := n.MemoryLevel(); ok {
				row("memory", append([]lm.Span{span(pad(fmt.Sprintf("%.1f of %s GB", r.RAMUsedGB, fmtGB(r.RAMTotalGB)), 18), ink)},
					append(meterN(l, line("memory", 100), 0, false, wide), span(fmt.Sprintf(" %.0f%%", 100*l), ink))...)...)
			} else if r.RAMUsedGB > 0 {
				row("memory", span(fmt.Sprintf("%.1f GB used · no total reported", r.RAMUsedGB), ink))
			}
			from := line("disk", 100)
			th, hasTh := thresholdIn(firing, "disk")
			first := true
			for _, d := range r.Disks {
				if d.TotalGB < 1 { // a boot stub is not a disk worth a meter
					continue
				}
				k := ""
				if first {
					k, first = "disk", false
				}
				l := d.UsedGB / d.TotalGB
				v := []lm.Span{span(pad(d.Mount, 18), ink)}
				v = append(v, meterN(l, from, 0, false, wide)...)
				v = append(v, span(fmt.Sprintf(" %.0f%%", 100*l), ink))
				if hasTh && 100*l > th {
					v = append(v, span("   ", dim), bold(fmt.Sprintf("▲ over %s", fmtGB(th)), live))
				}
				row(k, v...)
			}
			if r.GPU != "" {
				row("gpu", append([]lm.Span{span(pad(elide(r.GPU, 17), 18), ink)}, append(meterN(r.GPUMemUsed, -1, 0, false, wide), span(fmt.Sprintf(" %.0f%% memory", 100*r.GPUMemUsed), ink))...)...)
			}
			if r.SDWearPct != nil {
				row("sd card", append(meterN(*r.SDWearPct/100, -1, 0, false, wide), span(fmt.Sprintf(" %.0f%% worn", *r.SDWearPct), ink))...)
			}
		}
		for _, a := range firing {
			if strings.HasPrefix(a, "cpu_temp ") || (strings.HasPrefix(a, "disk ") && r != nil) {
				continue
			}
			row("alarm", bold("▲ "+a, live))
		}
	}
	// The rules in force, as sentences.
	lines = append(lines, lm.NewLine())
	sinks := h.snap.Sinks
	if sinks == "" {
		sinks = "sinks: none configured · alerts stay on this machine"
	}
	lines = append(lines, lm.NewLine(span("  ", dim), eyebrow(pad("rules", 8), muted), span(sinks, muted)))
	if h.rsel >= len(h.snap.Rules) {
		h.rsel = len(h.snap.Rules) - 1
	}
	if h.rsel < 0 {
		h.rsel = 0
	}
	if h.adding {
		lines = append(lines, lm.NewLine(span("          ", dim), span("add rule  ", paper), bold(string(h.input)+"▏", paper)),
			lm.NewLine(span("          metric (cpu_temp, disk, memory, cpu_load, sd_wear, offline) · gt or lt · value · fleet or a unit · enter · esc", muted)))
	}
	for i, rl := range h.snap.Rules {
		dot, dc := "○", dim
		if strings.HasPrefix(rl.State, "firing") {
			dot, dc = "●", live
		}
		state := rl.State
		if state == "quiet" {
			state = ""
		}
		rail := span("  ", dim)
		if i == h.rsel && (h.Actions.RuleOff != nil || h.Actions.RemoveRule != nil) {
			rail = bold("▌ ", live)
		}
		lines = append(lines, lm.NewLine(span("        ", dim), rail, bold(dot+" ", dc), span(pad(rl.When, 18), ink),
			span(pad(rl.Scope, 12), muted), span(pad(rl.Kind, 9), muted), span(strings.Replace(state, "firing:", "firing on", 1), dc)))
	}
	f.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
}

// sparkline draws a history as ▁..█ scaled to its own range, red where it is over the threshold.
func sparkline(xs []float64, th float64, hasTh bool) []lm.Span {
	lo, hi := xs[0], xs[0]
	for _, x := range xs {
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	var out []lm.Span
	for _, x := range xs {
		i := 0
		if hi > lo {
			i = int((x - lo) / (hi - lo) * float64(len(ramp)-1))
		}
		c := dial
		if hasTh && x > th {
			c = live
		}
		out = append(out, span(string(ramp[i]), c))
	}
	return out
}

// --- the strip's cells, with the CLI's honesty rules -------------------------------------------

func cellTemp(n edge.NodeHealth, firing []string, rules []RuleLine) []lm.Span {
	if n.Readings == nil || n.Readings.CPUTempC == nil {
		return append(meter(0, false, -1, 0, false), span(pad("", 9), dim))
	}
	from := -1.0
	if th, ok := ruleLine(rules, n.Name, "cpu_temp"); ok {
		from = th / 110
	} else if th, ok := thresholdIn(firing, "cpu_temp"); ok {
		from = th / 110
	}
	return append(meterN(*n.Readings.CPUTempC/110, from, 0, false, meterCells), span(pad(fmt.Sprintf(" %.1f°", *n.Readings.CPUTempC), 9), ink))
}

func cellMem(n edge.NodeHealth) []lm.Span {
	l, ok := n.MemoryLevel()
	if !ok {
		return append(meter(0, false, -1, 0, false), span(pad("", 9), dim))
	}
	return append(meterN(l, -1, 0, false, meterCells), span(pad(fmt.Sprintf(" %.0f%%", 100*l), 9), ink))
}

func cellDisk(n edge.NodeHealth, firing []string, rules []RuleLine) []lm.Span {
	l, ok := n.DiskLevel()
	if !ok {
		return append(meter(0, false, -1, 0, false), span(pad("", 9), dim))
	}
	from := -1.0
	if th, ok := ruleLine(rules, n.Name, "disk"); ok {
		from = th / 100
	} else if th, ok := thresholdIn(firing, "disk"); ok {
		from = th / 100
	}
	return append(meterN(l, from, 0, false, meterCells), span(pad(fmt.Sprintf(" %.0f%%", 100*l), 9), ink))
}

// ruleLine is the threshold in force for a unit on a metric, from the rules the pane lists: the
// unit's own rule when it has one, else the fleet's. A quiet unit sees its line too.
func ruleLine(rules []RuleLine, node, metric string) (float64, bool) {
	fleet, ok := 0.0, false
	for _, r := range rules {
		if strings.HasPrefix(r.State, "off") {
			continue
		}
		f := strings.Fields(r.When)
		if len(f) != 3 || f[0] != metric {
			continue
		}
		v, err := strconv.ParseFloat(f[2], 64)
		if err != nil {
			continue
		}
		if r.Scope == node {
			return v, true
		}
		if r.Scope == "fleet" {
			fleet, ok = v, true
		}
	}
	return fleet, ok
}
