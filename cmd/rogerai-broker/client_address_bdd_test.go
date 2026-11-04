package main

// client_address_bdd_test.go makes features/security/client_address_trust.feature
// EXECUTABLE. The steps drive the real clientIP, clientCountry and startup loader, the real
// /report handler with its per-address limiter, and the real origin tally store. The trust
// setting is process-wide, so every scenario starts from the setting the test found and the
// test puts it back when it ends.

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/origin"
)

type clientAddrBDD struct {
	proxies, country string // the two settings, as an operator writes them
	req              *http.Request
	startErr         error
	startLog         string
	b                *broker
	caller, proxy    string // the reporting caller and the proxy it reaches the broker through
	lastCode         int
	named            []string
}

// install parses the two settings the way startup does and puts the result in force.
func (s *clientAddrBDD) install() error {
	t, err := parseAddrTrust(s.proxies, s.country)
	if err != nil {
		return err
	}
	clientAddrTrust.Store(t)
	return nil
}

func (s *clientAddrBDD) noProxy() error { s.proxies = ""; return s.install() }

func (s *clientAddrBDD) trustedProxies(setting string) error { s.proxies = setting; return s.install() }

func (s *clientAddrBDD) countryHeader(name string) error { s.country = name; return s.install() }

// arrive starts a request whose TCP peer is peer. An address gets a port, as a real connection
// has; anything else is used as it is (a local socket has no IP to give).
func (s *clientAddrBDD) arrive(peer string) {
	s.req = httptest.NewRequest(http.MethodGet, "/", nil)
	s.req.RemoteAddr = peer
	if _, err := netip.ParseAddr(peer); err == nil {
		s.req.RemoteAddr = net.JoinHostPort(peer, "40000")
	}
}

func (s *clientAddrBDD) arriveBare(peer string) error { s.arrive(peer); return nil }

func (s *clientAddrBDD) arriveWithHeader(peer, h, v string) error {
	s.arrive(peer)
	s.req.Header.Set(h, v)
	return nil
}

func (s *clientAddrBDD) arriveWithXFF(peer, xff string) error {
	return s.arriveWithHeader(peer, "X-Forwarded-For", xff)
}

func (s *clientAddrBDD) arriveWithXFFLines(peer, first, second string) error {
	s.arrive(peer)
	s.req.Header.Add("X-Forwarded-For", first)
	s.req.Header.Add("X-Forwarded-For", second)
	return nil
}

func (s *clientAddrBDD) arriveWithHeaderLines(peer, h, first, second string) error {
	s.arrive(peer)
	s.req.Header.Add(h, first)
	s.req.Header.Add(h, second)
	return nil
}

func (s *clientAddrBDD) arriveWithXFFAnd(peer, xff, h, v string) error {
	s.arrive(peer)
	s.req.Header.Set("X-Forwarded-For", xff)
	s.req.Header.Set(h, v)
	return nil
}

func (s *clientAddrBDD) callerIs(want string) error {
	if got := clientIP(s.req); got != want {
		return fmt.Errorf("the broker took the caller to be %q, want %q", got, want)
	}
	return nil
}

// --- startup ---------------------------------------------------------------------------------

func (s *clientAddrBDD) start(env map[string]string) error {
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	s.startErr = loadClientAddrTrust(func(k string) string { return env[k] })
	log.SetOutput(out)
	log.SetFlags(flags)
	s.startLog = buf.String()
	return nil
}

func (s *clientAddrBDD) startWith(setting string) error {
	return s.start(map[string]string{"ROGERAI_TRUSTED_PROXIES": setting})
}

func (s *clientAddrBDD) startWithout() error { return s.start(map[string]string{}) }

func (s *clientAddrBDD) startWithCountry(h string) error {
	return s.start(map[string]string{"ROGERAI_COUNTRY_HEADER": h})
}

func (s *clientAddrBDD) refuses(bad string) error {
	if s.startErr == nil {
		return fmt.Errorf("the broker started (log %q); want a refusal naming %q", s.startLog, bad)
	}
	if !strings.Contains(s.startErr.Error(), fmt.Sprintf("%q", bad)) {
		return fmt.Errorf("the refusal %q does not name %q", s.startErr, bad)
	}
	return nil
}

func (s *clientAddrBDD) logReads(want string) error {
	if s.startErr != nil {
		return fmt.Errorf("the broker refused to start: %v", s.startErr)
	}
	if !strings.Contains(s.startLog, want) {
		return fmt.Errorf("the startup log %q does not read %q", s.startLog, want)
	}
	return nil
}

// --- the per-address limit on /report ---------------------------------------------------------

// report posts a real report through the real handler. A tight bucket (burst 2, one token a
// minute) keeps the "used up" steps short and cannot refill mid-scenario; the key is what is
// under test, not the numbers.
func (s *clientAddrBDD) report(peer, xff string, extra map[string]string) int {
	if s.b == nil {
		s.b = testBrokerWithDB(store.NewMem())
		s.b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 2}
	}
	r := httptest.NewRequest(http.MethodPost, "/report", strings.NewReader(`{"category":"abuse","node_id":"n-ca"}`))
	r.RemoteAddr = net.JoinHostPort(peer, "40000")
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	for k, v := range extra {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.b.report(w, r)
	return w.Code
}

func (s *clientAddrBDD) usedUpVia(ip, proxy string) error {
	s.caller, s.proxy = ip, proxy
	peer, xff := ip, ""
	if proxy != "" {
		peer, xff = proxy, ip
	}
	for i := 0; i < 20; i++ {
		if s.report(peer, xff, nil) == http.StatusTooManyRequests {
			return nil
		}
	}
	return fmt.Errorf("%s never ran out of report allowance", ip)
}

