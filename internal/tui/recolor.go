package tui

// recolor.go: carry the lines already on screen across a live /theme or /colorblind
// switch.
//
// The AGENT transcript, the chat transcript and the remote-session lines are stored
// already rendered, so each line's colors were fixed when it was drawn. A live switch
// retints everything drawn afterwards through the palette slots and the renderer's
// light/dark arm, but a stored line would keep its old colors: after /theme light, an
// earlier answer drawn in the dark arm's light ink would sit on the light page.
//
// So the switch also rewrites the stored lines, one color at a time: every color
// parameter in their SGR sequences that is a palette token's old value becomes that
// token's new value, at the terminal's own color profile. Text and layout are left
// exactly as they are.

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// themeTokens are the adaptive colors the stored lines are drawn with. The ink ramp
// comes first, so where two tokens share a value the ink's mapping wins.
func themeTokens() []lipgloss.AdaptiveColor {
	b := activeBoard()
	toks := []lipgloss.AdaptiveColor{
		cInk, cBody, cDim, cFaint, cRule, cInkBg,
		b.live, b.signal, b.dialGlow, b.dial, b.liveSurface,
		cBand, cDeck, cSlate, cSlateLit, cSlateShade, cSlateText, cReply, cTubeGlow,
	}
	return append(toks, waveSpectrum...)
}

// activeBoard is the hue board the slots hold right now.
func activeBoard() paletteBoard {
	return paletteBoard{
		live: *cRed.AdaptiveColor, signal: *cSignal.AdaptiveColor, dialGlow: *cDialGlow.AdaptiveColor,
		dial: *cDial.AdaptiveColor, liveSurface: *cLiveSurface.AdaptiveColor,
	}
}

func armOf(c lipgloss.AdaptiveColor, dark bool) string {
	if dark {
		return c.Dark
	}
	return c.Light
}

// armSwap pairs each token's value on one arm with its value on the other.
func armSwap(toks []lipgloss.AdaptiveColor, fromDark, toDark bool) [][2]string {
	out := make([][2]string, 0, len(toks))
	for _, t := range toks {
		out = append(out, [2]string{armOf(t, fromDark), armOf(t, toDark)})
	}
	return out
}

// boardSwap pairs each hue of one board with the same hue on another, on one arm.
func boardSwap(from, to paletteBoard, dark bool) [][2]string {
	f := []lipgloss.AdaptiveColor{from.live, from.signal, from.dialGlow, from.dial, from.liveSurface}
	t := []lipgloss.AdaptiveColor{to.live, to.signal, to.dialGlow, to.dial, to.liveSurface}
	out := make([][2]string, len(f))
	for i := range f {
		out[i] = [2]string{armOf(f[i], dark), armOf(t[i], dark)}
	}
	return out
}

// sgrMap turns hex pairs into SGR color-parameter pairs at the active profile, in both
// the foreground and the background form ("38;2;r;g;b" -> "38;2;r;g;b", "48;5;n" ->
// ...). The first pair for a given old value wins. Nil when there is no color at all.
func sgrMap(pairs [][2]string) map[string]string {
	p := lipgloss.DefaultRenderer().ColorProfile()
	if p == termenv.Ascii {
		return nil
	}
	out := map[string]string{}
	for _, pr := range pairs {
		from, to := p.Color(pr[0]), p.Color(pr[1])
		if from == nil || to == nil {
			continue
		}
		for _, bg := range []bool{false, true} {
			k, v := from.Sequence(bg), to.Sequence(bg)
			if k == "" || v == "" || k == v {
				continue
			}
			if _, seen := out[k]; !seen {
				out[k] = v
			}
		}
	}
	return out
}

var sgrSeq = regexp.MustCompile("\x1b\\[([0-9;]*)m")

// recolorSGR rewrites the color parameters of every SGR sequence in s through tbl. A
// 24-bit color is five parameters (38;2;r;g;b), a 256-color one three (38;5;n), and a
// 16-color one a single number; each is looked up whole, so a value is never split.
func recolorSGR(s string, tbl map[string]string) string {
	if len(tbl) == 0 || !strings.Contains(s, "\x1b[") {
		return s
	}
	return sgrSeq.ReplaceAllStringFunc(s, func(seq string) string {
		ps := strings.Split(seq[2:len(seq)-1], ";")
		out := make([]string, 0, len(ps))
		for i := 0; i < len(ps); {
			n := 1
			if (ps[i] == "38" || ps[i] == "48") && i+1 < len(ps) {
				switch ps[i+1] {
				case "2":
					n = 5
				case "5":
					n = 3
				}
				n = min(n, len(ps)-i)
			}
			spec := strings.Join(ps[i:i+n], ";")
			if to, ok := tbl[spec]; ok {
				spec = to
			}
			out = append(out, spec)
			i += n
		}
		return "\x1b[" + strings.Join(out, ";") + "m"
	})
}

// recolorStored rewrites the stored, already-rendered transcripts through tbl. Each
// slice is copied rather than edited in place, because an earlier model value may
// still share its backing array.
func (m model) recolorStored(tbl map[string]string) model {
	if len(tbl) == 0 {
		return m
	}
	for _, lines := range []*[]string{&m.transcript, &m.agentLines, &m.rsLines} {
		if len(*lines) == 0 {
			continue
		}
		out := make([]string, len(*lines))
		for i, ln := range *lines {
			out[i] = recolorSGR(ln, tbl)
		}
		*lines = out
	}
	return m
}
