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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
//
// A caller that wants "this host's Station, whether or not it has one yet" wants InitOrOpen.
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

// Open loads the Station a directory already holds, keys and all.
//
// This is the other half of Init, and the reason it had to exist. Init is a MINT: it refuses
// a directory that already holds a Station because re-minting would issue new keys while the
// attachment Core recorded still names the old ones. That refusal is right, but with no way
// to load, "mint" was the only verb this package had - so the second run of anything that
// needed its Station identity got an error instead of the identity, and the caller that
// treated attaching as best-effort silently stopped attaching for the life of the machine.
//
// A PARTIAL DIRECTORY FAILS LOUDLY. Every failure below is deliberately an error rather than
// a fresh mint: a missing key file, an unreadable one, or a state file whose recorded public
// halves do not match the private keys beside it all mean this directory is not the Station
// it claims to be. Answering that by minting a new identity would be the exact outcome Init's
// refusal exists to prevent, arrived at by a different road - the operator would come back
// with a Station ID no attachment names and no clue why.
func Open(dir string) (*Station, error) {
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return nil, err
	}
	s := &Station{dir: dir}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("%s: the Station state file is unreadable: %w", dir, err)
	}
	if s.StationID == "" {
		return nil, fmt.Errorf("%s: the Station state file names no station id", dir)
	}
	assertion, err := readKey(filepath.Join(dir, assertionKeyFile), ed25519.PrivateKeySize)
	if err != nil {
		return nil, fmt.Errorf("%s: the assertion key: %w", dir, err)
	}
	s.assertionPriv = ed25519.PrivateKey(assertion)
	session, err := readKey(filepath.Join(dir, sessionKeyFile), 32)
	if err != nil {
		return nil, fmt.Errorf("%s: the secure-session key: %w", dir, err)
	}
	// PublicKeyOf rather than SessionPub: SessionPub panics on a key X25519 will not take,
	// and a corrupt file on disk is a condition to report, not to crash the share over.
	sessionPub, err := envelope.PublicKeyOf(session)
	if err != nil {
		return nil, fmt.Errorf("%s: the secure-session key: %w", dir, err)
	}
	s.sessionPriv = session
	// THE STATE FILE AND THE KEYS MUST AGREE. The state file is what a human reads and what
	// an invitation was written from; the key files are what actually sign and decrypt. If
	// they have drifted apart - a half-finished copy between machines, a restore of one file
	// and not the others - then whatever this Station proves is not what anybody recorded
	// about it, and every offer it signs will be rejected downstream for reasons that point
	// nowhere near here.
	if got := hex.EncodeToString(s.AssertionPub()); got != s.Assertion {
		return nil, fmt.Errorf("%s: the assertion key does not match the one recorded in %s "+
			"(recorded %s, on disk %s) - this directory has been partially overwritten",
			dir, stateFile, short(s.Assertion), short(got))
	}
	if got := hex.EncodeToString(sessionPub); got != s.Session {
		return nil, fmt.Errorf("%s: the secure-session key does not match the one recorded in %s "+
			"(recorded %s, on disk %s) - this directory has been partially overwritten",
			dir, stateFile, short(s.Session), short(got))
	}
	return s, nil
}

// InitOrOpen is what a long-lived process wants: the Station this directory already holds,
// or a fresh one if it holds none. The distinction Init and Open draw is exactly right for a
// human running a one-off command and exactly wrong for a daemon that restarts, which needs
// the SAME identity every time and has no way to know whether this host has run before.
//
// It never falls back to minting. A directory that holds a broken Station is reported as
// broken, because the alternative - quietly issuing a second identity beside a first one
// that attachments still name - is unrecoverable in a way an error message is not.
func InitOrOpen(dir string) (*Station, error) {
	if _, err := os.Stat(filepath.Join(dir, stateFile)); err == nil {
		return Open(dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return Init(dir)
}

// readKey reads one hex-encoded private key file and checks its length. Length is checked
// here rather than at the point of use because a truncated key file is the most likely shape
// of corruption and the least obvious one at a call site.
func readKey(path string, want int) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("%s is not hex: %w", filepath.Base(path), err)
	}
	if len(key) != want {
		return nil, fmt.Errorf("%s is %d bytes, not %d", filepath.Base(path), len(key), want)
	}
	return key, nil
}

// short abbreviates a hex key for an error message: enough to tell two apart, never the
// whole thing, because these strings end up in support threads.
func short(hexKey string) string {
	if len(hexKey) <= 12 {
		return hexKey
	}
	return hexKey[:12] + "..."
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
