// Package node holds the live operator state of a Roger sharing node — the set of
// locally-detected models, which of them are ON AIR (each a running agent.Session),
// their price + schedule, the station callsign, and the headline link status — behind
// a single mutex so MULTIPLE front-ends can drive one node concurrently.
//
// The terminal TUI (internal/tui) and the browser web console (internal/webui) both
// hold the SAME *Controller: a toggle in the browser flips the TUI row and vice-versa,
// because there is exactly one owner of the session registry. The headless `roger share`
// daemon uses the same type, so the web console attaches to it too. Everything here is
// UI-free: mutating methods return structured results (ToggleResult/PrivateResult) that
// each front-end renders in its own idiom (lipgloss for the TUI, JSON for the web).
package node

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/protocol"
)

// DefaultMaxOnAir is the SOFT local on-air cap used when the host supplies none. A
// local UX guard so an operator does not over-subscribe their host; the broker's
// per-owner cap is the real backstop.
const DefaultMaxOnAir = 5

// ErrReason strips the transport-level wrapping off a start/registration failure so the
// clause an operator can ACT on comes FIRST.
//
// A rejected share arrives as a nest of three frames:
//
//	register with https://broker.example: broker rejected registration (403): private band limit reached (free plan allows 1)
//	└─ who we called ──────────────────┘ └─ that it was refused ─────────┘ └─ WHY, the only actionable part ──────────────┘
//
// Front-ends render this on a ONE-LINE status bar, so the leading two frames - which say
// nothing the operator did not already know (they just asked this broker to share) - push
// the remedy off the right edge and the line reads "...: brok". Dropping them puts the
// broker's own sentence first, where it survives the clip. The status code is kept only
// when the broker sent no message, since then it is all we have.
func ErrReason(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	// "register with <broker>: ..." - the URL is noise on a status line.
	if rest, ok := cutAfter(s, "register with "); ok {
		if i := strings.Index(rest, ": "); i >= 0 {
			s = strings.TrimSpace(rest[i+2:])
		}
	}
	// "broker rejected registration (403): <reason>" - keep the reason, drop the frame.
	if rest, ok := cutAfter(s, "broker rejected registration ("); ok {
		if i := strings.Index(rest, "): "); i >= 0 {
			if reason := strings.TrimSpace(rest[i+3:]); reason != "" {
				return reason
			}
		}
	}
	return s
}

// cutAfter returns what follows the first occurrence of sep, and whether sep was present.
func cutAfter(s, sep string) (string, bool) {
	i := strings.Index(s, sep)
	if i < 0 {
		return s, false
	}
	return s[i+len(sep):], true
}

// SchedWindow is one editable time-of-use price window (times "HH:MM" UTC). Free
// zeroes the in-window price.
type SchedWindow struct {
	Start, End string
	In, Out    float64
	Free       bool
}

// Pricing is the per-model saved price + schedule the editor produces. The host
// persists it; on-air it is applied when the model goes live.
type Pricing struct {
	In, Out float64
	Windows []SchedWindow
}

// ShareRow is one locally-detected model in the provider catalog. Each row carries
// its OWN upstream (the server that actually serves it) + the bearer key that server
// needs, so a multi-endpoint box shares each model against the right backend.
type ShareRow struct {
	Model        string
	Modality     string // "" / chat | tts | stt — the detected kind, carried onto the offer
	Ctx          int
	CtxEstimated bool
	Upstream     string
	UpstreamKey  string
	// Quant / Weights / Variant tell this row apart from another station's offer of the
	// SAME model id, and ride onto the offer when it goes on air. Detected only - empty
	// means the runtime and the file said nothing, which is common and renders as absent.
	Quant   string
	Weights string
	Variant string
}

// VoiceConfig is the SHARE VOICE BOOTH's result for one tts model: the on-air DJ identity the
// operator built. Name is the display name (/voices picker); Voice is the chosen default voice — a
// single Kokoro id OR a weighted blend string ("af_heart:0.5+af_aoede:0.5", the blend IS the
// shared voice); Speed is the default rate (0.5–2.0); Language is the display language. On on-air
// they ride the offer (agent.Config), so a consumer gets the operator's picked voice. The zero
// value means "not configured" — a plain chat model shares with no voice metadata.
//
// SampleURL is an operator-hosted short clip for the /voices picker (the app plays it instead of
// a live synth preview). It is set via the host's saved config (config.json share_voices), not the
// BOOTH, and is passed through UNVALIDATED - the broker owns voice-metadata validation/moderation,
// so the node never pre-rejects what the broker accepts.
type VoiceConfig struct {
	Name      string
	Voice     string
	Speed     float64
	Language  string
	SampleURL string
}

// startAgent is the process-edge seam for launching a share (defaults to the real agent.Start). It
// is a package var ONLY so a test can capture the built agent.Config without a live broker; the
// production path is agent.Start unchanged.
var startAgent = agent.Start

