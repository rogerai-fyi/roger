package enrollhttp_test

// The wire, over a REAL listener with a REAL authority behind it. The client and the
// handler in this package are the ones Core and a machine in a shed both run; the only
// thing a test changes is the address.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// rig stands the production handler up over a real local authority.
func rig(t *testing.T) (*edgeauth.Local, string, ed25519.PrivateKey) {
	t.Helper()
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	_, user, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, local.Allow(hex.EncodeToString(user.Public().(ed25519.PublicKey))))
	srv := httptest.NewServer(enrollhttp.Handler(local))
	t.Cleanup(srv.Close)
	return local, srv.URL, user
}

func ask(t *testing.T, user ed25519.PrivateKey) edgeauth.Request {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	r := edgeauth.NewRequest("", "workshop", "host", pub, time.Now())
	r.Sign(user)
	return r
}

func TestOneEndpointShapeCarriesAWholeEnrollment(t *testing.T) {
	local, url, user := rig(t)
	ctx := context.Background()

	resp, err := enrollhttp.Enroll(ctx, url, ask(t, user))
	require.NoError(t, err)
	require.Equal(t, edgeauth.LocalAccount, resp.Account)
	leaf, err := edgeauth.DecodeCert(resp.Cert)
	require.NoError(t, err)

	// The PUBLIC root, and only the public root.
	rootPEM, err := enrollhttp.Root(ctx, url)
	require.NoError(t, err)
	require.Equal(t, local.RootPEM(), rootPEM)
	require.NotContains(t, rootPEM, "PRIVATE")

	// A trailing slash on the address is the caller's business, not ours.
	_, err = enrollhttp.Root(ctx, url+"/")
	require.NoError(t, err)

	rev, err := enrollhttp.Revocations(ctx, url)
	require.NoError(t, err)
	require.Empty(t, rev)
	serial, err := local.Revoke(resp.NodeID)
	require.NoError(t, err)
	require.Equal(t, leaf.SerialNumber.String(), serial)
	rev, err = enrollhttp.Revocations(ctx, url)
	require.NoError(t, err)
	require.Equal(t, []string{serial}, rev)
}

func TestARefusalComesBackAsARefusalAndNothingElse(t *testing.T) {
	_, url, _ := rig(t)
	_, stranger, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	resp, err := enrollhttp.Enroll(context.Background(), url, ask(t, stranger))
	require.Error(t, err)
	require.Contains(t, err.Error(), "refused")
	require.Equal(t, edgeauth.Response{}, resp, "there is no half answer to half apply")
}

func TestTheHandlerRefusesWhatIsNotAnEnrollment(t *testing.T) {
	_, url, _ := rig(t)

	got, err := http.Get(url + enrollhttp.Path)
	require.NoError(t, err)
	defer got.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, got.StatusCode)

	got, err = http.Post(url+enrollhttp.Path, "application/json", http.NoBody)
	require.NoError(t, err)
	defer got.Body.Close()
	require.Equal(t, http.StatusBadRequest, got.StatusCode)
}

// bareIssuer publishes neither a root nor a revocation list: the handler must say so
// rather than answer with something made up.
type bareIssuer struct{}

func (bareIssuer) Issue(edgeauth.Request) (edgeauth.Response, error) {
	return edgeauth.Response{}, errors.New("no")
}

func TestAnAuthorityThatPublishesNeitherSaysSo(t *testing.T) {
	srv := httptest.NewServer(enrollhttp.Handler(bareIssuer{}))
	defer srv.Close()
	ctx := context.Background()

	_, err := enrollhttp.Root(ctx, srv.URL)
	require.Error(t, err)
	_, err = enrollhttp.Revocations(ctx, srv.URL)
	require.Error(t, err)
}

// oddIssuer answers 200 with something that is not a Response at all.
type oddIssuer struct{}

func (oddIssuer) Issue(edgeauth.Request) (edgeauth.Response, error) {
	return edgeauth.Response{}, nil
}

func TestAnAnswerThatCannotBeReadIsMalformed(t *testing.T) {
	truncating := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"cert": "half`))
	}))
	defer truncating.Close()
	ctx := context.Background()

	_, err := enrollhttp.Enroll(ctx, truncating.URL, edgeauth.Request{})
	require.ErrorIs(t, err, edgeauth.ErrMalformed)
	_, err = enrollhttp.Revocations(ctx, truncating.URL)
	require.ErrorIs(t, err, edgeauth.ErrMalformed)
}

func TestAnAuthorityThatIsNotThereIsAnError(t *testing.T) {
	// A port nothing is listening on: no route to this authority.
	dead := httptest.NewServer(http.NotFoundHandler())
	addr := dead.URL
	dead.Close()
	ctx := context.Background()

	_, err := enrollhttp.Enroll(ctx, addr, edgeauth.Request{})
	require.Error(t, err)
	_, err = enrollhttp.Root(ctx, addr)
	require.Error(t, err)
	_, err = enrollhttp.Enroll(ctx, "://not a url", edgeauth.Request{})
	require.Error(t, err)
	_, err = enrollhttp.Root(ctx, "://not a url")
	require.Error(t, err)
}

func TestABareIssuerStillIssues(t *testing.T) {
	// The handler takes the narrow Issuer interface, so a plain issuer with no custody
	// behind it serves the same endpoint. This is what Core mounts.
	a, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	_, user, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	userHex := hex.EncodeToString(user.Public().(ed25519.PublicKey))
	iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{
		Authority: a,
		Accounts: func(k string) (string, bool) {
			return "owner", k == userHex
		},
	})
	srv := httptest.NewServer(enrollhttp.Handler(iss))
	defer srv.Close()

	resp, err := enrollhttp.Enroll(context.Background(), srv.URL, ask(t, user))
	require.NoError(t, err)
	require.Equal(t, "owner", resp.Account)

	rootPEM, err := enrollhttp.Root(context.Background(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, edgeauth.EncodeCert(a.Root()), rootPEM)
}
