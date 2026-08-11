package towerjoin

// enroll.go is the network half of joining the public network: the Tower's side of the
// admission handshake.
//
// It lives here rather than in internal/tower because enrolling needs the network, and that
// package is covered by a gate test that fails if any file in it gains the ability to reach
// one. A standalone operator therefore links none of this into the path they run.
//
// WHAT THE TOWER PROVES, and in which order:
//
//  1. its OPERATOR is signed in - every request is signed with the account's CLI key;
//  2. it holds its IDENTITY key - by signing a challenge Roger Core just issued;
//  3. it holds a SEPARATE channel key - by signing a CSR with it.
//
// The transaction id is generated ONCE and reused on every retry, because the case this
// protects against is the response being lost after the admission committed. Generating a
// fresh one on retry would ask for a second Tower instead of asking again for the first.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"rogerai.fm/roger/v5/internal/client"
	"rogerai.fm/roger/v5/internal/tower"
)

// protocolVersion is the joined protocol this build speaks.
const protocolVersion = 1

// certFile and caFile are where an admitted Tower keeps what it was issued. The
// certificate is not secret - it crosses the wire on every handshake - so it is readable,
// unlike the keys beside it.
const (
	certFile = "tower.crt"
	caFile   = "roger-ca.crt"
	admitted = "admission.json"
)

// Admission is what an enrolled Tower records about its place on the network.
type Admission struct {
	TowerID      string    `json:"tower_id"`
	State        string    `json:"state"`
	LeaseExpires time.Time `json:"lease_expires"`
	NotAfter     time.Time `json:"not_after"`
	// TransactionID is kept so a retry after a lost response asks about the SAME
	// enrollment rather than starting another.
	TransactionID string `json:"transaction_id"`
}

// httpClient bounds every call. A Tower that hangs on enrollment looks broken to its
// operator, and the operator is usually watching a terminal at the time.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// enroll is the real admission call, replacing the Phase-2 placeholder.
func enroll(st *tower.State, a Account) error {
	dir := st.Dir()
	broker := brokerBase()

	identity, err := st.IdentityKey()
	if err != nil {
		return fmt.Errorf("this Tower's identity key is unreadable: %w", err)
	}
	tlsKey, err := st.TLSKey()
	if err != nil {
		return fmt.Errorf("this Tower's channel key is unreadable: %w", err)
	}

	// The transaction id survives a failed attempt, so a retry is recognisable as one.
	adm, _ := LoadAdmission(dir)
	if adm.TransactionID == "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		adm.TransactionID = hex.EncodeToString(raw)
		if err := saveAdmission(dir, adm); err != nil {
			return err
		}
	}

	token, err := requestToken(broker, identity)
	if err != nil {
		return err
	}
	nonce, signingInput, err := requestChallenge(broker, identity, token)
	if err != nil {
		return err
	}

	csr, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "roger-tower"}}, tlsKey)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{
		"token":            token,
		"transaction_id":   adm.TransactionID,
		"nonce":            nonce,
		"identity_key":     base64.StdEncoding.EncodeToString(identity.Public().(ed25519.PublicKey)),
		"signature":        base64.StdEncoding.EncodeToString(ed25519.Sign(identity, signingInput)),
		"csr":              base64.StdEncoding.EncodeToString(csr),
		"protocol_version": protocolVersion,
		"capabilities":     []string{"relay"},
	})
	if err != nil {
		return err
	}

	var out struct {
		TowerID      string `json:"tower_id"`
		Certificate  string `json:"certificate"`
		CA           string `json:"ca"`
		State        string `json:"state"`
		LeaseExpires int64  `json:"lease_expires"`
		NotAfter     int64  `json:"not_after"`
	}
	if err := signedPost(broker+"/tower/enroll", identity, body, &out); err != nil {
		return err
	}

	certDER, err := base64.StdEncoding.DecodeString(out.Certificate)
	if err != nil {
		return errors.New("the broker returned a certificate that could not be read")
	}
	caDER, err := base64.StdEncoding.DecodeString(out.CA)
	if err != nil {
		return errors.New("the broker returned an issuer certificate that could not be read")
	}
	// Parsed before it is stored: writing bytes we cannot read would leave the Tower
	// believing it is admitted and failing at its first handshake instead.
	if _, err := x509.ParseCertificate(certDER); err != nil {
		return fmt.Errorf("the issued certificate is unusable: %w", err)
	}
	if _, err := x509.ParseCertificate(caDER); err != nil {
		return fmt.Errorf("the issuer certificate is unusable: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, certFile), certDER, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, caFile), caDER, 0o644); err != nil {
		return err
	}
	adm.TowerID = out.TowerID
	adm.State = out.State
	adm.LeaseExpires = time.Unix(out.LeaseExpires, 0)
	adm.NotAfter = time.Unix(out.NotAfter, 0)
	return saveAdmission(dir, adm)
}

func requestToken(broker string, identity ed25519.PrivateKey) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := signedPost(broker+"/tower/token", identity, []byte(`{}`), &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("the broker issued no enrollment token")
	}
	return out.Token, nil
}

func requestChallenge(broker string, identity ed25519.PrivateKey, token string) (nonce string, signingInput []byte, err error) {
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return "", nil, err
	}
	var out struct {
		Nonce        string `json:"nonce"`
		SigningInput string `json:"signing_input"`
	}
	if err := signedPost(broker+"/tower/enroll/challenge", identity, body, &out); err != nil {
		return "", nil, err
	}
	// The exact bytes to sign come from the broker, so the client never reconstructs the
	// framing and cannot get it subtly wrong - a mismatch there would look like a bad key.
	input, err := base64.StdEncoding.DecodeString(out.SigningInput)
	if err != nil || out.Nonce == "" || len(input) == 0 {
		return "", nil, errors.New("the broker returned an unusable challenge")
	}
	return out.Nonce, input, nil
}

// signedPost signs with the OPERATOR's CLI key - the account credential - not the Tower's
// identity key. They are different proofs: this one says who is asking, and the challenge
// signature inside the body says which machine.
func signedPost(url string, _ ed25519.PrivateKey, body []byte, out any) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := signAsOperator(req, body); err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach RogerAI: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// THE BROKER'S ENVELOPE IS {"error":{"message":...}} - an OBJECT. This decoded a
		// STRING, so the unmarshal failed silently on every refusal and the operator got
		// "the broker replied 429": the one piece of information that tells them nothing,
		// while the sentence saying what to do about it sat unread in the body.
		if msg, ok := envelopeMessage(raw); ok {
			return errors.New(msg)
		}
		return fmt.Errorf("the broker replied %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// signAsOperator signs with the account key `roger-tower login` bound, reusing the same
// signing path every other CLI call uses so there is one implementation of "who is asking"
// rather than two that could drift.
func signAsOperator(req *http.Request, body []byte) error {
	client.SignRequest(req, body)
	return nil
}

// LoadAdmission reads what this Tower recorded about its admission.
func LoadAdmission(dir string) (Admission, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, admitted))
	if err != nil {
		return Admission{}, false
	}
	var a Admission
	if err := json.Unmarshal(raw, &a); err != nil {
		return Admission{}, false
	}
	return a, a.TowerID != ""
}

func saveAdmission(dir string, a Admission) error {
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, admitted), raw, 0o600)
}

// brokerBase is where Roger Core lives for this Tower.
func brokerBase() string {
	if v := os.Getenv("ROGER_BROKER"); v != "" {
		return v
	}
	return "https://broker.rogerai.fm"
}
