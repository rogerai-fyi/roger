package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// web/src/vast-onstart.sh (served at rogerai.fm/vast-onstart.sh) is what an operator pastes into Vast.ai's ONSTART box to turn a
// rented GPU into a station. It runs unattended on somebody else's machine with no TTY, so
// the failure modes that matter are the quiet ones:
//
//   - going on air FREE when the operator asked to EARN. Pricing needs a signed-in owner;
//     if the box has none, the honest move is to STOP and say so, not to serve for nothing
//     on rented hardware that bills by the hour. That is a money bug, so it is the first
//     thing tested here.
//   - guessing at the upstream. The detector probes a dozen ports and vLLM shares 8000
//     with TGI, so the script passes --upstream explicitly rather than hoping.
//   - starting nothing and hanging. A missing vLLM has to be a clear message, not a wait.
//
// ROGER_DRY_RUN=1 makes the script resolve everything, print the plan, and touch nothing,
// which is how all of that is exercised without a GPU.

// envWithoutRoger is os.Environ() with every ROGER_* setting stripped. (The package
// already has a cleanEnv, which strips GIT_* for the push-gate tests - a different job.) The script is steered
// ENTIRELY by those variables, so inheriting the developer's shell would let an ambient
// ROGER_PRICE_OUT or ROGER_MODEL decide what these tests observe - the "free by default"
// assertions would pass or fail depending on whose machine ran them.
func envWithoutRoger() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ROGER_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func runOnstart(t *testing.T, env map[string]string) (string, int) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command("bash", filepath.Join("..", "..", "web", "src", "vast-onstart.sh"))
	cmd.Env = append(envWithoutRoger(),
		"ROGER_DRY_RUN=1",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("vast-onstart.sh: %v", err)
	}
	return string(out), code
}

func TestVastOnstartRefusesToEarnWithoutAnOwner(t *testing.T) {
	out, code := runOnstart(t, map[string]string{"ROGER_PRICE_OUT": "0.30"})
	if code == 0 {
		t.Fatalf("a priced share with no signed-in owner must not proceed:\n%s", out)
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "roger login") {
		t.Errorf("the refusal must name the fix (`roger login`):\n%s", out)
	}
	// the specific trap: quietly serving for free on hardware that bills by the hour
	if strings.Contains(low, "going on air free") || strings.Contains(low, "price 0") {
		t.Errorf("it must not fall back to a free share when a price was asked for:\n%s", out)
	}
}

