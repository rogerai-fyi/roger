package main

// `roger edge fleet`: the Edge fleet screen (internal/fleetview, on Limoni) over the real Edge.
// Its source is the same state the CLI reads, and its actions are the same code the CLI's
// adopt / name / forget / prefer run - what the screen does, the CLI could have done.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/fleetview"
	"rogerai.fm/roger/v6/internal/store"
)

// edgeRunFleetScreen opens the screen (a seam: the test records the source and actions).
var edgeRunFleetScreen = func(ctx context.Context, src fleetview.FleetSource, acts fleetview.FleetActions) error {
	return fleetview.NewFleet(src, fleetview.NewHealth(edgeHealthSource), acts).Run(ctx)
}

func cmdEdgeFleet(cfg config, args []string) error {
	leaf, _ := edgeLeaf("fleet")
	if _, err := parseEdgeArgv(leaf, args); err != nil {
		return err
	}
	return edgeRunFleetScreen(context.Background(), edgeFleetSource, edgeFleetActions())
}

// edgeFleetSource is one read of the whole Edge as the screen draws it: this machine's facts,
// its rogers, the members (arranged as the TUI and console arrange them), the candidates, each
// with its health and the rules firing for it.
func edgeFleetSource() (fleetview.FleetSnapshot, error) {
	now := time.Now()
	st, err := loadEdgeState()
	if err != nil {
		return fleetview.FleetSnapshot{}, err
	}
	nodes, err := st.list()
	if err != nil {
		return fleetview.FleetSnapshot{}, err
	}
	self := edgeSelfStatus(st.fleet, edgeDiscoveryFactsFromEnv())
	if self.Err != "" {
		return fleetview.FleetSnapshot{}, fmt.Errorf("%s", self.Err)
	}
	health := map[string]*edge.NodeHealth{}
	if hs, err := edge.FleetHealth(st.fleet, now); err == nil {
		for i := range hs {
			health[hs[i].ID] = &hs[i]
		}
	}
	firing := edgeFiringAlerts()
	snap := fleetview.FleetSnapshot{At: now, Self: fleetview.SelfFacts{
		Name: self.Name, Root: self.RootBadge(), Prefer: self.PreferBadge(), PreferValue: self.EffectivePrefer(),
		Authority: "authority · " + edgeAuthorityWord(self), AuthorityLines: self.AuthorityLines(), Machine: self.MachineLine(),
		Discovery: self.DiscoveryLine(now), Enrolled: self.Enrolled}}
	if self.AuthorityHere && self.AuthorityAddr != "" {
		snap.Self.Join = self.EnrollAgainstLine()
	}
	if snap.Self.Name == "" {
		snap.Self.Name = "this machine"
	}
	// The hub.
	hub := fleetview.FleetItem{Kind: fleetview.ItemHub, ID: self.NodeID, Name: snap.Self.Name, NodeKind: "host",
		Presence: "online", Health: health[self.NodeID], Alerts: firing[self.NodeID]}
	snap.Items = append(snap.Items, hub)
	// Its rogers: the live household, then the persistent agents that are not running. This
	// process is a screen, not a registered roger, so no instance is marked as "this one".
	jobs := map[string]edge.AgentJob{}
	for _, j := range edge.AgentJobs(edgeJobsDir()) {
		jobs[j.Name] = j
	}
	live := map[string]bool{}
	for _, in := range edge.Household(edgeInstancesDir(), now) {
		live[in.Name] = true
		it := fleetview.FleetItem{Kind: fleetview.ItemInstance, Name: in.Name, Bands: in.Bands, Caps: edgeCapsOf(in.Caps)}
		if in.Agent {
			it.Kind = fleetview.ItemAgent
		}
		if j, ok := jobs[in.Name]; ok {
			it.Job = edgeJobWords(j)
		}
		snap.Items = append(snap.Items, it)
	}
	for _, p := range edge.PersistentAgents(edgePersistDir()) {
		if !live[p.Name] {
			snap.Items = append(snap.Items, fleetview.FleetItem{Kind: fleetview.ItemAgent, Name: p.Name, Dark: true})
		}
	}
	// The members, arranged; this machine's own row is the hub, not a member.
	for _, r := range edge.Arrange(nodes) {
		n := r.Node
		if n.ID == self.NodeID {
			continue
		}
		dark := edge.Presence(n.Presence) == edge.PresenceDark
		it := fleetview.FleetItem{Kind: fleetview.ItemMember, ID: n.ID, Name: n.Name, NodeKind: n.Kind, Dark: dark,
			Caps: edgeCapsOf(n.Caps), Via: r.Via, Health: health[n.ID], Alerts: firing[n.ID]}
		if n.LastSeen > 0 {
			it.LastSeen = time.Unix(n.LastSeen, 0)
		}
		switch {
		case dark && n.LastSeen > 0:
			it.Presence = "offline " + edge.Age(now.Sub(it.LastSeen))
		case dark:
			it.Presence = "offline"
		case edge.Presence(n.Presence) == edge.PresenceVerified:
			it.Presence = "online"
		default:
			it.Presence = strings.ToLower(n.Presence)
		}
		for _, in := range n.Instances {
			it.Bands = append(it.Bands, in.Bands...)
		}
		snap.Items = append(snap.Items, it)
	}
	for _, c := range st.candidates {
		snap.Items = append(snap.Items, fleetview.FleetItem{Kind: fleetview.ItemCandidate, ID: c.ID, Name: c.Name, NodeKind: c.Kind,
			Presence: "discovered", Caps: edgeCapsOf(c.Caps)})
	}
	return snap, nil
}

