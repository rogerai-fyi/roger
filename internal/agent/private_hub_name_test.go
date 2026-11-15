package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Review finding: the pin rule for a private hub covered only a literal address, so a hub NAME
// that resolves to the Station's own network was dialed unpinned. An unpinned hub given by name
// is now checked at the socket, on the address it resolves to, every time it is dialed.
func TestAnUnpinnedHubNameThatResolvesPrivateIsRefusedAtTheSocket(t *testing.T) {
	for _, tc := range []struct {
		ip      string
		refused bool
	}{
		{"192.168.1.20", true}, {"10.0.0.5", true}, {"172.16.4.4", true}, {"fd12::20", true},
		{"100.64.1.9", true}, {"203.0.113.7", false}, {"2001:db8::1", false}, {"127.0.0.1", false},
	} {
		err := unpinnedHubVet(net.ParseIP(tc.ip))
		require.Equal(t, tc.refused, err != nil, "%s: %v", tc.ip, err)
	}

	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer hub.Close()
	_, port, _ := net.SplitHostPort(hub.Listener.Addr().String())
	byName := net.JoinHostPort("localhost", port)

	// The vet runs on the resolved address of a name: one that refuses loopback refuses localhost.
	prev := unpinnedHubVet
	t.Cleanup(func() { unpinnedHubVet = prev })
	unpinnedHubVet = func(ip net.IP) error {
		if ip.IsLoopback() {
			return errors.New("a private hub needs a certificate pin from Roger Core")
		}
		return nil
	}
	base, client, plaintext, err := hubBaseURL(byName, "", &http.Client{Timeout: 3 * time.Second})
	require.NoError(t, err)
	require.True(t, plaintext)
	require.Equal(t, 3*time.Second, client.Timeout, "the caller's timeout is kept")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/", nil)
	_, err = client.Do(req)
	require.ErrorContains(t, err, "certificate pin", "a name resolving to a refused address was dialed")

	// The real rule leaves the same-machine rig alone.
	unpinnedHubVet = prev
	base, client, _, err = hubBaseURL(byName, "", &http.Client{Timeout: 3 * time.Second})
	require.NoError(t, err)
	resp, err := client.Get(base + "/")
	require.NoError(t, err)
	_ = resp.Body.Close()
}
