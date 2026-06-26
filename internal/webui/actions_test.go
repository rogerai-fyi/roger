package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/node"
)

// onairCtrl is a controller wired to a fake broker so a real session can start, with two
// free models in the catalog.
func onairCtrl(t *testing.T) *node.Controller {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	t.Cleanup(srv.Close)
	c := node.New(node.Config{Broker: srv.URL, Station: "amber-fox"})
	c.SetRows([]node.ShareRow{
		{Model: "m1", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
		{Model: "m2", Ctx: 8192, Upstream: "http://127.0.0.1:0/v1/chat/completions"},
	})
	return c
}

func postAction(t *testing.T, srv *httptest.Server, token, path, body string) actionResp {
	t.Helper()
	resp, err := http.Post(srv.URL+path+"?t="+token, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s = %d, want 200", path, resp.StatusCode)
	}
	var ar actionResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return ar
}

func TestOnAirActionTogglesNodeAndTUIWouldSee(t *testing.T) {
	c := onairCtrl(t)
	s := New(c)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ar := postAction(t, srv, s.Token(), "/api/share/onair", `{"model":"m1"}`)
	if !ar.OK || ar.Snapshot.OnAir != 1 {
		t.Fatalf("onair toggle: ok=%v on_air=%d, want ok + 1", ar.OK, ar.Snapshot.OnAir)
	}
	// The controller (which the TUI shares) really has the session now.
	if c.OnAirCount() != 1 {
		t.Fatalf("controller on-air = %d, want 1 (the TUI would render it too)", c.OnAirCount())
	}
	var m1 node.RowView
	for _, rv := range ar.Snapshot.Rows {
		if rv.Model == "m1" {
			m1 = rv
		}
	}
	if !m1.OnAir {
		t.Fatal("snapshot row m1 should be on air")
	}

	// Toggle off.
	ar = postAction(t, srv, s.Token(), "/api/share/onair", `{"model":"m1"}`)
	if ar.Snapshot.OnAir != 0 || c.OnAirCount() != 0 {
		t.Fatalf("off toggle: snapshot=%d controller=%d, want 0/0", ar.Snapshot.OnAir, c.OnAirCount())
	}
	c.StopAll()
}

func TestPricedOnAirActionReportsLoginNeeded(t *testing.T) {
	c := onairCtrl(t)
	c.SetPricing("m1", node.Pricing{Out: 2})
	s := New(c)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ar := postAction(t, srv, s.Token(), "/api/share/onair", `{"model":"m1"}`)
	if !ar.LoginNeeded || ar.Snapshot.OnAir != 0 {
		t.Fatalf("priced onair without login: login_needed=%v on_air=%d, want true/0", ar.LoginNeeded, ar.Snapshot.OnAir)
	}
}

func TestRenameAction(t *testing.T) {
	c := onairCtrl(t)
	s := New(c)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ar := postAction(t, srv, s.Token(), "/api/share/rename", `{"station":"violet-owl"}`)
	if ar.Snapshot.Station != "violet-owl" || c.Station() != "violet-owl" {
		t.Fatalf("rename: snapshot=%q controller=%q, want violet-owl", ar.Snapshot.Station, c.Station())
	}
}

func TestPriceAction(t *testing.T) {
	c := onairCtrl(t)
	s := New(c)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ar := postAction(t, srv, s.Token(), "/api/share/price", `{"model":"m1","in":1,"out":3,"windows":[{"start":"03:00","end":"03:30","free":true}]}`)
	if !ar.OK {
		t.Fatalf("price action not ok: %+v", ar)
	}
	if p := c.PricingFor("m1"); p.Out != 3 || len(p.Windows) != 1 {
		t.Fatalf("PricingFor m1 = %+v, want out 3 + 1 window", p)
	}
}

func TestWriteActionsRejectGET(t *testing.T) {
	c := onairCtrl(t)
	s := New(c)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	// A GET (CSRF-reachable) to a write endpoint must be refused even WITH the token.
	resp, err := http.Get(srv.URL + "/api/share/onair?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET on write action = %d, want 405", resp.StatusCode)
	}
	if c.OnAirCount() != 0 {
		t.Fatal("a GET must never have mutated state")
	}
}

func TestWriteActionRequiresToken(t *testing.T) {
	c := onairCtrl(t)
	s := New(c)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/share/onair", "application/json", strings.NewReader(`{"model":"m1"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write without token = %d, want 403", resp.StatusCode)
	}
}
