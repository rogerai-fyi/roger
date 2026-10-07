package main

// Executable spec: features/edge/enrollment_root_trust.feature - every scenario that is not
// @tui (internal/tui), @console (internal/webui) or @ios (the phone; docs/ios-enrollment-root-
// trust.md).
//
// REAL dependencies, no mocks of our own code:
//   - REAL local authorities on disk (edgeauth.Designate), served over REAL HTTP by the production
//     enrollhttp handler - the very handler the Edge host mounts on its LAN listener. "shed" is a
//     whole machine: its own config directory, so `roger edge authority allow` / `adopt` / `readmit`
//     run on it through the REAL dispatch.
//   - an IMPOSTOR is another real authority with a root of its own; a device IN BETWEEN is a real
//     HTTP relay. They doctor real answers, they never fake our code.
//   - the joiner is the REAL `roger edge enroll` dispatch on its own config directory; an
//     interactive join runs live, its stdin a pipe the scenario types into.
//   - the phone's rule runs through edgeauth.VerifyAnswer, the one rule every joiner applies, over a
//     REAL claim to a REAL authority.
//   - Core is the REAL enrollhttp handler over REAL TLS (httptest), trusted through the client seam.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
	"rogerai.fm/roger/v6/internal/webui"
)

// rtMachine is one machine: its own config directory and its own user key.
type rtMachine struct {
	dir string
	key ed25519.PrivateKey
}

// rtPhone is the phone, as far as the Go side can be it: a node key, the answers it holds, and
// what it kept. Its rule is edgeauth.VerifyAnswer.
type rtPhone struct {
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
	id    string
	held  []rtHeld
	kept  *edgeauth.Identity
	root  *x509.Certificate // the root it already holds, if any
	where map[string]string // which authority each held answer came from
	err   error
}

type rtHeld struct {
	resp  edgeauth.Response
	nonce string
	from  string
}

// liveRun is an interactive enroll in flight: its stdin a pipe the scenario types into, its
// output read as it is written.
type liveRun struct {
	in    *io.PipeWriter
	out   *io.PipeReader
	lines chan string
	mu    sync.Mutex
	buf   bytes.Buffer
	done  chan struct{}
	err   error
	orig  *os.File
}

func (l *liveRun) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *liveRun) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type rtBDD struct {
	t       *testing.T
	me      *rtMachine
	shedM   *rtMachine
	cur     *rtMachine
	auths   map[string]*rootedAuthority
	target  string // the address the next join is pointed at
	out     string
	err     error
	exit    int
	live    *liveRun
	codes   map[string]string
	pin     string
	code    string
	phone   *rtPhone
	logs    *bytes.Buffer
	relay   *httptest.Server
	relayed []string
	// before: a snapshot of the joiner's Edge directory, for "exactly as it was".
	before map[string]string
	// what a direct exchange captured
	req     edgeauth.Request
	resp    edgeauth.Response
	errs    []string
	results []edgeauth.Response
	coreSrv *httptest.Server
	extra   []string
	pairing struct{ joiner, authority, proof string }
}

// ---- machines --------------------------------------------------------------

func (s *rtBDD) newMachine() *rtMachine {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	return &rtMachine{dir: s.t.TempDir(), key: key}
}

func (s *rtBDD) use(m *rtMachine) {
	_ = os.Setenv("HOME", m.dir)
	_ = os.Setenv("XDG_CONFIG_HOME", m.dir)
	_ = os.Setenv("AppData", m.dir)
	edgeUserKey = func() ed25519.PrivateKey { return m.key }
	s.cur = m
	if got := configPath(); !strings.HasPrefix(got, m.dir) {
		s.t.Fatalf("config isolation FAILED: %q is not under %q", got, m.dir)
	}
}

// on runs fn with another machine current, then comes back.
func (s *rtBDD) on(m *rtMachine, fn func() error) error {
	was := s.cur
	s.use(m)
	defer s.use(was)
	return fn()
}

func (s *rtBDD) edgeDirOf(m *rtMachine) string {
	var d string
	_ = s.on(m, func() error { d = edgeAuthDir(); return nil })
	return d
}

func (s *rtBDD) keyHex(m *rtMachine) string {
	return hex.EncodeToString(m.key.Public().(ed25519.PublicKey))
}

// run is one non-interactive command on the current machine.
func (s *rtBDD) run(args ...string) {
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), args) })
	s.out, s.err, s.exit = out, err, exitCode(err)
	if err != nil {
		s.out += "\nerror: " + err.Error() + "\n"
	}
}

// startLive starts an interactive command whose stdin the scenario types into.
func (s *rtBDD) startLive(args ...string) {
	pr, pw := io.Pipe()
	l := &liveRun{in: pw, out: pr, lines: make(chan string, 64), done: make(chan struct{})}
	// Typed lines are written in order by one writer; a line typed after the command stopped
	// reading is dropped when the run is finished, never left blocking the scenario.
	go func() {
		for line := range l.lines {
			if _, err := io.WriteString(pw, line+"\n"); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	edgeIsInteractive = func() bool { return true }
	edgeStdin = pr
	r, w, _ := os.Pipe()
	l.orig = os.Stdout
	os.Stdout = w
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(l, r); close(copied) }()
	go func() {
		l.err = dispatch(loadConfig(), args)
		_ = w.Close()
		<-copied
		close(l.done)
	}()
	s.live = l
}

func (s *rtBDD) typeLine(line string) {
	s.live.lines <- line
}

// waitFor waits until the live output says sub, or the command ends.
func (s *rtBDD) waitFor(sub string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.live.text(), sub) {
			return nil
		}
		select {
		case <-s.live.done:
			if strings.Contains(s.live.text(), sub) {
				return nil
			}
			return fmt.Errorf("the command ended without saying %q:\n%s", sub, s.live.text())
		case <-time.After(20 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for %q:\n%s", sub, s.live.text())
}

// finishLive closes the input and lands the result.
func (s *rtBDD) finishLive() {
	if s.live == nil {
		return
	}
	close(s.live.lines)
	select {
	case <-s.live.done:
	case <-time.After(15 * time.Second):
		s.t.Fatal("an interactive command did not end")
	}
	_ = s.live.out.Close() // unblocks a line typed after the command stopped reading
	os.Stdout = s.live.orig
	s.out, s.err, s.exit = s.live.text(), s.live.err, exitCode(s.live.err)
	if s.err != nil {
		s.out += "\nerror: " + s.err.Error() + "\n"
	}
	s.live = nil
	edgeIsInteractive = func() bool { return false }
	edgeStdin = strings.NewReader("")
}

// ---- authorities -------------------------------------------------------------

func (s *rtBDD) authority(name string) *rootedAuthority {
	if a, ok := s.auths[name]; ok {
		return a
	}
	a := newRootedAuthority(s.t, name)
	s.auths[name] = a
	return a
}

func (s *rtBDD) shed() *rootedAuthority { return s.auths["shed"] }

func (s *rtBDD) shortOf(name string) string {
	return edgeauth.ShortFingerprint(s.authority(name).fingerprint())
}

// allowMe allows the joiner's key on an authority and returns the code that minted.
func (s *rtBDD) allowMe(name string) string { return s.authority(name).allow(s.t, s.me.key) }

// enroll runs `roger edge enroll` on the current machine against an authority, with extra flags.
func (s *rtBDD) enroll(url string, flags ...string) {
	s.run(append([]string{"edge", "enroll", "bench", "--authority", url}, flags...)...)
}

// lastExchange is the last request and answer an authority handled.
func (s *rtBDD) lastExchange(a *rootedAuthority) (edgeauth.Request, edgeauth.Response, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.wire) - 2; i >= 0; i -= 2 {
		var req edgeauth.Request
		var resp edgeauth.Response
		if json.Unmarshal([]byte(a.wire[i]), &req) == nil && req.Nonce != "" &&
			json.Unmarshal([]byte(a.wire[i+1]), &resp) == nil && resp.Cert != "" {
			return req, resp, true
		}
	}
	return edgeauth.Request{}, edgeauth.Response{}, false
}

// heldFP is the root fingerprint the current machine holds, or "".
func (s *rtBDD) heldFP() string {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return ""
	}
	return edgeauth.RootFingerprint(id.Root)
}

// snapshot is every file in the current machine's Edge directory and its fleet file, by content.
func (s *rtBDD) snapshot() map[string]string {
	out := map[string]string{}
	root := filepath.Dir(configPath())
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if strings.Contains(p, string(filepath.Separator)+"edge") {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

// unchanged reports whether the current machine's Edge files are exactly what they were.
func (s *rtBDD) unchanged() error {
	now := s.snapshot()
	for p, was := range s.before {
		if now[p] != was {
			return fmt.Errorf("%s changed", filepath.Base(p))
		}
	}
	for p := range now {
		if _, ok := s.before[p]; !ok {
			return fmt.Errorf("%s was written", filepath.Base(p))
		}
	}
	return nil
}

func (s *rtBDD) keptNothing() error {
	if _, _, ok, err := edgeIdentityStore().LoadIdentity(); err != nil || ok {
		return fmt.Errorf("an identity was kept (err %v)", err)
	}
	for _, f := range []string{edgeauth.RootCertFile, edgeauth.NodeKeyFile, edgeauth.NodeCertFile, edgeauth.TrustFile} {
		if _, err := os.Stat(filepath.Join(edgeAuthDir(), f)); !os.IsNotExist(err) {
			return fmt.Errorf("%s was kept", f)
		}
	}
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	if list, _ := st.fleet.List(); len(list) != 0 {
		return fmt.Errorf("a fleet row was kept")
	}
	return nil
}

func (s *rtBDD) says(subs ...string) error {
	flat := strings.Join(strings.Fields(s.out), " ")
	for _, sub := range subs {
		if !strings.Contains(flat, sub) {
			return fmt.Errorf("the output does not say %q:\n%s", sub, s.out)
		}
	}
	return nil
}

func (s *rtBDD) never(sub string) error {
	if strings.Contains(s.out, sub) {
		return fmt.Errorf("the output says %q:\n%s", sub, s.out)
	}
	return nil
}

func (s *rtBDD) refused() error {
	if s.err == nil {
		return fmt.Errorf("it was NOT refused:\n%s", s.out)
	}
	return nil
}

func (s *rtBDD) succeeded() error {
	if s.err != nil {
		return fmt.Errorf("it failed: %v\n%s", s.err, s.out)
	}
	return nil
}

// ---- Background ----------------------------------------------------------------

func (s *rtBDD) aLocalAuthority(name string) error {
	s.shedM = s.newMachine()
	a := newRootedAuthorityAt(s.t, s.edgeDirOf(s.shedM), name)
	s.auths[name] = a
	// As `roger edge authority local` leaves it: the designating machine's own key is allowed,
	// and protected, so it can enroll itself.
	if err := a.local.AllowProtected(s.keyHex(s.shedM)); err != nil {
		return err
	}
	// shed's own machine knows it is the authority, as `roger edge authority local` leaves it.
	return s.on(s.shedM, func() error { return edgeIdentityStore().SaveDescriptor(a.local.Descriptor()) })
}

func (s *rtBDD) authorityAnswers() error {
	s.target = s.shed().url
	return nil
}

// ---- 1. what needs proof ---------------------------------------------------------

func (s *rtBDD) holdsNoRoot() error {
	if s.heldFP() != "" {
		return fmt.Errorf("this machine already holds a root")
	}
	s.before = s.snapshot()
	return nil
}

func (s *rtBDD) enrollsWithNeither() error {
	s.allowMe("shed")
	s.enroll(s.shed().url)
	return nil
}

func (s *rtBDD) namesWhereTheCodeComesFrom() error { return s.says("--code", "allow or adopt") }
func (s *rtBDD) namesThePin() error {
	return s.says("--root sha256:", "the full fingerprint `roger edge authority` prints there")
}

func (s *rtBDD) noInteractiveInput() error {
	edgeIsInteractive = func() bool { return false }
	return nil
}

func (s *rtBDD) runsLiteral(line string) error {
	args := strings.Fields(line)
	if len(args) < 2 || args[0] != "roger" {
		return fmt.Errorf("not a roger command line: %q", line)
	}
	s.run(args[1:]...)
	return nil
}

func (s *rtBDD) refusedBeforeSending() error {
	if s.exit != 2 {
		return fmt.Errorf("not refused as a usage error before sending (exit %d):\n%s", s.exit, s.out)
	}
	return s.says("nothing was sent")
}

func (s *rtBDD) exitsNonZero() error {
	if s.exit == 0 {
		return fmt.Errorf("it exited 0:\n%s", s.out)
	}
	return nil
}

func (s *rtBDD) enrolledUnderShed() error {
	s.enroll(s.shed().url, "--code", s.allowMe("shed"))
	if err := s.succeeded(); err != nil {
		return err
	}
	s.before = s.snapshot()
	return nil
}

// closeToExpiry moves both clocks most of the way through the certificate's thirty days, so the
// certificate is genuinely due for renewal.
func (s *rtBDD) closeToExpiry() error {
	later := time.Now().Add(21 * 24 * time.Hour)
	edgeNow = func() time.Time { return later }
	s.shed().local.SetNow(func() time.Time { return later })
	return nil
}

func (s *rtBDD) renewsWithNoCode() error {
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	s.extra = []string{id.NodeID, id.CertPEM}
	n := s.shed().enrolls.Load()
	s.enroll(s.shed().url)
	if s.shed().enrolls.Load() == n {
		return fmt.Errorf("no renewal request reached the authority")
	}
	return nil
}

func (s *rtBDD) renewalAccepted() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	if id.CertPEM == s.extra[1] {
		return fmt.Errorf("the certificate was not renewed")
	}
	if edgeauth.RootFingerprint(id.Root) != s.shed().fingerprint() {
		return fmt.Errorf("the renewed root is not the one held")
	}
	return nil
}

func (s *rtBDD) nodeNameFleetUnchanged() error {
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	if id.NodeID != s.extra[0] {
		return fmt.Errorf("the node id changed")
	}
	st, _ := loadEdgeState()
	list, _ := st.fleet.List()
	if len(list) != 1 || list[0].ID != id.NodeID || list[0].Name != "bench" {
		return fmt.Errorf("the fleet changed: %+v", list)
	}
	return nil
}

func (s *rtBDD) designatedNotEnrolled(string) error {
	s.use(s.shedM)
	s.extra = []string{fmt.Sprint(s.shed().hits.Load())}
	return nil
}

func (s *rtBDD) enrollsWithNoAuthority() error {
	s.run("edge", "enroll", "shed")
	return nil
}

func (s *rtBDD) enrollsInProcess() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	if s.heldFP() != s.shed().fingerprint() {
		return fmt.Errorf("it does not hold the root it generated")
	}
	return nil
}

func (s *rtBDD) noCodeAtAll() error {
	raw, _ := os.ReadFile(filepath.Join(s.shed().dir, edgeauth.AuthorityDir, edgeauth.CodesFile))
	if strings.Contains(string(raw), "join") {
		return fmt.Errorf("a code was minted: %s", raw)
	}
	return s.never("enter the enrollment code")
}

func (s *rtBDD) nothingCrossed() error {
	if got := fmt.Sprint(s.shed().hits.Load()); got != s.extra[0] {
		return fmt.Errorf("the authority's listener was contacted")
	}
	return nil
}

func (s *rtBDD) runsAgainWithBogusCode() error {
	s.extra = []string{fmt.Sprint(s.shed().enrolls.Load())}
	s.enroll(s.shed().url, "--code", "ZZZZZ-ZZZZZ")
	return nil
}

// nothingSentChecked: no enrollment request was made and no code was checked. (The second-
// authority guard may read the authority's PUBLIC root first; that is a read, not an enrollment.)
func (s *rtBDD) nothingSentChecked() error {
	if fmt.Sprint(s.shed().enrolls.Load()) != s.extra[0] {
		return fmt.Errorf("an enrollment request was sent")
	}
	if err := s.never("does not match"); err != nil {
		return err
	}
	return s.unchanged()
}

func (s *rtBDD) saysAlreadyEnrolled() error { return s.says("already enrolled", "nothing to do") }

// ---- 2. the code -------------------------------------------------------------------

// allowOnShed runs `roger edge authority allow` for the joiner's key on shed's own machine, with
// the authority's LAN address known there (its port file, and its address for the command).
func (s *rtBDD) allowOnShed() error {
	port := strings.TrimPrefix(s.shed().url, "http://127.0.0.1:")
	return s.on(s.shedM, func() error {
		if err := os.WriteFile(edgeAuthorityPortFile(), []byte(port), 0o600); err != nil {
			return err
		}
		s.run("edge", "authority", "allow", s.keyHex(s.me))
		return s.succeeded()
	})
}

var codeRE = regexp.MustCompile(`[0-9A-Z]{5}-[0-9A-Z]{5}`)

func (s *rtBDD) codeOut() string { return codeRE.FindString(s.out) }

func (s *rtBDD) printsCodeShape() error {
	if s.codeOut() == "" {
		return fmt.Errorf("no code shaped XXXXX-XXXXX:\n%s", s.out)
	}
	return nil
}

func (s *rtBDD) printsJoinCommand() error {
	return s.says("roger edge enroll <name> --authority " + s.shed().url + " --code " + s.codeOut())
}

func (s *rtBDD) printsRootShort() error { return s.says(s.shortOf("shed")) }
func (s *rtBDD) saysFifteenOneJoin() error {
	return s.says("good for 15 minutes, for one join")
}

func (s *rtBDD) phoneCandidate(name string) error {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s.phone = &rtPhone{pub: pub, priv: priv, id: edgeauth.NodeID(pub), where: map[string]string{}}
	return s.on(s.shedM, func() error {
		st, err := loadEdgeState()
		if err != nil {
			return err
		}
		st.candidates = append(st.candidates, store.EdgeNode{ID: s.phone.id, Name: name, Kind: "mobile"})
		return st.save()
	})
}