// Hooks are the host-supplied persistence closures (disk I/O lives in the CLI, not
// here). All are nil-safe: a nil hook just skips persistence.
type Hooks struct {
	SaveUpstream func(upstream, key string)
	SavePrice    func(model string, p Pricing)
	// SaveAutoStart persists the per-model auto-start decision. Nil-safe like the rest.
	SaveAutoStart func(model string, on bool)
	SaveStation   func(station string)
}

// Config seeds a Controller with the immutable-ish node identity + defaults the host
// resolves once at startup.
type Config struct {
	Broker      string
	HW          string
	Station     string
	ShareModel  string  // the onboarding default model (sorted first; carries the saved price)
	SharePriceI float64 // saved onboarding price for ShareModel
	SharePriceO float64
	MaxOnAir    int                    // 0 -> DefaultMaxOnAir
	Upstream    string                 // saved/verified upstream base or chat URL (headline default)
	UpstreamKey string                 // bearer key the saved upstream needs, if any
	Prices      map[string]Pricing     // saved per-model pricing from a previous session
	Voices      map[string]VoiceConfig // saved per-model voice identity (config.json share_voices)
	// AutoStart seeds the per-model "put this back on air at launch" decision. Present =
	// the operator has decided; absent = they have not, and the opt-out default applies.
	AutoStart map[string]bool
	Hooks     Hooks
}

// Controller is the single, concurrency-safe owner of a node's live share state.
type Controller struct {
	mu sync.Mutex

	broker      string
	hw          string
	station     string
	shareModel  string
	sharePriceI float64
	sharePriceO float64
	maxOnAir    int
	hooks       Hooks

	rows     []ShareRow
	sessions map[string]*agent.Session
	private  map[string]bool
	prices   map[string]Pricing
	voices   map[string]VoiceConfig // per-model voice identity (config-seeded and/or BOOTH-set)
	// autostart is TRI-STATE by presence: absent = the operator has never said, true/false
	// = they have. Absence matters because the default is opt-OUT - putting a model on air
	// marks it for next launch - and that default must not silently re-arm a model the
	// operator deliberately turned off and then toggled on for one session.
	autostart map[string]bool
	// locks holds each live session's ON-AIR lock release (keyed by model, like
	// sessions). The lock is the cross-process one-broadcaster-per-node-id guard
	// shared with the headless CLI (internal/onair; the eager-puma-54-voice
	// double-broadcast fix) - held for the life of the session, released on every
	// stop path below.
	locks map[string]func()

	upstream    string // headline upstream (found[0]) — fallback for rows that predate per-row upstreams
	upstreamKey string
	savedUp     string // last endpoint persisted via Hooks.SaveUpstream (change detection)
	savedKey    string

	loggedIn bool // updated by the front-ends; gates priced/private shares
}

// New builds a Controller from cfg. The session/price/private registries start empty;
// the host calls LoadRows after the first detection scan.
func New(cfg Config) *Controller {
	c := &Controller{
		broker:      cfg.Broker,
		hw:          cfg.HW,
		station:     cfg.Station,
		shareModel:  cfg.ShareModel,
		sharePriceI: cfg.SharePriceI,
		sharePriceO: cfg.SharePriceO,
		maxOnAir:    cfg.MaxOnAir,
		hooks:       cfg.Hooks,
		sessions:    map[string]*agent.Session{},
		private:     map[string]bool{},
		prices:      map[string]Pricing{},
		voices:      map[string]VoiceConfig{},
		autostart:   map[string]bool{},
		locks:       map[string]func(){},
		// Seed the saved/verified upstream so the first scan probes it first and a saved
		// keyed upstream is reused without re-prompting. savedUp/Key mirror what is already
		// on disk so a re-detection of the same endpoint is a no-op (no SaveUpstream write).
		upstream:    NormalizeUpstream(cfg.Upstream),
		upstreamKey: cfg.UpstreamKey,
		savedUp:     cfg.Upstream,
		savedKey:    cfg.UpstreamKey,
	}
	for k, v := range cfg.Prices {
		c.prices[k] = v
	}
	// Copy (not alias) the saved voice identities, exactly like Prices: a later
	// SetVoiceConfig must never write back into the host's map.
	for k, v := range cfg.Voices {
		c.voices[k] = v
	}
	for k, v := range cfg.AutoStart {
		c.autostart[k] = v
	}
	return c
}

// AutoStartFor reports whether this model is armed to go on air at launch, and whether
// the operator has ever said either way. `set` is what makes the opt-out default safe:
// an unset model is armed by its FIRST successful share, but one the operator explicitly
// disarmed stays off even if they toggle it on for a single session.
func (c *Controller) AutoStartFor(model string) (on, set bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	on, set = c.autostart[model]
	return on, set
}

