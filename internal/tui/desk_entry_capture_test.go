package tui

// desk_entry_capture_test.go - a golden-RENDER capture harness for the AGENT [0] desk
// redesign. Run with ROGER_CAPTURE=<dir> to WRITE the screens (plain + ANSI) to .txt for
// the founder to eyeball in the PR; without it, it still renders every scene and asserts
// the load-bearing markers, so it is a real regression test (not just a screenshot tool).
//
//   go test ./internal/tui/ -run TestDeskEntryCaptures -count=1               (assert)
//   ROGER_CAPTURE=/tmp/caps go test ./internal/tui/ -run TestDeskEntryCaptures (write)

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/rogerai-fyi/roger/internal/operator"
)

func capOffer(model string, ctx int, free bool, caps []string, priceOut float64, signal int) offer {
	o := offer{NodeID: "n-" + model, Region: "us-w", Model: model, Online: true,
		Ctx: ctx, Signal: signal, TPS: 55, Capabilities: caps, PriceIn: priceOut / 2, PriceOut: priceOut}
	if free {
		o.FreeNow, o.PriceIn, o.PriceOut = true, 0, 0
	}
	return o
}

func capWrite(t *testing.T, dir, name, ansi string) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".ansi.txt"), []byte(ansi), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(stripANSI(ansi)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeskEntryCaptures(t *testing.T) {
	dir := os.Getenv("ROGER_CAPTURE")
	// Force TrueColor so the marquee hues + red beats render as ANSI (off a TTY the profile
	// is Ascii); the .txt (stripANSI) fallback stays legible for the PR body.
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	mustContain := func(scene, got string, wants ...string) {
		for _, w := range wants {
			if !strings.Contains(stripANSI(got), w) {
				t.Errorf("scene %q missing %q:\n%s", scene, w, stripANSI(got))
			}
		}
	}

	// ── Scene 1: [0] entry, nothing tuned in - the "finding a band" beat ──────────
	fresh := func(offers []offer, loggedIn bool) model {
		var tm tea.Model = New("http://broker.local", "tester")
		tm, _ = tm.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
		tm, _ = tm.Update(offersMsg(offers))
		tm, _ = tm.Update(balanceMsg{loggedIn: loggedIn, balance: 12.50})
		tm, _ = tm.Update(keyMsg("0"))
		return asModel(tm)
	}
	freeBand := capOffer("gpt-oss-20b", 32768, true, nil, 0, 72)
	beat := fresh([]offer{freeBand}, true)
	capWrite(t, dir, "01_entry_finding_band", beat.View())
	mustContain("entry-beat", beat.View(), "finding a free band")

	// ── Scene 2: [0] entry after the silent auto-tune lands a free band ───────────
	landedTm, _ := beat.Update(autoTuneMsg{})
	landed := asModel(landedTm)
	capWrite(t, dir, "02_entry_auto_tuned", landed.View())
	mustContain("entry-landed", landed.View(), "auto-tuned to", "gpt-oss-20b")

	// ── Scene 3: the honest EMPTY state (nothing on air) ─────────────────────────
	emptyTm, _ := fresh(nil, true).Update(autoTuneMsg{})
	empty := asModel(emptyTm)
	capWrite(t, dir, "03_honest_empty", empty.View())
	mustContain("empty", empty.View(), "no station on air right now")

	// ── Scene 4: badged TUNE IN rows + the legend ────────────────────────────────
	var browseTm tea.Model = New("http://broker.local", "tester")
	browseTm, _ = browseTm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	browseTm, _ = browseTm.Update(offersMsg([]offer{
		capOffer("qwen3-coder-30b", 131072, true, nil, 0, 88),                     // agent-ready + FREE
		capOffer("llava-vision-13b", 8192, false, []string{"vision"}, 0.25, 60),   // vision, small
		capOffer("big-multimodal-70b", 65536, false, []string{"vision"}, 0.9, 74), // agent-ready + vision
		capOffer("tiny-1b", 4096, true, nil, 0, 40),                               // known-small free
	}))
	browseTm, _ = browseTm.Update(balanceMsg{loggedIn: true, balance: 12.50})
	browseTm, _ = browseTm.Update(tickMsg{})
	browse := asModel(browseTm)
	capWrite(t, dir, "04_tunein_badges", browse.View())
	mustContain("badges", browse.browseView(100), "agent-ready", "vision")

	// ── Scene 5: the colored marquee picker, one capture per selected operator ────
	pick := func(cursor int) model {
		var tm tea.Model = New("http://broker.local", "tester")
		tm, _ = tm.Update(tea.WindowSizeMsg{Width: 96, Height: 34})
		tm, _ = tm.Update(offersMsg([]offer{freeBand}))
		tm, _ = tm.Update(balanceMsg{loggedIn: true, balance: 12.50})
		tm, _ = tm.Update(keyMsg("0"))
		m := asModel(tm)
		m.bindChannel(freeBand) // an open channel so the picker header is honest
		for _, name := range []string{"opencode", "hermes", "aider"} {
			g, _ := registryGuest(name)
			m.operatorDetections = append(m.operatorDetections, operator.Detection{Guest: g, Path: "/usr/bin/" + g.Bin, Version: g.KnownGood})
		}
		m.operatorPicker = true
		m.operatorRows = m.buildOperatorRows()
		m.operatorCursor = cursor
		return m
	}
	for i, name := range []string{"dj", "opencode", "hermes", "aider"} {
		m := pick(i)
		capWrite(t, dir, "05_picker_"+name, m.agentView(m.width))
	}
	mustContain("picker-dj", pick(0).agentView(96), "hand the mic", "ROGER")
}
