package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// THE AGENT MUST BE ABLE TO REACH A PRIVATE BAND.
//
// The broker HIDES every private node from routing unless the request carries
// X-Roger-Freq. The proxy path carried it from the start and chat was fixed later; the
// AGENT relay never sent one - so an operator who tuned a private band in [1] TUNE IN,
// switched to [0] AGENT and ran a turn on that model got "no station is serving <model>",
// having done everything right.
func TestBrokerCompleterCarriesTheFreq(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Roger-Freq")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	_, err := BrokerCompleterRoute(BrokerRoute{
		Broker: srv.URL, User: "u", Model: "grok-4.6", Freq: "147.520MHz-8F3K-9M2Q",
	})(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if got != "147.520MHz-8F3K-9M2Q" {
		t.Errorf("X-Roger-Freq = %q - the broker will not route to a hidden node without it", got)
	}
}

// NEGATIVE HALF: an OPEN MARKET turn must send no freq header at all. A private-band
// credential attached to an ordinary request broadens what the header authorises for no
// benefit, and the fix above could otherwise have hard-coded one.
func TestBrokerCompleterOmitsTheFreqOnTheOpenMarket(t *testing.T) {
	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["X-Roger-Freq"]
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	if _, err := BrokerCompleter(srv.URL, "u", "gpt-oss-20b", false, 0, nil)(
		context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if present {
		t.Error("an open-market turn sent an X-Roger-Freq header")
	}
}
