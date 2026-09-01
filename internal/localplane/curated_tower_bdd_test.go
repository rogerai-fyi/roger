package localplane

// Executable spec: features/curated/curated_tower.feature, the @standalone scenarios.
//
// The REAL loop, no mocks: a tower.State on disk, this plane's real Handler, a client and
// a station with real ed25519 keys, and a live httptest commercial "upstream" that demands
// a bearer key. The station goroutine is the operator-side proxy: it polls the plane,
// dials the upstream WITH the key, and completes - so the assertions about where the key
// appears (only the upstream dial) and where it never appears (poll/complete wire,
// discovery, receipts) run against the actual bytes.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

const towerUpstreamKey = "sk-operator-secret-upstream-key"

type curTowerState struct {
	t        *testing.T
	st       *tower.State
	srv      *Server
	client   ed25519.PrivateKey
	station  ed25519.PrivateKey
	upstream *httptest.Server

	upstreamHits   int
	upstreamAuthed bool
	pollBody       []byte // what the station RECEIVED from the plane (the job)
	completeBody   []byte // what the station SENT back (the answer envelope)
	answerRec      *httptest.ResponseRecorder
}

func (s *curTowerState) reset() {
	s.st, s.srv, s.client, s.station = nil, nil, nil, nil
	if s.upstream != nil {
		s.upstream.Close()
	}
	s.upstream = nil
	s.upstreamHits, s.upstreamAuthed = 0, false
	s.pollBody, s.completeBody, s.answerRec = nil, nil, nil
}

// --- Given ---------------------------------------------------------------

