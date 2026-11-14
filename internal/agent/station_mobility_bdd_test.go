package agent

// station_mobility_bdd_test.go runs the node's half of features/tower/station_mobility.feature:
// the three scenarios about what a node does with a re-attach nudge (lines 209, 215 and 272).
// Everything else in that file runs from cmd/rogerai-broker/station_mobility_bdd_test.go.
//
// The node is the real one: ServeTower, its tenancy, the nudge it hears on nudgeChan (what the
// cancel long-poll hands it, TestTheCancelPollHandsTheNudgeToTheRelayLoop) and the notice sink
// `roger share` prints. Core is movingCore, the stub that models the attach handler's three
// branches, behind a recording proxy so the attach body can be read. The Station's epoch is Core
// state the node is never told (TowerAttachment carries none); the broker runner owns it.
//
// THE CONSOLE NOTICE IS NOT BUILT (the file's BUILD STATUS says PARTIAL; web/ was out of scope).
// "The Station's owner is told where and why" therefore asserts the `roger share` notice only; the
// console half of that Then is reported as not built, not asserted.
//
// Two rationale lines of the approved file are not Gherkin and stop godog parsing it; see
// mobAgentFeature.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/bddrun"
)

// mobAgentFeature copies the approved file with its two non-Gherkin rationale lines ("Because ..."
// at line 151, "So that ..." at line 253) made into comments, line numbers intact.
func mobAgentFeature(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../features/tower/station_mobility.feature")
	require.NoError(t, err)
	lines := strings.Split(string(raw), "\n")
	fixed := 0
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "Because work through a Tower that is gone cannot complete") ||
			strings.HasPrefix(trimmed, "So that moves which raced live work can be counted against fence firings") {
			lines[i] = l[:len(l)-len(strings.TrimLeft(l, " "))] + "# " + trimmed
			fixed++
		}
	}
	require.LessOrEqual(t, fixed, 2)
	out := filepath.Join(t.TempDir(), "station_mobility.feature")
	require.NoError(t, os.WriteFile(out, []byte(strings.Join(lines, "\n")), 0o600))
	return out
}

// mobNode is one real node serving through ServeTower against a stub Core.
type mobNode struct {
	id       string
	core     *movingCore
	hubA     *httptest.Server
	hubB     *httptest.Server
	pollsA   func() int
	pollsB   func() int
	notices  *noticeSink
	nudgeMu  sync.Mutex
	nudgeAt  []any // the "nudge" field of every attach body Core received
	cancel   context.CancelFunc
	done     chan struct{}
	attaches func() int
}

// mobStartNode stands up Core advertising Tower A, starts the node, and waits until it is serving
// through A.
func mobStartNode(t *testing.T, prefix string) *mobNode {
	t.Helper()
	n := &mobNode{id: fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()), core: &movingCore{}, notices: &noticeSink{}}
	n.hubA, n.pollsA = countingHub(t, false)
	n.hubB, n.pollsB = countingHub(t, false)
	n.core.advertise(n.hubA.Listener.Addr().String(), "", "tw-a")
	target, err := url.Parse(n.core.start(t))
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tower/edge/attach" {
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			n.nudgeMu.Lock()
			n.nudgeAt = append(n.nudgeAt, m["nudge"])
			n.nudgeMu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	n.attaches = func() int { return len(n.core.attaches()) }

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	n.cancel, n.done = cancel, make(chan struct{})
	dir := t.TempDir()
	go func() {
		defer close(n.done)
		_ = ServeTower(ctx, Config{Broker: front.URL, NodeID: n.id, Model: "m", Modality: "chat",
			Parallel: 1, Upstream: "http://127.0.0.1:1"}, priv, dir, discard{}, n.notices.report)
	}()
	require.Eventually(t, func() bool { return n.pollsA() > 0 }, 5*time.Second, 10*time.Millisecond,
		"the node never served through Tower A")
	return n
}

