package edge_test

// features/edge/remote_model.feature §1: the wire carries a model id, and has nowhere to put a URL.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestTheWireCarriesAModelIDAndHasNowhereToPutAnAddress(t *testing.T) {
	good := map[string]edge.ModelTask{
		`{"action":"serve","model":"unsloth/Qwen3-4B-GGUF:Q4_K_M"}`:            {Action: edge.ModelServe, Model: "unsloth/Qwen3-4B-GGUF:Q4_K_M"},
		`{"action":"serve","model":"unsloth/Qwen3-4B-GGUF:Q4_K_M","ctx":4096}`: {Action: edge.ModelServe, Model: "unsloth/Qwen3-4B-GGUF:Q4_K_M", Ctx: 4096},
		`{"action":"serve","model":"mine-Q8_0"}`:                               {Action: edge.ModelServe, Model: "mine-Q8_0"},
		`{"action":"STATUS"}`:                                                  {Action: edge.ModelStatus},
		`{"action":"stop"}`:                                                    {Action: edge.ModelStop},
	}
	for raw, want := range good {
		got, refused := edge.ParseModelTask([]byte(raw))
		require.Empty(t, refused, raw)
		require.Equal(t, want, got, raw)
	}
	bad := map[string]string{
		`{"action":"serve","model":"x","url":"http://evil/x.gguf"}`:      "malformed",
		`{"action":"serve","model":"x","path":"/tmp/x.gguf"}`:            "malformed",
		`{"action":"serve","model":"x","runtime":"/bin/sh"}`:             "malformed",
		`{"action":"serve","model":"http://evil.example/x.gguf"}`:        "a model id",
		`{"action":"serve","model":"/home/x/model.gguf"}`:                "a model id",
		`{"action":"serve","model":"../../etc/passwd"}`:                  "a model id",
		`{"action":"serve","model":"C:\\models\\x.gguf"}`:                "a model id",
		`{"action":"serve","model":"x.gguf"}`:                            "a model id",
		`{"action":"serve"}`:                                             "names the model",
		`{"action":"serve","model":"x","ctx":-1}`:                        "context",
		`{"action":"exec","model":"x"}`:                                  "serve, status, stop",
		`{}`:                                                             "serve, status, stop",
		`not json`:                                                       "malformed",
		`{"action":"serve","model":"` + string(make([]byte, 300)) + `"}`: "a model id",
	}
	for raw, why := range bad {
		_, refused := edge.ParseModelTask([]byte(raw))
		require.Contains(t, refused, why, raw)
	}
}

func TestAnExactIndexIDIsToldApartFromEverythingElse(t *testing.T) {
	for id, want := range map[string]bool{
		"unsloth/Qwen3-4B-GGUF:Q4_K_M": true, "bartowski/x.y-z_1:IQ4_XS": true,
		"unsloth/Qwen3-4B-GGUF": false, "qwen3-4b": false, "Q4_K_M": false, "a/b/c:Q4": false, "a/b:": false,
	} {
		repo, quant, ok := edge.ExactIndexID(id)
		require.Equal(t, want, ok, id)
		if ok {
			require.NotEmpty(t, repo)
			require.NotEmpty(t, quant)
		}
	}
}

func TestServeControlIsACapabilityWithItsOwnSwitch(t *testing.T) {
	require.True(t, edge.ServeControl.Valid())
	require.Contains(t, edge.ServeControl.VerificationMethod(), "owner")
	require.Contains(t, edge.ServeControlOffReason, "roger edge serve-control on")
}

func TestAMembersModelIsWordedTheSameEverywhere(t *testing.T) {
	require.Equal(t, "serves nothing of its own", edge.ModelLine(nil))
	m := &edge.EdgeModel{Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M", Ctx: 8192,
		Fetch: &store.EdgeFetch{File: "Big-Q4_K_M.gguf", Done: 1 << 30, Total: 4 << 30, State: "fetching"}}
	require.Equal(t, `serving "tiny" (acme/Tiny-GGUF:Q4_K_M, 8192 context) · fetching Big-Q4_K_M.gguf: 1.0 GiB of 4.0 GiB`, edge.ModelLine(m),
		"a fetch never hides what is served NOW")
	require.Equal(t, "cannot serve here: needs glibc 2.38", edge.ModelLine(&edge.EdgeModel{CannotServe: "needs glibc 2.38"}))
	require.Contains(t, edge.ModelLine(&edge.EdgeModel{Fetch: &store.EdgeFetch{State: "interrupted"}}), "never served half-done")
	require.Contains(t, edge.ModelLine(&edge.EdgeModel{Fetch: &store.EdgeFetch{State: "failed", Err: "no space"}}), "failed: no space")
	require.Equal(t, "serving nothing", edge.ModelLine(&edge.EdgeModel{Have: []string{"x"}}))

	idle := &edge.EdgeModel{Have: []string{"x"}}
	serving := &edge.EdgeModel{Band: "tiny"}
	n := store.EdgeNode{Instances: []store.EdgeInstance{{Name: "a"}, {Name: "b", Model: idle}, {Name: "c", Model: serving}}}
	require.Same(t, serving, edge.ModelOf(n), "the roger that is serving answers for the machine")
	n.Instances = n.Instances[:2]
	require.Same(t, idle, edge.ModelOf(n))
	require.Nil(t, edge.ModelOf(store.EdgeNode{}))
}

