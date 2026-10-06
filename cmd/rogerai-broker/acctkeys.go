package main

// acctkeys.go - account keys: guardrailed bearer credentials an account mints for ITSELF
// (ROUTING-EXPRESSION-CONTRACT §11; features/auth/key_guardrails.feature,
// features/relay/key_limits.feature).
//
// A key (`Bearer rog-key_<secret>`) authenticates a relay as the MINTING ACCOUNT: its wallet
// pays, the whole network routes, and the key's own guardrails bind - a spend limit over a UTC
// window, an expiry, allow-lists, a disabled switch. Only sha256(secret) is stored.
//
// THE RESERVE. A key's reservation is the sum of the pending holds attributed to it
// (store.HoldForKey); capturing such a hold turns it into key spend inside the same store
// transaction, and releasing or sweeping it simply removes it. So the reserve is captured and
// released on exactly the wallet hold's exit paths - served, voided, failed over, refused,
// disconnected, or orphaned by a killed instance (the deploy-orphan sweep) - with no second
// bookkeeping to drift. The check-and-hold runs under a per-key lock held in the SHARED store
// (setIfAbsent), so N concurrent requests on any number of instances pass exactly
// floor(remaining / hold) times: zero overshoot.
//
// THE LOOKUP. Key records live in the money store. Each instance caches resolved keys locally,
// tagged with a key epoch held in the shared store that every mint / patch / delete bumps; a
// lookup reads the epoch (one shared-store command, the same for a hit, a miss, a revoked or a
// disabled key - constant work) and goes to the store only when its cache is older. A shared
// store that cannot answer fails closed unless this instance already holds the key, and then
// the key is re-read from the money store rather than trusted from the cache.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math"
	"net/http"
	"rogerai.fm/roger/v6/internal/protocol"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"rogerai.fm/roger/v6/internal/store"
)

const (
	acctKeyPrefix      = "rog-key_"
	acctKeyIDPrefix    = "key_"
	acctKeyMaxLive     = 32
	acctKeyMaxList     = 64
	acctKeyNameMax     = 128
	acctKeyBodyMax     = 4 << 10
	acctKeyEpochKey    = "acctkeys:epoch"
	acctKeyCacheTTL    = 60 * time.Second
	acctKeyIdemTTL     = 10 * time.Minute
	acctKeyLockWait    = 20 * time.Second
	acctKeyLockPollMax = 25 * time.Millisecond
	acctKeyNearRatio   = 0.8
)

// acctKeyCached is one locally cached lookup (found=false caches a miss).
type acctKeyCached struct {
	k     store.AccountKey
	found bool
	epoch float64
	at    time.Time
}

// acctKeyState is the broker's per-instance key machinery: the lookup cache, the local epoch
// used when no shared store is wired (single instance), the local per-key locks of that case,
// the management limiter (shared-store backed), and the once-per-window log de-duplication.
type acctKeyState struct {
	mu         sync.Mutex
	byHash     map[string]acctKeyCached
	byID       map[string]acctKeyCached
	localEpoch float64
	locks      [64]sync.Mutex // local per-key locks, striped by key id so they never grow
	mgmt       *rateLimiter
	miss       *rateLimiter      // per address: relays bearing a key that does not resolve (keyMissLimiter)
	logged     map[string]string // key id -> the window it was last logged at-limit in

	bumpOwed     int       // epoch bumps that failed and are not yet re-sent
	bumpLastFail time.Time // when the latest one failed
	bumpRetrying bool      // a retrier is running
}

func (s *acctKeyState) init() {
	if s.byHash == nil {
		s.byHash, s.byID = map[string]acctKeyCached{}, map[string]acctKeyCached{}
		s.logged = map[string]string{}
	}
}

var errKeyLookup = errors.New("key lookup failed")

func acctKeyHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func acctKeyToken(r *http.Request) string {
	a := r.Header.Get("Authorization")
	if len(a) > 7 && strings.EqualFold(a[:7], "bearer ") && strings.HasPrefix(a[7:], acctKeyPrefix) {
		return a[7:]
	}
	return ""
}

// keyEpoch reads the shared key epoch (one shared-store command). With no shared store the
// instance is alone and its local epoch is authoritative.
func (b *broker) keyEpoch() (float64, error) {
	if b.shared == nil {
		return b.localKeyEpoch(), nil
	}
	v, _, err := b.shared.counterGet(acctKeyEpochKey)
	if errors.Is(err, errNoSharedStore) {
		return b.localKeyEpoch(), nil
	}
	return v, err
}

func (b *broker) localKeyEpoch() float64 {
	b.ak.mu.Lock()
	defer b.ak.mu.Unlock()
	return b.ak.localEpoch
}

