package main

// storeoutage_test.go - one failed shared-store operation is a blip, not an outage: dispatch
// treats the store as down only after it has been failing continuously for the debounce.

import (
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
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