// SetAutoStart records an EXPLICIT decision and persists it.
func (c *Controller) SetAutoStart(model string, on bool) {
	c.mu.Lock()
	c.autostart[model] = on
	hook := c.hooks.SaveAutoStart
	c.mu.Unlock()
	if hook != nil {
		hook(model, on)
	}
}

// AutoStartModels lists the models armed for launch, in a STABLE order so a rig that hits
// the on-air cap starts the same subset every time rather than a different one per boot.
func (c *Controller) AutoStartModels() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for m, on := range c.autostart {
		if on {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// SetLoggedIn records that a front-end observed the operator as logged in. It is
// RAISE-ONLY: passing true marks the node logged in, passing false is a no-op. This lets
// BOTH front-ends push their best knowledge every refresh without one clobbering the
// other (the TUI ticks SetLoggedIn(false) before its first balance read; a web login must
// survive that). An actual sign-out goes through Logout.
func (c *Controller) SetLoggedIn(v bool) {
	if !v {
		return
	}
	c.mu.Lock()
	c.loggedIn = true
	c.mu.Unlock()
}

// Logout explicitly clears the logged-in state (an operator sign-out from either
// front-end). Priced/private shares re-lock until the next login.
func (c *Controller) Logout() {
	c.mu.Lock()
	c.loggedIn = false
	c.mu.Unlock()
}

// LoggedIn reports the current login state.
func (c *Controller) LoggedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loggedIn
}

// SetPrices seeds the saved per-model pricing (from the host's config) without going
// through the editor. Used once at startup so on-air uses the operator's saved prices.
func (c *Controller) SetPrices(p map[string]Pricing) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prices = map[string]Pricing{}
	for k, v := range p {
		c.prices[k] = v
	}
}

// LoadRows replaces the detected-model catalog from a detection scan. It adopts the
// headline upstream + key from the first server and PERSISTS a newly-verified endpoint
// (mirrors the CLI's save in `roger share`), only on a real change so a re-scan of the
// already-saved endpoint never rewrites config. Use for an EXPLICIT user re-detect.
func (c *Controller) LoadRows(found []detect.Found) { c.loadRows(found, true) }

// LoadRowsNoPersist is LoadRows that NEVER writes the upstream to disk. Used for the
// passive initial detection on web-console launch, so merely opening the console can't
// silently rewrite share config — persistence is reserved for an explicit re-detect.
func (c *Controller) LoadRowsNoPersist(found []detect.Found) { c.loadRows(found, false) }

// firstServing returns the first detected server that lists at least one model, else the
// first server of any kind, else nil. "Reachable" and "useful" are different questions and
// only the second one should decide which endpoint a station is bound to.
func firstServing(found []detect.Found) *detect.Found {
	for i := range found {
		if len(found[i].Models) > 0 {
			return &found[i]
		}
	}
	if len(found) > 0 {
		return &found[0]
	}
	return nil
}

func (c *Controller) loadRows(found []detect.Found, persist bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The station's upstream must be a server that actually SERVES something. found[0] is
	// whatever answered first - which includes a reachable-but-empty endpoint, and one of
	// those used to get saved as the upstream and then re-probed on every launch. Prefer
	// the first server with models; fall back to found[0] only when nothing has any, so a
	// machine with only empty servers still reports where it looked.
	if up := firstServing(found); up != nil {
		c.upstream = NormalizeUpstream(up.Chat)
		c.upstreamKey = up.Key
		if persist && c.hooks.SaveUpstream != nil && up.BaseURL != "" &&
			(up.BaseURL != c.savedUp || up.Key != c.savedKey) {
			c.savedUp, c.savedKey = up.BaseURL, up.Key
			c.hooks.SaveUpstream(up.BaseURL, up.Key)
		}
	}
	seen := map[string]bool{}
	rows := make([]ShareRow, 0)
	for _, srv := range found {
		up := NormalizeUpstream(srv.Chat)
		for _, mdl := range srv.Models {
			if mdl == "" || seen[mdl] {
				continue
			}
			seen[mdl] = true
			// One ctx resolver shared with the CLI/TUI: the real detected window when the
			// upstream reported it, else the estimated default (flagged).
			ctxLen, ctxEst := detect.ResolveCtx(srv.Ctx, mdl)
			rows = append(rows, ShareRow{
				Model: mdl, Modality: srv.Modality[mdl], Ctx: ctxLen, CtxEstimated: ctxEst,
				Upstream: up, UpstreamKey: srv.Key,
				Quant: srv.Quant[mdl], Weights: srv.Weights[mdl], Variant: srv.Variant[mdl],
			})
		}
	}
	// Saved onboarding model first, so the obvious default is at the cursor.
	if def := c.shareModel; def != "" {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Model == def && rows[j].Model != def })
	}
	c.rows = rows
}

