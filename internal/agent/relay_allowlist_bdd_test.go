package agent

// relay_allowlist_bdd_test.go makes features/voice/relay_allowlist.feature EXECUTABLE under
// godog, driving the REAL agent.serve() against a REAL httptest backend that RECORDS whether it
// was hit (and on what path). It is the node-side trust-boundary contract: an allowlisted broker
// Path forwards to the local backend unchanged; a NON-allowlisted Path is refused with a clean
// 404 and the backend is NEVER contacted. No mocks of the node internals - the only stub is the
// local backend, which is exactly the surface a compromised broker would try to steer.

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type relayAllowlistState struct {
	backend     *httptest.Server
	hits        atomic.Int64
	lastPath    atomic.Value // string: the last URL path the backend was hit on
	lastHeader  atomic.Value // http.Header: the headers the backend saw on the last hit
	cfg         Config
	priv        ed25519.PrivateKey
	res         protocol.JobResult
	panicked    bool
	upstreamURL string
	upstreamKey string
}

const relayBackendBody = "OK-FROM-LOCAL-BACKEND"

func (s *relayAllowlistState) reset() {
	if s.backend != nil {
		s.backend.Close()
	}
	*s = relayAllowlistState{}
	_, s.priv, _ = ed25519.GenerateKey(nil)
}

// --- Given ---

func (s *relayAllowlistState) recordingBackend() error {
	s.backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.lastPath.Store(r.URL.Path)
		s.lastHeader.Store(r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(relayBackendBody))
	}))
	s.upstreamURL = s.backend.URL
	s.upstreamKey = "sk-node-upstream-SECRET"
	return nil
}

func (s *relayAllowlistState) nodeRelayingToBackend() error {
	s.cfg = Config{
		NodeID:      "n1",
		Model:       "roger-operator-voice",
		Upstream:    s.backend.URL + "/v1/chat/completions",
		UpstreamKey: s.upstreamKey,
	}
	return nil
}

// --- When ---

// relayPath drives the REAL serve() with the given broker Path. It recovers any panic so a RED
// run reports the failing assertion cleanly rather than aborting the whole suite (a nil upstream
// request from a malformed derived URL is itself a bug this hardening removes).
func (s *relayAllowlistState) relayPath(path string) {
	defer func() {
		if r := recover(); r != nil {
			s.panicked = true
		}
	}()
	job := protocol.Job{ID: "j", User: "u", Body: []byte(`{"model":"m","input":"hi"}`), Path: path}
	s.res = serve(s.cfg, protocol.ModelOffer{Model: "m"}, s.priv, &http.Client{Timeout: 3 * time.Second}, job)
}

func (s *relayAllowlistState) relayAbsentPath() error { s.relayPath(""); return nil }
func (s *relayAllowlistState) relayWithPath(path string) error {
	s.relayPath(path)
	return nil
}

// --- Then ---

func (s *relayAllowlistState) backendHitOnceAt(want string) error {
	if s.panicked {
		return bddErr("serve panicked instead of forwarding to " + want)
	}
	if got := s.hits.Load(); got != 1 {
		return bddErr("expected the backend to be hit exactly once, got " + itoa(got))
	}
	got, _ := s.lastPath.Load().(string)
	if got != want {
		return bddErr("backend hit at " + got + ", want " + want)
	}
	return nil
}

func (s *relayAllowlistState) nodeReturnsBackendResponse() error {
	if s.res.Status != http.StatusOK {
		return bddErr("node status " + itoa(int64(s.res.Status)) + ", want 200 from the backend")
	}
	if string(s.res.Body) != relayBackendBody {
		return bddErr("node body " + string(s.res.Body) + ", want the backend body")
	}
	return nil
}

func (s *relayAllowlistState) refuses404Unsupported() error {
	if s.panicked {
		return bddErr("serve panicked instead of cleanly refusing")
	}
	if s.res.Status != http.StatusNotFound {
		return bddErr("refusal status " + itoa(int64(s.res.Status)) + ", want 404")
	}
	if !strings.Contains(strings.ToLower(string(s.res.Body)), "unsupported path") {
		return bddErr("refusal body " + string(s.res.Body) + ", want an 'unsupported path' error")
	}
	return nil
}

func (s *relayAllowlistState) backendNeverHit() error {
	if got := s.hits.Load(); got != 0 {
		lp, _ := s.lastPath.Load().(string)
		return bddErr("backend was hit " + itoa(got) + " time(s) (last path " + lp + "); a refused path must NEVER reach the local backend")
	}
	return nil
}

// forwardedHeaderDiscipline asserts A4: the node forwards ONLY the Content-Type + Authorization
// headers it sets itself - never a broker-supplied application header (Job carries no headers, so
// the node cannot be tricked into forwarding one). Go's transport adds its own hop-by-hop headers
// (Host/User-Agent/Accept-Encoding/Content-Length), which are not broker-controlled and allowed.
func (s *relayAllowlistState) forwardedHeaderDiscipline() error {
	h, _ := s.lastHeader.Load().(http.Header)
	if h == nil {
		return bddErr("no forwarded request was recorded")
	}
	if ct := h.Get("Content-Type"); ct != "application/json" {
		return bddErr("forwarded Content-Type " + ct + ", want application/json")
	}
	if auth := h.Get("Authorization"); auth != "Bearer "+s.upstreamKey {
		return bddErr("forwarded Authorization " + auth + ", want the node's Bearer upstream key")
	}
	allowed := map[string]bool{
		"Content-Type": true, "Authorization": true, // the only two the node sets
		"Host": true, "User-Agent": true, "Accept-Encoding": true, "Content-Length": true, // Go transport
	}
	for name := range h {
		if !allowed[name] {
			return bddErr("forwarded an unexpected header " + name + "; the node must forward ONLY Content-Type + Authorization")
		}
	}
	return nil
}

func (s *relayAllowlistState) refusalLeaksNothing() error {
	body := string(s.res.Body)
	if s.upstreamURL != "" && strings.Contains(body, s.upstreamURL) {
		return bddErr("refusal body leaked the upstream URL: " + body)
	}
	if strings.Contains(body, s.upstreamKey) {
		return bddErr("refusal body leaked the upstream key: " + body)
	}
	return nil
}

// itoa avoids pulling strconv into the assertion strings inline.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestRelayAllowlistBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &relayAllowlistState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.backend != nil {
					st.backend.Close()
					st.backend = nil
				}
				return ctx, nil
			})
			sc.Step(`^a local backend that records every request path it is hit on$`, st.recordingBackend)
			sc.Step(`^a share node relaying to that backend$`, st.nodeRelayingToBackend)
			sc.Step(`^the broker relays a job with an absent path$`, st.relayAbsentPath)
			sc.Step(`^the broker relays a job with path "([^"]*)"$`, st.relayWithPath)
			sc.Step(`^the backend is hit once at "([^"]*)"$`, st.backendHitOnceAt)
			sc.Step(`^the node returns the backend's response$`, st.nodeReturnsBackendResponse)
			sc.Step(`^the forwarded request carries only the node's Content-Type and Authorization headers$`, st.forwardedHeaderDiscipline)
			sc.Step(`^the node refuses with a 404 "unsupported path"$`, st.refuses404Unsupported)
			sc.Step(`^the backend is never hit$`, st.backendNeverHit)
			sc.Step(`^the refusal body leaks neither the upstream URL nor the upstream key$`, st.refusalLeaksNothing)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/voice/relay_allowlist.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("voice/relay_allowlist behavior scenarios failed (see godog output above)")
	}
}
