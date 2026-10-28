package main

// Coverage for the trouble feed as a terminal reads it (every line the feed can carry, and the real
// pinned dial against a real TLS face) and for `roger edge alert`'s rule verbs and test call.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

func TestEdgePrintTroubleFeedWordsEveryLine(t *testing.T) {
	now := time.Now()
	f := edge.TroubleFeed{At: now.Unix(), Cursor: "c42", Rate: 12,
		Standing: []edge.TroubleItem{
			{Priority: "HI", SinceS: 120, Node: "hot-pi", When: "cpu_temp > 85", Value: "99.0°C", Why: "it throttles", Do: "check the fan"},
			{Priority: "MED", SinceS: 60, Node: "nas", When: "offline > 5", Value: "-", Acked: true, AckBy: "luis", Why: "w", Do: "d"},
			{Priority: "LOW", SinceS: 30, Node: "cam", When: "memory > 90", Value: "95%", Shelved: true, UntilAt: now.Add(time.Hour).Unix(), Stale: true, Why: "w", Do: "d"},
		},
		Transitions: []edge.TroubleTransition{
			{At: now.Unix(), When: "cpu_temp > 85", Node: "hot-pi", Firing: true},
			{At: now.Unix(), When: "disk > 90", Node: "nas"},
			{At: now.Unix(), When: "test call", Node: "phone", Test: true},
		},
		SignOff: edge.TroubleSignOff{Raised: 4, Cleared: 2, Loudest: "hot-pi", LoudestCount: 3,
			DeadAir: []edge.TroubleDeadAir{{Node: "nas", ForS: 600}}},
	}
	out := captureStdout(t, func() { edgePrintTroubleFeed("desk", f) })
	require.Contains(t, out, "TROUBLE on desk · 3 standing · PILE-UP: 12 in 10 min")
	require.Contains(t, out, "hot-pi")
	require.Contains(t, out, "why it matters  it throttles")
	require.Contains(t, out, "do              check the fan")
	require.Contains(t, out, "ROGER luis")
	require.Contains(t, out, "STAND BY until "+time.Unix(f.Standing[2].UntilAt, 0).Local().Format("15:04"))
	require.Contains(t, out, "  STALE")
	require.Contains(t, out, "fired    cpu_temp > 85 on hot-pi")
	require.Contains(t, out, "cleared  disk > 90 on nas")
	require.Contains(t, out, "TEST     test call on phone")
	require.Contains(t, out, "sign-off  this shift  4 raised · 2 cleared · loudest hot-pi (3) · dead air nas 10m")
	require.Contains(t, out, "cursor c42")
	require.NotContains(t, out, "ALL CLEAR")

	quiet := captureStdout(t, func() { edgePrintTroubleFeed("desk", edge.TroubleFeed{At: now.Unix(), Rate: 2}) })
	require.Contains(t, quiet, "TROUBLE on desk · 0 standing · 2 in the last 10 min")
	require.Contains(t, quiet, "ALL CLEAR - nothing is over its line")
	require.NotContains(t, quiet, "recent")
	require.NotContains(t, quiet, "sign-off")
}

