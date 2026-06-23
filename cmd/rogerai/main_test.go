package main

import "testing"

func TestNormalizeUpstream(t *testing.T) {
	const want = "http://127.0.0.1:8060/v1/chat/completions"
	cases := map[string]string{
		"http://127.0.0.1:8060":                      want, // base URL (the natural input)
		"http://127.0.0.1:8060/":                     want, // trailing slash
		"http://127.0.0.1:8060/v1":                   want, // /v1 base
		"http://127.0.0.1:8060/v1/":                  want, // /v1 with slash
		"http://127.0.0.1:8060/v1/chat/completions":  want, // already full (idempotent)
		"http://127.0.0.1:8060/v1/chat/completions/": want, // full + slash
	}
	for in, exp := range cases {
		if got := normalizeUpstream(in); got != exp {
			t.Errorf("normalizeUpstream(%q) = %q, want %q", in, got, exp)
		}
	}
	if got := normalizeUpstream(""); got != "" {
		t.Errorf("normalizeUpstream(\"\") = %q, want empty", got)
	}
}
