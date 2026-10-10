package main

// join_admin_socket_bdd_test.go runs features/join/admin_socket.feature against a real
// `roger tower serve` (the standalone serve package, its admin socket included) and the real
// `roger tower` commands. "/srv/lab-tower" is a short temporary directory (a unix socket path has
// a length limit), and the user "rt" is whoever runs the test.
//
// Not run here yet: the outline rows tagged @blocked-on-request-approve (requests, approve,
// station revoke) and Windows.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/coreclient"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/localplane/serve"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
	"rogerai.fm/roger/v6/internal/towercli"
	"rogerai.fm/roger/v6/internal/towerpin"
)

const adminSocketFeature = "../../features/join/admin_socket.feature"

type adminSocketBDD struct {
	t      *testing.T
	dir    string
	p      *serve.Prepared
	done   chan error
	out    string
	err    error
	served int
}

func (s *adminSocketBDD) reset() {
	base, err := os.MkdirTemp("", "as")
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = os.RemoveAll(base) })
	s.dir = filepath.Join(base, "lab")
	if _, err := tower.Init(s.dir, tower.ModeStandalone); err != nil {
		s.t.Fatal(err)
	}
	s.p, s.done, s.out, s.err, s.served = nil, nil, "", nil, 0
}

// prepareServe runs the real serve.Prepare in this test process. Other tests in the binary
// construct Core clients on purpose, and serve's fail-closed guard counts them process-wide, so the
// note is cleared first: the guard itself is proven in internal/localplane/serve.
func prepareServe(args []string, w io.Writer) (*serve.Prepared, error) {
	coreclient.Reset()
	return serve.Prepare(args, w)
}

