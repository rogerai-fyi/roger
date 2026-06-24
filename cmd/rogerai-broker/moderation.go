package main

import (
	"bytes"
	"encoding/json"
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
// It is a pluggable hook: point MODERATION_URL at an OpenAI-moderation-compatible
// endpoint (the OpenAI Moderation API, or a small adapter in front of a self-hosted
// Llama Guard) and the broker screens every prompt against it. The broker itself
// runs no model - it just calls the endpoint - so this hook adds no model
// dependency until you configure one.
//
// Posture:
//   - MODERATION_URL unset, require=false -> DISABLED (dev only): pass through, with
//     a loud startup warning. NOT safe for real public traffic.
//   - ROGERAI_REQUIRE_MODERATION=1 -> fail CLOSED: if the screen is unset or
//     unreachable, requests are rejected (503) rather than served unscreened.
//
// Launch to real public traffic MUST run with MODERATION_URL set and require=true.
type moderation struct {
	url     string
	require bool
	client  *http.Client
}

func loadModeration() moderation {
	m := moderation{
		url:     os.Getenv("MODERATION_URL"),
		require: os.Getenv("ROGERAI_REQUIRE_MODERATION") == "1",
		client:  &http.Client{Timeout: 6 * time.Second},
	}
	switch {
	case m.url == "" && m.require:
		log.Printf("MODERATION: REQUIRED but MODERATION_URL is unset - all requests will be blocked (fail-closed). Set MODERATION_URL.")
	case m.url == "":
		log.Printf("MODERATION: DISABLED (no MODERATION_URL). NOT SAFE FOR PUBLIC TRAFFIC - set MODERATION_URL + ROGERAI_REQUIRE_MODERATION=1 before launch.")
	default:
		log.Printf("MODERATION: enabled via %s (require=%v)", m.url, m.require)
	}
	return m
}

// screen checks the prompt text before dispatch. It returns status=0 to ALLOW, or
// an HTTP status + message to REJECT with: 451 when the content is flagged by the
// policy, 503 when the screen is required but unavailable (fail-closed). When not
// required, a configuration or transport problem fails open (logged) so a screen
// outage does not take the marketplace down in non-launch posture.
func (m moderation) screen(text string) (status int, msg string) {
	if m.url == "" {
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
