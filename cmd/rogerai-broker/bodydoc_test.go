package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// bodydoc_test.go - the one-decode body (contract §14.B #12) answers every question exactly as
// the per-question whole-body decodes it replaced. The reference implementations below are
// those decodes, verbatim; each table row is run through both.

func refPromptText(body []byte) string {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function json.RawMessage `json:"function"`
		} `json:"tools"`
		Functions []json.RawMessage `json:"functions"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	var b bytes.Buffer
	for _, msg := range req.Messages {
		var s string
		if json.Unmarshal(msg.Content, &s) == nil {
			b.WriteString(s)
			b.WriteByte('\n')
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(msg.Content, &parts) == nil {
			for _, p := range parts {
				b.WriteString(p.Text)
				b.WriteByte('\n')
			}
		}
	}
	for _, t := range req.Tools {
		collectStrings(t.Function, &b)
	}
	for _, f := range req.Functions {
		collectStrings(f, &b)
	}
	return b.String()
}

func refNeedsVision(body []byte) bool {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	for _, m := range req.Messages {
		if partsHaveImage(m.Content) {
			return true
		}
	}
	return false
}

func refNeedsTools(body []byte) bool {
	var req struct {
		Tools []json.RawMessage `json:"tools"`
	}
	return json.Unmarshal(body, &req) == nil && len(req.Tools) > 0
}

func refParamsNeedTools(body []byte) bool {
	var req struct {
		ToolChoice     json.RawMessage `json:"tool_choice"`
		ResponseFormat struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	return (len(req.ToolChoice) > 0 && !isJSONNull(req.ToolChoice)) ||
		req.ResponseFormat.Type == "json_object" || req.ResponseFormat.Type == "json_schema"
}

func refStated(body []byte) int {
	var req struct {
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	}
	_ = json.Unmarshal(body, &req)
	if req.MaxCompletionTokens > 0 {
		return req.MaxCompletionTokens
	}
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return 0
}

func refIncludeUsage(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	if _, ok := m["stream"]; !ok {
		return body
	}
	so := map[string]json.RawMessage{}
	if raw, ok := m["stream_options"]; ok {
		_ = json.Unmarshal(raw, &so)
	}
	if so == nil {
		so = map[string]json.RawMessage{}
	}
	if _, set := so["include_usage"]; set {
		return body
	}
	so["include_usage"] = json.RawMessage("true")
	sob, _ := json.Marshal(so)
	m["stream_options"] = sob
	out, _ := json.Marshal(m)
	return out
}

var bodyDocCases = []string{
	`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
	`{"model":"m","Messages":[{"role":"user","content":"case-folded key"}]}`,
	`{"model":"m","messages":[{"content":"first"}],"messages":[{"content":"last wins"}]}`,
	`{"messages":[{"content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}}]}]}`,
	`{"messages":[{"content":[{"type":"image_url","text":5}]}]}`,
	`{"messages":[{"content":[{"type":7,"text":"t"}]}]}`,
	`{"messages":[{"content":[{"type":"image_url"}]},{"content":"s"}],"tools":"not-an-array"}`,
	`{"messages":"nope","tools":[{"function":{"name":"f","description":"d"}}]}`,
	`{"messages":[],"tools":[{"type":"function","function":{"name":"run","parameters":{"x":{"description":"deep"}}}}],"functions":[{"name":"g"}]}`,
	`{"tools":[1,2]}`,
	`{"tools":[]}`,
	`{"TOOLS":[{}]}`,
	`{"tool_choice":"auto"}`,
	`{"tool_choice":null,"response_format":{"type":"json_schema"}}`,
	`{"tool_choice":"auto","response_format":"text"}`,
	`{"max_tokens":100}`,
	`{"max_tokens":100,"max_completion_tokens":50}`,
	`{"max_tokens":"100","max_completion_tokens":7}`,
	`{"max_tokens":12.5}`,
	`{"Max_Tokens":64}`,
	`{"max_tokens":-1,"max_completion_tokens":0}`,
	`{"stream":true}`,
	`{"stream":true,"stream_options":null}`,
	`{"stream":true,"stream_options":{"include_usage":false}}`,
	`{"stream":true,"stream_options":{"continuous_usage_stats":true},"z":"<b>&</b>","a":[1, 2]}`,
	`{"stream":false,"x":1,"x":2}`,
	`{"model":"m"} trailing`,
	`[1,2]`,
	`not json`,
	``,
}

