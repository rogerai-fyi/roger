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
}

// defaultModerationModel is a current Groq Llama Guard model id (verified against
// Groq's catalog 2026-06-24). Override with MODERATION_MODEL to fix the name without
// a redeploy if Groq retires/renames it.
const defaultModerationModel = "meta-llama/llama-guard-4-12b"

func loadModeration() moderation {
	m := moderation{
		provider: strings.ToLower(strings.TrimSpace(os.Getenv("MODERATION_PROVIDER"))),
		url:      os.Getenv("MODERATION_URL"),
		require:  os.Getenv("ROGERAI_REQUIRE_MODERATION") == "1",
		client:   &http.Client{Timeout: 6 * time.Second},

		groqKey:   os.Getenv("GROQ_API_KEY"),
		groqURL:   "https://api.groq.com/openai/v1/chat/completions",
		groqModel: defaultModerationModel,
	}
	if v := strings.TrimSpace(os.Getenv("MODERATION_MODEL")); v != "" {
		m.groqModel = v
	}
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
		log.Printf("MODERATION: provider=groq but GROQ_API_KEY is unset - requests fail %s.", failMode(m.require))
	case m.provider == "url" && m.url == "":
		log.Printf("MODERATION: provider=url but MODERATION_URL is unset - requests fail %s.", failMode(m.require))
	case m.provider == "groq":
		log.Printf("MODERATION: enabled via Groq Llama Guard (%s) (require=%v)", m.groqModel, m.require)
	default:
		log.Printf("MODERATION: enabled via %s (require=%v)", m.url, m.require)
	}
	return m
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
func (m moderation) screen(text string) (status int, msg string) {
	// Backend not configured (or configured but missing its credential).
	switch {
	case m.provider == "",
		m.provider == "url" && m.url == "",
		m.provider == "groq" && m.groqKey == "":
		if m.require {
			return http.StatusServiceUnavailable, "content screening required but not configured"
		}
		return 0, ""
	}
	// Empty input has nothing to screen - short-circuit ALLOW and skip the network
	// round-trip (this is on the hot dispatch path). A no-text request is handled by
	// the dispatch logic, not by the content policy.
	if strings.TrimSpace(text) == "" {
		return 0, ""
	}
	if m.provider == "groq" {
		return m.screenGroq(text)
	}
	body, _ := json.Marshal(map[string]string{"input": text})
	resp, err := m.client.Post(m.url, "application/json", bytes.NewReader(body))
	if err != nil {
		if m.require {
			return http.StatusServiceUnavailable, "content screening unavailable"
		}
		log.Printf("MODERATION: screen unreachable (%v), failing open (require=false)", err)
		return 0, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if m.require {
			return http.StatusServiceUnavailable, "content screening error"
		}
		return 0, ""
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
		if hit := flaggedCategories(cats); hit != "" {
			log.Printf("MODERATION: blocked (categories: %s)", hit)
		}
		return http.StatusUnavailableForLegalReasons, "request blocked by the content policy"
	}
	return 0, ""
}

// screenGroq screens text with a Groq-hosted Llama Guard model over Groq's
// OpenAI-compatible chat/completions endpoint (the same shape concierge.go uses).
// Llama Guard classifies the message and replies with "safe" (ALLOW) or "unsafe"
// followed by the violated category codes on the next line (e.g. "unsafe\nS1,S3"),
// which we capture for the block log. Honors the same fail-open/closed posture as the
// URL backend: on a transport/non-200/parse error, fail-closed (503) when required,
// else fail-open (served). Caller has already short-circuited empty input.
func (m moderation) screenGroq(text string) (status int, msg string) {
	payload := map[string]any{
		"model": m.groqModel,
		"messages": []map[string]string{
			{"role": "user", "content": text},
		},
		"temperature": 0,
		"max_tokens":  100,
		"stream":      false,
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
	verdict := strings.TrimSpace(completionText(rb))
	if verdict == "" {
		// No verdict text is an error condition, not an implicit allow.
		return m.groqFailMode("empty verdict", nil)
	}
	// Llama Guard answers "safe" or "unsafe\n<categories>". Anything that does not
	// clearly begin with "safe" is treated as flagged (fail toward blocking on this
	// safety surface). The category codes (if any) are captured only for the log.
	first := verdict
	if i := strings.IndexAny(first, "\r\n"); i >= 0 {
		first = first[:i]
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(first)), "safe") {
		return 0, ""
	}
	cats := strings.TrimSpace(strings.TrimPrefix(verdict, first))
	cats = strings.ReplaceAll(strings.ReplaceAll(cats, "\r", " "), "\n", " ")
	cats = strings.TrimSpace(cats)
	if cats != "" {
		log.Printf("MODERATION: blocked by Llama Guard (categories: %s)", cats)
	} else {
		log.Printf("MODERATION: blocked by Llama Guard")
	}
	return http.StatusUnavailableForLegalReasons, "request blocked by the content policy"
}

// groqFailMode applies the require posture to a Groq-backend error: fail-closed (503)
// when required, else fail-open (allow, logged).
func (m moderation) groqFailMode(what string, err error) (int, string) {
	if m.require {
		return http.StatusServiceUnavailable, "content screening unavailable"
	}
	log.Printf("MODERATION: groq screen error (%s: %v), failing open (require=false)", what, err)
	return 0, ""
}

// flaggedCategories renders the matched policy categories (value true) as a sorted,
// comma-separated string for the block log. Returns "" when none are reported.
func flaggedCategories(cats map[string]bool) string {
	var hit []string
	for name, matched := range cats {
		if matched {
			hit = append(hit, name)
		}
	}
	sort.Strings(hit)
	return strings.Join(hit, ", ")
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
