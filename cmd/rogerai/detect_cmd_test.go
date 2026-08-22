package main

import (
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/detect"
)

func withScan(t *testing.T, found []detect.Found, needKey []string) {
	t.Helper()
	prev := detectScan
	detectScan = func() ([]detect.Found, []string) { return found, needKey }
	t.Cleanup(func() { detectScan = prev })
}

// A model whose runtime said nothing must print the dash, NOT a label inferred from its
// id. "qwen3-27b-q4" in the id is exactly the kind of string a guesser would mine, and a
// guess printed here would be indistinguishable from a measured fact.
func TestUndetectedVariantPrintsAbsentNotAGuess(t *testing.T) {
	f := detect.Found{
		Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1",
		Models: []string{"qwen3-27b-q4"},
		Quant:  map[string]string{}, Weights: map[string]string{}, Variant: map[string]string{},
	}
	got := detectLine(f, "qwen3-27b-q4", false)
	if got != "—" {
		t.Fatalf("undetected model must render as absent, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "q4") {
		t.Fatalf("a quant was mined from the model id: %q", got)
	}
}

func TestDetectedFieldsAreAllShown(t *testing.T) {
	f := detect.Found{
		Models:  []string{"m"},
		Quant:   map[string]string{"m": "Q4_K_M"},
		Weights: map[string]string{"m": "unsloth"},
		Variant: map[string]string{"m": "thinking"},
	}
	got := detectLine(f, "m", false)
	for _, want := range []string{"Q4_K_M", "unsloth", "thinking"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detected %q missing from %q", want, got)
		}
	}
}

// Partial detection is the COMMON case: a quant with no producer. It must not print a
// stray separator or an empty slot that reads as a blank field.
func TestPartialDetectionRendersOnlyWhatWasFound(t *testing.T) {
	f := detect.Found{
		Models: []string{"m"},
		Quant:  map[string]string{"m": "IQ4_XS"},
	}
	got := detectLine(f, "m", false)
	if got != "IQ4_XS" {
		t.Fatalf("partial detection should render only the found field, got %q", got)
	}
}

// trunc is the same shape as the pad() bug that crashed the TUI at zero width:
// r[:n-1] becomes r[:-1]. Every width here is derived from a model id length, so a
// pathological input must return, not panic.
func TestTruncNeverPanicsAtDegenerateWidths(t *testing.T) {
	for _, n := range []int{-3, -1, 0, 1, 2} {
		for _, s := range []string{"", "a", "abc", "héllo-wörld"} {
			_ = trunc(s, n) // must not panic
		}
	}
	if got := trunc("abcdef", 3); got != "ab…" {
		t.Fatalf("trunc(abcdef,3) = %q", got)
	}
}

// Nothing serving is a STATE, not an error, and it must say what to do about it.
func TestNoServerSaysWhatToDo(t *testing.T) {
	withScan(t, nil, nil)
	out := captureStdout(t, func() {
		if err := cmdDetect(nil); err != nil {
			t.Fatalf("cmdDetect: %v", err)
		}
	})
	if !strings.Contains(out, "no local OpenAI-compatible server") {
		t.Fatalf("missing the state: %q", out)
	}
	if !strings.Contains(out, "ollama") {
		t.Fatalf("did not name a runtime to start: %q", out)
	}
}

// A server that is up but key-gated is distinct from nothing listening, and detect must
// keep them distinct - that distinction is why detect.Status is tri-state at all.
func TestKeyGatedServerIsReportedNotSwallowed(t *testing.T) {
	withScan(t, nil, []string{"http://127.0.0.1:8080/v1"})
	out := captureStdout(t, func() { _ = cmdDetect(nil) })
	if !strings.Contains(out, "needs a key") {
		t.Fatalf("key-gated server not reported: %q", out)
	}
}

func TestDetectListsModelsPerServer(t *testing.T) {
	withScan(t, []detect.Found{{
		Name: "llama.cpp", BaseURL: "http://127.0.0.1:8080/v1",
		Models: []string{"zeta", "alpha"},
		Quant:  map[string]string{"alpha": "Q8_0"},
	}}, nil)
	out := captureStdout(t, func() { _ = cmdDetect(nil) })
	for _, want := range []string{"llama.cpp", "alpha", "zeta", "Q8_0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from output: %q", want, out)
		}
	}
	// sorted: alpha before zeta, so the list is stable run to run
	if strings.Index(out, "alpha") > strings.Index(out, "zeta") {
		t.Fatalf("models not sorted: %q", out)
	}
}