func (s *rtBDD) runsAdoptOnShed(name string) error {
	return s.on(s.shedM, func() error { s.run("edge", "adopt", name); return s.succeeded() })
}

func (s *rtBDD) printsCodeFor(name string) error {
	code, _, ok := s.shed().local.NodeCode(s.phone.id)
	if !ok {
		return fmt.Errorf("no code was minted for %s", name)
	}
	return s.says(name, edgeauth.FormatCode(code))
}

func (s *rtBDD) saysEnterOn(name string) error {
	return s.says("on " + name + ", enter the enrollment code")
}

func (s *rtBDD) forgottenDevice(name string) error {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s.phone = &rtPhone{pub: pub, priv: priv, id: edgeauth.NodeID(pub), where: map[string]string{}}
	return s.on(s.shedM, func() error {
		return edgeRecordDeparted(store.EdgeNode{ID: s.phone.id, Name: name, Kind: "mobile"})
	})
}

func (s *rtBDD) readmitsAndConfirms(name string) error {
	return s.on(s.shedM, func() error {
		edgeStdin = strings.NewReader("y\n")
		s.run("edge", "readmit", name)
		edgeStdin = strings.NewReader("")
		return s.succeeded()
	})
}

func (s *rtBDD) saysHeldRootNeedsNone() error {
	return s.says("a device that still holds this Edge's root will not need it")
}

func (s *rtBDD) tenThousandCodes() error {
	s.extra = s.extra[:0]
	for i := 0; i < 10000; i++ {
		s.extra = append(s.extra, edgeauth.NewCode())
	}
	return nil
}

func (s *rtBDD) everySymbolCrockford() error {
	for _, c := range s.extra {
		if canon, err := edgeauth.CanonicalCode(c); err != nil || canon != c || len(c) != edgeauth.CodeLen {
			return fmt.Errorf("%q is not a canonical code", c)
		}
	}
	return nil
}

func (s *rtBDD) noTwoEqual() error {
	seen := map[string]bool{}
	for _, c := range s.extra {
		if seen[c] {
			return fmt.Errorf("a repeated code %q", c)
		}
		seen[c] = true
	}
	return nil
}

func (s *rtBDD) uniform() error {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var counts [edgeauth.CodeLen][32]int
	for _, c := range s.extra {
		for i, r := range c {
			counts[i][strings.IndexRune(alphabet, r)]++
		}
	}
	exp := float64(len(s.extra)) / 32
	for pos := range counts {
		for sym, n := range counts[pos] {
			if d := float64(n) - exp; d > exp*0.35 || d < -exp*0.35 {
				return fmt.Errorf("position %d symbol %c drawn %d times (expected about %.0f)", pos, alphabet[sym], n, exp)
			}
		}
	}
	return nil
}

func (s *rtBDD) allowedShownA() error {
	if err := s.allowOnShed(); err != nil {
		return err
	}
	s.codes["A"] = s.codeOut()
	return nil
}

func (s *rtBDD) allowedAgainShownB() error {
	if err := s.allowOnShed(); err != nil {
		return err
	}
	s.codes["B"] = s.codeOut()
	if s.codes["A"] == s.codes["B"] {
		return fmt.Errorf("allowing again showed the same code")
	}
	return nil
}

func (s *rtBDD) proofMadeWith(label string) error {
	s.enroll(s.shed().url, "--code", s.codes[label])
	if err := s.succeeded(); err != nil {
		return err
	}
	req, resp, ok := s.lastExchange(s.shed())
	if !ok {
		return fmt.Errorf("no answer was seen")
	}
	s.req, s.resp = req, resp
	return edgeauth.VerifyRootProof(s.codes[label], resp.RootProof, s.shed().fingerprint(), resp.Account, resp.NodeID, req.Nonce)
}

func (s *rtBDD) proofANeverAgain() error {
	if edgeauth.VerifyRootProof(s.codes["A"], s.resp.RootProof, s.shed().fingerprint(), s.resp.Account, s.resp.NodeID, s.req.Nonce) == nil {
		return fmt.Errorf("the answer's proof verifies with the voided code A")
	}
	other := s.newMachine()
	other.key = s.me.key
	return s.on(other, func() error {
		s.enroll(s.shed().url, "--code", s.codes["A"])
		if s.err == nil {
			return fmt.Errorf("a join with the voided code A was kept")
		}
		return nil
	})
}

func (s *rtBDD) codesForBenchAndPocket() error {
	s.codes["A"] = s.allowMe("shed")
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s.phone = &rtPhone{pub: pub, priv: priv, id: edgeauth.NodeID(pub), where: map[string]string{}}
	if err := s.shed().local.GrantClaim(s.phone.id, "pocket"); err != nil {
		return err
	}
	b, err := s.shed().local.MintNodeCode(s.phone.id)
	s.codes["B"] = b
	return err
}

// phoneClaims sends a REAL signed claim from the phone to an authority and holds the answer.
func (s *rtBDD) phoneClaims(url string) error {
	c := edgeauth.NewClaimRequest("pocket", "mobile", s.phone.pub, time.Now())
	c.Sign(s.phone.priv)
	resp, err := enrollhttp.Claim(context.Background(), url, c)
	if err != nil {
		s.phone.err = err
		return nil
	}
	s.phone.held = append(s.phone.held, rtHeld{resp: resp, nonce: c.Nonce, from: url})
	return nil
}

func (s *rtBDD) pocketEnrolls() error { return s.phoneClaims(s.shed().url) }

func (s *rtBDD) pocketProofBOnly() error {
	if len(s.phone.held) != 1 {
		return fmt.Errorf("the phone holds %d answers: %v", len(s.phone.held), s.phone.err)
	}
	h := s.phone.held[0]
	fp := s.shed().fingerprint()
	if err := edgeauth.VerifyRootProof(s.codes["B"], h.resp.RootProof, fp, h.resp.Account, h.resp.NodeID, h.nonce); err != nil {
		return fmt.Errorf("the proof does not verify with B: %v", err)
	}
	if edgeauth.VerifyRootProof(s.codes["A"], h.resp.RootProof, fp, h.resp.Account, h.resp.NodeID, h.nonce) == nil {
		return fmt.Errorf("the proof also verifies with A")
	}
	return nil
}

func (s *rtBDD) codeARemainsLive() error {
	s.enroll(s.shed().url, "--code", s.codes["A"])
	return s.succeeded()
}

// ---- where a code may never appear --------------------------------------------------

func (s *rtBDD) codeMintedForAJoin() error {
	// The whole life of a code, end to end: allow mints it (on the owner's screen), the joiner
	// uses it, an adopt mints another for a phone, shed enrolls itself and advertises.
	if err := s.allowOnShed(); err != nil {
		return err
	}
	s.codes["join"] = s.codeOut()
	s.enroll(s.shed().url, "--code", s.codes["join"])
	if err := s.succeeded(); err != nil {
		return err
	}
	if err := s.phoneCandidate("pocket"); err != nil {
		return err
	}
	if err := s.runsAdoptOnShed("pocket"); err != nil {
		return err
	}
	code, _, _ := s.shed().local.NodeCode(s.phone.id)
	s.codes["adopt"] = code
	return s.on(s.shedM, func() error { s.run("edge", "enroll", "shed"); return s.succeeded() })
}

func (s *rtBDD) codeForms() []string {
	var out []string
	for _, c := range []string{s.codes["join"], s.codes["adopt"]} {
		canon, _ := edgeauth.CanonicalCode(c)
		out = append(out, canon, edgeauth.FormatCode(canon))
	}
	return out
}

func (s *rtBDD) absentFrom(where, text string) error {
	for _, c := range s.codeForms() {
		if c != "" && strings.Contains(text, c) {
			return fmt.Errorf("the code %s appears in %s", c, where)
		}
	}
	return nil
}

func (s *rtBDD) doesNotAppearIn(place string) error {
	switch place {
	case "the mDNS advertisement":
		var txt []string
		err := s.on(s.shedM, func() error {
			h, err := newEdgeHost("")
			if err != nil {
				return err
			}
			defer h.closeListeners()
			adv, ok := h.startFace()
			if !ok {
				return fmt.Errorf("shed has nothing to advertise")
			}
			adv.AuthPort = 1
			txt = adv.TXT()
			return nil
		})
		if err != nil {
			return err
		}
		return s.absentFrom(place, strings.Join(txt, "\n"))
	case "any request or answer on the wire":
		return s.absentFrom(place, s.shed().wireLog())
	case "the roger log":
		return s.absentFrom(place, s.logs.String())
	case "the Edge events feed":
		var all string
		for _, m := range []*rtMachine{s.me, s.shedM} {
			_ = s.on(m, func() error { s.run("edge", "events", "--json"); all += s.out; return nil })
		}
		return s.absentFrom(place, all)
	case "the trouble feed", "the fleet store or the departed record":
		// Every file either machine keeps, except the authority's own code store.
		var all strings.Builder
		for _, m := range []*rtMachine{s.me, s.shedM} {
			_ = filepath.Walk(m.dir, func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() && filepath.Base(p) != edgeauth.CodesFile {
					b, _ := os.ReadFile(p)
					all.Write(b)
				}
				return nil
			})
		}
		return s.absentFrom(place, all.String())
	case "the console's unauthenticated reads":
		var body string
		err := s.on(s.shedM, func() error {
			srv := webui.New(node.New(node.Config{Station: "shed", Upstream: "http://127.0.0.1:1/v1"}), webui.Options{
				Edge: webui.EdgeHooks{
					Self: "shed", AdoptCode: edgeAdoptCode,
					Adopting: func() []store.EdgeNode { return []store.EdgeNode{{ID: s.phone.id, Name: "pocket"}} },
				},
			})
			hs := httptest.NewServer(srv.Handler())
			defer hs.Close()
			resp, err := http.Get(hs.URL + "/api/edge")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			body = string(b)
			if resp.StatusCode != http.StatusForbidden {
				return fmt.Errorf("an unauthenticated read answered %d", resp.StatusCode)
			}
			return nil
		})
		if err != nil {
			return err
		}
		return s.absentFrom(place, body)
	}
	return fmt.Errorf("unknown place %q", place)
}

// ---- the store ---------------------------------------------------------------------

func (s *rtBDD) codesPath() string {
	return filepath.Join(s.shed().dir, edgeauth.AuthorityDir, edgeauth.CodesFile)
}

func (s *rtBDD) codeMintedOnShed() error {
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) storedOwnerOnly() error {
	fi, err := os.Stat(s.codesPath())
	if err != nil {
		return err
	}
	if fi.Mode().Perm() != 0o600 {
		return fmt.Errorf("the code store is %v", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(s.codesPath())
	canon, _ := edgeauth.CanonicalCode(s.code)
	if !strings.Contains(string(raw), canon) {
		return fmt.Errorf("the live code is not in the store")
	}
	return nil
}

func (s *rtBDD) codeSpent() error {
	s.enroll(s.shed().url, "--code", s.code)
	return s.succeeded()
}

func (s *rtBDD) deletedFromStore() error {
	raw, _ := os.ReadFile(s.codesPath())
	canon, _ := edgeauth.CanonicalCode(s.code)
	if strings.Contains(string(raw), canon) {
		return fmt.Errorf("the spent code is still in the store")
	}
	return nil
}

func (s *rtBDD) restartNoRevive() error {
	s.shed().restart(s.t)
	return s.deletedFromStore()
}

// mintedAgo mints the joiner's code on shed's clock, minutes ago, then puts the clock back.
func (s *rtBDD) mintedAgo(minutes int) error {
	at := time.Now().Add(-time.Duration(minutes) * time.Minute)
	s.shed().local.SetNow(func() time.Time { return at })
	s.code = s.allowMe("shed")
	s.shed().local.SetNow(time.Now)
	return nil
}

func (s *rtBDD) authorityRestarts() error { s.shed().restart(s.t); return nil }

func (s *rtBDD) enrollsWithThatCode() error {
	s.enroll(s.shed().url, "--code", s.code)
	return nil
}

func (s *rtBDD) answerProofValid() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	return s.says("the code is what was checked")
}

// ---- 3. happy paths ------------------------------------------------------------------

func (s *rtBDD) allowedAndShown(string) error {
	if err := s.allowOnShed(); err != nil {
		return err
	}
	s.code = s.codeOut()
	return nil
}

func (s *rtBDD) runsEnrollWithCode() error {
	s.enroll(s.shed().url, "--code", s.code)
	return nil
}

func (s *rtBDD) answerVerifiesWithCode() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	req, resp, ok := s.lastExchange(s.shed())
	if !ok {
		return fmt.Errorf("no answer was seen")
	}
	return edgeauth.VerifyRootProof(s.code, resp.RootProof, s.shed().fingerprint(), resp.Account, resp.NodeID, req.Nonce)
}

func (s *rtBDD) keepsCertAndRoot() error {
	if s.heldFP() != s.shed().fingerprint() {
		return fmt.Errorf("this machine does not hold shed's root")
	}
	return nil
}

func (s *rtBDD) printsJoinedShed() error {
	return s.says("enrolled \"bench\"", s.shortOf("shed"))
}

func (s *rtBDD) allowedOnShed() error {
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) inputInteractive() error { return nil } // the live run below is interactive

func (s *rtBDD) runsEnrollNoCode() error {
	s.startLive("edge", "enroll", "bench", "--authority", s.shed().url)
	return nil
}

func (s *rtBDD) answerHeldNotKept() error {
	if err := s.waitFor("enter the enrollment code"); err != nil {
		return err
	}
	if s.shed().enrolls.Load() != 1 {
		return fmt.Errorf("the request was not sent")
	}
	if s.heldFP() != "" {
		return fmt.Errorf("the answer was kept before the code")
	}
	return nil
}

func (s *rtBDD) asksForCode() error {
	return s.waitFor("enter the enrollment code shown on the authority")
}

func (s *rtBDD) typesTheCode() error {
	s.typeLine(s.code)
	return nil
}

func (s *rtBDD) heldVerifiesKept() error {
	s.finishLive()
	if err := s.succeeded(); err != nil {
		return err
	}
	return s.keepsCertAndRoot()
}

func (s *rtBDD) interactiveHolding() error {
	s.code = s.allowMe("shed")
	s.runsEnrollNoCode()
	return s.waitFor("enter the enrollment code")
}

func (s *rtBDD) typesOneWrongSymbol() error {
	b := []byte(s.code)
	if b[0] == 'Z' {
		b[0] = 'Y'
	} else {
		b[0] = 'Z'
	}
	s.typeLine(string(b))
	return nil
}

func (s *rtBDD) saysNoMatchAsksAgain() error {
	if err := s.waitFor("does not match the Edge that answered"); err != nil {
		return err
	}
	if strings.Count(s.live.text(), "enter the enrollment code") < 1 {
		return fmt.Errorf("it did not ask")
	}
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(s.live.text(), "enter the enrollment code") < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Count(s.live.text(), "enter the enrollment code") < 2 {
		return fmt.Errorf("it did not ask again:\n%s", s.live.text())
	}
	return nil
}

func (s *rtBDD) typesRightCode() error { return s.typesTheCode() }
func (s *rtBDD) identityKept() error   { return s.heldVerifiesKept() }

func (s *rtBDD) askedOnce() error {
	if n := s.shed().enrolls.Load(); n != 1 {
		return fmt.Errorf("the authority was asked %d times", n)
	}
	return nil
}

func (s *rtBDD) authorityPrintedFullFP() error {
	return s.on(s.shedM, func() error {
		s.run("edge", "authority")
		s.pin = s.shed().fingerprint()
		return s.says("sha256:" + s.pin)
	})
}

func (s *rtBDD) runsEnrollWithPin() error {
	s.allowMe("shed")
	s.enroll(s.shed().url, "--root", "sha256:"+s.pin)
	return nil
}

func (s *rtBDD) rootHashesToPin() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	if s.heldFP() != s.pin {
		return fmt.Errorf("the kept root does not hash to the pin")
	}
	return nil
}

func (s *rtBDD) noCodeNeeded() error { return s.never("enter the enrollment code") }

func (s *rtBDD) runsAuthorityOnShed() error {
	return s.on(s.shedM, func() error { s.run("edge", "authority"); return s.succeeded() })
}

func (s *rtBDD) printsFullSHA() error { return s.says("--root sha256:" + s.shed().fingerprint()) }
func (s *rtBDD) printsShortBeside() error {
	return s.says("root " + s.shortOf("shed"))
}

// pinFrom renders one of the outline's pin shapes from shed's real fingerprint.
func (s *rtBDD) pinFrom(shape string) string {
	fp := s.shed().fingerprint()
	switch shape {
	case "sha256:<64 lowercase hex>":
		return "sha256:" + fp
	case "sha256:<64 UPPERCASE hex>":
		return "sha256:" + strings.ToUpper(fp)
	case "<64 hex, no prefix>":
		return fp
	case "sha256:<64 hex grouped with colons or spaces>":
		var parts []string
		for i := 0; i < 64; i += 2 {
			parts = append(parts, fp[i:i+2])
		}
		return "sha256:" + strings.Join(parts, ":")
	case "sha256:<the first 16 hex only>":
		return "sha256:" + fp[:16]
	case "sha256:<63 hex>":
		return "sha256:" + fp[:63]
	case "sha256:<64 chars with a non-hex symbol>":
		return "sha256:" + fp[:63] + "g"
	case "md5:<32 hex>":
		return "md5:" + fp[:32]
	}
	return shape
}

func (s *rtBDD) passesRoot(shape string) error {
	s.allowMe("shed")
	s.enroll(s.shed().url, "--root", s.pinFrom(shape))
	return nil
}

