package main

// dispatchloop_test.go drives the Tower's courier leg against a stub Core and a stub Station.
//
// What it is checking is that the Tower CARRIES things without changing them, and that every
// way an attempt can go wrong still ends in Core being told something. An attempt the Tower
// silently drops is a caller waiting out a deadline for an answer that was never coming, and
// that is the failure this loop exists to prevent.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towerjoin"
)

// dispatchCore is Core's dispatch surface: it hands out queued work and records results.
type dispatchCore struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	work     []map[string]any
	results  []map[string]any
	polls    int
	pollFail int
}

func newDispatchCore(t *testing.T) *dispatchCore {
	t.Helper()
	c := &dispatchCore{t: t}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(c.t, r.Header.Get("X-Roger-Pubkey"), "%s was unsigned", r.URL.Path)
		c.mu.Lock()
		defer c.mu.Unlock()
		switch r.URL.Path {
		case "/tower/dispatch":
			c.polls++
			if c.pollFail > 0 {
				c.pollFail--
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"core is busy"}}`))
				return
			}
			if len(c.work) == 0 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next := c.work[0]
			c.work = c.work[1:]
			_ = json.NewEncoder(w).Encode(next)
		case "/tower/dispatch/result":
			var got map[string]any
			_ = json.NewDecoder(r.Body).Decode(&got)
			c.results = append(c.results, got)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(c.srv.Close)
	t.Setenv("ROGER_BROKER", c.srv.URL)
	return c
}

func (c *dispatchCore) queue(w map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.work = append(c.work, w)
}

func (c *dispatchCore) got() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.results...)
}

// grantNaming is a stand-in for Core's signed grant. The loop only ever READS the Station ID
// out of it to decide where to send the work; everything that matters about a grant is
// verified by the Station, which is the point of the Tower not being trusted with it.
func grantNaming(stationID string) json.RawMessage {
	return json.RawMessage(`{"station_id":"` + stationID + `","attempt_id":"att-1"}`)
}

// THE COURIER PROPERTY. What the Station receives is byte-for-byte what Core sent, and what
// Core receives is byte-for-byte what the Station returned.
func TestTheTowerCarriesBothDirectionsUnchanged(t *testing.T) {
	core := newDispatchCore(t)
	st := servingTower(t)

	var sawGrant, sawEnvelope json.RawMessage
	stationSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in station.ExecuteRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		sawGrant, sawEnvelope = in.Grant, in.Envelope
		_, _ = w.Write([]byte(`{"receipt":{"attempt_id":"att-1","response_digest":"d","signed":"c2ln"},` +
			`"envelope":{"ct":"  spaced  ","nonce":"n"}}`))
	}))
	defer stationSrv.Close()

	grant := grantNaming("st-1")
	// A SEALED envelope, because that is all a Tower ever holds. The Tower relays it without
	// being able to read it, which is the property this whole leg is built around.
	sealedRequest := json.RawMessage(`{"epk":"ZQ","nonce":"bm9uY2U","ct":"c2VhbGVk"}`)
	core.queue(map[string]any{"attempt_id": "att-1", "grant": grant, "envelope": sealedRequest})

	runOnce(t, st, stationEndpoints{"st-1": stationSrv.URL + "/execute"})

	require.JSONEq(t, string(grant), string(sawGrant))
	require.Equal(t, string(sealedRequest), string(sawEnvelope),
		"the sealed request reached the Station unchanged")

	results := core.got()
	require.Len(t, results, 1)
	require.Equal(t, "att-1", results[0]["attempt_id"])
	require.NotNil(t, results[0]["receipt"])
	// The sealed result survives byte for byte: a Tower that decoded and re-encoded it would
	// break what Core opens and look exactly like tampering.
	body, err := json.Marshal(results[0]["envelope"])
	require.NoError(t, err)
	require.Contains(t, string(body), "  spaced  ")
}

// EVERY FAILURE STILL REPORTS. Core has a caller waiting; an attempt the Tower drops is a
// timeout instead of an answer.
func TestEveryWayAnAttemptFailsIsStillReported(t *testing.T) {
	for name, tc := range map[string]struct {
		stations func(url string) stationEndpoints
		handler  http.HandlerFunc
		expect   string
	}{
		"no endpoint for that Station": {
			stations: func(string) stationEndpoints { return stationEndpoints{"st-other": "http://127.0.0.1:1"} },
			expect:   "no endpoint",
		},
		"the Station is unreachable": {
			stations: func(string) stationEndpoints { return stationEndpoints{"st-1": "http://127.0.0.1:1/execute"} },
			expect:   "could not be reached",
		},
		"the Station errors": {
			stations: func(url string) stationEndpoints { return stationEndpoints{"st-1": url + "/execute"} },
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			expect: "replied 500",
		},
		"the Station answers gibberish": {
			stations: func(url string) stationEndpoints { return stationEndpoints{"st-1": url + "/execute"} },
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("not json"))
			},
			expect: "not readable",
		},
		"the Station refuses the grant": {
			stations: func(url string) stationEndpoints { return stationEndpoints{"st-1": url + "/execute"} },
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"failure":"this grant is for Station \"st-9\", not this one"}`))
			},
			expect: "not this one",
		},
		"the Station returns no receipt": {
			stations: func(url string) stationEndpoints { return stationEndpoints{"st-1": url + "/execute"} },
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"envelope":{"ct":"sealed"}}`))
			},
			expect: "no receipt",
		},
	} {
		t.Run(name, func(t *testing.T) {
			core := newDispatchCore(t)
			st := servingTower(t)
			url := ""
			if tc.handler != nil {
				s := httptest.NewServer(tc.handler)
				defer s.Close()
				url = s.URL
			}
			core.queue(map[string]any{
				"attempt_id": "att-1", "grant": grantNaming("st-1"),
				"envelope": json.RawMessage(`{"ct":"sealed","nonce":"n"}`),
			})
			runOnce(t, st, tc.stations(url))

			results := core.got()
			require.Len(t, results, 1, "the attempt must be reported either way")
			failure, _ := results[0]["failure"].(string)
			require.Contains(t, failure, tc.expect)
			require.Nil(t, results[0]["receipt"], "a failure must carry no receipt")
		})
	}
}

// A Tower with no Station endpoints does not poll at all, and says why. Polling for work it
// could only refuse would be a Tower busily failing every request sent to it.
func TestATowerWithNoStationEndpointsCollectsNothing(t *testing.T) {
	core := newDispatchCore(t)
	st := servingTower(t)
	var b syncBuffer
	runDispatch(st, &b, nil, closedStop())

	require.Contains(t, b.String(), "will not collect work")
	require.Contains(t, b.String(), "--station")
	core.mu.Lock()
	defer core.mu.Unlock()
	require.Zero(t, core.polls)
}

// A poll that fails backs off and carries on: Core having a bad minute must not end a
// Tower's willingness to serve.
func TestAFailedPollBacksOffAndKeepsGoing(t *testing.T) {
	core := newDispatchCore(t)
	st := servingTower(t)
	core.mu.Lock()
	core.pollFail = 1
	core.mu.Unlock()
	core.queue(map[string]any{
		"attempt_id": "att-1", "grant": grantNaming("st-1"),
		"envelope": json.RawMessage(`{"ct":"sealed","nonce":"n"}`),
	})

	stationSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"receipt":{"attempt_id":"att-1"},"envelope":{"ct":"sealed"}}`))
	}))
	defer stationSrv.Close()

	var b syncBuffer
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDispatch(st, &b, stationEndpoints{"st-1": stationSrv.URL + "/execute"}, stop)
	}()
	require.Eventually(t, func() bool { return len(core.got()) == 1 },
		20*time.Second, 20*time.Millisecond, "it recovered and delivered after the failure")
	close(stop)
	<-done
	require.Contains(t, b.String(), "could not collect work")
}

