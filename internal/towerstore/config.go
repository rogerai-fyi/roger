package towerstore

import (
	"fmt"
	"os"
	"strings"

	"rogerai.fm/roger/v7/internal/tower"
)

// ForConfig returns the store a Tower command must use: the data directory st, or, when the
// operator configured durable storage, st backed by that database. It fails closed: an
// unreadable database URL file is an error, never a quiet fall back to the data directory (a
// Tower that looks configured and keeps its admissions in the wrong place). The close function
// releases the database; it is a no-op for the data directory.
func ForConfig(c *tower.Config, st *tower.State) (*tower.State, func() error, error) {
	noop := func() error { return nil }
	if c == nil || c.Storage == nil || c.Storage.URLFile == "" {
		return st, noop, nil
	}
	raw, err := os.ReadFile(c.Storage.URLFile)
	if err != nil {
		return nil, noop, fmt.Errorf("cannot read the database URL file %s: %w", c.Storage.URLFile, err)
	}
	pg, err := Open(strings.TrimSpace(string(raw)), nil)
	if err != nil {
		return nil, noop, err
	}
	return st.WithStore(pg), pg.Close, nil
}
