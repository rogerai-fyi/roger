package edge

// Unit spec for the SHARED view rules (view.go): arrangement, session grouping, the
// outcome words and the age words. The TUI and the console both delegate here, so the
// rules are pinned once, at the source, rather than only through the surfaces.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

func lan(addr string) store.EdgeTransport  { return store.EdgeTransport{Kind: "lan", Addr: addr} }
func relay(via string) store.EdgeTransport { return store.EdgeTransport{Kind: "relay", Addr: via} }

func node(id, name string, ts ...store.EdgeTransport) store.EdgeNode {
	return store.EdgeNode{ID: id, Name: name, Transports: ts}
}

func namesOf(rows []Row) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Node.Name)
	}
	return out
}

func TestViaPrefersTheLANLink(t *testing.T) {
	require.True(t, HasLANTransport(node("a", "a", lan("10.0.0.1"))))
	require.False(t, HasLANTransport(node("a", "a", relay("shed"))))
	require.Equal(t, "", Via(node("a", "a", lan("10.0.0.1"), relay("shed"))), "a node with both is drawn on its LAN link")
	require.Equal(t, "shed", Via(node("a", "a", relay("shed"))))
	require.Equal(t, "", Via(node("a", "a")), "no transport at all is not a relayed node")
}

func TestArrangeDrawsEveryMemberOnceUnderItsRelay(t *testing.T) {
	cases := []struct {
		name string
		in   []store.EdgeNode
		want []string
		via  map[string]string // name -> relay it is drawn under
	}{
		{"child under its relay", []store.EdgeNode{
			node("b", "bench", relay("shed")), node("s", "shed", relay("relay.rogerai.fm")),
		}, []string{"shed", "bench"}, map[string]string{"bench": "shed"}},
		{"relay not a member: plain relayed edge", []store.EdgeNode{
			node("b", "bench", relay("gone")),
		}, []string{"bench"}, map[string]string{"bench": ""}},
		{"a node naming itself is not its own child", []store.EdgeNode{
			node("b", "bench", relay("bench")),
		}, []string{"bench"}, map[string]string{"bench": ""}},
		{"a cycle strands nobody", []store.EdgeNode{
			node("a", "a", relay("b")), node("b", "b", relay("a")),
		}, []string{"a", "b"}, map[string]string{}},
		{"a chain keeps every hop", []store.EdgeNode{
			node("c", "c", relay("b")), node("b", "b", relay("a")), node("a", "a", lan("10.0.0.1")),
		}, []string{"a", "b", "c"}, map[string]string{"b": "a", "c": "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := Arrange(c.in)
			require.Equal(t, c.want, namesOf(rows))
			seen := map[string]int{}
			for _, r := range rows {
				seen[r.Node.ID]++
				if want, ok := c.via[r.Node.Name]; ok {
					require.Equal(t, want, r.Via, r.Node.Name)
					require.Equal(t, want != "", r.Child, r.Node.Name)
				}
			}
			for id, n := range seen {
				require.Equal(t, 1, n, "node %s drawn %d times", id, n)
			}
		})
	}
	rows := Arrange([]store.EdgeNode{node("b", "bench", relay("shed")), node("s", "shed", lan("10.0.0.2"))})
	require.True(t, rows[0].Relay, "a node others are reached through is flagged as a relay")
	require.False(t, rows[1].Relay)
}

func TestOutcomeLabel(t *testing.T) {
	require.Equal(t, "served", Session{Outcome: OutcomeServed}.OutcomeLabel())
	require.Equal(t, EscalateLabel, Session{Outcome: OutcomeServed, Escalate: true}.OutcomeLabel())
	require.Equal(t, "REFUSED · over-limit", Session{Outcome: OutcomeRefused, Reason: "over-limit"}.OutcomeLabel())
	require.Equal(t, "REFUSED", Session{Outcome: OutcomeRefused}.OutcomeLabel())
}

func TestGroupSessionsBusiestFirstThenNewest(t *testing.T) {
	mk := func(req, who string, at int64) Session {
		return Session{Request: req, Kind: FromGuest, Who: who, Band: "b", Station: "s", Outcome: OutcomeServed, At: at}
	}
	list := []Session{mk("a1", "aider", 10), mk("o1", "opencode", 5), mk("o2", "opencode", 6), mk("o3", "opencode", 7), mk("z1", "zed", 10)}
	got := GroupSessions(list)
	require.Len(t, got, 3)
	require.Equal(t, "opencode", got[0].Session.Who)
	require.Equal(t, 3, got[0].Count)
	// two singletons at the same time: the newest first, then by request id
	require.Equal(t, "a1", got[1].Session.Request)
	require.Equal(t, "z1", got[2].Session.Request)
	require.Equal(t, 1, got[1].Count)
	// a different outcome is a different row, even for the same participant
	list = append(list, Session{Request: "o9", Kind: FromGuest, Who: "opencode", Band: "b", Station: "s", Outcome: OutcomeRefused, Reason: "x", At: 8})
	require.Len(t, GroupSessions(list), 4)
	require.Empty(t, GroupSessions(nil))
}

func TestAgeWords(t *testing.T) {
	require.Equal(t, "0s", Age(-time.Second))
	require.Equal(t, "45s", Age(45*time.Second))
	require.Equal(t, "6m", Age(6*time.Minute+20*time.Second))
	require.Equal(t, "3h", Age(3*time.Hour+5*time.Minute))
	require.Equal(t, "2d", Age(50*time.Hour))
}

func TestReportUnavailable(t *testing.T) {
	require.False(t, Report{}.Unavailable())
	require.False(t, Report{Notes: []string{"no peers found"}}.Unavailable())
	require.True(t, Report{Notes: []string{"no peers found", noteUnavailable}}.Unavailable())
}
