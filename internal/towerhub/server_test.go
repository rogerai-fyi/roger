package towerhub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubCheck reads a fake grant of the form "attempt|station" as its authorized metadata, and
// rejects a grant containing "bad". It stands in for dispatch.EdgeGrantMeta so the Server test
// stays independent of the real signing machinery (which edge_test.go covers).
func stubCheck(grant []byte) (string, string, error) {
	s := string(grant)
	if strings.Contains(s, "bad") {
		return "", "", errors.New("not authorized")
	}
	parts := strings.SplitN(s, "|", 2)
	if len(parts) != 2 {
		return "", "", errors.New("malformed")
	}
	return parts[0], parts[1], nil
}

// testServer is the ordinary hub under test: this tower's id bound into every signature, and
// the transition tolerance ON, which is what `roger-tower serve` defaults to today.
func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	return testServerWith(t, ServerOptions{TowerID: testTowerID, EpochKey: testHubKey, SubmitTTL: 3 * time.Second,
		PollTTL: 300 * time.Millisecond, AllowLegacyBearer: true})
}

// testServerWith is for the tests that are ABOUT a setting - a different tower id, or a hub
// that has ended the bearer tolerance. Those used to be written by reaching in and assigning
// Server.AllowLegacyBearer after the fact, which is a data race in a test's clothing: the field
// is read by live handlers. It is fixed at construction now, so a test that wants the other
// posture builds the other server.
func testServerWith(t *testing.T, opt ServerOptions) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer(New(), stubCheck, opt)
	mux := http.NewServeMux()
	mux.HandleFunc("/submit", s.Submit)
	mux.HandleFunc("/poll", s.Poll)
	mux.HandleFunc("/complete", s.Complete)
	mux.HandleFunc(PathAuditWanted, s.AuditWanted)
	mux.HandleFunc(PathAuditTranscript, s.AuditTranscript)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