// bumpKeyEpoch invalidates every instance's cached keys after a mint, patch or delete.
func (b *broker) bumpKeyEpoch() {
	b.ak.mu.Lock()
	b.ak.init()
	b.ak.localEpoch++
	b.ak.byHash, b.ak.byID = map[string]acctKeyCached{}, map[string]acctKeyCached{}
	b.ak.mu.Unlock()
	if b.shared != nil {
		if _, err := b.shared.counterIncr(acctKeyEpochKey, 1); err != nil && !errors.Is(err, errNoSharedStore) {
			log.Printf("account keys: epoch bump failed, retrying until it lands: %v", err)
			b.owedKeyEpochBump()
		}
	}
}

// owedKeyEpochBump records a bump that did not land and makes sure one background retrier
// re-sends it: peers that cached a key before the change keep serving it until the shared
// epoch moves (the shared store answering again does not move it), or their cache expires.
func (b *broker) owedKeyEpochBump() {
	b.ak.mu.Lock()
	defer b.ak.mu.Unlock()
	b.ak.bumpOwed++
	b.ak.bumpLastFail = time.Now()
	if b.ak.bumpRetrying {
		return
	}
	b.ak.bumpRetrying = true
	go b.retryKeyEpochBump()
}

// retryKeyEpochBump re-sends the owed bump with backoff until one lands after the latest
// failure, or until acctKeyCacheTTL past it (by then every peer's cache has expired anyway).
func (b *broker) retryKeyEpochBump() {
	wait := 50 * time.Millisecond
	for {
		time.Sleep(wait)
		b.ak.mu.Lock()
		owed, last := b.ak.bumpOwed, b.ak.bumpLastFail
		b.ak.mu.Unlock()
		_, err := b.shared.counterIncr(acctKeyEpochKey, 1)
		b.ak.mu.Lock()
		if ((err == nil || errors.Is(err, errNoSharedStore)) && b.ak.bumpOwed == owed) || time.Since(last) > acctKeyCacheTTL {
			b.ak.bumpOwed, b.ak.bumpRetrying = 0, false
			b.ak.mu.Unlock()
			return
		}
		b.ak.mu.Unlock()
		wait = min(2*wait, time.Second)
	}
}

// keyLookup resolves a key through the epoch-tagged local cache (byHash or byID).
func (b *broker) keyLookup(byHash bool, k string) (store.AccountKey, bool, error) {
	ep, eerr := b.keyEpoch()
	b.ak.mu.Lock()
	b.ak.init()
	cache := b.ak.byID
	if byHash {
		cache = b.ak.byHash
	}
	c, ok := cache[k]
	b.ak.mu.Unlock()
	if eerr != nil && !(ok && c.found) {
		return store.AccountKey{}, false, errKeyLookup // fail closed: nothing vouches for this key
	}
	if eerr == nil && ok && c.epoch == ep && time.Since(c.at) < acctKeyCacheTTL {
		return c.k, c.found, nil
	}
	// A stale cache, or a shared store that cannot say whether the cached key changed: read the
	// money store, which is the record (a key revoked on another instance is revoked here too).
	var rec store.AccountKey
	var found bool
	var err error
	if byHash {
		rec, found, err = b.db.AccountKeyByHash(k)
	} else {
		rec, found, err = b.db.AccountKeyByID(k)
	}
	if err != nil {
		return store.AccountKey{}, false, errKeyLookup
	}
	if eerr == nil && found { // a miss is never cached: the cache holds only real keys
		b.ak.mu.Lock()
		cache[k] = acctKeyCached{k: rec, found: found, epoch: ep, at: time.Now()}
		b.ak.mu.Unlock()
	}
	return rec, found, nil
}

// keyRefusal is a relay refused while resolving a key.
type keyRefusal struct {
	status    int
	code, msg string
	retry     int // Retry-After seconds, for a 429
}

// resolveRelayKey authenticates a `Bearer rog-key_...` relay. ok=false with a nil refusal means
// the request carries no key.
// An address that keeps presenting keys that do not resolve is rate limited BEFORE the lookup
// (keyMissLimiter), so it costs neither a store read nor memory; only a refused key draws from that
// budget, so a working key is never slowed by it. The caller records the use (TouchAccountKey)
// once the relay is admitted.
func (b *broker) resolveRelayKey(r *http.Request) (store.AccountKey, bool, *keyRefusal) {
	tok := acctKeyToken(r)
	if tok == "" {
		return store.AccountKey{}, false, nil
	}
	ip := clientIP(r)
	if blocked, retry := b.keyMissLimiter().blocked(ip); blocked {
		return store.AccountKey{}, false, &keyRefusal{status: http.StatusTooManyRequests, code: "rate_limited", msg: "too many requests with keys that do not work - slow down", retry: retry}
	}
	k, found, err := b.keyLookup(true, acctKeyHash(tok))
	if err != nil {
		return store.AccountKey{}, false, &keyRefusal{status: http.StatusServiceUnavailable, code: "key_lookup_failed", msg: "key lookup failed - try again shortly"}
	}
	if ref := keyStateRefusal(k, found, b.now()); ref != nil {
		b.keyMissLimiter().allow(ip)
		return store.AccountKey{}, false, ref
	}
	return k, true, nil
}

