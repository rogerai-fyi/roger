package client

// say_lan.go is `roger say` in LAN mode (features/lan/voice.feature): a line is spoken by a voice on
// this machine or on an Edge member, in that order, or refused. There is no market rung: nothing
// here can reach the broker's /v1/audio/speech. A voice that fails before any audio is moved past;
// one that fails partway is reported, never replaced, because the line was already being spoken.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"rogerai.fm/roger/v6/internal/protocol"
)

// LANVoice is one local source of a voice: this machine's own voice server (URL, Key), or an Edge
// member's face (Peer), reached over mutual TLS.
type LANVoice struct {
	Band   string
	Source string // "this machine", or the member's name
	URL    string
	Key    string
	Peer   *EdgePeer
}

// LANSpeechOptions carry this node's Edge identity and the receipt check for a member's answer.
type LANSpeechOptions struct {
	EdgeCert   *tls.Certificate
	EdgeVerify func(protocol.UsageReceipt, *x509.Certificate) bool
	OnReceipt  func(protocol.UsageReceipt)
	Speed      float64
}

// LANSpeech is a spoken line: the audio, the voice that spoke it, and the local voices it moved
// past (each with why) before that one answered.
type LANSpeech struct {
	Audio []byte
	Voice LANVoice
	Left  []string
}

// ErrLANVoiceStopped is a voice that failed after its audio had started: the line is not re-spoken.
type ErrLANVoiceStopped struct{ Source string }

func (e ErrLANVoiceStopped) Error() string {
	return "the local voice on " + e.Source + " stopped before finishing; nothing was played and the line was not re-spoken by another voice"
}

// SpeakLAN speaks text through the first voice in order that answers.
func SpeakLAN(ctx context.Context, voices []LANVoice, text string, opts LANSpeechOptions) (LANSpeech, error) {
	var left []string
	for _, v := range voices {
		audio, err := speakOne(ctx, v, text, opts)
		if err == nil {
			return LANSpeech{Audio: audio, Voice: v, Left: left}, nil
		}
		if stopped, ok := err.(ErrLANVoiceStopped); ok {
			return LANSpeech{}, stopped
		}
		left = append(left, v.Source+": "+err.Error())
	}
	return LANSpeech{}, fmt.Errorf("every local voice failed (%s); LAN mode is on and the network's voices were not tried", strings.Join(left, "; "))
}

// speakOne asks one voice. A failure before any audio is an ordinary error (the caller moves on);
// a read that breaks after the voice began answering is ErrLANVoiceStopped.
func speakOne(ctx context.Context, v LANVoice, text string, opts LANSpeechOptions) ([]byte, error) {
	payload := map[string]any{"model": v.Band, "input": text, "response_format": "wav"}
	if opts.Speed > 0 {
		payload["speed"] = opts.Speed
	}
	body, _ := json.Marshal(payload)
	var (
		resp *http.Response
		leaf *x509.Certificate
		err  error
	)
	if v.Peer != nil {
		resp, leaf, err = dialEdgePath(ctx, *v.Peer, "/edge/speech", body, opts.EdgeCert)
	} else {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, v.URL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if v.Key != "" {
			req.Header.Set("Authorization", "Bearer "+v.Key) // this machine's own voice server, loopback
		}
		resp, err = noRedirects(&http.Client{Timeout: speakTimeout}).Do(req)
	}
	if err != nil {
		return nil, err // a transport failure names no secret: the key rides only in a header
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The status only: a voice server's error body can echo the key it was sent.
		return nil, fmt.Errorf("answered %d", resp.StatusCode)
	}
	audio, rerr := io.ReadAll(io.LimitReader(resp.Body, speakAudioLimit+1))
	if rerr != nil || len(audio) > speakAudioLimit {
		return nil, ErrLANVoiceStopped{Source: v.Source}
	}
	if v.Peer != nil && opts.OnReceipt != nil {
		if rec, derr := protocol.DecodeReceipt(resp.Header.Get("X-RogerAI-Receipt")); derr == nil && rec.RequestID != "" {
			if opts.EdgeVerify == nil || opts.EdgeVerify(rec, leaf) {
				opts.OnReceipt(rec)
			}
		}
	}
	return audio, nil
}
