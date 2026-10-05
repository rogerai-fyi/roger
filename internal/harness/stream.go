package harness

// A streamed agent turn (stream:true with include_usage). The final usage chunk is the
// meter: its usage.cost is the turn's cost, and its usage.rogerai block names the station and
// model that actually served (a models[] fallback can differ from the one asked for) and the
// receipt's price lock. A broker that predates the chunk still sends the trailing
// `: rogerai-cost=` comment, which is the meter only when no chunk carried a cost - never both.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Served is what the final usage chunk says about a turn.
type Served struct {
	Model, Node, LockedUntil string
}

// streamed is a reassembled streamed completion.
type streamed struct {
	msg       Message
	cost      float64
	in, out   int
	served    Served
	errText   string
	sawChoice bool
	// complete is true when the stream ended properly: a `data: [DONE]` frame or a
	// finish_reason. A stream without either was cut (cancelled, reset, or the reader's size
	// cap) and its partial content is never a successful turn. readErr is the scanner's error.
	complete bool
	readErr  error
}

const sseCostComment = ": rogerai-cost="

// readStream reassembles an SSE completion: content, reasoning and tool calls (accumulated
// per index, arguments concatenated), the usage chunk, and the cost comment.
func readStream(r io.Reader) streamed {
	var st streamed
	var content, reasoning strings.Builder
	type callBuf struct {
		id, typ, name string
		args          strings.Builder
	}
	calls := map[int]*callBuf{}
	chunkCost, commentCost := -1.0, -1.0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, sseCostComment); ok {
			if c, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				commentCost = c
			}
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			st.complete = true
			continue
		}
		if data == "" {
			continue
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int      `json:"prompt_tokens"`
				CompletionTokens int      `json:"completion_tokens"`
				Cost             *float64 `json:"cost"`
				RogerAI          struct {
					Node        string          `json:"node"`
					Model       string          `json:"model"`
					LockedUntil json.RawMessage `json:"locked_until"`
				} `json:"rogerai"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		if ch.Error.Message != "" {
			st.errText = ch.Error.Message
		}
		for _, c := range ch.Choices {
			st.sawChoice = true
			content.WriteString(c.Delta.Content)
			reasoning.WriteString(c.Delta.ReasoningContent)
			reasoning.WriteString(c.Delta.Reasoning)
			for _, tc := range c.Delta.ToolCalls {
				b := calls[tc.Index]
				if b == nil {
					b = &callBuf{}
					calls[tc.Index] = b
				}
				if tc.ID != "" {
					b.id = tc.ID
				}
				if tc.Type != "" {
					b.typ = tc.Type
				}
				if tc.Function.Name != "" {
					b.name = tc.Function.Name
				}
				b.args.WriteString(tc.Function.Arguments)
			}
			if c.FinishReason == "length" {
				st.msg.Truncated = true
			}
			if c.FinishReason != "" {
				st.complete = true
			}
		}
		if u := ch.Usage; u != nil {
			st.in, st.out = u.PromptTokens, u.CompletionTokens
			if u.Cost != nil {
				chunkCost = *u.Cost
			}
			st.served = Served{Model: u.RogerAI.Model, Node: u.RogerAI.Node, LockedUntil: lockedUntilText(u.RogerAI.LockedUntil)}
		}
	}
	st.readErr = sc.Err()
	switch {
	case chunkCost >= 0:
		st.cost = chunkCost
	case commentCost >= 0:
		st.cost = commentCost
	}
	st.msg.Role = "assistant"
	st.msg.Content = content.String()
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		b := calls[i]
		typ := b.typ
		if typ == "" {
			typ = "function"
		}
		tc := ToolCall{ID: b.id, Type: typ}
		tc.Function.Name, tc.Function.Arguments = b.name, b.args.String()
		st.msg.ToolCalls = append(st.msg.ToolCalls, tc)
	}
	if st.msg.Content == "" && len(st.msg.ToolCalls) == 0 {
		st.msg.Thought = strings.TrimSpace(reasoning.String())
	}
	return st
}

// streamError is the turn's error for a stream that carried no reply, was cut before it
// finished, or failed to read.
func (st streamed) streamError() error {
	if st.errText != "" {
		return fmt.Errorf("%s", st.errText)
	}
	if st.readErr != nil {
		return fmt.Errorf("the reply stream broke off: %v - try again", st.readErr)
	}
	if !st.sawChoice {
		return fmt.Errorf("the station sent an empty response (status 200)")
	}
	if !st.complete {
		return fmt.Errorf("the reply stream ended before it finished (connection cut or reset) - try again")
	}
	return nil
}

// lockedUntilText renders the usage chunk's locked_until: the broker sends unix seconds (an
// integer; 0 = no lock), an older shape sent a string, kept as is.
func lockedUntilText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		if n <= 0 {
			return ""
		}
		return time.Unix(n, 0).UTC().Format(time.RFC3339)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}
