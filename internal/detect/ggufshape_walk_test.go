package detect

// THE SHAPE WALK AGAINST EVERY VALUE WIDTH AND EVERY WAY A HEADER CAN END EARLY.
//
// The fitness decision rests on parseGGUFShape stepping exactly over every value it does not
// want and reading every integer width a writer is allowed to use. These build the bytes by hand
// (no mocked reader) so each width, each truncation point and each desynchronising type is a
// real header the parser has to survive.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type rawKV struct {
	key     string
	vtype   uint32
	payload []byte
}

func le(v any) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, v)
	return b.Bytes()
}

func ggufStr(s string) []byte { return append(le(uint64(len(s))), s...) }

func rawKVBytes(kv rawKV) []byte {
	b := ggufStr(kv.key)
	b = append(b, le(kv.vtype)...)
	return append(b, kv.payload...)
}

// rawHeader is a GGUF prefix whose kv count claims len(kvs), followed by the kvs and then tail
// (which may be a partial kv).
func rawHeader(version uint32, kvs []rawKV, extraCount uint64, tail []byte) []byte {
	var b bytes.Buffer
	b.Write(ggufMagic[:])
	b.Write(le(version))
	b.Write(le(uint64(0)))
	b.Write(le(uint64(len(kvs)) + extraCount))
	for _, kv := range kvs {
		b.Write(rawKVBytes(kv))
	}
	b.Write(tail)
	return b.Bytes()
}

// mixedWidthKVs is a complete llama shape where every required integer uses a different width,
// interleaved with values the walk must skip.
func mixedWidthKVs() []rawKV {
	return []rawKV{
		{"general.architecture", ggufTypeString, ggufStr("llama")},
		{"llama.rope.freq_base", ggufTypeFloat32, le(float32(500000))},
		{"llama.block_count", ggufTypeInt64, le(int64(32))},
		{"llama.use_parallel_residual", ggufTypeBool, []byte{1}},
		{"llama.attention.head_count_kv", ggufTypeUint16, le(uint16(8))},
		{"llama.attention.key_length", ggufTypeInt32, le(int32(128))},
		{"llama.attention.value_length", ggufTypeInt16, le(int16(128))},
		{"llama.context_length", ggufTypeUint64, le(uint64(131072))},
		{"llama.rope.scaling.factor", ggufTypeFloat64, le(float64(8))},
		{"llama.expert_count", ggufTypeUint32, le(uint32(0))},
		// An array of uint32 is counted, not read; its length is what is kept.
		{"tokenizer.ggml.token_type", ggufTypeArray, append(append(le(ggufTypeUint32), le(uint64(3))...), le([3]uint32{1, 1, 3})...)},
		// A nested array (array of arrays of strings) must be stepped over exactly.
		{"tokenizer.ggml.merges_nested", ggufTypeArray, append(append(le(ggufTypeArray), le(uint64(1))...),
			append(append(le(ggufTypeString), le(uint64(2))...), append(ggufStr("a b"), ggufStr("c d")...)...)...)},
		{"tokenizer.ggml.tokens", ggufTypeArray, append(append(le(ggufTypeString), le(uint64(4))...),
			append(append(ggufStr("<s>"), ggufStr("</s>")...), append(ggufStr("a"), ggufStr("b")...)...)...)},
	}
}

func TestEveryIntegerWidthIsReadAndEverySkippedTypeIsSteppedOver(t *testing.T) {
	s, err := parseGGUFShape(bytes.NewReader(rawHeader(3, mixedWidthKVs(), 0, nil)))
	require.NoError(t, err)
	require.Equal(t, 32, s.Layers, "int64 block_count")
	require.Equal(t, 8, s.KVHeads, "uint16 head_count_kv")
	require.Equal(t, 128, s.KeyDim, "int32 key_length")
	require.Equal(t, 128, s.ValueDim, "int16 value_length")
	require.Equal(t, 131072, s.TrainCtx, "uint64 context_length")
	require.Equal(t, 4, s.Vocab, "vocab falls back to the token array's length")
	require.Equal(t, 0, s.Experts)
}

