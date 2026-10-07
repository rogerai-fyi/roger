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

// TestParseCtxRefusesOverflow: a k count too large to multiply is refused, never wrapped,
// and a count above what the broker accepts (2^31-1) is refused too.
func TestParseCtxRefusesOverflow(t *testing.T) {
	for _, v := range []string{"9007199254740993k", "9223372036854775807k", "3000000000", "2097152k"} {
		_, err := ParseCtx(v)
		require.Error(t, err, v)
	}
	n, err := ParseCtx("2097151k")
	require.NoError(t, err)
	require.Equal(t, 2097151*1024, n)
}

// TestFreeOnlyKeepsAStackedFreeVariant: an id already carrying :free among stacked sugar
// (m:free:nitro) is not suffixed again.
func TestFreeOnlyKeepsAStackedFreeVariant(t *testing.T) {
	out, err := Routing{FreeOnly: true}.Apply([]byte(`{"model":"m:free:nitro","models":["a:nitro:free","b"]}`))
	require.NoError(t, err)
	var got struct {
		Model  string
		Models []string
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, "m:free:nitro", got.Model)
	require.Equal(t, []string{"a:nitro:free", "b:free"}, got.Models)
}

// TestMistypedGuestValuesGoToTheBroker: a guest value of the wrong type is forwarded as sent
// for the broker's 400 (as a non-object max_price is), never silently swapped for the owner's.
func TestMistypedGuestValuesGoToTheBroker(t *testing.T) {
	owner := Routing{TrustMin: "verified", Region: []string{"eu"}, MinCtx: 32768, MaxTTFT: 1500}
	for _, tc := range []struct{ key, guest string }{
		{"trust_min", `5`}, {"region", `"eu"`}, {"min_ctx", `"32k"`}, {"max_ttft_ms", `"fast"`},
	} {
		out, err := owner.Apply([]byte(`{"model":"m","roger":{"` + tc.key + `":` + tc.guest + `}}`))
		require.NoError(t, err, tc.key)
		var got struct{ Roger map[string]json.RawMessage }
		require.NoError(t, json.Unmarshal(out, &got))
		require.JSONEq(t, tc.guest, string(got.Roger[tc.key]), tc.key)
	}
	// A well-typed looser value is still tightened to the owner's.
	out, err := owner.Apply([]byte(`{"model":"m","roger":{"trust_min":"any","min_ctx":1024}}`))
	require.NoError(t, err)
	require.Contains(t, string(out), `"trust_min":"verified"`)
	require.Contains(t, string(out), `"min_ctx":32768`)
}

// TestNonStringModelCannotSmuggleAForeignList: a guest model that is not a string never turns
// the band check off: the body is refused locally and nothing reaches the broker.
func TestNonStringModelCannotSmuggleAForeignList(t *testing.T) {
	hit := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(broker.Close)
	h := ProxyHandler(ProxyOptions{Broker: broker.URL, User: "u", Model: "band"})
	for _, body := range []string{
		`{"model":5,"models":["foreign"],"messages":[]}`,
		`{"model":{"x":1},"models":["foreign"],"messages":[]}`,
		`{"model":["band"],"messages":[]}`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
	require.False(t, hit, "a refused body never reaches the broker")
}
