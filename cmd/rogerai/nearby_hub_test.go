package main

// features/models/short_names_nearby.feature section 5, the hub fallback: a name served nowhere
// nearby is looked up on the model hub (the stand-in, never the real one), the owner picks from the
// top three that fit by a stated rule, and nothing is fetched on a guess.

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/provision"
)

func TestTheHubFallbackListsTheTopThreeThatFitByAStatedRule(t *testing.T) {
	r := newNearbyRig(t, 16384)
	out, err := r.do(t, "n\n", "serve", "qwen3-8b")
	require.NoError(t, err)
	require.Contains(t, out, "qwen3-8b is not served nearby; looking it up on the model hub, because you asked to serve it")
	require.Contains(t, out, "ranked by the model hub's download count, among GGUF repositories with a file that fits this machine; the top 3 are shown")
	require.Contains(t, out, "each is its author's word, not RogerAI's and not a nearby source's")
	require.Regexp(t, `1\) Qwen/Qwen3-8B-GGUF:Q4_K_M\s+4\.3 KiB\s+licence apache-2\.0\s+912\.3K downloads\s+fits`, out)
	require.Regexp(t, `2\) lookalike/Qwen3-8B-GGUF:Q4_K_M\s+4\.4 KiB\s+licence mit\s+120 downloads\s+fits`, out)
	require.Regexp(t, `3\) third/Qwen3-8B-Chat-GGUF:Q4_K_M\s+4\.5 KiB\s+licence apache-2\.0\s+50 downloads\s+fits`, out)
	require.NotContains(t, out, "4) ", "three at most")
	require.Contains(t, out, "1 more that fits: roger model search qwen3-8b shows every match")
	require.Regexp(t, `-\s+big/Qwen3-8B-Huge-GGUF\s+not offered: no file in it fits this machine`, out)
	require.Regexp(t, `-\s+proj/Qwen3-8B-Vision-GGUF\s+cannot be served: mmproj-Qwen3-8B-F16\.gguf is a vision projector`, out)
	require.Contains(t, out, "pick 1-3 to see it before anything is fetched, or n: ")
	require.Contains(t, out, "not fetched. Nothing changed.")
	for _, f := range []string{qwenQ4, "Qwen3-8B-Chat-Q4_K_M.gguf"} {
		require.False(t, r.hub.downloaded(f), "nothing is fetched until the owner picks")
	}
}

func TestTheOwnerPicksByNumberAndTheListingsDigestIsPinned(t *testing.T) {
	r := newNearbyRig(t, 16384)
	out, err := r.do(t, "2\ny\n", "get", "qwen3-8b")
	require.NoError(t, err)
	require.Contains(t, out, "lookalike/Qwen3-8B-GGUF  Qwen3-8B-Q4_K_M.gguf")
	require.Contains(t, out, "source: the model hub - its author's word, checked against the digest the listing gave")
	require.Contains(t, out, "fetched and verified: lookalike/Qwen3-8B-GGUF:Q4_K_M (sha256 "+r.hub.sha("lookalike/Qwen3-8B-GGUF", qwenQ4)[:12])
	require.False(t, r.hub.downloaded("Qwen3-8B-Chat-Q4_K_M.gguf"))
}

func TestAHubFileThatChangesBetweenListingAndFetchIsNotServed(t *testing.T) {
	r := newNearbyRig(t, 16384)
	edgeStdin = &changingStdin{answers: "1\ny\n", change: func() {
		f := r.hub.repo("Qwen/Qwen3-8B-GGUF").files[qwenQ4]
		b := append([]byte(nil), f.body...)
		b[len(b)-1] ^= 0xff // other bytes under the same name, the same size
		f.body = b
	}}
	// The listing is read before the pick: pin what it said then.
	listed := r.hub.sha("Qwen/Qwen3-8B-GGUF", qwenQ4)
	r.hub.repo("Qwen/Qwen3-8B-GGUF").files[qwenQ4].listSHA = listed
	r.out.Reset()
	err := cmdModel(r.cfg(), []string{"serve", "qwen3-8b"})
	require.ErrorIs(t, err, provision.ErrCorrupt)
	require.ErrorContains(t, err, "expected "+listed[:12])
	require.NotContains(t, r.out.String(), "on the air")
	all, _ := r.home.List()
	require.Empty(t, all)
}

