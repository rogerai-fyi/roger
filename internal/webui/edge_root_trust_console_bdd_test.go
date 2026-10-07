package webui

// Executable spec: features/edge/enrollment_root_trust.feature (@console) - the console's adopt
// carries the one-time enrollment code to the owner's token-gated request only, and the console's
// join refuses a code that does not verify, keeping nothing. The REAL Server over httptest with its
// token; the host closures are the seam cmd/rogerai fills from its real enrollment (and tests
// against real authorities there).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

type rootTrustConsole struct {
	t        *testing.T
	s        *Server
	srv      *httptest.Server
	adopted  bool
	joined   bool
	status   int
	body     string
	code     string
	until    time.Time
	noTokRes int
	noTokBod string
}

func (c *rootTrustConsole) rig() {
	fleet := edge.NewFleet(store.NewMem(), "edge-local")
	cand := store.EdgeNode{ID: "n_pocket", Name: "pocket", Kind: "mobile"}
	c.code, c.until = "7KQ2M-9XH4P", time.Now().Add(15*time.Minute)
	c.s = New(testCtrl(), Options{Edge: EdgeHooks{
		Self:       "shed",
		Fleet:      fleet,
		Candidates: func() []store.EdgeNode { return []store.EdgeNode{cand} },
		Adopt:      func(id, name string) error { c.adopted = true; return nil },
		Adopting: func() []store.EdgeNode {
			if !c.adopted {
				return nil
			}
			return []store.EdgeNode{cand}
		},
		AdoptCode: func(id string) (string, time.Time, bool) {
			if !c.adopted || id != cand.ID {
				return "", time.Time{}, false
			}
			return c.code, c.until, true
		},
		Join: func(name, authority, code string) error {
			if code != c.code {
				return errors.New("the enrollment code does not match the Edge that answered at 192.168.1.10:8791")
			}
			c.joined = true
			return nil
		},
	}})
	c.srv = httptest.NewServer(c.s.Handler())
	c.t.Cleanup(c.srv.Close)
}

func (c *rootTrustConsole) consoleWithPocket() error { c.rig(); return nil }

func (c *rootTrustConsole) adoptsThroughPost() error {
	resp, err := http.Post(c.srv.URL+"/api/edge/adopt?t="+c.s.Token(), "application/json", strings.NewReader(`{"id":"n_pocket"}`))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	c.status, c.body = resp.StatusCode, string(b)
	r2, err := http.Get(c.srv.URL + "/api/edge")
	if err != nil {
		return err
	}
	defer r2.Body.Close()
	b2, _ := io.ReadAll(r2.Body)
	c.noTokRes, c.noTokBod = r2.StatusCode, string(b2)
	return nil
}

func (c *rootTrustConsole) responseCarriesCode() error {
	if c.status != http.StatusOK {
		return fmt.Errorf("adopt answered %d: %s", c.status, c.body)
	}
	var snap struct {
		Adopting []struct {
			ID       string `json:"id"`
			Code     string `json:"code"`
			CodeLeft string `json:"code_left"`
		} `json:"adopting"`
	}
	if err := json.Unmarshal([]byte(c.body), &snap); err != nil {
		return err
	}
	if len(snap.Adopting) != 1 || snap.Adopting[0].Code != c.code || !strings.Contains(snap.Adopting[0].CodeLeft, "min") {
		return fmt.Errorf("the adopt response does not carry the code and how long it is good for: %s", c.body)
	}
	return nil
}

func (c *rootTrustConsole) consoleShowsInRow() error {
	js, err := consoleAsset("console.js")
	if err != nil {
		return err
	}
	if !strings.Contains(js, "a.code") || !strings.Contains(js, "code_left") {
		return fmt.Errorf("the console's adopting row does not draw the code")
	}
	return nil
}

func (c *rootTrustConsole) noTokenNoCode() error {
	if c.noTokRes != http.StatusForbidden || strings.Contains(c.noTokBod, c.code) {
		return fmt.Errorf("a read without the token answered %d and carried %q", c.noTokRes, c.noTokBod)
	}
	return nil
}

func (c *rootTrustConsole) consoleNotEnrolled() error { c.rig(); return nil }

func (c *rootTrustConsole) joinsWithBadCode() error {
	resp, err := http.Post(c.srv.URL+"/api/edge/join?t="+c.s.Token(), "application/json",
		strings.NewReader(`{"name":"bench","authority":"http://192.168.1.10:8791","code":"ZZZZZ-ZZZZZ"}`))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	c.status, c.body = resp.StatusCode, string(b)
	return nil
}

func (c *rootTrustConsole) saysNoMatch() error {
	if c.status == http.StatusOK || !strings.Contains(c.body, "does not match the Edge that answered") {
		return fmt.Errorf("the console did not say the code does not match (%d): %s", c.status, c.body)
	}
	return nil
}

func (c *rootTrustConsole) nothingKept() error {
	if c.joined {
		return fmt.Errorf("the console joined on a code that did not verify")
	}
	return nil
}

// consoleAsset reads one embedded console asset.
func consoleAsset(name string) (string, error) {
	b, err := assetsFS.ReadFile("assets/" + name)
	return string(b), err
}

func TestEnrollmentRootTrustFeatureConsole(t *testing.T) {
	st := &rootTrustConsole{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.adopted, st.joined, st.status, st.body = false, false, 0, ""
				return ctx, nil
			})
			// The Background's authority is the host's (cmd/rogerai); here it is the host closures.
			sc.Step(`^a local Edge authority "([^"]*)" whose root this machine has never held$`, func(string) error { return nil })
			sc.Step(`^the authority answers enrollment and claims on its LAN listener$`, func() error { return nil })
			sc.Step(`^a console over the authority with a phone candidate "pocket"$`, st.consoleWithPocket)
			sc.Step(`^the owner adopts "pocket" through the console's POST, with the token$`, st.adoptsThroughPost)
			sc.Step(`^the response carries the code and how long it is good for$`, st.responseCarriesCode)
			sc.Step(`^the console shows it in the adopting row$`, st.consoleShowsInRow)
			sc.Step(`^a read without the token never carries a code$`, st.noTokenNoCode)
			sc.Step(`^a console on a machine that is not enrolled$`, st.consoleNotEnrolled)
			sc.Step(`^the owner joins an authority through the console with a code that does not verify$`, st.joinsWithBadCode)
			sc.Step(`^the console says the code does not match the Edge that answered$`, st.saysNoMatch)
			sc.Step(`^nothing was kept$`, st.nothingKept)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@console",
			Paths: []string{"../../features/edge/enrollment_root_trust.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the enrollment root trust console scenarios failed")
	}
}
