package main

import (
	"encoding/json"
	"net/http"
)

// ftoa renders a float as its compact JSON number form (used for X-RogerAI-*
// numeric headers so they parse cleanly client-side).
func ftoa(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// writeJSON / jsonErr standardize every JSON response (content-type + error shape).
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"message": msg}})
}

// cors lets the public website (rogerai.fyi) fetch read-only market data from a
// browser. Applied only to public GET endpoints (/discover, /market).
func cors(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type")
}

// corsPreflight answers a CORS preflight (OPTIONS) for the public read endpoints
// with 204 + the CORS headers. Returns true if it handled the request.
func corsPreflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	cors(w)
	w.WriteHeader(http.StatusNoContent)
	return true
}

// allow guards a handler's HTTP method, writing 405 if it doesn't match.
func allow(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		w.Header().Set("Allow", method)
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}
