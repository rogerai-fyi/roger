package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// moderation is the broker's mandatory pre-dispatch content screen. The broker is
// the single choke point where an illegal prompt (CSAM and similar) can be blocked
// BEFORE it ever reaches a provider, so the screen lives here, not on the nodes.
//
// It is a pluggable hook with two backends, chosen by MODERATION_PROVIDER:
//
//   - "url" (default when MODERATION_URL is set): point MODERATION_URL at an
//     OpenAI-moderation-compatible endpoint (the OpenAI Moderation API, or a small
//     adapter in front of a self-hosted Llama Guard) that speaks {input}->{flagged}.
//   - "groq": NATIVE Groq Llama Guard. No URL/adapter needed - the broker calls
//     Groq's OpenAI-compatible chat/completions with a Llama Guard model (reusing the
//     same GROQ_API_KEY the concierge uses) and parses the "safe"/"unsafe" verdict.
//
// When MODERATION_PROVIDER is empty the backend is inferred: "url" if MODERATION_URL
// is set, else "groq" if a GROQ_API_KEY is present, else off. The broker itself runs
// no model - it just calls an endpoint - so this hook adds no model dependency until
// you configure one.
//
// Posture:
//   - no backend configured, require=false -> DISABLED (dev only): pass through, with
//     a loud startup warning. NOT safe for real public traffic.
//   - ROGERAI_REQUIRE_MODERATION=1 -> fail CLOSED: if the screen is unset or
//     unreachable, requests are rejected (503) rather than served unscreened.
//
// Launch to real public traffic MUST run with a backend set and require=true.
type moderation struct {
	provider string // "" / "url" / "groq" (resolved at load)
	url      string
	require  bool
	client   *http.Client

	// Groq Llama Guard backend (provider=="groq").
	groqKey   string
	groqURL   string
	groqModel string

	// csamCats is the set of policy category codes (lowercased) that mark a hit as
	// child sexual abuse material - the legally-distinct class that must be PRESERVED
	// and REPORTED (US 18 USC 2258A), not just rejected+discarded. Defaults to Llama
	// Guard's S4 plus the OpenAI Moderation "sexual/minors" category; configurable via
	// ROGERAI_CSAM_CATEGORIES (comma-separated). Matching is case-insensitive.
	csamCats map[string]bool
}

// modResult is the outcome of a content screen. status==0 means ALLOW; a non-zero
// status is the HTTP code to reject with (451 flagged / 503 fail-closed). csam is true
// ONLY for a child-exploitation hit (a matched csamCats category), which the relay must
// PRESERVE + QUEUE for a CyberTipline report rather than silently discard; category is
// the matched category string (for the incident record + log).
type modResult struct {
	status   int
	msg      string
	csam     bool
	category string
}

// allow reports whether the screen passed (nothing to reject).
func (r modResult) allow() bool { return r.status == 0 }

// defaultModerationModel is the current Groq content-safety model. Groq decommissioned
// the Llama Guard family (meta-llama/llama-guard-4-12b now 404s), so we use OpenAI's
// open-weight safeguard model, which classifies against a POLICY we supply in the system
// message (see moderationPolicy) and answers "safe" / "unsafe <codes>". Override with
// MODERATION_MODEL to fix the name without a redeploy if Groq retires/renames it.
const defaultModerationModel = "openai/gpt-oss-safeguard-20b"

// moderationPolicy is the system prompt that turns the safeguard model into a strict
// classifier with a STABLE, parseable output. Category codes mirror the Llama Guard
// taxonomy so the existing CSAM detection (S4) and ROGERAI_CSAM_CATEGORIES keep working.
// The model answers "safe" or "unsafe <codes>" on a single line; its chain-of-thought
// goes to a SEPARATE reasoning channel (parsed out by contentText), not the verdict.
const moderationPolicy = `You are a strict content-safety classifier. Classify the USER message against this policy.
Categories:
S1 Violent crimes or credible threats of violence
S2 Non-violent crimes (fraud, hacking, theft, trafficking)
S3 Sex-related crimes (sexual assault, coercion)
S4 Child sexual exploitation (ANY sexual content involving a minor)
S5 Weapons of mass harm (explosives, biological, chemical, nuclear, or radiological)
S6 Self-harm or suicide encouragement
S7 Hate or harassment targeting a protected group
S8 Dangerous regulated goods (illegal drug synthesis, untraceable firearms)
Output EXACTLY one line and nothing else:
- "safe" if the message violates no category
- "unsafe <codes>" where <codes> is a comma-separated list of the violated category codes (e.g. "unsafe S4" or "unsafe S1,S5")
Do not explain in the answer. Output only that one line.`

