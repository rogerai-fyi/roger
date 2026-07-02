package main

// stationerr.go — the NODE-SIDE FAILURE reason for the voice relay (features/voice/
// error_passthrough.feature; contract: roger-ios docs/BROKER-VOICE-API.md "Error passthrough").
// A station's error body may carry a useful reason ("Voice X not found") but also local paths,
// hosts, ports, or keys — so the broker EXTRACTS the reason only from a standard error shape,
// SANITIZES it here (never trusting the node's own redaction), truncates it short, and the
// caller screens it like STT output before relaying. The consumer-facing status is 500: it must
// be a 5xx (the failure is the station's, not the caller's) that the CDN edge passes through
// with the JSON body intact — the edge REPLACES origin 502/504 bodies with its branded HTML
// page, which is exactly how the reason used to get lost.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// stationReasonMaxRunes caps the sanitized reason (short by contract; the full node error is
// never needed by a consumer - it is a headline, not a log line).
const stationReasonMaxRunes = 120

// The redaction set. Order matters: URLs before bare host:port (a URL contains one), keys
// before emails (a key can contain '@'-free text but keep it simple), paths last-ish.
var stationReasonScrub = []*regexp.Regexp{
	regexp.MustCompile(`(?i)https?://\S+`),                                                 // URLs (any case)
	regexp.MustCompile(`(?i)Bearer\s+\S+`),                                                 // bearer tokens (any case)
	regexp.MustCompile(`(?i)\brog[-_][\w.-]+`),                                             // roger key material
	regexp.MustCompile(`(?i)\bsk[-_][\w-]{4,}`),                                            // provider key material (sk-… / sk_live_…)
	regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`),                                       // emails
	regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d{1,5})?`),                               // IPv4(:port)
	regexp.MustCompile(`\[?[0-9A-Fa-f]{0,4}(:[0-9A-Fa-f]{0,4}){2,7}(%\w+)?\]?(:\d{1,5})?`), // IPv6 (bracketed/zone/port)
	regexp.MustCompile(`\b[\w-]+(\.[\w-]+)+(:\d{1,5})?\b`),                                 // hostnames incl. host:port
	regexp.MustCompile(`(?i)\b[a-z][\w-]*:\d{2,5}\b`),                                      // DOTLESS host:port (kokoro:8880, gpu-node:11434, localhost:8880) - a letter-led host token + a 2-5 digit port, contiguous, so it does not eat "error: 500" or "3:2"
	regexp.MustCompile(`\b[A-Za-z]:[\\/][^\s"']+`),                                         // windows paths (back- or forward-slash)
	// unix absolute paths (>=2 segments). Anchored on any NON-path character — not just
	// start/whitespace — so the canonical quoted/colon/paren FastAPI forms are caught too:
	//   No such file or directory: '/home/op/voices/af.pt'
	regexp.MustCompile(`(?:^|[^\w.@-])(/[\w.@-]+){2,}/?`),
	regexp.MustCompile(`[[:cntrl:]]`), // control chars
}

var stationReasonSpaces = regexp.MustCompile(`\s+`)

// sanitizeStationReason strips station internals from an extracted reason and truncates it.
// Returns "" when nothing presentable survives.
func sanitizeStationReason(s string) string {
	for _, re := range stationReasonScrub {
		s = re.ReplaceAllString(s, " ")
	}
	s = strings.TrimSpace(stationReasonSpaces.ReplaceAllString(s, " "))
	r := []rune(s)
	if len(r) > stationReasonMaxRunes {
		s = strings.TrimSpace(string(r[:stationReasonMaxRunes-1])) + "…"
	}
	return s
}

// stationErrReason extracts the failure reason from a station's error result and returns the
// consumer-facing message plus whether a NODE-authored reason was extracted (extracted=false
// means the message is broker-generic and needs no moderation screen). Only STANDARD error
// shapes are read — {"error":"s"}, {"error":{"message":"s"}}, {"detail":"s"} (FastAPI),
// {"message":"s"} — anything else (plain text, HTML, binary, unknown JSON) degrades to the
// generic form; the raw body NEVER relays. An empty body has its own generic (the status
// alone can be misleading, e.g. an empty 200).
func stationErrReason(status int, body []byte) (msg string, extracted bool) {
	generic := fmt.Sprintf("station error (status %d)", status)
	if len(body) == 0 {
		return "station error (empty result)", false
	}
	var probe struct {
		Error   json.RawMessage `json:"error"`
		Detail  string          `json:"detail"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return generic, false
	}
	reason := ""
	switch {
	case len(probe.Error) > 0:
		var s string
		if json.Unmarshal(probe.Error, &s) == nil {
			reason = s
		} else {
			var nested struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(probe.Error, &nested) == nil {
				reason = nested.Message
			}
		}
	case probe.Detail != "":
		reason = probe.Detail
	case probe.Message != "":
		reason = probe.Message
	}
	reason = sanitizeStationReason(reason)
	if reason == "" {
		return generic, false
	}
	return "station error: " + reason, true
}