func TestVastOnstartEarnsWhenTheBoxHasAnOwner(t *testing.T) {
	home := t.TempDir()
	conf := filepath.Join(home, ".config", "rogerai")
	if err := os.MkdirAll(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "auth.json"), []byte(`{"github_login":"x","github_id":1,"bound_at":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "web", "src", "vast-onstart.sh"))
	cmd.Env = append(envWithoutRoger(), "ROGER_DRY_RUN=1", "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "ROGER_PRICE_OUT=0.30")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("a signed-in box must be allowed to earn: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--price-out 0.30") {
		t.Errorf("the plan must carry the price it was asked for:\n%s", out)
	}
}

func TestVastOnstartIsFreeByDefaultAndNeedsNoOwner(t *testing.T) {
	out, code := runOnstart(t, nil)
	if code != 0 {
		t.Fatalf("a free share must work with no login at all:\n%s", out)
	}
	if strings.Contains(out, "--price-out") || strings.Contains(out, "--price-in") {
		t.Errorf("a free share must not pass a price flag:\n%s", out)
	}
}

func TestVastOnstartPinsTheUpstreamRatherThanGuessing(t *testing.T) {
	// vLLM shares port 8000 with TGI in the detector's probe table, and the box may be
	// running something else too. The script says which endpoint it means.
	out, _ := runOnstart(t, map[string]string{"ROGER_PORT": "8123"})
	if !strings.Contains(out, "--upstream http://127.0.0.1:8123/v1") {
		t.Errorf("the share must pin the upstream to the port it started:\n%s", out)
	}
}

func TestVastOnstartCarriesTheModelThroughToBothSides(t *testing.T) {
	out, _ := runOnstart(t, map[string]string{"ROGER_MODEL": "Qwen/Qwen3-8B"})
	if !strings.Contains(out, "Qwen/Qwen3-8B") {
		t.Errorf("the chosen model must appear in the plan:\n%s", out)
	}
	// it has to reach the server that loads it AND the share that advertises it
	if strings.Count(out, "Qwen/Qwen3-8B") < 2 {
		t.Errorf("the model must be given to both vllm and roger share:\n%s", out)
	}
}

func TestVastOnstartDefaultsToAServeableSmallModel(t *testing.T) {
	out, _ := runOnstart(t, nil)
	if !strings.Contains(out, "Qwen") {
		t.Errorf("the default model should be a small open one an 8-16GB card can hold:\n%s", out)
	}
}

func TestVastOnstartDryRunStartsNothing(t *testing.T) {
	// The whole point of the mode: it must not install, download or serve anything.
	out, code := runOnstart(t, nil)
	if code != 0 {
		t.Fatalf("dry run should succeed:\n%s", out)
	}
	for _, bad := range []string{"Downloading", "install.sh | sh", "Killed"} {
		if strings.Contains(out, bad) {
			t.Errorf("dry run performed real work (%q):\n%s", bad, out)
		}
	}
	if !strings.Contains(strings.ToLower(out), "dry run") {
		t.Errorf("dry run should say that is what it is:\n%s", out)
	}
}

// Two ways this script can waste somebody's money without being wrong about anything:
// starting a multi-gigabyte download on a box with no usable GPU, and starting one for a
// gated model with no HuggingFace credential. Both end in an opaque failure minutes later,
// on an instance that has been billing the whole time. Both are cheap to catch first.
//
// And the credential itself must never reach a log: this script runs on rented hardware and
// its output is the instance console.

func TestVastOnstartChecksForAGPUBeforeDownloadingAnything(t *testing.T) {
	// NOT a substring search for "gpu": the plan already contains
	// --gpu-memory-utilization, so that passes on a script with no check at all. It has to
	// be the script's own reported field.
	out, _ := runOnstart(t, nil)
	if !regexp.MustCompile(`(?m)^\[roger\] gpu\s+\S`).MatchString(out) {
		t.Errorf("the plan should report what it found for a GPU as its own line:\n%s", out)
	}
}

func TestVastOnstartRefusesAGpulessBoxItWouldHaveToServeOn(t *testing.T) {
	// The realistic case, and the common one on Vast: the container was started without the
	// GPU attached, so nvidia-smi is installed but reports nothing. Emptying PATH instead
	// would only prove the script needs coreutils - it died on awk before reaching the
	// check, which is what the first version of this test actually measured.
	home := t.TempDir()
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "nvidia-smi"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "web", "src", "vast-onstart.sh"))
	cmd.Env = append(envWithoutRoger(), "ROGER_DRY_RUN=1", "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	// NOT a substring search: the STATUS line already says "nvidia-smi not found", so a
	// script with the refusal deleted still prints that and would pass. Assert the refusal
	// itself - a non-zero exit and the message that explains the stop.
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	if code == 0 {
		t.Fatalf("a box with no usable GPU must not proceed to download weights:\n%s", out)
	}
	if !strings.Contains(string(out), "no GPU visible on this box") {
		t.Errorf("the stop must explain itself:\n%s", out)
	}
	if !strings.Contains(string(out), "ROGER_SKIP_GPU_CHECK") {
		t.Errorf("and offer the override for a box that really can serve:\n%s", out)
	}
}

func TestVastOnstartAcceptsAHuggingFaceTokenAndNeverPrintsIt(t *testing.T) {
	const secret = "hf_thisMustNeverAppearInAnyLog"
	out, code := runOnstart(t, map[string]string{"ROGER_HF_TOKEN": secret})
	if code != 0 {
		t.Fatalf("a token should not break the run:\n%s", out)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("the HuggingFace token was printed to the console:\n%s", out)
	}
	// but the operator still needs to know it was picked up
	if !strings.Contains(strings.ToLower(out), "hugging") && !strings.Contains(out, "HF token") {
		t.Errorf("it should say a token was accepted, without showing it:\n%s", out)
	}
}

func TestVastOnstartSaysWhenNoTokenIsSet(t *testing.T) {
	// Silence here reads as "a token is not needed", which is wrong for a gated model.
	out, _ := runOnstart(t, map[string]string{"ROGER_MODEL": "meta-llama/Llama-3.1-8B-Instruct"})
	low := strings.ToLower(out)
	if !strings.Contains(low, "gated") && !strings.Contains(low, "no hugging") {
		t.Errorf("with no token set, the plan should mention gated models:\n%s", out)
	}
}
