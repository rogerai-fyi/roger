package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// BASE STATION's band card: the keys, and the commands they issue. The card is where a
// band's destructive actions live, so its refusals matter as much as its actions.

func manageCard(t *testing.T) model {
	t.Helper()
	m := privateTab(t)
	m.mode = modeBandManage
	m.bandManageID = "band_here"
	m.bandManageDisp = "145.225 MHz · ••••-••••"
	m.bandManageNode = "eager-puma-54-grok-4-6"
	return m
}

// A REVOKED band can be neither moved nor rotated - its code is burnt, and either action
// would resurrect it. Both refusals must SAY that rather than doing nothing.
func TestARevokedBandRefusesMoveAndRotate(t *testing.T) {
	m := manageCard(t)
	m.rcBands[0].Status = "revoked"
	for _, key := range []string{"m", "n"} {
		out, _ := m.onBandManageKey(keyMsg(key))
		gm := asModel(out)
		if gm.mode != modeBandManage {
			t.Errorf("%q acted on a revoked band (mode %v)", key, gm.mode)
		}
		if !strings.Contains(stripANSI(gm.status), "burnt") {
			t.Errorf("%q refused without saying the code is burnt: %q", key, stripANSI(gm.status))
		}
	}
}

// f is only for a band that is ALREADY dead. On a live one it must refuse and name the
// step that has to come first, because the broker will refuse it anyway.
func TestForgetRefusesALiveBandAtTheCard(t *testing.T) {
	m := manageCard(t)
	out, _ := m.onBandManageKey(keyMsg("f"))
	gm := asModel(out)
	if !strings.Contains(stripANSI(gm.status), "revoke it first") {
		t.Errorf("f on a live band must name the required first step, got %q", stripANSI(gm.status))
	}
}

// x opens the revoke confirm; an irreversible action is never one key away.
func TestXOpensTheRevokeConfirm(t *testing.T) {
	m := manageCard(t)
	out, _ := m.onBandManageKey(keyMsg("x"))
	if got := asModel(out).mode; got != modeBandRevokeConfirm {
		t.Errorf("x = mode %v, want the revoke confirm", got)
	}
	// And the confirm itself defaults to DENY: anything but y backs out.
	back, cmd := asModel(out).onBandRevokeConfirmKey(keyMsg("z"))
	if asModel(back).mode != modeBandManage || cmd != nil {
		t.Error("a non-y key must back out of the revoke confirm without acting")
	}
}

// The band ACTIONS are commands, and each must actually issue one - a card whose keys
// return nil is a screen that looks like it works.
func TestTheBandCommandsIssueWork(t *testing.T) {
	m := manageCard(t)
	m.hooks.BandRotate = func(string, string) (string, string, error) { return "code", "disp", nil }
	m.hooks.BandForget = func(string, string) error { return nil }
	m.hooks.BandLabel = func(string, string, string) error { return nil }
	for name, cmd := range map[string]tea.Cmd{
		"rotate": m.rotateBand("band_here"),
		"forget": m.forgetBand("band_here"),
		"label":  m.labelBand("band_here", "home gpu"),
	} {
		if cmd == nil {
			t.Fatalf("%s issued no command", name)
		}
		msg, ok := cmd().(bandActionMsg)
		if !ok {
			t.Fatalf("%s returned %T, want bandActionMsg", name, cmd())
		}
		if msg.err != "" {
			t.Errorf("%s reported an error with a working hook: %q", name, msg.err)
		}
	}
}

// With NO hook wired the commands must say the build cannot do it, rather than reporting
// a success nothing performed.
func TestTheBandCommandsSayWhenUnavailable(t *testing.T) {
	m := manageCard(t)
	m.hooks = Hooks{}
	for name, cmd := range map[string]tea.Cmd{
		"rotate": m.rotateBand("b"),
		"forget": m.forgetBand("b"),
		"label":  m.labelBand("b", "x"),
	} {
		msg, ok := cmd().(bandActionMsg)
		if !ok || msg.err == "" {
			t.Errorf("%s with no hook must report an error, got %+v", name, cmd())
		}
	}
}

// ⏎ on the card tunes in, routing through the PRIVATE tab's opener so the two surfaces
// cannot disagree about whether a band is reachable.
func TestTheManageCardTunesIn(t *testing.T) {
	m := manageCard(t)
	out, _ := m.onBandManageKey(keyMsg2(tea.KeyEnter))
	gm := asModel(out)
	if gm.mode != modeChat || gm.chatLocalChat == "" {
		t.Errorf("⏎ did not open the direct channel (mode %v local=%q)", gm.mode, gm.chatLocalChat)
	}
}

// A band that has left the list must not silently do nothing.
func TestTuningAVanishedBandSaysSo(t *testing.T) {
	m := manageCard(t)
	m.bandManageID = "band_gone"
	out, _ := m.tuneInBand()
	if !strings.Contains(stripANSI(asModel(out).status), "no longer in your list") {
		t.Errorf("a vanished band tuned silently: %q", stripANSI(asModel(out).status))
	}
}

// The move picker: ↑↓ bounded to the target list, enter issues the move, esc backs out.
func TestTheMovePickerIsBounded(t *testing.T) {
	m := manageCard(t)
	m.mode = modeBandMove
	m.bandMoveCursor = 0
	up, _ := m.onBandMoveKey(keyMsg2(tea.KeyUp))
	if asModel(up).bandMoveCursor != 0 {
		t.Error("the move cursor ran off the top")
	}
	targets := len(m.bandMoveTargets())
	m.bandMoveCursor = targets - 1
	down, _ := m.onBandMoveKey(keyMsg2(tea.KeyDown))
	if asModel(down).bandMoveCursor != targets-1 {
		t.Error("the move cursor ran off the end")
	}
	back, _ := m.onBandMoveKey(keyMsg2(tea.KeyEsc))
	if asModel(back).mode != modeBandManage {
		t.Error("esc did not return to the card")
	}
}

// The quota offer records which model a refused mint was for, so the share screen can act
// on a single key instead of pointing at another screen.
func TestTheQuotaOfferRemembersItsModel(t *testing.T) {
	m := manageCard(t)
	mm := &m
	mm.offerBandMove("grok-4.6")
	if mm.bandMoveOffer != "grok-4.6" {
		t.Errorf("the offer forgot its model: %q", mm.bandMoveOffer)
	}
	if !strings.Contains(stripANSI(bandQuotaOffer("grok-4.6")), "grok-4.6") {
		t.Error("the offer text must name the model it would move the band to")
	}
}
