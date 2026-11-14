package agent

// The node's half of a move (features/tower/station_mobility.feature): it declares the
// re-attach nudge at attach, hears it on the cancel long-poll, finishes what it is serving,
// and attaches to Core again at most once a minute.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAttachDeclaresTheReattachNudge(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"station_id": got["station_id"], "tower_id": "tw-1",
			"endpoint": "203.0.113.9:8443", "hub_token": "t", "state": "active", "tower_key_hash": "00"})
	}))
	defer srv.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, _, err = AttachTower(Config{Broker: srv.URL, NodeID: "n1", Model: "m", Modality: "chat"}, priv, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, true, got["nudge"])
}

func TestTheCancelPollHandsTheNudgeToTheRelayLoop(t *testing.T) {
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent := false
		once.Do(func() {
			sent = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ids": []string{}, "reattach": "better_tower"})
		})
		if !sent {
			time.Sleep(20 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	r := &reregistrar{token: "tok"}
	r.cond = sync.NewCond(&r.mu)
	sess := &Session{stop: make(chan struct{}), rereg: r}
	go cancelLoop(Config{Broker: srv.URL, NodeID: "n-move"}, sess)
	defer close(sess.stop)
	select {
	case reason := <-nudgeChan("n-move"):
		require.Equal(t, "better_tower", reason)
	case <-time.After(2 * time.Second):
		t.Fatal("the nudge never reached the relay loop")
	}
}

func TestTheNodeFinishesItsWorkBeforeActingOnANudge(t *testing.T) {
	g := &moveGate{}
	g.begin()
	ctx := context.Background()
	start := time.Now()
	go func() { time.Sleep(50 * time.Millisecond); g.end() }()
	require.True(t, g.waitIdle(ctx, time.Second), "idle once the job ends")
	require.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond)

	g.begin()
	require.False(t, g.waitIdle(ctx, 30*time.Millisecond), "still busy at expiry: the nudge is ignored")
	g.end()
}

func TestANodeActsOnAtMostOneNudgePerMinute(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	g := &moveGate{now: func() time.Time { return now }}
	require.True(t, g.allow())
	require.False(t, g.allow())
	now = now.Add(59 * time.Second)
	require.False(t, g.allow())
	now = now.Add(2 * time.Second)
	require.True(t, g.allow())
}

func TestTheOperatorIsToldWhereTheStationMovedAndWhy(t *testing.T) {
	require.Equal(t, "tower: this Station moved from relay tw-a to relay tw-b (a better relay was found for it)",
		movedNotice("tw-a", "tw-b", "better_tower"))
	require.Equal(t, "tower: this Station moved from relay tw-a to relay tw-b (its relay was failing)",
		movedNotice("tw-a", "tw-b", "tower_failing"))
	require.Equal(t, "tower: this Station moved from relay tw-a to relay tw-b (its relay was full for its trust stage)",
		movedNotice("tw-a", "tw-b", "stage_cap"))
	require.Equal(t, "tower: this Station moved from relay tw-a to relay tw-b (its relay was gone)",
		movedNotice("tw-a", "tw-b", "tower_gone"))
	require.Equal(t, "tower: this Station attached again and stayed on relay tw-a (Roger Core asked it to attach again)",
		movedNotice("tw-a", "tw-a", "something new"))
	var err error = movedError{reason: "better_tower"}
	require.ErrorIs(t, err, errMoved)
	require.Contains(t, err.Error(), "better_tower")
}

// THE WHOLE NODE LOOP: a nudge ends the tenancy, the node attaches again as itself at once (no
// backoff), Core places it on the new relay, it serves there, and the operator is told.
func TestANudgedNodeMovesToTheRelayCoreChoseAndSaysSo(t *testing.T) {
	fastReattach(t, time.Hour, time.Hour, 5*time.Second, 5*time.Second) // a backoff would show
	hub1, polls1 := countingHub(t, false)
	hub2, polls2 := countingHub(t, false)
	core := &movingCore{}
	core.advertise(hub1.Listener.Addr().String(), "", "tw-1")
	brokerURL := core.start(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	notices := &noticeSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeTower(ctx, Config{Broker: brokerURL, NodeID: "n-e2e-move", Model: "m", Modality: "chat",
			Parallel: 1, Upstream: "http://127.0.0.1:1"}, priv, t.TempDir(), discard{}, notices.report)
	}()
	require.Eventually(t, func() bool { return polls1() > 0 }, 5*time.Second, 10*time.Millisecond)

	// Core moves the Station: dormant, so its next attach is placed afresh, on tw-2.
	core.advertise(hub2.Listener.Addr().String(), "", "tw-2")
	core.mu.Lock()
	for k := range core.placed {
		core.placed[k] = "tw-2"
	}
	core.mu.Unlock()
	deliverNudge("n-e2e-move", "better_tower")

	require.Eventually(t, func() bool { return polls2() > 0 }, 3*time.Second, 10*time.Millisecond,
		"the node never reached the relay Core moved it to (or waited out a backoff)")
	require.Eventually(t, func() bool {
		return notices.sawContaining("moved from relay tw-1 to relay tw-2 (a better relay was found for it)")
	}, time.Second, 10*time.Millisecond)
	seen := core.attaches()
	require.Len(t, seen, 2)
	require.Equal(t, seen[0].StationID, seen[1].StationID, "it went back as itself")
	cancel()
	<-done
}
