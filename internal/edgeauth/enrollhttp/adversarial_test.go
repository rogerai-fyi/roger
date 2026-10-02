package enrollhttp_test

// The wire under attack: wrong methods, unreadable and oversized bodies, forged and replayed
// requests, claims for nodes nobody adopted, and an authority whose internal failure must not leak.
// Every case runs the PRODUCTION handler over a REAL local authority unless the point of the test
// is the handler's own error mapping.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
)

// status sends one raw request and returns the status code and the body text.
func status(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, strings.TrimSpace(string(raw))
}

// adoptedPhone is a node key the owner has adopted on the rig's authority.
func adoptedPhone(t *testing.T, local *edgeauth.Local) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim(edgeauth.NodeID(pub), "pixel"))
	return pub, priv
}

func claimFor(pub ed25519.PublicKey, priv ed25519.PrivateKey, now time.Time) edgeauth.ClaimRequest {
	c := edgeauth.NewClaimRequest("pixel", "mobile", pub, now)
	c.Sign(priv)
	return c
}

func TestAnAdoptedPhoneClaimsOverTheWireOnceAndOnlyOnce(t *testing.T) {
	local, url, _ := rig(t)
	pub, priv := adoptedPhone(t, local)
	ctx := context.Background()

	resp, err := enrollhttp.Claim(ctx, url, claimFor(pub, priv, time.Now()))
	require.NoError(t, err)
	require.Equal(t, edgeauth.NodeID(pub), resp.NodeID)
	require.Equal(t, edgeauth.LocalAccount, resp.Account)
	require.Equal(t, local.RootPEM(), resp.Root)
	leaf, err := edgeauth.DecodeCert(resp.Cert)
	require.NoError(t, err)
	got, err := local.Authority().Authenticate(leaf)
	require.NoError(t, err)
	require.Equal(t, edgeauth.NodeID(pub), got)

	// The grant was consumed: the same phone claiming again is refused, and nothing comes back.
	again, err := enrollhttp.Claim(ctx, url, claimFor(pub, priv, time.Now()))
	require.Error(t, err)
	require.Contains(t, err.Error(), "refused")
	require.Contains(t, err.Error(), edgeauth.ErrNotAdopted.Error())
	require.Equal(t, edgeauth.Response{}, again)
}

func TestTheClaimEndpointRefusesEveryForgery(t *testing.T) {
	local, url, _ := rig(t)
	pub, priv := adoptedPhone(t, local)
	_, impostor, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	strangerPub, strangerPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	cases := []struct {
		name  string
		claim func() edgeauth.ClaimRequest
		want  error
	}{
		{"signed by a key that is not the node key", func() edgeauth.ClaimRequest {
			c := edgeauth.NewClaimRequest("pixel", "mobile", pub, time.Now())
			c.Sign(impostor)
			return c
		}, edgeauth.ErrBadSignature},
		{"a field altered after signing", func() edgeauth.ClaimRequest {
			c := claimFor(pub, priv, time.Now())
			c.Name = "renamed-in-flight"
			return c
		}, edgeauth.ErrBadSignature},
		{"names one node, proves another", func() edgeauth.ClaimRequest {
			c := claimFor(strangerPub, strangerPriv, time.Now())
			c.NodeID = edgeauth.NodeID(pub)
			return c
		}, edgeauth.ErrBadSignature},
		{"a node key that is not hex", func() edgeauth.ClaimRequest {
			c := claimFor(pub, priv, time.Now())
			c.NodeKey = "zz"
			return c
		}, edgeauth.ErrBadSignature},
		{"a signature that is not hex", func() edgeauth.ClaimRequest {
			c := claimFor(pub, priv, time.Now())
			c.Sig = "not-hex"
			return c
		}, edgeauth.ErrBadSignature},
		{"no signature at all", func() edgeauth.ClaimRequest {
			c := claimFor(pub, priv, time.Now())
			c.Sig = ""
			return c
		}, edgeauth.ErrBadSignature},
		{"signed long ago (a captured claim)", func() edgeauth.ClaimRequest {
			return claimFor(pub, priv, time.Now().Add(-time.Hour))
		}, edgeauth.ErrStaleRequest},
		{"signed in the future", func() edgeauth.ClaimRequest {
			return claimFor(pub, priv, time.Now().Add(time.Hour))
		}, edgeauth.ErrStaleRequest},
		{"a well-signed claim for a node nobody adopted", func() edgeauth.ClaimRequest {
			return claimFor(strangerPub, strangerPriv, time.Now())
		}, edgeauth.ErrNotAdopted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := enrollhttp.Claim(context.Background(), url, tc.claim())
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want.Error())
			require.Equal(t, edgeauth.Response{}, resp)
		})
	}
	// None of the forgeries spent the real phone's adopt.
	require.True(t, local.ClaimGranted(edgeauth.NodeID(pub)))
	_, err = enrollhttp.Claim(context.Background(), url, claimFor(pub, priv, time.Now()))
	require.NoError(t, err)
}

