package main

// Executable spec: features/packaging/one_binary.feature sections 1 and 2 (the command map and
// tower login) plus the joined-on-Linux-only scenario of section 5. `roger tower ...` is driven
// as a real process: this test binary re-execs itself as roger (ROGER_TEST_AS_ROGER, see
// TestMain), so argument handling, stdout, stderr and the exit code are the shipped ones.
//
// "Identical to roger-tower <command> from the previous release" (the command-map outline): the
// previous release's behavior is pinned by its own unit tests, which moved unchanged with the
// code into internal/towercli. This runner proves the new entry point adds nothing: each row
// runs once through `roger tower` (a process) and once through towercli.Run in-process on a twin
// copy of the same directory, and the two must agree on stdout, the error, the exit code and the
// files left behind (byte for byte, except freshly generated key material, which is compared by
// name, size and mode because init mints new keys every time).
//
// Addresses: a test cannot bind 192.168.1.10, so serve scenarios use a free port on a loopback
// address in its place; the assertions (what listens, what is served, what is refused) are the
// scenario's.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/towercli"
)

const standaloneTowerYAML = "apiVersion: tower.rogerai.fm/v1alpha1\nkind: Tower\nmode: standalone\n"

const joinedTowerYAML = "apiVersion: tower.rogerai.fm/v1alpha1\nkind: Tower\nmode: joined\njoined:\n  authority: https://broker.rogerai.fm\n  enrollmentTokenFile: /run/secrets/enrollment-token\n"

type towerCLI struct {
	t       *testing.T
	root    string // per-scenario temp root
	home    string // XDG_CONFIG_HOME and HOME for the roger process
	dir     string // D
	twin    string // a byte copy of D for the in-process comparison
	cfg     string // C
	mode    string
	stdout  string
	stderr  string
	code    int
	twinOut string
	twinErr string
	twinRC  int
	serve   *exec.Cmd
	addr    string
	broker  *httptest.Server
	env     []string
	helpTop string
	helpSub string
}

func (s *towerCLI) reset(t *testing.T) {
	s.stop()
	if s.broker != nil {
		s.broker.Close()
	}
	*s = towerCLI{t: t}
	// A plain temp root, removed best-effort: the in-process twin of a joined serve leaves a
	// renewal goroutine that may still touch its directory as the scenario ends.
	root, err := os.MkdirTemp("", "towercli-bdd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s.root = root
	s.home = filepath.Join(s.root, "home")
	s.dir = filepath.Join(s.root, "D")
	s.twin = filepath.Join(s.root, "twin")
	s.cfg = filepath.Join(s.root, "C.yaml")
	_ = os.MkdirAll(s.home, 0o700)
}

func (s *towerCLI) stop() {
	if s.serve != nil && s.serve.Process != nil {
		_ = s.serve.Process.Kill()
		_, _ = s.serve.Process.Wait()
	}
	s.serve = nil
}

// roger runs this test binary as roger with args, under the scenario's own config home.
func (s *towerCLI) roger(args ...string) (stdout, stderr string, code int) {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "ROGER_TEST_AS_ROGER=1", "XDG_CONFIG_HOME="+s.home, "HOME="+s.home, "ROGERAI_NO_UPDATE_CHECK=1")
	cmd.Env = append(cmd.Env, s.env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
		e.WriteString(err.Error())
	}
	return o.String(), e.String(), code
}

func (s *towerCLI) dirFor(mode string) error {
	s.mode = mode
	switch mode {
	case "none":
		return os.MkdirAll(s.dir, 0o700)
	case "standalone":
		if _, err := tower.Init(s.dir, tower.ModeStandalone); err != nil {
			return err
		}
		return os.WriteFile(s.cfg, []byte(standaloneTowerYAML), 0o600)
	case "joined":
		if _, err := tower.Init(s.dir, tower.ModeJoined); err != nil {
			return err
		}
		return os.WriteFile(s.cfg, []byte(joinedTowerYAML), 0o600)
	}
	return fmt.Errorf("unknown mode %q", mode)
}

func (s *towerCLI) emptyDir() error { return s.dirFor("none") }

