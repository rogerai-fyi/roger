package agent

// The Station half of features/tower/private_hub_placement.feature: a Station dials a private hub
// only with a certificate pin, proves the hub before sending anything, and takes a private hub
// address from nowhere but Core's attach answer. The broker half runs in cmd/rogerai-broker.

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/towerhub"
)

type phsState struct {
	t        *testing.T
	hub      *httptest.Server
	hits     atomic.Int64
	goodPin  string
	wrongPin string
	err      error
	okErr    error
}

func phsCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), Subject: pkix.Name{CommonName: "hub"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, towerhub.CertPin(leaf)
}

func (s *phsState) handedPrivateWithPin() error {
	served, pin := phsCert(s.t)
	_, other := phsCert(s.t)
	s.goodPin, s.wrongPin = pin, other
	s.hub = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	s.hub.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{served}}
	s.hub.StartTLS()
	s.t.Cleanup(s.hub.Close)
	return nil
}

// connect is the node's own client path: towerhub.Reach with the pin Core handed over, then a
// signed poll, as a serve worker sends it.
func (s *phsState) connect() error {
	endpoint := s.hub.Listener.Addr().String()
	poll := func(pin string) error {
		base, hc, err := towerhub.Reach(endpoint, pin, nil)
		if err != nil {
			return err
		}
		c := &towerhub.Client{BaseURL: base, HTTP: hc}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _, err = c.PollJob(ctx, "st-1")
		return err
	}
	s.err = poll(s.wrongPin)
	before := s.hits.Load()
	s.okErr = poll(s.goodPin)
	if s.hits.Load() == before {
		return fmt.Errorf("with the right pin the request never reached the hub (%v), so the check proves nothing", s.okErr)
	}
	s.hits.Store(before)
	return nil
}

func (s *phsState) checksPinFirst() error {
	if !errors.Is(s.err, towerhub.ErrHubCertificateUnpinned) {
		return fmt.Errorf("a hub presenting another certificate answered %v", s.err)
	}
	return nil
}

func (s *phsState) neverSentAndReported() error {
	if n := s.hits.Load(); n != 0 {
		return fmt.Errorf("the hub that failed the pin was sent %d request(s)", n)
	}
	// Reported to Core: a certificate that is not the one Core named skips the failure window
	// and sends the node straight back to Core for a fresh attachment.
	if !(&hubFailureStreak{}).observe(s.err, time.Now()) {
		return fmt.Errorf("an unpinned hub does not send the node back to Core")
	}
	return nil
}

// attachAnswering runs the node's real attach against a Core that answers with endpoint and pin.
func (s *phsState) attachAnswering(endpoint, pin string) error {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"station_id": "st-1", "tower_id": "tw-1",
			"endpoint": endpoint, "endpoint_tls_spki": pin, "tower_key_hash": "ab"})
	}))
	defer core.Close()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, _, err := AttachTower(Config{Broker: core.URL, NodeID: "n-1", Model: "m"}, priv, s.t.TempDir())
	return err
}

func (s *phsState) onlyFromAttach() error {
	if err := s.attachAnswering("192.168.1.20:8444", ""); err == nil {
		return fmt.Errorf("an attach answer naming a private hub with no pin was accepted")
	}
	if err := s.attachAnswering("192.168.1.20:8444", s.goodPinOr()); err != nil {
		return fmt.Errorf("an attach answer naming a pinned private hub was refused: %v", err)
	}
	return nil
}

func (s *phsState) goodPinOr() string {
	if s.goodPin != "" {
		return s.goodPin
	}
	_, pin := phsCert(s.t)
	return pin
}

func (s *phsState) ignoresOtherSources() error {
	// The hub a node serves comes from TowerAttachment and nowhere else; and even from there a
	// private address without a pin is refused (privateHubUnpinned).
	for _, tc := range []struct {
		endpoint, pin string
		refused       bool
	}{
		{"192.168.1.20:8444", "", true}, {"10.0.0.5:8444", "", true}, {"[fd12::20]:8444", "", true},
		{"100.64.1.9:8444", "", true}, {"192.168.1.20:8444", "pin", false},
		{"203.0.113.7:8444", "", false}, {"127.0.0.1:8444", "", false}, {"hub.example:8444", "", false},
	} {
		if got := privateHubUnpinned(tc.endpoint, tc.pin); got != tc.refused {
			return fmt.Errorf("%s pin=%q refused=%v, want %v", tc.endpoint, tc.pin, got, tc.refused)
		}
	}
	return nil
}

func TestPrivateHubStationBDD(t *testing.T) {
	s := &phsState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*s = phsState{t: t}
				return ctx, nil
			})
			sc.Step(`^Roger Core runs two instances sharing one store$`, func() error { return nil })
			sc.Step(`^a Tower whose session carries the "pull" capability$`, func() error { return nil })
			sc.Step(`^a Station handed a private hub address with its pin$`, s.handedPrivateWithPin)
			sc.Step(`^it connects$`, s.connect)
			sc.Step(`^it checks the hub's TLS key against the pin before it sends its node credentials or any request$`, s.checksPinFirst)
			sc.Step(`^a hub that fails the pin is never sent anything, and the Station reports the failure to Core$`, s.neverSentAndReported)
			sc.Step(`^a private hub address reaches a Station only in Core's signed attach answer$`, s.onlyFromAttach)
			sc.Step(`^a Station ignores any private hub address from any other source$`, s.ignoresOtherSources)
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t, Paths: bddrun.Scenarios("../../features/tower/private_hub_placement.feature", bddrun.Titled(false, "The Station proves the hub before it sends anything", "A Station never dials a private address Core did not hand it"))},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("private_hub_placement.feature (Station half) failed")
	}
}

// Review finding: a Station that could not reach its private hub (a carrier NAT can match two
// households by public address) went back to Core and was handed the same hub forever. The attach
// it makes after giving up on a hub names that Tower; an ordinary attach names none.
func TestAnAttachAfterAnUnreachableHubNamesTheTower(t *testing.T) {
	var bodies []map[string]any
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		_ = json.NewEncoder(w).Encode(map[string]any{"station_id": "st-1", "tower_id": "tw-2",
			"endpoint": "203.0.113.7:8444", "tower_key_hash": "ab"})
	}))
	defer core.Close()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	cfg := Config{Broker: core.URL, NodeID: "n-1", Model: "m"}
	if _, _, err := AttachTower(cfg, priv, dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := attachTowerAvoiding(cfg, priv, dir, TowerAttachment{TowerID: "tw-1", EndpointTLSSPKI: "pin-1", Endpoint: "192.168.1.20:8444"}); err != nil {
		t.Fatal(err)
	}
	if _, named := bodies[0]["unreachable_tower"]; named {
		t.Fatalf("an ordinary attach named an unreachable Tower: %v", bodies[0])
	}
	if bodies[1]["unreachable_tower"] != "tw-1" || bodies[1]["unreachable_pin"] != "pin-1" ||
		bodies[1]["unreachable_endpoint"] != "192.168.1.20:8444" {
		t.Fatalf("the attach after an unreachable hub did not name it: %v", bodies[1])
	}
}
