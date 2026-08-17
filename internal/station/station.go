// Package station is the Station's half of the joined network: its two keys, and the
// offers it signs with one of them.
//
// A Station is the machine that actually serves work behind a Tower. It holds TWO keys and
// the Tower holds NEITHER:
//
//	assertion key   signs the offers this Station publishes, and later its receipts.
//	session key     terminates this Station's end of the inner channel, so Core is talking
//	                to the Station rather than to the relay in front of it.
//
// THE SEPARATION IS WHY A JOINED TOWER CAN BE UNTRUSTED. If a Tower could sign for a
// Station, "signed by the Station" would mean "signed by whoever is relaying", and every
// guarantee downstream - price, capacity, capability, the receipt chain - would rest on the
// word of the party with the most to gain from bending it. Core verifies each leaf against
// the key recorded at ATTACHMENT, so a relay that alters one byte invalidates it.
//
// # WHY THIS PACKAGE EXISTS
//
// There was no Station-side software at all. No way to generate the keys an attachment
// names, and no way to produce a signed offer. So a joined Tower pushed a valid inventory
// of ZERO leaves - honest, and permanently empty - and Core's whole leaf-verification path
// (nineteen rejection rows, price bands, quarantine, origin fencing) had nothing to verify.
// This is the other end of that contract.
//
// # NO NETWORK
//
// Nothing here dials. A Station's offer is a signed FILE: it is produced here, carried to
// the Tower by whatever the operator already trusts to move a file, and relayed verbatim.
// That keeps the Station's keys on the Station, and it means an offer can be inspected,
// diffed and archived before it ever reaches the network.
package station

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"rogerai.fm/roger/v5/internal/towercore/envelope"
)

const (
	stateFile        = "station.json"
	assertionKeyFile = "assertion.key"
	sessionKeyFile   = "session.key"
)

// Station is an initialized Station data directory.
type Station struct {
	StationID string `json:"station_id"`
	// The PUBLIC halves, hex, exactly as an invitation names them. The private halves live
	// in their own 0600 files and are never part of this record - a state file gets read,
	// copied and pasted into support threads, and a key that is in it will eventually be in
	// one of those.
	Assertion string `json:"assertion_key"`
	Session   string `json:"session_key"`

	dir           string
	assertionPriv ed25519.PrivateKey
	// sessionPriv is X25519, not Ed25519, because its job is KEY AGREEMENT rather than
	// signing: it is what Roger Core seals a request to so the Tower relaying it cannot
	// read the content. The assertion key signs; this one receives.
	sessionPriv []byte
}

// AssertionPub is the key Core verifies this Station's offers with.
func (s *Station) AssertionPub() ed25519.PublicKey {
	return s.assertionPriv.Public().(ed25519.PublicKey)
}

// SessionPub is the key Core seals this Station's requests to.
func (s *Station) SessionPub() []byte {
	pub, err := envelope.PublicKeyOf(s.sessionPriv)
	if err != nil {
		// Only reachable if the stored key is not an X25519 key, which Open refuses.
		panic("station: the secure-session key is unusable: " + err.Error())
	}
	return pub
}

// SessionPriv is the private half, for opening what Core sealed. Unexported elsewhere: it
// leaves this package only to the executor in it.
func (s *Station) SessionPriv() []byte { return s.sessionPriv }

// Dir is the data directory this Station was loaded from.
func (s *Station) Dir() string { return s.dir }

// Init creates a fresh Station data directory with both keys.
//
// It refuses a directory that already holds one. Re-initializing over a live Station mints
// new keys while the attachment Core recorded still names the OLD ones: the Station becomes
// cryptographically unable to prove it is itself, and there is no way back that does not go
// through revoking the identity and allocating a new Station ID.
func Init(dir string) (*Station, error) {
	if _, err := os.Stat(filepath.Join(dir, stateFile)); err == nil {
		return nil, fmt.Errorf("%s already holds an initialized Station: "+
			"re-initializing would mint new keys that no attachment names", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	assertion, err := writeFreshKey(filepath.Join(dir, assertionKeyFile))
	if err != nil {
		return nil, err
	}
	sessionPub, sessionPriv, err := envelope.NewKey()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, sessionKeyFile),
		[]byte(hex.EncodeToString(sessionPriv)), 0o600); err != nil {
		return nil, err
	}
	s := &Station{
		StationID:     "st-" + randomHex(12),
		Assertion:     hex.EncodeToString(assertion.Public().(ed25519.PublicKey)),
		Session:       hex.EncodeToString(sessionPub),
		dir:           dir,
		assertionPriv: assertion,
		sessionPriv:   sessionPriv,
	}
	return s, s.save()
}

func (s *Station) save() error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, stateFile), raw, 0o600)
}

func writeFreshKey(path string) (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	// 0600 and hex. Not PEM: PEM invites being pasted somewhere, and this file has exactly
	// one reader.
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

func randomHex(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw)
}
