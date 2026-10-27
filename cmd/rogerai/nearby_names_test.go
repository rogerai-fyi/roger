package main

// features/models/short_names_nearby.feature sections 1 to 3: what counts as nearby and whose word
// each mapping is, serving a name served nearby, and conflicts that are never guessed. Real Edge
// fleet records, a real pinned Tower, the stand-in hub.

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/servedfile"
)

const qwenQ4 = "Qwen3-8B-Q4_K_M.gguf"

func TestNearbyNamesEachSourceAndWhoseWordItIs(t *testing.T) {
	r := newNearbyRig(t, 16384)
	_, err := r.do(t, "", "get", "Qwen/Qwen3-8B-GGUF:Q8_0", "--yes")
	require.NoError(t, err)
	atlas := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "gpt-oss-20b", &atlas, true)
	st, addr := r.pinnedTower(t)
	lab := r.hubFileOf("third/Qwen3-8B-Chat-GGUF", "Qwen3-8B-Chat-Q4_K_M.gguf", "Q4_K_M")
	lab.Bytes = 4321
	station(t, st, "st-1", "smollm2-135m-instruct", &lab)
	r.market = []client.Offer{{Model: "llama-3.1-8b", Online: true,
		File: &servedfile.File{Repo: "evil/Llama-GGUF", File: "x.gguf", SHA256: strings.Repeat("ab", 32)}, FileBy: "station"}}

	var out string
	modelUntilDone = func(context.Context) {
		r.out.Reset()
		edgeStdin = strings.NewReader("")
		require.NoError(t, cmdModel(r.cfg(), []string{"nearby"}))
		out = r.out.String()
	}
	_, err = r.do(t, "", "serve", "qwen3-8b")
	require.NoError(t, err)

	line := func(name string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), name+" ") {
				return l
			}
		}
		t.Fatalf("no line for %s in:\n%s", name, out)
		return ""
	}
	require.Contains(t, line("qwen3-8b"), "Qwen/Qwen3-8B-GGUF:Q8_0")
	require.Contains(t, line("qwen3-8b"), "this machine's own serve")
	require.Contains(t, line("qwen3-8b"), "8.2 KiB", "the size its source says")
	require.Contains(t, line("gpt-oss-20b"), "Qwen/Qwen3-8B-GGUF:Q4_K_M")
	require.Contains(t, line("gpt-oss-20b"), "your Edge member atlas")
	require.Contains(t, line("smollm2-135m-instruct"), "third/Qwen3-8B-Chat-GGUF:Q4_K_M")
	require.Contains(t, line("smollm2-135m-instruct"), "a station on the Tower "+nearbyHost(addr)+" (its word)")
	require.Contains(t, line("smollm2-135m-instruct"), "4.2 KiB")
	require.Contains(t, line("llama-3.1-8b"), "on the market")
	require.NotContains(t, out, "evil/Llama-GGUF", "a market station's claim is never shown as a file")
	require.Greater(t, strings.Index(out, "llama-3.1-8b"), strings.Index(out, "smollm2-135m-instruct"),
		"what is served nearby comes first; the market's names follow")
}

func TestNothingNearbySaysSoAndHowToFindAModel(t *testing.T) {
	r := newNearbyRig(t, 16384)
	out, err := r.do(t, "", "nearby")
	require.NoError(t, err)
	require.Contains(t, out, "nothing is served nearby")
	require.Contains(t, out, "roger model search <words>")
	require.Contains(t, out, "roger model serve <name>")
}

func TestAMarketNameIsLookedUpOnTheHubAndItsClaimIsIgnored(t *testing.T) {
	r := newNearbyRig(t, 16384)
	r.market = []client.Offer{{Model: "qwen3-8b", Online: true,
		File: &servedfile.File{Repo: "evil/Qwen3-8B-GGUF", File: "Qwen3-8B-Q4_K_M.gguf", Quant: "Q4_K_M", SHA256: strings.Repeat("ab", 32)}, FileBy: "station"}}
	out, err := r.do(t, "n\n", "serve", "qwen3-8b")
	require.NoError(t, err)
	require.Contains(t, out, "qwen3-8b is on the market, but a market station never chooses a file")
	require.Contains(t, out, "looking it up on the model hub")
	require.NotContains(t, out, "evil/")
	require.NotContains(t, out, "as served by")
}