// keyStateRefusal is the 401 a key that is not live gets: revoked > expired > disabled, and
// an unknown secret its own code (the secret is 32 random bytes, so distinct codes are no
// enumeration oracle; the work is the same either way).
func keyStateRefusal(k store.AccountKey, found bool, now time.Time) *keyRefusal {
	switch {
	case !found:
		return &keyRefusal{status: http.StatusUnauthorized, code: "key_invalid", msg: "key invalid"}
	case k.Revoked:
		return &keyRefusal{status: http.StatusUnauthorized, code: "key_revoked", msg: "key revoked"}
	case k.Expired(now):
		return &keyRefusal{status: http.StatusUnauthorized, code: "key_expired", msg: "key expired"}
	case k.Disabled:
		return &keyRefusal{status: http.StatusUnauthorized, code: "key_disabled", msg: "key disabled"}
	}
	return nil
}

// ---- windows ----------------------------------------------------------------------------------

// keyWindow is the key's current spend window in unix MILLISECONDS: [from, until) with until 0
// for reset "none" (lifetime), and the words the notices use. A window never starts before the
// last reset change (Anchor).
func keyWindow(k store.AccountKey, now time.Time) (from, until int64, words string) {
	t := now.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	var start, next time.Time
	switch k.Reset {
	case "daily":
		start, next, words = day, day.AddDate(0, 0, 1), "today"
	case "weekly":
		start = day.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)) // weeks start Monday 00:00Z
		next, words = start.AddDate(0, 0, 7), "this week"
	case "monthly":
		start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		next, words = start.AddDate(0, 1, 0), "this month"
	default:
		return 0, 0, ""
	}
	from, until = start.UnixMilli(), next.UnixMilli()
	if k.Anchor > from {
		from = k.Anchor
	}
	return from, until, words
}

// keyResetAt is when the key's window next resets ("" for reset none).
func keyResetAt(k store.AccountKey, now time.Time) string {
	_, until, _ := keyWindow(k, now)
	if until == 0 {
		return ""
	}
	return time.UnixMilli(until).UTC().Format(time.RFC3339)
}

// keyLimitState is the key's window as the gate and the headers read it.
type keyLimitState struct {
	k        store.AccountKey
	spend    float64 // settled spend in the window
	reserved float64 // open reservations
	words    string
}

func (b *broker) keyLimitState(k store.AccountKey, now time.Time) (keyLimitState, error) {
	from, until, words := keyWindow(k, now)
	spend, err := b.db.KeySpend(k.ID, from, until)
	if err != nil {
		return keyLimitState{}, err
	}
	res, err := b.db.KeyReserved(k.ID)
	if err != nil {
		return keyLimitState{}, err
	}
	return keyLimitState{k: k, spend: round6(spend), reserved: res, words: words}, nil
}

// fits: spend + open reservations + amount within the limit (exactly at it is allowed).
func (s keyLimitState) fits(amount float64) bool {
	return s.k.LimitUSD <= 0 || s.spend+s.reserved+amount <= s.k.LimitUSD+1e-9
}

func (s keyLimitState) remaining() float64 {
	return math.Max(0, round6(s.k.LimitUSD-s.spend-s.reserved))
}

// setKeyHeaders writes the X-RogerAI-Key-* headers (absent for an unlimited key, as the
// monthly headers are when unlimited). spend is the window spend BEFORE this request.
func setKeyHeaders(w http.ResponseWriter, s keyLimitState, atLimit bool) {
	if s.k.LimitUSD <= 0 {
		return
	}
	h := w.Header()
	pct := s.spend / s.k.LimitUSD
	h.Set("X-RogerAI-Key-Limit", ftoa(round6(s.k.LimitUSD)))
	h.Set("X-RogerAI-Key-Spend", ftoa(round6(s.spend)))
	h.Set("X-RogerAI-Key-Pct", fmt.Sprintf("%.0f", pct*100))
	switch {
	case atLimit:
		h.Set("X-RogerAI-Key-Notice", strings.TrimSpace(fmt.Sprintf("key limit reached - $%.2f of $%.2f %s", s.spend, s.k.LimitUSD, s.words)))
	case pct >= acctKeyNearRatio-1e-9:
		h.Set("X-RogerAI-Key-Notice", fmt.Sprintf("you've used $%.2f of your $%.2f key limit (%.0f%%)", s.spend, s.k.LimitUSD, pct*100))
	}
}

