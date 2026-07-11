package main

// capsule_rendezvous_bdd_test.go makes features/capsule/broker_rendezvous.feature EXECUTABLE
// under godog: the content-blind, one-time, multi-instance broker rendezvous over a real
// shared store (miniredis, or ROGERAI_TEST_REDIS_URL) and REAL seal/open crypto. No mocks.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/capsule"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

func bddErrf(format string, a ...any) error { return fmt.Errorf(format, a...) }

type rvBDD struct {
	t         *testing.T
	url       string
	a, b      *broker // two instances sharing one store
	local     *broker // per-instance-map broker (no shared) for the expiry path
	priv      ed25519.PrivateKey
	lookup    string
	blob      []byte
	code      string
	plaintext []byte
	res       *httptest.ResponseRecorder
	resB      *httptest.ResponseRecorder
	unknown   *httptest.ResponseRecorder
	empty     *httptest.ResponseRecorder
	mintCode  int
}

func (s *rvBDD) shared() *broker {
	if s.a == nil {
		s.a = sharedBroker(s.t, s.url)
		s.b = sharedBroker(s.t, s.url)
	}
	return s.a
}

// --- Given ---------------------------------------------------------------

func (s *rvBDD) signedMintUnderLookup() error {
	s.shared()
	s.lookup = "lk-" + protocol.BandCodeHash("rendezvous-fixture")
	s.blob = []byte("opaque-ciphertext-the-broker-cannot-read")
	if w := capsuleMintReq(s.t, s.a, s.priv, s.lookup, s.blob, true); w.Code != http.StatusOK {
		return bddErrf("mint status %d", w.Code)
	}
	return nil
}

func (s *rvBDD) sealedAndMinted() error {
	s.shared()
	s.code, _, _ = protocol.NewRCLinkCode()
	s.plaintext = []byte(`{"capsule":"roger.context.v1","messages":[{"content":"SENSITIVE-SECRET-BODY"}]}`)
	sealed, err := capsule.SealForCode(s.plaintext, s.code)
	if err != nil {
		return err
	}
	s.blob = sealed
	s.lookup = capsule.TransportLookup(s.code)
	// capture the log to prove nothing sensitive is written during the mint.
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	w := capsuleMintReq(s.t, s.a, s.priv, s.lookup, sealed, true)
	log.SetOutput(old)
	if w.Code != http.StatusOK {
		return bddErrf("mint status %d", w.Code)
	}
	if bytes.Contains(buf.Bytes(), []byte(s.code)) || bytes.Contains(buf.Bytes(), s.plaintext) {
		return bddErrf("a log line leaked the code or plaintext")
	}
	return nil
}

func (s *rvBDD) expiredMint() error {
	s.local = capsuleBroker(s.t) // per-instance map, no shared
	s.lookup = "stale"
	s.blob = []byte("old-ciphertext")
	s.local.capsules.put(s.lookup, s.blob, time.Now().Add(-2*capsuleTTL))
	return nil
}

func (s *rvBDD) oversizeMint() error {
	s.shared()
	s.lookup = "toobig"
	big := bytes.Repeat([]byte{0x41}, capsuleMaxBlob+1)
	s.mintCode = capsuleMintReq(s.t, s.a, s.priv, s.lookup, big, true).Code
	return nil
}

func (s *rvBDD) unsignedMint() error {
	s.shared()
	s.lookup = "nosig"
	s.mintCode = capsuleMintReq(s.t, s.a, s.priv, s.lookup, []byte("x"), false).Code
	return nil
}

func (s *rvBDD) twoInstances() error { s.shared(); return nil }

func (s *rvBDD) mintOnAUnderLookup() error {
	s.lookup = "lk-A-to-B"
	s.blob = []byte("A-minted-ciphertext")
	if w := capsuleMintReq(s.t, s.a, s.priv, s.lookup, s.blob, true); w.Code != http.StatusOK {
		return bddErrf("mint on A status %d", w.Code)
	}
	return nil
}

func (s *rvBDD) minted() error { return s.mintOnAUnderLookup() }

// --- When ----------------------------------------------------------------

func (s *rvBDD) resolveLookup() error {
	s.res = capsuleResolveReq(s.t, s.a, s.lookup)
	return nil
}

func (s *rvBDD) resolveExpired() error {
	s.res = capsuleResolveReq(s.t, s.local, s.lookup)
	return nil
}

func (s *rvBDD) resolveUnknown() error {
	s.unknown = capsuleResolveReq(s.t, s.shared(), "does-not-exist")
	return nil
}

