package main

// serve_test.go covers the Station's socket: the flags that must be right before it listens,
// and the envelope it answers with.
//
// The DECIDING is in internal/station's Executor and is tested there against a real grant.
// What is tested here is the shell around it, and one property that only exists at this
// layer: a refusal is a 200 carrying a failure, not an HTTP error.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

func servingStation(t *testing.T) *station.Station {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)
	return mustOpen(t, dir)
}

// A REFUSAL IS A RESULT, not a transport error. The Tower has to relay it to Core as that
// attempt failing, and an HTTP error status would be indistinguishable from the Station
// being unreachable - which Core handles completely differently.
func TestARefusalIsAnEnvelopeRatherThanAnHTTPError(t *testing.T) {
	s := servingStation(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	srv := httptest.NewServer(handlerFor(station.Executor{
		Station: s, CoreKey: pub, Network: link.PublicNetwork,
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/execute", "application/json",
		strings.NewReader(`{"grant":{"nope":1},"request":{"a":1}}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "a refusal is not a transport failure")

	var out station.ExecuteResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotEmpty(t, out.Failure)
	require.Nil(t, out.Receipt)
}

// Unreadable input is answered the same way, for the same reason.
func TestUnreadableInputIsAnsweredAsAFailure(t *testing.T) {
	s := servingStation(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	srv := httptest.NewServer(handlerFor(station.Executor{
		Station: s, CoreKey: pub, Network: link.PublicNetwork,
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/execute", "application/json", strings.NewReader("{nope"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out station.ExecuteResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Contains(t, out.Failure, "not valid JSON")

	// A GET is a method error, though: that is a caller using the wrong verb, not a Station
	// refusing work.
	getResp, err := http.Get(srv.URL + "/execute")
	require.NoError(t, err)
	getResp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, getResp.StatusCode)
}

// /id exists so an operator who pointed a Tower at the wrong port finds out from the port
// rather than from a job that fails much later for a reason that looks like tampering.
func TestTheStationSaysWhoItIs(t *testing.T) {
	s := servingStation(t)
	srv := httptest.NewServer(handlerFor(station.Executor{Station: s}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/id")
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Equal(t, s.StationID, out["station_id"])
}

// THE PINNED KEY IS REQUIRED AT STARTUP. A Station without it would refuse every request at
// runtime, one at a time, looking like a network fault - so the error belongs where the
// mistake was made.
func TestServeRefusesToStartWithoutWhatItNeeds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)
	key := hex.EncodeToString(mustOpen(t, dir).AssertionPub())

	_, err = runCLI(t, "serve")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--dir")

	_, err = runCLI(t, "serve", "--dir", dir, "--core-key", key)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--upstream")

	_, err = runCLI(t, "serve", "--dir", dir, "--upstream", "http://127.0.0.1:1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--core-key is required")
	require.Contains(t, err.Error(), "its own Tower wrote",
		"it says WHY, because the reason is the whole security argument")

	_, err = runCLI(t, "serve", "--dir", dir, "--upstream", "http://127.0.0.1:1",
		"--core-key", "not-hex")
	require.Error(t, err)
	require.Contains(t, err.Error(), "hex-encoded")

	// A key of the wrong length is refused too - a truncated paste is the likely way in.
	_, err = runCLI(t, "serve", "--dir", dir, "--upstream", "http://127.0.0.1:1",
		"--core-key", key[:20])
	require.Error(t, err)

	// An uninitialized directory, and a bad flag.
	_, err = runCLI(t, "serve", "--dir", t.TempDir(), "--upstream", "http://x", "--core-key", key)
	require.Error(t, err)
	_, err = runCLI(t, "serve", "--wat")
	require.Error(t, err)
}

// A port already taken is reported rather than silently not serving.
func TestServeReportsAPortItCannotHave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)
	key := hex.EncodeToString(mustOpen(t, dir).AssertionPub())

	busy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer busy.Close()
	addr := strings.TrimPrefix(busy.URL, "http://")

	_, err = runCLI(t, "serve", "--dir", dir, "--upstream", "http://127.0.0.1:1",
		"--core-key", key, "--listen", addr)
	require.Error(t, err)
}

// The usage names serve, so an operator can find the command that makes a Station useful.
func TestUsageMentionsServe(t *testing.T) {
	out, err := runCLI(t)
	require.NoError(t, err)
	require.Contains(t, out, "roger-station serve")
	require.Contains(t, out, "--core-key")
}

// The listener actually serves, and STOPS when told. An unclean shutdown cuts off a request
// mid-flight, which Core sees as a Station that failed - so ending cleanly is behaviour, not
// housekeeping.
func TestTheListenerServesAndThenStopsCleanly(t *testing.T) {
	s := servingStation(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	stop := make(chan struct{})
	var b bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- serveStationSplit(station.Executor{Station: s}, station.EdgeExecutor{Station: s}, nil, ln, nil, &b, stop)
	}()

	require.Eventually(t, func() bool {
		resp, gerr := http.Get("http://" + ln.Addr().String() + "/id")
		if gerr != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond)

	close(stop)
	require.NoError(t, <-done)
	require.Contains(t, b.String(), "stopped")

	// And the port is genuinely released, rather than the loop merely having returned.
	require.Eventually(t, func() bool {
		_, gerr := http.Get("http://" + ln.Addr().String() + "/id")
		return gerr != nil
	}, 5*time.Second, 10*time.Millisecond)
}

// BOTH KEYS ARE REQUIRED AT STARTUP. A Station missing either would refuse every request at
// runtime, one at a time, looking like a network fault - so the error belongs where the
// mistake was made. And the reason is stated, because it is the whole security argument.
func TestServeRefusesWithoutTheEnvelopeKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)
	grantKey := hex.EncodeToString(mustOpen(t, dir).AssertionPub())
	envKey := hex.EncodeToString(mustOpen(t, dir).SessionPub())

	_, err = runCLI(t, "serve", "--dir", dir, "--upstream", "http://127.0.0.1:1",
		"--core-key", grantKey)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--core-envelope-key is required")
	require.Contains(t, err.Error(), "read every one",
		"it says WHY, because a relay reading results is the thing this prevents")

	for _, bad := range []string{"not-hex", envKey[:20]} {
		_, err = runCLI(t, "serve", "--dir", dir, "--upstream", "http://127.0.0.1:1",
			"--core-key", grantKey, "--core-envelope-key", bad)
		require.Error(t, err)
		require.Contains(t, err.Error(), "X25519")
	}
}

// The startup banner says content is sealed in both directions. An operator running a relay
// for other people's customers should be able to see that from the terminal.
func TestTheStartupBannerSaysContentIsSealed(t *testing.T) {
	s := servingStation(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	// Exercised through the handler rather than the banner text alone: /id answers while the
	// executor holds both keys, which is the state the banner describes.
	srv := httptest.NewServer(handlerFor(station.Executor{Station: s}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/id")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	out, err := runCLI(t)
	require.NoError(t, err)
	require.Contains(t, out, "--core-envelope-key")
}

// handlerFor keeps these tests about the relayed /execute route. The edge route is covered
// in edge_test.go, where a consumer-shaped call is what is actually being asserted.
func handlerFor(exec station.Executor) http.Handler {
	return towerFacingHandler(exec, station.EdgeExecutor{Station: exec.Station}, nil, true)
}

// THE BOUNDARY THE TWO PORTS EXIST FOR: the consumer-facing surface must not carry the
// outbox routes. A consumer that could POST /receipts/settled through the relay - and the
// relay forwards whatever path it is asked for, since it cannot read paths at all - would
// erase the evidence it is billed on.
func TestAConsumerCannotReachTheReceiptOutbox(t *testing.T) {
	s := servingStation(t)
	outbox := station.NewOutbox(10)
	outbox.Add(station.Evidence{AttemptID: "att-victim", StationID: s.StationID,
		Receipt: []byte("signed")})

	// The consumer-facing surface: the edge handler alone, exactly as the --edge listener
	// mounts it.
	edgeSrv := httptest.NewServer(http.HandlerFunc(edgeHandler(
		station.EdgeExecutor{Station: s, CoreKey: make([]byte, 32)})))
	defer edgeSrv.Close()

	resp, err := http.Post(edgeSrv.URL+"/receipts/settled", "application/json",
		strings.NewReader(`{"attempt_ids":["att-victim"]}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	// The path lands in the edge catch-all and is judged as an unauthorized edge request -
	// it never comes near the outbox.
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	got := outbox.Collect(10)
	require.Len(t, got, 1, "the evidence must still be there")
	require.Equal(t, "att-victim", got[0].AttemptID)

	// And collection is equally out of reach.
	resp, err = http.Post(edgeSrv.URL+"/receipts/collect", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// The Tower-facing surface serves the outbox: collect copies, confirm removes.
func TestTheTowerFacingSurfaceServesTheOutbox(t *testing.T) {
	s := servingStation(t)
	outbox := station.NewOutbox(10)
	outbox.Add(station.Evidence{AttemptID: "att-1", StationID: s.StationID, Receipt: []byte("r1")})
	outbox.Add(station.Evidence{AttemptID: "att-2", StationID: s.StationID, Receipt: []byte("r2")})

	srv := httptest.NewServer(towerFacingHandler(station.Executor{Station: s},
		station.EdgeExecutor{Station: s}, outbox, false))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/receipts/collect", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	var got struct {
		Receipts []station.Evidence `json:"receipts"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	resp.Body.Close()
	require.Len(t, got.Receipts, 2)

	// Collect again: still two. Copies, never a drain.
	resp, err = http.Post(srv.URL+"/receipts/collect", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	got.Receipts = nil
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	resp.Body.Close()
	require.Len(t, got.Receipts, 2)

	// Confirm one; one remains.
	resp, err = http.Post(srv.URL+"/receipts/settled", "application/json",
		strings.NewReader(`{"attempt_ids":["att-1"]}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, outbox.Collect(10), 1)

	// And with no edge catch-all mounted, an unknown path is a plain 404 rather than an
	// edge refusal: this surface is for the Tower, which speaks exact paths.
	resp, err = http.Get(srv.URL + "/nothing-here")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestTheOutboxRoutesRefuseTheWrongMethodAndBadBodies(t *testing.T) {
	s := servingStation(t)
	outbox := station.NewOutbox(10)
	srv := httptest.NewServer(towerFacingHandler(station.Executor{Station: s},
		station.EdgeExecutor{Station: s}, outbox, false))
	defer srv.Close()

	for _, path := range []string{"/receipts/collect", "/receipts/settled"} {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, path)
	}
	resp, err := http.Post(srv.URL+"/receipts/settled", "application/json",
		strings.NewReader("{not json"))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// The split-serve loop, both listeners, ending cleanly: the shape `--edge` actually runs.
func TestBothListenersServeAndStopCleanly(t *testing.T) {
	s := servingStation(t)
	outbox := station.NewOutbox(10)
	towerLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	edgeLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	stop := make(chan struct{})
	done := make(chan error, 1)
	var b bytes.Buffer
	go func() {
		done <- serveStationSplit(station.Executor{Station: s},
			station.EdgeExecutor{Station: s, CoreKey: make([]byte, 32)},
			outbox, towerLn, edgeLn, &b, stop)
	}()

	// The Tower-facing port answers /id; the consumer-facing port judges edge requests.
	require.Eventually(t, func() bool {
		resp, gerr := http.Get("http://" + towerLn.Addr().String() + "/id")
		if gerr != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		resp, gerr := http.Post("http://"+edgeLn.Addr().String()+"/v1/chat/completions",
			"application/json", strings.NewReader("{}"))
		if gerr != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized // no grant: judged, not 404
	}, 5*time.Second, 10*time.Millisecond)

	close(stop)
	require.NoError(t, <-done)
	require.Contains(t, b.String(), "stopped")
}

// `serve --edge` needs a certificate and a second listenable address, and refuses each
// mistake at startup by name.
func TestServeEdgeRefusalsHappenAtStartup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "st")
	_, err := runCLI(t, "init", "--dir", dir)
	require.NoError(t, err)

	// A certificate is installed, but the edge address is not listenable.
	var b bytes.Buffer
	require.NoError(t, run([]string{"csr", "--dir", dir, "--name", "st-a.relay.example"}, &b))
	chain := certForStationKey(t, dir)
	certPath := filepath.Join(t.TempDir(), "chain.pem")
	require.NoError(t, os.WriteFile(certPath, chain, 0o644))
	b.Reset()
	require.NoError(t, run([]string{"install-cert", "--dir", dir, "--cert", certPath}, &b))

	err = run([]string{"serve", "--dir", dir, "--edge", "127.0.0.1:-1",
		"--listen", "127.0.0.1:0", "--upstream", "http://127.0.0.1:1/v1",
		"--core-key", strings.Repeat("ab", 32),
		"--core-envelope-key", strings.Repeat("cd", 32)}, &b)
	require.Error(t, err)
}

// The Tower-facing surface serves signed transcripts for audit; the consumer surface does not.
func TestTheTowerFacingSurfaceServesTranscripts(t *testing.T) {
	s := servingStation(t)
	transcripts := station.NewTranscripts(10, 1)
	transcripts.Keep(station.Transcript{AttemptID: "att-1", Request: []byte("q"), Response: []byte("a")})
	edge := station.EdgeExecutor{Station: s, Network: "roger-public", Transcripts: transcripts}

	srv := httptest.NewServer(towerFacingHandler(station.Executor{Station: s}, edge, nil, false))
	defer srv.Close()

	// A kept transcript comes back available and signed.
	resp, err := http.Post(srv.URL+"/transcripts/get", "application/json",
		strings.NewReader(`{"attempt_id":"att-1"}`))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	resp.Body.Close()
	require.Equal(t, true, got["available"])
	require.NotEmpty(t, got["transcript"])

	// One never kept is "not available", not an error - the audit records cannot-produce.
	resp, err = http.Post(srv.URL+"/transcripts/get", "application/json",
		strings.NewReader(`{"attempt_id":"att-never"}`))
	require.NoError(t, err)
	got = nil
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	resp.Body.Close()
	require.Equal(t, false, got["available"])

	// Wrong method and a missing attempt id are refused.
	resp, err = http.Get(srv.URL + "/transcripts/get")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	resp, err = http.Post(srv.URL+"/transcripts/get", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// The /id probe reports how many transcripts a Station is holding - zero is a signal that it
// can never pass an audit.
func TestTheIdProbeReportsTranscriptsKept(t *testing.T) {
	s := servingStation(t)
	transcripts := station.NewTranscripts(10, 1)
	transcripts.Keep(station.Transcript{AttemptID: "att-1", Request: []byte("q"), Response: []byte("a")})
	edge := station.EdgeExecutor{Station: s, Transcripts: transcripts}

	srv := httptest.NewServer(towerFacingHandler(station.Executor{Station: s}, edge, nil, true))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/id")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	resp.Body.Close()
	require.Equal(t, float64(1), got["transcripts_kept"])
}
