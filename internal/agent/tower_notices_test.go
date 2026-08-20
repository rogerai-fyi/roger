package agent

// tower_notices_test.go is the spec for what the relay plane is allowed to swallow.
//
// The rule it pins: routine progress may be discarded, and an error that costs the operator
// money or breaks their trust assumptions may not. Before the `--tower` flag was removed, all of
// this went to os.Stdout because the operator had asked for the plane. After, `roger share`
// passed one io.Discard for the whole output seam - so a node that computed a completion and was
// never paid for it, a node whose audits were failing, and a node that could not tell a real
// Core grant from one its relay invented all said exactly nothing.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towerhub"
)

// noticeSink collects what the relay plane insists on saying.
type noticeSink struct {
	mu   sync.Mutex
	msgs []string
}

func (n *noticeSink) report(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, err.Error())
}

func (n *noticeSink) sawContaining(sub string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, m := range n.msgs {
		if len(sub) > 0 && containsFold(m, sub) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// relayRig stands up the two servers a serving node talks to: Core (attach + key pin) and the
// relay's hub. The hub answers polls with an empty long poll and REFUSES the audit plane, which
// is the condition an operator most needs to hear about and least gets told.
func relayRig(t *testing.T) (broker string, notices *noticeSink) {
	t.Helper()
	hubMux := http.NewServeMux()
	// One job, then empty long polls. The job's grant is nonsense, so the executor returns a
	// failure - which does not matter here: the point is that the node COMPLETES the attempt and
	// the hub answers 202, meaning it took the completion and never couriered the receipt.
	var handed atomic.Bool
	hubMux.HandleFunc(towerhub.PathPoll, func(w http.ResponseWriter, _ *http.Request) {
		if handed.CompareAndSwap(false, true) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"attempt_id": "at-1", "station_id": "st-test",
				"grant":    base64.StdEncoding.EncodeToString([]byte("not-a-grant")),
				"envelope": base64.StdEncoding.EncodeToString([]byte("not-an-envelope")),
			})
			return
		}
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})
	// 202: the hub accepted the completion but has no dispatch record for it, so the receipt
	// never started its ride to Core. towerhub.ErrNotCarried - the node did the work and will
	// not be paid, which is precisely the error that used to go in the bin.
	hubMux.HandleFunc(towerhub.PathComplete, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	hubMux.HandleFunc(towerhub.PathAuditWanted, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the audit plane is unavailable", http.StatusInternalServerError)
	})
	hub := httptest.NewServer(hubMux)
	t.Cleanup(hub.Close)

	corePub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	envKey := make([]byte, 32)
	_, err = rand.Read(envKey)
	require.NoError(t, err)

	coreMux := http.NewServeMux()
	coreMux.HandleFunc("/tower/edge/attach", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(TowerAttachment{
			StationID: "st-test", TowerID: "tw-test",
			// host:port, exactly as a real tower advertises it and as both ingress points
			// validate it: net.SplitHostPort accepts nothing else, which is the whole reason
			// the hub channel cannot express https today.
			Endpoint: hub.Listener.Addr().String(),
			// Core still mints one, for towers serving nodes too old to sign. This node
			// receives it and never transmits it - see towerhub.Client's missing Token field.
			HubToken: "hub-token", State: "active",
			// The relay's admitted identity fingerprint. A current node refuses to attach
			// without one: it is what lets it tell the hub's own epoch from an on-path
			// attacker's, and the epoch is a value it signs over. These stub hubs publish no
			// epoch at all, so nothing here ever adopts one - the field is present because
			// AttachTower requires it, which is itself the point.
			TowerKeyHash: "00",
		})
	})
	coreMux.HandleFunc("/tower/dispatch/key", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"dispatch_key": hex.EncodeToString(corePub),
			"envelope_key": hex.EncodeToString(envKey),
		})
	})
	core := httptest.NewServer(coreMux)
	t.Cleanup(core.Close)
	return core.URL, &noticeSink{}
}

