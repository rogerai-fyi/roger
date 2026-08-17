package main

// probe.go is the edge path, exercised as a CONSUMER: authorize with Roger Core, submit a
// SEALED request through a Tower's hub, open the answer, and acknowledge what came back.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY THIS COMMAND EXISTS
//
// It is the operator's own end-to-end check of the sealed loop - the same
// authorize -> seal -> submit -> open -> acknowledge flow the first-party client and Core's
// canaries drive, reachable from a binary rather than only from tests. A working probe is
// "my tower carries sealed work and the evidence comes back", proven from the outside.
//
// (The original probe drove the raw-TLS splice path; it was ported here when the hub became
// the data plane. The canary made the same move - see cmd/rogerai-broker/towercanary.go.)

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"rogerai.fm/roger/v5/internal/edgeclient"
)

func cmdProbe(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(out)
	broker := fs.String("broker", "", "Roger Core base URL (defaults to $ROGER_BROKER)")
	model := fs.String("model", "", "the model to ask for")
	bodyFile := fs.String("body", "", "a file holding the request body (default: a tiny probe body)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *model == "" {
		return fmt.Errorf("--model is required: a probe asks for a specific model")
	}
	base := *broker
	if base == "" {
		base = os.Getenv("ROGER_BROKER")
	}
	if base == "" {
		return fmt.Errorf("--broker or $ROGER_BROKER is required")
	}

	// A consumer identity. Ephemeral by design: a probe is not an account holder, it is a
	// signed caller, and a fresh key per run keeps a probe from being mistaken for one.
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}

	body := []byte(`{"probe":true}`)
	if *bodyFile != "" {
		body, err = os.ReadFile(*bodyFile)
		if err != nil {
			return err
		}
	}

	client := &edgeclient.Client{Broker: base, Key: key}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	fmt.Fprintf(out, "authorizing a sealed edge attempt for %q...\n", *model)
	auth, err := client.AuthorizeSealed(ctx, *model)
	if err != nil {
		return fmt.Errorf("authorize failed: %w", err)
	}
	fmt.Fprintf(out, "  attempt %s, hub at %s (price %d/%d micro-USD per 1M tokens)\n",
		auth.AttemptID, auth.Endpoint, auth.PriceInMicros, auth.PriceOutMicros)

	fmt.Fprintf(out, "submitting sealed work through the tower's hub...\n")
	res, err := client.DoSealed(ctx, &auth, body)
	if err != nil {
		return fmt.Errorf("the request did not complete: %w", err)
	}
	fmt.Fprintf(out, "  status %d, %d bytes (opened - the tower carried only ciphertext)\n", res.Status, len(res.Body))

	fmt.Fprintf(out, "acknowledging what was received...\n")
	if err := client.AckSealed(ctx, &auth, res); err != nil {
		// The attempt was still SERVED; the ack is best effort and its failure is worth
		// seeing without being fatal to the probe's own verdict.
		fmt.Fprintf(out, "  acknowledgement did not land: %v\n", err)
	} else {
		fmt.Fprintf(out, "  acknowledged - this attempt settles corroborated\n")
	}
	fmt.Fprint(out, "edge path OK: authorized, served sealed through a blind hub, evidence returned.\n")
	return nil
}
