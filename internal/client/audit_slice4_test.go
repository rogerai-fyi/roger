package client

// audit_slice4_test.go - pins for the slice-4 pre-push audit findings in the local proxy:
// the failover re-pick stays inside the OWNER's routing (only set, no-fallbacks / pinned
// node), and a `:free` band compares guest model ids bare to bare.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// auditBroker serves /discover with the given offers and answers chat with a 503 for the
// stations in failing, 200 otherwise. It records each chat attempt's provider.order / only.
type auditAttempt struct {
	order, only []string
}

func auditBroker(t *testing.T, offers string, failing map[string]bool) (*httptest.Server, func() []auditAttempt) {
	t.Helper()
	var mu sync.Mutex
	var attempts []auditAttempt
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"offers":[` + offers + `]}`))
		case "/v1/chat/completions":
			b, _ := io.ReadAll(r.Body)
			var m struct {
				Provider struct {
					Order []string `json:"order"`
					Only  []string `json:"only"`
				} `json:"provider"`
			}
			_ = json.Unmarshal(b, &m)
			mu.Lock()
			attempts = append(attempts, auditAttempt{order: m.Provider.Order, only: m.Provider.Only})
			mu.Unlock()
			// The station the broker would serve: the preferred one, else the first allowed.
			node := ""
			if len(m.Provider.Order) > 0 {
				node = m.Provider.Order[0]
			} else if len(m.Provider.Only) > 0 {
				node = m.Provider.Only[0]
			} else {
				node = "n1"
			}
			w.Header().Set("X-RogerAI-Provider", node)
			if failing[node] {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []auditAttempt {
		mu.Lock()
		defer mu.Unlock()
		return append([]auditAttempt(nil), attempts...)
	}
}

const auditOffers = `{"node_id":"n1","model":"m","price_in":0.1,"online":true,"tps":50},
	{"node_id":"n2","model":"m","price_in":0.1,"online":true,"tps":40},
	{"node_id":"n3","model":"m","price_in":0.1,"online":true,"tps":500}`

// Finding 1: the owner's --only bounds the failover's alternative.
func TestFailoverRepickStaysInsideTheOwnersOnlySet(t *testing.T) {
	srv, attempts := auditBroker(t, auditOffers, map[string]bool{"n1": true})
	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u", Only: []string{"n1", "n2"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`)))
	require.Equal(t, http.StatusOK, rec.Code, "the failover should reach n2, inside the owner's only set; attempts=%+v", attempts())
	for _, a := range attempts() {
		for _, o := range a.order {
			require.NotEqual(t, "n3", o, "the re-pick preferred n3, outside the owner's only set: %+v", attempts())
		}
	}
}

// Finding 2: an owner --no-fallbacks (or --node) session is never re-picked.
func TestOwnerNoFallbacksIsNeverRepicked(t *testing.T) {
	srv, attempts := auditBroker(t, auditOffers, map[string]bool{"n1": true})
	h := ProxyHandler(ProxyOptions{Broker: srv.URL, User: "u", Prefer: []string{"n1"}, NoFallbacks: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`)))
	got := attempts()
	require.Len(t, got, 1, "an owner no-fallbacks session made %d attempts: %+v", len(got), got)
	require.NotEqual(t, http.StatusOK, rec.Code)
}

// Finding 3: a `:free` band compares the guest's bare model id with the band's bare id.
func TestFreeBandAcceptsTheGuestsBareModel(t *testing.T) {
	require.NoError(t, GuestModelsWithin([]byte(`{"model":"qwen3","roger":{"pref":"fast"}}`), "qwen3:free"))
	require.NoError(t, GuestModelsWithin([]byte(`{"model":"roger/qwen3","models":["qwen3"],"roger":{}}`), "qwen3:free"))
	require.Error(t, GuestModelsWithin([]byte(`{"model":"other","roger":{}}`), "qwen3:free"))
	require.False(t, guestNamesOtherModelOf([]byte(`{"model":"qwen3","roger":{}}`), "qwen3:free"))
}

// Finding 6: params_b accepts 0 as the lower bound ("no floor"), as the broker does.
func TestProfileParamsBAcceptsAZeroFloor(t *testing.T) {
	var r map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"params_b":[0,8]}`), &r))
	require.NoError(t, validateRoger(r))
	require.NoError(t, json.Unmarshal([]byte(`{"params_b":[0,0]}`), &r))
	require.Error(t, validateRoger(r))
	require.NoError(t, json.Unmarshal([]byte(`{"params_b":[8,0]}`), &r))
	require.Error(t, validateRoger(r))
	require.NoError(t, json.Unmarshal([]byte(`{"params_b":[-1,8]}`), &r))
	require.Error(t, validateRoger(r))
}
