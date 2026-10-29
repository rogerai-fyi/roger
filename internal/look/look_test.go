package look

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHexAndPalette(t *testing.T) {
	require.Equal(t, "#ff4438", Live.Hex())
	require.Equal(t, "#0e0d0b", Ground.Hex())
	require.Len(t, Palette(), 12)
	seen := map[RGB]bool{}
	for _, c := range Palette() {
		require.False(t, seen[c], "a palette color is listed twice: %v", c)
		seen[c] = true
	}
}

func TestPiecesWithAndWithoutColor(t *testing.T) {
	cases := []struct {
		name       string
		got, color string
		mono       string
	}{
		{"key cap", "1", " 1 ", "[1]"},
		{"key cap word", "esc", " esc ", "[esc]"},
	}
	for _, c := range cases {
		require.Equal(t, c.color, KeyCap(c.got, false), c.name)
		require.Equal(t, c.mono, KeyCap(c.got, true), c.name)
	}
	require.Equal(t, "FIRST TIME HERE", Eyebrow("first time here"))
	require.Equal(t, "", Rule(0, false))
	require.Equal(t, "───", Rule(3, false))
	require.Equal(t, "---", Rule(3, true))
	for in, want := range map[string]string{Rail: ">", EyeWait: "oo", EyeSeen: "@O", EyeOff: "oo", "((•))": "((*))", "▟•▙": "[*]", "◇": "o", "◈": "+", "▸": ">"} {
		require.Equal(t, want, Fold(in, true), in)
		require.Equal(t, in, Fold(in, false), in)
	}
}

func TestWelcomeWords(t *testing.T) {
	keys := ""
	for _, o := range WelcomeOptions {
		keys += o.Key + " "
		require.NotContains(t, o.What, "$", "no amount on the card")
		require.NotEmpty(t, Fold(o.Glyph, true))
		for _, r := range Fold(o.Glyph, true) {
			require.Less(t, r, rune(128), "%s folds to ASCII", o.Glyph)
		}
	}
	require.Equal(t, WelcomeKeys+" ", keys)
	require.Equal(t, WelcomeOption{"l", "◈", "Log out", "logged in as @octo"}, WelcomeLoggedIn("octo"))
	require.Equal(t, "logged in as your account", WelcomeLoggedIn("").What)
}

func TestEdgeScanLines(t *testing.T) {
	cases := []struct {
		name        string
		s           EdgeScan
		first, next string
		eye         string
	}{
		{"before a pass", EdgeScan{Every: 30 * time.Second}, "scanning this network while this screen is open (every 30s)", "no pass has completed yet", EyeWait},
		{"interval not reported", EdgeScan{}, "scanning this network while this screen is open (every 30s)", "no pass has completed yet", EyeWait},
		{"10s pass, none", EdgeScan{Every: 10 * time.Second, PassDone: true}, "scanning this network while this screen is open (every 10s)", "no other roger machines seen nearby", EyeSeen},
		{"minutes", EdgeScan{Every: 2 * time.Minute, PassDone: true, Seen: 1}, "scanning this network while this screen is open (every 2m)", "1 roger machine seen nearby · none in an Edge yet", EyeSeen},
		{"hours", EdgeScan{Every: time.Hour, PassDone: true, Seen: 3, InEdge: 2}, "scanning this network while this screen is open (every 1h)", "3 roger machines seen nearby · 2 in an Edge", EyeSeen},
		{"off", EdgeScan{Off: true, PassDone: true, Seen: 4}, "scanning is off (ROGERAI_EDGE_DISCOVERY=0)", "the two ways in still work: e start, j join", EyeOff},
	}
	for _, c := range cases {
		f, n := c.s.Lines()
		require.Equal(t, c.first, f, c.name)
		require.Equal(t, c.next, n, c.name)
		require.Equal(t, c.eye, c.s.Eye(), c.name)
		require.False(t, strings.Contains(n, "0 roger machines"), c.name)
	}
	require.Equal(t, "2 roger machines seen nearby · none in an Edge yet", NearbyLine(2, 0))
	require.Equal(t, "no other roger machines seen nearby", NearbyLine(-1, 0))
	require.Equal(t, "90s", shortDur(90*time.Second))
}
