package client

// rc_statusline_test.go pins OperatorStatusLine, the ONE Go formatter that renders an
// RCKindStatus frame as the piecewise-degrading viewer line shared by every Go surface
// (the TUI transcript and the `roger remote` CLI viewer), so the "<op> has the mic on
// <model> · $<spend>" copy can never drift between them (the web console mirrors it in
// private.js). It is content-blind: only the operator name plus the additive Model/Spend
// metadata ever ride the line - never guest content.

import (
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestOperatorStatusLine pins the exact piecewise-degrade copy for every permutation:
// enriched (model+spend), model-only (drops the money), spend-only (drops the on-clause),
// bare operator (the short handoff line, never the frame's full sentence), the DJ-back /
// operator-less plain-text return, and the empty "render nothing" frame.
func TestOperatorStatusLine(t *testing.T) {
	const glyph = "◉"
	cases := []struct {
		name string
		f    protocol.RCFrame
		want string
	}{
		{
			"enriched with model and spend",
			protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "opencode", Model: "gpt-oss-120b", Spend: 0.19, Text: "guest has the mic: opencode - the DJ answers when the handoff ends"},
			glyph + " opencode has the mic on gpt-oss-120b · $0.19",
		},
		{
			"model only drops the money",
			protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "opencode", Model: "gpt-oss-120b"},
			glyph + " opencode has the mic on gpt-oss-120b",
		},
		{
			"spend only drops the on-clause",
			protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "aider", Spend: 1.05},
			glyph + " aider has the mic · $1.05",
		},
		{
			"bare operator is the short handoff line",
			protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "opencode", Text: "guest has the mic: opencode - the DJ answers when the handoff ends"},
			glyph + " guest has the mic: opencode",
		},
		{
			"DJ back is the plain text, no glyph",
			protocol.RCFrame{Kind: protocol.RCKindStatus, Text: "the DJ is back at the desk"},
			"the DJ is back at the desk",
		},
		{
			"neither operator nor text renders nothing",
			protocol.RCFrame{Kind: protocol.RCKindStatus},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OperatorStatusLine(tc.f, glyph); got != tc.want {
				t.Errorf("OperatorStatusLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOperatorStatusLineContentBlind pins that the guest's spoken content never leaks into
// the line: an operator frame renders only the operator/model/spend, never the frame Text's
// full sentence tail ("answers when the handoff ends").
func TestOperatorStatusLineContentBlind(t *testing.T) {
	f := protocol.RCFrame{Kind: protocol.RCKindStatus, Operator: "opencode", Model: "gpt-oss-120b", Spend: 0.19, Text: "guest has the mic: opencode - secret plan the guest typed"}
	got := OperatorStatusLine(f, "◉")
	if strings.Contains(got, "secret plan") || strings.Contains(got, "handoff") || strings.Contains(got, "answers") {
		t.Errorf("the line must be content-blind, got %q", got)
	}
}
