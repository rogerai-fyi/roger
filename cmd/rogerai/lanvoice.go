package main

// lanvoice.go: the local voices `roger --lan say` and the TUI voice preview speak through
// (features/lan/voice.feature). A voice is local when this machine serves it (in this process, or
// in a separate `roger share` through its owner-only record) or an Edge member serves it over the
// LAN. A LAN Tower is never one: a standalone Tower has no audio route.

import (
	"context"
	"crypto/x509"
	"fmt"
	"sort"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/protocol"
)

// edgeSpeechOf resolves a voice band this PROCESS has on air to its local text-to-speech URL; set
// beside edgeAttachUpstream once the node controller exists, nil for a roger that shares nothing.
var edgeSpeechOf func(band string) (speechURL, key string, ok bool)

// localSpeechOf is where this machine serves the voice band: this process first, then a separate
// share's local record. It is what the self rung and this machine's Edge face both read.
func localSpeechOf(band string) (speechURL, key string, ok bool) {
	if edgeSpeechOf != nil {
		if u, k, ok := edgeSpeechOf(band); ok && u != "" {
			return u, k, true
		}
	}
	for _, rec := range onair.Shares() {
		if rec.Band == band && rec.Modality == protocol.ModalityTTS && rec.SpeechURL != "" {
			return rec.SpeechURL, rec.Key, true
		}
	}
	return "", "", false
}

// voicesOf is the subset of bands (this machine's, on air) that are voices: what its Edge instance
// record declares as Voices, so members know which bands to ask for speech.
func voicesOf(bands []string) []string {
	var out []string
	for _, b := range bands {
		if _, _, ok := localSpeechOf(b); ok {
			out = append(out, b)
		}
	}
	return out
}

// lanVoices are the local voices serving band ("" = every voice), this machine first, then Edge
// members in name order, so the choice with several is deterministic.
func lanVoices(band string) []client.LANVoice {
	var out []client.LANVoice
	seen := map[string]bool{}
	for _, b := range edgeBandsOnAir() {
		if band != "" && b != band || seen[b] {
			continue
		}
		if u, k, ok := localSpeechOf(b); ok {
			seen[b] = true
			out = append(out, client.LANVoice{Band: b, Source: "this machine", URL: u, Key: k})
		}
	}
	out = append(out, edgeVoicePeers(band)...)
	return out
}

// edgeVoicePeers are the Edge members serving a voice band over the LAN: the band must be one the
// member's instance declares as a voice AND one the serving probe answered for.
func edgeVoicePeers(band string) []client.LANVoice {
	st, err := loadEdgeState()
	if err != nil {
		return nil
	}
	nodes, err := st.fleet.List()
	if err != nil {
		return nil
	}
	self := ""
	if id, _, ok, _ := edgeIdentityStore().LoadIdentity(); ok {
		self = id.NodeID
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	var out []client.LANVoice
	for _, n := range nodes {
		if n.ID == self || !edge.HasLANTransport(n) || n.Presence == string(edge.PresenceDark) {
			continue
		}
		addr, pin := "", ""
		for _, t := range n.Transports {
			if t.Kind == "lan" {
				addr, pin = t.Addr, t.Fingerprint
			}
		}
		if addr == "" || pin == "" {
			continue
		}
		for _, in := range n.Instances {
			loc := edge.Located{Node: n, Instance: in}
			for _, v := range in.Voices {
				if (band == "" || v == band) && loc.ServesBand(v) {
					out = append(out, client.LANVoice{Band: v, Source: n.Name,
						Peer: &client.EdgePeer{Addr: addr, Pin: pin, Node: n.Name, Instance: in.Name}})
				}
			}
		}
	}
	return out
}

// sayLANReceipt is where a member's local receipt for a spoken line is recorded (the Edge session
// log, like any other local turn); a seam so a test can read it.
var sayLANReceipt = edgeUseRecorder

// speakLAN speaks text through voices (in order) with this node's Edge identity.
func speakLAN(voices []client.LANVoice, text string, speed float64) (client.LANSpeech, error) {
	return client.SpeakLAN(context.Background(), voices, text, client.LANSpeechOptions{
		EdgeCert: edgeCallerCert(),
		EdgeVerify: func(rec protocol.UsageReceipt, serving *x509.Certificate) bool {
			return edge.VerifyLocalReceipt(rec, serving)
		},
		OnReceipt: sayLANReceipt(),
		Speed:     speed,
	})
}

// lanVoiceNames are the voice bands on the LAN dial.
func lanVoiceNames() []string {
	var out []string
	for b := range lanVoiceBands(lanVoices("")) {
		out = append(out, b)
	}
	return out
}

// lanSpeakLine is the TUI voice preview in LAN mode: the line spoken by a local voice serving band,
// or the honest refusal when it has gone off air since the dial was read.
func lanSpeakLine(band, text string) ([]byte, error) {
	voices := lanVoices(band)
	if len(voices) == 0 {
		return nil, fmt.Errorf("the local text-to-speech (TTS) model %s is no longer on air", band)
	}
	res, err := speakLAN(voices, text, 0)
	return res.Audio, err
}
