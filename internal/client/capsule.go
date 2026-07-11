package client

// capsule.go is the CLIENT side of the encrypted stranger transport (Stage 3): it seals a
// signed, redacted context capsule under a one-time CODE and mints it to the broker's
// content-blind rendezvous, and resolves+opens one on the receiving side. The broker only
// ever sees {lookup, ciphertext}: the code, the HKDF key, and the plaintext never leave the
// client (internal/capsule/transport.go).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rogerai-fyi/roger/internal/capsule"
)

// ErrCapsuleGone is the receiver's view of a uniform 404 from /capsule/resolve: the code is
// wrong, or the blob expired, or it was already consumed (one-time). The broker returns the
// same 404 for all three (no existence oracle), so the client cannot distinguish them either.
var ErrCapsuleGone = errors.New("capsule: no such capsule (wrong code, expired, or already used)")

// capsuleHTTP is the bounded client for the mint/resolve calls (small JSON, fast paths).
var capsuleHTTP = &http.Client{Timeout: 30 * time.Second}

// capsuleResolveReadCap bounds the resolve response read: the broker caps a blob at 1 MB, and
// base64 expands ~4/3, plus JSON envelope slack - so ~1.5 MB is a safe ceiling.
const capsuleResolveReadCap = 1<<20*3/2 + 1<<12

// PublishCapsule seals capsuleJSON (a signed roger.context.v1 wire object) under code and
// MINTS it to the broker: POST /capsule {lookup, blob}, owner-signed (attribution). The
// lookup is BandCodeHash(code); the blob is the AES-256-GCM ciphertext. The raw code is
// handed to the peer out-of-band (the reference channel) - never here, never on a frame.
//
// This is the NON-floor publisher used for the RECALL / return leg (the guest hands context
// back under a FRESH code): a return capsule is not a stranger export, so the summary-only
// floor does not apply (the receiver is protected by verify-before-merge + append-only, not
// redaction). The DJ->stranger leg uses PublishStrangerCapsule, which enforces the floor.
func PublishCapsule(broker, code string, capsuleJSON []byte) error {
	sealed, err := capsule.SealForCode(capsuleJSON, code)
	if err != nil {
		return err
	}
	return mintCapsule(broker, code, sealed)
}

// PublishStrangerCapsule is PublishCapsule with the redaction FLOOR: it refuses to mint a
// non-summary (full) capsule to a marketplace/stranger (ErrNotSummary), so a stranger
// transport can never carry a full transcript. This is the DJ->stranger handoff path.
func PublishStrangerCapsule(broker, code string, capsuleJSON []byte) error {
	sealed, err := capsule.SealForStranger(capsuleJSON, code)
	if err != nil {
		return err
	}
	return mintCapsule(broker, code, sealed)
}

// mintCapsule POSTs the sealed blob to the broker's content-blind /capsule endpoint,
// owner-signed. It is the shared tail of PublishCapsule / PublishStrangerCapsule.
func mintCapsule(broker, code string, sealed []byte) error {
	body, _ := json.Marshal(map[string]string{
		"lookup": capsule.TransportLookup(code),
		"blob":   base64.StdEncoding.EncodeToString(sealed),
	})
	req, err := http.NewRequest(http.MethodPost, broker+"/capsule", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, body) // owner-signed mint
	resp, err := capsuleHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return fmt.Errorf("capsule mint failed: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return nil
}

// FetchCapsule resolves the blob for code from the broker (POST /capsule/resolve {lookup},
// authed by possession of the lookup - no signature) and OPENS it with the code, returning
// the plaintext capsule JSON. A uniform 404 becomes ErrCapsuleGone. The resolve is one-time:
// the broker deletes the blob on read, so a second call is ErrCapsuleGone.
func FetchCapsule(broker, code string) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{"lookup": capsule.TransportLookup(code)})
	req, err := http.NewRequest(http.MethodPost, broker+"/capsule/resolve", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := capsuleHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrCapsuleGone
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("capsule resolve failed: %s", resp.Status)
	}
	var out struct {
		Blob string `json:"blob"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, capsuleResolveReadCap)).Decode(&out); err != nil {
		return nil, err
	}
	sealed, err := base64.StdEncoding.DecodeString(out.Blob)
	if err != nil {
		return nil, err
	}
	return capsule.OpenWithCode(sealed, code)
}
