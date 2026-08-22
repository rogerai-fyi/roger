package main

// preflight.go is `roger share`'s minimum-hardware check: the operator-facing half of the
// requirement, and the only half that exists today.
//
// WHY IT IS ONLY LOCAL. docs/relay-selection-design.md §4.1 rules that supply-side
// capability may never be self-declared, because a declared capability is a lever - claim
// the best hardware, receive the most work. This session found that exact defect twice
// (a decorative `--region`, and a self-declared `hw` that was moving edge placement by
// 2x). A minimum requirement enforced by reading what a node CLAIMS would therefore be
// worse than no requirement at all: it would add a gate whose only effect is to reward
// lying. So the bar is checked where lying is pointless - on the operator's own machine,
// for the operator's own benefit - and it is checked richly, because a local check may
// look at everything the privacy bucketing deliberately withholds from the network. The
// network-side counterpart is measured-only and is proposed in
// docs/minimum-hardware-requirement.md.
//
// WHY IT DOES NOT BLOCK. Somebody serving a small model on a laptop to their own grant
// keys is a legitimate user of this software, and a market-oriented gate must not lock
// them out. Nothing in this file can stop a share; the strongest thing it does is print.
//
// TWO SURFACES, deliberately different in size:
//
//   - `roger share --check` prints the full report and exits. It is modelled on
//     `roger-tower doctor` - keyed lines, the loud things, then a one-word verdict -
//     because an operator who has run one of these should recognise the other, and
//     consistency between the two binaries is worth more here than a better layout.
//   - a normal `roger share` on a below-bar machine prints ONE advisory line before going
//     on air. A full report at that moment would bury the on-air line the operator is
//     actually waiting for.
//
// An INCOMPLETE verdict is silent on the normal path on purpose. A machine we could not
// fully measure has done nothing wrong, and a warning that amounts to "we could not tell"
// on every start is a warning operators learn to skip past - including on the runs where
// it says something real.

import (
	"errors"
	"fmt"
	"io"

	"rogerai.fm/roger/v6/internal/detect"
)

// sharePreflight is the seam the whole surface hangs off: gather the local picture, apply
// the bar. Tests replace it to drive a chosen machine through the real reporting and
// advisory code, which is the part that has to be right - the platform gatherers behind
// detectLocalHW cannot be exercised on a GPU-less CI box and are seam'd separately.
var sharePreflight = func() detect.Preflight { return detect.Assess(detectLocalHW()) }

// errPreflightBelow is returned by the --check surface when the machine is under the bar,
// so the command exits non-zero for whoever is scripting the decision. Its wording has to
// survive main()'s "error: " prefix while still saying plainly that nothing is blocked,
// because the exit code is the only thing here that looks like a refusal and it is not one.
var errPreflightBelow = errors.New("hardware preflight: this machine is below the suggested minimum - " +
	"advisory only, and `roger share` is not blocked by it")

// runSharePreflight prints the full report and reports whether the caller should exit
// non-zero. `roger share --check` does the whole job here and returns before any upstream
// detection, broker call or registration: an operator asking "is this box worth it?"
// should not have their model server probed to find out.
func runSharePreflight(out io.Writer, p detect.Preflight) error {
	fmt.Fprint(out, p.String())
	if p.Verdict == detect.VerdictBelow {
		return errPreflightBelow
	}
	return nil
}

// shareAdvisory returns the single line a normal share prints when the machine is under
// the bar, or "" when there is nothing worth saying. It exists as its own function so the
// wording is pinned by a test rather than buried in cmdShare's body.
func shareAdvisory(p detect.Preflight) string { return p.AdvisoryLine() }
