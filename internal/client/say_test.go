package client

// say_test.go table-tests client.Speak (the signed POST to /v1/audio/speech) and client.Voices
// (the GET /voices roster read). REAL httptest broker, no mocks: the request is really signed and
// the handler asserts the exact {model,input,response_format,speed} body + returns the broker's
// real status/header shape.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestSpeakSignsAndPostsBody: Speak signs the request (X-Roger-Pubkey/TS/Sig verify) and POSTs the
// OpenAI-shaped body {model, input, response_format:"wav"}; --speed rides only when >0.
func TestSpeakSignsAndPostsBody(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cases := []struct {
		name         string
		model, text  string
		speed        float64
		wantSpeed    bool
		wantSpeedVal float64
	}{
		{"no speed omits the field", "af_heart", "hello there", 0, false, 0},
		{"speed rides when set", "af_heart", "hi", 1.5, true, 1.5},
		{"namespaced id sent verbatim", "@bownux/operator", "hi", 0, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			var signed bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				ts, _ := strconv.ParseInt(r.Header.Get(protocol.HeaderTS), 10, 64)
				if _, ok := protocol.VerifyRequest(r.Header.Get(protocol.HeaderPubkey), r.Header.Get(protocol.HeaderSig), ts, r.Method, r.URL.Path, raw); ok {
					signed = true
				}
				w.Header().Set("X-RogerAI-Cost", "0.0008")
				w.Header().Set("Content-Type", "audio/wav")
				_, _ = w.Write([]byte("RIFFwav"))
			}))
			defer srv.Close()

			res, err := Speak(srv.URL, "u", tc.model, tc.text, tc.speed)
			if err != nil {
				t.Fatalf("Speak: %v", err)
			}
			if !signed {
				t.Fatal("Speak must sign the request (missing/invalid signature)")
			}
			if str(body["model"]) != tc.model {
				t.Errorf("model = %q, want %q", str(body["model"]), tc.model)
			}
			if str(body["input"]) != tc.text {
				t.Errorf("input = %q, want %q", str(body["input"]), tc.text)
			}
			if str(body["response_format"]) != "wav" {
				t.Errorf("response_format = %q, want wav", str(body["response_format"]))
			}
			sp, has := body["speed"].(float64)
			if has != tc.wantSpeed || (tc.wantSpeed && sp != tc.wantSpeedVal) {
				t.Errorf("speed present=%v val=%v, want present=%v val=%v", has, sp, tc.wantSpeed, tc.wantSpeedVal)
			}
			if string(res.Audio) != "RIFFwav" {
				t.Errorf("audio = %q, want the returned WAV bytes", res.Audio)
			}
			if res.Cost != 0.0008 {
				t.Errorf("cost = %v, want 0.0008 (from X-RogerAI-Cost)", res.Cost)
			}
		})
	}
}

// TestSpeakErrorMapping: the broker's real status codes map to clear, human errors — the uniform
// 503 no-station, the anon-paid 403 sign-in gate, the 402 funds error (with the topup hint), and a
// transport failure -> "broker unreachable".
func TestSpeakErrorMapping(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cases := []struct {
		name    string
		status  int
		msg     string
		wantSub string
	}{
		{"no station 503", http.StatusServiceUnavailable, "no station on air for af_heart", "no station on air for af_heart"},
		{"anon paid 403", http.StatusForbidden, "sign in to use this voice model", "sign in to use this voice model"},
		{"insufficient funds 402", http.StatusPaymentRequired, "insufficient balance - add funds", "insufficient balance"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write(brokerErrBody(tc.msg)) // the broker's REAL nested {"error":{"message":...}} shape
			}))
			defer srv.Close()
			_, err := Speak(srv.URL, "u", "af_heart", "hello", 0)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Speak error = %v, want containing %q", err, tc.wantSub)
			}
		})
	}
}

// TestSpeakBrokerUnreachable: a dead broker address yields a graceful "broker unreachable", not a
// panic or an opaque dial error.
func TestSpeakBrokerUnreachable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := Speak("http://127.0.0.1:1", "u", "af_heart", "hello", 0)
	if err == nil || !strings.Contains(err.Error(), "broker unreachable") {
		t.Fatalf("Speak(dead broker) = %v, want a graceful 'broker unreachable'", err)
	}
}

