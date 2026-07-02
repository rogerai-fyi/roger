package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rogerai-fyi/roger/internal/agent"
	"github.com/rogerai-fyi/roger/internal/glyphs"
	"github.com/rogerai-fyi/roger/internal/node"
)

// The SHARE view's terse per-cell text (e.g. "set voice…") is ambiguous: it reads
// truncated and never says HOW. The founder wants a full-width contextual DETAIL
// BANNER for the SELECTED row, above the footer, spelling out the row's full state
// + next action. shareRowDetail is the pure helper that builds that line; the
// banner renders it in shareView; marquee scrolls it iff it overflows.

// TestShareRowDetail_Text asserts the exact full-detail string per modality + state,
// grounded in real row/VoiceConfig/live data. The strings are plain (no ANSI) — the
// banner render applies the chrome — and lead with the shared fold-safe glyphs.
func TestShareRowDetail_Text(t *testing.T) {
	cases := []struct {
		name      string
		row       shareRow
		voice     node.VoiceConfig // set on the controller for the row's model when Voice/Name non-empty
		price     node.Pricing     // saved price for the row (FREE by default)
		onAir     bool
		wantExact string   // when set, the whole detail must equal this
		wantHas   []string // substrings that must appear (used for on-air rows)
		wantNot   []string // substrings that must NOT appear
	}{
		{
			name:      "tts with no name or voice prompts the VOICE BOOTH via p, then enter",
			row:       shareRow{model: "kokoro-82m", modality: "tts", ctx: 0},
			wantExact: "♪ kokoro-82m needs a name + voice — press p to set it in the VOICE BOOTH (voice · blend · speed · price), then enter to go on air",
		},
		{
			name:      "tts with a voice but NO name is still not ready (broker 400s a nameless offer)",
			row:       shareRow{model: "kokoro-82m", modality: "tts"},
			voice:     node.VoiceConfig{Voice: "af_sky", Speed: 1.0}, // no Name
			wantExact: "♪ kokoro-82m needs a name + voice — press p to set it in the VOICE BOOTH (voice · blend · speed · price), then enter to go on air",
		},
		{
			name:      "tts configured (name + voice) free off air reads dj-name · voice · FREE",
			row:       shareRow{model: "kokoro-82m", modality: "tts"},
			voice:     node.VoiceConfig{Name: "Night Owl", Voice: "af_heart", Speed: 1.0},
			wantExact: "♪ Night Owl · af_heart · FREE — enter to go on air · p to edit",
		},
		{
			name:      "tts configured priced off air shows the DJ name + $/1k ch",
			row:       shareRow{model: "kokoro-82m", modality: "tts"},
			voice:     node.VoiceConfig{Name: "Deep Cuts", Voice: "af_sky", Speed: 1.0},
			price:     node.Pricing{In: 30}, // $30/1M ch -> $0.03/1k ch
			wantExact: "♪ Deep Cuts · af_sky · $0.03/1k ch — enter to go on air · p to edit",
		},
		{
			name:      "stt off air explains it's metered per uploaded byte",
			row:       shareRow{model: "whisper-large-v3", modality: "stt"},
			wantExact: "▽ whisper-large-v3 transcriber — enter to go on air (metered per uploaded byte) · p to price",
		},
		{
			name:      "chat off air free with ctx, prompts price + schedule",
			row:       shareRow{model: "gpt-oss-20b", modality: "", ctx: 32768},
			wantExact: "gpt-oss-20b · 33k ctx — enter to go on air free · p to set a price + schedule",
		},
		{
			name:    "chat on air reports served · out tok · earn",
			row:     shareRow{model: "gpt-oss-20b", ctx: 32768},
			onAir:   true,
			wantHas: []string{"◉ on air", "served", "tok", "$"},
			wantNot: []string{"enter to go on air"},
		},
		{
			name:    "stt on air reports served · earn (no tok)",
			row:     shareRow{model: "whisper-large-v3", modality: "stt"},
			onAir:   true,
			wantHas: []string{"◉ on air", "served", "$"},
			wantNot: []string{"enter to go on air", "tok"},
		},
		{
			name:    "tts on air names the dj + voice + served + earn",
			row:     shareRow{model: "kokoro-82m", modality: "tts"},
			voice:   node.VoiceConfig{Name: "Night Owl", Voice: "af_heart", Speed: 1.0},
			onAir:   true,
			wantHas: []string{"◉ on air", "Night Owl", "af_heart", "served", "$"},
			wantNot: []string{"enter to go on air", "needs a voice"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// On-air rows need a live session; build one against an ok broker and adopt it.
			var mm model
			var live *agent.Session
			if tc.onAir {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
				}))
				defer srv.Close()
				mm = New(srv.URL, "tester")
				sess, err := agent.Start(agent.Config{Broker: srv.URL, Upstream: "http://127.0.0.1:0", NodeID: "n", Model: tc.row.model, Modality: tc.row.modality, Ctx: tc.row.ctx, Parallel: 1})
				if err != nil {
					t.Fatalf("agent.Start: %v", err)
				}
				defer sess.Stop()
				mm.ctrl.Adopt(tc.row.model, sess)
				live = sess
			} else {
				mm = New("http://broker.local", "tester")
			}
			if tc.voice.Voice != "" || tc.voice.Name != "" {
				mm.ctrl.SetVoiceConfig(tc.row.model, tc.voice)
			}
			if tc.price.In != 0 || tc.price.Out != 0 {
				mm.ctrl.SetPrices(map[string]node.Pricing{tc.row.model: tc.price})
			}
			mm.syncShareCache()

			got := mm.shareRowDetail(tc.row, live)
			if tc.wantExact != "" && got != tc.wantExact {
				t.Errorf("shareRowDetail mismatch:\n got: %q\nwant: %q", got, tc.wantExact)
			}
			for _, sub := range tc.wantHas {
				if !strings.Contains(got, sub) {
					t.Errorf("shareRowDetail %q missing %q:\n%s", tc.name, sub, got)
				}
			}
			for _, sub := range tc.wantNot {
				if strings.Contains(got, sub) {
					t.Errorf("shareRowDetail %q should not contain %q:\n%s", tc.name, sub, got)
				}
			}
		})
	}
}

