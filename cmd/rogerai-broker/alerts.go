package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// alerts.go is the FOUNDER OPS ALERTS layer: operationally important conditions PAGE the
// founder (ADMIN_EMAIL) via the existing async mailer instead of being log-only. It is a
// thin, side-channel overlay on top of the money/relay path - it NEVER mutates state a
// request depends on and NEVER blocks or fails a request/checker.
//
// FAIL-SAFE: ADMIN_EMAIL unset => alerting is entirely OFF (zero behavior change). A mailer
// error is swallowed by the async mailer (log + move on), so an alert can never break the
// triggering operation.
//
// DEDUP: an alert fires ONCE on a condition's ONSET (a clear->fire transition) and never
// again while it stays fired; it re-fires only after it CLEARS and re-onsets. The onset is
// claimed in the SHARED store (alertstore.go: SETNX rogerai:alert:<key> with a TTL) so two
// instances page once, with the per-process map (alertFiring) as the fallback and the
// local mirror. The shared claim is a short LEASE (alertClaimTTL) that only an instance
// whose mirror says firing keeps alive each checker tick, so a claim orphaned by a restart
// expires within minutes; a model seen on air for the first time by a process also releases
// any claim a previous process left. A condition that stays fired for dedupTTL is re-paged
// once a day (a local timer, claimed once across instances via a separate repage key).
// Milestone alerts (first live top-up, first ban/dispute/report, first preserved CSAM
// incident) use a constant key, are never cleared and never re-paged: once per process.
//
// DELIVERY (features/ops/alert_delivery.feature): alerts raised inside one coalescing
// window become ONE digest email per recipient, sent on the mailer's ALERT lane behind any
// transactional mail (emailqueue.go paces + retries it). Every CSAM alert bypasses both:
// its own email, at once, on the priority lane. A deploy is not an outage: no noproviders
// page during the startup grace, and a model must be absent for debounceTicks consecutive
// ticks. A key that onsets flapCount times inside flapWindow is muted after one "flapping"
// page until it has stayed clear for flapQuiet, then summarized once.
//
// The alert email reuses the branded transactional shell (emailtemplates.go) with a clear
// "[RogerAI ALERT] ..." subject, the key facts as a receipt, and a CTA to the control panel.

// alertSubjectPrefix marks every ops alert so a filter/rule can route them.
const alertSubjectPrefix = "[RogerAI ALERT] "

// alertControlURL is the founder control panel the alert CTA links to.
const alertControlURL = "https://control.rogerai.fm"

// alertCheckInterval is how often the periodic checker re-evaluates the STATE/threshold
// conditions (0-providers, db/valkey health, CSAM SLA). Frequent enough to page promptly,
// cheap enough to run on a small instance (a market recompute + two health pings + one
// queue-stats read).
const alertCheckInterval = time.Minute

// defaultCSAMSLAHours is the age past which a still-queued CyberTipline report pages the
// founder (18 USC 2258A obligation). Override with ROGERAI_CSAM_SLA_HOURS.
const defaultCSAMSLAHours = 24

// driftEpsilon is the credit tolerance below which a balance-vs-derived difference is
// treated as float noise, not a real money invariant break. Real drift is materially
// larger than accumulated float rounding across a wallet's ledger rows.
const driftEpsilon = 1e-4

// alertClaimTTL is the shared onset claim's lease: a few checker intervals, refreshed each
// tick by any instance whose local mirror says the condition is firing (heartbeatClaims).
// An orphan (its owner restarted, the model recovered while nobody was looking) therefore
// expires within minutes instead of silencing the next real onset for a day.
const alertClaimTTL = 3 * alertCheckInterval

// alertMilestoneKeys are once-per-process-lifetime pages ("first ..."): they are never
// cleared and never re-paged after dedupTTL - a second "first ban" is a lie.
var alertMilestoneKeys = map[string]bool{
	"first_ban": true, "first_dispute": true, "first_report": true, "first_live_topup": true,
	"csam:first-report": true,
}

