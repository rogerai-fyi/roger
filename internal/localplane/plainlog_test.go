package localplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/tower"
)

// features/join/tower_identity.feature: a Tower serving TLS and plain http logs, once per client,
// the clients still on plain http, by the name they joined under. A TLS request logs nothing.
func TestThePlaneLogsAPlainHTTPClientOnce(t *testing.T) {
	st := standaloneState(t)
	srv := New(st)
	var mu sync.Mutex
	var logged []string
	srv.logf = func(f string, a ...any) {
		if line := fmt.Sprintf(f, a...); strings.Contains(line, "plain http") {
			mu.Lock()
			logged = append(logged, line)
			mu.Unlock()
		}
	}
	lines := func() []string {
		time.Sleep(100 * time.Millisecond) // the log line is written off the request path
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), logged...)
	}

	_, code, err := st.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
	require.NoError(t, err)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	n := protocol.NewNonce()
	require.Equal(t, http.StatusOK, doJoin(srv, priv, joinBody(t, st, priv, code, n, "client", "laptop-x", nil), n).Code)

	discover := func(overTLS bool) int {
		rec := httptest.NewRecorder()
		r := nonceReq(priv, http.MethodGet, "/discover", nil, protocol.NewNonce())
		if overTLS {
			r.TLS = &tls.ConnectionState{}
		}
		srv.Handler().ServeHTTP(rec, r)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, discover(true))
	require.Empty(t, lines(), "a TLS client is not logged")
	require.Equal(t, http.StatusOK, discover(false))
	require.Equal(t, http.StatusOK, discover(false))
	require.Equal(t, []string{"laptop-x used plain http; upgrade it"}, lines(), "logged once, by name")
}
