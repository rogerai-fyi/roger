package protocol

import "strings"

// rc.go is the wire protocol for /remote-control (BASE STATION, v5.0.0): a live embedded-
// agent session on a HOST machine, continuable from any other surface logged into the SAME
// account. The broker is a content-blind relay — it moves RCFrames between the host and the
// attached viewers and NEVER persists a frame. See rogerai-internal-docs/REMOTE-CONTROL-DESIGN.md.
//
// The link SECRET reuses the private-band frequency-code crypto verbatim (Crockford tail,
// sha256-at-rest, shown once). NewRCLinkCode wraps NewBandCode with an "RC "-prefixed cosmetic
// display so a session link can never be visually confused with a station band. The tail
// hashes with the SAME BandCodeHash, so the broker's constant-work lookup is shared.

// RC frame kinds (RCFrame.Kind). These are the events the host TEES out of its agent loop and
// the broker fans out to every attached viewer (plus the local TUI). None is ever stored.
const (
	RCKindUser        = "user"         // a typed turn (from any surface); Origin names the sender
	RCKindAssistant   = "assistant"    // an assistant message chunk/line from the host's model
	RCKindToolCall    = "tool_call"    // the agent is invoking a tool (Tool/Args set)
	RCKindToolResult  = "tool_result"  // a tool returned (Tool set, Text = summary)
	RCKindFinal       = "final"        // the assistant's turn completed
	RCKindError       = "error"        // an error surfaced on the host (Text)
	RCKindConfirmReq  = "confirm_req"  // a mutating tool awaits y/N (ConfirmID/Tool/Args set)
	RCKindConfirmDone = "confirm_done" // a confirm was answered (ConfirmID/Approve/Origin set)
	RCKindStatus      = "status"       // host online/offline transition (HostUp set)
	RCKindBackfill    = "backfill"     // a transcript-snapshot frame addressed to ONE viewer
	RCKindEnded       = "ended"        // the session was disabled/revoked; terminal
)

// RC inbound kinds (RCInbound.Kind): what a viewer (or the broker) sends TO the host.
const (
	RCInTurn      = "turn"      // inject a user turn (Text)
	RCInConfirm   = "confirm"   // answer a pending confirm (ConfirmID/Approve)
	RCInInterrupt = "interrupt" // cancel the in-flight turn
	RCInBackfill  = "backfill"  // ask the host for a transcript snapshot for Viewer
)

// RESERVED operator wire names (Guest Operators Phase 2, founder ruling 7, 2026-07-07).
// v1 attaches NO behavior to any of these: a guest-operator handoff is announced with plain
// RCKindStatus frames (carrying RCFrame.Operator additively). The names are reserved NOW so
// old hosts and future surfaces can never collide on them later (the persistent-state
// lesson: additive, idempotent wire evolution).
const (
	RCKindOperatorStatus = "operator_status"  // future dedicated operator-state frame kind
	RCInOperatorHandoff  = "operator_handoff" // future remote-initiated handoff inbound kind
	RCInOperatorRecall   = "operator_recall"  // future remote "give the DJ the mic back" inbound kind
)

// RCFrame is one broker-relayed event on a remote-control session. NEVER persisted at rest;
// it lives only in transit and in the broker's bounded transient replay ring.
type RCFrame struct {
	Seq       uint64 `json:"seq"`                  // per-session monotonic (broker-assigned)
	TS        int64  `json:"ts"`                   // unix seconds
	Kind      string `json:"kind"`                 // one of the RCKind* constants
	Origin    string `json:"origin,omitempty"`     // "local" | device label (user / confirm_done)
	Text      string `json:"text,omitempty"`       // message / tool-result summary / error
	Tool      string `json:"tool,omitempty"`       // tool name (tool_call / tool_result / confirm_req)
	Args      string `json:"args,omitempty"`       // JSON-string tool args (tool_call / confirm_req)
	ConfirmID string `json:"confirm_id,omitempty"` // confirm_req / confirm_done correlation
	Approve   *bool  `json:"approve,omitempty"`    // confirm_done: the answer (pointer distinguishes unset)
	Viewer    string `json:"viewer,omitempty"`     // backfill: the ONE addressed viewer id (others skip)
	HostUp    *bool  `json:"host_up,omitempty"`    // status: host reachable? (pointer distinguishes unset)
	Operator  string `json:"operator,omitempty"`   // status: the guest operator at the desk (Phase 2, additive; "" = the DJ)
}

// RCInbound is what a remote surface (or the broker itself, for backfill) sends TO the host.
type RCInbound struct {
	Kind      string `json:"kind"`                 // one of the RCIn* constants
	Text      string `json:"text,omitempty"`       // turn text
	ConfirmID string `json:"confirm_id,omitempty"` // confirm correlation
	Approve   bool   `json:"approve,omitempty"`    // confirm answer
	Origin    string `json:"origin"`               // device label of the sender (for the echoed user frame)
	Viewer    string `json:"viewer,omitempty"`     // backfill: who asked (host addresses the reply)
	TS        int64  `json:"ts"`
}

// rcDisplayPrefix marks a link display as a REMOTE-CONTROL code rather than a station band,
// so "RC 147.520 MHz · ••••-••••" is unmistakable in any roster. It is cosmetic only — the
// secret tail and its hash are identical to a band code, so BandCodeHash resolves both.
const rcDisplayPrefix = "RC "

// NewRCLinkCode mints a fresh session link secret. It returns the one-time full code (shown
// once to the host to save/share), a non-recoverable masked display safe to persist, and the
// canonical tail for hashing (via BandCodeHash). Thin wrapper over NewBandCode: same 40-bit
// Crockford tail, same hash discipline, only the display is RC-prefixed.
func NewRCLinkCode() (code, display, tail string) {
	code, display, tail = NewBandCode()
	return rcDisplayPrefix + code, rcDisplayPrefix + display, tail
}

// RCLinkShort returns the bare grouped tail ("8F3K-9M2Q") from a full link code, for the
// typeable short field + the /r/<code> deep link. Empty when the code carries no valid tail.
func RCLinkShort(code string) string {
	tail := CanonicalBandTail(strings.TrimPrefix(code, rcDisplayPrefix))
	if tail == "" {
		return ""
	}
	return tail[:4] + "-" + tail[4:]
}
