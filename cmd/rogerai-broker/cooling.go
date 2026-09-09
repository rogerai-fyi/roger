package main

// cooling.go - UPSTREAM FAILOVER + LEARNED STATION COOLDOWN (features/routing/upstream_failover).
//
// The Sep 7 incident: one station's upstream said 429 sixty-six times and the broker handed
// every one to the consumer, then routed the next request to the same station. This file is
// the OpenRouter/LiteLLM core the spec pins: the relay re-picks around a station that
// answered a no-output failure (relay/relayStream, the attempt plan below), a station that
// answers 429 is COOLING for the upstream's Retry-After (a pickFor hard filter, shared across
// instances, capped; never trust state), the only-station-cooling case is a fast honest 503
// with a Retry-After, and the Retry-After travels end to end. Probes never consult the filter.

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// Knobs (read per call so a scenario can flip them without rebuilding the broker; each is a
// process-wide env read, a map lookup under a lock).
const (
	defaultRelayAttempts   = 3
	defaultStationCooldown = 15 * time.Second
	maxStationCooldown     = 120 * time.Second
)

// relayFailoverOn: ROGERAI_RELAY_FAILOVER=0 restores today's single pick.
func relayFailoverOn() bool { return os.Getenv("ROGERAI_RELAY_FAILOVER") != "0" }

// relayAttempts: at most this many stations are tried for one request (ROGERAI_RELAY_ATTEMPTS).
func relayAttempts() int {
	if n := envInt("ROGERAI_RELAY_ATTEMPTS", defaultRelayAttempts); n >= 1 {
		return n
	}
	return 1
}

func cooldownDefault() time.Duration {
	return envDuration("ROGERAI_STATION_COOLDOWN_DEFAULT", defaultStationCooldown)
}

func cooldownMax() time.Duration {
	return envDuration("ROGERAI_STATION_COOLDOWN_MAX", maxStationCooldown)
}

// relayMinAttemptBudget is the least time that must remain on the request's deadline for the
// relay to start another attempt (a failover into a window too short to answer only converts a
// clean upstream error into a timeout). var: a scenario scales the deadline it pairs with.
var relayMinAttemptBudget = 10 * time.Second

// streamCommitGrace bounds how long a streaming relay withholds its 200/SSE headers waiting
// for the first upstream verdict. It MUST stay below the installed CLI's relay-transport
// ResponseHeaderTimeout (internal/client/client.go proxyResponseHeaderTimeout, 30s - every
// guest operator and `roger use` stream goes through it) or a slow first token (a long
// prefill on consumer GPUs) would die at the local proxy waiting for headers the broker is
// deliberately holding back; Cloudflare's ~100s no-bytes cap sits further out. An upstream
// 429 arrives in well under a second, so 20s loses no failover coverage. After the grace the
// headers go out and failover for that stream is over (the first content chunk commits them
// earlier in the normal case). Pinned by TestStreamCommitGraceBeatsProxyHeaderTimeout.
var streamCommitGrace = 20 * time.Second

// Cooling alert: a station cumulatively cooling for more than coolingAlertThreshold within
// coolingAlertWindow (with the cooldowns themselves as the demand evidence: each one was a
// real relay the upstream refused) pages the founder once; it clears after a window with no
// cooldown.
const (
	coolingAlertWindow    = time.Hour
	coolingAlertThreshold = 10 * time.Minute
)

// coolEvent is one cooldown a station entered: when, and how much cooling time it ADDED
// (repeated 429s extend the same window; the addition is the extension, so the cumulative
// figure is real cooling time, never a stacked sum).
type coolEvent struct {
	at    time.Time
	added time.Duration
}

// failoverable reports whether an upstream status is a NO-OUTPUT failure the broker may route
// around: a 429, any 5xx (incl. the station's own 502 "upstream unreachable"), or a 2xx that
// produced nothing usable. A client-caused 4xx (400/401/404/413/422...) would fail the same way
// on the next station, so it is answered as today.
func failoverable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500 || status < 400
}

// attemptID names attempt n of a request: the request id itself for the first, "<id>-n" for a
// failover attempt, so every attempt's receipt is its own row and the lineage is readable.
func attemptID(requestID string, n int) string {
	if n <= 1 {
		return requestID
	}
	return fmt.Sprintf("%s-%d", requestID, n)
}

// attemptCand is one station the relay may try for a request, with its billing plan and its
// upper-bound cost resolved up front (the ONE hold is sized over the plan).
type attemptCand struct {
	node    protocol.NodeRegistration
	offer   protocol.ModelOffer
	t       *nodeTunnel
	pricing pricingPlan
	maxCost float64 // this candidate's hold ceiling (0 = free plan: no hold)
}

