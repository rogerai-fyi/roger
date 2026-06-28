package agent

// Node-side TEE quote generation for the confidential tier.
//
// When the node runs inside a real TEE (today: AMD SEV-SNP, via the guest
// /dev/sev-guest device), it can produce a hardware attestation quote whose
// report_data binds the node's Ed25519 pubkey to a fresh broker-issued nonce. The
// broker verifies that quote (signature chain + binding + allowlisted measurement)
// before granting the `confidential ◆` badge.
//
// HONESTY RULE: when there is NO TEE, the node produces NO quote and does NOT claim
// confidential. `roger share --confidential` fails clearly (see cmd/rogerai) rather
// than sending a fake claim. Quote generation is platform-specific and lives behind a
// build tag (attest_sevsnp.go for linux/amd64; attest_stub.go everywhere else), so the
// device dependency never enters builds that cannot use it.

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// teeKind identifies the attestation backend the running node can produce. Empty
// means no TEE hardware is available (the honest "standard" case).
type teeKind string

const teeSEVSNP teeKind = "sev-snp"

// detectTEE reports the TEE backend available on this machine, or "" if none. It is
// set by the build-tagged generateQuote implementation.
func detectTEE() teeKind { return teeAvailable() }

// ErrNoTEEDevice is the typed preflight failure for a host that is not an AMD SEV-SNP
// confidential VM (no /dev/sev-guest). The CLI surfaces it verbatim so an operator who
// ran `roger share --confidential` on the wrong host gets an actionable message and we
// abort BEFORE any broker round-trip - distinct from the broker-side "measurement not
// allowlisted" rejection (right hardware, unblessed image).
var ErrNoTEEDevice = fmt.Errorf("not an AMD SEV-SNP confidential VM (no /dev/sev-guest)")

// ConfidentialPreflight is the cheap, local "are you even eligible for the confidential
// tier" check `roger share --confidential` runs FIRST: it returns ErrNoTEEDevice when no
// TEE device is present (so we never attempt a quote / registration on a non-CVM host),
// or nil when a real TEE backend is available. It does NOT contact the broker and does
// NOT prove the launch measurement is allowlisted - that gate is broker-side, surfaced
// after registration via Session.Confidential().
func ConfidentialPreflight() error {
	if detectTEE() == "" {
		return ErrNoTEEDevice
	}
	return nil
}

// reportData64 computes the 64-byte report_data the quote must carry: it must match
// protocol.AttestationReportData(pubkey, nonce) exactly so the broker's binding check
// passes. Computed here (not via the protocol hex round-trip) from the raw key bytes.
func reportData64(pub ed25519.PublicKey, nonceHex string) ([64]byte, error) {
	var out [64]byte
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		return out, fmt.Errorf("bad nonce hex: %w", err)
	}
	h := sha512.New()
	h.Write(pub)
	h.Write(nonce)
	copy(out[:], h.Sum(nil))
	return out, nil
}

// fetchChallenge asks the broker for a fresh attestation nonce.
func fetchChallenge(broker string) (protocol.AttestChallenge, error) {
	var ch protocol.AttestChallenge
	resp, err := http.Post(broker+"/nodes/challenge", "application/json", nil)
	if err != nil {
		return ch, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return ch, fmt.Errorf("challenge request failed (%d): %s", resp.StatusCode, msg)
	}
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return ch, err
	}
	if ch.Nonce == "" {
		return ch, fmt.Errorf("broker returned an empty nonce")
	}
	return ch, nil
}

// attestForRegistration fetches a fresh nonce and generates a TEE quote bound to
// (pubkey, nonce). It fills reg.AttestKind / reg.AttestNonce / reg.Attestation and
// leaves Confidential set. It returns an error (and clears the confidential claim) if
// no TEE is present or quote generation fails - so the node never sends a fake claim.
func attestForRegistration(broker string, priv ed25519.PrivateKey, reg *protocol.NodeRegistration) error {
	kind := detectTEE()
	if kind == "" {
		reg.Confidential = false
		reg.Attestation = ""
		reg.AttestKind = ""
		reg.AttestNonce = ""
		return fmt.Errorf("no TEE hardware detected (need an AMD SEV-SNP confidential VM); not claiming confidential")
	}
	ch, err := fetchChallenge(broker)
	if err != nil {
		return fmt.Errorf("get attestation nonce: %w", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	rd, err := reportData64(pub, ch.Nonce)
	if err != nil {
		return err
	}
	quote, err := generateQuote(rd)
	if err != nil {
		return fmt.Errorf("generate %s quote: %w", kind, err)
	}
	reg.Confidential = true
	reg.AttestKind = string(kind)
	reg.AttestNonce = ch.Nonce
	reg.Attestation = base64.StdEncoding.EncodeToString(quote)
	return nil
}
