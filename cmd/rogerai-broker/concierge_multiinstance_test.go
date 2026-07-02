package main

// concierge_multiinstance_test.go pins that the homepage-Ping dogfood relay works when the
// picked band node's poller lives on a PEER instance (audit finding #7). dogfoodRelay/
// grantRelayOnce awaited only a LOCAL waiter, but multi-instance agentResult publishes the
// result to the bus, so resCh never filled -> Ping hung the full relayWait then fell back to
// Groq on every band-node pick. Real two-instance miniredis bus, no mocks (reuses newMIBroker).

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

func TestConciergeDogfoodServesAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)     // Ping runs here
	bInst := newMIBroker(t, brokerPriv, db, mr) // provider polls here

	// A free band node, registered on BOTH instances (pick runs on A, poll on B).
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	const token = "tok-band"
	offers := []protocol.ModelOffer{{Model: "free-m"}}
	miRegisterNode(a, "band", hex.EncodeToString(nodePub), token, offers)
	miRegisterNode(bInst, "band", hex.EncodeToString(nodePub), token, offers)

	// A directly-built concierge so relayWait() is a short 2s (the pre-fix RED fails fast
	// instead of the 30s default); screen off, proven-live gate inert (no probe).
	a.concierge = &concierge{maxTokens: 64, relayTimeout: 2 * time.Second}

	// Provider long-polls instance B, then posts the signed result back to B over the bus.
	done := make(chan struct{})
	go func() {
		defer close(done)
		job, ok := pollOnce(t, bInst, "band", token)
		if !ok {
			return
		}
		res := miSignedResult(job.ID, "band", "free-m", "hello from B", nodePriv, 200)
		raw, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node=band", strings.NewReader(string(raw)))
		rr.Header.Set("Authorization", miNodeBearer(token))
		bInst.agentResult(httptest.NewRecorder(), rr)
	}()
	time.Sleep(150 * time.Millisecond) // let B's poll subscribe on the bus before dispatch

	reply, served := a.dogfoodRelay(dogfoodMsgs())
	<-done

	if !served {
		t.Fatalf("dogfoodRelay did not serve across instances (Ping would hang then fall back to Groq); reply=%q", reply)
	}
	if !strings.Contains(reply, "hello from B") {
		t.Errorf("reply = %q, want it to contain the peer-served completion", reply)
	}
}
