package detect

// preflight.go turns the local hardware picture into an answer to the question an
// operator currently has no way to ask: "is this box worth putting on the network?"
//
// Today there is no minimum requirement anywhere, and the only way to find out is to
// share, wait, and earn nothing. That is a bad way to learn it and it is also unfair -
// the placement work this session landed means an underpowered node is not refused, it is
// simply out-scored forever, silently.
//
// THE SHAPE OF THIS CHECK IS FORCED BY §4.1 OF docs/relay-selection-design.md.
// A minimum requirement enforced by reading what a node CLAIMS about itself is worse than
// no requirement at all: it adds a gate whose only effect is to reward lying. So the bar
// is checked in exactly one place where lying is pointless - on the operator's own
// machine, for the operator's own benefit, with the result printed to their terminal and
// sent nowhere. The network-side counterpart is measured-only and is proposed separately
// in docs/minimum-hardware-requirement.md.
//
// Three consequences follow, and all three are deliberate:
//
//  1. It is ADVISORY. Nothing here may refuse a share. Somebody serving a small model on
//     a laptop to their own grant keys is a legitimate user of this software and a
//     market-oriented gate must not lock them out.
//  2. It reports the CONSEQUENCE, not a scolding. The honest failure mode of an
//     underpowered node is "you will be scored below your peers, win little traffic and
//     earn approximately nothing", which is what the router actually does. Saying
//     anything stronger would be a threat the code does not carry out.
//  3. Where it cannot measure, it says so. An unreadable value is reported as unknown and
//     the verdict degrades to INCOMPLETE rather than passing or failing on invention.

import (
	"fmt"
	"sort"
	"strings"
)

// The bar. These numbers are ADVISORY and LOCAL: they decide what one operator is told
// about one machine, and they gate nothing on the network. They are the founder's to
// change - see docs/minimum-hardware-requirement.md, which also explains why the router's
// tpsTarget=120 and ttftCapMs=2000 are NOT reused here (those are the shape of a scoring
// curve, not a floor, and promoting a scoring constant into a policy threshold by quietly
// reading it twice is how a soft signal becomes a hard gate nobody decided on).
//
// The reasoning behind each, so a later reader can argue with the premise rather than the
// number:
//
//   - VRAM 8 GiB. The smallest pool that holds a 7-8B model at 4-bit (roughly 4.5-5 GiB of
//     weights) with a KV cache and a few thousand tokens of context left over. Below it a
//     node is either offloading layers to system RAM, which costs roughly an order of
//     magnitude of decode speed, or serving something small enough that consumers rarely
//     ask for it.
//   - Unified memory 16 GiB. On Apple Silicon there is no separate VRAM pool; macOS lets
//     Metal address a large fraction of system RAM but reserves the rest, and 16 GiB is the
//     first size where an 8B at 4-bit fits with context and the OS still has room.
//   - System RAM 16 GiB. Weights are read through the page cache and the loader needs
//     headroom; below this a desktop starts swapping while the model is resident, which
//     shows up as TTFT spikes rather than as an error.
//   - Free disk 20 GiB. One 7-8B model at 4-bit, room for a second, and their caches.
//   - CPU 4 cores. Tokenization, sampling and the HTTP relay all run on the CPU next to the
//     generation; at two cores the server stalls between tokens under any concurrency.
const (
	BarVRAMMiB     = 8 * 1024
	BarUnifiedMiB  = 16 * 1024
	BarRAMMiB      = 16 * 1024
	BarDiskFreeMiB = 20 * 1024
	BarCPUCores    = 4
)

// CheckStatus is the per-requirement outcome. Unknown is a first-class value, not an
// error: on some platforms a figure is genuinely unreadable and the report has to be able
// to say that without either passing or failing the machine on it.
type CheckStatus int

const (
	CheckMet CheckStatus = iota
	CheckBelow
	CheckUnknown
)

func (s CheckStatus) String() string {
	switch s {
	case CheckMet:
		return "OK"
	case CheckBelow:
		return "BELOW"
	default:
		return "UNKNOWN"
	}
}

