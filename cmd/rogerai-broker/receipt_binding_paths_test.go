package main

// End-to-end proof that the E3 binding guard is live on the STREAMING and AUDIO relay
// paths, not just the chat path.
//
// These exist because the first cut of the binding tests was vacuous: it asserted the
// money outcome from zero-valued fields without running a relay, so the suite passed
// with the guards deleted. Each test here drives the real relay and reads the real
// ledger, and each is mutation-checked - deleting its guard makes it fail.

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
)

// respondUnbound answers the next job with a signature-VALID receipt that names a
// different job, so only BindsTo can reject it.
func respondUnbound(tun *nodeTunnel, nodeID string, nodePriv ed25519.PrivateKey, body string, badReq, badNode string) {
	go func() {
		job := <-tun.jobs
		reqID, nID := job.ID, nodeID
		switch badReq {
		case "":
			// leave the real id
		case "-":
			reqID = "" // an EMPTY request id
		default:
			reqID = badReq
		}
		if badNode != "" {
			nID = badNode
		}
		rec := protocol.UsageReceipt{
			RequestID: reqID, NodeID: nID, Model: "m",
			PromptTokens: 9, CompletionTokens: 5, PriceIn: 7, PriceOut: 7, TS: time.Now().Unix(),
		}
		rec.SignNode(nodePriv) // genuinely valid signature
		res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte(body), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()
}

// TestStreamRelayRefusesUnboundReceipt: the streaming path must not settle a receipt
// that names another request, and the consumer's hold must come back.
func TestStreamRelayRefusesUnboundReceipt(t *testing.T) {
	for _, tc := range []struct{ name, badReq, badNode string }{
		{"foreign request", "req-FOREIGN", ""},
		{"empty request", "-", ""},
		{"foreign node", "", "n-OTHER"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := store.NewMem()
			b := relayBroker(db)
			nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
			b.nodes["s-node"] = protocol.NodeRegistration{
				NodeID: "s-node", PubKey: hex.EncodeToString(nodePub),
				Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 7, PriceOut: 7, Ctx: 4096}},
			}
			b.lastSeen["s-node"] = time.Now()
			tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
			b.tunnels["s-node"] = tun
			require.NoError(t, db.BindNode("s-node", "ownerStream"))

			_, userPriv, _ := ed25519.GenerateKey(nil)
			userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
			require.NoError(t, db.BindOwner(store.Owner{GitHubID: 21, Login: "streamer", Pubkey: userPubHex}))
			_, err := db.AddCredits("u_gh_21", 1000)
			require.NoError(t, err)
			before, _ := db.PeekBalance("u_gh_21")

			respondUnbound(tun, "s-node", nodePriv, `{"choices":[{"message":{"content":"hi"}}]}`, tc.badReq, tc.badNode)

			reqBody := []byte(`{"model":"m","max_tokens":8,"stream":true}`)
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
			signReq(r, userPriv, reqBody)
			w := httptest.NewRecorder()
			b.relay(w, r)

			after, _ := db.PeekBalance("u_gh_21")
			earn, _ := db.EarningsOf("s-node")
			require.Equal(t, before, after, "an unbound receipt must leave the consumer's balance untouched")
			require.Zero(t, earn, "an unbound receipt must mint no earning")
		})
	}
}

// TestAudioRelayRefusesUnboundReceipt: same guarantee on the audio money path, which
// holds the FULL cost up front, so a stranded hold is the whole charge.
func TestAudioRelayRefusesUnboundReceipt(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["a-node"] = protocol.NodeRegistration{
		NodeID: "a-node", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{
			Model: "v", PriceIn: 50, Ctx: 4096,
			Modality: protocol.ModalityTTS, Name: "Voice", Language: "en-US",
		}},
	}
	b.lastSeen["a-node"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["a-node"] = tun
	require.NoError(t, db.BindNode("a-node", "ownerAudio"))

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	require.NoError(t, db.BindOwner(store.Owner{GitHubID: 22, Login: "listener", Pubkey: userPubHex}))
	_, err := db.AddCredits("u_gh_22", 1000)
	require.NoError(t, err)
	before, _ := db.PeekBalance("u_gh_22")

	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: "req-FOREIGN", NodeID: "a-node", Model: "v",
			PromptTokens: 10, PriceIn: 50, TS: time.Now().Unix(),
		}
		rec.SignNode(nodePriv) // valid signature, wrong job
		res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte("RIFF0000WAVEdata"), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	reqBody := []byte(fmt.Sprintf(`{"model":"v","input":%q}`, "hello there"))
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(string(reqBody)))
	signReq(r, userPriv, reqBody)
	w := httptest.NewRecorder()
	b.audioRelay(w, r)

	after, _ := db.PeekBalance("u_gh_22")
	earn, _ := db.EarningsOf("a-node")
	require.Equal(t, before, after, "an unbound audio receipt must leave the consumer's balance untouched")
	require.Zero(t, earn, "an unbound audio receipt must mint no earning")
	require.NotEqual(t, http.StatusOK, w.Code, "an unbound audio receipt must not return a 200 body")
}
