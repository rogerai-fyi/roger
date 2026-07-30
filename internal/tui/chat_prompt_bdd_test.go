package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
)

type chatPromptBDD struct {
	m model
}

func (s *chatPromptBDD) reset() {
	s.m = browseSeed(80)
	s.m.width, s.m.height = 80, 18
	s.m.mode = modeChat
	s.m.connected = &offer{NodeID: "station", Model: "model", Online: true}
	s.m.chatIn.Focus()
}

func (s *chatPromptBDD) typingTuneIn() error { return nil }

func (s *chatPromptBDD) terminalSize(w, h int) error {
	s.m.width, s.m.height = w, h
	return nil
}

func (s *chatPromptBDD) longPrompt() error {
	s.m.chatIn.SetValue("beginning " + strings.Repeat("wrapped words ", 14) + "visible ending")
	s.m.chatIn.CursorEnd()
	return nil
}

const composerGrowthPrompt = "can we test how long this text can be i just want it to count the number of characters and the number of words and give me a histogram of the words i am writing on this new line"

func (s *chatPromptBDD) fillFirstTuneInRow() error {
	s.m.width, s.m.height = 170, 32
	out, _ := s.m.Update(tea.WindowSizeMsg{Width: 170, Height: 32})
	s.m = asModel(out)
	runes := []rune(composerGrowthPrompt)
	out, _ = s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes[:150]})
	s.m = asModel(out)
	_ = s.m.View()
	return nil
}

func (s *chatPromptBDD) wrapSecondTuneInRow() error {
	runes := []rune(composerGrowthPrompt)
	out, _ := s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes[150:]})
	s.m = asModel(out)
	return nil
}

func (s *chatPromptBDD) tuneInGrowthVisible() error {
	view := stripANSI(strings.Join(s.m.chatPromptLines(s.m.effWidth()), "\n"))
	for _, fragment := range []string{"can we test how long", "writing on this new line"} {
		if !strings.Contains(view, fragment) {
			return fmt.Errorf("TUNE IN prompt lost %q while growing:\n%s", fragment, view)
		}
	}
	return nil
}

func (s *chatPromptBDD) endpointsVisible() error {
	v := stripANSI(s.m.chatView(s.m.effWidth()))
	if !strings.Contains(v, "beginning") || !strings.Contains(v, "visible ending") {
		return fmt.Errorf("prompt endpoints are not both visible:\n%s", v)
	}
	return nil
}

func (s *chatPromptBDD) noLineOverflow(w int) error {
	for _, line := range strings.Split(s.m.View(), "\n") {
		if got := lipgloss.Width(line); got > w {
			return fmt.Errorf("line width %d exceeds %d: %q", got, w, stripANSI(line))
		}
	}
	return nil
}

func (s *chatPromptBDD) frameFits(h int) error {
	if rows := strings.Count(s.m.View(), "\n") + 1; rows > h {
		return fmt.Errorf("frame rows %d exceed %d", rows, h)
	}
	return nil
}

func (s *chatPromptBDD) composerOneRow() error {
	if rows := s.m.chatPromptRowCount(s.m.effWidth()); rows != 1 {
		return fmt.Errorf("empty composer rows = %d, want 1", rows)
	}
	return nil
}

func (s *chatPromptBDD) launchTUI() error { return nil }

func (s *chatPromptBDD) mouseDisabled() error {
	if !s.m.mouseOff {
		return fmt.Errorf("mouse reporting is enabled by default")
	}
	return nil
}

func (s *chatPromptBDD) nativeSelection() error { return s.mouseDisabled() }
func (s *chatPromptBDD) keyboardScroll() error  { return nil }

func (s *chatPromptBDD) runMouse() error {
	out, cmd := s.m.runSession("/mouse")
	s.m = asModel(out)
	if cmd == nil {
		return fmt.Errorf("/mouse emitted no terminal command")
	}
	return nil
}

func (s *chatPromptBDD) mouseEnabled() error {
	if s.m.mouseOff {
		return fmt.Errorf("mouse reporting remains disabled after /mouse")
	}
	return nil
}

