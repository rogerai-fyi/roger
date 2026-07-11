package capsule

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
)

func msg(turn int, role, content, agent string) Message {
	return Message{Role: role, Content: content, XRoger: XRoger{Turn: turn, Agent: agent, TS: int64(turn)}}
}

// signed builds a signed capsule with the given watermark + messages under priv.
func signed(t *testing.T, priv ed25519.PrivateKey, watermark int, msgs ...Message) Capsule {
	t.Helper()
	c := Capsule{
		Capsule:  Version,
		ID:       "cap",
		Thread:   Thread{OriginThreadID: "t1", Title: "T", BaseWatermark: watermark},
		Messages: msgs,
		Meta:     Meta{ToolsUsed: []string{}},
	}
	c.Sign(priv)
	return c
}

func turns(c Capsule) []int {
	var out []int
	for _, m := range c.Messages {
		out = append(out, m.XRoger.Turn)
	}
	return out
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestMergeIntoEmptyReproducesTranscript: merging a full capsule into an empty thread
// yields every turn in order (the golden turn 0 is NOT dropped by the watermark).
func TestMergeIntoEmptyReproducesTranscript(t *testing.T) {
	_, priv := keypair(t)
	incoming := signed(t, priv, 3, msg(0, "user", "hi", "user"), msg(1, "assistant", "yo", "roger:m"), msg(2, "user", "more", "user"))
	empty := Capsule{Capsule: Version, Thread: Thread{BaseWatermark: 0}}
	out, err := Merge(incoming, empty)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !eqInts(turns(out), []int{0, 1, 2}) {
		t.Errorf("turns = %v, want [0 1 2]", turns(out))
	}
	if out.Thread.BaseWatermark != 3 {
		t.Errorf("watermark = %d, want 3", out.Thread.BaseWatermark)
	}
	if out.Sig != "" {
		t.Error("merged capsule must clear Sig (re-sign on export)")
	}
}

// TestMergeAppendOnlyWatermark: only turns at/after into's watermark are appended, and a
// backdated turn below the watermark that into does not have is NOT inserted (append-only).
func TestMergeAppendOnlyWatermark(t *testing.T) {
	_, priv := keypair(t)
	into := signed(t, priv, 2, msg(0, "user", "a", "user"), msg(1, "assistant", "b", "roger:m"))
	// incoming has the DJ's 0,1 plus new 2,3 (guest turns) and a sneaky backdated -1.
	incoming := signed(t, priv, 2,
		msg(-1, "system", "backdated", "user"),
		msg(0, "user", "a", "user"),
		msg(1, "assistant", "b", "roger:m"),
		msg(2, "user", "c", "opencode"),
		msg(3, "assistant", "d", "opencode"),
	)
	out, err := Merge(incoming, into)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !eqInts(turns(out), []int{0, 1, 2, 3}) {
		t.Errorf("turns = %v, want [0 1 2 3] (backdated -1 rejected, 2/3 appended)", turns(out))
	}
	if out.Thread.BaseWatermark != 4 {
		t.Errorf("watermark = %d, want 4", out.Thread.BaseWatermark)
	}
}

// TestMergeDoubleDedups: merging the same capsule twice is idempotent.
func TestMergeDoubleDedups(t *testing.T) {
	_, priv := keypair(t)
	into := signed(t, priv, 0)
	incoming := signed(t, priv, 3, msg(0, "user", "a", "user"), msg(1, "assistant", "b", "roger:m"), msg(2, "user", "c", "opencode"))
	once, err := Merge(incoming, into)
	if err != nil {
		t.Fatalf("first Merge: %v", err)
	}
	twice, err := Merge(incoming, once)
	if err != nil {
		t.Fatalf("second Merge: %v", err)
	}
	if !eqInts(turns(twice), []int{0, 1, 2}) {
		t.Errorf("double-merge turns = %v, want [0 1 2]", turns(twice))
	}
}

// TestMergeDedupsWithinIncoming: an incoming capsule that repeats the SAME turn (same
// index + content) at/after the watermark appends it once (intra-set dedup).
func TestMergeDedupsWithinIncoming(t *testing.T) {
	_, priv := keypair(t)
	into := Capsule{Capsule: Version, Thread: Thread{BaseWatermark: 0}}
	incoming := signed(t, priv, 2,
		msg(0, "user", "a", "user"),
		msg(0, "user", "a", "user"), // exact duplicate of turn 0
		msg(1, "assistant", "b", "roger:m"),
	)
	out, err := Merge(incoming, into)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !eqInts(turns(out), []int{0, 1}) {
		t.Errorf("turns = %v, want [0 1] (duplicate turn 0 collapsed)", turns(out))
	}
}

// TestMergeNeverTruncates: an incoming capsule with FEWER turns than into never shrinks it.
func TestMergeNeverTruncates(t *testing.T) {
	_, priv := keypair(t)
	into := signed(t, priv, 3, msg(0, "user", "a", "user"), msg(1, "assistant", "b", "roger:m"), msg(2, "user", "c", "user"))
	incoming := signed(t, priv, 1, msg(0, "user", "a", "user")) // only the first turn
	out, err := Merge(incoming, into)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !eqInts(turns(out), []int{0, 1, 2}) {
		t.Errorf("turns = %v, want [0 1 2] (never truncated)", turns(out))
	}
}

// TestMergeRejectsUnverified: a capsule whose sig does not verify is rejected and into is
// left unchanged.
func TestMergeRejectsUnverified(t *testing.T) {
	_, priv := keypair(t)
	into := signed(t, priv, 1, msg(0, "user", "a", "user"))
	incoming := signed(t, priv, 2, msg(0, "user", "a", "user"), msg(1, "assistant", "TAMPERED", "roger:m"))
	incoming.Messages[1].Content = "evil" // break the sig without re-signing
	out, err := Merge(incoming, into)
	if !errors.Is(err, ErrUnverified) {
		t.Errorf("err = %v, want ErrUnverified", err)
	}
	if !eqInts(turns(out), []int{0}) {
		t.Errorf("into must be unchanged on reject, got %v", turns(out))
	}
}

// TestMergeRejectsForkedTurn: an incoming turn that collides with a present turn index but
// carries different content rejects the WHOLE capsule (ruling Q2); into is unchanged.
func TestMergeRejectsForkedTurn(t *testing.T) {
	_, priv := keypair(t)
	into := signed(t, priv, 2, msg(0, "user", "a", "user"), msg(1, "assistant", "b", "roger:m"))
	incoming := signed(t, priv, 2,
		msg(0, "user", "a", "user"),
		msg(1, "assistant", "FORKED", "roger:m"), // same turn 1, different content
		msg(2, "user", "c", "opencode"),          // a valid new turn that must NOT land
	)
	out, err := Merge(incoming, into)
	if !errors.Is(err, ErrForkedTurn) {
		t.Errorf("err = %v, want ErrForkedTurn", err)
	}
	if !eqInts(turns(out), []int{0, 1}) {
		t.Errorf("into must be unchanged on fork-reject, got %v", turns(out))
	}
}

// TestMergeRejectsUnknownVersion + tool_calls at the boundary.
func TestMergeRejectsBoundary(t *testing.T) {
	_, priv := keypair(t)
	into := signed(t, priv, 0)

	badVer := signed(t, priv, 1, msg(0, "user", "a", "user"))
	badVer.Capsule = "roger.context.v2"
	badVer.Sign(priv) // re-sign so it is a VALID sig of an UNKNOWN version
	if _, err := Merge(badVer, into); !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("unknown version: err = %v, want ErrUnknownVersion", err)
	}

	withTools := signed(t, priv, 0)
	withTools.Messages = []Message{{Role: "assistant", Content: "c", ToolCalls: json.RawMessage(`[{"id":"1"}]`), XRoger: XRoger{Turn: 0, Agent: "roger:m", TS: 1}}}
	withTools.Thread.BaseWatermark = 1
	withTools.Sign(priv)
	if _, err := Merge(withTools, into); !errors.Is(err, ErrToolCalls) {
		t.Errorf("tool_calls: err = %v, want ErrToolCalls", err)
	}
}

