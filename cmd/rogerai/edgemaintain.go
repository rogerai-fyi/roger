package main

// MAINTENANCE, host side (features/edge/maintenance.feature).
//
//	roger edge maintenance [on|off]                        the owner's switch, ON THIS MACHINE (off by default)
//	roger edge maintain <node> start|stop|restart <agent>  act on one of that machine's agents
//	roger edge logs <node>                                 read its recent log lines, redacted
//
// The two locks are both the owner's, on two machines: this switch on the TARGET, and a grant the
// authority signed scoped to `maintain` AND that node, held by the CALLER. Neither alone is enough.
// The actions only ever touch rogers the target's owner already marked persistent THERE, by name,
// and its own recent log lines: it is not a shell, and it runs nothing of the caller's choosing.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/tui"
)

// edgeMaintenanceOn reports whether the owner turned maintenance on THIS machine.
func edgeMaintenanceOn(cfg config) bool { return cfg.EdgeMaintenance }

// cmdEdgeMaintenance is the owner's switch on this machine.
func cmdEdgeMaintenance(cfg config, args []string) error {
	leaf, _ := edgeLeaf("maintenance")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	if len(argv.pos) == 0 {
		edgePrintMaintenance(cfg)
		return nil
	}
	on, ok := edgeParseOnOff(argv.pos[0])
	if !ok {
		return usagef("usage: roger edge maintenance %s", leaf.args)
	}
	cfg.EdgeMaintenance = on
	if err := saveConfig(cfg); err != nil {
		return err
	}
	if on {
		fmt.Println("maintenance is ON for this machine.")
		edgePrintMaintenanceGrants()
		fmt.Println("  this roger declares maintain from its next registration; `roger edge maintenance off` ends it.")
		return nil
	}
	fmt.Println("maintenance is OFF for this machine. No member can act on it, grant or not.")
	return nil
}

// edgePrintMaintenanceGrants says EVERYTHING the switch grants, and the boundaries that remain. The
// owner is consenting to it, so it is generated from the action list rather than written out: an
// action added without a word here would tell them less than they are giving, and the test that pins
// this fails if the two ever drift.
func edgePrintMaintenanceGrants() {
	fmt.Printf("  a member holding a grant scoped to maintain on this node can: %s.\n", edgeMaintenanceGrantsLine())
	fmt.Println("  it CANNOT run a command, a script or a shell, reboot or shut the machine down, read any")
	fmt.Println("  file, or touch a roger you did not mark persistent. The log it can read is redacted.")
}

// edgeMaintenanceGrantsLine words what the actions are, from the actions themselves.
func edgeMaintenanceGrantsLine() string {
	acts := []string{}
	for _, a := range edge.MaintainActs {
		switch a {
		case edge.MaintainStart:
			acts = append(acts, "START a persistent agent that is down")
		case edge.MaintainStop:
			acts = append(acts, "STOP one that is running")
		case edge.MaintainRestart:
			acts = append(acts, "RESTART one")
		default:
			acts = append(acts, strings.ToUpper(string(a))+" one")
		}
	}
	return strings.Join(acts, ", ") + ", and READ this machine's recent log lines"
}

func edgePrintMaintenance(cfg config) {
	if edgeMaintenanceOn(cfg) {
		fmt.Println("maintenance: ON for this machine.")
		edgePrintMaintenanceGrants()
	} else {
		fmt.Println("maintenance: OFF for this machine (the default).")
		fmt.Println("  no member can act on it, grant or not.")
	}
	pers := edge.PersistentAgents(edgePersistDir())
	if len(pers) == 0 {
		fmt.Println("  there are no persistent agents here, so there is nothing to start.")
	} else {
		names := make([]string, 0, len(pers))
		for _, p := range pers {
			names = append(names, p.Name)
		}
		fmt.Println("  the agents that could be started: " + strings.Join(names, ", "))
	}
	fmt.Println("\n  roger edge maintenance on | off")
}

// edgeMaintainRun is the CALLEE side: the whole action, after the face has authenticated the
// caller, checked the grant and checked this node declares maintain. It returns the result to put
// on the wire, or a refusal sentence. selfInstance is the roger serving the request, which can
// never be its own target.
func edgeMaintainRun(cfg config, callerID, callerName, selfInstance string, raw json.RawMessage, now time.Time) (edge.MaintainResult, string) {
	if !edgeMaintenanceOn(cfg) {
		return edge.MaintainResult{}, edge.MaintainOffReason
	}
	task, refused := edge.ParseMaintainTask(raw)
	if refused != "" {
		return edge.MaintainResult{}, refused
	}
	if task.Action == edge.MaintainLogs {
		// THE LOG TAIL (features/edge/logs.feature): this roger's own ring, redacted by Tail on the
		// way out. Nothing on disk is reachable and no path comes from the caller.
		lines, held := edgeLogRing.Tail(task.Lines)
		says := fmt.Sprintf("%d of %d lines from %s, secrets removed", len(lines), held, selfInstance)
		if held == 0 {
			says = "nothing has been logged on this machine yet"
		}
		edgeRecordMaintenance(callerID, callerName, task, now)
		return edge.MaintainResult{Action: task.Action, Lines: lines, Held: held, Says: says}, ""
	}
	if task.Target == selfInstance {
		return edge.MaintainResult{}, "that is the roger answering this request, and it cannot start, stop or " +
			"restart itself through its own face. On a machine running several rogers the node's recorded " +
			"face is whichever of them last observed itself, so a request for the machine can land on the " +
			"one you named - address another roger on it instead, as <node>/<roger>, and `roger edge " +
			"describe <node>` lists them"
	}
	res := edge.MaintainResult{Action: task.Action, Target: task.Target}
	var says, why string
	switch task.Action {
	case edge.MaintainStop:
		says, res.Changed, why = edgeStopAgent(task.Target, now)
	case edge.MaintainRestart:
		says, res.Changed, why = edgeRestartAgent(cfg, task.Target, now)
	default: // start
		says, res.Changed, why = edgeStartAgent(cfg, task.Target, now)
	}
	if why != "" {
		return res, why
	}
	res.Says = says
	if res.Changed {
		edgeRecordMaintenance(callerID, callerName, task, now)
	}
	return res, ""
}

