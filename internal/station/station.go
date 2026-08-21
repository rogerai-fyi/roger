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
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rogerai.fm/roger/v5/internal/protocol"
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

	// Warnings are conditions Open REPAIRED rather than refused - today, a data directory or key
	// file whose mode was looser than the 0600/0700 Init writes. They are not returned as errors
	// because refusing would take a working provider off the network over something we can simply
	// fix, and they are not silent because a key that has been world-readable may already have
	// been read, and only the operator can decide what to do about that. The serving loop prints
	// them (internal/agent.ServeTower).
	Warnings []string `json:"-"`

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

// SignRequest signs an outbound HTTP request with this Station's ASSERTION key, in the house
// scheme (protocol.SignRequest: method + target + timestamp + body digest). It returns the
// three values the caller puts in the X-Roger-Pubkey / X-Roger-TS / X-Roger-Sig headers.
//
// # WHY A METHOD RATHER THAN AN ACCESSOR FOR THE KEY
//
// The obvious alternative was an AssertionPriv() accessor, and it is worse: the assertion key
// is what every receipt this Station ever signs is verified against, so handing the raw
// private half to another package makes that package one more place it can be copied, logged,
// or used to sign material this Station never chose. SessionPriv() is exported only because
// OPENING a sealed envelope needs the bytes themselves; signing does not, so this hands out
// signatures instead of the thing that makes them.
//
// The caller supplies the request TARGET - the path, plus the query when there is one -
// rather than a bare path, because the canonical string binds exactly what it is given and
// the query is where a hub request carries its anti-replay nonce. See
// internal/towerhub/nodeauth.go for why that nonce lives in the target and not in a header.
//
// NOTHING HERE DIALS, and this does not change that: it produces the three header values and
// leaves the caller to make the request.
func (s *Station) SignRequest(method, target string, body []byte) (pubHex string, ts int64, sigHex string) {
	return protocol.SignRequest(s.assertionPriv, method, target, body)
}

