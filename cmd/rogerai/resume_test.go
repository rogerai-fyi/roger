package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/node"
	"github.com/rogerai-fyi/roger/internal/session"
	"github.com/rogerai-fyi/roger/internal/tui"
	"github.com/stretchr/testify/require"
)

func TestCmdResumeHelpDoesNotTouchSessionStore(t *testing.T) {
	oldDir := resumeStoreDir
	resumeStoreDir = func() string {
		t.Fatal("resume --help must not open the session store")
		return ""
	}
	t.Cleanup(func() { resumeStoreDir = oldDir })

	out := captureStdout(t, func() {
		require.NoError(t, cmdResume(config{}, []string{"--help"}))
	})
	require.Contains(t, out, "roger resume [session-id]")
}

func TestCmdResumeWithRuntimePreservesNoticeAndWebConsole(t *testing.T) {
	dir, item := seedResumeStore(t)
	oldDir, oldRun, oldWeb := resumeStoreDir, runResumedTUI, startWebConsoleFn
	t.Cleanup(func() {
		resumeStoreDir, runResumedTUI, startWebConsoleFn = oldDir, oldRun, oldWeb
	})
	resumeStoreDir = func() string { return dir }

	var webCtrl, tuiCtrl *node.Controller
	startWebConsoleFn = func(_ config, ctrl *node.Controller, port string) string {
		webCtrl = ctrl
		require.Equal(t, "5099", port)
		return "http://127.0.0.1:5099/?t=resume"
	}
	runResumedTUI = func(_ string, _ string, _ *tui.LimitStore, notice string, hooks tui.Hooks, ctrl *node.Controller, got session.Snapshot) error {
		tuiCtrl = ctrl
		require.Equal(t, item.ID, got.ID)
		require.Equal(t, "upgrade ready", notice)
		require.Equal(t, "http://127.0.0.1:5099/?t=resume", hooks.ConsoleURL)
		return nil
	}

	require.NoError(t, cmdResumeWithRuntime(config{}, []string{item.ID}, "upgrade ready", true, "5099"))
	require.Same(t, webCtrl, tuiCtrl)
}

func TestCanonicalRogerVersionAndHelpBranding(t *testing.T) {
	version := captureStdout(t, func() { require.NoError(t, dispatch(config{}, []string{"version"})) })
	require.True(t, strings.HasPrefix(version, "roger "))

	help := captureStdout(t, usage)
	require.True(t, strings.HasPrefix(help, "roger -"))
}

func seedResumeStore(t *testing.T) (string, session.Snapshot) {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	item := session.Snapshot{
		Version: session.CurrentVersion, ID: "th_resume123", Title: "Continue the work",
		Workdir: t.TempDir(), CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, session.NewStore(dir).Save(item))
	return dir, item
}

func TestCmdResumeKnownIDBypassesPickerAndLaunchesRestoredTUI(t *testing.T) {
	dir, want := seedResumeStore(t)
	oldDir, oldInteractive := resumeStoreDir, resumeInteractive
	oldPick, oldRun := pickResumeSession, runResumedTUI
	t.Cleanup(func() {
		resumeStoreDir, resumeInteractive = oldDir, oldInteractive
		pickResumeSession, runResumedTUI = oldPick, oldRun
	})
	resumeStoreDir = func() string { return dir }
	resumeInteractive = func() bool { return true }
	pickResumeSession = func([]session.Snapshot, string) (session.Snapshot, bool, error) {
		t.Fatal("known ID must bypass picker")
		return session.Snapshot{}, false, nil
	}
	var got session.Snapshot
	runResumedTUI = func(_ string, _ string, _ *tui.LimitStore, _ string, _ tui.Hooks, _ *node.Controller, item session.Snapshot) error {
		got = item
		return nil
	}

	require.NoError(t, cmdResume(config{Broker: "https://broker.example", User: "u"}, []string{"th_resume"}))
	require.Equal(t, want.ID, got.ID)
}

func TestCmdResumeBareInteractiveUsesPicker(t *testing.T) {
	dir, want := seedResumeStore(t)
	oldDir, oldInteractive := resumeStoreDir, resumeInteractive
	oldPick, oldRun := pickResumeSession, runResumedTUI
	t.Cleanup(func() {
		resumeStoreDir, resumeInteractive = oldDir, oldInteractive
		pickResumeSession, runResumedTUI = oldPick, oldRun
	})
	resumeStoreDir = func() string { return dir }
	resumeInteractive = func() bool { return true }
	pickResumeSession = func(items []session.Snapshot, cwd string) (session.Snapshot, bool, error) {
		require.Len(t, items, 1)
		require.NotEmpty(t, cwd)
		return items[0], false, nil
	}
	var got string
	runResumedTUI = func(_ string, _ string, _ *tui.LimitStore, _ string, _ tui.Hooks, _ *node.Controller, item session.Snapshot) error {
		got = item.ID
		return nil
	}

	require.NoError(t, cmdResume(config{}, nil))
	require.Equal(t, want.ID, got)
}

func TestCmdResumeBareNonInteractiveListsWithoutLaunching(t *testing.T) {
	dir, want := seedResumeStore(t)
	oldDir, oldInteractive := resumeStoreDir, resumeInteractive
	oldRun := runResumedTUI
	t.Cleanup(func() {
		resumeStoreDir, resumeInteractive, runResumedTUI = oldDir, oldInteractive, oldRun
	})
	resumeStoreDir = func() string { return dir }
	resumeInteractive = func() bool { return false }
	runResumedTUI = func(_ string, _ string, _ *tui.LimitStore, _ string, _ tui.Hooks, _ *node.Controller, _ session.Snapshot) error {
		t.Fatal("bare non-interactive resume only lists")
		return nil
	}

	out := captureStdout(t, func() { require.NoError(t, cmdResume(config{}, nil)) })
	require.Contains(t, out, want.ID)
	require.Contains(t, out, want.Title)
	require.Contains(t, out, "roger resume <id>")
}

func TestCmdResumeMissingDirectoryLoadsReadOnly(t *testing.T) {
	dir, item := seedResumeStore(t)
	require.NoError(t, os.RemoveAll(item.Workdir))
	oldDir, oldRun := resumeStoreDir, runResumedTUI
	t.Cleanup(func() { resumeStoreDir, runResumedTUI = oldDir, oldRun })
	resumeStoreDir = func() string { return dir }
	runResumedTUI = func(_ string, _ string, _ *tui.LimitStore, _ string, _ tui.Hooks, _ *node.Controller, got session.Snapshot) error {
		require.Equal(t, item.Workdir, got.Workdir)
		require.False(t, got.WorkdirAvailable)
		return nil
	}
	require.NoError(t, cmdResume(config{}, []string{item.ID}))
	require.NoError(t, os.MkdirAll(filepath.Dir(item.Workdir), 0o755))
}