// SetRows replaces the detected-model catalog directly (bypassing a detection scan).
// Used where the rows are already known — e.g. a unit test, or a host that resolves the
// catalog itself.
func (c *Controller) SetRows(rows []ShareRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append([]ShareRow(nil), rows...)
}

// ToggleResult describes what a ToggleOnAir call did, so each front-end can render its
// own status line without the controller importing a UI.
type ToggleResult struct {
	Model       string
	WentOff     bool    // was on air, now stopped
	Priced      bool    // started priced (vs FREE)
	PriceOut    float64 // for the "$x/1M out" label
	AtLimit     bool    // blocked: soft on-air cap reached
	LoginNeeded bool    // blocked: priced OR private share needs login
	Err         error   // agent.Start failed
	// NowPrivate reports that the model came back on air on its PRIVATE band. A start
	// resumes at the row's recorded visibility, so a front-end must be able to say which
	// one it got - "on air" alone would read as the open market for a model the operator
	// deliberately hid.
	NowPrivate bool
	// AutoStartArmed reports that THIS start also armed the model for the next launch
	// (the opt-out default firing on a model the operator had never decided about). It is
	// surfaced so the arming is visible the moment it happens - a rig that quietly starts
	// broadcasting on every boot because of a toggle weeks ago is exactly the surprise
	// this flag exists to prevent.
	AutoStartArmed bool
	// NotServed reports that this machine has no row for the model at all - detection has
	// not found it (yet), so there was nothing to put on air. Distinct from an error: it is
	// the ordinary state of a rig whose model server has not started.
	NotServed bool
}

// ToggleOnAir flips the on-air state of model: an off-air model starts an in-process
// agent.Session against its upstream at the saved/free price; an on-air model stops.
// Ports the TUI's toggleShareAt (login-gate, soft max-on-air cap, node-id derivation).
// It also fires the auto-start save OUTSIDE the lock.
//
// The save has to happen out here. ToggleOnAir holds c.mu for its whole body via a
// deferred Unlock registered first, so any defer added later in that body runs BEFORE the
// unlock, not after - a save hook that reached back into the controller (to read pricing,
// say) would deadlock against a lock it cannot see it is already holding. res.AutoStartArmed
// already carries the one bit this needs, so the wrapper reads it and calls out cleanly.
func (c *Controller) ToggleOnAir(model string) ToggleResult {
	res := c.toggleOnAir(model)
	if res.AutoStartArmed {
		c.mu.Lock()
		hook := c.hooks.SaveAutoStart
		c.mu.Unlock()
		if hook != nil {
			hook(model, true)
		}
	}
	return res
}

func (c *Controller) toggleOnAir(model string) ToggleResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := ToggleResult{Model: model}
	row, ok := c.rowFor(model)
	if !ok {
		// NOT A SUCCESS, AND IT USED TO LOOK LIKE ONE. A bare zero result carries no Err
		// and no flag, so every caller that branched on "no error" read this as "started".
		// AutoStartAll did exactly that and printed ON AIR for a model that does not exist
		// on this machine.
		res.NotServed = true
		return res
	}
	if sess := c.sessions[model]; sess != nil {
		sess.Stop()
		delete(c.sessions, model)
		c.releaseLockLocked(model)
		res.WentOff = true
		return res
	}
	if c.atLimitLocked() {
		res.AtLimit = true
		return res
	}
	p := c.pricingForLocked(model)
	priced := p.In > 0 || p.Out > 0 || len(p.Windows) > 0
	// ON AIR MUST RESUME AT THE ROW'S RECORDED VISIBILITY.
	//
	// This passed `false` unconditionally, so a model on a PRIVATE band that was taken off
	// air and put back on with the same key came back on the OPEN MARKET - while
	// c.private[model] stayed true, so every surface went on rendering it as PRIVATE. An
	// operator who hid a model, toggled it off and on, and read their own SHARE row had no
	// way to learn they were now broadcasting to everyone.
	//
	// Same family as the zombie band: a path that silently publishes something the operator
	// deliberately hid. Going private is a decision the row REMEMBERS, and every start has
	// to honour it - the only way to leave a private band is to say so explicitly (h), or
	// to revoke it.
	goPrivate := c.private[model]
	// A private start is login-gated exactly as a priced one is (a private band is an
	// account-scoped resource, and login state re-locks between sessions). Refusing is the
	// safe failure: starting PUBLIC because we could not start private is the leak.
	if (priced || goPrivate) && !c.loggedIn {
		res.LoginNeeded = true
		return res
	}
	sess, err := c.startLocked(row, p, goPrivate)
	if err != nil {
		res.Err = err
		return res
	}
	c.sessions[model] = sess
	// OPT-OUT DEFAULT: sharing a model arms it for the next launch, unless the operator
	// has already said otherwise. Only an UNSET model is armed - one they explicitly
	// disarmed stays disarmed, so toggling it on for a single session does not silently
	// re-arm it. The hook runs after the lock is dropped, like every other save here.
	// The persist itself happens in the ToggleOnAir wrapper, once the lock is dropped.
	armed := false
	if _, set := c.autostart[model]; !set {
		c.autostart[model] = true
		armed = true
	}
	res.NowPrivate = goPrivate
	res.Priced = p.In > 0 || p.Out > 0
	res.PriceOut = p.Out
	res.AutoStartArmed = armed
	return res
}

