package main

// Real TEE remote-attestation verification for the confidential tier.
//
// A node only earns the `confidential ◆` badge after the broker CRYPTOGRAPHICALLY
// verifies a hardware attestation quote. Verification has three independent gates,
// ALL of which must pass:
//
//  1. Signature chain (authenticity): for AMD SEV-SNP the ATTESTATION_REPORT is
//     signed by the VCEK, whose certificate chains VCEK -> ASK -> ARK up to AMD's
//     published root. We use github.com/google/go-sev-guest (verify.SnpAttestation)
//     which fetches the VCEK from the AMD KDS (cached here) and checks the chain to
//     the embedded AMD roots. We do NOT hand-roll any of this crypto.
//  2. Freshness + binding (anti-replay): the quote's report_data MUST equal
//     hash(node pubkey || broker nonce). The broker issues a single-use, short-lived
//     nonce per registration; binding the pubkey makes a quote useless to any OTHER
//     node, and binding the nonce makes it useless to replay or reuse once stale.
//  3. Measurement allowlist (what is running): the quote's launch MEASUREMENT must
//     be in a pinned, operator-configured allowlist of approved RogerAI serving-stack
//     measurements. An unknown measurement is rejected. With an EMPTY allowlist and
//     no require-flag, NO node is ever granted the tier (fail-closed: the tier is
//     simply unavailable, never falsely granted).
//
// The attestationVerifier interface is pluggable so Intel TDX
// (github.com/google/go-tdx-guest) and NVIDIA Confidential Computing GPU
// attestation can be added as additional backends later without touching the
// register/heartbeat flow.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"

	"github.com/google/go-sev-guest/abi"
	"github.com/google/go-sev-guest/kds"
	spb "github.com/google/go-sev-guest/proto/sevsnp"
	"github.com/google/go-sev-guest/validate"
	"github.com/google/go-sev-guest/verify"
	"github.com/google/go-sev-guest/verify/trust"
)

// attestKind names a TEE backend. Only SEV-SNP is implemented; the others are
// reserved so the interface is honestly pluggable.
const (
	attestSEVSNP   = "sev-snp"
	attestTDX      = "tdx"       // reserved: github.com/google/go-tdx-guest
	attestNvidiaCC = "nvidia-cc" // reserved: NVIDIA Confidential Computing GPU attestation
)

// attestParams is what a backend needs to verify a quote: the raw quote bytes, the
// node pubkey + broker nonce it must be bound to, and the measurement allowlist.
type attestParams struct {
	quote        []byte   // decoded quote bytes (backend-specific encoding)
	pubHex       string   // node Ed25519 pubkey (hex) the quote must bind
	nonceHex     string   // broker challenge nonce (hex) the quote must bind
	measurements [][]byte // allowlisted launch measurements (raw bytes); empty => none approved
}

// attestationVerifier verifies one TEE backend's quote. Verify returns the verified
// launch measurement (for logging/audit) and nil on success, or an error explaining
// the FIRST gate that failed. A backend must NOT return success unless the signature
// chain, the report_data binding, AND the measurement allowlist all pass.
type attestationVerifier interface {
	Kind() string
	Verify(ctx context.Context, p attestParams) (measurement []byte, err error)
}

// cachingGetter wraps go-sev-guest's default (retrying) KDS getter with a small
// in-process response cache so repeated registrations from the same chip do not
// refetch the VCEK from the AMD KDS every time. The VCEK is per-chip+TCB and stable,
// so URL-keyed caching is safe; entries expire so a TCB rotation is eventually
// re-fetched.
type cachingGetter struct {
	inner trust.HTTPSGetter
	ttl   time.Duration
	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	body []byte
	at   time.Time
}

func newCachingGetter(ttl time.Duration) *cachingGetter {
	return &cachingGetter{inner: trust.DefaultHTTPSGetter(), ttl: ttl, cache: map[string]cacheEntry{}}
}

func (g *cachingGetter) Get(url string) ([]byte, error) {
	g.mu.Lock()
	if e, ok := g.cache[url]; ok && time.Since(e.at) < g.ttl {
		body := e.body
		g.mu.Unlock()
		return body, nil
	}
	g.mu.Unlock()
	body, err := g.inner.Get(url)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.cache[url] = cacheEntry{body: body, at: time.Now()}
	g.mu.Unlock()
	return body, nil
}