// holdCostFor is the upper-bound cost of a request on one candidate, at the price the
// consumer will actually be billed: the FIXED plan price (grant / self), else the offer's
// active market price (the settle-time clamp must be a real ceiling, never a floor-to-~0 -
// C1), with an ESTIMATED context window clamped so a display sentinel can't inflate the
// pre-auth. A free plan holds nothing.
func holdCostFor(p pricingPlan, offer protocol.ModelOffer, body []byte, now time.Time) float64 {
	if p.free {
		return 0
	}
	holdIn, holdOut := p.in, p.out
	if !p.fixed {
		ain, aout, afree, _ := offer.ActivePrice(now)
		holdIn, holdOut = ain, aout
		if afree {
			holdIn, holdOut = 0, 0
		}
	}
	holdCtx := offer.Ctx
	if offer.CtxEstimated && holdCtx > 32768 {
		holdCtx = 32768
	}
	return estimateMaxCost(body, holdIn, holdOut, holdCtx)
}

// anonCannotPay mirrors the relay's login gate for a failover candidate: a signed but
// not-logged-in keypair may only be routed to a free offer (it has no balance to spend).
func anonCannotPay(gok bool, p pricingPlan, payer string, offer protocol.ModelOffer, now time.Time) bool {
	if gok || p.free || walletLoggedIn(payer) {
		return false
	}
	ain, aout, afree, _ := offer.ActivePrice(now)
	return !afree && (ain > 0 || aout > 0)
}

// planCeiling is the priciest candidate's upper-bound cost - what the ONE hold must cover
// for every station in the plan to be tryable.
func planCeiling(plan []attemptCand) float64 {
	c := 0.0
	for _, a := range plan {
		if a.maxCost > c {
			c = a.maxCost
		}
	}
	return c
}

// trimPlan drops the candidates the placed hold cannot cover (their attempt would settle
// above the reservation). The first candidate is the one the hold was placed for and stays.
func trimPlan(plan []attemptCand, held float64) []attemptCand {
	out := plan[:1]
	for _, a := range plan[1:] {
		if a.maxCost <= held+1e-12 {
			out = append(out, a)
		}
	}
	return out
}

// nextAttempt returns the index of the next candidate to try after attempt i failed with
// status, or -1 when the request must be answered as it stands: the failure is not routable,
// the plan is exhausted, too little of the deadline remains, or every remaining candidate
// started cooling - or left the broker - since the plan was made (a station that went off
// air during attempt 1 still has its tunnel in the plan; dispatching into it would only burn
// the deadline on a channel nobody drains).
func (b *broker) nextAttempt(plan []attemptCand, i, status int, deadline time.Time) int {
	if !failoverable(status) || (!deadline.IsZero() && time.Until(deadline) < relayMinAttemptBudget) {
		return -1
	}
	for j := i + 1; j < len(plan); j++ {
		c := plan[j]
		if _, cooling := b.coolingUntil(c.node.NodeID); cooling {
			continue
		}
		b.mu.Lock()
		live := b.tunnels[c.node.NodeID] == c.t
		b.mu.Unlock()
		if live {
			return j
		}
	}
	return -1
}

// bandAllow is the allow-list a FAILOVER re-pick runs under: the request's own allow-list
// (a grant's owner nodes; nil = everyone) unless the request rode a private band, in which
// case the band's admission set (intersected with the allow-list) - a band request never
// leaves the band.
func bandAllow(allow, privateAllow map[string]bool) map[string]bool {
	if len(privateAllow) == 0 {
		return allow
	}
	out := make(map[string]bool, len(privateAllow))
	for id := range privateAllow {
		if allow == nil || allow[id] {
			out[id] = true
		}
	}
	return out
}

// --- cooldown state (guarded by metricsMu) --------------------------------------------------

// cooldownFor normalizes a station-reported retry_after_sec into this broker's cooldown:
// absent/garbage (<= 0) -> the default, anything above the cap -> the cap (a forged value
// cannot bench a station for long). The SECONDS are clamped before the multiply:
// time.Duration(sec)*time.Second overflows past ~9.2e9 and went negative, which skipped the
// cap, cooled nothing, and put a negative Retry-After on the wire.
func (b *broker) cooldownFor(retryAfterSec int) time.Duration {
	maxD := cooldownMax()
	d := cooldownDefault()
	switch {
	case retryAfterSec <= 0:
	case retryAfterSec >= int(maxD/time.Second):
		d = maxD
	default:
		d = time.Duration(retryAfterSec) * time.Second
	}
	if d > maxD {
		d = maxD
	}
	return d
}

