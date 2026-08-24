// Package tui is the interactive `rogerai` experience - a two-way radio for Local Models,
// and the terminal twin of the website's "Live Operating Manual". Stations
// (providers) go on air; you tune in to a channel and talk. The look is the web's:
// ~95% monochrome + ONE red beacon, the shared instrument glyphs (◉ on air, ○ off
// air, ◆ verified, ▁▂▃▄▅▆▇█ signal bars), flat hairline structure, and a single
// carrier beat driving the beacon, the ((•)) spinner, and the signal-bar shimmer.
// Built on Bubble Tea + Lipgloss.
package tui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-isatty"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/capsule"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/glyphs"
	"rogerai.fm/roger/v6/internal/harness"
	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/operator"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/session"
)

// Hooks lets the host (cmd/rogerai) supply the few platform/auth bits the TUI
// can't compute itself, so the in-TUI /share, /login, /topup, /grant flows are
// REAL actions (not "run it elsewhere") without the tui package importing the
// host. All are optional; a nil hook degrades that flow to a labeled hint.
type Hooks struct {
	// Station is the owner's friendly, NON-SENSITIVE broadcast callsign (e.g.
	// `brave-otter`). Every band's broker node id is derived as `<station>-<model>` via
	// agent.ShareNodeID - so it carries the station, NEVER the hostname or a port, into
	// /discover. Seeded from the saved/auto-generated station; the in-TUI [2] SHARE `n`
	// rename updates it live + persists via SaveStation.
	Station     string
	SaveStation func(station string) // persist a station rename (nil = in-session only; the TUI does no disk I/O)
	// ConsoleURL is the tokenized URL of this run's browser node console ("" = no
	// console this run, e.g. --no-webui). The console no longer auto-opens at launch
	// (founder respec 2026-07-14); `w` in BROWSE and /webui in AGENT open it on demand.
	ConsoleURL  string
	HW          string  // hardware label for the offer
	GitHubID    string  // public GitHub OAuth client id (device flow)
	LinkedLogin string  // the locally-linked GitHub login at startup ("" = anonymous)
	ShareModel  string  // saved onboarding model (default offer)
	SharePriceI float64 // saved input price (0 = free)
	SharePriceO float64 // saved output price (0 = free)
	// ShareUpstream + ShareUpstreamKey seed the saved/verified local endpoint (and any
	// bearer key it needs) from the host config, so a custom / key-protected upstream
	// saved during onboarding is probed FIRST and reused on the TUI's first /share scan -
	// not re-hunted or re-prompted. Empty for the common auto-detected no-auth server.
	ShareUpstream    string
	ShareUpstreamKey string
	// SaveUpstream persists a newly verified local endpoint + any bearer key it needed
	// (auto-detected or pasted in the guided fallback), so a custom / key-protected
	// upstream survives a restart and is reused on the next scan - the TUI mirror of the
	// CLI's save in `roger share`. nil = session-only (the host owns the disk write).
	SaveUpstream func(upstream, key string)
	// ShareMaxOnAir is the SOFT local cap on how many bands may be ON AIR at once (the
	// share.max_on_air config knob), read once at startup. The [2] SHARE selector shows
	// the ON AIR n/max slots and BLOCKS flipping another row on air at the cap. <=0 means
	// "use the package default" (defaultShareMaxOnAir).
	ShareMaxOnAir int
	Login         func(broker, clientID string) (string, error) // device-flow login -> github login
	// LoginBegin starts the GitHub device flow and returns the URL + code to show
	// (no polling); LoginPoll then blocks until the user authorizes and returns the
	// linked login. Split so the TUI can render its own clean login panel + auto-open
	// the browser instead of relying on the CLI's stdout (hidden behind the TUI). When
	// nil the TUI falls back to the single-shot Login hook.
	LoginBegin func(broker, clientID string) (LoginDevice, error)
	LoginPoll  func(broker, clientID string, d LoginDevice) (string, error)
	// Logout forgets the local GitHub binding (the in-TUI logout). nil degrades the
	// logout panel to a labeled hint.
	Logout      func() error
	TopupURL    func(broker, user string, usd float64) (string, error)
	GrantCreate func(broker, name string, free bool) (secret string, err error)
	GrantList   func(broker string) ([]GrantRow, error)
	// SavePrice persists a per-model price + time-of-use schedule the in-TUI editor
	// produced, so the choice survives the session (nil = in-session only). The host
	// owns the config write; the TUI keeps no disk I/O.
	SavePrice func(model string, p Pricing)
	// SavedPrices seeds the editor with prices the user set in a previous session, so
	// the provider table shows them and on-air uses them (nil = none).
	SavedPrices map[string]Pricing
	// SavedVoices seeds each model's on-air voice identity (dj name / default voice /
	// speed / language / sample clip URL) from the host's config.json share_voices block,
	// so a saved identity - including the BOOTH-less sample_url - arms the offer without
	// a BOOTH pass (nil = none). The host owns the disk read; the TUI does no I/O.
	SavedVoices map[string]VoiceConfig
	// Compact seeds the "windowshade" compact mode at launch from the saved config, so
	// the [m] choice sticks across sessions (the host owns the disk read).
	Compact bool
	// SaveCompact persists the compact toggle when the user presses [m], so the calm
	// view is remembered next launch (nil = session-only; no disk I/O in the TUI).
	SaveCompact func(bool)
	// SaveSession atomically persists a completed AGENT conversation. The host owns the
	// private session directory; nil keeps the historical session-only behavior.
	SaveSession func(session.Snapshot) error
	// --- BASE STATION / remote control (v5.0.0). All nil-safe (a labeled hint degrades). ---
	// RCEnable starts a remote-control session for THIS machine's live agent and returns a
	// host bridge (tees agent events out, drains remote turns/confirms) + the one-time
	// enable info to print. The host owns the signing (local user key).
	RCEnable func(broker, name string) (RemoteBridge, RemoteInfo, error)
	// RCList fetches the owner's remote-session roster for BASE STATION (metadata only).
	RCList func(broker string) ([]RemoteSessionRow, error)
	// RCRevoke ends one session (id != "") or every session (id == "").
	RCRevoke func(broker, sessionID string) error
	// BandList fetches the owner's private bands for the BASE STATION bands list.
	BandList func(broker string) ([]BandRow, error)
	// BandRevoke permanently revokes a band. The frequency code stops resolving for
	// everyone and can never be revived; it frees the owner's quota slot.
	BandRevoke func(broker, bandID string) error
	// BandMove repoints a band at another node ("<station>-<model>") WITHOUT rotating its
	// secret, so everyone already tuned in keeps working.
	BandMove func(broker, bandID, nodeID string) error
	// BandRotate mints a FRESH secret for an existing band, in place: same id, same node,
	// same label, same quota slot, same cosmetic frequency. Returns the new code, shown
	// ONCE. The OLD code stops resolving immediately - anyone already tuned in IS cut off,
	// which is the whole difference from BandMove.
	BandRotate func(broker, bandID string) (code, display string, err error)
	// BandLabel sets a band's human name (empty clears it). The broker has accepted a
	// label since bands existed; nothing ever sent one, so every list identified bands by
	// their raw id.
	BandLabel func(broker, bandID, label string) error
	// BandForget deletes a REVOKED band row for good. It is the only way to clear the dead
	// history that otherwise accumulates around a live band forever; the broker refuses a
	// live band, so this can never strand a working code.
	BandForget func(broker, bandID string) error
	// RCAttach exchanges a link code for a per-device attach token, so the TUI can view a
	// session hosted on ANOTHER machine. Returns (attachToken, sessionID, name).
	RCAttach func(broker, code string) (attach, sessionID, name string, err error)
	// RCJoin mints an attach token for one of the OWNER's OWN sessions BY ID (no code — the
	// BASE STATION roster carries no code; same-account is sufficient to view your own session).
	RCJoin func(broker, sessionID string) (attach string, err error)
	// RCStream opens the viewer SSE stream and calls onFrame for each frame until ctx ends
	// or the session closes (long-lived; the TUI cancels ctx on esc/quit).
	RCStream func(ctx context.Context, broker, sessionID, attach string, lastSeq uint64, onFrame func(protocol.RCFrame)) error
	// RCSend posts a viewer turn/confirm to a session (interleaved input from the TUI).
	RCSend func(broker, sessionID, attach string, in protocol.RCInbound) error
	// Station is the owner's callsign (reused to auto-name a session "<station> · <cwd>").
	// (Station also seeds the share flow; declared once above.)
}

// RemoteBridge is the host side of a live /remote-control session: the TUI tees each local
// agent event out via Emit, drains remote turns/confirms/backfill from Inbound (via a
// re-armed Cmd), and ends the session via Disable. The concrete impl lives in internal/client
// (it polls + POSTs the broker); a test supplies a fake. Frames use the shared protocol types.
type RemoteBridge interface {
	Emit(f protocol.RCFrame)
	Inbound() <-chan protocol.RCInbound
	Done() <-chan struct{} // closed when the bridge is Stopped (revoked/quit) — unparks the drain
	SessionID() string
	Disable() error // take the session off the air (revoke)
	Stop()          // stop polling (session survives; used on quit)
	Run()           // start the poll + event pumps
	// Guest-operator interlock (Phase 2): while parked, inbound turns/confirms are
	// dropped AT THE BRIDGE with a status auto-frame and backfill is answered from the
	// snapshot - the host's event loop is suspended under tea.ExecProcess. Unpark on a
	// dead/stopped bridge is a no-op. model + spend (a LIVE session-spend reader, may be
	// nil) enrich the parked auto-frames (rc_enrichment.feature) - metadata only, never
	// a band label (founder ruling 2: the private Freq secret stays off every frame).
	Park(operator, snapshot, model string, spend func() float64)
	Unpark()
}

// RemoteInfo is what /remote-control prints once at enable.
type RemoteInfo struct {
	SessionID string
	Name      string
	Code      string // the full one-time link code (shown once)
	CodeShort string // the typeable / deep-link tail ("8FK3-9MQ2")
	LinkURL   string // rogerai.fm/r/<short>
}

// RemoteSessionRow is one BASE STATION roster row (metadata only).
type RemoteSessionRow struct {
	ID          string
	Name        string
	CodeDisplay string
	Online      bool
	Revoked     bool
}

// LoginDevice is the display-ready view of a started GitHub device flow the TUI
// renders in its login panel: the URL to open + the short code to type. Handle is
// the opaque continuation the host's LoginPoll uses to resume polling.
type LoginDevice struct {
	VerificationURI string
	UserCode        string
	Handle          any
}

// quiet is true when output isn't an interactive color TTY (NO_COLOR set, or
// piped / redirected). lipgloss already strips color in that case; we also
// freeze the animation to a single representative frame so the on-air pulse
// and signal bars render as a clean static fallback instead of garbled glyph
// churn in a pipe. Honors DESIGN.md: "static fallback when NO_COLOR / non-TTY".
var quiet = func() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return true
	}
	return !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd())
}()

// anim returns the live frame counter, or a fixed frame when quiet so motion
// settles into a stable, well-formed snapshot.
func anim(frame int) int {
	if quiet {
		return 1
	}
	return frame
}

// bandCapGlyph resolves a band-badge capability mark LIVE from the current glyph set
// (unlike the package-init glyph vars above), so a test that flips ROGERAI_ASCII after
// init sees the ASCII fold. agentReadyGlyph carries its inferred "~" at the call site.
func agentReadyGlyph() string { return glyphs.Current().AgentReady }
func visionGlyph() string     { return glyphs.Current().Vision }

// beaconPulse is the breathing "(( • ))" Ping beacon string, folded to ASCII
// ("((*))") on a legacy Windows console. Centralized so the one motif has one source.
func beaconPulse() string { return glyphs.Current().Beacon }

// beaconDot is the compact one-glyph "(•)" beacon, folded to "(*)" on a legacy
// Windows console (the bullet is the rune that garbles).
func beaconDot() string { return glyphs.Fold("(•)") }

// channelGlyph picks the honest mark for a held channel: the confidential ◆ ONLY when
// the connected node passed real TEE attestation, otherwise the lineage/identity ✓.
func channelGlyph(o *offer) string {
	if o != nil && o.Confidential {
		return glyphConf
	}
	return glyphLineage
}

// selCarat is the NO_COLOR / non-TTY selection marker: a bold `>` the eye still
// catches when the reverse-video background is stripped. A space keeps unselected
// rows aligned under the same gutter.
func selCarat(sel bool) string {
	if sel {
		return stSelText.Render(">")
	}
	return " "
}

// ticks the cursor `>` eases in after a move
// ticks (~3s) a transient status lingers before auto-dismiss

// caratGutter renders the 2-char selected-row gutter with a 1-frame slide cue: the cursor `>`
// eases in from the right (" >") for the first caratSlideFrames ticks after a move (caratFrame),
// then settles to "> ". Always exactly 2 columns (no row jiggle) and NO_COLOR-safe (the carat
// glyph itself moves). 0 caratFrame (fresh model / no move yet) = the settled "> ".
func (m model) caratGutter() string {
	if m.mode == modeBrowse && m.caratFrame > 0 && m.frame-m.caratFrame >= 0 && m.frame-m.caratFrame < caratSlideFrames {
		return " " + stSelText.Render(">")
	}
	return stSelText.Render(">") + " "
}

// ambientStatus is the PERSISTENT browse footer summary (bands · stations on air). It is what
// the status line falls back to when a transient toast auto-dismisses, so the browse footer
// never flickers blank between scans. "" outside the band views (CHANNEL's transcript carries
// the signal), so there the toast clears to empty.
func (m model) ambientStatus() string {
	if m.mode == modeBrowse || m.mode == modeCommand {
		// LLM (chat) bands + their stations only — voice bands live in THE DJ BOOTH, so folding
		// them into the top-level "N bands · M stations" would over-count what the list shows.
		return fmt.Sprintf("%s · %s on air", plural(m.llmBands(), "band"), plural(m.llmStationsOnAir(), "station"))
	}
	return ""
}

// rowSel renders a table row body so the SELECTED row is k9s-style reverse-video
// (a full-width accent background bar) and unselected rows are plain. The `plain`
// text for a selected row should carry no per-cell color - one reverse-video style
// governs the whole row (mixing fg colors inside a bg run reads as noise). Under
// NO_COLOR the background is stripped automatically and the caller's leading
// selCarat carries the cursor instead.
func rowSel(sel bool, plain string, width int) string {
	if !sel {
		return plain
	}
	if w := lipgloss.Width(plain); w < width {
		plain += strings.Repeat(" ", width-w)
	}
	return stRowSel.Render(plain)
}

// detectShares is the indirection over local-LLM detection used by the SHARE
// flows, so tests can make it deterministic (the real Detect scans the host's open
// ports). Production uses detect.DetectFull, which also reports key-protected
// servers (needKey) so the guided fallback can ask for a key instead of dead-ending.
var detectShares = func(extra ...string) (found []detect.Found, needKey []string) {
	return detect.DetectFull(extra...)
}

// marketMedianOut is the indirection over the live per-model market-median lookup
// used by the editor's fat-finger guard (the TUI mirror of the CLI softPriceWarn),
// so tests can make it deterministic. Production reads /discover via the client.
var marketMedianOut = func(broker, model string) (float64, bool) {
	return client.MarketMedianOut(broker, model)
}

// detectSharesCmd runs detectShares in a goroutine (a tea.Cmd) so the SHARE flows
// detect local models WITHOUT blocking the Bubble Tea event loop - probing a busy
// host's open ports can take a few seconds, which would otherwise freeze every
// keystroke with no feedback. The result comes back as a sharesDetectedMsg the
// Update handler folds into the provider table. detectShares stays injectable so
// tests can make this deterministic (a test can also feed sharesDetectedMsg
// directly to exercise the handler).
func detectSharesCmd(extra, key string) tea.Cmd {
	return func() tea.Msg {
		// A saved keyed upstream is reused without a re-prompt: try it WITH its key first
		// (the broad scan does not carry the key), then fall back to full detection. This
		// mirrors the CLI's bare-`roger share` reuse of a saved keyed endpoint.
		if extra != "" && key != "" {
			if f, st := detect.ProbeKey(extra, key); st == detect.Reachable {
				return sharesDetectedMsg{found: []detect.Found{f}}
			}
		}
		found, needKey := detectShares(extra)
		return sharesDetectedMsg{found: found, needKey: needKey}
	}
}

type offer struct {
	NodeID string `json:"node_id"`
	Region string `json:"region"`
	HW     string `json:"hw"` // privacy-bucketed hardware class (multi-gpu/single-gpu/apple/cpu)
	Model  string `json:"model"`
	// Modality is what the station DOES: "chat" (the back-compat default), "tts" (speak), or
	// "stt" (listen), carried from the broker's /discover feed. It is what lets the browser tell
	// a VOICE band apart from a chat band so a voice station is offered as a PREVIEW, never
	// (wrongly) as a chat channel that would 504 ("no station is serving <voice>").
	Modality     string  `json:"modality,omitempty"`
	PriceIn      float64 `json:"price_in"`
	PriceOut     float64 `json:"price_out"`
	PriceTier    int     `json:"price_tier"` // broker's neutral 0..4 $-tier (0 = FREE/unknown)
	Ctx          int     `json:"ctx"`
	CtxEstimated bool    `json:"ctx_estimated"` // Ctx is the estimated default, not a detected window
	// Capabilities is the broker's declared per-station capability set (e.g. "vision").
	// Decode-only on this side: the browser NEVER fabricates a capability the station
	// did not declare, so an ABSENT set claims nothing (no "text-only" badge).
	Capabilities []string `json:"capabilities,omitempty"`
	// Quant / Weights / Variant tell this station's offer apart from another station's
	// offer of the SAME model (MODEL-VARIANTS-DESIGN-2026-08-22). Decode-only, like
	// Capabilities: the browser never fabricates one, so an ABSENT value claims nothing -
	// it is not a quant, and it is never rendered as though the station stated one.
	Quant        string  `json:"quant,omitempty"`
	Weights      string  `json:"weights,omitempty"`
	Variant      string  `json:"variant,omitempty"`
	Online       bool    `json:"online"`
	Confidential bool    `json:"confidential"`
	FreeNow      bool    `json:"free_now"`
	TPS          float64 `json:"tps"`
	TTFTMs       float64 `json:"ttft_ms"`      // probe-measured time-to-first-token (ms; 0 = unmeasured)
	SuccessRate  float64 `json:"success"`      // 0..1 time-decayed success evidence
	SuccessSeen  bool    `json:"success_seen"` // SuccessRate is REAL (not the no-evidence fallback)
	Verified     bool    `json:"verified"`     // recent PASSED serving canary (distinct from confidential ◆)
	// Signal is the broker's 0..100 channel-health score (online + quality + tps +
	// reliability). It carries even when TPS==0, so a freshly-on-air band meters at
	// its baseline strength instead of a blank tps-driven bar.
	Signal int `json:"signal"`
	// InFlight is the broker's count of active (in-flight) requests on this station
	// right now (cmd/rogerai-broker market.go emits it per offer). It is what makes the
	// signal meter an HONEST live-activity readout: a station actively serving
	// (InFlight>0) visibly scans/pulses, an idle-but-online one is steady, offline is
	// flat. Drives only animation INTENSITY, never the bar LEVEL (that stays Signal).
	InFlight int `json:"in_flight"`
	// Terms is the broker's per-factor signal breakdown (supply/speed/latency/verified/
	// success/trust + congestion), surfaced so the expanded station view can explain
	// WHY a band scores what it does.
	Terms signalTerms `json:"terms"`
}

func (a *alertBox) set(s string) { a.mu.Lock(); a.msg = s; a.mu.Unlock() }
func (a *alertBox) take() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.msg
	a.msg = ""
	return s
}

type mode int

const (
	modeBrowse mode = iota
	modeCommand
	modeChat
	modeHelp
	modeConnectConfirm    // 3.2 cost confirmation (default DENY)
	modeConnecting        // staged scan/lock/handshake/CHANNEL-OPEN sequence (the web's tune-in)
	modeOverLimit         // 3.3 over-limit + inline edit-your-max
	modeLimits            // 3.4 per-model spend limits
	modeShare             // k9s-style provider table: list local models, toggle on/off-air
	modeBandCard          // private band code card: shows the one-time frequency code after going private
	modeShareEditor       // per-model pricing + time-of-use schedule editor (login-gated)
	modeShareSetup        // guided fallback: no local model detected, pick a tool / paste a URL
	modeQuitConfirm       // on-air quit-guard: confirm before going off air on quit
	modeAgent             // [0] AGENT: the embedded tool-capable agent harness (dj.md persona)
	modeLogin             // [L] confirmable login/logout panel (never an instant action)
	modeBandDetail        // [i] expanded per-station QSL view: every station's real metrics + the signal-term breakdown
	modeFreqEntry         // [~] small input to ENTER a private frequency code (tune off the OPEN MARKET onto a hidden band)
	modeBandManage        // BASE STATION: the actions card for ONE of your own bands (move / revoke) (rc.go)
	modeBandMove          // BASE STATION: pick which local model to MOVE a band to - the code survives (rc.go)
	modeBandRevokeConfirm // BASE STATION: the explicit y/N confirm before burning a band's code forever (rc.go)
	modeBandRotateConfirm // BASE STATION: the y/N confirm before replacing a band's code (cuts off everyone tuned in)
	modeBandConfig        // ONE CARD PER BAND: every setting for a single band, in one place (band_config.go)
	modeBandLabel         // the small input that names a band (a band's only human-readable handle)
	modeBandQuants        // the small input for a band's ACCEPTED QUANTS rule (band_config.go)
	modePingWorld         // [z] / `/ping`: the fullscreen Ping World screensaver; any key wakes back to prevMode
	modeLog               // /log: the captured node + broker log buffer (any key closes)
	modeVoicePreview      // a VOICE band (tts/stt): a sample-play/preview panel, NOT a chat channel (voice.go)
	modeVoiceBooth        // THE DJ BOOTH: the tts voices lineup, a CHILD screen of THE BAND (esc returns). Voice is a dim footnote off the LLM list, never a peer section (voice.go)
	modeListeningPost     // THE LISTENING POST: the stt info/how-to screen, drilled into FROM the Booth (esc returns to the Booth). Info only — no preview, no chat (voice.go)
	modeShareVoice        // SHARE VOICE BOOTH: the operator's voice-sharing wizard, reached via `p` on a tts share row — same depth as the chat price editor (voicebooth_share.go)
	modeVoicePicker       // SHARE VOICE BOOTH picker popover: pick a Kokoro voice (local list + bundled fallback), audition free (voicebooth_share.go)
	modePrivate           // [p] BASE STATION: your private side of the dial — remote agent sessions + private bands, a CHILD screen of THE BAND (esc returns) (rc.go)
	modeRemoteSession     // a live remote-control session view: continue a chat running on another machine, streamed + labeled private (rc.go)
)

// acceptsQuant reports whether q is allowed under this limit. An empty set accepts
// everything; an UNSTATED quant is accepted by any set, because a station that said
// nothing has not said the wrong thing - refusing it would narrow routing on the strength
// of missing metadata rather than on a station's actual claim.
func (l Limit) acceptsQuant(q string) bool {
	if len(l.Quants) == 0 || strings.TrimSpace(q) == "" {
		return true
	}
	for _, want := range l.Quants {
		if strings.EqualFold(strings.TrimSpace(want), q) {
			return true
		}
	}
	return false
}

func (s *LimitStore) resolve(model string) Limit {
	if s == nil {
		return Limit{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.Models[model]; ok {
		return l
	}
	return s.Default
}

func (s *LimitStore) typical() int {
	if s == nil || s.TypicalOut <= 0 {
		return 800
	}
	return s.TypicalOut
}

func (s *LimitStore) set(model string, l Limit) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setLocked(model, l)
}

// setLocked writes one cap; the caller must hold mu. Splitting the map mutation out of the
// locking lets Set and Update do a check-then-write under a SINGLE lock without re-entering
// (a sync.Mutex is not reentrant - set() calling itself through the lock would deadlock).
func (s *LimitStore) setLocked(model string, l Limit) {
	if s.Models == nil {
		s.Models = map[string]Limit{}
	}
	s.Models[model] = l
	if s.Save != nil {
		s.Save(s.Models, s.Default)
	}
}

// Set is the exported mutator, for a SECOND front-end editing the same store.
//
// The browser console shows the same per-band caps [3] CONFIG does, and it must write to
// THIS store rather than a copy: two stores would let the terminal and the browser disagree
// about what the operator is willing to pay, and the disagreement would only surface as an
// unexplained refusal on some later turn. A zero value clears the cap rather than recording
// "refuse everything" - the same rule the TUI's own editor follows.
func (s *LimitStore) Set(model string, l Limit) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// "Nothing set" now includes the QUANT rule. Without the Quants check this cleared a
	// quant-only preference the moment it was saved - the operator set a rule, the store
	// decided the limit was empty, and the rule vanished silently. A limit is unset only
	// when it says nothing at all.
	if limitIsUnset(l) {
		s.clearLocked(model)
		return
	}
	s.setLocked(model, l)
}

// Update applies f to the model's CURRENT stored cap and writes the result, as one atomic
// read-modify-write under the lock. The browser console's save needs exactly this: it edits
// the two fields its price form can see (MaxOut, MinTPS) and must carry every OTHER field -
// MaxIn, the quant rule, anything Limit gains later - from what is stored. Reading with
// Snapshot and writing with Set as two calls would let a concurrent TUI edit land between
// them and be lost; f runs while the lock is held so it cannot. f starts from the model's
// own stored cap (zero value if unset), NOT Default - a console edit pins this one band.
func (s *LimitStore) Update(model string, f func(cur Limit) Limit) {
	if s == nil || f == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := f(s.Models[model])
	if limitIsUnset(next) {
		s.clearLocked(model)
		return
	}
	s.setLocked(model, next)
}

// limitIsUnset reports whether a cap says nothing at all - every knob at/below zero and no
// quant rule - in which case storing it would clear the entry rather than record a cap.
func limitIsUnset(l Limit) bool {
	return l.MaxOut <= 0 && l.MinTPS <= 0 && l.MaxIn <= 0 && len(l.Quants) == 0
}

