package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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
	jsonErrMeta(w, code, "", msg, nil)
}

// jsonErrCode is jsonErr with a machine-readable error.code beside the message
// (ROUTING-EXPRESSION-CONTRACT §2: no_match, band_cooling, unknown_routing_key, ...).
func jsonErrCode(w http.ResponseWriter, code int, errCode, msg string) {
	jsonErrMeta(w, code, errCode, msg, nil)
}

// jsonErrMeta writes the one error envelope (contract §14.B6): {"error":{code, message, type,
// metadata}}. code is derived from the message (legacyErrCode) or the status when empty;
// metadata gains the request id and Retry-After already set on the response; every error says
// it cost nothing (X-RogerAI-Cost: 0) unless a caller already set a cost header.
func jsonErrMeta(w http.ResponseWriter, status int, errCode, msg string, meta map[string]any) {
	h := w.Header()
	if h.Get("X-RogerAI-Cost") == "" {
		h.Set("X-RogerAI-Cost", "0")
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(errEnvelope(h, status, errCode, msg, meta))
}

// errEnvelope builds the error envelope's bytes for a response whose headers are h (read for
// the request id and Retry-After). It never fails: a caller always gets a valid envelope.
func errEnvelope(h http.Header, status int, errCode, msg string, meta map[string]any) []byte {
	if errCode == "" {
		errCode = legacyErrCode(msg, status)
	}
	m := map[string]any{}
	for k, v := range meta {
		m[k] = v
	}
	if h != nil {
		if id := h.Get("X-RogerAI-Request-Id"); id != "" {
			m["request_id"] = id
		}
		if ra := h.Get("Retry-After"); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil {
				m["retry_after_s"] = n
			}
		}
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code":     errCode,
		"message":  msg,
		"type":     errType(errCode, status),
		"metadata": m,
	}})
	return b
}

// errorBody is the error envelope as bytes, for paths that hand a body to a writer (the lazy
// SSE fail path, the dispatch-failure writer) rather than answering directly. code may be "".
// The request id and Retry-After are added by the writer that knows the response headers
// (withEnvelopeHeaders).
func errorBody(status int, code, msg string) []byte {
	return errEnvelope(nil, status, code, msg, nil)
}

// withEnvelopeHeaders adds the response's request id and Retry-After to an envelope built
// before those headers were known (errorBody). A body that is not an envelope is returned
// unchanged.
func withEnvelopeHeaders(h http.Header, body []byte) []byte {
	var v map[string]map[string]any
	if json.Unmarshal(body, &v) != nil || v["error"] == nil {
		return body
	}
	e := v["error"]
	m, _ := e["metadata"].(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	if id := h.Get("X-RogerAI-Request-Id"); id != "" {
		m["request_id"] = id
	}
	if ra := h.Get("Retry-After"); ra != "" {
		if n, err := strconv.Atoi(ra); err == nil {
			m["retry_after_s"] = n
		}
	}
	e["metadata"] = m
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

// upstreamErrorBody wraps a station's own error body on a final upstream failure (contract
// §14.B6): code upstream_error, the raw body (capped) under metadata.raw, and the station and
// served model named unless station is "" (the no-oracle rules: a private band never names its
// station; an anonymous caller naming a Tower gets no station either), plus the number of
// station attempts made when the caller knows it.
func upstreamErrorBody(h http.Header, status int, station, model string, attempts int, raw []byte) []byte {
	if len(raw) > upstreamRawCap {
		raw = raw[:upstreamRawCap]
	}
	meta := map[string]any{"raw": string(raw)}
	if station != "" {
		meta["station"] = station
		if model != "" {
			meta["model"] = model
		}
	}
	if attempts > 0 {
		meta["attempts"] = attempts
	}
	return errEnvelope(h, status, "upstream_error", fmt.Sprintf("the station answered %d", status), meta)
}

// legacyErrCodes gives every broker message that predates the envelope its contract code
// (§14.B6), matched on the message's start so a suffix (a model name, a hint) still maps.
var legacyErrCodes = []struct{ prefix, code string }{
	{"no station on that frequency", "band_unavailable"},
	{"invalid request signature", "invalid_signature"},
	{"spending requires a signed request", "signature_required"},
	{"session expired or invalid", "session_expired"},
	{"log in to spend", "login_required"},
	{"grant rate limit exceeded", "grant_rate_limited"},
	{"rate limit exceeded", "rate_limited"},
	{"node timed out", "station_timeout"},
	{"request blocked by the content policy", "content_refused"},
	{"content screening unavailable", "moderation_unavailable"},
	{"no node of this grant's owner is serving", "grant_unavailable"},
	{"node busy (no poller free)", "no_poller"},
	{"station busy (no poller free)", "no_poller"},
	{"station off air", "station_off_air"},
	{"method not allowed", "method_not_allowed"},
}

// legacyErrCode is the code for a message with none: the table above, else one derived from
// the status so code is never empty.
func legacyErrCode(msg string, status int) string {
	for _, c := range legacyErrCodes {
		if strings.HasPrefix(msg, c.prefix) {
			return c.code
		}
	}
	switch {
	case status == http.StatusUnauthorized:
		return "unauthorized"
	case status == http.StatusPaymentRequired:
		return "payment_required"
	case status == http.StatusForbidden:
		return "forbidden"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusConflict:
		return "conflict"
	case status == http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 500:
		return "server_error"
	default:
		return "invalid_request"
	}
}

// errTypes is the OpenAI-compatible error.type for codes whose type the status alone does not
// decide (contract §14.B6).
var errTypes = map[string]string{
	"content_refused":        "invalid_request_error",
	"moderation_unavailable": "server_error",
	"insufficient_balance":   "insufficient_quota",
	"key_limit_reached":      "insufficient_quota",
	"monthly_cap_reached":    "insufficient_quota",
}

// errType maps a code (and its status) to one of OpenAI's error types.
func errType(code string, status int) string {
	if t, ok := errTypes[code]; ok {
		return t
	}
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusPaymentRequired:
		return "insufficient_quota"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status == http.StatusServiceUnavailable:
		return "overloaded_error"
	case status == http.StatusGatewayTimeout:
		return "timeout_error"
	case status >= 500:
		return "server_error"
	case status == 0:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}

// cors lets the public website (rogerai.fm) fetch read-only market data from a
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

// upstreamRawCap bounds how much of a station's own error body is echoed under
// error.metadata.raw (contract §14.1).
const upstreamRawCap = 4 << 10

// consumerRejectedBody wraps a station's refusal of the request itself (an upstream 400,
// 401, 404, 413 or 422, contract §14.1) in the broker's envelope: the station's own body
// under metadata.raw (capped), and the station named unless station is "" (the no-oracle
// rules: a private band never names its station).
func consumerRejectedBody(station string, raw []byte) []byte {
	if len(raw) > upstreamRawCap {
		raw = raw[:upstreamRawCap]
	}
	meta := map[string]any{"raw": string(raw)}
	if station != "" {
		meta["station"] = station
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code":     "consumer_rejected",
		"type":     "invalid_request_error",
		"message":  "the station refused the request itself (a parameter or size it does not accept); not retried elsewhere",
		"metadata": meta,
	}})
	return b
}

// isEnvelope reports whether body is already a broker error envelope ({"error":{...}}).
func isEnvelope(body []byte) bool {
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	e, ok := v["error"]
	if !ok {
		return false
	}
	var inner map[string]any
	return json.Unmarshal(e, &inner) == nil && inner["code"] != nil
}
