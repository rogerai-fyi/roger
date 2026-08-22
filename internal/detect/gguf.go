package detect

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
)

// READING A GGUF HEADER, for the model-variant fields (MODEL-VARIANTS-DESIGN-2026-08-22).
//
// Two operators sharing "qwen3.8-27b" can be running very different weights, and the
// distinction people actually make is the exact quant label plus WHO quantized it. The
// GGUF spec carries both, as standard keys:
//
//	general.quantized_by  "The name of the individual who quantized the model"
//	general.finetune      "What has the base model been optimized toward"
//	general.organization / general.basename / general.size_label / general.file_type
//
// Ollama hands these to us for free in /api/show's model_info. llama.cpp does not - its
// HTTP API exposes the model PATH and nothing else - so for that runtime we open the file
// and read the header ourselves. A local station is on the same machine by definition, so
// this is a local file read, never a network fetch.
//
// EVERYTHING HERE IS BEST-EFFORT AND BOUNDED. It runs during a port scan that already
// takes seconds, on a file that can be tens of gigabytes, and its result is a display
// label. It must never be the reason a scan is slow, and it must never be the reason a
// scan fails: every error path returns "nothing found" and the caller carries on.

// ggufMagic is the 4-byte file magic. A file without it is not a GGUF and is not guessed at.
var ggufMagic = [4]byte{'G', 'G', 'U', 'F'}

// ggufHeaderBudget bounds how far into the file we read.
//
// The header is a flat list of key/value pairs, and one of them is the tokenizer vocabulary
// - an array of ~150k strings that can run to many megabytes. Reaching a key BEYOND it
// would mean streaming all of that just to read a label.
//
// In practice llama.cpp's writer emits the general.* block FIRST, so the keys we want are
// in the first few KB. The budget makes that the contract rather than the hope: read a
// generous prefix, take what is there, and stop. A file that buries general.* behind its
// vocabulary simply reports nothing, which is the correct outcome for a display field.
const ggufHeaderBudget = 1 << 20 // 1 MiB

// ggufMeta is the subset of general.* this product has a use for. Empty strings mean the
// key was absent - which is the common case and is never presented as a value.
type ggufMeta struct {
	QuantizedBy  string // general.quantized_by  -> the "weights" axis (unsloth, bartowski)
	Finetune     string // general.finetune      -> the "variant" axis (instruct, thinking)
	Organization string // general.organization  -> the model's org (Qwen, Meta)
	Basename     string // general.basename      -> the base model name
	SizeLabel    string // general.size_label    -> "27B"
	FileType     uint32 // general.file_type     -> the quant enum; 0 also means "absent"
	FileTypeSet  bool   // file_type was present (0 is a REAL value: all-F32)
}

// empty reports whether nothing usable was read - so a caller can tell "no metadata" from
// "metadata that happens to be blank".
func (m ggufMeta) empty() bool {
	return m.QuantizedBy == "" && m.Finetune == "" && m.Organization == "" &&
		m.Basename == "" && m.SizeLabel == "" && !m.FileTypeSet
}

// readGGUFMeta opens path and reads the general.* keys out of its header.
//
// It returns a zero ggufMeta and no error for anything that is merely "not a readable
// GGUF" - a missing file, a directory, a truncated download, a safetensors file someone
// pointed the server at. Those are ordinary, and none of them is an incident worth
// surfacing to an operator who only asked to share a model.
func readGGUFMeta(path string) ggufMeta {
	if strings.TrimSpace(path) == "" {
		return ggufMeta{}
	}
	f, err := os.Open(path)
	if err != nil {
		return ggufMeta{}
	}
	defer f.Close()
	// A directory opens cleanly and then fails to read; check rather than rely on that.
	if st, serr := f.Stat(); serr != nil || st.IsDir() {
		return ggufMeta{}
	}
	meta, _ := parseGGUFHeader(io.LimitReader(f, ggufHeaderBudget))
	return meta
}

