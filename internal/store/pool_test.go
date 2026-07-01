package store

import (
	"os"
	"testing"
	"time"
)

// The broker's managed Postgres is a small shared cluster (~22 usable backend
// connections across EVERY app on it). An unbounded pool on 2 broker instances can
// exhaust the cluster and take down unrelated products. NewPostgres must therefore
// bound the pool, with env overrides for bigger clusters later.
func TestPoolLimitsFromEnv(t *testing.T) {
	cases := []struct {
		name         string
		maxConns     string // ROGERAI_DB_MAX_CONNS
		lifetime     string // ROGERAI_DB_CONN_LIFETIME
		wantOpen     int
		wantLifetime time.Duration
	}{
		{name: "defaults", wantOpen: 8, wantLifetime: 30 * time.Minute},
		{name: "override open", maxConns: "4", wantOpen: 4, wantLifetime: 30 * time.Minute},
		{name: "override lifetime", lifetime: "5m", wantOpen: 8, wantLifetime: 5 * time.Minute},
		{name: "both", maxConns: "16", lifetime: "1h", wantOpen: 16, wantLifetime: time.Hour},
		{name: "zero open falls back", maxConns: "0", wantOpen: 8, wantLifetime: 30 * time.Minute},
		{name: "negative open falls back", maxConns: "-3", wantOpen: 8, wantLifetime: 30 * time.Minute},
		{name: "garbage open falls back", maxConns: "lots", wantOpen: 8, wantLifetime: 30 * time.Minute},
		{name: "garbage lifetime falls back", lifetime: "soon", wantOpen: 8, wantLifetime: 30 * time.Minute},
		{name: "zero lifetime falls back", lifetime: "0s", wantOpen: 8, wantLifetime: 30 * time.Minute},
		{name: "negative lifetime falls back", lifetime: "-1m", wantOpen: 8, wantLifetime: 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ROGERAI_DB_MAX_CONNS", tc.maxConns)
			t.Setenv("ROGERAI_DB_CONN_LIFETIME", tc.lifetime)
			open, life := poolLimits()
			if open != tc.wantOpen {
				t.Errorf("open: got %d, want %d", open, tc.wantOpen)
			}
			if life != tc.wantLifetime {
				t.Errorf("lifetime: got %v, want %v", life, tc.wantLifetime)
			}
		})
	}
}

// NewPostgres must actually apply the bounds to the live pool (real Postgres, no mocks).
func TestNewPostgresBoundsPool(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping real-Postgres pool-bounds test")
	}
	t.Run("default bound", func(t *testing.T) {
		t.Setenv("ROGERAI_DB_MAX_CONNS", "")
		pg, err := NewPostgres(dsn)
		if err != nil {
			t.Fatalf("NewPostgres: %v", err)
		}
		defer pg.Close()
		if got := pg.db.Stats().MaxOpenConnections; got != 8 {
			t.Errorf("MaxOpenConnections: got %d, want 8", got)
		}
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv("ROGERAI_DB_MAX_CONNS", "3")
		pg, err := NewPostgres(dsn)
		if err != nil {
			t.Fatalf("NewPostgres: %v", err)
		}
		defer pg.Close()
		if got := pg.db.Stats().MaxOpenConnections; got != 3 {
			t.Errorf("MaxOpenConnections: got %d, want 3", got)
		}
	})
}
