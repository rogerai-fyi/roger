package main

// Executable spec for features/tower/private_hub_placement.feature (founder approved 2026-11-14):
// a Tower with no public address serves the Stations on its own network.
//
// Built on the placement_locality harness (plcState): a production broker behind a host proxy
// that overwrites the client-address header, so every Tower and Station is observed from the
// address a scenario names, with production address classing (vetPublicIP) on. The Tower side is
// played with the production pieces: signed link requests, a real towerhub.Server answering its
// pulls through towerhub.ServePulled, and real share nodes for the canary scenario. The serve
// steps run in internal/towercli and the Station-side steps in internal/agent; here they are the
// other half of the same scenarios.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/towercore/dispatch"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/placement"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// phpPin is a well-formed hub certificate pin (the shape is all Core checks at the door).
var phpPin = strings.Repeat("ab", 32)

type phpState struct {
	*plcState
	towerPeer map[string]string // the address each Tower's link is observed from
	stopPull  []func()
	moved     map[string]string // station -> trigger, from the mobility store
	outcome   reputation.Outcome
	extraCls  []func()
}

func (s *phpState) id(name string) string { return s.towers[name].id }

// openPrivate opens Tower name observed from peer, pulling, at endpoint with pin.
func (s *phpState) openAt(name, peer, endpoint, pin string, pull bool) error {
	if _, ok := s.towers[name]; !ok {
		s.newTower(name)
	}
	s.towerPeer[name] = peer
	s.front.setPeer(peer)
	caps := mandatoryCaps()
	if pull {
		caps = append(caps, link.CapPull)
	}
	extra := map[string]any{"capabilities": caps}
	if _, err := s.open(name, endpoint, pin, extra); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for endpoint != "" && time.Now().Before(deadline) {
		if s.b.tower.link.HubClass(s.id(name)) != placement.HubNone {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

// heartbeatFrom sends the Tower's heartbeat observed from peer.
func (s *phpState) heartbeatFrom(name, peer string) error {
	s.front.setPeer(peer)
	lt := s.towers[name]
	code, raw := lt.call(s.t, s.front.srv, "/tower/session/heartbeat", jsonOf(s.t, link.Frame{
		Network: link.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: s.sess[name]}), nil)
	if code != http.StatusOK {
		return fmt.Errorf("heartbeat answered %d: %s", code, raw)
	}
	return nil
}

// answerPulls answers the Tower's pulled work from a real hub with no Stations: the identity
// check passes, a submit finds no Station.
func (s *phpState) answerPulls(name string) {
	lt := s.towers[name]
	srv := towerhub.NewServer(towerhub.New(), func([]byte) (string, string, error) {
		return "", "", fmt.Errorf("no grants here")
	}, towerhub.ServerOptions{TowerID: lt.id, EpochKey: lt.priv, PollTTL: 100 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, srv.Submit)
	mux.HandleFunc(towerhub.PathPoll, srv.Poll)
	s.pullFor(name, mux)
}

// pullFor runs a pull loop for Tower name against hub, until the scenario ends.
func (s *phpState) pullFor(name string, hub http.Handler) {
	lt := s.towers[name]
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			body := jsonOf(s.t, map[string]string{"tower_id": lt.id, "session_id": s.sess[name]})
			var out struct {
				Work []towerhub.PullWork `json:"work"`
			}
			code, _ := signedPostTo(lt, s.srv.URL, "/tower/work/pull", body, &out, 10*time.Second)
			if code != http.StatusOK {
				select {
				case <-stop:
					return
				case <-time.After(20 * time.Millisecond):
				}
				continue
			}
			for _, w := range out.Work {
				w := w
				go func() {
					a := towerhub.ServePulled(context.Background(), hub, w, 32<<20)
					a.TowerID, a.SessionID = lt.id, s.sess[name]
					signedPostTo(lt, s.srv.URL, "/tower/work/answer?tower_id="+lt.id, jsonOf(s.t, a), nil, 10*time.Second)
				}()
			}
		}
	}()
	s.stopPull = append(s.stopPull, func() { close(stop); wg.Wait() })
}

// attachFrom attaches a new node's Station observed from peer.
func (s *phpState) attachFrom(peer string) (*plcNode, int, string) {
	n := s.newNode(peer, false)
	code, raw := s.attachNode(n)
	return n, code, raw
}

func (s *phpState) candidate(name string) bool {
	for _, f := range s.b.placementSnapshot() {
		if f.cand.TowerID == s.id(name) {
			return true
		}
	}
	return false
}

func (s *phpState) movesNow(at time.Time) {
	s.b.mobilityPassOnce(at)
	s.moved = map[string]string{}
	moves, _ := s.b.tower.mobility.Pending()
	for _, m := range moves {
		s.moved[m.StationID] = m.Trigger
	}
}

// --- background ----------------------------------------------------------------------------

func (s *phpState) twoInstances() error {
	// One broker carries the scenario; "every instance" is checked by reading the shared mirror
	// through a second, independent session layer (see everyInstance).
	s.b.canaryVet = vetPublicIP
	return nil
}

func (s *phpState) pullTower() error { return nil } // each scenario opens its Tower pulling

// everyInstance reads the Tower's locality as a second broker instance would: from the mirror.
func (s *phpState) everyInstance(name string) (placement.Locality, bool) {
	other := link.New(link.Config{Network: link.PublicNetwork, Versions: []int{towerProtocolMin, towerProtocolMax},
		Heartbeat: time.Minute, Freshness: 3 * time.Minute, Mirror: s.mirror})
	return other.Locality(s.id(name))
}

// --- what counts as a private hub ------------------------------------------------------------

func (s *phpState) advertises(addr string) error {
	if addr == "(none)" {
		addr = ""
	}
	s.endpoint = addr
	host, _, _ := net.SplitHostPort(addr)
	if host != "" && net.ParseIP(strings.Trim(host, "[]")) == nil && host != "hub.lan" {
		s.setAnswer("203.0.113.7")
	}
	return nil
}

func (s *phpState) resolvesHubLan() error { s.setAnswer("192.168.1.20"); return nil }

func (s *phpState) classesAs(want string) error {
	pin := phpPin
	if s.endpoint == "" {
		pin = "" // a pin with no address is refused at the door, and there is nothing to pin
	}
	if err := s.openAt("T", "198.51.100.40", s.endpoint, pin, true); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	if got := s.b.tower.link.HubClass(s.id("T")).String(); got != want {
		return fmt.Errorf("Core classes %q as %s, want %s", s.endpoint, got, want)
	}
	return nil
}

func (s *phpState) loopbackHub() error {
	return s.openAt("T", "198.51.100.40", "127.0.0.1:8444", phpPin, true)
}

func (s *phpState) noStationLoopback() error {
	n, _, _ := s.attachFrom("198.51.100.40")
	if n.placedOn == s.id("T") || s.candidate("T") {
		return fmt.Errorf("a Station was placed on a loopback hub")
	}
	return nil
}

func (s *phpState) noHub() error { return s.openAt("T", "198.51.100.40", "", "", true) }

func (s *phpState) noStationNoHub() error {
	n, _, _ := s.attachFrom("198.51.100.40")
	if n.placedOn == s.id("T") || s.candidate("T") {
		return fmt.Errorf("a Station was placed on a Tower with no hub")
	}
	return nil
}

// --- where a private-hub Tower is --------------------------------------------------------------

func (s *phpState) privateHub() error { s.endpoint = "192.168.1.20:8444"; return nil }

func (s *phpState) acceptsFrom40() error {
	return s.openAt("T", "198.51.100.40", s.endpoint, phpPin, true)
}

func (s *phpState) wantLocFrom(name, addr string) error {
	loc, ok := s.loc(name)
	if !ok {
		return fmt.Errorf("the Tower has no location")
	}
	want := s.b.addrLocality(addr)
	if loc != want {
		return fmt.Errorf("the Tower is at %+v, want %+v (from %s)", loc, want, addr)
	}
	return nil
}

func (s *phpState) computedFrom40() error { return s.wantLocFrom("T", "198.51.100.40") }

func (s *phpState) hubAddrNoPart() error {
	loc, _ := s.loc("T")
	if loc.Bucket == "" || strings.HasPrefix(loc.Bucket, "192.168.") {
		return fmt.Errorf("the location came from the hub address: %+v", loc)
	}
	return nil
}

func (s *phpState) forwardedFor() error {
	if _, ok := s.towers["T"]; !ok {
		s.newTower("T")
	}
	lt := s.towers["T"]
	s.front.setPeer("198.51.100.40")
	hello := map[string]any{"network": link.PublicNetwork, "versions": []int{1}, "tower_id": lt.id,
		"capabilities": append(mandatoryCaps(), link.CapPull), "relay_endpoint": "192.168.1.20:8444", "relay_tls_spki": phpPin}
	body := jsonOf(s.t, hello)
	req, _ := http.NewRequest(http.MethodPost, s.front.srv.URL+"/tower/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.66")
	pub, ts, sig := protocol.SignRequest(lt.priv, http.MethodPost, "/tower/session", body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var acc link.Accepted
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &acc)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("session open answered %d: %s", resp.StatusCode, raw)
	}
	s.sess["T"] = acc.SessionID
	return s.waitKnown("T")
}

func (s *phpState) usesProxyAddress() error { return s.wantLocFrom("T", "198.51.100.40") }

func (s *phpState) measuredAt40() error {
	if err := s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true); err != nil {
		return err
	}
	// A Station placed under the old address, for the last step.
	n, code, raw := s.attachFrom("198.51.100.40")
	if code != http.StatusOK || n.placedOn != s.id("T") {
		return fmt.Errorf("the same-network Station was not placed on the Tower: %d %s", code, raw)
	}
	s.stash["n"] = n
	return nil
}