// TestExportRoundtrip: Export -> Marshal -> Import reproduces a verifying capsule, stamps
// the producer meta, and Merge into an empty thread reproduces the transcript.
func TestExportRoundtrip(t *testing.T) {
	_, priv := keypair(t)
	d := Draft{
		ID:       "cap_e",
		Thread:   Thread{OriginThreadID: "t1", Title: "T", BaseWatermark: 2},
		Summary:  Summary{Text: "s", ProducedBy: "none", AsOfTurn: 1},
		Messages: []Message{msg(0, "user", "hi", "user"), msg(1, "assistant", "yo", "roger:m")},
	}
	c, err := Export(d, priv, "roger-cli", func() int64 { return 100 })
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if c.Meta.ExportedBy != "roger-cli" || c.Meta.CreatedAt != 100 || c.Meta.OwnerPubkey == "" {
		t.Errorf("Export meta not stamped: %+v", c.Meta)
	}
	if !c.Verify() {
		t.Fatal("exported capsule must verify")
	}
	raw, err := c.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	imported, err := Import(raw)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	out, err := Merge(imported, Capsule{Capsule: Version})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !eqInts(turns(out), []int{0, 1}) {
		t.Errorf("round-trip turns = %v, want [0 1]", turns(out))
	}
}

// TestExportRejectsToolCalls: Export refuses a draft carrying tool_calls (ruling Q1) so a
// capsule that crosses the boundary can never contain them.
func TestExportRejectsToolCalls(t *testing.T) {
	_, priv := keypair(t)
	d := Draft{ID: "cap_t", Messages: []Message{{Role: "assistant", Content: "c", ToolCalls: json.RawMessage(`[{"id":"1"}]`), XRoger: XRoger{Turn: 0, Agent: "roger:m"}}}}
	if _, err := Export(d, priv, "roger-cli", nil); !errors.Is(err, ErrToolCalls) {
		t.Errorf("err = %v, want ErrToolCalls", err)
	}
}

