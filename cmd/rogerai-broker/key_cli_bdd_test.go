package main

// key_cli_bdd_test.go: the CLI scenarios of features/auth/key_guardrails.feature run the REAL
// `roger` binary (go build ../rogerai, once per test run) as a subprocess against this
// scenario's broker, served over HTTP by httptest with a transparent recorder in front of
// routes(). The recorder logs every request the CLI or its local proxy sends, which is how "via
// the same endpoint" and "authenticates every relay with the key bearer" are observed. The CLI
// gets a temp config dir (XDG_CONFIG_HOME/HOME) holding the device key the scenario names, so
// "acct-a runs ..." signs as acct-a's logged-in device.
//
// OBSERVATION CHOICES (stated, so GREEN knows what the steps read):
//   - Subprocess stdout is a pipe, never a TTY. The outline's mint row ("the secret ONCE, then
//     the warning") is therefore observed as: the secret exactly once on stdout and the warning
//     on stderr; the TTY ordering (both on stdout, secret first) is pinned in-process by
//     cmd/rogerai's TestKeysMintTTYOrder.
//   - "key_x" in a command is a key this account minted first; the step substitutes its id.

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
)

var (
	kg5RogerOnce sync.Once
	kg5RogerBin  string
	kg5RogerErr  error
)

func kg5BuildRoger() (string, error) {
	kg5RogerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "kg5-roger-")
		if err != nil {
			kg5RogerErr = err
			return
		}
		kg5RogerBin = filepath.Join(dir, "roger")
		cmd := exec.Command("go", "build", "-o", kg5RogerBin, "../rogerai")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			kg5RogerErr = fmt.Errorf("build roger: %v\n%s", err, out)
		}
	})
	return kg5RogerBin, kg5RogerErr
}

type kg5Hop struct {
	method, path string
	header       http.Header
	body         []byte
}

// kg5CLI is one scenario's CLI harness: the broker over HTTP, the recorder, the config dir.
type kg5CLI struct {
	srv    *httptest.Server
	dir    string
	mu     sync.Mutex
	hops   []kg5Hop
	stdout string
	stderr string
	code   int
	keyID  string // the id "key_x" stands for
	secret string // the key secret a `--key` run carries

	webCookie string // the signed web session the keys page sends (key_web_bdd_test.go)
	webAcct   string
}

func (c *kg5CLI) recorded() []kg5Hop {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]kg5Hop(nil), c.hops...)
}

func (k *kg5State) cliHarness() *kg5CLI {
	if k.cli != nil {
		return k.cli
	}
	c := &kg5CLI{}
	routes := k.b.routes()
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		c.mu.Lock()
		c.hops = append(c.hops, kg5Hop{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		c.mu.Unlock()
		routes.ServeHTTP(w, r)
	}))
	c.dir, _ = os.MkdirTemp("", "kg5-cli-")
	k.cli = c
	return c
}

func (k *kg5State) cliTeardown() {
	c := k.cli
	if c == nil {
		return
	}
	c.srv.Close()
	_ = os.RemoveAll(c.dir)
	k.cli = nil
}

// cliKeyFor writes the device key the CLI signs with: an account's logged-in device key, or a
// key that never logged in ("unbound").
func (k *kg5State) cliKeyFor(who string) error {
	c := k.cliHarness()
	var priv []byte
	if who == "unbound" {
		if _, err := k.authFor("unbound"); err != nil {
			return err
		}
		priv = k.unbound
	} else {
		a, err := k.acct(who)
		if err != nil {
			return err
		}
		priv = a.who.priv
	}
	path := filepath.Join(c.dir, "rogerai", "user.key")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0o600)
}

func (k *kg5State) cliEnv() []string {
	c := k.cliHarness()
	return append(os.Environ(), "XDG_CONFIG_HOME="+c.dir, "HOME="+c.dir, "ROGER_BROKER="+c.srv.URL, "NO_COLOR=1", "ROGERAI_NO_UPDATE_CHECK=1")
}

