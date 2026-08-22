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

	"github.com/stretchr/testify/require"
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