// edgeRecordMaintenance puts the action on THIS machine's own fleet row, so the owner can see what
// was done to it and by whom, on the same history every other event is on.
// edgeLogRing is THIS roger's own log ring: the standard logger is teed into it at start-up, so the
// lines still reach wherever they went before and a member with a maintain grant can read the last
// few hundred of them, redacted (features/edge/logs.feature).
var edgeLogRing = edge.NewLogRing(0)

// edgeTeeLog points the standard logger at both its current destination and the ring. It returns a
// function that puts the logger back, so a test (or a TUI, which redirects the logger itself) can
// undo it.
func edgeTeeLog() func() {
	prev := log.Writer()
	log.SetOutput(io.MultiWriter(prev, edgeLogRing))
	// A TUI takes the standard logger for itself while it runs (internal/tui/logcap.go), which would
	// otherwise leave this ring empty for the whole session - exactly when an operator is looking. The
	// mirror hands it the same lines.
	tui.SetLogMirror(edgeLogRing)
	return func() {
		tui.SetLogMirror(nil)
		log.SetOutput(prev)
	}
}

func edgeRecordMaintenance(callerID, callerName string, task edge.MaintainTask, now time.Time) {
	st, err := loadEdgeState()
	if err != nil {
		return
	}
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return
	}
	who := callerName
	if who == "" {
		who = edgeDisplayName(callerID)
	}
	detail := fmt.Sprintf("%s %s, asked by %s", task.Action, task.Target, who)
	if task.Target == "" {
		detail = fmt.Sprintf("%s, asked by %s", task.Action, who)
	}
	if err := st.fleet.RecordEvent(id.NodeID, "maintenance", detail); err != nil {
		return
	}
	_ = st.save()
}

// cmdEdgeMaintain is the CALLER side: `roger edge maintain <node> start <agent>`.
func cmdEdgeMaintain(cfg config, args []string) error {
	leaf, _ := edgeLeaf("maintain")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	if len(argv.pos) < 3 {
		return usagef("usage: roger edge maintain %s", leaf.args)
	}
	node, action, target := argv.pos[0], argv.pos[1], argv.pos[2]
	res, err := edgeMaintainInvoke(cfg, node, edge.MaintainTask{Action: edge.MaintainAction(strings.ToLower(action)), Target: target})
	if err != nil {
		return err
	}
	fmt.Println(res.Says)
	return nil
}

func mustTaskJSON(t edge.MaintainTask) json.RawMessage {
	b, _ := json.Marshal(t)
	return b
}

// edgeNameOfNode is a member's owner-given name, for saying WHO asked; "" when the owner never named
// it (or the fleet cannot be read), and the caller then says it with edgeDisplayName.
func edgeNameOfNode(id string) string {
	if id == "" {
		return ""
	}
	st, err := loadEdgeState()
	if err != nil {
		return ""
	}
	if n, ok, err := st.fleet.Get(id); err == nil && ok && n.Name != n.ID {
		return n.Name // an id is not a name: a node the owner never named reads as a SHORT id
	}
	return ""
}

// edgeDisplayName is what a node is called in a sentence: this machine by its own label, any other
// unit by its owner's name or what it is (edge.UnitName) - never by its id.
func edgeDisplayName(id string) string {
	st, err := loadEdgeState()
	if err != nil {
		return edgeGoneUnit
	}
	nodes, err := st.list()
	if err != nil {
		return edgeGoneUnit
	}
	for _, n := range nodes {
		if n.ID != id {
			continue
		}
		if id == edgeThisNodeID() {
			return edge.SelfLabel(n.Name, edgeStationName())
		}
		return edge.UnitName(n, nodes)
	}
	return edgeGoneUnit
}

// edgeMaintainableNodes is the members that declare maintain, for the surfaces that say so.
func edgeMaintainableNodes(nodes []store.EdgeNode) map[string]bool {
	out := map[string]bool{}
	for _, n := range nodes {
		if edge.Declares(n, edge.Maintain) {
			out[n.ID] = true
		}
	}
	return out
}