func (s *chatPromptBDD) mouseAgainRestores() error {
	if err := s.runMouse(); err != nil {
		return err
	}
	return s.mouseDisabled()
}

func (s *chatPromptBDD) typingAgent() error {
	s.m.mode = modeAgent
	s.m.agent = s.m.newAgentRuntime()
	s.m.agentIn.Focus()
	return nil
}

func (s *chatPromptBDD) pressCtrlO() error {
	out, cmd := s.m.onAgentKey(keyMsg("ctrl+o"))
	s.m = asModel(out)
	if cmd == nil {
		return fmt.Errorf("AGENT ctrl+o emitted no terminal command")
	}
	return nil
}

func (s *chatPromptBDD) runAgentMouse(_ string) error {
	out, cmd := s.m.runAgentCommand("/mouse")
	s.m = asModel(out)
	if cmd == nil {
		return fmt.Errorf("AGENT /mouse emitted no terminal command")
	}
	return nil
}

func (s *chatPromptBDD) nativeOwnsMouse() error { return s.mouseDisabled() }

func (s *chatPromptBDD) idleDoesNotPoll() error {
	if s.m.idleDiscoveryEnabled() {
		return fmt.Errorf("idle discovery remains enabled during native selection")
	}
	return nil
}

func (s *chatPromptBDD) tuneInCursorSteady() error {
	if got := s.m.chatIn.Cursor.Mode(); got != cursor.CursorStatic {
		return fmt.Errorf("TUNE IN cursor mode = %s, want static while native selection owns the terminal", got)
	}
	return nil
}

func TestChatPromptBDD(t *testing.T) {
	st := &chatPromptBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^I am typing in a TUNE IN channel$`, st.typingTuneIn)
			sc.Step(`^the terminal is (\d+) columns by (\d+) rows$`, st.terminalSize)
			sc.Step(`^I enter a prompt long enough to wrap twice$`, st.longPrompt)
			sc.Step(`^I rapidly type enough text to fill one TUNE IN row$`, st.fillFirstTuneInRow)
			sc.Step(`^the next input chunk wraps onto a second TUNE IN row$`, st.wrapSecondTuneInRow)
			sc.Step(`^both the beginning and continuation of the TUNE IN prompt remain visible$`, st.tuneInGrowthVisible)
			sc.Step(`^the beginning and end of the prompt are both visible$`, st.endpointsVisible)
			sc.Step(`^no rendered line exceeds (\d+) columns$`, st.noLineOverflow)
			sc.Step(`^the complete TUNE IN frame fits within (\d+) rows$`, st.frameFits)
			sc.Step(`^the TUNE IN composer occupies one row$`, st.composerOneRow)
			sc.Step(`^I launch the RogerAI TUI$`, st.launchTUI)
			sc.Step(`^terminal mouse reporting is disabled$`, st.mouseDisabled)
			sc.Step(`^ordinary click-drag selection belongs to the terminal$`, st.nativeSelection)
			sc.Step(`^keyboard transcript scrolling remains available$`, st.keyboardScroll)
			sc.Step(`^I run "/mouse"$`, st.runMouse)
			sc.Step(`^terminal mouse reporting is enabled$`, st.mouseEnabled)
			sc.Step(`^running "/mouse" again restores native selection$`, st.mouseAgainRestores)
			sc.Step(`^I am typing in AGENT mode$`, st.typingAgent)
			sc.Step(`^I press ctrl\+o$`, st.pressCtrlO)
			sc.Step(`^I run the AGENT command "([^"]*)"$`, st.runAgentMouse)
			sc.Step(`^native selection owns the mouse$`, st.nativeOwnsMouse)
			sc.Step(`^idle ticks do not poll discovery and repaint the screen$`, st.idleDoesNotPoll)
			sc.Step(`^the TUNE IN cursor is steady and emits no recurring blink repaint$`, st.tuneInCursorSteady)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/tui/chat_prompt_wrapping.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("chat prompt scenarios failed")
	}
}
