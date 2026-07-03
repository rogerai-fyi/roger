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

// CapabilitiesForModel is the explicit-`--upstream` path's single-model classifier: it must
// return a NON-NIL [] for a text model (known text-only, survives the wire) and ["vision"] for a
// vision id, from the id heuristic alone when the metadata probe is unreachable.
func TestCapabilitiesForModel(t *testing.T) {
	if got := CapabilitiesForModel("http://127.0.0.1:0", "qwen2.5-vl-7b", ""); len(got) != 1 || got[0] != "vision" {
		t.Errorf("vision id => %v, want [vision]", got)
	}
	got := CapabilitiesForModel("http://127.0.0.1:0", "gpt-oss-20b", "")
	if got == nil || len(got) != 0 {
		t.Errorf("text-only id => %#v, want non-nil [] (known text-only, not undetermined)", got)
	}
}
