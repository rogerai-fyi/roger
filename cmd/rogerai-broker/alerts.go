package main

import (
	"fmt"
	"log"
	"os"
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
// again while it stays fired; it re-fires only after it CLEARS and re-onsets. State lives
// in memory (alertFiring), so it resets on restart - acceptable for an ops page (a still-true
// condition simply re-pages once after a redeploy). Milestone alerts (first live top-up,
// first ban/dispute/report) use a constant key and are never cleared, so they fire exactly
// once per process lifetime.
//
// The alert email reuses the branded transactional shell (emailtemplates.go) with a clear
// "[RogerAI ALERT] ..." subject, the key facts as a receipt, and a CTA to the control panel.

// alertSubjectPrefix marks every ops alert so a filter/rule can route them.
const alertSubjectPrefix = "[RogerAI ALERT] "

// alertControlURL is the founder control panel the alert CTA links to.
const alertControlURL = "https://control.rogerai.fyi"

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
// (onset dedup), delivering to EVERY ADMIN_EMAIL recipient. It is a no-op when alerting is
// off (no recipients) or the condition is already firing. It never blocks and never errors:
// the underlying mailer is async and swallows failures.
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
	b.alertMu.Lock()
	if b.alertFiring == nil {
		b.alertFiring = map[string]bool{}
	}
	if b.alertFiring[key] {
		b.alertMu.Unlock()
		return // already firing on this onset - dedup, do not re-page
	}
	b.alertFiring[key] = true
	b.alertMu.Unlock()

	subj := alertSubjectPrefix + subjectTail
	d := emailDoc{
		kicker:    "Ops alert",
		heading:   heading,
		preheader: subjectTail,
		bodyHTML:  receipt("", rows) + p(esc(body)),
		bodyText:  alertText(rows, body),
		ctaLabel:  "Open control",
		ctaHref:   alertControlURL,
	}
	htmlBody, textBody := renderHTML(d), renderText(d)
	// Deliver to every recipient. Each send is async + failure-swallowing (email.go), so a
	// slow/broken recipient can never block or fail the alert path.
	for _, to := range b.adminEmails {
		b.mail.sendEmail(to, subj, htmlBody, textBody)
	}
	log.Printf("alert: FIRED %q -> %d recipient(s): %s", key, len(b.adminEmails), subjectTail)
}

// alertClear marks a condition resolved so a later re-onset re-fires. A no-op when alerting
// is off or the condition was not firing.
func (b *broker) alertClear(key string) {
	if !b.alertingOn() {
		return
	}
	b.alertMu.Lock()
	was := b.alertFiring[key]
	delete(b.alertFiring, key)
	b.alertMu.Unlock()
	if was {
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
// the market view.
func (b *broker) checkProviderGapAlerts() {
	nowOnAir := b.liveModelProviders() // model -> provider count (every entry >= 1)

	b.alertMu.Lock()
	if b.alertOnAirSeen == nil {
		b.alertOnAirSeen = map[string]bool{}
	}
	for model := range nowOnAir {
		b.alertOnAirSeen[model] = true
	}
	var restored, dropped []string
	for model := range b.alertOnAirSeen {
		if _, ok := nowOnAir[model]; ok {
			restored = append(restored, model)
		} else {
			dropped = append(dropped, model)
		}
	}
	b.alertMu.Unlock()

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
		if v.Providers > 0 {
			out[v.Model] = v.Providers
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