// alertUrgent reports whether key rides the priority lane at once, never coalesced: every
// CSAM alert (the SLA breach AND the first preserved incident), keyed on the prefix so a new
// csam:* condition cannot quietly land behind a digest window.
func alertUrgent(key string) bool { return strings.HasPrefix(key, "csam") }

// alertConfig holds the delivery knobs (ROGERAI_ALERT_*). The zero value is the plainest
// behavior (page at once, no grace, first-tick, no flap muting) so a hand-built broker in a
// unit test keeps the raw onset semantics; loadAlertConfig applies the production defaults.
type alertConfig struct {
	coalesce      time.Duration // digest window; <=0 = each alert is its own email at once
	grace         time.Duration // no noproviders page this long after boot; <=0 = none
	debounceTicks int           // consecutive absent ticks before noproviders pages; <=1 = first
	flapCount     int           // onsets inside flapWindow that mute a key; <=0 = off
	flapWindow    time.Duration
	flapQuiet     time.Duration // clear this long lifts a mute (with one summary)
	dedupTTL      time.Duration // onset claim TTL (shared + local); <=0 = 24h
}

// loadAlertConfig reads the ROGERAI_ALERT_* knobs with the spec's defaults.
func loadAlertConfig() alertConfig {
	return alertConfig{
		coalesce:      envDuration("ROGERAI_ALERT_COALESCE", 5*time.Second),
		grace:         envDuration("ROGERAI_ALERT_GRACE", 120*time.Second),
		debounceTicks: envInt("ROGERAI_ALERT_DEBOUNCE_TICKS", 2),
		flapCount:     envInt("ROGERAI_ALERT_FLAP_COUNT", 3),
		flapWindow:    envDuration("ROGERAI_ALERT_FLAP_WINDOW", time.Hour),
		flapQuiet:     envDuration("ROGERAI_ALERT_FLAP_QUIET", 30*time.Minute),
		dedupTTL:      envDuration("ROGERAI_ALERT_DEDUP_TTL", 24*time.Hour),
	}
}

// alertCondition is one fired condition awaiting (or inside) a digest.
type alertCondition struct {
	key, tail, heading string
	rows               [][2]string
	body               string
}

// flapState is a key's per-process flap bookkeeping: onsets seen (mirrors the shared count),
// whether it is muted, when it last cleared (the quiet clock), and the local fixed window
// used only when the shared counter is unavailable.
type flapState struct {
	onsets    int  // onsets counted in this window (shared total, or this process's own)
	muted     bool // the flapping page went out; further onsets are silent until quiet
	lastOnset time.Time
	clearedAt time.Time
}

// alertClock / alertTimer are the clock seam for grace, debounce, flap windows and the
// coalescing timer (nil = real time), mirroring the mailer's.
func (b *broker) alertClock() time.Time {
	if b.alertNow != nil {
		return b.alertNow()
	}
	return time.Now()
}

func (b *broker) alertTimer(d time.Duration) <-chan time.Time {
	if b.alertAfter != nil {
		return b.alertAfter(d)
	}
	return time.After(d)
}

func (b *broker) dedupTTL() time.Duration {
	if b.alertCfg.dedupTTL > 0 {
		return b.alertCfg.dedupTTL
	}
	return 24 * time.Hour
}

// alertInitLocked lazily creates the alert maps (hand-built brokers leave them nil).
func (b *broker) alertInitLocked() {
	if b.alertFiring == nil {
		b.alertFiring = map[string]bool{}
	}
	if b.alertFiredAt == nil {
		b.alertFiredAt = map[string]time.Time{}
	}
	if b.alertAbsent == nil {
		b.alertAbsent = map[string]int{}
	}
	if b.alertFlap == nil {
		b.alertFlap = map[string]*flapState{}
	}
	if b.alertOnAirSeen == nil {
		b.alertOnAirSeen = map[string]bool{}
	}
}

// alertStats is the /admin/live block for the alert layer.
func (b *broker) alertStats() map[string]any {
	return map[string]any{
		"alerts_coalesced": b.alertCoalesced.Load(),
		"alerts_deduped":   b.alertDeduped.Load(),
		"alerts_muted":     b.alertMuted.Load(),
	}
}

