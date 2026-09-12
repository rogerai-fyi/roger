package main

// Units for the `roger edge` surface: the corners features/edge/cli.feature does not walk
// - the exit-code mapping, the hand-rolled argument parser, the cache that is not ours,
// and the refusals that keep an owner from adopting or forgetting the wrong thing.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// edgeWriteAuth writes the real auth record the CLI reads (client.LinkedLogin), so a test
// that needs a logged-in owner takes the production path to that fact.
func edgeWriteAuth(t *testing.T, login string) {
	t.Helper()
	dir := filepath.Dir(configPath())
	require.NoError(t, os.MkdirAll(dir, 0o700))
	b, err := json.Marshal(map[string]any{"github_login": login, "github_id": 42, "bound_at": time.Now().Unix()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600))
}

// edgeSeed stands an Edge up on disk through the real fleet.
func edgeSeed(t *testing.T, nodes ...store.EdgeNode) {
	t.Helper()
	st, err := loadEdgeState()
	require.NoError(t, err)
	for _, n := range nodes {
		_, err := st.fleet.Enroll(n)
		require.NoError(t, err)
	}
	require.NoError(t, st.save())
}

func edgeRun(t *testing.T, args ...string) (string, int) {
	t.Helper()
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), args) })
	if err != nil {
		out += "\nerror: " + err.Error()
	}
	return out, exitCode(err)
}

func TestExitCodeSeparatesUsageFromFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"a real failure", errors.New("the disk is on fire"), 1},
		{"a usage mistake", usagef("roger edge list takes no arguments"), 2},
		{"a wrapped usage mistake", fmt.Errorf("while parsing: %w", usagef("bad flag")), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, exitCode(tc.err))
		})
	}
}

func TestParseEdgeArgv(t *testing.T) {
	list, _ := edgeLeaf("list")
	describe, _ := edgeLeaf("describe")
	name, _ := edgeLeaf("name")
	for _, tc := range []struct {
		name    string
		leaf    edgeSubcommand
		args    []string
		wantErr string
		check   func(*testing.T, edgeArgv)
	}{
		{name: "a bare leaf", leaf: list, args: nil,
			check: func(t *testing.T, a edgeArgv) { require.Empty(t, a.pos) }},
		{name: "a value flag, separated", leaf: list, args: []string{"--capability", "serve"},
			check: func(t *testing.T, a edgeArgv) { require.Equal(t, "serve", a.flags["capability"]) }},
		{name: "a value flag, joined", leaf: list, args: []string{"--capability=sense"},
			check: func(t *testing.T, a edgeArgv) { require.Equal(t, "sense", a.flags["capability"]) }},
		{name: "a single-dash flag", leaf: list, args: []string{"-json"},
			check: func(t *testing.T, a edgeArgv) { require.True(t, a.has("json")) }},
		{name: "a flag after the positional", leaf: describe, args: []string{"bench-pi", "--verbose"},
			check: func(t *testing.T, a edgeArgv) {
				require.Equal(t, []string{"bench-pi"}, a.pos)
				require.True(t, a.has("verbose"))
			}},
		{name: "an unknown flag is refused", leaf: list, args: []string{"--frobnicate"},
			wantErr: "unknown flag"},
		{name: "a value flag with no value", leaf: list, args: []string{"--capability"},
			wantErr: "needs a value"},
		{name: "a stray argument on a leaf that takes none", leaf: list, args: []string{"extra"},
			wantErr: "takes no arguments"},
		{name: "a second argument on a one-argument leaf", leaf: describe, args: []string{"a", "b"},
			wantErr: "and nothing else"},
		{name: "a missing argument", leaf: describe, args: nil, wantErr: "usage: roger edge describe"},
		{name: "a half-given pair", leaf: name, args: []string{"bench-pi"}, wantErr: "usage: roger edge name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEdgeArgv(tc.leaf, tc.args)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				require.Equal(t, 2, exitCode(err), "a caller's mistake exits 2")
				return
			}
			require.NoError(t, err)
			tc.check(t, got)
		})
	}
}

