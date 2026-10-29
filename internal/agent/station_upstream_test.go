package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A station that offers models from more than one local server routes each job to the server
// that serves its model (features/join/invite.feature section 6); a model with no route goes to
// the share's own upstream, and the routed server's key is the one sent.
func TestStationJobGoesToTheServerOfItsModel(t *testing.T) {
	seen := map[string]string{}
	srv := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			seen[name] = string(b) + " " + r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + name + `"}}]}`))
		}))
	}
	a, b := srv("a"), srv("b")
	defer a.Close()
	defer b.Close()
	cfg := Config{Upstream: a.URL, UpstreamKey: "ka",
		StationUpstream: func(model string) (string, string, bool) {
			if model == "qwen3-8b" {
				return b.URL, "kb", true
			}
			return "", "", false
		}}
	c := &http.Client{Timeout: 5 * time.Second}
	ans := runLocalJob(c, cfg.forJob([]byte(`{"model":"qwen3-8b"}`)), []byte(`{"model":"qwen3-8b"}`))
	require.Contains(t, string(ans), `"b"`)
	require.Contains(t, seen["b"], "Bearer kb")
	ans = runLocalJob(c, cfg.forJob([]byte(`{"model":"smollm2"}`)), []byte(`{"model":"smollm2"}`))
	require.Contains(t, string(ans), `"a"`)
	require.Contains(t, seen["a"], "Bearer ka")
}
