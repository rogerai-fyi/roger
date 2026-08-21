package harness

import (
	"context"
	"fmt"
	"strings"
)

// Step definitions for the tool-output-budget scenarios in features/agent/agent.feature.
// They drive the REAL Loop with a REAL tool that returns an oversized result, so the cap is
// exercised where it actually lives (the loop's tool choke point), not re-implemented here.

type toolBudgetState struct {
	budget   int
	prev     int
	emitted  string // what the UI was shown
	seen     string // what the MODEL was given
	toolBody string
}

// runTurn drives one real agent turn whose single tool returns toolBody, at the given
// budget, and records both what the model received and what the UI was shown.
func (s *toolBudgetState) runTurn(root string, budget int) error {
	s.seen, s.emitted = "", ""
	step := 0
	complete := func(_ context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		step++
		if step == 1 {
			return Message{Role: "assistant", ToolCalls: []ToolCall{newCall("c1", "big_tool")}}, nil
		}
		for _, m := range msgs {
			if m.Role == "tool" && m.Name == "big_tool" {
				s.seen = m.Content
			}
		}
		return Message{Role: "assistant", Content: "done"}, nil
	}
	l := NewLoop(root, "", complete, func(string, map[string]any) bool { return true })
	l.MaxToolOutput = budget
	body := s.toolBody
	big := Tool{Name: "big_tool", Run: func(context.Context, string, map[string]any) (string, error) { return body, nil }}
	l.tools = append(l.tools, big)
	l.toolByName["big_tool"] = big

	_, err := l.Send(context.Background(), "go", func(e Event) {
		if e.Kind == EventToolResult {
			s.emitted = e.Result
		}
	})
	return err
}

func (s *toolBudgetState) givenSmallContextBand() error {
	s.budget = ToolOutputBudget(8192) // the founder's Apple `foundation` window
	s.toolBody = strings.Repeat("y", 10103)
	return nil
}

func (s *toolBudgetState) whenToolReturnsMoreThanFits(root string) error {
	return s.runTurn(root, s.budget)
}

func (s *toolBudgetState) thenCutToAShareOfTheWindow() error {
	if s.seen == "" {
		return fmt.Errorf("the tool result never reached the model")
	}
	if len(s.seen) >= len(s.toolBody) {
		return fmt.Errorf("the result was not cut: %d bytes of %d", len(s.seen), len(s.toolBody))
	}
	if len(s.seen) > s.budget+len("\n... (truncated)")+4 {
		return fmt.Errorf("the result (%d bytes) exceeds the budget %d", len(s.seen), s.budget)
	}
	return nil
}

func (s *toolBudgetState) thenMarkedTruncated() error {
	// AMENDED 2026-08-21: an oversized result is SPILLED to a file now and the notice
	// names the path rather than only saying "truncated" (spill.go). The scenario's
	// guarantee is unchanged - a cut result must SAY it was cut, never arrive silently
	// partial - so both wordings satisfy it.
	if !strings.Contains(s.seen, "truncated") && !strings.Contains(s.seen, "read_file it for the rest") {
		return fmt.Errorf("a cut result must say so, got %q", lastChars(s.seen))
	}
	return nil
}

func (s *toolBudgetState) thenRoomyBandUnaffected(root string) error {
	if err := s.runTurn(root, ToolOutputBudget(128000)); err != nil {
		return err
	}
	if s.seen != s.toolBody {
		return fmt.Errorf("a roomy band altered the result (%d bytes of %d)", len(s.seen), len(s.toolBody))
	}
	return nil
}

func (s *toolBudgetState) givenMovesToSmallBand() error {
	s.prev = ToolOutputBudget(128000)
	s.budget = ToolOutputBudget(8192)
	return nil
}

func (s *toolBudgetState) thenCapShrinks() error {
	if s.budget >= s.prev {
		return fmt.Errorf("the cap did not shrink with the window (%d -> %d)", s.prev, s.budget)
	}
	if s.budget >= 10103 {
		return fmt.Errorf("the shrunk cap %d would still accept the result that overflowed", s.budget)
	}
	return nil
}

func (s *toolBudgetState) givenNoReportedWindow() error {
	s.budget = ToolOutputBudget(0)
	return nil
}

func (s *toolBudgetState) thenFlatCapApplies() error {
	if s.budget != maxToolOutput {
		return fmt.Errorf("unknown window gave %d, want the historical %d", s.budget, maxToolOutput)
	}
	return nil
}

// The operator must see what the model saw: a result that differed between the two would
// make a truncation-caused answer impossible to explain.
func (s *toolBudgetState) thenTranscriptShowsTruncated() error {
	if s.emitted == "" {
		return fmt.Errorf("nothing was emitted to the UI")
	}
	if s.emitted != s.seen {
		return fmt.Errorf("the UI (%d bytes) and the model (%d bytes) were shown different text",
			len(s.emitted), len(s.seen))
	}
	return nil
}

func lastChars(s string) string {
	if len(s) <= 60 {
		return s
	}
	return "..." + s[len(s)-60:]
}
