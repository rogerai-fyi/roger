package tower

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJoinedLineNamesWhoJoinedAndHowToUndo(t *testing.T) {
	kh := "4be1c0ffee00a9"
	require.Equal(t, "laptop-x joined (key 4be1...a9); undo with: roger tower revoke-client laptop-x",
		JoinedLine("laptop-x", kh, JoinClient, nil))
	require.Equal(t, "station kitchen-pi joined (key 4be1...a9)", JoinedLine("kitchen-pi", kh, JoinStation, nil))
	require.Equal(t, "4be1...a9 joined (key 4be1...a9); undo with: roger tower revoke-client 4be1...a9",
		JoinedLine("", kh, JoinClient, nil), "a joiner that gave no name is named by its short key")
	require.Equal(t, "ab joined (key ab); undo with: roger tower revoke-client ab", JoinedLine("", "ab", JoinClient, nil))
}