func TestASpoofedOrUnpinnedSourceDefinesNoMapping(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "ghost", "qwen3-8b", &f, false) // claims the band; the serving probe never proved it

	st, addr := r.pinnedTower(t)
	station(t, st, "st-1", "pinned-changed", &f)
	other, _ := joinTowerFixture(t)
	wrong, err := other.JoinFingerprint()
	require.NoError(t, err)
	c := loadConfig()
	c.LanTowerPins[addr] = wrong // the Tower now answers with a key that does not match its pin
	require.NoError(t, saveConfig(c))
	lanmode.SetPins(c.LanTowerPins)

	plain := newRealTower(t) // a Tower added without a pin: plain http, on the LAN's word
	require.NoError(t, plain.admit(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey)))
	require.NoError(t, plain.attach("st-2", "unpinned-band"))
	_, err = plain.st.SetStationFiles("st-2", map[string]servedfile.File{"unpinned-band": f})
	require.NoError(t, err)
	c = loadConfig()
	c.LanTowers = append(c.LanTowers, plain.addr())
	require.NoError(t, saveConfig(c))

	// Not vacuous: the unpinned Tower answers and the dial shows its band, unverified.
	var seen bool
	for _, b := range lanDial(r.cfg()).Bands {
		seen = seen || b.Model == "unpinned-band"
	}
	require.True(t, seen, "the unpinned Tower is reachable")

	out, err := r.do(t, "", "nearby")
	require.NoError(t, err)
	require.NotContains(t, out, "qwen3-8b", "a band a member only claims is not served nearby")
	require.NotContains(t, out, "pinned-changed", "a Tower whose key no longer matches its pin is not nearby")
	require.NotContains(t, out, "unpinned-band", "an unpinned Tower is not a source of names")

	out, err = r.do(t, "n\n", "serve", "qwen3-8b")
	require.NoError(t, err)
	require.NotContains(t, out, "as served by")
	require.Contains(t, out, "looking it up on the model hub")
}

func TestAnOlderSourceThatNamesNoFileIsSaidAndTheHubIsAsked(t *testing.T) {
	r := newNearbyRig(t, 16384)
	r.member(t, "old", "qwen3-8b", nil, true)
	out, err := r.do(t, "n\n", "serve", "qwen3-8b")
	require.NoError(t, err)
	require.Contains(t, out, "qwen3-8b is served by your Edge member old, which does not say which file it is")
	require.Contains(t, out, "looking it up on the model hub")
}

func TestTheFileIsCheckedAgainstTheDigestItsSourceReported(t *testing.T) {
	t.Run("the hub lists other bytes than the source reported: never fetched", func(t *testing.T) {
		r := newNearbyRig(t, 16384)
		f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
		f.SHA256 = strings.Repeat("0f", 32)
		r.member(t, "atlas", "qwen3-8b", &f, true)
		_, err := r.do(t, "y\n", "serve", "qwen3-8b")
		require.ErrorContains(t, err, "your Edge member atlas reported sha256 "+f.SHA256)
		require.ErrorContains(t, err, "the model hub lists "+r.hub.sha("Qwen/Qwen3-8B-GGUF", qwenQ4))
		require.ErrorContains(t, err, "never served")
		require.False(t, r.hub.downloaded(qwenQ4))
	})
	t.Run("the downloaded bytes are not the ones reported: not served", func(t *testing.T) {
		r := newNearbyRig(t, 16384)
		f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
		r.member(t, "atlas", "qwen3-8b", &f, true)
		hf := r.hub.repo("Qwen/Qwen3-8B-GGUF").files[qwenQ4]
		hf.listSHA, hf.body = f.SHA256, modelGGUF(4001) // the repository now serves other bytes under that name
		hf.listSize = int64(len(hf.body))
		out, err := r.do(t, "y\n", "serve", "qwen3-8b")
		require.Error(t, err)
		require.ErrorContains(t, err, "as your Edge member atlas reported")
		require.ErrorContains(t, err, f.SHA256[:12])
		require.NotContains(t, out, "on the air")
		all, _ := r.home.List()
		require.Empty(t, all, "nothing was kept")
	})
}