// Snapshot returns a COPY of the per-model caps, safe to hand to another front-end to
// render. A copy rather than the live map: a reader iterating while the TUI writes would
// otherwise be a data race on the operator's money settings.
func (s *LimitStore) Snapshot() map[string]Limit {
	out := map[string]Limit{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for m, l := range s.Models {
		out[m] = l
	}
	return out
}

func (s *LimitStore) clear(model string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearLocked(model)
}

// clearLocked deletes one cap; the caller must hold mu (see setLocked).
func (s *LimitStore) clearLocked(model string) {
	if s.Models == nil {
		return
	}
	delete(s.Models, model)
	if s.Save != nil {
		s.Save(s.Models, s.Default)
	}
}

// quote is the resolved deal for a connect attempt: the band, the chosen
// station, the effective limit, and the est-cost numbers.
type quote struct {
	b         band
	limit     Limit
	estReply  float64 // credits per typical reply at the cheapest out-price
	typical   int
	overLimit bool
}

type model struct {
	broker, user string
	offers       []offer
	cursor       int
	// selectedModel is the band the cursor is ON (by name), so the selection STICKS to that
	// band across re-sorts/redraws (signal sorting reshuffles positions every rescan). Without
	// it, the cursor is a bare index and a re-sort mid-scroll would land Enter on the wrong band.
	selectedModel string
	width, height int
	frame         int
	tickGen       int // the live tick-chain generation; a kick bumps it so older chains die (see tick())
	mode          mode
	// prevMode + world back the in-TUI Ping World screensaver (`/ping` or z): we stash the
	// mode we came from, run the same pingWorldModel the standalone `roger --ping` uses, and
	// any key restores prevMode. The world advances on the shared 160ms tick (see tickMsg).
	prevMode mode
	world    pingWorldModel
	// message-in reveal: when a chat reply lands, msgInFrom marks where its block starts in
	// transcript and msgInFrame stamps the frame, so refreshScroll dims that block for a beat
	// then lets it settle to full ink (a calm "ink-settling" arrival). See revealBlock.
	msgInFrom  int
	msgInFrame int
	// caratFrame stamps the frame the browse cursor last moved, so the selected-row `>` eases
	// in for a beat (caratGutter) - a 1-cell motion cue. 0 = no pending slide.
	caratFrame int
	// statusFrame stamps when the status line last changed, so the tick auto-dismisses it as a
	// transient toast in the main views (A.6.6). Stamped centrally in Update. 0 = nothing fresh.
	statusFrame int
	cmd         textinput.Model
	// cmdHist is the command palette's recall buffer (prior run commands), distinct from
	// the chat/agent histories; persists to <config>/rogerai/history-command. See history.go.
	cmdHist *inputHistory
	chatIn  textarea.Model
	// chatHist is the CHANNEL chat input's shell-style recall buffer (Up = older sent
	// message, Down = newer; Down past the newest restores the in-progress draft). It
	// persists to <config>/rogerai/history-chat, distinct from the agent's history. See
	// history.go.
	chatHist   *inputHistory
	transcript []string
	// ring is the MINIMAL per-turn context ring (ruling Q4): one capsule.Message per
	// completed turn (role/content/turn/model/provider/agent/ts), fed from the chatMsg
	// before it is discarded. It is NOT a render source (the flat transcript stays that);
	// it exists only to EXPORT a portable roger.context.v1 capsule on an operator handoff
	// and MERGE a returning one append-only. ringTurn is the next turn index; threadID is
	// the session's stable origin thread id. See context_capsule.go.
	ring     []capsule.Message
	ringTurn int
	threadID string
	// Durable AGENT session metadata. The semantic ring remains the source of truth;
	// only completed user/assistant pairs are handed to SaveSession.
	sessionTitle            string
	sessionWorkdir          string
	sessionWorkdirAvailable bool
	sessionCreated          time.Time
	// agentTurnCalls accumulates the tool calls of the AGENT turn in flight; they are
	// consumed onto the assistant turn when it completes (context_capsule.go), so a call
	// rides on the turn that made it and never leaks into the next one.
	agentTurnCalls []capsule.ToolCall
	// lastReply is the RAW (unstyled) text of the most recent station reply, kept so
	// ctrl+y / `/copy` yank clean text to the clipboard (the transcript holds styled lines).
	lastReply string
	// mouseOff: mouse reporting state. The default is false: Roger owns transcript
	// dragging so release can copy exactly and report the character count. ctrl+o /
	// /mouse sets it true to restore native terminal selection immediately.
	mouseOff bool
	// smartSel: the application-owned drag selection while mouse capture is ON
	// (smart mouse mode) - anchor/head cells, drag/held state (smartselect.go).
	smartSel smartSelState
	// chatVP is the INDEPENDENT scroll region for the CHANNEL transcript: the
	// response area scrolls (PgUp/PgDn, Ctrl+U/D, mouse wheel, and the arrow keys
	// once command history is exhausted) on its own while the `you ›` input keeps
	// working and keeps its Up-arrow history recall. It auto-sticks to the bottom on
	// new output, but holds position when the user has scrolled up. Sized from the
	// window each Update (see refreshScroll / chatView). The agent has its own
	// agentVP. Source of truth stays m.transcript; the viewport renders from it.
	chatVP viewport.Model
	// helpVP scrolls the HELP screen (audit P0: at common terminal heights the
	// "start here" section was clipped off-screen with no way to scroll).
	helpVP    viewport.Model
	connected *offer
	endpoint  string
	apikey    string
	// lastConnected is the band we most recently TUNED IN to (a "sticky" recent
	// station). It is kept across band re-scans so a band you connected to never
	// vanishes from the browse list when its node ages out of /discover - it stays as
	// an available, tunable station you can re-tune. Set on connect, kept on disconnect
	// (you disconnected on purpose, so you most want to reconnect), cleared only when a
	// fresh /discover lists the band on air again (the live offer takes over). See the
	// offersMsg handler (sticky-band merge) + disconnect().
	lastConnected *offer
	// recentBands records every model we have tuned in to this session, so a re-connect
	// to one is FAST: the staged scan/lock/handshake animation plays only on the FIRST
	// (cold) tune-in to a band; a band in this set drops straight into the open channel
	// (warm reconnect). Cleared by nothing this session (a band stays "warm" once tuned).
	recentBands map[string]bool
	// operatorSeenModels records every model that has ALREADY surfaced the focused AGENT
	// DESK this session (Guest Operators): the first AGENT entry for a tuned model lands on
	// the selectable desk once a guest is detected; a second entry for the SAME model stays
	// ask-focused. Switching to a different model re-surfaces the desk once for it. Lazily
	// initialized; per-session (never cleared - an esc-exit keeps the record).
	operatorSeenModels map[string]bool
	// staged tune-in sequence (modeConnecting): connectStage is the step the
	// animation has reached (0..connectStageDone); connectStartFrame anchors the
	// per-step dwell to m.frame so the steps advance on the one carrier beat. Under
	// quiet (NO_COLOR / non-TTY / reduced-motion) the sequence renders fully resolved
	// in a single frame (no churn in a pipe).
	connectStage      int
	connectStartFrame int
	proxyUp           bool
	proxyAddr         string
	// proxyHolder is the LIVE options source the local proxy reads per request. It is created
	// once (first tune-in) and re-pointed on every (re)tune via SetBand, keeping the stable
	// per-session bearer key (proxyKey) so a running guest's config survives a re-tune; a
	// disconnect flips it to "refuse - no band tuned". nil until the proxy is bound.
	proxyHolder      *client.ProxyOptionsHolder
	proxyKey         string
	confidentialOnly bool
	balance          float64
	haveBal          bool
	monthlyCap       float64 // per-account monthly spend cap ($); 0 = unlimited
	monthlySpend     float64 // month-to-date captured spend ($)
	status           string
	alert            *alertBox
	// pricing UX state
	limits *LimitStore
	bands  []band // offers grouped by model (the band list, 3.1)
	// VOICE PREVIEW state (voice.go): selecting a voice (tts/stt) band opens modeVoicePreview
	// instead of a chat channel. previewBand is the band under preview; previewStage tracks the
	// panel (confirm-first for a PAID tts, synthesizing, played/saved, error, or the stt info
	// panel). previewCost/previewPlayed/previewPath/previewErr carry the last synth outcome.
	// previewPlayer is the INJECTABLE audio player (nil => the real system player) so the
	// synth+play path is testable without a real audio device. See startVoicePreview.
	previewBand   band
	previewStage  int
	previewCost   float64
	previewPlayed bool
	previewPath   string
	previewErr    string
	previewPlayer audioPlayerFn
	// boothCursor indexes the DJ BOOTH lineup (the tts voices drill-in). Voice is a DIM footnote
	// under the LLM band list (voiceFootnote); the footnote / `v` drills into modeVoiceBooth (a
	// CHILD screen of THE BAND). The Booth is the ONLY place a voice band is surfaced/cued — the
	// top-level list stays pure LLM. See boothDJs / voiceBoothView.
	boothCursor int
	// SHARE VOICE BOOTH state (voicebooth_share.go): the operator's voice-sharing wizard, reached
	// via `p` on a tts share row. vb* are the editor fields (dj-name/voice/blend/speed/lang/price +
	// focused field + inline error); vp* are the picker popover (fetched-or-bundled voice ids +
	// live filter + cursor). The result is stored on the shared *node.Controller on save, so the
	// row's offer carries the operator's picked voice/blend when it goes on air.
	vbModel       string
	vbName        string
	vbVoice       string
	vbBlend       []blendVoice
	vbSpeed       float64
	vbLang        string
	vbPrice       string
	vbField       int
	vbErr         string
	vpVoices      []string
	vpFilter      string
	vpCursor      int
	vpSourceLocal bool // true when vpVoices came from the LOCAL server (else the bundled fallback)
	// SCALE: the band browser is built for hundreds/thousands of stations, so the
	// list is FILTERED + SORTED into a derived view (visibleBands) and only the
	// VISIBLE window is rendered each frame (virtualized). m.cursor indexes the
	// VISIBLE set, never the raw m.bands. browseTop is the index of the first row
	// drawn in the window (it scrolls to keep the cursor in view). See visibleBands,
	// windowFor, and browseView. NOTE: the broker /discover returns the FULL on-air
	// set (no broker-side pagination) - client windowing + filter covers realistic
	// scale now; broker-side pagination + load-on-scroll is the next step IF on-air
	// counts ever exceed a few hundred. See fetchOffers.
	// dialPos / dialVel are the BROWSE tuning-dial pointer's spring state (harmonica): the
	// ◆ glides toward the tuned band's detent as you scrub the list. Advanced in the tick
	// loop, gated by `animating` like all motion (see dial.go / dialGlide).
	dialPos, dialVel float64
	dialInit         bool            // dialPos has been seeded to the tuned band (else snap, don't glide from 0)
	filterMode       bool            // the live filter input line is open (f)
	filterIn         textinput.Model // the live name filter buffer
	freqIn           textinput.Model // the private-frequency entry buffer (modeFreqEntry)
	filterApplied    string          // the applied name substring (kept after enter; lowercased compare)
	sortMode         int             // band sort cycle (see sort* consts) - mirrors the /bands web page
	fFree            bool            // toggle: only bands with a FREE-now station
	fConf            bool            // toggle: only confidential / verified (lineage) bands
	fOn              bool            // toggle: only bands with a station on air
	// fQuant narrows the dial to ONE compression label (Q cycles it). Empty = every band.
	// It is a VIEW, not a rule: it changes what you are looking at and binds nothing. The
	// standing rule that binds an unattended turn is the [3] CONFIG preference.
	fQuant     string
	browseTop  int    // first visible row index in the virtualized window
	loadedOnce bool   // a /discover scan has come back at least once (drives the initial ((•)) scanning pose)
	q          quote  // the in-flight connect quote (confirm / over-limit)
	editBuf    string // inline numeric edit buffer (over-limit + limits edit)
	editField  int    // which field is focused in the limits editor (0=out,1=tps)
	limCursor  int    // cursor in the limits view
	limModels  []string
	watching   string    // band we are "wait & notify" watching (stub label)
	detailBand band      // the band whose expanded per-station view (modeBandDetail) is showing
	showDetail bool      // [d] expands the connect-confirm screen; default off (simple)
	relaying   bool      // a chat request is in flight (drives Ping's transmit line)
	relayStart time.Time // when the in-flight chat began (for the elapsed "transmitting Ns")
	scanErr    bool      // last band scan failed (broker unreachable) -> Ping "...static"
	scanned    bool      // at least one scan has come back (good or empty) -> Ping idle, not tx
	emptyScans int       // consecutive EMPTY /discover scans; debounces a transient empty (a rescan that load-balanced onto a still-syncing broker instance) so a populated list doesn't flicker to "no stations". See the offersMsg handler.
	minimized  bool      // header toggle: thin one-line bar vs the full lockup
	// compact is the "windowshade" mode (XMMS/Winamp collapse): a calm, dense,
	// animation-free alternate view toggled by [m] in every non-text-entry context.
	// When set the header drops to one strip, all motion freezes (carrier beat, Ping,
	// the ((•)) spinner), rows tighten, and the frame tick idles when nothing is in
	// flight - an explicit prefers-reduced-motion within the app. Persisted via the
	// host SaveCompact hook (nil = session-only).
	compact bool
	// chat session state (CHANNEL mode)
	sysPrompt     string  // /system prompt prepended to each turn
	sessCost      float64 // running session cost in dollars (sum of per-reply costs)
	sessTokensIn  int     // running CHANNEL session BILLED prompt (↑) tokens — the broker re-count, for display (mirror of agentTokensIn)
	sessTokensOut int     // running CHANNEL session BILLED completion (↓) tokens — the broker re-count, for display
	showStats     bool    // /stats: append the verbose per-turn metric line (price in/out) to new replies
	// [0] AGENT state (modeAgent): the embedded tool-capable harness. agent holds the
	// session-only loop (dj.md persona + bounded tools); agentIn is the prompt; the
	// transcript carries the streamed turn (assistant text, tool calls, results,
	// answer). agentBusy is true while a turn runs in the background goroutine; the
	// confirm sub-state (agentPendingConfirm) pauses the turn for a y/N on a mutating
	// tool. agentCost is the running session cost. See agent.go for the wiring.
	agent   *agentRuntime // nil until first entered; built lazily
	agentIn textarea.Model
	// agentHist is the [0] AGENT prompt's shell-style recall buffer, separate from the
	// chat's (Up = older sent prompt, Down = newer; Down past the newest restores the
	// draft). It persists to <config>/rogerai/history-agent. See history.go.
	agentHist *inputHistory
	// agentPastes holds large pasted blocks by 1-based number while the composer shows
	// only a placeholder for each (paste.go). Expanded back at submit, so the model
	// receives what was pasted and the input stays legible.
	agentPastes []string
	// agentFullPersona is the operator's own dj.md, kept so refreshAgentBudget can swap
	// between it and the compact brief as the tuned band's window changes.
	agentFullPersona string
	// agentDelegates is the live view of subagents this turn, keyed by label. Fed by
	// forwarded child events (delegation.go) and cleared with the turn.
	agentDelegates      map[string]*delegateState
	agentLines          []string       // the rendered AGENT transcript (you ▸ / tool ◉ / answer ◂)
	agentVP             viewport.Model // the AGENT transcript's independent scroll region (mirror of chatVP)
	agentBusy           bool           // a turn is in flight (drives the working line)
	agentCanceling      bool           // esc-cancel requested for the in-flight turn; a 2nd esc force-stops
	agentQueued         []queuedPrompt // prompts parked mid-turn, auto-sent FIFO when the turn finishes (Claude-style queue); each entry carries its origin - a remote entry never slash-dispatches at drain
	agentLastEvent      time.Time      // last streamed event time; powers the receiving-vs-stalled working line (hung detection)
	agentTurnState      agentPose      // the reactive corner-Ping pose (waiting/thinking/streaming/tool), derived from the harness event stream
	agentHadToolResult  bool           // the current/previous turn produced a result; drives the next-step prompt hint
	agentNextHint       string         // outcome-derived next action shown by the idle composer
	agentStart          time.Time      // when the in-flight turn began (elapsed readout)
	agentPendingConfirm *agentConfirm  // non-nil while a mutating tool awaits y/N
	agentCost           float64        // running AGENT session cost in dollars
	agentTokensIn       int            // running AGENT session BILLED prompt (↑) tokens — the broker re-count, for display
	agentTokensOut      int            // running AGENT session BILLED completion (↓) tokens — the broker re-count, for display
	agentTPS            float64        // LATEST relay call's throughput (tokens/sec) for the live meter; not summed
	// TOOL CALLS AS DATA (toolrun.go). agentRuns holds the records; the transcript
	// holds ordered references into it. This replaced five fields that between them
	// hand-tracked "the card we are about to rewrite" (line index, tool, target,
	// running, approved) - with records the only thing to track is which record is
	// still open, and the facts live on the record instead of being re-derived from
	// the string it was formatted into.
	agentRuns     []toolRun // every tool call this session, oldest first
	agentOpenRun  int       // index of the call still in flight; -1 when none
	agentStep     int       // current model/tool-loop iteration (1-based; 0 between untouched turns)
	agentMaxSteps int       // harness safety ceiling shown in the truthful session rail
	// /model selection state. agentPicked marks that the user chose the model
	// explicitly (so auto-resolution does not snap it back). agentPicker is the modal
	// list (open with 2+ candidates); agentPickerRows is the candidate models and
	// agentPickerCursor the selected row. See agent.go (openAgentModelPicker / the
	// picker key + view).
	agentPicked bool // the model was chosen via /model (sticky over auto-resolve)
	// agentPickedOver is the channel identity (node+model) that was OPEN when the user
	// picked via /model - the pick must survive turns on that same channel (the founder's
	// "I switched to deepseek and the next ask snapped back to Qwen"). Only tuning a
	// DIFFERENT channel afterwards re-points the agent. "" = nothing was open at pick time.
	agentPickedOver   string
	agentPicker       bool             // the /model picker modal is open
	agentPickerRows   []agentPickerRow // candidate models in the open picker
	agentPickerCursor int              // selected row in the picker
	// localFound is the last BACKGROUND scan of OpenAI-compatible servers on THIS machine
	// (detect.DetectFull). It is a cache and is never fetched on picker-open: detect probes
	// ~12 ports at 1.5s each, and /model is instant today precisely because it reads only
	// in-memory state. See localModelsCmd.
	localFound []detect.Found
	// localScanning is true while the BACKGROUND scan of this machine's model servers is
	// in flight. It exists because the scan's absence was indistinguishable from its
	// result: /model on a freshly-launched app saw only the broker bands, and an operator
	// who knew they had four local models read that as the app being wrong (founder
	// 2026-08-22: "i thought something was wrong but after trying 3-4 times my local list
	// showed up"). Anything that shows a model list has to be able to say "still looking".
	localScanning bool
	// Guest Operators (Phase 2, THE DESK): the async desk-scan result, the /operator
	// picker modal, and the live handoff state. See operator.go.
	operatorDetections []operator.Detection // detected guest CLIs (registry order)
	operatorPicker     bool                 // the /operator hand-the-mic modal is open
	operatorRows       []operatorRow        // picker rows (DJ + detected + at most one suggestion)
	operatorCursor     int                  // selected picker row (never the suggestion)
	operatorHandoff    *operatorHandoff     // non-nil from staging until the exec returns
	operatorPlate      *operatorPlate       // the Phase 3 pre-launch confirm plate; nil = no plate up
	// AGENT [0] desk entry (the redesign): when the AGENT lands with nothing tuned in,
	// THE DESK becomes the FOCUSED, selectable operator picker (R3) - the ask box is NOT
	// focused, arrows move deskCursor, Enter on the DJ focuses the ask box and Enter on a
	// guest opens the pre-launch plate; any printable rune falls through to the ask box
	// and clears deskFocused (the DJ-still-types-through path). autoTuning marks a silent
	// auto-tune in flight (R1/R6); autoTuneBeatLen is the transcript length BEFORE the
	// "finding a band…" beat, so the beat is swapped for the outcome without stacking.
	deskFocused     bool
	deskCursor      int
	autoTuning      bool
	autoTuneBeatLen int
	// agentPending holds prompts submitted while NO model is tuned in: rather than fire a
	// doomed turn (the "no station on air" spam), the turn is parked, a silent auto-tune
	// is kicked, and the prompt is sent the moment a band lands (drained by runAutoTune).
	agentPending      []queuedPrompt
	agentLandingLines int // transcript length that still counts as the AGENT landing (entry chrome only)
	// `ask ›` slash-command autocomplete (agent.go: agentCommands / agentSlashStrip /
	// the tab case in onAgentKey). agentTabPrefix is the typed prefix a live Tab
	// completion cycle is stepping ("" = no cycle); agentTabIdx is the current pick
	// in agentSlashCandidates(agentTabPrefix) - the carated strip entry.
	agentTabPrefix string
	agentTabIdx    int
	// agentPaneFocus: which AGENT pane owns the keyboard. false = the ask input (the
	// default: arrows recall history, typing types). true = the TRANSCRIPT (tab from
	// an empty/non-slash input): arrows + pgup/pgdn/home/end scroll, the seam row
	// lights up as the focus cue, and esc / enter / any typed rune hand the keyboard
	// back to the input. The mouse wheel scrolls the transcript in EITHER state
	// (real wheel events; mouse capture is on by default).
	agentPaneFocus bool
	// showToolOutput expands the (default-hidden) tool-result OUTPUT previews across the
	// whole AGENT transcript; the `d` key (transcript pane focused) toggles it. Machinery
	// dims to texture: the tool CALL + result stay one dim line each, the full output rides
	// behind this toggle (design overhaul §4).
	showToolOutput bool

	// showToolCalls opens the folded tool-machinery cards (⌃o). Default FALSE: a turn
	// that touched eleven files used to print twenty-two rows of ⚙/✓ chatter and push
	// the actual answer off the screen (founder 2026-08-20, with a screenshot of it).
	// Folded, that same turn reads as one line naming what ran.
	showToolCalls bool
	// async, cached update check (non-blocking) + the in-TUI upgrade banner state
	updateLine string // "update available v<cur> -> v<new>" or "" (set by updateMsg)
	upg        upgState
	// in-TUI provider/account/money flows (TUI-V2-CRITIQUE D / audit C5)
	hooks     Hooks          // host-supplied platform/auth bits (nil-safe)
	share     *agent.Session // most-recently-shared in-process session (the panel's headline; nil = none)
	onAir     bool           // ON AIR indicator + panel (true while any share is live)
	ghLogin   string         // linked GitHub login (set at startup if linked, or once /login succeeds); "" = anonymous
	loggedIn  bool           // true when the broker confirms a real account wallet (gates the balance display)
	grantList []GrantRow     // last /grant list result
	// BASE STATION / remote control (v5.0.0). rcBridge is the live HOST bridge for THIS
	// machine's agent (nil unless /remote-control is on); rcInfo is its one-time enable info
	// (for re-copy); rcSessions is the roster cache for modePrivate; rcCursor/rcErr drive the
	// section. See rc.go (tui).
	rcBridge   RemoteBridge
	rcInfo     RemoteInfo
	rcSessions []RemoteSessionRow
	rcBands    []BandRow
	// Band management (modeBandManage / modeBandMove / modeBandRevokeConfirm): which band
	// the card is acting on, and the cursor in the move picker's local-model list.
	// bandMoveOffer is the model a quota refusal just happened for: the share screen
	// offers to move the existing band here rather than sending the operator away
	// (band_manage.go). "" when there is no offer standing.
	bandMoveOffer  string
	bandManageID   string
	bandManageDisp string
	bandManageNode string
	bandMoveCursor int
	rcCursor       int
	rcErr          string
	rcPrevMode     mode // where 'esc' returns from modePrivate / modeRemoteSession
	// modeRemoteSession (the in-TUI viewer): the session being viewed + its streamed lines.
	rsRow    RemoteSessionRow
	rsAttach string   // this device's attach token for rsRow
	rsLines  []string // the streamed remote transcript (rendered)
	rsVP     viewport.Model
	rsIn     textinput.Model
	rsSeq    uint64                // last frame seq seen (Last-Event-ID reconnect)
	rsFrames chan protocol.RCFrame // the viewer stream's frame channel (drained by a re-armed Cmd)
	rsCancel context.CancelFunc    // cancels the viewer stream on esc/quit
	rsGen    int                   // stream generation: a frame/end from an older session is ignored
	// Confirm correlation (mutating-tool safety). rcConfirmID is the HOST's current pending
	// confirm id; a remote answer must carry the matching id (a stale answer for a resolved
	// confirm can never resolve a NEW one). On the VIEWER, rsPendingConfirm gates y/n as a
	// confirm answer (a real flag, not a string-match) and rsConfirmID is echoed back.
	rcConfirmID      string
	rsPendingConfirm bool
	rsConfirmID      string
	// [L] confirmable login/logout panel (modeLogin). The panel never acts on arrival -
	// only y (logout) / enter (start login) inside it does - so arrow-nav can land on it
	// without surprises. loginReturn is the mode to restore when the panel is dismissed.
	loginReturn  mode        // mode to return to when the login/logout panel is dismissed
	loginDevice  LoginDevice // the started device flow (URL + code) while waiting for auth
	loginWaiting bool        // true once the device flow started and we are polling for auth
	loginNote    string      // a one-line panel note (e.g. "opened in your browser")
	// k9s-style SHARE / provider table (modeShare): one row per locally-detected
	// model, each independently flippable on/off air. shares holds the live session
	// per on-air model; shareRows is the rendered model list; shareCursor is the
	// highly-visible reverse-video selection cursor.
	// ctrl is the SINGLE, mutex-guarded owner of the live share state (sessions, rows,
	// prices, private flags, station, upstream). The web console (internal/webui) holds
	// the SAME *node.Controller, so a toggle in the browser flips a TUI row and vice-versa.
	// The fields below (shares/shareRows/...) are a TUI-goroutine-private render CACHE,
	// refreshed from the controller by syncShareCache(); every mutation goes through ctrl.
	ctrl        *node.Controller
	shares      map[string]*agent.Session // model -> live in-process session (on air) [cache]
	shareRows   []shareRow                // the provider table rows (detected models) [cache]
	shareCursor int                       // selected row in the provider table
	shareUp     string                    // the local upstream chat URL backing the shares
	shareKey    string                    // bearer key the headline upstream needs (env/paste), if any
	// shareSavedUp/Key track what was last PERSISTED via Hooks.SaveUpstream (the /v1
	// base + key), so a re-detection that lands the same endpoint doesn't rewrite config.
	shareSavedUp  string
	shareSavedKey string
	quitReturn    mode // the mode to restore if the on-air quit-guard is declined
	// station is the live, slugged broadcast callsign every band's node id is derived
	// from (`<station>-<model>`). Seeded from Hooks.Station; the `n` rename in [2] SHARE
	// edits it (renaming buffer = stationEdit while renaming==true) and persists via
	// Hooks.SaveStation. NEVER the hostname - it is the public /discover identity.
	station     string
	renaming    bool // [2] SHARE rename mode: keystrokes build stationEdit until enter/esc
	stationEdit string
	// Private bands ("frequency codes"): sharePrivate[model] marks a row shared on a
	// hidden band (h toggles it). The band-card buffers hold the one-time secret code +
	// cosmetic display to show ONCE on a modeBandCard card (c copies it). The card
	// returns to SHARE on any key.
	sharePrivate map[string]bool // model -> shared on a private (hidden) band
	// bandCardReturn is where the one-time code card goes back to, and bandCardReturnSet
	// says whether it was chosen. The card was written for the SHARE mint flow and
	// hard-returned to modeShare; a ROTATE can be started from BASE STATION or the PRIVATE
	// tab, and dumping the operator on the share table after it would be a silent teleport.
	//
	// The BOOL is load-bearing: modeBrowse is the ZERO value of mode, so a "return to the
	// band browser" was indistinguishable from "nothing was set" and got silently replaced
	// by modeShare - which is exactly the teleport this field exists to prevent.
	bandCardReturn    mode
	bandCardReturnSet bool
	bandCardCode      string // the one-time secret frequency code (cleared on leave)
	bandCardDisp      string // cosmetic "147.520 MHz · ..." for the card
	bandCardModel     string // which model the card is for
	// TUNE-IN private band: tuneFreq is the active frequency code (empty = OPEN MARKET);
	// tuneFreqLabel is the cosmetic display shown in the header (e.g. "147.520 MHz").
	// /freq sets them after a successful resolve; esc clears back to OPEN MARKET.
	tuneFreq      string
	tuneFreqLabel string
	// [1] TUNE IN's two halves: tabOpenMarket (the public dial) and tabPrivate (your own
	// bands, from /bands). t switches. privCursor is the private list's own cursor - it is
	// separate from m.cursor so switching tabs never lands the market cursor on a band
	// index that only existed in the other list. See tune_private.go.
	tuneTab    tuneTab
	privCursor int
	// THE BAND CARD (modeBandConfig, band_config.go): which band it is showing, and which
	// list to return to. cfgReturnSet is load-bearing for the same reason bandCardReturn's
	// is - modeBrowse is the ZERO value of mode, so "go back to the band browser" would be
	// indistinguishable from "nothing was set".
	cfgModel     string
	cfgReturn    mode
	cfgReturnSet bool
	// cfgLabelIn is the small input that names a band. A band's label is its only
	// human-readable handle: without one the list identifies bands by "band_2395187610cc7".
	cfgLabelIn textinput.Model
	// limReturn is where [3] CONFIG goes back to. It is normally the browser; when the
	// BAND CARD routed here to edit one field, it is the card - otherwise an operator who
	// pressed `e` on a card would be dropped on a spend-limit table they never opened.
	limReturn    mode
	limReturnSet bool
	// A LOCAL channel: the open CHANNEL runs DIRECT against a server on this machine
	// (harness.LocalCompleter) instead of through the broker relay. Set by openLocalChannel
	// when tuning one of your own private bands whose model runs here; cleared by
	// disconnect. Non-empty is what makes the chat send path, the pre-flight and the
	// channel header all take the direct route - so it must never outlive the channel.
	chatLocalChat string
	chatLocalKey  string
	// async SHARE detection: probing the host's open ports for local LLMs can take a
	// few seconds on a busy box (120+ listening ports). shareLoading marks the
	// provider table as "scanning the band…" while detection runs OFF the Bubble Tea
	// event loop (a tea.Cmd goroutine returning sharesDetectedMsg), so pressing
	// [2]/SHARE/r never freezes the UI. sharePending holds the optional `/share
	// <model>` shortcut model to flip on air once detection lands. setupOnEmpty
	// chooses whether an empty detect drops into the guided setup wizard (the initial
	// open) or stays on the table with a "still nothing" note (the in-table r
	// re-detect, which must not yank the user into the wizard mid-table).
	shareLoading bool
	sharePending string
	setupOnEmpty bool
	shareRescan  bool   // the in-flight detect is a retry (re-scan), not a first open
	setupHint    string // the note to show in the wizard if the in-flight rescan finds nothing
	// per-model pricing + time-of-use schedule editor (modeShareEditor). prices the
	// row at shareCursor; persisted via the host SavePrice hook (nil = in-session only).
	edPriceIn  string             // $/1M in edit buffer
	edPriceOut string             // $/1M out edit buffer
	edWindows  []SchedWindow      // time-of-use windows being edited
	edField    int                // focused field (see edField* consts)
	edWinSub   int                // focused sub-field within a window (see winSub* consts)
	edWinBuf   string             // in-progress digit buffer for the focused window price sub-field
	edModel    string             // the model this editor is pricing
	edErr      string             // inline validation error in the editor (blocks save; "" = none)
	prices     map[string]Pricing // per-model saved pricing (in/out + schedule)
	// guided-fallback share setup wizard (modeShareSetup): pick a tool for a
	// one-liner, or paste a URL we verify with detect.ProbeKey.
	setupCursor int    // selected option in the setup wizard
	setupPaste  string // the pasted-URL buffer (when the "Other" option is chosen)
	setupErr    string // last paste-verify error
	// setupAwaitKey + setupKey drive the second input step when a pasted endpoint is
	// reachable but KEY-PROTECTED (a 401/403): the input flips to collecting the API
	// key, which we send as a Bearer to re-verify and then carry onto the share row.
	setupAwaitKey bool
	setupKey      string
	// payout: a lightweight, lazily-fetched snapshot of the operator's Connect/KYC
	// state + payable balance, surfaced as a one-line hint in the ON-AIR / SHARE
	// earnings surface ("$X payable - run `roger payout`" or "complete KYC: ...").
	// Fetched off the event loop (a tea.Cmd) only for a logged-in owner; payoutFetched
	// guards the one-shot fetch so the SHARE view doesn't re-hit the broker on render.
	payout        payoutSnapshot
	payoutFetched bool
}

// edField identifies the focused field in the pricing/schedule editor.
const (
	edFieldIn       = iota // $/1M input price
	edFieldOut             // $/1M output price
	edFieldAddWin          // the "add a time-of-use window" affordance
	edFieldFirstWin        // first window row (each window is one field below this)
)

// winSub identifies the focused sub-field WITHIN a time-of-use window row, cycled
// with left/right so a window can edit its Start, End, and in/out prices (not just
// Start) - otherwise a window publishes with In=Out=0 unintentionally.
const (
	winSubStart = iota // "HH:MM" window start
	winSubEnd          // "HH:MM" window end
	winSubIn           // $/1M in inside the window
	winSubOut          // $/1M out inside the window
	winSubCount        // number of sub-fields (for modulo cycling)
)

// Pricing is the per-model saved price + schedule the editor produces. The host
// persists it (and feeds it back as Hooks.SavedPrices); on-air it is applied when a
// model goes live.
type Pricing = node.Pricing

// a chat turn failed - surfaced INLINE in the CHANNEL transcript

// gen: the tick-chain generation; a stale gen is a dead chain (see tick())

// github login on success
// checkout URL
// a newly created grant's secret (shown once)

// a flow failed (login/topup/grant) - shown on the status line

func New(broker, user string) model {
	return NewWith(broker, user, nil)
}

// NewWith builds the model with a spend-limit store (nil = no caps / no persist).
func NewWith(broker, user string, limits *LimitStore) model {
	return NewWithHooks(broker, user, limits, Hooks{})
}

// NewController builds the shared node controller from the host hooks (the SINGLE owner
// of the live share state). The host calls this once and hands the SAME *node.Controller
// to both NewWithHooksController and the web console, so a change in one front-end shows
// up in the other.
func NewController(broker string, hooks Hooks) *node.Controller {
	// The live broadcast station: the saved/auto-generated callsign (NEVER the hostname),
	// slugged so it matches the node id exactly; a fresh callsign if the host supplied none.
	station := agent.SlugStation(hooks.Station)
	if station == "" {
		station = agent.GenerateStation()
	}
	return node.New(node.Config{
		Broker: broker, HW: hooks.HW, Station: station,
		ShareModel: hooks.ShareModel, SharePriceI: hooks.SharePriceI, SharePriceO: hooks.SharePriceO,
		MaxOnAir:    hooks.ShareMaxOnAir,
		Upstream:    hooks.ShareUpstream,
		UpstreamKey: hooks.ShareUpstreamKey,
		Prices:      hooks.SavedPrices,
		Voices:      hooks.SavedVoices,
		Hooks: node.Hooks{
			SaveUpstream: hooks.SaveUpstream,
			SavePrice:    hooks.SavePrice,
			SaveStation:  hooks.SaveStation,
		},
	})
}

// NewWithHooks is NewWith plus the host-supplied hooks for the in-TUI provider /
// account / money flows. It builds its own controller; use NewWithHooksController to
// share one with the web console.
func NewWithHooks(broker, user string, limits *LimitStore, hooks Hooks) model {
	return NewWithHooksController(broker, user, limits, hooks, NewController(broker, hooks))
}

// NewWithHooksController is NewWithHooks over an EXISTING shared controller, so the TUI
// and the browser console drive one node.
func NewWithHooksController(broker, user string, limits *LimitStore, hooks Hooks, ctrl *node.Controller) model {
	m := newBase(broker, user, limits)
	m.hooks = hooks
	// Reflect the locally-linked login at startup so the header shows the right state
	// before the first /balance comes back. The broker's logged_in flag (from the signed
	// balance read) is the source of truth and confirms it.
	m.ghLogin = hooks.LinkedLogin
	m.ctrl = ctrl
	m.ctrl.SetLoggedIn(m.loggedInState())
	// Seed the windowshade compact mode from the saved config so the [m] choice sticks.
	m.compact = hooks.Compact
	m.syncShareCache() // populate the render cache (station, prices, upstream) from the controller
	return m
}

func newBase(broker, user string, limits *LimitStore) model {
	ci := textinput.New()
	// We render the `rog ›` lockup ourselves in promptLine, so the input carries no
	// prompt of its own (avoids a doubled marker). Its View() still echoes live.
	ci.Prompt = ""
	ci.Placeholder = "search · connect · chat · share · login · topup · grant · limits · balance · help · quit"
	ch := textarea.New()
	ch.Prompt = ""
	ch.Placeholder = "type to talk  ·  /? for commands  ·  drag to copy"
	ch.ShowLineNumbers = false
	ch.SetPromptFunc(chatPromptLeadWidth, func(line int) string {
		if line == 0 {
			return chatPromptLead
		}
		return strings.Repeat(" ", chatPromptLeadWidth)
	})
	ch.FocusedStyle.Prompt = stSelBar
	ch.BlurredStyle.Prompt = stSelBar
	ch.MaxHeight = chatPromptMaxRows
	// A blinking Bubbles cursor emits a message every ~530ms. Bubble Tea repaints for
	// each message, which clears native terminal selection even when the visible frame
	// is otherwise idle. Roger is native-select-first, so composers use a crisp steady
	// cursor; actual key events still repaint immediately.
	ch.Cursor.SetMode(cursor.CursorStatic)
	ag := textarea.New()
	ag.Prompt = ""
	ag.Placeholder = "ask the agent to do something"
	ag.ShowLineNumbers = false
	ag.SetPromptFunc(agentPromptLeadWidth, func(line int) string {
		if line == 0 {
			return agentPromptLead
		}
		return strings.Repeat(" ", agentPromptLeadWidth)
	})
	ag.FocusedStyle.Prompt = stSelBar
	ag.BlurredStyle.Prompt = stSelBar
	ag.MaxHeight = agentPromptMaxRows
	ag.SetHeight(1)
	ag.Cursor.SetMode(cursor.CursorStatic)
	fi := textinput.New()
	fi.Prompt = ""
	fi.Placeholder = "type to filter bands by name"
	fq := textinput.New()
	fq.Prompt = ""
	fq.Placeholder = "frequency code"
	// The band NAME input. Bounded to the broker's own label limit so an over-long name is
	// refused at the keyboard rather than by a 400 after the operator finished typing.
	bl := textinput.New()
	bl.Prompt = ""
	bl.Placeholder = "home gpu"
	bl.CharLimit = 64
	m := model{broker: broker, user: user, cmd: ci, chatIn: ch, agentIn: ag, filterIn: fi, freqIn: fq, cfgLabelIn: bl,
		// Per-surface input history (distinct files; load tolerates a missing/corrupt file).
		cmdHist:  newInputHistory("history-command"),
		chatHist: newInputHistory("history-chat"), agentHist: newInputHistory("history-agent"),
		// Independent transcript scroll regions (mouse-wheel enabled by viewport.New); sized
		// from the window on the first WindowSizeMsg (refreshScroll).
		chatVP: viewport.New(0, 0), agentVP: viewport.New(0, 0),
		proxyAddr: "127.0.0.1:4141", status: "tuning in…", alert: &alertBox{}, limits: limits,
		// Smart selection owns transcript drags by default: release copies exactly once and
		// produces counted feedback. ctrl+o / /mouse restores native terminal selection.
		mouseOff: false}
	m.sessionWorkdir = agentRoot()
	m.sessionWorkdirAvailable = true
	return m
}

func (m model) Init() tea.Cmd {
	// Seed the first tick chain at gen 0 (Init's model copy is discarded, so do NOT kick).
	return tea.Batch(fetchOffers(m.broker), fetchBalance(m.broker, m.user), tick(m.tickGen))
}

func (m model) syncComposerGeometry() model {
	w := m.effWidth()
	setTextareaGeometry(&m.chatIn, max(chatPromptLeadWidth+1, w), m.chatPromptRowCount(w))
	setTextareaGeometry(&m.agentIn, max(agentPromptLeadWidth+1, w), m.agentPromptRowCount(w))
	return m
}

// setTextareaGeometry keeps Bubbles' private viewport in sync when a composer grows.
// SetHeight alone does not reduce an existing YOffset: after a one-row viewport scrolls
// to the new continuation, growing it to two rows still starts at row 2 and hides row 1.
// Re-seeding the same value resets that private viewport to the top; cursor location and
// focus remain intact.
func setTextareaGeometry(input *textarea.Model, width, height int) {
	oldHeight := input.Height()
	line := input.Line()
	info := input.LineInfo()
	col := info.StartColumn + info.ColumnOffset
	value := input.Value()

	input.SetWidth(width)
	input.SetHeight(height)
	if height <= oldHeight || value == "" {
		return
	}
	input.SetValue(value)
	for input.Line() > line {
		input.CursorUp()
	}
	input.SetCursor(col)
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Refresh the share render cache from the shared controller FIRST, so anything the web
	// console changed (a model toggled on air, a price edited, a rename) shows up in the
	// terminal on the next message — most visibly the 160ms tick. Every TUI mutation also
	// re-syncs locally, so this never fights an in-flight keystroke.
	m.syncShareCache()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.world.w, m.world.h = msg.Width, msg.Height // keep the screensaver fullscreen on resize
		// A resize reflows the rows a drag was anchored to - cancel, never copy a
		// partial selection (the smart-mode cancellation contract).
		m.smartSel = smartSelState{}
	case tea.MouseMsg:
		// Smart mouse mode first: while capture is on, a left-drag over the
		// transcript is an application-owned selection (smartselect.go). Unhandled
		// events (the wheel) fall through to the viewport scroll below.
		var handled bool
		var cmd tea.Cmd
		if m, cmd, handled = m.onSmartMouse(msg); handled {
			return m, cmd
		}
		// Route the mouse wheel to the active transcript viewport so scrolling the
		// response area works (the viewport ignores everything but wheel events). Mouse
		// reporting is enabled via tea.WithMouseCellMotion in RunWithController.
		switch m.mode {
		case modeChat:
			m.chatVP, _ = m.chatVP.Update(msg)
		case modeAgent:
			m.agentVP, _ = m.agentVP.Update(msg)
		}
		return m, nil
	case smartCopyResultMsg:
		return m.onSmartCopyResult(msg), nil
	case tickMsg:
		// A stale tick chain (a kick bumped m.tickGen since this one was scheduled): let it die
		// silently - do NOT advance the frame or reschedule, so only the newest chain survives.
		if msg.gen != m.tickGen {
			return m, nil
		}
		// FRAME CLOCK + native-selection freeze: advance the animation clock ONLY when something is
		// actually animating (a turn in flight, a staged tune-in, share-detect, the screensaver, or
		// a transient toast clearing). When idle the frame FREEZES, so the rendered screen is
		// byte-identical tick-to-tick - the terminal's native mouse selection survives (a repaint
		// would wipe the highlight) and the idle UI reads calm + intentional rather than flickering.
		// A TRANSIENT toast keeps the clock ticking only until it auto-dismisses (the dismiss
		// window). Bounding to m.frame-m.statusFrame < toastFrames is what stops the PERSISTENT
		// browse ambient summary (which also sets a non-empty status) from pinning animating ON
		// forever - without this bound, browse/command never freeze and native selection is wiped.
		toastPending := m.status != "" && m.statusFrame > 0 && m.frame-m.statusFrame < toastFrames &&
			(m.mode == modeBrowse || m.mode == modeCommand || m.mode == modeChat || m.mode == modeAgent)
		// The BROWSE tuning-dial pointer glides toward the tuned band's detent (harmonica).
		// Under quiet/reduced-motion it SNAPS (no animation); otherwise it eases, and while
		// it's still settling it keeps the animation clock on (so the fast tick drives it).
		dialSettling := false
		if m.mode == modeBrowse {
			target := m.dialTargetX()
			switch {
			case !m.dialInit || quiet: // first use / reduced-motion: SNAP onto the tuned band
				m.dialPos, m.dialVel, m.dialInit = target, 0, true
			default:
				m.dialPos, m.dialVel, dialSettling = dialGlide(m.dialPos, m.dialVel, target)
			}
		}
		animating := m.relaying || m.agentBusy || m.shareLoading ||
			m.mode == modeConnecting || m.mode == modePingWorld || toastPending || dialSettling
		if animating {
			m.frame++
		}
		// The in-TUI Ping World owns the beat while it's up: advance its frame on the CALM
		// pingWorldTick (worldTickMs), bypassing both the compact/idle slow-tick and the
		// interactive fast tick. Any key exits back to prevMode (onKey's modePingWorld intercept).
		if m.mode == modePingWorld {
			m.world.frame++
			// keep the LIVE signal towers fresh: a calm re-scan every worldRescanFrames (the
			// normal browse rescan is skipped while the world owns the tick). offersMsg rebuilds
			// m.world.data. The world advances on the CALM pingWorldTick (worldTickMs), NOT the
			// app's fast 160ms tick, so it breathes like the standalone screensaver.
			if m.broker != "" && m.world.frame%worldRescanFrames == 0 {
				return m, tea.Batch(pingWorldTick(m.tickGen), fetchOffers(m.broker))
			}
			return m, pingWorldTick(m.tickGen)
		}
		// TOAST (A.6.6): auto-dismiss a transient status after toastFrames in the MAIN views, so
		// confirmations don't linger forever. Modal screens keep their status (it's the prompt).
		if m.status != "" && m.statusFrame > 0 && m.frame-m.statusFrame >= toastFrames &&
			(m.mode == modeBrowse || m.mode == modeCommand || m.mode == modeChat || m.mode == modeAgent) {
			// Revert to the persistent ambient summary (browse) instead of blanking, so the
			// footer never flickers empty between scans; CHANNEL/AGENT have none -> clears to "".
			m.status = m.ambientStatus()
		}
		if m.alert != nil {
			if a := m.alert.take(); a != "" {
				m.status = stEmber.Render("⚡ " + a)
			}
		}
		// While the staged tune-in is playing, advance it on the carrier beat (it owns
		// the tick until it drops into CHANNEL). It never fires a /discover re-scan mid
		// lock, so the sequence stays smooth.
		if m.mode == modeConnecting {
			return m.advanceConnect()
		}
		// IDLE: when nothing is animating (frame frozen above), drop to the calm 5s tick so the
		// screen stays static + natively selectable - the user can drag-select + copy like on any
		// normal terminal screen, and the view reads quiet. Real events (offers, balance, chat /
		// agent replies) still arrive via their own Cmds and repaint on change. (This used to be
		// compact-only; now EVERY idle view goes calm, which is also what makes copy work.)
		if !animating {
			if !m.idleDiscoveryEnabled() {
				return m, slowTick(m.tickGen)
			}
			return m, tea.Batch(slowTick(m.tickGen), fetchOffers(m.broker))
		}
		// Periodic band re-scan: the tick is 160ms; every ~rescanEveryFrames (~5s) we
		// pull a fresh /discover so the band table + the "is a station on air" check
		// stay live without the user pressing r. This keeps the consumer + share views
		// honest about who is actually on air (the broker ages a node out at ~35s).
		if m.frame%rescanEveryFrames == 0 {
			return m, tea.Batch(tick(m.tickGen), fetchOffers(m.broker))
		}
		return m, tick(m.tickGen)
	case freqResolvedMsg:
		if !msg.ok {
			// Uniform negative (wrong / revoked / expired / off air - indistinguishable).
			m.status = stEmber.Render("no station on that frequency (it may be off air)") + stDim.Render(" - check the code")
			return m, nil
		}
		// Tuned to a private band: show ONLY its offers, set the header indicator, and
		// route subsequent tune-ins via X-Roger-Freq. esc clears back to OPEN MARKET.
		m.tuneFreq, m.tuneFreqLabel = msg.freq, msg.label
		m.offers = msg.offers
		m.scanErr, m.scanned, m.loadedOnce = false, true, true
		m.bands = m.mergeStickyBand(groupBands(m.offers, m.limits))
		m.clampBrowse()
		m.mode = modeBrowse
		m.status = stRed.Render(glyphOnAir+" PRIVATE FREQ") + stDim.Render(" tuned · esc for OPEN MARKET")
		return m, nil
	case voicePreviewMsg:
		// A voice sample synth completed (or failed): fold the outcome into the preview panel.
		// Ignore a late result if the user already left the preview (mode changed).
		if m.mode != modeVoicePreview {
			return m, nil
		}
		return m.applyVoicePreview(msg), nil
	case boothPreviewMsg:
		// A SHARE VOICE BOOTH local preview / audition completed: fold the outcome (played/saved/
		// error) into the booth or picker. Ignore a late result once the operator left the booth.
		if m.mode != modeShareVoice && m.mode != modeVoicePicker {
			return m, nil
		}
		return m.applyBoothPreview(msg), nil
	case localVoicesMsg:
		// The LOCAL GET /v1/audio/voices fetch returned (or missed): refine the picker list, or keep
		// the bundled fallback. Only meaningful while the picker is open.
		if m.mode != modeVoicePicker {
			return m, nil
		}
		return m.applyLocalVoices(msg), nil
	case offersMsg:
		// A private freq is tuned: ignore the periodic public-market scan so it does not
		// clobber the freq-only band list (esc / a bare /freq returns to OPEN MARKET).
		if m.tuneFreq != "" {
			return m, nil
		}
		m.scanErr = false
		m.scanned = true // a scan returned (even empty) -> stop showing the loading pose
		// GLITCH FIX (band-list flicker): with 2 load-balanced broker instances, a re-scan can
		// land on the instance still mirroring the shared registry and return an EMPTY /discover
		// for a beat. Don't blank a POPULATED list on a single transient empty - keep the
		// last-known offers and only accept an empty once it's SUSTAINED (emptyScansToBlank
		// consecutive) or it's the first load. A genuine "all gone" still surfaces after the
		// short grace; the alternating-instance flicker stops (a full scan resets the counter).
		if len(msg) == 0 && m.loadedOnce && len(m.offers) > 0 {
			if m.emptyScans++; m.emptyScans < emptyScansToBlank {
				return m, nil // ignore the blip - keep the current band list + status
			}
		} else {
			m.emptyScans = 0
		}
		m.loadedOnce = true // the first scan has come back: never re-enter the initial loading pose
		m.offers = []offer(msg)
		m.bands = m.mergeStickyBand(groupBands(m.offers, m.limits))
		// AGENT [0] cold auto-tune: this scan was fetched to find a band for the DESK
		// landing. Decide now that the band list is in hand (single-shot; no retry loop).
		var autoTuneDrain tea.Cmd
		if m.autoTuning {
			autoTuneDrain = m.runAutoTune()
		}
		m.world.data = buildWorldData(m.bands) // refresh the screensaver's LIVE signal towers
		// Clamp the cursor + window into the FILTERED view (the list the user actually
		// navigates), so a re-scan that shrinks the matches never strands the cursor.
		m.clampBrowse()
		// "wait & notify" stub: if a watched band has dipped under the limit, say so.
		notified := false
		if m.watching != "" {
			for _, b := range m.bands {
				if b.model == m.watching && b.online {
					lim := m.limits.resolve(b.model)
					if lim.MaxOut == 0 || b.minOut <= lim.MaxOut {
						m.status = stLive.Render("⚡ " + b.model + " dipped under your limit (" + money(b.minOut) + " out) - tune in")
						m.watching = ""
						notified = true
					}
				}
			}
		}
		// Don't clobber a fresh dip-under notification, an in-flight relay, or a modal
		// sub-screen's own status with the periodic scan summary - it's a browse-mode
		// affordance only; in CHANNEL the transcript carries the signal.
		if !notified && !m.relaying && (m.mode == modeBrowse || m.mode == modeCommand) {
			m.status = m.ambientStatus()
		}
		return m, autoTuneDrain
	case autoTuneMsg:
		// The AGENT [0] DESK landing armed a silent auto-tune and a scan is already in
		// hand: decide now (R1/R6). Cold launches route through offersMsg instead.
		// runAutoTune has a pointer receiver + mutates m, so sequence the call BEFORE the
		// return value is copied (don't lean on Go's return arg-eval order).
		cmd := m.runAutoTune()
		return m, cmd
	case sharesDetectedMsg:
		return m.onSharesDetected(msg.found, msg.needKey)
	case privateRescanMsg:
		// A re-scan fired from a PRIVATE-band screen. It folds the detected rows in and
		// leaves the operator exactly where they were - unlike onSharesDetected, which
		// ends on the SHARE table.
		before := len(m.shareRows)
		if len(msg.found) > 0 {
			m.loadShareRows(msg.found)
		}
		m.syncShareCache()
		switch {
		case len(m.shareRows) > before:
			m.status = stLive.Render("found ") + stKey.Render(plural(len(m.shareRows)-before, "more model")) +
				stDim.Render(" on this machine")
		case len(m.shareRows) == 0:
			m.status = stEmber.Render("no local model server found - start one (ollama, llama.cpp, vLLM…), then press ") +
				stKey.Render("r")
		default:
			m.status = stDim.Render("re-scanned · ") + stDim.Render(plural(len(m.shareRows), "model")) +
				stDim.Render(" on this machine")
		}
		return m, nil
	case balanceMsg:
		m.loggedIn = msg.loggedIn
		if msg.loggedIn {
			m.balance, m.haveBal = msg.balance, true
			m.monthlyCap, m.monthlySpend = msg.monthlyCap, msg.monthlySpend
		} else {
			// Anonymous: no wallet/balance to show.
			m.balance, m.haveBal = 0, false
			m.monthlyCap, m.monthlySpend = 0, 0
		}
		// One-shot: a logged-in owner can have provider earnings, so fetch the payout
		// snapshot once (off the event loop) to drive the SHARE-view cash-out hint.
		if m.loggedInState() && !m.payoutFetched {
			m.payoutFetched = true
			return m, fetchPayoutStatus(m.broker)
		}
		return m, nil
	case chatMsg:
		m.relaying = false
		m.sessCost += msg.cost
		m.sessTokensIn += msg.tokensIn // running ↑ billed tokens (broker re-count), mirrors the AGENT meter
		m.sessTokensOut += msg.tokensOut
		reply := msg.reply
		if strings.TrimSpace(reply) == "" {
			// The station answered but with no content (an all-reasoning turn, or an
			// empty completion). Never render a blank arrow - say so plainly so the turn
			// is not a silent no-response.
			reply = stDim.Render("(the station replied with no text)")
		} else {
			m.lastReply = msg.reply // raw text, for ctrl+y / /copy
			// Record the assistant turn into the per-turn context ring (Q4). The tuned
			// band's model is public; the provider (if the broker reported one) rides the
			// x_roger provenance. Only real content is recorded (a no-text turn is skipped).
			mdl, prov := m.channelModelProvider(msg.provider)
			m.recordTurn("assistant", msg.reply, m.channelAgent(), mdl, prov)
		}
		m.msgInFrom, m.msgInFrame = len(m.transcript), m.frame // mark this block for the settle-in
		modelName := ""
		if m.connected != nil {
			modelName = m.connected.Model
		}
		m.transcript = append(m.transcript, chatAnswerBlock(modelName, reply)...)
		m.transcript = append(m.transcript, replyFooter(msg, m.showStats)...)
		// Per-turn session footer: the honest running ↑in ↓out (broker billed re-count) + cost,
		// via the SHARED sessionFooter so the CHANNEL + AGENT money surfaces never drift.
		if f := sessionFooter(m.sessTokensIn, m.sessTokensOut, m.sessCost); f != "" {
			m.transcript = append(m.transcript, "   "+f)
		}
		// Refresh the wallet after a billed turn so the header balance stays true.
		return m, fetchBalance(m.broker, m.user)
	case chatErrMsg:
		// A chat turn FAILED. The fix for the founder's silent no-response: the failure
		// lands IN the CHANNEL transcript (red, inline) - not just the footer - so the
		// user always sees an outcome right where they were typing.
		m.relaying = false
		// The same actionable surface the AGENT uses: a tight short cause + a [1] tune
		// in / [2] share next step, INLINE in the transcript (not just the footer) so a
		// 5xx / timeout / no-station is never a dead end.
		chatModel := ""
		if m.connected != nil {
			chatModel = m.connected.Model
		}
		// A DIRECT channel gets the local remedy: nothing about this turn went near the
		// broker, so [2] go on air / [1] tune in would send the operator to fix a
		// marketplace that was never involved.
		if m.chatLocalChat != "" {
			m.transcript = append(m.transcript, localFailureHint(string(msg), chatModel, m.narrow())...)
		} else {
			m.transcript = append(m.transcript, failureHint(string(msg), chatModel, m.narrow())...)
		}
		m.status = stEmber.Render("! " + shortFailure(string(msg), chatModel))
		return m, nil
	case errMsg:
		m.relaying = false
		if strings.HasPrefix(string(msg), "broker unreachable") {
			m.scanErr = true // the band scan dropped -> Ping goes "...static"
		}
		// A COLD AGENT [0] auto-tune fetches /discover first; if the broker is unreachable
		// the fetch fails HERE. Without this the auto-tune stays armed and the "finding a
		// free band…" beat sits up until a later rescan. Disarm, splice out the beat, and
		// note the honest unreachable state ONCE (noteOnce dedups), dropping any parked
		// prompt silently - there is no band to send it to.
		//
		// Scope the disarm to broker-UNREACHABLE errors only (audit finding): a non-unreachable
		// errMsg in the cold-fetch window (e.g. fetchBalance's errMsg("")) must NOT kill a tune
		// whose /discover then succeeds - and must not wrongly note "couldn't reach the broker".
		if m.autoTuning && strings.HasPrefix(string(msg), "broker unreachable") {
			m.autoTuning = false
			m.clearFindingBeat()
			m.noteOnce(
				stRed.Render("✕ ")+stEmber.Render("couldn't reach the broker to find a band"),
				hintTuneOrShare(m.narrow()))
			m.agentLandingLines = len(m.agentLines)
			m.flushPendingPrompts()
		}
		m.status = stEmber.Render("! " + string(msg))
		return m, nil
	case loginStartedMsg:
		// The device flow started: stash the URL + code so the login panel renders
		// them, auto-open the browser ONCE here (and only here - the poll never opens
		// anything), then kick off polling for the authorization. openURL self-gates on
		// an interactive TTY, so a headless / piped / background-service rogerai shows
		// the code but never hijacks a browser.
		m.loginDevice = LoginDevice(msg)
		m.loginWaiting = true
		if interactive() {
			m.loginNote = "opened in your browser (or copy the link above)"
		} else {
			m.loginNote = "open the link above + enter the code"
		}
		m.status = stDim.Render("waiting for GitHub authorization…")
		openURL(m.loginDevice.VerificationURI)
		return m, m.pollLoginCmd()
	case loginMsg:
		m.ghLogin = string(msg)
		m.loggedIn = true
		m.loginWaiting = false
		m.loginDevice = LoginDevice{}
		// Leave the login panel back to where the user was.
		if m.mode == modeLogin {
			m.mode = m.loginReturn
		}
		m.status = stLive.Render(glyphLineage + " verified operator @" + string(msg) + " - wallet ready ($1 starter credit on first login), you can now earn as a provider")
		// Refresh the wallet so the header flips to @login · $balance right away, and
		// (re)fetch the payout snapshot now that there is a signing identity to read it.
		m.payoutFetched = true
		return m, tea.Batch(fetchBalance(m.broker, m.user), fetchPayoutStatus(m.broker))
	case logoutMsg:
		m.ghLogin = ""
		m.loggedIn = false
		m.ctrl.Logout() // explicit sign-out: clear the shared login (SetLoggedIn is raise-only)
		m.haveBal = false
		m.balance = 0
		m.loginWaiting = false
		m.loginDevice = LoginDevice{}
		// Drop the payout snapshot: anonymous has no earnings/KYC to surface.
		m.payout = payoutSnapshot{}
		m.payoutFetched = false
		if m.mode == modeLogin {
			m.mode = m.loginReturn
		}
		m.status = stDim.Render("logged out - now anonymous (free models + grant keys); [L] to log back in")
		return m, nil
	case payoutStatusMsg:
		m.payout = payoutSnapshot(msg)
		return m, nil
	case topupMsg:
		// Auto-open the Stripe Checkout URL ONCE here (this msg lands once per /topup),
		// matching login/onboard/payout. openURL self-gates on an interactive TTY, so a
		// headless / piped / background-service rogerai prints the URL but never hijacks
		// a browser - hence the URL stays on screen as the copy-paste fallback.
		openURL(string(msg))
		hint := "  (opening in your browser - or copy to pay)"
		if !interactive() {
			hint = "  (open to pay)"
		}
		m.status = stEmber.Render("top up: ") + stKey.Render(string(msg)) + stDim.Render(hint)
		return m, nil
	case grantMsg:
		m.status = stLive.Render(glyphLineage+" grant created - secret (shown once): ") + stKey.Render(msg.secret)
		return m, nil
	case grantListMsg:
		m.grantList = []GrantRow(msg)
		if len(m.grantList) == 0 {
			m.status = stDim.Render("no grants yet - /grant create <name> mints a free key")
		} else {
			m.status = stLive.Render(plural(len(m.grantList), "grant") + " - see the panel")
		}
		return m, nil
	case bandActionMsg:
		// A move/revoke/rotate/forget landed. We return to BASE STATION and re-fetch the
		// roster, so the list reflects what actually happened rather than what we hoped.
		// A ROTATE is the exception: it carries a one-time secret, so it routes to the
		// show-once card instead - and remembers where to go back to, since the card was
		// written for the SHARE flow and would otherwise drop the operator on a screen
		// they did not come from.
		if msg.rotated && msg.code != "" {
			m.bandCardCode, m.bandCardDisp, m.bandCardModel = msg.code, msg.display, ""
			m.bandCardReturn, m.bandCardReturnSet = m.rotateReturnMode(), true
			m.mode = modeBandCard
			m.status = stRed.Render(glyphOnAir+" NEW CODE ") +
				stDim.Render("- the old one stopped working. Send this to anyone who needs the band.")
			return m, m.fetchRemoteRoster()
		}
		// Land back where the action was STARTED. BASE STATION is the historical home, but
		// the BAND CARD can start a rotate, a label or a revoke too, and dumping the
		// operator on a list they never opened is the same silent teleport the one-time
		// code card had.
		m.mode = modePrivate
		if m.cfgModel != "" {
			m.mode = modeBandConfig
		}
		switch {
		case msg.err != "":
			m.status = stEmber.Render("! " + msg.err)
			return m, nil
		case msg.labeled:
			name := msg.model
			if name == "" {
				m.status = stLive.Render("name cleared")
			} else {
				m.status = stLive.Render("named ") + stKey.Render(name)
			}
		case msg.forgotten:
			m.status = stLive.Render("forgotten") +
				stDim.Render(" - that dead row is gone from your list for good")
		case msg.moved:
			m.status = stLive.Render("moved - ") + stKey.Render(msg.model) +
				stDim.Render(" now answers on the same frequency code")
		case msg.revoked:
			// RECONCILE THE NODE. The band is gone broker-side, but this machine was
			// still registered PRIVATE behind it - hidden from the market and reachable
			// by nobody, with the SHARE row still reading PRIVATE. And because the
			// private flag survived, the operator's next `h` would have re-registered the
			// model PUBLICLY: the only way to rotate a code went through the open market.
			//
			// Taking it off air is the honest resolution. The operator revoked the one
			// way anyone could reach it; quietly publishing a model they deliberately hid
			// is the outcome that must never happen by accident.
			m.status = stDim.Render("revoked - that frequency code no longer resolves")
			if mdl := m.modelForNodeID(msg.node); mdl != "" && m.ctrl.BandRevoked(mdl) {
				m.syncShareCache()
				m.status = stDim.Render("revoked - the code no longer resolves, and ") +
					stKey.Render(mdl) + stDim.Render(" is off air. Press ") + stKey.Render("h") +
					stDim.Render(" on it in SHARE for a fresh band.")
			}
		}
		return m, m.fetchRemoteRoster()
	case flowErrMsg:
		m.status = stEmber.Render("! " + string(msg))
		return m, nil
	case agentEventMsg:
		return m.onAgentEvent(msg)
	case localModelsMsg: // a background scan of THIS machine's model servers landed
		return m.onLocalModels(msg)
	case operatorDetectedMsg: // an async desk scan landed (Guest Operators)
		return m.onOperatorDetected(msg)
	case operatorExecMsg: // the staged PATCHING paint elapsed - issue the exec
		return m.onOperatorExec()
	case operatorDoneMsg: // the guest returned the terminal (every child outcome)
		return m.onOperatorDone(msg)
	case remoteEnabledMsg:
		return m.onRemoteEnabled(msg)
	case remoteInboundMsg:
		return m.onRemoteInbound(protocol.RCInbound(msg))
	case remoteRosterMsg:
		return m.onRemoteRoster(msg)
	case remoteAttachedMsg:
		return m.onRemoteAttached(msg)
	case remoteFrameMsg:
		nm, cmd := m.onRemoteFrame(msg)
		// keep streaming while the viewer is open on THIS generation
		if mm, ok := nm.(model); ok && mm.mode == modeRemoteSession && msg.gen == mm.rsGen {
			return nm, tea.Batch(cmd, mm.reArmRemoteStream())
		}
		return nm, cmd
	case remoteViewerEndMsg:
		// A viewer stream ended. Ignore a stale generation (an older, esc'd session tearing
		// down) so it can't clobber a newly-opened session's live status.
		if msg.gen == m.rsGen && m.mode == modeRemoteSession {
			m.status = stDim.Render("stream ended · esc back")
		}
		return m, nil
	case remoteHostEndMsg:
		return m.onRemoteHostEnd()
	case agentConfirmMsg:
		// A side-effecting tool wants to run: pause the turn for an on-screen y/N (default
		// DENY). The loop goroutine is blocked on the confirm's resp channel meanwhile.
		c := agentConfirm(msg)
		m.agentPendingConfirm = &c
		// NO TRANSCRIPT LINE. This used to append a dim "? <summary>" record, on the theory
		// that "the resolution line (approved/denied) completes the story". There is no
		// resolution line: approval settles the tool BOX (markAgentActivityApproved), and
		// this line was never removed or updated - so every answered confirm left a
		// permanent "?" in the transcript claiming to still be waiting, and a turn with
		// three shell calls showed three stale questions plus the live one.
		//
		// It also contradicted the design the renderer already implements: while a confirm
		// is pending the box row for that call is deliberately HIDDEN, because "the
		// confirmation gate is the sole command surface while approval is pending". This
		// line was a second surface, and the one that could not settle. The gate shows the
		// full command while asking; the box shows the call and its outcome afterwards.
		m.status = ""
		// BASE STATION: give this confirm a fresh id and let any attached surface answer it.
		// The id lets the host reject a STALE remote answer (for an already-resolved confirm)
		// so a delayed 'approve' can never resolve a DIFFERENT mutating tool.
		m.rcConfirmID = protocol.NewRequestID()
		m.rcEmitConfirmReq(&c, m.rcConfirmID)
		return m, nil
	case upgradeDoneMsg:
		if msg.err != nil {
			m.upg = upgFailed
			tuiLog.Write([]byte("upgrade failed: " + msg.err.Error() + "\n"))
		} else {
			m.upg = upgDone
		}
		return m, nil
	case agentCostMsg:
		m.agentCost += msg.cost
		m.agentTokensIn += msg.tokensIn // running ↑ billed tokens (broker re-count)
		m.agentTokensOut += msg.tokensOut
		if msg.tps > 0 {
			m.agentTPS = msg.tps // LATEST call's throughput (not summed)
		}
		m.agentLastEvent = time.Now() // a cost tick is activity too (proof of life)
		// CRITICAL: a cost tick must NOT stop the stream. The drain (waitAgentEvent) is the
		// single reader of the events channel; if this handler returns without re-arming it,
		// draining halts at the FIRST cost event of a turn, the turn's real agentDoneMsg is
		// never observed, agentBusy never clears, and the turn appears hung forever (the
		// 835s freeze: working line + corner Ping spin on, input blocked, esc stuck on
		// "cancelling…"). Re-arm so the rest of the turn keeps flowing.
		return m, m.waitAgentEvent()
	case agentDrainRetryMsg:
		// The parked-queue re-check (see submitAgentPrompt). If the previous goroutine
		// has finished, drain; if it somehow has not, arm one more beat rather than
		// dropping the prompt on the floor - which is the bug this exists to fix.
		if len(m.agentQueued) == 0 || m.agentBusy {
			return m, nil
		}
		if m.agent != nil && m.agent.running.Load() {
			return m, agentDrainSoon()
		}
		nm, cmd := m.dequeueAgentPrompts()
		return nm, cmd
	case agentDoneMsg:
		m.agentBusy = false
		m.agentCanceling = false
		m.agentTurnState = poseWaiting // turn finished: the corner Ping stands by
		// THE DELEGATION RECEIPT. The live strip showed the children while they worked;
		// this is what they cost, once. Without it the per-agent receipts the harness
		// keeps would be invisible - and attribution nobody can see is not attribution,
		// it is bookkeeping for its own sake.
		if line := m.delegationReceiptLine(); line != "" {
			m.agentLines = append(m.agentLines, line)
		}
		m.agentDelegates = nil
		// Auto-send the next queued prompt (typed mid-turn), Claude-style. dequeue runs any
		// leading slash-commands inline and starts the first chat turn; the rest wait for it.
		if len(m.agentQueued) > 0 {
			nm, cmd := m.dequeueAgentPrompts()
			if nm.agentBusy {
				nm.status = stDim.Render("sent the queued message")
			} else {
				nm.status = stDim.Render("AGENT ready - ask it to do something")
			}
			return nm, tea.Batch(cmd, fetchBalance(nm.broker, nm.user))
		}
		m.status = stDim.Render("AGENT ready - ask it to do something")
		return m, fetchBalance(m.broker, m.user)
	case tea.KeyMsg:
		// Escape during (or after) a smart-mode drag clears the transient
		// highlight and copies nothing - it cancels the selection, not the view.
		if msg.Type == tea.KeyEsc && (m.smartSel.active || m.smartSel.held) {
			m.smartSel = smartSelState{}
			return m, nil
		}
		return m.onKey(msg)
	}
	// route to the active text input
	var cmd tea.Cmd
	switch m.mode {
	case modeCommand:
		m.cmd, cmd = m.cmd.Update(msg)
	case modeChat:
		m.chatIn, cmd = m.chatIn.Update(msg)
	}
	return m, cmd
}

