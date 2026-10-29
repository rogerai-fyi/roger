package coreclient

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoteNotedReset(t *testing.T) {
	Reset()
	require.Empty(t, Noted())
	Note("towerjoin: link")
	Note("edgeclient")
	got := Noted()
	require.Equal(t, []string{"towerjoin: link", "edgeclient"}, got)
	got[0] = "changed"
	require.Equal(t, "towerjoin: link", Noted()[0], "Noted returns a copy")
	Reset()
	require.Empty(t, Noted())
}
