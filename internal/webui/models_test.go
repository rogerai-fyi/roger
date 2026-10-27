package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

// features/serving/local_models.feature §8: the console's models page, driven over HTTP with its
// token the way the page drives it.

type modelCalls struct {
	fetch, serve, stop, remove []string
}

func modelsRig(t *testing.T) (*httptest.Server, string, *modelCalls) {
	t.Helper()
	c := &modelCalls{}
	view := ModelsView{Budget: 16 << 30, Pool: "system memory", Runtime: "llama.cpp b11146 installed", CanServe: true,
		Models: []ModelRow{{ID: "acme/Tiny-GGUF:Q4_K_M", File: "Tiny-Q4_K_M.gguf", Band: "tiny", Verified: true}}}
	s := New(testCtrl(), Options{Models: ModelHooks{
		View: func() (ModelsView, error) { return view, nil },
		Search: func(ctx context.Context, q string) ([]RepoRow, error) {
			if q == "boom" {
				return nil, errors.New("the source did not answer - check this machine's network")
			}
			return []RepoRow{{Repo: "acme/Tiny-GGUF", Downloads: 9, License: "mit"}, {Repo: "acme/Locked-GGUF", Gated: true}}, nil
		},
		Files: func(ctx context.Context, repo string, ctxLen int) (FilesView, error) {
			return FilesView{Repo: repo, Ctx: ctxLen, Choices: []FileChoice{{Label: "Q4_K_M", File: "Tiny-Q4_K_M.gguf", Fit: "fits", Suggested: true}}}, nil
		},
		Fetch: func(repo, file string) error {
			c.fetch = append(c.fetch, repo+"|"+file)
			if file == "busy.gguf" {
				return errors.New("a model is already being fetched on this machine")
			}
			return nil
		},
		Serve: func(id string, ctxLen int, add bool) (string, error) {
			if add {
				id += "+add"
			}
			c.serve = append(c.serve, id)
			if id == "huge" {
				return "", errors.New("will not fit at 40K context in 16.0 GiB of system memory; fits at 8K")
			}
			return "starting " + id, nil
		},
		Stop: func(id string) (string, error) {
			c.stop = append(c.stop, "stop:"+id)
			return "stopped serving tiny.", nil
		},
		Remove: func(id string) (string, error) { c.remove = append(c.remove, id); return "removed " + id, nil },
	}})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s.Token(), c
}

