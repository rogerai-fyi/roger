package tui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The [?] HELP screen is the only place a keyboard-driven app tells you what its keys do,
// and it has now fallen behind twice: `b` (the band card) and `Q` (the quant filter) both
// shipped and worked for releases while HELP never mentioned them. A key nobody can
// discover is, for most users, a key that does not exist.
//
// This reads the dial's own `case "x":` handlers out of the source and asserts HELP
// mentions each one. It is deliberately source-driven rather than a hand-kept list,
// because a hand-kept list is exactly the thing that drifted.
func TestHelpMentionsEveryDialKey(t *testing.T) {
	src, err := os.ReadFile("tui.go")
	if err != nil {
		t.Fatalf("read tui.go: %v", err)
	}
	help := stripANSI(model{}.helpView())

	// The dial's single-character key handlers. Multi-key and control cases (esc, enter,
	// ctrl+c, arrows) are described in prose in HELP rather than as literal glyphs.
	re := regexp.MustCompile(`(?m)^\s*case ("([a-zA-Z])"(, "[a-zA-Z]")*):`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		seen[m[2]] = true
	}

	// Keys that are real but deliberately not in the start-here list: they are either
	// aliases of a documented key, or documented in the command table instead. Each is
	// named so removing one from HELP is a deliberate act, not an omission.
	documentedElsewhere := map[string]string{
		"c": "/chat (c · tab) in the command table",
		"d": "disconnect - described in prose on the channel line",
		"j": "vim extras line",
		"k": "vim extras line",
		"l": "vim extras line",
		"h": "vim extras line",
		"g": "vim extras / jump",
		"n": "described inside the `t` (YOUR BANDS) entry",
		"p": "band card row, reached via b",
		"e": "band card row, reached via b",
		"a": "band card row, reached via b",
		"i": "inspect - vim extras line",
		"r": "/search in the command table",
		"v": "voice booth, reached from SHARE",
		"x": "band card row, reached via b",
		"y": "copy - contextual",
		"u": "contextual",
		"o": "contextual",
	}

	// A key counts as documented only when it appears as a KEY CELL - the left column of
	// the start-here list. A bare strings.Contains would pass for "Q" on the strength of
	// "Q4_K_M" appearing in someone else's description, which is a test that cannot fail.
	// helpView renders each cell as two spaces + the key padded into a fixed column, so
	// the key must be followed by whitespace or a cell separator.
	// Collect the left column: everything before the run of spaces that separates a cell
	// from its description. Cells legitimately group keys ("F/C/O", "m  ·  alt+m"), so a
	// key is documented if it appears in a cell as its own token.
	var cells []string
	cellSplit := regexp.MustCompile(`\s{2,}`)
	for _, line := range strings.Split(help, "\n") {
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		parts := cellSplit.Split(strings.TrimLeft(line, " "), 2)
		if len(parts) == 2 && parts[0] != "" {
			cells = append(cells, parts[0])
		}
	}
	tokenSplit := regexp.MustCompile(`[^A-Za-z+]+`)
	keyCell := func(k string) bool {
		for _, c := range cells {
			for _, tok := range tokenSplit.Split(c, -1) {
				if tok == k {
					return true
				}
			}
		}
		return false
	}

	var missing []string
	for k := range seen {
		if documentedElsewhere[k] != "" {
			continue
		}
		if !keyCell(k) {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("dial keys with no mention in HELP: %v\n"+
			"Either add them to helpView's start-here list, or add them to "+
			"documentedElsewhere with the reason.", missing)
	}
}

// The two that actually drifted, pinned by name and by what they DO - so a future edit
// that keeps the letter but guts the explanation still fails.
func TestHelpExplainsTheBandCardAndQuantFilter(t *testing.T) {
	help := stripANSI(model{}.helpView())

	if !strings.Contains(help, "BAND CARD") {
		t.Error("HELP does not mention the band card (b) - it shipped undocumented once already")
	}
	if !strings.Contains(help, "QUANT") {
		t.Error("HELP does not mention the quant filter (Q)")
	}
	// The quant entry has to say WHY it exists, or it reads as a cosmetic filter.
	if !strings.Contains(help, "same model name are not serving the same weights") {
		t.Error("the quant entry does not say what problem it solves")
	}
}
