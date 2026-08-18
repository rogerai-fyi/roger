package agent

// tower_attach_reattach_test.go covers the part of the relay join that only shows up on a
// machine's SECOND run, which is why nothing caught it: every other test of this path starts
// from a fresh t.TempDir(), and the bug needs a directory that has been used before.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// attachStub answers /tower/edge/attach the way Core does and records every station id it
// was offered, so a test can see whether the node came back as itself.
func attachStub(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			StationID string `json:"station_id"`
			NodeID    string `json:"node_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.StationID)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"station_id": body.StationID, "tower_id": "tw-1",
			"endpoint": "203.0.113.9:8443", "hub_token": "t", "state": "active",
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// THE SECOND SHARE ON A HOST MUST STILL REACH THE RELAY FABRIC.
//
// The station identity is persistent and lives at a FIXED path under the user config dir, so
// the second `roger share` on any machine finds a directory that already holds a Station.
// Minting is the only thing that can happen the first time and exactly the wrong thing every
// time after: the keys Core recorded on the attachment are the ones it verifies receipts
// against, so a re-mint would strand the attachment - which is why station.Init refuses. With
// no way to LOAD, that refusal became an error out of AttachTower, and the caller discards it
// silently, so a host that had attached once never attached again and nothing said so.
func TestAttachTowerReusesThePersistentStationIdentity(t *testing.T) {
	srv, stationsSeen := attachStub(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir() // ONE directory, reused - this is the whole point
	cfg := Config{Broker: srv.URL, NodeID: "brave-otter-m", Model: "m", Modality: "chat"}

	first, at, err := AttachTower(cfg, priv, dir)
	if err != nil {
		t.Fatalf("the first attach failed: %v", err)
	}
	if at.TowerID != "tw-1" {
		t.Fatalf("unexpected attachment: %+v", at)
	}

	for run := 2; run <= 4; run++ {
		again, _, aerr := AttachTower(cfg, priv, dir)
		if aerr != nil {
			t.Fatalf("run %d of `roger share` on this host could not attach: %v", run, aerr)
		}
		if again.StationID != first.StationID {
			t.Fatalf("run %d attached as %s, not %s - a re-mint strands the recorded attachment",
				run, again.StationID, first.StationID)
		}
		if again.Assertion != first.Assertion || again.Session != first.Session {
			t.Fatalf("run %d presented different keys than the ones Core recorded", run)
		}
	}
	seen := stationsSeen()
	if len(seen) != 4 {
		t.Fatalf("Core was offered %d attachments, want 4: %v", len(seen), seen)
	}
	for _, id := range seen {
		if id != first.StationID {
			t.Fatalf("Core was offered a second identity %q beside %q", id, first.StationID)
		}
	}
}

// A directory that holds a HALF a Station is a fault to report, not one to route around by
// minting a new identity: the operator would end up attached under an id no earlier
// attachment names, and the failure would surface far from its cause.
func TestAttachTowerRefusesACorruptStationDirectory(t *testing.T) {
	srv, _ := attachStub(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := Config{Broker: srv.URL, NodeID: "brave-otter-m", Model: "m", Modality: "chat"}
	if _, _, err = AttachTower(cfg, priv, dir); err != nil {
		t.Fatal(err)
	}
	// Lose one of the two key files, the shape a partial copy between machines takes.
	if err = os.Remove(filepath.Join(dir, "tower-station", "session.key")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = AttachTower(cfg, priv, dir); err == nil {
		t.Fatal("a Station directory missing a key attached anyway - a new identity was minted over a recorded one")
	}
}
