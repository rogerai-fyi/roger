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
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"encoding/json"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rogerai-fyi/roger/internal/glyphs"
)

const worldTickMs = 120 // ~8fps: smoother than the 160ms TUI tick, still calm

// worldCell is one composited cell. eye=true is the ONLY thing rendered red; bright=true is a
// near/foreground element drawn brighter (a depth cue, NEVER red - one-red is untouched); tone is
// an optional COOL ambient color (sky/globe/aurora/water) - cool by law, so the red beacon stays
// the singular HOT glint (see toneStyle).
type worldCell struct {
	r      rune
	eye    bool
	bright bool
	tone   worldTone
}

// worldTone is a cell's optional COOL ambient color. The screensaver is the ONE place RogerAI's
// strict mono+red brand relaxes into color (founder: "is it possible to add more color
// somewhere?") - but every tone is COOL (blue/teal/green/violet), so the on-air ◉ + Ping's eye •
// stay the ONLY hot (red) glints and pop HARDER against the cool world. toneNone = default dim ink.
type worldTone uint8

const (
	toneNone    worldTone = iota // dim ink (default): ground, characters, brand, towers, beacon
	toneSky                      // frost blue: the drifting stars
	toneSun                      // warm gold: the daytime sun (NOT red - red stays the beacon)
	toneEarth                    // teal: the night moon/globe
	toneAurora                   // green: the deep-night aurora wisp
	toneAuroraV                  // violet: the aurora tail + the day flower + the butterfly's wings
	toneLeaf                     // grass green: the daytime plants growing from the ground
	toneWater                    // blue: the still shore pond + its reflection
	tonePale                     // pale frost: the daytime drifting clouds (cool + soft, never red)
	toneSat                      // bright aqua: the orbiting satellite (kept distinct from the teal moon)
	toneShip                     // warm amber: the rare spaceship hull (distinct from the gold sun)
)

// The screensaver's COOL palette - kept SEPARATE from tui.go's brand mono+red on purpose: this is
// the relax-view Easter egg, not a brand surface. Nord-leaning, AdaptiveColor so it tracks the
// terminal background and strips cleanly under NO_COLOR. NONE is red - red is reserved for on-air.
var (
	cSky     = lipgloss.AdaptiveColor{Light: "#5E81AC", Dark: "#81A1C1"} // frost blue (stars)
	cSun     = lipgloss.AdaptiveColor{Light: "#C8881A", Dark: "#EBCB8B"} // warm gold (the sun)
	cEarth   = lipgloss.AdaptiveColor{Light: "#3B6E6A", Dark: "#88C0D0"} // teal (the moon/globe)
	cAurora  = lipgloss.AdaptiveColor{Light: "#4F894C", Dark: "#A3BE8C"} // green (aurora)
	cAuroraV = lipgloss.AdaptiveColor{Light: "#8A5CA8", Dark: "#B48EAD"} // violet (aurora/flower/wings)
	cLeaf    = lipgloss.AdaptiveColor{Light: "#5E8C3A", Dark: "#A3BE8C"} // grass green (plants)
	cWater   = lipgloss.AdaptiveColor{Light: "#4C6F9C", Dark: "#5E81AC"} // deeper blue (pond)
	cPale    = lipgloss.AdaptiveColor{Light: "#9AA7B5", Dark: "#D8DEE9"} // pale frost (day clouds)
	cSat     = lipgloss.AdaptiveColor{Light: "#2B8AA0", Dark: "#7FE0E8"} // bright aqua (satellite)
	cShip    = lipgloss.AdaptiveColor{Light: "#B5651D", Dark: "#E8A55C"} // warm amber (spaceship hull)
)

