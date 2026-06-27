package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHwClassLabel covers the hardware-class normalizer across its branches.
func TestHwClassLabel(t *testing.T) {
	cases := map[string]string{
		"": "", "unknown": "", "multi-gpu": "multi-gpu",
		"Apple M3 Max": "apple", "RTX 4090": "single-gpu", "NVIDIA A100": "single-gpu",
		"AMD Ryzen 9": "cpu", "Intel Xeon": "cpu", "something weird": "",
	}
	for in, want := range cases {
		if got := hwClassLabel(in); got != want {
			t.Errorf("hwClassLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCoarseRegion covers the region bucketing across macro-regions + empty/unmatched.
func TestCoarseRegion(t *testing.T) {
	cases := map[string]string{
		"us-west": "US-W", "nyc": "US-E", "chicago-central": "US-C",
		"london": "UK", "frankfurt": "DE", "amsterdam": "NL", "paris": "FR",
		"": "", "qzqzq-nowhere": "",
	}
	for in, want := range cases {
		if got := coarseRegion(in); got != want {
			t.Errorf("coarseRegion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestElide covers the rune-safe truncation incl. the n<1 clamp and the no-trim path.
func TestElide(t *testing.T) {
	if elide("short", 10) != "short" {
		t.Error("elide(no trim) wrong")
	}
	if got := elide("0123456789", 5); got != "0123…" {
		t.Errorf("elide(trim) = %q, want 0123…", got)
	}
	if elide("x", 0) == "" {
		t.Error("elide(n<1) should clamp to 1, not empty")
	}
}

// TestCornerFrameFor covers the corner-Ping animation selector for every pose.
func TestCornerFrameFor(t *testing.T) {
	for _, p := range []agentPose{poseWaiting, poseThinking, poseStreaming, poseTool} {
		for f := 0; f < 40; f++ {
			head, eye := cornerFrameFor(p, f)
			if head == (cornerHead{}) && eye == "" {
				t.Fatalf("cornerFrameFor(%v,%d) returned an empty frame", p, f)
			}
		}
	}
}

// TestArgStrAndToolSummary covers the agent tool-arg coercion + per-tool summary.
func TestArgStrAndToolSummary(t *testing.T) {
	if argStr(nil) != "" || argStr("x") != "x" || argStr(42) != "42" {
		t.Error("argStr coercion wrong")
	}
	if toolArgSummary("run_shell", map[string]any{"cmd": "ls -la"}) != "ls -la" {
		t.Error("run_shell summary should be the cmd")
	}
	if toolArgSummary("write_file", map[string]any{"path": "a.txt"}) != "a.txt" {
		t.Error("write_file summary should be the path")
	}
	if toolArgSummary("list_dir", map[string]any{}) != "." {
		t.Error("list_dir summary should default to .")
	}
	if toolArgSummary("unknown_tool", nil) != "" {
		t.Error("unknown tool summary should be empty")
	}
}

// TestDriveAgentChat drives the in-channel AGENT chat: enter chat, type a prompt, and
// submit, exercising onAgentKey / startAgentTurn wiring (the turn itself no-ops without a
// live broker, but the key + state plumbing runs).
func TestDriveAgentChat(t *testing.T) {
	var model tea.Model = seedFor(120, modeChat, false)
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	for _, r := range "hello there" {
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if strings.TrimSpace(model.View()) == "" {
		t.Error("agent chat view should render after typing + submit")
	}
}

// TestDriveLimitsAndShareSetup drives the LIMITS editor and the SHARE-setup keys, hitting
// onLimitsKey / onShareSetupKey / toggleSection beyond the smoke pass.
func TestDriveLimitsAndShareSetup(t *testing.T) {
	for _, md := range []mode{modeLimits, modeShare} {
		var model tea.Model = seedFor(120, md, false)
		model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		seq := []tea.Msg{
			tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyUp},
			tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}},
			tea.KeyMsg{Type: tea.KeyEsc}, tea.KeyMsg{Type: tea.KeyTab},
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}, // edit
			tea.KeyMsg{Type: tea.KeyEsc},
		}
		for _, k := range seq {
			model, _ = model.Update(k)
			_ = model.View()
		}
	}
}
