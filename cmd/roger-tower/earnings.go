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

	"rogerai.fm/roger/v5/internal/towerjoin"
)

func cmdEarnings(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("earnings", flag.ContinueOnError)
	fs.SetOutput(out)
	dir, cfg := dirAndConfig(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDirWith(*dir, *cfg)
	if err != nil {
		return err
	}
	defer release()

	e, err := towerjoin.FetchEarnings(st)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "earnings for this account (%s)\n\n", e.Unit)
	fmt.Fprintf(out, "  payable now   %.4f\n", e.Payable)
	fmt.Fprintf(out, "  held          %.4f", e.Held)
	if e.NextRelease > 0 {
		fmt.Fprintf(out, "   (next release %s)", time.Unix(e.NextRelease, 0).Format("2006-01-02"))
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  paid to date  %.4f\n\n", e.Paid)
	fmt.Fprintf(out, "  from relaying %.4f   (your Tower carrying sealed work)\n", e.FromRelaying)
	fmt.Fprintf(out, "  from serving  %.4f   (your own nodes running models)\n", e.FromServing)
	if e.Attempts > 0 {
		fmt.Fprintf(out, "  settled       %d attempt(s)\n", e.Attempts)
	}
	if e.CashOut != "" {
		fmt.Fprintf(out, "\ncash out: %s\n", e.CashOut)
	}
	return nil
}
