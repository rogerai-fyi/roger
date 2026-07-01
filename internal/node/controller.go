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
	"sort"
	"strings"
	"sync"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/detect"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// DefaultMaxOnAir is the SOFT local on-air cap used when the host supplies none. A
// local UX guard so an operator does not over-subscribe their host; the broker's
// per-owner cap is the real backstop.
const DefaultMaxOnAir = 4

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
}

// Hooks are the host-supplied persistence closures (disk I/O lives in the CLI, not
// here). All are nil-safe: a nil hook just skips persistence.
type Hooks struct {
	SaveUpstream func(upstream, key string)
	SavePrice    func(model string, p Pricing)
	SaveStation  func(station string)
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
	MaxOnAir    int                // 0 -> DefaultMaxOnAir
	Upstream    string             // saved/verified upstream base or chat URL (headline default)
	UpstreamKey string             // bearer key the saved upstream needs, if any
	Prices      map[string]Pricing // saved per-model pricing from a previous session
	Hooks       Hooks
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
	return c
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

func (c *Controller) loadRows(found []detect.Found, persist bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(found) > 0 {
		c.upstream = NormalizeUpstream(found[0].Chat)
		c.upstreamKey = found[0].Key
		if persist && c.hooks.SaveUpstream != nil && found[0].BaseURL != "" &&
			(found[0].BaseURL != c.savedUp || found[0].Key != c.savedKey) {
			c.savedUp, c.savedKey = found[0].BaseURL, found[0].Key
			c.hooks.SaveUpstream(found[0].BaseURL, found[0].Key)
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
			rows = append(rows, ShareRow{Model: mdl, Modality: srv.Modality[mdl], Ctx: ctxLen, CtxEstimated: ctxEst, Upstream: up, UpstreamKey: srv.Key})
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
	LoginNeeded bool    // blocked: priced share needs login
	Err         error   // agent.Start failed
}

// ToggleOnAir flips the on-air state of model: an off-air model starts an in-process
// agent.Session against its upstream at the saved/free price; an on-air model stops.
// Ports the TUI's toggleShareAt (login-gate, soft max-on-air cap, node-id derivation).
func (c *Controller) ToggleOnAir(model string) ToggleResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := ToggleResult{Model: model}
	row, ok := c.rowFor(model)
	if !ok {
		return res
	}
	if sess := c.sessions[model]; sess != nil {
		sess.Stop()
		delete(c.sessions, model)
		res.WentOff = true
		return res
	}
	if c.atLimitLocked() {
		res.AtLimit = true
		return res
	}
	p := c.pricingForLocked(model)
	priced := p.In > 0 || p.Out > 0 || len(p.Windows) > 0
	if priced && !c.loggedIn {
		res.LoginNeeded = true
		return res
	}
	sess, err := c.startLocked(row, p, false)
	if err != nil {
		res.Err = err
		return res
	}
	c.sessions[model] = sess
	res.Priced = p.In > 0 || p.Out > 0
	res.PriceOut = p.Out
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
	}
	p := c.pricingForLocked(model)
	sess, err := c.startLocked(row, p, goPrivate)
	if err != nil {
		res.Err = err
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
func (c *Controller) startLocked(row ShareRow, p Pricing, private bool) (*agent.Session, error) {
	up := row.Upstream
	if up == "" {
		up = c.upstream
	}
	upKey := pickUpstreamKey(up, row.UpstreamKey, c.upstream, c.upstreamKey)
	node := agent.ShareNodeID(c.station, row.Model, 0)
	return agent.Start(agent.Config{
		Broker: c.broker, Upstream: up, UpstreamKey: upKey, NodeID: node,
		Region: "home", HW: c.hw, Model: row.Model, Modality: row.Modality,
		PriceIn: p.In, PriceOut: p.Out, Ctx: row.Ctx, CtxEstimated: row.CtxEstimated, Parallel: 4,
		Private: private, Schedule: SchedToProtocol(p.Windows),
	})
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

// Detect runs an async-safe local-LLM scan (used by the re-detect action). It does not
// mutate the catalog; the caller passes the result to LoadRows.
func (c *Controller) Detect(extra, key string) (found []detect.Found, needKey []string) {
	// A pasted URL+key takes priority; otherwise fall back to the saved/verified upstream
	// (and its key). A bare DetectFull only scans the default ports + listening sockets, so
	// without this a saved CUSTOM/keyed endpoint — the one the CLI finds because it seeds it
	// — would be missed by re-detect, leaving the SHARE tab empty.
	c.mu.Lock()
	savedUp, savedKey := c.upstream, c.upstreamKey
	c.mu.Unlock()
	url, k := extra, key
	if url == "" {
		url, k = savedUp, savedKey
	}
	if url != "" {
		if f, st := detect.ProbeKey(url, k); st == detect.Reachable {
			return []detect.Found{f}, nil
		}
	}
	// Seed the (saved or pasted) endpoint as a priority candidate so it wins de-dup, then
	// scan the defaults — exactly the CLI's DetectFull path.
	return detect.DetectFull(url)
}

// StopAll takes every model off air (clean exit / `/share off`).
func (c *Controller) StopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for mdl, sess := range c.sessions {
		if sess != nil {
			sess.Stop()
		}
		delete(c.sessions, mdl)
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