func (s *phpState) nextFrom77() error { return s.heartbeatFrom("T", "198.51.100.77") }

func (s *phpState) remeasured77() error {
	if err := s.wantLocFrom("T", "198.51.100.77"); err != nil {
		return err
	}
	peer, ok := s.everyInstance("T")
	if !ok || peer != s.b.addrLocality("198.51.100.77") {
		return fmt.Errorf("another instance reads %+v", peer)
	}
	return nil
}

func (s *phpState) stationsRechecked() error {
	n := s.stash["n"].(*plcNode)
	s.movesNow(time.Now())
	if s.moved[n.out.StationID] != triggerLeftNetwork {
		return fmt.Errorf("the Station placed under the old address got %q", s.moved[n.out.StationID])
	}
	return nil
}

// --- which Stations may be placed on a private hub ---------------------------------------------

func (s *phpState) towerObservedFrom(peer string) error {
	return s.openAt("T", peer, "192.168.1.20:8444", phpPin, true)
}

func (s *phpState) stationObservedFrom(peer string) error {
	n, _, _ := s.attachFrom(peer)
	s.stash["n"] = n
	return nil
}

func (s *phpState) candidateOutcome(is string) error {
	n := s.stash["n"].(*plcNode)
	on := n.placedOn == s.id("T")
	if (is == "is") != on {
		return fmt.Errorf("the Station was placed on %q; want the private-hub Tower %s a candidate", n.placedOn, is)
	}
	return nil
}

func (s *phpState) publicTower(name, peer string) error {
	s.newTower(name)
	return s.openAt(name, peer, "203.0.113.7:8443", "", true)
}

func (s *phpState) privateAndOther() error {
	if err := s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true); err != nil {
		return err
	}
	return s.publicTower("P", "203.0.113.20")
}

func (s *phpState) stationAttaches() error {
	n, code, raw := s.attachFrom("198.51.101.9")
	s.stash["n"], s.stash["code"], s.stash["raw"] = n, code, raw
	return nil
}

func (s *phpState) attachNeverNamesPrivate() error {
	n := s.stash["n"].(*plcNode)
	if strings.Contains(n.out.Endpoint, "192.168.") || n.placedOn == s.id("T") {
		return fmt.Errorf("the attach answer named the private hub: %+v", n.out)
	}
	return nil
}

func (s *phpState) placedOnPublic() error {
	n := s.stash["n"].(*plcNode)
	if n.placedOn != s.id("P") {
		return fmt.Errorf("the Station was placed on %q, want the public-hub Tower", n.placedOn)
	}
	return nil
}

func (s *phpState) sameNetworkSameTier() error {
	if err := s.privateAndOther(); err != nil {
		return err
	}
	n, code, raw := s.attachFrom("198.51.100.40")
	if code != http.StatusOK {
		return fmt.Errorf("attach answered %d %s", code, raw)
	}
	s.stash["n"] = n
	return nil
}

func (s *phpState) coreRanks() error { return nil } // the attach above is the ranking