func TestEdgeDialTroubleAgainstARealFace(t *testing.T) {
	var gotSince string
	status, body := http.StatusOK, `{"at":1,"cursor":"c1","standing":[],"transitions":[],"rate":0,"sign_off":{}}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, edge.TroublePath, r.URL.Path)
		gotSince = r.URL.Query().Get("since")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	leaf := srv.TLS.Certificates[0]
	pin := fmt.Sprintf("%x", sha256.Sum256(leaf.Certificate[0]))
	p := client.EdgePeer{Addr: strings.TrimPrefix(srv.URL, "https://"), Pin: pin, Node: "desk"}
	ctx := context.Background()

	feed, err := edgeDialTrouble(ctx, p, &leaf, "c0")
	require.NoError(t, err)
	require.Equal(t, "c1", feed.Cursor)
	require.Equal(t, "c0", gotSince, "the cursor rides as since")

	status, body = http.StatusForbidden, `{"error":{"message":"not a member"}}`
	_, err = edgeDialTrouble(ctx, p, &leaf, "")
	require.EqualError(t, err, "desk refused the feed: not a member")
	status, body = http.StatusInternalServerError, `oops`
	_, err = edgeDialTrouble(ctx, p, &leaf, "")
	require.EqualError(t, err, "desk refused the feed: 500 Internal Server Error", "no message: the status is the reason")
	status, body = http.StatusOK, `not a feed`
	_, err = edgeDialTrouble(ctx, p, &leaf, "")
	require.ErrorContains(t, err, "desk answered with something that is not a feed")

	bad := p
	bad.Pin = "beef"
	_, err = edgeDialTrouble(ctx, bad, &leaf, "")
	require.ErrorContains(t, err, "could not reach desk's face")
	require.ErrorContains(t, err, "does not match its pin")
}

func TestEdgeTroubleNeedsAFaceAndACertificate(t *testing.T) {
	newFacedAuthority(t, "desk")
	prev := edgeCallerCertFn
	t.Cleanup(func() { edgeCallerCertFn = prev })
	edgeCallerCertFn = func() *tls.Certificate { return nil }
	out, code := edgeRun(t, "edge", "trouble")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "no Edge certificate to read a feed with")
	out, code = edgeRun(t, "edge", "trouble", "ghost")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, `no such node on this Edge: "ghost"`)
}

func TestEdgeAlertRuleVerbsAndTheTestCall(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "board"})

	for _, args := range [][]string{
		{"edge", "alert", "off"}, {"edge", "alert", "on"}, {"edge", "alert", "rm"},
		{"edge", "alert", "test"}, {"edge", "alert", "bogus"},
	} {
		out, code := edgeRun(t, args...)
		require.Equal(t, 2, code, "%v is a usage error: %s", args, out)
		require.Contains(t, out, "usage: roger edge alert")
	}

	out, code := edgeRun(t, "edge", "alert", "off", "cpu_temp")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "rule cpu_temp is off")
	rs, err := edgeAlertLog().Rules()
	require.NoError(t, err)
	for _, r := range rs {
		if r.ID == "cpu_temp" {
			require.True(t, r.Off)
		}
	}
	out, code = edgeRun(t, "edge", "alert", "on", "cpu_temp")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "rule cpu_temp is on")

	out, code = edgeRun(t, "edge", "alert", "set", "disk", "gt", "70", "--scope", "attic")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "rule disk-n_attic:")
	out, code = edgeRun(t, "edge", "alert", "rm", "disk-n_attic")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "rule disk-n_attic removed")

	out, code = edgeRun(t, "edge", "alert", "test", "ghost")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "a test call goes to a member's Roger app")
	out, code = edgeRun(t, "edge", "alert", "test", "attic")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "test call sent to the feed for attic - it appears once there, marked TEST")
	feed, err := edgeAlertLog().Feed(time.Now().Add(-time.Hour), time.Now(), func(string) string { return "" })
	require.NoError(t, err)
	var tests int
	for _, tr := range feed.Transitions {
		if tr.Test {
			tests++
		}
	}
	require.Equal(t, 1, tests, "one marked transition, no real alarm")
}

func TestEdgeFiringAlertsReadsWhatTheHostJudged(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	now := time.Now()
	temp := 99.0
	edgeSeed(t, store.EdgeNode{ID: "n_hot", Name: "hot-pi", Kind: "board", Presence: string(edge.PresenceVerified), LastSeen: now.Unix(),
		Instances: []store.EdgeInstance{{Name: "sensor", Resources: &store.EdgeResources{CPUTempC: &temp, ReadAt: now.Unix()}}}})
	_, _, err := edgeReadHealth(now)
	require.NoError(t, err)

	edgeFiringMu.Lock()
	edgeFiringAt = time.Time{}
	edgeFiringMu.Unlock()
	t.Cleanup(func() { edgeFiringMu.Lock(); edgeFiringAt = time.Time{}; edgeFiringMu.Unlock() })
	got := edgeFiringAlerts()
	require.Equal(t, []string{"cpu_temp > 85"}, got["n_hot"])

	// Turned off, a rule stops firing on every surface.
	require.NoError(t, edgeAlertLog().SetOff("cpu_temp", true))
	require.Empty(t, edgeReadFiring()["n_hot"])
}