// expand turns the scenario's placeholders into this scenario's paths and values.
func (s *towerCLI) expand(flags, dir string) []string {
	if strings.TrimSpace(flags) == "" {
		return nil
	}
	repl := map[string]string{
		"D": dir, "C": s.cfg, "K": strings.Repeat("ab", 32), "I": "client-1", "X": "000000",
		"S": "st-" + strings.Repeat("cd", 16), "m": "m", "B": "http://127.0.0.1:1", "F": filepath.Join(s.root, "body.json"),
		"N": "127.0.0.1:1",
	}
	var out []string
	for _, f := range strings.Fields(flags) {
		if v, ok := repl[f]; ok {
			out = append(out, v)
			continue
		}
		out = append(out, f)
	}
	return out
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}

func (s *towerCLI) runsCommand(command, flags string) error {
	_ = os.WriteFile(filepath.Join(s.root, "body.json"), []byte(`{"model":"m","messages":[]}`), 0o600)
	if err := copyTree(s.dir, s.twin); err != nil {
		return err
	}
	args := append(strings.Fields(command), s.expand(flags, s.dir)...)
	s.stdout, s.stderr, s.code = s.roger(append([]string{"tower"}, args...)...)
	targs := append(strings.Fields(command), s.expand(flags, s.twin)...)
	var o, e bytes.Buffer
	old := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(s.root, "twinhome"))
	defer os.Setenv("XDG_CONFIG_HOME", old)
	s.twinRC = towercli.Run(targs, &o, &e)
	s.twinOut, s.twinErr = o.String(), e.String()
	return nil
}

// minted values differ between any two runs by design (ids, invitation codes, network ids, a
// probe's ephemeral source port); they are masked before comparing.
var minted = regexp.MustCompile(`[0-9a-f]{16,}|[A-Z2-7]{26}|local-[0-9a-f]+|127\.0\.0\.1:\d+->`)

func (s *towerCLI) sameResult() error {
	norm := func(x, dir string) string { return minted.ReplaceAllString(strings.ReplaceAll(x, dir, "D"), "#") }
	if norm(s.stdout, s.dir) != norm(s.twinOut, s.twin) {
		return fmt.Errorf("stdout differs:\nroger tower: %q\nin-process:  %q", s.stdout, s.twinOut)
	}
	if norm(s.stderr, s.dir) != norm(s.twinErr, s.twin) {
		return fmt.Errorf("stderr differs:\nroger tower: %q\nin-process:  %q", s.stderr, s.twinErr)
	}
	return nil
}

func (s *towerCLI) sameExit() error {
	if s.code != s.twinRC {
		return fmt.Errorf("exit %d via roger tower, %d in-process", s.code, s.twinRC)
	}
	return nil
}

var keyMaterial = regexp.MustCompile(`(?i)(\.key|\.pem|\.crt|root|nonce|token|bootstrap)`)

type fileFacts struct {
	size int64
	mode fs.FileMode
	sum  string
}

func treeFacts(root string) (map[string]fileFacts, error) {
	out := map[string]fileFacts{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, _ := os.ReadFile(p)
		h := sha256.Sum256(minted.ReplaceAll(bytes.ReplaceAll(b, []byte(root), []byte("D")), []byte("#")))
		rel, _ := filepath.Rel(root, p)
		out[rel] = fileFacts{size: info.Size(), mode: info.Mode().Perm(), sum: hex.EncodeToString(h[:])}
		return nil
	})
	return out, err
}

func (s *towerCLI) sameFiles() error {
	a, err := treeFacts(s.dir)
	if err != nil {
		return err
	}
	b, err := treeFacts(s.twin)
	if err != nil {
		return err
	}
	if len(a) != len(b) {
		return fmt.Errorf("roger tower left %d files, in-process %d", len(a), len(b))
	}
	for name, fa := range a {
		fb, ok := b[name]
		if !ok {
			return fmt.Errorf("%s exists only after roger tower", name)
		}
		if fa.mode != fb.mode {
			return fmt.Errorf("%s mode %v vs %v", name, fa.mode, fb.mode)
		}
		if keyMaterial.MatchString(name) {
			continue // fresh key material differs by design; name and mode are compared
		}
		if fa.sum != fb.sum {
			return fmt.Errorf("%s differs between roger tower and in-process", name)
		}
	}
	return nil
}

func (s *towerCLI) runsLine(line string) error {
	if _, err := os.Stat(s.cfg); err != nil {
		_ = os.WriteFile(s.cfg, []byte(standaloneTowerYAML), 0o600)
	}
	args := s.expand(strings.TrimPrefix(line, "roger "), s.dir)
	s.stdout, s.stderr, s.code = s.roger(args...)
	return nil
}

