package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReasoningFallbackDecision is the pure transform decision: fill EMPTY content from a
// reasoning channel, never overwrite real content, prefer reasoning_content, ignore
// whitespace-only reasoning.
func TestReasoningFallbackDecision(t *testing.T) {
	cases := []struct {
		name             string
		content          string
		reasoning        string
		reasoningContent string
		wantText         string
		wantApply        bool
	}{
		{"empty content, reasoning present", "", "the answer", "", "the answer", true},
		{"whitespace content, reasoning present", "  \n\t ", "the answer", "", "the answer", true},
		{"reasoning_content preferred over reasoning", "", "second", "first", "first", true},
		{"reasoning_content only", "", "", "rc text", "rc text", true},
		{"real content is never overwritten", "real", "hidden", "", "", false},
		{"single-space content is filled", " ", "r", "", "r", true},
		{"empty content and empty reasoning", "", "", "", "", false},
		{"whitespace-only reasoning is ignored", "", "   ", "\n\t", "", false},
		{"content present, reasoning present -> no change", "hi", "thinking", "rc", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, apply := reasoningFallback(c.content, c.reasoning, c.reasoningContent)
			if apply != c.wantApply || text != c.wantText {
				t.Fatalf("reasoningFallback(%q,%q,%q) = (%q,%v), want (%q,%v)",
					c.content, c.reasoning, c.reasoningContent, text, apply, c.wantText, c.wantApply)
			}
		})
	}
}

func msgContent(t *testing.T, body []byte) string {
	t.Helper()
	var d struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if len(d.Choices) == 0 {
		t.Fatalf("no choices in %q", body)
	}
	return d.Choices[0].Message.Content
}

// TestApplyReasoningFallbackNonStreaming exercises the whole-body transform.
func TestApplyReasoningFallbackNonStreaming(t *testing.T) {
	t.Run("empty content + reasoning -> content filled", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"role":"assistant","content":"","reasoning":"42"}}]}`)
		if got := msgContent(t, applyReasoningFallback(in)); got != "42" {
			t.Fatalf("content = %q, want 42", got)
		}
	})

	t.Run("reasoning_content field name honored", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"content":"","reasoning_content":"rc"}}]}`)
		if got := msgContent(t, applyReasoningFallback(in)); got != "rc" {
			t.Fatalf("content = %q, want rc", got)
		}
	})

	t.Run("null content + reasoning -> filled", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"content":null,"reasoning":"nn"}}]}`)
		if got := msgContent(t, applyReasoningFallback(in)); got != "nn" {
			t.Fatalf("content = %q, want nn", got)
		}
	})

	t.Run("missing content field + reasoning -> filled", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"reasoning":"mm"}}]}`)
		if got := msgContent(t, applyReasoningFallback(in)); got != "mm" {
			t.Fatalf("content = %q, want mm", got)
		}
	})

	t.Run("non-empty content -> byte-identical passthrough", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"content":"real","reasoning":"hidden"}}]}`)
		if got := string(applyReasoningFallback(in)); got != string(in) {
			t.Fatalf("mutated a non-empty-content body:\n got %q\nwant %q", got, in)
		}
	})

	t.Run("empty content + empty reasoning -> byte-identical passthrough", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"content":"","reasoning":""}}]}`)
		if got := string(applyReasoningFallback(in)); got != string(in) {
			t.Fatalf("mutated a nothing-to-do body:\n got %q\nwant %q", got, in)
		}
	})

	t.Run("malformed JSON -> returned unchanged", func(t *testing.T) {
		in := []byte(`not json at all`)
		if got := string(applyReasoningFallback(in)); got != string(in) {
			t.Fatalf("mutated malformed body: %q", got)
		}
	})

	t.Run("no choices -> unchanged", func(t *testing.T) {
		in := []byte(`{"error":{"message":"nope"}}`)
		if got := string(applyReasoningFallback(in)); got != string(in) {
			t.Fatalf("mutated an error body: %q", got)
		}
	})

	t.Run("usage token numbers preserved exactly", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"content":"","reasoning":"x"}}],"usage":{"total_tokens":1234567,"prompt_tokens":10}}`)
		out := applyReasoningFallback(in)
		if got := msgContent(t, out); got != "x" {
			t.Fatalf("content = %q, want x", got)
		}
		var d struct {
			Usage struct {
				Total  json.Number `json:"total_tokens"`
				Prompt json.Number `json:"prompt_tokens"`
			} `json:"usage"`
		}
		dec := json.NewDecoder(strings.NewReader(string(out)))
		dec.UseNumber()
		if err := dec.Decode(&d); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if d.Usage.Total.String() != "1234567" || d.Usage.Prompt.String() != "10" {
			t.Fatalf("usage numbers altered: total=%s prompt=%s", d.Usage.Total, d.Usage.Prompt)
		}
	})

	t.Run("empty content + reasoning + tool_calls -> NOT filled (byte-identical)", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"content":"","reasoning":"calling api","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}`)
		if got := string(applyReasoningFallback(in)); got != string(in) {
			t.Fatalf("tool-call turn was filled:\n got %q\nwant %q", got, in)
		}
	})

	t.Run("empty tool_calls array does NOT block the fallback", func(t *testing.T) {
		in := []byte(`{"choices":[{"message":{"content":"","reasoning":"ans","tool_calls":[]}}]}`)
		if got := msgContent(t, applyReasoningFallback(in)); got != "ans" {
			t.Fatalf("content = %q, want ans (empty tool_calls should not guard)", got)
		}
	})

	t.Run("multi-choice: only the empty one is filled, the other preserved", func(t *testing.T) {
		in := []byte(`{"choices":[{"index":0,"message":{"content":"","reasoning":"a"}},{"index":1,"message":{"content":"b","reasoning":"c"}}]}`)
		out := applyReasoningFallback(in)
		var d struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(out, &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if d.Choices[0].Message.Content != "a" || d.Choices[1].Message.Content != "b" {
			t.Fatalf("choices = %q / %q, want a / b", d.Choices[0].Message.Content, d.Choices[1].Message.Content)
		}
	})
}

