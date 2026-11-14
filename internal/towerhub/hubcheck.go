package towerhub

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CheckHub is Core's Station-free health check of a hub (features/tower/tower_trust_ramp.feature,
// "the hub check"): one GET of the poll route carrying a fresh nonce and no credential. Every
// hub answer - the 401 included - carries the hub's identity key and its signature over the
// tower id, its epoch and that nonce (stampEpoch), so the answer proves this is the admitted
// Tower's hub without any Station being involved. hc and base come from Reach/ReachVetted, so
// the certificate pin is enforced by the transport.
//
// It returns the round trip and "" on a pass, or the failure reason: unreachable, pin_mismatch,
// no_proof, malformed_proof, wrong_key or stale_proof.
func CheckHub(ctx context.Context, hc *http.Client, base, towerID, keyHash string) (time.Duration, string) {
	nonce := newNonce()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(base, "/")+PathPoll+"?"+url.Values{nonceParam: {nonce}}.Encode(), nil)
	if err != nil {
		return 0, "unreachable"
	}
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, ErrHubCertificateUnpinned) {
			return 0, "pin_mismatch"
		}
		return 0, "unreachable"
	}
	rtt := time.Since(start)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()

	epoch := resp.Header.Get(HubEpochHeader)
	keyHex := strings.TrimSpace(resp.Header.Get(HubKeyHeader))
	proofHex := strings.TrimSpace(resp.Header.Get(HubProofHeader))
	if epoch == "" || keyHex == "" || proofHex == "" {
		return 0, "no_proof"
	}
	key, kerr := hex.DecodeString(keyHex)
	sig, serr := hex.DecodeString(proofHex)
	if kerr != nil || serr != nil || len(key) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return 0, "malformed_proof"
	}
	sum := sha256.Sum256(key)
	if keyHash == "" || !strings.EqualFold(hex.EncodeToString(sum[:]), keyHash) {
		return 0, "wrong_key"
	}
	if !ed25519.Verify(ed25519.PublicKey(key), hubEpochStatement(towerID, epoch, nonce), sig) {
		return 0, "stale_proof"
	}
	return rtt, ""
}
