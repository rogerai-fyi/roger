package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The spool is the courier's crash insurance: a receipt queued before a restart must come
// back after it, and only within its settle window.
func TestSettleSpoolSurvivesARestart(t *testing.T) {
	base := t.TempDir()
	sp, err := newSettleSpool(base)
	require.NoError(t, err)

	now := time.Now()
	live := pendingSettle{stationID: "st1", attemptID: "att-live", receipt: []byte("r1"),
		deadline: now.Add(5 * time.Minute)}
	dead := pendingSettle{stationID: "st1", attemptID: "att-dead", receipt: []byte("r2"),
		deadline: now.Add(-time.Minute)}
	require.NoError(t, sp.put(live))
	require.NoError(t, sp.put(dead))

	// "The restart": a fresh spool over the same dir.
	sp2, err := newSettleSpool(base)
	require.NoError(t, err)
	got := sp2.load(now)
	require.Len(t, got, 1, "the expired receipt is discarded, the live one recovered")
	require.Equal(t, "att-live", got[0].attemptID)
	require.Equal(t, []byte("r1"), got[0].receipt)
	require.Equal(t, "st1", got[0].stationID)

	// The expired entry's file is gone; a successful forward drops the live one too.
	sp2.drop("att-live")
	require.Empty(t, sp2.load(now), "nothing left after delivery")

	// An unreadable file is removed rather than re-loaded forever.
	junk := filepath.Join(base, spoolDirName, "junk.json")
	require.NoError(t, os.WriteFile(junk, []byte("{not json"), 0o600))
	require.Empty(t, sp2.load(now))
	_, statErr := os.Stat(junk)
	require.True(t, os.IsNotExist(statErr), "junk is cleaned up")

	// A nil spool (setup failed) degrades gracefully.
	var none *settleSpool
	require.NoError(t, none.put(live))
	none.drop("x")
	require.Empty(t, none.load(now))
}
