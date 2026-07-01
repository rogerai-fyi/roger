package main

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// voiceView is one entry in GET /voices — the shape the built iOS picker consumes (roger-ios
// docs/BROKER-VOICE-API.md). It carries voice DISPLAY METADATA only; a node's bridge URL,
// hostname, or IP is NEVER included (the broker proxies all node traffic, like the chat bridge).
type voiceView struct {
	ID              string  `json:"id"`
	Name            string  `json:"name,omitempty"`
	Provider        string  `json:"provider,omitempty"`
	PricePer1kChars float64 `json:"price_per_1k_chars"`
	Free            bool    `json:"free"`
	LatencyMs       int     `json:"latency_ms,omitempty"`
	Language        string  `json:"language,omitempty"`
	SampleURL       string  `json:"sample_url,omitempty"`
}

// voices handles GET /voices: the anonymous voice picker (mirrors /discover — no auth, per-IP
// rate-limited, short-TTL cached). Lists the on-air TTS stations in the app's shape.
func (b *broker) voices(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	if ok, retry := b.anonRL.allow(clientIP(r)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
		return
	}
	cors(w)
	b.serveCachedJSON(w, "voices", publicMarketTTL, b.computeVoices)
}

// computeVoices builds the /voices payload: every ON-AIR public TTS offer as a voiceView, cheapest
// first. A READ of broker state (safe to cache). SECURITY: it copies voice metadata + price only —
// a node's BridgeURL / hostname / IP is never read into the payload (banned + private nodes are
// excluded exactly as they are from /discover).
func (b *broker) computeVoices() any {
	b.mu.Lock()
	now := time.Now()
	out := []voiceView{} // empty serializes as [] (not null) so the app's array decoder never sees null
	for _, n := range b.nodes {
		if b.isBanned(n.NodeID) || b.private[n.NodeID] {
			continue
		}
		if time.Since(b.lastSeen[n.NodeID]) >= nodeTTL {
			continue // off air
		}
		for _, o := range n.Offers {
			if o.Modality != protocol.ModalityTTS {
				continue
			}
			pin, _, free, _ := o.ActivePrice(now)
			out = append(out, voiceView{
				ID:              o.Model,
				Name:            o.Name,
				PricePer1kChars: pin / 1000, // credits per 1M chars -> per 1k chars (credit == USD today)
				Free:            free || pin == 0,
				LatencyMs:       o.LatencyMS,
				Language:        o.Language,
				SampleURL:       o.SampleURL,
			})
		}
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].PricePer1kChars < out[j].PricePer1kChars })
	return map[string]any{"voices": out}
}
