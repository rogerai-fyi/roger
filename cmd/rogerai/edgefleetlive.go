package main

// `roger edge fleet`: the Edge fleet screen (internal/fleetview, on Limoni) over the real Edge.
// Its source is the same state the CLI reads, and its actions are the same code the CLI's
// adopt / name / forget / prefer run - what the screen does, the CLI could have done.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
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
	// The fleet's health, JUDGED on this read (as `roger edge health` judges), so the screen
	// never shows a stale verdict and a standing call is standing the moment it is over its line.
	health := map[string]*edge.NodeHealth{}
	firing := map[string][]string{}
	if hs, err := edge.FleetHealth(st.fleet, now); err == nil {
		for i := range hs {
			health[hs[i].ID] = &hs[i]
		}
		if judged, _, err := edgeEvaluateAlerts(hs, now); err == nil {
			if rules, err := edgeAlertLog().Rules(); err == nil {
				firing = edgeFiringWords(judged, rules)
			}
		}
	}
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
	// A candidate that is a member re-advertising is the SAME unit asking to re-join - told on
	// its member row, never listed twice. A standing claim grant is told too.
	asking := map[string]bool{}
	for _, c := range st.candidates {
		asking[c.ID] = true
	}
	standing := map[string]bool{}
	if grants, err := edgeauth.ReadClaims(edgeAuthDir()); err == nil {
		for _, g := range grants {
			standing[g.NodeID] = true
		}
	}
	authPort := "" // "" = the authority is not listening right now (no port was ever saved)
	if port := edgeSavedAuthorityPort(); port > 0 {
		authPort = strconv.Itoa(port)
	}
	samples, _ := edgeAlertLog().States(now.Add(-time.Hour)) // the log is a bonus: unreadable = empty
	snap.Items[0].Log, snap.Items[0].Since, snap.Items[0].SinceCapped = edge.LogOf(samples, self.NodeID, now)
	members := map[string]bool{}
	// The members, arranged; this machine's own row is the hub, not a member.
	for _, r := range edge.Arrange(nodes) {
		n := r.Node
		if n.ID == self.NodeID {
			continue
		}
		dark := edge.Presence(n.Presence) == edge.PresenceDark
		members[n.ID] = true
		it := fleetview.FleetItem{Kind: fleetview.ItemMember, ID: n.ID, Name: n.Name, NodeKind: n.Kind, Dark: dark,
			Caps: edgeCapsOf(n.Caps), Via: r.Via, Health: health[n.ID], Alerts: firing[n.ID],
			Asking: asking[n.ID] && dark, ClaimStanding: standing[n.ID], AuthPort: authPort}
		it.Log, it.Since, it.SinceCapped = edge.LogOf(samples, n.ID, now)
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
		if members[c.ID] {
			continue // told on its member row
		}
		snap.Items = append(snap.Items, fleetview.FleetItem{Kind: fleetview.ItemCandidate, ID: c.ID, Name: c.Name, NodeKind: c.Kind,
			Presence: "discovered", Caps: edgeCapsOf(c.Caps), ClaimStanding: standing[c.ID], AuthPort: authPort})
	}
	names := map[string]string{}
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	if snap.Grants, err = edgeGrantLines(names, now); err != nil {
		return fleetview.FleetSnapshot{}, err
	}
	snap.Sessions = edgeSessionLines(st, now)
	calls, rate, r, err := edgeAlertLog().Report(now)
	if err != nil {
		return fleetview.FleetSnapshot{}, err
	}
	snap.Alarms = edgeAlarmLines(calls, names)
	snap.Rate = rate
	{
		snap.SignOff = fleetview.SignOffLine{Raised: r.Raised, Cleared: r.Cleared, Loudest: r.Loudest, LoudestCount: r.LoudestCount}
		for id, d := range r.DeadAir {
			name := names[id]
			if name == "" {
				name = edgeShortID(id)
			}
			snap.SignOff.DeadAir = append(snap.SignOff.DeadAir, fleetview.DeadAir{Node: name, For: d})
		}
		sort.Slice(snap.SignOff.DeadAir, func(i, j int) bool { return snap.SignOff.DeadAir[i].For > snap.SignOff.DeadAir[j].For })
	}
	snap.Self.AgentActive = edgeIsAgentRole(loadConfig())
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
		RevokeGrant: func(id string) error { return cmdEdgeGrantRevoke(id) },
		SetAgent:    func(on bool) error { return edgeAgentSetRole(loadConfig(), on) },
		ResumeAgent: func(name string) error { return edgeAgentResume(loadConfig(), name) },
		RemoveAgent: func(name string) error { return edgeAgentRemove(loadConfig(), name) },
		Roger: func(ruleID, nodeID string) error {
			return edgeAlertLog().Roger(ruleID, nodeID, edgeOperatorName(), time.Now())
		},
		StandBy: func(ruleID, nodeID string, d time.Duration) error {
			return edgeAlertLog().StandBy(ruleID, nodeID, time.Now().Add(d))
		},
		BindJob: func(name, model, job string) error {
			if err := edge.ValidInstanceName(name); err != nil {
				return err
			}
			if job != "" && job != "serve" && job != "watch" && job != "relay" {
				return fmt.Errorf("a job is serve, watch or relay, not %q", job)
			}
			j, _ := edge.AgentJobOf(edgeJobsDir(), name)
			j.Name, j.Node = name, edgeThisNodeID()
			if model != "" {
				j.Model, j.Use = model, true
			}
			if job != "" {
				j.Job = job
				j.Share = j.Share || job == "serve"
			}
			return edge.SetAgentJob(edgeJobsDir(), j, time.Now())
		},
		ClearJob: func(name string) error {
			_, err := edge.ClearAgentJob(edgeJobsDir(), name)
			return err
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
