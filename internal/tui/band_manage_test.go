package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/agent"
)

// BASE STATION band management. Until now bands were RENDERED but not selectable: the
// cursor only ever indexed rcSessions, and there was no revoke or move anywhere in the
// product - while the broker's own quota error told operators to "revoke an existing band
// first". Spec: features/sharing/band_management.feature.

// baseStation returns a BASE STATION model carrying one session and two bands.
func baseStation(t *testing.T) model {
	t.Helper()
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 40
	m.mode = modePrivate
	m.station = "eager-puma-54"
	m.rcSessions = []RemoteSessionRow{{ID: "sess_1", Name: "desk"}}
	m.rcBands = []BandRow{
		{ID: "band_1", Display: "145.225 MHz · ••••-••••", Status: "active", NodeID: "roggentoo-gemma-4-31b"},
		{ID: "band_2", Display: "147.520 MHz · ••••-••••", Status: "revoked", NodeID: "eager-puma-54-old"},
	}
	return m
}

// The list must say WHICH model each band is on. That single fact is what was missing when
// the founder hit the quota wall with their band parked on another machine.
func TestBandListShowsTheModelItIsOn(t *testing.T) {
	m := baseStation(t)
	out := stripANSI(m.View())
	if !strings.Contains(out, "gemma-4-31b") {
		t.Errorf("BASE STATION must show the model a band is on, got:\n%s", out)
	}
	// And it must never print the secret or a bare hash.
	if strings.Contains(out, "8F3K") || strings.Contains(out, "code_hash") {
		t.Error("the band list must never render a secret")
	}
}

// The cursor must be able to reach a band row at all - the core gap.
func TestCursorReachesBandRows(t *testing.T) {
	m := baseStation(t)
	var tm tea.Model = m
	// One session then two bands: three stops in total.
	for i := 0; i < 3; i++ {
		tm, _ = tm.Update(keyMsg2(tea.KeyDown))
	}
	gm := asModel(tm)
	if gm.rcCursor != 2 {
		t.Fatalf("cursor stopped at %d - it cannot reach the band rows (want 2)", gm.rcCursor)
	}
	if idx := gm.bandCursorIndex(); idx != 1 {
		t.Errorf("bandCursorIndex() = %d, want 1 (the second band)", idx)
	}
	// It must not run off the end.
	tm, _ = tm.Update(keyMsg2(tea.KeyDown))
	if asModel(tm).rcCursor != 2 {
		t.Error("the cursor must stop at the last band")
	}
}

// A session row must NOT be mistaken for a band.
func TestBandCursorIndexIsNegativeOnASession(t *testing.T) {
	m := baseStation(t)
	m.rcCursor = 0 // the session
	if idx := m.bandCursorIndex(); idx >= 0 {
		t.Errorf("bandCursorIndex() = %d on a session row, want -1", idx)
	}
}

// Enter on a band opens its management card rather than trying to open a remote session.
func TestEnterOnABandOpensManagement(t *testing.T) {
	m := baseStation(t)
	m.rcCursor = 1 // the first band
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg2(tea.KeyEnter))
	gm := asModel(tm)
	if gm.mode != modeBandManage {
		t.Fatalf("mode = %v, want modeBandManage", gm.mode)
	}
	out := stripANSI(gm.View())
	// The card must offer only what the product can actually do.
	if !strings.Contains(out, "move") || !strings.Contains(out, "revoke") {
		t.Errorf("the band card must offer move and revoke, got:\n%s", out)
	}
	// It must never offer to show the code again - it was never stored.
	if strings.Contains(strings.ToLower(out), "show code") || strings.Contains(strings.ToLower(out), "reveal") {
		t.Error("a band card must never offer to re-show a code")
	}
}

