package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"golang.org/x/text/unicode/norm"
)

// voicename.go computes the voice-name SLUG that forms the second segment of a public voice's
// namespaced id (@<station>/<slug>), parses + RESOLVES that namespaced id back to a node, screens
// the slug for chat-model impersonation, and enforces cross-owner station uniqueness. The slug is
// a COMPUTED VIEW over the offer's display Name (founder Q1); the raw o.Model a node registers is
// never touched (it stays the routing key pickFor matches). The station is the operator's public
// callsign (authoritative from the signed reg.Station). Both the register-time guard (tunnel.go)
// and the /voices view (voices.go) call slugVoiceName so the id a caller sees is exactly the one
// that was validated, and resolveNamespacedVoice uses the SAME slug + station to route it.

// voiceSlugMaxRunes bounds the voice-name segment. The login segment is already GitHub-
// bounded (<=39); this caps the operator-controlled half so a namespaced id can't grow
// without limit. 64 runes is roomy for a human label yet a hard ceiling.
const voiceSlugMaxRunes = 64

// slugVoiceName normalizes a voice display name into the id's second segment: NFKC-fold
// (so a fullwidth/compatibility homoglyph collapses to its ASCII base), lowercase, collapse
// any run of non-[a-z0-9] to a single "-", trim leading/trailing "-", and cap at
// voiceSlugMaxRunes. ok is false when the result is empty (nothing survives normalization)
// so the caller rejects it — an empty slug can never form a valid id.
func slugVoiceName(name string) (slug string, ok bool) {
	folded := strings.ToLower(norm.NFKC.String(name))
	var b strings.Builder
	lastDash := false
	for _, r := range folded {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		// any other rune (space, "/", "@", punctuation, a non-ASCII letter NFKC left
		// intact) becomes a single separating dash — this is what stops a "/" or "@" in a
		// name from forging a second namespace segment or an operator prefix.
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug = strings.Trim(b.String(), "-")
	if slug == "" {
		return "", false
	}
	if r := []rune(slug); len(r) > voiceSlugMaxRunes {
		slug = strings.Trim(string(r[:voiceSlugMaxRunes]), "-")
	}
	return slug, slug != ""
}

// defaultImpersonationDenylist is the built-in set of chat-model family roots a public voice
// name may NOT masquerade as. Matching is PREFIX on the normalized slug (founder Q3), so
// "qwen3-coder-next" (prefix "qwen3", and "qwen") and "gpt-oss-120b" (prefix "gpt", "gpt-oss")
// are caught. Homoglyph/case/whitespace variants fold into the slug BEFORE the check, so
// "ｇｐｔ"/"GPT-OSS"/" Llama 3.2 " all resolve to a matching slug. Override (replace) with
// ROGERAI_VOICE_IMPERSONATION_DENYLIST (comma-separated).
var defaultImpersonationDenylist = []string{
	"qwen", "qwen3", "gpt", "gpt-oss", "llama", "claude", "grok", "mistral", "deepseek", "gemma", "phi",
}

// impersonationDenylist returns the active denylist roots (each itself slug-normalized so an
// env entry like "Acme Brand" matches a slug), env-override replacing the default.
func impersonationDenylist() []string {
	env := strings.TrimSpace(os.Getenv("ROGERAI_VOICE_IMPERSONATION_DENYLIST"))
	src := defaultImpersonationDenylist
	if env != "" {
		src = strings.Split(env, ",")
	}
	out := make([]string, 0, len(src))
	for _, tok := range src {
		if s, ok := slugVoiceName(tok); ok {
			out = append(out, s)
		}
	}
	return out
}

// impersonatesChatModel reports whether a voice-name slug PREFIX-matches a denylisted
// chat-model family root (founder Q3: a plain prefix, not a dash-bounded one, so
// "llama3.2" -> slug "llama3-2" is caught by root "llama", and "gpt-oss-120b" by "gpt").
// This is deliberately strict against masquerade at the cost of blocking a benign name that
// happens to start with a family root (e.g. "gptunes"); the moderation screen is the softer
// catch-all for near-misses and the denylist is env-overridable when a real name is caught.
func impersonatesChatModel(slug string) bool {
	for _, root := range impersonationDenylist() {
		if strings.HasPrefix(slug, root) {
			return true
		}
	}
	return false
}

// screenVoiceOffers is the off-lock half of the public-voice register guard, run for an
// owner-bound registration (station is the operator's public callsign handle). For every TTS
// offer it: derives the namespaced slug and rejects an empty-after-normalize name (400); rejects
// a slug that impersonates a chat-model family (400); then screens Name+slug+station through the
// EXISTING moderation hook (b.mod.screenVoiceRegistration), rejecting with the screen's status
// (451 flagged / 503 fail-closed) on a non-allow. Non-TTS offers (chat/stt) are skipped — only a
// TTS offer becomes a public voice. Returns (0,"") when every voice offer is clean; a non-zero
// HTTP code + message otherwise. Does NO locking and NO mutation (the slug is a computed view; the
// raw o.Model is never changed).
func (b *broker) screenVoiceOffers(offers []protocol.ModelOffer, station string) (int, string) {
	seen := map[string]bool{} // slugs already brought by THIS registration (intra-node dedup)
	for _, o := range offers {
		if o.Modality != protocol.ModalityTTS {
			continue
		}
		slug, ok := slugVoiceName(o.Name)
		if !ok {
			return http.StatusBadRequest, "voice name is empty after normalization - give the voice a name with letters or digits"
		}
		if impersonatesChatModel(slug) {
			return http.StatusBadRequest, fmt.Sprintf("voice name %q impersonates a chat model - pick a name that is not a chat-model family (this list is enforced to keep voices from masquerading as models)", o.Name)
		}
		// Intra-registration collision: two offers on the SAME node whose names slug to the
		// SAME @<station>/<slug> would be indistinguishable public voices — reject the whole
		// register (deterministic ids; the cross-node case is caught by duplicateVoiceName).
		if seen[slug] {
			return http.StatusConflict, fmt.Sprintf("duplicate voice name %q (%q) - this registration already has a voice with that name", o.Name, slug)
		}
		seen[slug] = true
		if res := b.mod.screenVoiceRegistration(o.Name, slug, station); !res.allow() {
			return res.status, res.msg
		}
	}
	return 0, ""
}

// slugStation normalizes a station callsign to the broker-safe slug the node id uses: lowercase,
// collapse every run of non-[a-z0-9] to a single "-", trim leading/trailing "-". This is the SAME
// rule internal/agent.slugify / agent.SlugStation applies when deriving the node-id prefix; the
// broker keeps a tiny local copy rather than importing the node-agent package into the SERVER
// binary, and TestSlugStationMatchesAgent PINS the two byte-for-byte so they can never drift (the
// advertised @<station> and the resolved station must agree). The station arrives client-slugged
// (ShareNodeID slugs it), but a node could send an unslugged value, so the broker re-normalizes
// before attribution/resolution. Empty in => empty out (the caller then treats the node as
// station-less: no public voice). Unlike slugVoiceName it does NOT NFKC-fold or cap length — a
// callsign is ASCII adjective-animal-number by construction.
func slugStation(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// parseNamespacedVoice splits a NAMESPACED voice id "@<station>/<slug>" into its station + voice
// slug. ok is false for a RAW id (no leading "@", the back-compat routing key pickFor matches), or
// a malformed namespaced id (missing "/", empty station, empty slug, OR a slug that carries a
// further "/" — a forged deeper segment can't resolve). The two returned parts are normalized with
// the SAME slug rules the /voices emitter uses (slugStation for the station, slugVoiceName for the
// voice), so a caller-typed id resolves to exactly the id /voices advertised. The station segment
// stops at the FIRST "/", so "@station/a/b" is rejected (not read as station "station", slug "a").
func parseNamespacedVoice(model string) (station, slug string, ok bool) {
	if !strings.HasPrefix(model, "@") {
		return "", "", false // a raw id: not namespaced (routes on the raw model, unchanged)
	}
	rest := model[1:]
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return "", "", false // "@foo" with no "/": not a valid namespaced id
	}
	stRaw, slRaw := rest[:i], rest[i+1:]
	if strings.IndexByte(slRaw, '/') >= 0 {
		return "", "", false // a further "/" in the voice segment: forged deeper segment, no match
	}
	st := slugStation(stRaw)
	sl, sok := slugVoiceName(slRaw)
	if st == "" || !sok {
		return "", "", false
	}
	return st, sl, true
}

// nsCandidate is one on-air offer whose voice-name slug matched the requested slug, collected
// under b.mu in phase 1 before its owner station is resolved off-lock in phase 2 (mirrors
// computeVoices: no store IO under the hot-path lock).
type nsCandidate struct {
	nodeID string
	model  string // the RAW offer model to route on
}

// resolveNamespacedVoice resolves a namespaced voice id's (station, voiceSlug) to the SPECIFIC
// on-air node that serves it, returning that node's RAW offer model + node id. It is modality-
// scoped (a /v1/audio/speech request resolves within tts offers, transcriptions within stt), so a
// same-slug chat model of the same station never cross-routes. Resolution requires BOTH the
// operator STATION and the voice-name SLUG to match — a same-slug voice on a DIFFERENT station, or
// a same-station DIFFERENT slug, does NOT resolve (=> the uniform 503). Off-air / banned / private
// / unbound nodes are excluded exactly as /voices excludes them, so a namespaced id only ever
// resolves to a node that is publicly listable. Two-phase locking: collect slug-matching offers
// under b.mu (phase 1), then match each candidate's operatorStation OFF the lock (phase 2, since
// the owner lookup does store IO). The station-uniqueness guard makes at most one node match; if
// several somehow do (a rename race), the first is chosen — any correct station+slug node bills
// the right operator.
func (b *broker) resolveNamespacedVoice(station, voiceSlug, modality string) (rawModel, nodeID string, ok bool) {
	b.mu.Lock()
	now := time.Now()
	cands := make([]nsCandidate, 0, 4)
	for id, reg := range b.nodes {
		if b.isBanned(id) || b.private[id] {
			continue // banned / private nodes are never public voices
		}
		if now.Sub(b.lastSeen[id]) >= nodeTTL {
			continue // off air
		}
		for _, o := range reg.Offers {
			if o.Modality != modality {
				continue // modality isolation: resolve only within the request's modality
			}
			if sl, sok := slugVoiceName(o.Name); !sok || sl != voiceSlug {
				continue // the voice-name slug must match
			}
			cands = append(cands, nsCandidate{nodeID: id, model: o.Model})
		}
	}
	b.mu.Unlock()

	// Phase 2 (off b.mu): the station is resolved via operatorStation (which reads the owner
	// binding + the node's signed reg.Station), matching the requested station.
	for _, c := range cands {
		if st, sok := b.operatorStation(c.nodeID); sok && st == station {
			return c.model, c.nodeID, true
		}
	}
	return "", "", false
}

// stationClaimedByOther reports the node id of an ON-AIR public (TTS) voice already broadcasting
// under `station` and bound to a DIFFERENT owner account than `selfOwner` (else ""). It is the
// cross-owner station-uniqueness backstop: the auto-generated callsign is ~unique but renameable,
// so two different owners could pick the same public @<station>; the second is refused so the
// handle is unambiguous. The SAME owner reusing their own station (another model / an idempotent
// re-register) is NOT a collision (the owner accounts match). The caller holds b.mu. Mirrors
// duplicateVoiceName's live-node walk: within nodeTTL, TTS offers only, station read from the
// node's signed reg.Station (normalized), owner resolved via AccountOfNode.
func (b *broker) stationClaimedByOther(station, selfOwner string) string {
	station = slugStation(station)
	if station == "" {
		return ""
	}
	now := time.Now()
	for id, reg := range b.nodes {
		if now.Sub(b.lastSeen[id]) >= nodeTTL {
			continue // aged out: not on air
		}
		if slugStation(reg.Station) != station {
			continue // a different (or no) station: no claim on this callsign
		}
		if !offersTTS(reg.Offers) {
			continue // chat/stt-only under this station reserves no public voice
		}
		acct, ok, _ := b.db.AccountOfNode(id)
		if !ok || acct == "" || acct == selfOwner {
			continue // unbound, or the SAME owner reusing their own station: not a collision
		}
		if b.isOwnerBanned(acct) {
			continue // a banned owner's node never appears publicly, so it holds no live claim
		}
		return id // a DIFFERENT owner already broadcasts a public voice under this station
	}
	return ""
}

// duplicateVoiceName reports a duplicate-voice-name message when a NEW TTS offer's slug
// collides with an on-air voice the SAME operator already serves on a DIFFERENT node — an
// operator may not shadow themselves (deterministic namespaced ids). The caller holds b.mu.
// It mirrors ownerOnAirCount's live-node walk (within nodeTTL, excluding this node id) and
// resolves each node's owner via AccountOfNode. Returns "" when there is no collision.
func (b *broker) duplicateVoiceName(owner, self string, offers []protocol.ModelOffer) string {
	// The slugs this registration is bringing on air.
	newSlugs := map[string]string{} // slug -> display name (for the message)
	for _, o := range offers {
		if o.Modality != protocol.ModalityTTS {
			continue
		}
		if slug, ok := slugVoiceName(o.Name); ok {
			newSlugs[slug] = o.Name
		}
	}
	if len(newSlugs) == 0 {
		return ""
	}
	now := time.Now()
	for id, reg := range b.nodes {
		if id == self {
			continue // the node refreshing itself is not a collision with its own prior slug
		}
		if now.Sub(b.lastSeen[id]) >= nodeTTL {
			continue // aged out: not on air
		}
		acct, ok, _ := b.db.AccountOfNode(id)
		if !ok || acct != owner {
			continue // a different operator's node namespaces away (its own @<station>/), never a dup
		}
		for _, o := range reg.Offers {
			if o.Modality != protocol.ModalityTTS {
				continue
			}
			existing, ok := slugVoiceName(o.Name)
			if !ok {
				continue
			}
			if name, dup := newSlugs[existing]; dup {
				return fmt.Sprintf("you already have a voice named %q (%q) - pick a different name", name, existing)
			}
		}
	}
	return ""
}
