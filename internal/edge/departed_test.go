package edge_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestDepartedOfKeepsOnlyAnOwnersName(t *testing.T) {
	at := time.Unix(100, 0)
	for _, tc := range []struct {
		name string
		n    store.EdgeNode
		want edge.Departed
	}{
		{"named board", store.EdgeNode{ID: "n_a", Name: "attic", Kind: "board"},
			edge.Departed{ID: "n_a", Name: "attic", Kind: "board", At: 100}},
		{"an id is not a name", store.EdgeNode{ID: "n_0123456789abcdef0123", Name: "n_0123456789abcdef0123", Kind: "mobile"},
			edge.Departed{ID: "n_0123456789abcdef0123", Kind: "mobile", At: 100}},
		{"blank name", store.EdgeNode{ID: "n_b", Name: "  ", Kind: "host"},
			edge.Departed{ID: "n_b", Kind: "host", At: 100}},
		{"hardware word", store.EdgeNode{ID: "n_c", Name: "jet", Kind: "host", Instances: []store.EdgeInstance{
			{Name: "a"}, {Name: "b", Software: &store.EdgeSoftware{Machine: "NVIDIA Jetson Orin Nano"}}}},
			edge.Departed{ID: "n_c", Name: "jet", Kind: "host", Hardware: "NVIDIA Jetson", At: 100}},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, edge.DepartedOf(tc.n, at)) })
	}
}

func TestRecordDepartedReplacesTheSameDevice(t *testing.T) {
	list := edge.RecordDeparted(nil, edge.Departed{ID: "n_a", Name: "attic", At: 1})
	list = edge.RecordDeparted(list, edge.Departed{ID: "n_b", Name: "shed", At: 2})
	list = edge.RecordDeparted(list, edge.Departed{ID: "n_a", Name: "loft", At: 3})
	require.Equal(t, []edge.Departed{{ID: "n_b", Name: "shed", At: 2}, {ID: "n_a", Name: "loft", At: 3}}, list,
		"forgetting a device twice keeps one record: the latest")
	// An out-of-order record (a clock that ran backwards) still bounds by age, not by arrival.
	list = edge.RecordDeparted(list, edge.Departed{ID: "n_c", Name: "early", At: 0})
	require.Equal(t, "early", list[0].Name)
}

func TestFindDeparted(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	list := []edge.Departed{
		{ID: "n_a1", Name: "attic", Hardware: "Pi 4", At: now.Add(-3 * 24 * time.Hour).Unix()},
		{ID: "n_b", Name: "shed", At: 2},
		{ID: "n_a2", Name: "attic", Kind: "mobile", At: now.Add(-2 * time.Hour).Unix()},
		{ID: "n_u", At: 4},
	}
	for _, tc := range []struct {
		want string
		id   string
		ok   bool
	}{
		{"shed", "n_b", true},
		{" shed ", "n_b", true},
		{"n_u", "n_u", true}, // an unnamed record, by the only handle it has
		{"n_a1", "n_a1", true},
		{"loft", "", false},
		{"", "", false},
	} {
		t.Run(tc.want, func(t *testing.T) {
			d, ok, err := edge.FindDeparted(list, tc.want, now)
			require.NoError(t, err)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.id, d.ID)
		})
	}
	// Two forgotten devices that had one name: each is said by what it is and when it left, and the
	// id appears only as the argument that picks it.
	_, ok, err := edge.FindDeparted(list, "attic", now)
	require.False(t, ok)
	require.EqualError(t, err, `"attic" names 2 forgotten devices - re-admit the one you mean: `+
		"a Pi 4 forgotten 3 days ago (`roger edge readmit n_a1`), "+
		"a phone forgotten 2 hours ago (`roger edge readmit n_a2`)")
}

func TestReAdmitHowNeverOffersANameThatResolvesElsewhere(t *testing.T) {
	list := []edge.Departed{
		{ID: "n_a", Name: "attic"},
		{ID: "n_s1", Name: "shed"}, {ID: "n_s2", Name: "shed"},
		{ID: "n_l", Name: "loft"},
		{ID: "n_u"},
		{ID: "n_p", Name: "porch"},
	}
	others := []store.EdgeNode{
		{ID: "n_new", Name: "loft"}, // a member that took a forgotten device's name
		{ID: "n_p", Name: "porch"},  // the same device, back: its own name is no conflict
	}
	for _, tc := range []struct{ id, want string }{
		{"n_a", "roger edge readmit attic"},
		{"n_s1", "roger edge readmit n_s1"},
		{"n_s2", "roger edge readmit n_s2"},
		{"n_l", "roger edge readmit n_l"},
		{"n_u", "roger edge readmit n_u"},
		{"n_p", "roger edge readmit porch"},
	} {
		var d edge.Departed
		for _, x := range list {
			if x.ID == tc.id {
				d = x
			}
		}
		require.Equal(t, tc.want, edge.ReAdmitHow(d, list, others), tc.id)
	}
	require.Equal(t, []store.EdgeNode{{ID: "n_new", Name: "loft"}}, edge.NameHeldElsewhere(list[3], others))
	require.Empty(t, edge.NameHeldElsewhere(list[5], others))
	require.Empty(t, edge.NameHeldElsewhere(list[4], others), "an unnamed record has no name to collide")
}

