package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

func dogfoodMsgs() []chatMsg { return []chatMsg{{Role: "user", Content: "hi ping"}} }

// TestDogfoodGrantRelayMisses locks every fall-through miss of the grant-dogfood path, so
// the Ping widget never breaks: no key, an unresolved key, a model the grant denies, an
// owner with no bound nodes, and a grant whose node is off air all return served=false.
func TestDogfoodGrantRelayMisses(t *testing.T) {
	// (a) No grant key configured.
	b := relayBroker(store.NewMem())
	b.concierge = &concierge{grantModel: "m", maxTokens: 64}
	if _, served := b.dogfoodGrantRelay(dogfoodMsgs()); served {
		t.Error("no grant key must not serve")
	}

	// (b) Key set but resolves to no stored grant.
	b.concierge.grantKey = "rog-grant_unknown"
	if _, served := b.dogfoodGrantRelay(dogfoodMsgs()); served {
		t.Error("unresolved grant key must not serve")
	}

	// (c) Grant exists but DENIES the concierge model.
	db := store.NewMem()
	b2 := relayBroker(db)
	owner := "ownerA"
	_ = db.BindNode("na", owner)
	makeGrant(t, db, store.Grant{ID: "g_md", Owner: owner, Free: true, Models: []string{"other"}}, "rog-grant_md")
	b2.concierge = &concierge{grantKey: "rog-grant_md", grantModel: "m", maxTokens: 64}
	if _, served := b2.dogfoodGrantRelay(dogfoodMsgs()); served {
		t.Error("model-denied grant must not serve")
	}

	// (d) Grant owner has NO bound nodes (empty nodeAllow).
	db3 := store.NewMem()
	b3 := relayBroker(db3)
	makeGrant(t, db3, store.Grant{ID: "g_none", Owner: "lonely", Free: true}, "rog-grant_none")
	b3.concierge = &concierge{grantKey: "rog-grant_none", grantModel: "m", maxTokens: 64}
	if _, served := b3.dogfoodGrantRelay(dogfoodMsgs()); served {
		t.Error("ownerless grant must not serve")
	}

	// (e) Owner has a bound node but it is OFF AIR (no lastSeen / no offer) -> no station.
	db4 := store.NewMem()
	b4 := relayBroker(db4)
	_ = db4.BindNode("offair", "ownerB")
	makeGrant(t, db4, store.Grant{ID: "g_off", Owner: "ownerB", Free: true}, "rog-grant_off")
	b4.concierge = &concierge{grantKey: "rog-grant_off", grantModel: "m", maxTokens: 64}
	if _, served := b4.dogfoodGrantRelay(dogfoodMsgs()); served {
		t.Error("off-air grant node must not serve")
	}
}

// TestDogfoodGrantRelayServes locks the success path: an on-air grant node answers the
// dogfood job and the assistant text is returned with served=true.
func TestDogfoodGrantRelayServes(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	owner := "ownerLive"
	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes["live"] = protocol.NodeRegistration{
		NodeID: "live", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1, PriceOut: 1, Ctx: 4096}},
	}
	b.lastSeen["live"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["live"] = tun
	_ = db.BindNode("live", owner)
	makeGrant(t, db, store.Grant{ID: "g_live", Owner: owner, Free: true}, "rog-grant_live")
	b.concierge = &concierge{grantKey: "rog-grant_live", grantModel: "m", maxTokens: 64}

	go func() {
		job := <-tun.jobs
		res := protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"content":"ping dogfood reply"}}]}`)}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	reply, served := b.dogfoodGrantRelay(dogfoodMsgs())
	if !served {
		t.Fatal("on-air grant node should serve the dogfood relay")
	}
	if !strings.Contains(reply, "ping dogfood reply") {
		t.Errorf("reply = %q, want the assistant text", reply)
	}
}
