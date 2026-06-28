package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// ftoa renders a float as its compact JSON number form (used for X-RogerAI-*
// numeric headers so they parse cleanly client-side).
func ftoa(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// fmtCostHeader formats a billed cost for the X-RogerAI-Cost DISPLAY header at its EXACT
// value, replacing the old round6(cost) that collapsed a real sub-microcredit charge to a
// bare "0". A few output tokens at $0.01/1M cost ~$0.00000036; round6 floored that to 0, so
// the consumer's per-reply + session cost read "$0.00" as if it were free. This sends the
// exact value ("0.00000036") so dollars() renders the truth. It rounds to 6 SIGNIFICANT
// figures (cleaning float noise like 0.1+0.2 -> 0.3) and re-emits a plain decimal (never
// scientific) so a tiny value parses + renders cleanly client-side. Billing settles at full
// precision elsewhere - this is display only. A zero/negative cost sends "0" (a free turn).
func fmtCostHeader(cost float64) string {
	if cost <= 0 {
		return "0"
	}
	g := strconv.FormatFloat(cost, 'g', 6, 64)
	f, err := strconv.ParseFloat(g, 64)
	if err != nil {
		f = cost
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
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
