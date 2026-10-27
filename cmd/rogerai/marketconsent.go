package main

// marketconsent.go: nothing auto-detected goes on the public market without a yes
// (features/sharing/market_offer_consent.feature, founder ruling 2026-10-11). `roger share`
// with no model named shows what it would offer, where it comes from and what kind of server
// that is, and asks y/N (default N). Without a terminal it refuses and says how to name one.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"rogerai.fm/roger/v7/internal/detect"
)

// shareInteractive reports whether `roger share` can ask its operator; a test replaces it.
var shareInteractive = func() bool { return edgeIsInteractive() }

// shareStdin is where the market confirmation is read from; a test replaces it.
var shareStdin io.Reader = os.Stdin

// errNotOffered is the operator declining: nothing went on air, and that is not a failure.
var errNotOffered = errors.New("nothing went on the market")

// confirmMarketOffer asks before an auto-picked model goes on the public market.
func confirmMarketOffer(out io.Writer, model string, from detect.Found) error {
	where := from.BaseURL
	if where == "" {
		where = from.Chat
	}
	if !shareInteractive() {
		return fmt.Errorf("found %s at %s, but nothing auto-detected goes on the market without a yes.\nname the model to share it: roger share <model>", model, where)
	}
	fmt.Fprintf(out, "about to offer on the public market: %s\n", model)
	fmt.Fprintf(out, "  from: %s (%s)\n", where, from.Name)
	switch {
	case isAccountProxy(from.Models) || isHostedModel(model):
		fmt.Fprintf(out, "  this is a proxy to your %s account: anyone on the market would use your quota\n", hostedAccount(model, from.Models))
	case isRogerServed(from.BaseURL, rogerServedBases()):
		fmt.Fprintln(out, "  served by roger on this machine")
	default:
		fmt.Fprintln(out, "  another server on this machine (not served by roger)")
	}
	fmt.Fprint(out, "put it on the public market? [y/N]: ")
	line, _ := bufio.NewReader(shareStdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	fmt.Fprintln(out, "nothing went on the market. To share a model, name it: roger share <model>")
	return errNotOffered
}

// hostedAccount names the hosted family a proxy fronts, for the warning ("your claude account").
func hostedAccount(model string, models []string) string {
	for _, m := range append([]string{model}, models...) {
		id := strings.ToLower(m)
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = id[i+1:]
		}
		for _, p := range hostedFamilies {
			if strings.HasPrefix(id, p) && !strings.HasPrefix(id, "gpt-oss") {
				return strings.TrimSuffix(p, "-")
			}
		}
	}
	return "hosted"
}
