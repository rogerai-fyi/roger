package main

// Executable spec: features/packaging/one_binary.feature section 6 - the tokenizer becomes a
// broker mode. `rogerai-broker tokenizer` runs as a real process: this test binary re-execs
// itself as the broker (BROKER_TEST_AS_BROKER, see TestMain), so the mode dispatch, the
// environment it reads and the listeners it opens are the shipped ones.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/tokenizer"
)

func TestMain(m *testing.M) {
	if os.Getenv("BROKER_TEST_AS_BROKER") == "1" {
		main()
		os.Exit(0)
	}
	// One dial try per shared call: outage scenarios fail every call against a closed server,
	// and go-redis's default 5 tries x 100ms made each of those calls cost half a second.
	valkeyDialerRetries = 1
	os.Exit(m.Run())
}

type tokMode struct {
	t      *testing.T
	cmd    *exec.Cmd
	addr   string
	logs   bytes.Buffer
	dir    string
	result recountResult
	dur    time.Duration
}

type recountResult struct {
	tokens int
	exact  bool
	ok     bool
}

func (s *tokMode) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	s.cmd = nil
}

func freePortStr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	return p
}

// startMode runs `rogerai-broker tokenizer` with a free TOKENIZER_PORT and waits for /health.
// The broker's own configuration is set to unreachable stores, so a mode that opened them would
// show it in its log.
func (s *tokMode) startMode() error {
	port := freePortStr(s.t)
	s.dir = s.t.TempDir()
	cmd := exec.Command(os.Args[0], "tokenizer")
	cmd.Env = append(os.Environ(), "BROKER_TEST_AS_BROKER=1", "TOKENIZER_PORT="+port, "TOKENIZER_DIR="+s.dir,
		"DATABASE_URL=postgres://u:p@127.0.0.1:1/none", "ROGERAI_DATABASE_URL=postgres://u:p@127.0.0.1:1/none",
		"VALKEY_URL=redis://127.0.0.1:1", "REDIS_URL=redis://127.0.0.1:1", "PORT=")
	cmd.Stdout, cmd.Stderr = &s.logs, &s.logs
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	s.addr = "127.0.0.1:" + port
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + s.addr + "/health"); err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("rogerai-broker tokenizer never answered /health; log %q", s.logs.String())
}

func (s *tokMode) listensOnPort() error {
	if !strings.Contains(s.logs.String(), "listening on "+s.addr) {
		return fmt.Errorf("the mode does not report listening on %s: %q", s.addr, s.logs.String())
	}
	if tokenizer.ListenAddr("") != "127.0.0.1:9099" {
		return fmt.Errorf("the default address is %s, want 127.0.0.1:9099", tokenizer.ListenAddr(""))
	}
	return nil
}

func (s *tokMode) countAsBefore() error {
	resp, err := http.Post("http://"+s.addr+"/count", "application/json", strings.NewReader(`{"model":"gpt-4o","text":"hello world"}`))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		return err
	}
	want := tokenizer.New("").Count("gpt-4o", "hello world")
	if got["tokens"] != float64(want.Tokens) || got["exact"] != want.Exact || got["method"] != want.Method {
		return fmt.Errorf("/count answered %v, want %+v", got, want)
	}
	return nil
}

func (s *tokMode) healthOK() error {
	resp, err := http.Get("http://" + s.addr + "/health")
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(b)) != "ok" {
		return fmt.Errorf("/health %q", b)
	}
	return nil
}

func (s *tokMode) dirHonored() error {
	if !strings.Contains(s.logs.String(), "TOKENIZER_DIR="+s.dir) {
		return fmt.Errorf("the mode did not take TOKENIZER_DIR %s: %q", s.dir, s.logs.String())
	}
	return nil
}

func (s *tokMode) urlPointsAtMode() error { return s.startMode() }

func (s *tokMode) brokerRecounts() error {
	c := recountConfig{url: "http://" + s.addr, client: &http.Client{Timeout: 4 * time.Second}}
	s.result.tokens, s.result.exact, s.result.ok = c.sidecarCount("gpt-4o", "the quick brown fox")
	return nil
}

func (s *tokMode) identicalToSidecar() error {
	want := tokenizer.New("").Count("gpt-4o", "the quick brown fox")
	if !s.result.ok || s.result.tokens != want.Tokens || s.result.exact != want.Exact {
		return fmt.Errorf("recount through the mode %+v, want %+v", s.result, want)
	}
	// Fail-open as before: with the tokenizer gone the recount reports not-ok and the claim stands.
	s.stop()
	c := recountConfig{url: "http://" + s.addr, client: &http.Client{Timeout: 4 * time.Second}}
	if _, _, ok := c.sidecarCount("gpt-4o", "x"); ok {
		return fmt.Errorf("a stopped tokenizer still answered")
	}
	return nil
}

