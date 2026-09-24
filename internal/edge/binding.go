package edge

// AGENT ASSIGNMENTS (features/edge/jobs.feature). An agent on the Edge can be given WORK: a model
// bound to it (with a use/share posture) and a named job. Without one, an agent is drawn honestly
// as idle - it is an agent, but it is doing nothing, and the surfaces say so rather than faking it.
//
// The record is durable and small, exactly like the persistent-agent record beside it: one file
// per agent, a name, its node, the model, the posture, the job and its targets, and when it was
// set. It carries no secret and no command line.
//
// USE vs SHARE, the two postures on the bound model (the founder's distinction, 2026-09-21):
//   - Share: the model is on air to the Edge; other authorized nodes may route to it.
//   - Use:   the agent draws on the model for its own work, but does not offer it out.
// They are independent: a model may be used, shared, both, or (bound but idle) neither.
//
// THE JOB vocabulary (small and honest to start): "serve" keeps a model on air, "watch" monitors
// named nodes, "relay" carries requests to a model on another node, "" is none.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AgentJob is the durable "this is what the agent does" record.
type AgentJob struct {
	Name    string   `json:"name"`              // the agent's instance name
	Node    string   `json:"node"`              // the node id it runs on
	Model   string   `json:"model,omitempty"`   // the model bound to it, "" for none
	Use     bool     `json:"use,omitempty"`     // draws on the model for its own work
	Share   bool     `json:"share,omitempty"`   // offers the model to the Edge
	Job     string   `json:"job,omitempty"`     // serve | watch | relay | "" (none)
	Targets []string `json:"targets,omitempty"` // watch/relay targets, by node name
	SetAt   int64    `json:"set_at,omitempty"`
}

// SharesModel returns the model this agent puts on air to the Edge, or "" when it shares nothing.
func (j AgentJob) SharesModel() string {
	if j.Share {
		return j.Model
	}
	return ""
}

// UsesModel returns the model this agent draws on for its own work, or "" when it uses none.
func (j AgentJob) UsesModel() string {
	if j.Use {
		return j.Model
	}
	return ""
}

// IsIdle reports whether the agent has no work at all: no job and no bound model.
func (j AgentJob) IsIdle() bool { return j.Job == "" && j.Model == "" }

// SetAgentJob writes (or replaces) one agent's assignment. Idempotent, and a blank name is a no-op
// (there is nothing to assign work to). It follows MarkPersistent's file-per-name shape.
func SetAgentJob(dir string, j AgentJob, now time.Time) error {
	if strings.TrimSpace(j.Name) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	j.SetAt = now.Unix()
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, persistFileName(j.Name)), b, 0o600)
}

// AgentJobs reads every assignment. Non-nil even when empty: an empty set is the fact "no agent
// here has work", distinct from a directory that cannot be read. Sorted by name.
func AgentJobs(dir string) []AgentJob {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	out := []AgentJob{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var j AgentJob
		if json.Unmarshal(b, &j) != nil || j.Name == "" {
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// AgentJobOf returns one agent's assignment, and whether it has one.
func AgentJobOf(dir, name string) (AgentJob, bool) {
	f := filepath.Join(dir, persistFileName(name))
	b, err := os.ReadFile(f)
	if err != nil {
		return AgentJob{}, false
	}
	var j AgentJob
	if json.Unmarshal(b, &j) != nil || j.Name != name {
		return AgentJob{}, false
	}
	return j, true
}

// ClearAgentJob removes one assignment, reporting whether one was there (so a caller can tell
// "cleared" from "nothing to clear"). Clearing a missing one is not an error.
func ClearAgentJob(dir, name string) (bool, error) {
	f := filepath.Join(dir, persistFileName(name))
	if _, err := os.Stat(f); err != nil {
		return false, nil
	}
	if err := os.Remove(f); err != nil {
		return false, err
	}
	return true, nil
}
