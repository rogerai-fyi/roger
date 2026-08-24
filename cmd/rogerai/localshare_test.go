package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"rogerai.fm/roger/v6/internal/agent"
	"strings"
	"testing"
)

func TestIsLocalTowerBrokerDetectsTheTowerByProbe(t *testing.T) {
	// A standalone Tower: /local/poll answers (401 unsigned here).
	tower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/local/poll" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`)) // the Tower's uniform 401 body
			return
		}
		http.NotFound(w, r)
	}))
	defer tower.Close()

	// A public-broker-shaped server: no /local/poll route -> 404.
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer broker.Close()

	// httptest URLs are http://127.0.0.1:PORT (loopback) - the local-address gate passes.
	if !isLocalTowerBroker(tower.URL) {
		t.Errorf("a Tower (answers /local/poll) must be detected as local")
	}
	if isLocalTowerBroker(broker.URL) {
		t.Errorf("a broker (404s /local/poll) must NOT be detected as local")
	}
}

func TestIsLocalTowerBrokerRejectsPublicAndBadTargets(t *testing.T) {
	cases := []string{
		"https://broker.rogerai.fm", // public https: never probed, never local
		"http://203.0.113.5:8787",   // public IP over http: not local
		"http://roggentoo:8787",     // hostname (not a literal IP): not local
		"://bad",                    // unparseable
		"",                          // empty
	}
	for _, u := range cases {
		if isLocalTowerBroker(u) {
			t.Errorf("%q must not be treated as a local Tower", u)
		}
	}
}

func TestServeLocalTowerShareNeedsAnUpstream(t *testing.T) {
	var out bytes.Buffer
	err := serveLocalTowerShare(agent.Config{Broker: "http://127.0.0.1:1"}, &out)
	if err == nil {
		t.Fatal("serving with no upstream must error with guidance")
	}
	if !strings.Contains(err.Error(), "upstream") {
		t.Fatalf("the error should name --upstream, got %q", err)
	}
}

func TestIsLocalTowerBrokerRequiresTheTowersExact401(t *testing.T) {
	// A local dev broker that catch-alls a 200 for any path (like the share test harness) must
	// NOT be mistaken for a Tower - otherwise `roger share` would poll it forever.
	catchAll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer catchAll.Close()
	if isLocalTowerBroker(catchAll.URL) {
		t.Errorf("a catch-all 200 broker must not be detected as a Tower")
	}

	// A generic 401 that is NOT the Tower's uniform unauthorized body is also not a Tower.
	other401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_error"}`))
	}))
	defer other401.Close()
	if isLocalTowerBroker(other401.URL) {
		t.Errorf("a 401 without the Tower's uniform body must not be detected as a Tower")
	}
}
