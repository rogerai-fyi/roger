package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// ghUserStub points gitHubAPI at a server returning a fixed user. An empty email is
// emitted as JSON null - GitHub's shape for a user who keeps their email private.
func ghUserStub(t *testing.T, id int64, login, name, email string) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := map[string]any{"id": id, "login": login, "name": name, "email": nil}
		if email != "" {
			m["email"] = email
		}
		_ = json.NewEncoder(w).Encode(m)
	}))
	old := gitHubAPI
	gitHubAPI = srv.URL
	return func() { gitHubAPI = old; srv.Close() }
}

// welcomeBroker builds a minimal broker with a Mem store + an ENABLED mailer that
// captures every send onto a channel (the welcome trigger can then be awaited).
func welcomeBroker(t *testing.T) (*broker, chan map[string]any) {
	t.Helper()
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	sends := make(chan map[string]any, 8)
	m := enabledMailer(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(b, &payload)
		sends <- payload
		return &http.Response{StatusCode: 200, Body: io.NopCloser(emptyBody{})}, nil
	})
	b := &broker{
		db: store.NewMem(), priv: brokerPriv, mail: m,
		pubOfUser: map[string]string{}, seedFunds: 100, bill: billing{creditUSD: 1.0},
	}
	return b, sends
}

// ghLogin posts a signed POST /auth/github for the given user key (CLI bind path).
func ghLogin(t *testing.T, b *broker, priv ed25519.PrivateKey) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"access_token": "tok"})
	r := httptest.NewRequest(http.MethodPost, "/auth/github", bytes.NewReader(body))
	signReq(r, priv, body)
	w := httptest.NewRecorder()
	b.authGitHub(w, r)
	return w.Code
}

// patchEmail sets the account email via the session-cookie PATCH /account path.
func patchEmail(t *testing.T, b *broker, login string, gid int64, email string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email})
	r := httptest.NewRequest(http.MethodPatch, "/account", bytes.NewReader(body))
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: b.signSession(login, gid, time.Now().Add(time.Hour).Unix())})
	w := httptest.NewRecorder()
	b.account(w, r)
	return w.Code
}

func waitSend(t *testing.T, ch chan map[string]any) map[string]any {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("expected a welcome email, got none")
		return nil
	}
}

// TestWelcomeFirstBindWithEmail: a first bind whose GitHub account has a PUBLIC email
// sends exactly one personalized welcome, and a re-login never re-sends.
func TestWelcomeFirstBindWithEmail(t *testing.T) {
	defer ghUserStub(t, 100, "octocat", "Mona Lisa", "mona@example.com")()
	b, sends := welcomeBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)

	if code := ghLogin(t, b, priv); code != http.StatusOK {
		t.Fatalf("first login = %d, want 200", code)
	}
	got := waitSend(t, sends)
	if got["subject"] != "Welcome to RogerAI" {
		t.Errorf("subject = %v, want Welcome to RogerAI", got["subject"])
	}
	html, _ := got["html"].(string)
	if !strings.Contains(html, "Mona Lisa") {
		t.Errorf("welcome HTML not personalized by name: %q", firstN(html, 200))
	}
	if !strings.Contains(html, "rogerai.fyi/models.html") {
		t.Errorf("welcome HTML missing the Browse-models CTA")
	}

	// Re-login (same key, same GitHub email) must NOT re-send.
	if code := ghLogin(t, b, priv); code != http.StatusOK {
		t.Fatalf("re-login = %d, want 200", code)
	}
	if n := drain(sends, 250*time.Millisecond); n != 0 {
		t.Errorf("re-login sent %d welcome(s), want 0 (once only)", n)
	}
}

// TestWelcomeEmailSetLater: a first bind with NO email sends nothing; setting the email
// later (PATCH /account) sends exactly one welcome, and a second PATCH never re-sends.
func TestWelcomeEmailSetLater(t *testing.T) {
	// Empty name + empty email: the eventual greeting falls back to "@login".
	defer ghUserStub(t, 200, "noemail", "", "")()
	b, sends := welcomeBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)

	if code := ghLogin(t, b, priv); code != http.StatusOK {
		t.Fatalf("bind without email = %d, want 200", code)
	}
	// No email on file -> no welcome yet.
	if n := drain(sends, 250*time.Millisecond); n != 0 {
		t.Fatalf("no-email bind sent %d welcome(s), want 0", n)
	}

	// Set the email later -> exactly one welcome, greeted by @login.
	if code := patchEmail(t, b, "noemail", 200, "later@example.com"); code != http.StatusOK {
		t.Fatalf("set email = %d, want 200", code)
	}
	got := waitSend(t, sends)
	html, _ := got["html"].(string)
	if !strings.Contains(html, "@noemail") {
		t.Errorf("welcome not greeted by @login: %q", firstN(html, 200))
	}

	// A second email change must NOT re-send (already welcomed).
	if code := patchEmail(t, b, "noemail", 200, "later2@example.com"); code != http.StatusOK {
		t.Fatalf("second email set = %d, want 200", code)
	}
	if n := drain(sends, 250*time.Millisecond); n != 0 {
		t.Errorf("second email set sent %d welcome(s), want 0 (once only)", n)
	}
}

// TestWelcomeNoEmailNeverSends: an account that never has an email is never welcomed,
// even across repeated logins.
func TestWelcomeNoEmailNeverSends(t *testing.T) {
	defer ghUserStub(t, 300, "privacy", "Private Person", "")()
	b, sends := welcomeBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)

	for i := 0; i < 3; i++ {
		if code := ghLogin(t, b, priv); code != http.StatusOK {
			t.Fatalf("login %d = %d, want 200", i, code)
		}
	}
	if n := drain(sends, 300*time.Millisecond); n != 0 {
		t.Errorf("no-email account got %d welcome(s), want 0", n)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
