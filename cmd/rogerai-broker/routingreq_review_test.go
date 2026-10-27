package main

// routingreq_review_test.go pins the slice-1 review fixes that live in the pure routing
// helpers: a bounded models[] list (A1) and a per-request cap that binds every output
// limit a caller can send (A2).

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
)

func TestModelsListIsBoundedBeforeDeDup(t *testing.T) {
	list := func(n int, distinct bool) string {
		ids := make([]string, n)
		for i := range ids {
			if distinct {
				ids[i] = fmt.Sprintf("%q", fmt.Sprintf("m-%d", i))
			} else {
				ids[i] = `"m"`
			}
		}
		return `{"model":"a","models":[` + strings.Join(ids, ",") + `]}`
	}
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"32 identical entries (de-dup to one) are fine", list(32, false), true},
		{"33 entries are refused before any de-dup, even identical", list(33, false), false},
		{"33 distinct entries are refused", list(33, true), false},
	} {
		_, err := parseRoutingBody([]byte(tc.body))
		if tc.ok {
			require.NoError(t, err, tc.name)
			continue
		}
		require.Error(t, err, tc.name)
		re, isRE := err.(*routingError)
		require.True(t, isRE, tc.name)
		require.Equal(t, "invalid_routing_value", re.code, tc.name)
		require.Contains(t, re.msg, "models", tc.name)
	}
}

func TestHugeModelsListIsRefusedInBoundedTime(t *testing.T) {
	ids := make([]string, 10000)
	for i := range ids {
		ids[i] = fmt.Sprintf("%q", fmt.Sprintf("m-%d", i))
	}
	body := []byte(`{"model":"a","models":[` + strings.Join(ids, ",") + `]}`)
	start := time.Now()
	_, err := parseRoutingBody(body)
	require.Error(t, err)
	// effectiveModels must never see more than 32 raw entries; it stops at the sixth
	// distinct id either way.
	_, _, _, rerr := effectiveModels("a", []string{"b", "c", "d", "e", "f", "g", "h"})
	require.NotNil(t, rerr)
	require.Less(t, time.Since(start), 2*time.Second, "a 10k-entry list must be refused without quadratic work")
}

func TestCapBodyBindsEveryOutputLimit(t *testing.T) {
	get := func(body []byte, key string) (int, bool) {
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body, &m))
		raw, ok := m[key]
		if !ok {
			return 0, false
		}
		var v int
		require.NoError(t, json.Unmarshal(raw, &v))
		return v, true
	}
	// $0.004 cap, $1/1M in, $2/1M out, 1000 prompt tokens: $0.001 in -> $0.003 left -> 1500 out.
	buys, drop := capBuys(0.004, 1000, 1.0, 2.0)
	require.False(t, drop)
	require.Equal(t, 1500, buys)

	for _, tc := range []struct {
		name, body          string
		wantMax, wantMaxCmp int // -1 = key absent
	}{
		{"max_completion_tokens alone is capped", `{"model":"m","max_completion_tokens":100000}`, -1, 1500},
		{"both limits are capped", `{"model":"m","max_tokens":100000,"max_completion_tokens":100000}`, 1500, 1500},
		{"a limit already within the cap is kept", `{"model":"m","max_tokens":200}`, 200, -1},
		{"a body naming neither gains max_tokens", `{"model":"m"}`, 1500, -1},
		{"one within, one above: only the one above is lowered", `{"model":"m","max_tokens":200,"max_completion_tokens":9000}`, 200, 1500},
	} {
		got := capBody([]byte(tc.body), buys)
		mt, okT := get(got, "max_tokens")
		mc, okC := get(got, "max_completion_tokens")
		if tc.wantMax < 0 {
			require.False(t, okT, tc.name)
		} else {
			require.Equal(t, tc.wantMax, mt, tc.name)
		}
		if tc.wantMaxCmp < 0 {
			require.False(t, okC, tc.name)
		} else {
			require.Equal(t, tc.wantMaxCmp, mc, tc.name)
		}
	}
}

