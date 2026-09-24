package edge

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/protocol"
)

// A streamed reply that TRUNCATES (an oversized line, a dropped upstream) must NOT end in a signed
// receipt: a signed receipt attests a completed turn, so signing one for a partial reply would bill
// the caller for what they did not get. Instead the stream ends in an error comment. (audit 2026-09-23)
func TestStreamInferDoesNotSignReceiptOnTruncation(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	s.SetServing(Serving{NodeID: "n_test", Key: key})

	// One line far larger than the scanner's 1 MiB cap and with no newline: bufio.Scanner stops with
	// ErrTooLong, which is exactly the silent-truncation shape.
	huge := strings.Repeat("x", 2<<20)
	resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(huge))}

	w := httptest.NewRecorder()
	s.streamInfer(w, resp, protocol.UsageReceipt{RequestID: "r1", NodeID: "n_test"})

	body := w.Body.String()
	if strings.Contains(body, "rogerai-receipt=") {
		t.Fatalf("a truncated stream must not emit a signed receipt; got:\n%s", body)
	}
	if !strings.Contains(body, "rogerai-error=stream-truncated") {
		t.Fatalf("a truncated stream must announce the error; got:\n%s", body)
	}
}

// An upstream that answered with an error status - even framed as SSE - did not complete the turn,
// so the streamed receipt must be VOIDED, not signed as a billable completion (audit 2026-09-24).
func TestStreamInferVoidsReceiptOnUpstreamError(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	s.SetServing(Serving{NodeID: "n_test", Key: key})

	resp := &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader("data: {\"error\":\"upstream\"}\n\n"))}
	w := httptest.NewRecorder()
	s.streamInfer(w, resp, protocol.UsageReceipt{RequestID: "r1", NodeID: "n_test"})

	body := w.Body.String()
	i := strings.Index(body, "rogerai-receipt=")
	if i < 0 {
		t.Fatalf("no receipt emitted:\n%s", body)
	}
	enc := body[i+len("rogerai-receipt="):]
	if j := strings.IndexAny(enc, "\r\n"); j >= 0 {
		enc = enc[:j]
	}
	rec, err := protocol.DecodeReceipt(strings.TrimSpace(enc))
	if err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if rec.VoidReason == "" {
		t.Fatalf("a receipt for an upstream error must carry a VoidReason; got %+v", rec)
	}
}