// toneStyle maps a cool tone to its lipgloss style (bright = a touch bolder, for near elements).
// Under NO_COLOR lipgloss renders these plain, so the screensaver degrades to mono. toneNone (and
// any unknown) falls back to the shared dim ink. It NEVER returns red - that's the one-red law.
func toneStyle(t worldTone, bright bool) lipgloss.Style {
	var c lipgloss.AdaptiveColor
	switch t {
	case toneSky:
		c = cSky
	case toneSun:
		c = cSun
	case toneEarth:
		c = cEarth
	case toneAurora:
		c = cAurora
	case toneAuroraV:
		c = cAuroraV
	case toneLeaf:
		c = cLeaf
	case toneWater:
		c = cWater
	case tonePale:
		c = cPale
	case toneSat:
		c = cSat
	case toneShip:
		c = cShip
	default:
		return stDim
	}
	st := lipgloss.NewStyle().Foreground(c)
	if bright {
		st = st.Bold(true)
	}
	return st
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
	w, h   int
	frame  int
	seed   int
	data   *worldData // LIVE on-air snapshot (nil = the seeded world); set by the host
	broker string     // standalone only: the broker to /discover for live towers ("" = seeded)
}

// worldDataMsg carries a fresh LIVE snapshot to the standalone screensaver (nil data on any
// fetch error => the calm seeded world).
type worldDataMsg struct{ data *worldData }

type worldTickMsg struct{}

func worldTick() tea.Cmd {
	return tea.Tick(worldTickMs*time.Millisecond, func(time.Time) tea.Msg { return worldTickMsg{} })
}

func (m pingWorldModel) Init() tea.Cmd {
	if m.broker != "" {
		return tea.Batch(worldTick(), worldFetch(m.broker)) // live towers from the first frame
	}
	return worldTick()
}

func (m pingWorldModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m, tea.Quit // any key wakes (standalone)
	case worldDataMsg:
		m.data = msg.data // refresh the live towers (nil => seeded fallback)
		return m, nil
	case worldTickMsg:
		m.frame++
		// keep the live towers fresh on a calm cadence (a screensaver should breathe).
		if m.broker != "" && m.frame%worldRescanFrames == 0 {
			return m, tea.Batch(worldTick(), worldFetch(m.broker))
		}
		return m, worldTick()
	}
	return m, nil
}

