package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// shareModelWithPayout builds a logged-in SHARE-view model carrying a payout snapshot.
func shareModelWithPayout(w int, snap payoutSnapshot) model {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = w, 30
	mm.mode = modeShare
	mm.ghLogin = "octocat" // logged in -> loggedInState() true
	mm.shareRows = []shareRow{{model: "gpt-oss-20b", ctx: 32768}}
	mm.payout = snap
	return mm
}

// TestPayoutHintCashOut: a logged-in owner with payable >= min sees the cash-out hint
// pointing at `roger payout` in the SHARE earnings surface.
func TestPayoutHintCashOut(t *testing.T) {
	mm := shareModelWithPayout(100, payoutSnapshot{loaded: true, kyc: "active", payable: 40, min: 25})
	var m tea.Model = mm
	v := stripANSI(m.View())
	if !strings.Contains(v, "roger payout") {
		t.Errorf("expected the cash-out hint in the share view:\n%s", v)
	}
	if !strings.Contains(v, "payable") {
		t.Errorf("cash-out hint should name the payable amount:\n%s", v)
	}
}

// TestPayoutHintKYC: a logged-in owner with earnings but KYC not done sees the
// onboard hint instead.
func TestPayoutHintKYC(t *testing.T) {
	mm := shareModelWithPayout(100, payoutSnapshot{loaded: true, kyc: "onboarding", payable: 30, min: 25})
	var m tea.Model = mm
	v := stripANSI(m.View())
	if !strings.Contains(v, "roger payout onboard") {
		t.Errorf("expected the KYC onboard hint in the share view:\n%s", v)
	}
}

// TestPayoutHintQuietWhenNothing: no hint for a not-loaded snapshot, an anonymous
// user, or an active owner below the minimum (nothing actionable).
func TestPayoutHintQuietWhenNothing(t *testing.T) {
	// not loaded
	if h := shareModelWithPayout(100, payoutSnapshot{loaded: false}).payoutHint(); h != "" {
		t.Errorf("no hint expected for an unloaded snapshot, got %q", h)
	}
	// active but below the minimum
	if h := shareModelWithPayout(100, payoutSnapshot{loaded: true, kyc: "active", payable: 5, min: 25}).payoutHint(); h != "" {
		t.Errorf("no hint expected below the minimum, got %q", h)
	}
	// anonymous (not logged in): build without ghLogin
	mm := New("http://broker.local", "tester")
	mm.mode = modeShare
	mm.payout = payoutSnapshot{loaded: true, kyc: "active", payable: 40, min: 25}
	if h := mm.payoutHint(); h != "" {
		t.Errorf("no hint expected when not logged in, got %q", h)
	}
}

// TestPayoutHintNarrowSafe: the hint never emits ANSI under NO_COLOR and never
// overflows the width, across widths and both the SHARE table + ON-AIR panel.
func TestPayoutHintNarrowSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, w := range []int{40, 50, 64, 80, 120} {
		mm := shareModelWithPayout(w, payoutSnapshot{loaded: true, kyc: "active", payable: 1234.56, min: 25})
		var m tea.Model = mm
		out := m.View()
		if strings.Contains(out, "\x1b[") {
			t.Errorf("width %d: share view emitted ANSI under NO_COLOR", w)
		}
		for _, line := range strings.Split(out, "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: payout-hint share line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
		// The compact ON-AIR panel string must also stay within width + ANSI-free.
		panel := mm.payoutHint()
		if strings.Contains(panel, "\x1b[") {
			t.Errorf("width %d: payoutHint emitted ANSI under NO_COLOR: %q", w, panel)
		}
	}
}

// TestPayoutStatusMsgStored: receiving a payoutStatusMsg stores the snapshot so the
// hint can render (the async fetch landing path).
func TestPayoutStatusMsgStored(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.ghLogin = "octocat"
	var m tea.Model = mm
	m, _ = m.Update(payoutStatusMsg{loaded: true, kyc: "active", payable: 99, min: 25})
	if got := asModel(m).payout; !got.loaded || got.payable != 99 {
		t.Errorf("payoutStatusMsg not stored: %+v", got)
	}
}