func call(t *testing.T, srv *httptest.Server, tok, method, path, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	require.NoError(t, err)
	if tok != "" {
		req.Header.Set("X-Roger-Token", tok)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestTheModelsPageAnswersWhatThisMachineCanRun(t *testing.T) {
	srv, tok, _ := modelsRig(t)
	code, body := call(t, srv, tok, "GET", "/api/models", "")
	require.Equal(t, http.StatusOK, code, body)
	var v ModelsView
	require.NoError(t, json.Unmarshal([]byte(body), &v))
	require.Equal(t, "system memory", v.Pool)
	require.True(t, v.CanServe)
	require.Len(t, v.Models, 1)
	require.True(t, v.Models[0].Verified)

	code, _ = call(t, srv, "", "GET", "/api/models", "")
	require.Equal(t, http.StatusForbidden, code, "the page answers only to its own token")
}

func TestSearchingFromTheConsoleIsTheAuthorsWord(t *testing.T) {
	srv, tok, _ := modelsRig(t)
	code, body := call(t, srv, tok, "GET", "/api/models/search?q=tiny", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"source":"huggingface"`)
	require.Contains(t, body, `"gated":true`)

	code, body = call(t, srv, tok, "GET", "/api/models/search?q=boom", "")
	require.Equal(t, http.StatusBadGateway, code)
	require.Contains(t, body, "network", "the failure names which failure")

	code, _ = call(t, srv, tok, "GET", "/api/models/search?q="+strings.Repeat("x", 300), "")
	require.Equal(t, http.StatusBadRequest, code)

	code, body = call(t, srv, tok, "GET", "/api/models/files?repo=acme/Tiny-GGUF&ctx=4096", "")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"suggested":true`)
	require.Contains(t, body, `"ctx":4096`)
	code, _ = call(t, srv, tok, "GET", "/api/models/files", "")
	require.Equal(t, http.StatusBadRequest, code)
}

func TestEveryChangeFromTheConsoleNeedsTheOwnersYes(t *testing.T) {
	srv, tok, c := modelsRig(t)
	for _, p := range []struct{ path, body string }{
		{"/api/models/fetch", `{"repo":"acme/Tiny-GGUF","file":"Tiny-Q4_K_M.gguf"}`},
		{"/api/models/serve", `{"id":"tiny"}`},
		{"/api/models/stop", `{}`},
		{"/api/models/remove", `{"id":"tiny"}`},
	} {
		code, _ := call(t, srv, tok, "POST", p.path, p.body)
		require.Equal(t, http.StatusPreconditionRequired, code, p.path)
		code, _ = call(t, srv, tok, "GET", p.path, "")
		require.Equal(t, http.StatusMethodNotAllowed, code, "%s changes nothing on a GET", p.path)
	}
	require.Empty(t, c.fetch)
	require.Empty(t, c.serve)
	require.Empty(t, c.stop)
	require.Empty(t, c.remove)
}

func TestTheConsoleCarriesARepoAndFileNeverAnAddress(t *testing.T) {
	srv, tok, c := modelsRig(t)
	// A URL smuggled beside the fields is not a field: the hook only ever sees repo and file.
	code, body := call(t, srv, tok, "POST", "/api/models/fetch",
		`{"confirm":true,"repo":"acme/Tiny-GGUF","file":"Tiny-Q4_K_M.gguf","url":"http://evil.example/x.gguf","runtime":"/bin/sh"}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, []string{"acme/Tiny-GGUF|Tiny-Q4_K_M.gguf"}, c.fetch)
	require.Contains(t, body, `"view"`)

	code, body = call(t, srv, tok, "POST", "/api/models/fetch", `{"confirm":true,"repo":"acme/Tiny-GGUF","file":"busy.gguf"}`)
	require.Equal(t, http.StatusConflict, code)
	require.Contains(t, body, "already being fetched")
	code, _ = call(t, srv, tok, "POST", "/api/models/fetch", `{"confirm":true}`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = call(t, srv, tok, "POST", "/api/models/fetch", `{nope`)
	require.Equal(t, http.StatusBadRequest, code)
}

func TestServingStoppingAndRemovingFromTheConsole(t *testing.T) {
	srv, tok, c := modelsRig(t)
	code, body := call(t, srv, tok, "POST", "/api/models/serve", `{"confirm":true,"id":"tiny","ctx":4096}`)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "starting tiny")

	code, body = call(t, srv, tok, "POST", "/api/models/serve", `{"confirm":true,"id":"huge"}`)
	require.Equal(t, http.StatusConflict, code)
	require.Contains(t, body, "fits at 8K", "a refusal carries the arithmetic")
	code, _ = call(t, srv, tok, "POST", "/api/models/serve", `{"confirm":true,"id":"tiny","ctx":-1}`)
	require.Equal(t, http.StatusBadRequest, code)

	code, body = call(t, srv, tok, "POST", "/api/models/stop", `{"confirm":true}`)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "stopped serving tiny")

	// local_models.feature §5: a second beside the first is asked for as such, and one of two is
	// stopped by its id.
	code, _ = call(t, srv, tok, "POST", "/api/models/serve", `{"confirm":true,"id":"other","add":true}`)
	require.Equal(t, http.StatusOK, code)
	code, _ = call(t, srv, tok, "POST", "/api/models/stop", `{"confirm":true,"id":"acme/Tiny-GGUF:Q4_K_M"}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []string{"stop:", "stop:acme/Tiny-GGUF:Q4_K_M"}, c.stop)
	require.Contains(t, c.serve, "other+add")

	code, body = call(t, srv, tok, "POST", "/api/models/remove", `{"confirm":true,"id":"tiny"}`)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "removed tiny")
	require.Equal(t, []string{"tiny"}, c.remove)
}

func TestAConsoleWithNoModelHooksSaysToUseTheTerminal(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, p := range []string{"/api/models", "/api/models/search?q=x", "/api/models/files?repo=a/b"} {
		code, body := call(t, srv, s.Token(), "GET", p, "")
		require.Equal(t, http.StatusNotImplemented, code, p)
		require.Contains(t, body, "roger model")
	}
	for _, p := range []string{"/api/models/fetch", "/api/models/serve", "/api/models/stop", "/api/models/remove"} {
		code, _ := call(t, srv, s.Token(), "POST", p, `{"confirm":true,"id":"x","repo":"a/b","file":"f"}`)
		require.Equal(t, http.StatusNotImplemented, code, p)
	}
}

// The page draws text from the wider world (repo names, licences), so it never parses any of it as
// HTML, and every change is asked through the one dialog whose focused answer is No.
func TestTheModelsPageNeverTreatsIndexTextAsHTMLAndAsksSafely(t *testing.T) {
	js := consoleJS(t)
	a, b := strings.Index(js, "/* 8c. MODELS"), strings.Index(js, "/* end MODELS */")
	require.True(t, a > 0 && b > a, "the models section is missing")
	sec := js[a:b]
	require.NotContains(t, sec, "innerHTML")
	require.NotContains(t, sec, "insertAdjacentHTML")
	require.NotContains(t, sec, "window.confirm", "a native confirm focuses OK; the page uses its own safe dialog")
	require.Contains(t, sec, `$("ask-no").focus()`, "Cancel holds the focus, so Enter alone answers No")
	require.Equal(t, 6, strings.Count(sec, "confirm: true"), "fetch, a nearby fetch, serve, stop, remove and a serving setting each carry the owner's yes")
	require.Equal(t, 6, strings.Count(sec, ".then(function (yes) {\n      if (!yes) return;"), "and each is asked first, and stops on a No")
}

// features/edge/remote_model.feature (@web): a member's model is on its card, and the console asks it
// to serve one with the owner's yes, carrying a node and a model id only.
func TestTheConsoleShowsAndSwitchesAMembersModel(t *testing.T) {
	fleet := edge.NewFleet(store.NewMem(), "acct-1")
	n := edge.NewNode(nil, "jetson", edge.Host)
	n.ID = "n_jetson"
	n.Presence = string(edge.PresenceVerified)
	n.Caps = []store.EdgeCap{{Name: string(edge.ServeControl), State: string(edge.Claimed)}}
	n.Instances = []store.EdgeInstance{{Name: "jetson", Bands: []string{"qwen3-4b"}, ServeProven: []string{"qwen3-4b"}, Model: &store.EdgeModel{Band: "qwen3-4b", ID: "unsloth/Qwen3-4B-GGUF:Q4_K_M", Ctx: 8192,
		Have: []string{"unsloth/Qwen3-4B-GGUF:Q4_K_M"}}}}
	_, err := fleet.Enroll(n)
	require.NoError(t, err)
	var asked []string
	s := New(testCtrl(), Options{Edge: EdgeHooks{Fleet: fleet, ServeModel: func(node, id string) (string, error) {
		asked = append(asked, node+"|"+id)
		return "starting; it serves once proven", nil
	}}})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	code, body := call(t, srv, s.Token(), "GET", "/api/edge", "")
	require.Equal(t, http.StatusOK, code, body)
	require.Contains(t, body, `"serve_model_manage":true`)
	require.Contains(t, body, `"serve_control":true`)
	require.Contains(t, body, `serving \"qwen3-4b\" (unsloth/Qwen3-4B-GGUF:Q4_K_M, 8192 context)`)
	require.Contains(t, body, `"model_have":["unsloth/Qwen3-4B-GGUF:Q4_K_M"]`)
	require.Contains(t, body, `"state":"HEARD now · no control path"`, "the card carries the shared state word (fleet_window.feature)")

	code, _ = call(t, srv, s.Token(), "POST", "/api/edge/serve-model", `{"id":"n_jetson","model":"unsloth/Qwen3-8B-GGUF:Q4_K_M"}`)
	require.Equal(t, http.StatusPreconditionRequired, code, "no yes, no request")
	code, body = call(t, srv, s.Token(), "POST", "/api/edge/serve-model",
		`{"confirm":true,"id":"n_jetson","model":"unsloth/Qwen3-8B-GGUF:Q4_K_M","url":"http://evil/x.gguf"}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Contains(t, body, "starting")
	require.Equal(t, []string{"n_jetson|unsloth/Qwen3-8B-GGUF:Q4_K_M"}, asked, "the member is sent a node and an id, nothing else")
	code, _ = call(t, srv, s.Token(), "POST", "/api/edge/serve-model", `{"confirm":true,"id":"n_jetson"}`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = call(t, srv, s.Token(), "GET", "/api/edge/serve-model", "")
	require.Equal(t, http.StatusMethodNotAllowed, code)

	js := consoleJS(t)
	require.Contains(t, js, "edgeServeModelControls(card, n)")
	require.Contains(t, js, "searches nowhere for a model")

	off := New(testCtrl(), Options{Edge: EdgeHooks{Fleet: fleet}})
	srv2 := httptest.NewServer(off.Handler())
	defer srv2.Close()
	code, _ = call(t, srv2, off.Token(), "POST", "/api/edge/serve-model", `{"confirm":true,"id":"n_jetson","model":"a/b:Q4"}`)
	require.Equal(t, http.StatusNotImplemented, code)
}
