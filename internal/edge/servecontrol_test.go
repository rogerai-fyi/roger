package edge_test

// features/edge/remote_model.feature §1: the wire carries a model id, and has nowhere to put a URL.

import (
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
