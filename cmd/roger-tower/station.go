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

  roger-tower station invite --dir DIR --assertion-key HEX --session-key HEX [--station-id ID]
  roger-tower station attach --dir DIR --invitation ID --secret S \
                             --assertion-key HEX --session-key HEX [--station-id ID]
  roger-tower station revoke --dir DIR --station-id ID

Run ` + "`roger-station init`" + ` ON THE STATION first: it mints the two keys and prints
their public halves, which are what these commands carry. The Station keeps the private
halves and this Tower never sees them - if the relay could sign for a Station, "signed by
the Station" would mean nothing.

invite is signed by your ACCOUNT (` + "`roger-tower login`" + `); attach is signed by this
TOWER. That is not an accident of who runs what: authorizing a machine to serve under your
account is an account decision, and redeeming is the relay proving which origin the Station
is attached behind.
`

func cmdStation(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, stationUsage)
		return nil
	}
	switch args[0] {
	case "invite":
		return cmdStationInvite(args[1:], out)
	case "attach":
		return cmdStationAttach(args[1:], out)
	case "revoke":
		return cmdStationRevoke(args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, stationUsage)
		return nil
	default:
		return fmt.Errorf("unknown station subcommand %q\n\n%s", args[0], stationUsage)
	}
}

// stationKeyFlags registers the pair every one of these commands needs.
func stationKeyFlags(fs *flag.FlagSet) (assertion, session, id *string) {
	assertion = fs.String("assertion-key", "", "the Station's assertion public key (hex)")
	session = fs.String("session-key", "", "the Station's secure-session public key (hex)")
	id = fs.String("station-id", "", "the Station's id (Roger Core allocates one if omitted)")
	return
}

func cmdStationInvite(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("station invite", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	assertion, session, id := stationKeyFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDir(*dir)
	if err != nil {
		return err
	}
	defer release()

	inv, err := towerjoin.InviteStation(st, towerjoin.StationKeys{
		StationID: *id, AssertionKey: *assertion, SessionKey: *session,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "invitation: %s\n", inv.InvitationID)
	fmt.Fprintf(out, "station:    %s\n", inv.StationID)
	fmt.Fprintf(out, "secret:     %s\n", inv.Secret)
	fmt.Fprintf(out, "expires in: %ds\n", inv.ExpiresIn)
	// Said at the point the secret is on screen, because that is the only moment it can be
	// acted on. Core does not store it and cannot show it again; a lost invitation is
	// re-issued, never recovered.
	fmt.Fprint(out, "\nThe secret is shown ONCE and is not stored. Redeem it with:\n")
	fmt.Fprintf(out, "  roger-tower station attach --dir DIR --invitation %s --secret %s \\\n",
		inv.InvitationID, inv.Secret)
	fmt.Fprintf(out, "      --station-id %s --assertion-key %s --session-key %s\n",
		inv.StationID, *assertion, *session)
	return nil
}

func cmdStationAttach(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("station attach", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Tower data directory")
	invitation := fs.String("invitation", "", "the invitation id")
	secret := fs.String("secret", "", "the one-time secret from `station invite`")
	assertion, session, id := stationKeyFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, release, err := openDir(*dir)
	if err != nil {
		return err
	}
	defer release()

	at, err := towerjoin.AttachStation(st, towerjoin.Invitation{
		InvitationID: *invitation, Secret: *secret, StationID: *id,
		Keys: towerjoin.StationKeys{
			StationID: *id, AssertionKey: *assertion, SessionKey: *session,
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "attached station %s (%s)\n", at.StationID, at.State)
	if at.State == "quarantine" {
		// Not a failure, and the single most likely thing to be misread as one. A Station is
		// never trusted with public work on arrival.
		fmt.Fprint(out, "\nQuarantine is the expected state: a new Station is not yet eligible for\n"+
			"public work. Roger Core opens that gate itself once it has its own evidence.\n")
	}
	fmt.Fprint(out, "\nNext, on the STATION:\n"+
		"  roger-station offer --dir DIR --tower TOWER --model NAME ... --out offer.json\n"+
		"then copy offer.json into this Tower's `offers` directory. `roger-tower serve`\n"+
		"relays it byte for byte.\n")
	return nil
}

// cmdStationRevoke retires a Station identity.
//
// Signed by the ACCOUNT rather than the Tower, so an operator can still do it when the Tower
// is the thing that has gone wrong. A revocation that needed a healthy relay would be
// unavailable in precisely the situation it exists for.
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
