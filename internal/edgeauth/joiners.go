package edgeauth

// joiners.go is what this authority has heard each joiner say about checking codes
// (features/edge/enrollment_root_trust.feature, ruling D9 and its 2026-10-13 fix).
//
// A joiner from before root proofs keeps the first answer it gets, so the owner is warned to update
// it. A joiner that checks says so with `checks_root` and PROVES it said so with `checks_root_sig`
// (checksroot.go) on its enroll, bearer join or claim request. The authority can only learn that when
// the joiner SPEAKS, which is often after the owner allowed or adopted it, so the note is kept here,
// on disk beside the other authority state, where three moments read it:
//
//   - adopt: a phone polls /edge/claim before it is adopted, so by the time the owner presses adopt
//     the authority has usually heard it, and says nothing, or says plainly that this one is old;
//   - allow: a machine that tried to enroll before it was allowed has been heard the same way;
//   - after the join: the owner's fleet window says whether the member said it checks codes.
//
// Audit 2026-10-13: a note is credited "checks" only when checks_root_sig verifies, so a device in the
// middle cannot set it and a replayed old request cannot claim it. Notes from requests that were
// REFUSED (not adopted or allowed yet) are kept apart from notes of joins that were issued, so a
// stranger churning the first can never evict the second; a refused request never overwrites a note
// already there, and no note is replaced by one from an older request.

import (
	"path/filepath"
	"sort"
	"time"
)

// JoinersFile is where the notes are kept, in the authority directory.
const JoinersFile = "joiners.json"

// maxJoinerNotes bounds each map of the book. Far above any real Edge's joiners in a day.
const maxJoinerNotes = 256

// JoinerNote is what one joiner said the last time it was heard.
type JoinerNote struct {
	ChecksRoot bool  `json:"checks_root"`
	At         int64 `json:"at"` // the signed time of the request it said it in
}

type joinerBook struct {
	Nodes      map[string]JoinerNote `json:"nodes,omitempty"`       // issued joins, by node id
	Users      map[string]JoinerNote `json:"users,omitempty"`       // issued joins, by user key (hex)
	HeardNodes map[string]JoinerNote `json:"heard_nodes,omitempty"` // refused claims, by node id
	HeardUsers map[string]JoinerNote `json:"heard_users,omitempty"` // refused enrolments, by user key
}

func (l *Local) readJoiners() (joinerBook, error) {
	var b joinerBook
	err := l.readJSON(JoinersFile, &b)
	for _, m := range []*map[string]JoinerNote{&b.Nodes, &b.Users, &b.HeardNodes, &b.HeardUsers} {
		if *m == nil {
			*m = map[string]JoinerNote{}
		}
	}
	return b, err
}

// put records one note when it is newer than the one there (or, with keep, only when there is none),
// reporting whether the map changed. The map is bounded by dropping its oldest notes.
func put(m map[string]JoinerNote, key string, n JoinerNote, keep bool) bool {
	if key == "" {
		return false
	}
	if old, ok := m[key]; ok && (keep || n.At <= old.At || old == n) {
		return false
	}
	m[key] = n
	if len(m) > maxJoinerNotes {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]].At < m[keys[j]].At })
		for _, k := range keys[:len(m)-maxJoinerNotes] {
			delete(m, k)
		}
	}
	return true
}

// unheardPerMinute bounds how many NEW notes refused requests may write in a minute. A claim refused
// because nobody adopted the node is signed by a key anyone can make, so this is the one place a
// stranger could make the authority write; the budget keeps that to a trickle.
const unheardPerMinute = 30

// joinerBudget is the in-process allowance for notes from refused requests.
type joinerBudget struct {
	minute int64
	spent  int
}

// allow spends one note from this minute's budget. The caller holds the authority lock.
func (b *joinerBudget) allow(now time.Time) bool {
	m := now.Unix() / 60
	if m != b.minute {
		b.minute, b.spent = m, 0
	}
	if b.spent >= unheardPerMinute {
		return false
	}
	b.spent++
	return true
}

// noteRefusedLocked notes a signed request refused because the owner has not adopted or allowed it
// YET: a phone polling its claim, a machine that tried before it was allowed. It writes only a note
// that is not there yet, within the budget. The caller holds the authority lock.
func (l *Local) noteRefusedLocked(nodeID, userKey string, checks bool, ts int64, now time.Time) {
	b, err := l.readJoiners()
	if err != nil {
		return
	}
	_, nodeHeard := b.HeardNodes[nodeID]
	_, userHeard := b.HeardUsers[userKey]
	if (nodeID == "" || nodeHeard) && (userKey == "" || userHeard) {
		return
	}
	if !l.heard.allow(now) {
		return
	}
	n := JoinerNote{ChecksRoot: checks, At: ts}
	changed := put(b.HeardNodes, nodeID, n, true)
	if put(b.HeardUsers, userKey, n, true) {
		changed = true
	}
	if changed {
		_ = l.writeJSON(JoinersFile, b)
	}
}

// noteJoinerLocked notes what an ISSUED join said. The caller holds the authority lock. A note that
// cannot be written is dropped: it only ever decides a warning.
func (l *Local) noteJoinerLocked(nodeID, userKey string, checks bool, ts int64) {
	b, err := l.readJoiners()
	if err != nil {
		return
	}
	n := JoinerNote{ChecksRoot: checks, At: ts}
	changed := put(b.Nodes, nodeID, n, false)
	if put(b.Users, userKey, n, false) {
		changed = true
	}
	if changed {
		_ = l.writeJSON(JoinersFile, b)
	}
}

func lookup(joined, heard map[string]JoinerNote, key string) (checks, ok bool) {
	if n, found := joined[key]; found {
		return n.ChecksRoot, true
	}
	n, found := heard[key]
	return n.ChecksRoot, found
}

// NodeChecksRoot reports whether the node proved it checks codes, and whether it was heard at all.
func (l *Local) NodeChecksRoot(nodeID string) (checks, heard bool) {
	b, err := l.readJoiners()
	if err != nil {
		return false, false
	}
	return lookup(b.Nodes, b.HeardNodes, nodeID)
}

// UserChecksRoot is NodeChecksRoot for a joiner known by its user key (an allowed machine).
func (l *Local) UserChecksRoot(userKey string) (checks, heard bool) {
	b, err := l.readJoiners()
	if err != nil {
		return false, false
	}
	return lookup(b.Users, b.HeardUsers, userKey)
}

// ReadJoiners is what every JOINED node said, under edgeDir's authority, WITHOUT opening its key
// custody - for the fleet window, which says whether a member said it checks codes when it joined.
// Empty when this machine is no authority or has issued to nobody since notes were kept.
func ReadJoiners(edgeDir string) map[string]JoinerNote {
	l := &Local{dir: filepath.Join(edgeDir, AuthorityDir)}
	b, err := l.readJoiners()
	if err != nil {
		return map[string]JoinerNote{}
	}
	return b.Nodes
}