// parseAdminEmails splits ADMIN_EMAIL into a trimmed recipient list, dropping blanks. An
// unset/blank/comma-only value yields nil, which turns alerting entirely OFF (fail-safe).
func parseAdminEmails(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if e := strings.TrimSpace(part); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// csamSLAHoursEnv reads ROGERAI_CSAM_SLA_HOURS (>0), else the default.
func csamSLAHoursEnv() int {
	if v := os.Getenv("ROGERAI_CSAM_SLA_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultCSAMSLAHours
}

// alertingOn reports whether founder alerting is enabled (at least one ADMIN_EMAIL
// recipient is configured).
func (b *broker) alertingOn() bool { return len(b.adminEmails) > 0 }

// adminAlert fires ONE founder ops alert for `key` on the CLEAR->FIRE transition only
// (onset dedup, claimed across instances), delivering to EVERY ADMIN_EMAIL recipient via
// the paced alert lane, coalesced with any other alert raised inside the same window. It is
// a no-op when alerting is off (no recipients), the condition is already firing, a peer
// instance already paged this onset, or the key is muted for flapping. It never blocks and
// never errors: the mailer queue is async and failure-swallowing.
//
//	key        - the dedup key for this condition instance (e.g. "noproviders:<model>")
//	subjectTail- appended to the "[RogerAI ALERT] " subject prefix
//	heading    - the human headline in the email body
//	rows       - key/value facts rendered as a receipt
//	body       - a short plain sentence of context
func (b *broker) adminAlert(key, subjectTail, heading string, rows [][2]string, body string) {
	if !b.alertingOn() {
		return // fail-safe: ADMIN_EMAIL unset => alerting entirely OFF
	}
	now := b.alertClock()
	b.alertMu.Lock()
	b.alertInitLocked()
	repage := b.alertFiring[key]
	if repage && (alertMilestoneKeys[key] || now.Sub(b.alertFiredAt[key]) < b.dedupTTL()) {
		b.alertMu.Unlock()
		return // already firing on this onset - dedup, do not re-page
	}
	b.alertFiring[key] = true
	b.alertFiredAt[key] = now
	b.alertMu.Unlock()

	// Everything past the local mark talks to the shared store (two bounded round trips)
	// and the mailer; it runs on its own goroutine so an onset raised from a request path
	// (/billing drift, /report, a webhook, a strike) never adds to that request's latency.
	// alertInflight lets shutdown (and the tests' quiescence check) wait for it.
	b.alertInflight.Add(1)
	go func() {
		defer b.alertInflight.Add(-1)
		b.alertOnset(key, subjectTail, heading, rows, body, now, repage)
	}()
}

// alertOnset is the off-request half of adminAlert: claim the onset across instances (or,
// for a condition fired longer than dedupTTL, the daily re-page), apply flap suppression,
// then send at once or coalesce into the open digest window.
func (b *broker) alertOnset(key, subjectTail, heading string, rows [][2]string, body string, now time.Time, repage bool) {
	claimed := b.sharedOnset
	if repage {
		claimed = b.sharedRepage
	}
	if !claimed(key) {
		b.alertDeduped.Add(1)
		log.Printf("alert: DEDUPED %q (a peer instance already paged this onset)", key)
		return
	}
	// An urgent (csam-prefixed) key never enters flap accounting: csam_sla clears when the
	// queue drains and re-fires on the next breach, and a legal-obligation page must not be
	// muted for 30 minutes because it happened three times in an hour.
	urgent := alertUrgent(key)
	var suffix string
	if !urgent {
		var muted bool
		if suffix, muted = b.flapOnset(key, now); muted {
			b.alertMuted.Add(1)
			log.Printf("alert: MUTED (flapping) %s", key)
			return
		}
	}
	log.Printf("alert: FIRED %q -> %d recipient(s): %s", key, len(b.adminEmails), subjectTail)

	cond := alertCondition{key: key, tail: subjectTail + suffix, heading: heading, rows: rows, body: body}
	if urgent || b.alertCfg.coalesce <= 0 {
		b.sendAlertDigest([]alertCondition{cond}, urgent)
		return
	}
	b.alertMu.Lock()
	b.alertPending = append(b.alertPending, cond)
	if b.alertFlushArmed {
		b.alertCoalesced.Add(1)
		b.alertMu.Unlock()
		return
	}
	b.alertFlushArmed = true
	b.alertMu.Unlock()
	window := b.alertCfg.coalesce
	go func() {
		<-b.alertTimer(window)
		// Tracked from the moment it flushes: shutdown waits for a flush in progress
		// (shutdownAlerts flushes the still-open window itself, synchronously).
		b.alertInflight.Add(1)
		defer b.alertInflight.Add(-1)
		b.flushAlerts()
	}()
}

// flushAlerts sends everything the coalescing window collected as ONE digest, ordered by
// key so the subject's "first" condition does not depend on which onset goroutine won.
func (b *broker) flushAlerts() {
	b.alertMu.Lock()
	conds := b.alertPending
	b.alertPending = nil
	b.alertFlushArmed = false
	b.alertMu.Unlock()
	if len(conds) > 0 {
		sort.SliceStable(conds, func(i, j int) bool { return conds[i].key < conds[j].key })
		b.sendAlertDigest(conds, false)
	}
}

// waitAlertsInflight blocks (bounded) until every onset goroutine (and any flush in
// progress) has finished, so a page raised right before shutdown still reaches the mail
// queue before it drains.
func (b *broker) waitAlertsInflight(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for b.alertInflight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// shutdownAlerts is the stop-signal sequence for the alert layer, run BEFORE the mailer
// drains: wait for the in-flight onsets, then flush the still-open coalescing window at
// once rather than waiting out the rest of it. Whatever the mailer cannot then deliver
// inside its drain budget is counted dropped{shutdown} and logged - never silently gone.
func (b *broker) shutdownAlerts(timeout time.Duration) {
	b.waitAlertsInflight(timeout)
	b.flushAlerts()
}

// sendAlertDigest renders one email per recipient for the given conditions (a single
// condition keeps the classic subject; several get "N conditions: <first> (+N-1 more)" and
// a body listing each condition's facts). urgent rides the transactional lane.
func (b *broker) sendAlertDigest(conds []alertCondition, urgent bool) {
	n := len(conds)
	subj := alertSubjectPrefix + conds[0].tail
	heading := conds[0].heading
	if n > 1 {
		subj = alertSubjectPrefix + fmt.Sprintf("%d conditions: %s (+%d more)", n, conds[0].tail, n-1)
		heading = fmt.Sprintf("%d conditions fired together", n)
	}
	var htmlB, textB strings.Builder
	for i, c := range conds {
		if n > 1 {
			htmlB.WriteString(`<p style="margin:0 0 6px;font-weight:bold;">` + esc(c.heading) + `</p>`)
			textB.WriteString(strings.ToUpper(c.heading) + "\n")
		}
		htmlB.WriteString(receipt("", c.rows) + p(esc(c.body)))
		textB.WriteString(alertText(c.rows, c.body))
		if i < n-1 {
			textB.WriteString("\n\n")
		}
	}
	d := emailDoc{
		kicker:    "Ops alert",
		heading:   heading,
		preheader: conds[0].tail,
		bodyHTML:  htmlB.String(),
		bodyText:  textB.String(),
		ctaLabel:  "Open control",
		ctaHref:   alertControlURL,
	}
	htmlBody, textBody := renderHTML(d), renderText(d)
	// Deliver to every recipient through the paced queue: a slow/broken provider can never
	// block or fail the alert path, and a burst can never exceed the provider cap.
	for _, to := range b.adminEmails {
		if urgent {
			b.mail.sendEmail(to, subj, htmlBody, textBody)
		} else {
			b.mail.sendAlertEmail(to, subj, htmlBody, textBody)
		}
	}
}

// flapOnset counts one onset of key inside the flap window (shared counter, per-process
// window as fallback) and decides: page normally, page once more with the "flapping"
// suffix (the flapCount-th onset), or mute. The mute lifts only via checkFlapStabilized.
func (b *broker) flapOnset(key string, now time.Time) (suffix string, muted bool) {
	cfg := b.alertCfg
	if cfg.flapCount <= 0 {
		return "", false
	}
	// 0 when the shared counter is unreachable (or unconfigured): the local count carries.
	// sharedFlapIncr has already logged the fallback.
	n, _ := b.sharedFlapIncr(key, cfg.flapWindow)
	b.alertMu.Lock()
	defer b.alertMu.Unlock()
	fs := b.alertFlap[key]
	if fs == nil {
		fs = &flapState{}
		b.alertFlap[key] = fs
	}
	// A whole window with no onset in it has rolled: a key that blips once a day is not
	// flapping, so counting starts over. A MUTED key is left alone - only a quiet spell
	// lifts a mute (checkFlapStabilized), and it sends the summary when it does.
	if !fs.muted && !fs.lastOnset.IsZero() && now.Sub(fs.lastOnset) >= cfg.flapWindow {
		fs.onsets = 0
	}
	// THE COUNT NEVER GOES BACKWARDS ON THIS INSTANCE: the shared total when the store
	// answered (so a peer's onsets count too), else one more than this process has already
	// seen. Restarting the count on a failed round trip is what bought a flapping key three
	// fresh pages - CI caught it as a fourth page (features/ops/alert_delivery.feature).
	count := max(n, fs.onsets+1)
	fs.onsets, fs.lastOnset, fs.clearedAt = count, now, time.Time{}
	switch {
	case fs.muted:
		return "", true
	case count == cfg.flapCount:
		fs.muted = true
		return fmt.Sprintf(" (flapping - further onsets muted until %s quiet)", shortDuration(cfg.flapQuiet)), false
	case count > cfg.flapCount:
		fs.muted = true // a peer sent the flapping page; this instance just joins the mute
		return "", true
	}
	return "", false
}

// checkFlapStabilized lifts the mute of every key that has stayed clear for flapQuiet and
// sends ONE "stabilized after N onsets" summary per recipient (claimed across instances).
// It also forgets never-muted keys whose last onset is older than the flap window, so the
// table cannot grow with every model that ever blipped.
func (b *broker) checkFlapStabilized(now time.Time) {
	quiet, window := b.alertCfg.flapQuiet, b.alertCfg.flapWindow
	if quiet <= 0 {
		return
	}
	type done struct {
		key    string
		onsets int
	}
	var lifted []done
	b.alertMu.Lock()
	for key, fs := range b.alertFlap {
		switch {
		case fs.muted && !b.alertFiring[key] && !fs.clearedAt.IsZero() && now.Sub(fs.clearedAt) >= quiet:
			lifted = append(lifted, done{key, fs.onsets})
			delete(b.alertFlap, key)
		case !fs.muted && window > 0 && now.Sub(fs.lastOnset) >= window:
			delete(b.alertFlap, key)
		}
	}
	b.alertMu.Unlock()
	for _, l := range lifted {
		b.sharedFlapReset(l.key)
		if !b.sharedStableOnce(l.key, quiet) {
			continue
		}
		log.Printf("alert: STABILIZED %s after %d onsets (quiet %s) - mute lifted", l.key, l.onsets, shortDuration(quiet))
		b.sendAlertDigest([]alertCondition{{
			key:     l.key,
			tail:    fmt.Sprintf("%s stabilized after %d onsets", l.key, l.onsets),
			heading: "A flapping condition has stabilized",
			rows: [][2]string{
				{"Condition", l.key},
				{"Onsets", strconv.Itoa(l.onsets)},
				{"Quiet for", shortDuration(quiet)},
			},
			body: "The condition bounced repeatedly (muted after the flapping page) and has now stayed clear for the quiet window. The mute is lifted; its next onset pages normally.",
		}}, false)
	}
}

// shortDuration renders 30m0s as "30m" and 1h0m0s as "1h" (a subject line, not a stopwatch).
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// alertClear marks a condition resolved so a later re-onset re-fires: the local mirror
// and the shared onset key are both dropped. A no-op when alerting is off or the condition
// was not firing.
func (b *broker) alertClear(key string) {
	if !b.alertingOn() {
		return
	}
	now := b.alertClock()
	b.alertMu.Lock()
	was := b.alertFiring[key]
	delete(b.alertFiring, key)
	delete(b.alertFiredAt, key)
	if fs := b.alertFlap[key]; fs != nil && fs.muted {
		fs.clearedAt = now
	}
	b.alertMu.Unlock()
	if was {
		b.sharedClear(key)
		log.Printf("alert: CLEARED %q", key)
	}
}

// alertText renders the plain-text body for an alert: the facts as "LABEL: value" lines
// followed by the context sentence.
func alertText(rows [][2]string, body string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r[0] + ": " + r[1] + "\n")
	}
	if len(rows) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(body)
	return b.String()
}

// ---- periodic checker ---------------------------------------------------------

// alertCheckerLoop periodically re-evaluates the STATE/threshold alert conditions (a live
// model dropping to 0 providers, db/Valkey unreachable, a CSAM item past its SLA) and pages
// the founder on each condition's onset. It is a single small goroutine, started only when
// alerting is on. stop is the nil-in-production test seam (a nil channel case never fires,
// so the loop waits on the ticker exactly as the other sweeps do).
func (b *broker) alertCheckerLoop(stop <-chan struct{}) {
	if !b.alertingOn() {
		log.Printf("alerts: ADMIN_EMAIL unset - founder ops alerts DISABLED (log-only)")
		return
	}
	log.Printf("alerts: ON - founder ops alerts to %d recipient(s) (checker every %s, CSAM SLA %dh)", len(b.adminEmails), alertCheckInterval, b.csamSLAHours)
	t := time.NewTicker(alertCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.alertCheckOnce(time.Now())
		}
	}
}

