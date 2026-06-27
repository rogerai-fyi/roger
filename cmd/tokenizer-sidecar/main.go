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
	"net"
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
	if err := run(os.Getenv("TOKENIZER_PORT"), os.Getenv("TOKENIZER_DIR"), nil, nil); err != nil {
		log.Fatal(err)
	}
}

// run binds the sidecar on 127.0.0.1:<port> (default 9099) and serves until stop is
// closed (nil = serve forever). When ready != nil the actual listen address is sent
// once bound, so a test can pass port "0", learn the chosen port, drive requests, then
// close stop for a clean shutdown. Extracted from main() so the full server lifecycle
// is exercised by tests; main() is just the env + fatal-log glue.
func run(port, dir string, ready chan<- string, stop <-chan struct{}) error {
	if port == "" {
		port = "9099"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: newMux(dir)}
	if dir != "" {
		log.Printf("tokenizer-sidecar: listening on %s (TOKENIZER_DIR=%s)", ln.Addr(), dir)
	} else {
		log.Printf("tokenizer-sidecar: listening on %s (no TOKENIZER_DIR; tiktoken-exact + heuristic only)", ln.Addr())
	}
	if ready != nil {
		ready <- ln.Addr().String()
	}
	if stop != nil {
		go func() { <-stop; _ = srv.Close() }()
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
