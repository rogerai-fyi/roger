package agent

// voice_offer_wire_test.go pins the WIRE contract for the voice sample clip on the
// registration offer (the end-to-end half of the sample_url plumbing): a set
// Config.SampleURL lands in the register body as the offer's `sample_url`, and an unset
// one is OMITTED from the JSON entirely (protocol omitempty) - so /voices never sees an
// empty-string field (the broker's own BDD spec covers that hop, voices_bdd_test.go).
// Real HTTP against an httptest broker; no mocks.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

func TestRegisterOfferSampleURLOnWire(t *testing.T) {
	cases := []struct {
		name       string
		sample     string
		wantOnWire bool
	}{
		{"a set sample_url rides the offer body", "https://cdn.example/operator-sample.mp3", true},
		{"an unset sample_url is omitted from the offer body", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tmp) // loadOrCreateKey writes here (Linux)
			t.Setenv("HOME", tmp)            // ...and here on macOS

			var mu sync.Mutex
			var regBody []byte
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/nodes/register") {
					b := make([]byte, r.ContentLength)
					_, _ = r.Body.Read(b)
					mu.Lock()
					regBody = b
					mu.Unlock()
				}
				_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			}))
			defer broker.Close()

			sess, err := Start(Config{
				Broker: broker.URL, Upstream: "http://127.0.0.1:0", NodeID: "n-wire",
				Model: "kokoro", Modality: "tts", Name: "1950s Operator", Language: "en-US",
				SampleURL: tc.sample, Parallel: 1,
			})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer sess.Stop()

			mu.Lock()
			body := string(regBody)
			mu.Unlock()
			var reg protocol.NodeRegistration
			if err := json.Unmarshal([]byte(body), &reg); err != nil {
				t.Fatalf("decode captured register body: %v\n%s", err, body)
			}
			if len(reg.Offers) != 1 || reg.Offers[0].SampleURL != tc.sample {
				t.Errorf("registered offer SampleURL = %+v, want %q", reg.Offers, tc.sample)
			}
			if got := strings.Contains(body, `"sample_url"`); got != tc.wantOnWire {
				t.Errorf("register body contains sample_url = %v, want %v:\n%s", got, tc.wantOnWire, body)
			}
		})
	}
}