func (s *adminSocketBDD) startServe() error {
	p, err := prepareServe([]string{"--dir", s.dir, "--bind", "127.0.0.1:0"}, &bytes.Buffer{})
	if err != nil {
		return err
	}
	s.p, s.done = p, make(chan error, 1)
	go func() { s.done <- p.Serve() }()
	s.t.Cleanup(func() { _ = p.Close() })
	for i := 0; i < 50 && !toweradmin.Running(s.dir); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func (s *adminSocketBDD) rogerTower(cmdline string) error {
	args := strings.Fields(strings.TrimPrefix(cmdline, "roger tower "))
	args = append([]string{args[0], "--dir", s.dir}, args[1:]...)
	var b strings.Builder
	code := towercli.Run(args, &b, &b)
	s.out = b.String()
	if code != 0 {
		s.err = fmt.Errorf("roger tower %s exited %d:\n%s", strings.Join(args, " "), code, s.out)
	} else {
		s.err = nil
	}
	return nil
}

// stopServe stops the running serve and waits for it to let go of the directory.
func (s *adminSocketBDD) stopServe() error {
	if s.p == nil {
		return nil
	}
	_ = s.p.Close()
	err := <-s.done
	s.p = nil
	return err
}

// stillServing proves clients keep being served: the running Tower answers a TLS handshake under
// its own key while (and after) the command runs.
func (s *adminSocketBDD) stillServing() error {
	st, err := tower.Open(s.dir)
	if err != nil {
		return err
	}
	fp, _ := st.JoinFingerprint()
	got, err := towerpin.Fetch(s.p.Addr(), 2*time.Second)
	if err != nil || got != fp {
		return fmt.Errorf("the Tower stopped serving: %v", err)
	}
	return nil
}

func TestJoinAdminSocketBDD(t *testing.T) {
	s := &adminSocketBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				return ctx, nil
			})
			sc.Step(`^a standalone Tower directory /srv/lab-tower owned by the user "rt"$`, func() error { return nil })
			sc.Step(`^"roger tower serve --dir /srv/lab-tower" is running as "rt"$`, s.startServe)

			sc.Step(`^the directory holds "admin\.sock"$`, func() error {
				fi, err := os.Lstat(filepath.Join(s.dir, "admin.sock"))
				if err != nil {
					return err
				}
				if fi.Mode()&os.ModeSocket == 0 {
					return fmt.Errorf("admin.sock is not a socket")
				}
				return nil
			})
			sc.Step(`^the socket's mode is 0600 and its owner is "rt"$`, func() error {
				fi, err := os.Lstat(toweradmin.Path(s.dir))
				if err != nil {
					return err
				}
				if fi.Mode().Perm() != 0o600 {
					return fmt.Errorf("mode %v", fi.Mode().Perm())
				}
				if uid, ok := fileOwnerUID(fi); ok && uid != os.Getuid() {
					return fmt.Errorf("owned by %d, not the serving user", uid)
				}
				return nil
			})
			sc.Step(`^serve removes it on a clean stop$`, func() error {
				if err := s.stopServe(); err != nil {
					return err
				}
				if _, err := os.Lstat(toweradmin.Path(s.dir)); !os.IsNotExist(err) {
					return fmt.Errorf("admin.sock is still there after a clean stop")
				}
				return nil
			})

			sc.Step(`^"rt" runs "([^"]*)"$`, func(cmd string) error {
				if strings.Contains(cmd, "attach") {
					// the network's first client makes it ready to take Stations
					if err := s.rogerTower("roger tower invite --client u_owner"); err != nil || s.err != nil {
						return s.err
					}
					id := between(s.out, "invitation: ", "\n")
					code := between(s.out, "code: ", "\n")
					if err := s.rogerTower("roger tower admit --client u_owner --id " + id + " --code " + code); err != nil || s.err != nil {
						return s.err
					}
					cmd = strings.Replace(cmd, " K ", " "+strings.Repeat("ab", 8)+" ", 1)
				}
				if strings.Contains(cmd, "revoke-client") {
					// a client named laptop-x joined through a real bearer invite
					st, err := tower.Open(s.dir)
					if err != nil {
						return err
					}
					_, code, err := toweradmin.Client{Dir: s.dir}.CreateJoinInvitation(tower.JoinClient, "", nil, time.Minute)
					if err != nil {
						return err
					}
					pub, _, _ := ed25519.GenerateKey(rand.Reader)
					ph := hex.EncodeToString(pub)
					fp, _ := st.JoinFingerprint()
					if _, err := st.ConsumeJoin(tower.JoinRequest{Proof: joincode.Tower.Proof(code, "n", ph, fp), Nonce: "n",
						PubHex: ph, KeyHash: protocol.UserIDFromPubkey(ph), Name: "laptop-x"}); err != nil {
						return err
					}
				}
				if strings.Contains(cmd, "invite") && !strings.Contains(cmd, "--list") {
					cmd += " --at 127.0.0.1:8787"
				}
				return s.rogerTower(cmd)
			})
			sc.Step(`^it succeeds through the admin socket without stopping serve$`, func() error {
				if s.err != nil {
					return s.err
				}
				if !toweradmin.Running(s.dir) {
					return fmt.Errorf("serve stopped")
				}
				// The lock is still serve's: the command could only have gone through the socket
				// (or, for a read like stations, read without the lock).
				st, _ := tower.Open(s.dir)
				if rel, err := st.Lock(); err == nil {
					_ = rel()
					return fmt.Errorf("the directory lock was free: serve no longer holds it")
				}
				return nil
			})
			sc.Step(`^clients keep being served while it runs$`, s.stillServing)

			sc.Step(`^serve is not running$`, s.stopServe)
			sc.Step(`^it takes the directory lock and works as before$`, func() error {
				if s.err != nil {
					return s.err
				}
				if toweradmin.Running(s.dir) {
					return fmt.Errorf("an admin socket answered with no serve running")
				}
				if !strings.Contains(s.out, "roger join ") {
					return fmt.Errorf("invite printed:\n%s", s.out)
				}
				return nil
			})

			sc.Step(`^a different local user connects to admin\.sock$`, func() error { return nil })
			sc.Step(`^the connection is refused by the file permission$`, func() error {
				fi, err := os.Lstat(toweradmin.Path(s.dir))
				if err != nil {
					return err
				}
				di, err := os.Stat(s.dir)
				if err != nil {
					return err
				}
				// Neither another user's connect (needs write on the socket) nor its path walk
				// (needs search on the directory) is allowed.
				if fi.Mode().Perm()&0o077 != 0 || di.Mode().Perm()&0o077 != 0 {
					return fmt.Errorf("socket %v, directory %v: another user has access", fi.Mode().Perm(), di.Mode().Perm())
				}
				return nil
			})
			sc.Step(`^on Linux the Tower also checks the peer's user id and refuses a mismatch$`, func() error {
				// The peer check itself is covered with a real socket in internal/toweradmin
				// (TestAnotherUserIsRefused); here: the serving user is let in.
				_, err := toweradmin.Client{Dir: s.dir}.JoinFingerprint()
				return err
			})

			sc.Step(`^serve crashed and left admin\.sock behind$`, func() error {
				if err := s.stopServe(); err != nil {
					return err
				}
				ln, err := net.Listen("unix", toweradmin.Path(s.dir))
				if err != nil {
					return err
				}
				ln.(*net.UnixListener).SetUnlinkOnClose(false)
				return ln.Close()
			})
			sc.Step(`^serve starts again$`, s.startServe)
			sc.Step(`^it detects nothing is listening, replaces the socket and starts$`, func() error {
				if !toweradmin.Running(s.dir) {
					return fmt.Errorf("no admin socket after the restart")
				}
				return s.stillServing()
			})

			sc.Step(`^a second "roger tower serve" starts on the same directory$`, func() error {
				_, s.err = prepareServe([]string{"--dir", s.dir, "--bind", "127.0.0.1:0"}, &bytes.Buffer{})
				return nil
			})
			sc.Step(`^it is refused because the first holds the lock, exactly as today$`, func() error {
				if s.err == nil || !strings.Contains(s.err.Error(), "another Tower process already owns") {
					return fmt.Errorf("the second serve was not refused by the lock: %v", s.err)
				}
				return s.stillServing()
			})

			sc.Step(`^anything other than a known administration request arrives on the socket$`, func() error {
				conn, err := net.Dial("unix", toweradmin.Path(s.dir))
				if err != nil {
					return err
				}
				_, _ = conn.Write([]byte(`{"op":"drop-everything"}` + "\n"))
				buf := make([]byte, 512)
				n, _ := conn.Read(buf)
				_ = conn.Close()
				s.out = string(buf[:n])
				return nil
			})
			sc.Step(`^it is refused and logged, and the socket stays up$`, func() error {
				if !strings.Contains(s.out, "unknown administration request") {
					return fmt.Errorf("answered %q", s.out)
				}
				if !toweradmin.Running(s.dir) {
					return fmt.Errorf("the socket went down")
				}
				return nil
			})

			sc.Step(`^the machine has no network interfaces up except loopback$`, func() error { return nil })
			sc.Step(`^it succeeds$`, func() error { return s.err })
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Tags: "~@blocked-on-request-approve",
			Paths: []string{adminSocketFeature + ":15", adminSocketFeature + ":20", adminSocketFeature + ":44",
				adminSocketFeature + ":49", adminSocketFeature + ":54", adminSocketFeature + ":59",
				adminSocketFeature + ":63", adminSocketFeature + ":73"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/join/admin_socket.feature failed")
	}
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	if j := strings.Index(s, end); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}