// idleDiscoveryEnabled reports whether the calm tick may refresh /discover. Native
// terminal selection must survive indefinitely; even an identical offer response
// triggers a Bubble Tea update that can clear the terminal's highlighted cells.
func (m model) idleDiscoveryEnabled() bool {
	return !m.mouseOff
}

func textareaCanMoveUp(input textarea.Model) bool {
	info := input.LineInfo()
	return input.Line() > 0 || info.RowOffset > 0
}

func textareaCanMoveDown(input textarea.Model) bool {
	info := input.LineInfo()
	return input.Line() < input.LineCount()-1 || info.RowOffset < info.Height-1
}

// loadShareRows builds the provider table by FLATTENING every detected server x
// its served models into one row list (de-duplicated by model id), with EACH row
// carrying its own upstream chat URL. On a multi-endpoint box this lists all real
// local models - e.g. :8060 gpt-oss-20b, :8080 gpt-oss-120b, :8081 qwen3-vl-8b, and
// a shim's many models on :8788 - not just the first server's. The first detected
// server's chat URL is kept as m.shareUp for back-compat (the headline default),
// but on-air uses each row's own upstream so a model goes live against the server
// that actually serves it. The first server's models keep priority on a dup id.
// loadShareRows hands a detection result to the shared controller (which flattens every
// server × model into the de-duplicated catalog, adopts the headline upstream + key, and
// persists a newly-verified endpoint) and refreshes the render cache.
func (m *model) loadShareRows(found []detect.Found) {
	m.ctrl.LoadRows(found)
	m.syncShareCache()
}

