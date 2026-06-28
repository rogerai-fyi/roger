package tui

// `roger --ping` (and the in-TUI `/ping` / `z`): the "Ping World" screensaver - a slow,
// relaxing little planet where Ping ambles along the horizon, another Ping or two wander by,
// stars twinkle + parallax-drift, and ONE star pulses red = a station on air (the band, seen
// from Ping's world at night). Design: docs/tui-ping-world-design.md.
//
// Two invariants the design (and a test) pin:
//   1. ONE RED. The whole world is ink/dim EXCEPT each Ping's eye and the single on-air star.
//      Enforced by compositing into a cell buffer whose `eye` bit is the only thing tinted red
//      (this fixes tintEyeLine's "first eye per line only" limit when several Pings share a row).
//   2. PURE + SEEDED. renderWorld(w,h,frame,seed) is deterministic (positions/twinkle from
//      pingHash), so it is reproducible and unit-testable, like idleScene's desync.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const worldTickMs = 120 // ~8fps: smoother than the 160ms TUI tick, still calm

// worldCell is one composited cell. eye=true is the ONLY thing rendered red.
type worldCell struct {
	r   rune
	eye bool
}

type pingWorldModel struct {
	w, h  int
	frame int
	seed  int
}

type worldTickMsg struct{}

func worldTick() tea.Cmd {
	return tea.Tick(worldTickMs*time.Millisecond, func(time.Time) tea.Msg { return worldTickMsg{} })
}

func (m pingWorldModel) Init() tea.Cmd { return worldTick() }

func (m pingWorldModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m, tea.Quit // any key wakes (standalone)
	case worldTickMsg:
		m.frame++
		return m, worldTick()
	}
	return m, nil
}

func (m pingWorldModel) View() string { return renderWorld(m.w, m.h, m.frame, m.seed) }

// worldHash is the deterministic desync for star placement/twinkle + wanderer spawn - pure in
// (a,b,seed) so the world is reproducible yet never metronomic (like idleScene's pingHash use).
func worldHash(a, b, seed int) uint32 { return pingHash(a*7349 + b*916703 + seed*2654435761) }

// blit paints sprite lines into the buffer at (x,y); spaces are transparent, and a cell whose
// rune == eye is marked red (eye=true). Out-of-bounds cells are clipped, never wrap-corrupt.
func blit(buf [][]worldCell, x, y int, lines []string, eye rune) {
	if len(buf) == 0 {
		return
	}
	w := len(buf[0])
	for dy, line := range lines {
		ry := y + dy
		if ry < 0 || ry >= len(buf) {
			continue
		}
		cx := x
		for _, r := range line {
			if r != ' ' && cx >= 0 && cx < w {
				buf[ry][cx] = worldCell{r: r, eye: eye != 0 && r == eye}
			}
			cx++
		}
	}
}

// renderWorld is the pure, seeded screensaver frame: the cell buffer composited + tinted
// (ink/dim everywhere, red ONLY on eye cells). "" for a degenerate size.
func renderWorld(w, h, frame, seed int) string { return compositeWorld(worldBuffer(w, h, frame, seed)) }