func TestTheClaimEndpointAnswersRefusalsAs403WithTheReason(t *testing.T) {
	_, url, _ := rig(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	body := fmt.Sprintf(`{"node_id":%q,"node_key":"%x","name":"pixel","kind":"mobile","nonce":"00","ts":%d,"sig":"00"}`,
		edgeauth.NodeID(pub), []byte(pub), time.Now().Unix())
	code, text := status(t, http.MethodPost, url+enrollhttp.ClaimPath, body)
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, text, "no usable signature")
}

func TestTheClaimEndpointRefusesWhatIsNotAClaim(t *testing.T) {
	_, url, _ := rig(t)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		code, _ := status(t, m, url+enrollhttp.ClaimPath, "")
		require.Equal(t, http.StatusMethodNotAllowed, code, m)
	}
	for _, body := range []string{"", "not json", `{"node_id":`, `[]`, `"a string"`} {
		code, text := status(t, http.MethodPost, url+enrollhttp.ClaimPath, body)
		require.Equal(t, http.StatusBadRequest, code, body)
		require.Equal(t, "that is not a claim", text)
	}
}

func TestTheEnrollEndpointRefusesEveryWrongMethodAndNonRequest(t *testing.T) {
	_, url, _ := rig(t)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		code, text := status(t, m, url+enrollhttp.Path, "")
		require.Equal(t, http.StatusMethodNotAllowed, code, m)
		require.Equal(t, "method not allowed", text)
	}
	for _, body := range []string{"", "not json", `{"user_key":`, `[]`} {
		code, text := status(t, http.MethodPost, url+enrollhttp.Path, body)
		require.Equal(t, http.StatusBadRequest, code, body)
		require.Equal(t, "that is not an enrollment request", text)
	}
}

// An unauthenticated caller cannot make the authority hold more than maxBody (64 KiB): the body is
// cut there, so an oversized document is never parsed as a request, whatever it would have said.
func TestOversizedBodiesAreCutAndRefused(t *testing.T) {
	_, url, _ := rig(t)
	huge := `{"name":"` + strings.Repeat("A", 70<<10) + `"}`
	code, text := status(t, http.MethodPost, url+enrollhttp.Path, huge)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "that is not an enrollment request", text)

	code, text = status(t, http.MethodPost, url+enrollhttp.ClaimPath, huge)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "that is not a claim", text)
}

// A body that breaks while it is being read is a 400, not a hang and not a 500.
func TestABodyThatCannotBeReadIs400(t *testing.T) {
	local, _, _ := rig(t)
	h := enrollhttp.Handler(local)
	for path, want := range map[string]string{
		enrollhttp.Path:      "that request could not be read",
		enrollhttp.ClaimPath: "that claim could not be read",
	} {
		req := httptest.NewRequest(http.MethodPost, path, io.NopCloser(iotest.ErrReader(errors.New("wire cut"))))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, path)
		require.Equal(t, want, strings.TrimSpace(rec.Body.String()))
	}
}

func TestTheEnrollEndpointRefusesForgedStaleAndReplayedRequestsWith403(t *testing.T) {
	_, url, user := rig(t)
	ctx := context.Background()

	// Replay: the exact same signed request a second time.
	good := ask(t, user)
	_, err := enrollhttp.Enroll(ctx, url, good)
	require.NoError(t, err)
	_, err = enrollhttp.Enroll(ctx, url, good)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refused")

	// Tampered after signing.
	tampered := ask(t, user)
	tampered.Name = "renamed-in-flight"
	_, err = enrollhttp.Enroll(ctx, url, tampered)
	require.ErrorContains(t, err, edgeauth.ErrBadSignature.Error())

	// Stale.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stale := edgeauth.NewRequest("", "workshop", "host", pub, time.Now().Add(-time.Hour))
	stale.Sign(user)
	_, err = enrollhttp.Enroll(ctx, url, stale)
	require.ErrorContains(t, err, edgeauth.ErrStaleRequest.Error())

	// And over a raw POST every one of these is a 403, never a 200 or a 500.
	code, _ := status(t, http.MethodPost, url+enrollhttp.Path, `{"user_key":"00","sig":"00"}`)
	require.Equal(t, http.StatusForbidden, code)
}

// errIssuer is an authority whose Issue and Claim fail with a chosen error. It exists only to pin
// the handler's error MAPPING, which no real authority can be made to exercise for every sentinel.
type errIssuer struct{ err error }

func (e errIssuer) Issue(edgeauth.Request) (edgeauth.Response, error) { return edgeauth.Response{}, e.err }
func (e errIssuer) Claim(edgeauth.ClaimRequest, time.Time) (edgeauth.Response, error) {
	return edgeauth.Response{}, e.err
}