// TestExportUsesTimeNowWhenNil: Export with a nil now stamps a real (non-zero) created_at.
func TestExportUsesTimeNowWhenNil(t *testing.T) {
	_, priv := keypair(t)
	c, err := Export(Draft{ID: "cap_n"}, priv, "roger-cli", nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if c.Meta.CreatedAt == 0 {
		t.Error("nil now must stamp a real created_at")
	}
}

// TestImportErrors: malformed JSON, unknown version, tool_calls, and a bad signature are
// each rejected by Import.
func TestImportErrors(t *testing.T) {
	_, priv := keypair(t)
	good, _ := Export(Draft{ID: "cap_i", Thread: Thread{BaseWatermark: 1}, Messages: []Message{msg(0, "user", "hi", "user")}}, priv, "roger-cli", func() int64 { return 1 })
	goodRaw, _ := good.Marshal()

	if _, err := Import([]byte("{not json")); err == nil {
		t.Error("malformed JSON must be rejected")
	}

	var badVer Capsule
	_ = json.Unmarshal(goodRaw, &badVer)
	badVer.Capsule = "vX"
	raw, _ := json.Marshal(badVer)
	if _, err := Import(raw); !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("err = %v, want ErrUnknownVersion", err)
	}

	var tampered Capsule
	_ = json.Unmarshal(goodRaw, &tampered)
	tampered.Messages[0].Content = "evil"
	raw2, _ := json.Marshal(tampered)
	if _, err := Import(raw2); !errors.Is(err, ErrUnverified) {
		t.Errorf("err = %v, want ErrUnverified", err)
	}
}

// TestSummaryOnly: drops memory, sets redaction=summary, keeps only the current (last)
// turn; and is a no-op on an empty message set (no panic).
func TestSummaryOnly(t *testing.T) {
	d := Draft{
		Redaction: "full",
		Memory:    Memory{Notes: "secret", Facts: []string{"x"}},
		Messages:  []Message{msg(0, "user", "old", "user"), msg(1, "assistant", "mid", "roger:m"), msg(2, "user", "current", "user")},
	}
	got := SummaryOnly(d)
	if got.Redaction != "summary" {
		t.Errorf("redaction = %q, want summary", got.Redaction)
	}
	if got.Memory.Notes != "" || len(got.Memory.Facts) != 0 {
		t.Errorf("memory must be dropped, got %+v", got.Memory)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "current" {
		t.Errorf("must keep only the current turn, got %+v", got.Messages)
	}
	if n := len(SummaryOnly(Draft{}).Messages); n != 0 {
		t.Errorf("empty draft must stay empty, got %d messages", n)
	}
}