// Check is one requirement, what was measured against it, and why it exists. Why is
// carried per-check rather than collected in a footnote because an operator reading
// "BELOW" wants the reason on the same line, not in a legend.
type Check struct {
	Name   string
	Detail string // the measured value in plain words, or why it could not be read
	Bar    string // the requirement, in the same units as Detail
	Status CheckStatus
	Why    string
}

// Verdict is the whole-machine answer.
type Verdict string

const (
	// VerdictClears - every requirement that could be measured was met, and everything
	// was measurable.
	VerdictClears Verdict = "CLEARS THE BAR"
	// VerdictBelow - at least one requirement was measured and missed. A measured miss
	// outranks an unknown: the machine's problem is established even if its full picture
	// is not.
	VerdictBelow Verdict = "BELOW THE BAR"
	// VerdictIncomplete - nothing measured came in under the bar, but something could not
	// be measured, so this check cannot honestly say the machine clears it.
	VerdictIncomplete Verdict = "INCOMPLETE"
)

// Preflight is the finished report.
type Preflight struct {
	HW      LocalHW
	Checks  []Check
	Verdict Verdict
}

// Clears reports whether the machine met every requirement. An INCOMPLETE report is not
// a pass: a caller that needs a boolean gets the conservative one.
func (p Preflight) Clears() bool { return p.Verdict == VerdictClears }

// Assess applies the bar to a local hardware picture. It never consults the network, never
// reads configuration, and has no side effects, so it is fully testable from a struct
// literal - which is the point, because the platform gatherers that fill LocalHW in are
// the part that cannot be tested on a GPU-less CI box.
func Assess(hw LocalHW) Preflight {
	p := Preflight{HW: hw}
	p.Checks = append(p.Checks, acceleratorCheck(hw), ramCheck(hw), diskCheck(hw), cpuCheck(hw))

	p.Verdict = VerdictClears
	for _, c := range p.Checks {
		switch c.Status {
		case CheckBelow:
			p.Verdict = VerdictBelow
			return p // a measured miss is final; nothing an unknown says can soften it
		case CheckUnknown:
			p.Verdict = VerdictIncomplete
		}
	}
	return p
}

// acceleratorCheck is the requirement that decides most machines, and it has three
// distinct shapes rather than one, because "GPU memory" is not the same quantity on the
// three kinds of host this software runs on.
func acceleratorCheck(hw LocalHW) Check {
	c := Check{
		Name: "GPU memory",
		Why:  "the model's weights and its KV cache have to fit, or every token is paid for in host-memory round trips",
	}
	switch {
	case hw.UnifiedMemory:
		// Apple Silicon: there is no VRAM figure to read because there is no VRAM. The
		// requirement is real but it is a requirement on system RAM, and the report says
		// which quantity it just checked rather than printing "VRAM" over a RAM number.
		c.Name = "GPU memory (unified)"
		c.Bar = fmt.Sprintf("%s of unified memory", mib(BarUnifiedMiB))
		if !hw.RAMKnown {
			c.Status, c.Detail = CheckUnknown, "unified memory: the total could not be read on this host"
			return c
		}
		c.Detail = fmt.Sprintf("%s unified, shared with the system", mib(hw.RAMTotalMiB))
		c.Status = metIf(hw.RAMTotalMiB >= BarUnifiedMiB)
		return c
	case hw.Class == HWCPU:
		// Not "unknown". No accelerator was found, and that IS the measurement.
		c.Bar = fmt.Sprintf("%s of VRAM, or Apple unified memory", mib(BarVRAMMiB))
		c.Status, c.Detail = CheckBelow, "no GPU detected - this node would generate on the CPU"
		return c
	case len(hw.GPUs) == 0:
		c.Bar = fmt.Sprintf("%s of VRAM", mib(BarVRAMMiB))
		c.Status, c.Detail = CheckUnknown, "no GPU tooling answered on this host, so nothing could be enumerated"
		return c
	}

	c.Bar = fmt.Sprintf("%s of VRAM", mib(BarVRAMMiB))
	if !hw.VRAMKnown {
		c.Status = CheckUnknown
		c.Detail = fmt.Sprintf("%d GPU(s) present, but their memory size could not be read", len(hw.GPUs))
		return c
	}
	c.Detail = fmt.Sprintf("%s across %d GPU(s)", mib(hw.VRAMTotalMiB), len(hw.GPUs))
	c.Status = metIf(hw.VRAMTotalMiB >= BarVRAMMiB)
	return c
}

