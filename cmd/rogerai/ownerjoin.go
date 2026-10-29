package main

// ownerjoin.go fills the owner's join view for the TUI and the console from the same code the CLI
// runs (internal/towercli's owner helpers): the Towers this user serves (internal/onair records
// written by `roger tower serve`), their requests and fingerprints, invites and their outcome, and
// the Towers this machine joined and pinned (lan_tower_pins).

import (
	"sort"

	"rogerai.fm/roger/v7/internal/onair"
	"rogerai.fm/roger/v7/internal/ownerjoin"
	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towercli"
	"rogerai.fm/roger/v7/internal/towerpin"
)

func ownerJoinHooks() *ownerjoin.Hooks {
	return &ownerjoin.Hooks{
		Towers: func() []ownerjoin.Tower {
			var out []ownerjoin.Tower
			for _, t := range onair.Towers() {
				fp, err := towercli.OwnerFingerprint(t.Dir)
				if err != nil {
					continue // serving, but not answering on its admin socket yet
				}
				out = append(out, ownerjoin.Tower{Dir: t.Dir, Label: t.Label, Addr: t.Addr, FP: towerpin.Short(fp)})
			}
			return out
		},
		Requests: func(t ownerjoin.Tower) ([]tower.PairRequest, error) { return towercli.OwnerRequests(t.Dir) },
		Decide:   towercli.OwnerDecide,
		Invite: func(t ownerjoin.Tower) (ownerjoin.Invite, error) {
			inv, err := towercli.MintOwnerInvite(t.Dir, t.Addr)
			if err != nil {
				return ownerjoin.Invite{}, err
			}
			return ownerjoin.Invite{Dir: t.Dir, Tower: t.Label, ID: inv.ID, Heading: inv.Heading, Next: inv.Next,
				Line: inv.Line, ExpiresAt: inv.ExpiresAt}, nil
		},
		Outcome: towercli.OwnerInviteOutcome,
		Pinned: func() []ownerjoin.Pinned {
			pins := loadConfig().LanTowerPins
			out := make([]ownerjoin.Pinned, 0, len(pins))
			for addr, fp := range pins {
				out = append(out, ownerjoin.Pinned{Addr: addr, FP: towerpin.Short(fp)})
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
			return out
		},
	}
}
