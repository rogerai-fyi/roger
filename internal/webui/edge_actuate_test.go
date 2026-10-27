package webui

// THE ACTUATE CEREMONY in the console (features/edge/node_identity.feature). It licenses a machine
// to change the physical world, so the console opens NO new authority: same host operation as the
// terminal, POST-only, token-gated, confirmed in the browser first, and the host - never the
// browser - says who confirmed it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func actuateRig(t *testing.T, wire bool) (*Server, *httptest.Server, *edge.Fleet, *[]string) {
	t.Helper()
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	n := edge.NewNode(nil, "shed-door", edge.Board)
	n.ID = "n_door"
	n.Presence = string(edge.PresenceVerified)
	n.Caps = []store.EdgeCap{{Name: string(edge.Actuate), State: string(edge.Claimed)}}
	if _, err := fleet.Enroll(n); err != nil {
		t.Fatal(err)
	}
	confirmed := []string{}
	hooks := EdgeHooks{Self: "workshop", Fleet: fleet,
		Name:   func(id, name string) error { return fleet.Rename(id, name) },
		Forget: func(id string) error { return fleet.Forget(id) },
	}
	if wire {
		hooks.ConfirmActuate = func(id string) error {
			confirmed = append(confirmed, id)
			// The HOST supplies who; the request never carried it.
			return fleet.ConfirmActuate(id, "the-host-operator")
		}
	}
	s := New(testCtrl(), Options{Edge: hooks})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, fleet, &confirmed
}

func TestTheConsoleConfirmsActuateThroughTheHost(t *testing.T) {
	s, srv, fleet, confirmed := actuateRig(t, true)
	resp := postJSONTok(t, srv, s, "/api/edge/actuate", `{"id":"n_door"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("actuate = %d, want 200", resp.StatusCode)
	}
	if len(*confirmed) != 1 || (*confirmed)[0] != "n_door" {
		t.Fatalf("the host was not asked to confirm: %v", *confirmed)
	}
	got, _, _ := fleet.Get("n_door")
	for _, c := range got.Caps {
		if c.Name != string(edge.Actuate) {
			continue
		}
		if c.State != string(edge.Verified) {
			t.Errorf("actuate is %q, want VERIFIED", c.State)
		}
		if c.ConfirmedBy != "the-host-operator" {
			t.Errorf("confirmed by %q - the host's identity is the only one that counts", c.ConfirmedBy)
		}
		if c.ConfirmedAt == 0 {
			t.Errorf("the confirmation recorded no time")
		}
	}
}

// The request carries the node and NOTHING ELSE. A "who" the requester can name themselves for is
// worth nothing, so even when one is sent it must not be believed.
func TestTheBrowserIsNeverBelievedAboutWhoConfirmed(t *testing.T) {
	s, srv, fleet, _ := actuateRig(t, true)
	resp := postJSONTok(t, srv, s, "/api/edge/actuate", `{"id":"n_door","who":"somebody-else","confirmed_by":"admin"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("actuate = %d", resp.StatusCode)
	}
	got, _, _ := fleet.Get("n_door")
	for _, c := range got.Caps {
		if c.Name == string(edge.Actuate) && c.ConfirmedBy != "the-host-operator" {
			t.Errorf("the browser's word for who was taken: %q", c.ConfirmedBy)
		}
	}
}

func TestTheCeremonyIsGuardedLikeEveryOtherMutation(t *testing.T) {
	s, srv, _, _ := actuateRig(t, true)
	// No token: refused.
	resp, err := http.Post(srv.URL+"/api/edge/actuate", "application/json", strings.NewReader(`{"id":"n_door"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("an untokened confirmation was accepted")
	}
	// GET: refused. A capability is never granted by a link somebody can be sent.
	resp, err = http.Get(srv.URL + "/api/edge/actuate?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("actuate could be confirmed with a GET")
	}
	// No id: refused before anything is touched.
	bad := postJSONTok(t, srv, s, "/api/edge/actuate", `{}`)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("a confirmation with no node = %d, want 400", bad.StatusCode)
	}
}

// A build with no ceremony wired says so rather than pretending, and draws no button.
func TestAnUnwiredCeremonyIsHonestlyDisabled(t *testing.T) {
	s, srv, _, _ := actuateRig(t, false)
	resp := postJSONTok(t, srv, s, "/api/edge/actuate", `{"id":"n_door"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("unwired actuate = %d, want 501", resp.StatusCode)
	}
	got, err := http.Get(srv.URL + "/api/edge?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	var snap edgeSnap
	if err := json.NewDecoder(got.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.ActuateManage {
		t.Errorf("a build with no ceremony wired says it can perform one")
	}
}

// The browser asks before it sends, and says what the ceremony licenses.
func TestTheBrowserAsksBeforeItConfirms(t *testing.T) {
	b, err := os.ReadFile("assets/console.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	i := strings.Index(js, "function edgeConfirmActuate")
	if i < 0 {
		t.Fatal("the console has no actuate ceremony")
	}
	body := js[i : i+900]
	for _, want := range []string{"window.confirm", "PHYSICAL WORLD", "/api/edge/actuate"} {
		if !strings.Contains(body, want) {
			t.Errorf("the ceremony is missing %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "window.confirm") > strings.Index(body, "apiPost") {
		t.Errorf("the ceremony posts before it asks")
	}
}
