package detect

import "testing"

// The header's own word on what a file IS is carried out of ReadShape even when the shape is
// incomplete (an adapter has no block count), because whether a file is a model at all is decided
// before any arithmetic (edge_device_runtime.feature §2).
func TestTheHeaderSaysWhatKindOfFileItIs(t *testing.T) {
	adapter := writeGGUF(t, []kv{
		{"general.architecture", ggufTypeString, "llama"},
		{"general.type", ggufTypeString, "adapter"},
		{"adapter.type", ggufTypeString, "lora"},
		{"general.base_model.0.name", ggufTypeString, "Nano Sft Instruct V3D"},
	}, 4096)
	s, err := ReadShape(adapter)
	if err == nil {
		t.Fatal("an adapter header has no shape to resolve; want an error beside the kind")
	}
	if s.Kind != "adapter" || s.BaseModel != "Nano Sft Instruct V3D" {
		t.Errorf("kind %q base %q, want adapter / Nano Sft Instruct V3D", s.Kind, s.BaseModel)
	}
	if s.FileBytes != 4096 {
		t.Errorf("file bytes %d, want 4096 beside the error", s.FileBytes)
	}
	model := writeGGUF(t, append(shapeKVs(), kv{"general.type", ggufTypeString, "model"}), 1<<20)
	s, err = ReadShape(model)
	if err != nil || s.Kind != "model" || s.Layers != 36 {
		t.Errorf("a model header: kind %q layers %d err %v", s.Kind, s.Layers, err)
	}
	s, err = ReadShape(writeGGUF(t, shapeKVs(), 1<<20))
	if err != nil || s.Kind != "" {
		t.Errorf("a header that does not say: kind %q err %v, want empty and no error", s.Kind, err)
	}
}
