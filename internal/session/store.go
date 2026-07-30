// Package session owns Roger's private, local durable AGENT snapshots.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"rogerai.fm/roger/internal/capsule"
)

const CurrentVersion = 1

var (
	ErrNotFound  = errors.New("session not found")
	ErrAmbiguous = errors.New("ambiguous session id")
)

// Snapshot is semantic conversation state, not a serialized live runtime. In-flight tool
// state, confirmation decisions, credentials, and provider tokens have no fields here.
type Snapshot struct {
	Version   int               `json:"version"`
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Workdir   string            `json:"workdir"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Model     string            `json:"model,omitempty"`
	Messages  []capsule.Message `json:"messages"`
	// WorkdirAvailable is derived at resume time and never persisted. False keeps a
	// missing-root session transcript-readable without silently widening tool access.
	WorkdirAvailable bool `json:"-"`
}

type Store struct {
	Dir string
	Now func() time.Time
	mu  sync.Mutex
}

func NewStore(dir string) *Store {
	return &Store{Dir: dir, Now: time.Now}
}

func DefaultDir() string {
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "rogerai", "sessions")
}

func validID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if !(r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func (s *Store) Save(in Snapshot) error {
	if s == nil || s.Dir == "" {
		return errors.New("session directory is empty")
	}
	if !validID(in.ID) {
		return fmt.Errorf("invalid session id %q", in.ID)
	}
	if in.Version == 0 {
		in.Version = CurrentVersion
	}
	if in.Version != CurrentVersion {
		return fmt.Errorf("unsupported session version %d", in.Version)
	}
	if !filepath.IsAbs(in.Workdir) {
		return fmt.Errorf("session workdir must be absolute")
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = s.Now()
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = s.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(s.Dir, 0o700)
	dst := filepath.Join(s.Dir, in.ID+".json")
	if old, err := readFile(dst); err == nil && old.UpdatedAt.After(in.UpdatedAt) {
		return nil
	}
	raw, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	f, err := os.CreateTemp(s.Dir, "."+in.ID+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	if d, err := os.Open(s.Dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func readFile(path string) (Snapshot, error) {
	var out Snapshot
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	if out.Version != CurrentVersion {
		return Snapshot{}, fmt.Errorf("unsupported session version %d", out.Version)
	}
	if !validID(out.ID) {
		return Snapshot{}, fmt.Errorf("invalid session id %q", out.ID)
	}
	if !filepath.IsAbs(out.Workdir) {
		return Snapshot{}, fmt.Errorf("session workdir must be absolute")
	}
	return out, nil
}

func (s *Store) List() ([]Snapshot, []string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []Snapshot
	var warnings []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		item, err := readFile(filepath.Join(s.Dir, entry.Name()))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", entry.Name(), err))
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	sort.Strings(warnings)
	return out, warnings, nil
}

func Resolve(items []Snapshot, query string) (Snapshot, error) {
	for _, item := range items {
		if item.ID == query {
			return item, nil
		}
	}
	var matches []Snapshot
	for _, item := range items {
		if strings.HasPrefix(item.ID, query) {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Snapshot{}, fmt.Errorf("%w: %q; run `roger resume` to choose one", ErrNotFound, query)
	default:
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		rows := make([]string, 0, len(matches))
		for _, item := range matches {
			rows = append(rows, item.ID+" ("+safeTitle(item.Title)+")")
		}
		return Snapshot{}, fmt.Errorf("%w %q: %s", ErrAmbiguous, query, strings.Join(rows, ", "))
	}
}

func safeTitle(s string) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// SafeLabel removes terminal controls from user-derived session metadata before a CLI/TUI
// prints it. It does not alter the persisted conversation or working-directory value.
func SafeLabel(s string) string { return safeTitle(s) }