// keyLimitMessage is the 402 message for a refused key.
func keyLimitMessage(s keyLimitState, now time.Time) string {
	if s.words == "" {
		return fmt.Sprintf("key spend limit reached: $%.2f of $%.2f (no reset)", s.spend, s.k.LimitUSD)
	}
	return fmt.Sprintf("key spend limit reached: $%.2f of $%.2f %s (resets %s)", s.spend, s.k.LimitUSD, s.words, keyResetAt(s.k, now))
}

// jsonErr402 is the 402 envelope with its source and a remedy (contract §11).
func jsonErr402(w http.ResponseWriter, code, msg, source, hint string) {
	w.Header().Set("X-RogerAI-Cost", "0")
	writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": map[string]any{
		"code": code, "message": msg,
		"metadata": map[string]string{"limit_source": source, "remedy_hint": hint},
	}})
}

const (
	remedyMonthly = "raise it with `roger limit --monthly` (or [3] CONFIG), or wait until next month"
	remedyCredits = "top up at /billing"
)

func remedyKey(id string) string {
	return "raise it with `roger keys set " + id + " --limit`, wait for the reset, or use another key"
}

// keyAtLimit records one at-limit refusal: counted always, logged once per key per window,
// and the 100% notice mailed once per window.
func (b *broker) keyAtLimit(s keyLimitState, now time.Time) {
	b.stats.keyLimitRefusals.Add(1)
	from, _, _ := keyWindow(s.k, now)
	win := strconv.FormatInt(from, 10)
	b.ak.mu.Lock()
	b.ak.init()
	first := b.ak.logged[s.k.ID] != win
	b.ak.logged[s.k.ID] = win
	b.ak.mu.Unlock()
	if first {
		log.Printf("account key %s at its limit: $%.2f of $%.2f", s.k.ID, s.spend, s.k.LimitUSD)
	}
	b.emailKeyNotice(s, "100", win)
}

// acctKeyLockTTL bounds how long a crashed holder can block a key's check-and-hold. The lock
// only reduces contention: correctness comes from the hold transaction's own re-check
// (store.HoldForKey with a KeyLimit), so a lock that lapses can never overshoot. A var so a
// test can make it lapse.
var acctKeyLockTTL = 5 * time.Second

// keyHoldGapForTest, when set (tests only), runs after a request passes its key-limit
// pre-check and before it places its hold: the window in which another instance can act.
var keyHoldGapForTest func(b *broker)

// keyMintGapForTest, when set (tests only), runs after a mint passes its checks and before it
// writes the key: the window in which another instance can mint for the same account.
var keyMintGapForTest func(b *broker)

// keyPatchGapForTest, when set (tests only), runs after a PATCH has read the key and before it
// writes: the window in which another instance can delete or edit the same key.
var keyPatchGapForTest func(b *broker)