// setShareRows seeds the catalog directly from already-known rows (the paste-verify path
// and unit tests), going through the controller so the web console sees the same rows.
func (m *model) setShareRows(rows []shareRow) {
	nr := make([]node.ShareRow, len(rows))
	for i, r := range rows {
		nr[i] = node.ShareRow{Model: r.model, Modality: r.modality, Ctx: r.ctx, CtxEstimated: r.ctxEstimated, Upstream: r.upstream, UpstreamKey: r.upstreamKey}
	}
	m.ctrl.SetRows(nr)
	m.syncShareCache()
}

// syncShareCache refreshes the TUI's single-goroutine render cache (shares/shareRows/
// sharePrivate/station/prices/shareUp/shareKey/share/onAir) from the shared controller,
// so a change made in the web console appears in the terminal on the next tick. Every
// share mutation the TUI makes goes THROUGH the controller, then calls this to re-read.
func (m *model) syncShareCache() {
	if m.ctrl == nil {
		return
	}
	m.ctrl.SetLoggedIn(m.loggedInState())
	nr := m.ctrl.Rows()
	rows := make([]shareRow, len(nr))
	for i, r := range nr {
		rows[i] = shareRow{
			model: r.Model, modality: r.Modality, ctx: r.Ctx, ctxEstimated: r.CtxEstimated,
			upstream: r.Upstream, upstreamKey: r.UpstreamKey,
			quant: r.Quant, weights: r.Weights, variant: r.Variant,
		}
	}
	m.shareRows = rows
	m.shares = m.ctrl.Sessions()
	m.sharePrivate = m.ctrl.Private()
	m.prices = m.ctrl.Prices()
	m.station = m.ctrl.Station()
	m.shareUp = m.ctrl.Upstream()
	m.shareKey = m.ctrl.UpstreamKey()
	m.shareSavedUp, m.shareSavedKey = m.ctrl.SavedUpstream()
	m.share, m.onAir = m.ctrl.Headline()
	if m.shareCursor >= len(m.shareRows) {
		m.shareCursor = 0
	}
}

// namelessVoiceBlocks is the shared nameless-voice guard for both on-air paths: a tts voice
// needs a DJ NAME + a picked VOICE before it can go on air, because the broker 400s a nameless
// voice offer ("voice name is empty after normalization"). When the OFF-air row at i is such a
// voice it sets the VOICE BOOTH prompt on m.status and returns true so the caller BLOCKS before
// firing a doomed register; stt + chat rows (and an already-live row going off) return false.
func (m *model) namelessVoiceBlocks(i int) bool {
	if i < 0 || i >= len(m.shareRows) {
		return false
	}
	row := m.shareRows[i]
	if row.modality != "tts" || m.shares[row.model] != nil {
		return false
	}
	if vc := m.ctrl.VoiceConfigFor(row.model); vc.Name == "" || vc.Voice == "" {
		m.status = stEmber.Render("♪ "+row.model+" needs a name + voice") +
			stDim.Render(" - press ") + stKey.Render("p") + stDim.Render(" to set it in the VOICE BOOTH before going on air")
		return true
	}
	return false
}

// rotateReturnMode is where a rotate should land the operator once they have saved the new
// code: back on the screen they started from. modeShare is not a candidate - a rotate can
// only be started from BASE STATION or the PRIVATE tab.
func (m model) rotateReturnMode() mode {
	if m.cfgModel != "" {
		return modeBandConfig
	}
	if m.tuneTab == tabPrivate {
		return modeBrowse
	}
	return modePrivate
}

// osc52 is the OSC 52 clipboard escape for s (base64, BEL-terminated). It is a
// non-rendering control sequence the terminal consumes to set the system clipboard, so it
// reaches the clipboard even over SSH where wl-copy/xclip aren't local - and it does not
// draw, so emitting it under the alt-screen renderer is safe.
func osc52(s string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\a"
}

// copiedToast is the shared, PROMINENT clipboard confirmation (opencode #927 style): a
// clear "✓ Copied to clipboard" the user can't miss, used by every copy path (ctrl+y,
// /copy, /copy all, freq code) so the feedback is consistent and obvious. It rides the
// transient status toast (auto-dismissed after toastFrames). detail names what was copied
// when it adds signal ("the transcript"), else "" for the bare confirmation. Bold ink so
// it stands out in the mono palette (the ✓ keeps the live accent).
func copiedToast(detail string) string {
	t := stLive.Render("✓ ") + stKey.Render("Copied to clipboard")
	if detail != "" {
		t += stDim.Render(" · " + detail)
	}
	return t
}

// agentTranscriptText is the AGENT transcript as clean, unstyled text (ANSI stripped), for
// ctrl+y / the agent's /copy - mirrors transcriptText for the channel.
func (m model) agentTranscriptText() string {
	lines := make([]string, 0, len(m.agentLines))
	for _, l := range m.agentLines {
		if strings.HasPrefix(l, agentAnswerMark) {
			lines = append(lines, strings.TrimPrefix(l, agentAnswerMark))
			continue
		}
		// A tool REFERENCE is resolved to the call it names, plus its output preview.
		// The copied transcript is what the operator saw with the box OPEN - a copy that
		// silently dropped the machinery would be a worse record than the screen.
		if i := toolRefIndex(l); i >= 0 {
			if i >= len(m.agentRuns) {
				continue
			}
			r := m.agentRuns[i]
			lines = append(lines, ansi.Strip(r.render()))
			for _, pl := range r.Preview {
				lines = append(lines, ansi.Strip(pl))
			}
			continue
		}
		// Un-mark the remaining tagged lines: these are C0 control bytes that ansi.Strip
		// preserves, so they would otherwise leak invisibly into the clipboard and across
		// the RC wire. The content is kept, only the tag byte is dropped.
		l = strings.TrimPrefix(strings.TrimPrefix(l, toolOutMark), askMark)
		lines = append(lines, ansi.Strip(l))
	}
	return strings.Join(lines, "\n")
}

// clipboardWrite returns a tea.Cmd that copies s to the clipboard BOTH ways - the OSC 52
// terminal escape (SSH-safe) and the local clipboard tool (copyToClipboard) - off the
// render path. The caller sets its own optimistic "copied" toast.
func clipboardWrite(s string) tea.Cmd {
	if s == "" {
		return nil
	}
	return func() tea.Msg {
		fmt.Print(osc52(s))
		copyToClipboard(s)
		return nil
	}
}

// transcriptText is the whole channel transcript as clean, unstyled text (ANSI stripped),
// for `/copy all`.
func (m model) transcriptText() string {
	lines := make([]string, 0, len(m.transcript))
	for _, l := range m.transcript {
		lines = append(lines, ansi.Strip(l))
	}
	return strings.Join(lines, "\n")
}

// copyToClipboard best-effort copies s to the OS clipboard via the platform tool
// (wl-copy / xclip / xsel on Linux, pbcopy on macOS, clip on Windows). Returns true
// on success. Never fatal - a missing tool just returns false and the caller falls
// back to "select it manually". No network, no persistence.
func copyToClipboard(s string) bool {
	if s == "" {
		return false
	}
	type tool struct {
		bin  string
		args []string
	}
	var tools []tool
	switch runtime.GOOS {
	case "darwin":
		tools = []tool{{"pbcopy", nil}}
	case "windows":
		tools = []tool{{"clip", nil}}
	default:
		tools = []tool{{"wl-copy", nil}, {"xclip", []string{"-selection", "clipboard"}}, {"xsel", []string{"--clipboard", "--input"}}}
	}
	for _, t := range tools {
		path, err := exec.LookPath(t.bin)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, t.args...)
		cmd.Stdin = strings.NewReader(s)
		if cmd.Run() == nil {
			return true
		}
	}
	return false
}

// refreshShareHeadline repoints m.share / m.onAir at any still-live session so the
// header ON-AIR badge and the onAirPanel reflect the current set after a toggle.
func (m *model) refreshShareHeadline() {
	m.share, m.onAir = m.ctrl.Headline()
}

// stopAllShares takes every model off air (used by /share off and a clean exit).
func (m *model) stopAllShares() {
	m.ctrl.StopAll()
	m.syncShareCache()
}

// requestQuit is the single quit entry point. While ON AIR (sharing as a provider)
// it does NOT quit immediately: it opens a confirm so the user knows quitting takes
// them off air. Off air, quit is immediate. Returns the (model, cmd) to apply.
func (m model) requestQuit() (tea.Model, tea.Cmd) {
	if m.onAirCount() > 0 {
		m.quitReturn = m.mode
		m.mode = modeQuitConfirm
		return m, nil
	}
	return m, tea.Quit
}

// quitNow goes cleanly off air (releasing every share) and quits. Used when the
// on-air quit-guard is confirmed.
func (m *model) quitNow() (tea.Model, tea.Cmd) {
	m.stopAllShares()
	return m, tea.Quit
}

// setupOptions are the guided-fallback choices: a tool (with a start one-liner) or
// the paste-a-URL path. Order is the on-screen order.
var setupOptions = []struct{ key, label, oneLiner string }{
	{"ollama", "Ollama", "ollama serve   then:  ollama run llama3.2   (→ :11434)"},
	{"lm-studio", "LM Studio", "LM Studio → Developer → Start Server   (→ :1234)"},
	{"unsloth", "Unsloth Studio", "Unsloth Studio → load a model → Settings → API → copy endpoint + key   (→ :8888)"},
	{"vllm", "vLLM", "vllm serve <model> --port 8000   (→ :8000)"},
	{"llamacpp", "llama.cpp", "llama-server -m <model>.gguf --port 8080   (→ :8080)"},
	{"other", "Other - paste a URL", ""},
}

// editShareField applies edit fn to the buffer of the focused editor field. Price
// fields (in/out) edit the price buffers; a window field edits its focused sub-field
// (Start/End time, or in/out price - cycled with left/right) so a window can set all
// of its values, not just Start.
func (m *model) editShareField(fn func(string) string) {
	switch m.edField {
	case edFieldIn:
		m.edPriceIn = fn(m.edPriceIn)
	case edFieldOut:
		m.edPriceOut = fn(m.edPriceOut)
	case edFieldAddWin:
		// nothing to type on the add-window affordance
	default:
		i := m.edField - edFieldFirstWin
		if i < 0 || i >= len(m.edWindows) {
			return
		}
		w := &m.edWindows[i]
		switch m.edWinSub {
		case winSubEnd:
			w.End = fn(w.End)
		case winSubIn:
			// Edit a persistent string buffer (so a typed "0." survives a keystroke that
			// would parse to 0), then reflect it into the window's float price.
			m.edWinBuf = fn(m.edWinBuf)
			w.In, _ = strconv.ParseFloat(strings.TrimSpace(m.edWinBuf), 64)
		case winSubOut:
			m.edWinBuf = fn(m.edWinBuf)
			w.Out, _ = strconv.ParseFloat(strings.TrimSpace(m.edWinBuf), 64)
		default: // winSubStart
			w.Start = fn(w.Start)
		}
	}
}

// syncWinBuf loads edWinBuf from the focused window's price sub-field (so editing
// continues from the current value), and clears it otherwise. Called whenever the
// focused field or sub-field changes.
func (m *model) syncWinBuf() {
	m.edWinBuf = ""
	if m.edField < edFieldFirstWin {
		return
	}
	i := m.edField - edFieldFirstWin
	if i < 0 || i >= len(m.edWindows) {
		return
	}
	switch m.edWinSub {
	case winSubIn:
		m.edWinBuf = trimZero(m.edWindows[i].In)
	case winSubOut:
		m.edWinBuf = trimZero(m.edWindows[i].Out)
	}
}

// Public price ceilings the editor enforces INLINE (at edit time, where the typo
// happens) so a bad price is caught at the cause, not only far away at broker
// register. These MIRROR the broker's hard public ceilings (cmd/rogerai-broker
// pricesafety.go: ROGERAI_MAX_PRICE_OUT default $100/1M, ROGERAI_MAX_PRICE_IN
// default $50/1M), which remain the marketplace invariant no matter which client
// registered the node. Kept as plain constants here to avoid the TUI importing the
// broker; the broker is still the source of truth that actually rejects.
const (
	editorMaxPriceOut = 100.0 // $/1M out public ceiling
	editorMaxPriceIn  = 50.0  // $/1M in public ceiling
)

// validHHMM reports whether s is a well-formed "HH:MM" 24h time (00:00..23:59). A
// malformed window time ("25:99", "6pm") silently NEVER matches at runtime, so we
// block it at save time instead of letting the operator publish a dead window.
func validHHMM(s string) bool {
	s = strings.TrimSpace(s)
	p := strings.SplitN(s, ":", 2)
	if len(p) != 2 {
		return false
	}
	h, e1 := strconv.Atoi(p[0])
	min, e2 := strconv.Atoi(p[1])
	if e1 != nil || e2 != nil {
		return false
	}
	return h >= 0 && h <= 23 && min >= 0 && min <= 59 && len(p[0]) > 0 && len(p[1]) > 0
}

