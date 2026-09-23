package main

// `roger edge model` and `roger edge job` - binding a model and a job to an agent on the Edge
// (features/edge/jobs.feature). An agent with no assignment is idle and drawn so; these give it
// work: a model to serve or use, and a named job. The records are durable, one file per agent,
// beside the persistent-agent records.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
)

// edgeJobsDir is where the durable agent-assignment records live (jobs.feature), beside the
// ephemeral instance registry and the persistent-agent records.
func edgeJobsDir() string { return filepath.Join(filepath.Dir(configPath()), "edge-jobs") }

// edgeParsePosture turns a posture word into use/share flags. The default (empty) is share: the
// common intent is to put a local model on air for the Edge.
func edgeParsePosture(s string) (use, share bool, ok bool) {
	if strings.TrimSpace(s) == "" {
		return false, true, true
	}
	use, share = false, false
	for _, part := range strings.Split(strings.ToLower(s), ",") {
		switch strings.TrimSpace(part) {
		case "use":
			use = true
		case "share":
			share = true
		default:
			return false, false, false
		}
	}
	return use, share, true
}

// cmdEdgeModel binds a model to an agent with a use/share posture:
//
//	roger edge model <agent> <model> [use|share|use,share]
func cmdEdgeModel(cfg config, args []string) error {
	if len(args) < 2 {
		return usagef("roger edge model <agent> <model> [use|share|use,share]")
	}
	agent, model := args[0], args[1]
	posture := ""
	if len(args) > 2 {
		posture = args[2]
	}
	use, share, ok := edgeParsePosture(posture)
	if !ok {
		return usagef("posture is use, share, or use,share; %q is none of them", posture)
	}
	if err := edge.ValidInstanceName(agent); err != nil {
		return err
	}
	// Keep an existing job over the model if one is already set for this agent.
	j, _ := edge.AgentJobOf(edgeJobsDir(), agent)
	j.Name, j.Node, j.Model, j.Use, j.Share = agent, edgeThisNodeID(), model, use, share
	if err := edge.SetAgentJob(edgeJobsDir(), j, time.Now()); err != nil {
		return err
	}
	fmt.Printf("bound %q to %q (%s).\n", model, agent, edgePostureWord(use, share))
	if share {
		fmt.Println("  it is on air to the Edge: other authorized nodes may route to it.")
	}
	if use && !share {
		fmt.Println("  it is private to this agent: used for its own work, not offered out.")
	}
	return nil
}

// edgePostureWord renders the posture for the confirmation line.
func edgePostureWord(use, share bool) string {
	switch {
	case use && share:
		return "use and share"
	case use:
		return "use"
	case share:
		return "share"
	default:
		return "bound, idle"
	}
}

// cmdEdgeJob assigns (or clears) an agent's job:
//
//	roger edge job <agent> <serve|watch|relay|none> [targets...]
func cmdEdgeJob(cfg config, args []string) error {
	if len(args) < 2 {
		return usagef("roger edge job <agent> <serve|watch|relay|none> [targets...]")
	}
	agent, job := args[0], strings.ToLower(args[1])
	if err := edge.ValidInstanceName(agent); err != nil {
		return err
	}
	if job == "none" {
		had, err := edge.ClearAgentJob(edgeJobsDir(), agent)
		if err != nil {
			return err
		}
		if had {
			fmt.Printf("%q has no job now; its assignment is cleared.\n", agent)
		} else {
			fmt.Printf("%q had no job; nothing to clear.\n", agent)
		}
		return nil
	}
	switch job {
	case "serve", "watch", "relay":
	default:
		return usagef("a job is serve, watch, relay or none; %q is none of them", job)
	}
	j, _ := edge.AgentJobOf(edgeJobsDir(), agent)
	j.Name, j.Node, j.Job = agent, edgeThisNodeID(), job
	if len(args) > 2 {
		j.Targets = append([]string{}, args[2:]...)
	} else {
		j.Targets = nil
	}
	if err := edge.SetAgentJob(edgeJobsDir(), j, time.Now()); err != nil {
		return err
	}
	line := fmt.Sprintf("%q now runs the %s job", agent, job)
	if len(j.Targets) > 0 {
		line += " over " + strings.Join(j.Targets, ", ")
	}
	if job == "serve" && j.Model != "" {
		line += " on " + j.Model
	}
	fmt.Println(line + ".")
	if job == "serve" && j.Model == "" {
		fmt.Println("  no model is bound yet: run `roger edge model " + agent + " <model>` to put one on air.")
	}
	return nil
}
