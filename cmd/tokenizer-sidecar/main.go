// tokenizer-sidecar is a tiny standalone HTTP service that re-counts tokens for
// the broker's L1 independent token re-count (see docs-internal/
// VERIFICATION-DESIGN.md). It is a SEPARATE process the broker calls over
// localhost, off the request hot path, so token re-counting never adds latency
// to inference and the broker stays dependency-light.
//
// It holds no model weights - just BPE/SentencePiece merge tables - so it is
// CPU-cheap (microseconds to low-ms per request) and trivially parallel.
//
// API:
//
//	POST /count  {"model":"gpt-4o","text":"..."} -> {"tokens":12,"exact":true,"method":"tiktoken:o200k_base"}
//	GET  /health -> "ok"
//
// Listens on 127.0.0.1:$TOKENIZER_PORT (default 9099). TOKENIZER_DIR (optional)
// points at pinned per-model HuggingFace tokenizer.json files (exact path is a
// follow-up; see internal/tokenizer/hf.go).
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/rogerai-fyi/roger/internal/tokenizer"
)

type countRequest struct {
	Model string `json:"model"`
	Text  string `json:"text"`
}

type countResponse struct {
	Tokens int    `json:"tokens"`
	Exact  bool   `json:"exact"`
	Method string `json:"method,omitempty"`
}

// newMux builds the sidecar's HTTP routes over a tokenizer.Counter for TOKENIZER_DIR
// (empty = tiktoken-exact + heuristic only). Extracted from main() so the real handler
// wiring is exercised by tests (main() only adds the env/listen glue).
func newMux(dir string) http.Handler {
	counter := tokenizer.New(dir)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		var req countRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		res := counter.Count(req.Model, req.Text)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(countResponse{Tokens: res.Tokens, Exact: res.Exact, Method: res.Method})
	})
	return mux
}

func main() {
	port := os.Getenv("TOKENIZER_PORT")
	if port == "" {
		port = "9099"
	}
	dir := os.Getenv("TOKENIZER_DIR")
	mux := newMux(dir)

	addr := "127.0.0.1:" + port
	if dir != "" {
		log.Printf("tokenizer-sidecar: listening on %s (TOKENIZER_DIR=%s)", addr, dir)
	} else {
		log.Printf("tokenizer-sidecar: listening on %s (no TOKENIZER_DIR; tiktoken-exact + heuristic only)", addr)
	}
	log.Fatal(http.ListenAndServe(addr, mux))
}