// postJSON posts with an optional BEARER token. It is the consumer's helper (which authorizes
// with a grant in the body and needs no token) and the legacy-bearer path's; a node-side signed
// POST is testNode.postSigned.
func postJSON(t *testing.T, url, token string, body any) (*http.Response, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

// A consumer's sealed job reaches the polling node over HTTP and the node's sealed result comes
// back to the consumer - the whole tower data plane, end to end, with the tower blind.
func TestHTTPSubmitReachesTheNodeAndReturnsTheResult(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth())

	// The serving node: long-poll, then complete with a sealed result + receipt. Both calls are
	// signed with the Station's assertion key - see nodeauth.go.
	go func() {
		resp, raw := node.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
		if resp.StatusCode != http.StatusOK {
			return
		}
		var job pollResp
		_ = json.Unmarshal(raw, &job)
		// The node sees the consumer's sealed request (opaque here), returns a sealed answer.
		node.postSigned(t, srv.URL, PathComplete, completeReq{
			AttemptID: job.AttemptID, StationID: job.StationID,
			Envelope: base64.StdEncoding.EncodeToString([]byte("sealed-answer")),
			Receipt:  base64.StdEncoding.EncodeToString([]byte("token-receipt")),
		})
	}()

	resp, raw := postJSON(t, srv.URL+"/submit", "", submitReq{
		Grant:    base64.StdEncoding.EncodeToString([]byte("att-1|st-1")),
		Envelope: base64.StdEncoding.EncodeToString([]byte("sealed-request")),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	var out submitResp
	require.NoError(t, json.Unmarshal(raw, &out))
	env, _ := base64.StdEncoding.DecodeString(out.Envelope)
	rec, _ := base64.StdEncoding.DecodeString(out.Receipt)
	require.Equal(t, []byte("sealed-answer"), env)
	require.Equal(t, []byte("token-receipt"), rec)
}

// A grant the tower cannot validate is refused before anything is queued.
func TestHTTPSubmitWithAnUnauthorizedGrantIsRefused(t *testing.T) {
	_, srv := testServer(t)
	resp, _ := postJSON(t, srv.URL+"/submit", "", submitReq{
		Grant:    base64.StdEncoding.EncodeToString([]byte("bad-grant")),
		Envelope: base64.StdEncoding.EncodeToString([]byte("x")),
	})
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// A valid grant for a Station no node serves here is a 404, promptly.
func TestHTTPSubmitToAnUnservedStationIs404(t *testing.T) {
	_, srv := testServer(t)
	resp, _ := postJSON(t, srv.URL+"/submit", "", submitReq{
		Grant:    base64.StdEncoding.EncodeToString([]byte("att-1|st-nobody")),
		Envelope: base64.StdEncoding.EncodeToString([]byte("x")),
	})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// Poll and Complete require the Station's own signature: unsigned, or signed by another key, is
// a 401 on both.
func TestHTTPPollAndCompleteRequireTheStationsSignature(t *testing.T) {
	node, stranger := newTestNode(t), newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth())

	// Poll with nothing at all.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/poll?station=st-1", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Poll signed by someone else's key.
	resp2, _ := stranger.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusUnauthorized, resp2.StatusCode)

	// Complete signed by someone else's key.
	resp3, _ := stranger.postSigned(t, srv.URL, PathComplete, completeReq{AttemptID: "att-1", StationID: "st-1"})
	require.Equal(t, http.StatusUnauthorized, resp3.StatusCode)
}

// An empty queue long-poll returns 204 (poll again), not a hang.
func TestHTTPPollReturns204WhenIdle(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", node.auth())
	start := time.Now()
	resp, _ := node.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Less(t, time.Since(start), 2*time.Second, "the long poll returns near its TTL, not the submit TTL")
}

// A submit whose node never answers times out as a 504 rather than hanging forever.
func TestHTTPSubmitTimesOutWhenNoNodeAnswers(t *testing.T) {
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID, EpochKey: testHubKey,
		SubmitTTL: 120 * time.Millisecond, PollTTL: 60 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc("/submit", s.Submit)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.RegisterNode("st-1", newTestNode(t).auth()) // registered, but no node polls

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, _ := json.Marshal(submitReq{
		Grant:    base64.StdEncoding.EncodeToString([]byte("att-1|st-1")),
		Envelope: base64.StdEncoding.EncodeToString([]byte("x")),
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/submit", bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusGatewayTimeout, resp.StatusCode)
}

// Method guards: the wrong verb is a 405 on every handler.
func TestHTTPMethodGuards(t *testing.T) {
	_, srv := testServer(t)
	resp, err := http.Get(srv.URL + "/submit")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)

	resp2, _ := postJSON(t, srv.URL+"/poll", "", map[string]any{})
	require.Equal(t, http.StatusMethodNotAllowed, resp2.StatusCode)
}

// A grant that is not valid base64 is a clean 400, not a 500.
func TestHTTPSubmitBadBase64Is400(t *testing.T) {
	_, srv := testServer(t)
	resp, _ := postJSON(t, srv.URL+"/submit", "", submitReq{Grant: "!!!not base64!!!", Envelope: "x"})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// Re-registering a Station (RegisterNode again) replaces what authenticates it immediately -
// which is how a revoked or re-keyed node stops polling within one refresh of the tower's node
// list, and why the credential lives in one map rather than being remembered per connection.
func TestHTTPReRegisteringAStationRetiresTheOldKey(t *testing.T) {
	old, current := newTestNode(t), newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st-1", old.auth())
	s.RegisterNode("st-1", current.auth()) // re-key

	resp, _ := old.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the retired key still authenticated")
	ok, _ := current.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st-1"}})
	require.Equal(t, http.StatusNoContent, ok.StatusCode)
}

// The settle courier only rides for attempts the hub actually HANDED to that node (audit L2):
// a registered-but-hostile node fabricating attempt ids + self-signed receipts must not get
// them forwarded to Core under the tower's signature.
func TestOnCompleteFiresOnlyForDispatchedAttempts(t *testing.T) {
	node := newTestNode(t)
	s, srv := testServer(t)
	s.RegisterNode("st1", node.auth())
	fired := make(chan string, 4)
	s.OnComplete = func(_ string, res Result) { fired <- res.AttemptID }

	// A fabricated completion: the hub never dispatched "made-up". Answered 202, not 200 -
	// the receipt was NOT couriered, and an honest node deserves to hear that loudly (a hub
	// restart mid-job produces the same shape).
	resp, _ := node.postSigned(t, srv.URL, PathComplete, map[string]string{
		"attempt_id": "made-up", "station_id": "st1",
		"receipt": base64.StdEncoding.EncodeToString([]byte("forged")),
	})
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	// A REAL round trip: submit -> poll (records the dispatch) -> complete.
	go func() {
		r, _ := postJSON(t, srv.URL+"/submit", "", map[string]string{
			"grant":    base64.StdEncoding.EncodeToString([]byte("att-real|st1")),
			"envelope": base64.StdEncoding.EncodeToString([]byte("sealed")),
		})
		r.Body.Close()
	}()
	var polled pollResp
	require.Eventually(t, func() bool {
		pr, raw := node.getSigned(t, srv.URL, PathPoll, url.Values{"station": {"st1"}})
		if pr.StatusCode != http.StatusOK {
			return false
		}
		return json.Unmarshal(raw, &polled) == nil
	}, 3*time.Second, 50*time.Millisecond)
	resp, _ = node.postSigned(t, srv.URL, PathComplete, map[string]string{
		"attempt_id": polled.AttemptID, "station_id": "st1",
		"envelope": base64.StdEncoding.EncodeToString([]byte("sealed-answer")),
		"receipt":  base64.StdEncoding.EncodeToString([]byte("signed")),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	select {
	case id := <-fired:
		require.Equal(t, "att-real", id, "only the dispatched attempt rides the courier")
	case <-time.After(2 * time.Second):
		t.Fatal("the dispatched attempt's completion never reached the courier")
	}
	select {
	case id := <-fired:
		t.Fatalf("a completion the hub never carried reached the courier: %q", id)
	case <-time.After(300 * time.Millisecond):
	}
}

// An unknown-Station submit tells the tower (audit M3), so it can refresh registrations
// immediately instead of leaving a freshly attached node dark until the periodic tick.
func TestSubmitToUnknownStationTriggersTheRefreshHook(t *testing.T) {
	s, srv := testServer(t)
	hit := make(chan string, 1)
	s.OnUnknownStation = func(stationID string) { hit <- stationID }
	resp, _ := postJSON(t, srv.URL+"/submit", "", map[string]string{
		"grant":    base64.StdEncoding.EncodeToString([]byte("att|ghost")),
		"envelope": base64.StdEncoding.EncodeToString([]byte("sealed")),
	})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	select {
	case id := <-hit:
		require.Equal(t, "ghost", id)
	case <-time.After(2 * time.Second):
		t.Fatal("the unknown-station hook never fired")
	}
}

// --- the doors Submit and Complete close ------------------------------------------
//
// Every branch here measured zero, and together they are the hub's whole answer to a
// malformed caller. A consumer talks to Submit unauthenticated by design (the grant is the
// credential), so these refusals are the ONLY thing between line noise and a queue slot.

func refusalServer(t *testing.T) (*Server, *httptest.Server, *testNode) {
	t.Helper()
	id := newTestNode(t)
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID, EpochKey: testHubKey,
		SubmitTTL: 2 * time.Second, PollTTL: 50 * time.Millisecond})
	s.RegisterNode("st1", id.auth())
	mux := http.NewServeMux()
	mux.HandleFunc(PathSubmit, s.Submit)
	mux.HandleFunc(PathPoll, s.Poll)
	mux.HandleFunc(PathComplete, s.Complete)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv, id
}

func TestSubmitRefusesMalformedConsumers(t *testing.T) {
	_, srv, _ := refusalServer(t)
	post := func(body string) (*http.Response, string) {
		resp, err := http.Post(srv.URL+PathSubmit, "application/json", strings.NewReader(body))
		require.NoError(t, err)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(raw)
	}
	t.Run("not JSON", func(t *testing.T) {
		resp, raw := post("{nope")
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, raw, "invalid JSON")
	})
	t.Run("grant not base64", func(t *testing.T) {
		resp, raw := post(`{"grant":"%%%","envelope":"AAAA"}`)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, raw, "grant is not valid base64")
	})
	t.Run("envelope not base64", func(t *testing.T) {
		resp, raw := post(`{"grant":"AAAA","envelope":"%%%"}`)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, raw, "envelope is not valid base64")
	})
}

// The SAME attempt submitted twice while the first is still in flight is a 409, not a second
// queue slot: one grant authorizes one ride, and a duplicate that queued would double-serve
// (and double-bill) the attempt if both got polled.
//
// SEQUENCED, NOT RACED. The first version of this test retried the duplicate in an
// Eventually loop against a background submit - and whichever POST arrived first became the
// in-flight attempt, so the "duplicate" sometimes queued and blocked out the whole retry
// budget. The waiter lives until Complete or TTL, so the deterministic order is: submit,
// PROVE the attempt is in flight by polling the job out, then send the duplicate.
func TestADuplicateInFlightSubmitConflicts(t *testing.T) {
	_, srv, id := refusalServer(t)
	grant := base64.StdEncoding.EncodeToString([]byte("att-dup|st1"))
	env := base64.StdEncoding.EncodeToString([]byte("sealed"))
	body := `{"grant":"` + grant + `","envelope":"` + env + `"}`

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		resp, err := http.Post(srv.URL+PathSubmit, "application/json", strings.NewReader(body))
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()

	// The node pulls the job: from here the attempt is DEFINITELY in flight, and stays so
	// until it is completed below - polling dequeues the job but the waiter remains.
	node := id.client(srv.URL, 5*time.Second)
	var job Job
	require.Eventually(t, func() bool {
		j, ok, perr := node.PollJob(t.Context(), "st1")
		if perr != nil || !ok {
			return false
		}
		job = j
		return true
	}, 5*time.Second, 20*time.Millisecond)

	resp, err := http.Post(srv.URL+PathSubmit, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(raw))
	require.Contains(t, string(raw), "already in flight")

	// Complete so the first submit returns and nothing leaks past the test.
	require.NoError(t, node.CompleteResult(t.Context(), "st1",
		Result{AttemptID: job.AttemptID, Envelope: []byte("ans"), Receipt: []byte("r")}))
	<-firstDone
}

// A grant whose metadata names no attempt is refused by name - the check function is the
// consumer's credential, and an empty answer from it must not become an empty-keyed queue
// entry that every later duplicate check would collide with.
func TestAGrantNamingNothingIsRefused(t *testing.T) {
	s := NewServer(New(), func([]byte) (string, string, error) { return "", "", nil },
		ServerOptions{TowerID: testTowerID, EpochKey: testHubKey, SubmitTTL: time.Second})
	s.RegisterNode("st1", newTestNode(t).auth())
	srv := httptest.NewServer(http.HandlerFunc(s.Submit))
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL, "application/json",
		strings.NewReader(`{"grant":"AAAA","envelope":"AAAA"}`))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, string(raw), "names no attempt")
}