func TestDepartedWordsAndAgo(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	for _, tc := range []struct {
		d    edge.Departed
		ago  time.Duration
		want string
	}{
		{edge.Departed{Name: "attic", Hardware: "Pi 4"}, 3 * 24 * time.Hour, "attic, a Pi 4 forgotten 3 days ago"},
		{edge.Departed{Name: "pocket", Hardware: "iPhone 15"}, 26 * time.Hour, "pocket, an iPhone 15 forgotten 1 day ago"},
		{edge.Departed{Name: "rig", Kind: "host"}, 2 * time.Hour, "rig, a machine forgotten 2 hours ago"},
		{edge.Departed{Name: "rig", Kind: "host"}, 61 * time.Minute, "rig, a machine forgotten 1 hour ago"},
		{edge.Departed{Kind: "board"}, 5 * time.Minute, "a board forgotten 5 minutes ago"},
		{edge.Departed{Kind: "board"}, time.Minute, "a board forgotten 1 minute ago"},
		{edge.Departed{}, 10 * time.Second, "a device forgotten just now"},
		{edge.Departed{Hardware: "ESP32"}, -time.Hour, "an ESP32 forgotten just now"},
	} {
		tc.d.At = now.Add(-tc.ago).Unix()
		require.Equal(t, tc.want, edge.DepartedWords(tc.d, now))
	}
	q := edge.ReAdmitQuestion(edge.Departed{Hardware: "Pi 4", At: now.Unix()}, now)
	require.Equal(t, "re-admit a Pi 4 forgotten just now?", q)
}

func TestNewestDepartedLeavesOutDevicesThatAreBack(t *testing.T) {
	list := []edge.Departed{{ID: "n_a", At: 1}, {ID: "n_b", At: 3}, {ID: "n_c", At: 2}}
	got := edge.NewestDeparted(list, []store.EdgeNode{{ID: "n_c"}})
	require.Equal(t, []edge.Departed{{ID: "n_b", At: 3}, {ID: "n_a", At: 1}}, got)
	require.Equal(t, []edge.Departed{{ID: "n_a", At: 1}, {ID: "n_b", At: 3}, {ID: "n_c", At: 2}}, list, "the input is not reordered")
}

func TestDepartedName(t *testing.T) {
	list := []edge.Departed{{ID: "n_a", Name: "attic"}, {ID: "n_u"}}
	require.Equal(t, "attic", edge.DepartedName(list, "n_a"))
	require.Equal(t, "", edge.DepartedName(list, "n_u"))
	require.Equal(t, "", edge.DepartedName(list, "n_x"))
	require.Equal(t, "", edge.DepartedName(nil, "n_a"))
}

func TestAReturningNameNeverTakesAnotherMembersName(t *testing.T) {
	f := edge.NewFleet(store.NewMem(), "acct")
	_, err := f.Enroll(store.EdgeNode{ID: "n_new", Name: "attic", Kind: "host"})
	require.NoError(t, err)
	f.NameReturning(func(string) string { return "attic" })
	got, err := f.Observe("n_old", edge.Observation{Name: "raspberrypi", Kind: "host", Addr: "10.0.0.2:1", Fingerprint: strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.Equal(t, "raspberrypi", got.Name, "the name is held by another member now: the device keeps its own")

	// An unusable remembered name is not forced on it either.
	f.NameReturning(func(string) string { return "../bad" })
	got, err = f.Observe("n_odd", edge.Observation{Name: "odd", Kind: "host", Addr: "10.0.0.3:1", Fingerprint: strings.Repeat("b", 64)})
	require.NoError(t, err)
	require.Equal(t, "odd", got.Name)

	// A member already on the Edge keeps its name: the hook is for coming BACK, not for renaming.
	f.NameReturning(func(string) string { return "renamed" })
	got, err = f.Observe("n_odd", edge.Observation{Name: "odd", Kind: "host", Addr: "10.0.0.3:1", Fingerprint: strings.Repeat("b", 64)})
	require.NoError(t, err)
	require.Equal(t, "odd", got.Name)
}