func (s *clientAddrBDD) usedUp(ip string) error { return s.usedUpVia(ip, "") }

func (s *clientAddrBDD) reportForged(cf, xff string) error {
	s.lastCode = s.report(s.caller, "", map[string]string{"CF-Connecting-IP": cf, "X-Forwarded-For": xff})
	return nil
}

func (s *clientAddrBDD) reportVia(ip string) error {
	s.lastCode = s.report(s.proxy, ip, nil)
	return nil
}

func (s *clientAddrBDD) refusedWith(code int) error {
	if s.lastCode != code {
		return fmt.Errorf("the report got %d, want %d", s.lastCode, code)
	}
	return nil
}

func (s *clientAddrBDD) notRefusedWith(code int) error {
	if s.lastCode == code {
		return fmt.Errorf("the report got %d, want anything but %d", s.lastCode, code)
	}
	return nil
}

// --- one reader of forwarding headers ---------------------------------------------------------

// forwardingHeaderName is every header that names a caller or its country. A string literal
// equal to one of them, or a read of a request's RemoteAddr, in any broker file but
// clientaddr.go, is a second reader in waiting.
var forwardingHeaderName = regexp.MustCompile(`(?i)^(x-forwarded-[a-z-]+|forwarded|cf-connecting-ip|x-real-ip|true-client-ip|cf-ipcountry|x-country)$`)

func (s *clientAddrBDD) scan() error {
	files, err := filepath.Glob("*.go")
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			return err
		}
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if v, err := strconv.Unquote(n.Value); err == nil && n.Kind == token.STRING && forwardingHeaderName.MatchString(v) {
					found = true
				}
			case *ast.SelectorExpr:
				found = found || n.Sel.Name == "RemoteAddr"
			}
			return !found
		})
		if found {
			s.named = append(s.named, f)
		}
	}
	return nil
}

func (s *clientAddrBDD) onlyClientAddr() error {
	if len(s.named) != 1 || s.named[0] != "clientaddr.go" {
		return fmt.Errorf("broker files naming a forwarding header: %v, want only clientaddr.go", s.named)
	}
	return nil
}

// --- the country -------------------------------------------------------------------------------

func (s *clientAddrBDD) countryIs(want string) error {
	st := origin.NewMemStore()
	if err := st.Record("tw-ca", "attempt-ca", clientCountry(s.req), time.Now()); err != nil {
		return err
	}
	got, err := st.ByTower("tw-ca", time.Time{})
	if err != nil {
		return err
	}
	if len(got) != 1 || got[0].Country != want || got[0].Attempts != 1 {
		return fmt.Errorf("the origin tally is %+v, want one attempt from %q", got, want)
	}
	return nil
}

func TestClientAddressTrustBDD(t *testing.T) {
	found := clientAddrTrust.Load()
	t.Cleanup(func() { clientAddrTrust.Store(found) })
	s := &clientAddrBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*s = clientAddrBDD{}
				clientAddrTrust.Store(found)
				return ctx, nil
			})
			sc.Step(`^no proxy is named$`, s.noProxy)
			sc.Step(`^the broker's trusted proxies are "([^"]*)"$`, s.trustedProxies)
			sc.Step(`^the operator names "([^"]*)" as the country header$`, s.countryHeader)
			sc.Step(`^a request arrives from "([^"]*)" with no forwarding headers$`, s.arriveBare)
			sc.Step(`^a request arrives from "([^"]*)" with header "([^"]*)" set to "([^"]*)"$`, s.arriveWithHeader)
			sc.Step(`^a request arrives from "([^"]*)" with X-Forwarded-For "([^"]*)"$`, s.arriveWithXFF)
			sc.Step(`^a request arrives from "([^"]*)" with X-Forwarded-For lines "([^"]*)" and "([^"]*)"$`, s.arriveWithXFFLines)
			sc.Step(`^a request arrives from "([^"]*)" with X-Forwarded-For "([^"]*)" and header "([^"]*)" set to "([^"]*)"$`, s.arriveWithXFFAnd)
			sc.Step(`^a request arrives from "([^"]*)" with header "([^"]*)" lines "([^"]*)" and "([^"]*)"$`, s.arriveWithHeaderLines)
			sc.Step(`^the broker takes the caller to be "([^"]*)"$`, s.callerIs)
			sc.Step(`^the broker starts with trusted proxies "([^"]*)"$`, s.startWith)
			sc.Step(`^the broker starts with no trusted-proxy setting$`, s.startWithout)
			sc.Step(`^the broker starts with country header "([^"]*)"$`, s.startWithCountry)
			sc.Step(`^it refuses to start, naming "([^"]*)"$`, s.refuses)
			sc.Step(`^its startup log reads "([^"]*)"$`, s.logReads)
			sc.Step(`^a caller at "([^"]*)" has used up its report allowance$`, s.usedUp)
			sc.Step(`^a caller at "([^"]*)" behind the proxy "([^"]*)" has used up its report allowance$`, s.usedUpVia)
			sc.Step(`^it reports again with CF-Connecting-IP "([^"]*)" and X-Forwarded-For "([^"]*)"$`, s.reportForged)
			sc.Step(`^a caller at "([^"]*)" reports through the same proxy$`, s.reportVia)
			sc.Step(`^the report is refused with (\d+)$`, s.refusedWith)
			sc.Step(`^the report is not refused with (\d+)$`, s.notRefusedWith)
			sc.Step(`^the broker's source is scanned for forwarding and country header names and RemoteAddr$`, s.scan)
			sc.Step(`^only the client-address code names them$`, s.onlyClientAddr)
			sc.Step(`^the request's country is counted as "([^"]*)"$`, s.countryIs)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/security/client_address_trust.feature"},
			TestingT: t, Strict: true,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("client_address_trust.feature: scenarios failed")
	}
}
