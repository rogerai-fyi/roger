package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// --- Fix 1: BROKER_PRIVATE_KEY fail-closed guard --------------------------

// TestResolveBrokerKeyRequireRefusesBoot: with requireKey=true and no/invalid key,
// resolveBrokerKey returns an error (the caller refuses to boot); off by default it
// falls back to an ephemeral key.
func TestResolveBrokerKeyRequireRefusesBoot(t *testing.T) {
	// require + unset -> error
	if _, err := resolveBrokerKey("", true); err == nil {
		t.Error("require + unset BROKER_PRIVATE_KEY should error (refuse to boot)")
	}
	// require + invalid hex -> error
	if _, err := resolveBrokerKey("not-hex", true); err == nil {
		t.Error("require + invalid BROKER_PRIVATE_KEY should error")
	}
	// require + wrong-length seed -> error
	if _, err := resolveBrokerKey(hex.EncodeToString([]byte("short")), true); err == nil {
		t.Error("require + wrong-length seed should error")
	}
	// NOT required + unset -> ephemeral key, no error (dev)
	if k, err := resolveBrokerKey("", false); err != nil || k == nil {
		t.Errorf("not-required + unset should yield an ephemeral key, got key=%v err=%v", k != nil, err)
	}
	// valid seed -> loaded, no error, even with require on
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	k, err := resolveBrokerKey(hex.EncodeToString(seed), true)
	if err != nil || k == nil {
		t.Fatalf("valid seed should load, got err=%v", err)
	}
	if !k.Equal(ed25519.NewKeyFromSeed(seed)) {
		t.Error("loaded key does not match the seed")
	}
}

// TestRequireBrokerKeyEnv: the env parse mirrors requireLive (1/true/yes/on).
func TestRequireBrokerKeyEnv(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv("ROGERAI_REQUIRE_BROKER_KEY", v)
		if !requireBrokerKey() {
			t.Errorf("ROGERAI_REQUIRE_BROKER_KEY=%q should be true", v)
		}
	}
	for _, v := range []string{"", "0", "no", "off"} {
		t.Setenv("ROGERAI_REQUIRE_BROKER_KEY", v)
		if requireBrokerKey() {
			t.Errorf("ROGERAI_REQUIRE_BROKER_KEY=%q should be false", v)
		}
	}
}

// --- Fix 2: moderation fail-open always logs ------------------------------

// TestModerationURLNon200FailsOpenLogs: a non-200 from the URL backend with require=0
// fails open AND logs (we assert the fail-open by an allow, and that policy is intact).
func TestModerationURLNon200FailsOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	// not required -> fail open (allow)
	if res := (moderation{provider: "url", url: srv.URL, client: srv.Client()}).screen("x"); !res.allow() {
		t.Errorf("non-200 + not-required should fail open, got status %d", res.status)
	}
	// required -> fail closed (503)
	if res := (moderation{provider: "url", url: srv.URL, require: true, client: srv.Client()}).screen("x"); res.status != http.StatusServiceUnavailable {
		t.Errorf("non-200 + required should 503, got %d", res.status)
	}
}

// --- Fix 3: CSAM preserve vs non-CSAM reject ------------------------------

// TestModerationCSAMDetection: an S4 (or sexual/minors) hit sets csam=true with the
// category; a non-CSAM unsafe hit (S1) sets csam=false.
func TestModerationCSAMDetection(t *testing.T) {
	m := moderation{provider: "groq", client: nil, groqKey: "k", groqModel: "x",
		csamCats: loadCSAMCategories("")} // default S4 + sexual/minors
	// S4 -> CSAM
	if csam, cat := m.isCSAM([]string{"S4"}); !csam || strings.ToLower(cat) != "s4" {
		t.Errorf("S4 should be CSAM, got csam=%v cat=%q", csam, cat)
	}
	// sexual/minors -> CSAM
	if csam, _ := m.isCSAM([]string{"sexual/minors"}); !csam {
		t.Error("sexual/minors should be CSAM")
	}
	// S1 -> not CSAM
	if csam, _ := m.isCSAM([]string{"S1", "S3"}); csam {
		t.Error("S1/S3 should NOT be CSAM")
	}
	// configurable set: S2 only
	m2 := moderation{csamCats: loadCSAMCategories("S2")}
	if csam, _ := m2.isCSAM([]string{"S2"}); !csam {
		t.Error("configured S2 should be CSAM")
	}
	if csam, _ := m2.isCSAM([]string{"S4"}); csam {
		t.Error("with S2 configured, S4 should NOT be CSAM")
	}
}

