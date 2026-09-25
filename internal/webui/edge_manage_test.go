package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// The console's Name and Forget actions go through the SAME host operations the TUI and CLI use
// (features/edge/manage_web.feature): a rename is not a browser-only label, and a forget is never a
// silent delete. These drive the real Server over httptest with its token.

func manageRig(t *testing.T) (*Server, *httptest.Server, *edge.Fleet, *[]string, *[]string) {
	t.Helper()
	db := store.NewMem()
	fleet := edge.NewFleet(db, "acct-1")
	n := edge.NewNode(nil, "attic-pi", edge.Host)
	n.ID = "n_attic"
	n.Presence = string(edge.PresenceVerified)
	if _, err := fleet.Enroll(n); err != nil {
		t.Fatal(err)
	}
	named := []string{}
	forgot := []string{}
	s := New(testCtrl(), Options{Edge: EdgeHooks{
		Self:  "workshop",
		Fleet: fleet,
		Name: func(id, name string) error {
			named = append(named, id+"="+name)
			return fleet.Rename(id, name)
		},
		Forget: func(id string) error {
			forgot = append(forgot, id)
			return fleet.Forget(id)
		},
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, fleet, &named, &forgot
}

func postJSONTok(t *testing.T, srv *httptest.Server, s *Server, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+path+"?t="+s.Token(), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestConsoleNameRenamesThroughTheHost(t *testing.T) {
	s, srv, fleet, named, _ := manageRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/name", `{"id":"n_attic","name":"garage-pi"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("name = %d, want 200", resp.StatusCode)
	}
	if len(*named) != 1 || (*named)[0] != "n_attic=garage-pi" {
		t.Fatalf("host Name not called as expected: %v", *named)
	}
	got, _, _ := fleet.Get("n_attic")
	if got.Name != "garage-pi" {
		t.Fatalf("node name = %q, want garage-pi (renamed through the host, not a browser label)", got.Name)
	}
	// The fresh snapshot returns so the tab shows the change.
	var snap edgeSnap
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
}

func TestConsoleForgetRemovesThroughTheHost(t *testing.T) {
	s, srv, fleet, _, forgot := manageRig(t)
	resp := postJSONTok(t, srv, s, "/api/edge/forget", `{"id":"n_attic"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forget = %d, want 200", resp.StatusCode)
	}
	if len(*forgot) != 1 || (*forgot)[0] != "n_attic" {
		t.Fatalf("host Forget not called: %v", *forgot)
	}
	if _, ok, _ := fleet.Get("n_attic"); ok {
		t.Fatalf("node still in fleet after forget")
	}
}

func TestConsoleNameForgetNeedTheToken(t *testing.T) {
	_, srv, _, _, _ := manageRig(t)
	// A write endpoint without the token is refused (CSRF guard), like every other action.
	for _, path := range []string{"/api/edge/name", "/api/edge/forget"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(`{"id":"n_attic","name":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s without token = %d, want 403", path, resp.StatusCode)
		}
	}
}

func TestConsoleNameForgetGETRefused(t *testing.T) {
	s, srv, _, _, _ := manageRig(t)
	// A GET (CSRF-reachable) to a write endpoint is refused even WITH the token.
	for _, path := range []string{"/api/edge/name", "/api/edge/forget"} {
		resp, err := http.Get(srv.URL + path + "?t=" + s.Token())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("GET %s = 200, want a refusal (write endpoints are POST-only)", path)
		}
	}
}

func TestConsoleNameForgetDisabledWhenUnwired(t *testing.T) {
	// No Name/Forget hooks: the endpoints say so rather than pretending.
	s := New(testCtrl(), Options{Edge: EdgeHooks{Fleet: edge.NewFleet(store.NewMem(), "acct-1")}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	for _, path := range []string{"/api/edge/name", "/api/edge/forget"} {
		resp := postJSONTok(t, srv, s, path, `{"id":"n_x","name":"y"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s unwired = %d, want 501", path, resp.StatusCode)
		}
	}
}
