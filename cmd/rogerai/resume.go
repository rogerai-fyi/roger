package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mattn/go-isatty"
	"rogerai.fm/roger/internal/session"
	"rogerai.fm/roger/internal/tui"
)

var (
	resumeStoreDir    = session.DefaultDir
	resumeInteractive = func() bool {
		return (isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())) &&
			(isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()))
	}
	pickResumeSession = tui.SelectResumeSession
	runResumedTUI     = tui.RunResumedWithController
)

func cmdResume(cfg config, args []string) error {
	return cmdResumeWithRuntime(cfg, args, "", false, defaultWebuiPort)
}

func cmdResumeWithRuntime(cfg config, args []string, notice string, webuiOn bool, webuiPort string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Println("usage: roger resume [session-id]")
		fmt.Println("       roger continue [session-id]")
		return nil
	}
	if len(args) > 1 {
		return fmt.Errorf("usage: roger resume [session-id]")
	}
	store := session.NewStore(resumeStoreDir())
	items, warnings, err := store.List()
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, "warning: skipped session:", warning)
	}
	if len(items) == 0 {
		fmt.Println("No saved sessions. Complete an AGENT turn to create one.")
		return nil
	}

	var selected session.Snapshot
	if len(args) == 1 {
		selected, err = session.Resolve(items, args[0])
		if err != nil {
			return err
		}
	} else if !resumeInteractive() {
		for _, item := range items {
			fmt.Printf("%s  %-20s  %s\n", item.ID, item.UpdatedAt.Format(time.RFC3339), session.SafeLabel(item.Title))
		}
		fmt.Println("\nResume one with: roger resume <id>")
		return nil
	} else {
		cwd, _ := os.Getwd()
		var cancelled bool
		selected, cancelled, err = pickResumeSession(items, cwd)
		if err != nil {
			return err
		}
		if cancelled || selected.ID == "" {
			return nil
		}
	}

	selected.Workdir = filepath.Clean(selected.Workdir)
	info, statErr := os.Stat(selected.Workdir)
	selected.WorkdirAvailable = statErr == nil && info.IsDir()
	hooks := tuiHooks(cfg)
	ctrl := tui.NewController(cfg.Broker, hooks)
	if webuiOn {
		hooks.ConsoleURL = startWebConsoleFn(cfg, ctrl, webuiPort)
	}
	return runResumedTUI(cfg.Broker, cfg.User, tuiLimits(cfg), notice, hooks, ctrl, selected)
}
