package agent

// relay_allowlist_test.go is the white-box, table-driven companion to
// features/voice/relay_allowlist.feature. It pins (1) the exact normalization contract
// (cleanUpstreamPath) and (2) the allow/refuse decision the way serve() consumes it -
// isAllowedUpstreamPath(cleanUpstreamPath(raw)) - including the purely-lexical corners a Gherkin
// table cannot carry faithfully (godog trims cell whitespace; NUL/control bytes and backslashes
// read poorly in a table). A normalization or matching regression fails RED here.

import "testing"

// allowRaw mirrors exactly how serve() gates a broker-supplied path.
func allowRaw(raw string) bool { return isAllowedUpstreamPath(cleanUpstreamPath(raw)) }

func TestCleanUpstreamPath(t *testing.T) {
	tests := []struct{ raw, want string }{
		{"", ""},
		{"   ", ""},  // whitespace-only trims to blank (chat)
		{"\t\n", ""}, // any unicode whitespace
		{"/v1/chat/completions", "/v1/chat/completions"},   // canonical unchanged
		{" /v1/chat/completions ", "/v1/chat/completions"}, // surrounding whitespace trimmed
		{"/v1/audio/speech/", "/v1/audio/speech"},          // trailing slash stripped
		{"/v1//audio/speech", "/v1/audio/speech"},          // double slash collapsed
		{"/v1/./audio/speech", "/v1/audio/speech"},         // "." segment resolved
		{"/v1/../agents/run", "/agents/run"},               // ".." resolved AWAY from canonical
		{"//agents/run", "/agents/run"},                    // leading double slash collapsed
		{"http://x/agents/run", "http://x/agents/run"},     // not rooted -> returned untouched (won't match)
		{"\\agents\\run", "\\agents\\run"},                 // backslash form untouched (won't match)
		{"/v1/audio/speech\x00", "/v1/audio/speech\x00"},   // NUL is not whitespace -> preserved -> won't match
	}
	for _, tt := range tests {
		if got := cleanUpstreamPath(tt.raw); got != tt.want {
			t.Errorf("cleanUpstreamPath(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestIsAllowedUpstreamPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// ALLOWED - the only paths the broker ever dispatches.
		{"absent path is chat back-compat", "", true},
		{"canonical chat", "/v1/chat/completions", true},
		{"chat alias", "/chat/completions", true},
		{"tts speech", "/v1/audio/speech", true},
		{"stt transcriptions", "/v1/audio/transcriptions", true},

		// ALLOWED - cosmetic variants normalized to canonical (A5).
		{"trailing slash speech", "/v1/audio/speech/", true},
		{"trailing slash chat", "/v1/chat/completions/", true},
		{"double slash speech", "/v1//audio/speech", true},
		{"dot segment chat", "/v1/./chat/completions", true},
		{"leading space chat", " /v1/chat/completions", true},
		{"trailing space chat", "/v1/chat/completions ", true},
		{"leading tab speech", "\t/v1/audio/speech", true},
		{"trailing newline speech", "/v1/audio/speech\n", true},
		{"trailing crlf speech", "/v1/audio/speech\r\n", true},
		{"whitespace only is chat", "   ", true},
		{"single space is chat", " ", true},

		// REFUSED - dangerous local routes.
		{"agents run", "/agents/run", false},
		{"agents id run", "/agents/abc-123/run", false},
		{"mcp call", "/mcp/call", false},
		{"memory", "/memory/anything", false},
		{"pair", "/pair", false},
		{"secure", "/secure/x", false},
		{"admin", "/admin/reset", false},

		// REFUSED - traversal / slashes resolve AWAY from canonical.
		{"dotdot traversal", "/v1/../agents/run", false},
		{"audio dotdot", "/v1/audio/../agents/run", false},
		{"chat dotdot", "/v1/chat/../agents/run", false},
		{"speech dotdot to run", "/v1/audio/speech/../run", false},
		{"double slash route", "//agents/run", false},
		{"inner double slash route", "/v1//agents/run", false},

		// REFUSED - inner whitespace, control / NUL bytes (not trimmed away).
		{"inner space", "/v1/audio /speech", false},
		{"nul byte suffix", "/v1/audio/speech\x00", false},
		{"nul byte then route", "/v1/chat/completions\x00/agents/run", false},
		{"crlf injection mid", "/v1/audio/speech\r\nX-Evil: 1", false},

		// REFUSED - backslashes.
		{"backslashes", "\\agents\\run", false},
		{"mixed slash", "/v1\\audio\\speech", false},

		// REFUSED - case variants (matching is case-sensitive).
		{"upper chat", "/V1/Chat/Completions", false},
		{"upper speech tail", "/v1/audio/SPEECH", false},
		{"all upper speech", "/V1/AUDIO/SPEECH", false},

		// REFUSED - absolute URLs / schemes (not rooted, or collapse to a non-canonical path).
		{"http absolute", "http://127.0.0.1/agents/run", false},
		{"https absolute", "https://evil.example/mcp/call", false},
		{"file scheme", "file:///etc/passwd", false},
		{"scheme relative host", "//evil.example/agents/run", false},

		// REFUSED - query strings / fragments smuggling a route.
		{"speech query route", "/v1/audio/speech?x=/agents/run", false},
		{"chat query route", "/v1/chat/completions?path=/agents/run", false},
		{"fragment", "/v1/audio/speech#/agents/run", false},

		// REFUSED - percent-encoded traversal / separators (never decoded).
		{"encoded dotdot", "/v1/audio/%2e%2e/agents", false},
		{"encoded dotdot root", "/v1/%2e%2e/agents/run", false},
		{"encoded slash", "/agents%2frun", false},
		{"encoded null", "/v1/audio/speech%00", false},

		// REFUSED - lookalike suffixes (the old suffix match's blind spot) and bare root.
		{"root slash", "/", false},
		{"chat extra segment", "/chat/completions/extra", false},
		{"run then completions", "/agents/run/completions", false},
		{"evil speech suffix", "/evil/audio/speech", false},
		{"speech no v1", "/audio/speech", false},
		{"transcriptions no v1", "/audio/transcriptions", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allowRaw(tt.path); got != tt.want {
				t.Errorf("allow(%q) = %v, want %v (cleaned = %q)", tt.path, got, tt.want, cleanUpstreamPath(tt.path))
			}
		})
	}
}
