package protocol

// A node returns a served job's result to the broker by JSON-encoding a JobResult and POSTing it
// to /agent/result; the broker JSON-decodes it and writes Body back to the consumer. For a VOICE
// (tts/stt) job the Body is OPAQUE BINARY (a WAV / MP3), not JSON. These tests pin that a JobResult
// round-trips ARBITRARY bytes through json.Marshal/Unmarshal byte-for-byte — the regression guard
// for the live hang where a binary Body could not be marshalled at all (a WAV is not valid JSON, so
// the old json.RawMessage Body made json.Marshal FAIL and the node posted an empty result).

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

// wavBytes is a tiny but REAL RIFF/WAVE header + a non-JSON, non-UTF8 tail (0xff bytes) — the exact
// shape that broke json.RawMessage marshalling (the '{'-expecting validator rejects 'R').
func wavBytes() []byte {
	return append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), 0x00, 0xff, 0xfe, 0x01)
}

func TestJobResultRoundTripsBinaryBody(t *testing.T) {
	cases := map[string][]byte{
		"wav":          wavBytes(),
		"raw-bytes":    {0x00, 0x01, 0x02, 0xff, 0xfe},
		"not-json":     []byte("not-json-{[}]"),
		"valid-json":   []byte(`{"choices":[{"message":{"content":"ok"}}]}`), // the chat path must still work
		"empty":        {},
		"nil":          nil,
		"utf8-invalid": {0xc3, 0x28}, // an invalid UTF-8 sequence must survive too
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			in := JobResult{ID: "j1", Status: 200, Body: body}
			wire, err := json.Marshal(in)
			if err != nil {
				t.Fatalf("json.Marshal(JobResult) failed for %s body: %v", name, err)
			}
			var out JobResult
			if err := json.Unmarshal(wire, &out); err != nil {
				t.Fatalf("json.Unmarshal for %s body: %v", name, err)
			}
			if out.ID != "j1" || out.Status != 200 {
				t.Errorf("meta lost: id=%q status=%d", out.ID, out.Status)
			}
			// A nil and an empty slice are equivalent as a response body (both write nothing).
			if len(body) == 0 {
				if len(out.Body) != 0 {
					t.Errorf("empty body did not round-trip empty, got %q", out.Body)
				}
				return
			}
			if !bytes.Equal(out.Body, body) {
				t.Errorf("body not byte-for-byte:\n in=%v\nout=%v", body, out.Body)
			}
		})
	}
}

// TestJobResultBinaryOverHTTPBody mirrors the real wire: the node marshals the result and the
// broker reads it back off an io.Reader (as agentResult does with io.ReadAll(r.Body)). Proves the
// binary body survives an actual encode -> stream -> decode, not just an in-memory Marshal.
func TestJobResultBinaryOverHTTPBody(t *testing.T) {
	body := wavBytes()
	wire, err := json.Marshal(JobResult{ID: "j2", Status: 200, Body: body})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Simulate the broker side: read the POST body off a reader, then decode.
	raw, _ := io.ReadAll(bytes.NewReader(wire))
	var out JobResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("broker-side decode: %v", err)
	}
	if !bytes.Equal(out.Body, body) {
		t.Fatalf("binary body corrupted over the wire:\n in=%v\nout=%v", body, out.Body)
	}
}
