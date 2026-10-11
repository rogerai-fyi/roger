// Package ownerjoin is what the owner's screens (the TUI and the web console) show and do about
// devices joining a Tower they serve (features/join/approve.feature, invite.feature,
// surfaces.feature, tower_identity.feature): the Towers this user serves and their fingerprints,
// the join requests waiting for an answer, a one-paste invite and whether it was used, and the
// Towers this machine has joined and pinned. The host (cmd/rogerai) fills Hooks from the same code
// the CLI uses, so the CLI, TUI and console say the same thing; both screens take their sentences
// from here so they cannot drift apart.
package ownerjoin

import (
	"fmt"

	"rogerai.fm/roger/v6/internal/tower"
)

// Tower is a Tower this user serves right now: its data directory (where its admin socket is),
// its name, where it serves and the short form of its identity fingerprint.
type Tower struct {
	Dir   string `json:"dir"`
	Label string `json:"label"`
	Addr  string `json:"addr"`
	FP    string `json:"fp"`
}

// Request is a join request waiting on one of this user's Towers, with the two sentences the
// owner's screens show for it.
type Request struct {
	Dir   string `json:"dir"`
	Tower string `json:"tower"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Ask   string `json:"ask"`  // "laptop-x wants to join · amber-river-71"
	Hint  string `json:"hint"` // "approve only if laptop-x shows the same phrase"
}

// Invite is a one-paste invite as the owner's screens show it.
type Invite struct {
	Dir       string `json:"dir"`
	Tower     string `json:"tower"`
	ID        string `json:"id"`
	Heading   string `json:"heading"`
	Next      string `json:"next"`
	Line      string `json:"line"`
	ExpiresAt int64  `json:"expires_at"` // unix nanoseconds
}

// Pinned is a Tower this machine joined: where it is and the fingerprint it is pinned to (short).
type Pinned struct {
	Addr string `json:"addr"`
	FP   string `json:"fp"`
}

// Hooks are the owner's operations, filled by the host. Any of them may be nil on a build that
// cannot do it; the screens then leave that part out.
type Hooks struct {
	Towers   func() []Tower
	Requests func(t Tower) ([]tower.PairRequest, error)
	Decide   func(dir, id string, approve bool) error
	Invite   func(t Tower) (Invite, error)
	Outcome  func(dir, id string) (line string, over bool) // who joined, or that it ran out unused
	Pinned   func() []Pinned
}

// RequestFor is the request as both screens show it.
func RequestFor(t Tower, r tower.PairRequest) Request {
	return Request{Dir: t.Dir, Tower: t.Label, ID: r.ID, Name: r.Name, Ask: tower.AskLine(r), Hint: tower.ApproveHint(r.Name)}
}

// Card is the TUI's request line: who, the phrase and the two keys.
func (r Request) Card() string { return r.Ask + " · y approves, n refuses" }

// Notice is the one line shown on screens where y and n may be typing: who is waiting and where
// to answer.
func Notice(reqs []Request) string {
	switch len(reqs) {
	case 0:
		return ""
	case 1:
		return reqs[0].Name + " wants to join " + reqs[0].Tower + ": open [3] EDGE to answer"
	}
	return fmt.Sprintf("%d devices want to join: open [3] EDGE to answer", len(reqs))
}

// TowerLine is how a served Tower is listed: name, address and fingerprint.
func TowerLine(t Tower) string {
	return fmt.Sprintf("Tower %s · %s · %s", t.Label, t.Addr, t.FP)
}

// PinnedLine is how a joined Tower is listed: where it is and the fingerprint it is pinned to.
func PinnedLine(p Pinned) string { return fmt.Sprintf("%s · pinned %s", p.Addr, p.FP) }
