package main

// features/models/short_names_nearby.feature section 6: `roger model serve` with no name suggests
// what is served nearby that fits this machine, then a few hub models that fit by a stated rule,
// and never fetches without a pick at a terminal.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/servedfile"
)

func TestWithNoNameRogerSuggestsWhatIsNearbyThatFits(t *testing.T) {
	r := newNearbyRig(t, 16384)
	fits := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	huge := r.hubFileOf("big/Qwen3-8B-Huge-GGUF", "Qwen3-8B-Huge-Q8_0.gguf", "Q8_0")
	r.memberBands(t, "atlas", true, []string{"qwen3-8b", "gpt-oss-20b"}, []*servedfile.File{&fits, &huge})

	out, err := r.do(t, "n\n", "serve")
	require.NoError(t, err)
	require.Contains(t, out, "served nearby, and fits this machine:")
	require.Regexp(t, `1\) qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas\s+4\.3 KiB\s+fits`, out)
	require.Regexp(t, `-\s+gpt-oss-20b = big/Qwen3-8B-Huge-GGUF:Q8_0 as served by your Edge member atlas\s+900\.0 GiB\s+would not fit here: will not fit`, out)
	require.NotContains(t, out, "2) ")
	require.Contains(t, out, "pick 1 to serve it, or n: ")
	require.Contains(t, out, "not fetched. Nothing changed.")
	require.False(t, r.hub.searchedFor(""), "something nearby fits, so the hub is not browsed")

	out, err = r.do(t, "1\ny\n", "serve")
	require.NoError(t, err)
	require.Contains(t, out, "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; fetch 4.3 KiB? [y/N]")
	require.Contains(t, out, `on the air locally: "qwen3-8b"`)
}

func TestWithNothingNearbyRogerSuggestsAFewHubModelsThatFitByAStatedRule(t *testing.T) {
	r := newNearbyRig(t, 16384)
	// More downloaded than any above, but not chat, or only a tight fit: neither is suggested.
	r.hub.add(&hubRepo{id: "rank/Qwen3-8B-Reranker-GGUF", downloads: 990000, license: "apache-2.0", pipeline: "text-ranking",
		files: map[string]*hubFile{"Qwen3-8B-Reranker-Q4_K_M.gguf": {body: modelGGUF(4000)}}})
	r.hub.add(&hubRepo{id: "tight/Qwen3-8B-Tight-GGUF", downloads: 980000, license: "apache-2.0", pipeline: "text-generation",
		files: map[string]*hubFile{"Qwen3-8B-Tight-Q8_0.gguf": {body: modelGGUF(4000), listSize: 25 << 29}}})
	out, err := r.do(t, "n\n", "serve")
	require.NoError(t, err)
	require.Contains(t, out, "nothing is served nearby")
	require.Contains(t, out, "the most-downloaded chat GGUFs on the model hub with a file that fits this machine comfortably; up to 3")
	require.Contains(t, out, "each is its author's word, not RogerAI's and not a nearby source's")
	require.Regexp(t, `1\) Qwen/Qwen3-8B-GGUF:Q4_K_M\s+4\.3 KiB\s+licence apache-2\.0\s+912\.3K downloads\s+fits`, out)
	require.Regexp(t, `2\) lookalike/Qwen3-8B-GGUF:Q4_K_M`, out)
	require.Regexp(t, `3\) third/Qwen3-8B-Chat-GGUF:Q4_K_M`, out)
	require.NotContains(t, out, "Vision", "not a chat model")
	require.NotContains(t, out, "Huge", "nothing in it fits")
	require.NotContains(t, out, "Reranker", "not a chat model")
	require.NotContains(t, out, "Tight", "it fits, but not comfortably")
	require.Contains(t, out, "pick 1-3 to see it before anything is fetched, or n: ")
	require.False(t, r.hub.downloaded(qwenQ4))

	out, err = r.do(t, "3\ny\n", "serve")
	require.NoError(t, err)
	require.Contains(t, out, "fetched and verified: third/Qwen3-8B-Chat-GGUF:Q4_K_M")
	require.Contains(t, out, "on the air locally")
}

func TestWithNothingNearbyAndNoInternetRogerSuggestsNothing(t *testing.T) {
	r := newNearbyRig(t, 16384)
	modelHubBase = "http://127.0.0.1:1"
	out, err := r.do(t, "1\ny\n", "serve")
	require.ErrorContains(t, err, "nothing is served nearby, and the model hub cannot be reached now")
	require.ErrorContains(t, err, "so roger suggests nothing")
	require.NotContains(t, out, "1) ")
}

func TestAMachineWhoseMemoryCannotBeReadGetsNoGuessedSuggestion(t *testing.T) {
	r := newNearbyRig(t, 16384)
	modelHW = func() detect.LocalHW { return detect.LocalHW{} }
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	out, err := r.do(t, "1\ny\n", "serve")
	require.NoError(t, err)
	require.Contains(t, out, "roger cannot judge what fits this machine: ")
	require.Regexp(t, `-\s+qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas\s+4\.3 KiB\s+needs `, out)
	require.Contains(t, out, "roger model serve <name> serves one on your word")
	require.NotContains(t, out, "1) ")
	require.False(t, r.hub.downloaded(qwenQ4))
}

func TestANonInteractiveRunWithNoNameNeverFetches(t *testing.T) {
	r := newNearbyRig(t, 16384)
	edgeIsInteractive = func() bool { return false }
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	for _, args := range [][]string{{"serve"}, {"serve", "--yes"}} {
		out, err := r.do(t, "1\ny\n", args...)
		require.NoError(t, err, "%v", args)
		require.Contains(t, out, "1) qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M")
		require.Contains(t, out, "no terminal, so nothing is fetched: roger model serve <name> serves one")
	}
	r2 := newNearbyRig(t, 16384)
	edgeIsInteractive = func() bool { return false }
	out, err := r2.do(t, "1\ny\n", "serve", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, "1) Qwen/Qwen3-8B-GGUF:Q4_K_M")
	require.Contains(t, out, "no terminal, so nothing is fetched")
	require.False(t, r.hub.downloaded(qwenQ4))
	require.False(t, r2.hub.downloaded(qwenQ4))
}
