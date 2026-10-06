package main

// Shared LAN-mode fixtures: a REAL standalone Tower (internal/localplane over a real
// tower.State) served in-process, and a dial map that lets a scenario's LAN address
// (192.168.1.20:8787) reach it while the local-address rule still judges the scenario's
// address. Unmapped addresses refuse the connection, like nothing listening.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

// lanDialMap maps a scenario address to the real listener behind it.
type lanDialMap struct {
	mu sync.Mutex
	to map[string]string
}

func (m *lanDialMap) set(scenarioAddr, real string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.to == nil {
		m.to = map[string]string{}
	}
	m.to[scenarioAddr] = real
}

func (m *lanDialMap) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	m.mu.Lock()
	real, ok := m.to[addr]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("dial %s: connection refused", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, real)
}

func (m *lanDialMap) client() *http.Client {
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: m.dial}}
}

// realTower is a standalone Tower: its state, and its consumer plane served on loopback.
type realTower struct {
	st  *tower.State
	srv *httptest.Server
}

func newRealTower(t *testing.T) *realTower {
	t.Helper()
	dir := t.TempDir()
	if _, err := tower.Init(dir, tower.ModeStandalone); err != nil {
		t.Fatal(err)
	}
	st, err := tower.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(localplane.New(st).Handler())
	t.Cleanup(srv.Close)
	return &realTower{st: st, srv: srv}
}

// addr is the listener's host:port.
func (r *realTower) addr() string { return r.srv.Listener.Addr().String() }

// admit admits a client key on the Tower.
func (r *realTower) admit(pub ed25519.PublicKey) error {
	keyHash := protocol.UserIDFromPubkey(hex.EncodeToString(pub))
	inv, code, err := r.st.CreateInvitation(keyHash, time.Hour, 5)
	if err != nil {
		return err
	}
	_, err = r.st.ConsumeInvitation(inv.ID, code, keyHash)
	return err
}

// attach attaches a station serving models.
func (r *realTower) attach(id string, models ...string) error {
	_, err := r.st.AttachStation(id, "sk-"+id, models)
	return err
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}
