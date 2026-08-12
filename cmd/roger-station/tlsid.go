package main

// tlsid.go is the operator's side of getting a certificate onto a Station.
//
// Two commands, and the shape of them is the security property: the Station MINTS its own
// private key, EMITS a public request, and ACCEPTS a public certificate. The key is never an
// input and never an output, so there is no command here that could put it on a Tower even by
// mistake - and a Tower holding it could read every prompt and completion it relays.

import (
	"flag"
	"fmt"
	"io"
	"os"

	"rogerai.fm/roger/v5/internal/station"
)

func cmdCSR(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("csr", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Station data directory")
	name := fs.String("name", "", "the name Roger Core will issue for, e.g. st-abc123.relay.example")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	if *name == "" {
		// Core issues under a domain it controls. A Station that picked its own name could
		// answer for another Station, which is the one thing the naming scheme prevents.
		return fmt.Errorf("--name is required: Roger Core issues for a name under a domain it " +
			"controls, and a Station cannot pick that name for itself")
	}
	s, err := station.Open(*dir)
	if err != nil {
		return err
	}
	req, err := s.SignCSR(*name)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s", req)
	fmt.Fprintf(out, "\nThe private key stays at %s and must never be copied to a Tower:\n"+
		"a Tower holding it could read every prompt and completion it relays.\n", s.TLSKeyPath())
	return nil
}

func cmdInstallCert(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("install-cert", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "Station data directory")
	certPath := fs.String("cert", "", "the PEM chain Roger Core issued for this Station")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	if *certPath == "" {
		return fmt.Errorf("--cert is required: the PEM chain Roger Core issued for this Station")
	}
	s, err := station.Open(*dir)
	if err != nil {
		return err
	}
	chain, err := os.ReadFile(*certPath)
	if err != nil {
		return err
	}
	if err := s.InstallCert(chain); err != nil {
		return err
	}
	fmt.Fprintf(out, "certificate installed at %s\n", s.TLSCertPath())
	fmt.Fprint(out, "serve with --edge ADDR to terminate consumer sessions here.\n")
	return nil
}