// PrivateResult describes what a TogglePrivate call did.
type PrivateResult struct {
	Model       string
	NowPrivate  bool
	Code        string // freshly-minted one-time frequency code (empty if none minted)
	Display     string // cosmetic band display
	AtLimit     bool
	LoginNeeded bool
	Err         error
	// Restored reports that Err came from a FAILED visibility change whose previous
	// session was put back on air unharmed - so the front-ends can say "nothing
	// changed" instead of leaving the operator to guess whether they are still
	// broadcasting. False alongside a non-nil Err means the row really did go dark.
	Restored bool
}

// TogglePrivate flips a row's PRIVATE-band state, (re)starting its session with the new
// visibility. Going private is login-gated (an earning-adjacent per-owner resource).
// Ports the TUI's togglePrivateAt.
func (c *Controller) TogglePrivate(model string) PrivateResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := PrivateResult{Model: model}
	row, ok := c.rowFor(model)
	if !ok {
		return res
	}
	if !c.loggedIn {
		res.LoginNeeded = true
		return res
	}
	goPrivate := !c.private[model]
	wasOn := c.sessions[model] != nil
	if !wasOn && c.atLimitLocked() {
		res.AtLimit = true
		return res
	}
	if sess := c.sessions[model]; sess != nil {
		sess.Stop()
		delete(c.sessions, model)
		// Release BEFORE the restart below re-acquires: a stale release closure from
		// the old session must never be able to delete the fresh session's lock.
		c.releaseLockLocked(model)
	}
	p := c.pricingForLocked(model)
	sess, err := c.startLocked(row, p, goPrivate)
	if err != nil {
		// The visibility change is a STOP-then-START, so a rejected start (the broker
		// refusing a private registration, say) would otherwise leave a model that was
		// happily on air a moment ago silently OFF AIR - the operator asked to change how
		// the row is listed, never to take it down. Put the previous session back at its
		// previous visibility so a failed toggle is a no-op, and report whether that
		// restore actually succeeded.
		res.Err = err
		if wasOn {
			if back, rerr := c.startLocked(row, p, !goPrivate); rerr == nil {
				c.sessions[model] = back
				res.Restored = true
			}
		}
		return res
	}
	c.sessions[model] = sess
	c.private[model] = goPrivate
	res.NowPrivate = goPrivate
	if goPrivate {
		_, code, display := sess.Band()
		res.Code, res.Display = code, display
	}
	return res
}

// startLocked launches an agent.Session for row at pricing p (caller holds the lock).
// Same unique/stable/privacy-preserving node id the CLI uses: <station>-<model>.
// It first claims the node id's cross-process ON-AIR lock (internal/onair, the same
// file the headless daemon holds): if another LIVE process is broadcasting this node
// id the start is refused with the daemon's exact error - the front-ends render it
// verbatim - instead of double-registering and rotating that process's bridge token.
func (c *Controller) startLocked(row ShareRow, p Pricing, private bool) (*agent.Session, error) {
	up := row.Upstream
	if up == "" {
		up = c.upstream
	}
	upKey := pickUpstreamKey(up, row.UpstreamKey, c.upstream, c.upstreamKey)
	node := agent.ShareNodeID(c.station, row.Model, 0)
	release, err := onair.Acquire(node, c.station, row.Model)
	if err != nil {
		return nil, err
	}
	// The SHARE VOICE BOOTH result (if any) rides onto the offer so a voice goes on air as the
	// operator's named DJ with their picked voice/blend/speed. An unconfigured model has the zero
	// VoiceConfig, so a plain chat share carries no voice metadata (unchanged).
	vc := c.voices[row.Model]
	sess, err := startAgent(agent.Config{
		Broker: c.broker, Upstream: up, UpstreamKey: upKey, NodeID: node, Station: c.station,
		Region: "home", HW: c.hw, Model: row.Model, Modality: row.Modality,
		PriceIn: p.In, PriceOut: p.Out, Ctx: row.Ctx, CtxEstimated: row.CtxEstimated, Parallel: 4,
		Quant: row.Quant, Weights: row.Weights, Variant: row.Variant,
		Private: private, Schedule: SchedToProtocol(p.Windows),
		Name: vc.Name, Voice: vc.Voice, Speed: vc.Speed, Language: vc.Language, SampleURL: vc.SampleURL,
	})
	if err != nil {
		release() // a failed start must not leave the node id locked
		return nil, err
	}
	c.locks[row.Model] = release
	return sess, nil
}

