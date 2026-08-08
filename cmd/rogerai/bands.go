package main

import (
	"fmt"

	"rogerai.fm/roger/v5/internal/agent"
	"rogerai.fm/roger/v5/internal/client"
)

// cmdBands is the owner-facing private-band verb group: list | move | revoke.
//
// THE GAP IT CLOSES: `roger share --private` MINTS a band from the CLI, but nothing could
// see, move or revoke one afterwards - `roger bands` simply did not exist. An operator who
// minted from a terminal had no way to learn what they held, and the broker's own quota
// refusal ("private band limit reached ... revoke an existing band first") named an action
// the CLI could not perform. Mirrors `roger grant`'s shape.
//
// There is deliberately no `bands create`: a band cannot be minted on its own. It is born
// only when a model goes on air privately (mintBandForNode is called from /nodes/register),
// so the command that creates one is `roger share --private`, and this help says so.
func cmdBands(cfg config, args []string) error {
	if len(args) == 0 {
		bandsUsage()
		return nil
	}
	switch args[0] {
	case "list", "ls":
		return bandsList(cfg)
	case "move", "mv":
		if len(args) < 3 {
			return fmt.Errorf("usage: roger bands move <band-id> <model>  (the band keeps its frequency code)")
		}
		return bandsMove(cfg, args[1], args[2])
	case "revoke", "rm":
		// Never infer WHICH band to burn: a revoke is irreversible and the code dies with
		// it, so the id is always explicit even when the owner holds exactly one.
		if len(args) < 2 {
			return fmt.Errorf("usage: roger bands revoke <band-id>  (run `roger bands list` for ids)")
		}
		return bandsRevoke(cfg, args[1])
	case "help", "--help", "-h":
		bandsUsage()
		return nil
	}
	return fmt.Errorf("unknown bands command %q; run 'roger bands help'", args[0])
}

func bandsUsage() {
	fmt.Println(`roger bands - your private bands (hidden stations only a frequency code can tune)

  roger bands list                       what you hold, and which model each one is on
  roger bands move <band-id> <model>     point a band at another model on THIS machine
                                         - it KEEPS its frequency code, so nobody tuned
                                           in is cut off
  roger bands revoke <band-id>           burn a band's code for good, freeing your slot

  a band is minted by putting a model on air privately:  roger share --private
  the frequency code is shown ONCE at mint and is never stored - if it is lost, revoke
  the band and mint a new one.`)
}

func bandsList(cfg config) error {
	bands, err := client.ListBands(cfg.Broker)
	if err != nil {
		return err
	}
	if len(bands) == 0 {
		fmt.Println("no private bands yet - `roger share --private` mints one (a one-time frequency code)")
		return nil
	}
	fmt.Printf("  %-22s %-28s %-10s %s\n", "BAND", "FREQUENCY", "STATUS", "ON")
	for _, b := range bands {
		// The node id is printed WHOLE. A station callsign is not always three words, so
		// splitting it off would silently rename someone's model in the one place they
		// look to identify it.
		on := b.NodeID
		if on == "" {
			on = "-"
		}
		fmt.Printf("  %-22s %-28s %-10s %s\n", b.ID, b.Display, b.Status, on)
	}
	fmt.Println("\n  move one to a different model and it keeps its code:  roger bands move <band-id> <model>")
	return nil
}

// bandsMove repoints a band at another model on THIS machine. The destination node id MUST
// be built with the same helper the share path registers with, or the band binds to an id
// no node will ever claim and quietly stops resolving for everyone.
func bandsMove(cfg config, bandID, model string) error {
	station := cfg.Station
	if station == "" {
		return fmt.Errorf("this install has no station callsign yet - run `roger share` once to create one")
	}
	nodeID := agent.ShareNodeID(station, model, 0)
	if err := client.MoveBand(cfg.Broker, bandID, nodeID); err != nil {
		return err
	}
	fmt.Printf("moved - %s now answers on the same frequency code (node %s)\n", model, nodeID)
	fmt.Println("it binds when that model next goes on air privately: roger share --private --model " + model)
	return nil
}

func bandsRevoke(cfg config, bandID string) error {
	if err := client.RevokeBand(cfg.Broker, bandID); err != nil {
		return err
	}
	fmt.Printf("revoked %s - that frequency code no longer resolves for anyone, and cannot be revived\n", bandID)
	fmt.Println("your free band slot is available again: roger share --private")
	return nil
}
