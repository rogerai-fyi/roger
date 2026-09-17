package edge

// Executable spec: features/edge/session_attribution.feature (@edge) - the session MIRROR:
// one machine, many processes, one view. Two REAL ledgers over one real temp directory;
// the clock is injected, never slept on.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/protocol"
)

type mirrorBDD struct {
	t        *testing.T
	dir      string
	now      time.Time
	first    *Sessions
	second   *Sessions
	errs     []error
	before   []byte // the first's mirror file as written
	panicked bool
}

func (s *mirrorBDD) reset() {
	s.dir = filepath.Join(s.t.TempDir(), "edge-sessions")
	s.now = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	s.first, s.second, s.errs, s.before, s.panicked = nil, nil, nil, nil, false
}

func (s *mirrorBDD) ledger(account string) *Sessions {
	l := NewSessions(account)
	l.SetClock(func() time.Time { return s.now })
	l.OnError(func(err error) { s.errs = append(s.errs, err) })
	return l
}

func (s *mirrorBDD) receipt(req, station, band string) protocol.UsageReceipt {
	return protocol.UsageReceipt{RequestID: req, NodeID: station, Model: band, PromptTokens: 11, CompletionTokens: 22, PriceIn: 0.2, PriceOut: 0.5}
}

func (s *mirrorBDD) record(l *Sessions, req string) error {
	_, err := l.Record(Traffic{Account: l.Account(), Kind: FromUse, Request: req,
		Receipts: []protocol.UsageReceipt{s.receipt(req, "house-or-1", "gpt-oss-120b")}})
	return err
}

// ---- givens ----------------------------------------------------------------

func (s *mirrorBDD) twoLedgers() error {
	s.first, s.second = s.ledger("acct-1"), s.ledger("acct-1")
	s.first.Mirror(s.dir)
	s.second.Mirror(s.dir)
	return nil
}

func (s *mirrorBDD) twoAccounts() error {
	s.first, s.second = s.ledger("acct-1"), s.ledger("acct-2")
	s.first.Mirror(s.dir)
	s.second.Mirror(s.dir)
	return nil
}

func (s *mirrorBDD) corruptFile() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "999999-dead.json"), []byte("{not json"), 0o600)
}

func (s *mirrorBDD) noMirror() error { s.first = s.ledger("acct-1"); return nil }

func (s *mirrorBDD) unwritableMirror() error {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		s.t.Skip("an unwritable directory needs a non-root POSIX user")
	}
	parent := s.t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		return err
	}
	s.t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	s.dir = filepath.Join(parent, "edge-sessions")
	s.first = s.ledger("acct-1")
	s.first.Mirror(s.dir)
	return nil
}

// ---- whens -----------------------------------------------------------------

func (s *mirrorBDD) firstRecords() error {
	if err := s.record(s.first, "r1"); err != nil {
		return err
	}
	b, err := os.ReadFile(s.first.MirrorPath())
	s.before = b
	return err
}

func (s *mirrorBDD) secondRecords() error { return s.record(s.second, "r2") }

func (s *mirrorBDD) firstRecordsWithPrices() error { return s.firstRecords() }

func (s *mirrorBDD) firstRecordsAndDies() error {
	if err := s.firstRecords(); err != nil {
		return err
	}
	s.first = nil // gone: it writes nothing more
	return nil
}

func (s *mirrorBDD) secondsPass(n int) error {
	s.now = s.now.Add(time.Duration(n) * time.Second)
	return nil
}

func (s *mirrorBDD) secondReadsManyTimes() error {
	for i := 0; i < 25; i++ {
		s.second.Live()
	}
	return nil
}

func (s *mirrorBDD) secondReads() error { s.second.Live(); return nil }

func (s *mirrorBDD) itRecords() error {
	defer func() {
		if r := recover(); r != nil {
			s.panicked = true
		}
	}()
	return s.record(s.first, "r1")
}

// ---- thens -----------------------------------------------------------------

