package main

// localhw.go holds the parts of the local hardware probe that are the same on every
// platform, so the three build-tagged hw_*.go files differ only where the operating
// systems actually differ.
//
// Everything gathered through here is LOCAL ONLY. See internal/detect/localhw.go for why
// that is a hard rule rather than a nicety: the network is given a four-value privacy
// bucket and nothing else, because docs/relay-selection-design.md §4.1 forbids the supply
// side declaring its own capability at all. The rich picture exists for the operator's
// terminal and is dropped when the process that printed it exits.

import (
	"os"
	"runtime"
)

// hwCores is a seam over runtime.NumCPU. Production always uses the real count; a test
// pins it so the CPU-cores requirement can be driven both ways on whatever machine the
// suite happens to run on.
var hwCores = runtime.NumCPU

// hwDiskFreeMiB is a seam over the platform's free-space call (statfs on unix; nothing
// usable in the standard library on Windows, which is why it reports unknown there). It
// returns ok=false rather than 0 when it cannot answer: a report that prints "0 GiB free"
// for a disk it never managed to stat has told the operator something false about their
// own machine.
var hwDiskFreeMiB = diskFreeMiB

// hwProbeDir is the directory whose filesystem the free-space check measures.
//
// It is the home directory, and the report prints the path alongside the number, because
// the honest scope of the measurement is "this filesystem" and not "the disk your weights
// live on". We do not know where the upstream server keeps its weights - Ollama, LM Studio
// and llama.cpp all choose differently and all of them can be pointed elsewhere - so
// naming the filesystem we actually measured is the only way the number is not a
// confident claim about the wrong disk.
func hwProbeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return "."
}
