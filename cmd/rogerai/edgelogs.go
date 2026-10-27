package main

// `roger edge logs <node> [--lines N]` (features/edge/logs.feature): a member's recent log lines,
// redacted on the serving side. It rides the MAINTENANCE path, so it is gated by the owner's switch
// on that machine and by a grant that carries maintain - no new authenticated path exists for it.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"rogerai.fm/roger/v7/internal/edge"
)

func cmdEdgeLogs(cfg config, args []string) error {
	leaf, _ := edgeLeaf("logs")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	if len(argv.pos) == 0 {
		return usagef("usage: roger edge logs %s", leaf.args)
	}
	node := argv.pos[0]
	lines := 0
	if v, ok := argv.flags["lines"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return usagef("--lines takes a whole number, not %q", v)
		}
		lines = n
	}
	res, name, err := edgeMaintainInvokeNamed(cfg, node, edge.MaintainTask{Action: edge.MaintainLogs, Lines: lines})
	if err != nil {
		return err
	}
	if len(res.Lines) == 0 {
		fmt.Printf("%s: %s\n", name, res.Says)
		return nil
	}
	fmt.Printf("LOG · %s · %s\n", name, res.Says)
	for _, ln := range res.Lines {
		fmt.Println("  " + ln)
	}
	return nil
}

// edgeMaintainInvoke is the one caller-side path for every maintenance action: resolve the node's
// face, find a live grant that carries maintain, present this machine's certificate, and read the
// result. `roger edge maintain` and `roger edge logs` both go through it.
func edgeMaintainInvoke(cfg config, node string, task edge.MaintainTask) (edge.MaintainResult, error) {
	res, _, err := edgeMaintainInvokeNamed(cfg, node, task)
	return res, err
}

// edgeMaintainInvokeNamed is edgeMaintainInvoke and the node's own NAME as the fleet resolved it, so
// a surface prints "shed" rather than echoing back the "." the operator typed.
func edgeMaintainInvokeNamed(cfg config, node string, task edge.MaintainTask) (edge.MaintainResult, string, error) {
	var out edge.MaintainResult
	if _, refused := edge.ParseMaintainTask(mustTaskJSON(task)); refused != "" {
		return out, node, fmt.Errorf("%s", refused) // refused here, before anything is sent
	}
	peer, calleeID, err := edgeResolveNodeFace(node)
	if err != nil {
		return out, node, err
	}
	name := peer.Node
	if name == "" {
		name = node
	}
	callerID := edgeThisNodeID()
	grants, err := edgeGrantStore().List()
	if err != nil {
		return out, name, err
	}
	g, ok := edgeSelectGrant(grants, callerID, calleeID, string(edge.Maintain), time.Now())
	if !ok {
		return out, name, fmt.Errorf("no live grant lets %s maintain %s - issue one on the authority with `roger edge grant self maintain %s`",
			edgeGrantWho(callerID), name, name)
	}
	cert := edgeCallerCert()
	if cert == nil {
		return out, name, fmt.Errorf("this machine has no Edge certificate to act with - run `roger edge enroll` first")
	}
	res, _, err := edgeDialInvokeTask(context.Background(), peer, cert, g, string(edge.Maintain), mustTaskJSON(task))
	if err != nil {
		return out, name, err
	}
	if res.Refused != "" {
		return out, name, fmt.Errorf("%s refused: %s", name, res.Refused)
	}
	if json.Unmarshal(res.Output, &out) != nil {
		return out, name, fmt.Errorf("%s answered with something that is not a maintenance result", name)
	}
	return out, name, nil
}