// coolStation records that a station's upstream said 429: it is cooling until now+cooldown,
// locally (the pick filter reads this map) and in the shared store (TTL = the cooldown) so
// every instance skips it. Repeated 429s EXTEND the window to the latest expiry, never stack.
// Routing state only: no trust, no strike, no success grading (exitInflightStatus).
func (b *broker) coolStation(node, model string, retryAfterSec int) time.Time {
	d := b.cooldownFor(retryAfterSec)
	now := b.now()
	until := now.Add(d)
	b.metricsMu.Lock()
	if b.cooling == nil {
		b.cooling, b.coolModel, b.coolEvents = map[string]time.Time{}, map[string]string{}, map[string][]coolEvent{}
	}
	added := d
	if prev := b.cooling[node]; prev.After(now) {
		if !until.After(prev) {
			until = prev // a shorter hint never cuts a running cooldown short
		}
		added = until.Sub(prev)
	}
	b.cooling[node] = until
	b.coolModel[node] = model
	b.coolEvents[node] = append(pruneCoolEvents(b.coolEvents[node], now.Add(-coolingAlertWindow)), coolEvent{at: now, added: added})
	b.metricsMu.Unlock()
	b.stats.stationCooldowns.Add(1)
	if b.shared != nil {
		if err := b.shared.markCooling(node, model, until, until.Sub(now)); err != nil && err != errNoSharedStore {
			b.coolFallbackOnce.Do(func() {
				log.Printf("cooldown: shared store unavailable (%v) - per-instance cooldown only until it returns", err)
			})
		}
	}
	log.Printf("COOLDOWN node=%s model=%s for=%s (retry_after_sec=%d) - routing around it, not a strike", node, model, d, retryAfterSec)
	return until
}

func pruneCoolEvents(evs []coolEvent, cutoff time.Time) []coolEvent {
	out := evs[:0]
	for _, e := range evs {
		if e.at.After(cutoff) {
			out = append(out, e)
		}
	}
	return out
}

// coolingUntilLocked reports whether node is cooling at `now` (caller holds metricsMu).
func (b *broker) coolingUntilLocked(node string, now time.Time) (time.Time, bool) {
	until, ok := b.cooling[node]
	if !ok {
		return time.Time{}, false
	}
	if !now.Before(until) {
		delete(b.cooling, node) // lapsed: drop it so the map never grows past the live set
		return time.Time{}, false
	}
	return until, true
}

func (b *broker) coolingUntil(node string) (time.Time, bool) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	return b.coolingUntilLocked(node, b.now())
}

// syncCooling merges the shared cooldown set into this instance's map (the same sync tick as
// liveness), keeping the later expiry. A read error keeps the current view.
func (b *broker) syncCooling() {
	if b.shared == nil {
		return
	}
	shared, err := b.shared.cooling()
	if err != nil {
		return
	}
	now := b.now()
	b.metricsMu.Lock()
	if b.cooling == nil {
		b.cooling, b.coolModel, b.coolEvents = map[string]time.Time{}, map[string]string{}, map[string][]coolEvent{}
	}
	for node, sc := range shared {
		if now.Before(sc.until) && sc.until.After(b.cooling[node]) {
			b.cooling[node] = sc.until
			if sc.model != "" {
				b.coolModel[node] = sc.model
			}
		}
	}
	b.metricsMu.Unlock()
}

// soonestCoolingExpiry answers the question the relay asks when a pick found NOTHING: is that
// because every otherwise-eligible station is cooling? It re-runs the same pick with the
// cooling filter lifted, excluding each cooling station it finds, and returns the soonest
// expiry among them (found=false when no cooling station would have been eligible - a real
// no-provider band). Caller holds b.mu.
func (b *broker) soonestCoolingExpiry(model string, confidentialOnly bool, minTPS, maxPriceIn, maxPriceOut float64, pin string, exclude, allow, privateAllow map[string]bool, req pickReq) (time.Time, bool) {
	b.metricsMu.Lock()
	none := len(b.cooling) == 0
	b.metricsMu.Unlock()
	if none {
		return time.Time{}, false
	}
	seen := make(map[string]bool, len(exclude)+4)
	for k := range exclude {
		seen[k] = true
	}
	req.allowCooling = true
	var soonest time.Time
	found := false
	for i := 0; i < len(b.nodes)+1; i++ {
		n, _, ok := b.pickFor(model, confidentialOnly, minTPS, maxPriceIn, maxPriceOut, pin, seen, allow, privateAllow, req)
		if !ok {
			break
		}
		seen[n.NodeID] = true
		if until, cooling := b.coolingUntil(n.NodeID); cooling && (!found || until.Before(soonest)) {
			soonest, found = until, true
		}
	}
	return soonest, found
}

