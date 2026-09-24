package edge

// LOCAL INFERENCE - a peer serves a turn at its own LAN face (features/edge/
// local_inference.feature). This is rung 2 of the ladder: a member of this Edge asks an
// instance that serves the band for a completion, over mutual TLS, with no broker, no
// internet and no account lookup.
//
// THE WIRE. The caller presents ITS node certificate; the face checks it chains to the
// Edge's root and names a member of this fleet. The caller, for its part, checked this
// face's certificate against the pin the fleet recorded before it sent a byte. Nothing else
// authenticates a peer turn: a certificate is the one credential this layer already has.
//
// THE RECEIPT. Rung 2 moves no money, but everything drawn was receipted, so a peer turn
// produces a LOCAL RECEIPT: the same shape as a broker receipt, signed by the SERVING
// NODE's key, cost zero, marked local. It rides the X-RogerAI-Receipt header on a plain
// reply and the `: rogerai-receipt=` comment at a stream's end - exactly where the broker
// puts its own, so the caller's proxy reads both with one parser.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// InferPath is the face's serving endpoint.
const InferPath = "/edge/infer"

const (
	inferBodyCap = 4 << 20 // the local proxy's own cap; a peer is not more trusted with memory
	inferTimeout = 10 * time.Minute
)

// Upstream is where a band is served on this node: the OpenAI-compatible URL of the local
// server that has it loaded, its key if any, and the instance that has it on air. ok is
// false when nothing here serves the band.
type Upstream func(band string) (chatURL, key, instance string, ok bool)

// Serving is what the face needs to serve peers: the trust to authenticate them, the
// membership to admit them, the upstreams to answer with, and the key to receipt with.
type Serving struct {
	Trust *cert.Authority // the verify-only authority: the TLS root pool, set once
	// TrustNow, when set, returns a FRESH authority per request so a serial revoked while the face
	// runs is refused at once, not only after a restart (audit 2026-09-23). Falls back to Trust.
	TrustNow func() *cert.Authority
	Member   func(nodeID string) bool // is that node on THIS Edge (account scope)
	Upstream Upstream
	NodeID   string
	Key      ed25519.PrivateKey // the node key that signs local receipts
	Now      func() time.Time
}

// SetServing arms the infer path. Without it the face answers describe only.
func (s *Server) SetServing(sv Serving) { s.serving = &sv }

// tlsConfigServing is the listener's TLS with client certificates REQUESTED and verified
// when given: describe stays open to a peer that has not presented one, while infer and
// invoke require one (checked per request). The root pool is the Edge's public root.
func (s *Server) tlsConfigServing() *tls.Config {
	cfg := s.TLSConfig()
	if s.serving != nil && s.serving.Trust != nil {
		pool := x509.NewCertPool()
		pool.AddCert(s.serving.Trust.Root())
		cfg.ClientCAs = pool
		// REQUEST, do not verify at the handshake: caller() authenticates against the Edge
		// root at the app layer, so a foreign or missing cert gets a shaped refusal that names
		// the reason, not a raw TLS alert.
		cfg.ClientAuth = tls.RequestClientCert
	}
	return cfg
}

// caller authenticates the peer on this connection: a certificate under the Edge's root
// that names a member of this fleet. It returns the node id, or the refusal to write.
func (s *Server) caller(r *http.Request) (string, int, string) {
	if s.serving == nil || s.serving.Trust == nil {
		return "", http.StatusNotImplemented, "this node does not serve peers"
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", http.StatusUnauthorized, "a peer turn needs your node certificate"
	}
	trust := s.serving.Trust
	if s.serving.TrustNow != nil {
		// A fresh source is configured: use it, and if it cannot be read REFUSE rather than fall back
		// to the startup snapshot - falling back would re-open the stale-revocation hole (fail closed).
		fresh := s.serving.TrustNow()
		if fresh == nil {
			return "", http.StatusServiceUnavailable, "this node cannot verify callers right now"
		}
		trust = fresh
	}
	id, err := trust.Authenticate(r.TLS.PeerCertificates[0])
	if err != nil {
		return "", http.StatusUnauthorized, "that certificate is not under this Edge's root"
	}
	if s.serving.Member == nil || !s.serving.Member(id) {
		// The same words whether the node is another account's or nothing at all: a
		// refusal that told them apart would be an oracle for who is enrolled here.
		return "", http.StatusForbidden, "that node is not a member of this Edge"
	}
	return id, 0, ""
}

func inferError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "edge_error"}})
}