func TestCompleteRefusesMalformedNodes(t *testing.T) {
	_, srv, id := refusalServer(t)
	t.Run("GET is not a completion", func(t *testing.T) {
		resp, err := http.Get(srv.URL + PathComplete)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	})
	t.Run("garbage JSON, signed, is named", func(t *testing.T) {
		body := []byte("{not json")
		target := hubTarget(testTowerID, hubEpochAt(t, srv.URL), PathComplete, url.Values{})
		resp, raw := do(t, id.signedRequest(t, http.MethodPost, srv.URL, target, body)())
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(raw), "invalid JSON")
	})
	t.Run("an OVERSIZED body on a GET is refused", func(t *testing.T) {
		// The first version of this test sent a small body and expected a refusal - wrong on
		// the contract. A small GET body is read and covered by the signature like any other
		// bytes; what readGETBody refuses is a body past its 4KB bound, because a poll is a
		// read and a caller streaming megabytes into one is not polling.
		big := bytes.Repeat([]byte("x"), maxGETBody+1)
		target := hubTarget(testTowerID, hubEpochAt(t, srv.URL), PathPoll, url.Values{"station": {"st1"}})
		resp, raw := do(t, id.signedRequest(t, http.MethodGet, srv.URL, target, big)())
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(raw), "carries no body")
	})
}

