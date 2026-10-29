package qr

// show.go decides whether an invite's QR code can be drawn where it is going
// (features/join/surfaces.feature, "The QR fits a normal terminal").

import (
	"io"
	"os"

	"github.com/charmbracelet/x/term"
)

// TooSmall is said instead of a code that would wrap, or that cannot be drawn without colours.
const TooSmall = "terminal too small for the QR; paste the line instead"

// ForTerminal is text as a code drawn for a terminal cols wide, or TooSmall when it would wrap,
// when colours are off (the code needs its white ground to scan in a dark terminal), or when
// text does not fit in a code at all.
func ForTerminal(text string, cols int, noColor bool) string {
	c, err := Encode([]byte(text), L) // on a screen nothing smudges: the smallest code scans best
	if err != nil || noColor || c.Size+2*Quiet > cols {
		return TooSmall + "\n"
	}
	return c.Terminal()
}

// Show writes text's code to out, sized to out when it is a terminal (80 columns otherwise), and
// honouring NO_COLOR.
func Show(out io.Writer, text string) {
	cols := 80
	if f, ok := out.(*os.File); ok {
		if w, _, err := term.GetSize(f.Fd()); err == nil && w > 0 {
			cols = w
		}
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	io.WriteString(out, ForTerminal(text, cols, noColor))
}