func (s *towerCLI) exitsNonZero() error {
	if s.code == 0 {
		return fmt.Errorf("exited 0; stdout %q", s.stdout)
	}
	return nil
}

func (s *towerCLI) redactExplained() error {
	if !strings.Contains(s.stderr, "--redact=false is not supported: configuration is always printed secret-safe") {
		return fmt.Errorf("stderr %q", s.stderr)
	}
	return nil
}

func freeLoopback(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}

// startServe starts `roger tower serve` with args in the background and waits for its listener.
func (s *towerCLI) startServe(args ...string) error {
	cmd := exec.Command(os.Args[0], append([]string{"tower", "serve"}, args...)...)
	cmd.Env = append(os.Environ(), "ROGER_TEST_AS_ROGER=1", "XDG_CONFIG_HOME="+s.home, "HOME="+s.home, "ROGERAI_NO_UPDATE_CHECK=1")
	var e bytes.Buffer
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, &e
	if err := cmd.Start(); err != nil {
		return err
	}
	s.serve = cmd
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return fmt.Errorf("serve exited: %s", e.String())
			}
			s.stdout += l + "\n"
			if strings.Contains(l, "serving the standalone consumer plane") {
				go func() {
					for range lines {
					}
				}()
				return nil
			}
		case <-deadline:
			return fmt.Errorf("serve did not report listening; stdout %q stderr %q", s.stdout, e.String())
		}
	}
}

func (s *towerCLI) serveBind(bind string) error {
	s.addr = freeLoopback(s.t)
	return s.startServe("--dir", s.dir, "--bind", s.addr)
}

func (s *towerCLI) planeListens(string) error {
	c, err := net.DialTimeout("tcp", s.addr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("nothing listens at %s: %v", s.addr, err)
	}
	c.Close()
	return nil
}

func (s *towerCLI) servesRoutes() error {
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/discover"}, {http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/local/poll"}, {http.MethodPost, "/local/complete"},
	} {
		req, _ := http.NewRequest(r.method, "http://"+s.addr+r.path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// The plane answers an unsigned request with its uniform 401, the same on every route.
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(b), `"unauthorized"`) {
			return fmt.Errorf("%s %s: %d %s", r.method, r.path, resp.StatusCode, b)
		}
	}
	return nil
}

// listeners counts the TCP sockets the serve process is listening on (Linux /proc).
func listeners(pid int) (int, error) {
	inodes := map[string]bool{}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0, err
	}
	for _, fd := range fds {
		l, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
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
			if len(fl) > 9 && fl[3] == "0A" && inodes[fl[9]] { // 0A = LISTEN
				n++
			}
		}
	}
	return n, nil
}

func (s *towerCLI) noHubListener() error {
	n, err := listeners(s.serve.Process.Pid)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("the standalone serve listens on %d sockets, want only the consumer plane", n)
	}
	return nil
}

func (s *towerCLI) joinedServe() error {
	return s.runsCommand("serve", "--dir D --hub 127.0.0.1:0")
}

func (s *towerCLI) asBefore() error {
	if err := s.sameResult(); err != nil {
		return err
	}
	return s.sameExit()
}

// serveWith is every "roger tower serve --dir D <flags>" step: a scenario's own flags in the
// directory's own mode run the serve; a flag of the other mode is the refusal under test.
func (s *towerCLI) serveWith(flags string) error {
	switch {
	case flags == "--config C":
		return s.serveWithConfig()
	case s.mode == "standalone" && strings.HasPrefix(flags, "--bind 192.168.1.10"):
		return s.serveBind(strings.TrimPrefix(flags, "--bind "))
	case s.mode == "joined" && flags == "--hub :8443":
		return s.joinedServe()
	}
	return s.otherModeFlag(flags)
}

func (s *towerCLI) otherModeFlag(flag string) error {
	args := append([]string{"tower", "serve", "--dir", s.dir}, strings.Fields(flag)...)
	s.stdout, s.stderr, s.code = s.roger(args...)
	return nil
}

func (s *towerCLI) exits2BeforeListener() error {
	if s.code != 2 {
		return fmt.Errorf("exit %d, want 2; stderr %q", s.code, s.stderr)
	}
	if strings.Contains(s.stdout, "serving") || strings.Contains(s.stdout, "listening") {
		return fmt.Errorf("a listener opened: %q", s.stdout)
	}
	return nil
}

