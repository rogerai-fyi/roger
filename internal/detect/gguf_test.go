package detect

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These build REAL GGUF bytes rather than mocking the parser. The whole risk in this file
// is mis-stepping a value width - which does not lose one field, it desynchronises every
// key after it - and only real bytes can catch that.

type kv struct {
	key   string
	vtype uint32
	val   any
}

func ggufBytes(t *testing.T, version uint32, kvs []kv) []byte {
	t.Helper()
	var b bytes.Buffer
	b.Write(ggufMagic[:])
	_ = binary.Write(&b, binary.LittleEndian, version)
	_ = binary.Write(&b, binary.LittleEndian, uint64(0)) // tensor count
	_ = binary.Write(&b, binary.LittleEndian, uint64(len(kvs)))
	for _, e := range kvs {
		writeGGUFString(&b, e.key)
		_ = binary.Write(&b, binary.LittleEndian, e.vtype)
		writeGGUFValue(t, &b, e.vtype, e.val)
	}
	return b.Bytes()
}

func writeGGUFString(b *bytes.Buffer, s string) {
	_ = binary.Write(b, binary.LittleEndian, uint64(len(s)))
	b.WriteString(s)
}

func writeGGUFValue(t *testing.T, b *bytes.Buffer, vtype uint32, v any) {
	t.Helper()
	switch vtype {
	case ggufTypeString:
		writeGGUFString(b, v.(string))
	case ggufTypeUint32:
		_ = binary.Write(b, binary.LittleEndian, v.(uint32))
	case ggufTypeUint8:
		_ = binary.Write(b, binary.LittleEndian, v.(uint8))
	case ggufTypeBool:
		var one uint8
		if v.(bool) {
			one = 1
		}
		_ = binary.Write(b, binary.LittleEndian, one)
	case ggufTypeFloat32:
		_ = binary.Write(b, binary.LittleEndian, v.(float32))
	case ggufTypeUint64:
		_ = binary.Write(b, binary.LittleEndian, v.(uint64))
	case ggufTypeArray:
		arr := v.([]string)
		_ = binary.Write(b, binary.LittleEndian, ggufTypeString)
		_ = binary.Write(b, binary.LittleEndian, uint64(len(arr)))
		for _, s := range arr {
			writeGGUFString(b, s)
		}
	default:
		t.Fatalf("test writer cannot emit type %d", vtype)
	}
}

// THE HEADLINE: the two fields the founder asked about come out of a real header.
func TestGGUFHeaderYieldsTheWeightsAndVariantAxes(t *testing.T) {
	raw := ggufBytes(t, 3, []kv{
		{"general.architecture", ggufTypeString, "qwen3"},
		{"general.basename", ggufTypeString, "Qwen3.8"},
		{"general.size_label", ggufTypeString, "27B"},
		{"general.finetune", ggufTypeString, "thinking"},
		{"general.organization", ggufTypeString, "Qwen"},
		{"general.quantized_by", ggufTypeString, "unsloth"},
		{"general.file_type", ggufTypeUint32, uint32(15)},
	})
	m, err := parseGGUFHeader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.QuantizedBy != "unsloth" {
		t.Errorf("quantized_by = %q, want unsloth - this IS the weights axis", m.QuantizedBy)
	}
	if m.Finetune != "thinking" {
		t.Errorf("finetune = %q, want thinking - this IS the variant axis", m.Finetune)
	}
	if m.Organization != "Qwen" || m.Basename != "Qwen3.8" || m.SizeLabel != "27B" {
		t.Errorf("the rest of general.* did not survive: %+v", m)
	}
	if !m.FileTypeSet || m.FileType != 15 {
		t.Errorf("file_type = %d (set=%v), want 15", m.FileType, m.FileTypeSet)
	}
}

// STEPPING OVER OTHER TYPES MUST BE EXACT. A wrong width here does not lose one value - it
// desynchronises the whole rest of the header, and every field after it becomes confident
// nonsense. So: every type the writer can emit, sitting BEFORE the keys we want.
func TestGGUFSkipsEveryTypeWithoutDesynchronising(t *testing.T) {
	raw := ggufBytes(t, 3, []kv{
		{"a.u8", ggufTypeUint8, uint8(7)},
		{"a.bool", ggufTypeBool, true},
		{"a.u32", ggufTypeUint32, uint32(4242)},
		{"a.f32", ggufTypeFloat32, float32(1.5)},
		{"a.u64", ggufTypeUint64, uint64(1 << 40)},
		{"a.str", ggufTypeString, "some other value"},
		{"tokenizer.ggml.tokens", ggufTypeArray, []string{"a", "bb", "ccc", "dddd"}},
		{"general.quantized_by", ggufTypeString, "bartowski"},
	})
	m, err := parseGGUFHeader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.QuantizedBy != "bartowski" {
		t.Errorf("a key AFTER every other type read as %q - the walk desynchronised", m.QuantizedBy)
	}
}