func TestReqDocMatchesTheDecodesItReplaced(t *testing.T) {
	for _, c := range bodyDocCases {
		b := []byte(c)
		d := decodeReqDoc(b)
		text, vision := d.promptScan()
		require.Equal(t, refPromptText(b), text, "prompt text of %s", c)
		require.Equal(t, refNeedsVision(b), vision, "vision of %s", c)
		require.Equal(t, refNeedsTools(b), d.needsTools(), "tools of %s", c)
		require.Equal(t, refParamsNeedTools(b), d.paramsNeedTools(), "params tools of %s", c)
		require.Equal(t, refStated(b), d.outLimits().stated(), "stated output of %s", c)
		require.Equal(t, string(refIncludeUsage(b)), string(d.withIncludeUsage().body), "include_usage of %s", c)
		var top map[string]json.RawMessage
		require.Equal(t, json.Unmarshal(b, &top) == nil, d.ok, "object-ness of %s", c)
	}
}

func TestReqDocRewriteIsRewriteBody(t *testing.T) {
	set := map[string]json.RawMessage{"model": json.RawMessage(`"m2"`), "max_tokens": json.RawMessage(`9`)}
	for _, c := range bodyDocCases {
		d := decodeReqDoc([]byte(c))
		if !d.ok {
			require.Equal(t, c, string(d.rewrite(carrierKeys, set).body), "a non-object is returned as given")
			continue
		}
		want := rewriteBody([]byte(c), true, set)
		got := d.rewrite(carrierKeys, set)
		require.Equal(t, string(want), string(got.body), "rewrite of %s", c)
		require.Equal(t, string(rewriteBody(want, false, map[string]json.RawMessage{"x": json.RawMessage(`1`)})),
			string(got.rewrite(nil, map[string]json.RawMessage{"x": json.RawMessage(`1`)}).body), "a rewrite of a rewrite needs no parse")
	}
}

func TestOutLimitsCapSetIsCapBody(t *testing.T) {
	for _, c := range []string{`{}`, `{"max_tokens":100}`, `{"max_completion_tokens":100}`, `{"max_tokens":5,"max_completion_tokens":500}`, `{"max_tokens":3}`} {
		for _, buys := range []int{-1, 0, 4, 50, 1000} {
			d := decodeReqDoc([]byte(c))
			lim := d.outLimits()
			got := []byte(c)
			if set := lim.capSet(buys); set != nil {
				got = d.rewrite(nil, set).body
				require.Equal(t, refStated(got), lim.apply(set).stated(), "limits after the cap on %s at %d", c, buys)
			}
			require.Equal(t, string(capBody([]byte(c), buys)), string(got), "cap %d on %s", buys, c)
		}
	}
}

func TestPickBudgetCountsAndRefuses(t *testing.T) {
	t.Setenv("ROGERAI_PICK_BUDGET", "2")
	p := newPickBudget()
	require.True(t, p.take())
	require.True(t, p.take())
	require.False(t, p.over())
	require.False(t, p.take(), "the third pick is over a budget of 2")
	require.True(t, p.over())
	require.EqualValues(t, 2, p.count(), "the count is the picks that ran")
	var none *pickBudget
	require.True(t, none.take(), "no budget (audio, probes) is unbounded")
	require.False(t, none.over())
	require.Zero(t, none.count())
	for _, v := range []string{"", "0", "-3", "x"} {
		t.Setenv("ROGERAI_PICK_BUDGET", v)
		require.Equal(t, 64, pickBudgetLimit(), "%q falls back to the default", v)
	}
}

func TestCapFilterMirrorsTheBilledPrice(t *testing.T) {
	c := capFilter{usd: 0.001, prompt: 1000}
	require.True(t, c.drops("n", 1000, 1, false), "the input alone costs the cap")
	require.False(t, c.drops("n", 0.5, 1, false), "half the cap on input leaves output to buy")
	require.False(t, c.drops("n", 1000, 1, true), "free right now")
	c.own = map[string]bool{"mine": true}
	require.False(t, c.drops("mine", 1000, 1, false), "self-use is $0")
	g := capFilter{usd: 0.001, prompt: 1000, grant: true, grantIn: 1000, grantOut: 1}
	require.True(t, g.drops("n", 0, 0, true), "a priced grant bills its own price")
	g.grantIn, g.grantOut = 0, 0
	require.False(t, g.drops("n", 1000, 1000, false), "a free grant bills nothing")
	require.False(t, capFilter{}.drops("n", 1e9, 1e9, false), "no cap")
}
