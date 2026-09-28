package edge

// THE LOG TAIL (features/edge/logs.feature): a roger's own last log lines, kept in memory and served
// to a member that holds a maintain grant, with secrets removed on the way out.
//
// It is NOT a file read: the only thing here is this process's own ring, so no path ever comes from a
// caller and nothing on disk is reachable through it. The redaction is the point of the file: log
// lines on a real fleet carry upstream keys, and a line that leaves a machine has to be safe to read.

import (
	"regexp"
	"strings"
	"sync"
)

// Log ring bounds. A few hundred lines is what an operator reads; the byte cap stops one absurd line
// (a pasted payload, a stack trace) from holding the whole ring.
const (
	MaxLogLines   = 400
	MaxLogLineLen = 2000
	// DefaultLogLines is what a caller that does not say gets.
	DefaultLogLines = 100
)

// LogRing is a bounded, concurrency-safe ring of log lines that is also an io.Writer, so it can be
// TEED onto the standard logger: the lines still reach wherever they went before.
//
// internal/tui has a ring of its own for the terminal's /log, which REPLACES the logger for the life
// of a TUI. This one is separate on purpose: it tees rather than diverts, it bounds line length, and
// it is read by the face, which must not depend on a terminal being up.
type LogRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

// NewLogRing builds a ring holding at most max lines (0 means MaxLogLines).
func NewLogRing(max int) *LogRing {
	if max <= 0 {
		max = MaxLogLines
	}
	return &LogRing{max: max}
}

// Write takes log output. It never fails and never blocks on anything but its own mutex.
func (r *LogRing) Write(p []byte) (int, error) {
	n := len(p)
	for _, ln := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if len([]rune(ln)) > MaxLogLineLen {
			ln = string([]rune(ln)[:MaxLogLineLen]) + " …(line cut)"
		}
		r.mu.Lock()
		r.lines = append(r.lines, ln)
		if len(r.lines) > r.max {
			r.lines = append([]string(nil), r.lines[len(r.lines)-r.max:]...)
		}
		r.mu.Unlock()
	}
	return n, nil
}

// Tail is the newest n lines, oldest first, REDACTED. n is clamped to the ring's size; 0 or less
// means DefaultLogLines. held is how many the ring holds in total.
func (r *LogRing) Tail(n int) (lines []string, held int) {
	if n <= 0 {
		n = DefaultLogLines
	}
	r.mu.Lock()
	held = len(r.lines)
	if n > held {
		n = held
	}
	out := make([]string, 0, n)
	out = append(out, r.lines[held-n:]...)
	r.mu.Unlock()
	for i := range out {
		out[i] = RedactSecrets(out[i])
	}
	return out, held
}

// redactMarker is what replaces a secret: it says something was removed rather than leaving a line
// that reads as if nothing was there.
const redactMarker = "[redacted]"

// Ordered: the labelled forms first, so "key=sk-abc" is caught as a labelled value rather than
// leaving the label dangling, then the known key shapes, then a long high-entropy run.
var redactors = []*regexp.Regexp{
	// authorization: Bearer xyz / authorization: Basic xyz
	regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*)(bearer|basic|token)?\s*\S+`),
	regexp.MustCompile(`(?i)\b(bearer)\s+\S+`),
	// A LABELLED value, however the label is spelled: key=, api_key=, upstream_key=, "api_key": "x",
	// UPSTREAM-TOKEN: x. The label may carry a prefix joined by _ - or . (so \b would not find it),
	// may be quoted, and the separator may be : or =. The spec promises "anything after key=", and a
	// value with no recognisable shape is exactly the case that used to leak.
	regexp.MustCompile(`(?i)"?[a-z0-9_.-]*(?:key|token|secret|password|passwd|pwd)"?\s*[:=]\s*"?[^"\s,;)]+"?`),
	// The FLAG form, where the separator is a space: --api-key abc, -token abc, --upstream-key=abc.
	regexp.MustCompile(`(?i)(--?[a-z0-9_.-]*(?:key|token|secret|password|passwd|pwd))[\s=]+"?[^"\s,;)]+"?`),
	// the key shapes this fleet really uses
	regexp.MustCompile(`\bsk-or-v1-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bcsk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bxai-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bgsk_[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bghp_[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\brgr_[A-Za-z0-9_-]{16,}`),
	// a long unbroken high-entropy run: what a secret looks like when we do not know its shape.
	regexp.MustCompile(`\b[A-Za-z0-9+/_-]{48,}={0,2}\b`),
}

// RedactSecrets removes what looks like a credential from one line, leaving the rest readable. It is
// applied on the way OUT, so it covers lines logged before it existed, and a caller cannot ask for
// the raw text. It is deliberately eager: a false positive costs an operator one unreadable token, a
// false negative ships a key to another machine.
func RedactSecrets(line string) string {
	out := line
	for i, re := range redactors {
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			// The labelled forms keep their label so the line still says WHAT was removed.
			if i <= 3 {
				if j := strings.IndexAny(m, ":="); j >= 0 {
					return m[:j+1] + " " + redactMarker
				}
				if j := strings.IndexAny(m, " \t"); j >= 0 {
					return m[:j] + " " + redactMarker
				}
			}
			return redactMarker
		})
	}
	return out
}