// A file that is not a GGUF is not guessed at.
func TestGGUFRefusesWhatItCannotRead(t *testing.T) {
	for name, raw := range map[string][]byte{
		"empty":        {},
		"not gguf":     []byte("this is a safetensors file, actually"),
		"truncated":    ggufBytes(t, 3, []kv{{"general.quantized_by", ggufTypeString, "unsloth"}})[:12],
		"bad version":  ggufBytes(t, 1, nil),
		"future ver":   ggufBytes(t, 99, nil),
		"unknown type": append(ggufBytes(t, 3, nil)[:24], 0xff),
	} {
		if m, err := parseGGUFHeader(bytes.NewReader(raw)); err == nil && !m.empty() {
			t.Errorf("%s: returned metadata from something unreadable: %+v", name, m)
		}
	}
}

// A CORRUPT LENGTH must not become an arbitrary allocation - the classic way a malformed
// header turns a parser into an out-of-memory kill.
func TestGGUFRefusesAnAbsurdStringLength(t *testing.T) {
	var b bytes.Buffer
	b.Write(ggufMagic[:])
	_ = binary.Write(&b, binary.LittleEndian, uint32(3))
	_ = binary.Write(&b, binary.LittleEndian, uint64(0))
	_ = binary.Write(&b, binary.LittleEndian, uint64(1))
	_ = binary.Write(&b, binary.LittleEndian, uint64(1<<62)) // a key claiming 4 exabytes
	if _, err := parseGGUFHeader(bytes.NewReader(b.Bytes())); err == nil {
		t.Error("an absurd string length was accepted")
	}
}

// A corrupt KV COUNT must not spin the loop.
func TestGGUFRefusesAnAbsurdKVCount(t *testing.T) {
	var b bytes.Buffer
	b.Write(ggufMagic[:])
	_ = binary.Write(&b, binary.LittleEndian, uint32(3))
	_ = binary.Write(&b, binary.LittleEndian, uint64(0))
	_ = binary.Write(&b, binary.LittleEndian, uint64(1<<40))
	if _, err := parseGGUFHeader(bytes.NewReader(b.Bytes())); err == nil {
		t.Error("an implausible kv count was accepted")
	}
}

// THE BUDGET IS THE CONTRACT. A vocabulary array bigger than the budget ends the walk, and
// whatever was read BEFORE it survives. This is what keeps a port scan from streaming a
// multi-gigabyte file to read a display label.
func TestGGUFKeepsWhatItReadBeforeRunningOutOfBudget(t *testing.T) {
	big := make([]string, 40000)
	for i := range big {
		big[i] = "token-that-is-not-especially-short"
	}
	raw := ggufBytes(t, 3, []kv{
		{"general.quantized_by", ggufTypeString, "unsloth"}, // BEFORE the vocabulary
		{"tokenizer.ggml.tokens", ggufTypeArray, big},
		{"general.finetune", ggufTypeString, "never-reached"}, // AFTER it
	})
	if len(raw) <= ggufHeaderBudget {
		t.Fatalf("this lock needs a header over the %d budget, built %d", ggufHeaderBudget, len(raw))
	}
	m, _ := parseGGUFHeader(io.LimitReader(bytes.NewReader(raw), ggufHeaderBudget))
	if m.QuantizedBy != "unsloth" {
		t.Errorf("the key before the vocabulary was lost: %+v", m)
	}
	if m.Finetune != "" {
		t.Errorf("a key past the budget was somehow read: %q", m.Finetune)
	}
}