// --- what the node's client refuses from a hub that answers garbage ----------------
//
// A hub is somebody else's box. Every decode on the client side is a trust boundary, and
// every one of these branches measured zero: the client had never once been shown a
// malformed answer.

func TestTheClientRefusesAHubAnsweringGarbage(t *testing.T) {
	id := newTestNode(t)
	cases := []struct {
		name, path string
		answer     string
		call       func(c *Client) error
		wantErr    string
	}{
		{"poll: not JSON", PathPoll, `{nope`,
			func(c *Client) error { _, _, err := c.PollJob(t.Context(), "st1"); return err },
			"unreadable poll response"},
		{"poll: grant not base64", PathPoll, `{"attempt_id":"a","station_id":"st1","grant":"%%%","envelope":"AAAA"}`,
			func(c *Client) error { _, _, err := c.PollJob(t.Context(), "st1"); return err },
			"grant is not valid base64"},
		{"poll: envelope not base64", PathPoll, `{"attempt_id":"a","station_id":"st1","grant":"AAAA","envelope":"%%%"}`,
			func(c *Client) error { _, _, err := c.PollJob(t.Context(), "st1"); return err },
			"envelope is not valid base64"},
		{"submit: not JSON", PathSubmit, `{nope`,
			func(c *Client) error { _, err := c.SubmitJob(t.Context(), []byte("g"), []byte("e")); return err },
			"unreadable submit response"},
		{"submit: envelope not base64", PathSubmit, `{"envelope":"%%%","receipt":"AAAA"}`,
			func(c *Client) error { _, err := c.SubmitJob(t.Context(), []byte("g"), []byte("e")); return err },
			"envelope is not valid base64"},
		{"submit: receipt not base64", PathSubmit, `{"envelope":"AAAA","receipt":"%%%"}`,
			func(c *Client) error { _, err := c.SubmitJob(t.Context(), []byte("g"), []byte("e")); return err },
			"receipt is not valid base64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc(tc.path, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.answer)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			err := tc.call(id.client(srv.URL, 5*time.Second))
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// A completion the hub accepted but did not courier is ErrNotCarried - the one answer where
// "accepted" and "your pay is safe" come apart, and the caller must be able to branch on it.
func TestAnUncourieredCompletionIsNamed(t *testing.T) {
	id := newTestNode(t)
	mux := http.NewServeMux()
	mux.HandleFunc(PathComplete, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	err := id.client(srv.URL, 5*time.Second).CompleteResult(t.Context(), "st1",
		Result{AttemptID: "a", Envelope: []byte("e"), Receipt: []byte("r")})
	require.ErrorIs(t, err, ErrNotCarried)
}

func TestCompleteRefusesUndecodableFields(t *testing.T) {
	_, srv, id := refusalServer(t)
	post := func(body map[string]any) (*http.Response, string) {
		resp, raw := id.postSigned(t, srv.URL, PathComplete, body)
		return resp, string(raw)
	}
	t.Run("envelope not base64", func(t *testing.T) {
		resp, raw := post(map[string]any{"attempt_id": "a", "station_id": "st1",
			"envelope": "%%%", "receipt": "AAAA"})
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, raw, "envelope is not valid base64")
	})
	t.Run("receipt not base64", func(t *testing.T) {
		resp, raw := post(map[string]any{"attempt_id": "a", "station_id": "st1",
			"envelope": "AAAA", "receipt": "%%%"})
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, raw, "receipt is not valid base64")
	})
}

// A hub built WITHOUT its identity key still proves an epoch - with a minted per-process
// key that every node holding Core's real fingerprint will refuse. That refusal is the
// correct outcome for a mis-wired hub, and it only works if the mint actually happens.
func TestAHubWithoutItsIdentityStillSignsItsEpoch(t *testing.T) {
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID})
	require.NotEmpty(t, s.EpochKeyHash(),
		"no epoch key was minted: nodes would get an epoch with no proof at all")
	require.NotEqual(t, testHubKeyHash(), s.EpochKeyHash(),
		"the minted key must be its own, not the real identity")
}

// Clearing a station's wanted list deletes the entry rather than storing an empty slice,
// and an oversized GET body is refused at the wanted door exactly as it is at the poll's.
func TestWantedListClearsAndBoundsItsDoor(t *testing.T) {
	s, srv, id := auditServer(t)
	s.SetWanted("st1", []string{"att-1"})
	s.SetWanted("st1", nil) // cleared: the entry is deleted, not left empty

	resp, raw := id.getSigned(t, srv.URL, PathAuditWanted, url.Values{"station": {"st1"}})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	var out struct {
		Wanted []string `json:"wanted"`
	}
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Empty(t, out.Wanted)

	big := bytes.Repeat([]byte("x"), maxGETBody+1)
	target := hubTarget(testTowerID, hubEpochAt(t, srv.URL), PathAuditWanted, url.Values{"station": {"st1"}})
	resp2, raw2 := do(t, id.signedRequest(t, http.MethodGet, srv.URL, target, big)())
	require.Equal(t, http.StatusBadRequest, resp2.StatusCode)
	require.Contains(t, string(raw2), "carries no body")
}
