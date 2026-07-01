package agent

// binary_return_test.go proves the NODE's NON-STREAM return path (serve() + postResult()) carries
// an OPAQUE BINARY body (a WAV, the /v1/audio/speech case) intact to the broker. This is the node
// half of the live `roger say` HANG: a voice job takes serve()+postResult() (not the SSE stream
// path), postResult() json.Marshal's the JobResult, and the broker json.Unmarshal's it off
// /agent/result. When JobResult.Body was a json.RawMessage, marshalling a WAV FAILED (a WAV is not
// valid JSON) and postResult posted an EMPTY body the broker rejected with 400 "bad result" — the
// consumer then got nothing and timed out. These tests use the REAL serve()/postResult() against a
// real stub upstream + a real /agent/result receiver — no mocks.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// binaryWAV is a tiny REAL RIFF/WAVE payload with non-JSON, non-UTF8 tail bytes — the exact shape
// that could not be JSON-marshalled as a json.RawMessage.
func binaryWAV() []byte {
	return append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), 0x00, 0xff, 0xfe, 0x01, 0x02)
}

// TestServePostResultCarriesBinaryBody drives the node's real return path end-to-end at the node
// boundary: serve() fetches a binary WAV from a real stub upstream, then postResult() marshals +
// POSTs the JobResult to a real /agent/result receiver. The receiver decodes exactly as the broker
// does and asserts the body arrived BYTE-FOR-BYTE — the direct regression for the marshalling hang.
func TestServePostResultCarriesBinaryBody(t *testing.T) {
	wav := binaryWAV()

	// Real stub upstream: returns binary audio on the speech path (mirrors voice_stub.py).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wav)
	}))
	defer upstream.Close()

	// Real /agent/result receiver: decode the POSTed JobResult exactly like broker.agentResult.
	var got protocol.JobResult
	var decodeErr error
	resultURL := ""
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/result" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err := json.Unmarshal(body, &got); err != nil {
			decodeErr = err
			http.Error(w, "bad result", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer broker.Close()
	resultURL = broker.URL

	_, priv, _ := ed25519.GenerateKey(nil)
	cfg := Config{NodeID: "n1", Broker: resultURL, Model: "af_heart", Upstream: upstream.URL + "/v1/chat/completions"}
	offer := protocol.ModelOffer{Model: "af_heart", Modality: protocol.ModalityTTS}

	// A voice job (broker-tagged Path) served against the local speech endpoint.
	job := protocol.Job{ID: "j1", Body: []byte(`{"input":"roger that"}`), Path: "/v1/audio/speech"}
	res := serve(cfg, offer, priv, upstream.Client(), job)
	if res.Status != http.StatusOK {
		t.Fatalf("serve status = %d, want 200", res.Status)
	}
	if !bytes.Equal(res.Body, wav) {
		t.Fatalf("serve() Body not the upstream WAV:\n in=%v\nout=%v", wav, res.Body)
	}

	// The REAL return to the broker: this is the json.Marshal that used to fail on binary.
	postResult(broker.Client(), cfg, "tok", res)

	if decodeErr != nil {
		t.Fatalf("broker could not decode the posted result (the hang bug): %v", decodeErr)
	}
	if got.ID != "j1" || got.Status != http.StatusOK {
		t.Fatalf("posted result meta lost: id=%q status=%d (empty body => postResult sent nothing)", got.ID, got.Status)
	}
	if !bytes.Equal(got.Body, wav) {
		t.Fatalf("binary body did not survive postResult -> /agent/result:\n in=%v\nout=%v", wav, got.Body)
	}
}

// TestPostResultBinaryMarshalsNonEmpty is the tightest guard: postResult MUST send a non-empty,
// decodable body for a binary JobResult. It records the raw bytes the node actually PUT on the wire
// (the thing that was empty when json.Marshal failed) and confirms they decode back to the WAV.
func TestPostResultBinaryMarshalsNonEmpty(t *testing.T) {
	wav := binaryWAV()
	var wire []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cfg := Config{NodeID: "n1", Broker: srv.URL}
	postResult(srv.Client(), cfg, "tok", protocol.JobResult{ID: "j2", Status: 200, Body: wav})

	if len(wire) == 0 {
		t.Fatal("postResult sent an EMPTY body for a binary result — the json.Marshal-fails hang")
	}
	var out protocol.JobResult
	if err := json.Unmarshal(wire, &out); err != nil {
		t.Fatalf("posted wire is not decodable JSON: %v\nwire=%s", err, wire)
	}
	if !bytes.Equal(out.Body, wav) {
		t.Fatalf("posted binary body corrupted:\n in=%v\nout=%v", wav, out.Body)
	}
}