func TestEdgeBrokerAddr(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://broker.rogerai.fm", "broker.rogerai.fm:443"},
		{"http://broker.rogerai.fm", "broker.rogerai.fm:80"},
		{"http://127.0.0.1:8080", "127.0.0.1:8080"},
		{"", ""},
		{"://nonsense", ""},
	} {
		require.Equal(t, tc.want, edgeBrokerAddr(tc.in), tc.in)
	}
}

func TestEdgeAgeSaysSecondsWhereSecondsMatter(t *testing.T) {
	now := time.Now()
	require.Equal(t, "never", edgeAge(0))
	require.Equal(t, "5s ago", edgeAge(now.Add(-5*time.Second).Unix()))
	require.Equal(t, "5m ago", edgeAge(now.Add(-5*time.Minute).Unix()))
	require.Equal(t, "5h ago", edgeAge(now.Add(-5*time.Hour).Unix()))
	require.Equal(t, "5d ago", edgeAge(now.Add(-120*time.Hour).Unix()))
	require.Equal(t, "0s ago", edgeAge(now.Add(time.Hour).Unix()), "a clock skew is not a negative age")
}

func TestEdgeCapAndSeenLabels(t *testing.T) {
	require.Equal(t, "serve", edgeCapLabel(store.EdgeCap{Name: "serve", State: string(edge.Verified)}))
	require.Equal(t, "serve(claimed)", edgeCapLabel(store.EdgeCap{Name: "serve", State: string(edge.Claimed)}))
	require.Equal(t, "actuate(pending your confirmation)",
		edgeCapLabel(store.EdgeCap{Name: "actuate", State: string(edge.PendingConfirmation)}))
	require.Equal(t, "-", edgeCapsLabel(store.EdgeNode{}))
	require.Equal(t, "none declared", edgeDetailCaps(&edgeState{}, store.EdgeNode{}))
	require.Equal(t, "none known", edgeDetailTransports(store.EdgeNode{}))
	require.Contains(t, edgeSeenLabel(store.EdgeNode{
		Presence: string(edge.PresenceDark), LastSeen: time.Now().Add(-time.Hour).Unix()}), "dark")
	require.Equal(t, string(edge.PresenceVerified), edgePresence(store.EdgeNode{}))
	require.Equal(t, "n_abcdefgh", edgeShortID("n_abcdefghijklmnop"))
	require.Equal(t, "short", edgeShortID("short"))
	require.Equal(t, "aabbccddeeff...", edgeShortFP(strings.Repeat("aabbccddeeff", 5)))
	require.Equal(t, "short", edgeShortFP("short"))
}

