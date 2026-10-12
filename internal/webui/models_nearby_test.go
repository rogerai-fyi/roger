package webui

// features/models/short_names_nearby.feature section 7: the console's get-a-model view shows the same
// nearby suggestions the terminal does (the same hook, filled from the same code), and choosing one
// is the same consented fetch: nothing moves without the owner's yes.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/modelview"
)

func nearbyRig(t *testing.T) (*httptest.Server, string, *[]modelview.NearbyPick) {
	t.Helper()
	var picked []modelview.NearbyPick
	nv := modelview.NearbyView{Budget: 16 << 30, Pool: "system memory",
		Fits: []modelview.NearbyRow{{Name: "qwen3-8b", Repo: "Qwen/Qwen3-8B-GGUF", File: "Qwen3-8B-Q4_K_M.gguf", Quant: "Q4_K_M",
			SHA256: strings.Repeat("ab", 32), Bytes: 5 << 30, By: "your Edge member atlas", Fit: "fits", Short: "fits  at 8K",
			Ask: "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; fetch 5.0 GiB?"}}}
	s := New(testCtrl(), Options{Models: ModelHooks{
		View:   func() (ModelsView, error) { return ModelsView{}, nil },
		Nearby: func(context.Context) (modelview.NearbyView, error) { return nv, nil },
		FetchNearby: func(p modelview.NearbyPick) (string, error) {
			picked = append(picked, p)
			return "fetching " + p.File, nil
		},
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s.Token(), &picked
}

func TestTheConsoleShowsTheSameNearbySuggestions(t *testing.T) {
	srv, tok, _ := nearbyRig(t)
	code, body := call(t, srv, tok, http.MethodGet, "/api/models/nearby", "")
	require.Equal(t, http.StatusOK, code, body)
	var nv modelview.NearbyView
	require.NoError(t, json.Unmarshal([]byte(body), &nv))
	require.Len(t, nv.Fits, 1)
	require.Equal(t, "your Edge member atlas", nv.Fits[0].By)
	require.Contains(t, nv.Fits[0].Ask, "as served by your Edge member atlas; fetch 5.0 GiB?")
	require.NotNil(t, nv.Other, "an empty list, never null, so the page draws it")

	code, _ = call(t, srv, "", http.MethodGet, "/api/models/nearby", "")
	require.Equal(t, http.StatusForbidden, code, "the page token is required")
}

func TestChoosingANearbyBandInTheConsoleNeedsTheOwnersYes(t *testing.T) {
	srv, tok, picked := nearbyRig(t)
	pick := `"name":"qwen3-8b","repo":"Qwen/Qwen3-8B-GGUF","file":"Qwen3-8B-Q4_K_M.gguf","sha256":"` + strings.Repeat("ab", 32) + `"`
	code, _ := call(t, srv, tok, http.MethodPost, "/api/models/fetch-nearby", `{`+pick+`}`)
	require.Equal(t, http.StatusPreconditionRequired, code)
	require.Empty(t, *picked)
	code, _ = call(t, srv, tok, http.MethodPost, "/api/models/fetch-nearby", `{"confirm":true,"name":"qwen3-8b"}`)
	require.Equal(t, http.StatusBadRequest, code, "a pick names the exact file")
	code, body := call(t, srv, tok, http.MethodPost, "/api/models/fetch-nearby", `{"confirm":true,`+pick+`}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Contains(t, body, "fetching Qwen3-8B-Q4_K_M.gguf")
	require.Equal(t, []modelview.NearbyPick{{Name: "qwen3-8b", Repo: "Qwen/Qwen3-8B-GGUF", File: "Qwen3-8B-Q4_K_M.gguf", SHA256: strings.Repeat("ab", 32)}}, *picked)
}

func TestABuildWithoutNearbySaysSo(t *testing.T) {
	srv, tok, _ := modelsRig(t)
	code, _ := call(t, srv, tok, http.MethodGet, "/api/models/nearby", "")
	require.Equal(t, http.StatusNotImplemented, code)
	code, _ = call(t, srv, tok, http.MethodPost, "/api/models/fetch-nearby", `{"confirm":true}`)
	require.Equal(t, http.StatusNotImplemented, code)
}

// The page itself: opening get-a-model reads what is nearby before anything else, draws it above the
// search, and a pick asks with the terminal's own consent line before posting the owner's yes.
func TestTheConsoleGetAModelViewOpensOnNearby(t *testing.T) {
	js, err := os.ReadFile("assets/console.js")
	require.NoError(t, err)
	html, err := os.ReadFile("assets/console.html")
	require.NoError(t, err)
	src := string(js)
	open := src[strings.Index(src, "+ Get a model that fits this machine"):]
	open = open[:strings.Index(open, "trg.appendChild")]
	require.Contains(t, open, "modelsNearby()", "opening the view reads what is nearby")
	require.NotContains(t, open, "modelsSearch(", "opening the view never sends a search")
	fn := src[strings.Index(src, "function modelsNearby()"):]
	fn = fn[:strings.Index(fn, "\n  }\n")]
	require.Contains(t, fn, `apiGet("/api/models/nearby")`)
	require.Contains(t, fn, "nothing is served nearby")
	pick := src[strings.Index(src, "function modelsFetchNearby("):]
	pick = pick[:strings.Index(pick, "\n  }\n")]
	require.Contains(t, pick, "r.ask")
	require.Contains(t, pick, "askSafe(")
	require.Contains(t, pick, `apiPost("/api/models/fetch-nearby", { confirm: true, name: r.name, repo: r.repo, file: r.file, sha256: r.sha256 })`)
	page := string(html)
	n, q := strings.Index(page, `id="models-nearby"`), strings.Index(page, `id="models-search"`)
	require.True(t, n > 0 && n < q, "the nearby bands are drawn above the search")
}