// validateEditor checks the in-progress editor state and returns a human inline
// error (or "" when clean). It surfaces the failures the editor used to swallow:
// an unparseable base/window price (ParseFloat error kept a stale value), a
// malformed HH:MM window time (never matches), and a price over the public ceiling
// (previously only caught at broker register, far from the typo). On success it
// returns the parsed base in/out so commit doesn't re-parse.
func (m *model) validateEditor() (in, out float64, errMsg string) {
	in, err := strconv.ParseFloat(strings.TrimSpace(orZero(m.edPriceIn)), 64)
	if err != nil {
		return 0, 0, "input price must be a number (e.g. 0.5) - got " + strconv.Quote(m.edPriceIn)
	}
	out, err = strconv.ParseFloat(strings.TrimSpace(orZero(m.edPriceOut)), 64)
	if err != nil {
		return 0, 0, "output price must be a number (e.g. 0.7) - got " + strconv.Quote(m.edPriceOut)
	}
	if in < 0 || out < 0 {
		return 0, 0, "prices cannot be negative"
	}
	if out > editorMaxPriceOut {
		return 0, 0, fmt.Sprintf("output price $%.2f/1M is over the $%.0f/1M public ceiling - lower it, or share PRIVATE", out, editorMaxPriceOut)
	}
	if in > editorMaxPriceIn {
		return 0, 0, fmt.Sprintf("input price $%.2f/1M is over the $%.0f/1M public ceiling - lower it, or share PRIVATE", in, editorMaxPriceIn)
	}
	for i, w := range m.edWindows {
		if !validHHMM(w.Start) || !validHHMM(w.End) {
			return 0, 0, fmt.Sprintf("window %d time must be HH:MM (00:00-23:59) - got %q-%q", i+1, w.Start, w.End)
		}
		if w.Free {
			continue
		}
		if w.In < 0 || w.Out < 0 {
			return 0, 0, fmt.Sprintf("window %d prices cannot be negative", i+1)
		}
		if w.Out > editorMaxPriceOut {
			return 0, 0, fmt.Sprintf("window %d output $%.2f/1M is over the $%.0f/1M public ceiling", i+1, w.Out, editorMaxPriceOut)
		}
		if w.In > editorMaxPriceIn {
			return 0, 0, fmt.Sprintf("window %d input $%.2f/1M is over the $%.0f/1M public ceiling", i+1, w.In, editorMaxPriceIn)
		}
	}
	return in, out, ""
}

// orZero maps an empty edit buffer to "0" so a blank price field reads as free
// rather than a parse error.
func orZero(s string) string {
	if strings.TrimSpace(s) == "" {
		return "0"
	}
	return s
}

// commitShareEditor validates the edited price + schedule and, when clean, writes it
// into m.prices, persists it via the host SavePrice hook (if any), and re-prices a
// live share so an on-air model reflects the new base price immediately. It returns
// false (keeping the editor open with an inline error) when validation fails, so a
// malformed time / unparseable price / over-ceiling price never saves silently.
func (m *model) commitShareEditor() bool {
	in, out, errMsg := m.validateEditor()
	if errMsg != "" {
		m.edErr = errMsg
		return false
	}
	m.edErr = ""
	p := Pricing{In: in, Out: out, Windows: append([]SchedWindow(nil), m.edWindows...)}
	// Through the shared controller (it persists via Hooks.SavePrice), so a price the
	// operator sets in the TUI editor is the same one the web console shows.
	m.ctrl.SetPricing(m.edModel, p)
	m.syncShareCache()
	kind := "FREE"
	if in > 0 || out > 0 {
		kind = dollars(out) + "/1M out · " + dollars(in) + "/1M in"
	}
	win := ""
	if len(p.Windows) > 0 {
		win = stDim.Render(" · " + plural(len(p.Windows), "window"))
	}
	m.status = stLive.Render("saved ") + stKey.Render(m.edModel) + stDim.Render(" at ") + stEmber.Render(kind) + win
	// Fat-finger guard: mirror the CLI's softPriceWarn (>3x the live market median is
	// likely a typo) into the TUI commit path, so a $300 fumble warns instead of going
	// on air with only the hard $100 ceiling as a backstop. Best-effort + non-blocking:
	// no market signal = no warn, and it never fails the save (the price is already
	// persisted above). It augments the saved-status line rather than replacing it.
	if warn := m.softPriceWarn(out); warn != "" {
		m.status += "  " + stEmber.Render(warn)
	}
	return true
}

// softPriceWarn returns a non-blocking fat-finger warning when out is well above the
// live per-model market median (>3x) - mirroring cmd/rogerai's softPriceWarn so the
// TUI commit path gets the same typo guard the headless `share` path has. Returns ""
// when there is no signal (price 0, no market data, or within range). Best-effort: a
// market-fetch miss is silent.
func (m *model) softPriceWarn(out float64) string {
	if out <= 0 {
		return ""
	}
	med, ok := marketMedianOut(m.broker, m.edModel)
	if !ok || med <= 0 {
		return ""
	}
	if out > 3*med {
		return fmt.Sprintf("! %.2f $/1M out is %.1fx the market median (%.2f) - typo?", out, out/med, med)
	}
	return ""
}

// pricingFor returns the saved (edited) pricing for a model, falling back to the
// host's saved onboarding price for the default model, else free.
func (m model) pricingFor(model string) Pricing { return m.ctrl.PricingFor(model) }

// startLogin begins the GitHub device flow (called only from an explicit ENTER in
// the login panel). It prefers the begin/poll hook pair so the TUI renders its own
// clean panel + auto-opens the browser; it falls back to the single-shot Login hook
// (terminal-printed codes) when only that is wired.
func (m model) startLogin() (tea.Model, tea.Cmd) {
	broker, clientID := m.broker, m.hooks.GitHubID
	if m.hooks.LoginBegin != nil {
		begin := m.hooks.LoginBegin
		m.status = stDim.Render("starting GitHub device login…")
		return m, func() tea.Msg {
			d, err := begin(broker, clientID)
			if err != nil {
				return flowErrMsg("login failed: " + err.Error())
			}
			return loginStartedMsg(d)
		}
	}
	if m.hooks.Login != nil {
		// Legacy single-shot hook: it prints the code to the terminal and blocks.
		m.loginWaiting = true
		m.loginNote = "follow the code shown in your terminal"
		m.status = stDim.Render("opening GitHub device login…")
		login := m.hooks.Login
		return m, func() tea.Msg {
			l, err := login(broker, clientID)
			if err != nil {
				return flowErrMsg("login failed: " + err.Error())
			}
			return loginMsg(l)
		}
	}
	m.status = stDim.Render("login unavailable in this build - run `roger login`")
	return m, nil
}

// pollLoginCmd waits (off the event loop) for the user to authorize the started
// device flow, landing a loginMsg on success or a flowErrMsg on failure/timeout.
func (m model) pollLoginCmd() tea.Cmd {
	if m.hooks.LoginPoll == nil {
		return nil
	}
	broker, clientID := m.broker, m.hooks.GitHubID
	poll := m.hooks.LoginPoll
	dev := m.loginDevice
	return func() tea.Msg {
		l, err := poll(broker, clientID, dev)
		if err != nil {
			return flowErrMsg("login failed: " + err.Error())
		}
		return loginMsg(l)
	}
}

// startLogout clears the local GitHub binding (called only from an explicit y in
// the logout confirm panel).
func (m model) startLogout() (tea.Model, tea.Cmd) {
	if m.hooks.Logout == nil {
		m.status = stDim.Render("logout unavailable in this build - run `roger logout`")
		m.mode = m.loginReturn
		return m, nil
	}
	logout := m.hooks.Logout
	return m, func() tea.Msg {
		if err := logout(); err != nil {
			return flowErrMsg("logout failed: " + err.Error())
		}
		return logoutMsg{}
	}
}

// resolveFreq resolves a private-band frequency code OFF the event loop via the SAME
// constant-work client.ResolveBand the `roger use --freq` consumer path uses, then
// hands the result to the freqResolvedMsg handler. It is the single resolve entry
// point for BOTH the /freq command and the [~] PRIVATE FREQUENCY input, so they share
// one security model: every miss (wrong / empty / nonexistent / revoked / off-air)
// comes back as the broker's UNIFORM negative and is reported identically - no
// enumeration oracle. arg is passed through verbatim (the broker tolerates the
// cosmetic MHz part / spacing); an empty arg simply never matches.
func (m model) resolveFreq(arg string) (tea.Model, tea.Cmd) {
	broker := m.broker
	m.status = stDim.Render("scanning frequency…")
	return m, func() tea.Msg {
		offs, display, ok := client.ResolveBand(broker, arg, "")
		if !ok {
			return freqResolvedMsg{freq: arg, ok: false}
		}
		// Map client offers -> TUI offers (the browse list's shape). InFlight rides along
		// so a private band's signal meter is the same honest live-activity readout as a
		// public one.
		out := make([]offer, 0, len(offs))
		for _, o := range offs {
			// Carry every real field the broker's /bands/resolve emits (region, hw, ctx +
			// ctx_estimated, free-now, ttft, verified) so a PRIVATE band's row + [i] detail
			// read with the same real metrics as a public one - not a stripped-down subset.
			out = append(out, offer{
				NodeID: o.NodeID, Region: o.Region, HW: o.HW, Model: o.Model,
				PriceIn: o.PriceIn, PriceOut: o.PriceOut,
				Ctx: o.Ctx, CtxEstimated: o.CtxEstimated,
				Online: o.Online, Confidential: o.Confidential, FreeNow: o.FreeNow,
				TPS: o.TPS, TTFTMs: o.TTFTMs, Verified: o.Verified,
				Signal: o.Signal, InFlight: o.InFlight,
			})
		}
		return freqResolvedMsg{freq: arg, label: display, offers: out, ok: true}
	}
}

// freqLabelShort renders the cosmetic frequency for the header: the "<n>.<n> MHz"
// part of a display string (the part before the middot), or the whole thing if it
// has no separator. Falls back to "private" for an empty label.
func freqLabelShort(display string) string {
	if display == "" {
		return "private"
	}
	if i := strings.Index(display, "·"); i > 0 {
		return strings.TrimSpace(display[:i])
	}
	return strings.TrimSpace(display)
}

// openChannel binds the local proxy (once) and marks the band connected, sending
// the resolved spend limits to the relay so routing stays within them. Called
// only after the user accepts the cost confirmation.
// liveProxyOpts builds the LIVE ProxyOptions for the band `o` under the current spend limits /
// freq / confidential toggle, carrying the STABLE per-session bearer key and the tuned band's
// model (the proxy rewrites incoming models to it). Budget stays 0 (the interactive TUI is a
// single-user, hands-on flow; the guest-operator launch is where DefaultSessionBudget applies).
func (m model) liveProxyOpts(o offer, alert *alertBox) client.ProxyOptions {
	return client.ProxyOptions{
		Broker: m.broker, User: m.user, Model: o.Model, SessionKey: m.proxyKey,
		Confidential: m.confidentialOnly,
		MaxPriceIn:   m.q.limit.MaxIn, MaxPriceOut: m.q.limit.MaxOut, MinTPS: m.q.limit.MinTPS,
		Freq: m.tuneFreq, // private band tune-in: route via X-Roger-Freq (empty = open market)
		// The tuned row IS a quant, so the stations running a different one are named as
		// exclusions - otherwise the broker (which groups by model alone) could route this
		// turn to weights the operator did not choose. See quant_route.go.
		ExcludeNodes: m.routeExcludes(m.q.b),
		// ROGERAI_REASONING_RAW is a global session knob: honor it in the TUI booth too, not just
		// `roger use --raw`, so exporting it disables the reasoning->content fallback everywhere.
		ReasoningFallbackOff: client.RawReasoningEnv(),
		Alert:                func(s string) { alert.set(s) },
	}
}

// bindChannel is the endpoint-binding half of tuning in, factored out of openChannel so
// the SILENT auto-tune (autoTuneCmd) can open a channel WITHOUT the staged animation or
// any mode switch: bind (or re-point) the local proxy to station o, mark it connected,
// and record it as the sticky/recent band. It returns warm=true when the model was
// already tuned in this session (a reconnect skips the cold-tune animation) and any
// endpoint-bind error (openChannel bounces back to BROWSE; the auto-tune notes it once).
// It mutates the receiver in place - callers pass a &m.
func (m *model) bindChannel(o offer) (warm bool, err error) {
	if !m.proxyUp {
		// Auto-pick a free port instead of dead-ending if 4141 is taken (mirrors the CLI's
		// freePort): scan upward from the configured port so a busy port never bounces the
		// user back to browse with a bind error and no recovery.
		ln, lerr := listenFreePort(m.proxyAddr)
		if lerr != nil {
			return false, lerr
		}
		m.proxyAddr = ln.Addr().String() // remember the port we actually bound
		m.endpoint = "http://" + ln.Addr().String() + "/v1"
		m.proxyUp = true
		// Failover alerts from the relay land in a shared box the tick loop drains
		// onto the status line - bots keep hitting the same endpoint regardless.
		alert := m.alert
		// Mint the STABLE per-session bearer key once; the hardened proxy enforces it on every
		// route, and the LIVE options holder is re-pointed on each re-tune (below) without ever
		// rotating the key, so a running guest agent's generated config keeps working.
		m.proxyKey = client.NewSessionKey()
		m.proxyHolder = client.NewProxyOptionsHolder(m.liveProxyOpts(o, alert))
		go http.Serve(ln, client.ProxyHandlerLive(m.proxyHolder))
	}
	// LIVE re-point: every (re)tune updates the band model / caps / freq / confidential on the
	// SAME endpoint (ruling 9), keeping the session key + budget stable. A no-op-safe guard for
	// the tests that pre-set proxyUp without a holder.
	if m.proxyHolder != nil {
		m.proxyHolder.SetBand(m.liveProxyOpts(o, m.alert))
	}
	oc := o
	m.connected = &oc
	m.apikey = m.proxyKey
	if m.apikey == "" {
		m.apikey = "roger-local"
	}
	// Remember this station as the "sticky" recent band so it never vanishes from the
	// browse list if its node ages out of /discover while we are on the channel (the
	// founder's vanishing-band bug). mergeStickyBand re-includes it on every re-scan.
	sticky := o
	m.lastConnected = &sticky
	warm = m.recentBands[o.Model]
	if m.recentBands == nil {
		m.recentBands = map[string]bool{}
	}
	m.recentBands[o.Model] = true
	return warm, nil
}

func (m model) openChannel() (tea.Model, tea.Cmd) {
	q := m.q
	o := *q.b.cheapest
	// WARM RECONNECT: a band we have tuned in to before this session skips the staged
	// scan/lock/handshake animation and drops straight into the open channel - only a
	// FIRST (cold) tune-in plays the full sequence. The endpoint is already bound, so a
	// reconnect is genuinely instant.
	warm, err := m.bindChannel(o)
	if err != nil {
		m.mode = modeBrowse
		m.status = stEmber.Render("! endpoint bind failed: " + err.Error())
		return m, nil
	}
	if warm {
		m.mode = modeConnecting
		m.connectStage = connectStageDone
		return m.finishConnect()
	}
	// Rather than snapping straight to the channel, run the web's staged tune-in:
	//   ◉ scanning stations … ok
	//   ◉ locking strongest @x · NN t/s · 0.NN $/M … ok
	//   ◉ lineage handshake ◆ weights·shard·token … ok
	//   ◉ CHANNEL OPEN <model> via @x ◆ verified
	// then the clean BASE URL / API KEY / MODEL plate + "roger that." This replaces
	// the old blank wait with a legible "what's happening" sequence that matches the
	// site's tune-in animation. The endpoint is already bound (above); the channel
	// itself opens when the sequence completes (advanceConnect). Under quiet the
	// sequence is rendered fully resolved in a single frame.
	m.mode = modeConnecting
	m.connectStage = 0
	m.connectStartFrame = m.frame
	m.status = stRed.Render(glyphOnAir+" ") + stLive.Render("tuning in to ") + stSelText.Render(o.NodeID) + stDim.Render(" …")
	if quiet || m.compact {
		// No animation in a pipe / NO_COLOR, or in the windowshade compact mode (an
		// explicit reduced-motion): jump straight to the resolved channel, no staged
		// tune-in churn.
		return m.finishConnect()
	}
	return m, m.kickTick() // start the staged tune-in on a fresh chain
}

// advanceConnect steps the staged tune-in on each tick: every connectDwellFrames
// it reveals the next step; once every step is "ok" it drops into the live CHANNEL.
// Called from the tick handler while in modeConnecting.
func (m model) advanceConnect() (tea.Model, tea.Cmd) {
	// Called FROM the tick handler - a continuation of the current chain, so reschedule with
	// the same gen (never kick, or the connect sequence would churn the generation each frame).
	if m.mode != modeConnecting {
		return m, tick(m.tickGen)
	}
	elapsed := m.frame - m.connectStartFrame
	stage := elapsed / connectDwellFrames
	if stage > connectStageDone {
		stage = connectStageDone
	}
	m.connectStage = stage
	if stage >= connectStageDone {
		return m.finishConnect()
	}
	return m, tick(m.tickGen) // continuation of the tick chain (same gen)
}

// finishConnect drops the completed tune-in sequence into the live CHANNEL: it
// auto-switches to CHANNEL mode and compacts the header (the founder's
// "compact-on-connect"). The endpoint stays live regardless of mode.
func (m model) finishConnect() (tea.Model, tea.Cmd) {
	o := m.connected
	m.mode = modeChat
	m.connectStage = connectStageDone
	m.chatIn.Focus()
	if len(m.transcript) == 0 {
		m.transcript = append(m.transcript, stDim.Render("◂ ")+stLive.Render("roger that")+stDim.Render(" - channel open. type to talk, /? for commands · drag to copy any text."))
	}
	m.status = stGold.Render(channelGlyph(o)+" ") + stLive.Render("on channel ") + o.NodeID + stDim.Render(" - endpoint live · roger that")
	return m, textinput.Blink
}

// disconnect leaves the current CHANNEL: it drops the connected band and returns
// to the band browser. This is "leave this channel", a distinct action from
// quitting RogerAI (q from BROWSE / the on-air guard). The local proxy endpoint is
// left bound (cheap, and bots may still hold it) but the conversation is cleared
// so re-tuning starts fresh. A no-op when not connected.
func (m model) disconnect() (tea.Model, tea.Cmd) {
	if m.connected == nil {
		m.mode = modeBrowse
		return m, nil
	}
	was := m.connected.Model
	// The endpoint stays bound (bots may still hold it), but a disconnected proxy must REFUSE
	// to spend rather than serve the last band's stale routing (ruling 5). A re-tune re-points
	// it via openChannel/SetBand. Guard for tests that never bound a holder.
	if m.proxyHolder != nil {
		m.proxyHolder.Disconnect()
	}
	m.connected = nil
	// The direct-route binding dies WITH the channel. Left set, the next channel - a real
	// broker band - would send its turns to the previous band's local server under the new
	// band's name, which is the same class of bug bindAgentEndpoint's clear exists to stop.
	m.chatLocalChat, m.chatLocalKey = "", ""
	m.transcript = nil
	m.lastReply = "" // leaving the channel: don't let ctrl+y / /copy yank a prior channel's reply
	m.sessCost = 0
	m.sessTokensIn, m.sessTokensOut = 0, 0 // a new channel starts fresh: zero the running ↑↓ totals
	m.sysPrompt = ""
	m.minimized = false
	m.chatIn.Blur()
	m.chatIn.SetValue("")
	m.mode = modeBrowse
	m.status = stDim.Render("disconnected from ") + stKey.Render(was) + stDim.Render(" - back on the band · enter to tune in, q to quit RogerAI")
	return m, nil
}

// nudgeLimit steps an edit buffer by one unit of its field. price==true is the $/1M cap
// (cents, two decimals); false is min t/s (whole tokens).
//
// An unparsable or empty buffer starts from zero, so the first press of up on a blank
// field gives the smallest real value rather than doing nothing - which is what an
// operator reaching for the arrow keys is asking for.
func nudgeLimit(buf string, price, up bool) string {
	v, err := strconv.ParseFloat(strings.TrimSpace(buf), 64)
	if err != nil || v < 0 {
		v = 0
	}
	step := 1.0
	if price {
		step = 0.01
	}
	if up {
		v += step
	} else {
		v -= step
	}
	if v < 0 {
		v = 0
	}
	if price {
		// Round to the cent: repeated float steps drift ("0.30000000000000004"), and a
		// price field that shows that has lost the operator's trust over a rounding
		// artifact.
		return strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64)
	}
	return strconv.FormatFloat(math.Round(v), 'f', -1, 64)
}

// commitLimitField writes the current edit buffer into the focused field of the
// selected model's limit and persists it.
func (m *model) commitLimitField() {
	if m.limCursor >= len(m.limModels) {
		return
	}
	mdl := m.limModels[m.limCursor]
	lim := m.limits.resolve(mdl)
	v, _ := strconv.ParseFloat(strings.TrimSpace(m.editBuf), 64)
	if m.editField == 0 {
		lim.MaxOut = v
	} else {
		lim.MinTPS = v
	}
	m.limits.set(mdl, lim)
}

// nudge adjusts a numeric edit buffer by delta, clamped at 0, 2dp.
func nudge(buf string, delta float64) string {
	v, _ := strconv.ParseFloat(strings.TrimSpace(buf), 64)
	v += delta
	if v < 0 {
		v = 0
	}
	return fmt.Sprintf("%.2f", v)
}

// digitsDot returns a single digit or dot keypress (for the inline numeric edit),
// or "" for anything else.
func digitsDot(s string) string {
	if len(s) == 1 && (s[0] >= '0' && s[0] <= '9' || s[0] == '.') {
		return s
	}
	return ""
}

// trimZero renders a float for editing, blank for 0 (so "no cap" shows empty).
func trimZero(v float64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%g", v)
}

// narrowCols is the width below which the TUI reflows to a single, slimmer column
// (drops the band table's signal/flags columns, two-line footer).
const narrowCols = 64

// effWidth returns the width to DRAW at. Width 0 is the unsized initial frame
// (before the first WindowSizeMsg) - balloon to 88 so the first paint isn't a
// 1-column sliver. A genuinely small terminal draws at its REAL width (floored at
// 40), so the rules + footer match the viewport instead of overflowing at 88.
// (TUI-V2-CRITIQUE A.)
func (m model) effWidth() int {
	if m.width == 0 {
		return 88
	}
	if m.width < 40 {
		return 40
	}
	return m.width
}

// narrow reports whether to use the single-column reflow (real width is small).
// At exactly narrowCols (64) the wide band grid (~67 cols) would still overflow,
// so the boundary is inclusive: width <= 64 reflows.
func (m model) narrow() bool { return m.width != 0 && m.width <= narrowCols }

// cyclePreset steps the preset bank one button in dir (+1 next / -1 previous),
// wrapping around the ends, and fires the destination's jump - so left/right behave
// exactly like pressing that preset's number/letter. The "current" preset is the lit
// one in presetButtons() (exactly one is lit in every context cyclePreset is reached
// from: AGENT / TUNE IN / SHARE / CONFIG / HELP); LOGIN is never a resting mode, so a
// missing lit preset just falls back to the TUNE IN slot. The new key is dispatched
// back through presetForKey so the jump action is identical to the keypress.
func (m model) cyclePreset(dir int) (tea.Model, tea.Cmd, bool) {
	btns := m.presetButtons()
	cur := 1 // default to TUNE IN if nothing is lit (LOGIN has no resting mode)
	for i, b := range btns {
		if b.active {
			cur = i
			break
		}
	}
	n := len(btns)
	next := ((cur+dir)%n + n) % n
	return m.presetForKey(btns[next].key)
}

func pulseWith(frame int, eyeStyle lipgloss.Style) string {
	// arc widths 1..3..1, on a 9-cell stage; the eye sits dead center. Under quiet
	// (NO_COLOR / pipe) anim() freezes the frame so a pipe sees a stable beacon.
	//
	// Animation craft (cited for the local design record): motion is glyph
	// substitution in a fixed monospace grid - the arcs breathe the "broadcast"
	// ripple and the eye does a tiny phosphor-decay (full • on the bright phase,
	// a faint · on the decay phase), the CRT-afterglow trick. Same approach as
	// GitHub Copilot CLI's animated banner; static under NO_COLOR / non-TTY.
	// https://github.blog/engineering/from-pixels-to-characters-the-engineering-behind-github-copilot-clis-animated-ascii-banner/
	f := anim(frame)
	// A SLOW radio breath (founder: the beacon flickered too fast at the 160ms tick). Each
	// arc step holds ~0.8s (f/5) so the ripple reads as a calm swell, not a jitter; §7
	// "radios are alive but slow · don't animate faster than the eye reads."
	arcs := []int{1, 2, 3, 2}[f/5%4]
	if quiet {
		// Freeze to the canonical two-arc ((•)) brand beacon (brand-ascii.txt §2)
		// rather than the collapsed single arc a frozen frame happens to land on,
		// so a pipe / NO_COLOR sees the recognizable on-air motif.
		arcs = 2
	}
	open := strings.Repeat("(", arcs)
	clos := strings.Repeat(")", arcs)
	// phosphor decay: the eye glows full on the breath peak, fades to a faint dot
	// on the trough. Frozen to the bright eye under quiet (no churn in a pipe).
	// The eye stays a STEADY bright • - the slow arc breath is the only motion now (founder:
	// the beacon flickered too fast; the fast •/· phosphor blink was a chunk of that flicker).
	eye := eyeStyle.Render("•")
	body := stLive.Render(open) + " " + eye + " " + stLive.Render(clos)
	const stage = 9 // width of "((( • )))"
	return lipgloss.PlaceHorizontal(stage, lipgloss.Center, body)
}

// inShareSection reports whether the current screen is part of the SHARE (provide)
// section vs the TUNE IN (consume) section. The header names the section so it is
// never ambiguous that RogerAI does both.
func (m model) inShareSection() bool {
	switch m.mode {
	case modeShare, modeBandCard, modeShareEditor, modeShareSetup, modeShareVoice, modeVoicePicker:
		// The SHARE VOICE BOOTH + its picker are reached FROM the SHARE table (via `p` on a tts
		// row, same depth as the chat price editor), so they belong to the SHARE section.
		return true
	}
	return false
}

// sectionName is the two-mode top-level indicator: TUNE IN (consume: browse /
// connect / chat) vs SHARE (provide: your models / earnings / on air).
func (m model) sectionName() string {
	if m.inShareSection() {
		return "SHARE"
	}
	return "TUNE IN"
}

// sectionBadge renders the section indicator with the inactive section shown dim
// beside it, so the header reads "TUNE IN | share" (or "tune in | SHARE") and the
// `s` toggle is self-evident. SHARE is ember (provide = money), TUNE IN is volt
// (consume). At narrow widths it collapses to just the ACTIVE section so it never
// overflows the (already stacked) header line.
// sectionBadge is the SINGLE "where am I" indicator: it names the CURRENT section
// (TUNE IN vs SHARE) once, and is the one home for that status (audit #9). The
// preset bar above is the keyboard nav MENU (all sections + their keys); this badge
// is the "you are here" readout, so it no longer restates the whole TUNE IN│SHARE
// toggle pair - that lived in two places at once. `[s]` still teaches the switch key.
func (m model) sectionBadge() string {
	if m.inShareSection() {
		return stEmber.Bold(true).Render("SHARE") + stDim.Render(" [s]")
	}
	return stSelText.Render("TUNE IN")
}

// modeName returns the current mode's short label for the indicator, so the
// header badge names the actual screen (not a stale BROWSE) while you are in a
// confirm / over-limit / limits sub-screen.
func (m model) modeName() string {
	switch m.mode {
	case modeChat:
		return "CHANNEL"
	case modeConnectConfirm:
		return "CONFIRM"
	case modeConnecting:
		return "LOCKING"
	case modeOverLimit:
		return "OVER LIMIT"
	case modeLimits:
		return "SPEND LIMITS"
	case modeShare:
		return "SHARE"
	case modeShareEditor:
		return "PRICE + SCHEDULE"
	case modeShareSetup:
		return "SET UP A MODEL"
	default:
		return "BROWSE"
	}
}

