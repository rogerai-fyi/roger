package main

import (
	"net/http"
	"strconv"
	"time"
)

// Monitoring for "the broker disagrees with itself about who is signed in".
//
// A logged-out visitor getting a 401 is normal. A person holding a VALID signed session
// being refused as logged-out by an account feed is not: it is how an identity the login
// route mints but a feed does not recognise (the 2026-10 email-login refresh loop) shows up
// server-side, long before anyone writes in. noteSessionRefused records each one;
// checkSessionRefusedAlert pages the founder when they repeat.
const (
	sessionRefusedWindow    = 10 * time.Minute
	sessionRefusedThreshold = 5
)

// refuseSession answers 401 and, when the request carried a valid session, counts it.
func (b *broker) refuseSession(w http.ResponseWriter, r *http.Request, msg string) {
	if _, _, _, ok := b.sessionOwner(r); ok {
		b.noteSessionRefused(time.Now())
	}
	jsonErr(w, http.StatusUnauthorized, msg)
}

func (b *broker) noteSessionRefused(now time.Time) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	b.sessRefused = append(pruneTimes(b.sessRefused, now.Add(-sessionRefusedWindow)), now)
	if max := sessionRefusedThreshold * 4; len(b.sessRefused) > max { // only the count matters
		b.sessRefused = b.sessRefused[len(b.sessRefused)-max:]
	}
}

func (b *broker) sessionRefusedCount(now time.Time) int {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	b.sessRefused = pruneTimes(b.sessRefused, now.Add(-sessionRefusedWindow))
	return len(b.sessRefused)
}

func pruneTimes(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}

// checkSessionRefusedAlert runs on the alert checker tick.
func (b *broker) checkSessionRefusedAlert(now time.Time) {
	n := b.sessionRefusedCount(now)
	if n < sessionRefusedThreshold {
		b.alertClear("auth_session_refused")
		return
	}
	b.adminAlert("auth_session_refused", "signed-in users are being refused as logged-out",
		"Signed-in users are being refused as logged-out",
		[][2]string{
			{"Refusals (last 10 min)", strconv.Itoa(n)},
			{"Signal", "valid session cookie, 401 from an account feed"},
		},
		"People holding a valid session are being told they are not logged in. The login page sends a signed-in person to the dashboard, so this can mean a login <-> dashboard refresh loop. Check the newest sign-in route (email, GitHub, Apple) and what identity its session carries.")
}
