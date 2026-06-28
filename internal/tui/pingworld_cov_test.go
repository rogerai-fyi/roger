package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestWorldBufferOneRedInvariant pins the screensaver's signature law: across frames + sizes,
// the ONLY red cells are Ping eyes (•) or the single on-air star (◉) — nothing else.
func TestWorldBufferOneRedInvariant(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {50, 16}} {
		for f := 0; f < 160; f += 7 {
			buf := worldBuffer(sz[0], sz[1], f, 3)
			if len(buf) != sz[1] {
				t.Fatalf("buffer height %d, want %d", len(buf), sz[1])
			}
			eyes := 0
			for _, row := range buf {
				if len(row) != sz[0] {
					t.Fatalf("row width %d, want %d", len(row), sz[0])
				}
				for _, c := range row {
					if c.eye {
						if c.r != '•' && c.r != '◉' {
							t.Errorf("frame %d size %v: red cell %q is not a Ping eye • or on-air star ◉", f, sz, string(c.r))
						}
						if c.r == '•' {
							eyes++
						}
					}
				}
			}
			if eyes == 0 {
				t.Errorf("frame %d size %v: no red Ping eye on screen", f, sz)
			}
		}
	}
}

func TestWorldDeterministicAndEvolving(t *testing.T) {
	if renderWorld(80, 24, 42, 9) != renderWorld(80, 24, 42, 9) {
		t.Error("renderWorld must be deterministic for the same (w,h,frame,seed)")
	}
	if renderWorld(80, 24, 42, 9) == renderWorld(80, 24, 220, 9) {
		t.Error("the world should evolve across frames (twinkle/positions)")
	}
}

func TestWorldSizeBoundsAndDegenerate(t *testing.T) {
	for _, bad := range [][2]int{{0, 10}, {10, 0}, {-5, -5}} {
		if renderWorld(bad[0], bad[1], 0, 1) != "" {
			t.Errorf("degenerate size %v should render empty", bad)
		}
	}
	_ = renderWorld(1, 1, 5, 1) // must not panic
	w := 64
	for _, line := range strings.Split(renderWorld(w, 20, 33, 2), "\n") {
		if vis := len([]rune(stripANSI(line))); vis > w {
			t.Errorf("line overflows width: %d > %d", vis, w)
		}
	}
}

func TestWorldHasOneOnAirStar(t *testing.T) {
	// exactly one red ◉ on EVERY frame (a shooting star / twinkle must never clobber it).
	for f := 0; f < 200; f += 13 {
		buf := worldBuffer(80, 24, f, 5)
		stars := 0
		for _, row := range buf {
			for _, c := range row {
				if c.r == '◉' {
					if !c.eye {
						t.Errorf("frame %d: on-air star ◉ must be red (eye)", f)
					}
					stars++
				}
			}
		}
		if stars != 1 {
			t.Errorf("frame %d: want exactly one on-air star, got %d", f, stars)
		}
	}
}

func TestPingWorldModelKeysAndTick(t *testing.T) {
	_, cmd := pingWorldModel{w: 80, h: 24}.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Error("any key should quit the standalone screensaver")
	}
	m2, _ := pingWorldModel{}.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if mm := m2.(pingWorldModel); mm.w != 100 || mm.h != 30 {
		t.Errorf("WindowSizeMsg should size the world, got %dx%d", mm.w, mm.h)
	}
	m3, cmd3 := pingWorldModel{w: 80, h: 24}.Update(worldTickMsg{})
	if mm := m3.(pingWorldModel); mm.frame != 1 || cmd3 == nil {
		t.Error("worldTickMsg should advance the frame + reschedule")
	}
}