// readGGUFMeta must be silent about ordinary non-files: a missing path, a directory, a
// server pointed at safetensors. None of those is an incident.
func TestReadGGUFMetaIsSilentOnOrdinaryMisses(t *testing.T) {
	dir := t.TempDir()
	notGGUF := filepath.Join(dir, "model.safetensors")
	if err := os.WriteFile(notGGUF, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", filepath.Join(dir, "missing.gguf"), dir, notGGUF} {
		if m := readGGUFMeta(path); !m.empty() {
			t.Errorf("readGGUFMeta(%q) invented metadata: %+v", path, m)
		}
	}
	// And a real file on disk round-trips.
	good := filepath.Join(dir, "Qwen3.8-27B-Q4_K_M.gguf")
	raw := ggufBytes(t, 3, []kv{{"general.quantized_by", ggufTypeString, "unsloth"}})
	if err := os.WriteFile(good, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if m := readGGUFMeta(good); m.QuantizedBy != "unsloth" {
		t.Errorf("a real gguf on disk read as %+v", m)
	}
	_ = strings.TrimSpace("")
}

// A TRUNCATION AT EVERY OFFSET must be an error, never a partial value presented as whole.
// This walks the cut point across a complete header: each prefix either parses cleanly or
// fails, and none of them may invent a field that was not fully read.
func TestGGUFTruncationAtAnyOffsetNeverInventsAField(t *testing.T) {
	raw := ggufBytes(t, 3, []kv{
		{"general.organization", ggufTypeString, "Qwen"},
		{"general.quantized_by", ggufTypeString, "unsloth"},
		{"general.file_type", ggufTypeUint32, uint32(15)},
	})
	for cut := 0; cut < len(raw); cut++ {
		m, err := parseGGUFHeader(bytes.NewReader(raw[:cut]))
		if err == nil {
			continue // a clean prefix boundary
		}
		// On error, whatever came back must be a PREFIX of the truth - never a value the
		// bytes did not contain.
		if m.QuantizedBy != "" && m.QuantizedBy != "unsloth" {
			t.Errorf("cut=%d invented quantized_by %q", cut, m.QuantizedBy)
		}
		if m.Organization != "" && m.Organization != "Qwen" {
			t.Errorf("cut=%d invented organization %q", cut, m.Organization)
		}
		if m.FileTypeSet && m.FileType != 15 {
			t.Errorf("cut=%d invented file_type %d", cut, m.FileType)
		}
	}
}

// A general.* key carrying the WRONG TYPE is stepped over, not coerced. A file_type
// written as a string is a malformed file, and reading it as a label would be the
// "confident nonsense" this parser is built to avoid.
func TestGGUFStepsOverAGeneralKeyOfTheWrongType(t *testing.T) {
	raw := ggufBytes(t, 3, []kv{
		{"general.file_type", ggufTypeString, "Q4_K_M"},     // wrong type for this key
		{"general.quantized_by", ggufTypeUint32, uint32(7)}, // wrong type for this key
		{"general.finetune", ggufTypeString, "thinking"},    // right type, AFTER them
	})
	m, err := parseGGUFHeader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.FileTypeSet {
		t.Errorf("a string file_type was coerced to %d", m.FileType)
	}
	if m.QuantizedBy != "" {
		t.Errorf("a numeric quantized_by became %q", m.QuantizedBy)
	}
	// And the walk stayed in step: the correctly-typed key after them still reads.
	if m.Finetune != "thinking" {
		t.Errorf("finetune = %q - stepping over a mistyped key desynchronised the walk", m.Finetune)
	}
}

// An array of an UNKNOWN element type cannot be stepped over - its width is unknowable -
// so the walk stops rather than guessing a stride and reading garbage as fields.
func TestGGUFStopsAtAnArrayItCannotStepOver(t *testing.T) {
	var b bytes.Buffer
	b.Write(ggufMagic[:])
	_ = binary.Write(&b, binary.LittleEndian, uint32(3))
	_ = binary.Write(&b, binary.LittleEndian, uint64(0))
	_ = binary.Write(&b, binary.LittleEndian, uint64(2))
	writeGGUFString(&b, "weird.array")
	_ = binary.Write(&b, binary.LittleEndian, ggufTypeArray)
	_ = binary.Write(&b, binary.LittleEndian, uint32(0xdead)) // unknown element type
	_ = binary.Write(&b, binary.LittleEndian, uint64(3))
	writeGGUFString(&b, "general.quantized_by")
	_ = binary.Write(&b, binary.LittleEndian, ggufTypeString)
	writeGGUFString(&b, "unsloth")

	m, err := parseGGUFHeader(bytes.NewReader(b.Bytes()))
	if err == nil {
		t.Fatal("an unknown array element type was accepted")
	}
	if m.QuantizedBy != "" {
		t.Errorf("a field past an unsteppable array was read as %q", m.QuantizedBy)
	}
}

// Nested arrays are stepped over element by element, so a key after one still reads.
func TestGGUFStepsOverAnArrayOfArrays(t *testing.T) {
	var b bytes.Buffer
	b.Write(ggufMagic[:])
	_ = binary.Write(&b, binary.LittleEndian, uint32(3))
	_ = binary.Write(&b, binary.LittleEndian, uint64(0))
	_ = binary.Write(&b, binary.LittleEndian, uint64(2))
	writeGGUFString(&b, "nested")
	_ = binary.Write(&b, binary.LittleEndian, ggufTypeArray)
	_ = binary.Write(&b, binary.LittleEndian, ggufTypeArray) // elements are arrays
	_ = binary.Write(&b, binary.LittleEndian, uint64(2))     // two of them
	for i := 0; i < 2; i++ {
		_ = binary.Write(&b, binary.LittleEndian, ggufTypeUint32)
		_ = binary.Write(&b, binary.LittleEndian, uint64(3))
		for j := 0; j < 3; j++ {
			_ = binary.Write(&b, binary.LittleEndian, uint32(j))
		}
	}
	writeGGUFString(&b, "general.finetune")
	_ = binary.Write(&b, binary.LittleEndian, ggufTypeString)
	writeGGUFString(&b, "instruct")

	m, err := parseGGUFHeader(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Finetune != "instruct" {
		t.Errorf("finetune = %q - a nested array desynchronised the walk", m.Finetune)
	}
}