// The two notices a node owes its operator the moment it joins: that the hub channel is
// unencrypted, and that its audit answering is failing. Neither may ride the discarded writer.
func TestServeTowerSpeaksUpAboutTheThingsThatCost(t *testing.T) {
	broker, notices := relayRig(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeTower(ctx, Config{
			Broker: broker, Model: "m", Modality: "chat", Parallel: 1, Upstream: "http://127.0.0.1:1",
		}, priv, t.TempDir(), io.Discard, notices.report)
	}()

	require.Eventually(t, func() bool { return notices.sawContaining("UNENCRYPTED") }, 3*time.Second, 20*time.Millisecond,
		"the node joined a plaintext hub and said nothing about it")
	require.Eventually(t, func() bool { return notices.sawContaining("did not forward the receipt") }, 3*time.Second, 20*time.Millisecond,
		"the hub took a completion and never couriered the receipt, and the operator was told nothing")
	cancel()
	<-done
}

// THE NOTICE SAYS WHAT IS TRUE NOW - all of it, and not a word more.
//
// It used to say the polling token rides in the clear, which it did, and which was the reason to
// care. Signed hub polls removed the token from the wire entirely, so keeping that sentence would
// have taught operators to fear an exposure that no longer exists - and a standing alarm that
// overstates its case is the one people learn to skip past.
//
// The first correction then UNDERSTATED it, by naming traffic shape as the whole residual. It is
// not: every poll carries the station's long-term assertion public key in an X-Roger-Pubkey
// header, and that key is the operator's payment identity. An observer on this link learns a
// stable global identifier and the address it is coming from, which is a privacy exposure the
// operator cannot infer from "traffic shape". So this test pins both halves - no reusable
// credential, AND the identity that genuinely is on the wire - because an honest notice is one
// somebody could act on and each previous version was missing a different piece of it.
func TestThePlaintextNoticeNamesTheWholeResidualAndNoCredential(t *testing.T) {
	msg := ErrHubChannelPlaintext.Error()
	require.Contains(t, msg, "UNENCRYPTED", "the operator is still told the channel is not encrypted")
	require.Contains(t, msg, "shape of the traffic", "and told what an observer can see of the flow")
	// THE UNDERSTATEMENT THIS TEST WAS ADDED FOR. The stable key is the part an operator would
	// never guess from a note about volumes and timings.
	require.Contains(t, msg, "ASSERTION PUBLIC KEY",
		"the notice does not mention the long-term key every poll puts on the wire")
	require.Contains(t, msg, "link",
		"the notice names the key but not what an observer does with it - correlate it to an address")
	// And still no claim that something reusable is exposed, which is what signed polls fixed.
	for _, gone := range []string{"token", "credential"} {
		require.NotContains(t, msg, gone,
			"the notice still implies a reusable credential is exposed; signed polls removed it (%q)", gone)
	}
}

