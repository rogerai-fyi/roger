package lanmode_test

// Executable spec: features/packaging/one_binary.feature section 7.6, one nonce decision. The
// roger signer (client) is driven for real; the share station's signer is pinned by the agent
// package's own test of the same table, and the structural scenario proves both signers ask
// lanmode.WantsNonce.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/protocol"
)

// towerURL is the request a signer sends to a Tower at addr: plain http to a LAN address, https
// to a public broker on 443.
func towerURL(addr string) string {
	if strings.HasSuffix(addr, ":443") {
		return "https://" + addr + "/v1/chat/completions"
	}
	if strings.Contains(addr, "%") { // a zoned IPv6 literal: the zone is percent-encoded in a URL
		addr = strings.Replace(addr, "%", "%25", 1)
	}
	return "http://" + addr + "/local/poll"
}

func TestOneNonceDecisionBDD(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	var req *http.Request
	sign := func(addr string) error {
		// A Tower at addr is one roger knows: the owner added it (`roger lan add`).
		client.SetLANTowerHosts(func() []string { return []string{addr} })
		r, err := http.NewRequest(http.MethodPost, towerURL(addr), nil)
		if err != nil {
			return err
		}
		client.SignRequestWith(r, nil, priv)
		if !lanmode.WantsNonce(r.URL, []string{addr}) != (r.Header.Get(protocol.HeaderNonce) == "") {
			return fmt.Errorf("the client signer and lanmode.WantsNonce disagree for %s", addr)
		}
		req = r
		return nil
	}
	carries := func(want string) error {
		got := "no"
		if req.Header.Get(protocol.HeaderNonce) != "" {
			got = "yes"
		}
		if got != want {
			return fmt.Errorf("%s: nonce %s, want %s", req.URL, got, want)
		}
		return nil
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				client.SetLANTowerHosts(nil)
				return ctx, nil
			})
			sc.Step(`^roger or a share station signs a request to a Tower at "([^"]*)"$`, sign)
			sc.Step(`^the request carries a signed X-Roger-Nonce: "([^"]*)"$`, carries)
			sc.Step(`^roger signs a request to 127\.0\.0\.1:8787$`, func() error { return sign("127.0.0.1:8787") })
			sc.Step(`^it carries a nonce$`, func() error { return carries("yes") })
			sc.Step(`^client\.signWithKey and agent\.signLocal both ask the same function whether to add a nonce$`, func() error {
				out, err := exec.Command("go", "env", "GOMOD").Output()
				if err != nil {
					return err
				}
				root := filepath.Dir(strings.TrimSpace(string(out)))
				for file, fn := range map[string]string{
					"internal/client/identity.go":  "signWithKey",
					"internal/agent/localtower.go": "signLocal",
				} {
					src, err := os.ReadFile(filepath.Join(root, file))
					if err != nil {
						return err
					}
					body := regexp.MustCompile(`(?s)func `+fn+`\(.*?\n}\n`).FindString(string(src))
					if !strings.Contains(body, "lanmode.WantsNonce(") {
						return fmt.Errorf("%s in %s does not ask lanmode.WantsNonce", fn, file)
					}
				}
				return nil
			})
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:530",
				"../../features/packaging/one_binary.feature:544",
				"../../features/packaging/one_binary.feature:547",
			},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("one_binary.feature section 7.6 failed")
	}
}