// alertCheckOnce runs one pass of every periodic (state-derived) alert check. Split out of
// the loop so it is testable without the ticker.
func (b *broker) alertCheckOnce(now time.Time) {
	b.checkHealthAlerts()
	b.checkProviderGapAlerts()
	b.checkCSAMSLAAlert(now)
	b.checkFlapStabilized(now)
	b.retryPendingClears()
	b.heartbeatClaims()
	b.checkCoolingAlerts(now)
}

// checkHealthAlerts pages when the durable store (Postgres) or the optional shared state
// layer (Valkey) is unreachable, and clears when it recovers. The shared layer is only a
// dependency when configured (nil = unconfigured = never alerts).
func (b *broker) checkHealthAlerts() {
	// Durable store.
	if b.db == nil {
		b.adminAlert("db_down", "broker database unreachable", "Broker database is unreachable",
			[][2]string{{"Component", "database"}, {"Status", "nil handle"}},
			"The broker has no durable store handle - it cannot serve money/ledger operations.")
	} else if err := b.db.Healthy(); err != nil {
		b.adminAlert("db_down", "broker database unreachable", "Broker database is unreachable",
			[][2]string{{"Component", "database (Postgres)"}, {"Error", err.Error()}},
			"The durable store failed its health ping - the broker is degraded/unhealthy.")
	} else {
		b.alertClear("db_down")
	}

	// Optional shared state layer (Valkey): only a dependency when wired.
	if b.shared != nil {
		if b.shared.healthy() {
			b.alertClear("valkey_down")
		} else {
			b.adminAlert("valkey_down", "shared state (Valkey) unreachable", "Shared state layer (Valkey) is unreachable",
				[][2]string{{"Component", "Valkey / shared store"}, {"Status", "unreachable"}},
				"The shared state layer failed its health check - cross-instance rate-limit/liveness sharing is degraded.")
		}
	}
}

