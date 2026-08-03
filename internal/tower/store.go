package tower

// The persistence seam.
//
// Two reasons it exists rather than the code writing files directly:
//
//  1. `internal/tower` is covered by a gate test that fails if any file in it gains the
//     ability to reach the network, and a database driver dials. Keeping the driver
//     behind this interface - implemented in a separate package - is what lets the
//     standalone core stay provably egress-free while still having durable storage.
//  2. A file-backed Tower is serialized by the identity-directory lock, but a
//     database-backed one can have several processes. So the contract is
//     compare-and-swap on a revision: a write from a stale read is REFUSED rather than
//     silently overwriting a newer one. Two operators being admitted and one vanishing
//     is the failure this prevents.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// ErrStaleWrite means the caller's snapshot was superseded. Re-read and retry; do not
// force the write, or you are choosing to discard whatever the other writer did.
var ErrStaleWrite = errors.New("this Tower's admission state changed since it was read")

// Snapshot is everything about local admission that must survive a restart.
//
// Each field is here for a concrete reason: losing HMACKey kills every open invitation;
// losing Operator leaves the network with nobody in charge; losing Stations un-attaches
// every machine; and losing Invitations would let an already-consumed code be replayed.
type Snapshot struct {
	// Revision is persisted and incremented on every write, so a stale write is
	// detectable without a second file and the check means the same thing in a database.
	Revision      int64                  `json:"revision"`
	HMACKey       string                 `json:"hmac_key"`
	Invitations   map[string]*Invitation `json:"invitations"`
	GlobalAttempt int                    `json:"global_attempts"`
	GlobalSince   int64                  `json:"global_attempts_since,omitempty"`
	Operator      *Credential            `json:"operator,omitempty"`
	Stations      map[string]*Station    `json:"stations,omitempty"`
}

// NewSnapshot mints the state a fresh Tower starts from, including its verifier secret.
// Exported so an alternative store can produce the same starting point rather than
// guessing at the fields - a store that forgot the HMAC key would silently break every
// invitation it later issued.
func NewSnapshot() (*Snapshot, error) {
	key, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	return &Snapshot{HMACKey: key, Invitations: map[string]*Invitation{}}, nil
}

// Store is durable local-admission state. Implementations must make Save atomic: a
// partially applied snapshot could leave a consumed code beside an unissued credential.
type Store interface {
	// Load returns the current snapshot, minting a fresh one when none exists.
	Load() (*Snapshot, error)
	// Save writes s if the stored revision still matches s.Revision, returning the new
	// revision. It returns ErrStaleWrite otherwise.
	Save(s *Snapshot) (int64, error)
}

// FileStore keeps the snapshot in the Tower's data directory. This is the development
// profile's storage, and it is genuinely durable for a single node - the durable profile
// exists for deployments whose disk is not.
type FileStore struct{ dir string }

// NewFileStore returns a Store backed by the data directory.
func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir} }

func (f *FileStore) path() string { return filepath.Join(f.dir, bootstrapFile) }

// Load reads the snapshot. A missing file is a fresh Tower; an UNREADABLE one is an
// error, because treating corrupt state as empty state would quietly re-mint the verifier
// secret and orphan every credential already issued.
func (f *FileStore) Load() (*Snapshot, error) {
	b, err := os.ReadFile(f.path())
	if os.IsNotExist(err) {
		return NewSnapshot()
	}
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Invitations == nil {
		s.Invitations = map[string]*Invitation{}
	}
	return &s, nil
}

// Save writes atomically via temp-plus-rename, refusing a stale write.
func (f *FileStore) Save(s *Snapshot) (int64, error) {
	cur, err := f.Load()
	if err != nil {
		return 0, err
	}
	if cur.Revision != s.Revision {
		return 0, ErrStaleWrite
	}
	next := s.Revision + 1
	out := *s
	out.Revision = next

	b, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		return 0, err
	}
	tmp := f.path() + ".tmp"
	if err := os.WriteFile(tmp, b, keyPerm); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, f.path()); err != nil {
		return 0, err
	}
	s.Revision = next
	return next, nil
}