// cliRun runs `roger args...` to completion.
func (k *kg5State) cliRun(args ...string) error {
	bin, err := kg5BuildRoger()
	if err != nil {
		return err
	}
	c := k.cliHarness()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = k.cliEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	c.mu.Lock()
	c.hops = nil
	c.mu.Unlock()
	runErr := cmd.Run()
	c.stdout, c.stderr, c.code = out.String(), errb.String(), 0
	if ee, ok := runErr.(*exec.ExitError); ok {
		c.code = ee.ExitCode()
	} else if runErr != nil {
		return runErr
	}
	return nil
}

func (k *kg5State) registerCLI(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		k.cliTeardown()
		return ctx, nil
	})
	sc.Step("^\"([^\"]+)\" runs `([^`]+)`$", k.cliRuns)
	sc.Step(`^it performs (.+) via the same endpoint and prints (.+)$`, k.cliPerformsAndPrints)
	sc.Step("^`roger keys mint` runs with stdout not a TTY$", func() error { return k.cliRuns("acct-a", "roger keys mint") })
	sc.Step(`^it prints only the secret \(machine-readable\) and the warning goes to stderr$`, k.cliOnlySecret)
	sc.Step(`^the device keypair never logged in$`, func() error { return k.cliKeyFor("unbound") })
	sc.Step("^`roger keys list` runs$", func() error { return k.cliRunAs("", "roger keys list") })
	sc.Step(`^it exits non-zero with "(.+)"$`, k.cliExitsWith)
	sc.Step("^`roger use qwen3-32b --key rog-key_\\.\\.\\.` runs$", k.cliUseKey)
	sc.Step(`^the local proxy authenticates every relay with the key bearer instead of the device signature$`, k.cliRelaysBearKey)
	sc.Step("^the key is never written to the config file unless `--save-key` is passed$", k.cliKeyNotSaved)
}

// cliRuns runs a `roger keys` command as acct's logged-in device. "key_x" names a key the
// account minted first; a list runs over two keys so the table has rows.
func (k *kg5State) cliRuns(acct, command string) error {
	if err := k.cliKeyFor(acct); err != nil {
		return err
	}
	return k.cliRunAs(acct, command)
}

func (k *kg5State) cliRunAs(acct, command string) error {
	c := k.cliHarness()
	if strings.Contains(command, "key_x") {
		if err := k.mintWith(acct, "key_x", `limit_usd 5, reset "weekly"`); err != nil {
			return err
		}
		c.keyID = k.keyID("key_x")
		command = strings.ReplaceAll(command, "key_x", c.keyID)
	}
	if acct != "" && strings.HasPrefix(command, "roger keys list") {
		for _, l := range []string{"k-list-1", "k-list-2"} {
			if err := k.mintWith(acct, l, `limit_usd 5, reset "weekly"`); err != nil {
				return err
			}
		}
	}
	args := strings.Fields(command)
	if len(args) == 0 || args[0] != "roger" {
		return fmt.Errorf("not a roger command: %q", command)
	}
	return k.cliRun(args[1:]...)
}

var kg5SecretRe = regexp.MustCompile(`rog-key_[A-Za-z0-9_-]+`)

