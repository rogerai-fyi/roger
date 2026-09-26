// Package fleetview holds the Edge fleet-management screens built on Limoni (immediate mode,
// per-cell diff, a semantic tree the tests address by name). It is an experiment beside the
// Bubble Tea TUI: the first screen is Health (features/edge/health.feature §2, §5, §6), a new
// surface with no existing tests to migrate, so the comparison is clean. The package knows the
// fleet's health types and nothing about how they are read: a HealthSource is handed in.
package fleetview

import (
	"context"
	"fmt"
	"strings"
	"time"

	lm "github.com/thebanri/limoni"
	"github.com/thebanri/limoni/widgets"

	"rogerai.fm/roger/v6/internal/edge"
)

// HealthSnapshot is one read of everything the screen draws, taken together so one frame is
// internally consistent (a node's row, its readout and its alerts come from the same moment).
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

// The palette: paper ink on the terminal's own ground, red as the ONE signal color.
var (
	live  = lm.RGB(224, 35, 28)
	ink   = lm.RGB(224, 222, 214)
	muted = lm.RGB(150, 146, 138)
	dim   = lm.RGB(105, 101, 93)
)

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
	table  *lm.TableState
}

// NewHealth makes the screen over src, re-reading every 2s.
func NewHealth(src HealthSource) *Health {
	return &Health{Source: src, Every: 2 * time.Second, table: lm.NewTableState()}
}

// Run opens the screen full-screen until q/esc or ctx ends.
func (h *Health) Run(ctx context.Context) error {
	return lm.RunWithContext(ctx, h.Draw, lm.WithTitle("roger edge health"), lm.WithFPS(4))
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

	h.refresh()
	if h.sel >= len(h.snap.Nodes) {
		h.sel = len(h.snap.Nodes) - 1
	}
	if h.sel < 0 {
		h.sel = 0
	}

	rows := lm.SplitVertical(f.Area(), lm.Fixed(1), lm.Fill(), lm.Fixed(9), lm.Fixed(1))
	f.RenderWidget(lm.NewParagraph(h.header()).WithStyle(lm.Fg(muted)), rows[0])
	if h.err != nil {
		f.RenderWidget(lm.NewBlock().Rounded().WithTitle(" could not read the fleet ").WithBorderStyle(lm.Fg(live)).
			WithChild(lm.NewParagraph(h.err.Error()).WithStyle(lm.Fg(ink))).WithPadding(0, 1, 0, 1), rows[1])
	} else {
		cols := lm.SplitHorizontal(rows[1], lm.Percentage(62), lm.Percentage(38))
		h.drawTable(f, cols[0])
		h.drawReadout(f, cols[1])
	}
	h.drawRules(f, rows[2])
	bar := " ↑↓ node · r re-read · q back"
	if h.BackHint != "" {
		bar = " ↑↓ node · r re-read · " + h.BackHint + " · q quit"
	}
	f.RenderWidget(lm.NewParagraph(bar).WithStyle(lm.Fg(dim)), rows[3])
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
	case ev.Key.Type == lm.KeyDown:
		h.sel++
	case ev.Key.Type == lm.KeyUp:
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
	if err == nil {
		h.snap = snap
	}
}

func (h *Health) header() string {
	n := len(h.snap.Nodes)
	firing := 0
	for _, a := range h.snap.Firing {
		firing += len(a)
	}
	s := fmt.Sprintf(" EDGE HEALTH · %d nodes", n)
	if firing > 0 {
		s += fmt.Sprintf(" · %d alert%s firing", firing, plural(firing))
	}
	if !h.snap.At.IsZero() {
		s += " · read " + h.snap.At.Format("15:04:05")
	}
	return s
}

