package wire

// With multicast loopback on (transport.go), a machine with K private interfaces hears its own query
// about K*K times - each copy it sends is delivered to every one of its sockets - and answered each
// copy on all K interfaces. The answer is multicast, so every browser hears one; the responder now
// answers an identical query once per short window (RFC 6762 section 6 suppresses the same way).

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type queuePlane struct {
	mu    sync.Mutex
	in    [][]byte
	sends int
}

func (q *queuePlane) Send([]byte) error { q.mu.Lock(); q.sends++; q.mu.Unlock(); return nil }
func (q *queuePlane) Recv(b []byte) (int, net.Addr, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.in) == 0 {
		return 0, nil, timeoutError{}
	}
	n := copy(b, q.in[0])
	q.in = q.in[1:]
	return n, &net.UDPAddr{IP: net.ParseIP("192.168.1.9"), Port: mdnsPort}, nil
}
func (q *queuePlane) SetReadDeadline(time.Time) error { return nil }
func (q *queuePlane) Close() error                    { return nil }
func (q *queuePlane) push(b []byte)                   { q.mu.Lock(); q.in = append(q.in, b); q.mu.Unlock() }
func (q *queuePlane) drained() bool                   { q.mu.Lock(); defer q.mu.Unlock(); return len(q.in) == 0 }
func (q *queuePlane) sent() int                       { q.mu.Lock(); defer q.mu.Unlock(); return q.sends }

func TestTheResponderAnswersOneQueryOncePerWindow(t *testing.T) {
	q, err := buildQuery(DefaultService)
	require.NoError(t, err)
	plane := &queuePlane{}
	clock := time.Unix(1000, 0)
	var cmu sync.Mutex
	r := NewResponder(plane, DefaultService, Advert{NodeID: "n_self", Port: 1}, []net.IP{net.ParseIP("192.168.1.9")})
	r.now = func() time.Time { cmu.Lock(); defer cmu.Unlock(); return clock }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Serve(ctx) }()

	for i := 0; i < 9; i++ { // three interfaces: nine copies of one query, arriving together
		plane.push(q)
	}
	require.Eventually(t, plane.drained, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return plane.sent() == 1 }, time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return plane.sent() > 1 }, 100*time.Millisecond, 10*time.Millisecond,
		"copies of one query inside the window are answered once")

	cmu.Lock()
	clock = clock.Add(answerOnceWindow)
	cmu.Unlock()
	plane.push(q)
	require.Eventually(t, func() bool { return plane.sent() == 2 }, time.Second, 5*time.Millisecond,
		"the same query after the window is answered again: a new browser is asking")
}