func (s *mirrorBDD) secondListsIt() error {
	defer func() {
		if r := recover(); r != nil {
			s.panicked = true
		}
	}()
	live := s.second.Live()
	if len(live) != 1 || live[0].Request != "r1" {
		return fmt.Errorf("second lists %+v", live)
	}
	return nil
}

func (s *mirrorBDD) firstListsOnce() error {
	live := s.first.Live()
	if len(live) != 1 {
		return fmt.Errorf("first lists %d sessions (its own mirror merged back?): %+v", len(live), live)
	}
	return nil
}

func (s *mirrorBDD) acct1ListsNone() error {
	if live := s.first.Live(); len(live) != 0 {
		return fmt.Errorf("acct-1 lists another account's sessions: %+v", live)
	}
	return nil
}

func (s *mirrorBDD) nothingInferable() error {
	for _, x := range s.first.Live() {
		if strings.Contains(x.Request, "r2") {
			return errors.New("leaked")
		}
	}
	return nil
}

func (s *mirrorBDD) secondListsNone() error {
	if live := s.second.Live(); len(live) != 0 {
		return fmt.Errorf("second still lists %+v", live)
	}
	return nil
}

func (s *mirrorBDD) fileCarriesTheView() error {
	var got struct {
		Account  string            `json:"account"`
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(s.before, &got); err != nil {
		return fmt.Errorf("mirror is not JSON: %v\n%s", err, s.before)
	}
	if got.Account != "acct-1" || len(got.Sessions) != 1 {
		return fmt.Errorf("mirror = %s", s.before)
	}
	var one map[string]any
	_ = json.Unmarshal(got.Sessions[0], &one)
	for _, k := range []string{"request", "kind", "band", "station", "outcome", "at"} {
		if _, ok := one[k]; !ok {
			return fmt.Errorf("mirror session lacks %q: %s", k, got.Sessions[0])
		}
	}
	return nil
}

func (s *mirrorBDD) fileCarriesNoReceipt() error {
	low := strings.ToLower(string(s.before))
	for _, banned := range []string{"receipt", "price", "token", "0.2", "0.5"} {
		if strings.Contains(low, banned) {
			return fmt.Errorf("mirror carries %q: %s", banned, s.before)
		}
	}
	return nil
}

func (s *mirrorBDD) nothingPanicked() error {
	if s.panicked {
		return errors.New("panicked")
	}
	return nil
}

func (s *mirrorBDD) firstFileUnchanged() error {
	b, err := os.ReadFile(s.first.MirrorPath())
	if err != nil {
		return err
	}
	if string(b) != string(s.before) {
		return fmt.Errorf("the first's mirror changed under a reader:\n%s\n---\n%s", s.before, b)
	}
	return nil
}

func (s *mirrorBDD) secondWroteNothing() error {
	if _, err := os.Stat(s.second.MirrorPath()); !os.IsNotExist(err) {
		return fmt.Errorf("the second wrote %s having recorded nothing (err=%v)", s.second.MirrorPath(), err)
	}
	return nil
}

func (s *mirrorBDD) firstFileGone() error {
	files, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	for _, f := range files {
		if filepath.Base(f) != filepath.Base(s.second.MirrorPath()) {
			return fmt.Errorf("a faded mirror is still there: %s", f)
		}
	}
	return nil
}

func (s *mirrorBDD) firstFileStillThere() error {
	files, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if len(files) != 1 {
		return fmt.Errorf("mirror files = %v, want the first's", files)
	}
	return nil
}

func (s *mirrorBDD) dirOwnerOnly() error {
	fi, err := os.Stat(s.dir)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		return fmt.Errorf("mirror dir mode = %v", fi.Mode().Perm())
	}
	return nil
}

func (s *mirrorBDD) fileOwnerOnly() error {
	fi, err := os.Stat(s.first.MirrorPath())
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		return fmt.Errorf("mirror file mode = %v", fi.Mode().Perm())
	}
	return nil
}