// parseGGUFHeader reads the KV block. The error is returned for tests; production callers
// take the meta and ignore it, because a partial read is still worth what it found.
func parseGGUFHeader(r io.Reader) (ggufMeta, error) {
	var out ggufMeta
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return out, err
	}
	if magic != ggufMagic {
		return out, errors.New("not a gguf file")
	}
	var version uint32
	if err := binary.Read(r, binary.LittleEndian, &version); err != nil {
		return out, err
	}
	// v1 laid out counts differently and is long dead. Refusing is better than
	// mis-parsing a file into confident nonsense.
	if version < 2 || version > 3 {
		return out, errors.New("unsupported gguf version")
	}
	var tensorCount, kvCount uint64
	if err := binary.Read(r, binary.LittleEndian, &tensorCount); err != nil {
		return out, err
	}
	if err := binary.Read(r, binary.LittleEndian, &kvCount); err != nil {
		return out, err
	}
	// A corrupt count could otherwise spin this loop billions of times inside the byte
	// budget's error path. No real model carries anywhere near this many keys.
	if kvCount > 1<<20 {
		return out, errors.New("implausible gguf kv count")
	}
	for i := uint64(0); i < kvCount; i++ {
		key, err := ggufString(r)
		if err != nil {
			return out, err // out of budget or truncated: keep whatever was read
		}
		var vtype uint32
		if err := binary.Read(r, binary.LittleEndian, &vtype); err != nil {
			return out, err
		}
		if !strings.HasPrefix(key, "general.") {
			if err := skipGGUFValue(r, vtype); err != nil {
				return out, err
			}
			continue
		}
		switch key {
		case "general.quantized_by", "general.finetune", "general.organization",
			"general.basename", "general.size_label":
			if vtype != ggufTypeString {
				if err := skipGGUFValue(r, vtype); err != nil {
					return out, err
				}
				continue
			}
			s, err := ggufString(r)
			if err != nil {
				return out, err
			}
			switch key {
			case "general.quantized_by":
				out.QuantizedBy = strings.TrimSpace(s)
			case "general.finetune":
				out.Finetune = strings.TrimSpace(s)
			case "general.organization":
				out.Organization = strings.TrimSpace(s)
			case "general.basename":
				out.Basename = strings.TrimSpace(s)
			case "general.size_label":
				out.SizeLabel = strings.TrimSpace(s)
			}
		case "general.file_type":
			if vtype != ggufTypeUint32 {
				if err := skipGGUFValue(r, vtype); err != nil {
					return out, err
				}
				continue
			}
			var n uint32
			if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
				return out, err
			}
			out.FileType, out.FileTypeSet = n, true
		default:
			if err := skipGGUFValue(r, vtype); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// GGUF value type tags (from the spec's gguf_metadata_value_type enum).
const (
	ggufTypeUint8 uint32 = iota
	ggufTypeInt8
	ggufTypeUint16
	ggufTypeInt16
	ggufTypeUint32
	ggufTypeInt32
	ggufTypeFloat32
	ggufTypeBool
	ggufTypeString
	ggufTypeArray
	ggufTypeUint64
	ggufTypeInt64
	ggufTypeFloat64
)

// ggufStringMax bounds ONE string. A corrupt length field would otherwise ask for an
// arbitrary allocation - the classic way a malformed header turns a parser into an
// out-of-memory kill.
const ggufStringMax = 1 << 20

func ggufString(r io.Reader) (string, error) {
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > ggufStringMax {
		return "", errors.New("gguf string too long")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// skipGGUFValue advances past a value we do not want. It has to understand every type,
// including nested arrays, because the ONLY way to reach a later key is to step exactly
// over the earlier one - a wrong width here does not lose one value, it desynchronises
// the whole rest of the header.
func skipGGUFValue(r io.Reader, vtype uint32) error {
	switch vtype {
	case ggufTypeUint8, ggufTypeInt8, ggufTypeBool:
		return discard(r, 1)
	case ggufTypeUint16, ggufTypeInt16:
		return discard(r, 2)
	case ggufTypeUint32, ggufTypeInt32, ggufTypeFloat32:
		return discard(r, 4)
	case ggufTypeUint64, ggufTypeInt64, ggufTypeFloat64:
		return discard(r, 8)
	case ggufTypeString:
		_, err := ggufString(r)
		return err
	case ggufTypeArray:
		var elem uint32
		if err := binary.Read(r, binary.LittleEndian, &elem); err != nil {
			return err
		}
		var n uint64
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return err
		}
		// The tokenizer vocabulary lives here. Stepping over it element by element is
		// what the byte budget is for: the LimitReader ends the walk, the caller keeps
		// what it read, and nothing tries to hold 150k strings in memory.
		for i := uint64(0); i < n; i++ {
			if err := skipGGUFValue(r, elem); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unknown gguf value type")
	}
}

func discard(r io.Reader, n int64) error {
	_, err := io.CopyN(io.Discard, r, n)
	return err
}
