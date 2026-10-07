package towercli

// Executable spec: features/packaging/one_binary.feature section 7.1, the joined serve's
// advertised-address tiers. The joined serve and `roger lan add` classify a link-local
// address with the one rule (internal/netpriv), so they land in the same class.

import (
	"context"
	"fmt"
	"net"
	"rogerai.fm/roger/v6/internal/bddrun"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/lanmode"
)

func TestOneBinaryJoinedServeBDD(t *testing.T) {
	var note string
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { note = ""; return ctx, nil })
			sc.Step(`^joined serve classifies an advertised fe80:: address$`, func() error {
				note = classifyAdvertised("fe80::1", []net.IP{net.ParseIP("fe80::1")}, false)
				return nil
			})
			sc.Step(`^it uses the same class as lan add$`, func() error {
				v := lanmode.ClassifyLiteral("[fe80::1%eth0]:8787")
				joinedLocal := strings.Contains(note, "LOCAL network address")
				if !v.Local || !joinedLocal {
					return fmt.Errorf("lan add local=%v, joined serve note %q: the two disagree on link-local", v.Local, note)
				}
				return nil
			})
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/packaging/one_binary.feature:437"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("one_binary.feature: the joined serve's advertised tiers failed")
	}
}
