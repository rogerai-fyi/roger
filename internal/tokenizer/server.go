package tokenizer

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
)

// The tokenizer service (`rogerai-broker tokenizer`): the broker's L1 independent token
// re-count, run as its own process off the request hot path so re-counting never adds latency
// to inference. It holds no model weights, only merge tables, so it is CPU-cheap.
//
//	POST /count  {"model":"gpt-4o","text":"..."} -> {"tokens":12,"exact":true,"method":"tiktoken:o200k_base"}
//	GET  /health -> "ok"
//
// It listens on 127.0.0.1:$TOKENIZER_PORT (default 9099); TOKENIZER_DIR (optional) points at
// pinned per-model HuggingFace tokenizer.json files.

type countRequest struct {
	Model string `json:"model"`
	Text  string `json:"text"`
}

type countResponse struct {
	Tokens int    `json:"tokens"`
	Exact  bool   `json:"exact"`
	Method string `json:"method,omitempty"`
}

// Handler is the service's HTTP routes over a Counter for dir (empty = tiktoken-exact plus the
// heuristic only).
func Handler(dir string) http.Handler {
	counter := New(dir)
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

// ListenAddr is where the service listens for port (empty = the default 9099): loopback only.
func ListenAddr(port string) string {
	if port == "" {
		port = "9099"
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// Serve binds the service on ListenAddr(port) and serves until stop is closed (nil = serve
// forever). When ready != nil the bound address is sent once listening, so a test can pass port
// "0", learn the chosen port, drive requests, then close stop for a clean shutdown.
func Serve(port, dir string, ready chan<- string, stop <-chan struct{}) error {
	ln, err := net.Listen("tcp", ListenAddr(port))
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: Handler(dir)}
	if dir != "" {
		log.Printf("tokenizer: listening on %s (TOKENIZER_DIR=%s)", ln.Addr(), dir)
	} else {
		log.Printf("tokenizer: listening on %s (no TOKENIZER_DIR; tiktoken-exact + heuristic only)", ln.Addr())
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
