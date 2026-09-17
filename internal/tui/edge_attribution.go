package tui

// WHO OPENED IT. Every receipted turn through the TUI's endpoint is a session on the Edge
// (features/edge/session_attribution.feature, @tui). The proxy hands the receipt back and
// never guesses the caller; THIS is where the attribution is decided: the guest's name while
// a guest holds the mic (its exec to its return), `roger use` otherwise - because an external
// program pointed at the TUI's endpoint is exactly what `roger use` serves.

import (
	"sync"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
)

// edgeMic is who holds the endpoint. Shared by pointer between the model's copies and the
// live proxy options, which read it at receipt time on the proxy's goroutine.
type edgeMic struct {
	mu    sync.Mutex
	guest string
}

func (c *edgeMic) take(name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.guest = name
	c.mu.Unlock()
}

func (c *edgeMic) drop() { c.take("") }

func (c *edgeMic) holder() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.guest
}

// edgeReceiptSink is the endpoint's OnReceipt: nil when no session ledger is wired (the
// relay then behaves exactly as before), otherwise a recorder that attributes at call time.
func (m model) edgeReceiptSink() func(protocol.UsageReceipt) {
	led, mic := m.hooks.EdgeSessions, m.mic
	if led == nil {
		return nil
	}
	return func(rec protocol.UsageReceipt) {
		if rec.RequestID == "" {
			return
		}
		t := edge.Traffic{Account: led.Account(), Kind: edge.FromUse, Request: rec.RequestID,
			Receipts: []protocol.UsageReceipt{rec}}
		if g := mic.holder(); g != "" {
			t.Kind, t.Who = edge.FromGuest, g
		}
		_, _ = led.Record(t)
	}
}
