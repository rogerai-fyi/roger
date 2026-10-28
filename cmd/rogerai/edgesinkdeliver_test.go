package main

// WHO DELIVERS, AND WHEN (features/edge/health.feature §5). Three rules decide where an alert is
// actually sent from, and each of them is a way to get it wrong:
//
//   - only the HOST delivers. Judge is also called by an ordinary `roger edge health`, and a READ
//     must never message anybody.
//   - only CHANGES are delivered. Judge returns what fired or cleared, so nobody is messaged every
//     thirty seconds about the same hot Pi.
//   - an Edge with no sink configured does nothing at all, which is the private default.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
)

// deliverySpy replaces the one seam between the host's pass and the network.
func deliverySpy(t *testing.T) *[]edge.Alert {
	t.Helper()
	var mu sync.Mutex
	sent := []edge.Alert{}
	prev := edgeDeliverOne
	edgeDeliverOne = func(ctx context.Context, l edge.AlertLog, a edge.Alert, now time.Time) []edge.Delivery {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, a)
		return []edge.Delivery{{Sink: "sk_spy", OK: true, Says: "delivered"}}
	}
	t.Cleanup(func() { edgeDeliverOne = prev })
	return &sent
}

// settle waits for the delivery goroutine, which is on its own so an unreachable sink cannot hold
// up the pass.
func settle(t *testing.T, sent *[]edge.Alert, want int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if len(*sent) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNothingIsSentWhenNoSinkIsConfigured(t *testing.T) {
	useTempConfig(t)
	sent := deliverySpy(t)
	edgeDeliverAlerts([]edge.Alert{{Node: "pi", Metric: "cpu_temp", Firing: true}}, time.Now())
	settle(t, sent, 1)
	require.Empty(t, *sent, "an alert was sent off a machine with no sink configured")
}

func TestWhatChangedIsDeliveredOnce(t *testing.T) {
	useTempConfig(t)
	l := edgeAlertLog()
	_, err := l.AddSink(edge.SinkWebhook, "http://192.168.1.50/hook", time.Now())
	require.NoError(t, err)

	sent := deliverySpy(t)
	edgeDeliverAlerts([]edge.Alert{
		{Node: "pi", Metric: "cpu_temp", Value: 82, Threshold: 80, Firing: true},
		{Node: "nas", Metric: "disk", Firing: false},
	}, time.Now())
	settle(t, sent, 2)
	require.Len(t, *sent, 2, "the transitions were not all delivered")

	// Nothing changed: nothing is sent. This is what stops a hot Pi messaging somebody every pass.
	before := len(*sent)
	edgeDeliverAlerts(nil, time.Now())
	settle(t, sent, before+1)
	require.Len(t, *sent, before, "an empty change set still sent something")
}

// A read must never message anybody: only the host's pass delivers, and `roger edge health` is a
// read. This is the rule that decides WHERE the call lives, so it is pinned by walking the real
// command rather than by reading the code.
func TestAHealthReadDeliversNothing(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t, edgeNodeFor("n_pi", "quiet-pi", "board"))
	l := edgeAlertLog()
	_, err := l.AddSink(edge.SinkWebhook, "http://192.168.1.50/hook", time.Now())
	require.NoError(t, err)

	sent := deliverySpy(t)
	out, code := edgeRun(t, "edge", "health")
	require.Equal(t, 0, code, out)
	settle(t, sent, 1)
	require.Empty(t, *sent, "reading the fleet's health messaged somebody")
}