func TestShapeFromHeaderUsesTheIndexSizeNotTheHeader(t *testing.T) {
	hdr := rawHeader(2, mixedWidthKVs(), 0, nil)
	s, err := ShapeFromHeader(hdr, 4_920_000_000)
	require.NoError(t, err)
	require.Equal(t, int64(4_920_000_000), s.FileBytes)
	require.Equal(t, 32, s.Layers)

	for _, n := range []int64{0, -1} {
		_, err := ShapeFromHeader(hdr, n)
		require.ErrorIs(t, err, ErrNotReadable, "a file of size %d cannot be estimated", n)
	}
	_, err = ShapeFromHeader([]byte("<html>404 Not Found</html>"), 1<<30)
	require.ErrorIs(t, err, ErrNotReadable, "an error page served in place of a header is not a GGUF")

	// An embedding model refused remotely is refused for the same reason as locally.
	emb := rawHeader(3, []rawKV{
		{"general.architecture", ggufTypeString, ggufStr("nomic-bert")},
		{"nomic-bert.pooling_type", ggufTypeUint32, le(uint32(1))},
		{"nomic-bert.block_count", ggufTypeUint32, le(uint32(12))},
	}, 0, nil)
	_, err = ShapeFromHeader(emb, 1<<28)
	require.ErrorIs(t, err, ErrNotGenerative)
}

func TestAMalformedPreambleIsNotReadable(t *testing.T) {
	good := rawHeader(3, mixedWidthKVs(), 0, nil)
	huge := append([]byte(nil), good[:16]...)
	huge = append(huge, le(uint64(1<<20+1))...)
	cases := map[string][]byte{
		"empty":                  nil,
		"short magic":            []byte("GGU"),
		"wrong magic":            append([]byte("GGML"), good[4:]...),
		"no version":             good[:4],
		"version 1":              rawHeader(1, mixedWidthKVs(), 0, nil),
		"version 4":              rawHeader(4, mixedWidthKVs(), 0, nil),
		"truncated tensor count": good[:12],
		"truncated kv count":     good[:20],
		"implausible kv count":   huge,
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseGGUFShape(bytes.NewReader(b))
			require.ErrorIs(t, err, ErrNotReadable)
		})
	}
}

// A TRUNCATED TAIL NEVER COSTS WHAT CAME BEFORE IT. A header cut off by the byte budget or a short
// ranged read ends mid-value; every field already read must still resolve. This cuts one extra
// trailing kv of every value kind at every byte offset.
func TestATruncatedTrailingValueKeepsEverythingReadBeforeIt(t *testing.T) {
	trailers := []rawKV{
		{"general.name", ggufTypeString, ggufStr("Llama 3.1 8B")},
		{"llama.x32", ggufTypeUint32, le(uint32(7))},
		{"llama.x64", ggufTypeUint64, le(uint64(7))},
		{"llama.x16", ggufTypeUint16, le(uint16(7))},
		{"llama.xf", ggufTypeFloat32, le(float32(1))},
		{"tokenizer.ggml.scores", ggufTypeArray, append(append(le(ggufTypeFloat32), le(uint64(2))...), le([2]float32{0, 1})...)},
	}
	for _, tr := range trailers {
		full := rawKVBytes(tr)
		for cut := 0; cut < len(full); cut++ {
			hdr := rawHeader(3, mixedWidthKVs(), 1, full[:cut])
			s, err := parseGGUFShape(bytes.NewReader(hdr))
			require.NoError(t, err, "%s cut at byte %d", tr.key, cut)
			require.Equal(t, 32, s.Layers, "%s cut at byte %d", tr.key, cut)
			require.Equal(t, 4, s.Vocab, "%s cut at byte %d", tr.key, cut)
		}
	}
}

// A required field that is itself cut short is missing, not half-read: no shape.
func TestARequiredFieldCutShortYieldsNoShape(t *testing.T) {
	kvs := mixedWidthKVs()
	last := rawKV{"llama.vocab_size", ggufTypeUint32, le(uint32(128256))}
	kvs = kvs[:len(kvs)-1] // drop the token array so vocab_size is the only source
	full := rawKVBytes(last)
	_, err := parseGGUFShape(bytes.NewReader(rawHeader(3, kvs, 1, full[:len(full)-2])))
	require.ErrorIs(t, err, ErrIncomplete)

	s, err := parseGGUFShape(bytes.NewReader(rawHeader(3, append(kvs, last), 0, nil)))
	require.NoError(t, err)
	require.Equal(t, 128256, s.Vocab, "the same field complete is read")
}