func edgeCapsOf(caps []store.EdgeCap) []fleetview.Cap {
	out := make([]fleetview.Cap, 0, len(caps))
	for _, c := range caps {
		out = append(out, fleetview.Cap{Name: c.Name, State: c.State})
	}
	return out
}

// edgeJobWords is an agent's assignment in a few words: "serve qwen3-8b", "watch gate, cam".
func edgeJobWords(j edge.AgentJob) string {
	s := j.Job
	if s == "" && j.Model != "" {
		s = "model"
	}
	switch {
	case j.Job == "serve" && j.Model != "":
		s += " " + j.Model
	case len(j.Targets) > 0:
		s += " " + strings.Join(j.Targets, ", ")
	case j.Model != "":
		s += " " + j.Model
	}
	return strings.TrimSpace(s)
}

// edgeFleetActions are the screen's keys, each the CLI's own code path.
func edgeFleetActions() fleetview.FleetActions {
	return fleetview.FleetActions{
		Adopt: func(id, _ string) (string, error) { return edgeAdoptWant(id) },
		Forget: func(id string) error {
			st, n, ok, err := edgeLookup(id)
			if err != nil {
				return err
			}
			if !ok {
				return errNoSuchNode(id)
			}
			_, err = edgeForgetMember(st, n)
			return err
		},
		Rename: func(id, name string) error {
			st, n, ok, err := edgeLookup(id)
			if err != nil {
				return err
			}
			if !ok {
				return errNoSuchNode(id)
			}
			if err := st.fleet.Rename(n.ID, name); err != nil {
				return err
			}
			return st.save()
		},
		SetPrefer: func(want string) error {
			if !edge.ValidPrefer(want) {
				return fmt.Errorf("prefer must be local, market or local-only, not %q", want)
			}
			cfg := loadConfig()
			cfg.EdgePrefer = want
			return saveConfig(cfg)
		},
	}
}

// edgeAuthorityWord is the one word after "authority ·" on the hub's card: LOCAL or CORE.
func edgeAuthorityWord(s edge.SelfStatus) string {
	if s.AuthorityLocal {
		return "LOCAL"
	}
	return "CORE"
}