func TestTheSourcesQuantUnlessItDoesNotFitHere(t *testing.T) {
	r := newNearbyRig(t, 16384)
	big := r.hub.repo("Qwen/Qwen3-8B-GGUF").files["Qwen3-8B-Q8_0.gguf"]
	big.listSize = 900 << 30
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", "Qwen3-8B-Q8_0.gguf", "Q8_0")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	out, err := r.do(t, "n\n", "serve", "qwen3-8b")
	require.NoError(t, err)
	require.Contains(t, out, "your Edge member atlas's Q8_0 will not fit here: ")
	require.Contains(t, out, "offering Q4_K_M of the same repository: that file is not the one your Edge member atlas serves, so its digest comes from the model hub's listing")
	require.Contains(t, out, "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M from the model hub's listing; fetch 4.3 KiB? [y/N]")
}

func TestAQuantCanBeNamedAfterTheShortName(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", "Qwen3-8B-Q8_0.gguf", "Q8_0")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	out, err := r.do(t, "y\n", "get", "qwen3-8b:Q4_K_M")
	require.NoError(t, err)
	require.Contains(t, out, "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M from the model hub's listing")
	require.Contains(t, out, "fetched and verified: Qwen/Qwen3-8B-GGUF:Q4_K_M")

	_, err = r.do(t, "y\n", "get", "qwen3-8b:Q2_K")
	require.ErrorContains(t, err, `no servable file "Q2_K" here`)
	require.ErrorContains(t, err, "Qwen3-8B-Q8_0.gguf")
}

