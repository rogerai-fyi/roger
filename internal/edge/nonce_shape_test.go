package edge

// A nonce that is not 16 random bytes as 32 lowercase hex is refused as MALFORMED, not as a replay
// (review 2026-10-26): a client sending a UUID or uppercase hex was told "already seen", which sends
// its author looking for a replay that never happened.

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const uuidNonce = "3f2a8c1e-9b4d-4e6f-a1c2-7d8e9f0a1b2c"

func TestAPresenceWithAMalformedNonceIsToldSo(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	h := PresenceHandler(f, a, time.Now)
	for _, nonce := range []string{uuidNonce, strings.ToUpper(freshNonce())} {
		p := validPresence(id, certPEM)
		p.Nonce = nonce
		signPresence(&p, priv)
		w := doPresence(h, p)
		require.Equal(t, http.StatusForbidden, w.Code)
		require.Contains(t, w.Body.String(), "malformed nonce", nonce)
		require.NotContains(t, w.Body.String(), "already seen", nonce)
	}
}

func TestAnInvocationWithAMalformedNonceIsToldSo(t *testing.T) {
	rig := newInvokeRig(t)
	req := freshInvoke()
	req.Nonce = uuidNonce
	resp := rig.post(t, req)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(b), "malformed nonce")
}