// checkProviderGapAlerts pages when a model that WAS on air drops to 0 providers (a supply
// gap), and clears when supply returns. It tracks every model ever seen on air so the
// drop-to-zero transition is detectable even though a 0-provider model no longer appears in
// the market view. A deploy is not an outage: nothing pages inside the startup grace, and a
// model must be absent for debounceTicks consecutive ticks (one heartbeat gap is not a gap
// in supply). Health and CSAM alerts are exempt from both. A model this process sees on
// air for the FIRST time (grace included) releases any shared claim a previous process
// left on it: the old process may have paged the drop and died before the recovery, and
// nobody else would ever DEL that claim.
func (b *broker) checkProviderGapAlerts() {
	now := b.alertClock()
	nowOnAir := b.liveModelProviders() // model -> provider count (every entry >= 1)

	b.alertMu.Lock()
	b.alertInitLocked()
	var fresh []string
	for model := range nowOnAir {
		if !b.alertOnAirSeen[model] {
			fresh = append(fresh, model)
		}
		b.alertOnAirSeen[model] = true
	}
	grace := b.alertCfg.grace
	inGrace := grace > 0 && now.Sub(b.startTime) < grace
	need := b.alertCfg.debounceTicks
	if need < 1 {
		need = 1
	}
	var restored, dropped []string
	for model := range b.alertOnAirSeen {
		if inGrace {
			break
		}
		if _, ok := nowOnAir[model]; ok {
			b.alertAbsent[model] = 0
			restored = append(restored, model)
			continue
		}
		b.alertAbsent[model]++
		if b.alertAbsent[model] >= need {
			dropped = append(dropped, model)
		}
	}
	b.alertMu.Unlock()
	sort.Strings(fresh)
	sort.Strings(restored)
	sort.Strings(dropped) // a stable first condition in the digest subject

	for _, model := range fresh {
		b.sharedDel("noproviders:"+model, "alertRelease") // best effort; the lease is the backstop
	}
	if inGrace {
		b.alertGraceOnce.Do(func() {
			log.Printf("alerts: in startup grace (%s window after boot) - noproviders paging suppressed while stations re-register", shortDuration(grace))
		})
		return
	}

	// Clear first (a model back on air), then fire the drops. adminAlert/alertClear take
	// alertMu themselves, so this runs outside the lock above.
	for _, model := range restored {
		b.alertClear("noproviders:" + model)
	}
	for _, model := range dropped {
		b.adminAlert("noproviders:"+model, "model "+model+" has 0 providers",
			"Model "+model+" dropped to 0 providers",
			[][2]string{{"Model", model}, {"Providers", "0"}},
			"This model was on air and now has no provider serving it - a supply gap. Requests for it will fail until a provider returns.")
	}
}