func (s *rtBDD) pinIs(verdict string) error {
	if verdict == "accepted" {
		if err := s.succeeded(); err != nil {
			return err
		}
		return s.rootHashesToPinOf(s.shed().fingerprint())
	}
	if s.exit != 2 {
		return fmt.Errorf("the pin was not refused before sending (exit %d):\n%s", s.exit, s.out)
	}
	return s.says(strings.TrimPrefix(verdict, "refused as "))
}

func (s *rtBDD) rootHashesToPinOf(fp string) error {
	if s.heldFP() != fp {
		return fmt.Errorf("the kept root is not the pinned one")
	}
	return nil
}

var inviteRE = regexp.MustCompile(`rogerai-edge://join\?\S+`)

func (s *rtBDD) allowPrintsInvite() error {
	if err := s.allowOnShed(); err != nil {
		return err
	}
	s.code = s.codeOut()
	s.extra = []string{inviteRE.FindString(s.out)}
	if s.extra[0] == "" {
		return fmt.Errorf("no invite was printed:\n%s", s.out)
	}
	return nil
}

func (s *rtBDD) runsEnrollInvite() error {
	n := s.shed().enrolls.Load()
	s.run("edge", "enroll", "bench", "--invite", s.extra[0])
	s.extra = append(s.extra, fmt.Sprint(n))
	return nil
}

func (s *rtBDD) enrollsAtInviteAddress() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	if fmt.Sprint(s.shed().enrolls.Load()) == s.extra[1] {
		return fmt.Errorf("the invite's address was not asked")
	}
	return nil
}

func (s *rtBDD) pinAndProofBoth() error {
	if s.heldFP() != s.shed().fingerprint() {
		return fmt.Errorf("the kept root is not the invite's")
	}
	return s.says("the code is what was checked")
}

func (s *rtBDD) disagreeingInviteRefused() error {
	other := s.authority("other")
	fresh := s.newMachine()
	fresh.key = s.me.key
	return s.on(fresh, func() error {
		code := s.allowMe("shed")
		inv := edgeInvite(strings.TrimPrefix(s.shed().url, "http://"), other.fingerprint(), code)
		s.run("edge", "enroll", "bench", "--invite", inv)
		if err := s.refused(); err != nil {
			return err
		}
		return s.keptNothing()
	})
}

func (s *rtBDD) entersCodeAs(typed string) error {
	s.code = typed
	return nil
}

func (s *rtBDD) readAsCode(want string) error {
	got, err := edgeauth.CanonicalCode(s.code)
	if strings.HasPrefix(want, "refused: ") {
		if err == nil {
			return fmt.Errorf("%q was read as %q", s.code, got)
		}
		reason := strings.TrimPrefix(want, "refused: ")
		switch {
		case strings.Contains(reason, "symbols"):
			if !strings.Contains(err.Error(), "10 symbols") {
				return fmt.Errorf("the refusal does not say a code is 10 symbols: %v", err)
			}
		case strings.Contains(reason, "not a code symbol"):
			if !strings.Contains(err.Error(), "not a code symbol") {
				return fmt.Errorf("the refusal does not name the symbol: %v", err)
			}
		}
		return nil
	}
	if err != nil || got != want {
		return fmt.Errorf("%q was read as %q (%v), want %q", s.code, got, err, want)
	}
	return nil
}

// ---- the phone, through the one rule every joiner applies ----------------------------

func (s *rtBDD) phoneAdoptedShown(name string) error {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s.phone = &rtPhone{pub: pub, priv: priv, id: edgeauth.NodeID(pub), where: map[string]string{}}
	return s.on(s.shedM, func() error {
		if err := edgeAdoptByClaim(s.phone.id, name); err != nil {
			return err
		}
		c, _, ok := edgeAdoptCode(s.phone.id)
		if !ok {
			return fmt.Errorf("adopt showed no code")
		}
		s.code = c
		return nil
	})
}

func (s *rtBDD) pocketClaimsAnswer() error {
	if err := s.phoneClaims(s.shed().url); err != nil {
		return err
	}
	if len(s.phone.held) != 1 || s.phone.held[0].resp.RootProof == "" {
		return fmt.Errorf("shed did not answer with a certificate and a proof: %v", s.phone.err)
	}
	return nil
}

func (s *rtBDD) pocketHoldsAsks() error {
	if s.phone.kept != nil {
		return fmt.Errorf("the phone kept an answer before the code")
	}
	return nil
}

// phoneEntersCode applies the phone's rule to every held answer: keep the one the code verifies.
func (s *rtBDD) phoneEntersCode() error {
	s.phone.err = nil
	for _, h := range s.phone.held {
		want := edgeauth.Expect{NodeID: s.phone.id, Key: s.phone.pub, Now: time.Now(), Root: s.phone.root}
		code := s.code
		if s.phone.root != nil {
			code = ""
		}
		id, err := edgeauth.VerifyAnswer(h.resp, want, h.nonce, code, "")
		if err == nil {
			s.phone.kept = id
			return nil
		}
		s.phone.err = err
		s.phone.where[h.from] = err.Error()
	}
	return nil
}

func (s *rtBDD) pocketKeeps() error {
	if s.phone.kept == nil {
		return fmt.Errorf("the phone kept nothing: %v", s.phone.err)
	}
	if edgeauth.RootFingerprint(s.phone.kept.Root) != s.shed().fingerprint() {
		return fmt.Errorf("the phone kept another root")
	}
	return nil
}

// presentsVerified presents the kept certificate to a REAL presence handler over shed's root.
func (s *rtBDD) presentsVerified() error {
	fleet := edge.NewFleet(store.NewMem(), edgeauth.LocalAccount)
	srv := httptest.NewServer(edge.PresenceHandler(fleet, s.shed().local.Authority(), nil))
	defer srv.Close()
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	p := edge.PresenceRequest{NodeID: s.phone.id, Cert: s.phone.kept.CertPEM, Name: "pocket", Kind: "mobile",
		Caps: []string{"classify"}, Nonce: hex.EncodeToString(nonce), TS: time.Now().Unix()}
	canon := strings.Join([]string{"rogerai-edge-present/v1", p.NodeID, p.Name, p.Kind, strings.Join(p.Caps, ","),
		p.Addr, p.Nonce, fmt.Sprint(p.TS)}, "\n")
	p.Sig = hex.EncodeToString(ed25519.Sign(s.phone.priv, []byte(canon)))
	body, _ := json.Marshal(p)
	resp, err := http.Post(srv.URL+edge.PresencePath, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	n, ok, _ := fleet.Get(s.phone.id)
	if resp.StatusCode != http.StatusOK || !ok || n.Presence != string(edge.PresenceVerified) {
		return fmt.Errorf("the phone did not present as a verified member (%d)", resp.StatusCode)
	}
	return nil
}

func (s *rtBDD) phoneKeyAllowedShown() error {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s.phone = &rtPhone{pub: pub, priv: priv, id: edgeauth.NodeID(pub), where: map[string]string{}}
	s.code = s.allowMe("shed")
	return nil
}

// phoneJoinsByAddress: the phone's typed-address join is an ENROLL signed by its account key.
func (s *rtBDD) phoneJoinsByAddress() error {
	req := edgeauth.NewRequest("", "pocket", "mobile", s.phone.pub, time.Now())
	req.Sign(s.me.key)
	resp, err := enrollhttp.Enroll(context.Background(), s.shed().url, req)
	if err != nil {
		return err
	}
	s.phone.held = append(s.phone.held, rtHeld{resp: resp, nonce: req.Nonce, from: s.shed().url})
	return s.phoneEntersCode()
}

func (s *rtBDD) headlessMachine() error {
	edgeIsInteractive = func() bool { return false }
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) runsHeadless() error {
	s.run("edge", "enroll", "pi-cabinet", "--authority", s.shed().url, "--code", s.code)
	return nil
}

func (s *rtBDD) enrollsNoPrompt() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	return s.never("enter the enrollment code")
}

func (s *rtBDD) wrongCodeHeadless() error {
	fresh := s.newMachine()
	fresh.key = s.me.key
	return s.on(fresh, func() error {
		done := make(chan struct{})
		go func() {
			s.run("edge", "enroll", "pi-cabinet", "--authority", s.shed().url, "--code", "ZZZZZ-ZZZZZ")
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			return fmt.Errorf("a headless join with a wrong code waited")
		}
		if s.exit == 0 {
			return fmt.Errorf("a wrong code exited 0")
		}
		return s.keptNothing()
	})
}

func (s *rtBDD) joinsShedWithCode() error {
	s.enroll(s.shed().url, "--code", s.allowMe("shed"))
	return s.succeeded()
}

func (s *rtBDD) joinerPrintsShort() error { return s.says("root " + s.shortOf("shed")) }

func (s *rtBDD) sameShortOnShed() error {
	joined := s.out
	return s.on(s.shedM, func() error {
		s.run("edge", "authority")
		if !strings.Contains(s.out, s.shortOf("shed")) || !strings.Contains(joined, s.shortOf("shed")) {
			return fmt.Errorf("the two short fingerprints differ")
		}
		s.out = joined
		return nil
	})
}

func (s *rtBDD) saysReassurance() error {
	return s.says("for reassurance", "the code is what was checked")
}

// ---- 4. impostors ---------------------------------------------------------------------

func (s *rtBDD) impostorOwnRoot() error {
	imp := s.authority("impostor")
	imp.allow(s.t, s.me.key)
	s.target = imp.url
	return nil
}

func (s *rtBDD) holdsShedCode(string) error {
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) holdsACode() error { return s.holdsShedCode("shed") }

// checksTheAnswer is this machine joining at the target with whatever the owner gave it.
func (s *rtBDD) checksTheAnswer() error {
	s.before = s.snapshot()
	var flags []string
	if s.code != "" {
		flags = append(flags, "--code", s.code)
	}
	if s.pin != "" {
		flags = append(flags, "--root", s.pin)
	}
	s.enroll(s.target, flags...)
	return nil
}

func (s *rtBDD) proofDoesNotVerify() error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.says("does not match the Edge that answered")
}

func (s *rtBDD) saysImpostorLine() error {
	return s.says("is not the Edge that showed you this code")
}

func (s *rtBDD) namesAddrAndShort() error {
	return s.says(strings.TrimPrefix(s.target, "http://"), s.shortOf("impostor"))
}

func (s *rtBDD) noDifferentCode() error { return s.never("different code") }

func (s *rtBDD) impostorNoProof() error {
	imp := s.authority("impostor")
	imp.allow(s.t, s.me.key)
	imp.strip.Store(true)
	s.target = imp.url
	return nil
}

func (s *rtBDD) refusedKeepingNothing() error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.keptNothing()
}

func (s *rtBDD) saysNoProof() error { return s.says("carried no proof") }

// impostorSameShort: a root whose first 64 bits collide with shed's cannot be manufactured in a
// test - that is the point of the full fingerprint. So this impostor is an ordinary one, and the
// scenario proves the decision never consumes the short form: a short pin is refused outright, and
// the proof covers all 64 hex characters.
func (s *rtBDD) impostorSameShort() error {
	if _, err := edgeauth.ParsePin(s.shortOf("shed")); err == nil {
		return fmt.Errorf("the short fingerprint was accepted as a pin")
	}
	fp := s.shed().fingerprint()
	tail := fp[:16] + strings.Repeat("0", 48)
	if edgeauth.RootProof("ABCDEABCDE", fp, "a", "n", "x") == edgeauth.RootProof("ABCDEABCDE", tail, "a", "n", "x") {
		return fmt.Errorf("the proof does not cover the whole fingerprint")
	}
	return s.impostorOwnRoot()
}

func (s *rtBDD) joinsWithCodeOrPin() error {
	s.code = s.allowMe("shed")
	s.enroll(s.target, "--code", s.code)
	s.extra = []string{s.out, fmt.Sprint(s.exit)}
	fresh := s.newMachine()
	fresh.key = s.me.key
	return s.on(fresh, func() error {
		s.enroll(s.target, "--root", s.shed().fingerprint())
		s.extra = append(s.extra, s.out, fmt.Sprint(s.exit))
		return s.keptNothing()
	})
}

func (s *rtBDD) proofAndPinFail() error {
	if s.extra[1] == "0" || !strings.Contains(s.extra[0], "does not match the Edge that answered") {
		return fmt.Errorf("the code join was not refused:\n%s", s.extra[0])
	}
	if s.extra[3] == "0" || !strings.Contains(s.extra[2], "does not match the fingerprint you gave") {
		return fmt.Errorf("the pinned join was not refused:\n%s", s.extra[2])
	}
	return nil
}

func (s *rtBDD) gaveShedPin() error {
	s.pin = s.shed().fingerprint()
	s.code = ""
	return nil
}

func (s *rtBDD) impostorAnswersOwnRoot() error { return s.impostorOwnRoot() }

func (s *rtBDD) rootNotPin() error { return s.says("does not match the fingerprint you gave") }

func (s *rtBDD) refusedNamingBoth() error {
	if err := s.refusedKeepingNothing(); err != nil {
		return err
	}
	return s.says("expected sha256:"+s.shed().fingerprint(), "offered sha256:"+s.authority("impostor").fingerprint())
}

// relayTo stands a real device in between: it forwards every request to the target and every
// answer back, logging both, and can doctor the answer.
func (s *rtBDD) relayTo(target string, doctor func(map[string]any)) {
	s.relay = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, _ := http.NewRequest(r.Method, target+r.URL.Path, bytes.NewReader(body))
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if doctor != nil && resp.StatusCode == http.StatusOK && r.URL.Path == enrollhttp.Path {
			var m map[string]any
			if json.Unmarshal(out, &m) == nil {
				doctor(m)
				out, _ = json.Marshal(m)
				w.Header().Set("X-Root-Proof", "skip")
			}
		}
		s.relayed = append(s.relayed, string(body), string(out))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
	}))
	s.t.Cleanup(s.relay.Close)
	s.target = s.relay.URL
}

func (s *rtBDD) deviceInBetween() error {
	s.relayTo(s.shed().url, nil)
	return nil
}

func (s *rtBDD) joinsWithTheCode() error {
	s.code = s.allowMe("shed")
	s.enroll(s.target, "--code", s.code)
	return nil
}

func (s *rtBDD) proofVerifiesShed() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	return s.keepsCertAndRoot()
}

func (s *rtBDD) certForOwnKey() error {
	id, key, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		return fmt.Errorf("no identity: %v", err)
	}
	if id.NodeID != edgeauth.NodeID(key.Public().(ed25519.PublicKey)) {
		return fmt.Errorf("the certificate is not for this machine's own node key")
	}
	return nil
}

func (s *rtBDD) relayHoldsNothing() error {
	keyPEM, _ := os.ReadFile(filepath.Join(edgeAuthDir(), edgeauth.NodeKeyFile))
	all := strings.Join(s.relayed, "\n")
	canon, _ := edgeauth.CanonicalCode(s.code)
	if strings.Contains(all, canon) || strings.Contains(all, s.code) {
		return fmt.Errorf("the code crossed the device in between")
	}
	if len(keyPEM) > 0 && strings.Contains(all, strings.TrimSpace(string(keyPEM))) {
		return fmt.Errorf("the node key crossed the device in between")
	}
	return nil
}

// forwardToShed sends the joiner's own request body on to shed and returns shed's answer: what an
// impostor in the path can obtain.
func (s *rtBDD) forwardToShed(body []byte) edgeauth.Response {
	resp, err := http.Post(s.shed().url+enrollhttp.Path, "application/json", bytes.NewReader(body))
	if err != nil {
		return edgeauth.Response{}
	}
	defer resp.Body.Close()
	var out edgeauth.Response
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (s *rtBDD) impostorShedProofOwnRoot() error {
	imp := s.authority("impostor")
	imp.allow(s.t, s.me.key)
	imp.setMangle(func(resp *edgeauth.Response, body []byte) {
		real := s.forwardToShed(body)
		resp.RootProof = real.RootProof
	})
	s.target = imp.url
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) proofNotForThatRoot() error { return s.proofDoesNotVerify() }

func (s *rtBDD) impostorShedRootOwnCert() error {
	imp := s.authority("impostor")
	imp.allow(s.t, s.me.key)
	imp.setMangle(func(resp *edgeauth.Response, body []byte) {
		real := s.forwardToShed(body)
		resp.Root, resp.RootProof, resp.Account = real.Root, real.RootProof, real.Account
	})
	s.target = imp.url
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) certNotUnderRoot() error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.says("unknown authority")
}

func (s *rtBDD) refusedUnknownAuthority() error {
	if err := s.certNotUnderRoot(); err != nil {
		return err
	}
	return s.keptNothing()
}

func (s *rtBDD) realAnswerVerifies() error {
	s.code = s.allowMe("shed")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest("", "bench", "host", pub, time.Now())
	req.Sign(s.me.key)
	resp, err := enrollhttp.Enroll(context.Background(), s.shed().url, req)
	if err != nil {
		return err
	}
	s.req, s.resp = req, resp
	s.extra = []string{edgeauth.RootFingerprint(mustCert(resp.Root))}
	return edgeauth.VerifyRootProof(s.code, resp.RootProof, s.extra[0], resp.Account, resp.NodeID, req.Nonce)
}

func mustCert(pemText string) *x509.Certificate {
	c, _ := edgeauth.DecodeCert(pemText)
	return c
}