// TestShareRowDetail_PrivateBand: a row on a hidden (private) band must note it's
// code-only, so the banner never implies it's on the open market.
func TestShareRowDetail_PrivateBand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()
	mm := New(srv.URL, "tester")
	sess, err := agent.Start(agent.Config{Broker: srv.URL, Upstream: "http://127.0.0.1:0", NodeID: "n", Model: "gpt-oss-20b", Ctx: 8192, Parallel: 1, Private: true})
	if err != nil {
		t.Fatalf("agent.Start: %v", err)
	}
	defer sess.Stop()
	mm.ctrl.Adopt("gpt-oss-20b", sess)
	mm.syncShareCache()
	mm.sharePrivate["gpt-oss-20b"] = true // as the controller reports a hidden band

	got := mm.shareRowDetail(shareRow{model: "gpt-oss-20b", ctx: 8192}, sess)
	low := strings.ToLower(got)
	if !strings.Contains(low, "hidden") && !strings.Contains(low, "private") && !strings.Contains(low, "code") {
		t.Errorf("private-band detail should note it's hidden / code-only:\n%s", got)
	}
}

// TestShareRowDetail_Fold: the detail must be ASCII-fold-safe (♪/▽/◉ fold to their
// ASCII stand-ins) — no raw Unicode glyph leaks when glyphs fold.
func TestShareRowDetail_Fold(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.syncShareCache()
	got := mm.shareRowDetail(shareRow{model: "kokoro-82m", modality: "tts"}, nil)
	if got != glyphs.Fold(got) {
		t.Errorf("detail is not fold-idempotent (a raw glyph leaked): %q vs folded %q", got, glyphs.Fold(got))
	}
}