func (s *phpState) rankedSameNetwork() error {
	n := s.stash["n"].(*plcNode)
	if n.placedOn != s.id("T") {
		return fmt.Errorf("the Station was placed on %q, not the private-hub Tower", n.placedOn)
	}
	class, err := s.placedClass(n)
	if err != nil {
		return err
	}
	if class != placement.SameNetwork.String() {
		return fmt.Errorf("the placement was recorded as %s", class)
	}
	return nil
}

func (s *phpState) tierBPrivateTierAPublic() error {
	if err := s.privateAndOther(); err != nil {
		return err
	}
	at := time.Now()
	for i := 0; i < 2; i++ {
		_ = s.b.tower.health.RecordCheck(s.id("T"), health.Check{OK: false, At: at.Add(time.Duration(i) * canaryInterval),
			Source: health.SourcePull, Slot: healthSlot(at.Add(time.Duration(i) * canaryInterval))})
	}
	_ = s.b.tower.health.RecordCheck(s.id("P"), health.Check{OK: true, At: at, Source: health.SourceHubCheck, Slot: healthSlot(at)})
	n, _, _ := s.attachFrom("198.51.100.40")
	s.stash["n"] = n
	return nil
}

func (s *phpState) tierAWins() error {
	n := s.stash["n"].(*plcNode)
	if n.placedOn != s.id("P") {
		return fmt.Errorf("the Station was placed on %q, want the tier A Tower", n.placedOn)
	}
	return nil
}

func (s *phpState) newStageTen() error {
	if err := s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true); err != nil {
		return err
	}
	setStage(s.t, s.b, s.id("T"), placement.StageNew)
	for i := 0; i < 10; i++ {
		n, code, raw := s.attachFrom("198.51.100.40")
		if code != http.StatusOK || n.placedOn != s.id("T") {
			return fmt.Errorf("community Station %d was not placed: %d %s", i+1, code, raw)
		}
	}
	return nil
}

func (s *phpState) eleventh() error {
	n, _, _ := s.attachFrom("198.51.100.40")
	s.stash["n"] = n
	return nil
}

func (s *phpState) notPlacedCap() error {
	if n := s.stash["n"].(*plcNode); n.placedOn == s.id("T") {
		return fmt.Errorf("the eleventh community Station was placed on a new Tower")
	}
	return nil
}

func (s *phpState) housePrivate() error {
	if err := s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true); err != nil {
		return err
	}
	tw, _ := s.b.tower.registry.Get(s.id("T"))
	if s.b.stationLimitExempt == nil {
		s.b.stationLimitExempt = map[string]bool{}
	}
	s.b.stationLimitExempt[tw.Owner] = true
	return nil
}

func (s *phpState) communityAttaches() error { return s.stationObservedFrom("198.51.100.40") }

func (s *phpState) houseRulesUnchanged() error {
	n := s.stash["n"].(*plcNode)
	ev, err := s.placedEvent(n)
	if err != nil {
		return err
	}
	// The one candidate, judged by the same chooser every placement uses: a community Station
	// on a house Tower, in the same_network class.
	if fmt.Sprint(ev.Detail["group"]) != "community" || fmt.Sprint(ev.Detail["class"]) != placement.SameNetwork.String() {
		return fmt.Errorf("the placement was recorded as %v", ev.Detail)
	}
	return nil
}

func (s *phpState) privateNoTLS() error {
	return s.openAt("T", "198.51.100.40", "192.168.1.20:8444", "", true)
}

func (s *phpState) noStationPlaced() error {
	n, _, _ := s.attachFrom("198.51.100.40")
	if n.placedOn == s.id("T") || s.candidate("T") {
		return fmt.Errorf("a Station was placed on the Tower")
	}
	return nil
}

// --- health ---------------------------------------------------------------------------------

func (s *phpState) towerPrivate() error {
	if err := s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true); err != nil {
		return err
	}
	s.answerPulls("T")
	return nil
}

func (s *phpState) checkDue() error {
	s.b.hubCheckMu.Lock()
	s.b.hubCheckAt = nil
	s.b.hubCheckMu.Unlock()
	s.b.towerHubCheckOnce(time.Now())
	return nil
}

func (s *phpState) notDialed() error {
	// Core never builds a dial for a private hub: the only check recorded is the pull's.
	sum, _ := s.b.tower.health.Summary(s.id("T"), time.Now())
	if sum.LastSource != health.SourcePull {
		return fmt.Errorf("the check landed as %q", sum.LastSource)
	}
	return nil
}

func (s *phpState) noFailure() error {
	sum, _ := s.b.tower.health.Summary(s.id("T"), time.Now())
	if sum.Failed != 0 || len(towerEventsOf(s.t, s.b, s.id("T"), evHubCheckFailed)) != 0 {
		return fmt.Errorf("a failure was recorded: %+v", sum)
	}
	return nil
}

func (s *phpState) checkedByPull() error {
	sum, _ := s.b.tower.health.Summary(s.id("T"), time.Now())
	if sum.Passed < 1 {
		return fmt.Errorf("no pull check passed: %+v", sum)
	}
	return nil
}

func (s *phpState) olderPrivate() error {
	return s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, false)
}

func (s *phpState) recordsNothing() error {
	sum, _ := s.b.tower.health.Summary(s.id("T"), time.Now())
	if !sum.LastAt.IsZero() || sum.Passed+sum.Failed != 0 {
		return fmt.Errorf("Core recorded %+v for an older private-hub Tower", sum)
	}
	return s.noStationPlaced()
}

func (s *phpState) noStations() error { return s.towerPrivate() }

func (s *phpState) daySweeps() error {
	start := time.Now()
	s.stash["dayStart"] = start
	for i := 0; i < 288; i++ {
		s.b.towerHubCheckOnce(start.Add(time.Duration(i) * canaryInterval))
	}
	return nil
}

func (s *phpState) pullChecksRecorded() error {
	sum, _ := s.b.tower.health.Summary(s.id("T"), s.stash["dayStart"].(time.Time).Add(-time.Minute))
	if sum.Passed < 288 || sum.Failed != 0 {
		return fmt.Errorf("a day of sweeps recorded %d passes and %d failures", sum.Passed, sum.Failed)
	}
	return nil
}

