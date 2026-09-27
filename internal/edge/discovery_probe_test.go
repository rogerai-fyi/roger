package edge

// Executable pins for features/edge/serving_probe.feature, the discovery half: after a node is
// certificate-verified, discovery runs the serving probe on each instance whose serve is still
// CLAIMED and records a pass on THAT instance only.

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

func claimServe(name string, bands ...string) Instance {
	return Instance{Name: name, Bands: bands, Caps: []store.EdgeCap{{Name: string(Serve), State: string(Claimed)}}}
}

func serveStateOf(t *testing.T, f *Fleet, id, inst string) string {
	t.Helper()
	n, ok, err := f.Get(id)
	require.NoError(t, err)
	require.True(t, ok)
	for _, in := range n.Instances {
		if in.Name != inst {
			continue
		}
		for _, c := range in.Caps {
			if c.Name == string(Serve) {
				return c.State
			}
		}
	}
	return ""
}

func observedDesk(t *testing.T, f *Fleet) store.EdgeNode {
	t.Helper()
	n, err := f.Observe("n_desk", Observation{Name: "desk", Kind: string(Host), Caps: []Capability{Serve},
		Addr: "10.0.0.2:44121", Fingerprint: "ab12",
		Instances: []Instance{claimServe("serve", "qwen"), claimServe("lab", "qwen")}})
	require.NoError(t, err)
	return n
}

func TestDiscoveryVerifiesServeOnTheInstanceTheProbePassed(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	n := observedDesk(t, f)
	var asked []string
	d := New(Options{Fleet: f, CallerCert: &tls.Certificate{}, Probe: func(_ context.Context, addr, pin string, _ *tls.Certificate, band string) (string, error) {
		asked = append(asked, addr+" "+pin+" "+band)
		return "serve", nil
	}})
	d.verifyServe(context.Background(), "10.0.0.2:44121", "ab12", n)
	require.Equal(t, string(Verified), serveStateOf(t, f, "n_desk", "serve"))
	require.Equal(t, string(Claimed), serveStateOf(t, f, "n_desk", "lab"), "the probe named 'serve', not 'lab'")
	require.Equal(t, []string{"10.0.0.2:44121 ab12 qwen", "10.0.0.2:44121 ab12 qwen"}, asked, "one probe per claimed instance, at the address and pin just verified")
}

func TestDiscoveryLeavesServeClaimedWhenTheProbeFails(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	n := observedDesk(t, f)
	d := New(Options{Fleet: f, CallerCert: &tls.Certificate{}, Probe: func(context.Context, string, string, *tls.Certificate, string) (string, error) {
		return "", errors.New("the server behind qwen does not list it")
	}})
	d.verifyServe(context.Background(), "10.0.0.2:44121", "ab12", n)
	require.Equal(t, string(Claimed), serveStateOf(t, f, "n_desk", "serve"))
}

func TestDiscoveryDoesNotReprobeAVerifiedInstanceNorProbeUnenrolled(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	n := observedDesk(t, f)
	require.NoError(t, f.RecordInstanceProbe("n_desk", "serve", Serve, true))
	require.NoError(t, f.RecordInstanceProbe("n_desk", "lab", Serve, true))
	n, _, _ = f.Get("n_desk")
	calls := 0
	probe := func(context.Context, string, string, *tls.Certificate, string) (string, error) {
		calls++
		return "serve", nil
	}
	New(Options{Fleet: f, CallerCert: &tls.Certificate{}, Probe: probe}).verifyServe(context.Background(), "10.0.0.2:44121", "ab12", n)
	require.Equal(t, 0, calls, "already VERIFIED: nothing to prove again")

	f2 := NewFleet(store.NewMem(), "acct-1")
	n2 := observedDesk(t, f2)
	New(Options{Fleet: f2, Probe: probe}).verifyServe(context.Background(), "10.0.0.2:44121", "ab12", n2)
	require.Equal(t, 0, calls, "no certificate to present: nothing is probed")
	require.Equal(t, string(Claimed), serveStateOf(t, f2, "n_desk", "serve"))
}
