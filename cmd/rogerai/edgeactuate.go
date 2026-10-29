package main

// THE ACTUATE CEREMONY (features/edge/node_identity.feature).
//
//	roger edge actuate <node> [--yes]
//
// `actuate` is the one capability that is DECLARED and OWNER-CONFIRMED and never probed, because it
// is the node saying it can change the physical world: a door, a pump, a relay, a moving machine.
// Everything else on this Edge may be verified by observation; this may only be GRANTED, by a
// person, on purpose.
//
// The rule shipped with the fleet and nothing could perform it. Fleet.ConfirmActuate existed, with
// the audit trail the rule asks for, and had no caller on any surface - so a node declaring actuate
// was pending for ever and its owner had no way to say yes. That is worse than not having the
// feature: the node looks adopted and is quietly inert.
//
// The ceremony is the same everywhere it appears: name the node, say what confirming LICENSES, ask
// first, and record who said yes and when.

import (
	"fmt"
	"time"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func cmdEdgeActuate(cfg config, args []string) error {
	leaf, _ := edgeLeaf("actuate")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	st, n, ok, err := edgeLookup(argv.pos[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no node %q on this Edge - `roger edge list` shows what is", argv.pos[0])
	}
	cap, declared := edgeActuateCap(n)
	if !declared {
		return fmt.Errorf("%s does not declare actuate, so there is nothing to confirm", edgeNodeLabel(n))
	}
	if cap.State == string(edge.Verified) {
		// Saying yes twice must not re-record: the FIRST confirmation is the one that licensed it,
		// and overwriting who and when would quietly destroy the only audit trail a physical action
		// has before it happens.
		who := cap.ConfirmedBy
		if who == "" {
			who = "somebody"
		}
		fmt.Printf("%s is already confirmed to actuate - %s said yes %s ago.\n",
			edgeNodeLabel(n), who, edge.Age(edgeRenderNow().Sub(time.Unix(cap.ConfirmedAt, 0))))
		return nil
	}
	if !argv.has("yes") {
		fmt.Printf("confirm that %s may ACTUATE?\n", edgeNodeLabel(n))
		fmt.Println("  actuate means this node can change the PHYSICAL WORLD: a door, a pump, a relay,")
		fmt.Println("  a moving machine. Nothing on this Edge infers it and no probe can verify it -")
		fmt.Println("  it is yours to grant, and your name goes on it.")
		fmt.Print("  confirm [y/N]: ")
		if !edgeConfirmed() {
			fmt.Println("left pending. Nothing changed.")
			return nil
		}
	}
	who := edgeOperatorName()
	if err := st.fleet.ConfirmActuate(n.ID, who); err != nil {
		return err
	}
	if err := st.save(); err != nil {
		return err
	}
	fmt.Printf("%s may actuate. Confirmed by %s, and recorded on the node.\n", edgeNodeLabel(n), who)
	return nil
}

// edgeActuateCap is a node's actuate declaration, and whether it declares one at all.
func edgeActuateCap(n store.EdgeNode) (store.EdgeCap, bool) {
	for _, c := range n.Caps {
		if c.Name == string(edge.Actuate) {
			return c, true
		}
	}
	return store.EdgeCap{}, false
}