// TestScreenGroqCSAMResult: a groq "unsafe\nS4" verdict yields a 451 modResult with
// csam=true; "unsafe\nS1" yields 451 with csam=false (reject-only, no preservation).
func TestScreenGroqCSAMResult(t *testing.T) {
	for _, c := range []struct {
		verdict  string
		wantCSAM bool
	}{
		{"unsafe\nS4", true},
		{"unsafe\nS1", false},
		{"unsafe\nS1,S4", true},
	} {
		srv := groqVerdictServer(t, c.verdict, nil)
		m := groqMod(srv, false)
		m.csamCats = loadCSAMCategories("")
		res := m.screen("bad")
		srv.Close()
		if res.status != http.StatusUnavailableForLegalReasons {
			t.Errorf("%q: want 451, got %d", c.verdict, res.status)
		}
		if res.csam != c.wantCSAM {
			t.Errorf("%q: csam=%v want %v", c.verdict, res.csam, c.wantCSAM)
		}
	}
}

// testBrokerWithDB builds a minimal broker with a stable key + the given store, enough
// to exercise the safety paths (preserveCSAM, report, ban filter in pick).
func testBrokerWithDB(db store.Store) *broker {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return &broker{
		db:           db,
		priv:         ed25519.NewKeyFromSeed(seed),
		nodes:        map[string]protocol.NodeRegistration{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
		banned:       map[string]bool{},
		rl:           loadRateLimiter(),
	}
}

// TestPreserveCSAMRoundTrip: preserveCSAM stores an encrypted incident + queues a
// report; the stored content is NOT the plaintext (encrypted-at-rest).
func TestPreserveCSAMRoundTrip(t *testing.T) {
	db := store.NewMem()
	b := testBrokerWithDB(db)
	plaintext := []byte("an offending prompt")
	b.preserveCSAM("u_abc", "1.2.3.4", "S4", plaintext)

	pending, err := db.PendingCSAMReports(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 queued incident, got %d", len(pending))
	}
	inc := pending[0]
	if inc.ReportState != store.CSAMQueued {
		t.Errorf("incident should be queued, got %q", inc.ReportState)
	}
	if inc.Category != "S4" || inc.IP != "1.2.3.4" || inc.Pseudonym != "u_abc" {
		t.Errorf("incident metadata wrong: %+v", inc)
	}
	if len(inc.Content) == 0 {
		t.Fatal("content should be preserved (non-empty)")
	}
	if string(inc.Content) == string(plaintext) {
		t.Error("content must be ENCRYPTED at rest, not stored as plaintext")
	}
}

// TestRelayCSAMPreservesNonCSAMDoesNot: drive the relay's moderation gate with a stub
// URL backend. An S4 hit preserves an incident; a non-CSAM (S1) hit rejects WITHOUT
// preserving anything.
func TestRelayCSAMPreservesNonCSAMDoesNot(t *testing.T) {
	cases := []struct {
		name         string
		categories   string // OpenAI-shape category key
		wantPreserve bool
	}{
		{"csam s4", "S4", true},
		{"csam sexual/minors", "sexual/minors", true},
		{"non-csam s1", "S1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			modSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"results":[{"flagged":true,"categories":{"` + c.categories + `":true}}]}`))
			}))
			defer modSrv.Close()
			db := store.NewMem()
			b := testBrokerWithDB(db)
			b.mod = moderation{provider: "url", url: modSrv.URL, client: modSrv.Client(), csamCats: loadCSAMCategories("")}

			// Sign a request so identityOf passes the spend gate up to the screen.
			_, priv, _ := ed25519.GenerateKey(nil)
			body := []byte(`{"model":"m","messages":[{"role":"user","content":"x"}]}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
			signReq(req, priv, body)
			rec := httptest.NewRecorder()
			b.relay(rec, req)

			if rec.Code != http.StatusUnavailableForLegalReasons {
				t.Fatalf("want 451, got %d", rec.Code)
			}
			pending, _ := db.PendingCSAMReports(0)
			if c.wantPreserve && len(pending) != 1 {
				t.Errorf("expected 1 preserved CSAM incident, got %d", len(pending))
			}
			if !c.wantPreserve && len(pending) != 0 {
				t.Errorf("non-CSAM hit must NOT preserve, got %d incidents", len(pending))
			}
		})
	}
}

// --- Fix 4: /report endpoint + ban flow -----------------------------------

func postReport(b *broker, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/report", strings.NewReader(body))
	req.RemoteAddr = "9.9.9.9:1234"
	rec := httptest.NewRecorder()
	b.report(rec, req)
	return rec
}

// TestReportPersistsAndContract: POST /report persists a report and returns the exact
// {"received":true} contract.
func TestReportPersistsAndContract(t *testing.T) {
	db := store.NewMem()
	b := testBrokerWithDB(db)
	rec := postReport(b, `{"category":"abuse","node_id":"node1","request_id":"r1","detail":"bad node"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["received"] != true {
		t.Errorf("want {received:true}, got %v", resp)
	}
	if n, _ := db.ReportCountByNode("node1"); n != 1 {
		t.Errorf("want 1 report for node1, got %d", n)
	}
}

// TestReportRateLimited: bursts past the limiter return 429.
func TestReportRateLimited(t *testing.T) {
	db := store.NewMem()
	b := testBrokerWithDB(db)
	b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 2}
	got429 := false
	for i := 0; i < 6; i++ {
		rec := postReport(b, `{"category":"spam","node_id":"n"}`)
		if rec.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Error("expected a 429 from /report after the burst")
	}
}

// TestReportThresholdEjectsNode: once a node crosses the report threshold it is banned
// and removed from pick/discover/market.
func TestReportThresholdEjectsNode(t *testing.T) {
	db := store.NewMem()
	b := testBrokerWithDB(db)
	b.reportEjectAt = 3
	// Register a serving node so pick would otherwise return it.
	now := time.Now()
	b.nodes["badnode"] = protocol.NodeRegistration{NodeID: "badnode", Offers: []protocol.ModelOffer{{Model: "m", PriceOut: 0.1}}}
	b.lastSeen["badnode"] = now

	// pick returns it before any reports.
	b.mu.Lock()
	_, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil)
	b.mu.Unlock()
	if !ok {
		t.Fatal("node should be pickable before reports")
	}

	// Three reports -> crosses threshold -> banned.
	for i := 0; i < 3; i++ {
		if rec := postReport(b, `{"category":"abuse","node_id":"badnode"}`); rec.Code != http.StatusOK {
			t.Fatalf("report %d: want 200, got %d", i, rec.Code)
		}
	}
	if !b.isBanned("badnode") {
		t.Fatal("node should be banned after crossing the threshold")
	}
	// pick now skips it (ejected from routing).
	b.mu.Lock()
	_, _, ok = b.pick("m", false, 0, 0, 0, "", nil, nil)
	b.mu.Unlock()
	if ok {
		t.Error("banned node must be removed from pick")
	}
	// /discover and /market must not surface it.
	for _, path := range []string{"/discover", "/market"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		if path == "/discover" {
			b.discover(rec, req)
		} else {
			b.market(rec, req)
		}
		if strings.Contains(rec.Body.String(), "badnode") {
			t.Errorf("%s should not surface a banned node, body=%s", path, rec.Body.String())
		}
	}
}

// TestUnknownReportCategoryNormalized: an unknown category is stored as "other" (the
// surface never hard-rejects a well-meant report).
func TestUnknownReportCategoryNormalized(t *testing.T) {
	db := store.NewMem()
	b := testBrokerWithDB(db)
	if rec := postReport(b, `{"category":"wat","node_id":"n"}`); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	reps, _ := db.ReportsByNode("n", 0)
	if len(reps) != 1 || reps[0].Category != "other" {
		t.Errorf("unknown category should normalize to other, got %+v", reps)
	}
}
