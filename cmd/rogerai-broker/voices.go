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
// matches the raw model); NamespacedID is the DUAL-EMITTED @<station>/<slug(Name)> alias and IS
// ROUTABLE (a caller may pass it as `voice`/`model` and the relay resolves it to this exact node,
// billing this operator — see resolveNamespacedVoice); Operator is the bare STATION CALLSIGN for
// "Name · by @operator" (no "@"). The station is the owner's non-sensitive, auth-agnostic broadcast
// handle (works for Apple-only accounts, unlike a GitHub login), authoritative from the signed
// reg.Station. An UNBOUND (anonymous) node's TTS offer is NOT listed (Q2: attributable operators
// only), and a node carrying no station has no public namespace, so a listed voice ALWAYS carries
// an Operator. NamespacedID is present whenever a slug is derivable from the display Name (always
// true for a voice that passed the register guard). Both are omitempty so nameless/station-less
// cases emit no empty field.
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
//	phase 2 (off b.mu):   resolve each node's operator STATION (operatorStation: owner-bound +
//	  not owner-banned + a signed reg.Station); DROP an UNBOUND / banned / station-less node's
//	  voice (Q2: public voices are attributable operators only); then DUAL-EMIT the raw id (ID,
//	  unchanged for routing/back-compat) plus the ROUTABLE namespaced alias @<station>/<slug(Name)>
//	  and the bare station as Operator. An empty-after-normalize slug can't be listed (it was
//	  rejected at register; belt-and-suspenders, we skip it here too).
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
		station, ok := b.operatorStation(p.nodeID)
		if !ok {
			continue // UNBOUND (anonymous) / banned / station-less node: not publicly listable (Q2)
		}
		p.v.Operator = station
		// DUAL-EMIT the namespaced alias @<station>/<slug(Name)> when a slug can be derived from
		// the display Name. ID stays the raw model regardless (back-compat/routing). A NEW public
		// voice always has a valid name (the register guard rejects an empty one), so namespaced_id
		// is present in practice; it is simply omitted if a name is somehow absent.
		if slug, ok := slugVoiceName(p.v.Name); ok {
			p.v.NamespacedID = "@" + station + "/" + slug
		}
		out = append(out, p.v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PricePer1kChars < out[j].PricePer1kChars })
	return map[string]any{"voices": out}
}

// operatorStation resolves a node's public STATION callsign — the per-machine broadcast handle
// (@<station>/…) a public voice is attributed to + routed by. It requires the node to be
// OWNER-BOUND (so anonymous supply stays unlisted, Q2), NOT durably owner-banned (a banned
// operator's voices never appear), AND to carry a station (the signed reg.Station field; a node
// that predates the field has none, so no public voice). The station is AUTHORITATIVE from the
// signed registration (regSigningBytes covers it, so it can't be forged/stripped) — the node id's
// prefix is deliberately NOT parsed back out (slugify is lossy). NO address is touched.
func (b *broker) operatorStation(nodeID string) (string, bool) {
	pub, ok := b.cachedOwnerOf(nodeID)
	if !ok || pub == "" {
		return "", false // UNBOUND (anonymous): not publicly listable
	}
	if b.isOwnerBanned(pub) {
		return "", false // a banned operator's voices never appear
	}
	b.mu.Lock()
	st := b.nodes[nodeID].Station
	b.mu.Unlock()
	st = slugStation(st)
	if st == "" {
		return "", false // no station carried => no public namespace for this node
	}
	return st, true
}
