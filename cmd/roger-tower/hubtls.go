package main

// hubtls.go gets a certificate onto the hub listener for an operator who has no way of
// getting one.
//
// # WHY THIS FILE EXISTS AT ALL
//
// `--hub-tls-cert`/`--hub-tls-key` have been here for a while and they are the right flags for
// an operator who already holds a certificate. They are the wrong answer, and for most of the
// fleet the only answer, for the operator this programme is FOR: a volunteer on a home
// connection, behind a dynamic address, with no domain name and therefore no route to a
// publicly-trusted certificate at any price. Telling that operator to obtain one is telling
// them to stop being a Tower.
//
// They do not need one. A node and a consumer verify this hub by PINNING the public key Core
// told them to expect (internal/towerhub/pin.go), so the only thing the certificate has to be
// is stable and this tower's. A self-signed one is exactly as verifiable as a purchased one
// under that rule, and rather MORE verifiable than one from a public authority, which proves
// control of a name rather than the identity Core admitted.
//
// So: `--hub-tls` with no files mints one, keeps it, and advertises its fingerprint.
//
// # WHY IT IS PERSISTED, AND FOR TEN YEARS
//
// PERSISTED because the pin is the identity: a fresh key on every restart would change the
// fingerprint, and every node attached before the restart holds the old one - a redeploy would
// take the tower's whole fleet off the air until each node re-attached. The file is the memory
// that makes a restart invisible.
//
// TEN YEARS because expiry is not a control we have here. A certificate's validity window
// exists so that relying parties who cannot be reached will eventually stop believing a key;
// these relying parties CAN be reached, on the same channel that gave them the pin - Core stops
// advertising a fingerprint and the tower is unpinnable within one attach. A one-year self-
// signed certificate would add nothing but a yearly outage for operators who never think about
// it, at a date chosen by whenever they happened to first run `--hub-tls`.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"rogerai.fm/roger/v5/internal/towerhub"
)

const (
	hubCertFile = "hub-tls.crt"
	hubKeyFile  = "hub-tls.key"
	// hubKeyPerm matches internal/tower's keyPerm. The hub's TLS key is not the tower's
	// identity - losing it costs a re-pin, not the tower - but it is still private material in
	// the operator's data directory, and one permission rule for the directory is easier to
	// hold than two.
	hubKeyPerm  = 0o600
	hubCertPerm = 0o644
	// hubCertYears is the minted certificate's validity. See the file comment: the pin, not the
	// clock, is what withdraws trust here.
	hubCertYears = 10
)

// hubTLSMaterial is a loaded hub certificate and the pin that must be advertised for it.
//
// The two are returned TOGETHER because they are the same decision seen from the two ends of
// the link: the bytes this listener presents, and the fingerprint Core hands whoever dials it.
// A version of this function that returned only the certificate is a tower serving TLS that
// nothing can verify, which is the trap this whole change exists to remove.
type hubTLSMaterial struct {
	Cert tls.Certificate
	Pin  string
}

// hubTLS resolves the hub's TLS material: the operator's files when they gave some, a minted
// and remembered self-signed certificate when they did not.
//
// towerID goes into the subject so an operator who inspects the file can tell which tower it
// belongs to. Nothing verifies it - the pin is over the public key and nothing else - and it
// is a label rather than a claim.
func hubTLS(dir, towerID, certPath, keyPath string) (hubTLSMaterial, error) {
	if certPath != "" || keyPath != "" {
		if certPath == "" || keyPath == "" {
			return hubTLSMaterial{}, fmt.Errorf("a hub certificate needs both its certificate and its key")
		}
		return loadHubTLS(certPath, keyPath)
	}
	minted := filepath.Join(dir, hubCertFile)
	mintedKey := filepath.Join(dir, hubKeyFile)
	if _, err := os.Stat(minted); err == nil {
		// REUSED WITHOUT QUESTION, including if it has expired. The pin does not look at
		// validity and neither may this: refusing to load an expired certificate here would
		// invent exactly the fleet-wide outage the ten-year validity exists to avoid, and would
		// do it on a property no client checks.
		return loadHubTLS(minted, mintedKey)
	}
	if err := mintHubCert(minted, mintedKey, towerID); err != nil {
		return hubTLSMaterial{}, err
	}
	return loadHubTLS(minted, mintedKey)
}

func loadHubTLS(certPath, keyPath string) (hubTLSMaterial, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return hubTLSMaterial{}, fmt.Errorf("cannot load the hub's TLS certificate: %w", err)
	}
	// PARSED HERE RATHER THAN AT HANDSHAKE TIME, because the pin cannot be computed without
	// it and the pin has to be on the wire to Core before the first node is ever routed here.
	// tls.LoadX509KeyPair leaves Leaf nil, and a hub that discovered an unparseable
	// certificate on its first connection would have already advertised itself as reachable.
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return hubTLSMaterial{}, fmt.Errorf("cannot parse the hub's TLS certificate: %w", err)
	}
	pair.Leaf = leaf
	return hubTLSMaterial{Cert: pair, Pin: towerhub.CertPin(leaf)}, nil
}

// mintHubCert writes a fresh self-signed certificate and its key.
//
// Ed25519, like every other key this product mints: it is what the identity, the assertion and
// the envelope keys already are, TLS 1.3 takes it directly, and it keeps the operator's data
// directory from acquiring a second key type nobody chose.
//
// NO SANs AND NO NAME CONSTRAINTS. There is nothing honest to put in one - the address is
// dynamic and the operator has no domain - and a name in a certificate that no client checks
// is a claim that will eventually be read as a promise.
func mintHubCert(certPath, keyPath, towerID string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "roger tower hub " + towerID},
		// BACKDATED AN HOUR against the one clock problem that does bite a self-signed
		// certificate: an operator whose box boots with a clock behind ours would otherwise mint
		// something not yet valid. Nothing in this system checks it, but curl and a browser do,
		// and an operator debugging their own hub should not be sent chasing that.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(hubCertYears, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	// THE KEY FIRST, AT 0600, AND THE CERTIFICATE ONLY IF THAT SUCCEEDED. A certificate on disk
	// with no key beside it is what the next run will try to load and fail on; a key with no
	// certificate is re-minted harmlessly, because the existence check above is on the
	// certificate.
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), hubKeyPerm); err != nil {
		return err
	}
	// WriteFile honours umask; the mode must be exact for private material - the same
	// correction internal/tower/init.go makes for the identity key.
	if err := os.Chmod(keyPath, hubKeyPerm); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), hubCertPerm)
}
