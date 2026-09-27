package fleetview

// THE RACK - the shared parts of the Edge screens. The Edge is drawn as a 1950s station's
// equipment rack: every machine is a unit with its own meter, the authority is the transmitter
// at the top with its carrier breathing, adopted machines are patched in below, discovered
// devices wait on the bench. No boxes: a unit is a 1-cell colored rail, a name, a meter; regions
// are separated by air and one hairline. Red is the ONE signal: selection, on air, alarm.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	lm "github.com/thebanri/limoni"

	"rogerai.fm/roger/v6/internal/edge"
)

const meterCells = 9

var ramp = []rune("▁▂▃▄▅▆▇█")

// motion is whether anything breathes. Off under NO_COLOR / ROGER_REDUCED_MOTION: every breather
// rests at its lit end state.
var motion = os.Getenv("NO_COLOR") == "" && os.Getenv("ROGER_REDUCED_MOTION") == ""

// eye is THE signature: a tuning-eye tube in two cells, its iris closing as the signal drops.
// S9 ◉◉ (the right cell breathes), S7 ◉◎, S5 ◎◎, S3 ◎○ shaded, S1 ○○ dim; dark ··; a device
// asking to be adopted ◌◌ in amber, blinking. An alarm turns the LEFT cell red while the right
// keeps the signal, so "S7 with an alarm" is one glance. row offsets the breathing so a rack of
// units never blinks in unison.
func eye(level float64, dark, asking, alarm bool, frame, row int) []lm.Span {
	f := frame + row*5
	switch {
	case asking:
		if motion && (f/6)%2 == 1 {
			return []lm.Span{span("◌ ", glow)}
		}
		return []lm.Span{span("◌◌", glow)}
	case dark:
		return []lm.Span{span("··", ash)}
	}
	var l, r string
	c := signal
	switch u := int(level*9 + 0.5); {
	case u >= 9:
		l, r = "◉", "◉"
		if motion && (f/12)%2 == 1 {
			r = "◎"
		}
	case u >= 7:
		l, r = "◉", "◎"
	case u >= 5:
		l, r = "◎", "◎"
	case u >= 3:
		l, r, c = "◎", "○", shade
	default:
		l, r, c = "○", "○", dim
	}
	if alarm {
		return []lm.Span{bold(l, live), span(r, c)}
	}
	return []lm.Span{span(l, c), span(r, c)}
}

// meter is a 9-cell S-meter as spans. level is 0..1 (lit cells); alarmFrom is the level a rule
// fires at (lit cells from there are red; <0 = no rule); ok=false is "no reading": a dim ┈ rail,
// never an empty meter that would read as zero. The frontier cell breathes when breathe is set,
// but the lit COUNT never moves while it animates.
func meter(level float64, ok bool, alarmFrom float64, frame int, breathe bool) []lm.Span {
	if !ok {
		return []lm.Span{span(strings.Repeat("┈", meterCells), dim)}
	}
	return meterN(level, alarmFrom, frame, breathe, meterCells)
}

func meterN(level, alarmFrom float64, frame int, breathe bool, cells int) []lm.Span {
	if level < 0 {
		level = 0
	}
	if level > 1 {
		level = 1
	}
	lit := int(level*float64(cells) + 0.5)
	if level > 0 && lit == 0 {
		lit = 1
	}
	alarmCell := cells + 1
	if alarmFrom >= 0 {
		alarmCell = int(alarmFrom * float64(cells))
		if level > alarmFrom && lit <= alarmCell { // over the line is always seen as over the line
			lit = alarmCell + 1
		}
	}
	var out []lm.Span
	var b strings.Builder
	cur, started := dim, false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, span(b.String(), cur))
			b.Reset()
		}
	}
	for i := 0; i < cells; i++ {
		var r rune
		c := dim
		switch {
		case i < lit:
			r = ramp[6]
			c = signal
			if i >= alarmCell {
				c = live
			}
			if i == lit-1 && breathe && motion {
				r = ramp[5+(frame/3)%3] // ▆ ▇ █ - breathing, never below the rail
			}
		case i == alarmCell:
			r = '┊' // the threshold, so the eye sees how far from the line a level sits
		default:
			r = ramp[0]
		}
		if !started || c != cur {
			flush()
			cur, started = c, true
		}
		b.WriteRune(r)
	}
	flush()
	return out
}

// carrierLevel is how strongly a unit is heard: full within the heartbeat, fading over the
// liveness window, silent when dark.
func carrierLevel(lastHeard time.Time, dark bool, now time.Time) float64 {
	if dark || lastHeard.IsZero() {
		return 0
	}
	age := now.Sub(lastHeard)
	switch {
	case age <= 30*time.Second:
		return 1
	case age >= edge.DefaultTTL:
		return 0.15
	}
	return 1 - 0.85*float64(age-30*time.Second)/float64(edge.DefaultTTL-30*time.Second)
}

// sUnit words a level the operator's way: S1..S9.
func sUnit(level float64) string {
	u := int(level*9 + 0.5)
	if u < 1 {
		u = 1
	}
	return "S" + strconv.Itoa(u)
}