// keyLock serializes a key's check-and-hold across every instance (a lock in the shared store;
// a local mutex when the instance is alone). A shared store that cannot answer fails closed.
func (b *broker) keyLock(id string) (func(), error) {
	if b.shared != nil {
		tok := randHex(8)
		deadline := time.Now().Add(acctKeyLockWait)
		wait := time.Millisecond
		for {
			set, err := b.shared.setIfAbsent("acctkeylock:"+id, tok, acctKeyLockTTL)
			if errors.Is(err, errNoSharedStore) {
				break
			}
			if err != nil {
				return nil, err
			}
			if set {
				// Release only if the lock is still ours: after a lapse another holder owns it.
				return func() { _ = b.shared.delIfEqual("acctkeylock:"+id, tok) }, nil
			}
			if time.Now().After(deadline) {
				return nil, errors.New("key lock wait exceeded")
			}
			time.Sleep(wait) // back off: a waiter re-asks at most every acctKeyLockPollMax
			wait = min(2*wait, acctKeyLockPollMax)
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	m := &b.ak.locks[h.Sum32()%uint32(len(b.ak.locks))]
	m.Lock()
	return m.Unlock, nil
}

func randHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// keyAllowsModel: the key's model allow-list (empty = all).
func keyAllowsModel(k store.AccountKey, model string) bool {
	return len(k.AllowedModels) == 0 || containsString(k.AllowedModels, model)
}

// ---- management -------------------------------------------------------------------------------

// accountKeys handles /account/keys and /account/keys/{id}: mint, list, read, patch, delete.
// Management needs the SIGNED account identity or a web session behind an allowlisted Origin;
// a bearer credential (a key or a grant) can never manage keys.
func (b *broker) accountKeys(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/account/keys"), "/")
	switch {
	case id == "" && r.Method != http.MethodGet && r.Method != http.MethodPost,
		id != "" && r.Method != http.MethodGet && r.Method != http.MethodPatch && r.Method != http.MethodDelete:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if tok := acctKeyToken(r); tok != "" || grantTokenFromHeader(r) != "" {
		if tok != "" {
			// Limited per address before the lookup and the audit row, so repeated attempts
			// cost neither a store read nor a ledger row each.
			if ok, retry := b.keyMissLimiter().allow(clientIP(r)); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				jsonErr(w, http.StatusTooManyRequests, "too many requests - slow down")
				return
			}
			if k, found, err := b.keyLookup(true, acctKeyHash(tok)); err == nil && found {
				b.keyAudit(k.Account, k.ID, "denied", nil)
			}
		}
		jsonErrCode(w, http.StatusForbidden, "key_cannot_manage_keys", "a key cannot manage keys - use your signed account identity")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, acctKeyBodyMax+1))
	if len(body) > acctKeyBodyMax {
		jsonErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	acct, notify, ok := b.keyManager(r, body)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "invalid request signature")
		return
	}
	if !walletLoggedIn(acct) {
		jsonErr(w, http.StatusUnauthorized, "log in to manage keys - run `roger login` (keys are per account)")
		return
	}
	if r.Method != http.MethodGet {
		if ok, retry := b.keyMgmtLimiter().allow(acct); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			jsonErr(w, http.StatusTooManyRequests, "too many key changes - slow down")
			return
		}
	}
	switch {
	case id == "" && r.Method == http.MethodGet:
		b.listKeys(w, acct)
	case id == "":
		b.mintKey(w, r, acct, notify, body)
	case r.Method == http.MethodGet:
		if k, ok := b.ownKey(acct, id); ok {
			writeJSON(w, http.StatusOK, b.keyEntry(k, b.now()))
			return
		}
		keyNotFound(w)
	case r.Method == http.MethodPatch:
		b.patchKey(w, acct, id, body)
	default:
		b.deleteKey(w, acct, id)
	}
}

// keyMgmtLimiter is the per-account limiter on key changes: one bucket in the shared store for
// every instance, degrading to this instance's own bucket while the shared store cannot answer
// (as every request limiter does).
// keyMissLimiter is the per-address limit on relays bearing a key that does not resolve.
func (b *broker) keyMissLimiter() *rateLimiter {
	b.ak.mu.Lock()
	defer b.ak.mu.Unlock()
	if b.ak.miss == nil {
		b.ak.miss = loadKeyMissRateLimiter()
	}
	return b.ak.miss
}

func (b *broker) keyMgmtLimiter() *rateLimiter {
	b.ak.mu.Lock()
	defer b.ak.mu.Unlock()
	if b.ak.mgmt == nil {
		b.ak.mgmt = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 10, burst: 10, name: "acctkeys", shared: b.shared}
	}
	return b.ak.mgmt
}

// keyManager authenticates a management request (a web session from an allowlisted Origin, or
// a signed request; an unsigned legacy id never qualifies): the account wallet, and the owner
// pubkey a minted key's notices are mailed to (an account wallet like u_gh_<id> is not one).
func (b *broker) keyManager(r *http.Request, body []byte) (acct, notify string, ok bool) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if !originAllowed(r) {
			return "", "", false
		}
		login, wallet, ok := b.webSession(r)
		if !ok {
			return "", "", false
		}
		if o, found, _ := b.db.OwnerByLogin(login); found {
			notify = o.Pubkey
		}
		return wallet, notify, true
	}
	rid, authed, ok := b.identityOf(r, body)
	if !ok || !authed {
		return "", "", false
	}
	return b.walletOf(r, rid), r.Header.Get(protocol.HeaderPubkey), true // the verified signer
}

func keyNotFound(w http.ResponseWriter) {
	jsonErrCode(w, http.StatusNotFound, "not_found", "no such key")
}

// ownKey is the lookup-then-owner-compare path: the same work for an id another account owns
// and one that does not exist, and the same 404 for both (and for a deleted key).
func (b *broker) ownKey(acct, id string) (store.AccountKey, bool) {
	k, found, err := b.keyLookup(false, id)
	if err != nil || !found || k.Account != acct || k.Revoked {
		return store.AccountKey{}, false
	}
	if fresh, ok, err := b.db.AccountKeyByID(id); err == nil && ok {
		k = fresh // the listing reads usage counters the cache does not track
	}
	if k.Revoked {
		return store.AccountKey{}, false
	}
	return k, true
}

// keyFields are the settable fields, validated (keyFieldError names the field).
type keyFields struct {
	set     []string
	name    *string
	limit   *float64
	reset   *string
	expires *int64 // 0 = never
	models  *[]string
	nodes   *[]string
	disable *bool
}

