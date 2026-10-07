package tui

// Executable spec: features/lan/regressions.feature:14 (@tui). The TUI dial, outside LAN mode,
// pointed at a REAL standalone Tower (internal/tower + internal/localplane) that admits this
// machine's key: the unsigned /discover is refused, the dial retries once signed, lists the
// Tower's bands, and is not drawn as "0 on air".

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"rogerai.fm/roger/v6/internal/bddrun"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	towerstate "rogerai.fm/roger/v6/internal/tower"
)

type lanRegressBDD struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	discover []int // the status of each /discover, in order
	signed   []bool
	offers   []offer
}

func (s *lanRegressBDD) towerAdmitting() error {
	dir := s.t.TempDir()
	if _, err := towerstate.Init(dir, towerstate.ModeStandalone); err != nil {
		return err
	}
	st, err := towerstate.Open(dir)
	if err != nil {
		return err
	}
	keyHash := protocol.UserIDFromPubkey(hex.EncodeToString(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey)))
	inv, code, err := st.CreateInvitation(keyHash, time.Hour, 5)
	if err != nil {
		return err
	}
	if _, err := st.ConsumeInvitation(inv.ID, code, keyHash); err != nil {
		return err
	}
	if _, err := st.AttachStation("st-desk", "sk-desk", []string{"llama-3.3-70b"}); err != nil {
		return err
	}
	plane := localplane.New(st).Handler()
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discover" {
			plane.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		plane.ServeHTTP(rec, r)
		s.mu.Lock()
		s.discover = append(s.discover, rec.Code)
		s.signed = append(s.signed, r.Header.Get(protocol.HeaderSig) != "")
		s.mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	s.t.Cleanup(s.srv.Close)
	return nil
}

func (s *lanRegressBDD) refreshes() error {
	msg := fetchOffers(s.srv.URL)()
	got, ok := msg.(offersMsg)
	if !ok {
		return fmt.Errorf("the dial got %#v, not offers", msg)
	}
	s.offers = got
	return nil
}

func TestLANRegressionsTUIBDD(t *testing.T) {
	s := &lanRegressBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.discover, s.signed, s.offers = nil, nil, nil
				SetLANDial(nil)
				return ctx, nil
			})
			sc.Step(`^the broker setting is a standalone Tower that admits this machine's key$`, s.towerAdmitting)
			sc.Step(`^LAN mode is off$`, func() error {
				if LANMode() {
					return fmt.Errorf("LAN mode is on")
				}
				return nil
			})
			sc.Step(`^the TUI dial refreshes$`, s.refreshes)
			sc.Step(`^the first unsigned /discover gets 401$`, func() error {
				if len(s.discover) == 0 || s.signed[0] || s.discover[0] != http.StatusUnauthorized {
					return fmt.Errorf("discover calls: status %v signed %v", s.discover, s.signed)
				}
				return nil
			})
			sc.Step(`^the dial retries once signed and lists the Tower's bands$`, func() error {
				if len(s.discover) != 2 || !s.signed[1] || s.discover[1] != http.StatusOK {
					return fmt.Errorf("discover calls: status %v signed %v, want one signed retry", s.discover, s.signed)
				}
				for _, o := range s.offers {
					if o.Model == "llama-3.3-70b" {
						return nil
					}
				}
				return fmt.Errorf("the dial does not list the Tower's band: %+v", s.offers)
			})
			sc.Step(`^it is not drawn as "0 on air"$`, func() error {
				m := browseSeed(120)
				next, _ := m.Update(offersMsg(s.offers))
				if v := asModel(next).View(); strings.Contains(v, "0 bands") || strings.Contains(v, "0 stations on air") {
					return fmt.Errorf("the dial is drawn empty:\n%s", v)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/lan/regressions.feature:14"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("regressions.feature:14 failed")
	}
}
