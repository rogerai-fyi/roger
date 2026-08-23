package harness

import "strings"

// failure.go - THE HUMAN FACE OF A FAILED TURN.
//
// A relay failure arrives as whatever the broker or the station wrote on the way out, and
// the worst of those is a bare "the station returned status 504 with no reply": it names a
// number, blames "the station", and leaves the reader with nothing to do. What a 504
// actually means is that the broker had nobody answering for that band in time.
//
// The mapping from raw text to that sentence used to live in the TUI, which is where the
// only surface that rendered a failed turn was. The browser console now runs turns too and
// hits the same 504 on the same bands, so the judgement moves HERE - beside the completers
// that produce the errors - exactly as the context-overflow spelling list did (see
// IsContextOverflow). Two copies would drift, and the failure mode is ugly: the terminal
// and the browser explaining the same dead band in two different ways, one of them wrong.
//
// This half is PURE TEXT. Each front-end pairs it with its own remedy, because the moves
// differ: the TUI can say "[2] go on air", the console has tabs and buttons instead.

// ShortFailure maps a raw relay error to a tight, plain first clause. It recognises the
// common shapes the broker/completer return (a 5xx with no reply, a timeout, an unreachable
// broker, an empty response, "no station / no node") and collapses each to a short phrase;
// anything else is passed through (clipped) so the real cause is never hidden.
//
// model (when known) names the band in the no-station / no-reply / empty-reply shapes, so a
// bare status code becomes a sentence about WHICH model has nobody on air.
func ShortFailure(raw, model string) string {
	s := strings.TrimSpace(raw)
	low := strings.ToLower(s)
	switch {
	// Checked BEFORE the no-station shapes: a context overflow is a healthy station
	// refusing an oversized conversation, and must never be reported as nobody being on
	// air. Name the model, because WHICH window was outgrown is the whole point - a small
	// on-device band (Apple foundation, 8K) fills where a big one would not.
	case IsContextOverflow(low):
		if model != "" {
			return "the conversation outgrew " + model + "'s context window"
		}
		return "the conversation outgrew this model's context window"
	case strings.Contains(low, "no station") || strings.Contains(low, "no node") || strings.Contains(low, "not on air") || strings.Contains(low, "no model is tuned in"):
		return NoStationServing(model) + StatusSuffix(s)
	case strings.Contains(low, "no reply") || strings.Contains(low, "within ") && strings.Contains(low, "slow or offline"):
		return NoStationServing(model) + StatusSuffix(s)
	case strings.Contains(low, "with no reply") || strings.Contains(low, "empty response") || strings.Contains(low, "no text"):
		return NoStationServing(model) + StatusSuffix(s)
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline exceeded") || strings.Contains(low, "timed out"):
		return "the station timed out" + StatusSuffix(s)
	case strings.Contains(low, "decode() failed") || strings.Contains(low, "failed to process"):
		// A station-side inference crash (e.g. llama.cpp 'failed to process speculative
		// batch'): the band exists and usually recovers - say so instead of implying
		// nobody is on air.
		return "the station hit an internal error - try again, it usually recovers" + StatusSuffix(s)
	case strings.Contains(low, "could not reach the broker") || strings.Contains(low, "broker unreachable") || strings.Contains(low, "connection refused") || strings.Contains(low, "connection reset"):
		return "could not reach the broker"
	}
	return clipFailure(s)
}

// NoStationServing is the no-station phrase, naming the model when we know it: "no station
// is serving gpt-oss-20b right now" (vs the generic "no station is on air right now" when
// the model is unknown). It is the human face of a relay 504 with nobody on the other end,
// and the TUI reaches for it directly on the paths that already know nobody is on air.
func NoStationServing(model string) string {
	if model == "" {
		return "no station is on air right now"
	}
	return "no station is serving " + model + " right now"
}

// StatusSuffix pulls a trailing "(NNN)" out of a raw error that named an HTTP status (e.g.
// "... status 504 ...") so the short phrase can carry the code: "no station answered (504)".
// Empty when no 3-digit status is present. Exported because the code is the one part of the
// raw text worth keeping when everything else about it is noise.
func StatusSuffix(s string) string {
	low := strings.ToLower(s)
	i := strings.Index(low, "status ")
	if i < 0 {
		return ""
	}
	rest := s[i+len("status "):]
	n := 0
	for n < len(rest) && n < 3 && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	if n == 0 {
		return ""
	}
	return " (" + rest[:n] + ")"
}

// clipFailure flattens and bounds a pass-through cause so an unrecognised error cannot
// swallow the line it is rendered on.
func clipFailure(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 80
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
