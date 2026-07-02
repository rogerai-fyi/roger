package main

// Pins the isChatShare persistence guard: `share.upstream` is the single headline (chat)
// upstream, so ONLY a chat share may persist its verified upstream/key. A voice/stt share
// (--modality tts|stt, or an auto-detected bare voice server) must NEVER clobber it - the
// founder-hit 2026-07-02 incident: a headless `roger share --model voice --upstream :8790`
// overwrote the chat :8060 and broke the chat share on its next launch.

import (
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

func TestShouldPersistShareUpstream(t *testing.T) {
	const chatUp = "http://127.0.0.1:8060/v1"
	const voiceUp = "http://127.0.0.1:8790/v1"
	cases := []struct {
		name          string
		foundModality string
		baseURL       string
		savedUp       string
		upKey         string
		saved         *Share
		want          bool
	}{
		// The incident: a voice share picking a NEW upstream must not touch the saved chat one.
		{"tts share never persists a new upstream", protocol.ModalityTTS, voiceUp, chatUp, "", &Share{Upstream: chatUp}, false},
		{"stt share never persists a new upstream", protocol.ModalitySTT, voiceUp, chatUp, "", &Share{Upstream: chatUp}, false},
		{"tts share never persists a key either", protocol.ModalityTTS, voiceUp, chatUp, "sk-voice", &Share{Upstream: chatUp}, false},
		{"tts share with no prior config still skips", protocol.ModalityTTS, voiceUp, "", "", nil, false},

		// Chat (and undetected-modality, which defaults to chat) keeps today's behavior.
		{"chat share persists a new upstream", protocol.ModalityChat, chatUp, "", "", nil, true},
		{"undetected modality persists (chat default)", "", chatUp, "", "", nil, true},
		{"chat share with unchanged upstream and no key skips", protocol.ModalityChat, chatUp, chatUp, "", &Share{Upstream: chatUp}, false},
		{"chat share persists a newly-needed key", protocol.ModalityChat, chatUp, chatUp, "sk-new", &Share{Upstream: chatUp}, true},
		{"chat share with the already-saved key skips", protocol.ModalityChat, chatUp, chatUp, "sk-same", &Share{Upstream: chatUp, UpstreamKey: "sk-same"}, false},
		{"chat share persists a key even with nil saved config", protocol.ModalityChat, "", "", "sk-new", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldPersistShareUpstream(tc.foundModality, tc.baseURL, tc.savedUp, tc.upKey, tc.saved)
			if got != tc.want {
				t.Fatalf("shouldPersistShareUpstream(%q, %q, %q, %q) = %v, want %v",
					tc.foundModality, tc.baseURL, tc.savedUp, tc.upKey, got, tc.want)
			}
		})
	}
}
