package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// alerts_test.go is the spec for the FOUNDER OPS ALERTS layer (alerts.go): operationally
// important conditions page ADMIN_EMAIL via the existing async mailer instead of being
// log-only. The invariants under test:
//   - each condition FIRES exactly one email (per recipient) on its ONSET,
//   - a repeat of the same still-firing condition does NOT refire (dedup),
//   - a refire happens after the condition CLEARS and re-onsets,
//   - NOTHING fires when ADMIN_EMAIL is unset (alerting entirely OFF),
//   - a mailer error is swallowed (never panics / blocks the checker or request path),
//   - a comma-separated ADMIN_EMAIL delivers to EVERY address.
// The mailer is injected as a recording seam (no real emails, no network).

// alertBroker builds a broker with a recording mailer + the given admin recipients wired
// for alerting. sends receives one payload per delivered email (async).
func alertBroker(t *testing.T, recipients ...string) (*broker, chan map[string]any) {
	t.Helper()
	b := relayBroker(store.NewMem())
	sends := make(chan map[string]any, 64)
	b.mail = enabledMailer(func(r *http.Request) (*http.Response, error) {
		by, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(by, &payload)
		sends <- payload
		return &http.Response{StatusCode: 200, Body: io.NopCloser(emptyBody{})}, nil
	})
	b.adminEmails = recipients
	b.alertFiring = map[string]bool{}
	b.alertOnAirSeen = map[string]bool{}
	b.csamSLAHours = 24
	return b, sends
}

// alertRecipients collects the single "to" recipient of each delivered email within wait.
func alertRecipients(sends chan map[string]any, want int, wait time.Duration) []string {
	var got []string
	deadline := time.After(wait)
	for len(got) < want {
		select {
		case p := <-sends:
			if to, ok := p["to"].([]any); ok && len(to) == 1 {
				if s, ok := to[0].(string); ok {
					got = append(got, s)
				}
			}
		case <-deadline:
			return got
		}
	}
	return got
}

// firstAlert returns the first delivered email payload, or fails.
func firstAlert(t *testing.T, sends chan map[string]any) map[string]any {
	t.Helper()
	select {
	case p := <-sends:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("expected an alert email, none was sent")
		return nil
	}
}

// noAlert asserts nothing was delivered within a short window.
func noAlert(t *testing.T, sends chan map[string]any) {
	t.Helper()
	select {
	case p := <-sends:
		t.Fatalf("expected NO alert, but one was sent: subj=%v", p["subject"])
	case <-time.After(150 * time.Millisecond):
	}
}

// TestCSAMSLAHoursEnv covers the SLA env override + default fallback.
func TestCSAMSLAHoursEnv(t *testing.T) {
	t.Setenv("ROGERAI_CSAM_SLA_HOURS", "48")
	if got := csamSLAHoursEnv(); got != 48 {
		t.Errorf("csamSLAHoursEnv() = %d, want 48", got)
	}
	t.Setenv("ROGERAI_CSAM_SLA_HOURS", "bogus")
	if got := csamSLAHoursEnv(); got != defaultCSAMSLAHours {
		t.Errorf("csamSLAHoursEnv() with bogus = %d, want default %d", got, defaultCSAMSLAHours)
	}
	t.Setenv("ROGERAI_CSAM_SLA_HOURS", "0") // non-positive rejected -> default
	if got := csamSLAHoursEnv(); got != defaultCSAMSLAHours {
		t.Errorf("csamSLAHoursEnv() with 0 = %d, want default %d", got, defaultCSAMSLAHours)
	}
}

// TestAlertCheckOnceHealthyBrokerIsQuiet proves the periodic pass fires nothing on a
// healthy, empty broker (all three checks clear) and exercises the aggregate entrypoint.
func TestAlertCheckOnceHealthyBrokerIsQuiet(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	b.alertCheckOnce(time.Now())
	noAlert(t, sends)
}

// TestAlertCheckerLoopDisabledReturns proves the checker goroutine is a no-op (returns
// immediately) when alerting is OFF, and honors the stop seam when ON.
func TestAlertCheckerLoopDisabledReturns(t *testing.T) {
	// OFF: no recipients -> returns at once.
	off, _ := alertBroker(t)
	done := make(chan struct{})
	go func() { off.alertCheckerLoop(nil); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("alertCheckerLoop must return immediately when alerting is OFF")
	}

	// ON: honors a closed stop channel.
	on, _ := alertBroker(t, "ops@example.com")
	stop := make(chan struct{})
	close(stop)
	done2 := make(chan struct{})
	go func() { on.alertCheckerLoop(stop); close(done2) }()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("alertCheckerLoop must return when stop is closed")
	}
}