type keyFieldError struct{ code, field, msg string }

func badKeyField(field, msg string) *keyFieldError {
	return &keyFieldError{"invalid_key_field", field, "invalid " + field + ": " + msg}
}

func parseKeyFields(body []byte, now time.Time) (keyFields, *keyFieldError) {
	var f keyFields
	var raw map[string]json.RawMessage
	if len(strings.TrimSpace(string(body))) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return f, &keyFieldError{"invalid_key_field", "body", "the body must be a JSON object"}
	}
	known := map[string]bool{"name": true, "limit_usd": true, "reset": true, "expires_at": true, "allowed_models": true, "allowed_nodes": true, "disabled": true}
	names := make([]string, 0, len(raw))
	for n := range raw {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !known[n] {
			return f, &keyFieldError{"unknown_key_field", n, "unknown key field " + n}
		}
	}
	for _, n := range names {
		v := raw[n]
		f.set = append(f.set, n)
		switch n {
		case "name":
			var s string
			if json.Unmarshal(v, &s) != nil {
				return f, badKeyField(n, "must be a string")
			}
			if len(s) > acctKeyNameMax || strings.IndexFunc(s, unicode.IsControl) >= 0 {
				return f, badKeyField(n, "at most 128 printable characters")
			}
			f.name = &s
		case "limit_usd":
			var x float64
			if len(v) == 0 || v[0] == '"' || json.Unmarshal(v, &x) != nil || math.IsNaN(x) || math.IsInf(x, 0) || x < 0 {
				return f, badKeyField(n, "must be a number >= 0 (0 = unlimited)")
			}
			x = round6(x)
			f.limit = &x
		case "reset":
			var s string
			if json.Unmarshal(v, &s) != nil || (s != "daily" && s != "weekly" && s != "monthly" && s != "none") {
				return f, badKeyField(n, "must be daily, weekly, monthly or none")
			}
			f.reset = &s
		case "expires_at":
			if string(v) == "null" {
				z := int64(0)
				f.expires = &z
				continue
			}
			var s string
			if json.Unmarshal(v, &s) != nil || !strings.HasSuffix(s, "Z") {
				return f, badKeyField(n, "must be an RFC 3339 UTC time ending in Z")
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return f, badKeyField(n, "must be an RFC 3339 UTC time ending in Z")
			}
			if !t.After(now) {
				return f, badKeyField(n, "must be in the future")
			}
			u := t.Unix()
			f.expires = &u
		case "allowed_models", "allowed_nodes":
			var arr []string
			if json.Unmarshal(v, &arr) != nil || arr == nil && string(v) != "[]" {
				return f, badKeyField(n, "must be a list of ids")
			}
			if len(arr) > acctKeyMaxList {
				return f, badKeyField(n, "at most 64 entries")
			}
			out := make([]string, 0, len(arr))
			for _, s := range arr {
				if s == "" || strings.TrimSpace(s) != s {
					return f, badKeyField(n, "entries are exact non-empty ids")
				}
				if !containsString(out, s) {
					out = append(out, s)
				}
			}
			if n == "allowed_models" {
				f.models = &out
			} else {
				f.nodes = &out
			}
		case "disabled":
			var x bool
			if json.Unmarshal(v, &x) != nil {
				return f, badKeyField(n, "must be true or false")
			}
			f.disable = &x
		}
	}
	return f, nil
}

func (f keyFields) apply(k *store.AccountKey, now time.Time) {
	if f.name != nil {
		k.Name = *f.name
	}
	if f.limit != nil {
		k.LimitUSD = *f.limit
	}
	if f.reset != nil && *f.reset != k.Reset {
		k.Reset = *f.reset
		k.Anchor = now.UnixMilli() // a changed interval starts its window at the change
	}
	if f.expires != nil {
		k.ExpiresAt = *f.expires
	}
	if f.models != nil {
		k.AllowedModels = *f.models
	}
	if f.nodes != nil {
		k.AllowedNodes = *f.nodes
	}
	if f.disable != nil {
		k.Disabled = *f.disable
	}
}

func writeKeyFieldError(w http.ResponseWriter, e *keyFieldError) {
	jsonErrCode(w, http.StatusBadRequest, e.code, e.msg)
}

