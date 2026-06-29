// Package glyphs is the single source of the instrument iconography shared by the
// TUI (internal/tui) and the plain-CLI status output (internal/client): the on-air
// /off-air/verified marks, the signal-tower ramp, and the Ping beacon.
//
// It exists for ONE reason: legacy Windows consoles (cmd.exe / conhost under an OEM
// codepage) garble the nice Unicode glyphs (the filled/hollow circles, the diamond,
// the box-drawing). Windows Terminal and PowerShell 7 render them fine, as do macOS
// and Linux terminals. So there are two glyph sets - the rich Unicode look (the
// default, no regression for capable terminals) and a tasteful ASCII fallback - and
// ONE chooser (ASCII()) that decides between them once. Everything routes through here
// so the choice is centralized.
package glyphs

import (
	"os"
	"runtime"
	"strings"
)

// Set is one resolved iconography set. Field names match the meanings the UI uses;
// the Unicode and ASCII values differ only in glyph, not in meaning.
type Set struct {
	OnAir   string // a live carrier / online / a tool firing
	OffAir  string // offline / off air / over-margin
	Verify  string // verified (confidential diamond)
	Lineage string // signed-lineage / verified-operator identity mark
	Beacon  string // the Ping one-eyed beacon, e.g. "(( • ))"
	Signal  []rune // the signal-strength tower ramp, low -> high
	SigOff  string // a flat "no signal" tower (5 cells)
	BoxV    string // box-drawing vertical
	BoxH    string // box-drawing horizontal
}

var unicodeSet = Set{
	OnAir:   "◉",
	OffAir:  "○",
	Verify:  "◆",
	Lineage: "✓",
	Beacon:  "(( • ))",
	Signal:  []rune("▁▂▃▄▅▆▇█"),
	SigOff:  "▁▁▁▁▁",
	BoxV:    "│",
	BoxH:    "─",
}

var asciiSet = Set{
	OnAir:   "(o)",
	OffAir:  "( )",
	Verify:  "<>",
	Lineage: "+",
	Beacon:  "((*))",
	Signal:  []rune(".:-=+*#@"),
	SigOff:  ".....",
	BoxV:    "|",
	BoxH:    "-",
}

// Current returns the resolved glyph set for this process (Unicode unless ASCII()).
func Current() Set {
	if ASCII() {
		return asciiSet
	}
	return unicodeSet
}

// goos is the resolved GOOS used by ASCII(). It is a package-var seam (defaulting
// to the real runtime.GOOS, so the production path is byte-for-byte unchanged) that
// lets a unit test exercise the Windows-only branches of ASCII() on a non-Windows
// host. Production never reassigns it.
var goos = runtime.GOOS

// ASCII reports whether to fall back to the ASCII glyph set instead of the rich
// Unicode one. The rule, in order:
//
//  1. An explicit override always wins: ROGERAI_ASCII=1 or NO_UNICODE set -> ASCII.
//  2. Non-Windows (macOS / Linux) -> Unicode. Their terminals render the glyphs.
//  3. Windows + a known-good UTF-8 terminal -> Unicode. We treat WT_SESSION set
//     (Windows Terminal) or an explicit UTF-8 codepage hint as known-good.
//  4. Otherwise (legacy cmd.exe / conhost under an OEM codepage) -> ASCII.
//
// The default on every capable terminal stays the current Unicode look.
func ASCII() bool {
	if envSet("ROGERAI_ASCII") || envSet("NO_UNICODE") {
		return true
	}
	if goos != "windows" {
		return false
	}
	if windowsUTF8Terminal() {
		return false
	}
	return true
}

// windowsUTF8Terminal reports whether the current Windows console is a known-good
// UTF-8 terminal where the Unicode glyphs render. Windows Terminal exports
// WT_SESSION; PowerShell 7 / a `chcp 65001` session can be signalled via an explicit
// UTF-8 hint in common encoding env vars. Conservative: unknown -> false (ASCII).
func windowsUTF8Terminal() bool {
	if strings.TrimSpace(os.Getenv("WT_SESSION")) != "" {
		return true
	}
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG", "PYTHONIOENCODING"} {
		if hasUTF8(os.Getenv(k)) {
			return true
		}
	}
	return false
}

// asciiFold maps the non-ASCII runes used in the Ping beacon art + signal towers to
// tasteful ASCII stand-ins, so a legacy Windows console renders the mascot without
// mojibake. Runes not present here pass through unchanged.
var asciiFold = map[rune]rune{
	'•': '*', '○': 'o', '◉': '@', '◆': '#', '✓': '+',
	'│': '|', '─': '-', '╰': '+', '╯': '+', '╮': '+', '╭': '+', '╲': '\\', '╱': '/',
	'▔': '"', '╿': '|', '╽': '|', '∩': 'n',
	'▁': '.', '▂': ':', '▃': '-', '▄': '=', '▅': '+', '▆': '*', '▇': '#', '█': '@',
	// Ping World screensaver glyphs (stars / surface shades / moon-adjacent / now-playing / aurora).
	'✦': '*', '✧': '*', '˙': '\'', '·': '.', '♪': '>',
	'░': '.', '▒': ':', '▓': '#',
	'≈': '~', '∼': '~', '∽': '~', '≋': '~',
	// Ping World day scene (the sun disc + the day flower).
	'☀': 'O', '❀': '*',
	// Ping World orbital traffic (the satellite bus + the spaceship cockpit).
	'▢': '#', '◊': 'o',
	// Ping World big round moon/sun outline (quarter-arc corners -> rough ASCII circle).
	'◜': '/', '◝': '\\', '◟': '\\', '◞': '/',
}

// Fold replaces non-ASCII art/signal runes with ASCII stand-ins WHEN ASCII() is in
// effect; otherwise it returns s unchanged. Used to keep the Ping beacon art legible
// on a legacy Windows console without touching the (rune-exact) animation tables.
func Fold(s string) string {
	if !ASCII() {
		return s
	}
	return foldASCII(s)
}

// foldASCII applies the asciiFold map to every rune of s. Exposed (unconditionally)
// for callers that have already decided to fold (e.g. a per-rune eye glyph).
func foldASCII(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if a, ok := asciiFold[r]; ok {
			b.WriteRune(a)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hasUTF8(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "utf-8") || strings.Contains(s, "utf8") || strings.Contains(s, "65001")
}

func envSet(k string) bool { return strings.TrimSpace(os.Getenv(k)) != "" }
