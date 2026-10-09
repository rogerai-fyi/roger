package main

// A LAN-direct Edge member's face for the LAN routing runner: an HTTPS listener whose certificate
// is pinned in the fleet record and which REQUIRES a client certificate, so a turn it answers was
// carried over mutual TLS. It answers /edge/infer as the member's serve; a delay makes it slow.

import (
	"crypto/ed25519"
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
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type edgeFace struct {
	srv       *httptest.Server
	pin       string
	hits      atomic.Int32
	mutualTLS atomic.Int32 // turns that arrived with the caller's certificate
	lastModel atomic.Value // the model the last turn named
	pub       ed25519.PublicKey
	// speech: /edge/speech answers (features/lan/voice.feature). speechMode "" = audio,
	// "fail" = a 502 before any audio, "cut" = the connection drops partway through the audio.
	speechHits atomic.Int32
	speechMode string
}

func (f *edgeFace) addr() string { return strings.TrimPrefix(f.srv.URL, "https://") }

func newEdgeFace(t *testing.T, node string, delay time.Duration) *edgeFace {
	t.Helper()
	// An ed25519 identity, as a real member's: its local receipt is signed with the same key the
	// certificate carries, so the caller can check the receipt against the face it dialed.
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: node},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	f := &edgeFace{pin: fmt.Sprintf("%x", sha256.Sum256(der)), pub: pub}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/edge/speech" {
			f.speech(w, r, node, key)
			return
		}
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
		rec := protocol.UsageReceipt{RequestID: "edge-" + node + "-" + fmt.Sprint(f.hits.Load()), NodeID: node,
			Model: p.Model, Local: true, TS: time.Now().Unix()}
		rec.SignNode(key)
		w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(rec))
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"from `+node+`"}}]}`)
	}))
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		ClientAuth: tls.RequireAnyClientCert}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

// memberAt records a VERIFIED Edge member serving band whose LAN face is at addr, pinned to pin.
// A quant, when given, is the label the member's own serve record carries for band.
func (s *lanDialBDD) memberAt(node, band, addr, pin string, quant ...string) error {
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
	if len(quant) > 0 {
		obs.Instances[0].Model = &store.EdgeModel{Band: band, Quant: quant[0]}
	}
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

// speech answers a member's speech request as a voice-serving member's face does.
func (f *edgeFace) speech(w http.ResponseWriter, r *http.Request, node string, key ed25519.PrivateKey) {
	f.speechHits.Add(1)
	var p struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&p)
	f.lastModel.Store(p.Model)
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		f.mutualTLS.Add(1)
	}
	switch f.speechMode {
	case "fail":
		w.WriteHeader(http.StatusBadGateway)
		return
	case "cut":
		// headers and part of the audio, then the member goes away
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "RIFF partial")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	rec := protocol.UsageReceipt{RequestID: "edge-" + node + "-speech-" + fmt.Sprint(f.speechHits.Load()), NodeID: node,
		Model: p.Model, Local: true, TS: time.Now().Unix()}
	rec.SignNode(key)
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(rec))
	_, _ = io.WriteString(w, "RIFF voice of "+node)
}