// TestStreamRelayBodyDirect covers the streaming injector's edge branches (overflow line,
// no-[DONE] stream, meter ordering) that the BDD happy paths don't reach.
func TestStreamRelayBodyDirect(t *testing.T) {
	t.Run("reasoning-only, no DONE sentinel -> synthesized at EOF", func(t *testing.T) {
		body := `data: {"choices":[{"index":0,"delta":{"reasoning":"hello"}}]}` + "\n\n"
		rec := httptest.NewRecorder()
		streamRelayBody(rec, strings.NewReader(body), true)
		if !strings.Contains(rec.Body.String(), `"content":"hello"`) {
			t.Fatalf("no synthesized content at EOF: %q", rec.Body.String())
		}
	})

	t.Run("disabled -> byte-identical passthrough", func(t *testing.T) {
		body := `data: {"choices":[{"index":0,"delta":{"reasoning":"hello"}}]}` + "\n\ndata: [DONE]\n\n"
		rec := httptest.NewRecorder()
		streamRelayBody(rec, strings.NewReader(body), false)
		if rec.Body.String() != body {
			t.Fatalf("disabled path altered the stream:\n got %q\nwant %q", rec.Body.String(), body)
		}
	})

	t.Run("very long non-terminal data line passes through intact", func(t *testing.T) {
		huge := strings.Repeat("x", 300000)
		body := `data: {"choices":[{"index":0,"delta":{"reasoning":"` + huge + `"}}]}` + "\n\ndata: [DONE]\n\n"
		rec := httptest.NewRecorder()
		streamRelayBody(rec, strings.NewReader(body), true)
		out := rec.Body.String()
		if !strings.Contains(out, huge) || !strings.Contains(out, "data: [DONE]") {
			t.Fatalf("giant line or terminal lost (len=%d)", len(out))
		}
	})

	t.Run("meter comment after DONE survives, synthesized delta lands before DONE", func(t *testing.T) {
		body := `data: {"choices":[{"index":0,"delta":{"reasoning":"r"}}]}` + "\n\ndata: [DONE]\n\n: rogerai-cost=0.5\n\n"
		rec := httptest.NewRecorder()
		cost := streamRelayBody(rec, strings.NewReader(body), true)
		if cost != 0.5 {
			t.Fatalf("cost = %v, want 0.5", cost)
		}
		out := rec.Body.String()
		if !strings.Contains(out, ": rogerai-cost=0.5") {
			t.Fatalf("meter comment lost: %q", out)
		}
		if ci, di := strings.Index(out, `"content":"r"`), strings.Index(out, "[DONE]"); ci < 0 || ci > di {
			t.Fatalf("synthesized content not before [DONE]: content@%d done@%d", ci, di)
		}
	})

	t.Run("reasoning-only stream ending without a trailing newline still synthesizes", func(t *testing.T) {
		body := `data: {"choices":[{"index":0,"delta":{"reasoning":"tail"}}]}` // no final \n
		rec := httptest.NewRecorder()
		streamRelayBody(rec, strings.NewReader(body), true)
		out := rec.Body.String()
		if !strings.Contains(out, "tail") || !strings.Contains(out, `"content":"tail"`) {
			t.Fatalf("trailing-line reasoning not preserved+synthesized: %q", out)
		}
	})

	t.Run("synthesized content precedes the finish_reason chunk", func(t *testing.T) {
		body := `data: {"choices":[{"index":0,"delta":{"reasoning":"ans"}}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
		rec := httptest.NewRecorder()
		streamRelayBody(rec, strings.NewReader(body), true)
		out := rec.Body.String()
		ci, fi := strings.Index(out, `"content":"ans"`), strings.Index(out, "finish_reason")
		if ci < 0 || fi < 0 || ci > fi {
			t.Fatalf("content not before finish_reason: content@%d finish@%d body=%q", ci, fi, out)
		}
	})

	t.Run("reasoning + tool_call stream -> no synthesized content", func(t *testing.T) {
		body := `data: {"choices":[{"index":0,"delta":{"reasoning":"thinking"}}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
		rec := httptest.NewRecorder()
		streamRelayBody(rec, strings.NewReader(body), true)
		if strings.Contains(rec.Body.String(), `"content":`) {
			t.Fatalf("tool-call stream got a synthesized content delta: %q", rec.Body.String())
		}
	})

	t.Run("CRLF line endings pass through byte-for-byte", func(t *testing.T) {
		body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\r\n\r\ndata: [DONE]\r\n\r\n"
		rec := httptest.NewRecorder()
		streamRelayBody(rec, strings.NewReader(body), true)
		if rec.Body.String() != body {
			t.Fatalf("CRLF stream altered:\n got %q\nwant %q", rec.Body.String(), body)
		}
	})

	t.Run("multi-choice reasoning-only -> one synthesized delta per choice", func(t *testing.T) {
		body := `data: {"choices":[{"index":0,"delta":{"reasoning":"zero"}}]}` + "\n\n" +
			`data: {"choices":[{"index":1,"delta":{"reasoning":"one"}}]}` + "\n\ndata: [DONE]\n\n"
		rec := httptest.NewRecorder()
		streamRelayBody(rec, strings.NewReader(body), true)
		out := rec.Body.String()
		if !strings.Contains(out, `"index":0`) || !strings.Contains(out, `"content":"zero"`) ||
			!strings.Contains(out, `"index":1`) || !strings.Contains(out, `"content":"one"`) {
			t.Fatalf("per-choice synthesized deltas missing: %q", out)
		}
	})
}

// TestCopyRelayResponseOversizedNonStreaming: a body over maxTransformBody is forwarded raw
// (never buffered/transformed) - the defensive ceiling.
func TestCopyRelayResponseOversizedNonStreaming(t *testing.T) {
	big := `{"choices":[{"message":{"content":"","reasoning":"x"}}],"pad":"` + strings.Repeat("a", maxTransformBody) + `"}`
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(big)),
	}
	rec := httptest.NewRecorder()
	copyRelayResponse(rec, resp, true)
	if rec.Body.String() != big {
		t.Fatalf("oversized body was not forwarded verbatim (len got=%d want=%d)", rec.Body.Len(), len(big))
	}
}