// TestHealthAndCSAMNilDB proves the checks are nil-db safe: a nil durable store pages
// db_down (health) and never panics (CSAM SLA just returns).
func TestHealthAndCSAMNilDB(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	b.db = nil
	b.shared = nil
	b.checkHealthAlerts()
	got := firstAlert(t, sends)
	if !strings.Contains(strings.ToLower(got["subject"].(string)), "database") {
		t.Errorf("nil-db subject = %v", got["subject"])
	}
	b.checkCSAMSLAAlert(time.Now()) // must not panic with a nil store
	noAlert(t, sends)
}

// TestParseAdminEmails covers the comma-separated recipient parse: trimmed, empties
// dropped, unset/blank => nil (alerting OFF).
func TestParseAdminEmails(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{" , ,", nil},
		{"a@b.com", []string{"a@b.com"}},
		{"a@b.com,c@d.com", []string{"a@b.com", "c@d.com"}},
		{" a@b.com , c@d.com ,e@f.com ", []string{"a@b.com", "c@d.com", "e@f.com"}},
		{"a@b.com,,c@d.com", []string{"a@b.com", "c@d.com"}},
	}
	for _, tc := range cases {
		got := parseAdminEmails(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseAdminEmails(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseAdminEmails(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// TestAdminAlertFiresOnceDedupsAndRefires is the core onset/dedup/refire contract.
func TestAdminAlertFiresOnceDedupsAndRefires(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")

	b.adminAlert("cond:x", "thing broke", "Thing broke", [][2]string{{"Key", "val"}}, "the body")
	got := firstAlert(t, sends)
	subj, _ := got["subject"].(string)
	if !strings.HasPrefix(subj, "[RogerAI ALERT] ") {
		t.Errorf("subject = %q, want [RogerAI ALERT] prefix", subj)
	}
	if !strings.Contains(subj, "thing broke") {
		t.Errorf("subject = %q, want the condition tail", subj)
	}
	htmlBody, _ := got["html"].(string)
	if !strings.Contains(htmlBody, "control.rogerai.fyi") {
		t.Errorf("alert HTML missing the control.rogerai.fyi link")
	}
	textBody, _ := got["text"].(string)
	if !strings.Contains(textBody, "control.rogerai.fyi") {
		t.Errorf("alert text missing the control.rogerai.fyi link")
	}

	// Repeat while still firing: NO refire (dedup).
	b.adminAlert("cond:x", "thing broke", "Thing broke", nil, "again")
	noAlert(t, sends)

	// Clear, then re-onset: refires.
	b.alertClear("cond:x")
	b.adminAlert("cond:x", "thing broke again", "Thing broke", nil, "reonset")
	if got := firstAlert(t, sends); !strings.Contains(got["subject"].(string), "again") {
		t.Errorf("refire subject = %v, want the new tail", got["subject"])
	}
}

// TestAdminAlertOffWhenNoRecipients proves an unset ADMIN_EMAIL makes alerting a no-op.
func TestAdminAlertOffWhenNoRecipients(t *testing.T) {
	b, sends := alertBroker(t) // no recipients
	if b.alertingOn() {
		t.Fatal("alertingOn must be false with no recipients")
	}
	b.adminAlert("cond:x", "broke", "Broke", nil, "body")
	b.alertClear("cond:x")
	noAlert(t, sends)
}

// TestAdminAlertMultipleRecipients proves a comma-separated ADMIN_EMAIL delivers to
// EVERY address, once each, on a single onset.
func TestAdminAlertMultipleRecipients(t *testing.T) {
	recips := []string{"lramos85@gmail.com", "rossi.ramos@icloud.com", "larry_rivera@mac.com"}
	b, sends := alertBroker(t, recips...)
	b.adminAlert("cond:multi", "supply gap", "Supply gap", nil, "body")

	got := alertRecipients(sends, len(recips), 2*time.Second)
	if len(got) != len(recips) {
		t.Fatalf("delivered to %d recipients (%v), want %d (%v)", len(got), got, len(recips), recips)
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r] = true
	}
	for _, r := range recips {
		if !seen[r] {
			t.Errorf("recipient %q was not alerted", r)
		}
	}
	// A repeat still does NOT refire even across many recipients.
	b.adminAlert("cond:multi", "supply gap", "Supply gap", nil, "body")
	noAlert(t, sends)
}

// TestAdminAlertMailerErrorSwallowed proves a transport error inside the mailer never
// panics or blocks the alert path.
func TestAdminAlertMailerErrorSwallowed(t *testing.T) {
	b := relayBroker(store.NewMem())
	b.mail = enabledMailer(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("resend down")
	})
	b.adminEmails = []string{"ops@example.com"}
	b.alertFiring = map[string]bool{}
	b.alertOnAirSeen = map[string]bool{}
	// Must return cleanly (the send is async + swallows); give the goroutine time to run.
	b.adminAlert("cond:x", "broke", "Broke", nil, "body")
	time.Sleep(80 * time.Millisecond)
}

// TestCheckDriftAlert proves the ledger verify-vs-balance drift condition fires on a
// divergence, dedups, and clears when balance re-derives cleanly.
func TestCheckDriftAlert(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")

	// No drift: balance == derived -> nothing fires.
	b.checkDriftAlert("u_gh_1", 10.0, 10.0)
	noAlert(t, sends)

	// Drift: cached balance diverges from the re-derived ledger sum -> one alert.
	b.checkDriftAlert("u_gh_1", 10.0, 7.5)
	got := firstAlert(t, sends)
	if !strings.Contains(got["subject"].(string), "u_gh_1") {
		t.Errorf("drift subject = %v, want the wallet id", got["subject"])
	}

	// Still drifting: dedup, no refire.
	b.checkDriftAlert("u_gh_1", 10.0, 7.5)
	noAlert(t, sends)

	// Reconciled: clears, and a later drift refires.
	b.checkDriftAlert("u_gh_1", 10.0, 10.0)
	b.checkDriftAlert("u_gh_1", 10.0, 6.0)
	if got := firstAlert(t, sends); !strings.Contains(got["subject"].(string), "u_gh_1") {
		t.Errorf("drift refire subject = %v", got["subject"])
	}

	// Sub-epsilon float noise is NOT drift.
	b.checkDriftAlert("u_gh_2", 3.0, 3.00000001)
	noAlert(t, sends)
}

// toggleStore wraps a store whose Healthy() can be flipped to unreachable.
type toggleStore struct {
	store.Store
	down bool
}

func (s *toggleStore) Healthy() error {
	if s.down {
		return errors.New("db down")
	}
	return s.Store.Healthy()
}

// TestCheckHealthAlertsDB proves an unreachable durable store fires once, dedups, and
// clears on recovery.
func TestCheckHealthAlertsDB(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	ts := &toggleStore{Store: b.db}
	b.db = ts

	// Healthy: nothing.
	b.checkHealthAlerts()
	noAlert(t, sends)

	// Down: one alert.
	ts.down = true
	b.checkHealthAlerts()
	got := firstAlert(t, sends)
	if !strings.Contains(strings.ToLower(got["subject"].(string)), "database") {
		t.Errorf("db-down subject = %v, want mention of the database", got["subject"])
	}
	// Still down: dedup.
	b.checkHealthAlerts()
	noAlert(t, sends)

	// Recover then fail again: refire.
	ts.down = false
	b.checkHealthAlerts()
	ts.down = true
	b.checkHealthAlerts()
	firstAlert(t, sends)
}

// toggleShared is a shared-store fake whose healthy() can be flipped.
type toggleShared struct {
	*memStore
	up bool
}

func (s *toggleShared) healthy() bool { return s.up }

// TestCheckHealthAlertsValkey proves an unreachable shared (Valkey) store fires once and
// clears on recovery, and that a NIL shared store (unconfigured) never alerts.
func TestCheckHealthAlertsValkey(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")

	// nil shared (unconfigured): never a readiness dependency, no alert.
	b.shared = nil
	b.checkHealthAlerts()
	noAlert(t, sends)

	sh := &toggleShared{memStore: newMemStore(), up: true}
	b.shared = sh
	b.checkHealthAlerts()
	noAlert(t, sends)

	// Down: one alert.
	sh.up = false
	b.checkHealthAlerts()
	got := firstAlert(t, sends)
	if !strings.Contains(strings.ToLower(got["subject"].(string)), "valkey") {
		t.Errorf("valkey-down subject = %v, want mention of Valkey", got["subject"])
	}
	// Dedup.
	b.checkHealthAlerts()
	noAlert(t, sends)
	// Recover -> clear.
	sh.up = true
	b.checkHealthAlerts()
	noAlert(t, sends)
}

// TestCheckCSAMSLAAlert proves a CSAM incident unactioned past the SLA fires once, dedups,
// and clears when the queue drains.
func TestCheckCSAMSLAAlert(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	b.csamSLAHours = 24
	mem := b.db.(*store.Mem)

	if _, err := mem.PreserveCSAM(store.CSAMIncident{Pseudonym: "u_x", Category: "csam", ReportState: store.CSAMQueued}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	// Within SLA: no alert.
	b.checkCSAMSLAAlert(now)
	noAlert(t, sends)

	// Past SLA (25h later): one alert.
	b.checkCSAMSLAAlert(now.Add(25 * time.Hour))
	got := firstAlert(t, sends)
	if !strings.Contains(strings.ToUpper(got["subject"].(string)), "CSAM") {
		t.Errorf("csam-sla subject = %v, want mention of CSAM", got["subject"])
	}
	// Dedup.
	b.checkCSAMSLAAlert(now.Add(26 * time.Hour))
	noAlert(t, sends)
}

// registerLiveOffer puts one on-air node offering `model` into the broker's live state.
func registerLiveOffer(b *broker, nodeID, model string) {
	pub, _, _ := ed25519.GenerateKey(nil)
	b.mu.Lock()
	b.nodes[nodeID] = protocol.NodeRegistration{NodeID: nodeID, PubKey: hex.EncodeToString(pub),
		Offers: []protocol.ModelOffer{{Model: model, PriceOut: 0.1}}}
	b.lastSeen[nodeID] = time.Now()
	b.mu.Unlock()
}

// TestCheckProviderGapAlert proves a model that was ON AIR and drops to 0 providers fires
// a supply-gap alert once, and clears/refires when supply returns and drops again.
func TestCheckProviderGapAlert(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	registerLiveOffer(b, "n1", "gpt-oss-120b")

	// On air: no gap.
	b.checkProviderGapAlerts()
	noAlert(t, sends)

	// Node goes dark -> 0 providers for a model we saw on air -> one alert.
	b.mu.Lock()
	delete(b.nodes, "n1")
	delete(b.lastSeen, "n1")
	b.mu.Unlock()
	b.checkProviderGapAlerts()
	got := firstAlert(t, sends)
	if !strings.Contains(got["subject"].(string), "gpt-oss-120b") || !strings.Contains(got["subject"].(string), "0 providers") {
		t.Errorf("supply-gap subject = %v, want the model + 0 providers", got["subject"])
	}
	// Still gone: dedup.
	b.checkProviderGapAlerts()
	noAlert(t, sends)

	// Supply returns (clear), then drops again (refire).
	registerLiveOffer(b, "n2", "gpt-oss-120b")
	b.checkProviderGapAlerts()
	noAlert(t, sends)
	b.mu.Lock()
	delete(b.nodes, "n2")
	delete(b.lastSeen, "n2")
	b.mu.Unlock()
	b.checkProviderGapAlerts()
	firstAlert(t, sends)
}

// TestFirstBanAlert proves the first NEW ban (node or owner) pages the founder once.
func TestFirstBanAlert(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	b.banNode("n1", "report threshold (5 distinct reporters)")
	got := firstAlert(t, sends)
	if !strings.Contains(strings.ToLower(got["subject"].(string)), "ban") {
		t.Errorf("first-ban subject = %v", got["subject"])
	}
	// A second ban (node or owner) is not the FIRST ban: dedup.
	b.banNode("n2", "report threshold (5 distinct reporters)")
	b.banOwner("pk_bad", "impossible_input", `{"x":1}`)
	noAlert(t, sends)
}

// TestFirstReportAlert proves the first abuse report pages the founder once.
func TestFirstReportAlert(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	post := func() {
		body, _ := json.Marshal(reportRequest{Category: "abuse", NodeID: "n1", Detail: "bad"})
		r := httptest.NewRequest(http.MethodPost, "/report", bytes.NewReader(body))
		w := httptest.NewRecorder()
		b.report(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("/report = %d, want 200", w.Code)
		}
	}
	post()
	got := firstAlert(t, sends)
	if !strings.Contains(strings.ToLower(got["subject"].(string)), "report") {
		t.Errorf("first-report subject = %v", got["subject"])
	}
	// Second report: not the first -> dedup.
	post()
	noAlert(t, sends)
}

// TestFirstDisputeAlert drives a real charge.dispute.created webhook and asserts the first
// dispute pages the founder once.
func TestFirstDisputeAlert(t *testing.T) {
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_dummy")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	t.Setenv("ROGERAI_CREDIT_USD", "1")
	b, sends := alertBroker(t, "ops@example.com")
	b.bill = loadBilling()
	mem := b.db.(*store.Mem)
	_ = mem.BindOwner(store.Owner{GitHubID: 9, Login: "op", Pubkey: "pk1"})
	_ = mem.BindNode("n", "pk1")

	// Fund the wallet + persist the charge mapping so the dispute resolves.
	csPayload, _ := json.Marshal(map[string]any{
		"type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id": "cs_d", "amount_total": 5000, "payment_intent": "pi_d",
			"metadata": map[string]any{"user": "u_gh_9"},
		}},
	})
	rcs := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(csPayload))
	rcs.Header.Set("Stripe-Signature", stripeSig(csPayload, "whsec_test", time.Now().Unix()))
	b.webhook(httptest.NewRecorder(), rcs)
	// A test-mode key must NOT alert on the top-up, so nothing here.
	noAlert(t, sends)

	dp, _ := json.Marshal(map[string]any{
		"type": "charge.dispute.created",
		"data": map[string]any{"object": map[string]any{
			"id": "dp_1", "amount": 5000, "payment_intent": "pi_d", "charge": "ch_d",
		}},
	})
	r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(dp))
	r.Header.Set("Stripe-Signature", stripeSig(dp, "whsec_test", time.Now().Unix()))
	w := httptest.NewRecorder()
	b.webhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("dispute webhook = %d, want 200", w.Code)
	}
	got := firstAlert(t, sends)
	if !strings.Contains(strings.ToLower(got["subject"].(string)), "dispute") {
		t.Errorf("first-dispute subject = %v", got["subject"])
	}
}

