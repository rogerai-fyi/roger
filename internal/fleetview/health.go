package fleetview

// The HEALTH screen (features/edge/health.feature §2, §5, §6) on the rack: a strip of units,
// each with its temperature, memory, disk and carrier as meters; below it the selected unit's
// instrument stack with wide meters and the history of its readings; then the rules in force
// as sentences. A metric a unit does not report is a dim rail, never a zero.

import (
	"context"
	"fmt"
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

const historyLen = 60

// history is the last readings of one unit, kept in this process, for the sparklines.
type history struct{ temp, load []float64 }

// Health is the screen's state: the last snapshot, when it was read, the selection.
type Health struct {
	Source HealthSource
	Every  time.Duration // how often the source is re-read; 0 = every frame
	// BackHint names the way back when this screen is a tab of another ("1 fleet").
	BackHint string

	snap   HealthSnapshot
	err    error
	readAt time.Time
	sel    int
	frame  int
	hist   map[string]*history
	lastAt map[string]int64 // the ReadAt last folded into hist, per unit
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
	if firing > 0 {
		right += fmt.Sprintf(" · ▲ %d", firing)
	}
	tabs, tab := []string{"health"}, 0
	if h.BackHint != "" {
		tabs, tab = []string{"fleet", "health"}, 1
	}
	brandBand(f, rows[0], tabs, tab, right, h.frame)
	if h.err != nil {
		f.RenderWidget(lm.NewText(
			lm.NewLine(),
			lm.NewLine(span("  ", dim), bold("could not read the fleet", live)),
			lm.NewLine(span("  ", dim), span(h.err.Error(), ink)),
		).WithWrap(false), rows[1])
	} else {
		h.drawBody(f, rows[1])
	}
	acts := []action{{"↑↓", "unit", ""}, {"r", "re-read", ""}}
	if h.BackHint != "" {
		acts = append(acts, action{"1", "fleet", ""})
	}
	acts = append(acts, action{"q", "quit", ""})
	actionBar(f, rows[2], acts, "", false, time.Time{})
	return true
}

// onKey handles one key; false ends the screen.
func (h *Health) onKey(ev *lm.Event) bool {
	if ev == nil || ev.Type != lm.EventKey {
		return true
	}
	switch {
	case ev.Key.Ch == 'q' || ev.Key.Ch == 'Q' || ev.Key.Type == lm.KeyEsc:
		return false
	case ev.Key.Ch == 'r':
		h.readAt = time.Time{} // re-read now
	case ev.Key.Type == lm.KeyDown || ev.Key.Ch == 'j':
		h.sel++
	case ev.Key.Type == lm.KeyUp || ev.Key.Ch == 'k':
		h.sel--
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
	// The strip: one row per unit.
	lines = append(lines, lm.NewLine(), lm.NewLine(span("   ", dim), eyebrow(pad("unit", 28), muted), eyebrow(pad("temp", 18), muted),
		eyebrow(pad("memory", 18), muted), eyebrow(pad("disk", 18), muted), eyebrow("carrier", muted)))
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
		g := "▣"
		if i == 0 && n.Kind == "host" {
			g = "◉"
		}
		l := lm.NewLine(span(" ", dim), rail, span(" ", dim), bold(g+" ", kindColor(n.Kind)), bold(pad(n.Name, 18), nc), span(pad(kindLabel(n.Kind), 7), muted))
		l.Spans = append(l.Spans, cellTemp(n, firing)...)
		l.Spans = append(l.Spans, cellMem(n)...)
		l.Spans = append(l.Spans, cellDisk(n, firing)...)
		lv := carrierLevel(n.LastHeard, n.Dark, now)
		l.Spans = append(l.Spans, meter(lv, !n.LastHeard.IsZero(), -1, h.frame, !n.Dark)...)
		if n.Dark {
			l.Spans = append(l.Spans, span(" offline "+edge.Age(n.DarkFor), ash))
		} else if n.Readings != nil && n.Readings.UptimeS > 0 {
			l.Spans = append(l.Spans, span(" "+edge.Age(time.Duration(n.Readings.UptimeS)*time.Second)+" up", mint))
		} else {
			l.Spans = append(l.Spans, span(" online", mint))
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
		headline := lm.NewLine(span("  ", dim), bold(n.Name, paper), span("   "+kindLabel(n.Kind)+" · ", muted))
		if n.Dark {
			headline.Spans = append(headline.Spans, span("offline "+edge.Age(n.DarkFor), ash))
		} else {
			headline.Spans = append(headline.Spans, span("online", mint))
		}
		if !n.LastHeard.IsZero() {
			headline.Spans = append(headline.Spans, span(" · last heard "+edge.Age(now.Sub(n.LastHeard))+" ago", muted))
		}
		if n.Readings != nil {
			headline.Spans = append(headline.Spans, span(" · readings "+edge.Age(n.ReadAge)+" ago", muted))
		}
		lines = append(lines, headline)
		wide := int(area.Width) - 40
		if wide < 20 {
			wide = 20
		}
		if wide > 60 {
			wide = 60
		}
		row := func(k string, v ...lm.Span) {
			l := lm.NewLine(span("  ", dim), eyebrow(pad(k, 8), muted))
			l.Spans = append(l.Spans, v...)
			lines = append(lines, l)
		}
		r := n.Readings
		hs := h.hist[n.ID]
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
					append(meterN(l, -1, 0, false, wide), span(fmt.Sprintf(" %.0f%%", 100*l), ink))...)...)
			} else if r.RAMUsedGB > 0 {
				row("memory", span(fmt.Sprintf("%.1f GB used · no total reported", r.RAMUsedGB), ink))
			}
			th, hasTh := thresholdIn(firing, "disk")
			for i, d := range r.Disks {
				k := ""
				if i == 0 {
					k = "disk"
				}
				l := d.UsedGB / d.TotalGB
				from := -1.0
				if hasTh {
					from = th / 100
				}
				v := []lm.Span{span(pad(d.Mount, 18), ink)}
				v = append(v, meterN(l, from, 0, false, wide)...)
				v = append(v, span(fmt.Sprintf(" %.0f%%", 100*l), ink))
				if hasTh && 100*l > th {
					v = append(v, span("   ", dim), bold(fmt.Sprintf("▲ over %s", fmtGB(th)), live))
				}
				row(k, v...)
			}
			if r.GPU != "" {
				row("gpu", append([]lm.Span{span(pad(r.GPU, 18), ink)}, append(meterN(r.GPUMemUsed, -1, 0, false, wide), span(fmt.Sprintf(" %.0f%% memory", 100*r.GPUMemUsed), ink))...)...)
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
	for _, rl := range h.snap.Rules {
		dot, dc := "○", dim
		if strings.HasPrefix(rl.State, "firing") {
			dot, dc = "●", live
		}
		state := rl.State
		if state == "quiet" {
			state = ""
		}
		lines = append(lines, lm.NewLine(span("          ", dim), bold(dot+" ", dc), span(pad(rl.When, 18), ink),
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
		c := teal
		if hasTh && x > th {
			c = live
		}
		out = append(out, span(string(ramp[i]), c))
	}
	return out
}

// --- the strip's cells, with the CLI's honesty rules -------------------------------------------

func cellTemp(n edge.NodeHealth, firing []string) []lm.Span {
	if n.Readings == nil || n.Readings.CPUTempC == nil {
		return append(meter(0, false, -1, 0, false), span(pad("", 9), dim))
	}
	from := -1.0
	if th, ok := thresholdIn(firing, "cpu_temp"); ok {
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

func cellDisk(n edge.NodeHealth, firing []string) []lm.Span {
	l, ok := n.DiskLevel()
	if !ok {
		return append(meter(0, false, -1, 0, false), span(pad("", 9), dim))
	}
	from := -1.0
	if th, ok := thresholdIn(firing, "disk"); ok {
		from = th / 100
	}
	return append(meterN(l, from, 0, false, meterCells), span(pad(fmt.Sprintf(" %.0f%%", 100*l), 9), ink))
}