// TestShareBanner_SelectedRowOnly: the banner renders the SELECTED row's detail
// above the footer, only when there are rows AND a row is selected. It must not
// appear when the table is empty, and must reflect the CURSOR (not row 0).
func TestShareBanner_SelectedRowOnly(t *testing.T) {
	// Empty table: no banner (and no crash).
	empty := New("http://broker.local", "tester")
	empty.width, empty.height = 100, 30
	empty.mode = modeShare
	empty.syncShareCache()
	if strings.Contains(stripANSI(empty.shareView(100)), "needs a name") {
		t.Errorf("empty share view must not render a row-detail banner:\n%s", stripANSI(empty.shareView(100)))
	}

	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.mode = modeShare
	mm.setShareRows([]shareRow{
		{model: "gpt-oss-20b", modality: "", ctx: 32768},
		{model: "kokoro-82m", modality: "tts"},
	})
	mm.syncShareCache()

	// Cursor on the chat row (0): banner shows the chat detail, not the tts prompt.
	mm.shareCursor = 0
	v0 := stripANSI(mm.shareView(100))
	if !strings.Contains(v0, "gpt-oss-20b · 33k ctx") {
		t.Errorf("banner should show the SELECTED (chat) row's detail:\n%s", v0)
	}
	if strings.Contains(v0, "needs a name") {
		t.Errorf("banner should reflect the cursor row, not the tts row:\n%s", v0)
	}

	// Move the cursor to the tts row (1): the banner follows it and shows the set-name+voice prompt.
	mm.shareCursor = 1
	v1 := stripANSI(mm.shareView(100))
	if !strings.Contains(v1, "needs a name + voice — press p to set it in the VOICE BOOTH") {
		t.Errorf("banner should follow the cursor to the tts set-voice prompt:\n%s", v1)
	}
}

// TestShareBanner_NoColorSafe: under NO_COLOR the BANNER LINE must not leak ANSI and must
// never exceed the terminal width (dense + wide). The check is scoped to the banner line
// (the one carrying the row detail) so it isolates the banner from the pre-existing footer
// affordance layout. (NO_COLOR strips color only — Unicode glyphs are kept; ASCII folding
// is a separate axis, covered by TestShareBanner_ASCIIFold.)
func TestShareBanner_NoColorSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, w := range []int{40, 60, 88, 120} {
		mm := New("http://broker.local", "tester")
		mm.width, mm.height = w, 30
		mm.mode = modeShare
		mm.setShareRows([]shareRow{{model: "kokoro-82m", modality: "tts"}})
		mm.syncShareCache()
		mm.shareCursor = 0
		v := mm.shareView(w)
		banner := bannerLine(v)
		if banner == "" {
			t.Fatalf("width %d: no banner line rendered:\n%s", w, stripANSI(v))
		}
		if strings.Contains(banner, "\x1b[") {
			t.Errorf("width %d: NO_COLOR banner leaked ANSI:\n%q", w, banner)
		}
		if vis := utf8.RuneCountInString(stripANSI(banner)); vis > w {
			t.Errorf("width %d: banner overflows (%d cols): %q", w, vis, stripANSI(banner))
		}
	}
}

// TestShareBanner_ASCIIFold: under a forced-ASCII console (ROGERAI_ASCII=1) the banner
// must fold its glyphs to the plain house stand-ins (♪→>, ·→.) — no raw Unicode survives.
func TestShareBanner_ASCIIFold(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 120, 30
	mm.mode = modeShare
	mm.setShareRows([]shareRow{{model: "kokoro-82m", modality: "tts"}})
	mm.syncShareCache()
	mm.shareCursor = 0
	banner := stripANSI(bannerLine(mm.shareView(120)))
	if banner == "" {
		t.Fatal("no banner line rendered under ASCII")
	}
	if strings.ContainsAny(banner, "♪·—▽◉") {
		t.Errorf("ASCII banner leaked a raw Unicode glyph (should fold): %q", banner)
	}
	if !strings.Contains(banner, ">") { // ♪ folds to >
		t.Errorf("ASCII banner should carry the folded > (from ♪): %q", banner)
	}
}