func (s *curTowerState) standaloneTowerWithKey() error {
	s.st = standaloneState(s.t)
	s.client = admitClient(s.t, s.st)
	// The commercial upstream: answers only with the operator's key.
	s.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.upstreamHits++
		s.upstreamAuthed = r.Header.Get("Authorization") == "Bearer "+towerUpstreamKey
		if !s.upstreamAuthed {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"from the commercial upstream"}}]}`)
	}))
	// The curated station: attached with the provider label.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if _, err := s.st.AttachCuratedStation("st-cur", protocol.UserIDFromPubkey(hexPub(pub)), []string{"gpt-4o"}, "openrouter"); err != nil {
		return err
	}
	s.station = priv
	s.srv = New(s.st)
	return nil
}

// runProxyOnce is the operator-side station loop, one job: poll -> dial upstream with the
// key -> complete. It records the exact wire bytes for the key-containment assertions.
func (s *curTowerState) runProxyOnce(ctx context.Context) error {
	pollRec := httptest.NewRecorder()
	s.srv.Handler().ServeHTTP(pollRec, signedBody(s.t, s.station, http.MethodPost, "/local/poll", nil))
	if pollRec.Code != http.StatusOK {
		return fmt.Errorf("poll = %d: %s", pollRec.Code, pollRec.Body.String())
	}
	s.pollBody = pollRec.Body.Bytes()
	var job struct {
		JobID   string          `json:"job_id"`
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(s.pollBody, &job); err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.upstream.URL, bytes.NewReader(job.Request))
	req.Header.Set("Authorization", "Bearer "+towerUpstreamKey) // read HERE, operator-side, only
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	answer, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s.completeBody, _ = json.Marshal(map[string]any{"job_id": job.JobID, "answer": json.RawMessage(answer)})
	compRec := httptest.NewRecorder()
	s.srv.Handler().ServeHTTP(compRec, signedBody(s.t, s.station, http.MethodPost, "/local/complete", s.completeBody))
	if compRec.Code != http.StatusOK {
		return fmt.Errorf("complete = %d: %s", compRec.Code, compRec.Body.String())
	}
	return nil
}

// --- When ----------------------------------------------------------------

func (s *curTowerState) localClientRequests() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	prompt := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	go func() {
		rec := httptest.NewRecorder()
		s.srv.Handler().ServeHTTP(rec, signedBody(s.t, s.client, http.MethodPost, "/v1/chat/completions", prompt))
		done <- rec
	}()
	if err := s.runProxyOnce(ctx); err != nil {
		return err
	}
	select {
	case rec := <-done:
		s.answerRec = rec
		return nil
	case <-ctx.Done():
		return fmt.Errorf("the consumer never received the curated answer")
	}
}

func (s *curTowerState) curatedRequestRelays() error {
	if err := s.standaloneTowerWithKey(); err != nil {
		return err
	}
	return s.localClientRequests()
}

// --- Then ----------------------------------------------------------------

func (s *curTowerState) servedFromUpstream() error {
	if s.answerRec.Code != http.StatusOK {
		return fmt.Errorf("consumer got %d: %s", s.answerRec.Code, s.answerRec.Body.String())
	}
	if s.upstreamHits == 0 || !s.upstreamAuthed {
		return fmt.Errorf("the upstream was not dialed with the operator's key (hits=%d authed=%v)", s.upstreamHits, s.upstreamAuthed)
	}
	if !strings.Contains(s.answerRec.Body.String(), "from the commercial upstream") {
		return fmt.Errorf("the answer is not the upstream's: %s", s.answerRec.Body.String())
	}
	return nil
}

func (s *curTowerState) markedLocalAndCurated() error {
	if s.answerRec.Header().Get("X-Roger-Local") != "1" {
		return fmt.Errorf("the answer is not marked local")
	}
	if got := s.answerRec.Header().Get("X-Roger-Curated"); got != "openrouter" {
		return fmt.Errorf("the answer is not marked curated (X-Roger-Curated=%q, want the provider)", got)
	}
	return nil
}

func (s *curTowerState) nothingLeavesTheNetwork() error {
	// Structural: this plane dials nobody (its own package doc + source-scan gate), the
	// receipt is stored on the tower's own disk and stamped with the LOCAL network id.
	recs, err := s.st.Receipts(0)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("no local receipt persisted")
	}
	if recs[len(recs)-1].NetworkID != s.st.LocalNetworkID {
		return fmt.Errorf("the receipt is stamped %q, not the local network", recs[len(recs)-1].NetworkID)
	}
	return nil
}

func (s *curTowerState) keyReadOnlyOnTower() error {
	if s.upstreamHits == 0 || !s.upstreamAuthed {
		return fmt.Errorf("the upstream never saw the key it requires")
	}
	return nil
}

func (s *curTowerState) keyNeverOnWireReceiptOrCore() error {
	// The poll the plane sent to the station, and the completion the station sent back:
	// neither carries the key (the station reads it locally and spends it only on the
	// upstream dial). Nor does discovery or any persisted receipt.
	for name, b := range map[string][]byte{"poll": s.pollBody, "complete": s.completeBody} {
		if bytes.Contains(b, []byte(towerUpstreamKey)) {
			return fmt.Errorf("the upstream key appears in the %s wire payload", name)
		}
	}
	disc := httptest.NewRecorder()
	s.srv.Handler().ServeHTTP(disc, signedBody(s.t, s.client, http.MethodGet, "/discover", nil))
	if bytes.Contains(disc.Body.Bytes(), []byte(towerUpstreamKey)) {
		return fmt.Errorf("the upstream key appears in discovery")
	}
	recs, err := s.st.Receipts(0)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if strings.Contains(r.String(), towerUpstreamKey) || strings.Contains(r.CuratedProvider, towerUpstreamKey) {
			return fmt.Errorf("the upstream key appears in a receipt")
		}
	}
	// "at the Core" is structural on this plane: a standalone Tower has no Core link at
	// all (the no-Core guarantee this package's egress gate proves); nothing to inspect.
	return nil
}

func (s *curTowerState) standaloneServingCurated() error {
	if err := s.standaloneTowerWithKey(); err != nil {
		return err
	}
	return s.localClientRequests()
}

func (s *curTowerState) everyCuratedAnswerFree() error {
	if got := s.answerRec.Header().Get("X-Roger-Cost"); got != "0" {
		return fmt.Errorf("X-Roger-Cost = %q, want the free 0", got)
	}
	recs, err := s.st.Receipts(0)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Cost != 0 {
			return fmt.Errorf("a local receipt carries cost %d", r.Cost)
		}
		if !r.Curated || r.CuratedProvider != "openrouter" {
			return fmt.Errorf("the receipt does not carry the curated label: %+v", r)
		}
	}
	return nil
}

func TestCuratedTowerStandaloneFeature(t *testing.T) {
	st := &curTowerState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.Step(`^a standalone tower with an upstream key$`, st.standaloneTowerWithKey)
			sc.Step(`^a local client requests that model$`, st.localClientRequests)
			sc.Step(`^the tower serves it from the upstream$`, st.servedFromUpstream)
			sc.Step(`^the answer is marked local-and-curated$`, st.markedLocalAndCurated)
			sc.Step(`^no request or receipt leaves the network$`, st.nothingLeavesTheNetwork)
			sc.Step(`^a curated request relays through the tower$`, st.curatedRequestRelays)
			sc.Step(`^the upstream key is read only on the tower$`, st.keyReadOnlyOnTower)
			sc.Step(`^it never appears on the wire, in a receipt, or at the Core$`, st.keyNeverOnWireReceiptOrCore)
			sc.Step(`^a standalone tower serving a curated upstream$`, st.standaloneServingCurated)
			sc.Step(`^every curated answer on the local plane is free$`, st.everyCuratedAnswerFree)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@standalone",
			Paths: []string{"../../features/curated/curated_tower.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the @standalone curated tower scenarios failed")
	}
}

// Discovery is a display surface too: `roger` pointed at a local Tower parses these
// offers into the same dial rows as the public market, so the curated label must ride
// them or a proxy renders as local hardware (the identity rule, curated_identity.feature).
func TestLocalDiscoveryLabelsCuratedStations(t *testing.T) {
	s := &curTowerState{t: t}
	defer s.reset()
	if err := s.standaloneTowerWithKey(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.srv.Handler().ServeHTTP(rec, signedBody(t, s.client, http.MethodGet, "/discover", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("discover = %d", rec.Code)
	}
	var out struct {
		Offers []struct {
			NodeID          string `json:"node_id"`
			Curated         bool   `json:"curated"`
			CuratedProvider string `json:"curated_provider"`
			Local           bool   `json:"local"`
		} `json:"offers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Offers) != 1 || !out.Offers[0].Curated || out.Offers[0].CuratedProvider != "openrouter" {
		t.Fatalf("the curated station is unlabeled in local discovery: %+v", out.Offers)
	}
}
