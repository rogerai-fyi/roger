package main

// Coverage for `roger config` get/set over every key it takes (and the refusals for values it does
// not), a few top-level routes, and the atomic config write refusing rather than half-writing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigGetAndSetEveryKey(t *testing.T) {
	useTempConfig(t)
	get := func(key string) string {
		t.Helper()
		out, code := edgeRun(t, "config", "get", key)
		require.Equal(t, 0, code, out)
		return strings.TrimSpace(out)
	}
	require.Equal(t, "on", get("deck"), "the deck is on by default")
	require.Equal(t, "full", get("palette"))
	require.Equal(t, "unlimited", get("agent-timeout"))
	require.Equal(t, "false", get("webui-open"), "auto-open is off by default")

	for _, tc := range []struct{ key, bad, want string }{
		{"webui-open", "maybe", "usage: roger config set webui-open true|false"},
		{"palette", "neon", "usage: roger config set palette full|mono"},
		{"deck", "sideways", "usage: roger config set deck on|off"},
		{"agent-timeout", "forever-ish", ""},
	} {
		out, code := edgeRun(t, "config", "set", tc.key, tc.bad)
		require.Equal(t, 1, code, "%s=%s: %s", tc.key, tc.bad, out)
		require.Contains(t, out, tc.want)
	}
	require.Equal(t, "on", get("deck"), "a refused value changes nothing")

	for _, kv := range [][2]string{{"webui-open", "true"}, {"palette", "mono"}, {"deck", "off"}, {"agent-timeout", "90s"},
		{"broker", "https://broker.example/"}, {"user", "operator"}} {
		out, code := edgeRun(t, "config", "set", kv[0], kv[1])
		require.Equal(t, 0, code, out)
		require.Contains(t, out, "set "+kv[0]+" = "+kv[1])
	}
	require.Equal(t, "true", get("webui-open"))
	require.Equal(t, "mono", get("palette"))
	require.Equal(t, "off", get("deck"))
	require.NotEqual(t, "unlimited", get("agent-timeout"))
	require.Equal(t, "https://broker.example", get("broker"), "a trailing slash is trimmed")
	require.Equal(t, "operator", get("user"))

	out, code := edgeRun(t, "config")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "palette = mono")
	require.Contains(t, out, "deck   = off")
	require.Contains(t, out, configPath())

	out, code = edgeRun(t, "config", "clear-limit", "tiny")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "cleared limit for tiny")
}

func TestTopLevelRoutesReachTheirCommands(t *testing.T) {
	useTempConfig(t)
	out, code := edgeRun(t, "context")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "roger context - carry a conversation across operators")
	out, code = edgeRun(t, "say")
	require.Equal(t, 1, code)
	require.Contains(t, out, "which voice? pass --voice <voice>")
	out, code = edgeRun(t, "resume", "--help")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "usage: roger resume [session-id]")
}

func TestAtomicWriteConfigRefusesRatherThanHalfWrites(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	require.Error(t, atomicWriteConfig(filepath.Join(blocker, "config.json"), []byte("{}")),
		"a directory that cannot exist is an error")

	p := filepath.Join(dir, "cfg", "config.json")
	require.NoError(t, atomicWriteConfig(p, []byte(`{"user":"a"}`)))
	fi, err := os.Stat(p)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "it can hold a credential, so it stays 0600")

	if os.Geteuid() != 0 {
		require.NoError(t, os.Chmod(filepath.Dir(p), 0o500))
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(p), 0o700) })
		require.Error(t, atomicWriteConfig(p, []byte(`{"user":"b"}`)))
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		require.Equal(t, `{"user":"a"}`, string(b), "a failed write leaves the old config whole")
	}
}