func TestEdgeResolvePrefersAnExactNameAndRefusesAnAmbiguousPrefix(t *testing.T) {
	nodes := []store.EdgeNode{
		{ID: "n_aaaa1", Name: "bench-pi"},
		{ID: "n_aaaa2", Name: "cabinet"},
		{ID: "n_bbbb1", Name: "n_aaaa1"}, // a name that looks like another node's id
	}
	got, ok, err := edgeResolve(nodes, "n_aaaa1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "n_bbbb1", got.ID, "an exact NAME wins over an id prefix")

	got, ok, err = edgeResolve(nodes, "n_bbbb")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "n_bbbb1", got.ID)

	_, ok, err = edgeResolve(nodes, "n_aaaa")
	require.False(t, ok)
	require.Error(t, err)
	require.Contains(t, err.Error(), "matches more than one node")
	require.Contains(t, err.Error(), "bench-pi")
	require.Contains(t, err.Error(), "cabinet")
	require.Equal(t, 1, exitCode(err), "an ambiguous name is a failure, not a usage error")

	_, ok, err = edgeResolve(nodes, "nothing")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestLoadEdgeStateIgnoresACacheThatIsNotOurs(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	require.NoError(t, os.MkdirAll(filepath.Dir(edgeStatePath()), 0o700))

	// A cache belonging to another account is treated as no cache at all.
	b, err := json.Marshal(edgeSnapshot{Account: "somebody-else",
		Nodes: []store.EdgeNode{{ID: "n_theirs", Account: "somebody-else", Name: "their-box"}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(edgeStatePath(), b, 0o600))
	st, err := loadEdgeState()
	require.NoError(t, err)
	list, err := st.list()
	require.NoError(t, err)
	require.Empty(t, list)

	// So is a corrupt one: a half-written cache never crashes a fleet view.
	require.NoError(t, os.WriteFile(edgeStatePath(), []byte("{not json"), 0o600))
	st, err = loadEdgeState()
	require.NoError(t, err)
	list, err = st.list()
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestEdgeGroupHelpAndUnknownCommand(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")

	out, code := edgeRun(t, "edge", "--help")
	require.Equal(t, 0, code)
	for _, c := range edgeSubcommands {
		require.Contains(t, out, c.purpose)
	}

	out, code = edgeRun(t, "edge", "frobnicate")
	require.Equal(t, 2, code)
	require.Contains(t, out, "list | describe | name | forget | adopt | scan")

	out, code = edgeRun(t, "edge", "list", "--frobnicate")
	require.Equal(t, 2, code)
	require.Contains(t, out, "unknown flag")
}

func TestEdgeListRefusesACapabilityThatIsNotInTheVocabulary(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	out, code := edgeRun(t, "edge", "list", "--capability", "levitate")
	require.Equal(t, 2, code)
	require.Contains(t, out, "is not a capability")
	require.Contains(t, out, "serve")
}

func TestEdgeListSaysWhenAFilterMatchesNothing(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t, store.EdgeNode{ID: "n_a1", Name: "bench-pi", Kind: "board",
		Caps: []store.EdgeCap{{Name: "sense", State: string(edge.Verified)}}})
	out, code := edgeRun(t, "edge", "list", "--dark")
	require.Equal(t, 0, code, "an empty filter is a success, not a failure")
	require.Contains(t, out, "nothing on this Edge matches")
	require.NotContains(t, out, "bench-pi")
}

func TestEdgeForgetLeavesTheNodeAloneUnlessTheAnswerIsYes(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t, store.EdgeNode{ID: "n_a1", Name: "bench-pi", Kind: "board"})

	for _, answer := range []string{"", "n\n", "nope\n"} {
		edgeStdin = strings.NewReader(answer)
		out, code := edgeRun(t, "edge", "forget", "bench-pi")
		require.Equal(t, 0, code)
		require.Contains(t, out, "left alone")
		st, err := loadEdgeState()
		require.NoError(t, err)
		_, ok, err := edgeResolve(mustList(t, st), "bench-pi")
		require.NoError(t, err)
		require.True(t, ok, "answering %q must not remove the node", answer)
	}

	edgeStdin = strings.NewReader("y\n")
	out, code := edgeRun(t, "edge", "forget", "bench-pi")
	require.Equal(t, 0, code)
	require.Contains(t, out, "pin is cleared")
	st, err := loadEdgeState()
	require.NoError(t, err)
	require.Empty(t, mustList(t, st))
}

func mustList(t *testing.T, st *edgeState) []store.EdgeNode {
	t.Helper()
	list, err := st.list()
	require.NoError(t, err)
	return list
}

func TestEdgeAdoptRefusals(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")

	// Nothing was ever seen: the answer says how to look.
	out, code := edgeRun(t, "edge", "adopt", "n_nobody")
	require.Equal(t, 1, code)
	require.Contains(t, out, "roger edge scan")

	// A candidate with no LAN address cannot be checked, so it is not adopted.
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{ID: "n_addrless", Name: "n_addrless", Kind: "host", Pin: "aa"}}
	require.NoError(t, st.save())
	out, code = edgeRun(t, "edge", "adopt", "n_addrless")
	require.Equal(t, 1, code)
	require.Contains(t, out, "no LAN address")

	// A candidate whose address answers nothing: refused, and nothing is added.
	st, err = loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{ID: "n_gone", Name: "n_gone", Kind: "host", Pin: "aa",
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:1", Fingerprint: "aa"}}}}
	require.NoError(t, st.save())
	out, code = edgeRun(t, "edge", "adopt", "n_gone")
	require.Equal(t, 1, code)
	require.Contains(t, out, "could not reach")
	st, err = loadEdgeState()
	require.NoError(t, err)
	require.Empty(t, mustList(t, st))
}

func TestEdgeAdoptNeedsALoginBeforeItTakesAnythingIntoAnEdge(t *testing.T) {
	useTempConfig(t) // no auth.json: anonymous
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{ID: "n_seen", Name: "n_seen", Kind: "host", Pin: "aa",
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:1", Fingerprint: "aa"}}}}
	require.NoError(t, st.save())
	out, code := edgeRun(t, "edge", "adopt", "n_seen")
	require.Equal(t, 1, code)
	require.Contains(t, out, "roger login")
}

func TestEdgeDescribeAnUnreachableNodeStillAnswers(t *testing.T) {
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1") // a relay with no address of its own falls back here
	edgeWriteAuth(t, "owner")
	edgeDialTimeout = 300 * time.Millisecond
	edgeSeed(t, store.EdgeNode{ID: "n_a1", Name: "bench-pi", Kind: "board",
		Caps:       []store.EdgeCap{{Name: "classify", State: string(edge.Claimed)}},
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "127.0.0.1:1"}, {Kind: "relay"}},
		Presence:   string(edge.PresenceVerified), LastSeen: time.Now().Unix()})

	out, code := edgeRun(t, "edge", "describe", "bench-pi")
	require.Equal(t, 0, code)
	require.Contains(t, out, "classify (claimed: a canary sample with a known label)",
		"a claim says how it would be verified")
	require.NotContains(t, out, "carried", "nothing about transports without --verbose")

	out, code = edgeRun(t, "edge", "describe", "bench-pi", "--verbose")
	require.Equal(t, 0, code)
	require.Contains(t, out, "lan attempt to 127.0.0.1:1 failed")
	require.Contains(t, out, "relay attempt to 127.0.0.1:1 failed", "the relay falls back to the broker address")
	require.NotContains(t, out, "carried over")
	require.Contains(t, out, "nothing answered")
}

func TestEdgeNameRefusesANodeThatIsNotOnThisEdge(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	out, code := edgeRun(t, "edge", "name", "ghost", "bench-pi")
	require.Equal(t, 1, code)
	require.Contains(t, out, "no such node on this Edge")

	edgeSeed(t, store.EdgeNode{ID: "n_a1", Name: "bench-pi", Kind: "board"})
	out, code = edgeRun(t, "edge", "name", "bench-pi", "../etc/passwd")
	require.Equal(t, 1, code)
	require.Contains(t, out, "not a usable node name")
}

func TestEdgeScanSaysSoWhenTheNetworkHasNoDiscovery(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeDiscoveryOptions = func(f *edge.Fleet) edge.Options {
		o := defaultEdgeDiscoveryOptions(f)
		o.Plane = func() (edge.Transport, error) { return nil, edge.ErrNoLAN }
		return o
	}
	t.Cleanup(func() { edgeDiscoveryOptions = defaultEdgeDiscoveryOptions })
	out, code := edgeRun(t, "edge", "scan")
	require.Equal(t, 0, code, "a machine with no multicast is not a failed command")
	require.Contains(t, out, "discovery is unavailable")
	require.Contains(t, out, "nothing answered")
}

func TestEdgeMergeCandidatesKeepsWhatThisPassMissed(t *testing.T) {
	old := []store.EdgeNode{{ID: "n_a", Name: "old-a"}, {ID: "n_b", Name: "old-b"}}
	fresh := []store.EdgeNode{{ID: "n_b", Name: "fresh-b"}}
	got := edgeMergeCandidates(old, fresh)
	require.Len(t, got, 2)
	require.Equal(t, "n_a", got[0].ID)
	require.Equal(t, "fresh-b", got[1].Name, "a candidate seen again is refreshed, not duplicated")
}
