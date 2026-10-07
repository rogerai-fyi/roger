package webui

// Executable spec: features/lan/edge_only.feature:217 (@console). The REAL console Server answers
// /api/state for a controller in LAN mode, edge-only, and the SHIPPED console-lan.js is EXECUTED
// with node over that snapshot (see lan_console_bdd_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
)

func TestLANEdgeOnlyConsoleBDD(t *testing.T) {
	s := &lanConsoleBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Step(`^"roger --edge-only webui" is running$`, func() error {
				c := testCtrl()
				c.SetLAN(func() []string { return nil })
				c.SetLANEdgeOnly(true)
				srv := New(c, Options{})
				hs := httptest.NewServer(srv.Handler())
				t.Cleanup(hs.Close)
				resp, err := http.Get(hs.URL + "/api/state?t=" + srv.Token())
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				s.snapshot = string(b)
				return nil
			})
			sc.Step(`^the console loads$`, s.renders)
			sc.Step(`^its top bar shows "([^"]*)"$`, s.topBarHas)
			sc.Step(`^the state snapshot carries "lan": true and "edge_only": true$`, func() error {
				var snap map[string]any
				if err := json.Unmarshal([]byte(s.snapshot), &snap); err != nil {
					return err
				}
				if snap["lan"] != true || snap["edge_only"] != true {
					return fmt.Errorf("the snapshot carries lan=%v edge_only=%v", snap["lan"], snap["edge_only"])
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/lan/edge_only.feature:217"}},
	}
	_ = context.Background
	if bddrun.Run(suite) != 0 {
		t.Fatal("edge_only.feature (console) failed")
	}
}
