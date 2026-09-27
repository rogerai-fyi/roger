package main

// The fleet screen's GRANTS and SESSIONS tabs and its self-management keys, over the same code
// the CLI runs: `roger edge grant` / `grant revoke`, `roger edge sessions`, `roger edge agent`.

import (
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/fleetview"
)

// edgeGrantLines is every grant as the screen lists it: facts and standing, never a secret.
func edgeGrantLines(names map[string]string, now time.Time) ([]fleetview.GrantLine, error) {
	gs, err := edgeGrantStore().List()
	if err != nil {
		return nil, err
	}
	out := make([]fleetview.GrantLine, 0, len(gs))
	for _, g := range gs {
		callee := ""
		if len(g.Nodes) > 0 {
			callee = names[g.Nodes[0]]
			if callee == "" {
				callee = edgeShortID(g.Nodes[0])
			}
		}
		caller := "your own instances"
		if g.Caller != "" {
			caller = names[g.Caller]
			if caller == "" {
				caller = edgeShortID(g.Caller)
			}
		}
		state := "live"
		switch {
		case g.Revoked:
			state = "REVOKED"
		case g.Expired(now):
			state = "expired"
		}
		until := "never"
		if g.ExpiresAt > 0 {
			until = time.Unix(g.ExpiresAt, 0).Format("2006-01-02")
		}
		out = append(out, fleetview.GrantLine{ID: g.ID, Caller: caller, Caps: strings.Join(g.Caps, ","), Callee: callee, Until: until, State: state})
	}
	return out, nil
}

// edgeSessionLines is the live session ledger, grouped, as the screen lists it.
func edgeSessionLines(st *edgeState, now time.Time) []fleetview.SessionLine {
	out := []fleetview.SessionLine{} // empty is a fact ("nothing carried yet"), never unknown
	for _, g := range edge.GroupSessions(st.sessions.Live()) {
		x := g.Session
		out = append(out, fleetview.SessionLine{Who: x.Attribution(), Band: x.Band, Route: x.Where, Station: x.Station,
			Outcome: x.OutcomeLabel(), Count: g.Count, Age: edge.Age(now.Sub(time.Unix(x.At, 0)))})
	}
	return out
}
