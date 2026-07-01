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

// voicename.go computes the per-operator voice-name SLUG that forms the second segment of a
// public voice's namespaced id (@<login>/<slug>) and screens that slug for chat-model
// impersonation. The slug is a COMPUTED VIEW over the offer's display Name (founder Q1);
// the raw o.Model a node registers is never touched (it stays the routing key pickFor
// matches). Both the register-time guard (tunnel.go) and the /voices view (voices.go) call
// slugVoiceName so the id a caller sees is exactly the one that was validated.

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
// owner-bound registration (login is the operator handle). For every TTS offer it: derives
// the namespaced slug and rejects an empty-after-normalize name (400); rejects a slug that
// impersonates a chat-model family (400); then screens Name+slug+handle through the EXISTING
// moderation hook (b.mod.screenVoiceRegistration), rejecting with the screen's status (451
// flagged / 503 fail-closed) on a non-allow. Non-TTS offers (chat/stt) are skipped — only a
// TTS offer becomes a public voice. Returns (0,"") when every voice offer is clean; a
// non-zero HTTP code + message otherwise. Does NO locking and NO mutation (the slug is a
// computed view; the raw o.Model is never changed).
func (b *broker) screenVoiceOffers(offers []protocol.ModelOffer, login string) (int, string) {
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
		// SAME @login/<slug> would be indistinguishable public voices — reject the whole
		// register (deterministic ids; the cross-node case is caught by duplicateVoiceName).
		if seen[slug] {
			return http.StatusConflict, fmt.Sprintf("duplicate voice name %q (%q) - this registration already has a voice with that name", o.Name, slug)
		}
		seen[slug] = true
		if res := b.mod.screenVoiceRegistration(o.Name, slug, login); !res.allow() {
			return res.status, res.msg
		}
	}
	return 0, ""
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
			continue // a different operator's node namespaces away by @login/, never a dup
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
