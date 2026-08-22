package main

// rc_refusals_test.go: the remote-control surface's anonymous doors, each measured zero.
// Every rc endpoint's first act is the same question - "is this a logged-in owner?" - and
// the refusal is what stands between an anonymous caller and another person's session
// roster. rcOnline's clock policy rides along: a REVOKED session is offline whatever its
// last poll said, or "revoke" would leave a session showing green while dead.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

func TestRemoteControlRefusesTheAnonymous(t *testing.T) {
	b := relayBroker(store.NewMem())
	doors := map[string]struct {
		method, path string
		handler      http.HandlerFunc
	}{
		"sessions":   {http.MethodGet, "/rc/sessions", b.rcSessions},
		"revoke all": {http.MethodPost, "/rc/revoke-all", b.rcRevokeAll},
	}
	for name, d := range doors {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			d.handler(w, httptest.NewRequest(d.method, d.path, nil))
			require.Equal(t, http.StatusForbidden, w.Code,
				"%s answered an anonymous caller with %d", name, w.Code)
		})
		t.Run(name+": wrong method", func(t *testing.T) {
			other := http.MethodPost
			if d.method == http.MethodPost {
				other = http.MethodGet
			}
			w := httptest.NewRecorder()
			d.handler(w, httptest.NewRequest(other, d.path, nil))
			require.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})
	}
}

func TestRevokedSessionsAreOfflineWhateverTheirClockSays(t *testing.T) {
	b := relayBroker(store.NewMem())
	now := time.Now()
	fresh := store.RCSession{LastHostSeen: now.Unix()}
	require.True(t, b.rcOnline(fresh, now), "a host that just polled is online")
	require.False(t, b.rcOnline(store.RCSession{LastHostSeen: now.Unix(), Revoked: true}, now),
		"a revoked session showing green is a kill switch that did not kill")
	stale := store.RCSession{LastHostSeen: now.Add(-24 * time.Hour).Unix()}
	require.False(t, b.rcOnline(stale, now), "a day-silent host is not online")
}