func (h *Health) drawTable(f *lm.Frame, area lm.Rect) {
	h.table.Selected = h.sel
	fixed := func(w int) lm.TableConstraint { return lm.TableConstraint{Type: widgets.ConstraintFixed, Value: w} }
	t := lm.NewTable().WithID("fleet-health").
		WithHeaders("NODE", "TEMP", "MEMORY", "DISK", "UPTIME", "PRESENCE", "ALERTS").
		WithConstraints(lm.TableConstraint{Type: widgets.ConstraintFill}, fixed(7), fixed(12), fixed(5), fixed(7), fixed(12), fixed(6)).
		WithState(h.table).WithSelectedStyle(lm.Fg(live).Bold())
	for _, n := range h.snap.Nodes {
		t = t.WithRow(n.Name, temp(n), mem(n), disk(n), uptime(n), presence(n), alertCount(h.snap.Firing[n.ID]))
	}
	f.RenderWidget(lm.NewBlock().Rounded().WithTitle(" THE FLEET ").WithBorderStyle(lm.Fg(muted)).
		WithChild(t).WithPadding(0, 1, 0, 1), area)
}

// drawReadout is the selected node in full: every reading it reports with its age, its gauges,
// and the rules firing for it. What it does not report is not drawn - never a zero.
func (h *Health) drawReadout(f *lm.Frame, area lm.Rect) {
	if len(h.snap.Nodes) == 0 {
		f.RenderWidget(lm.NewBlock().Rounded().WithTitle(" no nodes ").WithBorderStyle(lm.Fg(dim)).
			WithChild(lm.NewParagraph("nothing on this Edge reports yet").WithStyle(lm.Fg(dim))).WithPadding(0, 1, 0, 1), area)
		return
	}
	n := h.snap.Nodes[h.sel]
	parts := lm.SplitVertical(area, lm.Fill(), lm.Fixed(4))
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%-11s %s\n", k, v) }
	line("presence", presence(n))
	if !n.LastHeard.IsZero() {
		line("last heard", edge.Age(time.Since(n.LastHeard))+" ago")
	}
	r := n.Readings
	title := " " + n.Name + " · " + n.Kind + " "
	if r == nil {
		b.WriteString("\nthis node reports no health readings\n")
	} else {
		line("readings", edge.Age(n.ReadAge)+" ago")
		age := ""
		if r.CPUTempC != nil {
			line("cpu temp", fmt.Sprintf("%.1f°C%s", *r.CPUTempC, age))
		}
		if r.CPULoad1 != nil {
			line("cpu load", fmt.Sprintf("%.2f%s", *r.CPULoad1, age))
		}
		if r.RAMTotalGB > 0 {
			line("memory", fmt.Sprintf("%.1f / %.0f GB (%.0f%%)%s", r.RAMUsedGB, r.RAMTotalGB, 100*r.RAMUsedGB/r.RAMTotalGB, age))
		} else if r.RAMUsedGB > 0 {
			line("memory", fmt.Sprintf("%.1f GB used (no total reported)%s", r.RAMUsedGB, age))
		}
		for _, d := range r.Disks {
			line("disk", fmt.Sprintf("%s %.0f / %.0f GB (%.0f%%)%s", d.Mount, d.UsedGB, d.TotalGB, 100*d.UsedGB/d.TotalGB, age))
		}
		if r.UptimeS > 0 {
			line("uptime", edge.Age(time.Duration(r.UptimeS)*time.Second)+age)
		}
		if r.GPU != "" {
			line("gpu", fmt.Sprintf("%s %.0f%% memory%s", r.GPU, 100*r.GPUMemUsed, age))
		}
		if r.SDWearPct != nil {
			line("sd card", fmt.Sprintf("%.0f%% worn%s", *r.SDWearPct, age))
		}
	}
	if firing := h.snap.Firing[n.ID]; len(firing) > 0 {
		b.WriteString("\n")
		for _, a := range firing {
			line("ALERT", "firing · "+a)
		}
	} else {
		b.WriteString("\n")
		line("alerts", "none firing")
	}
	f.RenderWidget(lm.NewBlock().Rounded().WithTitle(title).WithBorderStyle(lm.Fg(live)).
		WithChild(lm.NewParagraph(b.String()).WithWrap(false).WithStyle(lm.Fg(ink))).WithPadding(0, 1, 0, 1), parts[0])

	// Gauges: memory and the fullest disk, only as levels of a REAL total.
	g := lm.NewBlock().WithTitle(" levels ").WithBorderStyle(lm.Fg(dim))
	f.RenderWidget(g, parts[1])
	inner := lm.SplitVertical(parts[1], lm.Fixed(1), lm.Fixed(1), lm.Fixed(1), lm.Fill())
	if l, ok := n.MemoryLevel(); ok {
		f.RenderWidget(gauge("memory", l), inset(inner[1]))
	} else {
		f.RenderWidget(lm.NewParagraph("  memory: no total reported").WithStyle(lm.Fg(dim)), inner[1])
	}
	if l, ok := n.DiskLevel(); ok {
		f.RenderWidget(gauge("disk", l), inset(inner[2]))
	} else {
		f.RenderWidget(lm.NewParagraph("  disk: nothing mounted reports a total").WithStyle(lm.Fg(dim)), inner[2])
	}
}

