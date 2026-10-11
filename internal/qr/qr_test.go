package qr

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

type vector struct {
	Data    string   `json:"data"`
	Error   string   `json:"error"`
	Mask    int      `json:"mask"`
	Version int      `json:"version"`
	Matrix  []string `json:"matrix"`
}

var levels = map[string]Level{"L": L, "M": M, "Q": Q, "H": H}

func loadVectors(t *testing.T) []vector {
	raw, err := os.ReadFile("testdata/qr_vectors.json")
	require.NoError(t, err)
	var f struct{ Cases []vector }
	require.NoError(t, json.Unmarshal(raw, &f))
	require.NotEmpty(t, f.Cases)
	return f.Cases
}

func rows(c *Code) []string {
	out := make([]string, c.Size)
	for y, row := range c.Modules {
		var b strings.Builder
		for _, d := range row {
			if d {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		out[y] = b.String()
	}
	return out
}

// Every vector from an independent encoder (python-qrcode) matches module for module: the version,
// the data and error-correction codewords and their interleaving, the placement, the format and
// version information, and each of the eight masks. The mask is fixed in the vectors: encoders
// score masks differently (and any mask decodes), so the automatic choice is tested on its own.
func TestMatchesTheIndependentEncoderModuleForModule(t *testing.T) {
	for _, v := range loadVectors(t) {
		c, err := EncodeWith([]byte(v.Data), levels[v.Error], 0, v.Mask)
		require.NoError(t, err)
		require.Equal(t, v.Version, c.Version, "version for %q at %s", v.Data, v.Error)
		require.Equal(t, v.Matrix, rows(c), "the modules of %q at %s mask %d", v.Data, v.Error, v.Mask)
	}
}

// The automatic mask is the one with the lowest standard penalty.
func TestTheAutomaticMaskHasTheLowestPenalty(t *testing.T) {
	for _, v := range loadVectors(t) {
		auto, err := Encode([]byte(v.Data), levels[v.Error])
		require.NoError(t, err)
		best := -1
		for k := 0; k < 8; k++ {
			c, err := EncodeWith([]byte(v.Data), levels[v.Error], 0, k)
			require.NoError(t, err)
			p := (&matrix{Code: c}).penalty()
			if best < 0 || p < best {
				best = p
			}
			if k == auto.Mask {
				require.Equal(t, best, p)
			}
		}
		require.Equal(t, best, (&matrix{Code: auto}).penalty(), "%q at %s", v.Data, v.Error)
	}
}

// The block table agrees with the geometry: total codewords equal the modules left for data.
func TestBlockTableMatchesTheGeometry(t *testing.T) {
	for v := 1; v <= 40; v++ {
		m := newCode(v)
		m.drawFunctionPatterns()
		free := 0
		for y := range m.fn {
			for x := range m.fn[y] {
				if !m.fn[y][x] {
					free++
				}
			}
		}
		for lvl := L; lvl <= H; lvl++ {
			total := 0
			for _, g := range ecBlocks[v][lvl] {
				total += g.count * g.total
			}
			require.Equal(t, free/8, total, "version %d level %d", v, lvl)
		}
	}
}

func TestTooLongIsRefused(t *testing.T) {
	_, err := Encode(make([]byte, 3000), M)
	require.ErrorIs(t, err, ErrTooLong)
	_, err = EncodeWith([]byte("hello"), M, 41, -1)
	require.ErrorIs(t, err, ErrTooLong)
}

func TestTerminalHasAQuietZoneAndExplicitColours(t *testing.T) {
	c, err := Encode([]byte("roger://join?kind=edge"), M)
	require.NoError(t, err)
	out := c.Terminal()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	require.Len(t, lines, (c.Size+2*Quiet+1)/2)
	for _, l := range lines {
		require.True(t, strings.HasPrefix(l, "\x1b[30;107m"), "black on white, whatever the terminal's theme")
		require.True(t, strings.HasSuffix(l, "\x1b[0m"))
		body := strings.TrimSuffix(strings.TrimPrefix(l, "\x1b[30;107m"), "\x1b[0m")
		require.Equal(t, c.Size+2*Quiet, len([]rune(body)))
		require.True(t, strings.HasPrefix(body, strings.Repeat(" ", Quiet)), "a quiet zone on the left")
	}
	require.Equal(t, strings.Repeat(" ", c.Size+2*Quiet), strings.TrimSuffix(strings.TrimPrefix(lines[0], "\x1b[30;107m"), "\x1b[0m"), "and on top")
}

func TestForTerminal(t *testing.T) {
	invite := "roger join roger:tower?a=192.168.1.20%3A8787&fp=sha256%3A9f3c0d5e7a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293e1&c=ABCDE-FGHJK"
	c, err := Encode([]byte(invite), L)
	require.NoError(t, err)
	require.Equal(t, c.Terminal(), ForTerminal(invite, 80, false), "an 80-column terminal draws the code")
	lines := strings.Split(strings.TrimSuffix(c.Terminal(), "\n"), "\n")
	for _, l := range lines {
		require.LessOrEqual(t, utf8.RuneCountInString(stripANSI(l)), 80, "without wrapping")
	}
	need := c.Size + 2*Quiet
	require.Equal(t, c.Terminal(), ForTerminal(invite, need, false), "exactly wide enough draws it")
	require.Equal(t, TooSmall+"\n", ForTerminal(invite, need-1, false), "one column short says so")
	require.Equal(t, TooSmall+"\n", ForTerminal(invite, 200, true), "NO_COLOR says so")
	require.Equal(t, TooSmall+"\n", ForTerminal(strings.Repeat("x", 3000), 1000, false), "too much data says so")
}

func TestShow(t *testing.T) {
	var b strings.Builder
	t.Setenv("NO_COLOR", "restored after the test")
	os.Unsetenv("NO_COLOR")
	Show(&b, "roger join x")
	require.Equal(t, ForTerminal("roger join x", 80, false), b.String(), "a non-terminal is drawn for 80 columns")

	t.Setenv("NO_COLOR", "")
	b.Reset()
	Show(&b, "roger join x")
	require.Equal(t, TooSmall+"\n", b.String(), "NO_COLOR, even empty, turns the code off")

	f, err := os.CreateTemp(t.TempDir(), "out")
	require.NoError(t, err)
	defer f.Close()
	os.Unsetenv("NO_COLOR")
	Show(f, "roger join x") // a file is not a terminal: the 80-column default
	got, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	require.Equal(t, ForTerminal("roger join x", 80, false), string(got))
}

func stripANSI(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		s = s[:i] + s[i+j+1:]
	}
}

// TestBlocksAreTheTerminalRowsWithoutColour: the console's plain rows are exactly the terminal's
// rows with the colour codes taken off, so both screens show the same code.
func TestBlocksAreTheTerminalRowsWithoutColour(t *testing.T) {
	c, err := Encode([]byte("roger join roger://join?kind=tower-client"), L)
	if err != nil {
		t.Fatal(err)
	}
	plain := strings.NewReplacer("\x1b[30;107m", "", "\x1b[0m", "").Replace(c.Terminal())
	if got := c.Blocks(); got != plain {
		t.Fatalf("Blocks differs from the terminal rows:\n%q\n%q", got, plain)
	}
	if strings.Contains(c.Blocks(), "\x1b") {
		t.Fatal("Blocks carries colour codes")
	}
}