// releaseLockLocked releases a stopped session's on-air lock (caller holds c.mu).
// Nil-safe for sessions the controller never locked (Adopt'ed ones - their host owns
// the lock).
func (c *Controller) releaseLockLocked(model string) {
	if rel := c.locks[model]; rel != nil {
		rel()
		delete(c.locks, model)
	}
}

// SetVoiceConfig records the SHARE VOICE BOOTH result for a model (dj-name + voice/blend + speed +
// language). Like SetPricing it does not restart a live session — the next on-air toggle applies
// it. Saved identities seed via Config.Voices (the host's config.json share_voices block); a BOOTH
// edit itself stays in-session (no save hook yet - the sample_url survives because the BOOTH
// carries the stored value through its save).
func (c *Controller) SetVoiceConfig(model string, vc VoiceConfig) {
	c.mu.Lock()
	c.voices[model] = vc
	c.mu.Unlock()
}

// VoiceConfigFor returns the stored BOOTH result for a model, or the zero VoiceConfig when the
// model never went through the BOOTH (so the editor can seed its fields on reopen).
func (c *Controller) VoiceConfigFor(model string) VoiceConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.voices[model]
}

// pickUpstreamKey chooses the bearer to send to a row's upstream: the row's OWN key if it
// has one, else the headline key ONLY when the row's upstream IS the headline upstream.
// A keyless row on a DIFFERENT detected server gets no key — never spray the saved/headline
// bearer onto the wrong endpoint (mirrors the CLI's sameEndpoint gate).
func pickUpstreamKey(rowUpstream, rowKey, headlineUpstream, headlineKey string) string {
	if rowKey != "" {
		return rowKey
	}
	if NormalizeUpstream(rowUpstream) == NormalizeUpstream(headlineUpstream) {
		return headlineKey
	}
	return ""
}

// SetPricing records a per-model price + schedule (from the editor) and persists it.
// Does not restart a live session — the next on-air toggle applies it.
func (c *Controller) SetPricing(model string, p Pricing) {
	c.mu.Lock()
	c.prices[model] = p
	hook := c.hooks.SavePrice
	c.mu.Unlock()
	if hook != nil {
		hook(model, p)
	}
}