// sevSNPVerifier verifies AMD SEV-SNP quotes via go-sev-guest.
type sevSNPVerifier struct {
	getter trust.HTTPSGetter
	// minTCB is the firmware/TCB floor: a quote whose reported TCB is below this is
	// rejected (an old, vulnerable firmware does not get the badge). Operator-tunable
	// via ROGERAI_TEE_MIN_* envs; zero-value means "no floor" (accept any TCB).
	minTCB kds.TCBParts
	// checkRevocations pulls the CRL from AMD and rejects a revoked VCEK/ASK. Off by
	// default (adds a network dependency on the hot register path); enable in prod.
	checkRevocations bool
	// testRoots / testProduct override the trusted AMD roots + product for tests that
	// sign quotes with a synthetic cert chain. Production leaves these nil and uses the
	// AMD-published roots embedded in go-sev-guest.
	testRoots   map[string][]*trust.AMDRootCerts
	testProduct *spb.SevProduct
}

func (v *sevSNPVerifier) Kind() string { return attestSEVSNP }

func (v *sevSNPVerifier) Verify(ctx context.Context, p attestParams) ([]byte, error) {
	if len(p.quote) == 0 {
		return nil, fmt.Errorf("empty quote")
	}
	// Parse the raw extended report (ATTESTATION_REPORT || VCEK cert table) into the
	// proto the verifier/validator consume.
	att, err := abi.ReportCertsToProto(p.quote)
	if err != nil {
		return nil, fmt.Errorf("parse sev-snp report: %w", err)
	}
	if att.GetReport() == nil {
		return nil, fmt.Errorf("no attestation report in quote")
	}

	// Gate 1: signature chain VCEK -> ASK -> ARK -> AMD root (go-sev-guest, AMD KDS).
	vopts := verify.DefaultOptions()
	if v.getter != nil {
		vopts.Getter = v.getter
	}
	vopts.CheckRevocations = v.checkRevocations
	if v.testRoots != nil {
		// Test-only: trust the synthetic ARK/ASK and skip the KDS fetch (the VCEK is
		// embedded in the quote's cert table). Never set in production.
		vopts.TrustedRoots = v.testRoots
		vopts.DisableCertFetching = true
	}
	if v.testProduct != nil {
		vopts.Product = v.testProduct
	}
	if err := verify.SnpAttestationContext(ctx, att, vopts); err != nil {
		return nil, fmt.Errorf("sev-snp signature chain invalid: %w", err)
	}

	// Gate 2 (binding) + Gate 3 (measurement): validate report_data == hash(pubkey ||
	// nonce) and the launch measurement against the allowlist, plus the TCB floor.
	wantReportData := protocol.AttestationReportData(p.pubHex, p.nonceHex)
	if len(wantReportData) != abi.ReportDataSize {
		return nil, fmt.Errorf("could not compute report_data binding")
	}
	if len(p.measurements) == 0 {
		// Fail-closed: no approved measurement => nobody is verified-confidential.
		return nil, fmt.Errorf("no approved TEE measurements configured (set ROGERAI_TEE_MEASUREMENTS)")
	}
	// go-sev-guest validate checks ONE expected measurement at a time, so try the
	// allowlist entry-by-entry. report_data + TCB floor are checked on every attempt;
	// success requires the measurement to match one allowlisted value.
	gotMeasurement := att.GetReport().GetMeasurement()
	var lastErr error
	for _, m := range p.measurements {
		vo := &validate.Options{
			ReportData:  wantReportData,
			Measurement: m,
			MinimumTCB:  v.minTCB,
			// LaunchTCB floor mirrors the component TCB floor.
			MinimumLaunchTCB: v.minTCB,
		}
		if err := validate.SnpAttestation(att, vo); err != nil {
			lastErr = err
			continue
		}
		return gotMeasurement, nil // all three gates passed
	}
	if lastErr != nil {
		// The most useful failure: distinguish a binding/TCB failure (same for every
		// allowlist entry) from a pure measurement mismatch.
		return nil, fmt.Errorf("sev-snp validation failed (binding/measurement/tcb): %w", lastErr)
	}
	return nil, fmt.Errorf("launch measurement %x not in the approved allowlist", gotMeasurement)
}