// liveModelProviders returns the count of on-air providers per model, derived from the SAME
// aggregation /market serves (respecting node TTL, bans, and private bands). Only models
// with at least one live provider appear, so a model absent from the result is off air.
func (b *broker) liveModelProviders() map[string]int {
	out := map[string]int{}
	res, ok := b.computeMarket().(map[string]any)
	if !ok {
		return out
	}
	views, ok := res["market"].([]marketView)
	if !ok {
		return out
	}
	for _, v := range views {
		// Curated stations SERVE REQUESTS: a model whose only live supply is a curated
		// proxy is on air, and paging "0 providers / requests will fail" over it is a
		// false alarm that also hides a real curated-supply outage. The market view
		// counts the two apart (an honesty rule for the dial); the pager's question is
		// "will a request fail?", so here they add up.
		if n := v.Providers + v.CuratedProviders; n > 0 {
			out[v.Model] = n
		}
	}
	return out
}

// checkCSAMSLAAlert pages when a preserved CSAM incident still owes a CyberTipline report
// past the SLA threshold (a legal-obligation escalation), and clears when the queue drains
// or the oldest item is back within SLA.
func (b *broker) checkCSAMSLAAlert(now time.Time) {
	if b.db == nil {
		return
	}
	depth, oldestAgeSecs, err := b.db.CSAMQueueStats(now)
	if err != nil {
		return // a transient store error is handled by the db-down health check
	}
	slaSecs := int64(b.csamSLAHours) * 3600
	if depth > 0 && oldestAgeSecs >= slaSecs {
		b.adminAlert("csam_sla", "CSAM report past SLA", "A CSAM report is past its filing SLA",
			[][2]string{
				{"Queue depth", strconv.Itoa(depth)},
				{"Oldest queued", strconv.FormatInt(oldestAgeSecs/3600, 10) + "h"},
				{"SLA", strconv.Itoa(b.csamSLAHours) + "h"},
			},
			"A preserved CSAM incident still owes a CyberTipline report past the SLA (18 USC 2258A). Drain via /admin/csam.")
	} else {
		b.alertClear("csam_sla")
	}
}

