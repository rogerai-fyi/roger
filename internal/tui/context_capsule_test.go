package tui

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/client"
)

// capsuleForkFixture is a signed return capsule that rewrites turn 0 with different content
// (a fork), for the append-only rejection path. It is signed by an ephemeral key so the
// signature verifies and Merge reaches the fork check.
func capsuleForkFixture(t *testing.T) []byte {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	c := capsule.Capsule{
		Capsule:  capsule.Version,
		Thread:   capsule.Thread{BaseWatermark: 1},
		Messages: []capsule.Message{{Role: "user", Content: "DIFFERENT", XRoger: capsule.XRoger{Turn: 0, Agent: "user", TS: 1}}},
	}
	c.Sign(priv)
	raw, _ := c.Marshal()
	return raw
}

// TestRecordTurnBoundedAndSkipsEmpty: empty turns are skipped; the ring is bounded to
// contextRingCap with monotonically increasing turn indexes preserved.
func TestRecordTurnBoundedAndSkipsEmpty(t *testing.T) {
	var m model
	m.recordTurn("", "", "user", nil, nil) // no-op
	if len(m.ring) != 0 || m.ringTurn != 0 {
		t.Fatal("empty turn must be a no-op")
	}
	for i := 0; i < contextRingCap+50; i++ {
		m.recordTurn("user", "t", "user", nil, nil)
	}
	if len(m.ring) != contextRingCap {
		t.Errorf("ring len = %d, want %d (bounded)", len(m.ring), contextRingCap)
	}
	if m.ringTurn != contextRingCap+50 {
		t.Errorf("ringTurn = %d, want %d (index preserved past the cap)", m.ringTurn, contextRingCap+50)
	}
	// The oldest turn aged out; the first retained turn's index is (total - cap).
	if got := m.ring[0].XRoger.Turn; got != 50 {
		t.Errorf("oldest retained turn index = %d, want 50", got)
	}
}

// TestChannelProvenance: channelAgent + channelModelProvider reflect the tuned band, and
// carry nil pointers (null in the capsule) when unknown.
func TestChannelProvenance(t *testing.T) {
	var m model
	if m.channelAgent() != "roger" {
		t.Error("untuned agent must be 'roger'")
	}
	if mdl, prov := m.channelModelProvider(""); mdl != nil || prov != nil {
		t.Error("untuned + no provider must be nil pointers")
	}
	m.connected = &offer{Model: "m1"}
	if m.channelAgent() != "roger:m1" {
		t.Errorf("tuned agent = %q, want roger:m1", m.channelAgent())
	}
	mdl, prov := m.channelModelProvider("openai")
	if mdl == nil || *mdl != "m1" || prov == nil || *prov != "openai" {
		t.Errorf("model/provider = %v/%v, want m1/openai", mdl, prov)
	}
}

// TestContextThreadIDStable: the thread id is minted once and stays stable.
func TestContextThreadIDStable(t *testing.T) {
	var m model
	a := m.contextThreadID()
	if a == "" || a != m.contextThreadID() {
		t.Error("thread id must be non-empty and stable")
	}
}

// TestWriteHandoffEmptyRingNoFile: an empty ring writes no capsule (nothing to hand off).
func TestWriteHandoffEmptyRingNoFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var m model
	dir := t.TempDir()
	path, err := m.writeHandoffCapsule(dir)
	if err != nil || path != "" {
		t.Errorf("empty ring: path=%q err=%v, want no file", path, err)
	}
	if _, err := os.Stat(filepath.Join(dir, handoffDir)); !os.IsNotExist(err) {
		t.Error("empty ring must not create the handoff dir")
	}
}

// TestWriteHandoffMkdirError: an unwritable workdir surfaces an error (best-effort caller
// narrates it).
func TestWriteHandoffMkdirError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var m model
	m.recordTurn("user", "hi", "user", nil, nil)
	// A regular file as the "workdir" makes MkdirAll(<file>/.roger) fail.
	f := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	if _, err := m.writeHandoffCapsule(f); err == nil {
		t.Error("mkdir under a file must error")
	}
}