// -v adds the fields an operator checks when a share behaves oddly: what KIND of model
// the runtime thinks it is, what it can do, and the context window. They are off by
// default because the common question is only "what variant am I publishing as".
func TestVerboseAddsModalityCapabilitiesAndCtx(t *testing.T) {
	f := detect.Found{
		Models:       []string{"m"},
		Quant:        map[string]string{"m": "Q4_K_M"},
		Modality:     map[string]string{"m": "tts"},
		Capabilities: map[string][]string{"m": {"vision"}},
		Ctx:          map[string]int{"m": 32768},
	}
	quiet := detectLine(f, "m", false)
	for _, hidden := range []string{"tts", "vision", "32768"} {
		if strings.Contains(quiet, hidden) {
			t.Fatalf("%q leaked into the default view: %q", hidden, quiet)
		}
	}
	loud := detectLine(f, "m", true)
	for _, want := range []string{"tts", "vision", "32768"} {
		if !strings.Contains(loud, want) {
			t.Fatalf("-v missing %q: %q", want, loud)
		}
	}
}

// "chat" is the default modality, so printing it would be noise on every single row.
func TestVerboseOmitsTheDefaultModality(t *testing.T) {
	f := detect.Found{Models: []string{"m"}, Modality: map[string]string{"m": "chat"}}
	if got := detectLine(f, "m", true); strings.Contains(got, "chat") {
		t.Fatalf("default modality printed as though it were a finding: %q", got)
	}
}

func TestVerboseFlagIsAccepted(t *testing.T) {
	withScan(t, []detect.Found{{
		Name: "ollama", Models: []string{"m"},
		Ctx: map[string]int{"m": 8192},
	}}, nil)
	for _, flag := range []string{"-v", "--verbose"} {
		out := captureStdout(t, func() { _ = cmdDetect([]string{flag}) })
		if !strings.Contains(out, "8192") {
			t.Fatalf("%s did not enable the verbose column: %q", flag, out)
		}
	}
}

func TestHelpPrintsUsageAndDoesNotScan(t *testing.T) {
	scanned := false
	prev := detectScan
	detectScan = func() ([]detect.Found, []string) { scanned = true; return nil, nil }
	t.Cleanup(func() { detectScan = prev })
	out := captureStdout(t, func() { _ = cmdDetect([]string{"--help"}) })
	if !strings.Contains(out, "usage: roger detect") {
		t.Fatalf("no usage: %q", out)
	}
	if scanned {
		t.Fatal("--help scanned the machine")
	}
}

// A server that answers but lists nothing is a distinct, reportable state - it means the
// runtime is up with no model loaded, which is a thing an operator fixes.
func TestServingButNoModelsIsStated(t *testing.T) {
	withScan(t, []detect.Found{{Name: "vllm", BaseURL: "http://127.0.0.1:8000/v1"}}, nil)
	out := captureStdout(t, func() { _ = cmdDetect(nil) })
	if !strings.Contains(out, "reported no models") {
		t.Fatalf("empty server not reported: %q", out)
	}
}

// The name column is width-capped, and the cap runs through the same truncation that
// crashed the TUI. A very long id must render, not panic or blow the column out.
func TestVeryLongModelIdIsTruncatedNotFatal(t *testing.T) {
	long := strings.Repeat("qwen3-very-long-model-identifier-", 4)
	withScan(t, []detect.Found{{
		Name: "ollama", Models: []string{long},
		Quant: map[string]string{long: "Q4_K_M"},
	}}, nil)
	out := captureStdout(t, func() { _ = cmdDetect(nil) })
	if !strings.Contains(out, "…") {
		t.Fatalf("long id was not truncated: %q", out)
	}
	if !strings.Contains(out, "Q4_K_M") {
		t.Fatalf("truncation ate the variant column: %q", out)
	}
}
