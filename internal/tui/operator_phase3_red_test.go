package tui

// operator_phase3_red_test.go — RED-stage unit tables for Guest Operators Phase 3
// (THE DESK view · agent-ready band gate · pre-launch plate), written against the
// EXISTING Phase 2 symbols so they compile and fail for the RIGHT reason: the Phase 3
// behavior does not exist yet. Spec: features/operator/desk_view|desk_strip|band_gate|
// prelaunch_plate|plate_budget|plate_workdir.feature (founder-approved).
//
// The one test that PASSES today is the zero-guest byte-identity pin — it is the
// permanent regression the desk work must never break.
//
// Assertions are stdlib testing (the repo convention - no testify in go.mod).

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/operator"
)

// phase3Seed builds an AGENT-mode model with a connected proxy holder on gpt-oss-20b,
// the given open-channel station ctx, and the given detections — pure in-process state,
// no servers (nothing here reaches the money path).
func phase3Seed(ctx int, ctxEst bool, ds []operator.Detection) model {
	var tm tea.Model = browseSeed(120)
	tm, _ = tm.Update(keyMsg("0"))
	m := asModel(tm)
	m.proxyHolder = client.NewProxyOptionsHolder(client.ProxyOptions{Model: "gpt-oss-20b", User: "tester"})
	m.endpoint = "http://127.0.0.1:1/v1"
	m.connected = &offer{NodeID: "demo-node", Model: "gpt-oss-20b", Online: true,
		TPS: 62, PriceIn: 0.2, PriceOut: 0.3, Ctx: ctx, CtxEstimated: ctxEst}
	m.operatorDetections = ds
	return m
}

func detOpencode() operator.Detection {
	return operator.Detection{Guest: operator.Registry()[0], Path: "/usr/bin/opencode", Version: "1.17.11"}
}

// TestPhase3ZeroGuestsByteIdentical — the permanent regression pin (desk_view.feature
// "Zero guests detected"): with no detections, the AGENT view carries ZERO desk chrome.
// This must pass BEFORE and AFTER the Phase 3 implementation.
func TestPhase3ZeroGuestsByteIdentical(t *testing.T) {
	m := phase3Seed(131072, false, nil)
	v := stripANSI(m.agentView(120))
	if strings.Contains(v, "THE DESK") {
		t.Fatalf("zero-guest AGENT view contains %q:\n%s", "THE DESK", v)
	}
	if strings.Contains(v, "at the desk") {
		t.Fatalf("zero-guest AGENT view contains %q:\n%s", "at the desk", v)
	}
}

// TestPhase3DeskRosterOnLanding — desk_view.feature: the roster renders exactly on the
// landing state (>=1 guest, empty transcript, not busy) and nowhere else.
func TestPhase3DeskRosterOnLanding(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*model)
		wantDesk bool
	}{
		{"landing with one guest", func(m *model) {}, true},
		{"transcript has lines", func(m *model) { m.agentLines = []string{"· hello"} }, false},
		{"busy turn", func(m *model) { m.agentBusy = true }, false},
		{"picker open", func(m *model) {
			var tm tea.Model
			tm, _ = m.runOperatorCommand(nil)
			*m = asModel(tm)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := phase3Seed(131072, false, []operator.Detection{detOpencode()})
			tt.mutate(&m)
			v := stripANSI(m.agentView(120))
			if tt.wantDesk {
				if !strings.Contains(v, "THE DESK") {
					t.Fatalf("the landing must render THE DESK roster (Phase 3 not implemented yet):\n%s", v)
				}
				if !strings.Contains(v, "resident") {
					t.Fatalf("the DJ resident row is part of the roster:\n%s", v)
				}
			} else if strings.Contains(v, "THE DESK") {
				t.Fatalf("the roster must collapse off the landing state:\n%s", v)
			}
		})
	}
}

// TestPhase3DeskStrip — desk_strip.feature: the one-line guests summary in the heading
// area, only when >=1 guest is detected.
func TestPhase3DeskStrip(t *testing.T) {
	m := phase3Seed(131072, false, []operator.Detection{detOpencode()})
	v := stripANSI(m.agentView(120))
	if !strings.Contains(v, "at the desk: opencode") {
		t.Fatalf("the desk strip must summarize the detected guests (Phase 3 not implemented yet):\n%s", v)
	}
	if !strings.Contains(v, "the DJ has the mic") {
		t.Fatalf("the desk strip must lead with the DJ line:\n%s", v)
	}
}

// TestPhase3BandGateRefusesUnderFloor — band_gate.feature: a direct jump on an
// under-16k channel is refused BEFORE any staging/exec, with the honest reason.
// Today (Phase 2) the handoff stages regardless — the right-reason failure.
func TestPhase3BandGateRefusesUnderFloor(t *testing.T) {
	tests := []struct {
		name   string
		ctx    int
		ctxEst bool
	}{
		{"8k detected window", 8192, false},
		{"one under the floor", 16383, false},
		{"8k estimated window", 8192, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := phase3Seed(tt.ctx, tt.ctxEst, []operator.Detection{detOpencode()})
			tm, _ := m.runOperatorCommand([]string{"opencode"})
			got := asModel(tm)
			if got.operatorHandoff != nil {
				t.Fatalf("an under-floor band must never stage a handoff (agent-ready gate missing)")
			}
			joined := stripANSI(strings.Join(got.agentLines, "\n"))
			if !strings.Contains(joined, "16k") {
				t.Fatalf("the refusal must name the 16k floor, got transcript:\n%s", joined)
			}
		})
	}
}

// TestPhase3PlateInterposes — prelaunch_plate.feature: on a clearing band, picking a
// guest shows the confirm plate and does NOT begin staging until y. Today Phase 2
// stages immediately — the right-reason failure.
func TestPhase3PlateInterposes(t *testing.T) {
	m := phase3Seed(131072, false, []operator.Detection{detOpencode()})
	tm, _ := m.runOperatorCommand([]string{"opencode"})
	got := asModel(tm)
	if got.operatorHandoff != nil {
		t.Fatalf("picking a guest must open the pre-launch plate, not stage the patch")
	}
	v := stripANSI(got.agentView(120))
	if !strings.Contains(v, "session budget") {
		t.Fatalf("the plate must show the spend ceiling:\n%s", v)
	}
	if !strings.Contains(v, "$2.00") {
		t.Fatalf("the default ceiling is client.DefaultSessionBudget:\n%s", v)
	}
}