// privateLAN is a private address of this machine a Station on it can really dial: a home
// network, or a CI runner's private interface.
func privateLAN() (string, error) {
	ip, err := outboundIPOf()
	if err != nil {
		return "", err
	}
	if placement.HubClassOf([]string{ip}) != placement.HubPrivate {
		return "", fmt.Errorf("this machine's address %s is not private; the canary scenario needs a private interface", ip)
	}
	return ip, nil
}

func outboundIPOf() (string, error) {
	conn, err := net.Dial("udp", "203.0.113.1:9")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// sameNetworkLive stands up a private-hub Tower on this machine's private interface with a real
// share node attached from the same observed network.
func (s *phpState) sameNetworkLive() error {
	ip, err := privateLAN()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		return err
	}
	s.extraCls = append(s.extraCls, func() { _ = ln.Close() })
	// A hub served over TLS with a pin, as a private hub must be.
	if err := s.openAt("T", "198.51.100.40", ln.Addr().String(), phpPin, true); err != nil {
		return err
	}
	lt := s.towers["T"]
	b := s.b
	hubServer := towerhub.NewServer(towerhub.New(), func(grant []byte) (string, string, error) {
		att, station, _, gerr := dispatch.EdgeGrantMeta(grant, b.tower.dispatchPub, link.PublicNetwork, lt.id, time.Now())
		return att, station, gerr
	}, towerhub.ServerOptions{TowerID: lt.id, EpochKey: lt.priv, SubmitTTL: 10 * time.Second, PollTTL: 500 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hubServer.Submit)
	mux.HandleFunc(towerhub.PathPoll, hubServer.Poll)
	mux.HandleFunc(towerhub.PathComplete, hubServer.Complete)
	cert, pin, err := phpHubTLS(s.t)
	if err != nil {
		return err
	}
	// Re-open with the real pin of the certificate the hub presents.
	if err := s.openAt("T", "198.51.100.40", ln.Addr().String(), pin, true); err != nil {
		return err
	}
	hs := &http.Server{Handler: mux, TLSConfig: cert}
	go func() { _ = hs.ServeTLS(ln, "", "") }()
	s.extraCls = append(s.extraCls, func() { _ = hs.Close() })
	s.pullFor("T", mux)

	upstream := phpUpstream(s.t)
	s.extraCls = append(s.extraCls, upstream.Close)
	s.front.setPeer("198.51.100.40")
	nodeOp := signedInOperator(s.t, b, "php-node-op")
	shareNodeID := registerShareNode(s.t, b, nodeOp)
	ctx, cancel := context.WithCancel(context.Background())
	s.extraCls = append(s.extraCls, cancel)
	go func() {
		_ = agent.ServeTower(ctx, agent.Config{NodeID: shareNodeID, Broker: s.front.srv.URL, Model: "php-model",
			Modality: "chat", Upstream: upstream.URL, Parallel: 1}, nodeOp.priv, s.t.TempDir(), io.Discard, nil)
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		ats, aerr := b.tower.stations.ByTower(lt.id)
		if aerr == nil && len(ats) > 0 {
			hubServer.RegisterNode(ats[0].StationID, hubAuthOf(s.t, ats[0]))
			s.stash["station"] = ats[0].StationID
			return nil
		}
		if time.Now().After(deadline) {
			logs := s.logs.String()
			if len(logs) > 3000 {
				logs = logs[len(logs)-3000:]
			}
			var facts []string
			for _, f := range b.placementSnapshot() {
				facts = append(facts, fmt.Sprintf("%s private=%v loc=%+v", f.cand.TowerID, f.private, f.loc))
			}
			tl, _ := b.tower.link.Locality(lt.id)
			ll, _ := b.tower.link.LinkLocality(lt.id)
			return fmt.Errorf("the same-network share node never attached to the private hub; class=%s pulls=%v plane=%+v loc=%+v link=%+v station=%+v snapshot=%v; broker log tail:\n%s",
				b.tower.link.HubClass(lt.id), b.tower.link.Pulls(lt.id), func() link.RelayPlane { p, _ := b.tower.link.RelayPlane(lt.id); return p }(),
				tl, ll, b.addrLocality("198.51.100.40"), facts, logs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *phpState) runsCanary() error {
	s.outcome = s.b.RunCanary(s.id("T"))
	return nil
}

func (s *phpState) canaryByPull() error {
	if s.outcome != reputation.CanaryPass {
		return fmt.Errorf("the canary through the private hub is %q", s.outcome)
	}
	return nil
}

// --- mobility -------------------------------------------------------------------------------------

func (s *phpState) placedSameNetwork() error {
	if err := s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true); err != nil {
		return err
	}
	n, code, raw := s.attachFrom("198.51.100.40")
	if code != http.StatusOK || n.placedOn != s.id("T") {
		return fmt.Errorf("the Station was not placed on the private hub: %d %s", code, raw)
	}
	s.stash["n"] = n
	// It is busy: the move must not wait for idle.
	s.b.metricsMu.Lock()
	if s.b.edgeLoad == nil {
		s.b.edgeLoad = map[string]int{}
	}
	s.b.edgeLoad[n.nodeID] = 1
	s.b.metricsMu.Unlock()
	return nil
}

func (s *phpState) nextRequestElsewhere() error {
	n := s.stash["n"].(*plcNode)
	n.peer = "203.0.112.50"
	// Observed from elsewhere, the Station is not named the private hub again: told to try again
	// while the mobility pass moves it.
	if code, raw := s.attachNode(n); code != http.StatusServiceUnavailable || strings.Contains(raw, "192.168.") {
		return fmt.Errorf("the re-attach answered %d %s", code, raw)
	}
	s.movesNow(time.Now())
	return nil
}

func (s *phpState) triggerLeft() error {
	n := s.stash["n"].(*plcNode)
	if s.moved[n.out.StationID] != triggerLeftNetwork {
		return fmt.Errorf("the move trigger is %q", s.moved[n.out.StationID])
	}
	return nil
}

func (s *phpState) movedWithoutIdle() error {
	n := s.stash["n"].(*plcNode)
	if ok, _ := s.b.stationQuiescent(n.nodeID); ok {
		return fmt.Errorf("the Station was idle, so the scenario proves nothing")
	}
	at, _, _ := s.b.tower.stationStore.ByStation(n.out.StationID)
	if at.Live() {
		return fmt.Errorf("the Station is still live on the private hub")
	}
	return nil
}

func (s *phpState) existingRules() error {
	n := s.stash["n"].(*plcNode)
	mv, ok, err := s.b.tower.mobility.Move(n.out.StationID)
	if err != nil || !ok || mv.From != s.id("T") || mv.Trigger != triggerLeftNetwork {
		return fmt.Errorf("no move row from the private hub: %+v %v", mv, err)
	}
	if len(towerEventsOf(s.t, s.b, s.id("T"), evStationMoveStarted)) == 0 {
		return fmt.Errorf("no station_move_started event")
	}
	return nil
}

func (s *phpState) threeSameNetwork() error {
	if err := s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true); err != nil {
		return err
	}
	var ns []*plcNode
	for i := 0; i < 3; i++ {
		n, code, raw := s.attachFrom("198.51.100.40")
		if code != http.StatusOK || n.placedOn != s.id("T") {
			return fmt.Errorf("Station %d was not placed: %d %s", i+1, code, raw)
		}
		ns = append(ns, n)
	}
	s.stash["ns"] = ns
	return nil
}

func (s *phpState) towerLeavesNetwork() error {
	if err := s.heartbeatFrom("T", "203.0.112.60"); err != nil {
		return err
	}
	s.movesNow(time.Now())
	return nil
}

func (s *phpState) allThreeLeft() error {
	for _, n := range s.stash["ns"].([]*plcNode) {
		if s.moved[n.out.StationID] != triggerLeftNetwork {
			return fmt.Errorf("Station %s got %q", n.out.StationID, s.moved[n.out.StationID])
		}
	}
	return nil
}

func (s *phpState) onPublicSameContinent() error {
	// P is in NA (203.0.113.0/24), the Station in NA on another /24: same_continent.
	if err := s.publicTower("P", "203.0.113.20"); err != nil {
		return err
	}
	// A node that hears the move nudge: better_tower moves only those (station_mobility.feature).
	n := s.newNode("203.0.112.40", false)
	n.body["nudge"] = true
	if code, raw := s.attachNode(n); code != http.StatusOK || n.placedOn != s.id("P") {
		return fmt.Errorf("the Station was not placed on the public Tower: %d %s", code, raw)
	}
	s.b.noteCancelCapable(n.nodeID)
	s.stash["n"] = n
	return nil
}

func (s *phpState) privateOnStationsNetwork() error {
	return s.openAt("T", "203.0.112.40", "192.168.1.20:8444", phpPin, true)
}

func (s *phpState) threeObservations() error { return s.observe(3) }

// observe records fresh health observations of the public Tower and runs a mobility pass after
// each, as the mobility runner does.
func (s *phpState) observe(k int) error {
	at := time.Now()
	s.answerPulls("T")
	for i := 1; i <= k; i++ {
		obs := at.Add(time.Duration(i) * canaryInterval)
		for _, name := range []string{"P", "T"} {
			if err := s.b.tower.health.RecordCheck(s.id(name), health.Check{OK: true, At: obs,
				Source: health.SourceHubCheck, Slot: healthSlot(obs)}); err != nil {
				return err
			}
		}
		_ = s.heartbeatFrom("P", s.towerPeer["P"])
		_ = s.heartbeatFrom("T", s.towerPeer["T"])
		s.movesNow(obs)
	}
	return nil
}

func (s *phpState) movedBetter() error {
	n := s.stash["n"].(*plcNode)
	mv, ok, _ := s.b.tower.mobility.Move(n.out.StationID)
	if !ok || mv.Trigger != triggerBetter || mv.To != s.id("T") {
		return fmt.Errorf("the move is %+v (found %v), want better_tower to the private hub", mv, ok)
	}
	return nil
}

func (s *phpState) stationOtherNetwork() error {
	if err := s.publicTower("P", "203.0.113.20"); err != nil {
		return err
	}
	n, code, raw := s.attachFrom("203.0.112.40")
	if code != http.StatusOK {
		return fmt.Errorf("attach answered %d %s", code, raw)
	}
	s.stash["n"] = n
	return s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true)
}

func (s *phpState) neverNamed() error {
	n := s.stash["n"].(*plcNode)
	f, _, _ := s.b.tower.mobility.Facts(n.out.StationID)
	owner := hexOf(n.op.priv.Public().(ed25519.PublicKey))
	if alt, ok := s.b.chooseFrom(s.b.placementSnapshot(), owner, f.Locality, map[string]bool{s.id("P"): true}, "", nil); ok && alt.towerID == s.id("T") {
		return fmt.Errorf("the chooser named the private hub for a Station on another network")
	}
	if err := s.observe(3); err != nil {
		return err
	}
	if mv, ok, _ := s.b.tower.mobility.Move(n.out.StationID); ok && mv.To == s.id("T") {
		return fmt.Errorf("a move named the private hub: %+v", mv)
	}
	return nil
}

// --- admin ----------------------------------------------------------------------------------

func (s *phpState) adminOpens() error {
	if err := s.threeSameNetwork(); err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodGet, s.srv.URL+"/admin/tower?id="+s.id("T"), nil)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("admin detail answered %d %s", resp.StatusCode, raw)
	}
	s.stash["detail"] = string(raw)
	return nil
}

