package main

// stt_offfield_screen_test.go pins that an STT result whose transcript sits in a NON-standard
// field is screened, not laundered raw (audit finding #12). transcriptText returned ("",true)
// for a body that HAS a "text"/"segments" key but whose actual words live elsewhere, so the
// output moderation screen (gated on non-empty extracted text) was skipped and the raw body
// forwarded. Reuses the real STT output-screen harness (relayBroker + a live moderation URL
// stub + a stub STT node), no mocks.

import (
	"net/http"
	"strings"
	"testing"
)

func TestSTTOffFieldTranscriptIsScreenedNotLaundered(t *testing.T) {
	s := &sttOutState{}
	s.reset(t)
	s.modConfig = true
	s.flagCat = "S5" // the screen classifies the body's content as unsafe
	// A body that passes the key-presence shape guard (has "text") but whose transcript is in
	// an off-field key the extractor never reads.
	s.rawBody = `{"text":"","transcript":"the offending words the mic picked up"}`
	s.transcribe()

	if s.code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("an off-field transcript must be screened + withheld (451), got %d body=%q", s.code, s.body)
	}
	if strings.Contains(s.body, "offending") {
		t.Fatalf("off-field transcript leaked raw to the consumer: %q", s.body)
	}
}

// A genuinely silent result ({"text":""}) still needs no screen and serves 200 (no regression).
func TestSTTBareEmptyResultStillServes(t *testing.T) {
	s := &sttOutState{}
	s.reset(t)
	s.modConfig = true
	s.flagCat = "S5" // even with the screen armed, a bare empty result must not be screened
	s.rawBody = `{"text":""}`
	s.transcribe()
	if s.code != http.StatusOK {
		t.Fatalf("a bare empty-text result is legitimate silent audio and must serve 200, got %d body=%q", s.code, s.body)
	}
}
