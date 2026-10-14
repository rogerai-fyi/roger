package main

// quiet_output.feature on the paths its runner does not walk: a name served nearby and a pick from
// the model hub say whose word the file is in the default line too, and a fetch into a pipe writes
// each archive's progress once.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/provision"
)

// quietDo is nearbyRig.do with the default output, not the full account.
func quietDo(t *testing.T, r *nearbyRig, in string, args ...string) string {
	t.Helper()
	edgeStdin = strings.NewReader(in)
	r.out.Reset()
	require.NoError(t, cmdModel(r.cfg(), args))
	return r.out.String()
}

func TestQuietANearbyNameSaysWhoseWordAndWhoseDigestInItsFirstLine(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	out := quietDo(t, r, "n\n", "get", "qwen3-8b")
	require.Contains(t, out, "Qwen/Qwen3-8B-GGUF:Q4_K_M · 4.3 KiB · licence apache-2.0 "+
		"(the author's word, from the model hub; checked against the digest your Edge member atlas reported)")
	require.Contains(t, out, "fits at 8K context")
	require.NotContains(t, out, "licence: apache-2.0", "the full account waits for --verbose")
	require.Contains(t, out, "not fetched. Nothing changed.")
}

func TestQuietAHubPickSaysItIsTheAuthorsWord(t *testing.T) {
	r := newNearbyRig(t, 16384)
	out := quietDo(t, r, "n\n", "serve", "qwen3-8b-mini")
	require.Contains(t, out, "fourth/Qwen3-8B-Mini-GGUF:Q4_K_M · 4.6 KiB · licence apache-2.0 (the author's word, from the model hub)")
	require.Contains(t, out, "fetch it? [y/N]: not fetched. Nothing changed.")
}

func TestQuietProgressIntoAPipeIsOneLinePerArchive(t *testing.T) {
	var buf bytes.Buffer
	old, oldTerm := modelOut, modelOutTerminal
	modelOut, modelOutTerminal = &buf, func() bool { return false }
	t.Cleanup(func() { modelOut, modelOutTerminal = old, oldTerm })

	modelProgressDone() // nothing shown, nothing said
	modelProgress(provision.Progress{Done: 1 << 20, Total: 2 << 20, Rate: 1 << 20, Remaining: time.Second})
	modelProgress(provision.Progress{Done: 2 << 20, Total: 2 << 20, Rate: 2 << 20})
	modelProgress(provision.Progress{Done: 3 << 20, Total: 8 << 20, Resumed: true}) // the next archive
	modelProgress(provision.Progress{Done: 8 << 20, Total: 8 << 20, Rate: 4 << 20, Resumed: true})
	modelProgressDone()

	require.NotContains(t, buf.String(), "\r", "a pipe gets no redraws")
	ls := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	require.Len(t, ls, 2, "one line per archive: %q", buf.String())
	require.Equal(t, "  2.0 MiB of 2.0 MiB  ·  2.0 MiB/s  ·  took 0s", ls[0])
	require.Equal(t, "  8.0 MiB of 8.0 MiB  ·  4.0 MiB/s  ·  took 0s  ·  resumed", ls[1])
}
