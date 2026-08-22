package main

import (
	"bytes"
	"testing"

	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"rogerai.fm/roger/v6/internal/station"
	"rogerai.fm/roger/v6/internal/towercore/dispatch"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
	"time"
)

// probe refuses its missing arguments by name, before touching the network.
func TestProbeRefusesMissingArguments(t *testing.T) {
	var b bytes.Buffer
	require.ErrorContains(t, cmdProbe(nil, &b), "--model")
	t.Setenv("ROGER_BROKER", "")
	require.ErrorContains(t, cmdProbe([]string{"--model", "m"}, &b), "--broker")
	require.Error(t, cmdProbe([]string{"--wat"}, &b))
	require.Error(t, cmdProbe([]string{"--model", "m", "--broker", "http://x", "--ca", "/no/such"}, &b))
	require.Error(t, cmdProbe([]string{"--model", "m", "--broker", "http://x", "--body", "/no/such"}, &b))
}

// A probe against a broker that refuses authorize reports the failure rather than pretending
// to have run.
func TestProbeReportsAnAuthorizeFailure(t *testing.T) {
	var b bytes.Buffer
	err := cmdProbe([]string{"--model", "m", "--broker", "http://127.0.0.1:1"}, &b)
	require.ErrorContains(t, err, "authorize failed")
}

func TestUsageMentionsProbe(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, run(nil, &b))
	require.Contains(t, b.String(), "probe")
}

// The --body flag's two halves. A probe with a caller-supplied body is how an operator
// reproduces a specific failing request; the file being unreadable must be its own clear
// error, and a readable one must actually be sent into the attempt.
func TestProbeNamesAnUnreadableBodyFile(t *testing.T) {
	var b bytes.Buffer
	err := cmdProbe([]string{"--model", "m", "--broker", "http://127.0.0.1:9",
		"--body", filepath.Join(t.TempDir(), "no-such-file")}, &b)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no-such-file")
}

func TestProbeReadsTheBodyFileBeforeAuthorizing(t *testing.T) {
	// The authorize still fails (nothing listens at the broker), but the failure must be the
	// AUTHORIZE failure - proof the body file was read and accepted first.
	body := filepath.Join(t.TempDir(), "body.json")
	require.NoError(t, os.WriteFile(body, []byte(`{"probe":true,"mine":1}`), 0o600))
	var b bytes.Buffer
	err := cmdProbe([]string{"--model", "m", "--broker", "http://127.0.0.1:9", "--body", body}, &b)
	require.Error(t, err)
	require.Contains(t, err.Error(), "authorize failed")
}

// The probe's whole point, run for real: authorize against a Core, submit sealed through a
// hub, get the answer back, acknowledge. Everything else in this file tests the refusals;
// this is the one that proves the tool an operator is told to trust ("drives the whole
// sealed loop as a consumer") actually drives it. The world is the same shape
// internal/edgeclient builds for its own loop test - a real station executor serving
// through a real towerhub - because a probe that only ever failed in tests would be a
// diagnostic nobody had diagnosed.
func TestProbeDrivesTheWholeSealedLoop(t *testing.T) {
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	// The serving node: a real station executor answering a fixed body.
	st, err := station.Init(t.TempDir())
	require.NoError(t, err)
	exec := station.EdgeExecutor{
		Station: st, CoreKey: corePub, Network: link.PublicNetwork,
		Upstream: probeUpstream(`{"answer":42}`),
		Outbox:   station.NewOutbox(4), Seen: station.NewAttemptCache(),
	}

	// The tower hub the node polls and the probe submits to.
	hub := towerhub.NewServer(towerhub.New(), func(grant []byte) (string, string, error) {
		att, stationID, _, gerr := dispatch.EdgeGrantMeta(grant, corePub, link.PublicNetwork, "tw-1", time.Now())
		return att, stationID, gerr
	}, towerhub.ServerOptions{TowerID: "tw-1", SubmitTTL: 10 * time.Second, PollTTL: 200 * time.Millisecond})
	hub.RegisterNode(st.StationID, towerhub.NodeAuth{AssertionKey: st.AssertionPub()})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathSubmit, hub.Submit)
	mux.HandleFunc(towerhub.PathPoll, hub.Poll)
	mux.HandleFunc(towerhub.PathComplete, hub.Complete)
	hubSrv := httptest.NewServer(mux)
	t.Cleanup(hubSrv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	nodeClient := &towerhub.Client{BaseURL: hubSrv.URL, TowerID: "tw-1",
		TowerKeyHash: hub.EpochKeyHash(), Sign: st.SignRequest,
		HTTP: &http.Client{Timeout: 5 * time.Second}}
	go func() {
		_ = towerhub.ServeLoop(ctx, nodeClient, st.StationID, probeServe{exec}, nil)
	}()

	// Roger Core: authorize mints a grant for the hub above; ack just records.
	reg := dispatch.NewWithStore(dispatch.Config{
		Network: link.PublicNetwork, Signer: corePriv, Lifetime: time.Minute,
	}, nil)
	coreMux := http.NewServeMux()
	coreMux.HandleFunc("/tower/edge/authorize", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			ConsumerEnvKey string `json:"consumer_env_key"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		require.NoError(t, json.Unmarshal(body, &req))
		envKey, kerr := hex.DecodeString(req.ConsumerEnvKey)
		require.NoError(t, kerr)
		g, gerr := reg.MintEdge(dispatch.EdgeTarget{
			TowerID: "tw-1", StationID: st.StationID, Model: "probe-model", Modality: "text",
			RelayName: "st.relay.example", MaxIn: 1 << 20, MaxOut: 1 << 20,
			AssertionKey: st.AssertionPub(), ConsumerKey: probeConsumerPub(t),
			ConsumerEnvKey: envKey, PriceOutMicros: 100,
		})
		require.NoError(t, gerr)
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"attempt_id": g.AttemptID, "grant": base64.StdEncoding.EncodeToString(g.Signed),
			"endpoint":            hubSrv.Listener.Addr().String(),
			"station_session_key": hex.EncodeToString(st.SessionPub()),
			"price_out_micros":    100,
		})
	})
	acked := make(chan struct{}, 1)
	coreMux.HandleFunc("/tower/edge/ack", func(rw http.ResponseWriter, r *http.Request) {
		select {
		case acked <- struct{}{}:
		default:
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"recorded": true})
	})
	coreSrv := httptest.NewServer(coreMux)
	t.Cleanup(coreSrv.Close)

	var b bytes.Buffer
	require.NoError(t, cmdProbe([]string{"--model", "probe-model", "--broker", coreSrv.URL}, &b))
	require.Contains(t, b.String(), "edge path OK",
		"the probe's own verdict line is what an operator greps for")
	require.Contains(t, b.String(), "acknowledged - this attempt settles corroborated")
	select {
	case <-acked:
	default:
		t.Fatal("the probe said it acknowledged but Core never received the ack")
	}
}

type probeUpstream string

func (u probeUpstream) Serve(context.Context, []byte) ([]byte, error) { return []byte(u), nil }

type probeServe struct{ e station.EdgeExecutor }

func (s probeServe) Serve(ctx context.Context, grant, env []byte) ([]byte, []byte, string) {
	return s.e.ServeSealed(ctx, grant, env)
}

func probeConsumerPub(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub
}