func TestEveryShapedRefusalIsA403AndEverythingElseIsAnOpaque500(t *testing.T) {
	shaped := []error{
		edgeauth.ErrBadSignature, edgeauth.ErrStaleRequest, edgeauth.ErrReplayed,
		edgeauth.ErrKeyMismatch, edgeauth.ErrMalformed, edgeauth.ErrUnknownMachine,
		edgeauth.ErrForeignAccount, edgeauth.ErrNotAdopted,
	}
	for _, sentinel := range shaped {
		wrapped := fmt.Errorf("%w: detail", sentinel)
		srv := httptest.NewServer(enrollhttp.Handler(errIssuer{wrapped}))
		for _, path := range []string{enrollhttp.Path, enrollhttp.ClaimPath} {
			code, text := status(t, http.MethodPost, srv.URL+path, `{}`)
			require.Equal(t, http.StatusForbidden, code, "%s on %s", sentinel, path)
			require.Equal(t, wrapped.Error(), text)
		}
		srv.Close()
	}

	// An unexpected failure may carry a filesystem path: it must never reach the caller.
	leaky := fmt.Errorf("open /home/owner/.roger/edge/authority/issued.json: %w", os.ErrPermission)
	srv := httptest.NewServer(enrollhttp.Handler(errIssuer{leaky}))
	defer srv.Close()
	for path, what := range map[string]string{enrollhttp.Path: "enrollment", enrollhttp.ClaimPath: "claim"} {
		code, text := status(t, http.MethodPost, srv.URL+path, `{}`)
		require.Equal(t, http.StatusInternalServerError, code)
		require.Equal(t, "the "+what+" could not be completed", text)
		require.NotContains(t, text, "/home/owner")
		require.NotContains(t, text, "issued.json")
	}
}

// A real authority whose state is broken on disk fails with an unshaped error: the caller gets the
// opaque 500, and no certificate.
func TestABrokenAuthorityAnswersAnOpaque500(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)
	pub, priv := adoptedPhone(t, local)
	srv := httptest.NewServer(enrollhttp.Handler(local))
	defer srv.Close()
	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.AuthorityDir, "revoked.json"), []byte("{bad"), 0o600))

	resp, err := enrollhttp.Claim(context.Background(), srv.URL, claimFor(pub, priv, time.Now()))
	require.ErrorContains(t, err, "the claim could not be completed")
	require.NotContains(t, err.Error(), dir)
	require.Equal(t, edgeauth.Response{}, resp)
}

func TestAnAuthorityThatTakesNoClaimsSaysSo(t *testing.T) {
	srv := httptest.NewServer(enrollhttp.Handler(bareIssuer{}))
	defer srv.Close()
	code, text := status(t, http.MethodPost, srv.URL+enrollhttp.ClaimPath, `{}`)
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, "this authority does not accept claims", text)
	code, text = status(t, http.MethodGet, srv.URL+enrollhttp.RootPath, "")
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, "this authority publishes no root", text)
	code, text = status(t, http.MethodGet, srv.URL+enrollhttp.RevocationsPath, "")
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, "this authority publishes no revocation list", text)

	_, err := enrollhttp.Claim(context.Background(), srv.URL, edgeauth.ClaimRequest{})
	require.ErrorContains(t, err, "this authority does not accept claims")
}

func TestAClaimAnswerThatCannotBeReadIsMalformed(t *testing.T) {
	truncating := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"cert": "half`))
	}))
	defer truncating.Close()
	_, err := enrollhttp.Claim(context.Background(), truncating.URL, edgeauth.ClaimRequest{})
	require.ErrorIs(t, err, edgeauth.ErrMalformed)
}

func TestAClaimToAnAuthorityThatIsNotThereIsAnError(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	addr := dead.URL
	dead.Close()
	_, err := enrollhttp.Claim(context.Background(), addr, edgeauth.ClaimRequest{})
	require.Error(t, err)
	_, err = enrollhttp.Claim(context.Background(), "://not a url", edgeauth.ClaimRequest{})
	require.Error(t, err)
	_, err = enrollhttp.Revocations(context.Background(), "://not a url")
	require.Error(t, err)
}

// An answer cut off mid-body (the server promised more bytes than it sent) is an error, never a
// short certificate.
func TestAnAnswerCutOffMidBodyIsAnError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.ReadAll(io.LimitReader(c, 1)) // let the request start arriving
			_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n{\"cert\":"))
			_ = c.Close()
		}
	}()
	_, err = enrollhttp.Root(context.Background(), "http://"+ln.Addr().String())
	require.Error(t, err)
}

// A cancelled context ends the attempt instead of waiting on the authority.
func TestACancelledEnrollmentReturnsPromptly(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := enrollhttp.Enroll(ctx, srv.URL, edgeauth.Request{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}