// ---- milestone / event alerts -------------------------------------------------

// alertFirstBan pages the founder on the FIRST account/node ban of this process lifetime (a
// safety escalation). Deduped on a constant key, so only the first ban ever pages.
func (b *broker) alertFirstBan(what, subject, evidence string) {
	b.adminAlert("first_ban", "first ban - "+subject, "First "+what+" ban",
		[][2]string{{"Subject", subject}, {"Evidence", evidence}},
		"The first ban of this broker's lifetime was just applied. Confirm it looks right.")
}

// alertFirstDispute pages the founder on the FIRST Stripe charge dispute (chargeback) of
// this process lifetime.
func (b *broker) alertFirstDispute(disputeID string, amountCredits float64) {
	b.adminAlert("first_dispute", "first charge dispute "+disputeID, "First charge dispute opened",
		[][2]string{{"Dispute", disputeID}, {"Amount", fmt.Sprintf("$%.2f", round6(amountCredits*b.bill.creditUSD))}},
		"A consumer opened the first chargeback dispute against a funding charge. Review the lineage clawback.")
}

// alertFirstReport pages the founder on the FIRST safety report (abuse/CSAM) of this process
// lifetime.
func (b *broker) alertFirstReport(category, nodeID string) {
	b.adminAlert("first_report", "first "+category+" report", "First "+category+" report received",
		[][2]string{{"Category", category}, {"Node", nodeID}},
		"The first safety report of this broker's lifetime just came in.")
}

