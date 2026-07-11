package capsule

import "testing"

// goldenInput builds the brief's golden fixture capsule (unsigned). exportedBy is the
// only value that differs by producer ("roger-cli" vs "roger-ios"); everything else is
// identical. The owner_pubkey is a literal "aa" in the vector (not derived from a key)
// because this test pins the CANONICAL BYTES, not a signature.
func goldenInput(exportedBy string) Capsule {
	return Capsule{
		Capsule:   Version,
		ID:        "cap_x",
		Thread:    Thread{OriginThreadID: "t1", Title: "Hi", BaseWatermark: 1},
		Redaction: "full",
		Summary:   Summary{Text: "hi", ProducedBy: "none", AsOfTurn: 1},
		Memory:    Memory{Notes: "", Facts: nil},
		Messages: []Message{{
			Role:    "user",
			Content: "hi",
			XRoger:  XRoger{Turn: 0, Agent: "user", Model: nil, Provider: nil, TS: 100},
		}},
		Meta: Meta{ToolsUsed: nil, ExportedBy: exportedBy, CreatedAt: 100, OwnerPubkey: "aa"},
	}
}

// TestGoldenVector is the ONE load-bearing interop contract: canonical() must reproduce
// the brief's golden bytes byte-for-byte, for BOTH producers. An app-signed capsule can
// only verify in Go if these bytes match token-for-token (RogerAI/Services/CapsuleWire.swift).
func TestGoldenVector(t *testing.T) {
	const cliGolden = `{"capsule":"roger.context.v1","id":"cap_x","thread":{"origin_thread_id":"t1","title":"Hi","base_watermark":1},"redaction":"full","summary":{"text":"hi","produced_by":"none","as_of_turn":1},"memory":{"notes":"","facts":[]},"messages":[{"role":"user","content":"hi","x_roger":{"turn":0,"agent":"user","model":null,"provider":null,"ts":100}}],"meta":{"tools_used":[],"exported_by":"roger-cli","created_at":100,"owner_pubkey":"aa"}}`
	const iosGolden = `{"capsule":"roger.context.v1","id":"cap_x","thread":{"origin_thread_id":"t1","title":"Hi","base_watermark":1},"redaction":"full","summary":{"text":"hi","produced_by":"none","as_of_turn":1},"memory":{"notes":"","facts":[]},"messages":[{"role":"user","content":"hi","x_roger":{"turn":0,"agent":"user","model":null,"provider":null,"ts":100}}],"meta":{"tools_used":[],"exported_by":"roger-ios","created_at":100,"owner_pubkey":"aa"}}`

	if got := string(goldenInput("roger-cli").canonical()); got != cliGolden {
		t.Errorf("roger-cli canonical mismatch\n got: %s\nwant: %s", got, cliGolden)
	}
	if got := string(goldenInput("roger-ios").canonical()); got != iosGolden {
		t.Errorf("roger-ios canonical mismatch\n got: %s\nwant: %s", got, iosGolden)
	}
}