func (h *Health) drawRules(f *lm.Frame, area lm.Rect) {
	fixed := func(w int) lm.TableConstraint { return lm.TableConstraint{Type: widgets.ConstraintFixed, Value: w} }
	t := lm.NewTable().WithID("alert-rules").WithHeaders("RULE", "WHEN", "SCOPE", "KIND", "STATE").
		WithConstraints(fixed(14), fixed(20), fixed(16), fixed(9), lm.TableConstraint{Type: widgets.ConstraintFill})
	for _, r := range h.snap.Rules {
		t = t.WithRow(r.ID, r.When, r.Scope, r.Kind, r.State)
	}
	sinks := h.snap.Sinks
	if sinks == "" {
		sinks = "none configured - alerts stay on this machine"
	}
	f.RenderWidget(lm.NewBlock().Rounded().WithTitle(" ALERT RULES · sinks: "+sinks+" ").WithBorderStyle(lm.Fg(muted)).
		WithChild(t).WithPadding(0, 1, 0, 1), area)
}

func gauge(label string, v float64) lm.Widget {
	col := muted
	if v > 0.9 {
		col = live
	}
	return widgets.Gauge{ID: "gauge-" + label, Ratio: v, Label: fmt.Sprintf("%s %.0f%%", label, 100*v),
		GaugeStyle: lm.Fg(col), Style: lm.Fg(ink)}
}

func inset(r lm.Rect) lm.Rect {
	c := lm.SplitHorizontal(r, lm.Fixed(2), lm.Fill(), lm.Fixed(2))
	return c[1]
}

// --- the cells, with the CLI's honesty rules -------------------------------------------------

func temp(n edge.NodeHealth) string {
	if n.Readings == nil || n.Readings.CPUTempC == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f°C", *n.Readings.CPUTempC)
}

func mem(n edge.NodeHealth) string {
	if n.Readings == nil || n.Readings.RAMUsedGB <= 0 {
		return "-"
	}
	if n.Readings.RAMTotalGB > 0 {
		return fmt.Sprintf("%.1f/%.0f GB", n.Readings.RAMUsedGB, n.Readings.RAMTotalGB)
	}
	return fmt.Sprintf("%.1f GB", n.Readings.RAMUsedGB)
}

func disk(n edge.NodeHealth) string {
	if l, ok := n.DiskLevel(); ok {
		return fmt.Sprintf("%.0f%%", 100*l)
	}
	return "-"
}

func uptime(n edge.NodeHealth) string {
	if n.Readings == nil || n.Readings.UptimeS <= 0 {
		return "-"
	}
	return edge.Age(time.Duration(n.Readings.UptimeS) * time.Second)
}

func presence(n edge.NodeHealth) string {
	if n.Dark {
		return "offline " + edge.Age(n.DarkFor)
	}
	if n.Readings != nil && n.ReadAge > time.Minute {
		return "online (reading " + edge.Age(n.ReadAge) + " old)"
	}
	return "online"
}

func alertCount(a []string) string {
	if len(a) == 0 {
		return ""
	}
	return fmt.Sprintf("%d", len(a))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