func (s *phpState) showsClassAndNetwork() error {
	var d struct {
		Hub map[string]any `json:"hub"`
	}
	raw := s.stash["detail"].(string)
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return err
	}
	if d.Hub["class"] != "private" || d.Hub["network_measured"] != true {
		return fmt.Errorf("the hub view is %v", d.Hub)
	}
	// Never the network itself (placement_locality.feature, "Locality is kept coarse").
	return plcNoAddr("the admin Tower detail", raw)
}

func (s *phpState) showsCounts() error {
	var d struct {
		Hub map[string]any `json:"hub"`
	}
	_ = json.Unmarshal([]byte(s.stash["detail"].(string)), &d)
	if d.Hub["stations_placed"] != float64(3) || d.Hub["stations_on_network"] != float64(3) {
		return fmt.Errorf("the hub view is %v", d.Hub)
	}
	return nil
}

// --- the runner -------------------------------------------------------------------------------

func TestPrivateHubPlacementBDD(t *testing.T) {
	s := &phpState{plcState: &plcState{t: t}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				s.towerPeer, s.moved, s.stopPull, s.extraCls, s.outcome = map[string]string{}, map[string]string{}, nil, nil, ""
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				for _, stop := range s.stopPull {
					stop()
				}
				for i := len(s.extraCls) - 1; i >= 0; i-- {
					s.extraCls[i]()
				}
				s.teardown()
				return ctx, nil
			})
			sc.Step(`^Roger Core runs two instances sharing one store$`, s.twoInstances)
			sc.Step(`^a Tower whose session carries the "pull" capability$`, s.pullTower)
			sc.Step(`^the Tower advertises hub address "([^"]*)"$`, s.advertises)
			sc.Step(`^Core resolves "hub.lan" to 192.168.1.20$`, s.resolvesHubLan)
			sc.Step(`^Core classes the hub as "([^"]+)"$`, s.classesAs)
			sc.Step(`^the Tower advertises a loopback hub address$`, s.loopbackHub)
			sc.Step(`^no Station is placed on it, because Core cannot tell the same machine from the same network$`, s.noStationLoopback)
			sc.Step("^`roger tower serve` tells the operator to advertise the machine's LAN address instead$", phpCLIHalf)
			sc.Step(`^the Tower advertises no hub address$`, s.noHub)
			sc.Step("^no Station is placed on it, and `roger tower serve` says the hub must be running to carry Stations$", s.noStationNoHub)
			sc.Step(`^the Tower advertises a private hub address$`, s.privateHub)
			sc.Step(`^Core accepts its link session from public address 198.51.100.40$`, s.acceptsFrom40)
			sc.Step(`^the Tower's network bucket and continent are computed from 198.51.100.40$`, s.computedFrom40)
			sc.Step(`^the private hub address plays no part in its location$`, s.hubAddrNoPart)
			sc.Step(`^a private-hub Tower's request carries a forwarded-for header naming another address$`, s.forwardedFor)
			sc.Step(`^Core uses the address its trusted front proxy recorded, as it does for every caller$`, s.usesProxyAddress)
			sc.Step(`^a private-hub Tower measured at 198.51.100.40$`, s.measuredAt40)
			sc.Step(`^its next link request is observed from 198.51.100.77$`, s.nextFrom77)
			sc.Step(`^its location is re-measured from 198.51.100.77, on every instance$`, s.remeasured77)
			sc.Step(`^Stations placed on it under the old address are re-checked against the new one$`, s.stationsRechecked)
			sc.Step(`^the Tower's link is observed from "([^"]+)"$`, s.towerObservedFrom)
			sc.Step(`^a Station attaches observed from "([^"]+)"$`, s.stationObservedFrom)
			sc.Step(`^the private-hub Tower (is|is not) a placement candidate for that Station$`, s.candidateOutcome)
			sc.Step(`^a private-hub Tower and a Station on a different network$`, s.privateAndOther)
			sc.Step(`^the Station attaches$`, s.stationAttaches)
			sc.Step(`^the attach answer never names the private hub$`, s.attachNeverNamesPrivate)
			sc.Step(`^the Station is placed on its best eligible public-hub Tower, or stays direct$`, s.placedOnPublic)
			sc.Step(`^a Station on the same network as a private-hub Tower in the same health tier as every other Tower$`, s.sameNetworkSameTier)
			sc.Step(`^Core ranks Towers for the Station$`, s.coreRanks)
			sc.Step(`^the private-hub Tower is in the same_network class, ahead of every other class$`, s.rankedSameNetwork)
			sc.Step(`^a same-network private-hub Tower in health tier B and a public-hub Tower in tier A$`, s.tierBPrivateTierAPublic)
			sc.Step(`^the tier A Tower wins, as placement_ranking.feature already says$`, s.tierAWins)
			sc.Step(`^a community private-hub Tower at stage "new" already holding 10 community Stations$`, s.newStageTen)
			sc.Step(`^an eleventh same-network community Station attaches$`, s.eleventh)
			sc.Step(`^it is not placed there, exactly as the stage cap says$`, s.notPlacedCap)
			sc.Step(`^a house private-hub Tower and a community Station on its network$`, s.housePrivate)
			sc.Step(`^the community Station attaches$`, s.communityAttaches)
			sc.Step(`^the house and community preferences in placement_ranking.feature decide, unchanged$`, s.houseRulesUnchanged)
			sc.Step(`^a private-hub Tower that serves its hub without TLS$`, s.privateNoTLS)
			sc.Step(`^no Station is placed on it$`, s.noStationPlaced)
			sc.Step("^`roger tower serve` tells the operator a private hub needs --hub-tls$", phpCLIHalf)
			sc.Step(`^a Tower advertises a private hub address$`, s.towerPrivate)
			sc.Step(`^its hub check is due$`, s.checkDue)
			sc.Step(`^Core does not dial the private address$`, s.notDialed)
			sc.Step(`^no failed hub check is recorded for it$`, s.noFailure)
			sc.Step(`^the Tower is checked by pull, as tower_pull_dispatch.feature says$`, s.checkedByPull)
			sc.Step(`^a Tower without the "pull" capability advertising a private hub address$`, s.olderPrivate)
			sc.Step(`^Core records nothing for it, no pass and no failure, and places no Station on it$`, s.recordsNothing)
			sc.Step(`^a private-hub Tower with no Stations attached$`, s.noStations)
			sc.Step(`^a day of hub check sweeps runs$`, s.daySweeps)
			sc.Step(`^its pull hub checks are recorded, so it can be promoted on the same terms as any Tower$`, s.pullChecksRecorded)
			sc.Step(`^a private-hub Tower with a same-network Station attached$`, s.sameNetworkLive)
			sc.Step(`^Core runs a canary against that Station$`, s.runsCanary)
			sc.Step(`^the canary is delivered by pull and counts exactly as any canary does$`, s.canaryByPull)
			sc.Step(`^a Station placed on a private-hub Tower while on the same network$`, s.placedSameNetwork)
			sc.Step(`^the Station's next request to Core is observed from a different network$`, s.nextRequestElsewhere)
			sc.Step(`^a Station placed on a private-hub Tower whose network matched by public address, behind a carrier NAT$`, s.placedBehindCGNAT)
			sc.Step(`^the Station's node cannot reach the hub and asks Core again, naming that Tower$`, s.cannotReachHub)
			sc.Step(`^Core does not name that hub again and tells the node to try again shortly$`, s.hubNotNamedAgain)
			sc.Step(`^the mobility pass moves the Station off that Tower with the trigger "unreachable", under the pacing every move follows$`, s.movedUnreachable)
			sc.Step(`^placement does not offer that Tower to the Station for 24 hours$`, s.avoidedForADay)
			sc.Step(`^a Station placed on a private-hub Tower$`, func() error { return nil })
			sc.Step(`^its node reports the hub unreachable naming a certificate pin that is not the Tower's current one$`, func() error { return nil })
			sc.Step(`^Core answers with the Tower's current pin, as for a certificate the Tower rotated, and keeps nothing off$`, func() error {
				return runSub(t, TestARotatedCertificateIsNotTakenForAnUnreachableHub)
			})
			sc.Step(`^its node reports the hub unreachable and no other Tower can take the Station$`, func() error { return nil })
			sc.Step(`^Core answers as before, with the only hub there is, and keeps nothing off$`, func() error {
				return runSub(t, TestAnUnreachableStationWithNowhereElseIsAnsweredAsBefore)
			})
			sc.Step(`^its node reports the hub unreachable while the Tower's own link is down, or for a public hub$`, func() error { return nil })
			sc.Step(`^Core answers as before: a Tower that is down is down for everyone, and a public hub is Core's to check$`, func() error {
				return runSub(t, TestAnUnreachableReportIsBelievedOnlyForALivePrivateHub)
			})
			sc.Step(`^Core records the move trigger "left_network"$`, s.triggerLeft)
			sc.Step(`^moves the Station to its best eligible Tower without proving it idle, because it can no longer reach the hub$`, s.movedWithoutIdle)
			sc.Step(`^the move follows the existing pacing, nudge and epoch rules in station_mobility.feature$`, s.existingRules)
			sc.Step(`^a private-hub Tower with three same-network Stations$`, s.threeSameNetwork)
			sc.Step(`^the Tower's link is observed from a different network and the Stations' addresses do not change$`, s.towerLeavesNetwork)
			sc.Step(`^all three Stations get the move trigger "left_network"$`, s.allThreeLeft)
			sc.Step(`^a Station on a public-hub Tower in the same_continent class$`, s.onPublicSameContinent)
			sc.Step(`^a private-hub Tower on the Station's own network, eligible and in the same health tier$`, s.privateOnStationsNetwork)
			sc.Step(`^three fresh observations agree$`, s.threeObservations)
			sc.Step(`^the Station is moved to the private-hub Tower under the existing better_tower rules$`, s.movedBetter)
			sc.Step(`^a Station on a different network from a private-hub Tower$`, s.stationOtherNetwork)
			sc.Step(`^no trigger ever names that Tower as the Station's destination$`, s.neverNamed)
			sc.Step(`^an admin opens the Tower detail$`, s.adminOpens)
			sc.Step(`^it shows the hub class \(public, private, loopback or none\) and the network bucket Core measured$`, s.showsClassAndNetwork)
			sc.Step(`^for a private hub, how many Stations are on its network and placed there$`, s.showsCounts)
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t,
			Paths: bddrun.Scenarios("../../features/tower/private_hub_placement.feature", bddrun.Titled(true, "The Station proves the hub before it sends anything", "A Station never dials a private address Core did not hand it", "Serve says plainly who this Tower can serve"))},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("private_hub_placement.feature failed")
	}
}