// worldBuffer builds the back->front composited cell buffer (pure + seeded); nil for a
// degenerate size. Split out so tests can assert the ONE-RED invariant on the cells directly.
func worldBuffer(w, h, frame, seed int) [][]worldCell {
	if w <= 0 || h <= 0 {
		return nil
	}
	buf := make([][]worldCell, h)
	for y := range buf {
		buf[y] = make([]worldCell, w)
		for x := range buf[y] {
			buf[y][x] = worldCell{r: ' '}
		}
	}
	horizon := h - 4
	if horizon < 2 {
		horizon = h - 1
	}

	// LAYER 0/1/2 — starfield: ~1 star per 18 cells of SKY (above the horizon). Far stars are
	// static + twinkle; near stars drift slowly. Star 0 is the single RED on-air station.
	skyRows := horizon
	if skyRows < 1 {
		skyRows = 1
	}
	nStars := (w * skyRows) / 18
	twinkle := []rune{'.', '˙', '\'', ',', '+', '*'}
	for i := 1; i < nStars; i++ { // i=0 is the on-air star, painted LAST (on top, never twinkled over)
		x0 := int(worldHash(i, 1, seed) % uint32(w))
		y := int(worldHash(i, 2, seed) % uint32(skyRows))
		if worldHash(i, 3, seed)%5 == 0 { // ~1/5 stars are "near" and parallax-drift
			x0 = ((x0-frame/16)%w + w) % w
		}
		g := twinkle[int(worldHash(i, frame/8, seed))%len(twinkle)]
		blit(buf, x0, y, []string{string(g)}, 0)
	}
	// (the ONE on-air station ◉ is painted LAST, at the end, so nothing overwrites it.)
	onAirX := int(worldHash(0, 1, seed) % uint32(w))
	onAirY := int(worldHash(0, 2, seed) % uint32(skyRows))

	// LAYER 3 — the planet horizon Ping walks along: a gentle rim + a banded surface line.
	if horizon >= 0 && horizon < h {
		rim := make([]rune, w)
		for x := 0; x < w; x++ {
			rim[x] = '_'
		}
		blit(buf, 0, horizon, []string{string(rim)}, 0)
		if horizon+1 < h {
			ramp := []rune("░▒▓▒░  ·  ") // banded surface = the band's "skin"
			brand := []rune(" R O G E R · A I ")
			s := make([]rune, 0, w+len(ramp))
			for len(s) < w {
				s = append(s, ramp...)
			}
			s = s[:w]
			// stamp the brand in the middle of the surface band
			if w > len(brand)+4 {
				off := (w - len(brand)) / 2
				copy(s[off:], brand)
			}
			blit(buf, 0, horizon+1, []string{string(s)}, 0)
		}
	}

	// LAYER 5 — Ping ambles along the rim (slow stroll; legs alternate), eye = the red dot.
	pf := pingWalkFrames[(frame/3)%len(pingWalkFrames)]
	px := (frame / 2) % maxI(1, w-pingWalkW) // a slow stroll, always fully on-screen
	blit(buf, px, horizon-len(pf.lines)+1, pf.lines[:], '•')

	// a second wanderer (a far/cornerHead Ping) drifting the other way + a baby trailing Ping.
	span := w + pingWalkW
	wx := w - 1 - (frame/5)%span + pingWalkW
	blit(buf, wx, horizon-len(cornerWanderer)+1, cornerWanderer, '•')
	blit(buf, px-4, horizon, []string{"(•)"}, '•') // baby (•) duckling

	// LAYER 6 — an occasional shooting star streak (transient, calm), upper sky only.
	if worldHash(frame/40, 7, seed)%4 == 0 {
		k := frame % 40
		if k < 6 {
			sx := int(worldHash(frame/40, 8, seed)%uint32(maxI(1, w-8))) + k*2
			sy := 1 + k
			blit(buf, sx, sy, []string{"╲."}, 0)
		}
	}

	// the ONE on-air station: a red ◉ painted LAST so nothing (twinkle, shooting star) ever
	// overwrites the sky's single red glint.
	blit(buf, onAirX, onAirY, []string{"◉"}, '◉')

	return buf
}

// cornerWanderer is a small 3-line "other Ping" (reuses the corner-head silhouette).
var cornerWanderer = []string{"(( • ))", " \\(   )/", "  ╰───╯"}

// compositeWorld flattens the cell buffer into a styled string: spaces stay bare, eye cells go
// red (stPingEye), everything else ink/dim (stDim). Same-style runs are batched into one
// Render call so a full frame is cheap.
func compositeWorld(buf [][]worldCell) string {
	var b strings.Builder
	for y, row := range buf {
		if y > 0 {
			b.WriteByte('\n')
		}
		i := 0
		for i < len(row) {
			c := row[i]
			j := i + 1
			for j < len(row) && row[j].eye == c.eye && (row[j].r == ' ') == (c.r == ' ') {
				j++
			}
			seg := make([]rune, 0, j-i)
			for k := i; k < j; k++ {
				seg = append(seg, row[k].r)
			}
			s := string(seg)
			switch {
			case c.r == ' ':
				b.WriteString(s)
			case c.eye:
				b.WriteString(stPingEye.Render(s))
			default:
				b.WriteString(stDim.Render(s))
			}
			i = j
		}
	}
	return b.String()
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// PingWorld runs the `roger --ping` screensaver: the live animated Ping world until any key.
// Under NO_COLOR / non-TTY (quiet) it prints ONE static postcard frame (lipgloss renders
// plain) + a friendly radio line and returns - no cursor churn in a pipe.
func PingWorld() error {
	if quiet {
		fmt.Println()
		fmt.Println(renderWorld(78, 18, 0, 7)) // one stable, color-free postcard frame
		fmt.Println()
		fmt.Println(lipgloss.NewStyle().Foreground(cDim).Render("  ((•)) roger that - Ping's out on the band. any key wakes the world."))
		return nil
	}
	return runProgram(pingWorldModel{seed: int(time.Now().UnixNano() & 0x7fffffff)}, tea.WithAltScreen())
}
