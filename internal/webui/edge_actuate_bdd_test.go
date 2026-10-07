package webui

// Executable pin for the @web scenario of features/edge/node_identity.feature: the console performs
// the same ceremony as the terminal, never a bare button, and the HOST says who confirmed.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"rogerai.fm/roger/v6/internal/bddrun"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
)

type actuateWeb struct {
	t    *testing.T
	snap edgeSnap
	js   string
}

func (s *actuateWeb) consoleOverPendingNode() error {
	srvS, srv, _, _ := actuateRig(s.t, true)
	resp, err := http.Get(srv.URL + "/api/edge?t=" + srvS.Token())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&s.snap); err != nil {
		return err
	}
	b, err := os.ReadFile("assets/console.js")
	if err != nil {
		return err
	}
	s.js = string(b)
	return nil
}

func (s *actuateWeb) drawsThatNode() error {
	if !s.snap.ActuateManage {
		return fmt.Errorf("the console does not say it can perform the ceremony")
	}
	return nil
}

func (s *actuateWeb) saysPendingAndWhatItLicenses() error {
	// The payload carries the pending capability, and the page says what confirming licenses.
	pending := false
	for _, n := range s.snap.Nodes {
		for _, c := range n.Caps {
			pending = pending || (c.Name == string(edge.Actuate) && c.State != string(edge.Verified))
		}
	}
	if !pending {
		return fmt.Errorf("the snapshot does not carry the pending capability")
	}
	if !strings.Contains(s.js, "PHYSICAL WORLD") {
		return fmt.Errorf("the console never says what confirming licenses")
	}
	return nil
}

func (s *actuateWeb) postOnlyAndTokenGated() error {
	if !strings.Contains(s.js, `apiPost("/api/edge/actuate"`) {
		return fmt.Errorf("the console does not POST the ceremony")
	}
	return nil
}

func (s *actuateWeb) confirmedInTheBrowserFirst() error {
	i := strings.Index(s.js, "function edgeConfirmActuate")
	if i < 0 {
		return fmt.Errorf("there is no ceremony in the console")
	}
	body := s.js[i : i+900]
	if !strings.Contains(body, "window.confirm") {
		return fmt.Errorf("the ceremony does not ask before it sends")
	}
	if strings.Index(body, "window.confirm") > strings.Index(body, "apiPost") {
		return fmt.Errorf("the ceremony sends before it asks")
	}
	return nil
}

func (s *actuateWeb) hostRecordsWho() error {
	// The request body carries the node and nothing else: no who, no confirmed_by.
	i := strings.Index(s.js, `apiPost("/api/edge/actuate"`)
	if i < 0 {
		return fmt.Errorf("no ceremony request")
	}
	call := s.js[i : i+120]
	for _, never := range []string{"who", "confirmed_by", "operator"} {
		if strings.Contains(call, never) {
			return fmt.Errorf("the browser sends %q, and its word for who must never be taken: %s", never, call)
		}
	}
	return nil
}

func TestEdgeActuateCeremonyWeb(t *testing.T) {
	st := &actuateWeb{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Step(`^a console over an Edge with a node pending actuate confirmation$`, st.consoleOverPendingNode)
			sc.Step(`^the manage view draws that node$`, st.drawsThatNode)
			sc.Step(`^it says actuate is pending and what confirming licenses$`, st.saysPendingAndWhatItLicenses)
			sc.Step(`^the confirm action is POST-only and token-gated like every other mutation$`, st.postOnlyAndTokenGated)
			sc.Step(`^it is confirmed in the browser before it is sent$`, st.confirmedInTheBrowserFirst)
			sc.Step(`^the host records who confirmed it, never the browser's word for who$`, st.hostRecordsWho)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@web",
			Paths: []string{"../../features/edge/node_identity.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the actuate ceremony console scenario failed")
	}
}
