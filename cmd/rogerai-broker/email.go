package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// email.go is the FLAG-GATED transactional email layer. It is INERT until a provider
// API key is set - exactly like ROGERAI_REDIS_URL: with no key the mailer is a no-op
// everywhere, ZERO behavior change, and it NEVER blocks or fails the caller. Sends are
// ALWAYS async (fired in a goroutine with their own timeout) so the request path never
// waits on an email; failures are logged, never propagated.
//
// The broker is SDK-free by design (raw HTTP + stdlib), so this talks to the provider
// REST API directly the same way payouts.go/billing.go talk to Stripe.
//
// TWO PROVIDERS are supported so a swap is a config change, not a code change:
//
//	ZEPTOMAIL_API_KEY set -> ZeptoMail (Zoho). Sends as @rogerai.fm.
//	RESEND_API_KEY set    -> Resend (legacy). Sends as @rogerai.fyi.
//
// ZeptoMail WINS when both are set, because mail is moving to .fm alongside the rest
// of the brand. Each provider signs with its OWN DKIM key on its OWN verified domain,
// so the sender address and the provider must move together - see loadMailer.
const (
	// resendEndpoint is the Resend send-email API.
	resendEndpoint = "https://api.resend.com/emails"
	// zeptoEndpoint is the ZeptoMail send-email API. Note the /v1.1/ version segment.
	zeptoEndpoint = "https://api.zeptomail.com/v1.1/email"

	providerResend = "resend"
	providerZepto  = "zeptomail"
)

// mailer holds the provider config + state. A zero apiKey == disabled (no-op). endpoint
// and httpDo are injectable for tests. sentCaps de-dupes the monthly-cap near/at
// notices so a holder is emailed at most once per (threshold, month) instead of on
// every request that crosses the line (the cap check sits in the hot relay path).
type mailer struct {
	apiKey   string
	provider string
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

// loadMailer builds the mailer from the environment. NO provider key set => the mailer
// is disabled (enabled()==false) and every send is a logged-once no-op.
//
// THE SENDER DOMAIN AND THE PROVIDER MOVE TOGETHER. Each provider will only accept a
// From address on a domain IT has verified with ITS own published DKIM key:
//
//	ZeptoMail verified rogerai.fm    (3121226783._domainkey.rogerai.fm)
//	Resend    verified rogerai.fyi   (resend._domainkey.rogerai.fyi)
//
// So the default From follows the selected provider. Do not point one provider at the
// other's domain: the send is rejected, or worse it is accepted unsigned and fails DMARC
// at the receiver. MAIL_FROM overrides the default; RESEND_FROM is still honoured for
// backward compatibility with existing deployments.
func loadMailer() *mailer {
	m := &mailer{
		timeout:  15 * time.Second,
		sentCaps: map[string]bool{},
	}
	// ZeptoMail wins when both keys are present: mail is moving to .fm with the brand.
	if k := os.Getenv("ZEPTOMAIL_API_KEY"); k != "" {
		m.apiKey = k
		m.provider = providerZepto
		m.endpoint = zeptoEndpoint
		m.from = envStr("MAIL_FROM", "RogerAI <noreply@rogerai.fm>")
		return m
	}
	m.apiKey = os.Getenv("RESEND_API_KEY")
	m.provider = providerResend
	m.endpoint = resendEndpoint
	m.from = envStr("MAIL_FROM", envStr("RESEND_FROM", "RogerAI <noreply@rogerai.fyi>"))
	return m
}

// splitFrom breaks a `Name <addr@host>` sender into its parts, tolerating a bare
// `addr@host`. ZeptoMail wants the address and display name as SEPARATE JSON fields,
// where Resend takes the single RFC 5322 string, so this is only used on the Zepto path.
func splitFrom(s string) (name, addr string) {
	s = strings.TrimSpace(s)
	lt := strings.LastIndex(s, "<")
	gt := strings.LastIndex(s, ">")
	if lt >= 0 && gt > lt {
		return strings.TrimSpace(s[:lt]), strings.TrimSpace(s[lt+1 : gt])
	}
	return "", s
}

// enabled reports whether the mailer is live (a provider API key is set). When false
// the whole layer is inert.
func (m *mailer) enabled() bool { return m != nil && m.apiKey != "" }

// sendEmail fires a transactional email ASYNCHRONOUSLY. It is a no-op (logged once)
// when the mailer is disabled, and skips silently when the recipient is empty. It
// NEVER blocks the caller and NEVER returns an error: the request goes out in its own
// goroutine with its own timeout, and any failure is logged, not propagated.
func (m *mailer) sendEmail(to, subject, htmlBody, textBody string) {
	if !m.enabled() {
		if m != nil {
			m.debugLogged.Do(func() {
				log.Printf("email: no provider key set (ZEPTOMAIL_API_KEY / RESEND_API_KEY) - transactional email disabled (no-op)")
			})
		}
		return
	}
	if to == "" {
		return // no recipient on file - nothing to send
	}
	go m.deliver(to, subject, htmlBody, textBody)
}

// deliver performs the actual POST to the configured provider. Runs in its own
// goroutine; all errors
// are logged and swallowed so a send failure can never fail the triggering operation.
func (m *mailer) deliver(to, subject, htmlBody, textBody string) {
	// The two providers disagree on BOTH the field names and the shape of the address
	// fields, so the payload is built per provider rather than translated.
	var payload map[string]any
	if m.provider == providerZepto {
		name, addr := splitFrom(m.from)
		from := map[string]any{"address": addr}
		if name != "" {
			from["name"] = name
		}
		payload = map[string]any{
			"from":     from,
			"to":       []any{map[string]any{"email_address": map[string]any{"address": to}}},
			"subject":  subject,
			"htmlbody": htmlBody,
			"textbody": textBody,
		}
	} else {
		payload = map[string]any{
			"from":    m.from,
			"to":      []string{to},
			"subject": subject,
			"html":    htmlBody,
			"text":    textBody,
		}
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
	// ZeptoMail uses its own scheme, NOT Bearer. The key issued by Zoho already begins
	// with "Zoho-enczapikey " in some places in their console; tolerate both so a
	// copy-paste from either screen works.
	if m.provider == providerZepto {
		auth := m.apiKey
		if !strings.HasPrefix(auth, "Zoho-enczapikey ") {
			auth = "Zoho-enczapikey " + auth
		}
		req.Header.Set("Authorization", auth)
	} else {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	req.Header.Set("Accept", "application/json")
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
		log.Printf("email: %s error %d (to=%s subj=%q): %s", m.provider, resp.StatusCode, maskAddr(to), subject, rb)
		return
	}
	log.Printf("email: sent %q to %s", subject, maskAddr(to))
}

// maskAddr renders an address for a LOG without disclosing it. Logs are shipped, retained,
// searched and pasted into tickets, so a full recipient address in one is a copy of our
// user list in a place with none of the account store's protections - and for a sign-in
// mail it would put the address next to the moment somebody signed in.
//
// The first character and the domain survive, which is enough to recognize a delivery in
// an incident and not enough to write to the person.
func maskAddr(to string) string {
	at := strings.LastIndex(to, "@")
	if at <= 0 {
		return "[redacted]"
	}
	return to[:1] + "***" + to[at:]
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
