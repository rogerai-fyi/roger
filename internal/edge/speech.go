package edge

// SPEECH - a member's voice served at its own LAN face (features/lan/voice.feature). Text in,
// audio out, so it is its own path beside /edge/infer: each shape keeps its own body limit. The
// caller is authenticated exactly as a chat turn is (caller), the reply carries a local receipt
// the serving node signs, and nothing is billed.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"rogerai.fm/roger/v7/internal/protocol"
)

// SpeechPath is the face's text-to-speech endpoint.
const SpeechPath = "/edge/speech"

const (
	speechBodyCap  = 64 << 10 // ruled 2026-10-07: `roger say` speaks a line, not a book
	speechAudioCap = 32 << 20 // a spoken line is small; a runaway body is cut off, never trusted
)

// handleSpeech serves one member's speech request: authenticate, find the voice, relay the text to
// the local voice server, and receipt it.
func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		inferError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	callerID, status, msg := s.caller(r)
	if status != 0 {
		inferError(w, status, msg)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, speechBodyCap+1))
	if err != nil || len(body) > speechBodyCap {
		inferError(w, http.StatusRequestEntityTooLarge, "speech request exceeds the 64 KiB limit")
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" {
		inferError(w, http.StatusBadRequest, "a speech request names the voice it wants")
		return
	}
	if callerID, status, msg = s.onBehalf(r, ActionSpeech, body, callerID); status != 0 {
		inferError(w, status, msg)
		return
	}
	var speechURL, key, instance string
	ok := false
	if s.serving.Speech != nil {
		speechURL, key, instance, ok = s.serving.Speech(req.Model)
	}
	if !ok {
		if _, _, _, chat := s.serving.Upstream(req.Model); chat {
			inferError(w, http.StatusBadRequest, req.Model+" is not a voice: this node serves it as a chat band")
			return
		}
		inferError(w, http.StatusNotFound, "this node does not serve "+req.Model+"; it serves: "+strings.Join(s.servedBands(), ", "))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), inferTimeout)
	defer cancel()
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, speechURL, bytes.NewReader(body))
	if err != nil {
		inferError(w, http.StatusBadGateway, err.Error())
		return
	}
	up.Header.Set("Content-Type", "application/json")
	if key != "" {
		up.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(up)
	if err != nil {
		inferError(w, http.StatusBadGateway, "the voice on this node did not answer")
		return
	}
	defer resp.Body.Close()
	audio, rerr := io.ReadAll(io.LimitReader(resp.Body, speechAudioCap+1))
	if resp.StatusCode >= 400 || rerr != nil || len(audio) > speechAudioCap {
		// Nothing complete to hand back: say so, never pass part of an answer as the whole, and
		// never echo the voice server's own body (it can carry the key this node sent it).
		inferError(w, http.StatusBadGateway, fmt.Sprintf("the voice on this node failed (status %d)", resp.StatusCode))
		return
	}
	requestID := r.Header.Get("X-Roger-Request")
	if requestID == "" {
		requestID = fmt.Sprintf("edge-%d", s.now().UnixNano())
	}
	rec := s.localReceipt(requestID, instance, req.Model, callerID)
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "audio/wav"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-RogerAI-Provider", s.serving.NodeID+"/"+instance)
	w.Header().Set("X-RogerAI-Cost", "0")
	w.Header().Set("X-RogerAI-Receipt", rec)
	w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio)
}

// localReceipt is the encoded, signed local receipt for one served request.
func (s *Server) localReceipt(requestID, instance, model, callerID string) string {
	rec := protocol.UsageReceipt{RequestID: requestID, NodeID: s.serving.NodeID, Instance: instance,
		Model: model, User: callerID, TS: s.now().Unix(), Local: true}
	if len(s.serving.Key) == ed25519.PrivateKeySize {
		rec.SignNode(s.serving.Key)
	}
	return protocol.EncodeReceipt(rec)
}
