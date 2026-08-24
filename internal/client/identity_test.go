package client

import (
	"net/url"
	"testing"
)

func TestTargetsLANTowerScoping(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://192.168.1.10:8787/v1/chat/completions", true},   // LAN private: nonce
		{"http://10.2.3.4:8787/discover", true},                  // LAN private: nonce
		{"http://172.16.5.5:8787/v1/chat/completions", true},     // LAN private: nonce
		{"http://127.0.0.1:8787/v1/chat/completions", false},     // loopback: no wire, no nonce
		{"http://[::1]:8787/discover", false},                    // ipv6 loopback: no nonce
		{"https://broker.rogerai.fm/v1/chat/completions", false}, // public https broker: no nonce
		{"http://roggentoo:8787/discover", false},                // hostname (not literal IP): no nonce
		{"http://203.0.113.5:8787/discover", false},              // public IP over http: no nonce
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.url)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.url, err)
		}
		if got := targetsLANTower(u); got != tc.want {
			t.Errorf("targetsLANTower(%s) = %v, want %v", tc.url, got, tc.want)
		}
	}
}