func (s *rtBDD) impostorChanges(field string) error {
	fp, acct, id, nonce, proof := s.extra[0], s.resp.Account, s.resp.NodeID, s.req.Nonce, s.resp.RootProof
	switch field {
	case "root":
		fp = s.authority("impostor").fingerprint()
	case "account":
		acct = "acct-impostor"
	case "node id":
		id = "n_impostor"
	case "request nonce it answers":
		nonce = strings.Repeat("0", 32)
	case "proof version":
		proof = "v2:" + strings.TrimPrefix(proof, "v1:")
	}
	s.err = edgeauth.VerifyRootProof(s.code, proof, fp, acct, id, nonce)
	return nil
}

func (s *rtBDD) theProofDoesNotVerify() error {
	if s.live == nil && s.err == nil && s.out == "" {
		return fmt.Errorf("the changed answer still verifies")
	}
	if s.out != "" {
		return s.proofDoesNotVerify()
	}
	return nil
}

func (s *rtBDD) capturedFromOther() error {
	other := s.newMachine()
	s.authority("shed").allow(s.t, other.key)
	var captured edgeauth.Response
	err := s.on(other, func() error {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		req := edgeauth.NewRequest("", "other", "host", pub, time.Now())
		req.Sign(other.key)
		resp, err := enrollhttp.Enroll(context.Background(), s.shed().url, req)
		captured = resp
		return err
	})
	if err != nil {
		return err
	}
	imp := s.authority("impostor")
	imp.allow(s.t, s.me.key)
	imp.setMangle(func(resp *edgeauth.Response, body []byte) { *resp = captured })
	s.target = imp.url
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) impostorReplays() error { return s.checksTheAnswer() }

func (s *rtBDD) namesAnotherNode() error {
	if err := s.refusedKeepingNothing(); err != nil {
		return err
	}
	return s.says("identity mismatch")
}

func (s *rtBDD) impostorRefusedForAJoin() error {
	imp := s.authority("impostor")
	imp.allow(s.t, s.me.key)
	s.target = imp.url
	s.startLive("edge", "enroll", "bench", "--authority", imp.url)
	if err := s.waitFor("enter the enrollment code"); err != nil {
		return err
	}
	s.typeLine("ZZZZZ-ZZZZZ")
	if err := s.waitFor("does not match"); err != nil {
		return err
	}
	s.typeLine("YYYYY-YYYYY")
	s.typeLine("")
	s.finishLive()
	return nil
}

func (s *rtBDD) oneLineAddrShort() error {
	addr := strings.TrimPrefix(s.target, "http://")
	n := 0
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, addr) && strings.Contains(ln, s.shortOf("impostor")) {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%d lines name the address and the root:\n%s", n, s.out)
	}
	return nil
}

func (s *rtBDD) saysCouldNotVerify() error { return s.saysImpostorLine() }

func (s *rtBDD) notRepeated() error {
	if c := strings.Count(s.out, "is not the Edge that showed you this code"); c != 1 {
		return fmt.Errorf("said %d times:\n%s", c, s.out)
	}
	return nil
}

// ---- 5. wrong codes -------------------------------------------------------------------

func (s *rtBDD) runsNeverMinted() error {
	s.allowMe("shed")
	s.before = s.snapshot()
	s.enroll(s.shed().url, "--code", "ZZZZZ-ZZZZZ")
	return nil
}

func (s *rtBDD) nothingIsKept() error { return s.keptNothing() }

func (s *rtBDD) saysCheckOrMint() error {
	return s.says("does not match the Edge that answered", "check the code", "mint a fresh one")
}

func (s *rtBDD) twoEdges() error {
	s.authority("lab")
	return nil
}

func (s *rtBDD) holdsLabCode() error {
	s.code = s.allowMe("lab")
	s.allowMe("shed")
	return nil
}

func (s *rtBDD) enrollsShedWithThatCode() error {
	s.enroll(s.shed().url, "--code", s.code)
	return nil
}

func (s *rtBDD) codesAForPocketBForMe() error {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s.phone = &rtPhone{pub: pub, priv: priv, id: edgeauth.NodeID(pub), where: map[string]string{}}
	if err := s.shed().local.GrantClaim(s.phone.id, "pocket"); err != nil {
		return err
	}
	a, err := s.shed().local.MintNodeCode(s.phone.id)
	if err != nil {
		return err
	}
	s.codes["A"], s.codes["B"] = a, s.allowMe("shed")
	return nil
}

func (s *rtBDD) enrollsWithA() error {
	s.enroll(s.shed().url, "--code", s.codes["A"])
	return nil
}

func (s *rtBDD) answerMadeWithB() error {
	req, resp, ok := s.lastExchange(s.shed())
	if !ok {
		return fmt.Errorf("no answer was seen")
	}
	return edgeauth.VerifyRootProof(s.codes["B"], resp.RootProof, s.shed().fingerprint(), resp.Account, resp.NodeID, req.Nonce)
}

func (s *rtBDD) runsMalformed(code string) error {
	s.allowMe("shed")
	s.extra = []string{fmt.Sprint(s.shed().enrolls.Load())}
	s.enroll(s.shed().url, "--code", code)
	return nil
}

func (s *rtBDD) refusedNamingShape() error { return s.says("XXXXX-XXXXX") }

func (s *rtBDD) noRequestSent() error {
	if fmt.Sprint(s.shed().enrolls.Load()) != s.extra[0] {
		return fmt.Errorf("a request was sent")
	}
	return nil
}

// ---- 6. replay ------------------------------------------------------------------------

func (s *rtBDD) joinedWithItsCode() error {
	s.code = s.allowMe("shed")
	s.enroll(s.shed().url, "--code", s.code)
	return s.succeeded()
}

func (s *rtBDD) anotherForSameKey() error {
	second := s.newMachine()
	second.key = s.me.key
	s.use(second)
	s.enroll(s.shed().url, "--code", s.code)
	return nil
}

func (s *rtBDD) noProofSpent() error {
	_, resp, ok := s.lastExchange(s.shed())
	if !ok || resp.RootProof != "" {
		return fmt.Errorf("the answer carries a proof for the spent code")
	}
	return nil
}

func (s *rtBDD) spentRefused() error { return s.refusedKeepingNothing() }

func (s *rtBDD) saysAlreadyUsed() error { return s.says("already used", "mint a fresh one") }

func (s *rtBDD) codeNotStoredHere() error {
	canon, _ := edgeauth.CanonicalCode(s.code)
	var found string
	_ = filepath.Walk(s.me.dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), canon) || strings.Contains(string(b), s.code) {
				found = p
			}
		}
		return nil
	})
	if found != "" {
		return fmt.Errorf("the code is stored in %s", found)
	}
	return nil
}

func (s *rtBDD) laterNoReuse() error {
	n := s.shed().enrolls.Load()
	s.enroll(s.shed().url)
	if s.shed().enrolls.Load() != n {
		return fmt.Errorf("a later enroll asked the authority again")
	}
	return s.says("nothing to do")
}

func (s *rtBDD) pocketAdoptedClaims() error {
	if err := s.phoneAdoptedShown("pocket"); err != nil {
		return err
	}
	return s.phoneClaims(s.shed().url)
}

func (s *rtBDD) grantAndCodeSpent() error {
	if _, _, ok := s.shed().local.NodeCode(s.phone.id); ok {
		return fmt.Errorf("the code is still live")
	}
	if s.shed().local.ClaimGranted(s.phone.id) {
		return fmt.Errorf("the grant is still live")
	}
	return nil
}

func (s *rtBDD) secondClaimRefused() error {
	n := len(s.phone.held)
	s.phone.err = nil
	if err := s.phoneClaims(s.shed().url); err != nil {
		return err
	}
	if len(s.phone.held) != n || s.phone.err == nil || !strings.Contains(s.phone.err.Error(), "not been adopted") {
		return fmt.Errorf("a second claim was not refused as not adopted: %v", s.phone.err)
	}
	return nil
}

func (s *rtBDD) noSecondProof() error {
	proofs := 0
	for _, h := range s.phone.held {
		if h.resp.RootProof != "" {
			proofs++
		}
	}
	if proofs != 1 {
		return fmt.Errorf("%d proofs were produced", proofs)
	}
	return nil
}

func (s *rtBDD) oneLiveCode() error {
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) twoAtOnce() error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	s.results = nil
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pub, _, _ := ed25519.GenerateKey(rand.Reader)
			req := edgeauth.NewRequest("", "bench", "host", pub, time.Now())
			req.Sign(s.me.key)
			resp, err := enrollhttp.Enroll(context.Background(), s.shed().url, req)
			if err == nil {
				mu.Lock()
				s.results = append(s.results, resp)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return nil
}

func (s *rtBDD) atMostOneProof() error {
	n := 0
	for _, r := range s.results {
		if r.RootProof != "" {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("%d answers carry a proof for one code", n)
	}
	return nil
}

func (s *rtBDD) signedAlreadyAnswered() error {
	s.code = s.allowMe("shed")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	s.req = edgeauth.NewRequest("", "bench", "host", pub, time.Now())
	s.req.Sign(s.me.key)
	_, err := enrollhttp.Enroll(context.Background(), s.shed().url, s.req)
	return err
}

func (s *rtBDD) identicalAgain() error {
	s.resp, s.err = enrollhttp.Enroll(context.Background(), s.shed().url, s.req)
	return nil
}

func (s *rtBDD) refusedAlreadyUsed() error {
	if s.err == nil || !strings.Contains(s.err.Error(), "already been used") {
		return fmt.Errorf("the replay was not refused as already used: %v", s.err)
	}
	return nil
}

func (s *rtBDD) noProofProduced() error {
	if s.resp.RootProof != "" {
		return fmt.Errorf("a proof was produced")
	}
	return nil
}

// ---- 7. expiry -------------------------------------------------------------------------

func (s *rtBDD) mintedSixteenAgo() error  { return s.mintedAgo(16) }
func (s *rtBDD) mintedFourteenAgo() error { return s.mintedAgo(14) }
func (s *rtBDD) mintedTwoAgo() error      { return s.mintedAgo(2) }

func (s *rtBDD) enrollsWithIt() error {
	s.before = s.snapshot()
	return s.enrollsWithThatCode()
}

func (s *rtBDD) answerNoProofForCode() error {
	_, resp, ok := s.lastExchange(s.shed())
	if !ok {
		return fmt.Errorf("no answer was seen")
	}
	if resp.RootProof != "" {
		return fmt.Errorf("the answer carries a proof")
	}
	return nil
}

func (s *rtBDD) saysExpired() error { return s.says("has expired", "allow or adopt") }

func (s *rtBDD) proofVerifiesKept() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	return s.keepsCertAndRoot()
}

func (s *rtBDD) heldSixteenMinutes() error {
	s.code = s.allowMe("shed")
	start, late := time.Now(), false
	var mu sync.Mutex
	edgeNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if late {
			return start.Add(16 * time.Minute)
		}
		return start
	}
	s.startLive("edge", "enroll", "bench", "--authority", s.shed().url)
	if err := s.waitFor("enter the enrollment code"); err != nil {
		return err
	}
	mu.Lock()
	late = true
	mu.Unlock()
	return nil
}

func (s *rtBDD) heldDiscarded() error {
	s.finishLive()
	if err := s.refused(); err != nil {
		return err
	}
	return s.never("does not match")
}

func (s *rtBDD) saysRunAgain() error {
	if err := s.says("run the join again", "nothing was kept"); err != nil {
		return err
	}
	return s.keptNothing()
}

func (s *rtBDD) codeExpiredOnShed() error { return s.mintedAgo(16) }

func (s *rtBDD) notInStore() error {
	raw, _ := os.ReadFile(s.codesPath())
	canon, _ := edgeauth.CanonicalCode(s.code)
	if strings.Contains(string(raw), canon) {
		return fmt.Errorf("the expired code is still in the store")
	}
	return nil
}

func (s *rtBDD) noAnswerProof() error {
	s.enroll(s.shed().url, "--code", s.code)
	if err := s.refused(); err != nil {
		return err
	}
	return s.answerNoProofForCode()
}

func (s *rtBDD) pocketAdoptedTwentyAgo() error {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s.phone = &rtPhone{pub: pub, priv: priv, id: edgeauth.NodeID(pub), where: map[string]string{}}
	at := time.Now().Add(-20 * time.Minute)
	if err := s.shed().local.GrantClaim(s.phone.id, "pocket"); err != nil {
		return err
	}
	s.shed().local.SetNow(func() time.Time { return at })
	c, err := s.shed().local.MintNodeCode(s.phone.id)
	s.shed().local.SetNow(time.Now)
	s.code = c
	return err
}

func (s *rtBDD) codeExpiredGrantLive() error {
	if _, _, ok := s.shed().local.NodeCode(s.phone.id); ok {
		return fmt.Errorf("the code is still live")
	}
	if !s.shed().local.ClaimGranted(s.phone.id) {
		return fmt.Errorf("the adopt grant is not live")
	}
	return nil
}

// panelSaysExpired: the host's adopting hook - the one the [3] panel and the console draw from -
// says there is no live code, which the panel shows as expired with the adopt that renews it.
func (s *rtBDD) panelSaysExpired() error {
	return s.on(s.shedM, func() error {
		if _, _, ok := edgeAdoptCode(s.phone.id); ok {
			return fmt.Errorf("the panel would still show a code")
		}
		return nil
	})
}

func (s *rtBDD) adoptsAgain(name string) error {
	return s.on(s.shedM, func() error { s.run("edge", "adopt", name); return s.succeeded() })
}

func (s *rtBDD) freshCodeShown() error {
	code, _, ok := s.shed().local.NodeCode(s.phone.id)
	if !ok || code == s.code {
		return fmt.Errorf("no fresh code")
	}
	return s.says(edgeauth.FormatCode(code), "the old one is void")
}

func (s *rtBDD) clockDayBehind() error {
	behind := time.Now().Add(-24 * time.Hour)
	s.shed().local.SetNow(func() time.Time { return behind })
	return nil
}

func (s *rtBDD) enrollsWithSkewedCode() error {
	s.code = s.allowMe("shed")
	s.before = s.snapshot()
	s.enroll(s.shed().url, "--code", s.code)
	return nil
}

// codeLifeOnAuthorityClock: the code was stamped with the authority's own clock, and a join the
// skewed authority cannot place in time is refused - never accepted on the strength of the skew.
func (s *rtBDD) codeLifeOnAuthorityClock() error {
	raw, _ := os.ReadFile(s.codesPath())
	var got []struct {
		At int64 `json:"at"`
	}
	_ = json.Unmarshal(raw, &got)
	if len(got) != 1 || time.Since(time.Unix(got[0].At, 0)) < 23*time.Hour {
		return fmt.Errorf("the code was not stamped on the authority's clock: %s", raw)
	}
	if err := s.refused(); err != nil {
		return err
	}
	return s.unchanged()
}

// joinerWindowOwnClock: the joiner's window is measured by the joiner's clock alone.
func (s *rtBDD) joinerWindowOwnClock() error {
	s.shed().local.SetNow(time.Now)
	return s.heldSixteenMinutesThenDiscard()
}

func (s *rtBDD) heldSixteenMinutesThenDiscard() error {
	if err := s.heldSixteenMinutes(); err != nil {
		return err
	}
	s.typeLine(s.code)
	return s.heldDiscarded()
}

// ---- 8. brute force -------------------------------------------------------------------

type goldenVectors struct {
	Domain     string `json:"domain"`
	Iterations int    `json:"iterations"`
	Proofs     []struct {
		Code, RootFP, Account, NodeID, Nonce, Key, Proof string
	} `json:"proofs"`
}

