package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestWrapText(t *testing.T) {
	for _, c := range []struct {
		name  string
		s     string
		width int
		max   int
		want  []string
	}{
		{"fits keeps spacing", "a  ·  b", 20, 0, []string{"a  ·  b"}},
		{"words", "aa bb cc", 5, 0, []string{"aa bb", "cc"}},
		{"hard break", "abcdefgh", 3, 0, []string{"abc", "def", "gh"}},
		{"rune wider than the row", "火火", 1, 0, []string{"火", "火"}},
		{"zero width", "ab", 0, 0, []string{"a", "b"}},
		{"cap with ellipsis", "aa bb cc dd", 5, 1, []string{"aa b…"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := wrapText(c.s, c.width, c.max)
			require.Equal(t, c.want, got)
			for _, r := range got {
				if c.width >= 2 {
					require.LessOrEqual(t, lipgloss.Width(r), c.width)
				}
			}
		})
	}
}
