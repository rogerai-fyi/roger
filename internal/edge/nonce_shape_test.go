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

// Adversarial review 2026-10-27: the phone paths say "malformed nonce" too (a nonce's shape tells a
// caller nothing about who is enrolled), and a node at its quota of live nonces is told to slow
// down, not that a fresh request "was already seen".
func TestAPhoneTurnWithAMalformedNonceIsToldSo(t *testing.T) {
	rig := newPhoneRig(t)
	for _, nonce := range []string{uuidNonce, strings.ToUpper(freshNonce())} {
		w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, signed{action: ActionInfer, nonce: nonce})
		require.Equal(t, http.StatusForbidden, w.Code)
		require.Contains(t, w.Body.String(), "malformed nonce", nonce)
	}
}

func TestANodeAtItsNonceQuotaIsToldToSlowDown(t *testing.T) {
	c := newNonceCache()
	now := time.Now().Unix()
	for i := 0; i < maxNoncesPerNode; i++ {
		require.True(t, c.admit("n", freshNonce(), now+300, now))
	}
	fresh := freshNonce()
	require.False(t, c.admit("n", fresh, now+300, now))
	require.Contains(t, c.refusal("n", fresh, now), "too many")
	spent := freshNonce()
	other := newNonceCache()
	require.True(t, other.admit("m", spent, now+300, now))
	require.False(t, other.admit("m", spent, now+300, now))
	require.Equal(t, "", other.refusal("m", spent, now), "a real replay is the caller's own words")
	require.Equal(t, malformedNonce, other.refusal("m", "nope", now))
}
