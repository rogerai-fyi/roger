package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// newTestMailer builds an ENABLED mailer whose sends are captured synchronously via a
// channel (so the async goroutine can be awaited in tests).
func enabledMailer(do func(*http.Request) (*http.Response, error)) *mailer {
	return &mailer{
		apiKey:   "re_test_key",
		from:     "RogerAI <noreply@rogerai.fm>",
		endpoint: "https://api.resend.com/emails",
		httpDo:   do,
		timeout:  5 * time.Second,
		sentCaps: map[string]bool{},
	}
}

// TestMailerBuildsCorrectRequest proves the client POSTs the right URL, auth header,
// content-type, and JSON body to Resend.
func TestMailerBuildsCorrectRequest(t *testing.T) {
	var (
		gotMethod, gotAuth, gotCT, gotURL string
		gotBody                           map[string]any
		done                              = make(chan struct{})
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotURL = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"em_123"}`))
		close(done)
	}))
	defer srv.Close()

	m := enabledMailer(nil)
	m.endpoint = srv.URL + "/emails"
	m.sendEmail("op@example.com", "Payout sent", "<p>hi</p>", "hi")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("send did not reach the server")
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotURL != "/emails" {
		t.Errorf("path = %q, want /emails", gotURL)
	}
	if gotAuth != "Bearer re_test_key" {
		t.Errorf("auth = %q, want Bearer re_test_key", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotBody["subject"] != "Payout sent" || gotBody["html"] != "<p>hi</p>" || gotBody["text"] != "hi" {
		t.Errorf("body fields wrong: %#v", gotBody)
	}
	if gotBody["from"] != "RogerAI <noreply@rogerai.fm>" {
		t.Errorf("from = %v", gotBody["from"])
	}
	to, _ := gotBody["to"].([]any)
	if len(to) != 1 || to[0] != "op@example.com" {
		t.Errorf("to = %v", gotBody["to"])
	}
}

// TestMailerDisabledIsNoOp proves an unset RESEND_API_KEY makes every send a no-op:
// the HTTP path is never invoked, and a nil mailer is safe.
func TestMailerDisabledIsNoOp(t *testing.T) {
	var calls atomic.Int32
	m := &mailer{ // no apiKey => disabled
		from:    "x",
		httpDo:  func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, nil },
		timeout: time.Second,
	}
	if m.enabled() {
		t.Fatal("mailer with empty apiKey must be disabled")
	}
	m.sendEmail("op@example.com", "s", "<p>h</p>", "h")
	// nil mailer must not panic
	var nilM *mailer
	if nilM.enabled() {
		t.Fatal("nil mailer must report disabled")
	}
	nilM.sendEmail("op@example.com", "s", "h", "h")

	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("disabled mailer made %d HTTP calls, want 0", calls.Load())
	}
}

// TestMailerSkipsEmptyRecipient proves an empty recipient short-circuits before any
// HTTP attempt even when the mailer is enabled.
func TestMailerSkipsEmptyRecipient(t *testing.T) {
	var calls atomic.Int32
	m := enabledMailer(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(nil)}, nil
	})
	m.sendEmail("", "s", "h", "h")
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("empty recipient made %d HTTP calls, want 0", calls.Load())
	}
}

// TestMailerSendIsAsync proves sendEmail returns immediately and does not block on a
// slow delivery (the request path must not wait on the email).
func TestMailerSendIsAsync(t *testing.T) {
	release := make(chan struct{})
	delivered := make(chan struct{})
	m := enabledMailer(func(*http.Request) (*http.Response, error) {
		<-release // block until the test allows delivery to proceed
		close(delivered)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(emptyBody{})}, nil
	})
	start := time.Now()
	m.sendEmail("op@example.com", "s", "h", "h")
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("sendEmail blocked for %v - it must be async", elapsed)
	}
	close(release)
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("async delivery never ran")
	}
}

// emptyBody is a zero-length io.Reader for stubbed responses.
type emptyBody struct{}

func (emptyBody) Read([]byte) (int, error) { return 0, io.EOF }

// TestMailerSendFailureNeverPropagates proves a transport error or a non-2xx response
// is swallowed (logged) and never surfaces to the caller.
func TestMailerSendFailureNeverPropagates(t *testing.T) {
	m := enabledMailer(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})
	// sendEmail returns nothing; the goroutine must not panic. Give it time to run.
	m.sendEmail("op@example.com", "s", "h", "h")
	time.Sleep(50 * time.Millisecond)

	// A non-2xx must also be swallowed.
	m2 := enabledMailer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(emptyBody{})}, nil
	})
	m2.sendEmail("op@example.com", "s", "h", "h")
	time.Sleep(50 * time.Millisecond)
}

// brokerWithMailer builds a minimal broker with a seeded owner + an enabled mailer
// that captures sends to a channel.
func brokerWithMailer(t *testing.T, ownerEmail string) (*broker, chan map[string]any) {
	t.Helper()
	mem := store.NewMem()
	if ownerEmail != "" {
		if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "op", Pubkey: "ownerpk", Email: ownerEmail}); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "op", Pubkey: "ownerpk"}); err != nil {
			t.Fatal(err)
		}
	}
	sends := make(chan map[string]any, 8)
	m := enabledMailer(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(b, &payload)
		sends <- payload
		return &http.Response{StatusCode: 200, Body: io.NopCloser(emptyBody{})}, nil
	})
	b := &broker{db: mem, mail: m, bill: billing{creditUSD: 1.0}}
	return b, sends
}

// TestTouchpointSendsWhenFlagAndEmailPresent proves a touchpoint (payout sent) fires
// when the mailer is enabled AND the account has an email.
func TestTouchpointSendsWhenFlagAndEmailPresent(t *testing.T) {
	b, sends := brokerWithMailer(t, "op@example.com")
	b.emailPayoutSent("op@example.com", 12.34, "tr_abc")
	select {
	case got := <-sends:
		if got["subject"] != "Payout sent" {
			t.Errorf("subject = %v", got["subject"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("payout-sent email was not sent")
	}
}

// TestTouchpointSkipsWithoutEmail proves a touchpoint is a no-op when the account has
// no email on file, even with the flag set.
func TestTouchpointSkipsWithoutEmail(t *testing.T) {
	b, sends := brokerWithMailer(t, "")
	// resolve via emailOf (owner exists, no email) - the reversal touchpoint path.
	b.emailPayoutReversed(b.emailOf("ownerpk"), 5, "dp_1")
	select {
	case <-sends:
		t.Fatal("must NOT send when the account has no email")
	case <-time.After(150 * time.Millisecond):
	}
}

// TestTouchpointSkipsWhenFlagOff proves the same touchpoint is a no-op when the mailer
// is disabled (RESEND_API_KEY unset), even with a valid email.
func TestTouchpointSkipsWhenFlagOff(t *testing.T) {
	mem := store.NewMem()
	_ = mem.BindOwner(store.Owner{GitHubID: 7, Login: "op", Pubkey: "ownerpk", Email: "op@example.com"})
	var calls atomic.Int32
	disabled := &mailer{ // no apiKey
		httpDo: func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, nil },
	}
	b := &broker{db: mem, mail: disabled, bill: billing{creditUSD: 1.0}}
	b.emailPayoutSent("op@example.com", 1, "tr_x")
	b.emailDisputeOpened(b.emailOf("ownerpk"), 1, "dp_x")
	time.Sleep(80 * time.Millisecond)
	if calls.Load() != 0 {
		t.Errorf("flag-off made %d sends, want 0", calls.Load())
	}
}

// TestTouchpointFailureDoesNotFailOperation proves a send failure inside a touchpoint
// never panics or blocks the triggering code path.
func TestTouchpointFailureDoesNotFailOperation(t *testing.T) {
	mem := store.NewMem()
	_ = mem.BindOwner(store.Owner{GitHubID: 7, Login: "op", Pubkey: "ownerpk", Email: "op@example.com"})
	b := &broker{db: mem, bill: billing{creditUSD: 1.0}, mail: enabledMailer(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	})}
	// All touchpoints must return cleanly despite the transport error.
	b.emailPayoutSent("op@example.com", 1, "tr")
	b.emailPayoutReversed("op@example.com", 1, "dp")
	b.emailDisputeOpened("op@example.com", 1, "dp")
	b.emailAccountWarning("op@example.com", "empty_output", `{"x":1}`, 3, 5)
	b.emailAccountBanned("op@example.com", "impossible_input", `{"x":1}`)
	b.emailCapNotice("ownerpk", "80", 8, 10, time.Now())
	b.emailCapNotice("ownerpk", "100", 10, 10, time.Now())
	time.Sleep(100 * time.Millisecond) // let the goroutines run; they must not panic
}

// TestCapNoticeDeduped proves the monthly-cap notice is sent at most once per
// (holder, threshold, month).
func TestCapNoticeDeduped(t *testing.T) {
	b, sends := brokerWithMailer(t, "op@example.com")
	now := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	b.emailCapNotice("ownerpk", "80", 8, 10, now)
	b.emailCapNotice("ownerpk", "80", 9, 10, now)                  // same threshold+month: deduped
	b.emailCapNotice("ownerpk", "100", 10, 10, now)                // distinct threshold: sends
	b.emailCapNotice("ownerpk", "80", 8, 10, now.AddDate(0, 1, 0)) // next month: sends

	got := drain(sends, 200*time.Millisecond)
	if got != 3 {
		t.Errorf("cap notices sent = %d, want 3 (80 once, 100 once, next-month 80 once)", got)
	}
}

// TestTouchpointHTMLIsBrandedAndHasCTA proves every touchpoint emits the table-based
// branded shell (the email-safe constraint) AND a bulletproof CTA pointing at the right
// page, plus a matching plain-text fallback. This guards the on-brand HTML so a future
// edit can't silently drop the layout or the action button.
func TestTouchpointHTMLIsBrandedAndHasCTA(t *testing.T) {
	cases := []struct {
		name string
		fire func(b *broker)
		cta  string // expected CTA href substring
	}{
		{"payout-sent", func(b *broker) { b.emailPayoutSent("op@example.com", 12.34, "tr_abc") }, "rogerai.fm/payouts.html"},
		{"payout-reversed", func(b *broker) { b.emailPayoutReversed("op@example.com", 5, "dp_1") }, "rogerai.fm/payouts.html"},
		{"dispute-opened", func(b *broker) { b.emailDisputeOpened("op@example.com", 5, "dp_2") }, "rogerai.fm/account.html"},
		{"account-warning", func(b *broker) { b.emailAccountWarning("op@example.com", "empty_output", `{"x":1}`, 3, 5) }, "rogerai.fm/account.html"},
		{"account-banned", func(b *broker) { b.emailAccountBanned("op@example.com", "impossible_input", `{"x":1}`) }, "rogerai.fm/account.html"},
		{"cap-80", func(b *broker) { b.emailCapNotice("ownerpk", "80", 8, 10, time.Now()) }, "rogerai.fm/billing.html"},
		{"cap-100", func(b *broker) { b.emailCapNotice("ownerpk", "100", 10, 10, time.Now()) }, "rogerai.fm/billing.html"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, sends := brokerWithMailer(t, "op@example.com")
			tc.fire(b)
			select {
			case got := <-sends:
				htmlBody, _ := got["html"].(string)
				textBody, _ := got["text"].(string)
				// Email-safe table shell + 600px container.
				if !strings.Contains(htmlBody, `role="presentation"`) {
					t.Errorf("%s: HTML missing the table shell (role=presentation)", tc.name)
				}
				if !strings.Contains(htmlBody, "max-width:600px") {
					t.Errorf("%s: HTML missing the 600px container", tc.name)
				}
				// Branded beacon header (ROGERAI wordmark).
				if !strings.Contains(htmlBody, "ROGERAI") {
					t.Errorf("%s: HTML missing the ROGERAI brand mark", tc.name)
				}
				// Bulletproof CTA to the right page, in both HTML and text.
				if !strings.Contains(htmlBody, tc.cta) {
					t.Errorf("%s: HTML missing CTA href %q", tc.name, tc.cta)
				}
				if !strings.Contains(textBody, tc.cta) {
					t.Errorf("%s: text fallback missing CTA URL %q", tc.name, tc.cta)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s: email was not sent", tc.name)
			}
		})
	}
}

func drain(ch chan map[string]any, wait time.Duration) int {
	var n int
	var mu sync.Mutex
	deadline := time.After(wait)
	for {
		select {
		case <-ch:
			mu.Lock()
			n++
			mu.Unlock()
		case <-deadline:
			return n
		}
	}
}

// Originally found in the 2026-08-01 migration re-audit, and still the same invariant
// A provider will only send from a domain IT has verified with ITS OWN published DKIM key.
// Pointing one provider at another's domain is either rejected outright or, worse, accepted
// unsigned and then failed by the recipient's DMARC check.
//
// So the default sender MUST track the selected provider. Losing that coupling would
// silently kill every outbound mail, which is exactly the failure this test exists to
// prevent. Which domains a given deployment has verified, and with whom, is its own
// configuration - MAIL_FROM - and deliberately not recorded here.
func TestMailerDefaultSenderMatchesTheProvidersVerifiedDomain(t *testing.T) {
	for _, tc := range []struct {
		name       string
		zeptoKey   string
		wantDomain string
	}{
		{"zeptomail sends from the .fm domain it verified", "zepto_test_key", "@rogerai.fm"},
		{"resend fallback sends from the .fyi domain it verified", "", "@rogerai.fyi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ZEPTOMAIL_API_KEY", tc.zeptoKey)
			t.Setenv("MAIL_FROM", "")
			t.Setenv("RESEND_FROM", "")
			m := loadMailer()
			if !strings.Contains(m.from, tc.wantDomain) {
				t.Fatalf("default From = %q, want the provider-verified domain %s. "+
					"If mail has genuinely moved, publish that provider's DKIM record and "+
					"verify the domain FIRST, then change this default.", m.from, tc.wantDomain)
			}
		})
	}
}

// ZeptoMail must win when both keys are present, so a half-finished cutover cannot
// leave production silently sending through the old provider.
func TestMailerPrefersZeptoMailWhenBothKeysAreSet(t *testing.T) {
	t.Setenv("ZEPTOMAIL_API_KEY", "zepto_test_key")
	t.Setenv("RESEND_API_KEY", "re_test_key")
	m := loadMailer()
	if m.provider != providerZepto {
		t.Fatalf("provider = %q, want %q", m.provider, providerZepto)
	}
	if m.endpoint != zeptoEndpoint {
		t.Fatalf("endpoint = %q, want %q", m.endpoint, zeptoEndpoint)
	}
}

func TestMailerSenderStillHonoursTheEnvironment(t *testing.T) {
	t.Setenv("ZEPTOMAIL_API_KEY", "")
	t.Setenv("MAIL_FROM", "")
	t.Setenv("RESEND_FROM", "RogerAI <ops@example.test>")
	if got := loadMailer().from; got != "RogerAI <ops@example.test>" {
		t.Fatalf("From = %q, want the environment value to win", got)
	}
}

// MAIL_FROM is the provider-neutral override and must beat the legacy RESEND_FROM.
func TestMailFromBeatsLegacyResendFrom(t *testing.T) {
	t.Setenv("ZEPTOMAIL_API_KEY", "")
	t.Setenv("MAIL_FROM", "RogerAI <new@example.test>")
	t.Setenv("RESEND_FROM", "RogerAI <old@example.test>")
	if got := loadMailer().from; got != "RogerAI <new@example.test>" {
		t.Fatalf("From = %q, want MAIL_FROM to win", got)
	}
}

func TestSplitFrom(t *testing.T) {
	for _, tc := range []struct{ in, wantName, wantAddr string }{
		{"RogerAI <noreply@rogerai.fm>", "RogerAI", "noreply@rogerai.fm"},
		{"noreply@rogerai.fm", "", "noreply@rogerai.fm"},
		{"  Spaced Name  <a@b.test>  ", "Spaced Name", "a@b.test"},
	} {
		name, addr := splitFrom(tc.in)
		if name != tc.wantName || addr != tc.wantAddr {
			t.Errorf("splitFrom(%q) = (%q,%q), want (%q,%q)", tc.in, name, addr, tc.wantName, tc.wantAddr)
		}
	}
}

// The ZeptoMail wire format differs from Resend in BOTH the auth scheme and the body
// shape. This pins it against their documented API so a regression is caught here and
// not by a silent production 401.
func TestMailerBuildsZeptoMailRequest(t *testing.T) {
	var gotAuth, gotURL string
	var gotBody map[string]any
	m := &mailer{
		apiKey:   "zepto_raw_key",
		provider: providerZepto,
		from:     "RogerAI <noreply@rogerai.fm>",
		endpoint: zeptoEndpoint,
		timeout:  5 * time.Second,
		sentCaps: map[string]bool{},
		httpDo: func(r *http.Request) (*http.Response, error) {
			gotAuth = r.Header.Get("Authorization")
			gotURL = r.URL.String()
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		},
	}
	m.deliver("op@example.com", "Subject", "<b>hi</b>", "hi")

	if want := "Zoho-enczapikey zepto_raw_key"; gotAuth != want {
		t.Errorf("auth = %q, want %q", gotAuth, want)
	}
	if gotURL != zeptoEndpoint {
		t.Errorf("url = %q, want %q", gotURL, zeptoEndpoint)
	}
	from, ok := gotBody["from"].(map[string]any)
	if !ok || from["address"] != "noreply@rogerai.fm" || from["name"] != "RogerAI" {
		t.Errorf("from = %#v, want split address/name object", gotBody["from"])
	}
	if gotBody["htmlbody"] != "<b>hi</b>" || gotBody["textbody"] != "hi" {
		t.Errorf("body fields = %#v, want ZeptoMail htmlbody/textbody naming", gotBody)
	}
	to, ok := gotBody["to"].([]any)
	if !ok || len(to) != 1 {
		t.Fatalf("to = %#v, want a single-element array", gotBody["to"])
	}
	ea, ok := to[0].(map[string]any)["email_address"].(map[string]any)
	if !ok || ea["address"] != "op@example.com" {
		t.Errorf("to[0] = %#v, want nested email_address.address", to[0])
	}
}

// A key pasted from the console screen that already includes the scheme prefix must
// not end up double-prefixed.
func TestMailerDoesNotDoublePrefixZeptoKey(t *testing.T) {
	var gotAuth string
	m := &mailer{
		apiKey:   "Zoho-enczapikey already_prefixed",
		provider: providerZepto,
		from:     "noreply@rogerai.fm",
		endpoint: zeptoEndpoint,
		sentCaps: map[string]bool{},
		httpDo: func(r *http.Request) (*http.Response, error) {
			gotAuth = r.Header.Get("Authorization")
			return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		},
	}
	m.deliver("op@example.com", "s", "h", "t")
	if want := "Zoho-enczapikey already_prefixed"; gotAuth != want {
		t.Errorf("auth = %q, want %q (no double prefix)", gotAuth, want)
	}
}
