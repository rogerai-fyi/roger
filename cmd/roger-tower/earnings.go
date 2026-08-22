package main

// earnings.go is `roger-tower earnings`: what this account has earned, read from Core.
//
// The Payouts page on the website has always shown this; an operator running a headless
// Tower had no way to ask. The numbers are the SAME numbers - credits, held/payable/paid,
// relaying told apart from serving - because they come from the same ledger the payout rail
// pays from, not from a parallel accrual.

import (
	"flag"
	"fmt"
	"io"
	"time"

	"rogerai.fm/roger/v6/internal/towerjoin"
)

// NO DATA DIRECTORY. Earnings are an ACCOUNT question answered by Core over a signed
// request; the Tower's own state has no part in it. Taking the data dir would also take its
// EXCLUSIVE lock - so this command would refuse to run on exactly the machine it is for, the
// one with `roger-tower serve` already holding that directory.
func cmdEarnings(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("earnings", flag.ContinueOnError)
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := towerjoin.FetchEarnings()
	if err != nil {
		return err
	}
	unit := e.Unit
	if unit == "" {
		unit = "credits"
	}
	fmt.Fprintf(out, "earnings for this account (%s)\n\n", unit)
	fmt.Fprintf(out, "  payable now   %.4f\n", e.Payable)
	fmt.Fprintf(out, "  held          %.4f", e.Held)
	if e.NextRelease > 0 {
		fmt.Fprintf(out, "   (next release %s)", time.Unix(e.NextRelease, 0).Format("2006-01-02"))
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  paid to date  %.4f\n\n", e.Paid)
	// LIFETIME, by stream - not a decomposition of the three figures above, which are current
	// and net of any reserve. Absent (rather than zero) when Core could not read the rollup:
	// "from relaying 0.0000" beside a real payable would read as earnings that vanished.
	if e.SplitKnown {
		fmt.Fprintf(out, "  lifetime by stream:\n")
		fmt.Fprintf(out, "    relaying    %.4f   (your Tower carrying sealed work)\n", e.FromRelaying)
		fmt.Fprintf(out, "    serving     %.4f   (your own nodes running models)\n", e.FromServing)
	} else {
		fmt.Fprintf(out, "  lifetime by stream: unavailable right now\n")
	}
	if e.Attempts > 0 {
		fmt.Fprintf(out, "  settled       %d attempt(s)\n", e.Attempts)
	}
	if e.CashOut != "" {
		fmt.Fprintf(out, "\ncash out: %s\n", e.CashOut)
	}
	return nil
}
