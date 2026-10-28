package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func constrainOpts() ProxyOptions {
	return ProxyOptions{
		LANMode:      true,
		LANSelf:      func(b string) (string, string) { return "http://127.0.0.1:9/v1/chat/completions", "" },
		LANSelfQuant: func(string) string { return "Q4_K_M" },
		LANSelfName:  "n_self",
		EdgePeers: func(string) []EdgePeer {
			return []EdgePeer{{Node: "atlas", Instance: "serve", Addr: "10.0.0.2:1", Quant: "Q4_K_M"},
				{Node: "zephyr", Instance: "serve", Addr: "10.0.0.3:1", Quant: "Q8_0"}}
		},
		LANTowers: func(string) []string { return []string{"192.168.1.20:8787", "192.168.1.21:8787"} },
	}
}

func TestLANConstrainNoConstraintsLeavesTheLadder(t *testing.T) {
	o, why := lanConstrain(constrainOpts(), "m", []byte(`{"model":"m"}`))
	require.Empty(t, why)
	require.Len(t, o.EdgePeers("m"), 2)
	require.False(t, o.lanOrdered)
}

func TestLANConstrainQuantizations(t *testing.T) {
	o, why := lanConstrain(constrainOpts(), "m", []byte(`{"provider":{"quantizations":["Q8_0"]}}`))
	require.Empty(t, why)
	u, _ := o.LANSelf("m")
	require.Empty(t, u, "this machine's Q4_K_M serve is out")
	peers := o.EdgePeers("m")
	require.Len(t, peers, 1)
	require.Equal(t, "zephyr", peers[0].Node, "the labelled Q8_0 member is admitted")
	require.Empty(t, o.LANTowers("m"), "a Tower lists no label, so it needs \"unknown\"")

	o, _ = lanConstrain(constrainOpts(), "m", []byte(`{"provider":{"quantizations":["Q8_0","unknown"]}}`))
	require.Len(t, o.LANTowers("m"), 2, "\"unknown\" admits the unlabelled Towers")

	_, why = lanConstrain(constrainOpts(), "m", []byte(`{"provider":{"quantizations":["BF16"]}}`))
	require.Contains(t, why, "provider.quantizations [bf16]")
	require.Contains(t, why, "this machine: quantized Q4_K_M")
	require.Contains(t, why, "192.168.1.20:8787: no quant label")
}

func TestLANConstrainOrder(t *testing.T) {
	// order reorders inside a rung, never across rungs
	o, why := lanConstrain(constrainOpts(), "m", []byte(`{"provider":{"order":["192.168.1.21:8787","zephyr"]}}`))
	require.Empty(t, why)
	require.Equal(t, "zephyr", o.EdgePeers("m")[0].Node)
	require.True(t, o.lanOrdered, "the pace order does not replace provider.order")
	require.Equal(t, []string{"192.168.1.21:8787", "192.168.1.20:8787"}, o.LANTowers("m"))
	u, _ := o.LANSelf("m")
	require.NotEmpty(t, u, "with fallbacks allowed the unnamed sources stay")

	// allow_fallbacks false: only the named sources, by node, node/instance, address or this machine's id
	o, why = lanConstrain(constrainOpts(), "m", []byte(`{"provider":{"order":["atlas/serve"],"allow_fallbacks":false}}`))
	require.Empty(t, why)
	u, _ = o.LANSelf("m")
	require.Empty(t, u)
	require.Len(t, o.EdgePeers("m"), 1)
	require.Equal(t, "atlas", o.EdgePeers("m")[0].Node)
	require.Empty(t, o.LANTowers("m"))

	o, _ = lanConstrain(constrainOpts(), "m", []byte(`{"provider":{"order":["n_self"],"allow_fallbacks":false}}`))
	u, _ = o.LANSelf("m")
	require.NotEmpty(t, u, "this machine is named by its Edge node id")

	_, why = lanConstrain(constrainOpts(), "m", []byte(`{"provider":{"order":["st-elsewhere"],"allow_fallbacks":false}}`))
	require.Contains(t, why, "st-elsewhere: the named station is not on this LAN")
}

func TestLANConstrainNothingLocalIsTheLaddersOwnRefusal(t *testing.T) {
	o := ProxyOptions{LANMode: true}
	_, why := lanConstrain(o, "m", []byte(`{"provider":{"quantizations":["Q8_0"]}}`))
	require.Empty(t, why, "nothing local serves it: the ladder's own refusal names that")
}

func TestLANResolveClass(t *testing.T) {
	o := ProxyOptions{LANMode: true,
		LANBands: func() []string { return []string{"gpt-oss-20b", "qwen3-8b", "llama-3.2-3b"} },
		EdgePeers: func(b string) []EdgePeer {
			if b == "qwen3-8b" {
				return []EdgePeer{{Node: "atlas"}}
			}
			return nil
		}}
	got, _, _ := lanResolveClass(o, "@class/small")
	require.Equal(t, "qwen3-8b", got, "the first band in the class's expansion that a source serves now")

	_, status, msg := lanResolveClass(o, "@class/huge")
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, msg, "unknown model class @class/huge (known: @class/coding-30b, @class/large, @class/medium, @class/small)")

	_, status, msg = lanResolveClass(o, "@class/large")
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Contains(t, msg, "no band on this LAN is in @class/large")
}

func TestLANUnknownClassCarriesTheMarketsCode(t *testing.T) {
	rec := httptest.NewRecorder()
	_, refused := relayLAN(context.Background(), rec, ProxyOptions{LANMode: true}, "@class/huge", []byte(`{"model":"@class/huge"}`), func(float64) {})
	require.True(t, refused)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"unknown_model_class"`)
}
