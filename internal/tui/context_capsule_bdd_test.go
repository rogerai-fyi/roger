package tui

// context_capsule_bdd_test.go wires godog for features/capsule/handoff.feature: it drives
// the per-turn ring + export/merge model methods over REAL ed25519 (the operator key,
// isolated to a throwaway XDG_CONFIG_HOME), mirroring the proven BDD pattern.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/client"
)

type handoffBDD struct {
	m         model
	workdir   string
	exported  capsule.Capsule
	exportErr error
	recall    int
}

func (s *handoffBDD) freshSession() error {
	s.m = model{connected: &offer{Model: "test-model"}}
	s.workdir = ""
	return nil
}

// userSaysStationReplies records a user turn then an assistant turn, mirroring the two
// CHANNEL feed points (the send path + the chatMsg handler).
func (s *handoffBDD) userSaysStationReplies(user, reply string) error {
	s.m.recordTurn("user", user, "user", nil, nil)
	mdl, prov := s.m.channelModelProvider("test-provider")
	s.m.recordTurn("assistant", reply, s.m.channelAgent(), mdl, prov)
	return nil
}

func (s *handoffBDD) ringHasTurns(n int) error {
	if len(s.m.ring) != n {
		return hbErr(fmt.Sprintf("ring has %d turns, want %d", len(s.m.ring), n))
	}
	return nil
}
func (s *handoffBDD) ringTurnIndexes(csv string) error {
	var got []string
	for _, msg := range s.m.ring {
		got = append(got, strconv.Itoa(msg.XRoger.Turn))
	}
	if j := strings.Join(got, ","); j != csv {
		return hbErr("ring indexes = " + j + ", want " + csv)
	}
	return nil
}

func (s *handoffBDD) exportCapsule() error {
	s.exported, s.exportErr = s.m.exportContextCapsule(false)
	return s.exportErr
}
func (s *handoffBDD) exportCapsuleStranger() error {
	s.exported, s.exportErr = s.m.exportContextCapsule(true)
	return s.exportErr
}
func (s *handoffBDD) exportedVerifies() error {
	if !s.exported.Verify() {
		return hbErr("exported capsule must verify")
	}
	return nil
}
func (s *handoffBDD) exportedHasTurns(n int) error {
	if len(s.exported.Messages) != n {
		return hbErr(fmt.Sprintf("exported has %d turns, want %d", len(s.exported.Messages), n))
	}
	return nil
}
func (s *handoffBDD) exportedOwnerIsOperator() error {
	if s.exported.Meta.OwnerPubkey != client.UserPubHex() {
		return hbErr("exported owner is not the operator key")
	}
	return nil
}
func (s *handoffBDD) exportedRedaction(v string) error {
	if s.exported.Redaction != v {
		return hbErr("redaction = " + s.exported.Redaction + ", want " + v)
	}
	return nil
}

func (s *handoffBDD) writeHandoff() error {
	s.workdir = handoffTestDir()
	_, err := s.m.writeHandoffCapsule(s.workdir)
	return err
}

// guestAppendsReturnCapsule simulates the guest: it imports the handoff capsule, appends a
// turn signed with the SAME owner key (a same-owner local guest), and leaves a return file.
func (s *handoffBDD) guestAppendsReturnCapsule() error {
	raw, err := os.ReadFile(filepath.Join(s.workdir, handoffDir, handoffCapsuleFile))
	if err != nil {
		return err
	}
	base, err := capsule.Import(raw)
	if err != nil {
		return err
	}
	base.Messages = append(base.Messages, capsule.Message{Role: "assistant", Content: "guest turn", XRoger: capsule.XRoger{Turn: base.Thread.BaseWatermark, Agent: "opencode", TS: 9}})
	base.Thread.BaseWatermark++
	base.Meta.ExportedBy = "opencode"
	base.Sign(client.LoadOrCreateUserKey())
	out, _ := base.Marshal()
	return os.WriteFile(filepath.Join(s.workdir, handoffDir, recallCapsuleFile), out, 0o600)
}
func (s *handoffBDD) djReadsReturn() error {
	s.recall, s.exportErr = s.m.readRecallCapsule(s.workdir)
	return s.exportErr
}
func (s *handoffBDD) djReadsEmptyWorkdir() error {
	s.recall, s.exportErr = s.m.readRecallCapsule(handoffTestDir())
	return s.exportErr
}
func (s *handoffBDD) recallAdded(n int) error {
	if s.recall != n {
		return hbErr(fmt.Sprintf("recall added %d turns, want %d", s.recall, n))
	}
	return nil
}

type handoffBDDErr string

func (e handoffBDDErr) Error() string { return string(e) }
func hbErr(s string) error            { return handoffBDDErr(s) }

var handoffTestDirFn func() string

func handoffTestDir() string { return handoffTestDirFn() }

func TestHandoffBDD(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate the operator key
	handoffTestDirFn = t.TempDir
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &handoffBDD{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = handoffBDD{}
				return ctx, nil
			})
			sc.Step(`^a fresh session$`, st.freshSession)
			sc.Step(`^the user says "([^"]*)" and the station replies "([^"]*)"$`, st.userSaysStationReplies)
			sc.Step(`^the ring has (\d+) turns$`, st.ringHasTurns)
			sc.Step(`^the ring turn indexes are "([^"]*)"$`, st.ringTurnIndexes)
			sc.Step(`^I export the context capsule$`, st.exportCapsule)
			sc.Step(`^I export the context capsule for a stranger$`, st.exportCapsuleStranger)
			sc.Step(`^the exported capsule verifies$`, st.exportedVerifies)
			sc.Step(`^the exported capsule has (\d+) turns$`, st.exportedHasTurns)
			sc.Step(`^the exported capsule owner is the operator key$`, st.exportedOwnerIsOperator)
			sc.Step(`^the exported capsule redaction is "([^"]*)"$`, st.exportedRedaction)
			sc.Step(`^I write the handoff capsule to the guest workdir$`, st.writeHandoff)
			sc.Step(`^the guest appends a turn and leaves a return capsule$`, st.guestAppendsReturnCapsule)
			sc.Step(`^the DJ reads the return capsule$`, st.djReadsReturn)
			sc.Step(`^the DJ reads the return capsule from an empty workdir$`, st.djReadsEmptyWorkdir)
			sc.Step(`^the recall added (\d+) turns$`, st.recallAdded)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/capsule/handoff.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("handoff behavior scenarios failed (see godog output above)")
	}
}