func (s *rtBDD) golden() (goldenVectors, error) {
	var v goldenVectors
	raw, err := os.ReadFile("../../internal/edgeauth/testdata/root_proof_vectors.json")
	if err != nil {
		return v, err
	}
	var parsed struct {
		Domain     string `json:"domain"`
		Iterations int    `json:"iterations"`
		Proofs     []struct {
			Code    string `json:"code"`
			RootFP  string `json:"root_fp"`
			Account string `json:"account"`
			NodeID  string `json:"node_id"`
			Nonce   string `json:"nonce"`
			Key     string `json:"key"`
			Proof   string `json:"proof"`
		} `json:"proofs"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return v, err
	}
	v.Domain, v.Iterations = parsed.Domain, parsed.Iterations
	for _, p := range parsed.Proofs {
		v.Proofs = append(v.Proofs, struct{ Code, RootFP, Account, NodeID, Nonce, Key, Proof string }{
			p.Code, p.RootFP, p.Account, p.NodeID, p.Nonce, p.Key, p.Proof})
	}
	return v, nil
}

func (s *rtBDD) keyIsPBKDF2() error {
	v, err := s.golden()
	if err != nil {
		return err
	}
	for _, p := range v.Proofs {
		k, _ := pbkdf2.Key(sha256.New, p.Code, []byte(edgeauth.ProofDomain+"\n"+p.Nonce), edgeauth.ProofIterations, 32)
		if hex.EncodeToString(k) != p.Key {
			return fmt.Errorf("the proof key for %s is not PBKDF2-HMAC-SHA256 of the code", p.Code)
		}
		m := hmac.New(sha256.New, k)
		m.Write([]byte(strings.Join([]string{edgeauth.ProofDomain, p.RootFP, p.Account, p.NodeID, p.Nonce}, "\n")))
		if "v1:"+hex.EncodeToString(m.Sum(nil)) != p.Proof || edgeauth.RootProof(p.Code, p.RootFP, p.Account, p.NodeID, p.Nonce) != p.Proof {
			return fmt.Errorf("the proof for %s is not the HMAC under that key", p.Code)
		}
	}
	return nil
}

func (s *rtBDD) saltIsDomainNonce() error {
	v, _ := s.golden()
	if v.Domain != edgeauth.ProofDomain {
		return fmt.Errorf("the domain differs")
	}
	return nil
}

func (s *rtBDD) iterationsAndLength() error {
	v, _ := s.golden()
	if edgeauth.ProofIterations != 200000 || v.Iterations != 200000 || len(v.Proofs[0].Key) != 64 {
		return fmt.Errorf("not 200000 iterations and a 32-byte key")
	}
	return nil
}

func (s *rtBDD) unrelatedKeys() error {
	a, _ := pbkdf2.Key(sha256.New, "7KQ2M9XH4P", []byte(edgeauth.ProofDomain+"\nnonce-one"), edgeauth.ProofIterations, 32)
	b, _ := pbkdf2.Key(sha256.New, "7KQ2M9XH4P", []byte(edgeauth.ProofDomain+"\nnonce-two"), edgeauth.ProofIterations, 32)
	if bytes.Equal(a, b) {
		return fmt.Errorf("two nonces gave one key")
	}
	return nil
}

func (s *rtBDD) tenOfThirtyTwo() error {
	if edgeauth.CodeLen != 10 {
		return fmt.Errorf("a code is %d symbols", edgeauth.CodeLen)
	}
	return s.tenThousandCodes()
}

func (s *rtBDD) drawnUniformly() error {
	if err := s.everySymbolCrockford(); err != nil {
		return err
	}
	return s.uniform()
}

func (s *rtBDD) interactiveHoldingAnswer() error { return s.interactiveHolding() }

func (s *rtBDD) entersFiveWrong() error {
	for i := 0; i < 5; i++ {
		s.typeLine(fmt.Sprintf("ZZZZZ-ZZZZ%d", i))
	}
	s.typeLine("ZZZZZ-ZZZZ9") // a sixth, which must never be checked
	s.finishLive()
	return nil
}

func (s *rtBDD) heldDiscardedNothingKept() error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.keptNothing()
}

func (s *rtBDD) saysCapImpostor() error {
	return s.says("did not match 5 times", "impersonating the authority", "mint a fresh code")
}

func (s *rtBDD) sixthNotChecked() error {
	if n := strings.Count(s.out, "enter the enrollment code"); n != 5 {
		return fmt.Errorf("asked %d times", n)
	}
	return nil
}

func (s *rtBDD) runsCodeThatFails() error {
	s.allowMe("shed")
	s.before = s.snapshot()
	s.enroll(s.shed().url, "--code", "ZZZZZ-ZZZZZ")
	return nil
}

func (s *rtBDD) checkedOnce() error {
	if n := strings.Count(s.out, "does not match the Edge that answered"); n != 1 || s.shed().enrolls.Load() != 1 {
		return fmt.Errorf("checked %d times, asked %d times", n, s.shed().enrolls.Load())
	}
	return nil
}

func (s *rtBDD) exitsNonZeroKeepingNothing() error {
	if err := s.exitsNonZero(); err != nil {
		return err
	}
	return s.keptNothing()
}

func (s *rtBDD) noRequestFieldCarriesCode() error {
	for _, typ := range []reflect.Type{reflect.TypeOf(edgeauth.Request{}), reflect.TypeOf(edgeauth.ClaimRequest{})} {
		for i := 0; i < typ.NumField(); i++ {
			n := strings.ToLower(typ.Field(i).Name)
			if strings.Contains(n, "code") || strings.Contains(n, "proof") {
				return fmt.Errorf("%s carries a field %s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
	return nil
}

// neverCompares: a caller sending the right code, or a wrong one, in its request changes nothing
// about the answer - the authority does not read it.
func (s *rtBDD) neverCompares() error {
	s.code = s.allowMe("shed")
	for _, sent := range []string{s.code, "ZZZZZ-ZZZZZ"} {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		req := edgeauth.NewRequest("", "bench", "host", pub, time.Now())
		req.Sign(s.me.key)
		raw, _ := json.Marshal(req)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["code"] = sent
		body, _ := json.Marshal(m)
		resp, err := http.Post(s.shed().url+enrollhttp.Path, "application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("a request carrying a %q code was answered %d", sent, resp.StatusCode)
		}
	}
	return nil
}

// nothingToRateLimit: what a caller sends never decides whether a proof comes back - the first of
// the two requests above carried the code's proof whatever "code" it sent, and the second (the code
// now spent) carried none, again whatever it sent. There is nothing at the authority to guess at.
func (s *rtBDD) nothingToRateLimit() error {
	_, resp, ok := s.lastExchange(s.shed())
	if !ok || resp.RootProof != "" {
		return fmt.Errorf("a request's own content decided a proof")
	}
	return nil
}

func (s *rtBDD) authorizedAnswered() error {
	s.results = nil
	for i := 0; i < 3; i++ {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		req := edgeauth.NewRequest("", "bench", "host", pub, time.Now())
		req.Sign(s.me.key)
		resp, err := enrollhttp.Enroll(context.Background(), s.shed().url, req)
		if err != nil {
			return err
		}
		s.results = append(s.results, resp)
	}
	return nil
}

func (s *rtBDD) onlyIssuingCarries() error {
	if s.results[0].RootProof == "" {
		return fmt.Errorf("the issuing answer carries no proof")
	}
	for _, r := range s.results[1:] {
		if r.RootProof != "" {
			return fmt.Errorf("a later answer carries a proof for the same code")
		}
	}
	return nil
}

func (s *rtBDD) unauthorizedNever() error {
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest("", "bench", "host", pub, time.Now())
	req.Sign(stranger)
	body, _ := json.Marshal(req)
	resp, err := http.Post(s.shed().url+enrollhttp.Path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK || strings.Contains(string(b), "root_proof") {
		return fmt.Errorf("an unauthorized request got %d %s", resp.StatusCode, b)
	}
	s.out = string(b)
	return nil
}

func (s *rtBDD) liveCodeForAllowed() error { return s.oneLiveCode() }
func (s *rtBDD) strangerSends() error      { return s.unauthorizedNever() }

func (s *rtBDD) refusedAsToday() error {
	if s.out != edgeauth.ErrUnknownMachine.Error()+"\n" {
		return fmt.Errorf("the refusal is not the plain unknown-machine answer: %q", s.out)
	}
	return nil
}

func (s *rtBDD) byteIdenticalRefusal() error {
	with := s.out
	fresh := newRootedAuthority(s.t, "fresh")
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest("", "bench", "host", pub, time.Now())
	req.Sign(stranger)
	body, _ := json.Marshal(req)
	resp, err := http.Post(fresh.url+enrollhttp.Path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != with {
		return fmt.Errorf("the refusal differs with a live code:\n%q\n%q", with, b)
	}
	return nil
}

// ---- 9. a root that changes -------------------------------------------------------------

func (s *rtBDD) reEnrollsDifferentRoot() error {
	s.authority("barn").allow(s.t, s.me.key)
	s.before = s.snapshot()
	s.enroll(s.authority("barn").url)
	return nil
}

func (s *rtBDD) refusedDifferentAuthority() error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.says("that certificate comes from a different authority")
}

func (s *rtBDD) identityRootRowUnchanged() error { return s.unchanged() }

func (s *rtBDD) memberNotAuthority() error {
	if _, ok, _ := edgeauth.OpenLocal(edgeAuthDir()); ok {
		return fmt.Errorf("this machine is an authority")
	}
	return s.enrolledUnderShed()
}

func (s *rtBDD) reEnrollRefusedDifferent() error { return s.reEnrollsDifferentRoot() }

func (s *rtBDD) saysRootedElsewhere() error {
	return s.says("this Edge is rooted at", "different authority")
}

func (s *rtBDD) saysAllowAdoptReplace() error {
	return s.says("if the owner moved the Edge there on purpose, its allow or adopt prints a code", "--replace-root")
}

func (s *rtBDD) noForceForMember() error { return s.never("authority local") }

func (s *rtBDD) edgeMoved() error {
	s.authority("barn")
	return s.enrolledUnderShed()
}

func (s *rtBDD) markedReenroll() error {
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	if _, err := st.fleet.RequireReenrollment("the Edge authority moved from shed to barn"); err != nil {
		return err
	}
	if err := st.save(); err != nil {
		return err
	}
	if s.heldFP() != s.shed().fingerprint() {
		return fmt.Errorf("it does not hold shed's root")
	}
	return nil
}

func (s *rtBDD) barnAllowedCode() error {
	s.codes["barn"] = s.authority("barn").allow(s.t, s.me.key)
	return nil
}

func (s *rtBDD) runsReplaceLive() error {
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	s.extra = []string{id.NodeID}
	s.startLive("edge", "enroll", "bench", "--authority", s.authority("barn").url, "--code", s.codes["barn"], "--replace-root")
	return nil
}

func (s *rtBDD) showsBothAsks() error {
	if err := s.waitFor("[y/N]"); err != nil {
		return err
	}
	t := s.live.text()
	if !strings.Contains(t, s.shortOf("shed")) || !strings.Contains(t, s.shortOf("barn")) {
		return fmt.Errorf("the prompt does not show both roots:\n%s", t)
	}
	return nil
}

func (s *rtBDD) answersYes() error { s.typeLine("y"); s.finishLive(); return nil }
func (s *rtBDD) answersNo() error  { s.typeLine("n"); s.finishLive(); return nil }

func (s *rtBDD) replacedInOneStep() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	if id.NodeID == s.extra[0] || s.heldFP() != s.authority("barn").fingerprint() {
		return fmt.Errorf("the identity and root were not replaced")
	}
	return nil
}

func (s *rtBDD) memberUnderBarn() error {
	d := edgeDescriptor()
	if d.Kind != edgeauth.KindLocal || d.Where != strings.TrimPrefix(s.authority("barn").url, "http://") {
		return fmt.Errorf("this machine is rooted at %+v", d)
	}
	st, _ := loadEdgeState()
	list, _ := st.fleet.List()
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	for _, n := range list {
		if n.ID == id.NodeID && n.Presence == string(edge.PresenceVerified) && n.Name == "bench" {
			return nil
		}
	}
	return fmt.Errorf("this machine is not a verified member under barn: %+v", list)
}

func (s *rtBDD) noLongerShedRoot() error {
	auth, _, err := edgeIdentityStore().Trust(time.Now())
	if err != nil {
		return err
	}
	if edgeauth.RootFingerprint(auth.Root()) != s.authority("barn").fingerprint() {
		return fmt.Errorf("it still verifies against another root")
	}
	return nil
}

func (s *rtBDD) reEnrollDifferentVerified() error {
	if err := s.enrolledUnderShed(); err != nil {
		return err
	}
	s.codes["barn"] = s.authority("barn").allow(s.t, s.me.key)
	s.startLive("edge", "enroll", "bench", "--authority", s.authority("barn").url, "--code", s.codes["barn"], "--replace-root")
	return s.waitFor("[y/N]")
}

func (s *rtBDD) nothingChangesOld() error {
	if err := s.says("left alone"); err != nil {
		return err
	}
	return s.unchanged()
}

func (s *rtBDD) holdsShedRoot() error { return s.enrolledUnderShed() }

func (s *rtBDD) replaceNoCode() error {
	s.authority("barn").allow(s.t, s.me.key)
	s.enroll(s.authority("barn").url, "--replace-root")
	return nil
}

func (s *rtBDD) refusedBeforeKept() error {
	if s.exit != 2 {
		return fmt.Errorf("not refused before anything was asked (exit %d):\n%s", s.exit, s.out)
	}
	return s.unchanged()
}

func (s *rtBDD) saysNeedsCode() error { return s.says("a new root needs the code from its authority") }

func (s *rtBDD) replaceWrongCode() error {
	s.authority("barn").allow(s.t, s.me.key)
	s.enroll(s.authority("barn").url, "--replace-root", "--code", "ZZZZZ-ZZZZZ", "--yes")
	return nil
}

func (s *rtBDD) oldUnchanged() error { return s.unchanged() }

func (s *rtBDD) replaceNoYes() error {
	if err := s.enrolledUnderShed(); err != nil {
		return err
	}
	edgeIsInteractive = func() bool { return false }
	code := s.authority("barn").allow(s.t, s.me.key)
	s.enroll(s.authority("barn").url, "--replace-root", "--code", code)
	return nil
}

func (s *rtBDD) refusedMustConfirm() error {
	if err := s.refused(); err != nil {
		return err
	}
	if err := s.says("a root change must be confirmed"); err != nil {
		return err
	}
	return s.unchanged()
}

func (s *rtBDD) replaceAgainstShed() error {
	s.enroll(s.shed().url, "--replace-root")
	return nil
}

func (s *rtBDD) saysUnchanged() error {
	if err := s.says("the root is unchanged", "nothing is replaced"); err != nil {
		return err
	}
	return s.unchanged()
}

func (s *rtBDD) forgottenKeptRoot() error {
	if err := s.enrolledUnderShed(); err != nil {
		return err
	}
	id, _, _, _ := edgeIdentityStore().LoadIdentity()
	if _, _, err := s.shed().local.Forget(id.NodeID); err != nil {
		return err
	}
	// The machine leaves too, the way an owner does it: `roger edge forget` on itself gives up its
	// identity and its row. The Edge's public root stays on disk.
	s.run("edge", "forget", "bench", "--yes")
	if err := s.succeeded(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(edgeAuthDir(), edgeauth.RootCertFile)); err != nil {
		return fmt.Errorf("the root did not stay: %v", err)
	}
	return nil
}

func (s *rtBDD) ownerReadmitted() error {
	s.allowMe("shed") // the enroll path's re-admit: the owner allows the machine's key again
	return nil
}

func (s *rtBDD) enrollsNoCode() error {
	s.enroll(s.shed().url)
	return nil
}

func (s *rtBDD) acceptedHeldRoot() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	return s.keepsCertAndRoot()
}

func (s *rtBDD) comesBackAsReadmit() error {
	st, _ := loadEdgeState()
	list, _ := st.fleet.List()
	for _, n := range list {
		if n.Name == "bench" && n.Presence == string(edge.PresenceVerified) {
			return nil
		}
	}
	return fmt.Errorf("it did not come back named bench: %+v", list)
}

func (s *rtBDD) pocketHoldsShedRoot() error {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s.phone = &rtPhone{pub: pub, priv: priv, id: edgeauth.NodeID(pub), where: map[string]string{}}
	s.phone.root = s.shed().local.Authority().Root()
	return nil
}

func (s *rtBDD) claimDifferentRootNoCode() error {
	imp := s.authority("impostor")
	if err := imp.local.GrantClaim(s.phone.id, "pocket"); err != nil {
		return err
	}
	if err := s.phoneClaims(imp.url); err != nil {
		return err
	}
	s.code = ""
	return s.phoneEntersCode()
}

func (s *rtBDD) refusedKeepsOldRoot() error {
	if s.phone.kept != nil || s.phone.err == nil || !errors.Is(s.phone.err, edgeauth.ErrDifferentAuthority) {
		return fmt.Errorf("the phone did not refuse a different root: %v", s.phone.err)
	}
	if !s.phone.root.Equal(s.shed().local.Authority().Root()) {
		return fmt.Errorf("the phone's root changed")
	}
	return nil
}

func (s *rtBDD) readmittedRootBefore(name string) error {
	return s.pocketHoldsShedRoot()
}

func (s *rtBDD) readmittedClaimsNoCode() error {
	if err := s.shed().local.GrantClaim(s.phone.id, "pocket"); err != nil {
		return err
	}
	if err := s.phoneClaims(s.shed().url); err != nil {
		return err
	}
	s.code = ""
	return s.phoneEntersCode()
}

// ---- 11. downgrade ---------------------------------------------------------------------

func (s *rtBDD) shedFromBefore() error {
	s.shed().strip.Store(true)
	return nil
}

func (s *rtBDD) enrollsWithACode() error {
	s.code = s.allowMe("shed")
	s.before = s.snapshot()
	s.enroll(s.target, "--code", s.code)
	return nil
}

func (s *rtBDD) answerCarriesNoProof() error {
	if err := s.refused(); err != nil {
		return err
	}
	if strings.Contains(s.shed().wireLog(), "root_proof") {
		return fmt.Errorf("the answer carried a proof")
	}
	return nil
}

func (s *rtBDD) saysUpdateOrPin() error {
	return s.says("does not prove its root", "update roger on it", "--root", "`roger edge authority` prints there")
}

func (s *rtBDD) enrollsWithShedPin() error {
	s.allowMe("shed")
	s.enroll(s.target, "--root", s.shed().fingerprint())
	return nil
}

func (s *rtBDD) pinMatchedKept() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	return s.keepsCertAndRoot()
}

func (s *rtBDD) shedSendsProofs() error { s.shed().strip.Store(false); return nil }

func (s *rtBDD) strippedInBetween() error {
	s.relayTo(s.shed().url, func(m map[string]any) {
		delete(m, "root_proof")
		m["root_proof_status"] = "proved" // a lie, and a header saying to skip: neither is believed
		m["version"] = "0"
	})
	return nil
}

func (s *rtBDD) refusedAsNone() error {
	if err := s.refusedKeepingNothing(); err != nil {
		return err
	}
	return s.says("carried no proof")
}

func (s *rtBDD) nothingSkips() error { return s.keptNothing() }

func (s *rtBDD) helpListsFlags() error {
	s.run("edge", "enroll", "--help")
	return s.says("--code", "--root", "--invite", "--replace-root", "--yes")
}

func (s *rtBDD) noEscape() error {
	for name := range edgeFlags {
		for _, bad := range []string{"trust", "insecure", "tofu", "skip", "unverified"} {
			if strings.Contains(name, bad) {
				return fmt.Errorf("a flag %q exists", name)
			}
		}
	}
	for _, env := range []string{"ROGERAI_EDGE_TRUST_FIRST_ROOT", "ROGERAI_EDGE_INSECURE", "ROGERAI_EDGE_SKIP_ROOT_CHECK", "ROGER_EDGE_TOFU"} {
		_ = os.Setenv(env, "1")
	}
	defer func() {
		for _, env := range []string{"ROGERAI_EDGE_TRUST_FIRST_ROOT", "ROGERAI_EDGE_INSECURE", "ROGERAI_EDGE_SKIP_ROOT_CHECK", "ROGER_EDGE_TOFU"} {
			_ = os.Unsetenv(env)
		}
	}()
	s.allowMe("shed")
	s.enroll(s.shed().url)
	if s.exit != 2 {
		return fmt.Errorf("an environment variable let a first enrollment through:\n%s", s.out)
	}
	return s.keptNothing()
}

func (s *rtBDD) unknownVersion() error {
	s.shed().setMangle(func(resp *edgeauth.Response, _ []byte) {
		resp.RootProof = "v9:" + strings.TrimPrefix(resp.RootProof, "v1:")
	})
	s.code = s.allowMe("shed")
	s.target = s.shed().url
	return nil
}

func (s *rtBDD) machineChecksIt() error { return s.checksTheAnswer() }

func (s *rtBDD) saysNewerUpdate() error {
	if err := s.refusedKeepingNothing(); err != nil {
		return err
	}
	return s.says("newer", "update roger here", "--root")
}

// ---- 12. mixed versions -------------------------------------------------------------------

func (s *rtBDD) allowsOrAdopts() error {
	if err := s.allowOnShed(); err != nil {
		return err
	}
	allowOut := s.out
	if err := s.phoneCandidate("pocket"); err != nil {
		return err
	}
	if err := s.runsAdoptOnShed("pocket"); err != nil {
		return err
	}
	s.out = allowOut + "\n" + s.out
	return nil
}

func (s *rtBDD) warnsOldJoiner() error {
	if strings.Count(s.out, "before root proofs") < 2 {
		return fmt.Errorf("allow and adopt do not both warn:\n%s", s.out)
	}
	return nil
}

func (s *rtBDD) saysUpdateFirst() error { return s.says("before it joins") }

// oldJoin is a joiner from before root proofs: it signs, asks, and keeps whatever root came back.
func (s *rtBDD) oldJoin(url string) (map[string]any, *edgeauth.Identity, error) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := edgeauth.NewRequest("", "old", "host", pub, time.Now())
	req.Sign(s.me.key)
	body, _ := json.Marshal(req)
	resp, err := http.Post(url+enrollhttp.Path, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	var old struct {
		NodeID  string `json:"node_id"`
		Account string `json:"account"`
		Cert    string `json:"cert"`
		Root    string `json:"root"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		return m, nil, err
	}
	id, err := edgeauth.CheckResponse(edgeauth.Response{NodeID: old.NodeID, Account: old.Account, Cert: old.Cert, Root: old.Root},
		edgeauth.Expect{NodeID: req.NodeID, Key: pub, Now: time.Now()})
	return m, id, err
}

