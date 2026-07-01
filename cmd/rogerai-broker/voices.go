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
//
// NamespacedID + Operator are the per-operator attribution layer (founder-approved). ID stays
// the RAW o.Model for BACK-COMPAT + routing (the app treats ID as opaque, and pickFor still
// matches the raw model); NamespacedID is the DUAL-EMITTED @<login>/<slug(Name)> alias for the
// migration window (Q5); Operator is the bare GitHub login for "Name · by @operator" (no "@").
// An UNBOUND (anonymous) node's TTS offer is NOT listed at all (Q2: public voices are signed-in
// operators only), so a listed voice ALWAYS carries an Operator. NamespacedID is present whenever
// a slug is derivable from the display Name — which is always true for a voice that passed the
// register guard (an empty name is rejected there); it is simply omitted if a name is absent.
// Both Operator and NamespacedID are omitempty so anonymous/nameless cases emit no empty field.
type voiceView struct {
	ID              string  `json:"id"`
	NamespacedID    string  `json:"namespaced_id,omitempty"`
	Operator        string  `json:"operator,omitempty"`
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

// pendingVoice is a voice collected under b.mu in phase 1, before its owner is resolved
// off-lock in phase 2. It pairs the address-free voiceView with the node id needed for the
// owner lookup (the node id itself NEVER lands in the payload — it is dropped in phase 2).
type pendingVoice struct {
	nodeID string
	v      voiceView
}

// computeVoices builds the /voices payload: every ON-AIR, OWNER-BOUND public TTS offer as a
// voiceView, cheapest first. Two phases so the owner resolution (a store read) never runs
// under b.mu (the immutable-binding cache warns against store IO under the hot-path lock):
//
//	phase 1 (under b.mu): collect the address-free voice metadata + node id for every on-air,
//	  non-banned, non-private TTS offer (banned/private excluded exactly as from /discover).
//	phase 2 (off b.mu):   resolve each node's operator (AccountOfNode -> OwnerByPubkey ->
//	  Login); DROP an UNBOUND node's voice (Q2: public voices are signed-in operators only)
//	  and a durably-owner-banned operator's voices; then DUAL-EMIT the raw id (ID, unchanged
//	  for routing/back-compat) plus the namespaced alias @<login>/<slug(Name)> (Q5) and the
//	  bare login as Operator. An empty-after-normalize slug can't be listed (it was rejected
//	  at register; belt-and-suspenders, we skip it here too).
//
// SECURITY: only voice display metadata + price are copied; a node's BridgeURL / hostname /
// IP / pubkey / node id are NEVER read into the payload (the node id is used only for the
// owner lookup and then discarded). Result is a pure read of broker state, safe to cache.
func (b *broker) computeVoices() any {
	b.mu.Lock()
	now := time.Now()
	pending := make([]pendingVoice, 0, len(b.nodes))
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
			pending = append(pending, pendingVoice{nodeID: n.NodeID, v: voiceView{
				ID:              o.Model,
				Name:            o.Name,
				PricePer1kChars: pin / 1000, // credits per 1M chars -> per 1k chars (credit == USD today)
				Free:            free || pin == 0,
				LatencyMs:       o.LatencyMS,
				Language:        o.Language,
				SampleURL:       o.SampleURL,
			}})
		}
	}
	b.mu.Unlock()

	out := []voiceView{} // empty serializes as [] (not null) so the app's array decoder never sees null
	for _, p := range pending {
		login, ok := b.operatorLogin(p.nodeID)
		if !ok {
			continue // UNBOUND (anonymous) node: not publicly listable (Q2)
		}
		p.v.Operator = login
		// DUAL-EMIT the namespaced alias when a slug can be derived from the display Name
		// (Q5). ID stays the raw model regardless (back-compat/routing). A NEW public voice
		// always has a valid name (the register guard rejects an empty one), so namespaced_id
		// is present in practice; it is simply omitted if a name is somehow absent.
		if slug, ok := slugVoiceName(p.v.Name); ok {
			p.v.NamespacedID = "@" + login + "/" + slug
		}
		out = append(out, p.v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PricePer1kChars < out[j].PricePer1kChars })
	return map[string]any{"voices": out}
}

// operatorLogin resolves a node's attributable operator handle: the node's TOFU-bound owner
// account (AccountOfNode, via the immutable-binding cache) -> the owner's GitHub login
// (OwnerByPubkey). ok is false when the node is UNBOUND (anonymous), the owner row is
// missing, the login is empty, OR the owner is durably banned — in every such case the
// voice is not publicly listable. NO address is touched; only the login string is returned.
func (b *broker) operatorLogin(nodeID string) (string, bool) {
	pub, ok := b.cachedOwnerOf(nodeID)
	if !ok || pub == "" {
		return "", false
	}
	if b.isOwnerBanned(pub) {
		return "", false // a banned operator's voices never appear
	}
	o, ok, err := b.db.OwnerByPubkey(pub)
	if err != nil || !ok || o.Login == "" {
		return "", false
	}
	return o.Login, true
}
