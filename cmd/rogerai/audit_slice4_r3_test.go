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

// TestSaveConfigNeverOverwritesAnUnreadableConfig: a config.json this process could not read
// (not missing, unreadable) is refused rather than replaced with defaults.
func TestSaveConfigNeverOverwritesAnUnreadableConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	useTempConfig(t)
	path := configPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(`{"user":"real","broker":"https://keep.example"}`), 0o600))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	c := loadConfig()
	require.Error(t, saveConfig(c), "an unreadable config is not overwritten")
	require.NoError(t, os.Chmod(path, 0o600))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(b), "keep.example")
}

// TestSaveConfigChecksTheFileItIsAboutToReplace: the read saveConfig makes under the lock is
// the one that decides; a config.json that turned unreadable after load is not overwritten.
func TestSaveConfigChecksTheFileItIsAboutToReplace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	useTempConfig(t)
	path := configPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(`{"user":"real","broker":"https://keep.example"}`), 0o600))
	c := loadConfig() // readable now
	c.User = "changed"
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	require.Error(t, saveConfig(c))
	require.NoError(t, os.Chmod(path, 0o600))
	b, _ := os.ReadFile(path)
	require.Contains(t, string(b), "keep.example")
}

// TestProfileSetNullIsRefused: `profile set <key> null` is not a value; unset removes a key.
func TestProfileSetNullIsRefused(t *testing.T) {
	useTempConfig(t)
	require.NoError(t, cmdProfile([]string{"set", "p", "roger.pref", "fast"}))
	err := cmdProfile([]string{"set", "p", "roger.pref", "null"})
	require.ErrorContains(t, err, "roger profile unset")
	b, _ := os.ReadFile(configPath())
	require.NotContains(t, string(b), "null")
}