// phpCLIHalf marks a step the operator-side runner (internal/towercli) checks for the same
// scenario: what `roger tower serve` prints.
func phpCLIHalf() error { return nil }

// phpHubTLS mints a self-signed hub certificate and returns its TLS config and pin.
func phpHubTLS(t *testing.T) (*tls.Config, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), Subject: pkix.Name{CommonName: "hub"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, "", err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, "", err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}}}, towerhub.CertPin(cert), nil
}

// phpUpstream is the model behind the same-network Station.
func phpUpstream(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong on the home network"}}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}))
}

// Review finding: a private-hub Tower whose own network is unknown (a mirror error, not yet
// measured, or a local link address) is not evidence that its Stations left. Only two known
// networks that differ trigger left_network.
func TestAnUnknownTowerNetworkMovesNoStation(t *testing.T) {
	s := &phpState{plcState: &plcState{t: t}, towerPeer: map[string]string{}, moved: map[string]string{}}
	s.reset()
	t.Cleanup(s.teardown)
	s.b.canaryVet = vetPublicIP
	require.NoError(t, s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true))
	n, code, raw := s.attachFrom("198.51.100.40")
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, s.id("T"), n.placedOn)
	require.True(t, s.b.tower.link.SetLocality(s.id("T"), s.sess["T"], placement.Locality{}))
	s.movesNow(time.Now())
	require.Empty(t, s.moved[n.out.StationID], "an unknown Tower network moved a Station")
}

