package main

// context_bdd_test.go wires godog for features/capsule/cli.feature: it drives the REAL
// `roger context` export/import/merge entry points over temp files, with the operator key
// isolated to a throwaway XDG_CONFIG_HOME (never the developer's real user.key).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/capsule"
)

type contextCLIBDD struct {
	dir        string
	draftPath  string
	capPath    string
	basePath   string
	guestPath  string
	mergedPath string
	summary    string
	exportErr  error
	importErr  error
}

// draftJSON builds an unsigned draft (the capsule wire shape) with the given turns.
func draftJSON(turnsCSV string, withToolCalls bool) []byte {
	c := capsule.Capsule{
		Capsule:   capsule.Version,
		ID:        "cap_cli",
		Thread:    capsule.Thread{OriginThreadID: "t1", Title: "T"},
		Redaction: "full",
		Summary:   capsule.Summary{Text: "s", ProducedBy: "none"},
	}
	max := -1
	for _, p := range strings.Split(turnsCSV, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, _ := strconv.Atoi(p)
		m := capsule.Message{Role: "user", Content: "c" + p, XRoger: capsule.XRoger{Turn: n, Agent: "user", TS: int64(n)}}
		if withToolCalls {
			m.ToolCalls = capsule.ToolCallsRaw([]capsule.ToolCall{{Arguments: `{"url":"https://x.com/a"}`, ID: "call_1", Name: "open_url"}})
		}
		c.Messages = append(c.Messages, m)
		if n > max {
			max = n
		}
	}
	c.Thread.BaseWatermark = max + 1
	raw, _ := json.Marshal(c)
	return raw
}

func (s *contextCLIBDD) writeDraft(turnsCSV string) error {
	s.draftPath = filepath.Join(s.dir, "draft.json")
	return os.WriteFile(s.draftPath, draftJSON(turnsCSV, false), 0o600)
}
func (s *contextCLIBDD) writeToolCallDraft() error {
	s.draftPath = filepath.Join(s.dir, "draft.json")
	return os.WriteFile(s.draftPath, draftJSON("0", true), 0o600)
}
func (s *contextCLIBDD) writeGuestDraft(turnsCSV string) error {
	s.guestPath = filepath.Join(s.dir, "guest-draft.json")
	return os.WriteFile(s.guestPath, draftJSON(turnsCSV, false), 0o600)
}

func (s *contextCLIBDD) exportToCapsule() error {
	s.capPath = filepath.Join(s.dir, "convo.rcap.json")
	s.exportErr = cmdContextExport([]string{s.draftPath, "-o", s.capPath})
	return nil
}
func (s *contextCLIBDD) exportAsBase() error {
	if err := s.exportToCapsule(); err != nil {
		return err
	}
	s.basePath = s.capPath
	return s.exportErr
}
func (s *contextCLIBDD) exportGuest() error {
	out := filepath.Join(s.dir, "guest.rcap.json")
	if err := cmdContextExport([]string{s.guestPath, "-o", out}); err != nil {
		return err
	}
	s.guestPath = out
	return nil
}

// captureCtxStdout runs fn with os.Stdout redirected to a pipe and returns what it printed.
func captureCtxStdout(fn func() error) (string, error) {
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := fn()
	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	return string(out), err
}

func (s *contextCLIBDD) importCapsule() error {
	s.summary, s.importErr = captureCtxStdout(func() error {
		return cmdContextImport([]string{s.capPath})
	})
	return nil
}
func (s *contextCLIBDD) tamperCapsuleOnDisk() error {
	data, err := os.ReadFile(s.capPath)
	if err != nil {
		return err
	}
	return os.WriteFile(s.capPath, []byte(strings.Replace(string(data), `"c0"`, `"EVIL"`, 1)), 0o600)
}
func (s *contextCLIBDD) importGuestIntoBase() error {
	s.mergedPath = filepath.Join(s.dir, "merged.rcap.json")
	s.importErr = cmdContextImport([]string{s.guestPath, "--into", s.basePath, "-o", s.mergedPath})
	return nil
}

func (s *contextCLIBDD) importReportsTurns(n int) error {
	if !strings.Contains(s.summary, strconv.Itoa(n)+" turns") {
		return bddErrf("summary %q did not report %d turns", s.summary, n)
	}
	return nil
}
func (s *contextCLIBDD) importReportsVerified() error {
	if !strings.HasPrefix(s.summary, "verified capsule") {
		return bddErrf("summary %q did not report verified", s.summary)
	}
	return nil
}
func (s *contextCLIBDD) importFails() error {
	if s.importErr == nil {
		return bddErrf("expected import to fail")
	}
	return nil
}
func (s *contextCLIBDD) mergedHasTurns(n int) error {
	if s.importErr != nil {
		return bddErrf("merge errored: %v", s.importErr)
	}
	c := s.readMerged()
	if len(c.Messages) != n {
		return bddErrf("merged has %d turns, want %d", len(c.Messages), n)
	}
	return nil
}
func (s *contextCLIBDD) mergedVerifies() error {
	if !s.readMerged().Verify() {
		return bddErrf("merged capsule must verify")
	}
	return nil
}
func (s *contextCLIBDD) readMerged() capsule.Capsule {
	data, _ := os.ReadFile(s.mergedPath)
	var c capsule.Capsule
	_ = json.Unmarshal(data, &c)
	return c
}
func (s *contextCLIBDD) exportSucceeds() error {
	if s.exportErr != nil {
		return bddErrf("expected export to succeed (gate lifted), got %v", s.exportErr)
	}
	return nil
}

// ctxBDDErr is a readable godog assertion failure (mirrors the other packages' bddErr).
type ctxBDDErr string

func (e ctxBDDErr) Error() string      { return string(e) }
func bddErrf(f string, a ...any) error { return ctxBDDErr(fmt.Sprintf(f, a...)) }

func TestContextCLIBDD(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate the operator key
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &contextCLIBDD{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = contextCLIBDD{dir: t.TempDir()}
				return ctx, nil
			})
			sc.Step(`^a draft with turns "([^"]*)"$`, st.writeDraft)
			sc.Step(`^a draft carrying tool_calls$`, st.writeToolCallDraft)
			sc.Step(`^a guest draft with turns "([^"]*)"$`, st.writeGuestDraft)
			sc.Step(`^I export it to a capsule file$`, st.exportToCapsule)
			sc.Step(`^I export it to a capsule file as the base thread$`, st.exportAsBase)
			sc.Step(`^I export the guest draft to a capsule file$`, st.exportGuest)
			sc.Step(`^I import the capsule file$`, st.importCapsule)
			sc.Step(`^the capsule file is tampered on disk$`, st.tamperCapsuleOnDisk)
			sc.Step(`^I import the guest capsule into the base thread$`, st.importGuestIntoBase)
			sc.Step(`^the import reports (\d+) turns$`, st.importReportsTurns)
			sc.Step(`^the import reports it verified$`, st.importReportsVerified)
			sc.Step(`^the import fails$`, st.importFails)
			sc.Step(`^the merged capsule has (\d+) turns$`, st.mergedHasTurns)
			sc.Step(`^the merged capsule verifies$`, st.mergedVerifies)
			sc.Step(`^the export succeeds$`, st.exportSucceeds)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/capsule/cli.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("context CLI behavior scenarios failed (see godog output above)")
	}
}
