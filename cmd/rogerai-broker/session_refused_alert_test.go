package main

// Monitoring for the login-loop class of bug: a person holds a VALID session yet an
// account feed refuses them as logged-out. One such refusal is a stale tab; a handful
// inside minutes is the broker disagreeing with itself about who is signed in (the
// 2026-10 email-login refresh loop), and it pages the founder.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRepeatedRefusalOfAValidSessionPagesTheFounder(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	now := time.Now()

	for i := 0; i < sessionRefusedThreshold-1; i++ {
		b.noteSessionRefused(now)
	}
	b.checkSessionRefusedAlert(now)
	noAlert(t, sends) // under the threshold: a stale tab, not an incident

	b.noteSessionRefused(now)
	b.checkSessionRefusedAlert(now)
	p := firstAlert(t, sends)
	require.Contains(t, p["subject"], "signed-in")
}

func TestTheSessionRefusedPageClearsOnceItStops(t *testing.T) {
	b, sends := alertBroker(t, "ops@example.com")
	now := time.Now()
	for i := 0; i < sessionRefusedThreshold; i++ {
		b.noteSessionRefused(now)
	}
	b.checkSessionRefusedAlert(now)
	firstAlert(t, sends)

	later := now.Add(sessionRefusedWindow + time.Minute)
	b.checkSessionRefusedAlert(later)
	b.alertMu.Lock()
	firing := b.alertFiring["auth_session_refused"]
	b.alertMu.Unlock()
	require.False(t, firing, "quiet for a whole window clears the condition")
}

func TestARealRouteRefusingAValidSessionIsCounted(t *testing.T) {
	b, _ := alertBroker(t, "ops@example.com")
	// A cryptographically valid session whose wallet is not an account wallet: exactly the
	// shape the original bug produced (a session the feeds could not recognise).
	exp := time.Now().Add(time.Hour).Unix()
	c := &http.Cookie{Name: sessionCookie, Value: b.signSessionFull("someone", 0, "u_mystery_1", "", exp)}
	req := httptest.NewRequest(http.MethodGet, "/metrics/series", strings.NewReader(""))
	req.Header.Set("Origin", testWebOrigin)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	b.metricsSeries(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	require.Equal(t, 1, b.sessionRefusedCount(time.Now()), "a valid session refused as logged-out must be counted")
}

func TestAnAnonymousRefusalIsNotCounted(t *testing.T) {
	b, _ := alertBroker(t, "ops@example.com")
	req := httptest.NewRequest(http.MethodGet, "/metrics/series", nil)
	req.Header.Set("Origin", testWebOrigin)
	rec := httptest.NewRecorder()
	b.metricsSeries(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, 0, b.sessionRefusedCount(time.Now()), "a logged-out visitor is normal traffic, never an incident")
}