func (k *kg5State) cliPerformsAndPrints(effect, output string) error {
	c := k.cli
	if c == nil {
		return fmt.Errorf("no roger run in this scenario")
	}
	if c.code != 0 {
		return fmt.Errorf("roger exited %d\nstdout:\n%s\nstderr:\n%s", c.code, c.stdout, c.stderr)
	}
	effect = strings.ReplaceAll(effect, "key_x", c.keyID)
	method, path, _ := strings.Cut(effect, " ")
	wantBody := ""
	if path == "disabled true" { // "PATCH disabled true": the PATCH carries {"disabled":true}
		path, wantBody = "/account/keys/"+c.keyID, `"disabled":true`
	}
	var hit *kg5Hop
	for _, h := range c.recorded() {
		h := h
		if h.method == method && h.path == path && (wantBody == "" || bytes.Contains(h.body, []byte(wantBody))) {
			hit = &h
		}
	}
	if hit == nil {
		return fmt.Errorf("roger never sent %s %s %s; it sent %v", method, path, wantBody, kg5HopLines(c.recorded()))
	}
	if hit.header.Get("X-Roger-Sig") == "" {
		return fmt.Errorf("%s %s was not signed by the device key", method, path)
	}
	if strings.Contains(effect, "PATCH /account/keys/") {
		for _, frag := range []string{`"limit_usd":10`, `"allowed_models":["qwen3-32b","llama-3.3-70b"]`} {
			if !bytes.Contains(hit.body, []byte(frag)) {
				return fmt.Errorf("the PATCH body %s does not carry %s", hit.body, frag)
			}
		}
	}
	switch {
	case strings.HasPrefix(output, "a mono table"):
		return k.cliTable(c.stdout, []string{"k-list-1", "k-list-2"})
	case strings.HasPrefix(output, "the secret ONCE"):
		secrets := kg5SecretRe.FindAllString(c.stdout+c.stderr, -1)
		if len(secrets) != 1 {
			return fmt.Errorf("the secret appears %d times in the output, want exactly once:\n%s\n%s", len(secrets), c.stdout, c.stderr)
		}
		if !strings.Contains(c.stdout+c.stderr, "store it now - it is not shown again") {
			return fmt.Errorf("no \"store it now - it is not shown again\" warning:\n%s\n%s", c.stdout, c.stderr)
		}
		return nil
	case output == "the updated row":
		if err := k.cliTable(c.stdout, nil); err != nil {
			return err
		}
		row := kg5RowOf(c.stdout, c.keyID)
		if row == "" {
			return fmt.Errorf("the updated row for %s is not printed:\n%s", c.keyID, c.stdout)
		}
		if strings.Contains(effect, "disabled") && !strings.Contains(row, "disabled") {
			return fmt.Errorf("the updated row does not show the key disabled: %q", row)
		}
		if strings.Contains(effect, "/account/keys/") && !strings.Contains(row, "10.00") {
			return fmt.Errorf("the updated row does not show the new $10 limit: %q", row)
		}
		return nil
	case strings.HasPrefix(output, `"revoked `):
		want := strings.Trim(strings.ReplaceAll(output, "key_x", c.keyID), `"`)
		if !strings.Contains(c.stdout, want) {
			return fmt.Errorf("stdout does not say %q:\n%s", want, c.stdout)
		}
		return nil
	}
	return fmt.Errorf("unknown output expectation %q", output)
}

// cliTable checks the mono key table: a header naming id, name, hint, limit, used, resets,
// expires and state in that order, and a row per named key.
func (k *kg5State) cliTable(out string, labels []string) error {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var header string
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l), "hint") {
			header = strings.ToLower(l)
			break
		}
	}
	if header == "" {
		return fmt.Errorf("no table header in:\n%s", out)
	}
	at := -1
	for _, col := range []string{"id", "name", "hint", "limit", "used", "resets", "expires", "state"} {
		i := strings.Index(header[at+1:], col)
		if i < 0 {
			return fmt.Errorf("the header %q lacks %q (or has it out of order)", header, col)
		}
		at += 1 + i
	}
	for _, l := range labels {
		if kg5RowOf(out, k.keyID(l)) == "" {
			return fmt.Errorf("no row for %s (%s):\n%s", l, k.keyID(l), out)
		}
	}
	return nil
}

func kg5RowOf(out, id string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), id) {
			return l
		}
	}
	return ""
}

func kg5HopLines(hops []kg5Hop) []string {
	out := make([]string, 0, len(hops))
	for _, h := range hops {
		out = append(out, h.method+" "+h.path)
	}
	return out
}

func (k *kg5State) cliOnlySecret() error {
	c := k.cli
	if c.code != 0 {
		return fmt.Errorf("roger exited %d: %s", c.code, c.stderr)
	}
	if !regexp.MustCompile(`^rog-key_[A-Za-z0-9_-]+\n?$`).MatchString(c.stdout) {
		return fmt.Errorf("stdout is not only the secret: %q", c.stdout)
	}
	if !strings.Contains(c.stderr, "store it now - it is not shown again") {
		return fmt.Errorf("the warning is not on stderr: %q", c.stderr)
	}
	return nil
}

func (k *kg5State) cliExitsWith(msg string) error {
	c := k.cli
	if c.code == 0 {
		return fmt.Errorf("roger exited 0, want non-zero\nstdout:\n%s", c.stdout)
	}
	if !strings.Contains(c.stdout+c.stderr, msg) {
		return fmt.Errorf("the output does not say %q:\n%s\n%s", msg, c.stdout, c.stderr)
	}
	return nil
}