func TestCapBuysDropsAStationTheCapBuysNothingAt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cap      float64
		prompt   int
		in, out  float64
		wantDrop bool
	}{
		{"input alone meets the cap", 0.002, 2000, 1.0, 2.0, true},
		{"less than one output token left", 0.0020015, 2000, 1.0, 2.0, true},
		{"exactly one output token left", 0.002002, 2000, 1.0, 2.0, false},
		{"free output, priced input below the cap", 0.004, 1000, 1.0, 0, false},
		{"free output, input alone meets the cap", 0.001, 1000, 1.0, 0, true},
		{"free station", 0.001, 1000, 0, 0, false},
	} {
		_, drop := capBuys(tc.cap, tc.prompt, tc.in, tc.out)
		require.Equal(t, tc.wantDrop, drop, tc.name)
	}
}

// TestFreeNeverAdmitsAPricedTowerRow (review A3): under `:free` a Tower row is eligible only
// when it costs the caller nothing. Owning the NODE behind a Tower row does not make the row
// free - the bridge bills the Tower's price, never self-use - so the freeFor exemption that
// applies to a caller's own direct station must not reach a priced row.
func TestFreeNeverAdmitsAPricedTowerRow(t *testing.T) {
	b := relayBroker(store.NewMem())
	now := time.Now()
	b.nodes["n-mine"] = protocol.NodeRegistration{NodeID: "n-mine", Offers: []protocol.ModelOffer{{Model: "m"}}}
	b.lastSeen["n-mine"] = now
	priced := fleet.Station{TowerID: "tw-1", StationID: "st-a", Model: "m", NodeID: "n-mine", PriceIn: 100000, PriceOut: 500000}
	free := fleet.Station{TowerID: "tw-2", StationID: "st-b", Model: "m", NodeID: "n-mine", PriceIn: 0, PriceOut: 0}
	c := edgeConstraints{freeOnly: true} // the caller owns n-mine; the bridge does not care
	a, bb, _ := b.edgeEligibleM([]fleet.Station{priced, free}, nil, now, c)
	got := map[int]bool{}
	for _, s := range append(a, bb...) {
		got[s.idx] = true
	}
	require.False(t, got[0], "a priced Tower row is never eligible under :free, even when the caller owns the node behind it")
	require.True(t, got[1], "a free Tower row is eligible under :free")
}

// TestDispatchedModelKeepsTheNodeSignature (review A5): when a station's receipt names a
// different model than the one the attempt was dispatched for, the broker records the
// dispatched model WITHOUT touching what the node signed: the stored receipt's node
// signature still verifies and its hash (the next receipt's PrevHash) is unchanged.
func TestDispatchedModelKeepsTheNodeSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	rec := protocol.UsageReceipt{RequestID: "r-1", NodeID: "n-1", User: "u", Model: "x-model", PromptTokens: 10, CompletionTokens: 20, TS: 1}
	rec.SignNode(priv)
	before := rec.Hash()
	b := relayBroker(store.NewMem())
	b.dispatchedModel(&rec, attemptCand{model: "m", node: protocol.NodeRegistration{NodeID: "n-1"}})
	require.True(t, rec.VerifyNode(hex.EncodeToString(pub)), "the node's signature still verifies after the broker records the dispatched model")
	require.Equal(t, before, rec.Hash(), "the chain hash is the node's, unchanged")
	require.Equal(t, "x-model", rec.Model, "the node-signed model is kept as signed")
	require.Equal(t, "m", rec.ServedModel(), "billing and X-RogerAI-Model follow the dispatched model")
	rec.SigVersion = protocol.BrokerSigVersion
	bpub, bpriv, _ := ed25519.GenerateKey(nil)
	rec.SignBroker(bpriv)
	ok, covers := rec.VerifyBrokerCoverage(hex.EncodeToString(bpub))
	require.True(t, ok && covers, "the broker signature covers the dispatched model")
	tampered := rec
	tampered.DispatchedModel = "other"
	ok, _ = tampered.VerifyBrokerCoverage(hex.EncodeToString(bpub))
	require.False(t, ok, "rewriting the dispatched model breaks the broker signature")
}

