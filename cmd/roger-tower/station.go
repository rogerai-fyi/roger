package main

// station.go is the JOINED Station lifecycle: authorizing a Station onto the public network
// and redeeming that authorization.
//
// It is deliberately a separate command family from the top-level `attach`, which admits a
// Station to a STANDALONE Tower's own local network. The two look similar and are not:
// standalone attachment is authorized by the local administrator against a local trust root
// and never leaves the machine, while this is Roger Core recording an identity on the public
// network under an account that can be suspended. Collapsing them into one command would
// mean one flag deciding which trust root a Station belongs to.
//
// THE ROUTES EXISTED AND NOTHING CALLED THEM. /tower/station/invite and
// /tower/station/attach were built, tested from the server's side, and reachable only by
// hand-rolling a signed HTTP request - so an operator following the documentation could not
// attach a Station at all. Every joined Tower was therefore inert: attachment is what
// records the key each offer is verified against, and Core refuses a leaf from a Station it
// has no record of.

import (
	"flag"
	"fmt"
	"io"

	"rogerai.fm/roger/v5/internal/towerjoin"
)

const stationUsage = `roger-tower station - Stations on the public network

  roger-tower station revoke --dir DIR --station-id ID

Nodes attach themselves now: a provider runs ` + "`roger share`" + ` and Roger Core
records the attachment (the invite-file ceremony died with the roger-station binary).
revoke remains the operator's kill switch for a station serving under their tower.
`

func cmdStation(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, stationUsage)
		return nil
	}
	switch args[0] {
	case "revoke":
		return cmdStationRevoke(args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, stationUsage)
		return nil
	default:
		return fmt.Errorf("unknown station subcommand %q\n\n%s", args[0], stationUsage)
	}
}

func cmdStationRevoke(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("station revoke", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	id := fs.String("station-id", "", "the Station to retire")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDir(*dir)
	if err != nil {
		return err
	}
	defer release()

	if err := towerjoin.RevokeStation(st, *id); err != nil {
		return err
	}
	fmt.Fprintf(out, "revoked station %s\n", *id)
	// The leaf goes when the Tower next pushes; until then policy refuses it because the
	// attachment is revoked. Saying so stops an operator concluding the revocation did not
	// take when they see the Station in their own offers directory a moment later.
	fmt.Fprint(out, "Its offers stop being routable immediately. Remove its file from this\n"+
		"Tower's offers directory so the next inventory stops carrying it.\n")
	return nil
}

// cmdDrain pauses this Tower: no new work, link kept so in-flight work can finish.
//
// Distinct from stopping `serve`, which drops the inventory and goes. Draining leaves the
// Tower connected and visible, which is what an operator wants before a disk swap or an
// upgrade - and it is reversible with `resume`.
func cmdDrain(args []string, out io.Writer) error {
	return setOwnState(args, out, "drain", "draining",
		"draining: Roger Core will send no new work.\n"+
			"In-flight work finishes on its own deadlines, and the link stays up.\n"+
			"Run `roger-tower resume --dir DIR` to take work again.\n")
}

// cmdResume puts a drained Tower back into service.
//
// It can only return a Tower to a state it already held. Leaving QUARANTINE is an
// administrator's decision and this cannot make it - see operatorMayMove on the server.
func cmdResume(args []string, out io.Writer) error {
	return setOwnState(args, out, "resume", "active",
		"back in service: Roger Core may route work to this Tower's Stations again.\n")
}

// cmdRevoke retires this Tower's identity, for good.
func cmdRevoke(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	confirm := fs.Bool("yes", false, "confirm: this cannot be undone")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		// TERMINAL AND UNRECOVERABLE. There is no path back from revoked - the Tower must
		// enroll again as a NEW identity, and everything attached to the old one goes with
		// it. A flag is a small price for a decision with no undo.
		return fmt.Errorf("revoking retires this Tower's identity permanently: it cannot be " +
			"un-revoked, and a replacement enrolls as a NEW Tower with new Stations.\n" +
			"Re-run with --yes if that is what you mean")
	}
	st, release, err := openDir(*dir)
	if err != nil {
		return err
	}
	defer release()
	if err := towerjoin.SetOwnState(st, "revoked"); err != nil {
		return err
	}
	fmt.Fprint(out, "revoked: this Tower is retired and can no longer hold a link.\n"+
		"Its data directory is now inert; a replacement must `init` and `register` afresh.\n")
	return nil
}

func setOwnState(args []string, out io.Writer, name, state, note string) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDir(*dir)
	if err != nil {
		return err
	}
	defer release()
	if err := towerjoin.SetOwnState(st, state); err != nil {
		return err
	}
	fmt.Fprint(out, note)
	return nil
}