// cliUseKey starts `roger use qwen3-32b --key <a real key of acct-a>` and sends one relay
// through its local proxy.
func (k *kg5State) cliUseKey() error {
	if err := k.cliKeyFor("acct-a"); err != nil {
		return err
	}
	if err := k.mintWith("acct-a", "k-use", "defaults"); err != nil {
		return err
	}
	secret, err := k.secretOf("k-use")
	if err != nil {
		return err
	}
	k.ensurePriced()
	c := k.cliHarness()
	c.secret = secret
	return k.cliUseRelay(secret, false)
}

// cliUseRelay runs `roger use` (optionally with --save-key) in the background, waits for the
// channel plate, sends one relay through the local proxy, then stops it.
func (k *kg5State) cliUseRelay(secret string, save bool) error {
	bin, err := kg5BuildRoger()
	if err != nil {
		return err
	}
	c := k.cliHarness()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	args := []string{"use", "qwen3-32b", "--key", secret, "--yes", "--port", fmt.Sprint(port)}
	if save {
		args = append(args, "--save-key")
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = k.cliEnv()
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = pw.Close() // the reader sees EOF when roger exits early
		close(exited)
	}()
	defer func() {
		_ = cmd.Process.Kill()
		<-exited
	}()
	plate := make(chan string, 1)
	go func() {
		var seen strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := pr.Read(buf)
			seen.Write(buf[:n])
			if m := regexp.MustCompile(`OPENAI_API_KEY=(\S+)`).FindStringSubmatch(seen.String()); m != nil {
				plate <- m[1]
				_, _ = io.Copy(io.Discard, pr)
				return
			}
			if rerr != nil {
				plate <- "!" + seen.String()
				return
			}
		}
	}()
	var session string
	select {
	case session = <-plate:
	case <-time.After(30 * time.Second):
		return fmt.Errorf("roger use never opened the channel")
	}
	if strings.HasPrefix(session, "!") {
		return fmt.Errorf("roger use exited before the channel opened:\n%s", session[1:])
	}
	c.mu.Lock()
	c.hops = nil
	c.mu.Unlock()
	body := []byte(`{"model":"qwen3-32b","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+session)
	req.Header.Set("Content-Type", "application/json")
	var resp *http.Response
	for i := 0; i < 50; i++ {
		if resp, err = http.DefaultClient.Do(req); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	if err != nil {
		return fmt.Errorf("the local proxy never answered: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

func (k *kg5State) cliRelaysBearKey() error {
	c := k.cli
	relays := 0
	for _, h := range c.recorded() {
		if h.path != "/v1/chat/completions" {
			continue
		}
		relays++
		if got := h.header.Get("Authorization"); got != "Bearer "+c.secret {
			return fmt.Errorf("a relay carried Authorization %q, want the key bearer", got)
		}
		for _, sig := range []string{"X-Roger-Sig", "X-Roger-Pubkey"} {
			if h.header.Get(sig) != "" {
				return fmt.Errorf("a relay still carried the device signature header %s", sig)
			}
		}
	}
	if relays == 0 {
		return fmt.Errorf("no relay reached the broker; it saw %v", kg5HopLines(c.recorded()))
	}
	return nil
}

func (k *kg5State) cliKeyNotSaved() error {
	c := k.cli
	leaked := func() (string, error) {
		var hit string
		err := filepath.Walk(c.dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil && bytes.Contains(b, []byte(c.secret)) {
				hit = p
			}
			return nil
		})
		return hit, err
	}
	if p, err := leaked(); err != nil || p != "" {
		return fmt.Errorf("the key was written to %s without --save-key (%v)", p, err)
	}
	if err := k.cliUseRelay(c.secret, true); err != nil {
		return err
	}
	cfg, err := os.ReadFile(filepath.Join(c.dir, "rogerai", "config.json"))
	if err != nil || !bytes.Contains(cfg, []byte(c.secret)) {
		return fmt.Errorf("--save-key did not write the key to config.json (%v): %s", err, cfg)
	}
	return nil
}
