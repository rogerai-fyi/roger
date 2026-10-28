package tui

// Review 2026-10-27 (a semantic conflict from the main merge): SHARE's g opens the category editor
// for a DETECTED row (features/categories/tui_share_editor.feature). Roger's own rows sit past the
// detected ones, and g on one of them indexed past the detected rows and crashed the screen.

import (
	"testing"

	"rogerai.fm/roger/v7/internal/node"
)

func TestGOnOneOfRogersOwnRowsDoesNothing(t *testing.T) {
	f := &modelsFake{view: baseView()}
	m := shareWithModels(t, f)
	m.ctrl = node.New(node.Config{})
	m.shareCursor = m.shareRowCount() - 1
	if _, ok := m.shareExtraAt(m.shareCursor); !ok {
		t.Fatal("the cursor is not on one of roger's own rows")
	}
	got, _ := shareKey(t, m, "g")
	if got.mode != modeShare {
		t.Errorf("g on roger's own row left SHARE for mode %v", got.mode)
	}
}
