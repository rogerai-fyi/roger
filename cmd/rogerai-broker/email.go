package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// email.go is the FLAG-GATED transactional email layer (Resend). It is INERT until
// RESEND_API_KEY is set - exactly like ROGERAI_REDIS_URL: with the key unset the
// mailer is a no-op everywhere, ZERO behavior change, and it NEVER blocks or fails
// the caller. Sends are ALWAYS async (fired in a goroutine with their own timeout)
// so the request path never waits on an email; failures are logged, never propagated.
//
// The broker is SDK-free by design (raw HTTP + stdlib), so this talks to the Resend
// REST API directly the same way payouts.go/billing.go talk to Stripe.

// resendEndpoint is the Resend send-email API. Kept as a field on the mailer so tests
// can point it at an httptest stub without touching the network.
const resendEndpoint = "https://api.resend.com/emails"

// mailer holds the Resend config + state. A zero apiKey == disabled (no-op). endpoint
// and httpDo are injectable for tests. sentCaps de-dupes the monthly-cap near/at
// notices so a holder is emailed at most once per (threshold, month) instead of on
// every request that crosses the line (the cap check sits in the hot relay path).
type mailer struct {
	apiKey   string
	from     string
	endpoint string
	httpDo   func(*http.Request) (*http.Response, error)
	timeout  time.Duration

	// debugLogged ensures the "disabled, skipping" debug line is logged ONCE, not on
	// every attempted send (the no-op path is otherwise silent).
	debugLogged sync.Once

	mu       sync.Mutex
	sentCaps map[string]bool // key: holder|threshold|YYYY-MM -> already emailed
}

// loadMailer builds the mailer from the environment. UNSET RESEND_API_KEY => the
// mailer is disabled (enabled()==false) and every send is a logged-once no-op.
func loadMailer() *mailer {
	return &mailer{
		apiKey:   os.Getenv("RESEND_API_KEY"),
		from:     envStr("RESEND_FROM", "RogerAI <noreply@rogerai.fyi>"),
		endpoint: resendEndpoint,
		timeout:  15 * time.Second,
		sentCaps: map[string]bool{},
	}
}

// enabled reports whether the mailer is live (RESEND_API_KEY is set). When false the
// whole layer is inert.
func (m *mailer) enabled() bool { return m != nil && m.apiKey != "" }

// sendEmail fires a transactional email ASYNCHRONOUSLY. It is a no-op (logged once)
// when the mailer is disabled, and skips silently when the recipient is empty. It
// NEVER blocks the caller and NEVER returns an error: the request goes out in its own
// goroutine with its own timeout, and any failure is logged, not propagated.
func (m *mailer) sendEmail(to, subject, htmlBody, textBody string) {
	if !m.enabled() {
		if m != nil {
			m.debugLogged.Do(func() {
				log.Printf("email: RESEND_API_KEY unset - transactional email disabled (no-op)")
			})
		}
		return
	}
	if to == "" {
		return // no recipient on file - nothing to send
	}
	go m.deliver(to, subject, htmlBody, textBody)
}

// deliver performs the actual POST to Resend. Runs in its own goroutine; all errors
// are logged and swallowed so a send failure can never fail the triggering operation.
func (m *mailer) deliver(to, subject, htmlBody, textBody string) {
	payload := map[string]any{
		"from":    m.from,
		"to":      []string{to},
		"subject": subject,
		"html":    htmlBody,
		"text":    textBody,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("email: marshal failed (to=%s subj=%q): %v", to, subject, err)
		return
	}

	timeout := m.timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	req, err := http.NewRequest(http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		log.Printf("email: build request failed (to=%s): %v", to, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	do := m.httpDo
	if do == nil {
		do = (&http.Client{Timeout: timeout}).Do
	}
	resp, err := do(req)
	if err != nil {
		log.Printf("email: send failed (to=%s subj=%q): %v", to, subject, err)
		return
	}
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("email: resend error %d (to=%s subj=%q): %s", resp.StatusCode, to, subject, rb)
		return
	}
	log.Printf("email: sent %q to %s", subject, to)
}

// capNoticeOnce reports whether a monthly-cap notice for this (holder, threshold,
// month) has NOT yet been sent, marking it sent when it returns true. This collapses
// the per-request hot-path cap crossings into at most one email per threshold per
// month. threshold is "80" or "100". It is a no-op-safe guard: when disabled it still
// returns false so callers short-circuit. Concurrency-safe.
func (m *mailer) capNoticeOnce(holder, threshold string, now time.Time) bool {
	if !m.enabled() {
		return false
	}
	key := holder + "|" + threshold + "|" + now.Format("2006-01")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sentCaps[key] {
		return false
	}
	m.sentCaps[key] = true
	return true
}