// A HUB THAT REFUSES THIS NODE'S IDENTITY IS NOT TRANSPORT CHATTER, and the relay plane's
// default output seam is a discard - so without this the node polls into a 401 every two seconds
// for the life of the process and the operator sees a station that simply never earns.
//
// The realistic cause is a version split: `roger` and `roger-tower` install and update
// separately, and a relay running a roger-tower older than signed polls cannot verify a
// signature, so it refuses every request a current node makes. Nothing about the next poll will
// differ from this one, which is exactly what separates it from a tower being down.
func TestARefusedIdentityIsSaidOutLoud(t *testing.T) {
	hubMux := http.NewServeMux()
	hubMux.HandleFunc(towerhub.PathPoll, func(w http.ResponseWriter, _ *http.Request) {
		// What a pre-signature hub answers a signed poll: it looked for a bearer token and
		// found none.
		http.Error(w, "not the registered node for this Station", http.StatusUnauthorized)
	})
	hubMux.HandleFunc(towerhub.PathAuditWanted, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not the registered node for this Station", http.StatusUnauthorized)
	})
	hub := httptest.NewServer(hubMux)
	t.Cleanup(hub.Close)

	corePub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	envKey := make([]byte, 32)
	_, err = rand.Read(envKey)
	require.NoError(t, err)
	coreMux := http.NewServeMux()
	coreMux.HandleFunc("/tower/edge/attach", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(TowerAttachment{
			StationID: "st-test", TowerID: "tw-old",
			// A fingerprint is present because attach requires one; this pre-signature hub
			// publishes no epoch header at all, so nothing here is ever adopted and the 401
			// stays what the test is about - a refused identity, not an unproved epoch.
			Endpoint: hub.Listener.Addr().String(), TowerKeyHash: "00", State: "active",
		})
	})
	coreMux.HandleFunc("/tower/dispatch/key", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"dispatch_key": hex.EncodeToString(corePub),
			"envelope_key": hex.EncodeToString(envKey),
		})
	})
	core := httptest.NewServer(coreMux)
	t.Cleanup(core.Close)

	notices := &noticeSink{}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeTower(ctx, Config{
			Broker: core.URL, Model: "m", Modality: "chat", Parallel: 1, Upstream: "http://127.0.0.1:1",
		}, priv, t.TempDir(), io.Discard, notices.report)
	}()
	require.Eventually(t, func() bool { return notices.sawContaining("refuses its identity") },
		5*time.Second, 20*time.Millisecond,
		"the hub refused this node on every poll and the operator was never told")
	cancel()
	<-done
}

// The classification itself, stated as a rule rather than inferred from a log line: WORK DONE
// AND UNPAID is loud, transport chatter is not.
func TestOnlyWorkDoneAndUnpaidIsLoud(t *testing.T) {
	require.True(t, costlyRelayError(towerhub.ErrNotCarried),
		"the hub took the completion and never couriered the receipt - the node computed and will not be paid")
	require.True(t, costlyRelayError(towerhub.ErrResultUndelivered),
		"the node served and could not hand the answer back - the GPU time is spent either way")
	require.False(t, costlyRelayError(errors.New("wrapped: "+towerhub.ErrNotCarried.Error())),
		"classification is by sentinel, not by string matching")
	require.False(t, costlyRelayError(&towerhub.HTTPError{Status: 502, Body: "bad gateway"}),
		"a poll that could not reach the hub is retried by the loop and costs nothing")
	require.False(t, costlyRelayError(context.Canceled))

	// A refused IDENTITY is its own class: no work was done, so it is not costly, but it will
	// never come right on retry either - which is what earns it the notice channel.
	require.True(t, hubRefusedIdentity(&towerhub.HTTPError{Status: 401, Body: "no"}))
	require.False(t, hubRefusedIdentity(&towerhub.HTTPError{Status: 502, Body: "bad gateway"}))
	require.False(t, hubRefusedIdentity(context.Canceled))
}

// hubBaseURL's comment used to promise a capability the wire format cannot express: it claimed
// an endpoint carrying its own scheme would be honoured, on a wire that refuses one. This is the
// property that replaces the promise - the scheme is decided by the PIN Core publishes beside
// the endpoint, and by nothing else.
func TestHubBaseURLTakesItsSchemeFromThePin(t *testing.T) {
	// No pin: exactly what every relay does today, and it says so.
	base, client, plaintext, err := hubBaseURL("203.0.113.9:8443", "", nil)
	require.NoError(t, err)
	require.Equal(t, "http://203.0.113.9:8443", base)
	require.True(t, plaintext, "a relay that published no certificate pin serves plaintext")
	require.Nil(t, client.Transport, "an unpinned client is the caller's, untouched")

	// A pin: https, and a transport that will check the certificate. Verifying the CHECK
	// rather than the string is the point - a test that only looked at the "https" prefix
	// would pass just as happily against a client with verification switched off.
	pin := strings.Repeat("ab", 32)
	base, client, plaintext, err = hubBaseURL("203.0.113.9:8443", pin, nil)
	require.NoError(t, err)
	require.Equal(t, "https://203.0.113.9:8443", base)
	require.False(t, plaintext)
	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "a pinned client dials through its own transport")
	require.NotNil(t, tr.TLSClientConfig.VerifyPeerCertificate,
		"a pinned client that verifies nothing is the thing this whole change removes")
	require.Equal(t, uint16(tls.VersionTLS13), tr.TLSClientConfig.MinVersion)

	// A MALFORMED PIN IS A REFUSAL, NOT A FALLBACK. Silently dropping to http here would be a
	// downgrade reachable by corrupting one field.
	_, _, _, err = hubBaseURL("203.0.113.9:8443", "not-a-fingerprint", nil)
	require.Error(t, err)
}

