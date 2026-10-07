package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/agent"
)

// deadPID is the pid of a process that has already exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("no `true` binary: %v", err)
	}
	return cmd.Process.Pid
}

// A band a SEPARATE `roger share` process serves is this machine's own serve in LAN mode: the
// share writes a local, owner-only record the LAN relay reads, and nothing else carries it.
func TestLANShareRecordIsThisMachinesServe(t *testing.T) {
	useTempConfig(t)
	stop, err := writeLANShare("qwen3-8b", "http://127.0.0.1:11434", "up-key")
	require.NoError(t, err)

	url, key := lanSelfServe("qwen3-8b")
	require.Equal(t, "http://127.0.0.1:11434/v1/chat/completions", url, "routed to the share's own upstream")
	require.Equal(t, "up-key", key)
	require.Contains(t, lanSelfBands(), "qwen3-8b", "the dial lists it as this machine's")

	files, _ := filepath.Glob(filepath.Join(lanSharesDir(), "*.json"))
	require.Len(t, files, 1)
	fi, err := os.Stat(files[0])
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "the record holds the upstream key: owner-only")
	di, err := os.Stat(lanSharesDir())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), di.Mode().Perm())

	other, _ := lanSelfServe("gpt-oss-20b")
	require.Empty(t, other, "a band no share serves is not this machine's")

	stop()
	gone, _ := lanSelfServe("qwen3-8b")
	require.Empty(t, gone, "a stopped share is no longer served")
	require.NotContains(t, lanSelfBands(), "qwen3-8b")
}

// A record left by a share that died without cleaning up is ignored and removed.
func TestLANShareRecordOfADeadProcessIsIgnored(t *testing.T) {
	useTempConfig(t)
	require.NoError(t, writeLANShareRecord(lanShareRecord{PID: deadPID(t), Band: "qwen3-8b",
		ChatURL: "http://127.0.0.1:11434/v1/chat/completions"}))

	url, _ := lanSelfServe("qwen3-8b")
	require.Empty(t, url, "a dead share's record is never routed to")
	require.NotContains(t, lanSelfBands(), "qwen3-8b")
	files, _ := filepath.Glob(filepath.Join(lanSharesDir(), "*.json"))
	require.Empty(t, files, "the stale record is removed")
}

// A damaged record is skipped, never fatal.
func TestLANShareRecordDamagedIsSkipped(t *testing.T) {
	useTempConfig(t)
	require.NoError(t, os.MkdirAll(lanSharesDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(lanSharesDir(), "share-1.json"), []byte("{not json"), 0o600))
	url, _ := lanSelfServe("qwen3-8b")
	require.Empty(t, url)
	require.Empty(t, lanShares())
}

// `roger share` writes the record while it serves and removes it when it stops.
func TestCmdShareRecordsItsServeForTheLANRelay(t *testing.T) {
	useTempConfig(t)
	origStart, origBlock := agentStart, shareBlock
	t.Cleanup(func() { agentStart, shareBlock = origStart, origBlock })
	var during string
	agentStart = func(agent.Config) (*agent.Session, error) {
		during, _ = lanSelfServe("m1")
		return nil, fmt.Errorf("stop here")
	}
	shareBlock = func() {}
	_ = cmdShare(config{Broker: "https://b", User: "u"}, []string{"m1", "--upstream", "http://127.0.0.1:5555/v1"})
	require.Equal(t, "http://127.0.0.1:5555/v1/chat/completions", during, "on air: this machine serves m1")
	after, _ := lanSelfServe("m1")
	require.Empty(t, after, "stopped: the record is gone")
}
