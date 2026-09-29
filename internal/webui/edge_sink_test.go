package webui

// WHERE AN ALERT GOES, from the console (features/edge/health.feature §5). The rule that shapes
// every line of this: a webhook URL is a BEARER CREDENTIAL. It travels one way - in from the owner,
// never back out - so a console left open on a shared screen cannot be walked through to recover
// one, and no payload, log line or error message can carry it by accident.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func sinkRig(t *testing.T, wire bool) (*Server, *httptest.Server, edge.AlertLog) {
	t.Helper()
	l := edge.AlertLog{Dir: t.TempDir(), SinkBackoff: time.Millisecond}
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	hooks := EdgeHooks{Self: "workshop", Fleet: fleet}
	if wire {
		hooks.Sinks = func() ([]edge.Sink, error) { return l.Sinks() }
		hooks.AddSink = func(kind, url string) (edge.Sink, error) {
			return l.AddSink(edge.SinkKind(kind), url, time.Now())
		}
		hooks.RemoveSink = func(id string) (bool, error) { return l.RemoveSink(id) }
		hooks.TestSink = func(id string) (edge.Delivery, error) {
			return l.TestSink(t.Context(), id, time.Now())
		}
	}
	s := New(testCtrl(), Options{Edge: hooks})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, l
}

func sinkSnap(t *testing.T, s *Server, srv *httptest.Server) edgeSnap {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/edge?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snap edgeSnap
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestTheConsoleAddsASinkAndIsNeverToldItsSecretAgain(t *testing.T) {
	s, srv, _ := sinkRig(t, true)
	secret := "https://discord.com/api/webhooks/123456789/AbCdEf-verysecrettoken"
	resp := postJSONTok(t, srv, s, "/api/edge/sink", `{"do":"add","kind":"discord","url":"`+secret+`"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add = %d", resp.StatusCode)
	}

	// The WHOLE payload is searched, not just the sink list: a secret that leaks anywhere leaks.
	raw, err := http.Get(srv.URL + "/api/edge?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Body.Close()
	whole, err := io.ReadAll(raw.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, never := range []string{"AbCdEf-verysecrettoken", "123456789"} {
		if strings.Contains(string(whole), never) {
			t.Errorf("the console payload carries the sink's secret (%q)", never)
		}
	}
	snap := sinkSnap(t, s, srv)
	if len(snap.Sinks) != 1 {
		t.Fatalf("the console does not list the sink: %+v", snap.Sinks)
	}
	if snap.Sinks[0].Host != "discord.com" {
		t.Errorf("the owner cannot tell which sink this is: %+v", snap.Sinks[0])
	}
}

func TestTheConsoleRemovesAndTestsASink(t *testing.T) {
	var hit int
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer dest.Close()
	s, srv, _ := sinkRig(t, true)
	add := postJSONTok(t, srv, s, "/api/edge/sink", `{"do":"add","kind":"webhook","url":"`+dest.URL+`"}`)
	add.Body.Close()
	id := sinkSnap(t, s, srv).Sinks[0].ID

	test := postJSONTok(t, srv, s, "/api/edge/sink", `{"do":"test","id":"`+id+`"}`)
	test.Body.Close()
	if test.StatusCode != http.StatusOK {
		t.Fatalf("test = %d", test.StatusCode)
	}
	if hit != 1 {
		t.Errorf("the test did not reach the sink (%d hits)", hit)
	}

	rm := postJSONTok(t, srv, s, "/api/edge/sink", `{"do":"rm","id":"`+id+`"}`)
	rm.Body.Close()
	if rm.StatusCode != http.StatusOK {
		t.Fatalf("rm = %d", rm.StatusCode)
	}
	if got := sinkSnap(t, s, srv).Sinks; len(got) != 0 {
		t.Errorf("the sink is still configured: %+v", got)
	}
}

// A sink that will not take the test says so, and the failure message names the sink by id and
// host - never by URL, because an error page is as public as any other output.
func TestAFailedTestSaysSoWithoutTheSecret(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	s, srv, _ := sinkRig(t, true)
	secret := dead.URL + "/hooks/AbCdEf-verysecrettoken"
	add := postJSONTok(t, srv, s, "/api/edge/sink", `{"do":"add","kind":"webhook","url":"`+secret+`"}`)
	add.Body.Close()
	id := sinkSnap(t, s, srv).Sinks[0].ID

	resp := postJSONTok(t, srv, s, "/api/edge/sink", `{"do":"test","id":"`+id+`"}`)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a dead sink reported success")
	}
	msg, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(msg), "AbCdEf-verysecrettoken") {
		t.Errorf("the failure message carries the secret: %s", msg)
	}
	if !strings.Contains(string(msg), id) {
		t.Errorf("the failure does not say which sink: %s", msg)
	}
}

func TestAnUnwiredSinkSurfaceIsHonestlyDisabled(t *testing.T) {
	s, srv, _ := sinkRig(t, false)
	resp := postJSONTok(t, srv, s, "/api/edge/sink", `{"do":"add","kind":"webhook","url":"http://x/y"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("unwired sink = %d, want 501", resp.StatusCode)
	}
	if sinkSnap(t, s, srv).SinkManage {
		t.Errorf("a build that cannot change sinks says it can")
	}
}

// A refused sink is refused with a reason, and nothing is stored.
func TestTheConsoleRefusesASinkThatCannotWork(t *testing.T) {
	s, srv, _ := sinkRig(t, true)
	for _, body := range []string{
		`{"do":"add","kind":"webhook","url":""}`,
		`{"do":"add","kind":"pigeon","url":"https://x/y"}`,
		`{"do":"add","kind":"email","url":"mailto:a@b"}`,
		`{"do":"sideways","id":"sk_1"}`,
	} {
		resp := postJSONTok(t, srv, s, "/api/edge/sink", body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s was accepted", body)
		}
	}
	if got := sinkSnap(t, s, srv).Sinks; len(got) != 0 {
		t.Errorf("a refused sink was stored: %+v", got)
	}
}
