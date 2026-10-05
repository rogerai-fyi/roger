package agent

// probe_min_wire_test.go pins the WIRE contract for the operator-declared minimum probe
// interval: Config.ProbeMin lands in the signed register body as probe_min_s (whole
// seconds), and an unset one is OMITTED so an operator who never declares it registers
// byte-identically to before. Real HTTP against an httptest broker; no mocks.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

func TestRegisterProbeMinOnWire(t *testing.T) {
	for _, tc := range []struct {
		name     string
		probeMin time.Duration
		want     int
	}{
		{"six hours rides the registration", 6 * time.Hour, 21600},
		{"whole seconds", 90 * time.Second, 90},
		{"unset is omitted", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tmp)
			t.Setenv("HOME", tmp)

			var mu sync.Mutex
			var regBody []byte
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/nodes/register") {
					b, _ := io.ReadAll(r.Body)
					mu.Lock()
					regBody = b
					mu.Unlock()
				}
				_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			}))
			defer broker.Close()

			sess, err := Start(Config{Broker: broker.URL, Upstream: "http://127.0.0.1:0", NodeID: "n-pm",
				Model: "m", Parallel: 1, ProbeMin: tc.probeMin})
			require.NoError(t, err)
			defer sess.Stop()

			mu.Lock()
			body := regBody
			mu.Unlock()
			var reg protocol.NodeRegistration
			require.NoError(t, json.Unmarshal(body, &reg))
			require.Equal(t, tc.want, reg.ProbeMinSeconds)
			require.Equal(t, tc.want != 0, strings.Contains(string(body), `"probe_min_s"`))
			require.True(t, reg.VerifyRegistration(), "the declared interval must be covered by the node's signature")
		})
	}
}
