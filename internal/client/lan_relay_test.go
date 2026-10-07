package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
)

// lanUpstream is a completions endpoint that records what reached it.
func lanUpstream(t *testing.T, name string, hits *atomic.Int32, check func(*http.Request)) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if check != nil {
			check(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Roger-Local", "1")
		w.Header().Set("X-Roger-Cost", "0")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"from ` + name + `"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func lanTurn(t *testing.T, opts ProxyOptions, band string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body := []byte(`{"model":"` + band + `","messages":[{"role":"user","content":"hi"}]}`)
	served, refused := relayLAN(context.Background(), rec, opts, band, body, func(float64) {})
	require.True(t, served || refused, "a LAN turn is always served or refused, never handed on")
	return rec
}

func TestRelayLANSelfFirst(t *testing.T) {
	var selfHits, towerHits atomic.Int32
	self := lanUpstream(t, "self", &selfHits, nil)
	tw := lanUpstream(t, "tower", &towerHits, nil)
	opts := ProxyOptions{LANMode: true,
		LANSelf:   func(string) (string, string) { return self.URL + "/v1/chat/completions", "rt-key" },
		LANTowers: func(string) []string { return []string{"192.168.1.20:8787"} },
		LANHTTP:   dialMap{"192.168.1.20:8787": tw.Listener.Addr().String()}.client()}
	rec := lanTurn(t, opts, "qwen3-8b")
	require.Contains(t, rec.Body.String(), "from self")
	require.Zero(t, towerHits.Load())
}

func TestRelayLANTowerSignedWithNonceAndNoAccountKey(t *testing.T) {
	var hits atomic.Int32
	tw := lanUpstream(t, "tower", &hits, func(r *http.Request) {
		require.NotEmpty(t, r.Header.Get(protocol.HeaderSig))
		require.NotEmpty(t, r.Header.Get(protocol.HeaderNonce), "a LAN Tower gets a per-request nonce")
		require.Empty(t, r.Header.Get("Authorization"), "an account key never goes to a plain-http Tower")
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		require.Contains(t, string(b), "llama-3.3-70b")
	})
	opts := ProxyOptions{LANMode: true, LANKey: lanKey(t),
		LANTowers: func(string) []string { return []string{"192.168.1.20:8787"} },
		LANHTTP:   dialMap{"192.168.1.20:8787": tw.Listener.Addr().String()}.client()}
	rec := lanTurn(t, opts, "llama-3.3-70b")
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "from tower")
	require.Equal(t, int32(1), hits.Load())
}

func TestRelayLANRefusesWhatNothingLocalServes(t *testing.T) {
	rec := lanTurn(t, ProxyOptions{LANMode: true}, "gpt-oss-120b")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var e struct {
		Error struct{ Code, Message string }
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	require.Equal(t, "lan_only", e.Error.Code)
	require.Contains(t, e.Error.Message, "gpt-oss-120b")
	require.Contains(t, e.Error.Message, "nothing on this LAN serves")
	require.Contains(t, e.Error.Message, "the market was not tried")
}

func TestRelayLANFailsOverInsideTheLANAndNamesEachFailure(t *testing.T) {
	var hits atomic.Int32
	good := lanUpstream(t, "second", &hits, nil)
	opts := ProxyOptions{LANMode: true, LANKey: lanKey(t),
		LANTowers: func(string) []string { return []string{"192.168.1.21:8787", "192.168.1.20:8787"} },
		LANHTTP:   dialMap{"192.168.1.20:8787": good.Listener.Addr().String()}.client()}
	rec := lanTurn(t, opts, "m")
	require.Contains(t, rec.Body.String(), "from second")

	opts.LANTowers = func(string) []string { return []string{"192.168.1.21:8787", "192.168.1.22:8787"} }
	rec = lanTurn(t, opts, "m")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "192.168.1.21:8787")
	require.Contains(t, rec.Body.String(), "192.168.1.22:8787")
}

func TestRelayLANNeverFollowsARedirectOrDialsAPublicTower(t *testing.T) {
	var away atomic.Int32
	elsewhere := lanUpstream(t, "elsewhere", &away, nil)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	opts := ProxyOptions{LANMode: true, LANKey: lanKey(t),
		LANTowers: func(string) []string { return []string{"192.168.1.20:8787", "203.0.113.7:8787"} },
		LANHTTP:   dialMap{"192.168.1.20:8787": redir.Listener.Addr().String(), "203.0.113.7:8787": elsewhere.Listener.Addr().String()}.client()}
	rec := lanTurn(t, opts, "m")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "redirect")
	require.Contains(t, rec.Body.String(), "public address")
	require.Zero(t, away.Load(), "neither the redirect target nor the public Tower was reached")
}

func TestRelayLANHostnameFlippedToPublicIsNotDialed(t *testing.T) {
	var hits atomic.Int32
	tw := lanUpstream(t, "tower", &hits, nil)
	opts := ProxyOptions{LANMode: true, LANKey: lanKey(t),
		LANTowers:  func(string) []string { return []string{"tower.lan:8787"} },
		LANResolve: staticResolver("203.0.113.7"),
		LANHTTP:    dialMap{"203.0.113.7:8787": tw.Listener.Addr().String(), "tower.lan:8787": tw.Listener.Addr().String()}.client()}
	rec := lanTurn(t, opts, "m")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "resolves to a public address")
	require.Zero(t, hits.Load())
	require.False(t, strings.Contains(rec.Body.String(), "from tower"))
}

