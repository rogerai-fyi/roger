package edge

// PERSISTENT AGENTS (features/edge/agents.feature §3). An agent declared with the command is
// PERSISTENT: the Edge keeps a durable record that this agent is meant to be running, so when its
// process stops the surfaces still draw it - dark - and offer to resume it. This is distinct from
// the ephemeral instance registry (internal/edge/instance.go), which ages out when a process
// stops; a persistent record outlives the process and is removed only on the owner's say-so.
//
// The record is deliberately small and honest: a name, the node it belongs to, and when it was
// marked. It carries NO command line and NO secret - resume is a plain `roger` invocation the
// host performs, not a stored script.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PersistentAgent is the durable "this agent should be running" record.
type PersistentAgent struct {
	Name     string `json:"name"`
	Node     string `json:"node"` // the node id this agent runs on
	MarkedAt int64  `json:"marked_at"`
}

// persistFileName is a safe file name for an agent name. Instance/agent names are already
// validated (ValidInstanceName: letters, digits, - and _), so a name maps to a file directly;
// anything unexpected is folded to keep the path safe.
func persistFileName(name string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
	return safe + ".json"
}

// MarkPersistent writes (or refreshes) the durable record for one agent. It is idempotent.
func MarkPersistent(dir, node, name string, now time.Time) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(PersistentAgent{Name: name, Node: node, MarkedAt: now.Unix()}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, persistFileName(name)), b, 0o600)
}

// PersistentAgents reads every durable record. Non-nil even when empty: an empty set is the fact
// "nothing is meant to persist here", distinct from a directory that cannot be read.
func PersistentAgents(dir string) []PersistentAgent {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	out := []PersistentAgent{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p PersistentAgent
		if json.Unmarshal(b, &p) != nil || p.Name == "" {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// IsPersistent reports whether an agent of this name is marked persistent.
func IsPersistent(dir, name string) bool {
	for _, p := range PersistentAgents(dir) {
		if p.Name == name {
			return true
		}
	}
	return false
}

// UnmarkPersistent removes one durable record. It reports whether a record was actually there,
// so a caller can tell "removed" from "nothing to remove"; removing a missing one is not an error
// (the desired end state - not persistent - already holds).
func UnmarkPersistent(dir, name string) (bool, error) {
	f := filepath.Join(dir, persistFileName(name))
	if _, err := os.Stat(f); err != nil {
		if os.IsNotExist(err) {
			return false, nil // nothing to remove
		}
		return false, err // an unreadable record is an error, not "already gone"
	}
	if err := os.Remove(f); err != nil {
		return false, err
	}
	return true, nil
}