// Review 2026-10-01: what a member says about its model is its word, stored by the authority and
// drawn on every surface - so it is bounded and cleaned on the way in, whatever the sender did.
func TestAMembersModelReportIsBoundedAndCleanedWhenStored(t *testing.T) {
	var have []string
	for i := 0; i < 500; i++ {
		have = append(have, fmt.Sprintf("acme/m-%d:Q4_K_M", i))
	}
	have = append([]string{"/etc/passwd", "http://evil/x.gguf", "ok/fine:Q8_0"}, have...)
	esc := "\x1b]0;owned\x07\x1b[31mred" + strings.Repeat("x", 500)
	m := edge.CleanModel(&store.EdgeModel{Band: esc, ID: esc, Quant: esc, CannotServe: esc, Have: have,
		Fetch: &store.EdgeFetch{Repo: esc, File: esc, Err: esc, State: "fetching\x1b[2J"}})
	for _, s := range []string{m.Band, m.ID, m.Quant, m.CannotServe, m.Fetch.Repo, m.Fetch.File, m.Fetch.Err, m.Fetch.State} {
		require.NotContains(t, s, "\x1b", "a control character reached the store")
		require.NotContains(t, s, "\x07")
		require.LessOrEqual(t, len(s), 200)
	}
	require.Len(t, m.Have, edge.MaxHave)
	require.Equal(t, "ok/fine:Q8_0", m.Have[0], "only real model ids survive; a path or a URL is dropped")
	require.Nil(t, edge.CleanModel(nil))
}

func TestTheFleetStoresAMembersModelCleaned(t *testing.T) {
	f := edge.NewFleet(store.NewMem(), "acct-1")
	n, err := f.Observe("n_jet", edge.Observation{Name: "jetson", Kind: "host", Addr: "1.2.3.4:5", Fingerprint: "ab",
		Instances: []edge.Instance{{Name: "jetson", Model: &store.EdgeModel{Band: "\x1b[31mevil", Have: []string{"/etc/passwd", "a/b:Q4"}}}}})
	require.NoError(t, err)
	require.Equal(t, "[31mevil", n.Instances[0].Model.Band)
	require.Equal(t, []string{"a/b:Q4"}, n.Instances[0].Model.Have)
}

// features/edge/fleet_window.feature §5: a band that is advertised but not proven is not shown as
// serving (review 2026-10-01: the window drew the member's own word).
func TestOnlyAProvenBandIsShownAsServing(t *testing.T) {
	m := &store.EdgeModel{Band: "qwen3-4b", ID: "unsloth/Qwen3-4B-GGUF:Q4_K_M", Quant: "Q4_K_M", Ctx: 8192}
	n := store.EdgeNode{Instances: []store.EdgeInstance{{Name: "jet", Bands: []string{"qwen3-4b"}, Model: m}}}
	line := edge.ModelLineFor(n)
	require.NotContains(t, line, `serving "qwen3-4b"`)
	require.Contains(t, line, `says it serves "qwen3-4b" - not proven yet`)
	require.Empty(t, edge.ServingShort(n), "the row shows nothing as served until it is proven")

	n.Instances[0].ServeProven = []string{"qwen3-4b"}
	require.Contains(t, edge.ModelLineFor(n), `serving "qwen3-4b" (unsloth/Qwen3-4B-GGUF:Q4_K_M, 8192 context)`)
	require.Equal(t, "qwen3-4b Q4_K_M · 8K", edge.ServingShort(n), "the row shows the band, the quantisation and the context")

	// A fetch still shows, and what is proven still leads.
	m.Fetch = &store.EdgeFetch{File: "Big.gguf", Done: 1, Total: 4, State: "fetching"}
	require.Equal(t, "qwen3-4b Q4_K_M · 8K · fetching 25%", edge.ServingShort(n))
	// Bands with no model report of its own (another runtime): only the proven ones.
	plain := store.EdgeNode{Instances: []store.EdgeInstance{{Name: "a", Bands: []string{"x", "y"}, ServeProven: []string{"y"}}}}
	require.Equal(t, "y", edge.ServingShort(plain))
	require.Equal(t, "cannot serve here", edge.ServingShort(store.EdgeNode{Instances: []store.EdgeInstance{{Model: &store.EdgeModel{CannotServe: "glibc"}}}}))
	require.Empty(t, edge.ModelLineFor(store.EdgeNode{}))
}
