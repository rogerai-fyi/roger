package tower

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
	"syscall"
)

// PublicNetworkID names the RogerAI public network. A standalone Tower mints its own
// network ID and must never produce this one: a local network that could pass itself off
// as the public network would defeat the whole separation.
const PublicNetworkID = "rogerai-public"

// File names inside a Tower data directory. The state file records the mode for life;
// the .key files hold private material and are owner-read-only.
const (
	stateFile    = "tower.json"
	identityKey  = "identity.key"
	tlsKey       = "tls.key"
	offlineRoot  = "offline-root.key"
	lockFile     = ".lock"
	dirPerm      = 0o700
	keyPerm      = 0o600
	statePerm    = 0o600
	localIDBytes = 16
)

// State is a Tower data directory's durable identity. It is written once at init and is
// never rewritten to a different mode: Open + RequireMode is the only path into serving.
type State struct {
	Mode    Mode   `json:"mode"`
	TowerID string `json:"tower_id"`
	// LocalNetworkID is set ONLY in standalone mode - it is the id of the separate
	// local network this Tower is the root of. A joined Tower has none, because it
	// belongs to the public network and mints no trust root of its own.
	LocalNetworkID string `json:"local_network_id,omitempty"`

	dir string
	// st overrides the default file store. Set by WithStore for a durable deployment;
	// nil means the data directory.
	st Store
}

// WithStore returns a copy of this State that persists through the given store. It is how
// a durable Tower gets database-backed admission state without internal/tower ever
// linking a driver - the thing its no-egress gate exists to prevent.
func (s *State) WithStore(st Store) *State {
	c := *s
	c.st = st
	return &c
}

// Dir is the data directory this state was loaded from. The joined-mode account flow
// (internal/towerjoin) stores its credential beside the identity, so it needs the path.
func (s *State) Dir() string { return s.dir }

// Init creates a fresh Tower data directory in exactly one mode.
//
// It is all-or-nothing: an invalid mode or a non-empty directory fails before anything
// is written, so a rejected init never leaves a partial identity behind for a later run
// to pick up and treat as real.
func Init(dir string, mode Mode) (*State, error) {
	if _, err := ParseMode(string(mode)); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("%s is not empty: initialize a Tower in a fresh data directory", dir)
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, err
	}
	// MkdirAll honours umask, so set the mode explicitly - the directory holds keys.
	if err := os.Chmod(dir, dirPerm); err != nil {
		return nil, err
	}

	st := &State{Mode: mode, dir: dir}

	// Distinct keys for distinct powers, from the first moment the directory exists.
	// The identity key proves who this Tower is; the TLS key secures its channel. They
	// are separate so rotating one never forces rotating the other.
	id, err := newKeyFile(filepath.Join(dir, identityKey))
	if err != nil {
		return nil, cleanupOnError(dir, err)
	}
	if _, err := newKeyFile(filepath.Join(dir, tlsKey)); err != nil {
		return nil, cleanupOnError(dir, err)
	}
	st.TowerID = hex.EncodeToString(id[:8])

	if mode == ModeStandalone {
		// A standalone Tower is the root of its own network: it mints a unique local
		// network ID and a pinned offline root that never leaves this directory.
		nid, err := randomHex(localIDBytes)
		if err != nil {
			return nil, cleanupOnError(dir, err)
		}
		if nid == PublicNetworkID {
			return nil, cleanupOnError(dir, errors.New("generated network ID collided with the public network"))
		}
		st.LocalNetworkID = "local-" + nid
		if _, err := newKeyFile(filepath.Join(dir, offlineRoot)); err != nil {
			return nil, cleanupOnError(dir, err)
		}
	}

	if err := st.write(); err != nil {
		return nil, cleanupOnError(dir, err)
	}
	return st, nil
}

// cleanupOnError removes a half-written data directory so a failed init cannot leave
// key material or a partial identity for a later run to adopt.
func cleanupOnError(dir string, err error) error {
	_ = os.RemoveAll(dir)
	return err
}

// Open loads an existing Tower data directory.
func Open(dir string) (*State, error) {
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return nil, fmt.Errorf("%s is not an initialized Tower data directory: %w", dir, err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s holds unreadable Tower state: %w", dir, err)
	}
	if _, err := ParseMode(string(st.Mode)); err != nil {
		return nil, fmt.Errorf("%s records an unsupported mode: %w", dir, err)
	}
	st.dir = dir
	return &st, nil
}

// RequireMode refuses to run a data directory as the other mode.
//
// This is not a convenience check. Switching in place would carry an identity, trust
// root, or Station registry across the boundary the two modes exist to separate, so the
// only supported answer is a new data directory and an explicit init.
func (s *State) RequireMode(want Mode) error {
	if s.Mode == want {
		return nil
	}
	return fmt.Errorf(
		"this data directory was initialized as %q and cannot run as %q: create a new data directory and initialize it explicitly (nothing is copied automatically)",
		s.Mode, want)
}

// Lock takes exclusive ownership of the identity directory for this process. Two Towers
// sharing one identity would reuse each other's session and sequence state, so the
// second must fail before it connects or listens.
//
// The lock is an advisory flock on a file in the directory, so it is released by the OS
// if the process dies - a crash must not wedge the directory.
func (s *State) Lock() (release func() error, err error) {
	f, err := os.OpenFile(filepath.Join(s.dir, lockFile), os.O_CREATE|os.O_RDWR, keyPerm)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another Tower process already owns %s", s.dir)
	}
	return func() error {
		defer f.Close()
		return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}, nil
}

func (s *State) write() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, stateFile), b, statePerm)
}

// newKeyFile generates an ed25519 private key and writes it owner-read-only, returning
// the public half for identity derivation.
func newKeyFile(path string) (ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)), keyPerm); err != nil {
		return nil, err
	}
	// WriteFile honours umask; the mode must be exact for private material.
	if err := os.Chmod(path, keyPerm); err != nil {
		return nil, err
	}
	return pub, nil
}

// IdentityKey returns the Tower's persistent identity key: who this Tower IS. It is the
// key an enrollment challenge is signed with, and it is separate from the TLS key so
// rotating a certificate never touches the Tower's identity.
func (s *State) IdentityKey() (ed25519.PrivateKey, error) { return s.readKey(identityKey) }

// TLSKey returns the Tower's channel key. A certificate is issued over this one, so it
// rotates on the certificate's schedule rather than the Tower's lifetime.
func (s *State) TLSKey() (ed25519.PrivateKey, error) { return s.readKey(tlsKey) }

// readKey loads private material from the data directory. Reading a local file is not
// egress, so this stays inside the package the no-network gate covers.
func (s *State) readKey(name string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return nil, err
	}
	priv, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("%s is not readable key material", name)
	}
	if len(priv) != ed25519.PrivateKeySize {
		// A truncated or replaced key file must not be used as though it were a key: the
		// signature it produced would simply never verify, and the failure would surface
		// far from its cause.
		return nil, fmt.Errorf("%s is not a complete key", name)
	}
	return ed25519.PrivateKey(priv), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