// TestSpeak402AppendsTopupHint: a 402 carries the actionable topup hint so the caller is never
// dead-ended (mirrors WithTopupHint used by chat).
func TestSpeak402AppendsTopupHint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write(brokerErrBody("insufficient balance - add funds"))
	}))
	defer srv.Close()
	_, err := Speak(srv.URL, "u", "af_heart", "hello", 0)
	if err == nil || !strings.Contains(err.Error(), "topup") {
		t.Fatalf("Speak 402 = %v, want the topup hint", err)
	}
}

// brokerErrBody builds the EXACT error JSON the broker emits (cmd/rogerai-broker/httputil.go's
// jsonErr → {"error":{"message":msg}}), so a stub can't diverge from the real contract and let a
// wrong parser pass. This is the shape Speak must read to surface the 503/403/402 gate messages.
func brokerErrBody(msg string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg}})
	return b
}

// TestVoicesFetchesRoster: Voices reads GET /voices into the roster shape, preserving the broker's
// cheapest-first order and the per-voice metadata.
func TestVoicesFetchesRoster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/voices" {
			t.Errorf("Voices hit %q, want /voices", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"voices":[
			{"id":"v-free","operator":"acme","name":"Kiosk","language":"en-GB","price_per_1k_chars":0,"free":true},
			{"id":"v-heart","namespaced_id":"@bownux/operator","operator":"bownux","name":"Operator","language":"en-US","price_per_1k_chars":0.02,"free":false}
		]}`))
	}))
	defer srv.Close()
	vs, err := Voices(srv.URL)
	if err != nil {
		t.Fatalf("Voices: %v", err)
	}
	if len(vs) != 2 {
		t.Fatalf("got %d voices, want 2", len(vs))
	}
	if vs[0].ID != "v-free" || !vs[0].Free || vs[0].Operator != "acme" {
		t.Errorf("voice[0] wrong: %+v", vs[0])
	}
	if vs[1].NamespacedID != "@bownux/operator" || vs[1].PricePer1kChars != 0.02 || vs[1].Name != "Operator" {
		t.Errorf("voice[1] wrong: %+v", vs[1])
	}
}

// TestVoicesEmptyRoster: an empty roster is a clean empty slice + nil error (the CLI then prints
// the friendly "no voices" line).
func TestVoicesEmptyRoster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"voices":[]}`))
	}))
	defer srv.Close()
	vs, err := Voices(srv.URL)
	if err != nil {
		t.Fatalf("Voices(empty): %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("got %d voices, want 0", len(vs))
	}
}

// TestVoicesBrokerUnreachable: a dead broker yields a graceful "broker unreachable"; a non-2xx
// status is also treated as unreachable (a real broker down / 500 must not look like an empty
// roster).
func TestVoicesBrokerUnreachable(t *testing.T) {
	_, err := Voices("http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "broker unreachable") {
		t.Fatalf("Voices(dead) = %v, want a graceful 'broker unreachable'", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := Voices(srv.URL); err == nil || !strings.Contains(err.Error(), "broker unreachable") {
		t.Fatalf("Voices(500) = %v, want 'broker unreachable'", err)
	}
}

// TestVoicesMalformedBody: a 200 with a body that isn't the roster JSON surfaces a clear read error
// (not a silent empty list).
func TestVoicesMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json at all"))
	}))
	defer srv.Close()
	if _, err := Voices(srv.URL); err == nil || !strings.Contains(err.Error(), "roster") {
		t.Fatalf("Voices(garbage) = %v, want a 'could not read the voice roster' error", err)
	}
}

// TestSpeakNonJSONError: a non-200 whose body isn't JSON falls back to a terse status summary (so a
// bare 5xx/HTML error page still yields a readable message, never an empty error).
func TestSpeakNonJSONError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502</html>"))
	}))
	defer srv.Close()
	_, err := Speak(srv.URL, "u", "af_heart", "hello", 0)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("Speak(non-JSON 502) = %v, want a terse status summary naming 502", err)
	}
}

// str pulls a JSON string field out of the decoded body (non-string -> "").
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