func (b *broker) mintKey(w http.ResponseWriter, r *http.Request, acct, notify string, body []byte) {
	now := b.now()
	f, ferr := parseKeyFields(body, now)
	if ferr != nil {
		writeKeyFieldError(w, ferr)
		return
	}
	idem := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	secret := acctKeyPrefix + randHex(32)
	k := store.AccountKey{
		ID: acctKeyIDPrefix + randHex(8), SecretHash: acctKeyHash(secret), Account: acct,
		Hint: "..." + secret[len(secret)-4:], Reset: "none", IdemKey: idem, OwnerPub: notify,
		CreatedAt: now.UnixNano(), AllowedModels: []string{}, AllowedNodes: []string{},
	}
	f.apply(&k, now)
	k.Anchor = 0 // a new key's window is the calendar's
	if keyMintGapForTest != nil {
		keyMintGapForTest(b)
	}
	// The cap and the replay check run inside the store's write, so mints racing on several
	// instances cannot pass them.
	k, err := b.db.CreateAccountKey(k, store.MintKeyRules{MaxLive: acctKeyMaxLive, IdemSince: now.Add(-acctKeyIdemTTL).UnixNano()})
	var replay *store.KeyReplayError
	switch {
	case errors.As(err, &replay):
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{
			"code": "already_minted", "message": "this mint was already done as " + replay.ID + " - the secret is shown once, ever",
			"id": replay.ID}, "id": replay.ID})
		return
	case errors.Is(err, store.ErrKeyCount):
		jsonErrCode(w, http.StatusBadRequest, "key_limit_count", "at most 32 keys per account - delete one first")
		return
	case err != nil:
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	b.bumpKeyEpoch()
	b.keyAudit(acct, k.ID, "mint", f.set)
	log.Printf("account key %s minted", k.ID) // the id only: never the secret or its hash
	e := b.keyEntry(k, now)
	e["secret"] = secret
	writeJSON(w, http.StatusCreated, e)
}

func (b *broker) patchKey(w http.ResponseWriter, acct, id string, body []byte) {
	now := b.now()
	f, ferr := parseKeyFields(body, now)
	if ferr != nil {
		writeKeyFieldError(w, ferr)
		return
	}
	if len(f.set) == 0 {
		jsonErrCode(w, http.StatusBadRequest, "empty_patch", "empty patch - name at least one field to change")
		return
	}
	k, ok := b.ownKey(acct, id)
	if !ok {
		keyNotFound(w)
		return
	}
	if keyPatchGapForTest != nil {
		keyPatchGapForTest(b)
	}
	// The edit applies to the live row under its own lock: a delete or another edit that
	// landed since the read above is never written back over.
	k, found, err := b.db.UpdateAccountKey(k.ID, func(live *store.AccountKey) { f.apply(live, now) })
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !found {
		keyNotFound(w)
		return
	}
	b.bumpKeyEpoch()
	b.keyAudit(acct, k.ID, "patch", f.set)
	writeJSON(w, http.StatusOK, b.keyEntry(k, now))
}

