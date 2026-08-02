package main

// Executable scenarios for the AUDIO half of features/relay/browser_session.feature:
// /v1/audio/speech and /v1/audio/transcriptions share one spine with the chat relay
// (audioRelayCore), so they inherit the same door - an allowlisted Origin, the
// session cookie as identity, cookieless callers pinned to the anonymous per-IP
// bucket. This is what lets the Playbox speak with a REAL station's voice.

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
)

// pbVoice puts one TTS station on air at the given price and answers the next job
// with canned audio bytes.
func pbVoice(b *broker, db store.Store, nodeID, owner, voice string, price float64) {
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes[nodeID] = protocol.NodeRegistration{
		NodeID: nodeID, PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: voice, Modality: protocol.ModalityTTS, PriceIn: price}},
	}
	b.lastSeen[nodeID] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels[nodeID] = tun
	_ = db.BindNode(nodeID, owner)
	go func() {
		job, ok := <-tun.jobs
		if !ok {
			return
		}
		rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: nodeID, Model: voice, TS: time.Now().Unix()}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte("~audio bytes~"), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}()
}

func pbSpeechReq(voice, origin, cookie string) *http.Request {
	body := `{"model":"` + voice + `","input":"Roger, roger."}`
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	return r
}

func TestPlayboxAudioPreflight(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)

	for _, tc := range []struct{ name, path string }{
		{"speech", "/v1/audio/speech"},
		{"transcriptions", "/v1/audio/transcriptions"},
	} {
		t.Run(tc.name+": allowlisted origin is granted with credentials", func(t *testing.T) {
			r := httptest.NewRequest(http.MethodOptions, tc.path, nil)
			r.Header.Set("Origin", pbOrigin)
			w := httptest.NewRecorder()
			if tc.path == "/v1/audio/speech" {
				b.audioRelay(w, r)
			} else {
				b.transcribeRelay(w, r)
			}
			require.Equal(t, http.StatusNoContent, w.Code)
			require.Equal(t, pbOrigin, w.Header().Get("Access-Control-Allow-Origin"))
			require.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
		})
		t.Run(tc.name+": a foreign origin gets no grant", func(t *testing.T) {
			r := httptest.NewRequest(http.MethodOptions, tc.path, nil)
			r.Header.Set("Origin", "https://evil.example")
			w := httptest.NewRecorder()
			if tc.path == "/v1/audio/speech" {
				b.audioRelay(w, r)
			} else {
				b.transcribeRelay(w, r)
			}
			require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}

func TestPlayboxAudioSessionSpeaksFreeVoice(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbVoice(b, db, "v-free", "owner-v", "roger-operator-voice", 0)
	cookie := b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())

	w := httptest.NewRecorder()
	b.audioRelay(w, pbSpeechReq("roger-operator-voice", pbOrigin, cookie))

	body := readBody(w)
	require.Equal(t, http.StatusOK, w.Code, body)
	require.Contains(t, body, "audio bytes")
	require.Equal(t, pbOrigin, w.Header().Get("Access-Control-Allow-Origin"),
		"the audio response must carry the credentialed CORS headers")
}

func TestPlayboxAudioAnonBrowserFreeVoice(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbVoice(b, db, "v-free", "owner-v", "roger-operator-voice", 0)

	w := httptest.NewRecorder()
	b.audioRelay(w, pbSpeechReq("roger-operator-voice", pbOrigin, ""))
	require.Equal(t, http.StatusOK, w.Code, readBody(w))
}

func TestPlayboxAudioAnonBrowserPaidVoiceSignIn(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbVoice(b, db, "v-paid", "owner-v", "paid-voice", 15)

	w := httptest.NewRecorder()
	b.audioRelay(w, pbSpeechReq("paid-voice", pbOrigin, ""))
	body := readBody(w)
	// The audio relay's own paid gate answers 403 (its long-standing contract), not
	// the chat relay's 401 - either way nothing is spent and the page says sign in.
	require.Equal(t, http.StatusForbidden, w.Code, body)
	require.Contains(t, body, "sign in", "the error must tell the visitor to sign in")
	earn, _ := db.EarningsOf("v-paid")
	require.Zero(t, earn, "no station may be paid for a refused request")
}

func TestPlayboxAudioCookieNeedsAllowlistedOrigin(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	pbVoice(b, db, "v-paid", "owner-v", "paid-voice", 15)
	_, _ = db.AddCredits("u_gh_7", 500)
	valid := b.signSession("octocat", 7, time.Now().Add(time.Hour).Unix())

	for _, tc := range []struct{ name, origin, cookie string }{
		{"foreign origin: the cookie is ignored (CSRF)", "https://evil.example", valid},
		{"no origin header: the cookie is ignored (CSRF)", "", valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, _ := db.PeekBalance("u_gh_7")
			w := httptest.NewRecorder()
			b.audioRelay(w, pbSpeechReq("paid-voice", tc.origin, tc.cookie))
			require.Equal(t, http.StatusUnauthorized, w.Code, readBody(w))
			end, _ := db.PeekBalance("u_gh_7")
			require.Equal(t, start, end, "no wallet may be touched")
		})
	}
}

func TestPlayboxAudioSpoofedOriginLegacyIDStaysAnon(t *testing.T) {
	db := store.NewMem()
	b := pbBroker(t, db)
	b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}
	pbVoice(b, db, "v-free", "owner-v", "roger-operator-voice", 0)

	r := pbSpeechReq("roger-operator-voice", pbOrigin, "")
	r.Header.Set("X-Roger-User", "rotating-id-1")
	w := httptest.NewRecorder()
	b.audioRelay(w, r)
	require.Equal(t, http.StatusOK, w.Code, readBody(w))

	r = pbSpeechReq("roger-operator-voice", pbOrigin, "")
	r.Header.Set("X-Roger-User", "rotating-id-2")
	w = httptest.NewRecorder()
	b.audioRelay(w, r)
	require.Equal(t, http.StatusTooManyRequests, w.Code,
		"a rotated legacy id behind a spoofed Origin must stay inside the ONE per-IP anon bucket")
}