func TestAFileAlreadyOnDiskUnderTheSameDigestIsServedWithoutFetching(t *testing.T) {
	r := newNearbyRig(t, 16384)
	_, err := r.do(t, "", "get", "Qwen/Qwen3-8B-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "house-qwen", &f, true)
	r.hub.fetched = nil
	out, err := r.do(t, "", "serve", "house-qwen")
	require.NoError(t, err)
	require.Contains(t, out, "house-qwen = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; it is already on this disk under the same digest, so nothing is fetched")
	require.Contains(t, out, "on the air locally")
	require.False(t, r.hub.downloaded(qwenQ4))
}

func TestAModelPlacedByHandKeepsItsName(t *testing.T) {
	r := newNearbyRig(t, 16384)
	require.NoError(t, os.MkdirAll(r.home.Models(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(r.home.Models(), "qwen3-8b.gguf"), modelGGUF(4000), 0o644))
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	out, err := r.do(t, "", "serve", "qwen3-8b")
	require.NoError(t, err)
	require.Contains(t, out, `on the air locally: "qwen3-8b" - qwen3-8b`)
	require.Contains(t, out, `a nearby source also serves a "qwen3-8b": Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas. roger model serve Qwen/Qwen3-8B-GGUF:Q4_K_M serves that one instead`)
}

func TestEveryCommandThatTakesAModelAcceptsANearbyName(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)

	out, err := r.do(t, "", "files", "qwen3-8b")
	require.NoError(t, err)
	require.Contains(t, out, "qwen3-8b is Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas")
	require.Contains(t, out, "* Q4_K_M")

	out, err = r.do(t, "y\n", "get", "qwen3-8b")
	require.NoError(t, err)
	require.Contains(t, out, "fetched and verified: Qwen/Qwen3-8B-GGUF:Q4_K_M")

	out, err = r.do(t, "", "serve", "qwen3-8b", "--add")
	require.NoError(t, err)
	require.Contains(t, out, `on the air locally: "qwen3-8b"`)

	var sent edge.ModelTask
	old := modelEdgeInvoke
	t.Cleanup(func() { modelEdgeInvoke = old })
	modelEdgeInvoke = func(_ config, node string, task edge.ModelTask) (edge.ModelResult, string, error) {
		sent = task
		return edge.ModelResult{Says: "starting"}, node, nil
	}
	out, err = r.do(t, "y\n", "serve", "qwen3-8b", "--on", "pi-1")
	require.NoError(t, err)
	require.Contains(t, out, "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; tell pi-1 to serve exactly that file? [y/N]")
	require.Equal(t, "Qwen/Qwen3-8B-GGUF:Q4_K_M", sent.Model, "the member is sent the exact reference, never the name")
	require.Equal(t, f.SHA256, sent.SHA256)

	sent = edge.ModelTask{}
	_, err = r.do(t, "n\n", "serve", "qwen3-8b", "--on", "pi-1")
	require.NoError(t, err)
	require.Empty(t, sent.Model, "nothing is sent without a yes")
}

func TestTwoSourcesThatDisagreeAreBothShownAndNeverGuessed(t *testing.T) {
	setup := func(t *testing.T) (*nearbyRig, servedfile.File, servedfile.File) {
		r := newNearbyRig(t, 16384)
		a := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
		b := r.hubFileOf("lookalike/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
		r.member(t, "atlas", "qwen3-8b", &a, true)
		st, _ := r.pinnedTower(t)
		station(t, st, "st-1", "qwen3-8b", &b)
		return r, a, b
	}
	t.Run("the owner picks one by number", func(t *testing.T) {
		r, _, _ := setup(t)
		out, err := r.do(t, "2\ny\n", "get", "qwen3-8b")
		require.NoError(t, err)
		require.Contains(t, out, "qwen3-8b is served nearby as 2 different files:")
		require.Regexp(t, `1\) Qwen/Qwen3-8B-GGUF:Q4_K_M\s+4\.3 KiB\s+fits[^\n]*as served by your Edge member atlas`, out)
		require.Regexp(t, `2\) lookalike/Qwen3-8B-GGUF:Q4_K_M\s+4\.4 KiB\s+fits[^\n]*as served by a station on the Tower`, out)
		require.Contains(t, out, "fetched and verified: lookalike/Qwen3-8B-GGUF:Q4_K_M")
	})
	t.Run("no pick, nothing fetched", func(t *testing.T) {
		r, _, _ := setup(t)
		out, err := r.do(t, "n\n", "get", "qwen3-8b")
		require.NoError(t, err)
		require.Contains(t, out, "not fetched. Nothing changed.")
		require.False(t, r.hub.downloaded(qwenQ4))
	})
	t.Run("no terminal: refused, never resolved", func(t *testing.T) {
		r, _, _ := setup(t)
		edgeIsInteractive = func() bool { return false }
		out, err := r.do(t, "", "serve", "qwen3-8b", "--yes")
		require.ErrorContains(t, err, "qwen3-8b is served nearby as 2 different files and there is no terminal to choose on")
		require.ErrorContains(t, err, "roger model serve <owner/repo>:<quant>")
		require.Contains(t, out, "Qwen/Qwen3-8B-GGUF:Q4_K_M")
		require.Contains(t, out, "lookalike/Qwen3-8B-GGUF:Q4_K_M")
		require.False(t, r.hub.downloaded(qwenQ4))
	})
}

func TestTwoSourcesThatAgreeAreOneChoice(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	r.member(t, "pi-1", "qwen3-8b", &f, true)
	out, err := r.do(t, "n\n", "serve", "qwen3-8b")
	require.NoError(t, err)
	require.Contains(t, out, "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas and your Edge member pi-1; fetch")
	require.NotContains(t, out, "different files")
}

func TestAMappingThatChangesBeforeTheFetchIsAskedAgain(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	g := r.hubFileOf("lookalike/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	edgeStdinBefore := &changingStdin{answers: "y\nn\n", change: func() { r.member(t, "atlas", "qwen3-8b", &g, true) }}
	edgeStdin = edgeStdinBefore
	r.out.Reset()
	err := cmdModel(r.cfg(), []string{"get", "qwen3-8b"})
	require.NoError(t, err)
	out := r.out.String()
	require.Contains(t, out, "the file qwen3-8b maps to changed since you said yes: now lookalike/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; asking again")
	require.Contains(t, out, "qwen3-8b = lookalike/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; fetch")
	require.Contains(t, out, "not fetched. Nothing changed.")
	require.False(t, r.hub.downloaded(qwenQ4), "the new file is never fetched under the old yes")
}