// The routing flag, and its refusals. A malformed --station is caught at startup rather than
// when the first request arrives.
func TestStationEndpointsAreParsedStrictly(t *testing.T) {
	got, err := parseStationEndpoints([]string{"st-1=http://a", "st-2=http://b"})
	require.NoError(t, err)
	require.Equal(t, stationEndpoints{"st-1": "http://a", "st-2": "http://b"}, got)

	for _, bad := range []string{"st-1", "=http://a", "st-1=", ""} {
		_, err := parseStationEndpoints([]string{bad})
		require.Error(t, err, bad)
		require.Contains(t, err.Error(), "ID=URL")
	}

	// And `serve` refuses it too, before opening anything.
	_, err = runCLI(t, "serve", "--dir", t.TempDir(), "--station", "nonsense")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ID=URL")
}

// With exactly one Station configured, a grant that names no Station still routes to it.
// Several, and the grant must name one this Tower knows.
func TestRoutingFallsBackToTheOnlyStationOnlyWhenThereIsOne(t *testing.T) {
	one := stationEndpoints{"st-1": "http://a"}
	id, url := routeFor(one, towerWorkOf(`{"attempt_id":"x"}`))
	require.Equal(t, "http://a", url)
	// It reports the Station it fell back TO, not the empty name it was given: the id is what
	// the log line names, and "relayed to """ helps nobody.
	require.Equal(t, "st-1", id)

	id, url = routeFor(one, towerWorkOf(`{"station_id":"st-1"}`))
	require.Equal(t, "st-1", id)
	require.Equal(t, "http://a", url)

	// Named but unknown: no guessing, even with one configured.
	_, url = routeFor(one, towerWorkOf(`{"station_id":"st-9"}`))
	require.Empty(t, url)

	two := stationEndpoints{"st-1": "http://a", "st-2": "http://b"}
	_, url = routeFor(two, towerWorkOf(`{"attempt_id":"x"}`))
	require.Empty(t, url, "with several, an unnamed grant is not routable")

	// A grant that is not JSON names nothing, which is a refusal rather than a panic.
	_, url = routeFor(two, towerWorkOf(`{nope`))
	require.Empty(t, url)
}

// `serve` accepts the flag and reaches the loop.
func TestServeAcceptsStationEndpoints(t *testing.T) {
	out, err := runCLI(t, "serve", "--station", "st-1=http://a")
	require.Error(t, err, "it still needs a data directory")
	require.Contains(t, err.Error(), "--dir")
	require.Empty(t, out)
}

// runOnce drives exactly one attempt through the loop and stops.
func runOnce(t *testing.T, st *tower.State, stations stationEndpoints) {
	t.Helper()
	var b syncBuffer
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDispatch(st, &b, stations, stop)
	}()
	require.Eventually(t, func() bool { return strings.Contains(b.String(), "collecting work") },
		5*time.Second, 5*time.Millisecond)
	// The loop reports each attempt before looking for the next, so waiting for the result
	// to land at Core is what tells us the attempt is done.
	time.Sleep(300 * time.Millisecond)
	close(stop)
	<-done
}

func towerWorkOf(grant string) towerjoin.Work {
	return towerjoin.Work{Grant: json.RawMessage(grant)}
}