// A TYPE THE WALK DOES NOT UNDERSTAND DESYNCHRONISES EVERYTHING AFTER IT. The walk stops there
// rather than reading the following bytes as keys, so fields beyond it are absent and the answer
// is "incomplete", never a shape assembled from misaligned bytes.
func TestAnUnknownTypeStopsTheWalkRatherThanReadingNonsense(t *testing.T) {
	head := mixedWidthKVs()[:2] // architecture and one skipped float
	rest := mixedWidthKVs()[2:]
	for name, bad := range map[string]rawKV{
		"unknown top-level type":     {"llama.mystery", 99, le(uint32(32))},
		"array of an unknown type":   {"llama.mystery_arr", ggufTypeArray, append(append(le(uint32(99)), le(uint64(2))...), le(uint64(0))...)},
		"nested unknown in an array": {"llama.nested", ggufTypeArray, append(append(le(ggufTypeArray), le(uint64(1))...), append(le(uint32(77)), le(uint64(1))...)...)},
	} {
		t.Run(name, func(t *testing.T) {
			kvs := append(append(append([]rawKV(nil), head...), bad), rest...)
			_, err := parseGGUFShape(bytes.NewReader(rawHeader(3, kvs, 0, nil)))
			require.ErrorIs(t, err, ErrIncomplete)
		})
	}
	// Control: without the bad key the same fields resolve.
	_, err := parseGGUFShape(bytes.NewReader(rawHeader(3, append(append([]rawKV(nil), head...), rest...), 0, nil)))
	require.NoError(t, err)
}

// THE DISPLAY WALK tolerates a general.* key written with an unexpected type: it steps over it and
// still reads the keys after it, rather than misreading the value as the type it expected.
func TestTheDisplayWalkStepsOverAGeneralKeyOfTheWrongType(t *testing.T) {
	hdr := rawHeader(3, []rawKV{
		{"general.basename", ggufTypeUint32, le(uint32(5))},
		{"general.file_type", ggufTypeString, ggufStr("Q4_K_M")},
		{"general.size_label", ggufTypeString, ggufStr("8B")},
		{"general.file_type", ggufTypeUint32, le(uint32(15))},
	}, 0, nil)
	m, err := parseGGUFHeader(bytes.NewReader(hdr))
	require.NoError(t, err)
	require.Equal(t, "", m.Basename, "a numeric basename is not a name")
	require.Equal(t, "8B", m.SizeLabel)
	require.True(t, m.FileTypeSet)
	require.Equal(t, uint32(15), m.FileType)

	// A wrongly-typed key whose value is itself truncated surfaces the read error.
	for _, bad := range []rawKV{
		{"general.basename", ggufTypeUint64, le(uint64(5))},
		{"general.file_type", ggufTypeUint64, le(uint64(5))},
		{"general.file_type", ggufTypeUint32, le(uint32(5))},
		{"general.basename", ggufTypeString, ggufStr("Llama")},
	} {
		full := rawKVBytes(bad)
		_, err := parseGGUFHeader(bytes.NewReader(rawHeader(3, nil, 1, full[:len(full)-1])))
		require.Error(t, err, "%s/%d truncated", bad.key, bad.vtype)
	}
}

func TestReadShapeRefusesWhatIsNotAReadableFile(t *testing.T) {
	_, err := ReadShape("/nonexistent/model.gguf")
	require.ErrorIs(t, err, ErrNotReadable)
	_, err = ReadShape(t.TempDir()) // a directory opens but is not a model
	require.ErrorIs(t, err, ErrNotReadable)
	empty := filepath.Join(t.TempDir(), "empty.gguf")
	require.NoError(t, os.WriteFile(empty, nil, 0o644))
	_, err = ReadShape(empty)
	require.ErrorIs(t, err, ErrNotReadable, "a zero-byte file has no header")
}
