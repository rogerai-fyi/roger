package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The broker refuses a below-minimum top-up with 400 and a message that names the
// minimum. Both client entry points threw that message away: they special-cased 503 and
// let everything else fall through to "no checkout URL returned", which tells the
// operator nothing about what to do next.
//
// It matters most for binaries already in the field. A shipped v6.2.0 client sending
// usd=0.50 gets the new refusal and has no idea why, because its own error text is
// generic. The broker's message is the only place the reason exists.
func brokerSaying(t *testing.T, code int, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestTopupURLSurfacesTheBrokersRefusal(t *testing.T) {
	srv := brokerSaying(t, http.StatusBadRequest, `{"error":{"message":"top-up minimum is $1"}}`)
	_, err := TopupURL(srv.URL, "u", 0.5)
	if err == nil {
		t.Fatal("TopupURL accepted a refused top-up")
	}
	if !strings.Contains(err.Error(), "minimum is $1") {
		t.Errorf("TopupURL error = %q, want the broker's own reason", err)
	}
}

func TestTopupSurfacesTheBrokersRefusal(t *testing.T) {
	srv := brokerSaying(t, http.StatusBadRequest, `{"error":{"message":"top-up minimum is $1"}}`)
	err := Topup(srv.URL, "u", 0.5, nil)
	if err == nil {
		t.Fatal("Topup accepted a refused top-up")
	}
	if !strings.Contains(err.Error(), "minimum is $1") {
		t.Errorf("Topup error = %q, want the broker's own reason", err)
	}
}

// A refusal with no readable body still has to fail, and say something better than
// nothing - the status is the only fact available.
func TestTopupURLFailsClearlyOnAnUnreadableRefusal(t *testing.T) {
	srv := brokerSaying(t, http.StatusBadRequest, `not json`)
	_, err := TopupURL(srv.URL, "u", 0.5)
	if err == nil {
		t.Fatal("TopupURL accepted a refused top-up")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("TopupURL error = %q, want the status when there is no message", err)
	}
}

// The not-configured case keeps its own wording; it is a different problem from a
// refused amount and the operator can do nothing about it.
func TestTopupURLKeepsTheNotConfiguredWording(t *testing.T) {
	srv := brokerSaying(t, http.StatusServiceUnavailable, `{"error":{"message":"billing not configured"}}`)
	_, err := TopupURL(srv.URL, "u", 25)
	if err == nil || !strings.Contains(err.Error(), "configured") {
		t.Errorf("TopupURL error = %v, want the not-configured wording", err)
	}
}
