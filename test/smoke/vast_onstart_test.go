package smoke

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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

// freePort asks the kernel for a port, then releases it: close enough for "nothing is
// listening here", and it cannot collide with whatever this machine happens to run.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

// stubGPU puts an nvidia-smi on PATH that reports one card, or none. Without it these tests
// pass or fail on whether the HOST has a GPU: this box has four, the coverage runner has
// none, so the dry-run cases were green here and red there. The script's GPU check is
// deliberate and fires in dry run too, so a test about anything else has to fix the answer.
func stubGPU(t *testing.T, ok bool) string {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\nexit 1\n"
	if ok {
		body = "#!/bin/sh\necho 'NVIDIA Test Card'\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "nvidia-smi"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func runOnstart(t *testing.T, env map[string]string) (string, int) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command("bash", filepath.Join("..", "..", "web", "src", "vast-onstart.sh"))
	cmd.Env = append(envWithoutRoger(),
		"ROGER_DRY_RUN=1",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"PATH="+stubGPU(t, true),
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
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "ROGER_PRICE_OUT=0.30",
		"PATH="+stubGPU(t, true))
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
	cmd := exec.Command("bash", filepath.Join("..", "..", "web", "src", "vast-onstart.sh"))
	// A port nothing is on. The script skips the GPU check when something already serves
	// the upstream - correct behaviour, but it would silently turn this test into a no-op
	// on a machine running anything on the default 8000.
	cmd.Env = append(envWithoutRoger(), "ROGER_DRY_RUN=1", "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"ROGER_PORT="+strconv.Itoa(freePort(t)),
		"PATH="+stubGPU(t, false))
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
	if !strings.Contains(string(out), "no GPU visible on this box") &&
		!strings.Contains(string(out), "would not proceed: no GPU visible") {
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

func TestVastOnstartDoesNotExportTheTokenProcessWide(t *testing.T) {
	// The credential is for the model server and nothing else. `export` would also hand it
	// to the `exec roger share` this script ends with, which has no use for it - and the
	// comment there once claimed the opposite, which is the wrong thing to be wrong about.
	// It is scoped to the one command via env(1); this fails if it goes back to an export.
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "vast-onstart.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`export\s+HF_TOKEN|export\s+HUGGING_FACE_HUB_TOKEN`).Match(src) {
		t.Error("the HuggingFace token must not be exported process-wide, only scoped to the server")
	}
	if !regexp.MustCompile(`env \$\{HF_TOKEN_IN:\+`).Match(src) {
		t.Error("expected the token to be scoped onto the server command with env(1)")
	}
}

func TestVastOnstartDropsTheTokenBeforeHandingOverToTheClient(t *testing.T) {
	// Scoping the vLLM launch was only half of it: a token the OPERATOR exported in the
	// instance environment - which is how Vast expects you to pass one - would still be
	// inherited by the `exec roger share` at the end. The client never needs it.
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "vast-onstart.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	unset := regexp.MustCompile(`(?m)^unset .*HF_TOKEN.*HUGGING_FACE_HUB_TOKEN.*$`).FindStringIndex(body)
	if unset == nil {
		t.Fatal("the token variables must be unset before the client is exec'd")
	}
	execAt := regexp.MustCompile(`(?m)^exec roger `).FindStringIndex(body)
	if execAt == nil {
		t.Fatal("expected the script to exec the client")
	}
	if unset[0] > execAt[0] {
		t.Error("the unset must come BEFORE the exec, or it never runs")
	}
}