// compactHeader is the windowshade-mode header: the whole brand lockup + preset bar
// collapses to ONE dense, animation-free strip carrying the live state + account +
// the `m:expand` hint, with a single hairline rule under it. No big banner, no arcs.
// The static `(•)` beacon stands in for the breathing pulse (frozen, per the
// reduced-motion contract). Width-safe: the strip is built as labeled segments and
// truncated to the real width before the rule, so it never overflows at 40 cols.
//
// Shapes (illustrative):
//
//	browsing: (•) ROGER·AI · TUNE IN · 3 on air · ◆ @bownux $42.17   m:expand
//	on air:   (•) ROGER·AI · ◆ on @nyx · gpt-oss-20b · $0.30/1M · $42.17   m:expand
//
// spectrumBlocks is the 8-level bar ramp (▁..█) used for the compact windowshade's per-band
// signal bars (see compactBandCell).
var spectrumBlocks = []rune("▁▂▃▄▅▆▇█")

// truncVisible cuts s to at most n display columns, preserving ANSI styling and never
// splitting an escape sequence. It is the compact strip's width clamp (ansi.Truncate
// is display-width aware and ANSI-safe, so a colored segment is cut cleanly rather
// than leaking a half escape).
// listenFreePort binds the first free TCP port at/above the port in addr ("host:port"),
// returning the open listener. It mirrors the CLI's freePort (cmd/rogerai/onboard.go):
// the configured port (4141) is tried first; if it is busy the scan walks upward so the
// TUI's tune-in never dead-ends on "address in use". It returns an error only when the
// whole window is busy (never falls back to a known-busy port). A malformed/portless addr
// degrades to letting the OS pick (":0").
func listenFreePort(addr string) (net.Listener, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return net.Listen("tcp", addr)
	}
	start, perr := strconv.Atoi(portStr)
	if perr != nil || start <= 0 {
		// No usable start port: let the OS assign one rather than fail.
		return net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	var lastErr error
	for p := start; p < start+200; p++ {
		ln, lerr := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if lerr == nil {
			return ln, nil
		}
		lastErr = lerr
	}
	return nil, fmt.Errorf("no free TCP port in %d-%d (close some listeners): %v", start, start+199, lastErr)
}

// header is the PERSISTENT status bar, always visible: the brand lockup with the
// live-red on-air eye + the current state. It COMPACTS to a thin one-line bar
// once a channel is open (so you never lose "what am I on + my balance"), and the
// [m] key toggles minimized vs expanded.
func (m model) header(w int) string {
	// The tube-glow (catalog #10): while a channel is open, a FAINT amber wash sits behind
	// the brand lockup - the set is warm. Painted into the brand/tag styles themselves (a
	// bg on a pre-rendered string would be reset mid-span), and ONLY where canTint allows
	// (ANSI256+, not quiet) and the palette is full - so mono / dumb terminals stay plain.
	brandSt, tagSt := stBrand, stTag
	if m.connected != nil && !paletteMono && canTint(lipgloss.DefaultRenderer().ColorProfile()) {
		brandSt = brandSt.Background(cTubeGlow)
		tagSt = tagSt.Background(cTubeGlow)
	}
	// Tube Ping's compact station bug is the persistent house mark. It is one row,
	// cell-stable, and keeps the same header budget as the former radio tower.
	tower := compactTubePingMark()
	name := brandSt.Render(" R O G E R") + tagSt.Render(" · A I")
	eye := onAirPulse(m.frame)
	rule := stHeadRule.Render(strings.Repeat("─", w))

	// COMPACT: once connected (or the user minimized), a single thin bar carrying
	// channel + model + out-price + balance + a tiny live signal.
	if m.connected != nil && (m.minimized || m.mode == modeChat) {
		o := m.connected
		// A DIRECT channel has no meter, so the price field carries the ROUTE instead.
		// "$0.00/1M" is a price quote, and quoting one for your own hardware asserts a rate
		// that was never charged - the same rail that keeps a local row unpriced in the
		// agent picker. This strip was missed when the CHANNEL header got its version, so
		// the founder's own private band still showed "$0.00/1M" over a free direct line.
		price := stEmber.Render(dollars(o.PriceOut)+"/1M") + priceTierSuffix(o.PriceTier, o.PriceOut)
		if m.chatLocalChat != "" {
			price = stRed.Render(glyphOnAir) + stDim.Render(" direct")
		}
		bar := stGold.Render(channelGlyph(o)) + " " + eye + stLive.Render(" on channel ") + stSelText.Render(o.NodeID) +
			stDim.Render(" · ") + stKey.Render(o.Model) +
			stDim.Render(" · ") + price +
			stDim.Render(" · ") + m.accountTag(true) +
			// CONNECTED header: the in-flight count is the live load on the open channel, so
			// the meter scans with real throughput while the channel is actively serving.
			"  " + m.bandSMeter(m.frame, o.Signal, o.TPS, true, o.InFlight, 0, false)
		return bar + "\n" + rule
	}

	// EXPANDED: brand lockup + eye on the left; the SECTION + screen badge on the
	// right. The section (TUNE IN vs SHARE) is the load-bearing "which half of the app
	// am I in" indicator, always shown so it is never ambiguous that you can both
	// consume and provide; the screen mode is the secondary detail. When /share is
	// live, a single ON AIR mark leads the badge (the one on-air indicator).
	left := tower + name + "  " + eye
	// Narrow: just the section + ON AIR (the screen "mode X" detail is dropped so the
	// stacked badge line fits the real width). Wide: section + screen mode.
	badge := m.sectionBadge()
	// The "mode X" screen detail only rides along on actual SUB-screens (confirm /
	// limits / provider table / ...). On the resting BROWSE screen it just restated the
	// section, so it is dropped there - the section badge alone is the "where am I".
	if !m.narrow() && m.modeName() != "BROWSE" {
		badge += stDim.Render("  ·  ") + stSelText.Render(m.modeName())
	}
	if m.onAir && m.share != nil {
		badge = m.headlineBadge() + stDim.Render("  ·  ") + badge
	}
	var top string
	if m.narrow() {
		// Single column: stack the badge under the lockup so neither overflows the
		// real (narrow) width.
		top = left + "\n" + badge
	} else {
		gap := w - lipgloss.Width(left) - lipgloss.Width(badge)
		if gap < 1 {
			gap = 1
		}
		top = left + strings.Repeat(" ", gap) + badge
	}

	// the state line: while browsing, "scanning the band · N on air · balance $X";
	// once connected AND back on the band (channel held, expanded, not minimized) it
	// names the channel. A connect-time sub-screen (confirm / the staged LOCKING
	// sequence) does NOT show this line - those views carry the channel context
	// themselves - so the header stays compact and width-safe through the tune-in.
	holdingChannel := m.connected != nil && (m.mode == modeBrowse || m.mode == modeCommand)
	var state string
	if holdingChannel {
		// Narrow: drop the "([m] compact)" hint so the line fits the real width.
		hint := stDim.Render("  ([m] compact)")
		if m.narrow() {
			hint = ""
		}
		state = stGold.Render("  "+channelGlyph(m.connected)+" ") + stLive.Render("on channel ") + stSelText.Render(m.connected.NodeID) +
			stDim.Render(" · ") + stKey.Render(m.connected.Model) +
			stDim.Render(" · ") + m.accountTag(true) + hint
	} else {
		// LLM (chat) stations on air — matches the LLM-only band list; voice stations are counted
		// in the Booth (the "also on air: N voices" footnote), not the top-level "N on air".
		summary := "scanning the band…"
		if m.scanned {
			summary = fmt.Sprintf("%d on air", m.llmStationsOnAir())
		}
		// The beacon in the lockup above already carries the (( • )) motif, so the
		// state line drops its literal ((•)) prefix - exactly one on-air mark in the
		// header (TUI-V2-CRITIQUE C). The account lockup carries login state + balance;
		// the balance only appears when logged in.
		state = stDim.Render("  ") + stDim.Render(summary) +
			stDim.Render(" · ") + m.accountTag(m.narrow())
	}
	return top + "\n" + state + "\n" + rule
}

// selectedBand resolves the cursor against the FILTERED + SORTED view (the same
// list the browse window renders + navigates), returning the band under the cursor.
// Every band action (connect, cursorOnConnected) goes through this so the cursor
// never desyncs from what the user sees when a filter / sort is applied. ok is
// false when the visible list is empty.
func (m model) selectedBand() (band, bool) {
	vis := m.visibleBands()
	if len(vis) == 0 {
		return band{}, false
	}
	i := m.cursor
	if i < 0 {
		i = 0
	}
	if i >= len(vis) {
		i = len(vis) - 1
	}
	return vis[i], true
}

// syncSelected records the band currently under the cursor (by name) so a later re-sort can
// re-anchor the cursor to it (the sticky-selection contract). Called right after a cursor move.
func (m *model) syncSelected() {
	vis := m.visibleBands()
	if m.cursor >= 0 && m.cursor < len(vis) {
		m.selectedModel = vis[m.cursor].model
	}
}

// scrollBrowse clamps the cursor and then scrolls the virtualized window so the
// cursor stays visible (used on every up/down nav). It persists browseTop so the
// remembered scroll position survives between frames; browseView recomputes the
// same window each render, so the view stays correct even without this, but
// storing it keeps the "remembered top" honest when the cursor jumps via a re-scan.
func (m *model) scrollBrowse() {
	m.clampBrowse()
	rows := m.browseRows()
	m.browseTop, _ = windowFor(m.browseTop, m.cursor, rows, len(m.visibleBands()))
}

// cursorOnConnected reports whether the browse cursor is on the band we are
// currently connected to (used so Enter toggles into the open channel rather than
// re-running the connect flow).
func (m model) cursorOnConnected() bool {
	cm := m.connectedModel()
	if cm == "" {
		return false
	}
	bd, ok := m.selectedBand()
	return ok && bd.model == cm
}

// sigFrame is the frame the view feeds every animation function (the signal-bar
// shimmer, the beacon pulse, Ping, the working spinner). In compact ("windowshade")
// mode it returns a fixed frozen frame so motion settles to a static snapshot - the
// app's own prefers-reduced-motion. Otherwise it is the live carrier beat (m.frame).
func (m model) sigFrame() int {
	if m.compact {
		return frozenFrame
	}
	return m.frame
}

// balDollars renders the wallet balance in dollars, or "-" before it loads.
func (m model) balDollars() string {
	if !m.haveBal {
		return "-"
	}
	return dollars(m.balance)
}

// loggedInState reports whether the user has a real account wallet: the broker's
// logged_in flag, or (before the first balance comes back) a locally-linked login.
func (m model) loggedInState() bool { return m.loggedIn || m.ghLogin != "" }

// accountTag renders the header/footer account lockup: logged in shows
// "✓ @login · $balance"; anonymous shows a calm, steady "not logged in · /login to
// use your wallet" prompt (no balance number is ever shown when anonymous). When
// `compact` is set it drops to a terser form for the thin bar / narrow widths.
func (m model) accountTag(compact bool) string {
	if !m.loggedInState() {
		if compact {
			return stKey.Render("/login")
		}
		return stDim.Render("not logged in · ") + stKey.Render("/login") + stDim.Render(" to use your wallet")
	}
	// Compact (thin bar / narrow footer): just the balance ($), the load-bearing bit.
	if compact {
		if !m.haveBal {
			return stGold.Render(glyphLineage)
		}
		return stEmber.Render(dollars(m.balance))
	}
	who := stGold.Render(glyphLineage) + stDim.Render(" logged in")
	if m.ghLogin != "" {
		who = stGold.Render(glyphLineage) + stDim.Render(" @") + stSelText.Render(m.ghLogin)
	}
	if !m.haveBal {
		return who
	}
	return who + stDim.Render(" · ") + stEmber.Render(dollars(m.balance))
}

// Band sort cycle - mirrors the /bands web page's sort <select> so the CLI and
// the web read the same dial (strongest signal / cheapest / fastest / most
// stations). sortSignal is the default (live-first, then strongest signal).
const (
	sortSignal   = iota // strongest signal (live first, then signal desc) - the default
	sortCheapest        // cheapest $/1M out (ascending)
	sortFastest         // fastest measured tok/s (descending)
	sortStations        // most stations on air (descending)
	sortCount           // number of sort modes (for the S cycle)
)

// sortLabel is the short word shown in the footer / filter line for a sort mode.
func sortLabel(mode int) string {
	switch mode {
	case sortCheapest:
		return "cheapest"
	case sortFastest:
		return "fastest"
	case sortStations:
		return "most-stations"
	default:
		return "strongest"
	}
}

// visibleBands is the DERIVED browse list: m.bands run through the active name
// filter + quick toggles (free-now / confidential / on-air) and the sort cycle.
// The cursor + the virtualized window both index THIS slice, never the raw
// m.bands, so filtering and scaling never desync from navigation. It mirrors the
// /bands web page's applyFilters (same predicates + sort keys) so CLI and web
// match. Cheap to recompute each frame (a filter + a stable sort over the grouped
// bands, not the raw offers); at thousands of bands this is the only full pass and
// it is O(n log n) once, while RENDER stays O(window).
func (m model) visibleBands() []band {
	q := strings.ToLower(strings.TrimSpace(m.filterApplied))
	out := make([]band, 0, len(m.bands))
	for _, b := range m.bands {
		// LLM PRIMACY (founder): the top-level list is the LLM (chat) bands ONLY. VOICE bands
		// (tts/stt) are NOT peers here — they live one drill-in deeper (THE DJ BOOTH), surfaced
		// only via the dim "also on air: N voices ▸ [v]" footnote. Excluding them keeps THE BAND
		// pure LLM at full weight, so voice can never sit inline-and-equal to the main event.
		if b.isVoice() {
			continue
		}
		// The name filter matches the QUANT too, so "q4_k_m" narrows the dial to those
		// rows and "qwen q4" is not needed as separate syntax. Splitting by quant made the
		// list longer; letting the filter already in everyone's fingers cut it by quant is
		// the cheapest way to make that cost back.
		if q != "" && !strings.Contains(strings.ToLower(b.model), q) &&
			!strings.Contains(strings.ToLower(b.quant), q) {
			continue
		}
		if m.fQuant != "" && !strings.EqualFold(b.quant, m.fQuant) {
			continue
		}
		if m.fFree && !b.free {
			continue
		}
		if m.fConf && b.lineage == 0 { // confidential == lineage in /discover
			continue
		}
		if m.fOn && !b.online {
			continue
		}
		// COMPACT windowshade: an at-a-glance deck of what's LIVE - show on-air bands only.
		// (Cursor/tune/render all read visibleBands, so navigation stays consistent; the total
		// band count still shows in the compact header.)
		if m.compact && !b.online {
			continue
		}
		out = append(out, b)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch m.sortMode {
		case sortCheapest:
			// offline bands (no live price) sort last; then cheapest out-price first.
			if a.online != b.online {
				return a.online
			}
			return a.minOut < b.minOut
		case sortFastest:
			return bandSignal(a) > bandSignal(b)
		case sortStations:
			return a.stations > b.stations
		default: // sortSignal: live first, then strongest signal
			if a.online != b.online {
				return a.online
			}
			return bandSignal(a) > bandSignal(b)
		}
	})
	return out
}

// filtersActive reports whether any name filter or quick toggle is narrowing the
// list (used to show the "filter: ... (n/total)" line + the clear hint).
func (m model) filtersActive() bool {
	return strings.TrimSpace(m.filterApplied) != "" || m.fFree || m.fConf || m.fOn || m.fQuant != ""
}

// cycleQuantFilter advances the quant filter: off -> each quant on air -> off. Cycling
// rather than prompting keeps it in the same family as F/C/O, which are all one keypress
// and no input box.
func (m model) cycleQuantFilter() model {
	qs := m.quantsOnAir()
	if len(qs) == 0 {
		m.status = stDim.Render("no band on the dial states a quant to filter by")
		return m
	}
	next := ""
	for i, q := range qs {
		if strings.EqualFold(q, m.fQuant) {
			if i+1 < len(qs) {
				next = qs[i+1]
			}
			break
		}
		if m.fQuant == "" {
			next = qs[0]
			break
		}
	}
	m.fQuant = next
	m.clampBrowse()
	if next == "" {
		m.status = stDim.Render("quant filter off - every band showing")
		return m
	}
	m.status = stDim.Render("showing only ") + stKey.Render(next) + stDim.Render(" bands · Q cycles")
	return m
}

// browseRows is how many band rows the virtualized window may draw at the current
// terminal height. It reserves the fixed chrome (preset bar, header, section tab +
// column header, prompt, footer, any endpoint/on-air panel) so the window scrolls
// instead of pushing the footer off-screen on a short terminal. Floored so a tiny
// terminal still shows a few rows + the position indicator.
func (m model) browseRows() int {
	h := m.height
	if h <= 0 {
		h = 30 // unsized first frame: a sensible default window
	}
	// Fixed chrome above/below the list: preset bar (~2) + header (~1) + section tab
	// (1) + column header (1) + filter line when open (1) + prompt (1) + footer
	// (2-3) + the two "more" hint lines + the position line. Compact trims the header.
	chrome := 12
	if m.compact {
		chrome = 9
	}
	if m.filterMode || m.filtersActive() {
		chrome++
	}
	if m.connected != nil {
		chrome += 4 // the endpoint panel rides under the list
	}
	if m.onAir && m.share != nil {
		chrome += 4 // the ON AIR panel too
	}
	rows := h - chrome
	if rows < 3 {
		rows = 3
	}
	return rows
}

// windowFor computes the virtualized slice [top, end) over a list of length n,
// given the cursor and how many rows fit. It scrolls the window so the cursor is
// always visible (clamped at both edges), starting from the caller's current top.
// Returns the new top and the exclusive end. Correct with the cursor at 0, at n-1,
// with a window larger than the list (whole list, no scroll), and with n == 0.
func windowFor(top, cursor, rows, n int) (int, int) {
	if rows < 1 {
		rows = 1
	}
	if n <= rows {
		return 0, n // everything fits: no scroll
	}
	if cursor < top {
		top = cursor // scrolled above the window: pull the top up to the cursor
	}
	if cursor >= top+rows {
		top = cursor - rows + 1 // below the window: pull the top down
	}
	if top > n-rows {
		top = n - rows // never leave a blank tail
	}
	if top < 0 {
		top = 0
	}
	return top, top + rows
}

// offerHasCapability reports whether the station DECLARED cap (case-insensitive). It
// is the ONLY source of a capability badge: an absent set claims nothing.
func offerHasCapability(o offer, cap string) bool {
	for _, c := range o.Capabilities {
		if strings.EqualFold(strings.TrimSpace(c), cap) {
			return true
		}
	}
	return false
}

// agentReadyTag is the agent-ready badge glyph for a band, or "" when it is not
// agent-ready: "⌁" VERIFIED (a station carries the broker-probed "tools" capability), "⌁~"
// INFERRED (window qualifies but tool-calling is unproven). The ONE place the ⌁ / inferred-~
// shape is composed, shared by the band table + the /model picker tail.
func agentReadyTag(bd band) string {
	ready, inferred := bandAgentReady(bd)
	if !ready {
		return ""
	}
	if inferred {
		return agentReadyGlyph() + "~"
	}
	return agentReadyGlyph()
}

// plainBandBadge is bandBadge without color, for the reverse-video selected row
// (one accent style governs the whole row; an embedded fg color reads as noise).
// connected leads the cell with the "◉ connected" marker so the open channel's
// band is unmistakable even on the cursor row / under NO_COLOR.
func plainBandBadge(bd band, limits *LimitStore, connected bool) string {
	parts := []string{}
	if connected {
		parts = append(parts, glyphOnAir+" connected")
	}
	if bd.verified {
		parts = append(parts, glyphLineage+" verified")
	}
	if bd.lineage > 0 {
		parts = append(parts, fmt.Sprintf("◆ %d", bd.lineage))
	}
	if tag := agentReadyTag(bd); tag != "" {
		parts = append(parts, tag)
	}
	if bd.vision {
		parts = append(parts, visionGlyph())
	}
	if bd.free {
		parts = append(parts, "FREE")
	}
	if bandOverLimit(bd, limits) {
		parts = append(parts, "above limit")
	}
	if len(parts) == 0 {
		return "·"
	}
	return strings.Join(parts, " ")
}

// mergeStickyBand keeps a band you recently TUNED IN to in the browse list even
// when the broker's latest /discover no longer carries it (the founder's
// vanishing-band bug: a node you were on ages out of /discover at ~35s, so the
// next periodic re-scan dropped it from m.bands and r could not bring it back).
// If m.lastConnected is set and the fresh band list already contains that model,
// the live offer wins and the sticky placeholder is cleared (it is on air again).
// Otherwise we append a synthetic OFFLINE band carrying the remembered station, so
// the row stays present, marked offline/available, and is still selectable to
// re-tune. nil-safe: with no sticky band the input list passes through unchanged.
func (m *model) mergeStickyBand(bands []band) []band {
	if m.lastConnected == nil {
		return bands
	}
	want := m.lastConnected.Model
	for _, b := range bands {
		if b.model == want {
			// The band is back in /discover (on air or listed) - the live offer is the
			// source of truth now; drop the stale sticky placeholder.
			m.lastConnected = nil
			return bands
		}
	}
	// Not in the fresh scan: keep it as an offline, tunable station so it never
	// vanishes. minOut/cheapest from the remembered offer let Enter re-tune it.
	o := *m.lastConnected
	sticky := band{
		model:    o.Model,
		stations: 0,
		minIn:    o.PriceIn,
		minOut:   o.PriceOut,
		maxOut:   o.PriceOut,
		cheapest: nil, // offline: no on-air station to lock right now
		online:   false,
		free:     o.FreeNow || (o.PriceOut == 0 && o.PriceIn == 0),
		all:      []offer{o},
	}
	if o.Confidential {
		sticky.lineage = 1
	}
	return append(bands, sticky)
}

// pickAutoBand chooses the band the AGENT [0] DESK auto-tunes onto when it lands with
// nothing tuned in. PURE + deterministic. Rulings:
//
//   - R1 (never auto-spend): a FREE band is the only kind ever SILENTLY connectable, and
//     the CALLER (runAutoTune) - never this function - decides that a PAID pick lands on
//     the honest paid state instead of spending. A PAID band is offered here ONLY when
//     loggedIn (a logged-out user cannot pay), so a logged-out user with no free band
//     gets nil -> the honest empty state, never a named paid band it cannot reach.
//   - R6 (agent-ready first): a coding handoff must not dead-end, so agent-ready bands
//     (window unknown or >=16k) sort before KNOWN-small ones. Within a partition FREE
//     precedes paid; free bands sort by signal desc (the iOS order), paid by cheapest
//     out-price. Model name is the final deterministic tie-break.
//
// Only ONLINE, non-voice (a brain is a chat band) candidates are considered.
func pickAutoBand(bands []band, loggedIn bool) *band {
	var cands []band
	for _, b := range bands {
		if !b.online || b.isVoice() {
			continue
		}
		if !b.free && !loggedIn {
			continue // a paid band needs a wallet
		}
		cands = append(cands, b)
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		bi, bj := cands[i], cands[j]
		// FREE is the top-level key: only a free band is ever SILENTLY connected (R1), so a
		// never-connectable paid band must NEVER outrank a connectable free one - even a
		// known-small free one (else auto-tune would report "no free band" while a $0 band
		// is on air). The agent-ready partition (R6) orders only WITHIN free (and within paid).
		if bi.free != bj.free {
			return bi.free
		}
		if si, sj := bandKnownSmall(bi), bandKnownSmall(bj); si != sj {
			return !si // agent-ready (not known-small) first
		}
		if bi.free {
			if gi, gj := bandSignal(bi), bandSignal(bj); gi != gj {
				return gi > gj // free: strongest signal first
			}
		} else if bi.minOut != bj.minOut {
			return bi.minOut < bj.minOut // paid: cheapest first
		}
		return bi.model < bj.model
	})
	top := cands[0]
	return &top
}

// bestFreeStation returns the highest-signal ONLINE genuinely-free station in b (FreeNow, or
// zero-priced: PriceIn==0 && PriceOut==0), or nil when the band carries none. It is the ONLY
// station kind runAutoTune / the operator handoff may SILENTLY bind (R1: a $0 spend, no
// confirm). It is DISTINCT from b.cheapest, which is the min-PRICE station across ALL of the
// band's stations and can be a PAID station even in a band flagged free - a FreeNow promo
// station carrying a nonzero nominal price sitting beside a cheaper paid one makes b.free true
// while b.cheapest points at the paid station. Binding cheapest there would silently spend on
// a paid station labelled "(free)" (the R1 money-safety trap); binding bestFreeStation cannot.
// Deterministic: strongest signal wins, NodeID breaks a tie.
func bestFreeStation(b band) *offer {
	var best *offer
	for i := range b.all {
		o := &b.all[i]
		if !o.Online {
			continue
		}
		if !(o.FreeNow || (o.PriceIn == 0 && o.PriceOut == 0)) {
			continue
		}
		if best == nil || o.Signal > best.Signal || (o.Signal == best.Signal && o.NodeID < best.NodeID) {
			best = o
		}
	}
	return best
}

// noteOnce appends a transcript block UNLESS it already IS the tail - the guard that
// stops the "no station on air / no free band / no model tuned in" honest states from
// stacking on every turn / re-entry (founder live-test pain). Dedup is per-BLOCK so a
// two-line honest state (the ✕ + its hint) collapses as a unit.
func (m *model) noteOnce(lines ...string) {
	if n := len(m.agentLines); n >= len(lines) && len(lines) > 0 {
		same := true
		for i, ln := range lines {
			if m.agentLines[n-len(lines)+i] != ln {
				same = false
				break
			}
		}
		if same {
			return
		}
	}
	m.agentLines = append(m.agentLines, lines...)
}

// drainPendingPrompts starts the first prompt parked while no model was tuned (now that
// a free band is bound) and moves any others to the normal busy queue.
func (m *model) drainPendingPrompts() tea.Cmd {
	if len(m.agentPending) == 0 {
		return nil
	}
	q := m.agentPending[0]
	rest := m.agentPending[1:]
	m.agentPending = nil
	// The requeued prompts were ALREADY echoed at park time; mark them so submitAgentPrompt
	// does not re-echo the "▸ …" ask line when the busy queue drains (audit finding).
	for i := range rest {
		rest[i].echoed = true
	}
	m.agentQueued = append(m.agentQueued, rest...)
	// The prompt was already echoed at park time, so start the turn WITHOUT re-echoing.
	nm, cmd := m.startParkedTurn(q)
	*m = nm
	return cmd
}

// flushPendingPrompts drops prompts parked while no model was tuned, when the auto-tune
// found no free band to land on. It drops them SILENTLY: runAutoTune has already noted
// the ONE honest state (empty / paid) right after the echoed ask, so a second "no station
// on air" failureHint would be exactly the per-turn spam this redesign kills.
func (m *model) flushPendingPrompts() {
	m.agentPending = nil
}

// clearFindingBeat splices out the single "finding a free band…" beat line the fresh
// AGENT landing shows while an auto-tune is in flight, so the outcome replaces it in
// place. It removes ONLY that one line (index autoTuneBeatLen), never the tail: a prompt
// the user typed + parked while the auto-tune was in flight sits AFTER the beat, and must
// survive to be drained (the review's echo-eating bug). A content guard keeps it from
// deleting an unrelated line if the transcript shifted underneath it.
func (m *model) clearFindingBeat() {
	i := m.autoTuneBeatLen
	m.autoTuneBeatLen = 0
	if i <= 0 || i >= len(m.agentLines) {
		return
	}
	if !strings.Contains(m.agentLines[i], "finding a free band") {
		return
	}
	m.agentLines = append(m.agentLines[:i], m.agentLines[i+1:]...)
	if m.agentLandingLines > len(m.agentLines) {
		m.agentLandingLines = len(m.agentLines)
	}
}

