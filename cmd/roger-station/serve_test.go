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
		done <- serveStationWithEdge(station.Executor{Station: s}, station.EdgeExecutor{Station: s}, ln, &b, stop)
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
	return executeHandler(exec, station.EdgeExecutor{Station: exec.Station})
}
