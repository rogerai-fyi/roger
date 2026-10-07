package main

// A LAN-direct Edge member's face for the LAN routing runner: an HTTPS listener whose certificate
// is pinned in the fleet record and which REQUIRES a client certificate, so a turn it answers was
// carried over mutual TLS. It answers /edge/infer as the member's serve; a delay makes it slow.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

type edgeFace struct {
	srv       *httptest.Server
	pin       string
	hits      atomic.Int32
	mutualTLS atomic.Int32 // turns that arrived with the caller's certificate
	lastModel atomic.Value // the model the last turn named
}

func (f *edgeFace) addr() string { return strings.TrimPrefix(f.srv.URL, "https://") }

func newEdgeFace(t *testing.T, node string, delay time.Duration) *edgeFace {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: node},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	f := &edgeFace{pin: fmt.Sprintf("%x", sha256.Sum256(der))}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/edge/infer" {
			http.NotFound(w, r)
			return
		}
		f.hits.Add(1)
		var p struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.lastModel.Store(p.Model)
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			f.mutualTLS.Add(1)
		}
		time.Sleep(delay)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"from `+node+`"}}]}`)
	}))
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		ClientAuth: tls.RequireAnyClientCert}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

// memberAt records a VERIFIED Edge member serving band whose LAN face is at addr, pinned to pin.
func (s *lanDialBDD) memberAt(node, band, addr, pin string) error {
	if err := s.enroll(); err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	id := "n_" + node + strings.Repeat("0", 24-len(node))
	obs := edge.Observation{Name: node, Kind: "host", Addr: addr, Fingerprint: pin, Instances: []edge.Instance{{Name: "serve",
		Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Claimed)}}, Bands: []string{band}}}}
	if _, err := st.fleet.Observe(id, obs); err != nil {
		return err
	}
	if err := st.fleet.RecordInstanceProbe(id, "serve", edge.Serve, true); err != nil {
		return err
	}
	if err := st.fleet.RecordServeProbe(id, "serve", []string{band}); err != nil {
		return err
	}
	return st.save()
}
