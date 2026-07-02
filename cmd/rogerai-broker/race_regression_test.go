package main

// race_regression_test.go - the #52 reviewer's two pre-existing race soft-spots,
// demonstrated with REAL handlers under `go test -race` (they pass without -race;
// the race detector is the assertion):
//
//	(a) register() marshaled reg for the shared-registry mirror OUTSIDE b.mu while
//	    applyOverrideLive can mutate the SAME live offer backing array in place
//	    (under b.mu). syncRegistry already documents the correct pattern: snapshot
//	    the bytes UNDER the lock, publish outside it.
//	(b) the node handlers (heartbeat/agentPoll/agentResult/agentStream) read
//	    t.token with NO lock while register/syncRegistry/rehydrate write it under
//	    b.mu - a torn/unordered read on the node-auth credential.
//
// Each side runs in several goroutines and the racing partner loops UNTIL the
// writers finish (not a fixed count), so the unordered windows genuinely overlap.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// raceReg builds one signed FREE registration body for nodeID (reusable: an
// idempotent re-register of a known node).
func raceReg(t *testing.T, nodeID string) []byte {
	t.Helper()
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: hex.EncodeToString(nodePub), BridgeToken: "tok-" + nodeID,
		TS:     time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096}},
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	return body
}

func racePostRegister(t *testing.T, b *broker, body []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	w := httptest.NewRecorder()
	b.register(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("register = %d: %s", w.Code, w.Body.String())
	}
}

// TestRaceRegisterMirrorVsLiveOverride: re-registers (whose shared-registry mirror
// marshals the just-stored reg) race a web-console price override mutating the live
// offer in place. -race flags the unsynchronized marshal read vs the locked write.
func TestRaceRegisterMirrorVsLiveOverride(t *testing.T) {
	vs, err := newValkeyStore(xiRedisURL(t))
	if err != nil {
		t.Fatalf("valkey: %v", err)
	}
	t.Cleanup(func() { _ = vs.Close() })
	b := relayBroker(store.NewMem())
	b.shared = vs // the mirror publish is what marshals reg

	body := raceReg(t, "race-mirror")
	racePostRegister(t, b, body) // prime: the node exists for the override side

	done := make(chan struct{})
	var overrides sync.WaitGroup
	for g := 0; g < 4; g++ {
		overrides.Add(1)
		go func(g int) {
			defer overrides.Done()
			ov := store.OfferOverride{Owner: "op-race", NodeID: "race-mirror", Model: "m", UpdatedAt: time.Now().Unix()}
			for i := 0; ; i++ {
				select {
				case <-done:
					return
				default:
				}
				ov.PriceIn, ov.PriceOut = float64(i%7), float64((i+g)%5)
				b.applyOverrideLive("op-race", ov)
			}
		}(g)
	}
	var registers sync.WaitGroup
	for g := 0; g < 4; g++ {
		registers.Add(1)
		go func() {
			defer registers.Done()
			for i := 0; i < 60; i++ {
				racePostRegister(t, b, body)
			}
		}()
	}
	registers.Wait()
	close(done)
	overrides.Wait()
}

// TestRaceNodeTokenReadVsRegister: heartbeats (authNode's t.token read) race
// re-registers rewriting the bridge token under b.mu. The unlocked read's window is
// tiny (between tunnelFor's unlock and markSeen's lock), so many readers loop until
// the writers finish. -race flags the unlocked credential read.
func TestRaceNodeTokenReadVsRegister(t *testing.T) {
	b := relayBroker(store.NewMem())
	body := raceReg(t, "race-token")
	racePostRegister(t, b, body) // prime: tunnel + token exist

	hb, _ := json.Marshal(map[string]string{"node_id": "race-token"})
	done := make(chan struct{})
	var readers sync.WaitGroup
	for g := 0; g < 8; g++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				r := httptest.NewRequest(http.MethodPost, "/nodes/heartbeat", bytes.NewReader(hb))
				r.Header.Set("Authorization", "Bearer tok-race-token")
				w := httptest.NewRecorder()
				b.heartbeat(w, r)
				if w.Code != http.StatusOK {
					t.Errorf("heartbeat = %d: %s", w.Code, w.Body.String())
				}
			}
		}()
	}
	var writers sync.WaitGroup
	for g := 0; g < 2; g++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; i < 150; i++ {
				racePostRegister(t, b, body)
			}
		}()
	}
	writers.Wait()
	close(done)
	readers.Wait()
}
