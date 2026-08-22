package towerhub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"

	"net/http/httptest"
	"rogerai.fm/roger/v6/internal/towercore/envelope"
	"testing"
	"time"

	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"strings"
)

type fakeTranscripts map[string][3][]byte

func (f fakeTranscripts) SignedTranscript(id string) (signed, req, resp []byte, ok bool, err error) {
	t, found := f[id]
	if !found {
		return nil, nil, nil, false, nil
	}
	return t[0], t[1], t[2], true, nil
}

// The whole audit plane, node to courier: the tower lists Core's wants, the node's answer
// loop fetches its slice, uploads the signed transcript for what it kept and a truthful
// "not retained" for what it did not, and only LISTED attempts reach the courier.
func TestAuditPlaneCarriesTranscriptsFromAPollOnlyNode(t *testing.T) {
	id := newTestNode(t)
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID, EpochKey: testHubKey, SubmitTTL: time.Second, PollTTL: 100 * time.Millisecond})
	s.RegisterNode("st1", id.auth())
	forwarded := make(chan TranscriptReply, 4)
	s.OnTranscript = func(station string, r TranscriptReply) {
		require.Equal(t, "st1", station)
		forwarded <- r
	}
	mux := http.NewServeMux()
	mux.HandleFunc(PathAuditWanted, s.AuditWanted)
	mux.HandleFunc(PathAuditTranscript, s.AuditTranscript)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	s.SetWanted("st1", []string{"att-kept", "att-lost"})
	src := fakeTranscripts{"att-kept": {[]byte("signed"), []byte("q"), []byte("a")}}
	corePub, corePriv, err := envelope.NewKey()
	require.NoError(t, err)
	c := id.client(srv.URL, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go AnswerAudits(ctx, c, "st1", src, corePub, 20*time.Millisecond, nil)

	got := map[string]TranscriptReply{}
	for len(got) < 2 {
		select {
		case r := <-forwarded:
			got[r.AttemptID] = r
		case <-time.After(5 * time.Second):
			t.Fatalf("audit answers never arrived; have %v", got)
		}
	}
	require.True(t, got["att-kept"].Available)
	// SEALED TO CORE: the tower relays the audit answer exactly as blind as the job. The
	// plaintext fields stay empty; only Core's envelope key opens the bundle, and only for
	// this attempt.
	require.Empty(t, got["att-kept"].Transcript, "no plaintext crosses the tower")
	require.Empty(t, got["att-kept"].Request)
	require.Empty(t, got["att-kept"].Response)
	sealedRaw, err := base64.StdEncoding.DecodeString(got["att-kept"].SealedBundle)
	require.NoError(t, err)
	parsed, err := envelope.Parse(sealedRaw)
	require.NoError(t, err)
	bundle, err := envelope.OpenWith(corePriv, parsed, "att-kept")
	require.NoError(t, err, "Core opens the bundle with its own key + the attempt AAD")
	var inner struct {
		Transcript string `json:"transcript"`
	}
	require.NoError(t, json.Unmarshal(bundle, &inner))
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("signed")), inner.Transcript)
	require.False(t, got["att-lost"].Available, "not retained is answered truthfully, not silently")

	// The list is consumed: nothing further is forwarded, and an unlisted upload is refused a ride.
	resp, raw := id.postSigned(t, srv.URL, PathAuditTranscript, map[string]any{
		"station_id": "st1", "attempt_id": "att-unasked",
		"available": true, "transcript": "", "request": "", "response": ""})
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(raw))
	select {
	case r := <-forwarded:
		t.Fatalf("an unlisted attempt rode the courier: %v", r)
	case <-time.After(200 * time.Millisecond):
	}

	// Someone else's signature: the audit plane is gated exactly as Poll is, so a station's
	// wanted list is not readable by whoever asks first.
	r2, _ := newTestNode(t).getSigned(t, srv.URL, PathAuditWanted, url.Values{"station": {"st1"}})
	require.Equal(t, http.StatusUnauthorized, r2.StatusCode)
}

// --- the audit plane's refusals, door by door --------------------------------------
//
// The happy path above proves the plane carries; these prove it refuses. Every one of
// these branches measured zero, and they are the plane's entire authorization story:
// what happens when the caller is unregistered, unsigned, malformed, or asking with
// the wrong verb.

func auditServer(t *testing.T) (*Server, *httptest.Server, *testNode) {
	t.Helper()
	id := newTestNode(t)
	s := NewServer(New(), stubCheck, ServerOptions{TowerID: testTowerID, EpochKey: testHubKey,
		SubmitTTL: time.Second, PollTTL: 50 * time.Millisecond})
	s.RegisterNode("st1", id.auth())
	mux := http.NewServeMux()
	mux.HandleFunc(PathAuditWanted, s.AuditWanted)
	mux.HandleFunc(PathAuditTranscript, s.AuditTranscript)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv, id
}

