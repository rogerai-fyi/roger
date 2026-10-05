package main

// storeoutage_test.go - one failed shared-store operation is a blip, not an outage: dispatch
// treats the store as down only after it has been failing continuously for the debounce.

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

func TestDispatchTreatsOnlyASustainedFailureAsAnOutage(t *testing.T) {
	mr := miniredis.RunT(t)
	vs, err := newValkeyStoreTopology(valkeyTopology{urls: []string{"redis://" + mr.Addr()}})
	require.NoError(t, err)
	b := &broker{shared: vs, multiInstance: true}

	prev := storeOutageDebounce
	storeOutageDebounce = 300 * time.Millisecond
	defer func() { storeOutageDebounce = prev }()

	vs.noteErr("blip", errors.New("one failed op"))
	require.False(t, b.dispatchStoreDown(), "one failed op is a blip, not an outage")

	_, _, _ = vs.cacheGet("probe") // a success clears it
	require.False(t, b.dispatchStoreDown())

	vs.noteErr("x", errors.New("failing"))
	time.Sleep(350 * time.Millisecond)
	vs.noteErr("x", errors.New("still failing"))
	require.True(t, b.dispatchStoreDown(), "a failure sustained past the debounce is an outage")

	_, _, _ = vs.cacheGet("probe") // recovery
	require.False(t, b.dispatchStoreDown(), "the first success ends the outage")
}

// TestMayHaveLandedSurvivesWrapping: a bus publish whose reply was lost stays marked as
// may-have-landed through the dispatch path's wrapping, so it is never retried in memory, while
// a store error raised before the job left is still eligible for the local handoff.
func TestMayHaveLandedSurvivesWrapping(t *testing.T) {
	lost := landedErr{errors.New("i/o timeout")}
	require.True(t, mayHaveLanded(lost))
	require.True(t, mayHaveLanded(fmt.Errorf("dispatch store: %w", lost)))
	require.Equal(t, "i/o timeout", lost.Error())
	require.False(t, mayHaveLanded(errors.New("subscribe refused")))
	require.False(t, mayHaveLanded(nil))
}

// A job one instance handed over in memory is local to THAT instance only. A result for it
// posted to a peer (which never held it) still routes through the shared store to the job's
// origin, so two instances in one process (the two-instance tests) behave as two processes.
func TestLocalJobIsLocalToTheInstanceThatHandedItOver(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newQBroker(t, priv, db, mr, dispatchViaQueueOnly)
	b := newQBroker(t, priv, db, mr, dispatchViaQueueOnly)
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	miRegisterNode(b, "n1", hex.EncodeToString(nodePub), "tok", []protocol.ModelOffer{{Model: "free-m"}})

	a.markLocalJob("j1")
	defer a.unmarkLocalJob("j1")
	require.True(t, a.isLocalJob("j1"))
	require.False(t, b.isLocalJob("j1"), "the peer never handed j1 over")

	// The result reaches the peer: it routes it to the origin's inbox, not its own memory.
	require.NoError(t, mr.Set(dqOriginPrefix+"j1", "origin-x"))
	require.Equal(t, http.StatusOK, qPostResult(t, b, "n1", "tok", "free-m", protocol.Job{ID: "j1"}, nodePriv))
	require.True(t, mr.Exists(dqInboxPrefix+"origin-x"), "the result was routed to the origin's inbox")
}
