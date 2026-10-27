// Command roger-tower is the one-release stand-in for `roger tower` (features/packaging/one_binary.feature
// section 3): it prints one deprecation line on stderr and hands the process to roger. It is
// removed in the next release.
package main

import (
	"os"

	"rogerai.fm/roger/v7/internal/towershim"
)

// osExit is a seam so main is testable without ending the test process.
var osExit = os.Exit

func main() { osExit(towershim.Main("roger-tower", os.Args[1:])) }
