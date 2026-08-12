package main

// probe.go is the edge path, exercised as a CONSUMER: authorize with Roger Core, reach a
// Station through a Tower that cannot read the session, and acknowledge what came back.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY THIS COMMAND EXISTS
//
// The edge path had every part built and no first-party consumer to drive it, which meant
// the acknowledgement - the one account of an attempt that does not come from the party being
// paid - had no production caller. `internal/edgeclient` is that consumer; this command is
// what calls it, so the whole authorize -> serve -> acknowledge -> settle loop is reachable
// from a binary rather than only from tests.
//
// It is also the shape a CANARY will take when canaries are built: Core dialling a Tower's
// advertised endpoint with a real grant, indistinguishable in the path from a customer. A
// working `probe` is the proof that shape is sound before anything automated depends on it.

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
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
	path := fs.String("path", "/v1/chat/completions", "the request path the Station will serve")
	bodyFile := fs.String("body", "", "a file holding the request body (default: a tiny probe body)")
	caFile := fs.String("ca", "", "PEM roots to verify the Station cert against (default: system pool)")
	maxIn := fs.Int64("max-in", 64<<10, "the input ceiling to ask Core for")
	maxOut := fs.Int64("max-out", 1<<20, "the output ceiling to ask Core for")
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

	var roots *x509.CertPool
	if *caFile != "" {
		pemBytes, rerr := os.ReadFile(*caFile)
		if rerr != nil {
			return rerr
		}
		roots = x509.NewCertPool()
		if block, _ := pem.Decode(pemBytes); block == nil || !roots.AppendCertsFromPEM(pemBytes) {
			return fmt.Errorf("--ca %q holds no usable certificates", *caFile)
		}
	}

	client := &edgeclient.Client{Broker: base, Key: key, Roots: roots}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	fmt.Fprintf(out, "authorizing an edge attempt for %q...\n", *model)
	auth, err := client.Authorize(ctx, *model, *maxIn, *maxOut)
	if err != nil {
		return fmt.Errorf("authorize failed: %w", err)
	}
	fmt.Fprintf(out, "  attempt %s, relay %s at %s\n", auth.AttemptID, auth.RelayName, auth.Endpoint)

	fmt.Fprintf(out, "connecting to the Station through the Tower...\n")
	res, err := client.Do(ctx, auth, *path, body)
	if err != nil {
		return fmt.Errorf("the request did not complete: %w", err)
	}
	fmt.Fprintf(out, "  status %d, %d bytes\n", res.Status, len(res.Body))

	fmt.Fprintf(out, "acknowledging what was received...\n")
	if err := client.Ack(ctx, auth, res); err != nil {
		// The attempt was still SERVED; the ack is best effort and its failure is worth
		// seeing without being fatal to the probe's own verdict.
		fmt.Fprintf(out, "  acknowledgement did not land: %v\n", err)
	} else {
		fmt.Fprintf(out, "  acknowledged - this attempt settles corroborated\n")
	}
	fmt.Fprint(out, "edge path OK: authorized, served through a blind relay, evidence returned.\n")
	return nil
}
