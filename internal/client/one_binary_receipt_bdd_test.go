package client

// Executable spec: features/packaging/one_binary.feature section 7.7, one local-receipt helper.
// The market path (a standalone Tower as the broker setting) and the LAN ladder both record a
// free answer through localTowerReceipt, and the two receipts agree on every field but LAN.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"rogerai.fm/roger/v7/internal/bddrun"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/protocol"
)

func TestOneLocalReceiptHelperBDD(t *testing.T) {
	var market, lan protocol.UsageReceipt
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				market, lan = protocol.UsageReceipt{}, protocol.UsageReceipt{}
				return ctx, nil
			})
			sc.Step(`^a free answer from a standalone Tower, through the market path or the LAN ladder$`, func() error {
				// The market path names the Tower by the broker setting (a URL); the LAN ladder by
				// its lan_towers entry (host:port). Both are the same Tower.
				market = localTowerReceipt("http://192.168.1.10:8787", "qwen3-8b", false)
				lan = localTowerReceipt("192.168.1.10:8787", "qwen3-8b", true)
				return nil
			})
			sc.Step(`^the receipt is written by client\.localTowerReceipt$`, func() error {
				for _, f := range []string{"client.go", "lan_relay.go"} {
					src, err := os.ReadFile(f)
					if err != nil {
						return err
					}
					if regexp.MustCompile(`protocol\.UsageReceipt\{[^}]*Local: *true`).Match(src) {
						return fmt.Errorf("%s still builds a local receipt by hand", f)
					}
					if !regexp.MustCompile(`localTowerReceipt\(`).Match(src) {
						return fmt.Errorf("%s does not record its free local answer through localTowerReceipt", f)
					}
				}
				return nil
			})
			sc.Step(`^both paths produce the same fields, with LAN set only on the LAN ladder$`, func() error {
				if market.NodeID != lan.NodeID || market.Model != lan.Model || !market.Local || !lan.Local {
					return fmt.Errorf("market %+v and LAN %+v disagree", market, lan)
				}
				if market.LAN || !lan.LAN {
					return fmt.Errorf("LAN is set on the market path (%v) or missing on the ladder (%v)", market.LAN, lan.LAN)
				}
				if market.RequestID == "" || lan.RequestID == "" || market.TS == 0 {
					return fmt.Errorf("a receipt lacks its request id or time: %+v %+v", market, lan)
				}
				return nil
			})
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/packaging/one_binary.feature:558"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("one_binary.feature section 7.7 failed")
	}
}