// attestRegistry holds the broker's verification policy + backends and the
// short-lived nonce store for the register handshake.
type attestRegistry struct {
	verifiers    map[string]attestationVerifier
	measurements [][]byte      // allowlist (raw measurement bytes)
	required     bool          // ROGERAI_TEE_REQUIRE: if a node CLAIMS confidential it MUST verify
	reattestTTL  time.Duration // verified-confidential status lapses after this without a fresh quote
	nonceTTL     time.Duration // how long an issued challenge nonce is valid

	mu     sync.Mutex
	nonces map[string]nonceEntry // nonce(hex) -> issued/expiry; single-use
}

type nonceEntry struct {
	expires time.Time
}

// loadAttestRegistry builds the verification policy from the environment:
//
//	ROGERAI_TEE_MEASUREMENTS    comma-separated hex launch measurements (the allowlist),
//	                            and/or ROGERAI_TEE_MEASUREMENTS_FILE (one hex per line, # comments).
//	ROGERAI_TEE_REQUIRE=1       a node that CLAIMS confidential MUST pass real attestation
//	                            (otherwise its registration is rejected, never silently downgraded).
//	ROGERAI_TEE_REATTEST        re-attestation cadence (default 1h); verified status lapses after this.
//	ROGERAI_TEE_NONCE_TTL       challenge nonce lifetime (default 5m).
//	ROGERAI_TEE_CHECK_REVOCATION=1  pull the AMD CRL and reject revoked VCEK/ASK.
//	ROGERAI_TEE_MIN_{BL,TEE,SNP,UCODE}_SPL  per-component TCB floor (firmware floor).
//
// With NO measurements configured the allowlist is empty: no node is ever granted the
// tier (fail-closed). The tier is simply unavailable until measurements are pinned.
func loadAttestRegistry() *attestRegistry {
	ms := parseMeasurements(os.Getenv("ROGERAI_TEE_MEASUREMENTS"))
	if f := os.Getenv("ROGERAI_TEE_MEASUREMENTS_FILE"); f != "" {
		if data, err := os.ReadFile(f); err == nil {
			ms = append(ms, parseMeasurements(string(data))...)
		} else {
			logf("TEE: could not read ROGERAI_TEE_MEASUREMENTS_FILE %q: %v", f, err)
		}
	}
	reattest := envDuration("ROGERAI_TEE_REATTEST", time.Hour)
	nonceTTL := envDuration("ROGERAI_TEE_NONCE_TTL", 5*time.Minute)

	sev := &sevSNPVerifier{
		getter:           newCachingGetter(6 * time.Hour),
		minTCB:           tcbFloorFromEnv(),
		checkRevocations: os.Getenv("ROGERAI_TEE_CHECK_REVOCATION") == "1",
	}
	r := &attestRegistry{
		verifiers:    map[string]attestationVerifier{sev.Kind(): sev},
		measurements: ms,
		required:     os.Getenv("ROGERAI_TEE_REQUIRE") == "1",
		reattestTTL:  reattest,
		nonceTTL:     nonceTTL,
		nonces:       map[string]nonceEntry{},
	}
	if len(ms) == 0 {
		logf("TEE: confidential tier UNAVAILABLE - no approved measurements (set ROGERAI_TEE_MEASUREMENTS to enable)")
	} else {
		logf("TEE: confidential tier ON - %d approved measurement(s), re-attest every %s, require=%v", len(ms), reattest, r.required)
	}
	return r
}

// setVerifier installs/replaces a backend (used by tests to inject a deterministic
// verifier, and the extension point for adding TDX / NVIDIA-CC backends).
func (a *attestRegistry) setVerifier(v attestationVerifier) {
	if a.verifiers == nil {
		a.verifiers = map[string]attestationVerifier{}
	}
	a.verifiers[v.Kind()] = v
}

// issueNonce mints a single-use challenge nonce and records its expiry.
func (a *attestRegistry) issueNonce() protocol.AttestChallenge {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	nonce := hex.EncodeToString(b)
	exp := time.Now().Add(a.nonceTTL)
	a.mu.Lock()
	a.nonces[nonce] = nonceEntry{expires: exp}
	a.pruneLocked()
	a.mu.Unlock()
	return protocol.AttestChallenge{Nonce: nonce, Expires: exp.Unix()}
}