// thresholdIn reads the number out of a rule's wording ("cpu_temp > 85") when it names metric.
func thresholdIn(wordings []string, metric string) (float64, bool) {
	for _, w := range wordings {
		f := strings.Fields(w)
		if len(f) == 3 && f[0] == metric {
			if v, err := strconv.ParseFloat(f[2], 64); err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

// alarm is the sentence at the end of a meter row when a rule fires on it.
func alarm(wordings []string, metric string) []lm.Span {
	for _, w := range wordings {
		if strings.HasPrefix(w, metric+" ") {
			return []lm.Span{span("   ", dim), bold("▲ "+w, live)}
		}
	}
	return nil
}

// eyebrow is a section word: uppercase, quiet.
func eyebrow(s string, c lm.Color) lm.Span { return span(strings.ToUpper(s), c) }

// chips draws capabilities: VERIFIED bold on its color, claimed lowercase and quiet with a dot.
func chips(caps []Cap) []lm.Span {
	var out []lm.Span
	for _, c := range caps {
		if c.State == "VERIFIED" {
			out = append(out, bold(strings.ToUpper(c.Name), capColor(c.Name)), span("  ", dim))
		} else {
			out = append(out, span(c.Name+"·", glow), span("  ", dim))
		}
	}
	return out
}

// brandBand is the one-row head: the mark, the name, the on-air lamp breathing, the tabs, and
// the right-hand facts - then the rule: heavy red for a stretch, then a hairline.
func brandBand(fr *lm.Frame, area lm.Rect, tabs []string, tab int, right string, frame int) {
	rows := lm.SplitVertical(area, lm.Fixed(1), lm.Fixed(1))
	lampC := live
	if motion && (frame/6)%2 == 1 {
		lampC = lamp
	}
	head := lm.NewLine(bold(" ▟•▙ ", live), bold("ROGER", paper), span(" · ", live), bold("EDGE", paper),
		span("    ", dim), bold("◉", lampC), span(" ON AIR", muted), span("            ", dim))
	for i, t := range tabs {
		if i == tab {
			head.Spans = append(head.Spans, bold(strings.ToUpper(t), paper), span("   ", dim))
		} else {
			head.Spans = append(head.Spans, span(strings.ToLower(t), muted), span("   ", dim))
		}
	}
	// Right-align the facts by padding to the width; when they do not fit, the clock goes first,
	// then whatever else must, so the alert count is the last thing lost.
	used := 0
	for _, s := range head.Spans {
		used += len([]rune(s.Text))
	}
	parts := strings.Split(right, " · ")
	for len(parts) > 1 && int(area.Width)-used-len([]rune(strings.Join(parts, " · ")))-1 < 1 {
		parts = parts[1:]
	}
	right = strings.Join(parts, " · ")
	gap := int(area.Width) - used - len([]rune(right)) - 1
	if gap > 1 {
		head.Spans = append(head.Spans, span(strings.Repeat(" ", gap), dim))
	}
	head.Spans = append(head.Spans, span(right, muted))
	fr.RenderWidget(lm.NewText(head).WithWrap(false), rows[0])
	w := int(area.Width)
	heavy := 24
	if heavy > w {
		heavy = w
	}
	fr.RenderWidget(lm.NewText(lm.NewLine(span(" "+strings.Repeat("━", heavy), live), span(strings.Repeat("─", w-heavy-1), dim))).WithWrap(false), rows[1])
}

// hairline draws the one vertical rule between two regions, as tall as their content.
func hairline(fr *lm.Frame, area lm.Rect, rows int) {
	if rows > int(area.Height) || rows <= 0 {
		rows = int(area.Height)
	}
	lines := make([]lm.Line, 0, rows)
	for i := 0; i < rows; i++ {
		lines = append(lines, lm.NewLine(span("│", dim)))
	}
	fr.RenderWidget(lm.NewText(lines...).WithWrap(false), area)
}

// actionBar is the keys at the foot: key cap in paper, verb quiet, the last outcome at the right.
func actionBar(fr *lm.Frame, area lm.Rect, acts []action, toast string, toastErr bool, toastAt time.Time) {
	l := lm.NewLine(span(" ", dim))
	for _, a := range acts {
		l.Spans = append(l.Spans, bold(a.key, paper), span(" "+a.label+"   ", muted))
	}
	if toast != "" && time.Since(toastAt) < 6*time.Second {
		c := signal
		if toastErr {
			c = live
		}
		used := 0
		for _, s := range l.Spans {
			used += len([]rune(s.Text))
		}
		msg := "▸ " + toast
		if gap := int(area.Width) - used - len([]rune(msg)) - 1; gap > 1 {
			l.Spans = append(l.Spans, span(strings.Repeat(" ", gap), dim))
		}
		l.Spans = append(l.Spans, bold(msg, c))
	}
	fr.RenderWidget(lm.NewText(l).WithWrap(false), area)
}

// confirmBox is the question, centered: a heavy red rule, the title, one sentence, two keys.
// Our own, so it wears the palette (the library dialog paints a gradient of its own).
func confirmBox(fr *lm.Frame, title, sentence, yes, no string) {
	a := fr.Area()
	w := uint16(60)
	if a.Width < w+4 {
		w = a.Width - 4
	}
	h := uint16(6)
	box := lm.Rect{X: a.X + (a.Width-w)/2, Y: a.Y + (a.Height-h)/2, Width: w, Height: h}
	lines := []lm.Line{
		lm.NewLine(span(strings.Repeat("━", int(w)), live)),
		lm.NewLine(span("  ", dim), bold(title, paper)),
		lm.NewLine(span("  ", dim), span(sentence, ink)),
		lm.NewLine(),
		lm.NewLine(span("  ", dim), bold(yes, live), span("      ", dim), span(no, muted)),
		lm.NewLine(span(strings.Repeat("─", int(w)), dim)),
	}
	fr.RenderWidget(lm.NewBlock().WithBorders(0).WithStyle(lm.Bg(ground)).
		WithChild(lm.NewText(lines...).WithWrap(false).WithStyle(lm.Bg(ground))), box)
}

// elide cuts s to n cells with an ellipsis, so a long name folds instead of running into the
// next column.
func elide(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func fmtGB(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) }

func clock(t time.Time) string { return t.Format("15:04:05") }

func unitsWord(n int) string { return fmt.Sprintf("%d unit%s", n, plural(n)) }

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
