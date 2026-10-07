package main

// audit_slice4_test.go - slice-4 pre-push audit regressions for the config writers: a config
// file that cannot be read is never rewritten from scratch, `profile unset` never invents a
// profile, success is printed only after the write, and a repeated set-limit list flag adds
// to the list instead of replacing it.

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEditConfigRawRefusesAnUnreadableConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := configPath()
	require.NoError(t, cmdProfile([]string{"set", "p", "roger.pref", "fast"}))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	var werr error
	out := captureStdout(t, func() { werr = cmdProfile([]string{"set", "q", "roger.pref", "cheap"}) })
	require.Error(t, werr, "an unreadable config is an error, never an empty document")
	require.NotContains(t, out, "set profile", "success is printed only after the write")
	require.NoError(t, os.Chmod(path, 0o600))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "the file is untouched")
}

func TestProfileUnsetNeverCreatesAProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var err error
	out := captureStdout(t, func() { err = cmdProfile([]string{"unset", "nope", "roger.pref"}) })
	require.Error(t, err)
	require.Contains(t, err.Error(), "no profile named nope")
	require.NotContains(t, out, "unset profile")
	if b, rerr := os.ReadFile(configPath()); rerr == nil {
		require.NotContains(t, string(b), "nope")
	}

	require.NoError(t, cmdProfile([]string{"set", "p", "roger.pref", "fast"}))
	out = captureStdout(t, func() { err = cmdProfile([]string{"unset", "p", "roger.pref"}) })
	require.NoError(t, err)
	require.Contains(t, out, "unset profile p roger.pref")
}

func TestSetLimitListFlagsAccumulate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, cmdSetLimit([]string{"m",
		"--quant", "Q8_0", "--quant", "BF16",
		"--require", "tools", "--require", "vision",
		"--region", "us", "--region", "eu"}))
	l := loadConfig().Limits.Models["m"]
	require.Equal(t, []string{"Q8_0", "BF16"}, l.Quants)
	require.Equal(t, []string{"tools", "vision"}, l.Require)
	require.Equal(t, []string{"us", "eu"}, l.Region)
}
