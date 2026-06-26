package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/node"
)

func TestBrowseReturnsOffers(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discover" {
			_ = json.NewEncoder(w).Encode(map[string]any{"offers": []map[string]any{
				{"node_id": "amber-fox-m1", "model": "m1", "price_out": 2.0, "online": true},
			}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer broker.Close()

	s := New(node.New(node.Config{}), Options{Broker: broker.URL})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/browse?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("browse = %d, want 200", resp.StatusCode)
	}
	var offers []client.Offer
	if err := json.NewDecoder(resp.Body).Decode(&offers); err != nil {
		t.Fatalf("decode offers: %v", err)
	}
	if len(offers) != 1 || offers[0].Model != "m1" {
		t.Fatalf("offers = %+v, want one m1 offer", offers)
	}
}

func TestAccountNotConfiguredWithoutBroker(t *testing.T) {
	s := New(node.New(node.Config{}), Options{}) // no broker
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/account?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("account without broker = %d, want 503", resp.StatusCode)
	}
}

func TestBrowseRequiresToken(t *testing.T) {
	s := New(node.New(node.Config{}), Options{Broker: "http://127.0.0.1:0"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/browse")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("browse without token = %d, want 403", resp.StatusCode)
	}
}