func ramCheck(hw LocalHW) Check {
	c := Check{
		Name: "system RAM",
		Bar:  mib(BarRAMMiB),
		Why:  "weights are loaded through the page cache; a host that swaps while a model is resident shows it as first-token spikes, not as an error",
	}
	if !hw.RAMKnown {
		c.Status, c.Detail = CheckUnknown, "could not be read on this host"
		return c
	}
	c.Detail = mib(hw.RAMTotalMiB)
	c.Status = metIf(hw.RAMTotalMiB >= BarRAMMiB)
	return c
}

func diskCheck(hw LocalHW) Check {
	c := Check{
		Name: "free disk",
		Bar:  mib(BarDiskFreeMiB),
		Why:  "one 7-8B model at 4-bit plus room for a second and their caches",
	}
	if !hw.DiskKnown {
		c.Status, c.Detail = CheckUnknown, "could not be read on this host"
		return c
	}
	// The path is part of the measurement, not decoration. We cannot know where the
	// upstream server keeps its weights, so the honest claim is about THIS filesystem.
	c.Detail = fmt.Sprintf("%s free on %s", mib(hw.DiskFreeMiB), hw.DiskPath)
	c.Status = metIf(hw.DiskFreeMiB >= BarDiskFreeMiB)
	return c
}

func cpuCheck(hw LocalHW) Check {
	c := Check{
		Name: "CPU cores",
		Bar:  fmt.Sprintf("%d", BarCPUCores),
		Why:  "tokenization, sampling and the relay run beside the generation; at two cores the server stalls between tokens",
	}
	if hw.CPUCores <= 0 {
		c.Status, c.Detail = CheckUnknown, "could not be read on this host"
		return c
	}
	c.Detail = fmt.Sprintf("%d", hw.CPUCores)
	c.Status = metIf(hw.CPUCores >= BarCPUCores)
	return c
}

func metIf(ok bool) CheckStatus {
	if ok {
		return CheckMet
	}
	return CheckBelow
}

// mib renders a MiB count the way an operator thinks about their machine: GiB with one
// decimal once it is worth it, MiB below that. Anything that would round to "0.0 GiB" is
// printed in MiB so a small figure never displays as nothing.
func mib(n int) string {
	if n >= 1024 {
		return fmt.Sprintf("%.1f GiB", float64(n)/1024)
	}
	return fmt.Sprintf("%d MiB", n)
}

// Consequences is the part that matters most and the part that is easiest to get wrong.
//
// It says what the software ACTUALLY DOES to an underpowered node, which is not a refusal
// and not a punishment: the broker probes every node with canaries, scores placement on
// the measurements, and a slow node is simply picked less often. The temptation is to
// write something firmer to make the operator take it seriously. That would be a threat
// the code does not carry out, and this repo's rule is that user-facing copy must not
// overclaim - which cuts in both directions.
//
// It also names the case where none of this matters, because that case is a real and
// supported use of this software rather than a consolation: a node serving its own grant
// keys or its own private band has no peers to be out-ranked by.
func Consequences(v Verdict) []string {
	switch v {
	case VerdictClears:
		return nil
	case VerdictIncomplete:
		return []string{
			"this check could not read everything it wanted to, so it is not saying your machine is fine - it is saying it does not know.",
			"the values it could not read are marked UNKNOWN above. Sharing is unaffected either way: nothing here gates `roger share`.",
		}
	default:
		return []string{
			"nothing refuses you. `roger share` runs, your node registers, and it is routable exactly like any other.",
			"the broker measures every node with its own canary probes - first-token latency and tokens per second - and places consumer work by what it measured, not by anything your node says about itself.",
			"a node that generates slowly therefore scores below its peers on the same model, is picked less often, and on public traffic earns approximately nothing.",
			"you are not banned, throttled, hidden, or told to go away, and you will not be penalised before you are measured: an unmeasured node scores neutral. The penalty, such as it is, arrives with the measurements.",
			"none of this applies to traffic that was already yours. A private band, or people you handed a grant key to, reach your node because they asked for it - there are no peers to be out-ranked by.",
		}
	}
}

