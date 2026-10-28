package edge

// Executable pins for features/edge/serving_probe.feature, the discovery half: after a node is
// certificate-verified, discovery runs the serving probe on each instance whose serve is still
// CLAIMED and records a pass on THAT instance only.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/cert"
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
	require.Equal(t, []string{"10.0.0.2:44121 ab12 qwen", "10.0.0.2:44121 ab12 qwen"}, asked,
		"one probe per claimed instance's single band, at the address and pin just verified")
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

// The wiring, end to end inside discovery: a certificate-verified sighting runs the probe and the
// fleet row comes out VERIFIED - not only verifyServe called by hand (audit 2026-09-27).
func TestConsiderRunsTheServingProbeAfterVerifyingAPeer(t *testing.T) {
	authority, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	id := NodeID(pub)
	leaf, err := authority.Issue(id, pub)
	require.NoError(t, err)
	fp := Fingerprint(leaf.Raw)
	f := NewFleet(store.NewMem(), "acct-1")
	probed := []string{}
	d := New(Options{
		Fleet: f, Authority: authority, CallerCert: &tls.Certificate{},
		Self: Advert{NodeID: "n_self", Account: "acct-1"},
		Dial: func(context.Context, string) (*Peer, error) {
			return &Peer{Cert: leaf, Fingerprint: fp, Describe: Describe{NodeID: id, Account: "acct-1", Kind: string(Host),
				Caps: []string{string(Serve)}, Instances: []Instance{claimServe("serve", "qwen")}}}, nil
		},
		Probe: func(_ context.Context, addr, pin string, _ *tls.Certificate, band string) (string, error) {
			probed = append(probed, addr+" "+pin+" "+band)
			return "serve", nil
		},
	})
	var rep Report
	d.consider(context.Background(), Advert{NodeID: id, Account: "acct-1", Kind: string(Host), IP: net.ParseIP("10.0.0.2"), Port: 44121, Fingerprint: fp}, &rep)
	require.Len(t, rep.Verified, 1)
	require.Equal(t, []string{"10.0.0.2:44121 " + fp + " qwen"}, probed, "probed at the address and pin just verified")
	require.Equal(t, string(Verified), serveStateOf(t, f, id, "serve"))
}

// EVERY band an instance advertises must answer: proving one and marking the instance would let the
// fleet route a peer the band it cannot serve (features/edge/serving_probe.feature).
func TestDiscoveryRequiresEveryBandToAnswer(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	n, err := f.Observe("n_desk", Observation{Name: "desk", Kind: string(Host), Caps: []Capability{Serve},
		Addr: "10.0.0.2:44121", Fingerprint: "ab12",
		Instances: []Instance{claimServe("serve", "qwen", "mistral")}})
	require.NoError(t, err)

	only := New(Options{Fleet: f, CallerCert: &tls.Certificate{}, Probe: func(_ context.Context, _, _ string, _ *tls.Certificate, band string) (string, error) {
		if band != "qwen" {
			return "", errors.New("the server behind mistral does not list it")
		}
		return "serve", nil
	}})
	only.verifyServe(context.Background(), "10.0.0.2:44121", "ab12", n)
	require.Equal(t, string(Claimed), serveStateOf(t, f, "n_desk", "serve"), "one band is not the set")

	both := New(Options{Fleet: f, CallerCert: &tls.Certificate{}, Probe: func(context.Context, string, string, *tls.Certificate, string) (string, error) {
		return "serve", nil
	}})
	both.verifyServe(context.Background(), "10.0.0.2:44121", "ab12", n)
	require.Equal(t, string(Verified), serveStateOf(t, f, "n_desk", "serve"))
}

// A peer seen a second time while its serve is still CLAIMED has proven nothing: its advertised bands
// must not be filed as proven, or verifyServe skips it on every later pass and its serve is never
// verified (found 2026-10-09: a member that joined after a peer was already serving saw that peer's
// serve stay claimed forever). Only an earlier VERIFIED serve with no recorded band set may stand on
// its band list, the shape of a record written before bands were recorded.
// Pins features/edge/serving_probe.feature "a claimed serve seen again is still probed".
func TestASecondSightingOfAClaimedServeIsStillProbed(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	observedDesk(t, f)
	n := observedDesk(t, f) // the next pass: same instances, serve never probed yet
	for _, in := range n.Instances {
		require.Empty(t, in.ServeProven, "%s: a claimed serve has proven no band", in.Name)
	}
	calls := 0
	d := New(Options{Fleet: f, CallerCert: &tls.Certificate{}, Probe: func(context.Context, string, string, *tls.Certificate, string) (string, error) {
		calls++
		return "serve", nil
	}})
	d.verifyServe(context.Background(), "10.0.0.2:44121", "ab12", n)
	require.Equal(t, 2, calls, "both claimed instances are probed on the second sighting")
	require.Equal(t, string(Verified), serveStateOf(t, f, "n_desk", "serve"))
}

// Pins features/edge/serving_probe.feature "a serve that comes back after a restart is probed again".
// A peer that reboots stops serving for a while and then serves the same band again. Its earlier
// probe verdict lapsed with the serve, so the returning serve is claimed and must be probed again:
// a proven-band list left over from before the reboot must not let verifyServe skip it.
func TestAServeThatComesBackAfterARestartIsProbedAgain(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	obs := func(in Instance) store.EdgeNode {
		t.Helper()
		n, err := f.Observe("n_pi", Observation{Name: "pi-1", Kind: string(Host), Caps: []Capability{Serve},
			Addr: "10.0.0.5:44100", Fingerprint: "cd34", Instances: []Instance{in}})
		require.NoError(t, err)
		return n
	}
	n := obs(claimServe("pi-1", "smollm"))
	calls := 0
	d := New(Options{Fleet: f, CallerCert: &tls.Certificate{}, Probe: func(context.Context, string, string, *tls.Certificate, string) (string, error) {
		calls++
		return "pi-1", nil
	}})
	d.verifyServe(context.Background(), "10.0.0.5:44100", "cd34", n)
	require.Equal(t, string(Verified), serveStateOf(t, f, "n_pi", "pi-1"))

	// the reboot: the instance is back, still operating, but serves nothing yet
	obs(Instance{Name: "pi-1", Caps: []store.EdgeCap{{Name: string(Operate), State: string(Claimed)}}})
	// it serves the same band again
	n = obs(claimServe("pi-1", "smollm"))
	require.Equal(t, string(Claimed), serveStateOf(t, f, "n_pi", "pi-1"), "the returning serve starts claimed")
	for _, in := range n.Instances {
		require.Empty(t, in.ServeProven, "the proof lapsed with the restart")
	}
	d.verifyServe(context.Background(), "10.0.0.5:44100", "cd34", n)
	require.Equal(t, 2, calls, "the returning serve is probed again")
	require.Equal(t, string(Verified), serveStateOf(t, f, "n_pi", "pi-1"))
}
