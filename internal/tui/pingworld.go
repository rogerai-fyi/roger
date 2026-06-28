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
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rogerai-fyi/roger/internal/glyphs"
)

const worldTickMs = 120 // ~8fps: smoother than the 160ms TUI tick, still calm

// worldCell is one composited cell. eye=true is the ONLY thing rendered red; bright=true is a
// near/foreground star drawn in brighter ink (a depth cue, NEVER red - one-red is untouched).
type worldCell struct {
	r      rune
	eye    bool
	bright bool
}

// worldStation is one ON-AIR band feeding the LIVE screensaver (rendered as a signal tower);
// worldData is the live snapshot injected into the world. A nil *worldData is the pure seeded
// world - byte-identical to before - so every existing test + the offline path are unchanged.
type worldStation struct {
	model    string
	signal   int // 0..100 -> tower height
	inFlight int // >0 -> the tower scans (actively serving)
}
type worldData struct {
	stations []worldStation // on-air bands, strongest-signal first, capped
}

type pingWorldModel struct {
	w, h  int
	frame int
	seed  int
	data  *worldData // LIVE on-air snapshot (nil = the seeded world); set by the host
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

func (m pingWorldModel) View() string { return renderWorldData(m.w, m.h, m.frame, m.seed, m.data) }

// worldHash is the deterministic desync for star placement/twinkle + wanderer spawn - pure in
// (a,b,seed) so the world is reproducible yet never metronomic (like idleScene's pingHash use).
func worldHash(a, b, seed int) uint32 { return pingHash(a*7349 + b*916703 + seed*2654435761) }

// Depth-weighted starfield (v2 P0-2): three tiers give the sky genuine parallax instead of a
// flat speckle. Far stars are tiny/faint/static, mid drift slowly, near are bright + drift
// fastest. Glyph sets are disjoint so a cell's depth is legible at a glance.
var (
	starsFar  = []rune{'.', '˙', '·'} // distant: tiny faint specks, twinkle in place
	starsMid  = []rune{',', '+', '*'} // middle distance: medium, slow drift
	starsNear = []rune{'o', '✦', '✧'} // foreground: bold + bright, fastest parallax
)

// starTier buckets star i into 0=far, 1=mid, 2=near, weighted FAR-heavy (~4/6 far) so most of
// the sky reads as distant - the essence of depth.
func starTier(i, seed int) int {
	switch worldHash(i, 9, seed) % 6 {
	case 4:
		return 1 // mid  (~1/6)
	case 5:
		return 2 // near (~1/6)
	default:
		return 0 // far  (~4/6)
	}
}

// dayNightPeriod is the frames in one full day<->night cycle (~a few minutes at ~140ms/frame).
const dayNightPeriod = 1600

// dayNightDarkness returns 0..100 sky darkness: 100 = deep night (all stars out), 0 = midday
// (only the brightest near stars + moon remain). A slow triangle wave, starting at night
// (frame 0). Pure in frame - so the sky "breathes" yet stays reproducible.
func dayNightDarkness(frame int) int {
	half := dayNightPeriod / 2
	p := ((frame % dayNightPeriod) + dayNightPeriod) % dayNightPeriod
	if p < half {
		return 100 - p*100/half // night -> day
	}
	return (p - half) * 100 / half // day -> night
}

// globeRamp is the limb-darkened shading band for the rotating planet (faint rim -> bright
// centre -> faint rim); scrolling it fakes a 3D rotation.
var globeRamp = []rune("░▒▓▓▒░")

// globeLines renders the planet Ping gazes at as a small ROTATING 3D sphere: the surface band
// sweeps diagonally to fake rotation and the ( ) rims + limb-darkened ░▒▓ give the round, lit
// curve. Pure in frame (slow, calm); dim ink, NEVER red. 5 rows x 8 cols.
func globeLines(frame int) []string {
	surf := func(n, row int) string {
		b := make([]rune, n)
		for i := range b {
			// diagonal scroll = rotation (frame) + per-latitude tilt (row).
			b[i] = globeRamp[((i+2*row+frame/3)%len(globeRamp)+len(globeRamp))%len(globeRamp)]
		}
		return string(b)
	}
	return []string{
		"  .--.  ",
		" (" + surf(4, 0) + ") ",
		" (" + surf(4, 1) + ") ",
		" (" + surf(4, 2) + ") ",
		"  `--'  ",
	}
}

// moonPos returns the planet's top-left (x,y): parked in the UPPER sky and drifting ~1 cell per
// 24 frames (a slow arc). Pure + seeded; x wraps into [0,w). seed b-values 5/6 don't collide
// with the on-air star's (1/2).
func moonPos(w, skyRows, frame, seed int) (int, int) {
	ww := maxI(1, w)
	x := ((int(worldHash(0, 5, seed)%uint32(ww)) + frame/24) % ww) % ww
	y := int(worldHash(0, 6, seed) % uint32(maxI(1, skyRows/3)))
	return x, y
}

// starColumn is star i's drifting column for its tier, wrapped into [0,w): far is static, mid
// drifts slowly, near drifts fastest (parallax). w is assumed > 0 (worldBuffer guards it).
func starColumn(x0, frame, w, tier int) int {
	div := 0
	switch tier {
	case 2:
		div = 10 // near: fastest
	case 1:
		div = 28 // mid: slow
	default:
		return ((x0 % w) + w) % w // far: static
	}
	return ((x0-frame/div)%w + w) % w
}

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

// Ping's behavior loop (v2 P0-1): instead of a mechanical edge-to-edge slide, Ping lives a
// small repeating "day" - mostly ambling, with pauses, a look-around, a short run, and a
// transmit wink. Pure + seeded; the schedule repeats every waCycle windows so worldPingX can
// integrate the per-window speed in O(waCycle). The eye stays the red '•' in EVERY act, so the
// ONE-RED 'at least one red eye' law holds no matter where the wandering Pings have drifted.
type worldAct int

const (
	waAmble    worldAct = iota // a slow stroll (speed 1)
	waRun                      // a brief trot (speed 3)
	waPause                    // stands a beat (idle bob)
	waLook                     // looks around
	waTransmit                 // a little on-air wink toward the band
)

const (
	waWindow = 20 // frames per act (~3s at ~140ms/frame)
	waCycle  = 24 // acts before the loop repeats (~1min)
)

// worldActAt is the (periodic, seeded) act for window wi - weighted heavily toward calm amble.
func worldActAt(wi, seed int) worldAct {
	switch worldHash(((wi%waCycle)+waCycle)%waCycle, 11, seed) % 12 {
	case 0, 1:
		return waPause
	case 2:
		return waLook
	case 3:
		return waRun
	case 4:
		return waTransmit
	default:
		return waAmble // ~7/12
	}
}

// worldActSpeed is the per-frame columns an act advances (only amble/run move).
func worldActSpeed(a worldAct) int {
	switch a {
	case waAmble:
		return 1
	case waRun:
		return 3
	default:
		return 0
	}
}

// worldPingX integrates the act speeds into Ping's column, wrapped into [0,span). Bounded to
// O(waCycle) by summing one loop cycle (the schedule is periodic in waCycle). 0 for span<=0.
func worldPingX(frame, seed, span int) int {
	if span <= 0 || frame < 0 {
		return 0
	}
	wi, prog := frame/waWindow, frame%waWindow
	cycLen := 0
	for k := 0; k < waCycle; k++ {
		cycLen += worldActSpeed(worldActAt(k, seed)) * waWindow
	}
	pos := (wi / waCycle) * cycLen
	for k := 0; k < wi%waCycle; k++ {
		pos += worldActSpeed(worldActAt(k, seed)) * waWindow
	}
	pos += worldActSpeed(worldActAt(wi, seed)) * prog
	return ((pos % span) + span) % span
}

// worldPingPose returns Ping's sprite lines + the red eye for the act at this frame. The eye is
// ALWAYS '•' (Ping never closes it - see the one-red note above).
func worldPingPose(frame, seed int) ([]string, rune) {
	wi, local := frame/waWindow, frame%waWindow
	switch worldActAt(wi, seed) {
	case waRun:
		return pingWalkFrames[(frame/2)%len(pingWalkFrames)].lines[:], '•' // faster legs
	case waPause:
		return pingIdleFrames[(frame/4)%len(pingIdleFrames)].lines[:], '•'
	case waLook:
		return pingLookFrames[(local/4)%len(pingLookFrames)].lines[:], '•'
	case waTransmit:
		return pingTxFrames[(local/2)%len(pingTxFrames)].lines[:], '•'
	default: // waAmble
		return pingWalkFrames[(frame/3)%len(pingWalkFrames)].lines[:], '•'
	}
}

// renderWorld is the pure, seeded screensaver frame: the cell buffer composited + tinted
// (ink/dim everywhere, red ONLY on eye cells). "" for a degenerate size.
func renderWorld(w, h, frame, seed int) string { return renderWorldData(w, h, frame, seed, nil) }

// renderWorldData is renderWorld with an optional LIVE data snapshot (nil = byte-identical to
// the pure seeded world, so every existing test + the offline standalone path are unchanged).
func renderWorldData(w, h, frame, seed int, d *worldData) string {
	return compositeWorld(worldBufferData(w, h, frame, seed, d))
}

// worldBuffer builds the pure SEEDED cell buffer (no live data); nil for a degenerate size.
func worldBuffer(w, h, frame, seed int) [][]worldCell { return worldBufferData(w, h, frame, seed, nil) }

// worldBufferData builds the back->front composited cell buffer. d is an optional LIVE snapshot
// (on-air bands -> signal towers on the horizon + the ◉ riding the strongest); nil => the pure
// seeded world. Split out so tests assert the ONE-RED invariant on the cells directly.
func worldBufferData(w, h, frame, seed int, d *worldData) [][]worldCell {
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

	// LAYER 0/1/2 — depth-weighted starfield: ~1 star per 18 cells of SKY (above the horizon),
	// bucketed into far/mid/near tiers for genuine parallax (see starTier/starColumn). Far are
	// faint+static, mid drift slowly, near are bright + drift fastest. Star 0 is the RED on-air
	// station, painted LAST so nothing twinkles over it.
	skyRows := horizon
	if skyRows < 1 {
		skyRows = 1
	}
	nStars := (w * skyRows) / 18
	darkness := dayNightDarkness(frame) // day washes the faint stars out; the sky breathes
	for i := 1; i < nStars; i++ {
		tier := starTier(i, seed)
		// Faint far/mid stars fade as it brightens toward day; the bright near stars persist
		// (like real first-magnitude stars + planets lingering at dusk). The moon + on-air ◉
		// are separate and always shown.
		if tier != 2 && int(worldHash(i, 4, seed)%100) >= darkness {
			continue
		}
		set := starsFar
		bright := false
		switch tier {
		case 2:
			set, bright = starsNear, true
		case 1:
			set = starsMid
		}
		x0 := starColumn(int(worldHash(i, 1, seed)%uint32(w)), frame, w, tier)
		y := int(worldHash(i, 2, seed) % uint32(skyRows))
		g := set[int(worldHash(i, frame/8, seed))%len(set)]
		if y >= 0 && y < len(buf) && x0 >= 0 && x0 < w { // in-bounds by construction; guard anyway
			buf[y][x0] = worldCell{r: g, bright: bright}
		}
	}
	// LAYER 0.5 — a faint aurora wisp near the top, ONLY at deep night, drifting slowly. Dim
	// ink, never red; behind the moon + on-air star (both painted later).
	if darkness > 70 && skyRows >= 3 {
		aur := []rune("≈ ∼ ∽ ≋   ") // gappy so it reads as a wisp, not a solid bar
		row := make([]rune, w)
		for x := 0; x < w; x++ {
			row[x] = aur[(x+frame/12)%len(aur)]
		}
		blit(buf, 0, 1, []string{string(row)}, 0)
	}

	// LAYER 1.5 — the planet: a slowly ROTATING 3D globe hanging high, drifting the sky. Dim
	// ink, never red (painted over the stars; the on-air ◉ is still painted LAST, on top).
	mx, my := moonPos(w, skyRows, frame, seed)
	blit(buf, mx, my, globeLines(frame), 0)

	// (the ONE on-air station ◉ is painted LAST, at the end, so nothing overwrites it.)
	onAirX := int(worldHash(0, 1, seed) % uint32(w))
	onAirY := int(worldHash(0, 2, seed) % uint32(skyRows))
	// LIVE DATA: each on-air band becomes a signal tower on the horizon; the ◉ rides the
	// STRONGEST band's tower top. towers is empty in the seeded (d==nil) world, so the ◉ keeps
	// its seeded sky position there.
	towers := worldTowers(w, horizon, d)
	if len(towers) > 0 {
		onAirX, onAirY = towers[0].x, towers[0].tipY // the flagship (strongest) tower top
	}

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

	// LAYER 3.5 — LIVE signal towers (one per on-air band): a dim │ mast rising from the rim,
	// height = the band's real signal, a bright cell SCANNING up the mast when it's actively
	// serving (inFlight>0). Painted after the horizon, before Ping (Ping walks in front). The
	// flagship's tip is left for the ◉ (painted last); the rest get a dim ○. Empty when seeded.
	for ti, t := range towers {
		paintTower(buf, t, horizon, ti == 0, frame)
	}

	// LAYER 4 — a still pond at the shore: the banded surface above is the beach, and the
	// bottom rows give back a dim, rippled reflection of the moon (water for a duck). Dim ink,
	// NEVER red - even reflections stay dim, reinforcing the one-red law. Additive: the
	// ROGER·AI shore band is untouched.
	for wy := horizon + 2; wy < h; wy++ {
		ripple := make([]rune, w)
		for x := 0; x < w; x++ {
			if (x+frame/6+wy)%7 == 0 { // mostly-still water, a slow drifting ripple
				ripple[x] = '~'
			} else {
				ripple[x] = ' ' // transparent in blit - leaves the row calm
			}
		}
		blit(buf, 0, wy, []string{string(ripple)}, 0)
	}
	if rw := horizon + 2; rw < h { // the moon's wobbling reflection on the near water
		rmx := (mx + frame/6) % maxI(1, w)
		blit(buf, rmx, rw, []string{"(.)"}, 0)
	}

	// LAYER 5 — Ping lives along the rim: a seeded behavior loop (amble / pause / look / run /
	// transmit). The eye is the red '•' in every act EXCEPT the brief transmit swell, where the
	// tx pose's own broadcasting 'O' shows (dim) - so the "at least one red eye" law is carried
	// by the always-on-screen baby duckling below (and the on-air ◉), never assumed of Ping.
	pingLines, pingEye := worldPingPose(frame, seed)
	px := worldPingX(frame, seed, maxI(1, w-pingWalkW)) // always fully on-screen
	blit(buf, px, horizon-len(pingLines)+1, pingLines, pingEye)

	// a wandering Ping drifts the other way - but it COMES AND GOES (v2 P1-8): a hash-gated
	// window decides whether one is currently crossing, so the cast varies run-to-run instead
	// of an always-present drifter (sometimes Ping ambles alone with the ducklings).
	if worldHash(frame/80, 13, seed)%3 != 0 { // ~2/3 of windows have a wanderer crossing
		span := w + pingWalkW
		wx := w - 1 - (frame/5)%span + pingWalkW
		blit(buf, wx, horizon-len(cornerWanderer)+1, cornerWanderer, '•')
	}

	// LAYER 6 — an occasional shooting star streak (transient, calm), upper sky only.
	if worldHash(frame/40, 7, seed)%4 == 0 {
		k := frame % 40
		if k < 6 {
			sx := int(worldHash(frame/40, 8, seed)%uint32(maxI(1, w-8))) + k*2
			sy := 1 + k
			blit(buf, sx, sy, []string{"╲."}, 0)
		}
	}

	// A duckling trail follows Ping (v2 P1-4): two dim followers lag behind, and the LEAD
	// duckling - clamped on-screen, painted AFTER the shooting star - keeps the red '•' so it
	// survives at every reasonable size, even mid-transmit, even at h=8. (The single ◉ below is
	// the UNIVERSAL red-eye backstop at degenerate sizes like w=1 where the lead clips off.)
	blit(buf, px-12, horizon, []string{"(·)"}, 0) // far follower (dim)
	blit(buf, px-8, horizon, []string{"(·)"}, 0)  // near follower (dim)
	blit(buf, maxI(0, px-4), horizon, []string{"(•)"}, '•')

	// transmit-to-star (v2 P1-5): while Ping is broadcasting, the on-air ◉ "breathes back" - a
	// faint dim halo pulses around it (the ◉ itself stays the SINGLE red glint, painted last).
	if worldActAt(frame/waWindow, seed) == waTransmit {
		if frame%4 < 2 {
			blit(buf, onAirX-1, onAirY, []string{"("}, 0)
			blit(buf, onAirX+1, onAirY, []string{")"}, 0)
		} else {
			blit(buf, onAirX-2, onAirY, []string{"·"}, 0)
			blit(buf, onAirX+2, onAirY, []string{"·"}, 0)
		}
	}

	// on-air blip: a faint ring pulses outward from the station every ~30 frames (a radio blip
	// that says "live"), dim, expanding 1->3 cells then resetting. Distinct from the Ping-driven
	// transmit halo above.
	if b := frame % 30; b < 9 {
		rad := 1 + b/3 // 1,2,3
		blit(buf, onAirX-rad, onAirY, []string{"("}, 0)
		blit(buf, onAirX+rad, onAirY, []string{")"}, 0)
	}

	// the ONE on-air station: a red ◉ painted LAST so nothing (twinkle, shooting star, baby,
	// breathe-halo, blip) ever overwrites the sky's single red glint (off the baby's rim row).
	blit(buf, onAirX, onAirY, []string{"◉"}, '◉')

	return buf
}

// cornerWanderer is a small 3-line "other Ping" (reuses the corner-head silhouette).
var cornerWanderer = []string{"(( • ))", " \\(   )/", "  ╰───╯"}

// tower is one laid-out LIVE signal tower: column x, tipY (top row), + its station.
type tower struct {
	x, tipY int
	st      worldStation
}

// worldTowers lays out one tower per on-air band, evenly spaced across the width, height scaled
// by the band's signal (taller = stronger), STRONGEST first. Empty for a nil/empty snapshot or a
// too-small world (so the seeded world is untouched).
func worldTowers(w, horizon int, d *worldData) []tower {
	if d == nil || len(d.stations) == 0 || horizon < 3 || w < 6 {
		return nil
	}
	maxH := horizon - 1
	if maxH > 8 {
		maxH = 8
	}
	n := len(d.stations)
	out := make([]tower, 0, n)
	for i, s := range d.stations {
		h := 1 + s.signal*(maxH-1)/100 // 1..maxH
		if h < 1 {
			h = 1
		}
		if h > maxH {
			h = maxH
		}
		out = append(out, tower{x: (i + 1) * w / (n + 1), tipY: horizon - h, st: s})
	}
	return out
}

// paintTower draws a tower's dim │ mast from the rim up to its tip. The flagship leaves its tip
// for the ◉ (painted last); the rest get a dim ○ tip. A busy tower (inFlight>0) shows a single
// BRIGHT cell scanning up the mast (the "actively serving" pulse). Dim/bright ink, never red.
func paintTower(buf [][]worldCell, t tower, horizon int, flagship bool, frame int) {
	base := horizon - 1
	for y := t.tipY + 1; y <= base; y++ { // the mast below the tip
		blit(buf, t.x, y, []string{"│"}, 0)
	}
	if height := base - t.tipY; t.st.inFlight > 0 && height > 0 { // a bright scan rides a serving tower
		scanY := base - (frame/2)%(height+1)
		if scanY >= t.tipY && scanY >= 0 && scanY < len(buf) && t.x >= 0 && len(buf) > 0 && t.x < len(buf[0]) {
			buf[scanY][t.x] = worldCell{r: '│', bright: true}
		}
	}
	if !flagship { // dim ○ tip; the flagship's tip is the ◉ (painted last)
		blit(buf, t.x, t.tipY, []string{"○"}, 0)
	}
}

// buildWorldData snapshots the LIVE on-air bands into the screensaver's data (the signal towers).
// Strongest-signal first, capped; nil when nothing is on air -> the calm seeded world.
func buildWorldData(bands []band) *worldData {
	var st []worldStation
	for _, b := range bands {
		if !b.online {
			continue
		}
		st = append(st, worldStation{model: b.model, signal: int(bandSignal(b)), inFlight: b.inFlight})
	}
	if len(st) == 0 {
		return nil
	}
	sort.Slice(st, func(i, j int) bool { return st[i].signal > st[j].signal })
	const maxTowers = 8
	if len(st) > maxTowers {
		st = st[:maxTowers]
	}
	return &worldData{stations: st}
}

// compositeWorld flattens the cell buffer into a styled string: spaces stay bare, eye cells go
// red (stPingEye), bright (near-star) cells go brighter ink (stLive), everything else dim
// (stDim). Same-style runs are batched into one Render call so a full frame is cheap.
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
			for j < len(row) && row[j].eye == c.eye && row[j].bright == c.bright && (row[j].r == ' ') == (c.r == ' ') {
				j++
			}
			seg := make([]rune, 0, j-i)
			for k := i; k < j; k++ {
				seg = append(seg, row[k].r)
			}
			// Fold non-ASCII art to ASCII stand-ins on a legacy console (no-op on UTF-8), so
			// the screensaver degrades cleanly instead of mojibake-ing ░▒▓ ◉ ✦ etc.
			s := glyphs.Fold(string(seg))
			switch {
			case c.r == ' ':
				b.WriteString(s)
			case c.eye:
				b.WriteString(stPingEye.Render(s))
			case c.bright:
				b.WriteString(stLive.Render(s))
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
