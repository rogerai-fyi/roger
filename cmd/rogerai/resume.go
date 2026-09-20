package main

import (
	"fmt"
	"os"
	"path/filepath"
	"rogerai.fm/roger/v6/internal/client"
	"time"

	"github.com/mattn/go-isatty"
	"rogerai.fm/roger/v6/internal/session"
	"rogerai.fm/roger/v6/internal/tui"
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
	// The resumed TUI gets the same one Edge the fresh launch does (see run()).
	stopEdge := startEdge(&hooks)
	defer stopEdge()
	ctrl := tui.NewController(cfg.Broker, hooks)

	// This roger's bands on air feed its Edge registration (edgeinstance.go): the
	// household says what each process is really serving.
	edgeAttachOnAir(func() []string {
		var out []string
		for _, r := range ctrl.Snapshot().Rows {
			if r.Link == "on-air" {
				out = append(out, r.Model)
			}
		}
		return out
	})

	// The peer-serving upstream: a model this process has on air is served to Edge
	// peers from the same backend the market uses (features/edge/local_inference.feature).
	edgeAttachUpstream(func(band string) (string, string, bool) { return ctrl.ServeUpstreamFor(band) })

	// The dispatch ladder: the booth tries a peer on this Edge before the market
	// (features/edge/local_inference.feature), the same wiring `roger use` gets.
	hooks.EdgeLadder = func(o *client.ProxyOptions) { applyEdgeLadder(cfg, o) }
	// One store for both front-ends - see run()'s note.
	limits := tuiLimits(cfg)
	if webuiOn {
		hooks.ConsoleURL = startWebConsoleFn(cfg, ctrl, webuiPort, limits, &hooks)
	}
	return runResumedTUI(cfg.Broker, cfg.User, limits, notice, hooks, ctrl, selected)
}