// worldFetch pulls /discover ONCE for the standalone screensaver and turns it into live tower
// data. Any error (offline / timeout / malformed / no broker) yields nil -> the calm seeded
// world. It's always a Cmd (never blocks the render) and never crashes the screensaver.
func worldFetch(broker string) tea.Cmd {
	return func() tea.Msg {
		if broker == "" {
			return worldDataMsg{nil}
		}
		resp, err := http.Get(broker + "/discover")
		if err != nil {
			return worldDataMsg{nil}
		}
		defer resp.Body.Close()
		var d struct {
			Offers []offer `json:"offers"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&d); err != nil && !errors.Is(err, io.EOF) {
			return worldDataMsg{nil}
		}
		return worldDataMsg{buildWorldData(groupBands(d.Offers, nil))}
	}
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

// sunLines is the daytime sun: a bright gold disc ringed by rays. toneSun is warm GOLD, distinct
// from the reserved on-air RED. 3x3, small + calm - the arc across the sky supplies the motion.
func sunLines() []string {
	return []string{
		"\\|/",
		"-☀-",
		"/|\\",
	}
}

// sunArc is the sun's position over the day: up ONLY while it's day (darkness<50), rising from the
// horizon at dawn, arcing to near the top at noon, setting at dusk. Pure in frame; (x,y) is the
// sprite's top-left, kept in-bounds (blit clips anyway). The daytime window is the middle half of
// the cycle (centered on noon), matching dayNightDarkness<50.
func sunArc(w, skyRows, frame int) (up bool, x, y int) {
	d := dayNightDarkness(frame)
	if d >= 50 || w <= 0 || skyRows <= 0 {
		return false, 0, 0
	}
	p := ((frame % dayNightPeriod) + dayNightPeriod) % dayNightPeriod
	q0, q1 := dayNightPeriod/4, 3*dayNightPeriod/4 // the daytime window (darkness<50)
	x = (p - q0) * maxI(1, w-1) / maxI(1, q1-q0)   // sweep left -> right across the day
	if x < 0 {
		x = 0
	}
	if x >= w {
		x = w - 1
	}
	y = (skyRows - 1) * d / 50 // noon (d=0) -> top; dawn/dusk (d~50) -> near the horizon
	if y < 0 {
		y = 0
	}
	if y >= skyRows {
		y = skyRows - 1
	}
	return true, x, y
}

// plantMax is the tallest a daytime plant grows (stem cells including the bloom on top).
const plantMax = 3

// plantStage maps the day's darkness to a plant's growth 0..plantMax: dormant (0) at/under deep
// night (darkness>=50), tallest at high noon (darkness 0), growing monotonically between.
func plantStage(darkness int) int {
	if darkness >= 50 {
		return 0
	}
	return plantMax - darkness*plantMax/50
}

// paintPlant grows a plant up from base (the row just above the rim): a green stem topped by a
// leafy sprout (young) or, at full height, a violet flower. Stem/leaf = toneLeaf (green); the
// bloom borrows toneAuroraV (violet). Colored ink, never red.
func paintPlant(buf [][]worldCell, x, base, stage int) {
	if stage <= 0 {
		return
	}
	for i := 0; i < stage-1; i++ { // the stem
		blitT(buf, x, base-i, []string{"|"}, 0, toneLeaf)
	}
	topY := base - (stage - 1)
	if stage >= plantMax { // bloomed: a violet flower on the green stem
		blitT(buf, x, topY, []string{"❀"}, 0, toneAuroraV)
	} else { // young: a leafy sprout
		blitT(buf, x, topY, []string{"Y"}, 0, toneLeaf)
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

// blit paints sprite lines into the buffer at (x,y) in dim ink (no tone); see blitT.
func blit(buf [][]worldCell, x, y int, lines []string, eye rune) {
	blitT(buf, x, y, lines, eye, toneNone)
}

// blitT is blit with a COOL tone: spaces are transparent, a cell whose rune == eye is marked red
// (eye=true) AND left tone-free (the eye is red-only - it beats any passed tone), and every other
// painted cell takes the tone. Out-of-bounds cells are clipped, never wrap-corrupt.
func blitT(buf [][]worldCell, x, y int, lines []string, eye rune, tone worldTone) {
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
				isEye := eye != 0 && r == eye
				ct := tone
				if isEye {
					ct = toneNone // the eye is red-only; never also a cool tone
				}
				buf[ry][cx] = worldCell{r: r, eye: isEye, tone: ct}
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

// paintSatellite glides a small satellite across the sky on seeded ~70-frame windows (day OR
// night): a teal bus with solar-panel arms, trailing a periodic red '•' DOWNLINK blip - a
// deliberate EXTRA place for the live on-air dot ("transmitting to the ground"). Generative: only
// ~half the windows carry one, and it crosses either direction at a seeded altitude. Pure + seeded;
// the tone goes through blitT so it's NO_COLOR-safe, and the lone red dot keeps the one-red law.
func paintSatellite(buf [][]worldCell, w, skyRows, frame, seed int) {
	if skyRows < 3 || w < 10 {
		return
	}
	win := frame / 70
	if worldHash(win, 31, seed)%2 != 0 {
		return // only ~half the windows carry a satellite (don't overdo it)
	}
	k := frame % 70
	span := w + 12
	prog := k * span / 70
	x := prog - 6
	if worldHash(win, 32, seed)%2 == 0 {
		x = w + 5 - prog // sometimes it crosses the other way
	}
	y := 1 + int(worldHash(win, 33, seed)%uint32(maxI(1, skyRows/2)))
	blitT(buf, x, y, []string{"-=▢=-"}, 0, toneSat) // aqua bus + solar-panel arms
	if k%9 < 2 {                                       // a brief downlink every ~9 frames
		blit(buf, x+2, y+1, []string{"•"}, '•') // the on-air red dot, beamed groundward
	}
}

// paintSpaceship sends a RARE spaceship across the upper sky (~1/4 of 130-frame windows) with a dim
// fading ion trail and a single red '•' running light at the nose. Amber hull (toneShip) for a
// warm pop against the cool sky. Calm + infrequent so the sky never feels busy. Pure + seeded.
func paintSpaceship(buf [][]worldCell, w, skyRows, frame, seed int) {
	if skyRows < 3 || w < 12 {
		return
	}
	win := frame / 130
	if worldHash(win, 41, seed)%4 != 0 {
		return // rare
	}
	k := frame % 130
	span := w + 14
	x := k*span/130 - 7
	y := 1 + int(worldHash(win, 42, seed)%uint32(maxI(1, skyRows/2)))
	for t := 1; t <= 3; t++ {
		blit(buf, x-t, y, []string{"·"}, 0) // a fading ion trail behind
	}
	blitT(buf, x, y, []string{"<◊=>"}, 0, toneShip) // warm amber hull
	if k%6 < 3 {
		blit(buf, x+4, y, []string{"•"}, '•') // a red running light at the nose
	}
}

// paintRadioDish stands a ground-station dish on the rim that sweeps a widening frost transmission
// cone up into the sky, with a red '•' at the feed while it transmits (another deliberate place for
// the live on-air dot). One seeded dish, a calm 24-frame sweep. Painted after the towers, before
// Ping (Ping walks in front). Pure + seeded; the cone tone is NO_COLOR-safe via blitT.
func paintRadioDish(buf [][]worldCell, w, horizon, frame, seed int) {
	if horizon < 4 || w < 14 {
		return
	}
	dx := 5 + int(worldHash(0, 51, seed)%uint32(maxI(1, w-10)))
	dy := horizon - 1
	blit(buf, dx, dy, []string{"Y"}, 0) // the dish mast/feed on the rim
	b := frame % 24
	if b >= 12 {
		return // a quiet beat between sweeps
	}
	rad := 1 + b/4 // the cone widens 1->3 then resets
	for i := 1; i <= rad; i++ {
		if ay := dy - i; ay >= 0 {
			blitT(buf, dx-i, ay, []string{"/"}, 0, toneSky)
			blitT(buf, dx+i, ay, []string{"\\"}, 0, toneSky)
		}
	}
	if b < 3 {
		blit(buf, dx, dy-1, []string{"•"}, '•') // the feed transmits: the on-air red dot
	}
}

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
	day := darkness < 50                // the sun-up half: sun, plants, birds + the butterfly come out
	for i := 1; i < nStars; i++ {
		tier := starTier(i, seed)
		// Faint far/mid stars fade as it brightens toward day; the bright near stars linger at
		// dusk but wash out at full day (darkness<20) for a clean daytime sky. The sun/moon +
		// on-air ◉ are separate.
		if tier == 2 {
			if darkness < 20 {
				continue // full day: even the near stars are washed out
			}
		} else if int(worldHash(i, 4, seed)%100) >= darkness {
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
			buf[y][x0] = worldCell{r: g, bright: bright, tone: toneSky} // the starfield reads frost-blue
		}
	}
	// LAYER 0.5 — a faint aurora wisp near the top, ONLY at deep night, drifting slowly. Dim
	// ink, never red; behind the moon + on-air star (both painted later).
	if darkness > 70 && skyRows >= 3 && len(buf) > 1 {
		aur := []rune("≈ ∼ ∽ ≋   ") // gappy so it reads as a wisp, not a solid bar
		for x := 0; x < w; x++ {
			r := aur[(x+frame/12)%len(aur)]
			if r == ' ' {
				continue
			}
			tone := toneAurora // green, shimmering to violet along the wisp (both cool, never red)
			if (x/4+frame/10)%2 == 0 {
				tone = toneAuroraV
			}
			buf[1][x] = worldCell{r: r, tone: tone}
		}
	}

	// LAYER 0.7 — daytime DRIFTING CLOUDS: a few seeded puffs glide across the day sky with
	// parallax (nearer clouds drift faster), in a pale frost tone. Gentle + calm; gone at night,
	// behind the sun. Never red.
	if day {
		paintClouds(buf, w, skyRows, frame, seed)
	}

	// LAYER 1.5 — the celestial body, swapping with the day: by NIGHT a slowly ROTATING teal 3D
	// globe (the moon Ping gazes at); by DAY a gold SUN arcing across the sky. Never red; the
	// on-air ◉ is still painted LAST, on top.
	mx, my := moonPos(w, skyRows, frame, seed)
	if day {
		if upSun, sx, sy := sunArc(w, skyRows, frame); upSun {
			blitT(buf, sx, sy, sunLines(), 0, toneSun)
		}
	} else {
		blitT(buf, mx, my, globeLines(frame), 0, toneEarth)
	}

	// LAYER 1.6 — orbital traffic crossing the sky (day OR night): a satellite with a periodic red
	// DOWNLINK blip, and RARELY a spaceship with an ion trail + a red running light. Generative
	// (seeded windows, direction, altitude). The on-air ◉ is still painted LAST, on top of all.
	paintSatellite(buf, w, skyRows, frame, seed)
	paintSpaceship(buf, w, skyRows, frame, seed)

	// (the ONE on-air station ◉ is painted LAST, at the end, so nothing overwrites it.)
	onAirX := int(worldHash(0, 1, seed) % uint32(w))
	onAirY := int(worldHash(0, 2, seed) % uint32(skyRows))
	// LIVE DATA: each on-air band becomes a signal tower on the horizon; the ◉ rides the
	// STRONGEST band's tower top. towers is empty in the seeded (d==nil) world, so the ◉ keeps
	// its seeded sky position there.
	towers := worldTowers(w, horizon, d)
	if d == nil { // OFFLINE/seeded world: generative towers whose signal+height VARY over time, so
		towers = seededTowers(w, horizon, frame, seed) // the offline screensaver "breathes" too.
	}
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

	// LAYER 3.2 — daytime PLANTS growing from the ground: seeded columns sprout green stems that
	// grow taller toward noon and bloom a violet flower at full height (dormant at night). Painted
	// behind Ping + the ducklings (they walk in front).
	if stage := plantStage(darkness); stage > 0 && horizon >= 2 {
		for px := 3; px < w-2; px += 9 {
			jx := px + int(worldHash(px, 21, seed)%5) // a little seeded jitter so it isn't a grid
			if jx >= 0 && jx < w {
				paintPlant(buf, jx, horizon-1, stage)
			}
		}
	}

	// LAYER 3.5 — LIVE signal towers (one per on-air band): a dim │ mast rising from the rim,
	// height = the band's real signal, a bright cell SCANNING up the mast when it's actively
	// serving (inFlight>0). Painted after the horizon, before Ping (Ping walks in front). The
	// flagship's tip is left for the ◉ (painted last); the rest get a dim ○. Empty when seeded.
	for ti, t := range towers {
		paintTower(buf, t, horizon, ti == 0, frame)
	}

	// LAYER 3.6 — a ground-station dish sweeps a widening frost transmission cone up into the sky,
	// with a red '•' at the feed while it transmits (another deliberate place for the live on-air dot).
	paintRadioDish(buf, w, horizon, frame, seed)

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
		blitT(buf, 0, wy, []string{string(ripple)}, 0, toneWater)
	}
	if rw := horizon + 2; rw < h && !day { // the moon's wobbling reflection (night only)
		rmx := (mx + frame/6) % maxI(1, w)
		blitT(buf, rmx, rw, []string{"(.)"}, 0, toneWater)
	}

	// LAYER 4.5 — daytime life: a BIRD flock crosses the sky (comes + goes on seeded windows, like
	// the night wanderer) and the BUTTERFLY (the new character) flutters low by the plants on a
	// gentle bob. Both gone at night. Dim silhouette birds; violet butterfly. Never red. GENERATIVE:
	// the flock SIZE varies (with a rare big migration) and a 2nd butterfly occasionally joins.
	if day {
		if skyRows >= 4 && worldHash(frame/90, 17, seed)%3 != 0 { // ~2/3 of windows have a flock
			by := 2 + int(worldHash(frame/90, 18, seed)%uint32(maxI(1, skyRows/3)))
			bx := (frame / 4) % maxI(1, w+12)
			wing := "v"
			if frame%6 < 3 {
				wing = "^" // flap
			}
			for k := 0; k < flockSize(frame/90, seed); k++ { // a seeded V (rarely a big migration)
				blit(buf, bx-k*3, by-(k%2), []string{wing}, 0)
			}
		}
		if horizon >= 4 {
			for bi := 0; bi < butterflyCount(frame/120, seed); bi++ { // usually one, sometimes a pair
				ph := bi * 5 // a phase offset so a pair never overlaps
				bob := []int{0, 1, 1, 2, 1, 1, 0, 0}[((frame+ph*4)/4)%8]
				bx := 4 + ((frame+ph*7)/3)%maxI(1, w-8)
				by := horizon - 3 - bob - bi // the 2nd flutters a touch higher
				if by < 1 {
					by = 1
				}
				wings := "<o>"
				if (frame+ph)%4 < 2 {
					wings = ">o<" // wings open / closed
				}
				blitT(buf, bx, by, []string{wings}, 0, toneAuroraV)
			}
		}
	}

	// LAYER 5 — Ping lives along the rim: a seeded behavior loop (amble / pause / look / run /
	// transmit). The eye is the red '•' in every act EXCEPT the brief transmit swell, where the
	// tx pose's own broadcasting 'O' shows (dim) - so the "at least one red eye" law is carried
	// by the always-on-screen baby duckling below (and the on-air ◉), never assumed of Ping.
	pingLines, pingEye := worldPingPose(frame, seed)
	px := worldPingX(frame, seed, maxI(1, w-pingWalkW)) // always fully on-screen
	blit(buf, px, horizon-len(pingLines)+1, pingLines, pingEye)

	// Ping naps at deep night while he pauses: a soft Zzz drifts up over his head (his eye stays
	// the red • - the law is carried regardless). More life, no extra red.
	if darkness > 80 && worldActAt(frame/waWindow, seed) == waPause {
		zRow := horizon - len(pingLines) - (frame/10)%2 // drifts up a cell
		z := "z"
		if (frame/8)%2 == 0 {
			z = "Z"
		}
		blit(buf, px+pingWalkW/2, zRow, []string{z}, 0)
	}

	// wandering Pings amble by, tied to a full edge-to-edge TRAVERSAL (not a separate visibility
	// window): on a present traversal a wanderer ENTERS fully off one edge and EXITS off the other,
	// so it never pops/vanishes mid-screen (the old frame/80-window bug). Lane 0 crosses ~2/3 of
	// traversals; lane 1 occasionally adds a 2nd wanderer ambling the opposite way (they pass). The
	// wanderer keeps its red '•' eye, but the always-on-screen lead duckling carries the one-red law.
	for lane := 0; lane < 2; lane++ {
		if draw, lines, wx, wy := wandererAt(frame, seed, w, horizon, lane); draw {
			blit(buf, wx, wy, lines, '•')
		}
	}

	// LAYER 6 — occasional shooting stars (transient, calm), upper sky, NIGHT only. GENERATIVE: a
	// window is usually a single streak, but RARELY a meteor SHOWER of 2-3 staggered streaks. Dim
	// ink; painted BEFORE the lead duckling so a streak can never clobber its red-eye backstop.
	if !day && worldHash(frame/40, 7, seed)%4 == 0 {
		win, k := frame/40, frame%40
		for s := 0; s < meteorCount(win, seed); s++ {
			ks := k - s*2 // each extra streak starts a beat later (a staggered shower)
			if ks < 0 || ks >= 6 {
				continue
			}
			sx := int(worldHash(win, 8+s, seed)%uint32(maxI(1, w-8))) + ks*2
			sy := 1 + ks + s
			blit(buf, sx, sy, []string{"╲."}, 0)
		}
	}

	// A duckling trail follows Ping (v2 P1-4): two dim followers lag behind, and the LEAD
	// duckling - clamped on-screen, painted AFTER the shooting star - keeps the red '•' so it
	// survives at every reasonable size, even mid-transmit, even at h=8. (The single ◉ below is
	// the UNIVERSAL red-eye backstop at degenerate sizes like w=1 where the lead clips off.)
	wad := (frame / 5) % 2                                  // the ducklings waddle: followers bob out of phase
	blit(buf, px-12, horizon-wad, []string{"(·)"}, 0)       // far follower (dim)
	blit(buf, px-8, horizon-(1-wad), []string{"(·)"}, 0)    // near follower (dim)
	blit(buf, maxI(0, px-4), horizon, []string{"(•)"}, '•') // lead - steady red-eye backstop

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

// cornerWandererFrames is "another Ping" ambling by - a small 3-line silhouette with a 2-frame
// WALK (the feet alternate ╿/╽, like Ping's own walk) so it shuffles rather than slides. The eye
// is the red '•' (multiple Ping eyes are fine; the one-red law needs only >=1).
var cornerWandererFrames = [][]string{
	{"(( • ))", " \\(   )/", "  ╿   ╿"},
	{"(( • ))", " \\(   )/", "  ╽   ╽"},
}

const (
	wandererW      = 8 // widest wanderer line (the arms row) - the off-screen margin each side
	wandererStride = 5 // frames per column step (a calm amble, matching the old wanderer pace)
)

// wandererAt decides whether "another Ping" is crossing on the given lane this frame, and if so
// returns its walk sprite, left column, and top row. Presence + motion are tied to ONE full
// edge-to-edge TRAVERSAL (period = (w+wandererW+1)*stride frames): at a traversal's first and last
// frame the wanderer is fully OFF-SCREEN, so it always enters from one edge and exits the other and
// never pops/vanishes mid-screen (the old frame/80-window bug). Lane 0 crosses ~2/3 of traversals;
// lane 1 occasionally adds a 2nd wanderer ambling the OPPOSITE way. Pure + seeded.
func wandererAt(frame, seed, w, horizon, lane int) (draw bool, lines []string, wx, y int) {
	if w <= 0 || frame < 0 {
		return false, nil, 0, 0
	}
	travel := w + wandererW // columns from fully-off-left to fully-off-right
	period := (travel + 1) * wandererStride
	cyc := frame / period
	if lane == 0 {
		if worldHash(cyc, 13, seed)%3 == 0 { // ~1/3 of traversals: lane 0 rests (Ping ambles alone)
			return false, nil, 0, 0
		}
	} else if worldHash(cyc, 14, seed)%4 != 0 { // ~1/4 of traversals: a 2nd wanderer joins
		return false, nil, 0, 0
	}
	off := (frame % period) / wandererStride // 0..travel
	dir := int(worldHash(cyc, 13, seed)>>2) % 2
	if lane != 0 {
		dir = 1 - dir // the 2nd wanderer ambles the opposite way, so the pair pass each other
	}
	if dir == 0 {
		wx = off - wandererW // enter off-left, exit off-right
	} else {
		wx = w - off // enter off-right, exit off-left
	}
	lines = cornerWandererFrames[(frame/3)%len(cornerWandererFrames)] // a calm 2-frame leg shuffle
	return true, lines, wx, horizon - len(lines) + 1
}

// paintClouds drifts a few seeded daytime clouds across the upper sky with PARALLAX (nearer clouds
// drift faster) in a pale frost tone. Gentle + calm; the puff is a fluffy (~~~) of seeded width.
// Spaces aren't used so there are no holes; cool ink, NEVER red.
func paintClouds(buf [][]worldCell, w, skyRows, frame, seed int) {
	if w <= 0 || skyRows < 2 {
		return
	}
	n := maxI(2, w/40) // a few clouds, scaled to width
	for i := 0; i < n; i++ {
		size := 2 + int(worldHash(i, 31, seed)%3)                       // 2..4 tildes
		row := int(worldHash(i, 32, seed) % uint32(maxI(1, skyRows/2))) // upper half of the sky
		div := 16 + int(worldHash(i, 33, seed)%24)                      // drift speed (parallax)
		x0 := int(worldHash(i, 34, seed) % uint32(w))
		cx := ((x0+frame/div)%w + w) % w
		puff := "(" + strings.Repeat("~", size) + ")"
		blitT(buf, cx, row, []string{puff}, 0, tonePale)
	}
}

// flockSize is the seeded size of the daytime bird flock for window win: a small V of 2..5 most of
// the time, with a RARE big MIGRATION of 6..8 (a "special moment"). Pure + seeded.
func flockSize(win, seed int) int {
	if worldHash(win, 20, seed)%7 == 0 {
		return 6 + int(worldHash(win, 22, seed)%3) // 6..8: a rare migration
	}
	return 2 + int(worldHash(win, 19, seed)%4) // 2..5
}

// butterflyCount is the seeded number of daytime butterflies for window win: usually 1, occasionally
// a pair. Pure + seeded.
func butterflyCount(win, seed int) int {
	if worldHash(win, 23, seed)%3 == 0 {
		return 2
	}
	return 1
}

// meteorCount is the seeded number of streaks in a night shooting-star burst for window win: usually
// a single streak, but RARELY a meteor SHOWER of 2..3 (a "special moment"). Pure + seeded.
func meteorCount(win, seed int) int {
	if worldHash(win, 50, seed)%5 == 0 {
		return 2 + int(worldHash(win, 51, seed)%2) // 2..3
	}
	return 1
}

// triWave is a slow 0..100 triangle wave over a 0..199 input (rise then fall). Pure - drives the
// seeded towers' breathing signal.
func triWave(p int) int {
	p = ((p % 200) + 200) % 200
	if p < 100 {
		return p
	}
	return 200 - p
}

// seededTowers builds a few GENERATIVE signal towers for the OFFLINE/seeded world (d==nil) so the
// screensaver "breathes" even with no live bands: each tower's signal rises + falls on its own slow
// frame-driven cycle (a fake on-air pulse), so its mast HEIGHT changes over time. Dim ink only (no
// bright serving-scan - that stays a LIVE-data cue); the flagship (index 0) leaves its tip for the
// red ◉ (painted last). Empty for a too-small world (so the seeded ◉ keeps its sky position there).
// Pure + seeded - never touches the live (d!=nil) path.
func seededTowers(w, horizon, frame, seed int) []tower {
	if horizon < 3 || w < 6 {
		return nil
	}
	n := 2 + int(worldHash(0, 41, seed)%3) // 2..4 towers
	maxH := horizon - 1
	if maxH > 6 {
		maxH = 6
	}
	out := make([]tower, 0, n)
	for i := 0; i < n; i++ {
		phase := int(worldHash(i, 42, seed) % 200)
		speed := 6 + int(worldHash(i, 43, seed)%6) // frames per signal step (slow, calm)
		sig := triWave(frame/speed + phase)        // 0..100, breathing over time
		h := 1 + sig*(maxH-1)/100
		if h < 1 {
			h = 1
		}
		if h > maxH {
			h = maxH
		}
		out = append(out, tower{
			x:    (i + 1) * w / (n + 1),
			tipY: horizon - h,
			st:   worldStation{signal: sig}, // dim only: no inFlight scan in the seeded world
		})
	}
	return out
}

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
			for j < len(row) && row[j].eye == c.eye && row[j].bright == c.bright && row[j].tone == c.tone && (row[j].r == ' ') == (c.r == ' ') {
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
				b.WriteString(stPingEye.Render(s)) // the ONE hot color
			case c.tone != toneNone:
				b.WriteString(toneStyle(c.tone, c.bright).Render(s)) // cool ambient color
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
func PingWorld(broker string) error {
	if quiet {
		fmt.Println()
		fmt.Println(renderWorld(78, 18, 0, 7)) // one stable, color-free seeded postcard (no network)
		fmt.Println()
		fmt.Println(lipgloss.NewStyle().Foreground(cDim).Render("  ((•)) roger that - Ping's out on the band. any key wakes the world."))
		return nil
	}
	// broker set => the model fetches /discover for LIVE signal towers (falls back to the seeded
	// world on any error); the live beat re-fetches on a calm cadence.
	return runProgram(pingWorldModel{seed: int(time.Now().UnixNano() & 0x7fffffff), broker: broker}, tea.WithAltScreen())
}