// bannerLine returns the SHARE detail-banner line from a rendered view: the ▌-barred
// line carrying the row detail. Both the SHARE header and the banner use the ▌ bar, but
// the header also says "SHARE", so the banner is the ▌ line WITHOUT it (robust while the
// detail marquee-scrolls). Returns "" when no banner is present.
func bannerLine(view string) string {
	for _, line := range strings.Split(view, "\n") {
		plain := stripANSI(line)
		if strings.Contains(plain, "▌") && !strings.Contains(plain, "SHARE") {
			return line
		}
	}
	return ""
}

// TestMarquee_NoOpWhenFits: a string that fits the width is returned unchanged for
// every frame (no scroll, no gap artifacts) — the static-by-default contract.
func TestMarquee_NoOpWhenFits(t *testing.T) {
	s := "on air"
	for f := 0; f < 20; f++ {
		if got := marquee(s, 40, f); got != s {
			t.Errorf("marquee should be a no-op when it fits (frame %d): got %q", f, got)
		}
	}
	// Exactly-fits is still a no-op (boundary).
	if got := marquee("abcd", 4, 3); got != "abcd" {
		t.Errorf("marquee at exact width must be a no-op: got %q", got)
	}
}

// TestMarquee_ScrollsBoundedAndAdvances: when the text overflows, marquee returns a
// window of exactly `width` visible cells that ADVANCES with the frame and eventually
// reveals every part of the string (so the whole detail is readable over time).
func TestMarquee_ScrollsBoundedAndAdvances(t *testing.T) {
	s := "the quick brown fox jumps over the lazy dog" // 43 runes
	const w = 10
	seen := map[string]bool{}
	windows := make([]string, 0, 200)
	for f := 0; f < 200; f++ {
		win := marquee(s, w, f)
		if n := utf8.RuneCountInString(win); n != w {
			t.Fatalf("frame %d: marquee window must be exactly %d cols, got %d: %q", f, w, n, win)
		}
		seen[win] = true
		windows = append(windows, win)
	}
	// It must actually move (more than a couple distinct windows across frames).
	if len(seen) < 3 {
		t.Errorf("marquee should scroll (advance with the frame), saw %d distinct windows", len(seen))
	}
	// Every token of the source must surface in SOME window over a full cycle (the whole
	// detail becomes readable, not just a fixed prefix).
	joined := strings.Join(windows, "\n")
	for _, tok := range []string{"the", "quick", "brown", "fox", "lazy", "dog"} {
		if !strings.Contains(joined, tok) {
			t.Errorf("marquee never revealed %q over a full cycle:\n%s", tok, joined)
		}
	}
}

// TestMarquee_DrivenByFrame_InBanner: a very long detail on a narrow banner must
// change between two different animation frames (the marquee is wired to the model's
// existing frame counter, not a fresh ticker), while staying width-bounded.
func TestMarquee_DrivenByFrame_InBanner(t *testing.T) {
	newAt := func(frame int) string {
		mm := New("http://broker.local", "tester")
		mm.width, mm.height = 44, 30 // dense: the long "needs a voice" detail overflows
		mm.mode = modeShare
		mm.frame = frame
		mm.setShareRows([]shareRow{{model: "a-very-long-voice-model-name-that-overflows", modality: "tts"}})
		mm.syncShareCache()
		mm.shareCursor = 0
		return stripANSI(mm.shareView(44))
	}
	a, b := bannerLine(newAt(0)), bannerLine(newAt(7))
	if a == "" || b == "" {
		t.Fatalf("no banner line rendered for the long detail:\nframe0:\n%s\nframe7:\n%s", newAt(0), newAt(7))
	}
	if a == b {
		t.Errorf("a long banner detail should scroll between frames 0 and 7 (marquee off m.frame):\nframe0:\n%q\nframe7:\n%q", a, b)
	}
	// And never wider than the terminal, at either frame.
	for _, banner := range []string{a, b} {
		if vis := utf8.RuneCountInString(stripANSI(banner)); vis > 44 {
			t.Errorf("marquee banner overflowed 44 cols (%d): %q", vis, stripANSI(banner))
		}
	}
}
