package agent

// rereg_loop_bdd_test.go makes the NODE scenarios of features/multinode/rereg_loop.feature
// executable against the real reregistrar, pollers and heartbeat (agent.Start where the
// scenario is about a running share). The broker and end-to-end scenarios run against real
// broker instances in cmd/rogerai-broker/rereg_loop_bdd_test.go; godog runs each half by
// scenario line, so every scenario in the approved file runs exactly once.
//
// The Background's two broker processes are, on this side, two HTTP endpoints over ONE
// in-test registry (a broker cannot be imported here: it is a main package). They record what
// the node sends and answer the way a broker that knows the node does. Whether real broker
// instances converge is the broker suite's job; these scenarios pin what the NODE sends.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/protocol"
)

// rereg_loop.feature scenario lines that run HERE (the node half).
var rrNodeLines = []int{136, 148, 153, 158, 164, 170}

func TestReregLoopNodeBDD(t *testing.T) {
	paths := make([]string, 0, len(rrNodeLines))
	for _, l := range rrNodeLines {
		paths = append(paths, "../../features/multinode/rereg_loop.feature:"+strconv.Itoa(l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: rrNodeInit(t),
		Options:             &godog.Options{Format: "pretty", Paths: paths, TestingT: t, Strict: true},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("multinode/rereg_loop node scenarios failed (see godog output above)")
	}
}

type rrRegistered struct {
	at  time.Time
	reg protocol.NodeRegistration
}

// rrBrokers is the Background: two endpoints (A, B) over one registry.
type rrBrokers struct {
	mu       sync.Mutex
	token    map[string]string // node -> live token
	regs     []rrRegistered
	nonces   []string
	refuse   bool // answer every node-facing call 401 until the next register
	poll401  int
	beat401  int
	gateRegs bool // hold a register until 4 poller 401s + 1 heartbeat 401 were seen
	A, B     *httptest.Server
}

func newRRBrokers() *rrBrokers {
	k := &rrBrokers{token: map[string]string{}}
	h := http.HandlerFunc(k.serve)
	k.A, k.B = httptest.NewServer(h), httptest.NewServer(h)
	return k
}

func (k *rrBrokers) close() { k.A.Close(); k.B.Close() }

func (k *rrBrokers) authed(r *http.Request, node string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return !k.refuse && k.token[node] != "" && r.Header.Get("Authorization") == "Bearer "+k.token[node]
}

func (k *rrBrokers) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/nodes/challenge":
		n := fmt.Sprintf("%064x", time.Now().UnixNano())
		k.mu.Lock()
		k.nonces = append(k.nonces, n)
		k.mu.Unlock()
		_ = json.NewEncoder(w).Encode(protocol.AttestChallenge{Nonce: n, Expires: time.Now().Add(time.Minute).Unix()})
	case "/nodes/register":
		var reg protocol.NodeRegistration
		_ = json.NewDecoder(r.Body).Decode(&reg)
		if k.gateRegs {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				k.mu.Lock()
				ready := k.poll401 >= 4 && k.beat401 >= 1
				k.mu.Unlock()
				if ready {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		k.mu.Lock()
		k.regs = append(k.regs, rrRegistered{at: time.Now(), reg: reg})
		k.token[reg.NodeID] = reg.BridgeToken
		k.refuse = false
		k.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	case "/nodes/heartbeat":
		var m struct {
			NodeID string `json:"node_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		if !k.authed(r, m.NodeID) {
			k.mu.Lock()
			k.beat401++
			k.mu.Unlock()
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	case "/agent/poll":
		if !k.authed(r, r.URL.Query().Get("node")) {
			k.mu.Lock()
			k.poll401++
			k.mu.Unlock()
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		select { // a short hold, then "re-poll"
		case <-time.After(20 * time.Millisecond):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	default: // /agent/cancels and the rest: nothing to do
		w.WriteHeader(http.StatusNoContent)
	}
}

func (k *rrBrokers) registers() []rrRegistered {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]rrRegistered(nil), k.regs...)
}

type rrNode struct {
	t        *testing.T
	k        *rrBrokers
	rr       *reregistrar
	sess     *Session
	stop     chan struct{}
	t0       string
	gen0     uint64
	before   int // registers seen before the When
	stale    uint64
	restore  []func()
	prevSess *Session
}

func (n *rrNode) reset(t *testing.T) {
	*n = rrNode{t: t, stop: make(chan struct{})}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // agent.Start's node key lives here, not in the real config
	t.Setenv("HOME", t.TempDir())
	prevMin := reregisterMinInterval
	reregisterMinInterval = 300 * time.Millisecond // the production default is longer; the rule is the same
	prevBeat := heartbeatInterval.Load()
	heartbeatInterval.Store(int64(20 * time.Millisecond))
	n.restore = append(n.restore, func() { reregisterMinInterval = prevMin; heartbeatInterval.Store(prevBeat) })
}

func (n *rrNode) cleanup() {
	close(n.stop)
	for _, s := range []*Session{n.sess, n.prevSess} {
		if s != nil {
			s.Stop()
		}
	}
	if n.k != nil {
		n.k.close()
	}
	for _, f := range n.restore {
		f()
	}
}

func (n *rrNode) brokers() error { n.k = newRRBrokers(); return nil }

func (n *rrNode) reregistrarWith(confidential bool) {
	_, priv, _ := ed25519.GenerateKey(nil)
	n.t0 = "tok0-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	reg := protocol.NodeRegistration{NodeID: "st-pico-n", BridgeToken: n.t0, Confidential: confidential,
		PubKey: fmt.Sprintf("%x", priv.Public().(ed25519.PublicKey))}
	reg.TS = time.Now().Unix()
	reg.SignRegistration(priv)
	n.k.mu.Lock()
	n.k.token[reg.NodeID] = n.t0
	n.k.mu.Unlock()
	n.rr = newReregistrar(n.k.B.URL, reg, priv) // the share started on A; its re-registers land on B
}

func (n *rrNode) runningShareT0() error { n.reregistrarWith(false); return nil }

func (n *rrNode) recoversN(times int) error {
	n.before = len(n.k.registers())
	_, n.gen0 = n.rr.curToken()
	for i := 0; i < times; i++ {
		_, gen := n.rr.curToken()
		n.rr.recover(gen, n.stop)
	}
	return nil
}

func (n *rrNode) everyReregCarriedT0() error {
	regs := n.k.registers()[n.before:]
	if len(regs) == 0 {
		return fmt.Errorf("no re-register was sent")
	}
	for i, r := range regs {
		if r.reg.BridgeToken != n.t0 {
			return fmt.Errorf("re-register #%d carried a rotated token", i+1)
		}
	}
	return nil
}

func (n *rrNode) liveStillT0() error {
	if tok, _ := n.rr.curToken(); tok != n.t0 {
		return fmt.Errorf("the live token rotated")
	}
	return nil
}

func (n *rrNode) genAdvanced(times int) error {
	if _, gen := n.rr.curToken(); gen != n.gen0+uint64(times) {
		return fmt.Errorf("generation advanced %d times, want %d", gen-n.gen0, times)
	}
	return nil
}

func (n *rrNode) start() (*Session, error) {
	return Start(Config{Broker: n.k.A.URL, NodeID: "st-pico-n", Model: "pico", Parallel: 4,
		Upstream: "http://127.0.0.1:1/v1/chat/completions"})
}

func (n *rrNode) stoppedWithT0() error {
	s, err := n.start()
	if err != nil {
		return err
	}
	n.prevSess = s
	regs := n.k.registers()
	if len(regs) != 1 {
		return fmt.Errorf("first process sent %d registers, want 1", len(regs))
	}
	n.t0 = regs[0].reg.BridgeToken
	s.Stop()
	return nil
}

func (n *rrNode) newProcessStarts() error {
	n.before = len(n.k.registers())
	s, err := n.start()
	n.sess = s
	return err
}

func (n *rrNode) freshToken() error {
	regs := n.k.registers()[n.before:]
	if len(regs) == 0 {
		return fmt.Errorf("the new process did not register")
	}
	if regs[0].reg.BridgeToken == n.t0 || regs[0].reg.BridgeToken == "" {
		return fmt.Errorf("the new process reused the stopped process's token")
	}
	return nil
}

func (n *rrNode) runningWithPollers() error {
	s, err := n.start()
	if err != nil {
		return err
	}
	n.sess = s
	deadline := time.Now().Add(3 * time.Second)
	for s.Link() != LinkOnAir {
		if time.Now().After(deadline) {
			return fmt.Errorf("the share never went on air")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

func (n *rrNode) allFiveSee401() error {
	n.before = len(n.k.registers())
	n.k.mu.Lock()
	n.k.poll401, n.k.beat401 = 0, 0
	n.k.gateRegs = true
	n.k.refuse = true
	n.k.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for len(n.k.registers()) == n.before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // room for any extra re-register to show up
	n.k.mu.Lock()
	defer n.k.mu.Unlock()
	if n.k.poll401 < 4 || n.k.beat401 < 1 {
		return fmt.Errorf("only %d poller and %d heartbeat refusals were seen, want 4 and 1", n.k.poll401, n.k.beat401)
	}
	return nil
}

func (n *rrNode) exactlyOneRereg() error {
	if got := len(n.k.registers()) - n.before; got != 1 {
		return fmt.Errorf("%d re-registers were sent, want exactly 1", got)
	}
	return nil
}

func (n *rrNode) pollerSentAtN() error {
	n.reregistrarWith(false)
	_, n.stale = n.rr.curToken()
	return nil
}

func (n *rrNode) reregisteredMeanwhile() error {
	n.rr.recover(n.stale, n.stop)
	n.before = len(n.k.registers())
	return nil
}

func (n *rrNode) staleRequest401() error {
	n.rr.recover(n.stale, n.stop) // the late 401 reports the generation the request was sent with
	return nil
}

func (n *rrNode) noRereg() error {
	if got := len(n.k.registers()) - n.before; got != 0 {
		return fmt.Errorf("%d re-registers were sent for a stale refusal, want 0", got)
	}
	return nil
}

func (n *rrNode) reregisteredRecently() error {
	n.reregistrarWith(false)
	_, gen := n.rr.curToken()
	n.rr.recover(gen, n.stop)
	n.before = len(n.k.registers())
	return nil
}

func (n *rrNode) pollerSees401NewGen() error {
	_, gen := n.rr.curToken()
	n.rr.recover(gen, n.stop)
	return nil
}

func (n *rrNode) paced() error {
	regs := n.k.registers()
	if len(regs) != n.before+1 || n.before < 1 {
		return fmt.Errorf("want exactly one paced re-register after the first, got %d total", len(regs))
	}
	if gap := regs[n.before].at.Sub(regs[n.before-1].at); gap < reregisterMinInterval {
		return fmt.Errorf("the next re-register came %s after the last one, want at least %s", gap, reregisterMinInterval)
	}
	return nil
}

func (n *rrNode) confidentialShareT0() error {
	prevDetect, prevQuote := teeDetect, teeQuote
	teeDetect = func() teeKind { return teeSEVSNP }
	teeQuote = func(rd [64]byte) ([]byte, error) { return append([]byte("rl-test-quote:"), rd[:]...), nil }
	n.restore = append(n.restore, func() { teeDetect, teeQuote = prevDetect, prevQuote })
	n.reregistrarWith(true)
	return nil
}

func (n *rrNode) recoversOnce() error { return n.recoversN(1) }

func (n *rrNode) freshAttestation() error {
	regs := n.k.registers()[n.before:]
	if len(regs) != 1 {
		return fmt.Errorf("%d re-registers, want 1", len(regs))
	}
	r := regs[0].reg
	n.k.mu.Lock()
	issued := append([]string(nil), n.k.nonces...)
	n.k.mu.Unlock()
	if len(issued) == 0 || r.AttestNonce != issued[len(issued)-1] {
		return fmt.Errorf("the re-register did not carry the nonce just issued (got %q)", r.AttestNonce)
	}
	if !r.Confidential || r.Attestation == "" || r.AttestKind == "" {
		return fmt.Errorf("the re-register dropped the confidential claim or its quote")
	}
	return nil
}

func (n *rrNode) carriedT0() error { return n.everyReregCarriedT0() }

func rrNodeInit(t *testing.T) func(sc *godog.ScenarioContext) {
	return func(sc *godog.ScenarioContext) {
		n := &rrNode{}
		sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
			n.reset(t)
			return ctx, nil
		})
		sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
			n.cleanup()
			return ctx, nil
		})
		sc.Step(`^two broker processes A and B sharing one registry and one store$`, n.brokers)

		sc.Step(`^a running share whose live token is T0$`, n.runningShareT0)
		sc.Step(`^the share recovers (\d+) times$`, n.recoversN)
		sc.Step(`^every re-register carried token T0$`, n.everyReregCarriedT0)
		sc.Step(`^the live token is still T0$`, n.liveStillT0)
		sc.Step(`^the generation advanced (\d+) times$`, n.genAdvanced)

		sc.Step(`^a share process stopped with token T0$`, n.stoppedWithT0)
		sc.Step(`^a new share process starts for the same node id$`, n.newProcessStarts)
		sc.Step(`^it registers a token different from T0$`, n.freshToken)

		sc.Step(`^a running share with 4 pollers and a heartbeat at generation N$`, n.runningWithPollers)
		sc.Step(`^all five see 401 for generation N$`, n.allFiveSee401)
		sc.Step(`^exactly one re-register is sent$`, n.exactlyOneRereg)

		sc.Step(`^a poller sent a request with generation N$`, n.pollerSentAtN)
		sc.Step(`^the share re-registered to generation N\+1 meanwhile$`, n.reregisteredMeanwhile)
		sc.Step(`^that request comes back 401$`, n.staleRequest401)
		sc.Step(`^no re-register is sent$`, n.noRereg)

		sc.Step(`^a share that re-registered successfully less than the minimum re-register interval ago$`, n.reregisteredRecently)
		sc.Step(`^a poller sees 401 for the new generation$`, n.pollerSees401NewGen)
		sc.Step(`^the next re-register waits until the interval has passed$`, n.paced)
		sc.Step(`^the live token is unchanged$`, n.liveStillT0)

		sc.Step(`^a confidential share whose live token is T0$`, n.confidentialShareT0)
		sc.Step(`^the share recovers once$`, n.recoversOnce)
		sc.Step(`^the re-register carried a fresh attestation nonce and quote$`, n.freshAttestation)
		sc.Step(`^it carried token T0$`, n.carriedT0)
	}
}
