package client

// A broker that is momentarily unavailable must not end a device login.
//
// Per features/auth/device_login_persistence.feature: "a store outage during a poll is
// reported as a retryable condition ... and when the store returns, the same code still
// completes". The broker now answers 503 for that case instead of reusing the uniform
// "not valid" rejection - but that only helps if the CLI keeps polling through it. Before
// this, ANY non-200 ended the login, so a single blip during the minutes a person spends
// finding their mail would strand a perfectly good code.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeviceLoginPollSurvivesATemporaryOutage(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())

	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch polls.Add(1) {
		case 1, 2:
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "sign-in is temporarily unavailable - keep polling",
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "approved", "account": "acct-1",
			})
		}
	}))
	defer srv.Close()

	account, err := DeviceLoginPoll(srv.URL, DeviceLogin{
		DeviceCode: "dev-code", Interval: 1, ExpiresIn: 60,
	})
	require.NoError(t, err, "a transient outage must not end the login")
	require.Equal(t, "acct-1", account)
	require.GreaterOrEqual(t, polls.Load(), int32(3), "it kept polling through the outage")
}

func TestDeviceLoginPollStillStopsOnARealRejection(t *testing.T) {
	// The retry must not swallow the answers that genuinely end a flow. A 4xx is the
	// caller's fault and retrying it is how a CLI spins until expiry against a code that
	// will never work. Pin the classification directly: a handler-call count is not this
	// invariant, because net/http may replay a rewindable request after a connection-level
	// failure before it delivers any response to DeviceLoginPoll.
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	require.False(t, worthRetrying(&httpError{status: http.StatusBadRequest, msg: "rejected"}),
		"a delivered 4xx response must never enter the poll retry path")

	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "that login is not valid"})
	}))
	defer srv.Close()

	started := time.Now()
	_, err := DeviceLoginPoll(srv.URL, DeviceLogin{
		DeviceCode: "dev-code", Interval: 1, ExpiresIn: 5,
	})
	require.EqualError(t, err, "that login is not valid")
	require.Less(t, time.Since(started), 5*time.Second,
		"a rejection must return promptly, not poll until the 60-second minimum expiry")
	require.GreaterOrEqual(t, polls.Load(), int32(1), "the broker must receive the poll")
}

func TestDeviceLoginPollReportsDenialAndExpiry(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())

	for _, tc := range []struct {
		status string
		want   error
	}{
		{"denied", ErrLoginDenied},
		{"expired", ErrLoginExpired},
	} {
		t.Run(tc.status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": tc.status})
			}))
			defer srv.Close()

			_, err := DeviceLoginPoll(srv.URL, DeviceLogin{
				DeviceCode: "dev-code", Interval: 1, ExpiresIn: 30,
			})
			require.ErrorIs(t, err, tc.want)
		})
	}
}