// TestFirstLiveTopupAlert proves the first REAL (sk_live) Stripe top-up pages the founder,
// and that a TEST-mode top-up does NOT.
func TestFirstLiveTopupAlert(t *testing.T) {
	deliver := func(t *testing.T, secretKey string) (*broker, chan map[string]any) {
		t.Setenv("STRIPE_SECRET_KEY", secretKey)
		t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
		t.Setenv("ROGERAI_CREDIT_USD", "1")
		b, sends := alertBroker(t, "ops@example.com")
		b.bill = loadBilling()
		p, _ := json.Marshal(map[string]any{
			"type": "checkout.session.completed",
			"data": map[string]any{"object": map[string]any{
				"id": "cs_live", "amount_total": 2500, "payment_intent": "pi_live",
				"metadata": map[string]any{"user": "u_gh_5"},
			}},
		})
		r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(p))
		r.Header.Set("Stripe-Signature", stripeSig(p, "whsec_test", time.Now().Unix()))
		w := httptest.NewRecorder()
		b.webhook(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("topup webhook = %d, want 200", w.Code)
		}
		return b, sends
	}

	t.Run("test-mode does not alert", func(t *testing.T) {
		_, sends := deliver(t, "sk_test_dummy")
		noAlert(t, sends)
	})

	t.Run("live-mode alerts once", func(t *testing.T) {
		b, sends := deliver(t, "sk_live_dummy")
		got := firstAlert(t, sends)
		if !strings.Contains(strings.ToLower(got["subject"].(string)), "top-up") {
			t.Errorf("first-live-topup subject = %v", got["subject"])
		}
		// A second live top-up is not the FIRST: dedup.
		p, _ := json.Marshal(map[string]any{
			"type": "checkout.session.completed",
			"data": map[string]any{"object": map[string]any{
				"id": "cs_live2", "amount_total": 2500, "payment_intent": "pi_live2",
				"metadata": map[string]any{"user": "u_gh_6"},
			}},
		})
		r := httptest.NewRequest(http.MethodPost, "/billing/webhook", bytes.NewReader(p))
		r.Header.Set("Stripe-Signature", stripeSig(p, "whsec_test", time.Now().Unix()))
		b.webhook(httptest.NewRecorder(), r)
		noAlert(t, sends)
	})
}
