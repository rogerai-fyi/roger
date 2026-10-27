package edge

// A busy refusal must not spend the nonce (phone_consume.feature, "a turn refused for being busy
// can be sent again as it was"): the 503 says try again in two seconds, so the very same signed
// request, retried, has to be heard - not refused as a replay.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestABusyRefusalDoesNotSpendTheNonce(t *testing.T) {
	rig := newPhoneRig(t)
	pc, ok := rig.h.(*phoneConsume)
	require.True(t, ok, "the handler is the phone consume itself")
	// The phone's two slots are held by turns still streaming.
	require.True(t, pc.take(rig.phoneID))
	require.True(t, pc.take(rig.phoneID))

	s := signed{action: ActionInfer, nonce: freshNonce()}
	w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, s)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	require.Equal(t, "2", w.Header().Get("Retry-After"))

	pc.give(rig.phoneID)
	pc.give(rig.phoneID)
	w = rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, s)
	require.Equal(t, http.StatusOK, w.Code, "the same request, retried after the busy answer: %s", w.Body.String())

	w = rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, s)
	require.Equal(t, http.StatusForbidden, w.Code, "once served, it is a replay")
	require.Contains(t, w.Body.String(), "already seen")
}