func TestASingleMatchIsStillConfirmed(t *testing.T) {
	r := newNearbyRig(t, 16384)
	out, err := r.do(t, "n\n", "serve", "qwen3-8b-mini")
	require.NoError(t, err)
	require.Contains(t, out, "1) fourth/Qwen3-8B-Mini-GGUF:Q4_K_M")
	require.NotContains(t, out, "pick 1-")
	require.Contains(t, out, "licence: apache-2.0")
	require.Contains(t, out, "fits at 8K context")
	require.Contains(t, out, "fetch it? [y/N]: not fetched. Nothing changed.")
	require.False(t, r.hub.downloaded("Qwen3-8B-Mini-Q4_K_M.gguf"))
}

func TestANonInteractiveRunNeverPicksFromTheHub(t *testing.T) {
	r := newNearbyRig(t, 16384)
	edgeIsInteractive = func() bool { return false }
	for _, args := range [][]string{{"serve", "qwen3-8b"}, {"serve", "qwen3-8b", "--yes"}} {
		out, err := r.do(t, "y\n", args...)
		require.ErrorContains(t, err, "no terminal to pick on, so nothing was fetched; name one exactly: roger model serve <owner/repo>:<quant>", "%v", args)
		require.Contains(t, out, "Qwen/Qwen3-8B-GGUF:Q4_K_M")
		require.Contains(t, out, "lookalike/Qwen3-8B-GGUF:Q4_K_M")
	}
	require.False(t, r.hub.downloaded(qwenQ4))
}

func TestNoMatchOnTheHubIsSaidPlainly(t *testing.T) {
	r := newNearbyRig(t, 16384)
	_, err := r.do(t, "", "serve", "zzz-no-such-model")
	require.ErrorContains(t, err, `nothing nearby and nothing on the model hub matches "zzz-no-such-model" - try roger model search <other words>`)
}

func TestWithNoInternetTheFallbackSaysSoAndListsWhatIsNearby(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "gpt-oss-20b", &f, true)
	modelHubBase = "http://127.0.0.1:1" // nothing listens
	_, err := r.do(t, "", "serve", "qwen3-8b")
	require.ErrorContains(t, err, "qwen3-8b is not served nearby, and the model hub cannot be reached")
	require.ErrorContains(t, err, "served nearby instead: gpt-oss-20b")

	// A nearby name on a LAN with no internet: listed, and serving it says where the file must come from.
	out, err := r.do(t, "", "nearby")
	require.NoError(t, err)
	require.Contains(t, out, "gpt-oss-20b")
	_, err = r.do(t, "y\n", "serve", "gpt-oss-20b")
	require.ErrorContains(t, err, "the file must come from the model hub")
	require.ErrorContains(t, err, "cannot be reached")
}

func TestAHugeDownloadStatesTheFreeSpaceAndOneThatCannotFitIsRefusedFirst(t *testing.T) {
	r := newNearbyRig(t, 16384)
	modelFetcher = func() *provision.Fetcher {
		return &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 2048, true }, OnProgress: modelProgress}
	}
	out, err := r.do(t, "y\n", "get", "qwen3-8b-mini")
	require.Error(t, err)
	require.ErrorIs(t, err, provision.ErrNoSpace)
	require.Regexp(t, regexp.MustCompile(`(?s)free on this disk: 2\.0 KiB.*fetch it\? \[y/N\]`), out, "the free space is stated before asking")
	require.False(t, r.hub.downloaded("Qwen3-8B-Mini-Q4_K_M.gguf"), "refused before any byte moved")

	// A real fetch measures the disk it writes to.
	modelFetcher = defaultModelFetcher
	free, ok := modelFetcher().FreeSpace(t.TempDir())
	require.True(t, ok)
	require.Positive(t, free)
}

func TestTypingANameIsTheOnlyThingThatSendsItToTheHub(t *testing.T) {
	r := newNearbyRig(t, 16384)
	_, err := r.do(t, "", "nearby")
	require.NoError(t, err)
	_, err = r.do(t, "", "list")
	require.NoError(t, err)
	require.Empty(t, r.hub.searched(), "listing what is nearby or on disk never asks the hub")
	_, err = r.do(t, "n\n", "serve", "qwen3-8b")
	require.NoError(t, err)
	require.True(t, r.hub.searchedFor("qwen3-8b"))
}
