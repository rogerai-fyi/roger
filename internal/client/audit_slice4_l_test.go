package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCLIFreeTuneSuffixesEveryFallback: `roger use m:free` (the session model carries the
// variant and FreeOnly is not set) still asks for free on every models[] entry, so a
// fallback never lands on a paid station.
func TestCLIFreeTuneSuffixesEveryFallback(t *testing.T) {
	var got map[string]any
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		http.Error(w, `{"error":{"code":"no_match","message":"none"}}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(broker.Close)
	h := ProxyHandler(ProxyOptions{Broker: broker.URL, User: "u", Model: "m:free", Models: []string{"m", "b"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)))
	require.NotNil(t, got, "the request reached the broker")
	require.Equal(t, "m:free", got["model"])
	require.Equal(t, []any{"m:free", "b:free"}, got["models"], "every fallback asks for free")
}

// TestPrefixedProfileRefIsNotAForeignModel: a guest's provider prefix in front of a profile
// reference (roger/@profile/x) is the same reference, never a foreign model.
func TestPrefixedProfileRefIsNotAForeignModel(t *testing.T) {
	body := []byte(`{"model":"roger/@profile/x","roger":{"pref":"fast"}}`)
	require.False(t, guestNamesOtherModel(body, "roger/@profile/x", "b"))
	require.False(t, guestNamesOtherModel(body, "openai/@profile/x", "b"))
	require.True(t, guestNamesOtherModel(body, "roger/other", "b"))
}

// TestOwnerModelsCompareOnTheBareID: an owner list written with a variant (a:free) admits a
// guest naming the bare id.
func TestOwnerModelsCompareOnTheBareID(t *testing.T) {
	out, err := Routing{Models: []string{"a:free", "b"}}.Apply([]byte(`{"model":"b","models":["a"]}`))
	require.NoError(t, err)
	var got struct{ Models []string }
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, []string{"a"}, got.Models)
}

// TestFreeTuneKeepsAGuestSortSugar: on a band tuned as m:free, a guest's m:nitro keeps its
// sort sugar, and the session's :free still binds it.
func TestFreeTuneKeepsAGuestSortSugar(t *testing.T) {
	var got map[string]any
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		http.Error(w, `{"error":{"code":"no_match","message":"none"}}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(broker.Close)
	h := ProxyHandler(ProxyOptions{Broker: broker.URL, User: "u", Model: "m:free"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m:nitro","provider":{"quantizations":["Q8_0"]},"messages":[{"role":"user","content":"hi"}]}`)))
	require.NotNil(t, got, "the request reached the broker")
	require.Equal(t, "m:nitro:free", got["model"], "the guest's sort sugar is kept and the session's :free still binds")
}