func (s *rtBDD) oldJoiner() error { s.allowMe("shed"); return nil }

func (s *rtBDD) enrollsAgainstNewShed() error {
	m, id, err := s.oldJoin(s.shed().url)
	if err != nil {
		return err
	}
	if id == nil {
		return fmt.Errorf("the old joiner got nothing")
	}
	s.extra = nil
	for k := range m {
		s.extra = append(s.extra, k)
	}
	return nil
}

func (s *rtBDD) answeredAsBefore() error {
	has := map[string]bool{}
	for _, k := range s.extra {
		has[k] = true
	}
	if !has["root_proof"] {
		return fmt.Errorf("the new authority sent no proof for its live code")
	}
	return nil
}

func (s *rtBDD) byteCompatible() error {
	allowed := map[string]bool{"node_id": true, "account": true, "cert": true, "root": true, "root_proof": true, "root_proof_status": true}
	for _, k := range s.extra {
		if !allowed[k] {
			return fmt.Errorf("the answer has a key %q an old joiner does not know", k)
		}
	}
	return nil
}

func (s *rtBDD) newMemberHoldsShedRoot() error { return s.enrolledUnderShed() }
func (s *rtBDD) renews() error {
	if err := s.closeToExpiry(); err != nil {
		return err
	}
	return s.renewsWithNoCode()
}
func (s *rtBDD) renewalRootSame() error { return s.renewalAccepted() }

func (s *rtBDD) membersBeforeProofs() error {
	// A member that joined before proofs holds a root it took on trust: the same end state a pin
	// join leaves.
	s.allowMe("shed")
	s.enroll(s.shed().url, "--root", s.shed().fingerprint())
	return s.succeeded()
}

func (s *rtBDD) updateAnyOrder() error {
	s.shed().strip.Store(true)
	s.enroll(s.shed().url)
	if err := s.succeeded(); err != nil {
		return err
	}
	s.shed().strip.Store(false)
	s.enroll(s.shed().url)
	return s.succeeded()
}

func (s *rtBDD) stillVerifiesHeld() error { return s.keepsCertAndRoot() }

func (s *rtBDD) noReenrollTold() error {
	if err := s.never("re-enroll"); err != nil {
		return err
	}
	st, _ := loadEdgeState()
	list, _ := st.fleet.List()
	for _, n := range list {
		if n.Presence == string(edge.PresenceReenroll) {
			return fmt.Errorf("a member is marked to re-enroll")
		}
	}
	return nil
}

func (s *rtBDD) pairingVersions(joiner, authority string) error {
	s.pairing.joiner, s.pairing.authority = joiner, authority
	s.shed().strip.Store(authority == "old")
	return nil
}

func (s *rtBDD) pairingEnrolls(proof string) error {
	s.pairing.proof = proof
	s.before = s.snapshot()
	if s.pairing.joiner == "old" {
		s.allowMe("shed")
		_, id, err := s.oldJoin(s.shed().url)
		s.err = err
		if id == nil && err == nil {
			s.err = errors.New("nothing kept")
		}
		return nil
	}
	switch proof {
	case "a code":
		s.enroll(s.shed().url, "--code", s.allowMe("shed"))
	case "the pin":
		s.allowMe("shed")
		s.enroll(s.shed().url, "--root", s.shed().fingerprint())
	default:
		s.allowMe("shed")
		edgeIsInteractive = func() bool { return false }
		s.enroll(s.shed().url)
	}
	return nil
}

func (s *rtBDD) theResultIs(result string) error {
	switch {
	case strings.HasPrefix(result, "kept, proof verified"):
		if err := s.succeeded(); err != nil {
			return err
		}
		return s.says("the code is what was checked")
	case strings.HasPrefix(result, "kept, pin matched"):
		if err := s.succeeded(); err != nil {
			return err
		}
		return s.says("it matched the full fingerprint you gave")
	case strings.HasPrefix(result, "refused before sending"):
		if s.exit != 2 {
			return fmt.Errorf("not refused before sending (exit %d):\n%s", s.exit, s.out)
		}
		return s.keptNothing()
	case strings.HasPrefix(result, "refused: the authority sends no proof"):
		if err := s.refusedKeepingNothing(); err != nil {
			return err
		}
		return s.says("does not prove its root")
	case strings.HasPrefix(result, "kept unchecked, as before"):
		if s.err != nil {
			return fmt.Errorf("an old joiner was refused: %v", s.err)
		}
		if strings.Contains(result, "warned at allow") {
			if err := s.allowOnShed(); err != nil {
				return err
			}
			return s.says("before root proofs")
		}
		return nil
	case strings.HasPrefix(result, "enrolled"):
		if err := s.succeeded(); err != nil {
			return err
		}
		if s.heldFP() == "" {
			return fmt.Errorf("nothing was kept")
		}
		return nil
	case strings.HasPrefix(result, "refused before sending: Core enrollment needs https"):
		if err := s.refused(); err != nil {
			return err
		}
		if err := s.says("Core enrollment needs an https broker", "nothing was sent"); err != nil {
			return err
		}
		return s.keptNothing()
	}
	return fmt.Errorf("unknown result %q", result)
}

// ---- 13. Core ------------------------------------------------------------------------------

// coreTLS stands Core's enrollment up: the REAL issuer and handler, over REAL TLS, knowing this
// machine's user key as the account "owner" - and trusted by the client the way a public CA is.
func (s *rtBDD) coreTLS() *httptest.Server {
	ca, _ := cert.NewAuthority(cert.Config{TTL: time.Hour})
	pub := s.keyHex(s.me)
	iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: ca, Accounts: func(k string) (string, bool) {
		return "owner", k == pub
	}})
	srv := httptest.NewTLSServer(enrollhttp.Handler(iss))
	s.t.Cleanup(srv.Close)
	enrollhttp.Client = srv.Client()
	return srv
}

func (s *rtBDD) login() {
	dir := filepath.Dir(configPath())
	_ = os.MkdirAll(dir, 0o700)
	b, _ := json.Marshal(map[string]any{"github_login": "owner", "github_id": 42, "bound_at": time.Now().Unix()})
	_ = os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600)
}

func (s *rtBDD) coreHTTPS() error {
	s.coreSrv = s.coreTLS()
	_ = os.Setenv("ROGER_BROKER", s.coreSrv.URL)
	s.login()
	return nil
}

func (s *rtBDD) thisMachineEnrolls() error {
	s.before = s.snapshot()
	s.run("edge", "enroll", "bench")
	return nil
}

func (s *rtBDD) acceptedOnTLS() error {
	if err := s.succeeded(); err != nil {
		return err
	}
	if s.heldFP() == "" {
		return fmt.Errorf("no root was kept")
	}
	return nil
}

func (s *rtBDD) coreAuthority() error {
	s.coreSrv = s.coreTLS()
	s.login()
	return nil
}

func (s *rtBDD) interceptsBroker() error {
	imp := impostorTLS(s.t, s.coreSrv.Config.Handler)
	_ = os.Setenv("ROGER_BROKER", imp.URL)
	return nil
}

func (s *rtBDD) tlsDoesNotVerify() error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.says("certificate")
}

func (s *rtBDD) failsConnectivity() error {
	if err := s.says("could not reach Core", "nothing was enrolled"); err != nil {
		return err
	}
	return s.keptNothing()
}

func (s *rtBDD) brokerAddressIs(broker string) error {
	s.login()
	u := broker
	switch broker {
	case "https://broker.rogerai.fm":
		u = s.coreTLS().URL // a stand-in on this machine, authenticated the same way
	case "http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080":
		host := strings.TrimSuffix(strings.TrimPrefix(broker, "http://"), ":8080")
		ln, err := net.Listen("tcp", net.JoinHostPort(strings.Trim(host, "[]"), "0"))
		if err != nil && host == "localhost" {
			ln, err = net.Listen("tcp", "127.0.0.1:0")
		}
		if err != nil {
			return fmt.Errorf("cannot listen on %s: %v", host, err)
		}
		ca, _ := cert.NewAuthority(cert.Config{TTL: time.Hour})
		pub := s.keyHex(s.me)
		iss := edgeauth.NewIssuer(edgeauth.IssuerConfig{Authority: ca, Accounts: func(k string) (string, bool) {
			return "owner", k == pub
		}})
		srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: enrollhttp.Handler(iss)}}
		srv.Start()
		s.t.Cleanup(srv.Close)
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		u = "http://" + net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	_ = os.Setenv("ROGER_BROKER", u)
	return nil
}

func (s *rtBDD) enrollsUnderCore() error { return s.thisMachineEnrolls() }

func (s *rtBDD) plainHTTPOnLAN() error {
	s.login()
	_ = os.Setenv("ROGER_BROKER", "http://192.168.1.5:8080")
	return nil
}

func (s *rtBDD) saysNeedsHTTPS() error { return s.says("Core enrollment needs an https broker") }
func (s *rtBDD) saysLocalAuthorityInstead() error {
	return s.says("use a local authority with a code")
}