// PricingFor returns the price a model would share at: its edited price, else the saved
// onboarding price for the default model, else free.
func (c *Controller) PricingFor(model string) Pricing {
	// A nil controller is a REAL state, not a programming error: the TUI runs with
	// m.ctrl == nil before the first share is set up (syncShareCache guards on exactly
	// this), and the band card reads pricing while rendering. Free is the honest answer
	// for a station that has no controller to have priced anything.
	if c == nil {
		return Pricing{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pricingForLocked(model)
}

func (c *Controller) pricingForLocked(model string) Pricing {
	if p, ok := c.prices[model]; ok {
		return p
	}
	if model == c.shareModel {
		return Pricing{In: c.sharePriceI, Out: c.sharePriceO}
	}
	return Pricing{}
}

// Rename sets the station callsign and persists it. The new callsign applies to bands
// put on air AFTER the rename (a live session keeps its node id until it cycles).
func (c *Controller) Rename(station string) {
	c.mu.Lock()
	c.station = station
	hook := c.hooks.SaveStation
	c.mu.Unlock()
	if hook != nil {
		hook(station)
	}
}

// Detect re-scans this machine for OpenAI-compatible servers, so a re-detect from either
// front-end sees the WHOLE fleet.
//
// It used to SHORT-CIRCUIT: if the saved upstream probed reachable and served at least one
// model, that one server was returned and the machine was never scanned. On a box running
// twelve local servers that made eleven of them - and twenty-six of twenty-seven models -
// invisible in the browser console's SHARE tab, because the saved upstream happened to be
// cpu-bots on :8060, which serves exactly one model. `roger detect` in the same terminal
// listed all of them, which is the founder's benchmark: re-detect must behave like the TUI
// share tab.
//
// The short-circuit was not arbitrary - it was the only place a KEY reached the saved
// endpoint. detect.DetectFull takes URLs only (`extra ...string`), so a key-protected
// custom endpoint is probed with whatever keys the ENVIRONMENT exports and nothing else;
// the key the operator pasted (or that config saved) never gets tried, and the endpoint
// comes back as "needs a key" or not at all.
//
// So the scan always runs, and the KEYED probe is MERGED into its result rather than
// replacing it: full fleet AND the keyed endpoint. The merge is by base URL, so the
// endpoint appears once, and the keyed Found wins that slot because it is the only one
// holding the credential the row needs to go on air.
//
// The keyed probe is skipped when there is no key in hand: DetectFull already seeds the
// endpoint as a PRIORITY candidate and retries env keys against it, so an unkeyed re-probe
// would be the same request twice.
func (c *Controller) Detect(extra, key string) (found []detect.Found, needKey []string) {
	// A pasted URL+key takes priority; otherwise fall back to the saved/verified upstream
	// (and its key). A bare DetectFull only scans the default ports + listening sockets, so
	// without this a saved CUSTOM/keyed endpoint — the one the CLI finds because it seeds it
	// — would be missed by re-detect.
	c.mu.Lock()
	savedUp, savedKey := c.upstream, c.upstreamKey
	c.mu.Unlock()
	url, k := extra, key
	if url == "" {
		url, k = savedUp, savedKey
	}
	// The whole machine, every time - exactly the CLI's DetectFull path, with the (saved or
	// pasted) endpoint seeded as a priority candidate so it still wins de-dup and keeps its
	// "configured" name.
	found, needKey = detectFull(url)
	if url == "" || k == "" {
		return found, needKey
	}
	f, st := detect.ProbeKey(url, k)
	if st != detect.Reachable || len(f.Models) == 0 {
		return found, needKey
	}
	return mergeKeyed(found, f), dropNeedKey(needKey, f.BaseURL)
}

// mergeKeyed folds a keyed probe of one endpoint into a scan result, de-duplicated by base
// URL. An existing entry for the same base is REPLACED in place - position and all - so the
// priority-seeded endpoint stays first (loadRows binds the station to the first server that
// serves models) and so the row inherits the Key that scan pass could not know. An endpoint
// the scan missed entirely is appended.
func mergeKeyed(found []detect.Found, f detect.Found) []detect.Found {
	base := strings.TrimRight(f.BaseURL, "/")
	for i := range found {
		if strings.TrimRight(found[i].BaseURL, "/") == base {
			found[i] = f
			return found
		}
	}
	return append(found, f)
}

// dropNeedKey removes base from the "present but needs an API key" list. The scan reports a
// 401 for an endpoint whose key it was never given; once the keyed probe has opened it, it
// is no longer something to prompt about.
func dropNeedKey(needKey []string, base string) []string {
	base = strings.TrimRight(base, "/")
	out := needKey[:0]
	for _, n := range needKey {
		if strings.TrimRight(n, "/") != base {
			out = append(out, n)
		}
	}
	return out
}

// detectFull is the machine scan, behind a seam so a test can prove the fall-through
// happened without actually port-scanning the developer's machine. That scan is slow, and
// on a box already running a local model server it makes the result depend on what
// happens to be listening.
var detectFull = detect.DetectFull

// StopAll takes every model off air (clean exit / `/share off`).
func (c *Controller) StopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for mdl, sess := range c.sessions {
		if sess != nil {
			sess.Stop()
		}
		delete(c.sessions, mdl)
		c.releaseLockLocked(mdl)
	}
}

// Adopt registers an already-started session under model, so a host that launched the
// agent.Session itself (or a test) can hand it to the controller and have it counted,
// surfaced in snapshots, and stopped on StopAll. Replaces any existing session for model.
func (c *Controller) Adopt(model string, sess *agent.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[model] = sess
}

// rowFor returns the catalog row for model (caller holds the lock).
func (c *Controller) rowFor(model string) (ShareRow, bool) {
	for _, r := range c.rows {
		if r.Model == model {
			return r, true
		}
	}
	return ShareRow{}, false
}

// MaxOnAir is the effective soft on-air cap.
func (c *Controller) MaxOnAir() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxOnAirLocked()
}

func (c *Controller) maxOnAirLocked() int {
	if c.maxOnAir > 0 {
		return c.maxOnAir
	}
	return DefaultMaxOnAir
}

func (c *Controller) atLimitLocked() bool { return c.onAirCountLocked() >= c.maxOnAirLocked() }

func (c *Controller) onAirCountLocked() int {
	n := 0
	for _, s := range c.sessions {
		if s != nil {
			n++
		}
	}
	return n
}

// NormalizeUpstream canonicalizes a base/chat URL to the chat-completions endpoint the
// agent posts to. Shared by the controller, the TUI, and the CLI so they agree.
func NormalizeUpstream(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	switch {
	case u == "":
		return u
	case strings.HasSuffix(u, "/chat/completions"):
		return u
	case strings.HasSuffix(u, "/v1"):
		return u + "/chat/completions"
	default:
		return u + "/v1/chat/completions"
	}
}

// SchedToProtocol converts editable windows into the wire protocol.PriceWindow the
// agent publishes. Empty -> no schedule.
func SchedToProtocol(ws []SchedWindow) []protocol.PriceWindow {
	if len(ws) == 0 {
		return nil
	}
	out := make([]protocol.PriceWindow, 0, len(ws))
	for _, w := range ws {
		out = append(out, protocol.PriceWindow{Start: w.Start, End: w.End, In: w.In, Out: w.Out, Free: w.Free})
	}
	return out
}

