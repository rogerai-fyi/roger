package main

import (
	"os"
	"path/filepath"
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
