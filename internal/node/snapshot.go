package node

import "rogerai.fm/roger/v6/internal/agent"

// Snapshot is a consistent, JSON-able read of the node's live state, taken under the
// lock. The web console renders it (GET /api/state + the SSE stream) and the TUI uses
// the same accessors to refresh its render cache. The upstream KEY is never included —
// same defense-in-depth as agent.redactUpstreamKey — only the (non-secret) endpoint is.
type Snapshot struct {
	Station  string    `json:"station"`
	OnAir    int       `json:"on_air"`
	MaxOnAir int       `json:"max_on_air"`
	LoggedIn bool      `json:"logged_in"`
	Upstream string    `json:"upstream"`
	Rows     []RowView `json:"rows"`
	Totals   Totals    `json:"totals"`
}

// RowView is one model in the share table: its catalog facts plus live counters when
// on air. Link is "off" | "connecting" | "on-air" | "reconnecting".
type RowView struct {
	Model string `json:"model"`
	// Modality is what this model DOES: "chat" (the back-compat default, and what an
	// absent value means), "tts" or "stt". A front-end that offers a chat conversation has
	// to be able to leave a VOICE model out of the picker: a tts band cannot hold a
	// conversation, and offering one is an invitation to a turn that can only fail.
	Modality string `json:"modality,omitempty"`
	// Upstream is the LOCAL chat-completions URL that actually serves this model, so a
	// front-end can route a pick straight at it instead of relaying to the broker and back
	// to this same machine. It is the URL only - the bearer key that endpoint may need
	// never leaves the node, and the routing that uses it happens server-side.
	//
	// Empty means there is nothing to send to, and a row with no upstream must not be
	// offered as reachable.
	Upstream     string  `json:"upstream,omitempty"`
	Ctx          int     `json:"ctx"`
	CtxEstimated bool    `json:"ctx_estimated"`
	OnAir        bool    `json:"on_air"`
	Private      bool    `json:"private"`
	Link         string  `json:"link"`
	PriceIn      float64 `json:"price_in"`
	PriceOut     float64 `json:"price_out"`
	Scheduled    bool    `json:"scheduled"`
	Served       int64   `json:"served"`
	OutTokens    int64   `json:"out_tokens"`
	Earnings     float64 `json:"earnings"`
	Node         string  `json:"node,omitempty"`
	BandDisplay  string  `json:"band_display,omitempty"`
	// Quant / Weights / Variant are what detection read off THIS machine for this model.
	// They are omitempty because absent is the common case and must stay distinguishable
	// from a claim: a station whose runtime and file said nothing sends no key at all,
	// rather than an empty string a renderer could mistake for a measured blank.
	Quant   string `json:"quant,omitempty"`
	Weights string `json:"weights,omitempty"`
	Variant string `json:"variant,omitempty"`
}

// Totals sum every live band (the ON-AIR panel footer).
type Totals struct {
	Requests  int64   `json:"requests"`
	OutTokens int64   `json:"out_tokens"`
	Earnings  float64 `json:"earnings"`
}

func linkLabel(s agent.LinkState) string {
	switch s {
	case agent.LinkOnAir:
		return "on-air"
	case agent.LinkReconnecting:
		return "reconnecting"
	default:
		return "connecting"
	}
}

// Snapshot takes a consistent read of the whole node under the lock.
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := Snapshot{
		Station:  c.station,
		OnAir:    c.onAirCountLocked(),
		MaxOnAir: c.maxOnAirLocked(),
		LoggedIn: c.loggedIn,
		Upstream: c.upstream,
		Rows:     make([]RowView, 0, len(c.rows)),
	}
	for _, r := range c.rows {
		p := c.pricingForLocked(r.Model)
		rv := RowView{
			Model:        r.Model,
			Modality:     r.Modality,
			Upstream:     r.Upstream,
			Ctx:          r.Ctx,
			CtxEstimated: r.CtxEstimated,
			Private:      c.private[r.Model],
			Link:         "off",
			PriceIn:      p.In,
			PriceOut:     p.Out,
			Scheduled:    len(p.Windows) > 0,
			Quant:        r.Quant,
			Weights:      r.Weights,
			Variant:      r.Variant,
		}
		if sess := c.sessions[r.Model]; sess != nil {
			rv.OnAir = true
			rv.Link = linkLabel(sess.Link())
			in, out := sess.Price()
			rv.PriceIn, rv.PriceOut = in, out
			reqs, toks := sess.Served()
			rv.Served, rv.OutTokens = reqs, toks
			rv.Earnings = sess.Earnings()
			rv.Node = sess.Node()
			_, _, rv.BandDisplay = sess.Band()
			snap.Totals.Requests += reqs
			snap.Totals.OutTokens += toks
			snap.Totals.Earnings += sess.Earnings()
		}
		snap.Rows = append(snap.Rows, rv)
	}
	return snap
}

// --- accessors the TUI uses to refresh its single-goroutine render cache ---

// Rows returns a copy of the detected-model catalog.
func (c *Controller) Rows() []ShareRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ShareRow, len(c.rows))
	copy(out, c.rows)
	return out
}

// Sessions returns a copy of the live on-air session registry (map copy; the *Session
// values are shared pointers, which is intended — they're the live counters).
func (c *Controller) Sessions() map[string]*agent.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]*agent.Session, len(c.sessions))
	for k, v := range c.sessions {
		out[k] = v
	}
	return out
}

// Private returns a copy of the per-model private-band flags.
func (c *Controller) Private() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.private))
	for k, v := range c.private {
		out[k] = v
	}
	return out
}

// Prices returns a copy of the per-model saved pricing.
func (c *Controller) Prices() map[string]Pricing {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Pricing, len(c.prices))
	for k, v := range c.prices {
		out[k] = v
	}
	return out
}

// Station returns the current callsign.
func (c *Controller) Station() string { c.mu.Lock(); defer c.mu.Unlock(); return c.station }

// Upstream returns the headline upstream chat URL (never the key).
func (c *Controller) Upstream() string { c.mu.Lock(); defer c.mu.Unlock(); return c.upstream }

// UpstreamKey returns the headline upstream bearer key. In-process only — never
// serialized into a Snapshot or sent to a client.
func (c *Controller) UpstreamKey() string { c.mu.Lock(); defer c.mu.Unlock(); return c.upstreamKey }

// SavedUpstream returns the last endpoint+key persisted via Hooks.SaveUpstream (the
// TUI's change-detection state).
func (c *Controller) SavedUpstream() (up, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.savedUp, c.savedKey
}

// Headline returns any live session (for the header badge + ON-AIR panel) and whether
// the node is on air at all.
func (c *Controller) Headline() (*agent.Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sessions {
		if s != nil {
			return s, true
		}
	}
	return nil, false
}

// OnAirCount is how many models are currently on air.
func (c *Controller) OnAirCount() int { c.mu.Lock(); defer c.mu.Unlock(); return c.onAirCountLocked() }