// Audit finding: a live Station's re-attach answered with its Tower's endpoint unchecked, so a
// Station that had left a private hub's network was named that hub until the next mobility pass.
// Off the network it is now moved (left_network) and told to try again, never handed the hub; on
// the network it is answered as before.
func TestAReattachFromAnotherNetworkIsNeverHandedThePrivateHub(t *testing.T) {
	s := &phpState{plcState: &plcState{t: t}, towerPeer: map[string]string{}, moved: map[string]string{}}
	s.reset()
	t.Cleanup(s.teardown)
	s.b.canaryVet = vetPublicIP
	require.NoError(t, s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true))
	n, code, raw := s.attachFrom("198.51.100.40")
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, s.id("T"), n.placedOn)

	code, raw = s.attachNode(n) // the same network again
	require.Equal(t, http.StatusOK, code, raw)
	require.Contains(t, raw, "192.168.1.20:8444")

	n.peer = "203.0.113.50" // the Station moved to another network
	code, raw = s.attachNode(n)
	require.Equal(t, http.StatusServiceUnavailable, code, raw)
	require.NotContains(t, raw, "192.168.1.20", "a Station off the network was named the private hub")
	moves, err := s.b.tower.mobility.Pending()
	require.NoError(t, err)
	require.Empty(t, moves, "the attach answer starts no move: the paced mobility pass does")
	s.b.mobilityPassOnce(time.Now())
	moves, err = s.b.tower.mobility.Pending()
	require.NoError(t, err)
	require.Len(t, moves, 1)
	require.Equal(t, triggerLeftNetwork, moves[0].Trigger)
	require.Equal(t, n.out.StationID, moves[0].StationID)
}

// --- a hub the Station cannot reach -----------------------------------------------------------