// alertFirstLiveTopup pages the founder on the FIRST REAL (live-mode) Stripe top-up - the
// billing-works-end-to-end milestone. Only ever called on the sk_live path.
func (b *broker) alertFirstLiveTopup(user string, credits, newBalance float64) {
	b.adminAlert("first_live_topup", "first LIVE Stripe top-up", "First live Stripe top-up landed",
		[][2]string{
			{"Wallet", user},
			{"Amount", fmt.Sprintf("$%.2f", round6(credits*b.bill.creditUSD))},
			{"New balance", fmt.Sprintf("%.4f credits", newBalance)},
		},
		"The first real (live-mode) top-up credited a wallet - billing works end to end.")
}

// checkDriftAlert compares a wallet's cached balance against its independently re-derived
// ledger sum (the existing verify-vs-balance drift check) and pages when they diverge past
// the float-noise epsilon (a money invariant broke). Clears when they reconcile. Called from
// the /billing handler where both figures are already computed - no extra store reads.
func (b *broker) checkDriftAlert(user string, balance, derived float64) {
	if !b.alertingOn() {
		return
	}
	delta := balance - derived
	if delta < 0 {
		delta = -delta
	}
	key := "drift:" + user
	if delta > driftEpsilon {
		b.adminAlert(key, "ledger drift on wallet "+user, "Ledger drift detected",
			[][2]string{
				{"Wallet", user},
				{"Cached balance", fmt.Sprintf("%.6f", balance)},
				{"Derived (ledger sum)", fmt.Sprintf("%.6f", derived)},
				{"Delta", fmt.Sprintf("%.6f", delta)},
			},
			"A wallet's cached balance diverged from its re-derived ledger sum - a money invariant broke. Investigate before payouts.")
	} else {
		b.alertClear(key)
	}
}
