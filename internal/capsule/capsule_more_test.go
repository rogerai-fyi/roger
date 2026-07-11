package capsule

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

// keypair returns an ephemeral ed25519 keypair for a test. The unit tests deliberately
// use throwaway keys (never the CLI identity) so a passing test can never depend on, or
// mint, the on-disk user.key.
func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// TestCanonicalFieldOrderAndForm exhaustively pins the canonical shape: fixed field
// order, goString escaping of < > &, literal null (not omit) for nil model/provider, a
// real model/provider string, plain-decimal numbers (incl negative), non-empty
// facts/tools arrays, and the conditional tool_calls slot present vs absent.
func TestCanonicalFieldOrderAndForm(t *testing.T) {
	tests := []struct {
		name string
		in   Capsule
		want string
	}{
		{
			name: "real model/provider strings + non-empty arrays",
			in: Capsule{
				Capsule:   Version,
				ID:        "cap_1",
				Thread:    Thread{OriginThreadID: "t1", Title: "a b", BaseWatermark: 2},
				Redaction: "full",
				Summary:   Summary{Text: "xy", ProducedBy: "operator:m", AsOfTurn: 2},
				Memory:    Memory{Notes: "n", Facts: []string{"f1", "f2"}},
				Messages: []Message{{
					Role: "user", Content: "hey",
					XRoger: XRoger{Turn: 1, Agent: "user", Model: strp("gpt"), Provider: strp("openai"), TS: 100},
				}},
				Meta: Meta{ToolsUsed: []string{"bash", "read"}, ExportedBy: "roger-cli", CreatedAt: 100, OwnerPubkey: "aa"},
			},
			want: `{"capsule":"roger.context.v1","id":"cap_1","thread":{"origin_thread_id":"t1","title":"a b","base_watermark":2},"redaction":"full","summary":{"text":"xy","produced_by":"operator:m","as_of_turn":2},"memory":{"notes":"n","facts":["f1","f2"]},"messages":[{"role":"user","content":"hey","x_roger":{"turn":1,"agent":"user","model":"gpt","provider":"openai","ts":100}}],"meta":{"tools_used":["bash","read"],"exported_by":"roger-cli","created_at":100,"owner_pubkey":"aa"}}`,
		},
		{
			name: "negative numbers + empty everything + null model/provider",
			in: Capsule{
				Capsule:  Version,
				ID:       "cap_2",
				Thread:   Thread{BaseWatermark: -1},
				Summary:  Summary{AsOfTurn: -5},
				Messages: []Message{{Role: "assistant", Content: "", XRoger: XRoger{Turn: -2, Agent: "roger:m", TS: -9}}},
				Meta:     Meta{},
			},
			want: `{"capsule":"roger.context.v1","id":"cap_2","thread":{"origin_thread_id":"","title":"","base_watermark":-1},"redaction":"","summary":{"text":"","produced_by":"","as_of_turn":-5},"memory":{"notes":"","facts":[]},"messages":[{"role":"assistant","content":"","x_roger":{"turn":-2,"agent":"roger:m","model":null,"provider":null,"ts":-9}}],"meta":{"tools_used":[],"exported_by":"","created_at":0,"owner_pubkey":""}}`,
		},
		{
			name: "conditional tool_calls slot present (verbatim raw)",
			in: Capsule{
				Capsule:  Version,
				ID:       "cap_3",
				Messages: []Message{{Role: "assistant", Content: "c", ToolCalls: json.RawMessage(`[{"id":"1"}]`), XRoger: XRoger{Turn: 0, Agent: "roger:m", TS: 1}}},
				Meta:     Meta{OwnerPubkey: "bb"},
			},
			want: `{"capsule":"roger.context.v1","id":"cap_3","thread":{"origin_thread_id":"","title":"","base_watermark":0},"redaction":"","summary":{"text":"","produced_by":"","as_of_turn":0},"memory":{"notes":"","facts":[]},"messages":[{"role":"assistant","content":"c","tool_calls":[{"id":"1"}],"x_roger":{"turn":0,"agent":"roger:m","model":null,"provider":null,"ts":1}}],"meta":{"tools_used":[],"exported_by":"","created_at":0,"owner_pubkey":"bb"}}`,
		},
		{
			name: "multiple messages join with a comma, no trailing space",
			in: Capsule{
				Capsule: Version, ID: "cap_4",
				Messages: []Message{
					{Role: "user", Content: "a", XRoger: XRoger{Turn: 0, Agent: "user", TS: 1}},
					{Role: "assistant", Content: "b", XRoger: XRoger{Turn: 1, Agent: "roger:m", TS: 2}},
				},
				Meta: Meta{OwnerPubkey: "cc"},
			},
			want: `{"capsule":"roger.context.v1","id":"cap_4","thread":{"origin_thread_id":"","title":"","base_watermark":0},"redaction":"","summary":{"text":"","produced_by":"","as_of_turn":0},"memory":{"notes":"","facts":[]},"messages":[{"role":"user","content":"a","x_roger":{"turn":0,"agent":"user","model":null,"provider":null,"ts":1}},{"role":"assistant","content":"b","x_roger":{"turn":1,"agent":"roger:m","model":null,"provider":null,"ts":2}}],"meta":{"tools_used":[],"exported_by":"","created_at":0,"owner_pubkey":"cc"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(tc.in.canonical()); got != tc.want {
				t.Errorf("canonical mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestCanonicalExcludesSig proves the signature field is never part of the canonical
// bytes: setting Sig cannot change canonical() (so a sig can sign over sig-cleared bytes).
func TestCanonicalExcludesSig(t *testing.T) {
	c := goldenInput("roger-cli")
	before := string(c.canonical())
	c.Sig = "deadbeef"
	if after := string(c.canonical()); after != before {
		t.Errorf("Sig leaked into canonical bytes:\n before: %s\n after:  %s", before, after)
	}
}

// TestSignVerifyRoundtrip: a freshly signed capsule verifies, sets owner_pubkey from the
// key, and is deterministic per RFC-8032 (same key + bytes -> same sig) - but the test
// asserts the sig VERIFIES, it does not pin a fixed sig value.
func TestSignVerifyRoundtrip(t *testing.T) {
	pub, priv := keypair(t)
	c := goldenInput("roger-cli")
	if c.Verify() {
		t.Fatal("unsigned capsule must not verify")
	}
	c.Sign(priv)
	if c.Meta.OwnerPubkey != hex.EncodeToString(pub) {
		t.Errorf("Sign must set owner_pubkey to the signing key")
	}
	if !c.Verify() {
		t.Error("signed capsule must verify with its own key")
	}
	// A different key must not verify.
	otherPub, _ := keypair(t)
	c.Meta.OwnerPubkey = hex.EncodeToString(otherPub)
	if c.Verify() {
		t.Error("capsule must not verify under a different owner_pubkey")
	}
}

// TestVerifyMalformedInputs: garbage pubkey/sig are rejected, never panic.
func TestVerifyMalformedInputs(t *testing.T) {
	_, priv := keypair(t)
	c := goldenInput("roger-cli")
	c.Sign(priv)
	for _, bad := range []struct {
		name, pub, sig string
	}{
		{"non-hex pubkey", "zz", c.Sig},
		{"short pubkey", "aa", c.Sig},
		{"non-hex sig", c.Meta.OwnerPubkey, "zz"},
		{"short sig", c.Meta.OwnerPubkey, "aa"},
		{"empty sig", c.Meta.OwnerPubkey, ""},
	} {
		t.Run(bad.name, func(t *testing.T) {
			tc := c
			tc.Meta.OwnerPubkey, tc.Sig = bad.pub, bad.sig
			if tc.Verify() {
				t.Error("malformed input must not verify")
			}
		})
	}
}

// TestTamperMatrix: every mutation of a signed capsule breaks verification, because the
// sig covers the canonical bytes of every field. This includes the STRANGER escalation:
// flipping a summary-only capsule's redaction to "full" cannot re-verify without the key.
func TestTamperMatrix(t *testing.T) {
	_, priv := keypair(t)
	base, err := Export(SummaryOnly(Draft{
		ID:       "cap_s",
		Thread:   Thread{OriginThreadID: "t1", Title: "T", BaseWatermark: 3},
		Summary:  Summary{Text: "so far", ProducedBy: "operator:m", AsOfTurn: 2},
		Memory:   Memory{Notes: "secret", Facts: []string{"private"}},
		Messages: []Message{{Role: "user", Content: "current", XRoger: XRoger{Turn: 2, Agent: "user", TS: 9}}},
	}), priv, "roger-cli", func() int64 { return 100 })
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !base.Verify() {
		t.Fatal("base summary-only capsule must verify")
	}
	if base.Redaction != "summary" {
		t.Fatalf("SummaryOnly must set redaction=summary, got %q", base.Redaction)
	}

	tampers := map[string]func(*Capsule){
		"escalate redaction summary->full": func(c *Capsule) { c.Redaction = "full" },
		"flip a message content":           func(c *Capsule) { c.Messages[0].Content = "evil" },
		"flip a message role":              func(c *Capsule) { c.Messages[0].Role = "system" },
		"append a forged turn": func(c *Capsule) {
			c.Messages = append(c.Messages, Message{Role: "assistant", Content: "forged", XRoger: XRoger{Turn: 3, Agent: "roger:m", TS: 10}})
		},
		"change the title":        func(c *Capsule) { c.Thread.Title = "other" },
		"change the summary text": func(c *Capsule) { c.Summary.Text = "rewritten" },
		"change created_at":       func(c *Capsule) { c.Meta.CreatedAt = 200 },
		"change exported_by":      func(c *Capsule) { c.Meta.ExportedBy = "roger-ios" },
		"change base_watermark":   func(c *Capsule) { c.Thread.BaseWatermark = 99 },
	}
	for name, mut := range tampers {
		t.Run(name, func(t *testing.T) {
			tc := base
			// deep-copy the message slice so a mutation does not bleed across subtests
			tc.Messages = append([]Message(nil), base.Messages...)
			mut(&tc)
			if tc.Verify() {
				t.Errorf("tampered capsule (%s) must not verify", name)
			}
		})
	}
}

// TestCanonicalHTMLEscapes proves goString HTML-escapes < > & (matching the app's
// goString / the share receipts). A field carrying those runes must emit NO raw < > &
// in the canonical bytes (each is escaped to a \u00XX sequence), and the escaping
// backslash must appear. This is checked on runes so no fragile \u literal is needed.
func TestCanonicalHTMLEscapes(t *testing.T) {
	c := Capsule{Capsule: Version, Thread: Thread{Title: "<a & b>"}}
	got := string(c.canonical())
	for _, raw := range []rune{'<', '>', '&'} {
		if strings.ContainsRune(got, raw) {
			t.Errorf("canonical must not contain a RAW %q (must be HTML-escaped): %s", raw, got)
		}
	}
	if !strings.ContainsRune(got, '\\') {
		t.Errorf("canonical must contain a \\u escape for the escaped runes: %s", got)
	}
	// The exact escape must equal encoding/json's (the app's goString is json.Marshal),
	// built from the marshaler itself rather than a hand-typed literal.
	jm, _ := json.Marshal(c.Thread.Title)
	if !strings.Contains(got, string(jm)) {
		t.Errorf("canonical title must equal json.Marshal(title)=%s: %s", jm, got)
	}
}
