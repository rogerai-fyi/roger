package look

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNearbySeenWordsTheAgeAndGoesStale(t *testing.T) {
	for _, c := range []struct {
		since time.Duration
		want  string
	}{
		{0, "seen 0s ago"},
		{59 * time.Second, "seen 59s ago"},
		{time.Minute, "seen 1m ago"},
		{NearbyStaleAfter - time.Second, "seen 4m ago"},
		{NearbyStaleAfter, "last seen 5m ago"},
		{time.Hour, "last seen 1h ago"},
		{23*time.Hour + 59*time.Minute, "last seen 23h ago"},
		{24 * time.Hour, "last seen 1d ago"},
		{50 * time.Hour, "last seen 2d ago"},
	} {
		require.Equal(t, c.want, NearbySeen(c.since), "%s", c.since)
	}
}

func TestNearbyKeyNamesTheCertificateElseTheNodeKey(t *testing.T) {
	for _, c := range []struct {
		pin, id, label, short string
	}{
		{"23f7f59ebbb51e13880f437c", "n_8eda6e1a92c9aa", "fp", "23f7f59ebbb5"},
		{"", "n_8eda6e1a92c9aabbccdd", "key", "8eda6e1a92c9"},
		{"", "n_8eda", "key", "8eda"},
		{"abc", "", "fp", "abc"},
	} {
		label, short := NearbyKey(c.pin, c.id)
		require.Equal(t, c.label, label)
		require.Equal(t, c.short, short)
	}
	require.Equal(t, "123456789012", NearbyShort("1234567890123"))
	require.Equal(t, "", NearbyShort(""))
}

func TestNearbyLinesNameTheDeviceAndTheCommands(t *testing.T) {
	require.Equal(t, "invite for gentle-ibex-14", NearbyInviteHead("gentle-ibex-14"))
	require.Equal(t, "waiting for deskside to ask", NearbyWaitAsk("deskside"))
	require.Equal(t, "on it, run: roger join http://10.0.0.2:39009", NearbyRunLine("http://10.0.0.2:39009"))
	require.Equal(t, "invite it: roger edge invite --for n_ab", NearbyCLIInvite("n_ab"))
	require.Equal(t, "approve it when it asks: roger edge approve n_ab", NearbyCLIApprove("n_ab"))
}

func TestActionsPlainIsOneLine(t *testing.T) {
	require.Equal(t, "e start · j join · esc back", ActionsPlain([]Action{{"e", "start"}, {"j", "join"}, {"esc", "back"}}))
	require.Equal(t, "", ActionsPlain(nil))
}
