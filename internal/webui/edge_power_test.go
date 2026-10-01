package webui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// The console's rule form offers every metric the fleet can judge: a rule the CLI can set and the
// console cannot is a surface drifting from the others (the throttled metric arrived 2026-10-01).
func TestTheConsoleOffersEveryMetricARuleCanWatch(t *testing.T) {
	js := consoleJS(t)
	for _, m := range edge.Metrics {
		if !strings.Contains(js, fmt.Sprintf("%q", string(m))) {
			t.Errorf("the console's rule form does not offer %q", m)
		}
	}
	// A Pi's power is drawn on the node card in the same words as everywhere else.
	for _, w := range []string{"under-voltage", "frequency capped", "throttled", "soft temperature limit", "since boot", "pi_power"} {
		if !strings.Contains(js, w) {
			t.Errorf("the console does not word a Pi's power: missing %q", w)
		}
	}
}

// Every capability has a mark in the console too, so a node declaring a new one never draws a blank.
func TestTheConsoleMarksEveryCapability(t *testing.T) {
	js := consoleJS(t)
	i := strings.Index(js, "var EDGE_MARK = {")
	if i < 0 {
		t.Fatal("EDGE_MARK is gone")
	}
	table := js[i : i+strings.Index(js[i:], "};")]
	for _, c := range edge.Capabilities() {
		if !strings.Contains(table, string(c)+`"`) && !strings.Contains(table, string(c)+":") {
			t.Errorf("the console has no mark for %q", c)
		}
	}
}

// A phone's card in the console says it is asleep and what it reported, in the shared words.
func TestTheConsoleCardSaysAPhoneIsAsleepAndItsVitals(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	b, no := 64, false
	n := edge.NewNode(nil, "nina", edge.Mobile)
	n.ID = "n_nina"
	n.Presence = string(edge.PresenceVerified)
	n.Phone = &store.EdgePhone{BatteryPct: &b, Foreground: &no}
	if _, err := fleet.Enroll(n); err != nil {
		t.Fatal(err)
	}
	s := New(testCtrl(), Options{Edge: EdgeHooks{Fleet: fleet}})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/edge?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`"state":"ASLEEP now"`, `"phone":"battery 64% · app in the background"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if !strings.Contains(consoleJS(t), `edgeKV(card, "phone", n.phone)`) {
		t.Error("the console does not draw a phone's vitals")
	}
}
