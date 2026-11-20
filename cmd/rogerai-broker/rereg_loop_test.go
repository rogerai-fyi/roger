package main

// rereg_loop_test.go backs features/multinode/rereg_loop.feature with what the scenarios do not
// reach: a node that still ROTATES its token on every recovery (`roger share` up to v7.1.1,
// in the field long after the node fix ships), the register-ordering 409 and its clock-skew
// limit, and the real agent restarted against two real instances.

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/agent"
)

func TestLegacyRotatingNodeSettlesAgainstTwoInstances(t *testing.T) {
	cases := []struct {
		name  string
		order []string // where each heartbeat lands
		sync  int      // run a sync tick on both instances after every `sync` beats (0 = never)
	}{
		{"alternating, sync after every pair", []string{"B", "A", "B", "A", "B", "A", "B", "A", "B", "A"}, 2},
		{"alternating, no sync at all", []string{"B", "A", "B", "A", "B", "A", "B", "A", "B", "A"}, 0},
		{"stale peer first, three times", []string{"B", "B", "B", "A", "A", "B"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &xiState{}
			s.reset(t)
			defer s.cleanup()
			require.NoError(t, s.twoBrokers("ON"))
			require.NoError(t, s.nodeRegistersOn("st-pico", "A")) // the old process's token
			require.Equal(t, http.StatusOK, s.node.beat(t, s.inst["B"]))
			s.sweepAll()
			require.NoError(t, s.graceLapsed()) // long-running steady state before the restart

			s.node.legacyRotate = true
			s.node.token = "tok-new-process-" + s.nonce // the restart: a new process, a new token
			require.NoError(t, s.node.register(t, s.inst["A"]))
			s.node.mu.Lock()
			s.node.beats, s.node.reregs = nil, 0
			s.node.mu.Unlock()

			for i, inst := range c.order {
				s.node.beat(t, s.inst[inst])
				if c.sync > 0 && (i+1)%c.sync == 0 {
					s.sweepAll()
				}
				time.Sleep(5 * time.Millisecond)
			}
			s.node.mu.Lock()
			reregs, beats := s.node.reregs, append([]int(nil), s.node.beats...)
			s.node.mu.Unlock()
			require.Equalf(t, 0, reregs, "a v7.1.1 node was told to re-register %d times (beats %v)", reregs, beats)
		})
	}
}

// TestRegisterOrdering pins the 409 the commit promises, and its limit: an older register is
// refused only inside this instance's local grace (an out-of-order delivery). Outside it the
// likeliest cause is the node's clock stepping back, and refusing would keep it off air.
func TestRegisterOrdering(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name        string
		secondTS    int64
		graceLapsed bool
		want        int
		wantToken   string // "first" or "second"
	}{
		{"older inside the grace is refused", now - 10, false, http.StatusConflict, "first"},
		{"older outside the grace (clock stepped back) is accepted", now - 10, true, http.StatusOK, "second"},
		{"equal timestamp is accepted", now, false, http.StatusOK, "second"},
		{"newer is accepted", now + 1, false, http.StatusOK, "second"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &xiState{}
			s.reset(t)
			defer s.cleanup()
			require.NoError(t, s.singleBroker("ON"))
			n := s.mkNode("st-order", "free-m", 0, false)
			n.ts = now
			require.NoError(t, n.register(t, s.inst["A"]))
			if c.graceLapsed {
				require.NoError(t, s.graceLapsed())
			}
			n.token, n.ts = "tok-second-"+s.nonce, c.secondTS
			err := n.register(t, s.inst["A"])
			if c.want == http.StatusOK {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "= 409")
			}
			want := "tok-0-" + s.nonce
			if c.wantToken == "second" {
				want = n.token
			}
			b := s.inst["A"].b
			b.mu.Lock()
			got := b.tunnels[n.id].token
			b.mu.Unlock()
			require.Equal(t, want, got)
		})
	}
}

