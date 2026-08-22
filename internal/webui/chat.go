package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"rogerai.fm/roger/v5/internal/client"
)

// chat.go - the console's CHAT tab (founder 2026-08-20: the chat mechanics lived only
// in the TUI, so the browser console could put a GPU on air and read receipts but not
// actually TALK to a band).
//
// It relays through the SAME broker path the TUI's in-channel chat uses
// (client.ChatTurns), so failover, billing, the consumer price cap and the honest
// error surfacing are shared rather than reimplemented - a second code path here would
// be a second set of bugs and, worse, a second set of receipts.
//
// The conversation itself is HELD BY THE BROWSER and posted back each turn. The server
// keeps no chat state: the console is a live twin of a node, not a chat host, and a
// server-side transcript would be one more place a private conversation could linger.

// chatReq is the browser's turn: the whole conversation so far, the model to send it
// to, and the optional per-turn price ceiling.
type chatReq struct {
	Model        string            `json:"model"`
	Messages     []client.ChatTurn `json:"messages"`
	Confidential bool              `json:"confidential"`
	MaxOut       float64           `json:"max_out"`
}

// chatResp carries the reply AND its receipt. The receipt is not decoration: this
// console's whole claim is that you can see what a turn cost and which machine served
// it, so the chat tab shows the same numbers the TUI's meter does.
type chatResp struct {
	OK    bool   `json:"ok"`
	Reply string `json:"reply,omitempty"`
	Error string `json:"error,omitempty"`
	// Message duplicates Error under the key the console's shared api() helper reads
	// for every other endpoint. Without it a chat failure would surface as the bare
	// HTTP status text and the real cause - "no node offers", a timeout, the broker's
	// own words - would be dropped exactly where it matters most.
	Message   string  `json:"message,omitempty"`
	Provider  string  `json:"provider,omitempty"`
	Cost      float64 `json:"cost"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	TPS       float64 `json:"tps"`
	LatencyMS int64   `json:"latency_ms"`
}

// chatMaxTurns bounds what one POST may carry. A runaway page (or a pasted book) must
// not be able to push an unbounded body through the broker on the operator's key.
const chatMaxTurns = 200

// handleChat relays one conversation turn. POST-only via s.action, token-gated like
// every other write - it spends money, so it is a write even though it mutates no
// node state.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if !decode(r, &req) {
		writeChatErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if s.opts.Broker == "" {
		writeChatErr(w, http.StatusServiceUnavailable, "no broker configured - chat needs one to reach a band")
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeChatErr(w, http.StatusBadRequest, "pick a model first")
		return
	}
	if len(req.Messages) == 0 {
		writeChatErr(w, http.StatusBadRequest, "nothing to send")
		return
	}
	if len(req.Messages) > chatMaxTurns {
		writeChatErr(w, http.StatusRequestEntityTooLarge, "conversation too long - start a new one")
		return
	}
	// No freq: the console reaches the open market only. A private band's code is a
	// secret the browser has never been given, and inventing a path for it here would
	// mean putting one somewhere it could be read.
	res, err := client.ChatTurns(s.opts.Broker, s.opts.User, req.Model, req.Messages, req.Confidential, req.MaxOut, "")
	if err != nil {
		// Surfaced verbatim, exactly as the TUI does: a missing station, a slow-inference
		// timeout and the broker's own error body each say a different thing, and
		// flattening them to "chat failed" is how a user ends up retrying the one that
		// was never going to work.
		writeChatErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, chatResp{
		OK:        true,
		Reply:     res.Reply,
		Provider:  res.Provider,
		Cost:      res.Cost,
		TokensIn:  res.TokensIn,
		TokensOut: res.TokensOut,
		TPS:       res.TPS,
		LatencyMS: res.Latency.Milliseconds(),
	})
}

func writeChatErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(chatResp{OK: false, Error: msg, Message: msg})
}
