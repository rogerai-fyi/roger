package localplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// features/models/short_names_nearby.feature "a station on a pinned Tower reports what it serves,
// as its own word": a station's poll may name the exact file behind each model it serves; the
// Tower keeps that report only for models it routes, and /discover passes it on marked as the
// station's word. An older station reports no files, and its offers carry none.
func TestAStationsReportedFileReachesDiscoverAsItsWord(t *testing.T) {
	const sha = "2e8040ceae78a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990"
	st := standaloneState(t)
	client := admitClient(t, st)
	priv := newKeyPair()
	_, err := st.AttachStation("kitchen-pi", keyHashOf(priv), []string{"smollm2", "qwen3-8b"})
	require.NoError(t, err)
	srv := New(st)
	srv.pollTimeout = 50 * time.Millisecond
	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	c := &http.Client{Timeout: 5 * time.Second}
	poll := func(body string) {
		req, err := signedPost(h.URL, "/local/poll", []byte(body), priv)
		require.NoError(t, err)
		code, _, err := doBody(c, req)
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, code)
	}
	type off struct {
		Model  string         `json:"model"`
		File   map[string]any `json:"file"`
		FileBy string         `json:"file_by"`
	}
	discover := func() map[string]off {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, signedReq(t, client, http.MethodGet, "/discover", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var body struct{ Offers []off }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		out := map[string]off{}
		for _, o := range body.Offers {
			out[o.Model] = o
		}
		return out
	}

	poll(`{"streams":true,"models":["smollm2","qwen3-8b"],"files":[
	  {"model":"smollm2","repo":"bartowski/SmolLM2-135M-Instruct-GGUF","file":"SmolLM2-135M-Instruct-Q4_K_M.gguf","quant":"Q4_K_M","sha256":"` + sha + `"},
	  {"model":"qwen3-8b","repo":"../../etc","file":"passwd.gguf","sha256":"` + sha + `"},
	  {"model":"llama-70b","repo":"a/b","file":"b.gguf","sha256":"` + sha + `"}]}`)
	got := discover()
	require.Equal(t, "bartowski/SmolLM2-135M-Instruct-GGUF", got["smollm2"].File["repo"])
	require.Equal(t, sha, got["smollm2"].File["sha256"])
	require.Equal(t, "station", got["smollm2"].FileBy, "the Tower marks the file as the station's report")
	require.Nil(t, got["qwen3-8b"].File, "a report that cannot name one file is dropped")
	_, routed := got["llama-70b"]
	require.False(t, routed, "a file for a model the Tower does not route adds nothing")

	// An older station reports models and no files: its offers carry no file.
	poll(`{"streams":true,"models":["smollm2"]}`)
	require.Nil(t, discover()["smollm2"].File, "a station that stops reporting a file maps nothing")
}