func (s *towerCLI) otherModeMessage(flag, other, mode string) error {
	want := fmt.Sprintf("%s is a %s flag; this directory is %s", flag, other, mode)
	if !strings.Contains(s.stderr, want) {
		return fmt.Errorf("stderr %q does not say %q", s.stderr, want)
	}
	return nil
}

func (s *towerCLI) serveNoBind() error {
	s.stdout, s.stderr, s.code = s.roger("tower", "serve", "--dir", s.dir, "--check")
	return nil
}

func (s *towerCLI) bindsDefault() error {
	if s.code != 0 || !strings.Contains(s.stdout, "would serve the standalone consumer plane on 127.0.0.1:8787") {
		return fmt.Errorf("exit %d stdout %q stderr %q", s.code, s.stdout, s.stderr)
	}
	return nil
}

func (s *towerCLI) wildcardAndHostnameRefused() error {
	for _, b := range []string{"0.0.0.0:8787", "8.8.8.8:8787"} {
		_, e, code := s.roger("tower", "serve", "--dir", s.dir, "--bind", b, "--check")
		if code == 0 || !strings.Contains(e, "--allow-public") {
			return fmt.Errorf("bind %s: exit %d stderr %q", b, code, e)
		}
	}
	_, e, code := s.roger("tower", "serve", "--dir", s.dir, "--bind", "tower.example:8787", "--check")
	if code == 0 || !strings.Contains(strings.ToLower(e), "hostname") {
		return fmt.Errorf("hostname bind: exit %d stderr %q", code, e)
	}
	return nil
}

func (s *towerCLI) configSetsBind(bind string) error {
	if s.mode == "" {
		if err := s.dirFor("standalone"); err != nil {
			return err
		}
	}
	s.addr = strings.Replace(freeLoopback(s.t), "127.0.0.1", "127.0.0.2", 1)
	body := standaloneTowerYAML + "standalone:\n  bind: " + s.addr + "\n"
	return os.WriteFile(s.cfg, []byte(body), 0o600)
}

func (s *towerCLI) serveWithConfig() error {
	s.stdout, s.stderr, s.code = s.roger("tower", "serve", "--dir", s.dir, "--config", s.cfg, "--check")
	return nil
}

func (s *towerCLI) bindsFromConfig(string) error {
	want := "would serve the standalone consumer plane on " + s.addr
	if s.code != 0 || !strings.Contains(s.stdout, want) {
		return fmt.Errorf("exit %d stdout %q stderr %q, want %q", s.code, s.stdout, s.stderr, want)
	}
	return nil
}

func (s *towerCLI) flagWinsOverFile() error {
	flagAddr := freeLoopback(s.t)
	o, e, code := s.roger("tower", "serve", "--dir", s.dir, "--config", s.cfg, "--bind", flagAddr, "--check")
	if code != 0 || !strings.Contains(o, "would serve the standalone consumer plane on "+flagAddr) {
		return fmt.Errorf("exit %d stdout %q stderr %q", code, o, e)
	}
	return nil
}

func (s *towerCLI) namesInit(want string) error {
	want = strings.ReplaceAll(want, " D ", " "+s.dir+" ")
	if !strings.Contains(s.stderr, want) {
		return fmt.Errorf("stderr %q does not name %q", s.stderr, want)
	}
	return nil
}

func (s *towerCLI) runsTopAndSub(top, sub string) error {
	o, _, _ := s.roger("help")
	s.helpTop = o
	o, _, _ = s.roger("tower", "help")
	s.helpSub = o
	return nil
}

func (s *towerCLI) eachOwnJob() error { return nil } // the two help lines below are the observable

func helpLine(help, word string) string {
	for _, l := range strings.Split(help, "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && f[0] == word {
			return l
		}
	}
	return ""
}

func (s *towerCLI) topHelp(top, want string) error {
	l := ""
	for _, line := range strings.Split(s.helpTop, "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[0] == "roger" && f[1] == top {
			l = line
			break
		}
	}
	if !strings.Contains(l, want) {
		return fmt.Errorf("roger help line for %q is %q, want %q", top, l, want)
	}
	return nil
}

func (s *towerCLI) subHelp(sub, want string) error {
	if l := helpLine(s.helpSub, sub); !strings.Contains(l, want) {
		return fmt.Errorf("roger tower help line for %q is %q, want %q", sub, l, want)
	}
	return nil
}