// SignAttachProof signs the possession proof that binds THIS Station's keys to ONE self-attach
// request (protocol.AttachProof, and the long note in that file for what the statement covers
// and why it cannot be confused with a hub request or a receipt).
//
// # WHY THE STATION FILLS IN ITS OWN KEYS AND ITS OWN ID
//
// The caller supplies only what belongs to the REQUEST - the network, the account key signing
// it, its timestamp, and its body. The three fields the proof is ABOUT are taken from this
// Station, derived from the private halves on disk rather than copied from the state file, so
// the proof necessarily names the keys this Station can actually sign and decrypt with. A
// signature over keys handed in by the caller would prove possession of whatever the caller
// chose to name, which is the defect the proof exists to close, one layer down.
//
// The caller is expected to have put exactly these three values in the body it passes here -
// internal/agent's AttachTower does, from the same accessors - and if it has not, the body
// digest in the statement makes the mismatch a refusal rather than a silent success.
//
// Same reasoning as SignRequest for why this is a method and not an AssertionPriv() accessor:
// it hands out a signature over bytes this package chose, rather than the material to sign
// anything at all.
//
// # THE HAZARD IN THE FOUR ARGUMENTS, WHICH IS RECORDED RATHER THAN NARROWED
//
// A security review asked what a SECOND caller could do with this, and the answer is worth
// writing down before there is one. The domain tag bounds it hard: these bytes cannot be read
// as a hub poll (protocol.CanonicalRequest) or as a receipt (towerobj), so no caller can steer
// this method into signing in either of those spaces, and that is the property that keeps this
// from being an oracle over the assertion key.
//
// What a caller CAN do is choose the request the proof is bound to. Pass somebody else's
// account key as callerPubHex, their timestamp and their body, and you get a valid proof that
// binds THIS Station's keys to THEIR attach - which is the assertion-key squat with the victim's
// own software as the accomplice. Nothing in this file can detect that, because a proof for a
// legitimate attach and a proof for a hostile one differ only in whose request the four
// arguments describe.
//
// It is not narrowed today because there is exactly ONE caller, internal/agent's AttachTower,
// and it supplies values it produced itself, in the same function, moments earlier: the account
// key and timestamp come straight out of its own protocol.SignRequest and the body is the one
// it is about to send. Narrowing would mean this method building the whole attach body, which
// puts the offer (model, modality, prices) into a package whose stated boundary is "nothing here
// dials" and which knows nothing about offers.
//
// SO THE RULE FOR A SECOND CALLER IS THE THING TO CHECK, not the signature: every one of these
// four values must be the caller's OWN request, freshly signed by it, and never a value that
// arrived from outside the process. A caller that cannot say that needs a different design, not
// this method.
func (s *Station) SignAttachProof(network, callerPubHex string, ts int64, body []byte) string {
	return protocol.AttachProof{
		Network:      network,
		CallerPubkey: callerPubHex,
		TS:           ts,
		StationID:    s.StationID,
		AssertionKey: hex.EncodeToString(s.AssertionPub()),
		SessionKey:   hex.EncodeToString(s.SessionPub()),
		Body:         body,
	}.Sign(s.assertionPriv)
}

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
	// MkdirAll DOES NOT CHANGE AN EXISTING DIRECTORY'S MODE, and it applies the process umask to
	// one it creates - so "0o700" above is a request, not a result. A Station directory that came
	// out of Init at 0755 (an existing parent path, a generous umask) holds the keys that sign an
	// operator's receipts and is readable by every account on the box. Tighten it here rather than
	// leaving Open to repair what the mint should not have produced.
	//
	// tighten only ever REMOVES bits, which matters: an operator who made this directory 0500 on
	// purpose meant it, and a mint is not the place to hand out write permission nobody asked for.
	warnings := tighten(dir, 0o700)
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
	// THE ID IS DERIVED FROM THE ASSERTION KEY, not drawn from crypto/rand, and that is a
	// security property rather than a tidy-up. A random id is unguessable, which sounds like the
	// stronger choice and is not: it is also unRECLAIMABLE. Core's reaper deletes a terminal
	// attachment thirty days after a revoke, and the id it frees is public - it has been the
	// leftmost label of this Station's relay DNS name and the relay_name in every authorize
	// answer it ever served - so anybody could then bind that name to a Station of their own,
	// and this directory, which keeps its id forever with no re-mint path, would be refused its
	// own identity on every re-attach from then on. An id that is a function of the key can only
	// be claimed by the machine holding that key. See protocol.DeriveStationID.
	s := &Station{
		Warnings:      warnings,
		StationID:     protocol.DeriveStationID(assertion.Public().(ed25519.PublicKey)),
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
	// PERMISSIONS ARE CHECKED AND REPAIRED, not assumed. Init creates 0700/0600, but Init is not
	// the only way a directory gets here: a restore from a backup, a copy between machines, or a
	// generous umask around a MkdirAll (which does NOT change an existing directory's mode) all
	// leave keys that sign receipts readable by every account on the box. Nothing looked, so a
	// world-readable Station loaded without a word.
	s.Warnings = append(s.Warnings, tighten(dir, 0o700)...)
	s.Warnings = append(s.Warnings, tighten(filepath.Join(dir, assertionKeyFile), 0o600)...)
	s.Warnings = append(s.Warnings, tighten(filepath.Join(dir, sessionKeyFile), 0o600)...)
	assertion, err := readKey(filepath.Join(dir, assertionKeyFile), ed25519.PrivateKeySize)
	if err != nil {
		return nil, fmt.Errorf("%s: the assertion key: %w", dir, err)
	}
	s.assertionPriv = ed25519.PrivateKey(assertion)
	// THE SEED HALF IS CHECKED AGAINST THE PUBLIC HALF, and this is the check the cross-check
	// below cannot make.
	//
	// In Go, ed25519.PrivateKey.Public() returns the private key's TRAILING 32 BYTES VERBATIM. It
	// does not re-derive anything from the seed. So comparing hex(s.AssertionPub()) to the state
	// file proves only that the state file and the tail of the key file agree - it says nothing
	// whatever about the seed, which is the half that actually signs. A station whose seed was
	// corrupted (a truncated write, a partial restore, one flipped byte) passed Open's cross-check
	// with a clean bill of health and then signed receipts that do not verify under the key Core
	// recorded at attachment: the node serves, produces evidence nobody can check, and its work
	// settles nowhere. The doc comment above promised that case fails loudly and it did not.
	//
	// Re-deriving from the seed and comparing is the whole fix, and it is the cheapest possible
	// one - a single scalar multiplication at load, once per process.
	derived := ed25519.NewKeyFromSeed(assertion[:ed25519.SeedSize]).Public().(ed25519.PublicKey)
	if !bytes.Equal(derived, s.AssertionPub()) {
		return nil, fmt.Errorf("%s: the assertion key file is internally inconsistent - its seed derives "+
			"%s but the key records %s. This Station would sign receipts that verify under neither, "+
			"and nothing downstream would be able to say why", dir, short(hex.EncodeToString(derived)),
			short(hex.EncodeToString(s.AssertionPub())))
	}
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
	// AND THE ID IS RESTAMPED IF IT PREDATES DERIVATION - repaired, like the file modes above,
	// rather than refused like the mismatches above THAT.
	//
	// The distinction is which value is recoverable. A state file whose recorded public key
	// disagrees with the key file has lost information: nobody can say which half is the real
	// Station, so Open refuses. A state file whose id is not the one its key derives has lost
	// nothing at all - the correct id is a pure function of a key that is right here, so there
	// is exactly one possible answer and computing it is the whole repair. Refusing instead
	// would take a working provider off the network over a value we can recompute, with no
	// remedy but deleting the identity, which is precisely the outcome the derivation exists to
	// prevent.
	//
	// It is loud because a Station that Core already attached under the OLD id keeps serving
	// under that old id - the handler answers a re-attach idempotently from the assertion key,
	// so the row is found and its recorded id is what comes back - while this file now says
	// something else. That divergence is harmless and confusing, and only an operator can decide
	// whether to revoke and start clean. No node in the field is in this position: self-attach
	// is absent from tag v5.7.1, so the population is development directories.
	if want := protocol.DeriveStationID(s.AssertionPub()); s.StationID != want {
		s.Warnings = append(s.Warnings, fmt.Sprintf(
			"this Station's id (%s) predates identity derivation and has been restamped as %s, "+
				"which is the id its assertion key mints; if Core already attached it under the "+
				"old id it will keep serving under that one until it is revoked and re-attached",
			s.StationID, want))
		s.StationID = want
		if err := s.save(); err != nil {
			return nil, fmt.Errorf("%s: restamping the Station id: %w", dir, err)
		}
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

// tighten repairs a path whose mode is looser than want, returning a warning describing what it
// found. Group and other bits are the whole question: an 0644 key file is readable by every
// account on the machine, and this one signs the receipts an operator is paid against.
//
// It REPAIRS rather than refuses on purpose. Refusing would take a running provider off the
// network over a condition one chmod fixes, and would do it at the least convenient moment; the
// warning is what makes the repair honest, because a key that has been readable may already have
// been read and only the operator can weigh that.
func tighten(path string, want os.FileMode) []string {
	info, err := os.Stat(path)
	if err != nil {
		return nil // a missing file is the caller's problem to report, with a better message
	}
	mode := info.Mode().Perm()
	if mode&^want == 0 {
		return nil
	}
	if cerr := os.Chmod(path, want); cerr != nil {
		return []string{fmt.Sprintf("station: %s is mode %#o (should be %#o) and could not be tightened: %v - "+
			"a Station key readable by other accounts on this machine should be treated as exposed", path, mode, want, cerr)}
	}
	return []string{fmt.Sprintf("station: %s was mode %#o (should be %#o); tightened to %#o - "+
		"if other accounts had access to this machine, treat these keys as exposed", path, mode, want, want)}
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