func (s *phpState) placedBehindCGNAT() error {
	// One public address shared by the Tower's household and an unrelated Station's, as a carrier
	// NAT does: the networks match, the hub is still out of the Station's reach.
	if err := s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true); err != nil {
		return err
	}
	if err := s.openAt("U", "203.0.113.30", "203.0.113.30:8444", "", true); err != nil {
		return err
	}
	n, code, raw := s.attachFrom("198.51.100.40")
	if code != http.StatusOK || n.placedOn != s.id("T") {
		return fmt.Errorf("the Station was placed on %q (%d %s), want the private-hub Tower", n.placedOn, code, raw)
	}
	s.stash["n"] = n
	return nil
}

func (s *phpState) cannotReachHub() error {
	n := s.stash["n"].(*plcNode)
	n.body["unreachable_tower"], n.body["unreachable_pin"] = s.id("T"), phpPin // the pin it was handed
	code, raw := s.attachNode(n)
	s.stash["code"], s.stash["raw"] = code, raw
	return nil
}

func (s *phpState) hubNotNamedAgain() error {
	code, raw := s.stash["code"].(int), s.stash["raw"].(string)
	if code != http.StatusServiceUnavailable || strings.Contains(raw, "192.168.") {
		return fmt.Errorf("the re-attach answered %d %s", code, raw)
	}
	return nil
}

func (s *phpState) movedUnreachable() error {
	n := s.stash["n"].(*plcNode)
	s.movesNow(time.Now())
	if s.moved[n.out.StationID] != triggerUnreachable {
		return fmt.Errorf("the move trigger is %q, want %q", s.moved[n.out.StationID], triggerUnreachable)
	}
	return nil
}

func (s *phpState) avoidedForADay() error {
	n := s.stash["n"].(*plcNode)
	now := time.Now()
	if _, _, excl := s.b.moveOnReattach(n.out.StationID, now.Add(23*time.Hour)); !excl[s.id("T")] {
		return fmt.Errorf("the unreachable Tower is offered again within a day")
	}
	if s.b.avoidFor(n.out.StationID, now.Add(25*time.Hour))[s.id("T")] {
		return fmt.Errorf("the unreachable Tower is still kept off after a day")
	}
	// Nor is it the "better" Tower an idle move would send the Station back to.
	f, _, _ := s.b.tower.mobility.Facts(n.out.StationID)
	excl := s.b.avoidFor(n.out.StationID, now.Add(time.Hour))
	if alt, ok := s.b.chooseFrom(s.b.placementSnapshot(), "", f.Locality, excl, "", nil); ok && alt.cand.TowerID == s.id("T") {
		return fmt.Errorf("placement chose the unreachable Tower")
	}
	return nil
}

// Audit findings: with no other Tower to take it, an unreachable Station was taken dark and placed
// back every pass, and then told to try again for the whole day. With nowhere else to go it is
// answered as before, the only hub there is, and nothing is kept off.
func TestAnUnreachableStationWithNowhereElseIsAnsweredAsBefore(t *testing.T) {
	s := &phpState{plcState: &plcState{t: t}, towerPeer: map[string]string{}, moved: map[string]string{}}
	s.reset()
	t.Cleanup(s.teardown)
	s.b.canaryVet = vetPublicIP
	require.NoError(t, s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true))
	n, code, raw := s.attachFrom("198.51.100.40")
	require.Equal(t, http.StatusOK, code, raw)
	n.body["unreachable_tower"], n.body["unreachable_pin"] = s.id("T"), phpPin
	code, raw = s.attachNode(n)
	require.Equal(t, http.StatusOK, code, raw)
	tw, _, _ := s.b.tower.mobility.Avoid(n.out.StationID)
	require.Empty(t, tw, "a Station with nowhere else to go was kept off its only Tower")
	s.movesNow(time.Now())
	require.Empty(t, s.moved[n.out.StationID], "a Station with nowhere else to go was moved")
}

// Audit finding: a pin mismatch looks the same to the node whether a stranger's device answered or
// the Tower rotated its certificate. Core tells them apart: when the pin the node was using is not
// the Tower's current one, the node is handed the new pin and nothing is kept off.
func TestARotatedCertificateIsNotTakenForAnUnreachableHub(t *testing.T) {
	s := &phpState{plcState: &plcState{t: t}, towerPeer: map[string]string{}, moved: map[string]string{}}
	s.reset()
	t.Cleanup(s.teardown)
	s.b.canaryVet = vetPublicIP
	require.NoError(t, s.openAt("T", "198.51.100.40", "192.168.1.20:8444", phpPin, true))
	require.NoError(t, s.openAt("U", "203.0.113.30", "203.0.113.30:8444", "", true))
	n, code, raw := s.attachFrom("198.51.100.40")
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, s.id("T"), n.placedOn)
	n.body["unreachable_tower"], n.body["unreachable_pin"] = s.id("T"), strings.Repeat("cd", 32) // an older pin
	code, raw = s.attachNode(n)
	require.Equal(t, http.StatusOK, code, raw)
	require.Contains(t, raw, phpPin, "the node was not handed the Tower's current pin")
	tw, _, _ := s.b.tower.mobility.Avoid(n.out.StationID)
	require.Empty(t, tw, "a certificate rotation kept the Station off its Tower")
}

// Audit finding: any 90 s hub outage, a Tower restarting for one, sent every Station away for a
// day. Only a PRIVATE hub (which Core cannot dial) on a Tower whose link is up is believed.
func TestAnUnreachableReportIsBelievedOnlyForALivePrivateHub(t *testing.T) {
	s := &phpState{plcState: &plcState{t: t}, towerPeer: map[string]string{}, moved: map[string]string{}}
	s.reset()
	t.Cleanup(s.teardown)
	s.b.canaryVet = vetPublicIP
	require.NoError(t, s.openAt("P", "203.0.113.30", "203.0.113.30:8444", "", true))
	n, code, raw := s.attachFrom("198.51.100.77")
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, s.id("P"), n.placedOn)
	n.body["unreachable_tower"] = s.id("P")
	code, raw = s.attachNode(n)
	require.Equal(t, http.StatusOK, code, "a public hub is Core's to check, not the Station's word: %s", raw)
	tw, _, _ := s.b.tower.mobility.Avoid(n.out.StationID)
	require.Empty(t, tw, "a public hub was avoided on a Station's report")
}

// runSub runs a focused test as a scenario step, so a scenario and its regression test are one proof.
func runSub(t *testing.T, f func(*testing.T)) error {
	if !t.Run("step", f) {
		return fmt.Errorf("the scenario's check failed (see the subtest above)")
	}
	return nil
}