func (s *towerCLI) bareTower() error {
	s.stdout, s.stderr, s.code = s.roger("tower")
	return nil
}

func (s *towerCLI) printsTowerList() error {
	for _, c := range []string{"init", "serve", "status", "attach", "login"} {
		if helpLine(s.stdout, c) == "" {
			return fmt.Errorf("the tower command list lacks %q: %q", c, s.stdout)
		}
	}
	return nil
}

func (s *towerCLI) exits0() error {
	if s.code != 0 {
		return fmt.Errorf("exit %d stderr %q", s.code, s.stderr)
	}
	return nil
}

func (s *towerCLI) statusStandalone() error {
	if err := s.dirFor("standalone"); err != nil {
		return err
	}
	s.stdout, s.stderr, s.code = s.roger("tower", "status", "--dir", s.dir)
	return nil
}

func (s *towerCLI) printsStatus() error {
	if s.code != 0 || !strings.Contains(s.stdout, "standalone") {
		return fmt.Errorf("exit %d stdout %q stderr %q", s.code, s.stdout, s.stderr)
	}
	return nil
}

func (s *towerCLI) neverSeparateBinary() error {
	if strings.Contains(s.stdout+s.stderr, "separate") {
		return fmt.Errorf("output still says separate: %q", s.stdout+s.stderr)
	}
	return nil
}

func (s *towerCLI) shareTower() error {
	s.stdout, s.stderr, s.code = s.roger("share", "--tower")
	return nil
}

func (s *towerCLI) namesNewAttach() error {
	all := s.stdout + s.stderr
	if !strings.Contains(all, "roger tower attach") || strings.Contains(all, "roger-tower attach") {
		return fmt.Errorf("explanation %q", all)
	}
	return nil
}

// --- login --------------------------------------------------------------------------------

// fakeDeviceBroker answers the device flow: begin, then approved on the first poll.
func (s *towerCLI) fakeDeviceBroker() {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/device/start", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "ABCD-EFGH", "verification_uri": "https://example.invalid/device", "interval": 0, "expires_in": 60})
	})
	mux.HandleFunc("/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "approved", "account": "op"})
	})
	s.broker = httptest.NewServer(mux)
	s.env = append(s.env, "ROGER_BROKER="+s.broker.URL)
}

func (s *towerCLI) authFile() string { return filepath.Join(s.home, "rogerai", "auth.json") }

func (s *towerCLI) joinedNotSignedIn() error {
	if err := s.dirFor("joined"); err != nil {
		return err
	}
	s.fakeDeviceBroker()
	return nil
}

func (s *towerCLI) signedIn() error {
	if err := os.MkdirAll(filepath.Dir(s.authFile()), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(s.authFile(), []byte(`{"github_login":"op"}`), 0o600); err != nil {
		return err
	}
	if s.mode == "" {
		if err := s.dirFor("joined"); err != nil {
			return err
		}
	}
	// A broker that fails the test if the device flow runs.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.t.Errorf("an already signed-in machine ran the device flow: %s", r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	s.broker = httptest.NewServer(mux)
	s.env = append(s.env, "ROGER_BROKER="+s.broker.URL)
	return nil
}

func (s *towerCLI) withMarker() error {
	if err := s.signedIn(); err != nil {
		return err
	}
	o, e, code := s.roger("tower", "login", "--dir", s.dir)
	if code != 0 {
		return fmt.Errorf("setup login: %d %s %s", code, o, e)
	}
	return nil
}

func (s *towerCLI) towerLogin() error {
	s.stdout, s.stderr, s.code = s.roger("tower", "login", "--dir", s.dir)
	return nil
}

func (s *towerCLI) towerLogout() error {
	s.stdout, s.stderr, s.code = s.roger("tower", "logout", "--dir", s.dir)
	return nil
}

func (s *towerCLI) signedInLikeRoger() error {
	b, err := os.ReadFile(s.authFile())
	if err != nil || !strings.Contains(string(b), `"op"`) {
		return fmt.Errorf("roger's sign-in record %q (%v); stdout %q stderr %q", b, err, s.stdout, s.stderr)
	}
	return nil
}

func (s *towerCLI) markerPath() string { return filepath.Join(s.dir, "account.json") }

func (s *towerCLI) hasMarker() error {
	if _, err := os.Stat(s.markerPath()); err != nil {
		return fmt.Errorf("no tower-account marker in D: %v (stdout %q stderr %q)", err, s.stdout, s.stderr)
	}
	return nil
}

func (s *towerCLI) noSecondKey() error {
	var keys []string
	_ = filepath.WalkDir(s.home, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".key") {
			keys = append(keys, p)
		}
		return nil
	})
	if len(keys) > 1 {
		return fmt.Errorf("more than one key file: %v", keys)
	}
	return nil
}

