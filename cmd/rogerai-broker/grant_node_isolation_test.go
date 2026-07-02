package main

// grant_node_isolation_test.go enforces features/voice/grant_node_isolation.feature (audit
// BLOCKER #2): the voice money path must confine a grant to its owner's nodes + model
// allow-list, exactly as the chat relay does. Drives the REAL /v1/audio/speech path (no
// mocks): on-air TTS nodes under distinct owners, a grant scoped to one, asserting a foreign
// node is never served and an out-of-list model is refused before dispatch. RED before the
// audio.go `allow = gc.nodeAllow` + `gc.modelDenied` fix (audio.go currently passes allow=nil).

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type isoNode struct {
	id, owner string
	voices    []string // tts voice model ids this node offers
	onAir     bool
}

// runVoiceGrantRelay registers the given TTS nodes (each bound to its owner), mints a free
// grant scoped to grantNodes/grantModels under grantOwner, and POSTs a grant-authenticated
// /v1/audio/speech for reqVoice. Returns the HTTP status and which nodes actually served.
func runVoiceGrantRelay(t *testing.T, nodes []isoNode, grantOwner string, grantNodes, grantModels []string, reqVoice string) (int, map[string]bool) {
	t.Helper()
	mem := store.NewMem()
	b := relayBroker(mem)
	b.grantRL = loadRateLimiter()

	served := map[string]*atomic.Bool{}
	for _, n := range nodes {
		served[n.id] = &atomic.Bool{}
	}
	for _, n := range nodes {
		nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
		offers := make([]protocol.ModelOffer, 0, len(n.voices))
		for _, v := range n.voices {
			offers = append(offers, protocol.ModelOffer{Model: v, Modality: protocol.ModalityTTS, Name: v, PriceIn: 0})
		}
		if n.onAir {
			b.nodes[n.id] = protocol.NodeRegistration{NodeID: n.id, PubKey: hex.EncodeToString(nodePub), Station: n.owner, Offers: offers}
			b.lastSeen[n.id] = time.Now()
		}
		tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
		b.tunnels[n.id] = tun
		if err := mem.BindNode(n.id, n.owner); err != nil {
			t.Fatal(err)
		}
		recModel := ""
		if len(n.voices) > 0 {
			recModel = n.voices[0]
		}
		id, priv, flag := n.id, nodePriv, served[n.id]
		go func() {
			job, ok := <-tun.jobs
			if !ok {
				return
			}
			flag.Store(true)
			rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: id, Model: recModel, TS: time.Now().Unix()}
			rec.SignNode(priv)
			res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte("~audio~"), Receipt: rec}
			tun.mu.Lock()
			ch := tun.waiters[job.ID]
			tun.mu.Unlock()
			if ch != nil {
				ch <- res
			}
		}()
	}

	secret := "rog-grant_voiceiso"
	sum := sha256.Sum256([]byte(secret))
	if err := mem.CreateGrant(store.Grant{
		ID: "g_voiceiso", SecretHash: hex.EncodeToString(sum[:]),
		Owner: grantOwner, Nodes: grantNodes, Models: grantModels, Free: true, RPM: 6000, Burst: 600,
	}); err != nil {
		t.Fatal(err)
	}

	body := []byte(fmt.Sprintf(`{"model":%q,"input":"the operator is on the air","response_format":"mp3"}`, reqVoice))
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	b.audioRelay(w, r)

	out := map[string]bool{}
	for id, f := range served {
		out[id] = f.Load()
	}
	return w.Code, out
}

// V1 + the takeover: a grant scoped to owner A's (currently off-air) node must NEVER be served
// by owner B's on-air node. Today (allow=nil) the only on-air node — B's — serves it, so the
// grant escapes to a third party's hardware. Fixed: 503, B untouched.
func TestVoiceGrantCannotEscapeToForeignNode(t *testing.T) {
	code, served := runVoiceGrantRelay(t,
		[]isoNode{
			{id: "a1", owner: "ownerA", voices: []string{"af_heart"}, onAir: false},
			{id: "b1", owner: "ownerB", voices: []string{"af_heart"}, onAir: true},
		},
		"ownerA", []string{"a1"}, nil, "af_heart")
	if served["b1"] {
		t.Fatalf("grant scoped to owner A was served by owner B's node b1 - cross-owner voice escape (code %d)", code)
	}
	if code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when the grant owner's only node is off air, got %d", code)
	}
}

// V3: a grant is refused a voice model outside its allow-list, before any dispatch/hold.
func TestVoiceGrantModelDeniedBeforeDispatch(t *testing.T) {
	code, served := runVoiceGrantRelay(t,
		[]isoNode{{id: "a1", owner: "ownerA", voices: []string{"af_heart", "am_onyx"}, onAir: true}},
		"ownerA", []string{"a1"}, []string{"af_heart"}, "am_onyx")
	if served["a1"] {
		t.Fatalf("grant served a model outside its allow-list (am_onyx) - model scope bypass (code %d)", code)
	}
	if code != http.StatusServiceUnavailable {
		t.Errorf("want 503 for a voice model outside the grant's allow-list, got %d", code)
	}
}

// V5 no-regression: a grant legitimately scoped to the serving node + model still serves 200.
func TestVoiceGrantServesOwnScopedNode(t *testing.T) {
	code, served := runVoiceGrantRelay(t,
		[]isoNode{{id: "a1", owner: "ownerA", voices: []string{"af_heart"}, onAir: true}},
		"ownerA", []string{"a1"}, []string{"af_heart"}, "af_heart")
	if !served["a1"] {
		t.Fatalf("grant scoped to its own node a1 was not served it (code %d)", code)
	}
	if code != http.StatusOK {
		t.Errorf("want 200 for an in-scope grant request, got %d", code)
	}
}