// String renders the report for a terminal. It follows `roger-tower doctor` deliberately -
// keyed lines, then the loud things, then a one-word verdict - because an operator who has
// run one of these should recognise the other. Consistency across the two binaries is
// worth more here than any improvement in layout.
func (p Preflight) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "hardware preflight (local only - none of this is sent to RogerAI)\n\n")

	if len(p.HW.GPUs) > 0 {
		for _, g := range p.HW.GPUs {
			if g.VRAMMiB > 0 {
				fmt.Fprintf(&b, "  GPU: %s (%s)\n", g.Model, mib(g.VRAMMiB))
			} else {
				fmt.Fprintf(&b, "  GPU: %s (memory not reported)\n", g.Model)
			}
		}
	}
	for _, c := range p.Checks {
		fmt.Fprintf(&b, "  %-20s %-6s %s (bar: %s)\n", c.Name+":", c.Status, c.Detail, c.Bar)
	}

	// The one value that DOES leave the host, named next to everything that does not, so
	// the privacy claim above the report is checkable rather than asserted.
	fmt.Fprintf(&b, "\n  advertised to the network: hw=%q - the bucket, and nothing else on this page\n", p.HW.Class)

	if len(p.HW.Undetermined) > 0 {
		// Sorted so two runs on the same machine produce the same report; the gatherers
		// append in probe order, which is not stable across platforms.
		u := append([]string(nil), p.HW.Undetermined...)
		sort.Strings(u)
		fmt.Fprintf(&b, "\n")
		for _, n := range u {
			fmt.Fprintf(&b, "  could not determine: %s\n", n)
		}
	}

	fmt.Fprintf(&b, "\npreflight: %s\n", p.Verdict)
	if cs := Consequences(p.Verdict); len(cs) > 0 {
		fmt.Fprintf(&b, "\nwhat happens if you share anyway:\n")
		for _, c := range cs {
			fmt.Fprintf(&b, "  - %s\n", c)
		}
	}
	return b.String()
}

// AdvisoryLine is the one-line form, for a normal `roger share` that is about to go on air
// on a machine below the bar. A full report at that moment would bury the on-air line the
// operator is actually waiting for, and an operator who wants the full report has
// `roger share --check`. Empty string when there is nothing worth saying.
func (p Preflight) AdvisoryLine() string {
	switch p.Verdict {
	case VerdictBelow:
		return "  ! this machine is below the suggested minimum (" + p.shortfall() + "). Sharing works and nothing is blocked, " +
			"but the broker places work by measured speed, so on public traffic expect to be picked rarely and earn " +
			"approximately nothing. `roger share --check` explains it. Serving your own grant keys or a private band is unaffected."
	default:
		// INCOMPLETE is deliberately silent here. A machine we could not fully measure has
		// done nothing wrong, and a warning that amounts to "we could not tell" on every
		// start would train operators to ignore this line.
		return ""
	}
}

// shortfall names the requirements that were actually missed, so the one-line form is
// specific enough to act on without printing the whole table.
func (p Preflight) shortfall() string {
	var missed []string
	for _, c := range p.Checks {
		if c.Status == CheckBelow {
			missed = append(missed, c.Name)
		}
	}
	return strings.Join(missed, ", ")
}