func (s *towerCLI) noDeviceFlow() error {
	if strings.Contains(s.stdout, "enter code") || strings.Contains(s.stdout, "open:") {
		return fmt.Errorf("a device flow ran: %q", s.stdout)
	}
	return s.exits0()
}

func (s *towerCLI) markerGone() error {
	if _, err := os.Stat(s.markerPath()); !os.IsNotExist(err) {
		return fmt.Errorf("the marker is still there (%v)", err)
	}
	return nil
}

func (s *towerCLI) stillSignedIn() error {
	if _, err := os.Stat(s.authFile()); err != nil {
		return fmt.Errorf("logout removed roger's own sign-in: %v", err)
	}
	return nil
}

func (s *towerCLI) standaloneDir() error { return s.dirFor("standalone") }

func (s *towerCLI) noAccountByDesign() error {
	if !strings.Contains(s.stderr, "a standalone Tower has no RogerAI account by design") {
		return fmt.Errorf("stderr %q", s.stderr)
	}
	return nil
}

// --- platforms ----------------------------------------------------------------------------

func (s *towerCLI) onMacOrWindows() error {
	s.env = append(s.env, "ROGER_TEST_GOOS=darwin")
	return nil
}

func (s *towerCLI) initJoined() error {
	s.stdout, s.stderr, s.code = s.roger("tower", "init", "--dir", filepath.Join(s.root, "J"), "--mode", "joined")
	return nil
}

func (s *towerCLI) saysLinux() error {
	if !strings.Contains(s.stderr, "joined Towers run on Linux") {
		return fmt.Errorf("stderr %q", s.stderr)
	}
	return nil
}

func (s *towerCLI) standaloneEverywhere() error {
	d := filepath.Join(s.root, "S")
	if o, e, code := s.roger("tower", "init", "--dir", d, "--mode", "standalone"); code != 0 {
		return fmt.Errorf("init: %d %s %s", code, o, e)
	}
	s.dir = d
	s.addr = freeLoopback(s.t)
	return s.startServe("--dir", d, "--bind", s.addr)
}

func (s *towerCLI) servesLocal() error {
	if err := s.planeListens(""); err != nil {
		return err
	}
	return s.servesRoutes()
}

