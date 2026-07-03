package detect

import (
	"reflect"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

func TestVisionFromID(t *testing.T) {
	vision := []string{"qwen2.5-vl-7b", "llava-1.6", "pixtral-12b", "gpt-4o", "llama-3.2-11b-vision", "internvl2", "minicpm-v", "gemma-3-27b", "moondream2", "some-vlm"}
	text := []string{"gpt-oss-20b", "qwen3-coder-next", "llama-3.1-8b", "mistral-7b", "gemma-2-9b", "phi-3-mini", "deepseek-r1"}
	for _, id := range vision {
		if !visionFromID(id) {
			t.Errorf("visionFromID(%q) = false, want true", id)
		}
	}
	for _, id := range text {
		if visionFromID(id) {
			t.Errorf("visionFromID(%q) = true, want false", id)
		}
	}
}

// classifyCapabilities: chat models get ["vision"] (id match) or [] (text-only); voice/stt
// models get nothing. The metadata probe hits an unreachable base here, so the id heuristic
// decides - which is the app-parity fallback.
func TestClassifyCapabilities(t *testing.T) {
	f := &Found{
		Models: []string{"qwen2.5-vl-7b", "gpt-oss-20b", "voice-model", "whisper-1"},
		Modality: map[string]string{
			"qwen2.5-vl-7b": protocol.ModalityChat,
			"gpt-oss-20b":   protocol.ModalityChat,
			"voice-model":   protocol.ModalityTTS,
			"whisper-1":     protocol.ModalitySTT,
		},
	}
	classifyCapabilities(f, "http://127.0.0.1:0") // unreachable -> meta empty, id heuristic only

	if got := f.Capabilities["qwen2.5-vl-7b"]; !reflect.DeepEqual(got, []string{"vision"}) {
		t.Errorf("vision chat model caps = %v, want [vision]", got)
	}
	if got := f.Capabilities["gpt-oss-20b"]; !reflect.DeepEqual(got, []string{}) {
		t.Errorf("text-only chat model caps = %v, want [] (known text-only)", got)
	}
	if _, ok := f.Capabilities["voice-model"]; ok {
		t.Errorf("a tts model must have no chat capabilities, got %v", f.Capabilities["voice-model"])
	}
	if _, ok := f.Capabilities["whisper-1"]; ok {
		t.Errorf("an stt model must have no chat capabilities, got %v", f.Capabilities["whisper-1"])
	}
}