// money renders a price as a fixed 2-dp string (the per-1M band prices).
func money(v float64) string { return fmt.Sprintf("%.2f", v) }

// rangeStr renders a band's cross-station out-price spread as "min ~ max", or a
// single point when there is only one station (never fake a spread, per design).
func rangeStr(b band) string {
	if !b.online {
		return "-"
	}
	if b.stations <= 1 || b.minOut == b.maxOut {
		return money(b.minOut)
	}
	return money(b.minOut) + " ~ " + money(b.maxOut)
}

// hwClassLabel maps a node's advertised hardware to the coarse, BUCKETED class label
// (multi-gpu / single-gpu / apple / cpu) shown in the expanded station view. Nodes now
// advertise the bucketed class directly; a legacy raw string is still mapped to a broad
// family. Empty/unknown -> "" (no chip), matching the web's hwClass.
func hwClassLabel(hw string) string {
	h := strings.ToLower(strings.TrimSpace(hw))
	switch h {
	case "", "unknown":
		return ""
	case "multi-gpu", "single-gpu", "apple", "cpu":
		return h
	}
	switch {
	case strings.Contains(h, "apple") || strings.Contains(h, "mac"):
		return "apple"
	case strings.Contains(h, "rtx") || strings.Contains(h, "geforce") ||
		strings.Contains(h, "radeon") || strings.Contains(h, "nvidia") || strings.Contains(h, "gpu") ||
		strings.Contains(h, "cuda") || strings.Contains(h, "rocm") || strings.Contains(h, "instinct"):
		return "single-gpu"
	case strings.Contains(h, "ryzen") || strings.Contains(h, "epyc") || strings.Contains(h, "xeon") ||
		strings.Contains(h, "threadripper") || strings.Contains(h, "intel") || strings.Contains(h, "amd") ||
		strings.Contains(h, "cpu"):
		return "cpu"
	}
	return ""
}

// coarseRegion buckets a free-text region to a macro-region label, or "" when it is
// missing/unmatched - mirroring the web's coarseRegion so the TUI and web agree. An
// empty result renders as a dim "-" (not provided), never a literal "??".
func coarseRegion(region string) string {
	r := strings.ToLower(strings.TrimSpace(region))
	if r == "" {
		return ""
	}
	type rule struct {
		subs  []string
		label string
	}
	rules := []rule{
		{[]string{"us-w", "usw", "west", "sf", "sjc", "lax", "sea", "pdx", "california", "oregon"}, "US-W"},
		{[]string{"us-e", "use", "east", "nyc", "iad", "atl", "mia", "virginia"}, "US-E"},
		{[]string{"us-c", "central", "chi", "dfw", "texas"}, "US-C"},
		{[]string{"usa", "united states", "america"}, "US"},
		{[]string{"uk", "london", "lon", "britain", "england"}, "UK"},
		{[]string{"germany", "deutsch", "fra", "frankfurt", "berlin", "munich"}, "DE"},
		{[]string{"netherlands", "amsterdam", "ams"}, "NL"},
		{[]string{"france", "paris"}, "FR"},
		{[]string{"europe", "euro"}, "EU"},
		{[]string{"canada", "toronto", "montreal", "yyz"}, "CA"},
		{[]string{"australia", "sydney", "syd", "melbourne"}, "AU"},
		{[]string{"japan", "tokyo", "nrt", "osaka"}, "JP"},
		{[]string{"singapore", "sin"}, "SG"},
		{[]string{"india", "mumbai", "bom", "bangalore"}, "IN"},
		{[]string{"brazil", "sao", "gru"}, "BR"},
		{[]string{"korea", "seoul", "icn"}, "KR"},
	}
	for _, ru := range rules {
		for _, s := range ru.subs {
			if strings.Contains(r, s) {
				return ru.label
			}
		}
	}
	// bare two-letter codes ("us","eu","de",...) and "home" default
	switch r {
	case "us":
		return "US"
	case "eu":
		return "EU"
	case "de":
		return "DE"
	case "nl":
		return "NL"
	case "fr":
		return "FR"
	case "ca":
		return "CA"
	case "au":
		return "AU"
	case "jp":
		return "JP"
	case "sg":
		return "SG"
	case "in":
		return "IN"
	case "br":
		return "BR"
	case "kr":
		return "KR"
	}
	if strings.Contains(r, "asia") {
		return "ASIA"
	}
	return ""
}

// revealBlock dims the freshly-appended transcript block (entries [from:]) for the first
// msgRevealFrames frames of its age, so an incoming reply gently settles in instead of snapping.
// It re-styles those entries to dim (keeping their text via ansi.Strip), and returns the lines
// UNCHANGED once settled (age>=msgRevealFrames), under reduced motion (reduce), for a negative
// age, or an out-of-range from. Pure in (lines, from, age, reduce).
func revealBlock(lines []string, from, age int, reduce bool) []string {
	if reduce || age < 0 || age >= msgRevealFrames || from < 0 || from >= len(lines) {
		return lines
	}
	out := make([]string, len(lines))
	copy(out, lines)
	for i := from; i < len(out); i++ {
		out[i] = stDim.Render(ansi.Strip(out[i]))
	}
	return out
}

// truncate here. An empty slice yields "" (zero rows).
// transcriptContent renders the transcript entries into the viewport body, each line
// under the shared 2-space indent. Long lines are WRAPPED to the width (reflowing on
// resize) instead of being clipped at the right edge by the viewport - the founder's
// bug where a streamed reply past the margin ("…a layered \"cak") was lost. ansi.Wrap
// is ANSI- and wide-char-aware, preserves the model's own newlines, and hard-breaks an
// over-long unbroken token (a URL), so no reply text is ever dropped.
func transcriptContent(entries []string, width int) string {
	var b strings.Builder
	first := true
	wrapAt := width - 2 // the "  " indent below eats two columns
	for _, e := range entries {
		if wrapAt > 0 {
			e = ansi.Wrap(e, wrapAt, "")
		}
		for _, ln := range strings.Split(e, "\n") {
			if !first {
				b.WriteByte('\n')
			}
			first = false
			b.WriteString("  " + ln)
		}
	}
	return b.String()
}

// lineRows is the number of physical lines in viewport content ("" = 0 rows).
func lineRows(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

// agentCornerRows mirrors agentView: the reactive corner-Ping region only shows when a
// model is active, and its height drives the transcript budget.
func (m model) agentCornerRows() int {
	mdl := ""
	if m.agent != nil {
		mdl = m.agent.model
	}
	if mdl == "" {
		return 0
	}
	return len(agentCornerPing(m.agentTurnState, anim(m.frame), m.narrow(), m.agentMascotCompact(), m.agentBusy))
}

// agentMascotCompact protects short terminals while allowing the roomier five-row
// Tube Ping to breathe in ordinary AGENT layouts.
func (m model) agentMascotCompact() bool {
	return m.compact || (m.height > 0 && m.height < 20)
}

// agentWorkingRows is the READOUT SLOT under the composer: one status line while a
// turn runs, plus a second for the Spectrum carrier where there is room for it (the
// same gate agentWorkingLine uses for its sweep).
//
// It is reserved WHETHER OR NOT a turn is running, and that is the point. Sizing it
// to the live state instead made the composer hop up a row the instant a turn began
// and back down when it ended - the one element on the screen that must never move
// was the one that moved on every single turn. Holding the slot open costs one or
// two quiet rows above TOOLS: and buys an input that sits in exactly one place for
// the whole session (founder 2026-08-20).
//
// Deterministic in width and mode alone, so the pin above it is stable too.
func (m model) agentWorkingRows() int {
	if !m.compact && !quiet && !m.narrow() {
		return 2 // status line + carrier sweep
	}
	return 1
}

// agentTranscriptRows is chatTranscriptRows for the AGENT view (minus the corner Ping).
func (m model) agentTranscriptRows(cornerRows, promptRows int) int {
	// Expanded AGENT lives inside the global preset/header/footer chrome (7 rows)
	// and owns its deck, desk strip, seam, prompt, and mode row (6 more). Keeping
	// those 13 rows out of the viewport budget pins the top identity in place.
	budgetMax := m.height - 13 - cornerRows
	if m.compact {
		budgetMax = m.height - 6 - cornerRows
	}
	// The original budget reserved one prompt row. Wrapped/multiline input gives
	// back its extra rows, and a non-empty transcript reserves one separator seam.
	budgetMax -= max(0, promptRows-1)
	budgetMax -= m.agentWorkingRows()
	if len(m.agentLines) > 0 {
		budgetMax--
	}
	minRows := 3
	if m.height > 0 {
		minRows = 1
	}
	if budgetMax < minRows {
		budgetMax = minRows
	}
	return budgetMax
}

// refreshScroll keeps both transcript viewports sized to the window and fed from the
// current transcript slices, auto-sticking to the bottom ONLY when the user was already
// at the bottom (so a scroll-up holds while new output streams in below). Called after
// every Update via the Update wrapper, so any handler that appends to a transcript (a
// reply, an agent event, a system line) gets the right scroll behavior for free.
func (m model) refreshScroll() model {
	w := m.effWidth()

	chatBottom := m.chatVP.AtBottom()
	// Settle a freshly-arrived reply block in (dim -> full ink) over a couple of ticks; frozen
	// under quiet/compact (reduced motion). msgInFrame==0 means nothing pending.
	chatLines := m.transcript
	if m.msgInFrame > 0 {
		chatLines = revealBlock(m.transcript, m.msgInFrom, m.frame-m.msgInFrame, quiet || m.compact)
	}
	chatContent := transcriptContent(chatLines, w)
	m.chatVP.Width = w
	m.chatVP.Height = clampRows(lineRows(chatContent), m.chatTranscriptRows())
	m.chatVP.SetContent(chatContent)
	if chatBottom {
		m.chatVP.GotoBottom()
	}

	agentBottom := m.agentVP.AtBottom()
	agentContent := transcriptContent(m.displayAgentLines(w), w)
	m.agentVP.Width = w
	m.agentVP.Height = clampRows(lineRows(agentContent), m.agentTranscriptRows(m.agentCornerRows(), m.agentPromptRowCount(w)))
	m.agentVP.SetContent(agentContent)
	if agentBottom {
		m.agentVP.GotoBottom()
	}

	return m
}

func composerVisualRows(value string, contentWidth int) int {
	if value == "" {
		return 1
	}
	rows := 0
	for _, logical := range strings.Split(value, "\n") {
		rows += max(1, lineRows(ansi.Wrap(logical, contentWidth, "")))
	}
	return rows
}

func composerCursorVisualRow(input textarea.Model, contentWidth int) int {
	logical := strings.Split(input.Value(), "\n")
	row := 0
	for i := 0; i < input.Line() && i < len(logical); i++ {
		row += max(1, lineRows(ansi.Wrap(logical[i], contentWidth, "")))
	}
	return row + input.LineInfo().RowOffset
}

// emptyBandCTA is the single static actionable line for the quiet empty band (audit
// #10): one clear "what do I do next" instead of a rotating motivational carousel
// (which read as "loading forever" to a newcomer). The live signal-bar shimmer beside
// it carries the "live, not frozen" cue; this line carries the action. Stable across
// frames so it never reads as a spinner of its own. The narrow form trims the prose so
// the (non-clamped) line never overflows a slim ~40-col terminal.
func emptyBandCTA(narrow bool) string {
	if narrow {
		return stDim.Render("No stations on air · ") + stKey.Render("[2]") + stDim.Render(" share")
	}
	return stDim.Render("No stations on air - ") + stKey.Render("[2]") + stDim.Render(" to share, ") + stKey.Render("[1]") + stDim.Render(" to tune in")
}

// workingPhrases is the rotating radio voice of the working spinner - one coherent
// DJ persona (the same one the future dj.md will use). While a request is in flight
// the beacon pulses and the phrase advances, so the wait reads as a live broadcast
// being tuned, not a frozen hang.
var workingPhrases = []string{
	"Tuning in…",
	"Modulating…",
	"Carrier locked…",
	"Working the dial…",
	"Receiving…",
	"Squelch open…",
	"Riding the airwaves…",
	"Reading you five by five…",
	"Chasing the signal…",
	"Dialing it in…",
	"Boosting the gain…",
	"Sweeping the band…",
	"Clearing the static…",
	"Patching you through…",
	"Warming the tubes…",
	"Cueing the next track…",
	"Holding the frequency…",
	"Coming in clear…",
}

// phraseCadence is how many ticks a working phrase holds. At the 160ms tick that is ~5.4s
// per phrase - slow enough to READ a full sentence before it changes (founder: the words
// were changing too fast to read). Deliberately slower than the corner-Ping cadence.
const phraseCadence = 34

// workingPhrase returns the radio phrase for a frame: it advances every phraseCadence ticks
// so the words READ at a calm, deliberate pace, never a flicker. Under quiet (NO_COLOR /
// non-TTY) it freezes to the first phrase so a pipe sees a stable line. (And while idle the
// frame is frozen entirely, so the working line only advances mid-turn.)
func workingPhrase(frame int) string {
	if quiet {
		return workingPhrases[0]
	}
	return workingPhrases[(frame/phraseCadence)%len(workingPhrases)]
}

// workingSpinner is our answer to Claude Code's ✻ working spinner, in RogerAI's own
// radio idiom: the animated on-air beacon ((•)) (pulsing carrier rings, via
// pulseWith) next to a rotating radio phrase. It is the one coherent "we're on it"
// motif for any in-flight request/turn. quiet freezes both the rings and the phrase.
func workingSpinner(frame int) string {
	return pulseWith(frame, stPingEye) + " " + stLive.Render(workingPhrase(frame))
}

// staticSpinner is the compact ("windowshade") working spinner: a frozen (•) glyph
// (no pulsing carrier rings) next to a fixed phrase, so an in-flight request reads as
// "we're on it" without any motion - the reduced-motion form of workingSpinner.
func staticSpinner() string {
	return stPingEye.Render(beaconDot()) + " " + stLive.Render(workingPhrases[0])
}

// onAirPanel renders the live ON AIR provider instrument: model, price,
// connections served, and running earnings in $, with an off-air hint.
// linkBadge renders the TRUTHFUL provider status from the session's broker link
// state: a real "ON AIR" ONLY while the broker is accepting our heartbeats (200),
// "RECONNECTING" while heartbeats are failing/rejected/unreachable (we are NOT
// routable, so we must not claim on-air), and "connecting" in the brief opening
// window before the first heartbeat is acknowledged. NO_COLOR / narrow safe: the
// plain words carry the meaning, the glyph + color are decoration.
func linkBadge(s *agent.Session) string {
	switch s.Link() {
	case agent.LinkOnAir:
		return stRed.Render(glyphOnAir + " ON AIR")
	case agent.LinkReconnecting:
		return stEmber.Render(glyphOffAir+" RECONNECTING") + stDim.Render(" - broker not acknowledging")
	default: // LinkConnecting
		return stDim.Render(glyphOffAir + " connecting…")
	}
}

// headlineBadge is the terse header on-air indicator for the headline share session.
// Truthful: it reads the broker LINK state, so the header shows "ON AIR" only while
// the broker is accepting heartbeats, and "RECONNECTING" (no suffix, to fit the
// narrow strip) while it is not. NO_COLOR / narrow safe (the word carries it).
func (m model) headlineBadge() string {
	if m.share == nil {
		return stRed.Render(glyphOnAir + " ON AIR")
	}
	switch m.share.Link() {
	case agent.LinkOnAir:
		return stRed.Render(glyphOnAir + " ON AIR")
	case agent.LinkReconnecting:
		return stEmber.Render(glyphOffAir + " RECONNECTING")
	default:
		return stDim.Render(glyphOffAir + " connecting…")
	}
}

// liveShares returns the on-air sessions sorted stably by model id, so the ON AIR
// panel renders the same band order every frame (Go map iteration is randomized).
func (m model) liveShares() []*agent.Session {
	out := make([]*agent.Session, 0, len(m.shares))
	for _, s := range m.shares {
		if s != nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model() < out[j].Model() })
	return out
}