// TestRealAgentRestartAgainstTwoInstances runs the PRODUCTION node code (agent.Start: four
// pollers, the heartbeat, the cancel loop, the reregistrar) through a round-robin front end
// over two real broker instances, and restarts the share the way an operator does (stop, then
// start a new process with a new token). The new process must register once and stay there.
func TestRealAgentRestartAgainstTwoInstances(t *testing.T) {
	s := &xiState{}
	s.reset(t)
	defer s.cleanup()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // the node key both processes share
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, s.twoBrokers("ON"))

	var next, registers atomic.Int64
	targets := []*url.URL{mustURL(t, s.inst["A"].url()), mustURL(t, s.inst["B"].url())}
	lb := httptest.NewServer(&httputil.ReverseProxy{
		Director: func(r *http.Request) {
			if r.URL.Path == "/nodes/register" {
				registers.Add(1)
			}
			u := targets[next.Add(1)%2]
			r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		},
		FlushInterval: -1,
	})
	defer lb.Close()

	nodeID := "st-real-" + s.nonce
	cfg := agent.Config{Broker: lb.URL, NodeID: nodeID, Model: "free-m", Parallel: 4,
		Upstream: "http://127.0.0.1:1/v1/chat/completions"}
	onAir := func(sess *agent.Session) {
		require.Eventually(t, func() bool { return sess.Link() == agent.LinkOnAir }, 5*time.Second, 20*time.Millisecond)
	}

	old, err := agent.Start(cfg)
	require.NoError(t, err)
	onAir(old)
	time.Sleep(time.Second) // traffic reaches both instances, so both hold the old token
	old.Stop()
	time.Sleep(agentPollHold + 500*time.Millisecond) // the old process's long polls end
	s.node = &xiNode{id: nodeID}
	s.sweepAll()
	require.NoError(t, s.graceLapsed()) // steady state before the restart

	registers.Store(0)
	sess, err := agent.Start(cfg)
	require.NoError(t, err)
	defer sess.Stop()
	onAir(sess)
	time.Sleep(4 * time.Second) // two poll holds' worth of round-robined traffic
	require.Equal(t, int64(1), registers.Load(), "the restarted share re-registered after its first register")
	require.Equal(t, agent.LinkOnAir, sess.Link())
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

// TestConcurrentMismatchesShareOneRead: several requests presenting the node's new token reach
// the lagging instance at once (four pollers and the heartbeat do exactly this after a restart).
// The shared read is single-flight, so every one of them gets the answer, not just the first.
func TestConcurrentMismatchesShareOneRead(t *testing.T) {
	for _, n := range []int{2, 5, 16} {
		t.Run(strconv.Itoa(n)+" at once", func(t *testing.T) {
			s := &xiState{}
			s.reset(t)
			defer s.cleanup()
			require.NoError(t, s.twoBrokers("ON"))
			require.NoError(t, s.nodeRegistersOn("st-burst", "A"))
			require.Equal(t, http.StatusOK, s.node.beat(t, s.inst["B"])) // B holds T0
			s.node.token = "tok-new-" + s.nonce
			require.NoError(t, s.node.register(t, s.inst["A"])) // B is now behind
			c := &countingShared{sharedStore: s.inst["B"].b.shared}
			s.inst["B"].b.shared = c

			codes := make([]int, n)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range codes {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					req, _ := http.NewRequest(http.MethodPost, s.inst["B"].url()+"/nodes/heartbeat",
						strings.NewReader(`{"node_id":"`+s.node.id+`"}`))
					req.Header.Set("Authorization", "Bearer "+s.node.token)
					resp, err := s.node.hc.Do(req)
					if err != nil {
						return
					}
					resp.Body.Close()
					codes[i] = resp.StatusCode
				}(i)
			}
			close(start)
			wg.Wait()
			for i, code := range codes {
				require.Equalf(t, http.StatusOK, code, "request %d of %d (all %v)", i+1, n, codes)
			}
			require.LessOrEqual(t, c.reads.Load(), int64(2), "one shared read (two namespaces at most)")
		})
	}
}