func (s *rvBDD) resolveEmpty() error {
	s.empty = capsuleResolveReq(s.t, s.shared(), "")
	return nil
}

func (s *rvBDD) resolveOnB() error {
	s.resB = capsuleResolveReq(s.t, s.b, s.lookup)
	return nil
}

func (s *rvBDD) resolveConcurrently() error {
	var wg sync.WaitGroup
	out := make([]*httptest.ResponseRecorder, 2)
	brs := []*broker{s.a, s.b}
	wg.Add(2)
	for i := range brs {
		i := i
		go func() { defer wg.Done(); out[i] = capsuleResolveReq(s.t, brs[i], s.lookup) }()
	}
	wg.Wait()
	s.res, s.resB = out[0], out[1]
	return nil
}

// --- Then ----------------------------------------------------------------

func (s *rvBDD) resolveReturnsExact() error {
	if s.res.Code != http.StatusOK {
		return bddErrf("resolve status %d, want 200", s.res.Code)
	}
	got := decodeBlob(s.res)
	if !bytes.Equal(got, s.blob) {
		return bddErrf("resolved blob %q, want %q", got, s.blob)
	}
	return nil
}

func (s *rvBDD) secondResolveUniform404() error {
	if w := capsuleResolveReq(s.t, s.a, s.lookup); w.Code != http.StatusNotFound {
		return bddErrf("second resolve status %d, want 404 (one-time)", w.Code)
	}
	return nil
}

func (s *rvBDD) holdsOnlyLookupAndCiphertext() error {
	vs := s.a.shared.(*valkeyStore)
	raw, err := vs.rdb.Get(context.Background(), capsuleKeyPrefix+s.lookup).Bytes()
	if err != nil {
		return bddErrf("read stored blob: %v", err)
	}
	if !bytes.Equal(raw, s.blob) {
		return bddErrf("stored value is not exactly the ciphertext")
	}
	if bytes.Contains(raw, s.plaintext) {
		return bddErrf("the stored blob contains the plaintext")
	}
	return nil
}

func (s *rvBDD) hasNeitherCodeNorKey() error {
	vs := s.a.shared.(*valkeyStore)
	raw, _ := vs.rdb.Get(context.Background(), capsuleKeyPrefix+s.lookup).Bytes()
	if bytes.Contains(raw, []byte(s.code)) {
		return bddErrf("the stored blob contains the raw code")
	}
	// The lookup is all the broker holds; it must not derive the key/plaintext.
	if _, err := capsule.OpenWithCode(raw, s.lookup); err == nil {
		return bddErrf("the lookup decrypted the blob (key not domain-separated)")
	}
	return nil
}

func (s *rvBDD) cannotRecoverPlaintext() error {
	vs := s.a.shared.(*valkeyStore)
	raw, _ := vs.rdb.Get(context.Background(), capsuleKeyPrefix+s.lookup).Bytes()
	// Only the raw code recovers it.
	got, err := capsule.OpenWithCode(raw, s.code)
	if err != nil || !bytes.Equal(got, s.plaintext) {
		return bddErrf("the raw code must recover the plaintext: %v", err)
	}
	return nil
}

func (s *rvBDD) bothUniform404Identical() error {
	if s.unknown.Code != http.StatusNotFound || s.empty.Code != http.StatusNotFound {
		return bddErrf("unknown=%d empty=%d, both want 404", s.unknown.Code, s.empty.Code)
	}
	if s.unknown.Body.String() != s.empty.Body.String() {
		return bddErrf("unknown vs empty gave different bodies (an oracle)")
	}
	return nil
}

func (s *rvBDD) resolveIsUniform404() error {
	if s.res.Code != http.StatusNotFound {
		return bddErrf("resolve status %d, want 404", s.res.Code)
	}
	return nil
}

func (s *rvBDD) mintRefusedTooLarge() error {
	if s.mintCode != http.StatusRequestEntityTooLarge {
		return bddErrf("mint status %d, want 413", s.mintCode)
	}
	return nil
}

func (s *rvBDD) nothingStoredUnderLookup() error {
	if w := capsuleResolveReq(s.t, s.a, s.lookup); w.Code != http.StatusNotFound {
		return bddErrf("a refused mint must store nothing, got resolve %d", w.Code)
	}
	return nil
}

func (s *rvBDD) mintRejectedUnauthorized() error {
	if s.mintCode != http.StatusUnauthorized {
		return bddErrf("mint status %d, want 401", s.mintCode)
	}
	return nil
}

