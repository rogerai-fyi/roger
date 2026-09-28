package edge

// MAINTENANCE (features/edge/maintenance.feature): the smallest remote action that closes the
// monitoring loop - START an agent the owner already marked persistent on that machine, from
// another machine, when the screens say it is down.
//
// This file owns the vocabulary and the refusals that do not depend on the host: what an action is,
// what it names, and which actions exist. WHAT actually happens lives in cmd/rogerai (the host
// knows how to launch a roger); the face reaches it through Serving.Invoke like every other
// capability, so the certificate check, the grant check, the revocation check, the nonce cache and
// the receipts are the ones invoke already makes. Nothing new is opened.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaintainAction is what a maintenance task asks for.
type MaintainAction string

const (
	// MaintainStart launches a persistent agent that is not running.
	MaintainStart MaintainAction = "start"
	// The actions that are NOT built. They are named so a caller is refused by name rather than
	// with "unknown", and so nothing pretends they exist.
	MaintainStop     MaintainAction = "stop"
	MaintainRestart  MaintainAction = "restart"
	MaintainReboot   MaintainAction = "reboot"
	MaintainShutdown MaintainAction = "shutdown"
)

// MaintainTask is the task payload of a maintenance invoke.
type MaintainTask struct {
	Action MaintainAction `json:"action"`
	Target string         `json:"target"` // the agent the action acts on
}

// MaintainResult is what the callee answers with.
type MaintainResult struct {
	Action  MaintainAction `json:"action"`
	Target  string         `json:"target"`
	Changed bool           `json:"changed"` // false when it was already so
	Says    string         `json:"says"`
}

// notBuilt names the actions that are specified and deliberately not implemented, with the words
// the caller is refused with.
var notBuilt = map[MaintainAction]string{
	MaintainStop:     "stopping a running agent is not built yet",
	MaintainRestart:  "restarting a running agent is not built yet - start one that is down instead",
	MaintainReboot:   "rebooting a machine is not built yet",
	MaintainShutdown: "shutting a machine down is not built yet",
}

// ParseMaintainTask reads a maintenance task and refuses everything that cannot mean what it says,
// before the host looks anything up. The returned string is the refusal, "" when the task is good.
func ParseMaintainTask(raw json.RawMessage) (MaintainTask, string) {
	var t MaintainTask
	if json.Unmarshal(raw, &t) != nil {
		return t, "that maintenance task could not be read"
	}
	t.Action = MaintainAction(strings.ToLower(strings.TrimSpace(string(t.Action))))
	t.Target = strings.TrimSpace(t.Target)
	if t.Action == "" {
		return t, "a maintenance task names an action; the one there is: start"
	}
	if why, ok := notBuilt[t.Action]; ok {
		return t, why
	}
	if t.Action != MaintainStart {
		return t, fmt.Sprintf("%q is not a maintenance action; the one there is: start", string(t.Action))
	}
	if t.Target == "" {
		return t, "a maintenance action names the agent it acts on"
	}
	if err := ValidInstanceName(t.Target); err != nil {
		return t, "that is not an agent name: " + err.Error()
	}
	return t, ""
}

// MaintainOffReason is the refusal a machine gives when its owner has not turned maintenance on
// there. It is the same sentence wherever it is said.
const MaintainOffReason = "maintenance is off on that machine; its owner turns it on there with `roger edge maintenance on`"
