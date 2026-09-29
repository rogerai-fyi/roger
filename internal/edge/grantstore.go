package edge

// THE EDGE GRANT STORE (features/edge/control.feature, the @cli scenarios). When the owner issues an
// Edge grant, it is signed by the authority and has to live SOMEWHERE the owner's roger can find it
// again - to list it, to revoke it, and to carry it on `roger edge invoke`. It is a plain JSON file
// under the edge dir, not the authority dir, because a grant carries store.Grant and edgeauth is
// Core-free by construction: the record that imports store stays on this side of that line, exactly
// as grant signing does.
//
// It is deliberately spare: an authority-signed grant is self-describing and self-bounding (it
// carries its own expiry and its own signature), so the store is a list, not a database. It holds
// the issuing owner's copies; a grant travels to a caller on the wire, in the invoke request.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"rogerai.fm/roger/v6/internal/store"
)

// GrantStore is a file-backed list of the Edge grants this owner has issued.
type GrantStore struct {
	Path string
	mu   sync.Mutex
}

// Put stores a grant, replacing any earlier one with the same id (a re-issue), and persists atomically.
func (s *GrantStore) Put(g store.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.flock()
	if err != nil {
		return err
	}
	defer unlock()
	got, err := s.read()
	if err != nil {
		return err
	}
	for i := range got {
		if got[i].ID == g.ID {
			got[i] = g
			return s.write(got)
		}
	}
	return s.write(append(got, g))
}

// List returns every stored grant, in the order issued.
func (s *GrantStore) List() ([]store.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// Get returns one grant by id.
func (s *GrantStore) Get(id string) (store.Grant, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	got, err := s.read()
	if err != nil {
		return store.Grant{}, false, err
	}
	for _, g := range got {
		if g.ID == id {
			return g, true, nil
		}
	}
	return store.Grant{}, false, nil
}

// Revoke marks a grant revoked in place. It reports whether the id was found. A revoked grant is
// kept, not deleted, so `roger edge grant` still shows it as revoked rather than silently gone.
func (s *GrantStore) Revoke(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.flock()
	if err != nil {
		return false, err
	}
	defer unlock()
	got, err := s.read()
	if err != nil {
		return false, err
	}
	for i := range got {
		if got[i].ID == id {
			if got[i].Revoked {
				return true, nil
			}
			got[i].Revoked = true
			return true, s.write(got)
		}
	}
	return false, nil
}

// read is the unlocked load shared by every method while holding s.mu. A missing file is an empty
// store, never an error - the first issue creates it.
func (s *GrantStore) read() ([]store.Grant, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []store.Grant
	if len(b) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// write persists atomically: a temp file in the same dir, then a rename, so a crash mid-write never
// leaves a half-written grant list (the same discipline edgeauth uses for its records).
func (s *GrantStore) write(gs []store.Grant) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".grants-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

// flock serialises the store's read-modify-write ACROSS processes (the host and a CLI or a screen
// issuing beside it), which the in-process mutex alone cannot.
func (s *GrantStore) flock() (func(), error) { return FileLock(s.Path) }

// FileLock takes an exclusive advisory lock covering path, blocking until it is held, and returns
// the release. It locks a sidecar (path + ".lock") rather than the file itself, so the holder is
// free to replace the file atomically while the lock stands - which is how every store here writes.
//
// This is what makes a READ-MODIFY-WRITE of a shared file safe across processes. On this machine
// several rogers touch the same files at once: the host's own pass, whatever the owner types in a
// second terminal, and the fleet screen, which is a child process of its own. Without the lock the
// last writer wins with whatever it happened to be holding, and everything the others did in the
// meantime is gone (the founder's vanished phone, 2026-09-27).
func FileLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flockExclusive(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = flockUnlock(f); f.Close() }, nil
}
