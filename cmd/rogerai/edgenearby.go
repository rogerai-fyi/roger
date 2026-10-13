package main

// edgenearby.go is `roger edge nearby` and `roger edge approve` (features/edge/empty_edge_simple.feature
// §7, founder ruling 2026-10-12): the devices pinging on this network that are not on the Edge, each
// with the two commands that act on it. `roger edge invite --for` is in edgeinvite.go, and
// `roger edge adopt` stays one release as the old name for approve.

import (
	"fmt"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/look"
)

func cmdEdgeNearby(cfg config, args []string) error {
	leaf, _ := edgeLeaf("nearby")
	if _, err := parseEdgeArgv(leaf, args); err != nil {
		return err
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	if len(st.candidates) == 0 {
		fmt.Println(look.NearbyCLINone)
		return nil
	}
	fmt.Println(look.NearbyCLIHead)
	now := time.Now()
	for _, c := range st.candidates {
		label, short := look.NearbyKey(c.Pin, c.ID)
		facts := c.Kind
		if c.LastSeen > 0 {
			facts += " · " + look.NearbySeen(now.Sub(time.Unix(c.LastSeen, 0)))
		}
		// The id is printed whole: it is what the two commands take, and what tells two devices with
		// one name apart.
		fmt.Printf("  %s  %s · %s %s\n    id %s\n", edge.UnitName(c, st.candidates), facts, label, short, c.ID)
		fmt.Println("    " + look.NearbyCLIInvite(c.ID))
		fmt.Println("    " + look.NearbyCLIApprove(c.ID))
	}
	return nil
}

// cmdEdgeApprove lets one nearby device on: a phone (no certificate) by granting its claim and
// printing its code, a roger device by dialing the certificate it serves, checking it against its
// advert, and enrolling it. The TUI's a on a roger device instead waits for it to ask and shows the
// phrase; that needs the request the device sends to the running authority, which only the process
// serving it holds.
func cmdEdgeApprove(cfg config, args []string) error {
	leaf, _ := edgeLeaf("approve")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	msg, err := edgeAdoptWant(argv.pos[0])
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}