// handleInfer serves one peer turn: authenticate, find the band, relay to the local
// upstream, and receipt it.
func (s *Server) handleInfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		inferError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	callerID, status, msg := s.caller(r)
	if status != 0 {
		inferError(w, status, msg)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, inferBodyCap+1))
	if err != nil || len(body) > inferBodyCap {
		inferError(w, http.StatusRequestEntityTooLarge, "request body exceeds the 4 MiB limit")
		return
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" {
		inferError(w, http.StatusBadRequest, "a peer turn names the band it wants")
		return
	}
	chatURL, key, instance, ok := s.serving.Upstream(req.Model)
	if !ok {
		inferError(w, http.StatusNotFound, "this node does not serve "+req.Model+"; it serves: "+strings.Join(s.servedBands(), ", "))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), inferTimeout)
	defer cancel()
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(body))
	if err != nil {
		inferError(w, http.StatusBadGateway, err.Error())
		return
	}
	up.Header.Set("Content-Type", "application/json")
	if key != "" {
		up.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(up)
	if err != nil {
		inferError(w, http.StatusBadGateway, "the model on this node did not answer: "+err.Error())
		return
	}
	defer resp.Body.Close()

	requestID := r.Header.Get("X-Roger-Request")
	if requestID == "" {
		requestID = fmt.Sprintf("edge-%d", s.now().UnixNano())
	}
	rec := protocol.UsageReceipt{RequestID: requestID, NodeID: s.serving.NodeID, Instance: instance,
		Model: req.Model, User: callerID, TS: s.now().Unix(), Local: true}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		s.streamInfer(w, resp, rec)
		return
	}
	// Read one byte past the cap so an oversized body is DETECTED, not silently truncated, and check
	// the read error: a receipt attests a COMPLETE reply, so a truncated or over-long body must be
	// voided rather than signed as done (audit 2026-09-24).
	out, rerr := io.ReadAll(io.LimitReader(resp.Body, inferBodyCap+1))
	oversized := int64(len(out)) > inferBodyCap
	if oversized {
		out = out[:inferBodyCap]
	}
	rec.PromptTokens, rec.CompletionTokens = usageOf(out)
	switch {
	case resp.StatusCode >= 400:
		rec.VoidReason = "upstream_" + fmt.Sprint(resp.StatusCode)
	case rerr != nil:
		rec.VoidReason = "reply_read_error"
	case oversized:
		rec.VoidReason = "reply_oversized"
	}
	if len(s.serving.Key) == ed25519.PrivateKeySize {
		rec.SignNode(s.serving.Key)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RogerAI-Provider", s.serving.NodeID+"/"+instance)
	w.Header().Set("X-RogerAI-Cost", "0")
	w.Header().Set("X-RogerAI-Receipt", protocol.EncodeReceipt(rec))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}

// streamInfer passes an SSE reply through and puts the local receipt at its end, the way
// the broker puts its cost meter there: a comment line parsers ignore by spec.
func (s *Server) streamInfer(w http.ResponseWriter, resp *http.Response, rec protocol.UsageReceipt) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-RogerAI-Provider", rec.NodeID+"/"+rec.Instance)
	// An upstream that answered with an error status did not complete the turn, even if it framed the
	// error as SSE: void the receipt so nothing bills a failed turn (audit 2026-09-24).
	if resp.StatusCode >= 400 {
		rec.VoidReason = "upstream_" + fmt.Sprint(resp.StatusCode)
	}
	w.WriteHeader(resp.StatusCode)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"usage"`) {
			p, c := usageOf([]byte(strings.TrimPrefix(line, "data: ")))
			if p+c > 0 {
				rec.PromptTokens, rec.CompletionTokens = p, c
			}
		}
		_, _ = io.WriteString(w, line+"\n")
		if flusher != nil && line == "" {
			flusher.Flush()
		}
	}
	// A read error (an oversized line, a dropped upstream) means the turn did NOT complete. Do not
	// sign a receipt for a partial reply - a signed receipt attests a completed turn - emit an error
	// comment instead so nothing bills the caller for what they did not get (audit 2026-09-23).
	if err := sc.Err(); err != nil {
		_, _ = io.WriteString(w, "\n: rogerai-error=stream-truncated\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	if len(s.serving.Key) == ed25519.PrivateKeySize {
		rec.SignNode(s.serving.Key)
	}
	_, _ = io.WriteString(w, "\n: rogerai-cost=0\n\n: rogerai-receipt="+protocol.EncodeReceipt(rec)+"\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// usageOf reads OpenAI's usage block out of a reply, when the upstream reports one.
func usageOf(body []byte) (prompt, completion int) {
	var u struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &u)
	return u.Usage.PromptTokens, u.Usage.CompletionTokens
}

// servedBands is what this node's household has on air, for the refusal that names them.
func (s *Server) servedBands() []string {
	seen := map[string]bool{}
	var out []string
	insts, _ := s.describe().Instances, false
	for _, in := range insts {
		for _, b := range in.Bands {
			if !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
	}
	if len(out) == 0 {
		return []string{"nothing"}
	}
	return out
}

func (s *Server) now() time.Time {
	if s.serving != nil && s.serving.Now != nil {
		return s.serving.Now()
	}
	return time.Now()
}

// VerifyLocalReceipt checks a local receipt against the serving node's certificate: the
// node key that signed it is the key the certificate binds. A receipt that does not verify
// is not evidence, and the caller records no session from it.
func VerifyLocalReceipt(rec protocol.UsageReceipt, serving *x509.Certificate) bool {
	if !rec.Local || serving == nil {
		return false
	}
	pub, ok := serving.PublicKey.(ed25519.PublicKey)
	if !ok {
		return false
	}
	return rec.VerifyNode(fmt.Sprintf("%x", []byte(pub)))
}
