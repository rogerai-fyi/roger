// Command roger-station is the machine that serves work behind a Tower.
//
// RUN THIS ON THE STATION, NOT ON THE TOWER. That is not a style note. A Station holds two
// private keys and the Tower holds neither: the assertion key signs this Station's offers,
// and the secure-session key terminates its end of the inner channel. If both ended up on
// the Tower, "signed by the Station" would mean "signed by the relay", and every guarantee
// built on it - price, capacity, capability, the receipt chain - would be worth nothing.
//
// The separation is also why this is its own binary rather than a `roger-tower` subcommand:
// a subcommand is one `--dir` typo away from minting a Station's keys on the relay, and no
// comment prevents that.
//
// # THE FLOW
//
//	on the Station   roger-station init --dir DIR
//	                 -> prints the Station ID and BOTH PUBLIC keys
//	on the operator  roger-tower station invite --station-id ... --assertion-key ... --session-key ...
//	                 -> prints an invitation id and a one-time secret
//	on the Tower     roger-tower station attach --invitation ... --secret ...
//	                 -> the Station is attached, in quarantine
//	on the Station   roger-station offer --dir DIR --tower TOWER --model M ... > offer.json
//	                 -> copy offer.json into the Tower's offers directory; it relays it verbatim
//
// The offer moves as a FILE on purpose. Nothing here dials: the Station's keys stay on the
// Station, and a signed offer can be read, diffed and archived before it reaches anything.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

const usage = `roger-station - a Station on the RogerAI network

usage:
  roger-station init  --dir DIR                 (mint this Station's two keys, once)
  roger-station keys  --dir DIR                 (print the public keys for an invitation)
  roger-station offer --dir DIR --tower TOWER --model NAME [options]
  roger-station status --dir DIR
  roger-station version

offer options:
  --modality text        what kind of work this is
  --price-in N           what the consumer pays per input unit
  --price-out N          what the consumer pays per output unit
  --earn-in N            what this Station earns per input unit  (<= --price-in)
  --earn-out N           what this Station earns per output unit (<= --price-out)
  --capacity N           how many concurrent requests this Station will take
  --caps a,b             capabilities this Station supports
  --ttl 30m              how long the offer is good for
  --out FILE             write here instead of stdout

RUN THIS ON THE STATION. It holds private keys that must never reach the Tower relaying
for it - if the relay could sign, "signed by the Station" would mean nothing.

An offer is a signed file. Copy it to the Tower's offers directory (see
` + "`roger-tower serve`" + `); the Tower relays it byte for byte and cannot alter it.
`

// version is set at build time by the release pipeline.
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "roger-station:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	switch args[0] {
	case "init":
		return cmdInit(args[1:], out)
	case "keys":
		return cmdKeys(args[1:], out)
	case "offer":
		return cmdOffer(args[1:], out)
	case "status":
		return cmdStatus(args[1:], out)
	case "version":
		fmt.Fprintln(out, version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func cmdInit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Station data directory (must not already hold one)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	s, err := station.Init(*dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "initialized Station in %s\n", *dir)
	printIdentity(out, s)
	fmt.Fprint(out, "\nBoth private keys are in this directory, mode 0600. They never leave it:\n"+
		"the Tower that relays for this Station must not hold either one.\n")
	return nil
}

func cmdKeys(args []string, out io.Writer) error {
	s, err := openFlagged("keys", args, out)
	if err != nil {
		return err
	}
	printIdentity(out, s)
	return nil
}

func cmdStatus(args []string, out io.Writer) error {
	s, err := openFlagged("status", args, out)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "station id: %s\n", s.StationID)
	fmt.Fprintf(out, "data directory: %s\n", s.Dir())
	// Deliberately not a claim about the NETWORK. This binary makes no connection, so it
	// cannot know whether the Station is attached, in quarantine, or revoked - and printing
	// a stale guess would be worse than printing nothing. `roger-tower stations` asks Core.
	fmt.Fprint(out, "\nThis command reads local state only. Whether Roger Core has this\n"+
		"Station attached, quarantined or revoked is Core's to answer.\n")
	return nil
}

// printIdentity prints exactly what an invitation needs, in the order it is asked for.
func printIdentity(out io.Writer, s *station.Station) {
	fmt.Fprintf(out, "station id:    %s\n", s.StationID)
	fmt.Fprintf(out, "assertion key: %s\n", s.Assertion)
	fmt.Fprintf(out, "session key:   %s\n", s.Session)
}

func cmdOffer(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("offer", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Station data directory")
	tower := fs.String("tower", "", "the Tower ID allowed to relay this offer")
	model := fs.String("model", "", "the model this Station serves")
	modality := fs.String("modality", "text", "the kind of work")
	priceIn := fs.Int64("price-in", 0, "what the consumer pays per input unit")
	priceOut := fs.Int64("price-out", 0, "what the consumer pays per output unit")
	earnIn := fs.Int64("earn-in", 0, "what this Station earns per input unit")
	earnOut := fs.Int64("earn-out", 0, "what this Station earns per output unit")
	capacity := fs.Int64("capacity", 1, "concurrent requests this Station will take")
	caps := fs.String("caps", "", "comma-separated capabilities")
	ttl := fs.Duration("ttl", 30*time.Minute, "how long the offer is good for")
	outFile := fs.String("out", "", "write the signed offer here instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	s, err := station.Open(*dir)
	if err != nil {
		return err
	}
	raw, err := s.SignOffer(station.Offer{
		// The public network, not a flag. A Station signing for a network it was told about
		// on the command line is a Station that can be pointed at the wrong one by a typo,
		// and the signature would be perfectly valid for it.
		Network:      link.PublicNetwork,
		TowerID:      *tower,
		Model:        *model,
		Modality:     *modality,
		PriceIn:      *priceIn,
		PriceOut:     *priceOut,
		EarnIn:       *earnIn,
		EarnOut:      *earnOut,
		Capacity:     *capacity,
		Capabilities: splitCaps(*caps),
		TTL:          *ttl,
	}, time.Now())
	if err != nil {
		return err
	}
	// Pretty-printed for the human who has to look at it before copying it to a relay. The
	// SIGNATURE is over the canonical form, not these bytes, so indentation cannot break it.
	var pretty any
	if err := json.Unmarshal(raw, &pretty); err != nil {
		return err
	}
	indented, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		return err
	}
	indented = append(indented, '\n')

	if *outFile == "" {
		_, err = out.Write(indented)
		return err
	}
	if err := os.WriteFile(*outFile, indented, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "signed offer written to %s\n", *outFile)
	fmt.Fprintf(out, "copy it into the Tower's offers directory; it is relayed byte for byte.\n")
	return nil
}

// splitCaps turns the flag into the list the offer carries. An empty flag is an empty LIST,
// never a missing member: absent capabilities is how a relay would strip the field from the
// signed bytes and assert its own out of band.
func splitCaps(s string) []string {
	out := []string{}
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func openFlagged(name string, args []string, out io.Writer) (*station.Station, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Station data directory")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *dir == "" {
		return nil, fmt.Errorf("--dir is required")
	}
	return station.Open(*dir)
}