func (s *rvBDD) resolvingIsUniform404() error {
	if w := capsuleResolveReq(s.t, s.a, s.lookup); w.Code != http.StatusNotFound {
		return bddErrf("resolve status %d, want 404", w.Code)
	}
	return nil
}

func (s *rvBDD) resolveOnBReturnsExact() error {
	if s.resB.Code != http.StatusOK {
		return bddErrf("resolve on B status %d, want 200", s.resB.Code)
	}
	if got := decodeBlob(s.resB); !bytes.Equal(got, s.blob) {
		return bddErrf("resolve on B blob %q, want %q", got, s.blob)
	}
	return nil
}

func (s *rvBDD) exactlyOneWins() error {
	wins, misses := 0, 0
	for _, w := range []*httptest.ResponseRecorder{s.res, s.resB} {
		switch w.Code {
		case http.StatusOK:
			wins++
		case http.StatusNotFound:
			misses++
		default:
			return bddErrf("unexpected resolve status %d", w.Code)
		}
	}
	if wins != 1 || misses != 1 {
		return bddErrf("race must yield exactly one winner: wins=%d misses=%d", wins, misses)
	}
	return nil
}

func (s *rvBDD) noStoredValueLeaks() error {
	if err := s.holdsOnlyLookupAndCiphertext(); err != nil {
		return err
	}
	return s.hasNeitherCodeNorKey()
}

func decodeBlob(w *httptest.ResponseRecorder) []byte {
	var out struct{ Blob string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	b, _ := base64.StdEncoding.DecodeString(out.Blob)
	return b
}

func TestBrokerRendezvousBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &rvBDD{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				_, priv, _ := ed25519.GenerateKey(nil)
				*st = rvBDD{t: t, url: xiRedisURL(t), priv: priv}
				return ctx, nil
			})
			sc.Step(`^a signed capsule mint of an opaque blob under a lookup$`, st.signedMintUnderLookup)
			sc.Step(`^a stranger capsule sealed under a code and minted under its lookup$`, st.sealedAndMinted)
			sc.Step(`^a blob minted with an already-expired TTL$`, st.expiredMint)
			sc.Step(`^a signed capsule mint of a blob larger than the cap$`, st.oversizeMint)
			sc.Step(`^an UNSIGNED capsule mint under a lookup$`, st.unsignedMint)
			sc.Step(`^two broker instances sharing one store$`, st.twoInstances)
			sc.Step(`^a blob minted under a lookup$`, st.minted)
			sc.Step(`^I mint a blob on instance A under a lookup$`, st.mintOnAUnderLookup)

			sc.Step(`^I resolve that lookup$`, st.resolveLookup)
			sc.Step(`^I resolve its lookup$`, st.resolveExpired)
			sc.Step(`^I resolve an unknown lookup$`, st.resolveUnknown)
			sc.Step(`^I resolve an empty lookup$`, st.resolveEmpty)
			sc.Step(`^I resolve that lookup on instance B$`, st.resolveOnB)
			sc.Step(`^both instances resolve the same lookup concurrently$`, st.resolveConcurrently)

			sc.Step(`^the resolve returns the exact blob$`, st.resolveReturnsExact)
			sc.Step(`^a second resolve of the same lookup is a uniform 404$`, st.secondResolveUniform404)
			sc.Step(`^the broker holds only the lookup and the ciphertext$`, st.holdsOnlyLookupAndCiphertext)
			sc.Step(`^the broker has neither the code nor the key$`, st.hasNeitherCodeNorKey)
			sc.Step(`^the broker cannot recover the plaintext from what it stores$`, st.cannotRecoverPlaintext)
			sc.Step(`^both are a 404 with byte-identical bodies$`, st.bothUniform404Identical)
			sc.Step(`^the resolve is a uniform 404$`, st.resolveIsUniform404)
			sc.Step(`^the mint is refused as too-large$`, st.mintRefusedTooLarge)
			sc.Step(`^nothing is stored under its lookup$`, st.nothingStoredUnderLookup)
			sc.Step(`^the mint is rejected as unauthorized$`, st.mintRejectedUnauthorized)
			sc.Step(`^resolving that lookup is a uniform 404$`, st.resolvingIsUniform404)
			sc.Step(`^the resolve on B returns the exact blob$`, st.resolveOnBReturnsExact)
			sc.Step(`^exactly one resolve returns the blob and the other is a 404$`, st.exactlyOneWins)
			sc.Step(`^no stored value and no log line contains the code or the plaintext$`, st.noStoredValueLeaks)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/capsule/broker_rendezvous.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("broker rendezvous scenarios failed (see godog output above)")
	}
}