// BandRevoked reconciles a model whose PRIVATE BAND was just revoked at the broker.
//
// THE ZOMBIE. Revoking a band deletes it broker-side, but the node stays REGISTERED
// PRIVATE with no band behind it: hidden from the open market and reachable by nobody,
// while the SHARE row still reads PRIVATE. Worse is what happens next - `private[model]`
// is still true, so the operator's first `h` (the obvious way to mint a fresh code)
// computes goPrivate = !true = FALSE and re-registers the model PUBLICLY. The only
// documented way to rotate a code took your model through the open market on the way.
//
// So a revoke takes the model OFF AIR and clears the flag. Off air, not public: the
// operator revoked the only way anyone could reach it, and quietly publishing a model
// they had deliberately hidden is the one outcome that must never happen by accident.
// From there a single `h` mints a fresh band, which is the rotation they were after.
//
// Returns whether anything was actually stopped, so the caller can say so rather than
// claiming an action it did not take. A band pointing at another machine's model is not
// ours to reconcile and reports false.
func (c *Controller) BandRevoked(model string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.rowFor(model); !ok {
		return false
	}
	wasPrivate := c.private[model]
	sess := c.sessions[model]
	if sess == nil && !wasPrivate {
		return false // not on air and not flagged: nothing to reconcile
	}
	if sess != nil {
		sess.Stop()
		delete(c.sessions, model)
		c.releaseLockLocked(model)
	}
	// The flag goes LAST and unconditionally: leaving it set is what makes the next
	// toggle publish.
	delete(c.private, model)
	return true
}

// IsOnAir reports whether THIS process is currently broadcasting model. It says nothing
// about other processes - that is the on-air lock's job.
func (c *Controller) IsOnAir(model string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[model] != nil
}

// AutoStartReport is what one launch of the armed models actually did. Every model lands
// in exactly one bucket, because an operator who armed six models and sees four on air
// must be able to learn WHY the other two are not - silence there reads as a bug.
type AutoStartReport struct {
	Started []string // now on air
	// NotServed are armed models this machine has no row for - typically a launch that beat
	// the local model server up. Not a failure and not a success: nothing was attempted.
	NotServed  []string
	Held       []string         // another live process already broadcasts this node id
	AtLimit    []string         // the soft on-air cap was reached first
	NeedsLogin []string         // priced or private, and nobody is signed in
	Failed     map[string]error // anything else, with the reason
}

// Any reports whether the launch had anything to say at all.
func (r AutoStartReport) Any() bool {
	return len(r.Started)+len(r.Held)+len(r.AtLimit)+len(r.NeedsLogin)+len(r.NotServed)+len(r.Failed) > 0
}

// AutoStartAll puts every armed model on air, in the stable order AutoStartModels gives.
//
// It reuses ToggleOnAir rather than a second start path, so auto-start cannot drift from
// what the operator gets by pressing the key: same cap, same login gate, same visibility
// resume, same on-air lock.
//
// THE LOCK IS WHY MULTIPLE INSTANCES ARE SAFE. onair.Acquire is keyed on the node id, so a
// second `roger` starting with the same models finds the first one's live PID and bows
// out per model - and that is the system working, not a failure, which is why Held is its
// own bucket rather than an error. A lock left by a crashed process is reclaimed once its
// PID is gone, so a hard kill does not strand a model off air.
func (c *Controller) AutoStartAll() AutoStartReport {
	rep := AutoStartReport{Failed: map[string]error{}}
	for _, m := range c.AutoStartModels() {
		if c.IsOnAir(m) {
			continue // already broadcasting in THIS process
		}
		res := c.ToggleOnAir(m)
		switch {
		case res.WentOff:
			// ToggleOnAir is a toggle: if a race put it on air between the check above and
			// the call, we have just turned it off. Put it back - and CHECK, rather than
			// assuming the second toggle worked.
			c.ToggleOnAir(m)
			if c.IsOnAir(m) {
				rep.Started = append(rep.Started, m)
			} else {
				rep.Failed[m] = errors.New("raced off air and could not be restarted")
			}
		case res.NotServed:
			rep.NotServed = append(rep.NotServed, m)
		case res.AtLimit:
			rep.AtLimit = append(rep.AtLimit, m)
		case res.LoginNeeded:
			rep.NeedsLogin = append(rep.NeedsLogin, m)
		case res.Err != nil && errors.Is(res.Err, onair.ErrHeld):
			rep.Held = append(rep.Held, m)
		case res.Err != nil:
			rep.Failed[m] = res.Err
		default:
			// Believe the machine, not the absence of an error. Started is the one bucket an
			// operator reads as "you are broadcasting", so it is confirmed against the live
			// session rather than inferred from a result that said nothing.
			if c.IsOnAir(m) {
				rep.Started = append(rep.Started, m)
			} else {
				rep.Failed[m] = errors.New("reported no error but is not on air")
			}
		}
	}
	return rep
}
