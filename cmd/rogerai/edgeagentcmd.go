package main

// THE AGENT LIFECYCLE (features/edge/agents.feature §3): declare this roger a persistent agent
// (on/off), and resume or remove a persistent one. Persistence is a DURABLE record on the Edge
// (internal/edge/persist.go) so an agent that stops is still drawn - dark - and can be brought
// back. Resume is a PLAIN roger invocation (founder ruling 2026-09-21): it relaunches `roger`
// with the agent role and the agent's own name, detached, and iterates from there.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/tui"
)

// edgePersistDir is where the durable persistent-agent records live, beside the ephemeral
// instance registry.
func edgePersistDir() string { return filepath.Join(filepath.Dir(configPath()), "edge-agents") }

// edgeThisAgentName is the name THIS roger runs as - its configured instance name, or the node's
// default. It is what a persistent record is keyed by, and what resume relaunches.
func edgeThisAgentName(cfg config) string {
	node := edgeSelfStatus(nil, edgeDiscoveryFactsFromEnv()).Name
	taken := []string{}
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		taken = append(taken, in.Name)
	}
	return edgeInstanceName(cfg, node, taken)
}

// edgeThisNodeID is this machine's node id, or "" when it has not enrolled.
func edgeThisNodeID() string {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return ""
	}
	return id.NodeID
}

// edgeAgentShow prints whether this roger is an agent, and lists any persistent agents.
func edgeAgentShow(cfg config) error {
	if edgeIsAgentRole(cfg) {
		fmt.Println("this roger is an agent on your Edge (◆) - it can operate on other nodes.")
		if os.Getenv("ROGER_EDGE_AGENT") != "" {
			fmt.Println("  set for this run by ROGER_EDGE_AGENT (the env wins over the config).")
		}
	} else {
		fmt.Println("this roger is not an agent - it serves and uses the Edge, but does not operate on other nodes.")
	}
	live := map[string]bool{}
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		live[in.Name] = true
	}
	pers := edge.PersistentAgents(edgePersistDir())
	if len(pers) > 0 {
		fmt.Println("\npersistent agents (kept across restarts):")
		for _, p := range pers {
			state := "running"
			if !live[p.Name] {
				state = "not running · roger edge agent resume " + p.Name
			}
			fmt.Printf("  %-20s %s\n", p.Name, state)
		}
	}
	fmt.Println("\n  roger edge agent on | off | resume <name> | remove <name>")
	return nil
}

// edgeAgentSetRole turns the agent role on or off: it sets the config role AND writes/removes the
// durable persistent record, so `on` makes a persistent agent and `off` stops being one.
func edgeAgentSetRole(cfg config, on bool) error {
	cfg.EdgeAgent = on
	if err := saveConfig(cfg); err != nil {
		return err
	}
	name := edgeThisAgentName(cfg)
	if on {
		if err := edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), name, time.Now()); err != nil {
			return err
		}
		fmt.Printf("this roger is now a persistent agent on your Edge (◆), as %q.\n", name)
		fmt.Println("  it can operate on other nodes; if it stops, `roger edge agent resume` brings it back.")
	} else {
		if _, err := edge.UnmarkPersistent(edgePersistDir(), name); err != nil {
			return err
		}
		fmt.Println("this roger is no longer an agent, and no longer persistent.")
	}
	fmt.Println("  a running roger picks this up on its next pass; a new one starts with it.")
	return nil
}

// edgeSpawnRoger launches a plain, detached `roger` with the given extra environment. A seam so a
// test can record the launch instead of starting a process.
var edgeSpawnRoger = func(env map[string]string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(exe, edgeAgentRunArg)
	c.Env = os.Environ()
	for k, v := range env {
		c.Env = append(c.Env, k+"="+v)
	}
	c.Stdin, c.Stdout, c.Stderr = nil, nil, nil
	return edgeDetach(c) // start in its own session so it outlives this shell
}

// edgeAgentRunArg is the hidden argument a spawned agent is launched with. It runs the HEADLESS
// agent loop (below) instead of the interactive TUI, which a detached process with no terminal
// cannot run.
const edgeAgentRunArg = "__edge-agent"

// runEdgeAgent is the headless agent runtime: no TUI. It stands up this machine's Edge - registering
// this instance, serving its face, running discovery on the background host - and stays alive so the
// agent is genuinely present and operable, until it is signaled to stop. A plain no-arg `roger` ran
// the full-screen TUI, which exits immediately with no terminal while the launcher reported success
// (audit 2026-09-24).
func runEdgeAgent(cfg config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runEdgeAgentCtx(ctx, cfg)
}

// runEdgeAgentCtx is runEdgeAgent with the lifetime injected, so a test can start and stop it
// without sending a real signal to the test process.
func runEdgeAgentCtx(ctx context.Context, cfg config) error {
	// Build the host directly so a failure to read/stand up this machine's Edge is an ERROR that
	// exits, not a silent no-op that blocks forever while the launcher thinks the agent is running
	// (audit 2026-09-24).
	h, err := newEdgeHost(loadOrCreateStation())
	if err != nil {
		return fmt.Errorf("the Edge agent could not start: %w", err)
	}
	var hooks tui.Hooks
	h.wire(&hooks)
	h.start(context.Background())
	defer h.stop()
	<-ctx.Done()
	return nil
}

// edgeAgentResume relaunches a dark persistent agent as a plain roger with its role and name.
func edgeAgentResume(cfg config, name string) error {
	if !edge.IsPersistent(edgePersistDir(), name) {
		return fmt.Errorf("no persistent agent named %q - `roger edge agent` lists them", name)
	}
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		if in.Name == name {
			fmt.Printf("%q is already running - nothing to resume.\n", name)
			return nil
		}
	}
	if err := edgeSpawnRoger(map[string]string{"ROGER_EDGE_AGENT": "1", "ROGER_EDGE_INSTANCE": name}); err != nil {
		return fmt.Errorf("could not launch a roger for %q: %w", name, err)
	}
	fmt.Printf("launched a roger for %q as an agent; it returns to your Edge on its next registration.\n", name)
	return nil
}

// edgeAgentRemove drops a persistent agent's durable record. It does NOT kill a running process -
// remove stops the persistence (no more resume), it does not reach into a live agent.
func edgeAgentRemove(cfg config, name string) error {
	had, err := edge.UnmarkPersistent(edgePersistDir(), name)
	if err != nil {
		return err
	}
	if !had {
		fmt.Printf("no persistent agent named %q - nothing to remove.\n", name)
		return nil
	}
	fmt.Printf("removed the persistent agent %q; it will not be offered to resume.\n", name)
	fmt.Println("  a running one is left alone - remove stops the persistence, it does not stop a live process.")
	return nil
}