// refuseBandCooling answers a request whose every eligible station is cooling: a fast, honest
// 503 with Retry-After = the soonest expiry - no hold, no receipt, no upstream call. Caller
// holds b.mu. Returns false when the band is not a cooling-only band.
func (b *broker) refuseBandCooling(w http.ResponseWriter, model string, confidentialOnly bool, minTPS, maxPriceIn, maxPriceOut float64, pin string, exclude, allow, privateAllow map[string]bool, req pickReq) bool {
	until, ok := b.soonestCoolingExpiry(model, confidentialOnly, minTPS, maxPriceIn, maxPriceOut, pin, exclude, allow, privateAllow, req)
	if !ok {
		return false
	}
	secs := int(math.Ceil(until.Sub(b.now()).Seconds()))
	if secs < 1 {
		secs = 1
	}
	b.stats.bandCooling503.Add(1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	jsonErr(w, http.StatusServiceUnavailable, fmt.Sprintf("band cooling - the station serving %s was rate limited upstream, retry after %ds", model, secs))
	return true
}

// retryAfterHint is the Retry-After the consumer sees on a final upstream 429/503: the
// station-reported value (capped like the cooldown), else the default cooldown.
func (b *broker) retryAfterHint(res protocol.JobResult) int {
	return max(1, int(b.cooldownFor(res.RetryAfterSec)/time.Second)) // never 0 or negative on the wire
}

// setRetryAfter stamps Retry-After on a consumer response whose final answer is a 429/503.
// Must run before WriteHeader.
func (b *broker) setRetryAfter(h http.Header, res protocol.JobResult) {
	if res.Status == http.StatusTooManyRequests || res.Status == http.StatusServiceUnavailable {
		h.Set("Retry-After", strconv.Itoa(b.retryAfterHint(res)))
	}
}

// --- observability ------------------------------------------------------------------------

// routingLive is the /admin/live routing block: the failover/cooldown counters and the
// stations cooling right now.
func (b *broker) routingLive() map[string]any {
	now := b.now()
	b.metricsMu.Lock()
	stations := make([]map[string]any, 0, len(b.cooling))
	for node, until := range b.cooling {
		if now.Before(until) {
			stations = append(stations, map[string]any{"node": node, "model": b.coolModel[node], "cooling_until": until.Unix()})
		}
	}
	b.metricsMu.Unlock()
	sort.Slice(stations, func(i, j int) bool { return stations[i]["node"].(string) < stations[j]["node"].(string) })
	return map[string]any{
		"relay_failovers":   b.stats.relayFailovers.Load(),
		"station_cooldowns": b.stats.stationCooldowns.Load(),
		"band_cooling_503":  b.stats.bandCooling503.Load(),
		"stations":          stations,
	}
}

// checkCoolingAlerts pages the founder ONCE when a station has been cooling for more than
// coolingAlertThreshold cumulative inside coolingAlertWindow (each cooldown was a real relay
// the upstream refused - the demand behind it), and clears once a whole window passes with
// no cooldown. Runs on the alert checker tick.
func (b *broker) checkCoolingAlerts(now time.Time) {
	type row struct {
		node, model string
		total       time.Duration
		count       int
	}
	var rows []row
	var quiet []string
	b.metricsMu.Lock()
	for node, evs := range b.coolEvents {
		evs = pruneCoolEvents(evs, now.Add(-coolingAlertWindow))
		if len(evs) == 0 {
			delete(b.coolEvents, node)
			quiet = append(quiet, node)
			continue
		}
		b.coolEvents[node] = evs
		r := row{node: node, model: b.coolModel[node], count: len(evs)}
		for _, e := range evs {
			r.total += e.added
		}
		rows = append(rows, r)
	}
	b.metricsMu.Unlock()
	for _, node := range quiet {
		b.alertClear("station_cooling:" + node)
	}
	for _, r := range rows {
		if r.total <= coolingAlertThreshold {
			continue
		}
		b.adminAlert("station_cooling:"+r.node, "station "+r.node+" keeps cooling on band "+r.model,
			"Station "+r.node+" keeps hitting its upstream rate limit",
			[][2]string{
				{"Station", r.node},
				{"Band", r.model},
				{"Cooldowns (last hour)", strconv.Itoa(r.count)},
				{"Cooling time (last hour)", r.total.Round(time.Second).String()},
			},
			"The provider behind this station keeps answering 429 under real demand; requests are being routed around it (or refused with a Retry-After when it is the only station). Not a strike - a capacity ceiling worth a look.")
	}
}
