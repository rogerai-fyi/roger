// Command roger-tower-local serves a STANDALONE Tower's consumer plane. It is a thin main over
// internal/localplane/serve, the same code `roger tower serve` runs on a standalone directory.
//
// Contract: features/tower/standalone_consumer_plane.feature.
package main

import (
	"fmt"
	"io"
	"os"

	"rogerai.fm/roger/v6/internal/localplane/serve"
)

// version is set at build time by the release pipeline (-X main.version=...).
var version = "dev"

// osExit is a seam so main's failure exit is testable without ending the test process.
var osExit = os.Exit

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "roger-tower-local:", err)
		osExit(1)
	}
}

func run(args []string, out io.Writer) error {
	// `version` / `--version` reports the build and returns; there is nothing to serve.
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintln(out, version)
		return nil
	}
	return serve.Run(args, out)
}
