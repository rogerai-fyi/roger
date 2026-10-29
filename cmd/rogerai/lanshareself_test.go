package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/node"
	"rogerai.fm/roger/v7/internal/onair"
)

// writeShareRecord writes a share's local record as a share process would.
func writeShareRecord(s onair.Share) error {
	_, err := onair.WriteShare(s)
	return err
}

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
	stop, err := onair.WriteShare(onair.Share{PID: os.Getpid(), Band: "qwen3-8b", ChatURL: node.NormalizeUpstream("http://127.0.0.1:11434"), Key: "up-key"})
	require.NoError(t, err)

	url, key := lanSelfServe("qwen3-8b")
	require.Equal(t, "http://127.0.0.1:11434/v1/chat/completions", url, "routed to the share's own upstream")
	require.Equal(t, "up-key", key)
	require.Contains(t, lanSelfBands(), "qwen3-8b", "the dial lists it as this machine's")

	files, _ := filepath.Glob(filepath.Join(onair.SharesDir(), "*.json"))
	require.Len(t, files, 1)
	fi, err := os.Stat(files[0])
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "the record holds the upstream key: owner-only")
	di, err := os.Stat(onair.SharesDir())
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
	require.NoError(t, writeShareRecord(onair.Share{PID: deadPID(t), Band: "qwen3-8b",
		ChatURL: "http://127.0.0.1:11434/v1/chat/completions"}))

	url, _ := lanSelfServe("qwen3-8b")
	require.Empty(t, url, "a dead share's record is never routed to")
	require.NotContains(t, lanSelfBands(), "qwen3-8b")
	files, _ := filepath.Glob(filepath.Join(onair.SharesDir(), "*.json"))
	require.Empty(t, files, "the stale record is removed")
}

// A damaged record is skipped, never fatal.
func TestLANShareRecordDamagedIsSkipped(t *testing.T) {
	useTempConfig(t)
	require.NoError(t, os.MkdirAll(onair.SharesDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(onair.SharesDir(), "share-1.json"), []byte("{not json"), 0o600))
	url, _ := lanSelfServe("qwen3-8b")
	require.Empty(t, url)
	require.Empty(t, onair.Shares())
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

// Every front-end's controller (TUI, console, resume) is built from tuiHooks: in LAN mode the
// hooks carry the LAN Towers so SHARE serves them; out of LAN mode they carry none.
func TestTUIHooksCarryTheLANTowersInLANMode(t *testing.T) {
	useTempConfig(t)
	cfg := loadConfig()
	cfg.LanTowers = []string{"192.168.1.20:8787"}
	require.Nil(t, tuiHooks(cfg).LANTowers, "LAN mode off: no LAN Towers")
	cfg.lan.LAN = true
	h := tuiHooks(cfg)
	require.NotNil(t, h.LANTowers)
	require.Equal(t, []string{"http://192.168.1.20:8787"}, h.LANTowers())
}

// A chat band whose modality was detected (or stated) as "chat" is still a chat band: its record
// is written like one with no modality, so the LAN relay and the Edge face can reach it. Detection
// names a local llama.cpp server's model "chat", and the record was skipped for exactly that.
func TestCmdShareRecordsAChatModalityServe(t *testing.T) {
	useTempConfig(t)
	origStart, origBlock := agentStart, shareBlock
	t.Cleanup(func() { agentStart, shareBlock = origStart, origBlock })
	var during string
	agentStart = func(agent.Config) (*agent.Session, error) {
		during, _ = lanSelfServe("m1")
		return nil, fmt.Errorf("stop here")
	}
	shareBlock = func() {}
	_ = cmdShare(config{Broker: "https://b", User: "u"}, []string{"m1", "--upstream", "http://127.0.0.1:5555/v1", "--modality", "chat"})
	require.Equal(t, "http://127.0.0.1:5555/v1/chat/completions", during, "a chat-modality share is this machine's serve too")
}