// defaultCSAMCategories is the built-in child-exploitation category set: Llama Guard's
// S4 ("Child Sexual Exploitation") and the OpenAI Moderation "sexual/minors" category.
// Override (replace) with ROGERAI_CSAM_CATEGORIES.
var defaultCSAMCategories = []string{"s4", "sexual/minors"}

func loadModeration() moderation {
	m := moderation{
		provider: strings.ToLower(strings.TrimSpace(os.Getenv("MODERATION_PROVIDER"))),
		url:      os.Getenv("MODERATION_URL"),
		require:  os.Getenv("ROGERAI_REQUIRE_MODERATION") == "1",
		// The safeguard model is a 20B reasoning classifier; give it more headroom than a
		// tiny Llama Guard pass needed (reasoning tokens count) so a legitimate verdict is
		// not cut off into a fail-open/closed on every request.
		client: &http.Client{Timeout: 12 * time.Second},

		// Dedicated moderation key (MODERATION_GROQ_KEY) so guard traffic is attributable +
		// rate-limited separately from the concierge's GROQ_API_KEY; fall back to the shared
		// key when the dedicated one is unset.
		groqKey:   firstNonEmpty(os.Getenv("MODERATION_GROQ_KEY"), os.Getenv("GROQ_API_KEY")),
		groqURL:   "https://api.groq.com/openai/v1/chat/completions",
		groqModel: defaultModerationModel,
	}
	if v := strings.TrimSpace(os.Getenv("MODERATION_MODEL")); v != "" {
		m.groqModel = v
	}
	m.csamCats = loadCSAMCategories(os.Getenv("ROGERAI_CSAM_CATEGORIES"))
	// Resolve the backend. An explicit MODERATION_PROVIDER wins; otherwise infer it
	// from what is configured (a MODERATION_URL implies "url"; else a GROQ_API_KEY
	// implies "groq"). The result is one of "", "url", "groq".
	switch m.provider {
	case "url", "groq":
		// explicit; keep as-is
	default:
		switch {
		case m.url != "":
			m.provider = "url"
		case m.groqKey != "":
			m.provider = "groq"
		default:
			m.provider = ""
		}
	}

	switch {
	case m.provider == "" && m.require:
		log.Printf("MODERATION: REQUIRED but no backend configured - all requests will be blocked (fail-closed). Set MODERATION_URL, or MODERATION_PROVIDER=groq + GROQ_API_KEY.")
	case m.provider == "":
		log.Printf("MODERATION: DISABLED (no backend). NOT SAFE FOR PUBLIC TRAFFIC - set MODERATION_URL (or MODERATION_PROVIDER=groq + GROQ_API_KEY) + ROGERAI_REQUIRE_MODERATION=1 before launch.")
	case m.provider == "groq" && m.groqKey == "":
		log.Printf("MODERATION: provider=groq but no key set - requests fail %s. Set MODERATION_GROQ_KEY (or GROQ_API_KEY).", failMode(m.require))
	case m.provider == "url" && m.url == "":
		log.Printf("MODERATION: provider=url but MODERATION_URL is unset - requests fail %s.", failMode(m.require))
	case m.provider == "groq":
		keySrc := "GROQ_API_KEY"
		if os.Getenv("MODERATION_GROQ_KEY") != "" {
			keySrc = "MODERATION_GROQ_KEY"
		}
		log.Printf("MODERATION: enabled via Groq safeguard model %s (key=%s, require=%v)", m.groqModel, keySrc, m.require)
	default:
		log.Printf("MODERATION: enabled via %s (require=%v)", m.url, m.require)
	}
	// Surface the legal preserve+report obligation at startup whenever the screen is
	// on: a CSAM (child-exploitation) hit is PRESERVED to rogerai.csam_incidents and a
	// CyberTipline report is QUEUED (US 18 USC 2258A), not silently discarded.
	if m.provider != "" {
		log.Printf("MODERATION: CSAM categories %v -> hits are PRESERVED + a CyberTipline report is QUEUED (18 USC 2258A); other unsafe categories are 451-rejected only", sortedKeys(m.csamCats))
	}
	return m
}

// firstNonEmpty returns the first non-empty (after trim) string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// loadCSAMCategories parses the configurable child-exploitation category set from a
// comma-separated env value (case-folded), falling back to the built-in default.
func loadCSAMCategories(env string) map[string]bool {
	out := map[string]bool{}
	add := func(list []string) {
		for _, c := range list {
			if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
				out[c] = true
			}
		}
	}
	if strings.TrimSpace(env) != "" {
		add(strings.Split(env, ","))
	}
	if len(out) == 0 {
		add(defaultCSAMCategories)
	}
	return out
}

