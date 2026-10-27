package main

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/protocol"
)

// clientProxyResponseHeaderTimeout mirrors internal/client/client.go proxyResponseHeaderTimeout
// (30s): the installed CLI's relay transport gives up on a response that shows no headers
// within it. It is unexported there, so it is mirrored here and pinned on both sides
// (internal/client TestProxyResponseHeaderTimeoutPinned).
const clientProxyResponseHeaderTimeout = 30 * time.Second

// TestStreamCommitGraceBeatsProxyHeaderTimeout: the streaming relay withholds its SSE headers
// for at most streamCommitGrace while waiting for a first verdict; that MUST be shorter than
// the client's header timeout or a slow first token dies at the local proxy.
func TestStreamCommitGraceBeatsProxyHeaderTimeout(t *testing.T) {
	require.Less(t, streamCommitGrace, clientProxyResponseHeaderTimeout)
	require.Equal(t, 20*time.Second, streamCommitGrace)
}

// TestBandAllow: a failover re-pick under a private band stays inside the band's admission
// set (intersected with a grant's allow-list); without a band the request's own allow-list
// (nil = everyone) stands.
func TestBandAllow(t *testing.T) {
	cases := []struct {
		name         string
		allow, band  map[string]bool
		want         map[string]bool
		wantNilAllow bool
	}{
		{name: "no band, no allow-list", wantNilAllow: true},
		{name: "no band keeps the allow-list", allow: map[string]bool{"a": true}, want: map[string]bool{"a": true}},
		{name: "band alone admits only the band", band: map[string]bool{"s1": true, "s2": true}, want: map[string]bool{"s1": true, "s2": true}},
		{name: "band intersected with a grant allow-list", allow: map[string]bool{"s1": true, "g": true}, band: map[string]bool{"s1": true, "s2": true}, want: map[string]bool{"s1": true}},
		{name: "band disjoint from the allow-list admits nothing", allow: map[string]bool{"g": true}, band: map[string]bool{"s1": true}, want: map[string]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bandAllow(tc.allow, tc.band)
			if tc.wantNilAllow {
				require.Nil(t, got)
				return
			}
			require.Equal(t, tc.want, got)
		})
	}
}

// TestLazySSEPreCommitCap: the pre-commit buffer is bounded. A station that streams 64 KiB of
// non-data bytes (comments, keepalives) before its first data frame gets its headers committed
// and the bytes flushed at the cap instead of buffering without limit; below the cap nothing
// is on the wire yet (so a no-output failure can still fail over).
func TestLazySSEPreCommitCap(t *testing.T) {
	rec := httptest.NewRecorder()
	lw := &lazySSE{w: rec, flusher: rec}
	lw.begin("n1")
	chunk := bytes.Repeat([]byte(": keepalive\n"), 1024) // 12 KiB, no data: frame
	written := 0
	for written+len(chunk) < lazySSEPreCap {
		_, err := lw.Write(chunk)
		require.NoError(t, err)
		written += len(chunk)
		require.False(t, lw.isCommitted(), "committed at %d bytes, under the %d cap", written, lazySSEPreCap)
		require.Equal(t, 0, rec.Body.Len())
	}
	_, err := lw.Write(chunk) // crosses the cap
	require.NoError(t, err)
	written += len(chunk)
	require.True(t, lw.isCommitted())
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "n1", rec.Header().Get("X-RogerAI-Provider"))
	require.True(t, rec.Flushed)
	require.Equal(t, written, rec.Body.Len(), "the buffered bytes were flushed, none retained")
}

// TestNextAttemptHonorsTheMinimumAttemptBudget pins the deadline gate of the failover
// re-pick with no clock race in it: a sibling is re-tried only while at least
// relayMinAttemptBudget remains on the request's single deadline (the 10s-of-90s shape the
// routing/upstream_failover "respects the request's own deadline" scenario drives end to
// end), a zero deadline (the streaming relay) never gates, and a non-failoverable status
// never re-picks no matter how much time is left. Margins are minutes, not milliseconds.
func TestNextAttemptHonorsTheMinimumAttemptBudget(t *testing.T) {
	s1, s2 := &nodeTunnel{}, &nodeTunnel{}
	b := &broker{tunnels: map[string]*nodeTunnel{"s1": s1, "s2": s2}}
	plan := []attemptCand{
		{node: protocol.NodeRegistration{NodeID: "s1"}, t: s1},
		{node: protocol.NodeRegistration{NodeID: "s2"}, t: s2},
	}
	cases := []struct {
		name      string
		remaining time.Duration // 0 = no deadline
		status    int
		want      int
	}{
		{"plenty of budget left re-picks the sibling", time.Hour, 500, 1},
		{"a minute more than the budget left re-picks", relayMinAttemptBudget + time.Minute, 500, 1},
		{"less than the budget left does not start a second attempt", relayMinAttemptBudget / 2, 500, -1},
		{"a lapsed deadline does not start a second attempt", -time.Minute, 500, -1},
		{"no deadline never gates", 0, 500, 1},
		{"a non-failoverable status never re-picks", time.Hour, 400, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var deadline time.Time
			if tc.remaining != 0 {
				deadline = time.Now().Add(tc.remaining)
			}
			require.Equal(t, tc.want, b.nextAttempt(plan, 0, tc.status, deadline))
		})
	}
}