// TestTowerFailureStatusIsBounded (review A6): a Tower is a third party, so the status it
// reports for the station behind it is accepted only as a failure (400-599); anything else
// is a bad gateway, and its Retry-After never exceeds the broker's own cooldown cap.
func TestTowerFailureStatusIsBounded(t *testing.T) {
	maxSecs := int(cooldownMax() / time.Second)
	for _, tc := range []struct {
		class              string
		wantStatus, wantRA int
	}{
		{"the model did not answer (status 429, retry-after 6)", 429, 6},
		{"the model did not answer (status 503)", 503, 0},
		{"the model did not answer (status 200)", 502, 0},
		{"the model did not answer (status 101)", 502, 0},
		{"the model did not answer (status 999, retry-after 3)", 502, 3},
		{"the model did not answer (status 429, retry-after 99999999)", 429, maxSecs},
		{"the model did not answer", 0, 0}, // an old station's bare class: generic, unchanged
	} {
		st, ra := upstreamClassStatus(tc.class)
		require.Equal(t, tc.wantStatus, st, tc.class)
		require.Equal(t, tc.wantRA, ra, tc.class)
	}
}

// TestErrorBodyIsValidJSON (review A9): the stream path's refusal bodies are marshalled, so a
// message carrying a quote or a model id with odd characters cannot break the envelope.
func TestErrorBodyIsValidJSON(t *testing.T) {
	// Contract §14.B6: code is always set (derived from the status when the caller has none)
	// and every envelope carries a type and a metadata object.
	for _, tc := range []struct{ code, msg, want string }{
		{"no_match", `no node offers we"ird\model`, "no_match"},
		{"", "slot limit reached", "server_error"},
	} {
		var env struct {
			Error struct {
				Code     string         `json:"code"`
				Message  string         `json:"message"`
				Type     string         `json:"type"`
				Metadata map[string]any `json:"metadata"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(errorBody(503, tc.code, tc.msg), &env))
		require.Equal(t, tc.want, env.Error.Code)
		require.Equal(t, tc.msg, env.Error.Message)
		require.Equal(t, "overloaded_error", env.Error.Type)
		require.NotNil(t, env.Error.Metadata)
	}
	require.True(t, json.Valid(towerFailureBody(429)))
}

// TestWidestWindowNeverQuotesAPrivateStation (review A13): the context-window 400 on a public
// request names the widest window the request could reach; a private band's station is not
// reachable without its code, so its window is never quoted. Inside a band (an admission set
// naming the station) it is.
func TestWidestWindowNeverQuotesAPrivateStation(t *testing.T) {
	b := relayBroker(store.NewMem())
	now := time.Now()
	b.nodes["n-pub"] = protocol.NodeRegistration{NodeID: "n-pub", Offers: []protocol.ModelOffer{{Model: "m", Ctx: 8192}}}
	b.nodes["n-priv"] = protocol.NodeRegistration{NodeID: "n-priv", Offers: []protocol.ModelOffer{{Model: "m", Ctx: 131072}}}
	b.lastSeen["n-pub"], b.lastSeen["n-priv"] = now, now
	b.private["n-priv"] = true
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Equal(t, 8192, b.maxDeclaredCtxLocked("m", nil), "a public request never learns a private station's window")
	require.Equal(t, 8192, b.maxDeclaredCtxLocked("m"), "no scope is the public scope")
	require.Equal(t, 131072, b.maxDeclaredCtxLocked("m", map[string]bool{"n-priv": true}), "inside the band the band's station counts")
}
