package edge

// THE SERVING PROBE (features/edge/serving_probe.feature; instances.feature §2). An instance's
// `serve` is CLAIMED until a probe passes against it; this file is that probe. The face resolves a
// band to the server behind it - the very Upstream a peer's turn would be proxied to - and asks that
// server for its model list. It passes only when the server answers and lists the band. Nothing is
// generated, no token is spent, no session is receipted. Discovery runs it after a node is
// certificate-verified (verifyServe); the host runs it on its own household (observeSelf). Neither
// ever takes the node's word: the peer's asserted state is dropped on the floor in applyInstances,
// and only a pass recorded HERE lifts serve to VERIFIED.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProbePath is the member-only read that proves a band is really served here.
const ProbePath = "/edge/probe"

// probeTimeout bounds the whole probe, including the hop to the server behind the face.
const probeTimeout = 8 * time.Second

// ProbeResult is the face's answer to a probe that passed.
type ProbeResult struct {
	Instance string `json:"instance"`
	Band     string `json:"band"`
	Served   bool   `json:"served"`
}

// ProbeFunc is how discovery asks a verified peer to prove a band: dial addr pinned to pin, present
// the caller's certificate, and return the instance that serves band. It is a seam so a test can
// drive passes and failures deterministically; production is ProbeServe.
type ProbeFunc func(ctx context.Context, addr, pin string, caller *tls.Certificate, band string) (instance string, err error)

// handleProbe answers GET /edge/probe?band=<band> for a member.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		inferError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, status, msg := s.caller(r); status != 0 {
		inferError(w, status, msg)
		return
	}
	band := strings.TrimSpace(r.URL.Query().Get("band"))
	if band == "" {
		inferError(w, http.StatusBadRequest, "a serving probe names the band it checks")
		return
	}
	inst, status, msg := s.ProbeBand(r.Context(), band)
	if status != 0 {
		inferError(w, status, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ProbeResult{Instance: inst, Band: band, Served: true})
}

// ProbeBand runs the serving probe for one band on THIS node: it resolves the band through the
// face's upstream map and asks the server behind it for its model list. A zero status is a pass and
// names the instance that serves the band; otherwise status and msg are the refusal, in the same
// shape the infer path uses (404 not served here, 502 the server behind the face did not prove it).
func (s *Server) ProbeBand(ctx context.Context, band string) (instance string, status int, msg string) {
	if s.serving == nil || s.serving.Upstream == nil {
		return "", http.StatusNotFound, "this node does not serve " + band
	}
	chatURL, key, inst, ok := s.serving.Upstream(band)
	if !ok {
		return "", http.StatusNotFound, "this node does not serve " + band + "; it serves: " + strings.Join(s.servedBands(), ", ")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL(chatURL), nil)
	if err != nil {
		return "", http.StatusBadGateway, "the server behind " + band + " could not be asked: " + err.Error()
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", http.StatusBadGateway, "the server behind " + band + " did not answer: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", http.StatusBadGateway, fmt.Sprintf("the server behind %s answered %d to its model list", band, resp.StatusCode)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // a model list, not a download
	if json.Unmarshal(body, &list) != nil {
		return "", http.StatusBadGateway, "the server behind " + band + " did not answer with a model list"
	}
	for _, m := range list.Data {
		if m.ID == band {
			return inst, 0, ""
		}
	}
	return "", http.StatusBadGateway, "the server behind " + band + " does not list it"
}

// modelsURL is the model-list endpoint next to an OpenAI-compatible chat URL.
func modelsURL(chatURL string) string {
	return strings.TrimSuffix(strings.TrimRight(chatURL, "/"), "/chat/completions") + "/models"
}

// ProbeServe is the production ProbeFunc: GET /edge/probe on a peer's face over mutual TLS, the
// face pinned to the fingerprint the fleet recorded BEFORE any byte is sent, the caller identified
// by this node's certificate. It returns the instance the face named, or the face's own reason.
func ProbeServe(ctx context.Context, addr, pin string, caller *tls.Certificate, band string) (string, error) {
	if caller == nil {
		return "", fmt.Errorf("no certificate to present")
	}
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, // verified below, against the pin, before any request goes out
		MinVersion:         tls.VersionTLS12,
		Certificates:       []tls.Certificate{*caller},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("the peer presented no certificate")
			}
			if fp := fmt.Sprintf("%x", sha256.Sum256(cs.PeerCertificates[0].Raw)); fp != pin {
				return fmt.Errorf("the peer's certificate does not match its pin")
			}
			return nil
		},
	}
	tr := &http.Transport{TLSClientConfig: tlsCfg, DisableKeepAlives: true,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, probeTimeout+2*time.Second)
	defer cancel()
	u := "https://" + addr + ProbePath + "?band=" + url.QueryEscape(band)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error.Message == "" {
			e.Error.Message = resp.Status
		}
		return "", fmt.Errorf("probe refused: %s", e.Error.Message)
	}
	var res ProbeResult
	if json.Unmarshal(body, &res) != nil || !res.Served || res.Instance == "" {
		return "", fmt.Errorf("the face answered with something that is not a probe result")
	}
	return res.Instance, nil
}