// A PRIVATE BAND NEVER REACHES THE FABRIC, and the guarantee is structural rather than a
// consequence of where the call happens to sit in cmd/rogerai/main.go.
//
// This is the test that catches the merge. Merging `roger share`'s two agentStart branches -
// which the duplicated setup in that function visibly invites - used to publish private bands to
// the public relay fabric with no compile error and nothing going red.
func TestAPrivateShareNeverAttachesToTheRelayFabric(t *testing.T) {
	var dialled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dialled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cfg := Config{Broker: srv.URL, Model: "m", Modality: "chat", Private: true}

	_, _, aerr := AttachTower(cfg, priv, t.TempDir())
	require.ErrorIs(t, aerr, ErrPrivateShareNeverRelays)

	serr := ServeTower(context.Background(), cfg, priv, t.TempDir(), io.Discard, nil)
	require.ErrorIs(t, serr, ErrPrivateShareNeverRelays)

	require.False(t, dialled, "a private share opened a connection to the relay fabric")
}

// A HUB WHOSE CERTIFICATE IS NOT THE ONE CORE NAMED IS AN ALARM, NOT A BLIP.
//
// The failure arrives at the serve loop as an ordinary transport error, and the loop's honest
// response to those is to retry every two seconds into a discarded writer. That is right for a
// hub that is down and wrong for this: the pin was handed to this node at attach and is held for
// the life of the process, so retrying cannot fix it - either somebody is on the path, or the
// relay replaced its certificate and Core has not been told. Either way this station is not
// earning, and without this line nobody would ever find out why.
//
// The test also pins the OTHER half: a node given a pin does not print the plaintext warning.
// An alarm that fires on the encrypted channel is how operators learn to skip alarms.
func TestACertificateCoreDidNotNameIsSaidOutLoud(t *testing.T) {
	// A real TLS hub, and a pin for a DIFFERENT certificate - the on-path attacker, and equally
	// a relay that rotated its key. Any handler will do: nothing gets past the handshake.
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(hub.Close)
	wrongPin := strings.Repeat("ab", 32)

	corePub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	envKey := make([]byte, 32)
	_, err = rand.Read(envKey)
	require.NoError(t, err)
	coreMux := http.NewServeMux()
	coreMux.HandleFunc("/tower/edge/attach", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(TowerAttachment{
			StationID: "st-test", TowerID: "tw-test",
			Endpoint: hub.Listener.Addr().String(), EndpointTLSSPKI: wrongPin,
			HubToken: "hub-token", State: "active", TowerKeyHash: "00",
		})
	})
	coreMux.HandleFunc("/tower/dispatch/key", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"dispatch_key": hex.EncodeToString(corePub),
			"envelope_key": hex.EncodeToString(envKey),
		})
	})
	core := httptest.NewServer(coreMux)
	t.Cleanup(core.Close)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	notices := &noticeSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeTower(ctx, Config{
			Broker: core.URL, Model: "m", Modality: "chat", Parallel: 1, Upstream: "http://127.0.0.1:1",
		}, priv, t.TempDir(), io.Discard, notices.report)
	}()

	require.Eventually(t, func() bool { return notices.sawContaining("not the one Roger Core named") },
		3*time.Second, 20*time.Millisecond,
		"this node is talking to a hub Core did not name, or to nobody, and said neither")
	require.False(t, notices.sawContaining("UNENCRYPTED"),
		"the link is encrypted - warning about plaintext here is how a real alarm gets ignored")
	cancel()
	<-done
}