// noBrokerOpened: the mode process listens on exactly one socket (the tokenizer's) and never
// logs opening a store or the broker listener.
func (s *tokMode) noBrokerOpened() error {
	n, err := procListeners(s.cmd.Process.Pid)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("the tokenizer mode listens on %d sockets, want 1", n)
	}
	for _, w := range []string{"rogerai-broker 6", "postgres", "valkey", "L1 re-count"} {
		if strings.Contains(strings.ToLower(s.logs.String()), strings.ToLower(w)) {
			return fmt.Errorf("the tokenizer mode started broker machinery (%q in its log): %q", w, s.logs.String())
		}
	}
	return nil
}

func procListeners(pid int) (int, error) {
	inodes := map[string]bool{}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0, err
	}
	for _, fd := range fds {
		l, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), fd.Name()))
		if err == nil && strings.HasPrefix(l, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]")] = true
		}
	}
	n := 0
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n")[1:] {
			fl := strings.Fields(line)
			if len(fl) > 9 && fl[3] == "0A" && inodes[fl[9]] {
				n++
			}
		}
	}
	return n, nil
}

func (s *tokMode) twoProcesses() error { return s.startMode() }

// outageNeverFails: the broker reaches the tokenizer only through settleRecount's sidecar call,
// which fails open; with the tokenizer process gone, a settle bills the claim and returns
// within the client's bound.
func (s *tokMode) outageNeverFails() error {
	s.stop()
	b := &broker{recount: recountConfig{url: "http://" + s.addr, client: &http.Client{Timeout: 4 * time.Second}}}
	start := time.Now()
	billed := b.settleRecount("n1", "r1", "gpt-4o", "some completion text", 7)
	if billed != 7 {
		return fmt.Errorf("a tokenizer outage changed the bill to %d, want the claimed 7", billed)
	}
	if d := time.Since(start); d > 2*time.Second {
		return fmt.Errorf("a tokenizer outage delayed the settle by %v", d)
	}
	return nil
}

func TestTokenizerModeBDD(t *testing.T) {
	s := &tokMode{t: t}
	t.Cleanup(s.stop)
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.stop()
				s.logs.Reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.stop()
				return ctx, nil
			})
			sc.Step(`^the operator runs "rogerai-broker tokenizer"$`, s.startMode)
			sc.Step(`^it listens on 127\.0\.0\.1:\$TOKENIZER_PORT \(default 9099\)$`, s.listensOnPort)
			sc.Step(`^POST /count with \{"model","text"\} answers \{"tokens","exact","method"\} as before$`, s.countAsBefore)
			sc.Step(`^GET /health answers "ok"$`, s.healthOK)
			sc.Step(`^TOKENIZER_DIR is honored as before$`, s.dirHonored)
			sc.Step(`^TOKENIZER_URL points at "rogerai-broker tokenizer"$`, s.urlPointsAtMode)
			sc.Step(`^the broker recounts tokens$`, s.brokerRecounts)
			sc.Step(`^the counts and the fail-open behavior are identical to the sidecar's$`, s.identicalToSidecar)
			sc.Step(`^no database, shared store or HTTP broker listener is opened$`, s.noBrokerOpened)
			sc.Step(`^a broker and its tokenizer running as two processes from the same binary$`, s.twoProcesses)
			sc.Step(`^a tokenizer outage never fails or delays a relay$`, s.outageNeverFails)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:375",
				"../../features/packaging/one_binary.feature:382",
				"../../features/packaging/one_binary.feature:387",
				"../../features/packaging/one_binary.feature:391",
			},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("one_binary.feature section 6 (the tokenizer mode) failed")
	}
}

// TestBrokerModeDispatch: "tokenizer" runs the service with the environment's port and dir and
// is reported handled (an error is passed back for main to log); anything else is the broker.
func TestBrokerModeDispatch(t *testing.T) {
	old := serveTokenizer
	defer func() { serveTokenizer = old }()
	var gotPort, gotDir string
	serveTokenizer = func(port, dir string, _ chan<- string, _ <-chan struct{}) error {
		gotPort, gotDir = port, dir
		return fmt.Errorf("bind failed")
	}
	t.Setenv("TOKENIZER_PORT", "12345")
	t.Setenv("TOKENIZER_DIR", "/some/dir")
	handled, err := brokerMode([]string{"tokenizer"})
	if !handled || err == nil || gotPort != "12345" || gotDir != "/some/dir" {
		t.Fatalf("handled=%v err=%v port=%q dir=%q", handled, err, gotPort, gotDir)
	}
	for _, args := range [][]string{nil, {"-addr", "127.0.0.1:0"}} {
		if handled, _ := brokerMode(args); handled {
			t.Fatalf("%v was taken as a mode", args)
		}
	}
}