func TestTowerCLIBDD(t *testing.T) {
	s := &towerCLI{t: t}
	t.Cleanup(s.stop)
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.stop()
				return ctx, nil
			})
			sc.Step(`^a data directory initialized for "([^"]*)" mode$`, s.dirFor)
			sc.Step(`^a data directory initialized for (standalone|joined) mode$`, s.dirFor)
			sc.Step(`^the operator runs "roger tower ([^"]*)" with "([^"]*)"$`, s.runsCommand)
			sc.Step(`^the result is identical to "roger-tower [^"]*" with "[^"]*" from the previous release$`, s.sameResult)
			sc.Step(`^the exit code is the same$`, s.sameExit)
			sc.Step(`^the files written to the data directory are byte-identical$`, s.sameFiles)
			sc.Step(`^the operator runs "(roger tower config print --config C --redact=false)"$`, s.runsLine)
			sc.Step(`^it exits non-zero$`, s.exitsNonZero)
			sc.Step(`^it explains that an unredacted print is refused, exactly as before$`, s.redactExplained)
			sc.Step(`^the standalone consumer plane listens on (\S+)$`, s.planeListens)
			sc.Step(`^it serves /discover, /v1/chat/completions, /local/poll and /local/complete as before$`, s.servesRoutes)
			sc.Step(`^it opens no listener for the joined hub$`, s.noHubListener)
			sc.Step(`^the joined link and the sealed hub start exactly as "roger-tower serve" did$`, s.asBefore)
			sc.Step(`^the operator runs "roger tower serve --dir D (.+)"$`, s.serveWith)
			sc.Step(`^it exits 2 before opening any listener$`, s.exits2BeforeListener)
			sc.Step(`^the message says "(.+) is a (\S+) flag; this directory is (\S+)"$`, s.otherModeMessage)
			sc.Step(`^the operator runs "roger tower serve --dir D" with no bind$`, s.serveNoBind)
			sc.Step(`^it binds 127\.0\.0\.1:8787$`, s.bindsDefault)
			sc.Step(`^a wildcard or public bind is refused without --allow-public, as before$`, s.wildcardAndHostnameRefused)
			sc.Step(`^a hostname bind is refused, as before$`, func() error { return nil }) // checked with the wildcard step
			sc.Step(`^a standalone config file that sets the bind to (\S+)$`, s.configSetsBind)
			sc.Step(`^it binds (192\.168\.1\.10:8787)$`, s.bindsFromConfig)
			sc.Step(`^a --bind flag on the same command wins over the file$`, s.flagWinsOverFile)
			sc.Step(`^an empty directory D$`, s.emptyDir)
			sc.Step(`^the operator runs "(roger tower serve --dir D)"$`, s.runsLine)
			sc.Step(`^the message names "([^"]*)"$`, s.namesInit)
			sc.Step(`^the user runs "roger (\S+)" and "roger tower (\S+)"$`, s.runsTopAndSub)
			sc.Step(`^each does only its own job$`, s.eachOwnJob)
			sc.Step(`^"roger help" describes "([^"]*)" as "([^"]*)"$`, s.topHelp)
			sc.Step(`^"roger tower help" describes "([^"]*)" as "([^"]*)"$`, s.subHelp)
			sc.Step(`^the user runs "roger tower"$`, s.bareTower)
			sc.Step(`^it prints the tower command list$`, s.printsTowerList)
			sc.Step(`^it exits 0$`, s.exits0)
			sc.Step(`^the user runs "roger tower status --dir D" on a standalone directory$`, s.statusStandalone)
			sc.Step(`^it prints the directory's status$`, s.printsStatus)
			sc.Step(`^it never says that tower is a separate binary$`, s.neverSeparateBinary)
			sc.Step(`^the user runs "roger share --tower"$`, s.shareTower)
			sc.Step(`^the explanation now names "roger tower attach" instead of "roger-tower attach"$`, s.namesNewAttach)
			sc.Step(`^a joined data directory D and a machine not signed in to roger$`, s.joinedNotSignedIn)
			sc.Step(`^the operator runs "roger tower login --dir D" and completes the device flow$`, s.towerLogin)
			sc.Step(`^the machine is signed in to roger, the same as "roger login"$`, s.signedInLikeRoger)
			sc.Step(`^D carries the tower-account marker$`, s.hasMarker)
			sc.Step(`^no second key or credential file is created$`, s.noSecondKey)
			sc.Step(`^a machine signed in to roger$`, s.signedIn)
			sc.Step(`^the operator runs "roger tower login --dir D"$`, s.towerLogin)
			sc.Step(`^no device flow runs$`, s.noDeviceFlow)
			sc.Step(`^a machine signed in to roger and a directory D with the marker$`, s.withMarker)
			sc.Step(`^the operator runs "roger tower logout --dir D"$`, s.towerLogout)
			sc.Step(`^the marker is gone$`, s.markerGone)
			sc.Step(`^"roger status" still shows the machine signed in$`, s.stillSignedIn)
			sc.Step(`^a standalone data directory D$`, s.standaloneDir)
			sc.Step(`^it says a standalone Tower has no RogerAI account by design$`, s.noAccountByDesign)
			sc.Step(`^roger on macOS or Windows$`, s.onMacOrWindows)
			sc.Step(`^the operator runs "roger tower init --mode joined"$`, s.initJoined)
			sc.Step(`^it says "joined Towers run on Linux"$`, s.saysLinux)
			sc.Step(`^the operator runs "roger tower init --mode standalone" and "roger tower serve"$`, s.standaloneEverywhere)
			sc.Step(`^the standalone plane serves on the local network$`, s.servesLocal)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: towerCLIPaths(),
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("one_binary.feature sections 1-2 (roger tower) failed")
	}
}

func towerCLIPaths() []string {
	var out []string
	for _, l := range []int{31, 63, 68, 75, 80, 97, 104, 114, 120, 133, 138, 143, 156, 163, 169, 175, 329, 334} {
		out = append(out, "../../features/packaging/one_binary.feature:"+strconv.Itoa(l))
	}
	return out
}
