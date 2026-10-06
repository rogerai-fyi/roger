package main

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResolveUseValidatesStoredLimits: a hand-edited limits value that is not a contract
// value is refused at use, never shown as applied while setting nothing.
func TestResolveUseValidatesStoredLimits(t *testing.T) {
	ps := profilesOf(t, `{"profiles":{}}`)
	cfg := config{}
	cfg.Limits.Default = Limit{TrustMin: "Verified"}
	f, _, err := parseUseFlags([]string{"m"})
	require.NoError(t, err)
	_, err = resolveUse(cfg, f, ps)
	require.ErrorContains(t, err, "trust_min")
}

// TestUseOrderOutsideOnlyIsRefused: --order naming a station outside --only is refused.
func TestUseOrderOutsideOnlyIsRefused(t *testing.T) {
	ps := profilesOf(t, `{"profiles":{}}`)
	f, _, err := parseUseFlags([]string{"m", "--only", "n1", "--order", "n2"})
	if err == nil {
		_, err = resolveUse(config{}, f, ps)
	}
	require.ErrorContains(t, err, "outside provider.only")
}

// TestProfileSetRefusesUnknownSubKeys: a typo'd sub-key is refused, the file unchanged.
func TestProfileSetRefusesUnknownSubKeys(t *testing.T) {
	useTempConfig(t)
	require.NoError(t, cmdProfile([]string{"set", "p", "roger.pref", "fast"}))
	before, _ := os.ReadFile(configPath())
	require.Error(t, cmdProfile([]string{"set", "p", "roger.trust_mn", "verified"}))
	require.Error(t, cmdProfile([]string{"set", "p", "provider.max_price.complete", "2"}))
	after, _ := os.ReadFile(configPath())
	require.Equal(t, string(before), string(after))
}

// TestSaveConfigTakesTheConfigLock: saveConfig waits for a live config lock, so it never
// merges over a concurrent profile write.
func TestSaveConfigTakesTheConfigLock(t *testing.T) {
	useTempConfig(t)
	c := loadConfig()
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath()), 0o700))
	release, err := lockConfig(configPath() + ".lock")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- saveConfig(c) }()
	select {
	case <-done:
		t.Fatal("saveConfig wrote while another writer held the config lock")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	require.NoError(t, <-done)
}

// TestConfigLockReleasesOnlyItsOwn: a holder whose stale lock was taken over never removes
// the new holder's lock when it finally releases.
func TestConfigLockReleasesOnlyItsOwn(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "config.json.lock")
	release, err := lockConfig(lock)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(lock, []byte("someone-else"), 0o600)) // taken over meanwhile
	release()
	_, err = os.Stat(lock)
	require.NoError(t, err, "release removed a lock it no longer held")
}

// TestConfigLockStaleTakeoverIsExclusive: many waiters on one stale lock never hold it at
// the same time.
func TestConfigLockStaleTakeoverIsExclusive(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "config.json.lock")
	for round := 0; round < 20; round++ {
		require.NoError(t, os.WriteFile(lock, []byte("crashed"), 0o600))
		old := time.Now().Add(-time.Minute)
		require.NoError(t, os.Chtimes(lock, old, old))
		var active, overlap atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				release, err := lockConfig(lock)
				if err != nil {
					return
				}
				if active.Add(1) > 1 {
					overlap.Add(1)
				}
				time.Sleep(2 * time.Millisecond)
				active.Add(-1)
				release()
			}()
		}
		wg.Wait()
		require.Zero(t, overlap.Load(), "two waiters held the lock at once (round %d)", round)
	}
}
