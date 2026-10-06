package main

// key_web_bdd_test.go: the web scenarios of features/auth/key_guardrails.feature. The keys page's
// account-keys module (web/src/js/account-keys.js) is EXECUTED by node (web/test/support/
// account-keys-probe.mjs) against this scenario's broker over HTTP, with a real signed web-session
// cookie and an allowlisted Origin, exactly as the browser sends them. The broker side is the
// recorder of key_cli_bdd_test.go, so what the page sent is observed twice: by the probe, which
// records the module's own requests, and by the broker, which records what arrived.
//
// OBSERVATION CHOICES: "the section shows the same entries" compares the module's rendered rows
// with GET /account/keys for the same session; the design-system scenario runs the web suite's
// own design, contrast and phone-width checks plus the account-keys module tests (node:test).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

const kg5WebDir = "../../web"

func (k *kg5State) registerWeb(sc *godog.ScenarioContext) {
	sc.Step(`^"([^"]+)" is on /keys\.html with a web session$`, k.webSession)
	sc.Step(`^the "Account keys" section shows the same entries as GET /account/keys$`, k.webSameEntries)
	sc.Step(`^minting shows the secret once with a copy control and a warning that it is never shown again$`, k.webMintOnce)
	sc.Step(`^the page sends body JSON, never the secret in a URL$`, k.webBodyJSON)
	sc.Step(`^the account keys section uses the existing keys-page type scale, one red, no grid, no glow, no pinned bar, and passes phone width and dark-mode contrast$`, k.webDesignSystem)
}

// webSession signs acct in on the web (the session cookie the login flow sets) and mints two
// keys so the section has entries to show.
func (k *kg5State) webSession(acct string) error {
	a, err := k.acct(acct)
	if err != nil {
		return err
	}
	for _, l := range []string{"web-1", "web-2"} {
		if err := k.mintWith(acct, l, `limit_usd 5, reset "weekly"`); err != nil {
			return err
		}
	}
	c := k.cliHarness()
	c.webCookie = sessionCookie + "=" + k.b.signSessionWallet(a.who.login, a.gid, a.who.wallet, time.Now().Add(time.Hour).Unix())
	c.webAcct = acct
	return nil
}

// webProbe runs the account-keys module in node for one action and decodes what it printed.
func (k *kg5State) webProbe(action string) (map[string]any, error) {
	c := k.cli
	if c == nil || c.webCookie == "" {
		return nil, fmt.Errorf("no web session in this scenario")
	}
	cmd := exec.Command("node", filepath.Join(kg5WebDir, "test", "support", "account-keys-probe.mjs"), action)
	cmd.Env = append(os.Environ(), "BROKER="+c.srv.URL, "COOKIE="+c.webCookie, "ORIGIN="+pbOrigin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	c.mu.Lock()
	c.hops = nil
	c.mu.Unlock()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("the account-keys probe (%s) failed: %v\n%s%s", action, err, out.String(), errb.String())
	}
	var res map[string]any
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("the probe printed %q: %v", out.String(), err)
	}
	return res, nil
}

func (k *kg5State) webSameEntries() error {
	res, err := k.webProbe("list")
	if err != nil {
		return err
	}
	api, err := k.call(kg5Req{as: "cookie:" + k.cli.webAcct, method: http.MethodGet, path: "/account/keys"})
	if err != nil {
		return err
	}
	if api.code != http.StatusOK {
		return fmt.Errorf("GET /account/keys = %d: %s", api.code, api.body)
	}
	var want []string
	keys, _ := api.js["keys"].([]any)
	for _, e := range keys {
		m, _ := e.(map[string]any)
		want = append(want, fmt.Sprint(m["id"]))
	}
	got := fmt.Sprint(res["ids"])
	if got != fmt.Sprint(want) {
		return fmt.Errorf("the section shows %s, GET /account/keys returns %v", got, want)
	}
	html, _ := res["html"].(string)
	for _, e := range keys {
		m, _ := e.(map[string]any)
		for _, f := range []string{"id", "name", "hint"} {
			if !strings.Contains(html, fmt.Sprint(m[f])) {
				return fmt.Errorf("the rendered rows do not show %s %v:\n%s", f, m[f], html)
			}
		}
	}
	return nil
}

func (k *kg5State) webMintOnce() error {
	res, err := k.webProbe("mint")
	if err != nil {
		return err
	}
	secret, _ := res["secret"].(string)
	html, _ := res["html"].(string)
	if !strings.HasPrefix(secret, "rog-key_") {
		return fmt.Errorf("the mint returned no key secret: %v", res)
	}
	if n := strings.Count(html, secret); n != 1 {
		return fmt.Errorf("the reveal shows the secret %d times, want once:\n%s", n, html)
	}
	if !strings.Contains(html, `class="kf__copy"`) {
		return fmt.Errorf("the reveal has no copy control:\n%s", html)
	}
	if !strings.Contains(strings.ToLower(html), "never shown again") {
		return fmt.Errorf("the reveal does not warn that the secret is never shown again:\n%s", html)
	}
	return nil
}

// webBodyJSON runs the page's whole flow (list, mint, edit the limit, disable, revoke) and
// checks every request as the module built it and as the broker received it.
func (k *kg5State) webBodyJSON() error {
	res, err := k.webProbe("flow")
	if err != nil {
		return err
	}
	secret, _ := res["secret"].(string)
	if secret == "" {
		return fmt.Errorf("the flow minted nothing: %v", res)
	}
	calls, _ := res["calls"].([]any)
	if len(calls) < 5 {
		return fmt.Errorf("the flow sent %d requests, want list, mint, two edits and a revoke", len(calls))
	}
	for _, x := range calls {
		m, _ := x.(map[string]any)
		url, _ := m["url"].(string)
		body, _ := m["body"].(string)
		if strings.Contains(url, "rog-key_") {
			return fmt.Errorf("a request URL carries a key secret: %s", url)
		}
		if m["method"] == http.MethodPost || m["method"] == http.MethodPatch {
			if m["contentType"] != "application/json" || !json.Valid([]byte(body)) {
				return fmt.Errorf("%v %s did not send a JSON body (%v %q)", m["method"], url, m["contentType"], body)
			}
		}
	}
	for _, h := range k.cli.recorded() {
		if strings.Contains(h.path, "rog-key_") || strings.Contains(h.header.Get("Referer"), "rog-key_") {
			return fmt.Errorf("the broker received a key secret in a URL: %s %s", h.method, h.path)
		}
		if (h.method == http.MethodPost || h.method == http.MethodPatch) && !strings.HasPrefix(h.header.Get("Content-Type"), "application/json") {
			return fmt.Errorf("the broker received %s %s without a JSON body", h.method, h.path)
		}
	}
	return nil
}

// webDesignSystem runs the web suite's design, contrast and phone-width checks and the
// account-keys module tests.
func (k *kg5State) webDesignSystem() error {
	files := []string{"test/account-keys.test.mjs", "test/design-system.test.mjs", "test/contrast.test.mjs", "test/layout-overflow.test.mjs"}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(kg5WebDir, f)); err != nil {
			return fmt.Errorf("the web check %s is missing (node --test skips a missing file silently): %v", f, err)
		}
	}
	cmd := exec.Command("node", append([]string{"--test", "--test-concurrency=1"}, files...)...)
	cmd.Dir = kg5WebDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("the web design checks failed: %v\n%s", err, out)
	}
	return nil
}