func TestChatTurnsInLANModeNeverAsksTheBroker(t *testing.T) {
	var brokerHits, towerHits atomic.Int32
	broker := lanUpstream(t, "broker", &brokerHits, nil)
	tw := lanUpstream(t, "tower", &towerHits, nil)
	SetLANRelay(&ProxyOptions{LANMode: true, LANKey: lanKey(t),
		LANTowers: func(string) []string { return []string{"192.168.1.20:8787"} },
		LANHTTP:   dialMap{"192.168.1.20:8787": tw.Listener.Addr().String()}.client()})
	defer SetLANRelay(nil)
	res, err := ChatTurns(broker.URL, "u", "llama-3.3-70b", []ChatTurn{{Role: "user", Content: "hi"}}, false, 0, "", nil)
	require.NoError(t, err)
	require.Equal(t, "from tower", res.Reply)
	require.Zero(t, brokerHits.Load())

	// A band nothing local serves is refused once, not retried, and the broker is still not asked.
	SetLANRelay(&ProxyOptions{LANMode: true})
	_, err = ChatTurns(broker.URL, "u", "gpt-oss-120b", []ChatTurn{{Role: "user", Content: "hi"}}, false, 0, "", nil)
	require.ErrorContains(t, err, "nothing on this LAN serves")
	require.Zero(t, brokerHits.Load())
}

// A LAN answer tells the caller it cost nothing (features/lan/money_and_accounts.feature).
func TestRelayLANAnswersCarryAZeroCost(t *testing.T) {
	var hits atomic.Int32
	tw := lanUpstream(t, "tower", &hits, nil)
	opts := ProxyOptions{LANMode: true, LANKey: lanKey(t),
		LANTowers: func(string) []string { return []string{"192.168.1.20:8787"} },
		LANHTTP:   dialMap{"192.168.1.20:8787": tw.Listener.Addr().String()}.client()}
	rec := lanTurn(t, opts, "m")
	require.Equal(t, "0", rec.Header().Get("X-RogerAI-Cost"))
	self := lanUpstream(t, "self", &hits, nil)
	rec = lanTurn(t, ProxyOptions{LANMode: true, LANSelf: func(string) (string, string) { return self.URL, "" }}, "m")
	require.Equal(t, "0", rec.Header().Get("X-RogerAI-Cost"))
}

func TestLANOrderPeersMeasuresThenPrefersFaster(t *testing.T) {
	a := EdgePeer{Addr: "10.0.0.1:1", Pin: "a", Node: "a"}
	b := EdgePeer{Addr: "10.0.0.2:1", Pin: "b", Node: "b"}
	c := EdgePeer{Addr: "10.0.0.3:1", Pin: "c", Node: "c"}
	lanPace.Store(lanPaceKey(a), 300*time.Millisecond)
	lanPace.Store(lanPaceKey(b), 10*time.Millisecond)
	t.Cleanup(func() { lanPace.Delete(lanPaceKey(a)); lanPace.Delete(lanPaceKey(b)) })
	got := lanOrderPeers([]EdgePeer{a, b, c})
	require.Equal(t, []string{"c", "b", "a"}, []string{got[0].Node, got[1].Node, got[2].Node},
		"an unmeasured member is measured first, then the faster measured one")
}

func TestNoteLANLeftNamesEachSource(t *testing.T) {
	rec := httptest.NewRecorder()
	noteLANLeft(rec, nil)
	require.Empty(t, rec.Header().Get("X-RogerAI-LAN-Left"))
	noteLANLeft(rec, []string{"jetson/serve: connection refused", "192.168.1.20:8787: status 503"})
	require.Equal(t, "jetson, 192.168.1.20", rec.Header().Get("X-RogerAI-LAN-Left"))
}

func TestLANBandStripsOnlyRoutingVariants(t *testing.T) {
	for in, want := range map[string]string{"qwen3-8b": "qwen3-8b", "qwen3-8b:free": "qwen3-8b",
		"qwen3-8b:nitro": "qwen3-8b", "qwen3-8b:floor": "qwen3-8b", "org/m:q8": "org/m:q8"} {
		require.Equal(t, want, lanBand(in), in)
	}
}

func TestLANRefuseForeignOnlyRefusesWhatNothingLocalServes(t *testing.T) {
	opts := ProxyOptions{LANMode: true, Model: "qwen3-8b",
		LANSelf:   func(b string) (string, string) { return map[string]string{"qwen3-8b": "http://127.0.0.1:1"}[b], "" },
		EdgePeers: func(b string) []EdgePeer { return map[string][]EdgePeer{"gpt-oss-20b": {{Node: "jetson"}}}[b] },
		LANTowers: func(b string) []string { return map[string][]string{"llama": {"192.168.1.20:8787"}}[b] }}
	for model, refused := range map[string]bool{"qwen3-8b": false, "qwen3-8b:free": false, "gpt-oss-20b": false,
		"llama": false, "gpt-oss-120b": true, "": false} {
		rec := httptest.NewRecorder()
		got := lanRefuseForeign(rec, opts, []byte(`{"model":"`+model+`"}`))
		require.Equal(t, refused, got, model)
		if refused {
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			require.Contains(t, rec.Body.String(), "nothing on this LAN serves gpt-oss-120b")
		}
	}
	require.False(t, lanRefuseForeign(httptest.NewRecorder(), ProxyOptions{Model: "qwen3-8b"}, []byte(`{"model":"x"}`)), "outside LAN mode nothing changes")
	require.False(t, lanRefuseForeign(httptest.NewRecorder(), opts, []byte(`not json`)))
}