// Revoking is irreversible, so a single keypress must never do it.
func TestRevokeNeedsConfirmation(t *testing.T) {
	m := baseStation(t)
	m.rcCursor = 1
	m.mode = modeBandManage
	m.bandManageID = "band_1"

	var revoked string
	m.hooks.BandRevoke = func(broker, id string) error { revoked = id; return nil }

	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("x"))
	gm := asModel(tm)
	if revoked != "" {
		t.Fatal("a bare x must not revoke - the code is burnt forever")
	}
	if gm.mode != modeBandRevokeConfirm {
		t.Fatalf("mode = %v, want the revoke confirm", gm.mode)
	}
	// The confirm must state what it breaks.
	out := strings.ToLower(stripANSI(gm.View()))
	if !strings.Contains(out, "cut off") && !strings.Contains(out, "stops working") {
		t.Errorf("the confirm must say the code dies and tuned-in listeners are cut off, got:\n%s", out)
	}
	// esc must back out without revoking.
	tm2, _ := gm.Update(keyMsg2(tea.KeyEsc))
	if revoked != "" {
		t.Error("esc must not revoke")
	}
	if asModel(tm2).mode == modeBandRevokeConfirm {
		t.Error("esc must leave the confirm")
	}
}

// Confirming actually revokes, through the hook.
func TestConfirmedRevokeCallsTheHook(t *testing.T) {
	m := baseStation(t)
	m.mode = modeBandRevokeConfirm
	m.bandManageID = "band_1"
	var revoked string
	m.hooks.BandRevoke = func(broker, id string) error { revoked = id; return nil }

	tm, cmd := m.Update(keyMsg("y"))
	if cmd != nil {
		cmd() // the revoke runs in a command
	}
	if revoked != "band_1" {
		t.Fatalf("revoked %q, want band_1", revoked)
	}
	_ = tm
}

// Moving a band must offer the models on THIS machine as destinations, and must send the
// station-scoped node id the broker expects.
func TestMoveOffersLocalModelsAndSendsTheNodeID(t *testing.T) {
	m := baseStation(t)
	m.mode = modeBandManage
	m.bandManageID = "band_1"
	m.setShareRows([]shareRow{{model: "qwen3-vl-8b", ctx: 32768}})
	// The controller owns the callsign (setShareRows re-syncs m.station from it), and it is
	// what the share path uses to build node ids - so the move must agree with it.
	station := m.ctrl.Station()

	var gotID, gotNode string
	m.hooks.BandMove = func(broker, id, nodeID string) error { gotID, gotNode = id, nodeID; return nil }

	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("m"))
	gm := asModel(tm)
	if gm.mode != modeBandMove {
		t.Fatalf("mode = %v, want modeBandMove", gm.mode)
	}
	if !strings.Contains(stripANSI(gm.View()), "qwen3-vl-8b") {
		t.Errorf("the move picker must list local models, got:\n%s", stripANSI(gm.View()))
	}
	// It must promise the code survives - that is the whole reason to move rather than re-mint.
	if !strings.Contains(strings.ToLower(stripANSI(gm.View())), "same") {
		t.Error("the move picker should say the frequency code stays the same")
	}

	tm2, cmd := gm.Update(keyMsg2(tea.KeyEnter))
	if cmd != nil {
		cmd()
	}
	if gotID != "band_1" {
		t.Errorf("moved band %q, want band_1", gotID)
	}
	// "<station>-<model>", exactly the id agent.ShareNodeID builds for the share path. If
	// these ever diverge the band binds to a node id nothing registers, and it strands.
	want := agent.ShareNodeID(station, "qwen3-vl-8b", 0)
	if gotNode != want {
		t.Errorf("sent node id %q, want %q", gotNode, want)
	}
	_ = tm2
}

// A revoked band cannot be moved, so the card must not offer it.
func TestRevokedBandOffersNoMove(t *testing.T) {
	m := baseStation(t)
	m.rcCursor = 2 // the revoked band
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg2(tea.KeyEnter))
	gm := asModel(tm)
	out := strings.ToLower(stripANSI(gm.View()))
	if strings.Contains(out, "m move") || strings.Contains(out, "move it") {
		t.Errorf("a revoked band must not offer a move, got:\n%s", out)
	}
}

// The quota refusal on the SHARE screen must point at the surface that can fix it.
func TestQuotaRefusalPointsAtBaseStation(t *testing.T) {
	if !strings.Contains(bandQuotaHint(), "[p]") {
		t.Errorf("the quota hint must name the BASE STATION key, got %q", bandQuotaHint())
	}
	low := strings.ToLower(bandQuotaHint())
	for _, forbidden := range []string{"buy", "purchase", "$5", "pack"} {
		if strings.Contains(low, forbidden) {
			t.Errorf("the hint must not imply a purchase (%q): %q", forbidden, bandQuotaHint())
		}
	}
}
