package main

// genrecord_race_test.go - a late moderation verdict racing the relay's own record write must
// never be lost or overwritten (slice-3 audit, 2026-10-04). The verdict arrives from the async
// screener on the same instance at any moment around genClose.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

func TestLateVerdictSurvivesARacingRecordWrite(t *testing.T) {
	b := relayBroker(store.NewMem())
	for i := 0; i < 400; i++ {
		id := "race-" + strconv.Itoa(i)
		g := b.genOpen(id, time.Now())
		g.admit()
		w := &genWriter{ResponseWriter: httptest.NewRecorder()}
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); b.genClose(g, w, r) }()
		go func() { defer wg.Done(); b.genVerdictLater(id, "flagged_after_serve") }()
		wg.Wait()
		blob, found, err := b.genGet(id)
		require.NoError(t, err)
		require.True(t, found, "record %s was never written", id)
		var st genStored
		require.NoError(t, json.Unmarshal(blob, &st))
		require.Equal(t, "flagged_after_serve", st.Rec.Moderation.Verdict, "iteration %d: the late verdict was lost or overwritten", i)
	}
}