// TestReadRecallErrors: a missing file is a clean 0; a malformed return capsule errors.
func TestReadRecallErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var m model
	dir := t.TempDir()
	if n, err := m.readRecallCapsule(dir); n != 0 || err != nil {
		t.Errorf("missing return: n=%d err=%v, want 0/nil", n, err)
	}
	rdir := filepath.Join(dir, handoffDir)
	_ = os.MkdirAll(rdir, 0o700)
	_ = os.WriteFile(filepath.Join(rdir, recallCapsuleFile), []byte("{bad"), 0o600)
	if _, err := m.readRecallCapsule(dir); err == nil {
		t.Error("malformed return capsule must error")
	}
}

// TestRandHex: returns 2n hex chars.
func TestRandHex(t *testing.T) {
	if got := randHex(8); len(got) != 16 {
		t.Errorf("randHex(8) len = %d, want 16", len(got))
	}
}

// TestMergeReturnForked: a return capsule that forks an existing turn is rejected.
func TestMergeReturnForked(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var m model
	m.recordTurn("user", "hi", "user", nil, nil) // ring: turn 0 = user/hi
	// Build a signed return capsule that rewrites turn 0 with different content.
	fork := capsuleForkFixture(t)
	if _, err := m.mergeReturnCapsule(fork); err == nil {
		t.Error("forked return capsule must be rejected")
	}
}

// TestWriteHandoffWriteFileError: a pre-existing directory at the capsule path makes the
// WriteFile fail after the dir is created.
func TestWriteHandoffWriteFileError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var m model
	m.recordTurn("user", "hi", "user", nil, nil)
	wd := t.TempDir()
	blocking := filepath.Join(wd, handoffDir, handoffCapsuleFile)
	_ = os.MkdirAll(blocking, 0o700) // the capsule path is a directory -> WriteFile errors
	if _, err := m.writeHandoffCapsule(wd); err == nil {
		t.Error("WriteFile onto a directory must error")
	}
}

// TestReadRecallReadError: a directory at the return path yields a non-NotExist read error.
func TestReadRecallReadError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var m model
	wd := t.TempDir()
	_ = os.MkdirAll(filepath.Join(wd, handoffDir, recallCapsuleFile), 0o700)
	if _, err := m.readRecallCapsule(wd); err == nil {
		t.Error("reading a directory as the return file must error")
	}
}

// TestOperatorHandoffCarriesCapsuleE2E drives a real local handoff through onOperatorExec /
// onOperatorDone and proves the conversation is carried: onOperatorExec writes the signed
// handoff capsule to the guest workdir, and onOperatorDone merges the guest's return
// capsule back into the ring append-only. This is the "live E2E" for Task B.
func TestOperatorHandoffCarriesCapsuleE2E(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wd := t.TempDir()
	saveWd := operatorWorkdir
	operatorWorkdir = func() string { return wd }
	t.Cleanup(func() { operatorWorkdir = saveWd })

	m, _, _ := opRegressionSeed(t)
	m.recordTurn("user", "hello", "user", nil, nil)
	m.recordTurn("assistant", "hi there", "roger:m", nil, nil)

	var tm tea.Model = m
	tm, _ = tm.(model).runAgentCommand("/operator opencode")
	tm, _ = tm.Update(keyMsg("y")) // accept the plate -> staged
	tm, _ = tm.Update(operatorExecMsg{})

	// onOperatorExec wrote the handoff capsule for the guest.
	handoffPath := filepath.Join(wd, handoffDir, handoffCapsuleFile)
	raw, err := os.ReadFile(handoffPath)
	if err != nil {
		t.Fatalf("handoff capsule not written: %v", err)
	}
	c, err := capsule.Import(raw)
	if err != nil || !c.Verify() || len(c.Messages) != 2 {
		t.Fatalf("handoff capsule bad: err=%v turns=%d", err, len(c.Messages))
	}

	// The guest appends a turn and leaves a return capsule (same-owner: signed with the
	// operator key, which the sandboxed user.key is).
	c.Messages = append(c.Messages, capsule.Message{Role: "assistant", Content: "guest work", XRoger: capsule.XRoger{Turn: c.Thread.BaseWatermark, Agent: "opencode", TS: 9}})
	c.Thread.BaseWatermark++
	c.Sign(client.LoadOrCreateUserKey())
	retRaw, _ := c.Marshal()
	_ = os.WriteFile(filepath.Join(wd, handoffDir, recallCapsuleFile), retRaw, 0o600)

	tm, _ = tm.Update(operatorDoneMsg{})
	if got := len(tm.(model).ring); got != 3 {
		t.Fatalf("after recall the ring has %d turns, want 3 (guest turn merged append-only)", got)
	}
}
