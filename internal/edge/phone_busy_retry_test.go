package edge

// A busy refusal must not spend the nonce (phone_consume.feature, "a turn refused for being busy
// can be sent again as it was"): the 503 says try again in two seconds, so the very same signed
// request, retried, has to be heard - not refused as a replay.

import (
	"net/http"
	"sync"
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

// Adversarial review 2026-10-27: a replay of a request already served is refused as already seen
// BEFORE it takes a slot, so a flood of replays (a face that relayed the turn holds its bytes) takes
// none of the member's slots, and the member's own fresh turns are never told busy because of it.
func TestAReplayTakesNoneOfItsMembersSlots(t *testing.T) {
	rig := newPhoneRig(t)
	pc := rig.h.(*phoneConsume)
	captured := signed{action: ActionBands, nonce: freshNonce()}
	require.Equal(t, http.StatusOK, rig.ask(t, http.MethodPost, PhoneBandsPath, "", captured).Code)
	require.True(t, pc.take(rig.phoneID)) // one turn streaming: one slot left

	// A replay while the member is busy is a replay, not "busy": no slot, and no hint that it is busy.
	require.True(t, pc.take(rig.phoneID))
	w := rig.ask(t, http.MethodPost, PhoneBandsPath, "", captured)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "already seen")
	pc.give(rig.phoneID)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					rig.ask(t, http.MethodPost, PhoneBandsPath, "", captured)
				}
			}
		}()
	}
	busy := 0
	for i := 0; i < 300; i++ {
		if rig.ask(t, http.MethodPost, PhoneBandsPath, "", signed{action: ActionBands}).Code == http.StatusServiceUnavailable {
			busy++
		}
	}
	close(stop)
	wg.Wait()
	require.Zero(t, busy, "the member's own turns were told busy because of replays")
}

// Adversarial review 2026-10-27: only "busy" can be retried as it was. A turn refused after it was
// taken in (here: the member did not answer) spent its nonce, so the same bytes are a replay.
func TestATurnRefusedAfterItWasTakenInIsSignedAgainToRetry(t *testing.T) {
	rig := newPhoneRig(t)
	rig.face.Close()
	s := signed{action: ActionInfer, nonce: freshNonce()}
	w := rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, s)
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	w = rig.ask(t, http.MethodPost, PhoneInferPath, chatBody, s)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "already seen")
}
