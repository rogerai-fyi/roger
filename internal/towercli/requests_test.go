package towercli

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joinsas"
	"rogerai.fm/roger/v6/internal/localplane"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
)

// servingPlane is a standalone Tower serving its plane and its admin socket, as `roger tower serve`
// does, so the owner's pairing commands have something to reach (features/join/approve.feature).
func servingPlane(t *testing.T) (string, *tower.State, *localplane.Server) {
	t.Helper()
	d, err := os.MkdirTemp("", "tq")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	dir := filepath.Join(d, "lab")
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	plane := localplane.New(st)
	ln, err := toweradmin.Listen(dir)
	require.NoError(t, err)
	go func() { _ = toweradmin.ServeWith(ln, st, plane, func(string, ...any) {}) }()
	t.Cleanup(func() { _ = ln.Close(); toweradmin.Remove(dir) })
	return dir, st, plane
}

// ask makes a real join request on the plane, as `roger join <address>` does: start, then reveal.
func ask(t *testing.T, plane *localplane.Server, name string) string {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	pub := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	nonce := joinsas.NewNonce()
	post := func(path string, v any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(v)
		n := protocol.NewNonce()
		_, ts, sig := protocol.SignRequestNonce(key, http.MethodPost, path, body, n)
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		r.Header.Set(protocol.HeaderPubkey, pub)
		r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
		r.Header.Set(protocol.HeaderSig, sig)
		r.Header.Set(protocol.HeaderNonce, n)
		rec := httptest.NewRecorder()
		plane.Handler().ServeHTTP(rec, r)
		return rec
	}
	rec := post("/local/pair/start", localplane.PairStartBody{Kind: "client", Name: name,
		ClientEph: hex.EncodeToString(eph.PublicKey().Bytes()), Commit: joinsas.Commit(nonce)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var a localplane.PairStartAnswer
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &a))
	require.Equal(t, http.StatusOK, post("/local/pair/reveal", localplane.PairRevealBody{ID: a.ID, ClientNonce: nonce}).Code)
	return a.ID
}

func TestRequestsListsWhoIsAskingWithThePhrase(t *testing.T) {
	dir, _, plane := servingPlane(t)
	out, err := runCLI(t, "requests", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "no one is asking to join")

	id := ask(t, plane, "laptop-x")
	out, err = runCLI(t, "requests", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, id+"  laptop-x wants to join · ")
	require.Contains(t, out, "approve only if laptop-x shows the same phrase: roger tower approve --dir "+dir+" "+id)

	// The hint is the command itself: run it exactly as printed and the device is admitted.
	hint := out[strings.Index(out, "roger tower approve "):]
	hint = hint[:strings.Index(hint, " (or ")]
	_, err = runCLI(t, strings.Fields(strings.TrimPrefix(hint, "roger tower "))...)
	require.NoError(t, err, "the printed approve command runs as is: %s", hint)
	out, err = runCLI(t, "requests", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "no one is asking to join", "the printed command approved the request")
}

func TestApproveAdmitsAndRefuseAdmitsNothing(t *testing.T) {
	dir, _, plane := servingPlane(t)
	id := ask(t, plane, "laptop-x")
	out, err := runCLI(t, "approve", "--dir", dir, id)
	require.NoError(t, err)
	require.Contains(t, out, "approved "+id)

	id2 := ask(t, plane, "phone")
	out, err = runCLI(t, "refuse", "--dir", dir, id2)
	require.NoError(t, err)
	require.Contains(t, out, "refused "+id2+"; nothing was admitted")

	_, err = runCLI(t, "approve", "--dir", dir, id2)
	require.Error(t, err, "a refused request cannot be approved after")
	_, err = runCLI(t, "approve", "--dir", dir)
	require.ErrorContains(t, err, "name the request")
}

func TestPairingNeedsAServingTower(t *testing.T) {
	_, err := runCLI(t, "requests", "--dir", t.TempDir())
	require.ErrorContains(t, err, "is not serving")
	_, err = runCLI(t, "requests")
	require.ErrorContains(t, err, "--dir is required")
}

func TestRequestLineNamesAStationAndItsModels(t *testing.T) {
	now := time.Now()
	line := RequestLine(tower.PairRequest{ID: "r1", Name: "kitchen-pi", Kind: tower.JoinStation, Phrase: "a-b-c-12",
		Models: []string{"qwen3-8b", "smollm2"}, ExpiresAt: now.Add(90 * time.Second)}, now)
	require.Equal(t, "r1  kitchen-pi (a station) wants to join · a-b-c-12 · serving: qwen3-8b, smollm2 · 1m30s left", line)
	require.False(t, strings.Contains(RequestLine(tower.PairRequest{ID: "r2", Name: "x", ExpiresAt: now.Add(-time.Second)}, now), "left"),
		"an expired request shows no time left")
}

func TestStationRevokeOnAStandaloneDirectoryWithNoServe(t *testing.T) {
	d, err := os.MkdirTemp("", "ts")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	dir := filepath.Join(d, "lab")
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	inv, code, err := st.CreateInvitation("u_owner", time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, "u_owner")
	require.NoError(t, err)
	_, err = st.AttachStation("s", "abababababababab", []string{"m"})
	require.NoError(t, err)

	out, err := runCLI(t, "station", "revoke", "--dir", dir, "--station-id", "s")
	require.NoError(t, err)
	require.Contains(t, out, "revoked station s")
	_, err = runCLI(t, "station", "revoke", "--dir", dir, "--station-id", "s")
	require.ErrorContains(t, err, "no Station")
}
