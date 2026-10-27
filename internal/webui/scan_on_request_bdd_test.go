package webui

// Executable spec: features/edge/scan_on_request.feature (@console) - every read of the console's
// Edge view renews the on-request scan lease. The REAL console Server answers the read over
// httptest with the real token; the host side (what the lease allows, and the sockets) is pinned
// in cmd/rogerai/edge_scan_on_request_bdd_test.go.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

type scanConsoleBDD struct {
	t       *testing.T
	srv     *Server
	http    *httptest.Server
	renewed int
	status  int
}

func (s *scanConsoleBDD) reset() {
	if s.http != nil {
		s.http.Close()
	}
	s.srv, s.http, s.renewed, s.status = nil, nil, 0, 0
}

func (s *scanConsoleBDD) wired() error {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	s.srv = New(testCtrl(), Options{Edge: EdgeHooks{Fleet: fleet, ScanLease: func() { s.renewed++ }}})
	s.http = httptest.NewServer(s.srv.Handler())
	return nil
}

func (s *scanConsoleBDD) read() error {
	res, err := http.Get(s.http.URL + "/api/edge?t=" + s.srv.Token())
	if err != nil {
		return err
	}
	res.Body.Close()
	s.status = res.StatusCode
	return nil
}

func (s *scanConsoleBDD) leaseRenewed() error {
	if s.status != http.StatusOK {
		return fmt.Errorf("the Edge read answered %d", s.status)
	}
	if s.renewed != 1 {
		return fmt.Errorf("the scan lease was renewed %d time(s), want 1", s.renewed)
	}
	return nil
}

func TestScanOnRequestFeatureConsole(t *testing.T) {
	st := &scanConsoleBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.Given(`^a fresh install that has not joined an Edge and holds no Edge root$`, func() error { return nil })
			sc.Given(`^a console wired to a scan lease$`, st.wired)
			sc.When(`^the Edge data is read$`, st.read)
			sc.Then(`^the scan lease was renewed$`, st.leaseRenewed)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@console",
			Paths: []string{"../../features/edge/scan_on_request.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("scan_on_request.feature (@console) failed")
	}
}