// elide shortens s to at most n runes, using an ellipsis when it must cut. Used to
// keep long node ids on a single compact row in the ON AIR panel.
func elide(s string, n int) string {
	if n < 1 {
		n = 1
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// defaultShareMaxOnAir mirrors the controller's default soft on-air cap (the single
// source of truth lives in package node).
const defaultShareMaxOnAir = node.DefaultMaxOnAir

// maxOnAir is the effective SOFT local cap on simultaneously-on-air bands: the
// host-supplied share.max_on_air when positive, else the controller's default.
func (m model) maxOnAir() int { return m.ctrl.MaxOnAir() }

// atOnAirLimit reports whether the soft local on-air cap is already reached, so the
// SHARE selector blocks flipping ANOTHER row on air (taking one off air frees a slot).
func (m model) atOnAirLimit() bool { return m.ctrl.OnAirCount() >= m.ctrl.MaxOnAir() }

// hasSchedule reports whether a row has a time-of-use schedule set (so the table
// can flag it), live session schedules are not surfaced per-window here.
func (m model) hasSchedule(row shareRow) bool {
	return len(m.pricingFor(row.model).Windows) > 0
}

// pilotLamp is the SHARE dispatch console's per-model status lamp (catalog #6): ● on air
// (green), ◐ warming / reconnecting (amber), ○ idle / off-air (dim) - so the whole fleet's
// status reads in one glance down the column, like a dispatch console's unit-status lamps.
// Rides the increment-0 lamps, so palette mono collapses it to the ink ramp.
func pilotLamp(on bool, link agent.LinkState) (string, lipgloss.Style) {
	if !on {
		return "○", stDim
	}
	if link == agent.LinkOnAir {
		return "●", lampStyle(roleSignal)
	}
	return "◐", lampStyle(roleDialGlow)
}

// marquee is the SHARE banner's gentle horizontal scroller: when text fits in width it is
// returned UNCHANGED (static by default — the no-op contract); when it overflows, it returns
// a width-wide window that advances one cell per animation frame, with a small trailing GAP
// so the line reads as a loop (not a jump-cut) and a short start DWELL so the reader catches
// the beginning before it scrolls. It counts by RUNE (so a folded-ASCII and a Unicode line
// both stay width-bounded) and is ANSI-free — pass PLAIN text (fold + strip first), style the
// result. frame is the model's EXISTING animation counter (sigFrame); no new ticker. The raw
// wrapping slice is delegated to marqueeWindow (the Ping World ticker's window), so only the
// banner-specific policy (fit / gap / dwell) lives here.
func marquee(text string, width, frame int) string {
	if width <= 0 {
		return ""
	}
	if len([]rune(text)) <= width {
		return text // fits — static, every frame
	}
	const gap = 4   // spaces between the tail and the wrapped-around head
	const dwell = 3 // frames held at the start each cycle, so the opening is readable
	loop := text + strings.Repeat(" ", gap)
	period := len([]rune(loop))
	start := frame % (period + dwell)
	if start -= dwell; start < 0 {
		start = 0 // hold at the beginning for the dwell frames
	}
	return marqueeWindow(loop, start, width)
}

// editorLivePreview renders the "right now you would charge ..." line from the
// editor's current (in-progress) price + windows, using the SAME protocol.ActivePrice
// the broker evaluates - so the preview is honest about which window (if any) is live.
func (m model) editorLivePreview() string {
	in, _ := strconv.ParseFloat(strings.TrimSpace(orZero(m.edPriceIn)), 64)
	out, _ := strconv.ParseFloat(strings.TrimSpace(orZero(m.edPriceOut)), 64)
	offer := protocol.ModelOffer{
		PriceIn:  in,
		PriceOut: out,
		Schedule: schedToProtocol(m.edWindows),
	}
	now := time.Now()
	aIn, aOut, free, scheduled := offer.ActivePrice(now)
	// Name the source so the operator knows WHY: which window, FREE, or the flat base.
	src := "base"
	if scheduled {
		// Find the first matching window to label it HH:MM-HH:MM (first match wins,
		// same as ActivePrice).
		for _, w := range offer.Schedule {
			if w.Matches(now) {
				src = "window " + w.Start + "-" + w.End + " UTC"
				break
			}
		}
	}
	// Narrow terminals get a compact form (no "in" leg, terse prefix) so the preview
	// never overflows the SHARE column at <=64 cols.
	narrow := m.narrow()
	prefix := "right now you would charge "
	if narrow {
		prefix = "now: "
		// Compact the source label too (drop "window "/" UTC").
		switch {
		case scheduled && !free:
			src = "win"
		case free && scheduled:
			src = "win"
		}
	}
	label := stDim.Render(prefix)
	if free {
		return label + stLive.Render("FREE") + stDim.Render("  ("+src+")")
	}
	body := stEmber.Render(dollars(aOut) + "/1M out")
	if !narrow {
		body += stDim.Render(" · ") + stEmber.Render(dollars(aIn)+"/1M in")
	}
	return label + body + stDim.Render("  ("+src+")")
}

// maskKey renders an API key as bullets (keeping a short tail visible so the user
// can confirm what they typed) so the secret never sits in plaintext on screen.
func maskKey(k string) string {
	n := len([]rune(k))
	if n == 0 {
		return ""
	}
	if n <= 4 {
		return strings.Repeat("•", n)
	}
	// Rune-slice the last 4 CHARACTERS (byte-slicing k[len(k)-4:] can split a multi-byte
	// rune for a non-ASCII key and render a garbled tail).
	return strings.Repeat("•", n-4) + string([]rune(k)[n-4:])
}

// modalFooter renders a modal sub-screen's own footer (its keys + the balance),
// width-safe: it stacks under a narrow width and drops the right half when it
// can't fit. status rides under the rule like the main footer.
func modalFooter(w int, left, right, status string) string {
	rule := stHeadRule.Render(strings.Repeat("─", w))
	// WRAP THE STATUS. It was emitted on one line and ran straight off the right edge,
	// so a long refusal - "private band limit reached (free plan allows 1) - yours is on
	// <station>. Move it to this model..." - lost the half that says what to DO about it
	// (founder screenshot). A message the operator cannot finish reading is worse than
	// no message, because they know something is wrong and not what.
	st := ""
	if status != "" {
		st = "\n" + wrapStatus(status, w)
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return rule + "\n" + left + st // drop the right half; keys are what matter here
	}
	return rule + "\n" + left + strings.Repeat(" ", gap) + right + st
}

func (m model) footer(w int) string {
	// COMPACT (windowshade): a single, terse key-hint footer under one hairline rule -
	// no sprawling bal/broker/status block. It still adapts the leading hint to the
	// mode so the right keys are taught, and always carries the `m expand` reminder.
	// COMPACT has its own terse one-liner, but ONLY for the screens it actually knows.
	// Its switch was a second, parallel copy of the per-mode key knowledge, and it drifted:
	// every screen added since (BASE STATION, the band card, the confirms, the PRIVATE tab)
	// fell to its default and was taught the BAND BROWSER's keys - "↑↓ · ⏎ tune · s sort" -
	// on screens where none of them do anything.
	//
	// That is the exact failure BASE STATION had before it got a footer case, and the note
	// there still applies: a footer that describes a different screen is worse than none,
	// because it is the one place an operator looks to learn what a screen can do.
	//
	// So an unknown mode now FALLS THROUGH to the per-mode footer below. It is a little
	// longer than the windowshade line, and correct; the sub-screens it affects are all
	// transient, so the density cost is paid for seconds and the lie is paid for always.
	if m.compact && compactKnowsMode(m.mode) {
		return m.compactFooter(w)
	}
	// Keybindings adapt to the mode so the footer always teaches the right keys. At
	// narrow widths a terse key line replaces the full one so it fits.
	var left string
	// Modal sub-screens get their OWN footer keys (TUI-V2-CRITIQUE B) - the browse
	// "↑↓ tune · / cmd" keys do nothing here and mislead.
	switch m.mode {
	case modeConnectConfirm:
		left = stDim.Render("enter/y accept  ·  esc/n deny  ·  d detail")
		if m.narrow() {
			left = stDim.Render("⏎/y accept · esc/n deny · d detail")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeConnecting:
		left = stDim.Render("locking the channel  ·  ⏎ skip to channel  ·  esc cancel")
		if m.narrow() {
			left = stDim.Render("locking · ⏎ skip · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeOverLimit:
		left = stDim.Render("⏎ save & re-check  ·  ↑↓ nudge  ·  w wait  ·  esc deny")
		if m.narrow() {
			left = stDim.Render("⏎ save · ↑↓ nudge · w wait · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeLimits:
		left = stDim.Render("↑↓ move  ·  ⏎ edit  ·  tab field  ·  d clear  ·  esc done")
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎ edit · tab · d · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeShare:
		left = stDim.Render("↑↓ move  ·  ⏎/a on-air  ·  p price+schedule  ·  r re-detect  ·  s/esc tune in")
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎/a air · p · r · esc")
		}
		right := stRed.Render(fmt.Sprintf("%d on air", m.sharesOnAir()))
		return modalFooter(m.effWidth(), left, right, m.status)
	case modeShareEditor:
		left = stDim.Render("tab/↑↓ field  ·  type to set $  ·  a add window  ·  f free  ·  d delete  ·  ⏎ save  ·  esc cancel")
		if m.narrow() {
			left = stDim.Render("tab field · a/f/d · ⏎ save · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeShareSetup:
		left = stDim.Render("↑↓ pick  ·  ⏎ select/verify  ·  r re-scan  ·  s/esc tune in")
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎ · r · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeQuitConfirm:
		left = stDim.Render("y quit + go off air  ·  n/esc stay on air")
		if m.narrow() {
			left = stDim.Render("y quit · n/esc stay")
		}
		right := stRed.Render(fmt.Sprintf("%d on air", m.onAirCount()))
		return modalFooter(m.effWidth(), left, right, m.status)
	case modeAgent:
		switch {
		case m.agentPicker:
			left = stDim.Render("↑↓ pick a model  ·  ⏎ select  ·  esc keep current")
			if m.narrow() {
				left = stDim.Render("↑↓ pick · ⏎ select · esc keep")
			}
		case m.agentPendingConfirm != nil:
			left = stDim.Render("y run the tool  ·  n/esc deny (default DENY)")
			if m.narrow() {
				left = stDim.Render("y run · n/esc deny")
			}
		default:
			if m.agentBusy {
				left = stDim.Render("enter queue  ·  esc cancel (2× force)  ·  ") + stKey.Render("⌃y") + stDim.Render(" copy  ·  ⌃c quit")
				break
			}
			// PICK BY FIT, not by magic width. Every one of these teaches ⌃w (a
			// shortcut nobody is told about does not exist), and each drops the least
			// load-bearing words of the one above it. Hard-coded cut-offs kept being
			// off by a cell or two as the line's content changed - 100 was already
			// stale by 6 cells, and 118 by one at width 64 - so the ladder now MEASURES
			// each candidate and takes the richest one that actually fits beside the
			// account tag. Adding a key here can no longer overflow a terminal.
			for _, cand := range []string{
				// RUNG ORDER IS A SPEC, not a preference. Two behavioural specs pin words
				// here: desk_view.feature requires the AGENT footer to advertise
				// /operator, and agent_prompt_fixes.feature requires it to teach
				// "transcript". So those two are the LAST things dropped - the rungs
				// shed joins, then "enter", then /model, before either of them goes.
				stDim.Render("enter ask  ·  tab transcript  ·  ") + stKey.Render("⇧tab") +
					stDim.Render(" channel  ·  ") + stKey.Render("⌃y") +
					stDim.Render(" copy  ·  ⌃p perms  ·  ") + stKey.Render("⌃w") +
					stDim.Render(" console  ·  /model  ·  /operator  ·  esc exit"),
				stDim.Render("ask · tab transcript · ") + stKey.Render("⇧tab") +
					stDim.Render(" channel · ") + stKey.Render("⌃y") +
					stDim.Render(" copy · ⌃p perms · ") + stKey.Render("⌃w") +
					stDim.Render(" console · /model · /operator · esc exit"),
				stDim.Render("ask · tab transcript · ") + stKey.Render("⇧tab") +
					stDim.Render(" channel · ") + stKey.Render("⌃y") +
					stDim.Render(" copy · ⌃p perms · ") + stKey.Render("⌃w") +
					stDim.Render(" console · /operator · esc exit"),
				stDim.Render("ask · tab transcript · ") + stKey.Render("⌃y") +
					stDim.Render(" copy · ⌃p perms · ") + stKey.Render("⌃w") +
					stDim.Render(" console · /operator · esc exit"),
				stDim.Render("ask · tab transcript · ") + stKey.Render("⌃y") +
					stDim.Render(" copy · ⌃p perms · ") + stKey.Render("⌃w") + stDim.Render(" console · esc exit"),
				stDim.Render("ask · tab · ") + stKey.Render("⌃y") + stDim.Render(" copy · ⌃p perms · ") +
					stKey.Render("⌃w") + stDim.Render(" console · esc exit"),
				stDim.Render("ask · tab · copy · perms · ") + stKey.Render("⌃w") + stDim.Render(" web · exit"),
				stDim.Render("ask · tab · copy · perms · exit"),
			} {
				left = cand
				if lipgloss.Width(cand)+lipgloss.Width(m.accountTag(true))+2 <= m.effWidth() {
					break
				}
			}
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modePrivate:
		// BASE STATION had NO footer case, so it fell through to the browse keys and
		// taught "enter tune in · i log · f filter · ~ freq · s sort" - five keys that do
		// nothing here. A footer that describes a different screen is worse than none:
		// it is the one place an operator looks to learn what a screen can do, and this
		// one was actively lying. `x` (revoke) and `r` (refresh) were taught nowhere.
		// A LADDER, not one wide line and one narrow one: the wide form is 85 cells and
		// overflowed every terminal between narrow() and 86 - including an ordinary 80.
		for _, cand := range []string{
			"↑↓ move  ·  ⏎ manage a band  ·  x revoke  ·  r refresh  ·  ~ tune a freq  ·  esc back",
			"↑↓ · ⏎ manage · x revoke · r refresh · ~ freq · esc back",
			"↑↓ · ⏎ manage · x revoke · r · esc",
		} {
			left = stDim.Render(cand)
			if lipgloss.Width(cand)+lipgloss.Width(m.accountTag(true))+2 <= m.effWidth() {
				break
			}
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeBandManage:
		// A REVOKED band cannot be moved, and the card already omits the offer. The
		// footer has to agree: offering a key the screen will ignore is the same lie in
		// a different place, and it was caught by the lock that guards the card.
		if m.bandManageActive() {
			left = stKey.Render("⏎") + stDim.Render(" tune in  ·  ") + stKey.Render("m") +
				stDim.Render(" move  ·  ") + stKey.Render("n") + stDim.Render(" new code  ·  ") +
				stKey.Render("x") + stDim.Render(" revoke  ·  ") + stKey.Render("r") +
				stDim.Render(" re-scan  ·  esc back")
			if m.narrow() {
				left = stDim.Render("⏎ tune · m · n · x · r · esc")
			}
		} else {
			// A revoked band can do exactly one thing, and before `f` existed it could do
			// nothing at all - the row simply sat there forever.
			left = stDim.Render("this band is revoked  ·  ") + stKey.Render("f") +
				stDim.Render(" forget it  ·  ") + stKey.Render("esc") + stDim.Render(" back")
			if m.narrow() {
				left = stDim.Render("revoked · f forget · esc")
			}
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeBandMove:
		left = stDim.Render("↑↓ pick a model  ·  ") + stKey.Render("⏎") + stDim.Render(" move the band here  ·  esc cancel")
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎ move · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeBandRevokeConfirm:
		// A revoke burns the code forever, so the footer says the default out loud.
		left = stDim.Render("y revoke - burns the code forever  ·  n/esc keep it")
		if m.narrow() {
			left = stDim.Render("y revoke · n/esc keep")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeBandConfig:
		// Every key the card offers, in the order the sections appear. It is a long line
		// on purpose: the whole point of the card is that nothing about this band lives
		// somewhere else, and a footer that hid half the keys would undo that.
		// The whole point of the card is that nothing about this band lives elsewhere, so
		// the footer wants every key - but it must still fit. A ladder, widest first.
		for _, cand := range []string{
			"⏎ use  ·  a on air  ·  h public/private  ·  p price  ·  n new code  ·  l name  ·  e/t caps  ·  esc",
			"⏎ use · a on air · h private · p price · n code · l name · e/t caps · esc",
			"⏎ use · a air · h priv · p price · n code · l name · e/t caps · esc",
			"⏎ use · a air · h priv · p price · e/t caps · esc",
			"⏎ · a · h · p · e/t · esc",
		} {
			left = stDim.Render(cand)
			if lipgloss.Width(cand)+lipgloss.Width(m.accountTag(true))+2 <= m.effWidth() {
				break
			}
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeBandLabel:
		left = stDim.Render("type a name  ·  ⏎ save  ·  esc cancel  ·  an empty name clears it")
		if m.narrow() {
			left = stDim.Render("name it · ⏎ save · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeBandQuants:
		left = stDim.Render("space-separated  ·  ⏎ save  ·  esc cancel  ·  EMPTY accepts any quant")
		if m.narrow() {
			left = stDim.Render("quants · ⏎ save · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeBandRotateConfirm:
		// The cost, not the action, is what the operator has to weigh here: a rotate looks
		// like a move until you notice it cuts everyone off.
		left = stDim.Render("y new code - cuts off everyone on the old one  ·  n/esc keep it")
		if m.narrow() {
			left = stDim.Render("y new code · n/esc keep")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeBandDetail:
		left = stDim.Render("⏎ tune in  ·  esc/← back  ·  r re-scan")
		if m.narrow() {
			left = stDim.Render("⏎ tune · esc · r")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeVoicePreview:
		left = m.voicePreviewFooter()
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	case modeVoiceBooth:
		return modalFooter(m.effWidth(), m.voiceBoothFooter(), m.accountTag(true), m.status)
	case modeListeningPost:
		return modalFooter(m.effWidth(), m.listeningPostFooter(), m.accountTag(true), m.status)
	case modeShareVoice:
		return modalFooter(m.effWidth(), m.shareVoiceFooter(), m.accountTag(true), m.status)
	case modeVoicePicker:
		return modalFooter(m.effWidth(), m.voicePickerFooter(), m.accountTag(true), m.status)
	case modeFreqEntry:
		left = stDim.Render("type/paste a private frequency code  ·  ⏎ tune in  ·  esc cancel")
		if m.narrow() {
			left = stDim.Render("type a freq code · ⏎ tune · esc")
		}
		return modalFooter(m.effWidth(), left, m.accountTag(true), m.status)
	}
	if m.mode == modeChat {
		// One contextual hint (Zone 4): the keys live NOW, including the copy + connect
		// affordances; the full set (/quit, ⌃c, etc.) lives in /help.
		if m.narrow() {
			left = stDim.Render("talk · esc leave · ") + stKey.Render("shift-tab") + stDim.Render(" agent · ") + stKey.Render("⌃y") + stDim.Render(" copy")
		} else {
			left = stDim.Render("talk  ·  ") + stKey.Render("⏎") + stDim.Render(" send  ·  ") + stKey.Render("esc") + stDim.Render(" leave  ·  ") + stKey.Render("tab") + stDim.Render(" peek  ·  ") + stKey.Render("shift-tab") + stDim.Render(" agent (tools)  ·  ") + stKey.Render("⌃y") + stDim.Render(" copy  ·  /connect")
		}
	} else if m.filterMode {
		// FILTER ENTRY: teach the live-filter keys (type / esc / enter), not the browse keys.
		if m.narrow() {
			left = stDim.Render("type to filter · esc clear · ⏎ apply")
		} else {
			left = stDim.Render("type to filter the band by name  ·  esc clears + closes  ·  ⏎ keeps it applied")
		}
	} else if m.tuneTab == tabPrivate {
		// The PRIVATE half owns a different key set: no sort, no filter, no section
		// carousel - just move, tune, and the two ways out. Teaching the market keys here
		// is the exact failure BASE STATION had (a footer describing another screen).
		if m.narrow() {
			left = stDim.Render("↑↓ · ⏎ use · a air · n code · f forget · t mkt")
		} else {
			left = stDim.Render("↑↓ pick · ") + stKey.Render("⏎") +
				stDim.Render(" use it · ") + stKey.Render("a") +
				stDim.Render(" on/off air · ") + stKey.Render("n") +
				stDim.Render(" new code · ") + stKey.Render("f") +
				stDim.Render(" forget · ") + stKey.Render("t") +
				stDim.Render(" OPEN MARKET")
		}
	} else if m.narrow() {
		discKey := ""
		if m.connected != nil {
			discKey = " · d"
		}
		// Narrow keeps the ←→ section hint (load-bearing) and drops the ~ freq affordance to
		// fit width 40 - freq stays discoverable on wider terminals + in HELP. On a private
		// freq, esc (back to OPEN MARKET) is the load-bearing key, so teach it here.
		sect := " · ←→ section"
		if m.tuneFreq != "" {
			sect = " · esc mkt"
		}
		left = stDim.Render("↑↓ ⏎" + discKey + " · f filter" + sect + " · s · ?")
	} else if m.connected != nil {
		// Connected: lead with the channel + disconnect hints (load-bearing here); the
		// filter/sort keys still ride along but the toggles drop to keep the line tight.
		left = stDim.Render("↑↓ pick · enter tune in · i log · d disconnect · tab channel · s sort")
	} else if m.tuneFreq != "" {
		// On a PRIVATE FREQ: the load-bearing key is esc (back to OPEN MARKET). Teach it
		// up front so leaving the hidden channel is always discoverable.
		left = stDim.Render("↑↓ pick · enter tune in · i log · esc OPEN MARKET · s sort")
	} else {
		// ~ freq is the discoverable PRIVATE FREQUENCY affordance: it opens a small input
		// to enter a private band's frequency code. `v voices` (the DJ BOOTH drill-in) rides
		// here ONLY when a voice band is actually on air, so a pure-LLM screen never teaches a
		// voice key. The trailing "s" (share) is terse so it all fits the 80-col grid.
		if m.voiceBandsOnAir() > 0 {
			left = stDim.Render("↑↓ pick · enter tune in · i log · f filter · t private · v voices · s sort")
		} else {
			left = stDim.Render("↑↓ pick · enter tune in · i log · f filter · t private · s sort · ←/→ section")
		}
	}
	confMode := ""
	if m.confidentialOnly {
		confMode = stGold.Render("◆conf-only") + "  "
	}
	// Footer right half = balance only. The broker URL was dead weight here (it lives in
	// /config), so the footer stays rule + one key-hint line + balance (audit #9 de-clutter).
	right := confMode + m.accountTag(true)
	st := ""
	if m.status != "" {
		st = "\n" + stDim.Render("  ") + m.status
	}
	// The update banner rides in the status area when available - actionable in
	// BROWSE (u upgrades right here, x hides), passive prose elsewhere.
	if b := m.upgradeBanner(); b != "" {
		st += "\n" + stDim.Render("  ") + b
	}
	rule := stHeadRule.Render(strings.Repeat("─", w))
	// Narrow: stack the keys above the bal/broker line (a two-line status bar) so
	// neither half is forced to overflow the real width. (TUI-V2-CRITIQUE A §5.)
	if m.narrow() {
		return rule + "\n" + truncVisible(left, w) + "\n" + truncVisible(right, w) + st
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// The key-hint line + balance can't share one row at this width: stack them so
		// neither half overflows (balance is the load-bearing half on its own line).
		return rule + "\n" + truncVisible(left, w) + "\n" + truncVisible(right, w) + st
	}
	return rule + "\n" + left + strings.Repeat(" ", gap) + right + st
}

// supportURL is where /support (and `roger support`) sends people: the website,
// which hosts the community / Discord link in its footer. Per the founder, /support
// points at the site (not straight at Discord) so the single source of truth for
// the community link stays the footer.
const supportURL = "https://rogerai.fm"

// helpVersion is the client version shown in help; set by the host via SetVersion (always, in
// the real CLI). Empty default so a missed SetVersion shows no version rather than a STALE one
// (the prior hardcoded fallback drifted every release); render omits it when empty.
var helpVersion = ""

// SetVersion lets the host (cmd/rogerai) inject the build version so the help /
// about surfaces match `roger version`.
func SetVersion(v string) {
	if v == "" {
		return
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	helpVersion = v
}

// indentBlock prefixes every line of a multi-line block with pad (for placing
// art without disturbing its internal alignment).
func indentBlock(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// stairHeights are the glyph-ramp indices of the staircase meter's lit bars, low to
// high: ▃▄▅▇█ on the Unicode ramp. The count of LIT bars is the signal (cellphone
// style - instantly countable); an unlit cell renders the index-0 rail (▁) so every
// slot stays visible. The top two stairs sit at/above signalPeak, so the existing red
// glint lands only on a strong 4-5 bar carrier.
var stairHeights = [5]int{2, 3, 4, 6, 7}

// scanOffset returns the signal meter's per-cell animation offset: a triangle wave in
// [-amp,+amp] that advances with phase. amp==0 (an idle band) returns 0 for every
// phase, so the tower is dead-steady. amp>0 makes the cell oscillate, the swing
// widening with amp (= real in-flight load / tps). The mean is 0, so the animation
// never biases the resting LEVEL up or down - it is motion around the true signal.
func scanOffset(phase, amp int) int {
	if amp <= 0 {
		return 0
	}
	period := amp * 2 // full down-up cycle spans 2*amp steps
	p := ((phase % period) + period) % period
	if p > amp {
		p = period - p // reflect: 0..amp..0 triangle
	}
	return p - (amp+1)/2 // center the triangle near 0 so it swings both ways
}

// tintSignal grades a raw equalizer cell-by-cell so the bar carries meaning, not
// just a flat color: an online, measured tower is mono ink with its PEAK cells
// (the tallest bars) glinting the one accent red - a subtle dim->red gradient
// driven by tok/s. Offline / unmeasured is flat dim. Padding spaces stay bare
// (no visible color), so column alignment is unaffected. Under NO_COLOR lipgloss
// strips every color and the ▁..█ glyphs alone still read the signal.
func tintSignal(raw string, signal int, tps float64, online bool) string {
	// Grade (mono ink + a red peak glint) whenever the band is online with ANY
	// reading - a broker signal OR measured tps. An on-air node with no traffic still
	// carries a baseline signal, so its meter lights instead of going flat-dim.
	if !(online && (signal > 0 || tps > 0)) {
		return stDim.Render(raw)
	}
	ramp := signalRamp()
	lvlOf := func(r rune) int {
		for i, g := range ramp {
			if g == r {
				return i
			}
		}
		return -1
	}
	var b strings.Builder
	for _, r := range raw {
		lvl := lvlOf(r)
		switch {
		case lvl < 0: // a space / non-bar rune (alignment padding) - leave bare
			b.WriteRune(r)
		case lvl == 0: // the unlit rail - visibly empty, never inked
			b.WriteString(stDim.Render(string(r)))
		case lvl >= signalPeak: // peaking - the one red glint (the 4th/5th stair)
			b.WriteString(stRed.Render(string(r)))
		default: // lit bars - mono ink
			b.WriteString(stLive.Render(string(r)))
		}
	}
	return b.String()
}

// normalizeUpstream turns a detected base/chat URL into the chat-completions URL
// the agent POSTs to (mirrors cmd/rogerai's helper; kept local so the TUI's
// in-process /share has no host dependency).
func normalizeUpstream(u string) string { return node.NormalizeUpstream(u) }

func countOnline(o []offer) int {
	n := 0
	for _, x := range o {
		if x.Online {
			n++
		}
	}
	return n
}

// emptyScansToBlank is how many CONSECUTIVE empty /discover scans the band list tolerates before
// it actually blanks. At the ~5s rescan cadence, 3 ≈ 15s - long enough that a transient empty (a
// rescan that load-balanced onto a still-syncing broker instance) is absorbed without flicker,
// short enough that a genuine "all stations gone" still surfaces. See the offersMsg handler.
const emptyScansToBlank = 3

// The tick loop carries a GENERATION token (tickMsg.gen). Bubble Tea Cmds don't merge, so
// naively returning tick() from a key handler while the loop already has a tick pending would
// spawn a SECOND, parallel chain - and each navigation keypress another - accumulating loops
// that double the animation cadence and re-poll /discover (429 flicker). The rule: the handler
// reschedules with the CURRENT gen (one chain continues); any KICK bumps m.tickGen and schedules
// with the new gen, so every older chain's next tickMsg is stale (gen mismatch) and is dropped.
// Net: always exactly ONE live tick chain.
func tick(gen int) tea.Cmd {
	return tea.Tick(160*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{gen: gen} })
}

// kickTick starts a FRESH fast tick chain: it bumps the generation (so any older chain's next
// tickMsg is stale and dies) and returns the Cmd. Use it wherever a key or mode change must
// restart the animation clock promptly WITHOUT stacking a second parallel loop. Pointer
// receiver so the bump persists on the model the caller returns. (Never use it in Init(),
// whose model copy is discarded - Init seeds gen 0 with tick(m.tickGen).)
func (m *model) kickTick() tea.Cmd {
	m.tickGen++
	return tick(m.tickGen)
}

// slowTick is the compact ("windowshade") cadence: a calm ~5s beat that only drives
// the periodic band re-scan, never animation. It keeps the band/share tables live
// without the rapid 160ms churn, so compact + idle is genuinely quiet. The instant
// the user un-compacts, relays, or starts a staged tune-in, the tickMsg handler
// switches back to the fast tick().
func slowTick(gen int) tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return tickMsg{gen: gen} })
}

// pingWorldTick is the CALM cadence the in-TUI Ping World screensaver advances on: the same
// slow worldTickMs as the standalone `roger --ping` (NOT the app's fast 160ms tick). Without
// this the in-TUI world ran ~3.4x too fast - the day<->night cycle raced by (founder: "day to
// night in ~5 seconds"). A screensaver breathes; it should never ride the interactive tick.
func pingWorldTick(gen int) tea.Cmd {
	return tea.Tick(worldTickMs*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{gen: gen} })
}

// fetchOffers pulls the FULL on-air set from the broker /discover (the broker does
// NOT paginate - one response carries every live offer). The TUI scales this with
// CLIENT-SIDE windowing (browseView renders only the visible window) + name/sort/
// toggle filters (visibleBands), which covers realistic scale. NEXT STEP, if on-air
// counts ever exceed a few hundred: add broker-side pagination + load-on-scroll
// here (a cursor/offset on /discover, fetching the next page as the window nears the
// bottom) so the client never holds the whole list in memory.
func fetchOffers(broker string) tea.Cmd {
	return func() tea.Msg {
		resp, err := http.Get(broker + "/discover")
		if err != nil {
			return errMsg("broker unreachable: " + broker)
		}
		defer resp.Body.Close()
		var d struct {
			Offers []offer `json:"offers"`
		}
		// A valid 200 with an empty body is a legitimate "no offers" scan (io.EOF),
		// not a drop; only a genuinely malformed body is treated as a broker drop.
		if err := json.NewDecoder(resp.Body).Decode(&d); err != nil && !errors.Is(err, io.EOF) {
			return errMsg("broker unreachable: " + broker)
		}
		sort.Slice(d.Offers, func(i, j int) bool { return d.Offers[i].PriceIn < d.Offers[j].PriceIn })
		return offersMsg(d.Offers)
	}
}

func fetchBalance(broker, user string) tea.Cmd {
	return func() tea.Msg {
		req, _ := http.NewRequest(http.MethodGet, broker+"/balance", nil)
		client.SignRequest(req, nil)
		req.Header.Set("X-Roger-User", user)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return errMsg("")
		}
		defer resp.Body.Close()
		var b struct {
			Balance      float64 `json:"balance"`
			LoggedIn     bool    `json:"logged_in"`
			MonthlyCap   float64 `json:"monthly_cap"`
			MonthlySpend float64 `json:"monthly_spend"`
		}
		json.NewDecoder(resp.Body).Decode(&b)
		return balanceMsg{balance: b.Balance, loggedIn: b.LoggedIn, monthlyCap: b.MonthlyCap, monthlySpend: b.MonthlySpend}
	}
}

// fetchPayoutStatus reads the operator's Connect/KYC + payable snapshot off the
// event loop (the SAME signed CLI path `roger payout` uses), for the SHARE-view
// earnings hint. Best-effort: any error returns a not-loaded snapshot (no hint).
func fetchPayoutStatus(broker string) tea.Cmd {
	return func() tea.Msg {
		st, err := client.FetchPayoutStatus(broker)
		if err != nil {
			return payoutStatusMsg{loaded: false}
		}
		return payoutStatusMsg{loaded: true, kyc: st.Status, payable: st.Earnings.Payable, min: st.MinPayout}
	}
}

// replyFooter renders the per-turn metrics line(s) under an assistant reply, in the
// monochrome+one-red language: dimmed provider/tokens/latency, t/s in the live color, the
// cost in ember. It surfaces what the user asked for - how many tokens in/out, how fast,
// how long, and the cost - on one calm line. When /stats (verbose) is on, a second dim line
// adds the locked price in/out. Falls back to the legacy "provider · $cost" one-liner if
// the broker reported no metrics (e.g. a free turn with no receipt), never an empty footer.
func replyFooter(msg chatMsg, verbose bool) []string {
	if msg.provider == "" && msg.tokensIn == 0 && msg.tokensOut == 0 && msg.latency == 0 {
		return []string{stDim.Render("   " + msg.status)}
	}
	sep := stDim.Render(" · ")
	// A LOCAL turn: name the route and the fact nothing was metered, and print no dollar
	// figure at all. "$0.00" is the wrong claim twice over - it implies a meter ran, and it
	// implies the number could have been higher. Latency is real and stays.
	if msg.local {
		parts := []string{stDim.Render("direct · this machine")}
		if msg.latency > 0 {
			parts = append(parts, stDim.Render(humanLatency(msg.latency)))
		}
		parts = append(parts, stDim.Render("nothing metered"))
		return []string{"   " + strings.Join(parts, sep)}
	}
	var parts []string
	if msg.provider != "" {
		parts = append(parts, stDim.Render(msg.provider))
	}
	if msg.tokensIn > 0 || msg.tokensOut > 0 {
		parts = append(parts, stDim.Render("↑"+humanTokens(msg.tokensIn)+" ↓"+humanTokens(msg.tokensOut)+" tok"))
	}
	if msg.tps > 0 {
		parts = append(parts, stLive.Render(fmt.Sprintf("%.0f t/s", msg.tps)))
	}
	if msg.latency > 0 {
		parts = append(parts, stDim.Render(humanLatency(msg.latency)))
	}
	parts = append(parts, stEmber.Render(dollars(msg.cost)))
	lines := []string{"   " + strings.Join(parts, sep)}
	if verbose && (msg.priceIn > 0 || msg.priceOut > 0) {
		lines = append(lines, stDim.Render(fmt.Sprintf("   price  ↑$%.2f  ↓$%.2f /1M", msg.priceIn, msg.priceOut)))
	}
	return lines
}

// freq carries the tuned PRIVATE band's code, or "" on the open market. Without it the
// broker hides every private node from routing, so a channel opened on a private band
// green-lit the turn and then failed with "no station is serving <model>" - the
// operator had done everything right.
// sendChatLocal runs ONE chat turn DIRECT against a server on this machine - the route a
// private band of your own deserves, since the model is already here and relaying the turn
// out to the broker so it can come back is a round trip to reach localhost.
//
// It reuses harness.LocalCompleter (the agent's local route) with a nil tools array: TUNE-IN
// is chat, no tools, and passing tools here would let a model emit a tool_call this view has
// no loop to run.
//
// WHAT THE RECEIPT MAY CLAIM. Latency is measured here, so it is reported. Tokens and t/s
// are NOT reported by every local server and are not parsed on this path, so they are left
// zero and the renderer omits them - a printed zero would read as a measurement. Cost is the
// one number that is genuinely known: nothing is metered, no wallet is touched, so the local
// footer prints the ROUTE rather than a "$0.00" that would read as a charge that happened to
// round down.
func sendChatLocal(chatURL, key, mdl, prompt string, history []harness.Message) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		msgs := append(append([]harness.Message{}, history...), harness.Message{Role: "user", Content: prompt})
		reply, err := harness.LocalCompleter(chatURL, key, mdl)(
			context.Background(), msgs, nil)
		if err != nil {
			return chatErrMsg(err.Error())
		}
		return chatMsg{
			reply: reply.Content, status: "direct · this machine",
			local: true, latency: time.Since(start),
		}
	}
}

func sendChat(broker, user, mdl, prompt string, confidential bool, maxOut float64, freq string, history []client.ChatTurn, exclude []string) tea.Cmd {
	return func() tea.Msg {
		turns := append(append([]client.ChatTurn{}, history...), client.ChatTurn{Role: "user", Content: prompt})
		r, err := client.ChatTurns(broker, user, mdl, turns, confidential, maxOut, freq, exclude)
		if err != nil {
			// A chat failure is surfaced INLINE in the transcript (chatErrMsg), not on
			// the footer status line - that was the silent-no-response bug: the user
			// typed, the spinner vanished, and nothing appeared where they were looking.
			return chatErrMsg(err.Error())
		}
		return chatMsg{
			reply: r.Reply, status: r.Status, cost: r.Cost,
			provider: r.Provider, tokensIn: r.TokensIn, tokensOut: r.TokensOut,
			tps: r.TPS, priceIn: r.PriceIn, priceOut: r.PriceOut, latency: r.Latency,
		}
	}
}

// displayChatLines renders the CHANNEL transcript, enclosing each turn in its telegram
// block (slate.go). Same shape as the AGENT view's, so the two surfaces of one product
// look like one product - and same reason for doing it at display time: the blocks span
// the view, and only here is the width known.
//
// The width passed in is the VIEWPORT's; transcriptContent then wraps at width-2 and
// indents by two, so anything painting to its own edges is built to that content width.
func (m model) displayChatLines(w int) []string {
	cw := max(1, w-2)
	out := make([]string, 0, len(m.transcript))
	for _, ln := range m.transcript {
		switch {
		case strings.HasPrefix(ln, chatAskMark):
			rows := chatUserRows(ln[len(chatAskMark):], cw)
			if !slatesOn() {
				out = append(out, rows...)
				continue
			}
			out = append(out, slateBlock(rows, cw, cSlate, cSlateShade)...)
		case strings.HasPrefix(ln, chatReplyMark):
			body := ln[len(chatReplyMark):]
			model, text := "", body
			if i := strings.IndexByte(body, 0); i >= 0 {
				model, text = body[:i], body[i+1:]
			}
			rows := chatReplyRows(model, text, cw)
			if !slatesOn() {
				out = append(out, rows...)
				continue
			}
			out = append(out, slateBlock(rows, cw, cReply, cSlateShade)...)
		default:
			out = append(out, ln)
		}
	}
	return out
}

// wrapStatus soft-wraps a footer status to the terminal, indenting continuations so the
// block reads as one message rather than as several. ANSI-aware, because a status
// usually carries styling and a naive wrap would count escape bytes as width and break
// far too early.
func wrapStatus(status string, w int) string {
	const indent = "  "
	body := max(20, w-len(indent))
	rows := strings.Split(ansi.Wrap(status, body, ""), "\n")
	for i, r := range rows {
		rows[i] = stDim.Render(indent) + r
	}
	return strings.Join(rows, "\n")
}

// secretArgCommands are the palette verbs whose ARGUMENT is a secret. The band
// frequency code is the one that exists today; the set is a list rather than a special
// case so the next one is a single line and cannot be forgotten.
var secretArgCommands = map[string]bool{"freq": true, "f": true}

// scrubSecretArgs strips the argument from a command that carries a secret, leaving the
// verb. History is for "what did I run", and for these the answer is the verb - the
// argument is a thing the product promised never to store.
func scrubSecretArgs(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return cmd
	}
	verb := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if !secretArgCommands[verb] {
		return cmd
	}
	return fields[0]
}