// consumeNonce checks a nonce is known + unexpired and removes it (single-use), so a
// captured quote bound to a spent nonce cannot be replayed.
func (a *attestRegistry) consumeNonce(nonce string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.nonces[nonce]
	if !ok {
		return false
	}
	delete(a.nonces, nonce)
	return time.Now().Before(e.expires)
}

func (a *attestRegistry) pruneLocked() {
	now := time.Now()
	for n, e := range a.nonces {
		if now.After(e.expires) {
			delete(a.nonces, n)
		}
	}
}

// verifyRegistration is the broker's confidential-tier decision for a registration.
// It returns whether the node is verified-confidential and an error to REJECT the
// registration outright (used only when ROGERAI_TEE_REQUIRE is set and a claimed
// quote fails - so a node cannot quietly fall back to "standard" while still
// advertising itself as confidential to the operator's policy).
//
// Honest behavior:
//   - A node that does NOT claim confidential -> (false, nil): standard, no badge.
//   - A node that claims confidential with a quote that verifies -> (true, nil): ◆.
//   - A node that claims confidential but fails verification:
//   - require=false -> (false, nil): NO badge, registration still succeeds as standard.
//   - require=true  -> (false, err): registration REJECTED.
func (a *attestRegistry) verifyRegistration(ctx context.Context, reg protocol.NodeRegistration) (bool, error) {
	if a == nil || !reg.Confidential {
		return false, nil // no policy, or no claim -> no badge (honest)
	}
	measurement, err := a.verifyQuote(ctx, reg)
	if err != nil {
		if a.required {
			return false, fmt.Errorf("confidential claim failed attestation: %w", err)
		}
		logf("TEE: node %s claimed confidential but failed attestation (granted standard): %v", reg.NodeID, err)
		return false, nil
	}
	logf("TEE: node %s VERIFIED confidential (measurement %x)", reg.NodeID, measurement)
	return true, nil
}

// verifyQuote runs the full pipeline for a registration's quote: a known/fresh nonce
// (consumed single-use), then the backend's signature + binding + measurement checks.
func (a *attestRegistry) verifyQuote(ctx context.Context, reg protocol.NodeRegistration) ([]byte, error) {
	if len(a.measurements) == 0 {
		return nil, fmt.Errorf("confidential tier unavailable (no approved measurements configured)")
	}
	kind := reg.AttestKind
	if kind == "" {
		kind = attestSEVSNP // back-compat default
	}
	v, ok := a.verifiers[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported attestation kind %q", kind)
	}
	if reg.AttestNonce == "" {
		return nil, fmt.Errorf("missing attest_nonce (request one from /nodes/challenge)")
	}
	if !a.consumeNonce(reg.AttestNonce) {
		return nil, fmt.Errorf("attest_nonce unknown, expired, or already used")
	}
	quote, err := base64.StdEncoding.DecodeString(reg.Attestation)
	if err != nil {
		return nil, fmt.Errorf("attestation is not valid base64: %w", err)
	}
	return v.Verify(ctx, attestParams{
		quote:        quote,
		pubHex:       reg.PubKey,
		nonceHex:     reg.AttestNonce,
		measurements: a.measurements,
	})
}

// parseMeasurements parses a comma/newline-separated list of hex launch measurements,
// skipping blanks and #-comments.
func parseMeasurements(s string) [][]byte {
	var out [][]byte
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		f = strings.TrimSpace(f)
		if f == "" || strings.HasPrefix(f, "#") {
			continue
		}
		b, err := hex.DecodeString(f)
		if err != nil {
			logf("TEE: skipping invalid measurement hex %q: %v", f, err)
			continue
		}
		out = append(out, b)
	}
	return out
}

func tcbFloorFromEnv() kds.TCBParts {
	return kds.TCBParts{
		BlSpl:    uint8(envInt("ROGERAI_TEE_MIN_BL_SPL", 0)),
		TeeSpl:   uint8(envInt("ROGERAI_TEE_MIN_TEE_SPL", 0)),
		SnpSpl:   uint8(envInt("ROGERAI_TEE_MIN_SNP_SPL", 0)),
		UcodeSpl: uint8(envInt("ROGERAI_TEE_MIN_UCODE_SPL", 0)),
	}
}

// ensure spb is referenced even if a future refactor drops the direct use; the
// verifier consumes *spb.Attestation via abi.ReportCertsToProto.
var _ = (*spb.Attestation)(nil)

func logf(format string, args ...any) { log.Printf(format, args...) }

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