func TestAuditWantedRefusesTheWrongDoors(t *testing.T) {
	_, srv, _ := auditServer(t)

	t.Run("POST is not a read", func(t *testing.T) {
		resp, err := http.Post(srv.URL+PathAuditWanted, "application/json", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	})
	t.Run("unsigned is nobody", func(t *testing.T) {
		resp, err := http.Get(srv.URL + PathAuditWanted + "?station=st1")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestAuditTranscriptRefusesTheWrongDoors(t *testing.T) {
	_, srv, id := auditServer(t)

	t.Run("GET is not an upload", func(t *testing.T) {
		resp, err := http.Get(srv.URL + PathAuditTranscript)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	})
	t.Run("a stranger cannot make this tower buffer eight megabytes", func(t *testing.T) {
		// The credential check runs BEFORE the body read - that ordering is the defense,
		// and it is why this refusal must come back on a request whose body was never sent.
		req, err := http.NewRequest(http.MethodPost, srv.URL+PathAuditTranscript,
			strings.NewReader(`{"station_id":"st1","attempt_id":"a"}`))
		require.NoError(t, err)
		resp, rerr := http.DefaultClient.Do(req)
		require.NoError(t, rerr)
		defer resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		raw, _ := io.ReadAll(resp.Body)
		require.Contains(t, string(raw), "no credential")
	})
	t.Run("garbage JSON from a registered node is named", func(t *testing.T) {
		// Signed by hand so the body can be bytes JSON marshalling would never produce:
		// the signature covers the digest of exactly what arrived, garbage included.
		body := []byte("{not json")
		target := hubTarget(testTowerID, hubEpochAt(t, srv.URL), PathAuditTranscript, url.Values{})
		resp, raw := do(t, id.signedRequest(t, http.MethodPost, srv.URL, target, body)())
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(raw), "invalid JSON")
	})
	t.Run("an attempt-less transcript is refused", func(t *testing.T) {
		resp, raw := id.postSigned(t, srv.URL, PathAuditTranscript, map[string]any{
			"station_id": "st1", "available": false})
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(raw), "names its attempt")
	})
}

// AnswerAudits' own failure discipline: report and continue, never answer wrong.
func TestAnswerAuditsReportsWhatItCannotDo(t *testing.T) {
	t.Run("no core key disables the loop loudly", func(t *testing.T) {
		// Sealing to nothing would relay plaintext; sealing to a short key would fail every
		// attempt. The loop refuses to start and says so ONCE, rather than erroring per tick.
		var got []error
		AnswerAudits(t.Context(), nil, "st1", fakeTranscripts{}, []byte("short"), time.Millisecond,
			func(e error) { got = append(got, e) })
		require.Len(t, got, 1)
		require.ErrorContains(t, got[0], "audit answering disabled")
	})

	t.Run("a failing wanted-fetch is reported and retried", func(t *testing.T) {
		_, srv, id := auditServer(t)
		srv.Close() // the hub is gone; every fetch fails
		errs := make(chan error, 4)
		corePub, _, err := envelope.NewKey()
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go AnswerAudits(ctx, id.client(srv.URL, 0), "st1", fakeTranscripts{}, corePub,
			10*time.Millisecond, func(e error) { errs <- e })
		for i := 0; i < 2; i++ { // two reports = it retried rather than died
			select {
			case <-errs:
			case <-time.After(5 * time.Second):
				t.Fatal("the failing fetch was never reported")
			}
		}
	})

	t.Run("a transcript-store error is retried rather than answered wrong", func(t *testing.T) {
		s, srv, id := auditServer(t)
		s.SetWanted("st1", []string{"att-broken"})
		forwarded := make(chan TranscriptReply, 1)
		s.OnTranscript = func(_ string, r TranscriptReply) { forwarded <- r }
		errs := make(chan error, 4)
		corePub, _, err := envelope.NewKey()
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go AnswerAudits(ctx, id.client(srv.URL, 0), "st1", erroringTranscripts{}, corePub,
			10*time.Millisecond, func(e error) { errs <- e })
		select {
		case <-errs:
		case <-time.After(5 * time.Second):
			t.Fatal("the store error was never reported")
		}
		select {
		case r := <-forwarded:
			t.Fatalf("an erroring store still produced an answer: %+v - a wrong 'not retained' "+
				"is an audit miss the node did not deserve", r)
		case <-time.After(100 * time.Millisecond):
		}
	})
}

type erroringTranscripts struct{}

func (erroringTranscripts) SignedTranscript(string) ([]byte, []byte, []byte, bool, error) {
	return nil, nil, nil, false, errors.New("the transcript store is on fire")
}
