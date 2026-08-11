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
	"time"

	"rogerai.fm/roger/v5/internal/towercore/inv"
	"rogerai.fm/roger/v5/internal/towerobj"
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

	dir            string
	assertionPriv  ed25519.PrivateKey
	sessionPrivRaw ed25519.PrivateKey
}

// AssertionPub is the key Core verifies this Station's offers with.
func (s *Station) AssertionPub() ed25519.PublicKey {
	return s.assertionPriv.Public().(ed25519.PublicKey)
}

// SessionPub is the key that terminates this Station's end of the inner channel.
func (s *Station) SessionPub() ed25519.PublicKey {
	return s.sessionPrivRaw.Public().(ed25519.PublicKey)
}

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
	session, err := writeFreshKey(filepath.Join(dir, sessionKeyFile))
	if err != nil {
		return nil, err
	}
	s := &Station{
		StationID:      "st-" + randomHex(12),
		Assertion:      hex.EncodeToString(assertion.Public().(ed25519.PublicKey)),
		Session:        hex.EncodeToString(session.Public().(ed25519.PublicKey)),
		dir:            dir,
		assertionPriv:  assertion,
		sessionPrivRaw: session,
	}
	return s, s.save()
}

// Open reads an initialized Station data directory.
func Open(dir string) (*Station, error) {
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return nil, fmt.Errorf("%s is not an initialized Station data directory: %w", dir, err)
	}
	var s Station
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s is not an initialized Station data directory: %w", dir, err)
	}
	s.dir = dir
	if s.assertionPriv, err = readKey(filepath.Join(dir, assertionKeyFile)); err != nil {
		return nil, err
	}
	if s.sessionPrivRaw, err = readKey(filepath.Join(dir, sessionKeyFile)); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Station) save() error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, stateFile), raw, 0o600)
}

// Offer is what a Station is willing to serve, before it is signed.
//
// Prices and earnings are in the network's smallest unit and are integers: a rate expressed
// as a float is a rate two implementations will disagree about in the last place, and this
// one is multiplied by a token count and charged to somebody.
type Offer struct {
	Network      string
	TowerID      string
	Model        string
	Modality     string
	PriceIn      int64
	PriceOut     int64
	EarnIn       int64
	EarnOut      int64
	Capacity     int64
	Capabilities []string
	TTL          time.Duration
}

// SignOffer produces one signed leaf, ready to be relayed verbatim.
//
// The members are exactly Core's closed schema (inv.leafMembers) and every integer is a
// bounded base-10 STRING. Neither is stylistic: an extra member is refused, and a JSON
// number does not survive a round trip identically across languages, so a canonical form
// that admits one is not canonical.
//
// There is deliberately nowhere to put the Station's key. Core takes it from the attachment
// record; a leaf that supplied its own would turn "signed by the Station" into "signed by
// whoever wrote this leaf".
func (s *Station) SignOffer(o Offer, now time.Time) ([]byte, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	offerID := "off-" + randomHex(12)
	// Never nil: an ABSENT capabilities member is how a relay would strip the field from
	// the signed bytes and then assert capabilities out of band. Empty is a statement;
	// missing is a hole.
	caps := o.Capabilities
	if caps == nil {
		caps = []string{}
	}
	leaf := map[string]any{
		"network":      o.Network,
		"tower_id":     o.TowerID,
		"station_id":   s.StationID,
		"offer_id":     offerID,
		"model":        o.Model,
		"modality":     o.Modality,
		"price_in":     towerobj.FormatInt(o.PriceIn),
		"price_out":    towerobj.FormatInt(o.PriceOut),
		"earn_in":      towerobj.FormatInt(o.EarnIn),
		"earn_out":     towerobj.FormatInt(o.EarnOut),
		"capacity":     towerobj.FormatInt(o.Capacity),
		"capabilities": caps,
		"expires":      towerobj.FormatInt(now.Add(o.TTL).Unix()),
	}
	raw, err := json.Marshal(leaf)
	if err != nil {
		return nil, err
	}
	return towerobj.Sign(s.assertionPriv, o.Network, inv.TypeOffer, inv.Version, raw, "station_sig")
}

// check refuses locally what Core would refuse remotely.
//
// Not a duplicate of Core's decision and not a substitute for it - Core still applies every
// one of these plus the ones only it can (price band, model allowlist, quarantine, origin).
// The value is the FEEDBACK LOOP: a rejected leaf is dropped silently from a revision the
// Tower relayed, so an operator who got it wrong sees an offer that simply never appears,
// with the reason sitting in a response they never see.
func (o Offer) check() error {
	switch {
	case o.Network == "":
		return errors.New("an offer names the network it is for")
	case o.TowerID == "":
		return errors.New("an offer names the Tower that may relay it")
	case o.Model == "":
		return errors.New("an offer names a model")
	case o.Modality == "":
		return errors.New("an offer names a modality")
	case o.Capacity <= 0:
		return errors.New("capacity must be positive: an offer to serve nothing is not an offer")
	case o.PriceIn < 0 || o.PriceOut < 0 || o.EarnIn < 0 || o.EarnOut < 0:
		return errors.New("prices and earnings cannot be negative")
	case o.EarnIn > o.PriceIn || o.EarnOut > o.PriceOut:
		// The arithmetic an operator is most likely to try, and it is money out of Core's
		// pocket on every token it is applied to.
		return errors.New("the Station cannot earn more than the consumer pays")
	case o.TTL <= 0:
		return errors.New("an offer needs a positive lifetime")
	}
	return nil
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

func readKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("this Station's key is unreadable: %w", err)
	}
	dec, err := hex.DecodeString(string(raw))
	if err != nil || len(dec) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s is not a Station key", path)
	}
	return ed25519.PrivateKey(dec), nil
}

// randomHex panics rather than returning an error, matching the broker's own id minting.
// A predictable Station or offer id is not something to paper over with a fallback: ids are
// what an inventory keys on, and crypto/rand failing is a broken machine, not a condition to
// carry on through.
func randomHex(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw)
}
