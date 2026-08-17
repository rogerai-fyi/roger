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

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer(New(), stubCheck, 3*time.Second, 300*time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc("/submit", s.Submit)
	mux.HandleFunc("/poll", s.Poll)
	mux.HandleFunc("/complete", s.Complete)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

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
	s, srv := testServer(t)
	s.RegisterNode("st-1", "node-token")

	// The serving node: long-poll, then complete with a sealed result + receipt.
	go func() {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/poll?station=st-1", nil)
		req.Header.Set("Authorization", "Bearer node-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			return
		}
		var job pollResp
		_ = json.NewDecoder(resp.Body).Decode(&job)
		resp.Body.Close()
		// The node sees the consumer's sealed request (opaque here), returns a sealed answer.
		postJSON(t, srv.URL+"/complete", "node-token", completeReq{
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

// Poll and Complete require the Station's own node token: a wrong or missing token is 401.
func TestHTTPPollAndCompleteRequireTheNodeToken(t *testing.T) {
	s, srv := testServer(t)
	s.RegisterNode("st-1", "the-token")

	// Poll with no token.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/poll?station=st-1", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Poll with a wrong token.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/poll?station=st-1", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Complete with a wrong token.
	resp2, _ := postJSON(t, srv.URL+"/complete", "wrong", completeReq{AttemptID: "att-1", StationID: "st-1"})
	require.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

// An empty queue long-poll returns 204 (poll again), not a hang.
func TestHTTPPollReturns204WhenIdle(t *testing.T) {
	s, srv := testServer(t)
	s.RegisterNode("st-1", "t")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/poll?station=st-1", nil)
	req.Header.Set("Authorization", "Bearer t")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Less(t, time.Since(start), 2*time.Second, "the long poll returns near its TTL, not the submit TTL")
}

// A submit whose node never answers times out as a 504 rather than hanging forever.
func TestHTTPSubmitTimesOutWhenNoNodeAnswers(t *testing.T) {
	s := NewServer(New(), stubCheck, 120*time.Millisecond, 60*time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc("/submit", s.Submit)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.RegisterNode("st-1", "t") // registered, but no node polls

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

	resp2, _ := postJSON(t, srv.URL+"/poll", "t", map[string]any{})
	require.Equal(t, http.StatusMethodNotAllowed, resp2.StatusCode)
}

// A grant that is not valid base64 is a clean 400, not a 500.
func TestHTTPSubmitBadBase64Is400(t *testing.T) {
	_, srv := testServer(t)
	resp, _ := postJSON(t, srv.URL+"/submit", "", submitReq{Grant: "!!!not base64!!!", Envelope: "x"})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// Rotating a node's token (RegisterNode again) invalidates the old token immediately.
func TestHTTPTokenRotationInvalidatesTheOldToken(t *testing.T) {
	s, srv := testServer(t)
	s.RegisterNode("st-1", "old")
	s.RegisterNode("st-1", "new") // rotate

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/poll?station=st-1", nil)
	req.Header.Set("Authorization", "Bearer old")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the rotated-out token no longer authenticates")
}

// The settle courier only rides for attempts the hub actually HANDED to that node (audit L2):
// a registered-but-hostile node fabricating attempt ids + self-signed receipts must not get
// them forwarded to Core under the tower's signature.
func TestOnCompleteFiresOnlyForDispatchedAttempts(t *testing.T) {
	s, srv := testServer(t)
	s.RegisterNode("st1", "tok")
	fired := make(chan string, 4)
	s.OnComplete = func(_ string, res Result) { fired <- res.AttemptID }

	// A fabricated completion: the hub never dispatched "made-up".
	resp, _ := postJSON(t, srv.URL+"/complete", "tok", map[string]string{
		"attempt_id": "made-up", "station_id": "st1",
		"receipt": base64.StdEncoding.EncodeToString([]byte("forged")),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// A REAL round trip: submit -> poll (records the dispatch) -> complete.
	go func() {
		r, _ := postJSON(t, srv.URL+"/submit", "", map[string]string{
			"grant":    base64.StdEncoding.EncodeToString([]byte("att-real|st1")),
			"envelope": base64.StdEncoding.EncodeToString([]byte("sealed")),
		})
		r.Body.Close()
	}()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/poll?station=st1", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer tok")
	var polled pollResp
	require.Eventually(t, func() bool {
		pr, perr := http.DefaultClient.Do(req)
		if perr != nil || pr.StatusCode != http.StatusOK {
			if pr != nil {
				pr.Body.Close()
			}
			return false
		}
		defer pr.Body.Close()
		return json.NewDecoder(pr.Body).Decode(&polled) == nil
	}, 3*time.Second, 50*time.Millisecond)
	resp, _ = postJSON(t, srv.URL+"/complete", "tok", map[string]string{
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