// impostorTLS answers with a certificate of its own for 127.0.0.1, which no trusted root vouches for.
func impostorTLS(t *testing.T, h http.Handler) *httptest.Server {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "broker"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// ---- 14. roger edge setup -------------------------------------------------------------------

func (s *rtBDD) ownerAllowed() error {
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) setupJoinsBench() error {
	s.startLive("edge", "setup")
	s.typeLine("j")
	s.typeLine("bench")
	s.typeLine(s.shed().url)
	return nil
}

func (s *rtBDD) setupAsksCode() error {
	return s.waitFor("enter the enrollment code shown on the authority")
}

func (s *rtBDD) setupRightCode() error {
	s.typeLine(s.code)
	s.finishLive()
	if err := s.succeeded(); err != nil {
		return err
	}
	return s.says("Joined", s.shortOf("shed"))
}

func (s *rtBDD) notYetAllowed() error { return nil }

func (s *rtBDD) setupChoosesJoin() error {
	s.setupJoinsBench()
	s.finishLive()
	return nil
}

func (s *rtBDD) printsKeyAndAllow() error {
	return s.says("roger edge authority allow " + s.keyHex(s.me))
}

func (s *rtBDD) saysAllowPrintsCode() error { return s.says("prints the code to bring back here") }

// ---- 15. never half-applied --------------------------------------------------------------------

func (s *rtBDD) firstRefusedBecause(reason string) error {
	switch reason {
	case "no code and no pin":
		s.before = s.snapshot()
		return s.enrollsWithNeither()
	case "the proof did not verify":
		return s.runsNeverMinted()
	case "the answer carried no proof":
		s.shed().strip.Store(true)
		err := s.enrollsWithACode()
		s.shed().strip.Store(false)
		return err
	case "the pin did not match":
		s.allowMe("shed")
		s.before = s.snapshot()
		s.enroll(s.shed().url, "--root", s.authority("other").fingerprint())
		return nil
	case "the code was spent":
		code := s.allowMe("shed")
		other := s.newMachine()
		other.key = s.me.key
		if err := s.on(other, func() error { s.enroll(s.shed().url, "--code", code); return s.succeeded() }); err != nil {
			return err
		}
		s.before = s.snapshot()
		s.enroll(s.shed().url, "--code", code)
		return nil
	case "the code had expired":
		if err := s.mintedAgo(16); err != nil {
			return err
		}
		return s.enrollsWithIt()
	case "the held answer outlived its window":
		s.before = s.snapshot()
		return s.heldSixteenMinutesThenDiscard()
	case "the wrong-code cap was reached":
		s.before = s.snapshot()
		if err := s.interactiveHolding(); err != nil {
			return err
		}
		return s.entersFiveWrong()
	case "the proof version is unknown":
		if err := s.unknownVersion(); err != nil {
			return err
		}
		err := s.checksTheAnswer()
		s.shed().setMangle(nil)
		return err
	case "the owner declined a root change":
		if err := s.reEnrollDifferentVerified(); err != nil {
			return err
		}
		return s.answersNo()
	}
	return fmt.Errorf("unknown reason %q", reason)
}

// nothingRemains: nothing from the refused answer was kept - the machine's Edge files are
// exactly what they were before the attempt (for a first enrollment, nothing at all).
func (s *rtBDD) nothingRemains() error {
	if s.err == nil && !strings.Contains(s.out, "left alone") {
		return fmt.Errorf("it was not refused:\n%s", s.out)
	}
	return s.unchanged()
}

func (s *rtBDD) retryIsFirst() error {
	if s.heldFP() != "" {
		// The declined root change: trying the move again, deliberately, works as the first time.
		s.enroll(s.authority("barn").url, "--code", s.authority("barn").allow(s.t, s.me.key), "--replace-root", "--yes")
		if err := s.succeeded(); err != nil {
			return err
		}
		if s.heldFP() != s.authority("barn").fingerprint() {
			return fmt.Errorf("the retried move did not take")
		}
		return nil
	}
	s.shed().setMangle(nil)
	s.shed().strip.Store(false)
	edgeNow = time.Now
	s.enroll(s.shed().url, "--code", s.allowMe("shed"))
	return s.succeeded()
}

func (s *rtBDD) answerFromUnverified() error {
	if err := s.impostorOwnRoot(); err != nil {
		return err
	}
	s.code = s.allowMe("shed")
	return nil
}

func (s *rtBDD) nothingFromItUsed() error {
	if err := s.keptNothing(); err != nil {
		return err
	}
	if d, ok, _ := edgeIdentityStore().Descriptor(); ok && d.Kind == edgeauth.KindLocal {
		return fmt.Errorf("the impostor's address was stored as this Edge's authority")
	}
	if strings.Contains(s.authority("impostor").wireLog(), "revocations") {
		return fmt.Errorf("a revocation list was fetched from the impostor")
	}
	if err := s.on(s.me, func() error {
		_, err := os.Stat(filepath.Join(edgeAuthDir(), edgeauth.TrustFile))
		if !os.IsNotExist(err) {
			return fmt.Errorf("a revocation list was saved")
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *rtBDD) revocationsAfterRoot() error {
	fresh := s.newMachine()
	fresh.key = s.me.key
	return s.on(fresh, func() error {
		a := s.shed()
		code := s.allowMe("shed")
		a.mu.Lock()
		start := len(a.paths)
		a.mu.Unlock()
		s.enroll(a.url, "--code", code)
		if err := s.succeeded(); err != nil {
			return err
		}
		a.mu.Lock()
		asked := append([]string(nil), a.paths[start:]...)
		a.mu.Unlock()
		enrolled, revoked := -1, -1
		for i, p := range asked {
			if p == enrollhttp.Path && enrolled < 0 {
				enrolled = i
			}
			if strings.HasPrefix(p, enrollhttp.RevocationsPath) && revoked < 0 {
				revoked = i
			}
		}
		if enrolled < 0 || revoked < 0 || revoked < enrolled {
			return fmt.Errorf("the revocation list was not fetched after the root was kept: %v", asked)
		}
		return nil
	})
}

// ---- the suite -------------------------------------------------------------------------------

func TestEnrollmentRootTrustFeature(t *testing.T) {
	home, hOK := os.LookupEnv("HOME")
	xdg, xOK := os.LookupEnv("XDG_CONFIG_HOME")
	app, aOK := os.LookupEnv("AppData")
	brk, bOK := os.LookupEnv("ROGER_BROKER")
	origLog := log.Writer()
	t.Cleanup(func() {
		restoreEnv("HOME", home, hOK)
		restoreEnv("XDG_CONFIG_HOME", xdg, xOK)
		restoreEnv("AppData", app, aOK)
		restoreEnv("ROGER_BROKER", brk, bOK)
		log.SetOutput(origLog)
		edgeUserKey, edgeNow, edgeStdin = defaultEdgeUserKey, time.Now, os.Stdin
		edgeIsInteractive = func() bool { return false }
		enrollhttp.Client = &http.Client{}
	})
	lanIPs := edgeLANIPs
	t.Cleanup(func() { edgeLANIPs = lanIPs })
	interactive := edgeIsInteractive
	t.Cleanup(func() { edgeIsInteractive = interactive })

	st := &rtBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				if st.live != nil {
					st.finishLive()
				}
				*st = rtBDD{t: t, auths: map[string]*rootedAuthority{}, codes: map[string]string{}}
				st.logs = &bytes.Buffer{}
				log.SetOutput(io.MultiWriter(st.logs, io.Discard))
				edgeNow, edgeStdin = time.Now, strings.NewReader("")
				edgeIsInteractive = func() bool { return false }
				enrollhttp.Client = &http.Client{}
				edgeLANIPs = func() []string { return []string{"127.0.0.1"} }
				edgeDialTimeout = time.Second
				_ = os.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
				st.me = st.newMachine()
				st.use(st.me)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				if st.live != nil {
					st.finishLive()
				}
				return ctx, nil
			})

			// Background
			sc.Step(`^a local Edge authority "([^"]*)" whose root this machine has never held$`, st.aLocalAuthority)
			sc.Step(`^the authority answers enrollment and claims on its LAN listener$`, st.authorityAnswers)

			// 1
			sc.Step(`^this machine holds no Edge root$`, st.holdsNoRoot)
			sc.Step(`^it enrolls against "shed" with neither a code nor a root pin$`, st.enrollsWithNeither)
			sc.Step(`^it keeps no root, no certificate, no node key and no fleet row$`, st.keptNothing)
			sc.Step(`^it says the root of the Edge it is joining has to be confirmed$`, func() error { return st.says("has to be confirmed") })
			sc.Step(`^it names where the code comes from: allow or adopt on the authority$`, st.namesWhereTheCodeComesFrom)
			sc.Step("^it names the pin alternative: the full root fingerprint `roger edge authority` prints there$", st.namesThePin)
			sc.Step(`^no interactive input is available$`, st.noInteractiveInput)
			sc.Step(`^the owner runs "(roger edge enroll bench --authority http://192\.168\.1\.10:8791)"$`, st.runsLiteral)
			sc.Step(`^it is refused before any request leaves this machine$`, st.refusedBeforeSending)
			sc.Step(`^the refusal says to pass --code <code> from the authority, or --root sha256:<fingerprint>$`, func() error {
				return st.says("--code <code>", "--root sha256:<fingerprint>")
			})
			sc.Step(`^it exits non-zero$`, st.exitsNonZero)
			sc.Step(`^this machine is enrolled under "shed" and holds its root$`, st.enrolledUnderShed)
			sc.Step(`^its certificate is close to expiry$`, st.closeToExpiry)
			sc.Step(`^it renews against "shed" with no code$`, st.renewsWithNoCode)
			sc.Step(`^the answer is accepted because its root equals the one already held$`, st.renewalAccepted)
			sc.Step(`^the node id, the name and the place in the fleet are unchanged$`, st.nodeNameFleetUnchanged)
			sc.Step(`^this machine is the designated authority "([^"]*)" and has not enrolled itself$`, st.designatedNotEnrolled)
			sc.Step(`^the owner enrolls this machine with no --authority$`, st.enrollsWithNoAuthority)
			sc.Step(`^it enrolls in-process against the root it generated$`, st.enrollsInProcess)
			sc.Step(`^no code is minted, asked for or checked$`, func() error {
				if st.coreSrv != nil {
					return st.never("enter the enrollment code")
				}
				return st.noCodeAtAll()
			})
			sc.Step(`^nothing crossed the network$`, st.nothingCrossed)
			sc.Step(`^this machine is enrolled under "shed"$`, st.enrolledUnderShed)
			sc.Step(`^the owner runs enroll again with a code that does not match anything$`, st.runsAgainWithBogusCode)
			sc.Step(`^nothing is sent, nothing is checked and nothing changes$`, st.nothingSentChecked)
			sc.Step(`^it says this machine is already enrolled, as enrollment\.feature requires$`, st.saysAlreadyEnrolled)

			// 2
			sc.Step(`^the owner runs "roger edge authority allow <that machine's user key>" on "shed"$`, st.allowOnShed)
			sc.Step(`^it prints a one-time enrollment code shaped XXXXX-XXXXX$`, st.printsCodeShape)
			sc.Step(`^it prints the join command for that machine, including --code and the authority's address$`, st.printsJoinCommand)
			sc.Step(`^it prints the root's short fingerprint, so the owner can see the joiner name the same one$`, st.printsRootShort)
			sc.Step(`^it says the code is good for 15 minutes and for one join$`, st.saysFifteenOneJoin)
			sc.Step(`^a phone advertising as a candidate "([^"]*)"$`, st.phoneCandidate)
			sc.Step(`^the owner runs "roger edge adopt ([^"]*)" on "shed"$`, st.runsAdoptOnShed)
			sc.Step(`^it prints a one-time enrollment code for "([^"]*)"$`, st.printsCodeFor)
			sc.Step(`^it says to enter that code on "([^"]*)" to finish joining$`, st.saysEnterOn)
			sc.Step(`^a device "([^"]*)" that was forgotten$`, st.forgottenDevice)
			sc.Step(`^the owner runs "roger edge readmit ([^"]*)" and confirms$`, st.readmitsAndConfirms)
			sc.Step(`^it says a device that still holds this Edge's root will not need it$`, st.saysHeldRootNeedsNone)
			sc.Step(`^10000 codes are minted$`, st.tenThousandCodes)
			sc.Step(`^every symbol is from the Crockford alphabet$`, st.everySymbolCrockford)
			sc.Step(`^no two codes are equal$`, st.noTwoEqual)
			sc.Step(`^each symbol position is uniformly distributed within statistical tolerance$`, st.uniform)
			sc.Step(`^the owner allowed a machine and was shown code A$`, st.allowedShownA)
			sc.Step(`^the owner allows the same machine again and is shown code B$`, st.allowedAgainShownB)
			sc.Step(`^an answer to that machine carries a proof made with B$`, func() error { return st.proofMadeWith("B") })
			sc.Step(`^a proof made with A is never produced again$`, st.proofANeverAgain)
			sc.Step(`^code A was minted for machine "bench" and code B for the phone "pocket"$`, st.codesForBenchAndPocket)
			sc.Step(`^"pocket" enrolls$`, st.pocketEnrolls)
			sc.Step(`^the authority's answer to "pocket" carries a proof made with B only$`, st.pocketProofBOnly)
			sc.Step(`^"bench"'s code A remains live and unspent$`, st.codeARemainsLive)
			sc.Step(`^a code was minted for a join$`, st.codeMintedForAJoin)
			sc.Step(`^it does not appear in (.+)$`, st.doesNotAppearIn)
			sc.Step(`^a code was minted on "shed"$`, st.codeMintedOnShed)
			sc.Step(`^it is stored in the authority's directory readable only by its owner$`, st.storedOwnerOnly)
			sc.Step(`^the code is spent or expires$`, st.codeSpent)
			sc.Step(`^it is deleted from that store$`, st.deletedFromStore)
			sc.Step(`^a restart of the authority does not bring it back$`, st.restartNoRevive)
			sc.Step(`^a code was minted on "shed" 2 minutes ago$`, st.mintedTwoAgo)
			sc.Step(`^the authority restarts$`, st.authorityRestarts)
			sc.Step(`^the machine enrolls with that code$`, st.enrollsWithThatCode)
			sc.Step(`^the answer carries a valid proof made with it$`, st.answerProofValid)

			// 3
			sc.Step(`^the owner allowed this machine on "shed" and was shown its code$`, func() error { return st.allowedAndShown("shed") })
			sc.Step(`^the owner runs "roger edge enroll bench --authority <shed's address> --code <that code>"$`, st.runsEnrollWithCode)
			sc.Step(`^the answer's root proof verifies with that code$`, st.answerVerifiesWithCode)
			sc.Step(`^this machine keeps the certificate and the root$`, st.keepsCertAndRoot)
			sc.Step(`^it prints that it joined the Edge rooted at "shed", with the root's short fingerprint$`, st.printsJoinedShed)
			sc.Step(`^the owner allowed this machine on "shed"$`, st.allowedOnShed)
			sc.Step(`^input is interactive$`, st.inputInteractive)
			sc.Step(`^the owner runs "roger edge enroll bench --authority <shed's address>" with no code$`, st.runsEnrollNoCode)
			sc.Step(`^the request is sent and the answer is held, not kept$`, st.answerHeldNotKept)
			sc.Step(`^it asks for the code shown on the authority$`, st.asksForCode)
			sc.Step(`^the owner types the code$`, st.typesTheCode)
			sc.Step(`^the held answer's proof verifies and the identity is kept$`, st.heldVerifiesKept)
			sc.Step(`^an interactive enroll is holding the authority's answer$`, st.interactiveHolding)
			sc.Step(`^the owner types a code with one wrong symbol$`, st.typesOneWrongSymbol)
			sc.Step(`^it says the code does not match the Edge that answered, and asks again$`, st.saysNoMatchAsksAgain)
			sc.Step(`^the owner types the right code$`, st.typesRightCode)
			sc.Step(`^the identity is kept$`, st.identityKept)
			sc.Step(`^the authority was asked only once$`, st.askedOnce)
			sc.Step(`^the owner ran "roger edge authority" on "shed" and it printed the full root fingerprint$`, st.authorityPrintedFullFP)
			sc.Step(`^the owner runs "roger edge enroll bench --authority <shed's address> --root sha256:<that fingerprint>"$`, st.runsEnrollWithPin)
			sc.Step(`^the answer's root hashes to exactly that fingerprint$`, st.rootHashesToPin)
			sc.Step(`^no code was needed$`, st.noCodeNeeded)
			sc.Step(`^the owner runs "roger edge authority" on "shed"$`, st.runsAuthorityOnShed)
			sc.Step(`^it prints the root's full SHA-256 fingerprint as sha256:<64 hex>$`, st.printsFullSHA)
			sc.Step(`^it prints the short form beside it for reading aloud$`, st.printsShortBeside)
			sc.Step(`^the owner passes --root "([^"]*)"$`, st.passesRoot)
			sc.Step(`^the pin is (.+)$`, st.pinIs)
			sc.Step(`^the allow output also prints an invite string$`, st.allowPrintsInvite)
			sc.Step(`^the owner runs "roger edge enroll bench --invite <that string>"$`, st.runsEnrollInvite)
			sc.Step(`^it enrolls against the address in the invite$`, st.enrollsAtInviteAddress)
			sc.Step(`^the root must match the invite's pin AND the proof must verify with the invite's code$`, st.pinAndProofBoth)
			sc.Step(`^an invite whose pin and code disagree about the root is refused, keeping nothing$`, st.disagreeingInviteRefused)
			sc.Step(`^the owner enters the code as "([^"]*)"$`, st.entersCodeAs)
			sc.Step(`^it is read as the code "([^"]*)"$`, st.readAsCode)
			sc.Step(`^the owner adopted the phone "([^"]*)" on "shed" and was shown its code$`, st.phoneAdoptedShown)
			sc.Step(`^"pocket" claims and "shed" answers with a certificate and a root proof$`, st.pocketClaimsAnswer)
			sc.Step(`^"pocket" holds the answer and asks for the code shown on the authority$`, st.pocketHoldsAsks)
			sc.Step(`^the code is entered on "pocket"$`, st.phoneEntersCode)
			sc.Step(`^the proof verifies and "pocket" keeps its membership$`, st.pocketKeeps)
			sc.Step(`^it presents and is drawn as a verified member, as claim\.feature describes$`, st.presentsVerified)
			sc.Step(`^the owner allowed the phone's account key on "shed" and was shown a code$`, st.phoneKeyAllowedShown)
			sc.Step(`^the phone joins "shed" by address and the code is entered$`, st.phoneJoinsByAddress)
			sc.Step(`^the proof verifies and the phone keeps its membership$`, st.pocketKeeps)
			sc.Step(`^a machine with no terminal, run by the boot service$`, st.headlessMachine)
			sc.Step(`^it runs "roger edge enroll pi-cabinet --authority <shed's address> --code <code>"$`, st.runsHeadless)
			sc.Step(`^it enrolls without prompting$`, st.enrollsNoPrompt)
			sc.Step(`^with a wrong code it exits non-zero, keeping nothing, and never waits for input$`, st.wrongCodeHeadless)
			sc.Step(`^a machine joins "shed" with a code$`, st.joinsShedWithCode)
			sc.Step(`^the joiner prints the root's short fingerprint$`, st.joinerPrintsShort)
			sc.Step(`^it is the same short fingerprint "roger edge authority" prints on "shed"$`, st.sameShortOnShed)
			sc.Step(`^the joiner says the fingerprint is for reassurance and the code is what was checked$`, st.saysReassurance)

			// 4
			sc.Step(`^an impostor authority with a root of its own answers this machine's enrollment$`, st.impostorOwnRoot)
			sc.Step(`^the owner holds the code "([^"]*)" printed$`, st.holdsShedCode)
			sc.Step(`^this machine checks the answer$`, st.checksTheAnswer)
			sc.Step(`^the root proof does not verify$`, st.proofDoesNotVerify)
			sc.Step(`^this machine keeps no root, no certificate, no node key and no fleet row$`, st.keptNothing)
			sc.Step(`^it says: the device that answered at that address is not the Edge that showed you this code$`, st.saysImpostorLine)
			sc.Step(`^it names the address that answered and the short fingerprint of the root it offered$`, st.namesAddrAndShort)
			sc.Step(`^it does not tell the owner to retry with a different code$`, st.noDifferentCode)
			sc.Step(`^an impostor answers with a valid-looking certificate and root and no root proof$`, st.impostorNoProof)
			sc.Step(`^the owner holds a code$`, st.holdsACode)
			sc.Step(`^it is refused, keeping nothing$`, st.refusedKeepingNothing)
			sc.Step(`^the message says the answer carried no proof for the code$`, st.saysNoProof)
			sc.Step(`^an impostor whose root's short fingerprint equals "shed"'s$`, st.impostorSameShort)
			sc.Step(`^this machine joins with the code, or with the full pin$`, st.joinsWithCodeOrPin)
			sc.Step(`^the proof does not verify, and the full fingerprint does not match$`, st.proofAndPinFail)
			sc.Step(`^the owner gave --root with "shed"'s full fingerprint$`, st.gaveShedPin)
			sc.Step(`^an impostor answers with its own root$`, st.impostorAnswersOwnRoot)
			sc.Step(`^the root does not hash to the pin$`, st.rootNotPin)
			sc.Step(`^it is refused, keeping nothing, naming the expected and the offered fingerprints$`, st.refusedNamingBoth)
			sc.Step(`^a device between this machine and "shed" that passes the request and answer through$`, st.deviceInBetween)
			sc.Step(`^this machine joins with the code$`, st.joinsWithTheCode)
			sc.Step(`^the proof verifies because the root is "shed"'s$`, st.proofVerifiesShed)
			sc.Step(`^this machine holds a certificate for its own node key under "shed"'s root$`, st.certForOwnKey)
			sc.Step(`^the device in between holds nothing it can use: no key, no certificate of its own, no code$`, st.relayHoldsNothing)
			sc.Step(`^an impostor answers with "shed"'s proof but its own root$`, st.impostorShedProofOwnRoot)
			sc.Step(`^the proof does not verify for that root$`, st.proofNotForThatRoot)
			sc.Step(`^an impostor answers with "shed"'s root and proof and a certificate it signed itself$`, st.impostorShedRootOwnCert)
			sc.Step(`^the certificate does not verify under the root$`, st.certNotUnderRoot)
			sc.Step(`^it is refused as "unknown authority", keeping nothing$`, st.refusedUnknownAuthority)
			sc.Step(`^a real answer from "shed" whose proof verifies$`, st.realAnswerVerifies)
			sc.Step(`^an impostor changes the (.+) and keeps the proof$`, st.impostorChanges)
			sc.Step(`^the proof does not verify$`, st.theProofDoesNotVerify)
			sc.Step(`^a valid answer and proof that "shed" gave a different machine$`, st.capturedFromOther)
			sc.Step(`^an impostor replays it to this machine$`, st.impostorReplays)
			sc.Step(`^it names another node and another nonce$`, st.namesAnotherNode)
			sc.Step(`^an impostor's answer has been refused for a join$`, st.impostorRefusedForAJoin)
			sc.Step(`^the joiner prints one line naming the address and the offered root's short fingerprint$`, st.oneLineAddrShort)
			sc.Step(`^it says another device on the network answered as an Edge authority it could not verify$`, st.saysCouldNotVerify)
			sc.Step(`^it does not repeat the line on every retry of the same join$`, st.notRepeated)

			// 5
			sc.Step(`^the owner runs enroll with --code set to a code "shed" never minted$`, st.runsNeverMinted)
			sc.Step(`^nothing is kept$`, st.nothingIsKept)
			sc.Step(`^it says the code does not match the Edge that answered, and to check it or mint a fresh one$`, st.saysCheckOrMint)
			sc.Step(`^two Edges on one network, "shed" and "lab", each with its own root$`, st.twoEdges)
			sc.Step(`^the owner holds a code minted by "lab"$`, st.holdsLabCode)
			sc.Step(`^this machine enrolls against "shed" with that code$`, st.enrollsShedWithThatCode)
			sc.Step(`^"shed" minted code A for the phone "pocket" and code B for this machine$`, st.codesAForPocketBForMe)
			sc.Step(`^this machine enrolls with code A$`, st.enrollsWithA)
			sc.Step(`^the answer's proof was made with B$`, st.answerMadeWithB)
			sc.Step(`^the owner runs enroll with --code "([^"]*)"$`, st.runsMalformed)
			sc.Step(`^it is refused naming what a code looks like$`, st.refusedNamingShape)
			sc.Step(`^no request is sent$`, st.noRequestSent)

			// 6
			sc.Step(`^this machine joined "shed" with its code$`, st.joinedWithItsCode)
			sc.Step(`^another enrollment for the same allowed key arrives$`, st.anotherForSameKey)
			sc.Step(`^the authority's answer carries no proof made with the spent code$`, st.noProofSpent)
			sc.Step(`^a joiner presenting that spent code refuses the answer, keeping nothing$`, st.spentRefused)
			sc.Step(`^it says the code was already used and to mint a fresh one on the authority$`, st.saysAlreadyUsed)
			sc.Step(`^the code is not stored anywhere on this machine$`, st.codeNotStoredHere)
			sc.Step(`^a later enroll does not reuse it$`, st.laterNoReuse)
			sc.Step(`^the phone "pocket" was adopted and claims its certificate$`, st.pocketAdoptedClaims)
			sc.Step(`^the grant and the code are spent together$`, st.grantAndCodeSpent)
			sc.Step(`^a second claim for "pocket" is refused as not adopted, as claim\.feature requires$`, st.secondClaimRefused)
			sc.Step(`^no second proof is produced for that code$`, st.noSecondProof)
			sc.Step(`^one live code for this machine's allowed key$`, st.oneLiveCode)
			sc.Step(`^two enrollments for that key arrive at the same moment$`, st.twoAtOnce)
			sc.Step(`^at most one answer carries a proof made with that code$`, st.atMostOneProof)
			sc.Step(`^a signed enrollment request "shed" already answered$`, st.signedAlreadyAnswered)
			sc.Step(`^the identical request is sent again$`, st.identicalAgain)
			sc.Step(`^it is refused as already used, as enrollment\.feature requires$`, st.refusedAlreadyUsed)
			sc.Step(`^no proof is produced$`, st.noProofProduced)

			// 7
			sc.Step(`^a code was minted on "shed" 16 minutes ago and never used$`, st.mintedSixteenAgo)
			sc.Step(`^this machine enrolls with it$`, st.enrollsWithIt)
			sc.Step(`^the answer carries no proof for that code$`, st.answerNoProofForCode)
			sc.Step(`^the joiner refuses it, keeping nothing$`, st.refusedKeepingNothing)
			sc.Step(`^it says the code has expired and to mint a fresh one with allow or adopt$`, st.saysExpired)
			sc.Step(`^a code was minted on "shed" 14 minutes ago$`, st.mintedFourteenAgo)
			sc.Step(`^the proof verifies and the identity is kept$`, st.proofVerifiesKept)
			sc.Step(`^an interactive enroll has held the authority's answer for 16 minutes without a code$`, st.heldSixteenMinutes)
			sc.Step(`^the held answer is discarded, not checked$`, st.heldDiscarded)
			sc.Step(`^it says to run the join again, keeping nothing$`, st.saysRunAgain)
			sc.Step(`^a code expired on "shed"$`, st.codeExpiredOnShed)
			sc.Step(`^the code is not in its store$`, st.notInStore)
			sc.Step(`^no answer carries a proof made with it$`, st.noAnswerProof)
			sc.Step(`^the phone "pocket" was adopted 20 minutes ago and has not claimed$`, st.pocketAdoptedTwentyAgo)
			sc.Step(`^its code has expired while its adopt grant is still live$`, st.codeExpiredGrantLive)
			sc.Step(`^the adopting panel says the code expired and offers to show a new one$`, st.panelSaysExpired)
			sc.Step(`^the owner adopts "([^"]*)" again$`, st.adoptsAgain)
			sc.Step(`^a fresh code is shown and the old one is void$`, st.freshCodeShown)
			sc.Step(`^the authority's clock is a day behind$`, st.clockDayBehind)
			sc.Step(`^this machine enrolls with a code minted on that authority$`, st.enrollsWithSkewedCode)
			sc.Step(`^the code's life is measured on the authority's own clock, from when it was minted$`, st.codeLifeOnAuthorityClock)
			sc.Step(`^the joiner's own 15-minute window is measured on the joiner's clock$`, st.joinerWindowOwnClock)

			// 8
			sc.Step(`^the proof key is PBKDF2-HMAC-SHA256 of the canonical code$`, st.keyIsPBKDF2)
			sc.Step(`^its salt is the domain string followed by the request's own nonce$`, st.saltIsDomainNonce)
			sc.Step(`^it uses 200000 iterations and a 32-byte output$`, st.iterationsAndLength)
			sc.Step(`^two requests with the same code produce unrelated keys$`, st.unrelatedKeys)
			sc.Step(`^a code is 10 symbols from a 32-symbol alphabet$`, st.tenOfThirtyTwo)
			sc.Step(`^every symbol is drawn independently and uniformly$`, st.drawnUniformly)
			sc.Step(`^an interactive enroll is holding an answer$`, st.interactiveHoldingAnswer)
			sc.Step(`^the owner enters 5 codes that do not verify$`, st.entersFiveWrong)
			sc.Step(`^the held answers are discarded and nothing is kept$`, st.heldDiscardedNothingKept)
			sc.Step(`^it says the code did not match 5 times, that a device on the network may be impersonating the authority, and to mint a fresh code$`, st.saysCapImpostor)
			sc.Step(`^a sixth entry is not checked$`, st.sixthNotChecked)
			sc.Step(`^the owner runs enroll with a --code that does not verify$`, st.runsCodeThatFails)
			sc.Step(`^it is checked exactly once$`, st.checkedOnce)
			sc.Step(`^the command exits non-zero, keeping nothing$`, st.exitsNonZeroKeepingNothing)
			sc.Step(`^no request field carries the code or anything derived from it$`, st.noRequestFieldCarriesCode)
			sc.Step(`^the authority never compares a caller's input to a code$`, st.neverCompares)
			sc.Step(`^so there is no online guessing of the code at the authority to rate-limit$`, st.nothingToRateLimit)
			sc.Step(`^one live code for a join$`, st.oneLiveCode)
			sc.Step(`^the authorized joiner's requests are answered$`, st.authorizedAnswered)
			sc.Step(`^only the answer that issues a certificate carries a proof$`, st.onlyIssuingCarries)
			sc.Step(`^answers to unauthorized requests never carry one$`, st.unauthorizedNever)
			sc.Step(`^a live code for an allowed machine$`, st.liveCodeForAllowed)
			sc.Step(`^a caller whose key is not allowed sends an enrollment request$`, st.strangerSends)
			sc.Step(`^it is refused exactly as today, with no root proof in the refusal$`, st.refusedAsToday)
			sc.Step(`^the refusal is byte-identical whether or not a code is live$`, st.byteIdenticalRefusal)

			// 9
			sc.Step(`^it re-enrolls and the answer carries a different root$`, st.reEnrollsDifferentRoot)
			sc.Step(`^it is refused as "that certificate comes from a different authority"$`, st.refusedDifferentAuthority)
			sc.Step(`^its identity, its root and its fleet row are unchanged$`, st.identityRootRowUnchanged)
			sc.Step(`^this machine is a member, not an authority$`, st.memberNotAuthority)
			sc.Step(`^a re-enroll is refused for a different root$`, st.reEnrollRefusedDifferent)
			sc.Step(`^the message says the Edge it belongs to is rooted elsewhere$`, st.saysRootedElsewhere)
			sc.Step(`^it says that if the owner moved the Edge on purpose, the new authority's allow or adopt prints a code, and to re-enroll with it and --replace-root$`, st.saysAllowAdoptReplace)
			sc.Step(`^it does not tell the member to run "roger edge authority local --force"$`, st.noForceForMember)
			sc.Step(`^the Edge moved from "shed" to the new authority "barn"$`, st.edgeMoved)
			sc.Step(`^this machine is marked NEEDS RE-ENROLLMENT and holds "shed"'s root$`, st.markedReenroll)
			sc.Step(`^"barn" allowed this machine and printed a code$`, st.barnAllowedCode)
			sc.Step(`^the owner runs enroll against "barn" with that code and --replace-root$`, st.runsReplaceLive)
			sc.Step(`^it shows the old root's short fingerprint and the new one's, and asks y/N$`, st.showsBothAsks)
			sc.Step(`^the owner answers yes$`, st.answersYes)
			sc.Step(`^the old identity and root are replaced by the new ones in one step$`, st.replacedInOneStep)
			sc.Step(`^this machine is a member under "barn"$`, st.memberUnderBarn)
			sc.Step(`^it no longer verifies peers against "shed"'s root$`, st.noLongerShedRoot)
			sc.Step(`^a re-enroll against a different root, with a verified code and --replace-root$`, st.reEnrollDifferentVerified)
			sc.Step(`^the owner answers no$`, st.answersNo)
			sc.Step(`^nothing changes: the old identity, root and fleet row remain$`, st.nothingChangesOld)
			sc.Step(`^this machine holds "shed"'s root$`, st.holdsShedRoot)
			sc.Step(`^it re-enrolls against "barn" with --replace-root and no code or pin$`, st.replaceNoCode)
			sc.Step(`^it is refused before anything is kept$`, st.refusedBeforeKept)
			sc.Step(`^it says a new root needs the code from its authority$`, st.saysNeedsCode)
			sc.Step(`^it re-enrolls against "barn" with --replace-root and a wrong code$`, st.replaceWrongCode)
			sc.Step(`^it is refused$`, st.refused)
			sc.Step(`^the old identity, root and fleet row are unchanged$`, st.oldUnchanged)
			sc.Step(`^the owner runs enroll against "barn" with a verified code and --replace-root but no --yes$`, st.replaceNoYes)
			sc.Step(`^it is refused, saying a root change must be confirmed, and changes nothing$`, st.refusedMustConfirm)
			sc.Step(`^it re-enrolls against "shed" with --replace-root$`, st.replaceAgainstShed)
			sc.Step(`^it says the root is unchanged and nothing is replaced$`, st.saysUnchanged)
			sc.Step(`^this machine was forgotten by "shed" but still holds "shed"'s root$`, st.forgottenKeptRoot)
			sc.Step(`^the owner re-admitted it$`, st.ownerReadmitted)
			sc.Step(`^it enrolls against "shed" with no code$`, st.enrollsNoCode)
			sc.Step(`^the answer's root equals the root it holds, so it is accepted$`, st.acceptedHeldRoot)
			sc.Step(`^it comes back as described by node_identity\.feature's re-admit scenarios$`, st.comesBackAsReadmit)
			sc.Step(`^the phone "pocket" holds "shed"'s root$`, st.pocketHoldsShedRoot)
			sc.Step(`^an answer to its claim carries a different root and no code was entered$`, st.claimDifferentRootNoCode)
			sc.Step(`^it is refused as a different authority, keeping its old root$`, st.refusedKeepsOldRoot)

			// 11
			sc.Step(`^"shed" runs a roger from before root proofs$`, st.shedFromBefore)
			sc.Step(`^this machine enrolls with a code$`, st.enrollsWithACode)
			sc.Step(`^the answer carries no root proof$`, st.answerCarriesNoProof)
			sc.Step("^it says that authority does not prove its root, and to either update roger on it or join with --root and the full fingerprint \"roger edge authority\" prints there$", st.saysUpdateOrPin)
			sc.Step(`^this machine enrolls with --root and "shed"'s full fingerprint$`, st.enrollsWithShedPin)
			sc.Step(`^the answer's root matches the pin and it is kept$`, st.pinMatchedKept)
			sc.Step(`^"shed" sends root proofs$`, st.shedSendsProofs)
			sc.Step(`^a device in between removes the proof from the answer$`, st.strippedInBetween)
			sc.Step(`^it is refused exactly as against an authority that sends none$`, st.refusedAsNone)
			sc.Step(`^nothing about the answer's version or headers can make the joiner skip the check$`, st.nothingSkips)
			sc.Step(`^"roger edge enroll --help" lists --code, --root, --invite, --replace-root and --yes$`, st.helpListsFlags)
			sc.Step(`^no option, environment variable or config key makes a first enrollment keep an unproven root$`, st.noEscape)
			sc.Step(`^an answer whose root proof names a version this joiner does not implement$`, st.unknownVersion)
			sc.Step(`^this machine checks it$`, st.machineChecksIt)
			sc.Step(`^it says the authority is newer and to update roger here, or to join with the full pin$`, st.saysNewerUpdate)

			// 12
			sc.Step(`^the owner allows a machine or adopts a device on "shed"$`, st.allowsOrAdopts)
			sc.Step(`^the output says a joiner running a roger from before root proofs does not check the code$`, st.warnsOldJoiner)
			sc.Step(`^it says to update roger on that machine before it joins$`, st.saysUpdateFirst)
			sc.Step(`^a joiner running a roger from before root proofs$`, st.oldJoiner)
			sc.Step(`^it enrolls against a new "shed"$`, st.enrollsAgainstNewShed)
			sc.Step(`^it is answered exactly as before, with the proof as one extra field it ignores$`, st.answeredAsBefore)
			sc.Step(`^the answer's shape is otherwise byte-compatible with the old one$`, st.byteCompatible)
			sc.Step(`^this machine runs the new roger, is enrolled, and holds "shed"'s root$`, st.newMemberHoldsShedRoot)
			sc.Step(`^this machine renews$`, st.renews)
			sc.Step(`^the answer's root equals the one it holds and it is accepted$`, st.renewalRootSame)
			sc.Step(`^members that enrolled before root proofs existed$`, st.membersBeforeProofs)
			sc.Step(`^the authority and the members update in any order$`, st.updateAnyOrder)
			sc.Step(`^every member still verifies its peers against the root it holds$`, st.stillVerifiesHeld)
			sc.Step(`^no member is told to re-enroll because of this change$`, st.noReenrollTold)
			sc.Step(`^a joiner running (new|old) and an authority running (new|old)$`, st.pairingVersions)
			sc.Step(`^the joiner enrolls for the first time with (a code|the pin|nothing)$`, st.pairingEnrolls)
			sc.Step(`^the result is (.+)$`, st.theResultIs)

			// 13
			sc.Step(`^this Edge uses the Core authority and the broker address is https$`, st.coreHTTPS)
			sc.Step(`^this machine enrolls$`, st.thisMachineEnrolls)
			sc.Step(`^the root is accepted on the strength of the verified TLS connection to Core$`, st.acceptedOnTLS)
			sc.Step(`^this Edge uses the Core authority$`, st.coreAuthority)
			sc.Step(`^a device on the network intercepts the connection to the broker$`, st.interceptsBroker)
			sc.Step(`^the TLS certificate does not verify$`, st.tlsDoesNotVerify)
			sc.Step(`^enrollment fails naming connectivity, keeping nothing, as enrollment\.feature requires$`, st.failsConnectivity)
			sc.Step(`^the broker address is "([^"]*)"$`, st.brokerAddressIs)
			sc.Step(`^this machine enrolls under the Core authority$`, st.enrollsUnderCore)
			sc.Step(`^the broker address is plain HTTP on the LAN$`, st.plainHTTPOnLAN)
			sc.Step(`^it says Core enrollment needs an https broker$`, st.saysNeedsHTTPS)
			sc.Step(`^it says that a local authority with a code is the way to enroll on this network instead$`, st.saysLocalAuthorityInstead)

			// 14 (CLI)
			sc.Step(`^an owner whose machine is allowed on the authority$`, st.ownerAllowed)
			sc.Step(`^they run "roger edge setup", choose to join, name it "bench" and give the authority address$`, st.setupJoinsBench)
			sc.Step(`^it asks for the code the authority showed$`, st.setupAsksCode)
			sc.Step(`^with the right code it prints that it joined, with the root's short fingerprint$`, st.setupRightCode)
			sc.Step(`^an owner whose machine is not yet allowed on the authority$`, st.notYetAllowed)
			sc.Step(`^they run "roger edge setup" and choose to join$`, st.setupChoosesJoin)
			sc.Step(`^it prints this machine's user key and the allow command to run on the authority$`, st.printsKeyAndAllow)
			sc.Step(`^it says that command prints the code to bring back here$`, st.saysAllowPrintsCode)

			// 15
			sc.Step(`^a first enrollment refused because (.+)$`, st.firstRefusedBecause)
			sc.Step(`^nothing from the refused answer is kept: the machine's Edge files are exactly as before the attempt$`, st.nothingRemains)
			sc.Step(`^retrying later behaves as a first attempt$`, st.retryIsFirst)
			sc.Step(`^an answer from an unverified authority$`, st.answerFromUnverified)
			sc.Step(`^no address, name, account or revocation list from it is used or stored$`, st.nothingFromItUsed)
			sc.Step(`^the revocation list is fetched only after the root is kept$`, st.revocationsAfterRoot)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "~@tui && ~@console && ~@ios",
			Paths: []string{"../../features/edge/enrollment_root_trust.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the enrollment root trust scenarios failed")
	}
}