func (n *mobNode) stop() {
	n.cancel()
	<-n.done
}

// moveCoreToB makes Core place this Station on Tower B at its next attach: the dormant write a
// move makes, as the stub models it.
func (n *mobNode) moveCoreToB() {
	n.core.advertise(n.hubB.Listener.Addr().String(), "", "tw-b")
	n.core.mu.Lock()
	for k := range n.core.placed {
		n.core.placed[k] = "tw-b"
	}
	n.core.mu.Unlock()
}

type mobAgentState struct {
	t          *testing.T
	node       *mobNode
	extra      *mobNode
	gate       *moveGate
	busyAttach int
	busyPollsB int
	restore    func()
}

func (s *mobAgentState) background(int) error {
	s.node = mobStartNode(s.t, "n-mob")
	if got := s.node.attaches(); got != 1 {
		return fmt.Errorf("%d attaches, want 1", got)
	}
	return nil
}

func (s *mobAgentState) declaredNudge() error {
	s.node.nudgeMu.Lock()
	defer s.node.nudgeMu.Unlock()
	if len(s.node.nudgeAt) != 1 || s.node.nudgeAt[0] != true {
		return fmt.Errorf("the attach bodies declared nudge = %v", s.node.nudgeAt)
	}
	return nil
}

func (s *mobAgentState) nudgeWhileServing() error {
	// Work in flight on this node's move gate - exactly what gatedExec.Serve brackets around
	// every job the node serves, and nothing Core can see.
	s.gate = gateFor(s.node.id)
	s.gate.begin()
	s.node.moveCoreToB()
	deliverNudge(s.node.id, "better_tower")
	return nil
}

func (s *mobAgentState) handlesNudge() error {
	time.Sleep(400 * time.Millisecond)
	s.busyAttach, s.busyPollsB = s.node.attaches(), s.node.pollsB()
	return nil
}

func (s *mobAgentState) finishesThenReattaches() error {
	if s.busyAttach != 1 || s.busyPollsB != 0 {
		return fmt.Errorf("the node acted while busy: %d attaches, %d polls on B", s.busyAttach, s.busyPollsB)
	}
	s.gate.end()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.node.attaches() == 2 && s.node.pollsB() > 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("once idle the node did not re-attach (%d attaches, %d polls on B)", s.node.attaches(), s.node.pollsB())
}

func (s *mobAgentState) ignoresPastExpiry() error {
	old := nudgeExpiry
	nudgeExpiry = 300 * time.Millisecond
	s.restore = func() { nudgeExpiry = old }
	s.extra = mobStartNode(s.t, "n-mob-expiry")
	g := gateFor(s.extra.id)
	g.begin()
	s.extra.moveCoreToB()
	deliverNudge(s.extra.id, "better_tower")
	time.Sleep(nudgeExpiry + 300*time.Millisecond) // busy for longer than the nudge lives
	g.end()
	time.Sleep(400 * time.Millisecond)
	if n := s.extra.attaches(); n != 1 || s.extra.pollsB() != 0 {
		return fmt.Errorf("a node busy past the nudge's expiry acted on it (%d attaches)", n)
	}
	return nil
}

func (s *mobAgentState) severalNudges() error {
	s.node.moveCoreToB()
	deliverNudge(s.node.id, "better_tower")
	deadline := time.Now().Add(3 * time.Second)
	for s.node.attaches() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.node.attaches() != 2 {
		return fmt.Errorf("the first nudge was not acted on")
	}
	for i := 0; i < 2; i++ {
		time.Sleep(100 * time.Millisecond)
		deliverNudge(s.node.id, "better_tower")
	}
	return nil
}

func (s *mobAgentState) handlesThem() error {
	deadline := time.Now().Add(2 * time.Second)
	for len(nudgeChan(s.node.id)) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(nudgeChan(s.node.id)) != 0 {
		return fmt.Errorf("the later nudges were never taken off the slot")
	}
	time.Sleep(400 * time.Millisecond)
	return nil
}