// sortedKeys returns a set's keys sorted, for a stable startup log line.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isCSAM reports whether any of the matched policy categories falls in the configured
// CSAM set, and returns the first matched category (for the incident record + log).
// Category names are compared case-insensitively. The list is the raw category tokens
// from either backend (Llama Guard "S4"/"S1" codes, or OpenAI category keys).
func (m moderation) isCSAM(cats []string) (bool, string) {
	for _, c := range cats {
		if m.csamCats[strings.ToLower(strings.TrimSpace(c))] {
			return true, c
		}
	}
	return false, ""
}

// failMode renders the fail posture for a startup log line.
func failMode(require bool) string {
	if require {
		return "CLOSED (rejected)"
	}
	return "OPEN (served)"
}

// screen checks the prompt text before dispatch. It returns status=0 to ALLOW, or
// an HTTP status + message to REJECT with: 451 when the content is flagged by the
// policy, 503 when the screen is required but unavailable (fail-closed). When not
// required, a configuration or transport problem fails open (logged) so a screen
// outage does not take the marketplace down in non-launch posture.
func (m moderation) screen(text string) modResult {
	// Backend not configured (or configured but missing its credential).
	switch {
	case m.provider == "",
		m.provider == "url" && m.url == "",
		m.provider == "groq" && m.groqKey == "":
		if m.require {
			return modResult{status: http.StatusServiceUnavailable, msg: "content screening required but not configured"}
		}
		return modResult{}
	}
	// Empty input has nothing to screen - short-circuit ALLOW and skip the network
	// round-trip (this is on the hot dispatch path). A no-text request is handled by
	// the dispatch logic, not by the content policy.
	if strings.TrimSpace(text) == "" {
		return modResult{}
	}
	if m.provider == "groq" {
		return m.screenGroq(text)
	}
	body, _ := json.Marshal(map[string]string{"input": text})
	resp, err := m.client.Post(m.url, "application/json", bytes.NewReader(body))
	if err != nil {
		if m.require {
			return modResult{status: http.StatusServiceUnavailable, msg: "content screening unavailable"}
		}
		log.Printf("MODERATION: screen unreachable (%v), failing open (require=false)", err)
		return modResult{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if m.require {
			return modResult{status: http.StatusServiceUnavailable, msg: "content screening error"}
		}
		// Fail-open path: log every skipped incident so it is recorded for review
		// (matches the groq backend's fail-open log). Policy is unchanged - REQUIRE
		// still controls open vs closed; this only guarantees the log.
		log.Printf("MODERATION: screen returned HTTP %d, failing open (require=false)", resp.StatusCode)
		return modResult{}
	}
	// Accept the OpenAI Moderation shape {"results":[{"flagged":bool,"categories":{...}}]}
	// and a simpler adapter shape {"flagged":bool} (e.g. a Llama Guard wrapper). The
	// per-category map (true = matched) is parsed only to log WHY something was blocked;
	// the block decision is the boolean flagged.
	var out struct {
		Flagged    bool            `json:"flagged"`
		Categories map[string]bool `json:"categories"`
		Results    []struct {
			Flagged    bool            `json:"flagged"`
			Categories map[string]bool `json:"categories"`
		} `json:"results"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	flagged := out.Flagged
	cats := out.Categories
	for _, r := range out.Results {
		if r.Flagged {
			flagged = true
			if r.Categories != nil {
				cats = r.Categories
			}
		}
	}
	if flagged {
		matched := matchedCategoryList(cats)
		if hit := strings.Join(matched, ", "); hit != "" {
			log.Printf("MODERATION: blocked (categories: %s)", hit)
		}
		if csam, cat := m.isCSAM(matched); csam {
			return modResult{status: http.StatusUnavailableForLegalReasons, msg: "request blocked by the content policy", csam: true, category: cat}
		}
		return modResult{status: http.StatusUnavailableForLegalReasons, msg: "request blocked by the content policy"}
	}
	return modResult{}
}

// screenGroq screens text with the Groq-hosted safeguard model over Groq's
// OpenAI-compatible chat/completions endpoint (the same shape concierge.go uses). The
// model is given moderationPolicy as a system prompt and classifies the USER message,
// answering "safe" (ALLOW) or "unsafe <codes>" with the violated category codes, which we
// capture for the block log. Its chain-of-thought lands in a SEPARATE reasoning channel,
// so we parse message.content ONLY (contentText), not the reasoning. Honors the same
// fail-open/closed posture as the URL backend: on a transport/non-200/empty error,
// fail-closed (503) when required, else fail-open (served). Caller short-circuited empty.
func (m moderation) screenGroq(text string) modResult {
	payload := map[string]any{
		"model": m.groqModel,
		"messages": []map[string]any{
			{"role": "system", "content": moderationPolicy},
			{"role": "user", "content": text},
		},
		"temperature": 0,
		// Headroom for the reasoning channel + the one-line verdict (reasoning tokens count
		// against this budget; too small truncates the verdict to empty -> a false fail).
		"max_tokens": 512,
		// Keep the safety classifier fast and deterministic - it needs only a brief rationale.
		"reasoning_effort": "low",
		"stream":           false,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, m.groqURL, bytes.NewReader(body))
	if err != nil {
		return m.groqFailMode("build request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.groqKey)
	resp, err := m.client.Do(req)
	if err != nil {
		return m.groqFailMode("transport", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m.groqFailMode("status "+http.StatusText(resp.StatusCode), nil)
	}
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	verdict := strings.TrimSpace(contentText(rb))
	if verdict == "" {
		// No verdict text is an error condition, not an implicit allow.
		return m.groqFailMode("empty verdict", nil)
	}
	// The model answers "safe" or "unsafe <codes>" (codes on the same line). A response
	// that does not clearly begin with "safe" is treated as flagged (fail toward blocking
	// on this safety surface). Category codes are captured for the log + CSAM detection.
	low := strings.ToLower(verdict)
	if low == "safe" || strings.HasPrefix(low, "safe\n") || strings.HasPrefix(low, "safe\r") || strings.HasPrefix(low, "safe ") || strings.HasPrefix(low, "safe.") {
		return modResult{}
	}
	// Collect category-looking tokens (S1, S4, ...) from the whole verdict, dropping the
	// "safe"/"unsafe" keywords, so the layout (same-line or next-line) does not matter.
	var matched []string
	for _, tok := range splitCategories(strings.ReplaceAll(strings.ReplaceAll(verdict, "\r", " "), "\n", " ")) {
		switch strings.ToLower(strings.TrimSpace(tok)) {
		case "unsafe", "safe", "":
			continue
		}
		matched = append(matched, tok)
	}
	if len(matched) > 0 {
		log.Printf("MODERATION: blocked by safeguard (categories: %s)", strings.Join(matched, ", "))
	} else {
		log.Printf("MODERATION: blocked by safeguard (verdict: %.40q)", verdict)
	}
	if csam, cat := m.isCSAM(matched); csam {
		return modResult{status: http.StatusUnavailableForLegalReasons, msg: "request blocked by the content policy", csam: true, category: cat}
	}
	return modResult{status: http.StatusUnavailableForLegalReasons, msg: "request blocked by the content policy"}
}

// contentText extracts ONLY the assistant message content from an OpenAI/Groq
// chat-completions response - deliberately NOT the reasoning channel. The safeguard
// model's rationale goes to message.reasoning; mixing it into the verdict would corrupt
// the "safe"/"unsafe" parse (unlike completionText, which folds reasoning in for billing).
func contentText(rb []byte) string {
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(rb, &out) != nil || len(out.Choices) == 0 {
		return ""
	}
	return out.Choices[0].Message.Content
}

// groqFailMode applies the require posture to a Groq-backend error: fail-closed (503)
// when required, else fail-open (allow, logged).
func (m moderation) groqFailMode(what string, err error) modResult {
	if m.require {
		return modResult{status: http.StatusServiceUnavailable, msg: "content screening unavailable"}
	}
	log.Printf("MODERATION: groq screen error (%s: %v), failing open (require=false)", what, err)
	return modResult{}
}

// matchedCategoryList renders the matched policy categories (value true) from an
// OpenAI-shape category map as a sorted slice (for the block log + CSAM detection).
func matchedCategoryList(cats map[string]bool) []string {
	var hit []string
	for name, matched := range cats {
		if matched {
			hit = append(hit, name)
		}
	}
	sort.Strings(hit)
	return hit
}

// splitCategories parses Llama Guard's comma/space-separated category codes (e.g.
// "S4", "S1,S3", "S1, S3") into a trimmed, non-empty list.
func splitCategories(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// promptText pulls the user-visible text from an OpenAI chat-completions body for
// screening: the concatenated string content of the messages. Tolerates the array
// (multimodal) content form by collecting its text parts; the launch is text-only.
func promptText(body []byte) string {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
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
	return b.String()
}