func (s *mirrorBDD) itListsIt() error {
	if live := s.first.Live(); len(live) != 1 {
		return fmt.Errorf("lists %+v", live)
	}
	return nil
}

func (s *mirrorBDD) noFileAnywhere() error {
	if s.first.MirrorPath() != "" {
		return fmt.Errorf("an unmirrored ledger has a mirror path %q", s.first.MirrorPath())
	}
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		return fmt.Errorf("a mirror dir appeared: %v", err)
	}
	return nil
}

func (s *mirrorBDD) failureReportedOnce() error {
	// a second and third turn: still one report
	_ = s.record(s.first, "r2")
	_ = s.record(s.first, "r3")
	if len(s.errs) != 1 {
		return fmt.Errorf("write failure reported %d times: %v", len(s.errs), s.errs)
	}
	if s.first.Len() != 3 {
		return fmt.Errorf("a failed mirror refused sessions: %d", s.first.Len())
	}
	return nil
}

func TestSessionMirrorFeature(t *testing.T) {
	st := &mirrorBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step(`^two session ledgers for account "acct-1" mirrored under one directory$`, st.twoLedgers)
			sc.Step(`^a ledger for "acct-1" and a ledger for "acct-2" mirrored under one directory$`, st.twoAccounts)
			sc.Step(`^a corrupt file in the mirror directory$`, st.corruptFile)
			sc.Step(`^a session ledger with no mirror$`, st.noMirror)
			sc.Step(`^a session ledger mirrored under a directory that cannot be written$`, st.unwritableMirror)
			sc.Step(`^the first records a receipted turn$`, st.firstRecords)
			sc.Step(`^the "acct-2" ledger records a receipted turn$`, st.secondRecords)
			sc.Step(`^the first records a receipted turn with prices and token counts$`, st.firstRecordsWithPrices)
			sc.Step(`^the first records a receipted turn and its process is gone$`, st.firstRecordsAndDies)
			sc.Step(`^(\d+) seconds pass$`, st.secondsPass)
			sc.Step(`^the second reads its sessions many times$`, st.secondReadsManyTimes)
			sc.Step(`^the second reads its sessions$`, st.secondReads)
			sc.Step(`^it records a receipted turn$`, st.itRecords)
			sc.Step(`^the second lists that session$`, st.secondListsIt)
			sc.Step(`^the first lists it once, not twice$`, st.firstListsOnce)
			sc.Step(`^the "acct-1" ledger lists no session$`, st.acct1ListsNone)
			sc.Step(`^nothing about it is inferable from the "acct-1" ledger$`, st.nothingInferable)
			sc.Step(`^the second lists no session$`, st.secondListsNone)
			sc.Step(`^its mirror file carries the session's attribution, band, station, outcome and time$`, st.fileCarriesTheView)
			sc.Step(`^the mirror file carries no receipt, no price and no token count$`, st.fileCarriesNoReceipt)
			sc.Step(`^nothing panicked$`, st.nothingPanicked)
			sc.Step(`^the first's mirror file is byte-for-byte unchanged$`, st.firstFileUnchanged)
			sc.Step(`^the second has written no file of its own, having recorded nothing$`, st.secondWroteNothing)
			sc.Step(`^the first's mirror file is gone$`, st.firstFileGone)
			sc.Step(`^the first's mirror file is still there$`, st.firstFileStillThere)
			sc.Step(`^the mirror directory is readable by the owner only$`, st.dirOwnerOnly)
			sc.Step(`^the mirror file is readable by the owner only$`, st.fileOwnerOnly)
			sc.Step(`^it lists that session$`, st.itListsIt)
			sc.Step(`^no file was written anywhere$`, st.noFileAnywhere)
			sc.Step(`^the write failure is reported once, not on every turn$`, st.failureReportedOnce)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@edge",
			Paths: []string{"../../features/edge/session_attribution.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the session mirror scenarios failed")
	}
}
