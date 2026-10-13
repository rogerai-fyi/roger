package edgeauth

// joiners.go is what this authority has heard each joiner say about checking codes
// (features/edge/enrollment_root_trust.feature, ruling D9 and its 2026-10-13 fix).
//
// A joiner from before root proofs keeps the first answer it gets, so the owner is warned to update
// it. A joiner that checks says so with `checks_root` on its enroll, bearer join or claim request.
// The authority can only learn that when the joiner SPEAKS, which is often after the owner allowed
// or adopted it, so the note is kept here, on disk beside the other authority state, where three
// moments can read it:
//
//   - adopt: a phone polls /edge/claim before it is adopted, so by the time the owner presses adopt
//     the authority has usually heard it, and says nothing, or says plainly that this one is old;
//   - allow: a machine that tried to enroll before it was allowed has been heard the same way;
//   - after the join: the owner's fleet window says whether the member checked the code.
//
// What is noted is only what a correctly SIGNED, fresh request said. The flag itself is not inside
// the signature (request.go says why); flipping it in transit changes a warning and nothing else.
// The file is bounded: a stranger with endless keys can churn it but never grow it.

import (
	"path/filepath"
	"sort"
	"time"
)

// JoinersFile is where the notes are kept, in the authority directory.
const JoinersFile = "joiners.json"

// maxJoinerNotes bounds each half of the book. Far above any real Edge's joiners in a day.
const maxJoinerNotes = 256

// joinerNoteRefresh is how stale an unchanged note may get before it is written again, so a phone
// polling its claim every few seconds costs one write, not one per poll.
const joinerNoteRefresh = 10 * time.Minute

// JoinerNote is what one joiner said the last time it was heard.
type JoinerNote struct {
	ChecksRoot bool  `json:"checks_root"`
	At         int64 `json:"at"`
}

type joinerBook struct {
	Nodes map[string]JoinerNote `json:"nodes,omitempty"` // by node id
	Users map[string]JoinerNote `json:"users,omitempty"` // by user key (hex)
}

func (l *Local) readJoiners() (joinerBook, error) {
	var b joinerBook
	err := l.readJSON(JoinersFile, &b)
	if b.Nodes == nil {
		b.Nodes = map[string]JoinerNote{}
	}
	if b.Users == nil {
		b.Users = map[string]JoinerNote{}
	}
	return b, err
}

// put records one note, reporting whether the book changed enough to be written.
func put(m map[string]JoinerNote, key string, checks bool, now int64) bool {
	if key == "" {
		return false
	}
	if old, ok := m[key]; ok && old.ChecksRoot == checks && now-old.At < int64(joinerNoteRefresh.Seconds()) {
		return false
	}
	m[key] = JoinerNote{ChecksRoot: checks, At: now}
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

// unheardPerMinute bounds how many NEW notes a refused claim may write in a minute. A claim refused
// because nobody adopted the node is signed by a key anyone can make, so this is the one place a
// stranger could make the authority write; the budget keeps that to a trickle.
const unheardPerMinute = 30

// joinerBudget is the in-process allowance for notes from refused claims.
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

// noteRefusedLocked is noteJoinerLocked for a signed request that was refused because the owner has
// not adopted or allowed it YET: a phone polling its claim, a machine that tried before it was
// allowed. Only a joiner not heard before spends the budget. The caller holds the authority lock.
func (l *Local) noteRefusedLocked(nodeID, userKey string, checks bool, now time.Time) {
	b, err := l.readJoiners()
	if err != nil {
		return
	}
	_, nodeHeard := b.Nodes[nodeID]
	_, userHeard := b.Users[userKey]
	if (nodeID != "" && !nodeHeard) || (userKey != "" && !userHeard) {
		if !l.heard.allow(now) {
			return
		}
	}
	changed := put(b.Nodes, nodeID, checks, now.Unix())
	if put(b.Users, userKey, checks, now.Unix()) {
		changed = true
	}
	if changed {
		_ = l.writeJSON(JoinersFile, b)
	}
}

// noteJoinerLocked remembers what a signed request said. The caller holds the authority lock. A
// note that cannot be written is dropped: it only ever decides a warning.
func (l *Local) noteJoinerLocked(nodeID, userKey string, checks bool, now time.Time) {
	b, err := l.readJoiners()
	if err != nil {
		return
	}
	changed := put(b.Nodes, nodeID, checks, now.Unix())
	if put(b.Users, userKey, checks, now.Unix()) {
		changed = true
	}
	if changed {
		_ = l.writeJSON(JoinersFile, b)
	}
}

// NodeChecksRoot reports what the node last said about checking codes, and whether it was heard.
func (l *Local) NodeChecksRoot(nodeID string) (checks, heard bool) {
	b, err := l.readJoiners()
	if err != nil {
		return false, false
	}
	n, ok := b.Nodes[nodeID]
	return n.ChecksRoot, ok
}

// UserChecksRoot is NodeChecksRoot for a joiner known by its user key (an allowed machine).
func (l *Local) UserChecksRoot(userKey string) (checks, heard bool) {
	b, err := l.readJoiners()
	if err != nil {
		return false, false
	}
	n, ok := b.Users[userKey]
	return n.ChecksRoot, ok
}

// ReadJoiners is every node's note under edgeDir's authority, WITHOUT opening its key custody - for
// the fleet window, which only says whether a member checked the code. Empty when this machine is
// no authority or has heard nobody.
func ReadJoiners(edgeDir string) map[string]JoinerNote {
	l := &Local{dir: filepath.Join(edgeDir, AuthorityDir)}
	b, err := l.readJoiners()
	if err != nil {
		return map[string]JoinerNote{}
	}
	return b.Nodes
}