func (s *mobAgentState) reattachesOnce() error {
	if n := s.node.attaches(); n != 2 {
		return fmt.Errorf("%d attaches, want the first and one re-attach", n)
	}
	return nil
}

func (s *mobAgentState) isMoved() error {
	s.node.moveCoreToB()
	deliverNudge(s.node.id, "better_tower")
	deadline := time.Now().Add(3 * time.Second)
	for s.node.pollsB() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.node.pollsB() == 0 {
		return fmt.Errorf("the node never reached Tower B")
	}
	return nil
}

const mobMovedNotice = "tower: this Station moved from relay tw-a to relay tw-b (a better relay was found for it)"

func (s *mobAgentState) ownerTold() error {
	// The `roger share` half. The console half is not built (BUILD STATUS: PARTIAL).
	s.t.Log("station_mobility.feature:274: console notice NOT BUILT; asserting the `roger share` notice only")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.node.notices.sawContaining(mobMovedNotice) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("`roger share` never said %q; it said %v", mobMovedNotice, s.node.notices.msgs)
}

func (s *mobAgentState) idsNotOperators() error {
	s.node.notices.mu.Lock()
	var moved []string
	for _, m := range s.node.notices.msgs {
		if strings.Contains(m, "moved from relay") {
			moved = append(moved, m)
		}
	}
	s.node.notices.mu.Unlock()
	if len(moved) != 1 || moved[0] != mobMovedNotice {
		return fmt.Errorf("the move notices are %q, want exactly the Tower ids sentence", moved)
	}
	// Nothing the node is told about a Tower could carry its operator's account name.
	ty := reflect.TypeOf(TowerAttachment{})
	for i := 0; i < ty.NumField(); i++ {
		tag := strings.ToLower(ty.Field(i).Tag.Get("json") + ty.Field(i).Name)
		for _, w := range []string{"operator", "owner", "login", "account"} {
			if strings.Contains(tag, w) {
				return fmt.Errorf("the attach answer has a field %s", ty.Field(i).Name)
			}
		}
	}
	return nil
}

func TestStationMobilityNodeBDD(t *testing.T) {
	fastReattach(t, time.Hour, time.Hour, 5*time.Second, 5*time.Second) // a backoff would show
	feature := mobAgentFeature(t)
	s := &mobAgentState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*s = mobAgentState{t: t}
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				for _, n := range []*mobNode{s.node, s.extra} {
					if n != nil {
						n.stop()
					}
				}
				if s.restore != nil {
					s.restore()
				}
				return ctx, nil
			})
			sc.Step(`^a Station "S" live on Tower "A" at epoch (\d+)$`, s.background)
			sc.Step(`^S's node declared at attach that it understands the re-attach nudge$`, s.declaredNudge)
			sc.Step(`^a node receives a nudge while serving work Core could not see$`, s.nudgeWhileServing)
			sc.Step(`^it handles the nudge$`, s.handlesNudge)
			sc.Step(`^it finishes the work first and re-attaches when idle$`, s.finishesThenReattaches)
			sc.Step(`^if that takes longer than the nudge's expiry it ignores the nudge$`, s.ignoresPastExpiry)
			sc.Step(`^a node receives several nudges within one minute$`, s.severalNudges)
			sc.Step(`^it handles them$`, s.handlesThem)
			sc.Step(`^it re-attaches once$`, s.reattachesOnce)
			sc.Step(`^S is moved$`, s.isMoved)
			sc.Step("^S's owner sees, in the console and in `roger share` output, that S moved from Tower A to Tower B and the trigger in plain words$", s.ownerTold)
			sc.Step(`^the Tower ids are shown, never the Tower operators' account names$`, s.idsNotOperators)
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: []string{
			feature + ":209", feature + ":215", feature + ":272"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the node's half of station_mobility.feature is not satisfied (see godog output above)")
	}
}