func (b *broker) deleteKey(w http.ResponseWriter, acct, id string) {
	k, ok := b.ownKey(acct, id)
	if !ok {
		keyNotFound(w)
		return
	}
	_, found, err := b.db.UpdateAccountKey(k.ID, func(live *store.AccountKey) { live.Revoked = true })
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !found {
		keyNotFound(w)
		return
	}
	b.bumpKeyEpoch()
	b.keyAudit(acct, k.ID, "delete", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (b *broker) listKeys(w http.ResponseWriter, acct string) {
	keys, err := b.db.AccountKeysOf(acct)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "store error")
		return
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].CreatedAt != keys[j].CreatedAt {
			return keys[i].CreatedAt > keys[j].CreatedAt
		}
		return keys[i].ID > keys[j].ID // unreachable for one account's keys (strict mint order)
	})
	now := b.now()
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		if !k.Revoked && len(out) < 100 {
			out = append(out, b.keyEntry(k, now))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// keyEntry is one key as the list and the read show it: never the secret or its hash.
func (b *broker) keyEntry(k store.AccountKey, now time.Time) map[string]any {
	ts := func(sec int64) any {
		if sec == 0 {
			return nil
		}
		return time.Unix(sec, 0).UTC().Format(time.RFC3339)
	}
	e := map[string]any{
		"id": k.ID, "name": k.Name, "hint": k.Hint, "limit_usd": round6(k.LimitUSD), "reset": k.Reset,
		"expires_at": ts(k.ExpiresAt), "disabled": k.Disabled, "created_at": ts(k.CreatedAt / 1e9),
		"allowed_models": nonNil(k.AllowedModels), "allowed_nodes": nonNil(k.AllowedNodes),
		"expired": k.Expired(now), "requests": k.Requests, "last_used": nil, "limit_remaining": nil,
	}
	if k.LastUsed != 0 {
		e["last_used"] = time.Unix(0, k.LastUsed).UTC().Format(time.RFC3339Nano)
	}
	t := now.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	week := day.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
	month := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	from, _, _ := keyWindow(k, now) // the limit's window: its spend is the spend since it began
	sums, err := b.db.KeySpendSince(k.ID, []int64{0, day.UnixMilli(), week.UnixMilli(), month.UnixMilli(), from})
	if err != nil {
		sums = make([]float64, 5)
	}
	e["usage"], e["usage_daily"], e["usage_weekly"], e["usage_monthly"] = round6(sums[0]), round6(sums[1]), round6(sums[2]), round6(sums[3])
	if k.LimitUSD > 0 && err == nil {
		if res, rerr := b.db.KeyReserved(k.ID); rerr == nil {
			e["limit_remaining"] = keyLimitState{k: k, spend: round6(sums[4]), reserved: res}.remaining()
		}
	}
	return e
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// keyAudit writes the $0 key_event audit row: account, key id, action and the field NAMES
// (never values, the secret, or its hash).
func (b *broker) keyAudit(acct, id, action string, fields []string) {
	ref := "key_event key=" + id + " action=" + action
	if len(fields) > 0 {
		ref += " fields=" + strings.Join(fields, ",")
	}
	if err := b.db.AppendKeyEvent(acct, ref, b.now().Unix()); err != nil {
		log.Printf("account keys: audit row for %s %s failed: %v", id, action, err)
	}
}

// keyRefsOf maps every settled request ref of the account's keys (deleted ones included) to
// its key id, for attributing spend in /usage and the console lineage.
func (b *broker) keyRefsOf(acct string) map[string]string {
	out, err := b.db.AccountKeySpendRefs(acct)
	if err != nil || len(out) == 0 {
		return nil
	}
	return out
}

// emailKeyNotice mails the 80% / 100% key notice, once per key per window, naming the key by
// name and id (never the secret).
func (b *broker) emailKeyNotice(s keyLimitState, threshold, window string) {
	if !b.mail.enabled() {
		return
	}
	email := b.emailOf(s.k.OwnerPub)
	if email == "" {
		email = b.emailOf(s.k.Account) // a pubkey wallet is its own owner key
	}
	if email == "" {
		return
	}
	claimed, release := b.keyNoticeOnce(s, threshold, window)
	if !claimed {
		return
	}
	pct := s.spend / s.k.LimitUSD * 100
	if threshold == "100" {
		pct = 100
	}
	subj := fmt.Sprintf("Key %q (%s) at %.0f%% of its limit", s.k.Name, s.k.ID, pct)
	text := fmt.Sprintf("Your key %q (%s) has used $%.2f of its $%.2f limit (%.0f%%).", s.k.Name, s.k.ID, s.spend, s.k.LimitUSD, pct)
	b.mail.sendEmailOrRelease(email, subj, "<p>"+text+"</p>", text, release)
}

// keyNoticeOnce claims one key notice (key, window, threshold) for this instance, and returns
// how to give the claim back if the email is then dropped. The claim is held in the shared store
// until the window ends, so exactly one instance mails it; only when no shared store answers
// does the mailer's in-process record de-duplicate instead.
func (b *broker) keyNoticeOnce(s keyLimitState, threshold, window string) (bool, func()) {
	now := b.now()
	if b.shared != nil {
		ttl := 400 * 24 * time.Hour // reset none: the window is the key's whole life
		if _, until, _ := keyWindow(s.k, now); until > 0 {
			ttl = time.UnixMilli(until).Sub(now) + time.Hour
		}
		key := "keynotice:" + s.k.ID + ":" + window + ":" + threshold
		set, err := b.shared.setIfAbsent(key, "1", ttl)
		if err == nil {
			return set, func() { _ = b.shared.counterDel(key) }
		}
	}
	holder := "key:" + s.k.ID + ":" + window
	return b.mail.capNoticeOnce(holder, threshold, now), func() { b.mail.releaseCapNotice(holder, threshold, now) }
}

// keyChunkFields is the key state after settle that a stream's usage chunk carries.
func (b *broker) keyChunkFields(k store.AccountKey, start time.Time) map[string]any {
	out := map[string]any{"key_id": k.ID}
	st, err := b.keyLimitState(k, start)
	if err != nil {
		return out
	}
	out["key_limit"], out["key_spend"] = round6(k.LimitUSD), st.spend
	if k.LimitUSD > 0 {
		out["key_pct"] = math.Round(st.spend / k.LimitUSD * 100)
	}
	if r := keyResetAt(k, start); r != "" {
		out["key_reset_at"] = r
	}
	return out
}

// planCostsNothing reports whether every pair in the plan bills the caller $0 right now.
func planCostsNothing(plan []attemptCand, now time.Time) bool {
	for _, c := range plan {
		if c.pricing.free {
			continue
		}
		if in, out, free, _ := c.offer.ActivePrice(now); free || (in <= 0 && out <= 0) {
			continue
		}
		return false
	}
	return true
}
