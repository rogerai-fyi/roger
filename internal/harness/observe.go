package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// observe.go - READ BEFORE WRITE, and don't clobber what changed underneath you.
//
// Borrowed from the DeepSeek Harness's fs-observation-policy, which is the strongest
// idea left in that codebase after the four from the review shipped. Two real ways to
// lose someone's work, both of which our write_file allowed:
//
//   1. THE BLIND OVERWRITE. The model writes a file it never read. It intends to create
//      something; the file already exists with 400 lines in it; those lines are gone.
//      The confirm gate does not save you - it shows a PATH, not "this replaces work
//      you have not seen".
//   2. THE STALE OVERWRITE. The model reads a file, you edit it in your editor while
//      the turn is thinking, the model writes back what it planned from the old text.
//      Your edit is gone, and nothing anywhere reported a conflict.
//
// The fix is the pair: a write to a file the model has not observed may only CREATE,
// and a write to one it has observed must still match the version it saw. Both refusals
// are fed back as tool results, so the model reads the reason and does the right thing
// next - read the file, or re-read and re-plan.
//
// SCOPED PER LOOP, deliberately. A subagent has its own observations, so a child
// reading a file does not license the parent to overwrite it - the parent has not seen
// it, which is exactly the situation rule 1 exists for.

// fileVersion identifies a file's content well enough to notice a change. Size plus
// modification time, not a content hash: a hash means reading every byte of every file
// on every check, and an editor save moves mtime. The trade is a same-size same-mtime
// rewrite going unnoticed, which needs a deliberate effort to produce.
type fileVersion struct {
	size    int64
	modNano int64
	absent  bool // observed NOT to exist, which is what licenses a create
}

func versionOf(path string) fileVersion {
	fi, err := os.Stat(path)
	if err != nil {
		return fileVersion{absent: true}
	}
	return fileVersion{size: fi.Size(), modNano: fi.ModTime().UnixNano()}
}

func (v fileVersion) same(o fileVersion) bool {
	return v.absent == o.absent && v.size == o.size && v.modNano == o.modNano
}

// observations records what this agent has actually looked at.
type observations struct {
	mu   sync.Mutex
	seen map[string]fileVersion
}

func (o *observations) record(path string, v fileVersion) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen == nil {
		o.seen = map[string]fileVersion{}
	}
	o.seen[path] = v
}

func (o *observations) lookup(path string) (fileVersion, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.seen[path]
	return v, ok
}

// noteObserved records a successful read. Called from the ordered settle phase, so an
// observation is never recorded for a read that failed or was refused.
func (l *Loop) noteObserved(tool string, args map[string]any) {
	if tool != "read_file" {
		return
	}
	p := argStr(args["path"])
	if p == "" {
		return
	}
	l.observed.record(p, versionOf(filepath.Join(l.Root, p)))
}

// GuardWriteNeedsRead refuses a write that would destroy content the agent has not
// seen, or that has changed since it looked.
//
// Deny-only like every guard (guards.go), so it can never widen what a write may do -
// and it is the loop's own state it consults, never the model's claims.
func (l *Loop) GuardWriteNeedsRead(name string, args map[string]any, _ ConversationView) string {
	if name != "write_file" {
		return ""
	}
	rel := argStr(args["path"])
	if rel == "" {
		return ""
	}
	full := filepath.Join(l.Root, rel)
	now := versionOf(full)

	seen, everLooked := l.observed.lookup(rel)
	if !everLooked {
		if now.absent {
			return "" // creating a new file: nothing to lose
		}
		return fmt.Sprintf("refused: %s already exists and you have not read it. "+
			"Writing now would replace content you have never seen. read_file it first, "+
			"then write the full new contents.", rel)
	}
	if now.absent {
		return "" // it was there, it is gone: writing re-creates it, destroying nothing
	}
	if !seen.same(now) {
		return fmt.Sprintf("refused: %s changed on disk since you read it. "+
			"Writing now would discard that change. read_file it again and redo the edit "+
			"against the current contents.", rel)
	}
	return ""
}

// noteWritten records the post-write version, so a second write in the same turn is not
// refused for a change the agent itself made.
func (l *Loop) noteWritten(tool string, args map[string]any) {
	if tool != "write_file" {
		return
	}
	if p := argStr(args["path"]); p != "" {
		l.observed.record(p, versionOf(filepath.Join(l.Root, p)))
	}
}
