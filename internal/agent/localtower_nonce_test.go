package agent

import (
	"crypto/ed25519"
	"net/http"
	"testing"

	"rogerai.fm/roger/v7/internal/protocol"
)

// The share station signs a standalone Tower's requests with the same decision roger uses
// (one_binary.feature section 7.6): a nonce to every Tower over plain http, loopback included,
// and none to an https broker.
func TestSignLocalUsesTheOneNonceDecision(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"http://192.168.1.10:8787/local/poll", true},
		{"http://[fd00::5]:8787/local/poll", true},
		{"http://169.254.3.3:8787/local/poll", true},
		{"http://[fe80::1%25eth0]:8787/local/poll", true},
		{"http://100.64.0.9:8787/local/poll", true},
		{"http://127.0.0.1:8787/local/poll", true},
		{"https://broker.example:443/local/poll", false},
	} {
		req, err := http.NewRequest(http.MethodPost, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		signLocal(req, nil, priv)
		if got := req.Header.Get(protocol.HeaderNonce) != ""; got != tc.want {
			t.Errorf("%s: nonce %v, want %v", tc.url, got, tc.want)
		}
		if req.Header.Get(protocol.HeaderSig) == "" {
			t.Errorf("%s: not signed", tc.url)
		}
	}
}
