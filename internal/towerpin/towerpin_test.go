package towerpin

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// serveBoth serves h over one listener that answers TLS (a cert self-signed by priv) and plain
// http on the same port.
func serveBoth(t *testing.T, priv ed25519.PrivateKey, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cfg, err := ServerTLS(priv)
	require.NoError(t, err)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(Sniff(ln, cfg)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			_, _ = io.WriteString(w, "tls")
			return
		}
		_, _ = io.WriteString(w, "plain")
	})
}

func get(t *testing.T, hc *http.Client, url string) (string, error) {
	t.Helper()
	resp, err := hc.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// features/join/tower_identity.feature: a pinned client is accepted only when the Tower's key
// hashes to the pin; one port answers TLS and, for one release, plain http.
func TestAPinnedClientTrustsOnlyThePinnedKey(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	addr := serveBoth(t, priv, okHandler())
	pin := Fingerprint(pub)
	require.Len(t, pin, 64)

	body, err := get(t, Client(pin, 5*time.Second), "https://"+addr+"/")
	require.NoError(t, err)
	require.Equal(t, "tls", body)

	other, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err = get(t, Client(Fingerprint(other), 5*time.Second), "https://"+addr+"/")
	require.ErrorIs(t, err, ErrPinMismatch, "a different key is refused")

	plain, err := get(t, &http.Client{Timeout: 5 * time.Second}, "http://"+addr+"/")
	require.NoError(t, err)
	require.Equal(t, "plain", plain, "plain http still answers on the same port")
}

// A pinned client never follows a redirect.
func TestAPinnedClientFollowsNoRedirect(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	addr := serveBoth(t, priv, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://203.0.113.9/", http.StatusFound)
	}))
	resp, err := Client(Fingerprint(pub), 5*time.Second).Get("https://" + addr + "/")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
}

func TestFingerprintAndParse(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	fp := Fingerprint(pub)
	got, ok := Parse("SHA256:" + fp)
	require.True(t, ok)
	require.Equal(t, fp, got)
	_, ok = Parse(fp[:16])
	require.False(t, ok, "a short fingerprint is never a pin")
	require.Equal(t, fp[:4]+"-"+fp[4:8], Short(fp)[:9])
}

// A connection that never sends a byte is closed after the deadline, and does not stop another
// caller from being served meanwhile.
func TestASilentConnectionDoesNotBlockOthers(t *testing.T) {
	prev := sniffDeadline
	sniffDeadline = 200 * time.Millisecond
	t.Cleanup(func() { sniffDeadline = prev })
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	addr := serveBoth(t, priv, okHandler())
	silent, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer silent.Close()
	body, err := get(t, &http.Client{Timeout: 2 * time.Second}, "http://"+addr+"/")
	require.NoError(t, err)
	require.Equal(t, "plain", body)
	_ = silent.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = silent.Read(make([]byte, 1))
	require.Error(t, err, "the silent connection was closed")
}

func TestAClosedListenerEndsAccept(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	cfg, err := ServerTLS(priv)
	require.NoError(t, err)
	s := Sniff(ln, cfg)
	require.NoError(t, ln.Close())
	_, err = s.Accept()
	require.Error(t, err)
}

func TestShortAndParseEdges(t *testing.T) {
	require.Equal(t, "abc", Short("abc"))
	_, ok := Parse("sha256:" + string(make([]byte, 64)))
	require.False(t, ok)
	cfg := ClientTLS("00")
	require.ErrorIs(t, cfg.VerifyConnection(tls.ConnectionState{}), ErrPinMismatch)
}
