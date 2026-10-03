package station

// upstream_status_test.go - the typed non-2xx reply from the model behind a Station: the
// status and Retry-After a relay needs to answer a consumer honestly, and the guarantee that
// only those two numbers (never the model's words) cross a blind Tower.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPUpstreamNon2xxIsATypedStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", " 7 ")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"slow down"}`))
	}))
	defer srv.Close()

	_, err := HTTPUpstream{URL: srv.URL}.Serve(context.Background(), []byte(`{}`))
	var se *UpstreamStatusError
	require.True(t, errors.As(err, &se), "a non-2xx reply is typed, got %v", err)
	require.Equal(t, http.StatusTooManyRequests, se.Status)
	require.Equal(t, 7, se.RetryAfter, "Retry-After is read as whole seconds, whitespace trimmed")
	require.Equal(t, `{"error":"slow down"}`, se.Detail, "the operator's log keeps the model's words")
	require.Contains(t, se.Error(), "429")
	require.Contains(t, se.Error(), "slow down")
}

func TestUpstreamStatusErrorClassCarriesOnlyNumbers(t *testing.T) {
	for _, tc := range []struct {
		e    UpstreamStatusError
		want string
	}{
		{UpstreamStatusError{Status: 429, RetryAfter: 6, Detail: "secret"}, "the model did not answer (status 429, retry-after 6)"},
		{UpstreamStatusError{Status: 503, Detail: "secret"}, "the model did not answer (status 503)"},
	} {
		require.Equal(t, tc.want, tc.e.Class())
		require.NotContains(t, tc.e.Class(), "secret", "the class never echoes the model's words")
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for in, want := range map[string]int{
		"": 0, "0": 0, "12": 12, " 3 ": 3, "-4": 0, "soon": 0,
		"Wed, 21 Oct 2015 07:28:00 GMT": 0, // an HTTP-date is not seconds: no wait is claimed
	} {
		require.Equal(t, want, retryAfterSeconds(in), "Retry-After %q", in)
	}
}

// A typed status reply crosses the blind Tower as its class with the two numbers, and still
// never the model's words or a receipt.
func TestServeSealedTypedStatusCrossesAsNumbersOnly(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, s, g, _, up := sealedRig(t, now, nil)
	up.err = &UpstreamStatusError{Status: 429, RetryAfter: 9, Detail: `rate limited "the secret launch codes"`}

	env, receipt, failure := e.ServeSealed(context.Background(), g.Signed, sealFor(t, s, g.AttemptID, []byte(`{"p":"the secret launch codes"}`)))
	require.Equal(t, "the model did not answer (status 429, retry-after 9)", failure)
	require.NotContains(t, failure, "launch codes", "no upstream echo crosses the tower")
	require.Empty(t, receipt)
	require.Empty(t, env)
}
