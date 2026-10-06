package store

import (
	"errors"
	"math"
	"strings"
	"time"
)

// AccountKey is a guardrailed bearer credential (rog-key_<secret>) an account mints for
// ITSELF: it pays from the minting account's wallet, routes the whole network, and is held to
// its own spend limit over a UTC window, an expiry, and allow-lists (ROUTING-EXPRESSION-
// CONTRACT §11; features/auth/key_guardrails.feature). Only sha256(secret) is stored; the
// secret is shown once at mint. A deleted key is kept REVOKED (never erased) so its spend stays
// attributed in the account's history.
type AccountKey struct {
	ID            string   `json:"id"`      // "key_<rand>"
	SecretHash    string   `json:"-"`       // sha256(secret)
	Account       string   `json:"account"` // the minting account's wallet (the payer)
	Name          string   `json:"name"`
	Hint          string   `json:"hint"`      // "...<last 4 of the secret>"
	LimitUSD      float64  `json:"limit_usd"` // 0 = unlimited
	Reset         string   `json:"reset"`     // daily | weekly | monthly | none
	Anchor        int64    `json:"anchor"`    // unix MILLIS; a window never starts before the last reset change
	ExpiresAt     int64    `json:"expires_at"`
	AllowedModels []string `json:"allowed_models"`
	AllowedNodes  []string `json:"allowed_nodes"`
	Disabled      bool     `json:"disabled"`
	Revoked       bool     `json:"revoked"`
	IdemKey       string   `json:"-"`          // the Idempotency-Key the mint carried ("" = none)
	OwnerPub      string   `json:"-"`          // the minting identity's owner pubkey (where notices are mailed)
	CreatedAt     int64    `json:"created_at"` // unix NANOS, strictly increasing per account
	LastUsed      int64    `json:"last_used"`  // unix nanos
	Requests      int64    `json:"requests"`
}

// Expired reports whether the key has reached its expiry (0 = never). Exactly at the expiry
// is expired.
func (k AccountKey) Expired(now time.Time) bool {
	return k.ExpiresAt != 0 && now.Unix() >= k.ExpiresAt
}

// Key ledger kinds. Key spend rows are held under the KEY id (side "key"), never a wallet, so
// no wallet balance or month-spend read ever sees them; key_event rows are $0 audit rows on the
// account's wallet. UNITS: key_spend and key_reversal rows date their ts in unix MILLISECONDS
// (key windows are millisecond bounds); key_event rows, like every other ledger row, in seconds.
const (
	KindKeySpend    = "key_spend"    // key: settled spend attributed to the key (-amount)
	KindKeyReversal = "key_reversal" // key: a chargeback/refund of a key request (+amount)
	KindKeyEvent    = "key_event"    // consumer: $0 audit row for a key management action
)

// keySpendRow reports whether a ledger row is part of key keyID's spend and its signed
// contribution (spend positive, reversals negative).
func keySpendRow(r LedgerRow, keyID string) (float64, bool) {
	if r.Holder != keyID || r.State == StateReversed {
		return 0, false
	}
	switch r.Kind {
	case KindKeySpend:
		return -r.Amount, true
	case KindKeyReversal:
		return -r.Amount, true
	}
	return 0, false
}

// keyRefMatches: a key spend row's ref is the attempt id that settled ("<request>-<n>" after a
// failover rekey) or the request id itself.
func keyRefMatches(ref, requestID string) bool {
	return requestID != "" && (ref == requestID || strings.HasPrefix(ref, requestID+"-"))
}

// --- in-memory store ---------------------------------------------------------------------------

func (m *Mem) CreateAccountKey(k AccountKey, r MintKeyRules) (AccountKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.acctKeys == nil {
		m.acctKeys = map[string]AccountKey{}
	}
	var mine []AccountKey
	for _, x := range m.acctKeys {
		if x.Account == k.Account {
			mine = append(mine, x)
		}
	}
	k, err := r.admit(k, mine)
	if err != nil {
		return AccountKey{}, err
	}
	m.acctKeys[k.ID] = k
	return k, nil
}

func (m *Mem) AccountKeyByHash(hash string) (AccountKey, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.acctKeys {
		if k.SecretHash == hash {
			return k, true, nil
		}
	}
	return AccountKey{}, false, nil
}

func (m *Mem) AccountKeyByID(id string) (AccountKey, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.acctKeys[id]
	return k, ok, nil
}

func (m *Mem) AccountKeysOf(account string) ([]AccountKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AccountKey
	for _, k := range m.acctKeys {
		if k.Account == account {
			out = append(out, k)
		}
	}
	return out, nil
}

func (m *Mem) UpdateAccountKey(id string, edit func(*AccountKey)) (AccountKey, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.acctKeys[id]
	if !ok || k.Revoked {
		return AccountKey{}, false, nil
	}
	edit(&k)
	m.acctKeys[id] = k
	return k, true, nil
}

func (m *Mem) TouchAccountKey(id string, ts int64, request bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.acctKeys[id]
	if !ok {
		return nil
	}
	k.LastUsed = ts
	if request {
		k.Requests++
	}
	m.acctKeys[id] = k
	return nil
}

// KeyLimit asks HoldForKey to enforce a key's spend limit inside the hold's own transaction:
// the key's spend dated in [From, To) (To <= 0 = unbounded) plus its open reservations plus the
// new hold must stay within USD. USD <= 0 = no limit check.
type KeyLimit struct {
	USD      float64
	From, To int64
}

// MintKeyRules are the per-account rules CreateAccountKey enforces inside its own write, so
// mints racing on several instances cannot pass them. Zero values switch a rule off.
type MintKeyRules struct {
	MaxLive   int   // live (non-revoked) keys an account may hold
	IdemSince int64 // a key with the same IdemKey created at or after this (unix nanos) is a replay
}

// ErrKeyCount: the account already holds MaxLive live keys; nothing was written.
var ErrKeyCount = errors.New("store: account key count limit")

// KeyReplayError: a key was already minted under this Idempotency-Key (ID); nothing was written.
type KeyReplayError struct{ ID string }

func (e *KeyReplayError) Error() string { return "store: key already minted as " + e.ID }

// admit applies the rules to k against the account's existing keys (replay first, then the cap)
// and dates k strictly after the newest of them, so mint order never ties.
func (r MintKeyRules) admit(k AccountKey, existing []AccountKey) (AccountKey, error) {
	if k.CreatedAt == 0 {
		k.CreatedAt = time.Now().UnixNano()
	}
	live := 0
	var replay *AccountKey
	for i, x := range existing {
		if k.IdemKey != "" && r.IdemSince > 0 && x.IdemKey == k.IdemKey && x.CreatedAt >= r.IdemSince &&
			(replay == nil || x.CreatedAt < replay.CreatedAt) {
			replay = &existing[i]
		}
		if !x.Revoked {
			live++
		}
		if x.CreatedAt >= k.CreatedAt {
			k.CreatedAt = x.CreatedAt + 1
		}
	}
	if replay != nil {
		return AccountKey{}, &KeyReplayError{ID: replay.ID}
	}
	if r.MaxLive > 0 && live >= r.MaxLive {
		return AccountKey{}, ErrKeyCount
	}
	return k, nil
}

// ErrKeyLimit: the hold would take the key past its limit; no hold was placed.
var ErrKeyLimit = errors.New("store: key limit reached")

func (l KeyLimit) fits(spend, reserved, amount float64) bool {
	return spend+reserved+amount <= l.USD+1e-9
}

// HoldForKey is HoldFor whose reservation is attributed to an account key: the key's reserved
// amount is the sum of its pending holds, so the reservation is released, captured and swept on
// exactly the wallet hold's paths. keyTS dates the eventual key spend (the request's start).
func (m *Mem) HoldForKey(user, requestID string, amount float64, keyID string, keyTS int64, lim KeyLimit) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lim.USD > 0 && !lim.fits(m.keySpendLocked(keyID, lim.From, lim.To), m.keyReservedLocked(keyID, requestID), amount) {
		return false, ErrKeyLimit
	}
	if !m.holdRefLocked(user, amount, requestID) {
		return false, nil
	}
	m.pendingHolds[requestID] = pendingHold{user: user, amount: amount, placedAt: time.Now().Unix(), keyID: keyID, keyTS: keyTS}
	return true, nil
}

func (m *Mem) KeyReserved(keyID string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keyReservedLocked(keyID, ""), nil
}

// keyReservedLocked sums keyID's open holds, except the hold for skipRequest (a re-placed hold
// for the same request replaces itself). Caller holds m.mu.
func (m *Mem) keyReservedLocked(keyID, skipRequest string) float64 {
	sum := 0.0
	for rid, ph := range m.pendingHolds {
		if ph.keyID == keyID && rid != skipRequest {
			sum += ph.amount
		}
	}
	return sum
}

// KeySpend sums a key's settled spend dated in [from, to) (to <= 0 = no upper bound), net of
// chargeback/refund reversals.
func (m *Mem) KeySpend(keyID string, from, to int64) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keySpendLocked(keyID, from, to), nil
}

// keySpendLocked is KeySpend for a caller holding m.mu.
func (m *Mem) keySpendLocked(keyID string, from, to int64) float64 {
	sum := 0.0
	for _, r := range m.ledger {
		v, ok := keySpendRow(r, keyID)
		if ok && r.TS >= from && (to <= 0 || r.TS < to) {
			sum += v
		}
	}
	return sum
}

func (m *Mem) KeySpendSince(keyID string, froms []int64) ([]float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]float64, len(froms))
	for _, r := range m.ledger {
		if v, ok := keySpendRow(r, keyID); ok {
			for i, f := range froms {
				if r.TS >= f {
					out[i] += v
				}
			}
		}
	}
	return out, nil
}

func (m *Mem) AccountKeySpendRefs(account string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for _, r := range m.ledger {
		if k, ok := m.acctKeys[r.Holder]; ok && k.Account == account && r.Ref != "" {
			if _, spend := keySpendRow(r, r.Holder); spend {
				out[r.Ref] = r.Holder
			}
		}
	}
	return out, nil
}

func (m *Mem) AppendKeyEvent(wallet, ref string, ts int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendLedgerLocked(wallet, "consumer", KindKeyEvent, 0, "", StatePosted, ref, ts)
	return nil
}

// keySpendCaptureLocked records the key spend a captured hold carried. Caller holds m.mu.
func (m *Mem) keySpendCaptureLocked(ph pendingHold, ref string, cost float64) {
	if ph.keyID == "" || cost <= 0 {
		return
	}
	m.appendLedgerLocked(ph.keyID, "key", KindKeySpend, -cost, "", StatePosted, ref, ph.keyTS)
}

// keyReverseLocked writes the key-side reversal of a chargeback/refund of requestID: dated at
// the spend it reverses (so it nets out of THAT window, never freeing budget in a later one) and
// capped at what is left of that spend (a request is reversed on its key at most once in total).
// Caller holds m.mu.
func (m *Mem) keyReverseLocked(requestID string, amount float64) {
	if requestID == "" || amount <= 0 {
		return
	}
	for _, r := range m.ledger {
		if r.Kind == KindKeySpend && keyRefMatches(r.Ref, requestID) {
			left := -r.Amount
			for _, x := range m.ledger {
				if x.Kind == KindKeyReversal && x.Holder == r.Holder && x.Ref == r.Ref {
					left -= x.Amount
				}
			}
			if back := math.Min(amount, left); back > 1e-9 {
				m.appendLedgerLocked(r.Holder, "key", KindKeyReversal, back, "", StatePosted, r.Ref, r.TS)
			}
			return
		}
	}
}

// RetireAccountKeys revokes every key of a deleted account and re-keys its key_event audit
// rows to the anonymous holder, as the rest of the account is de-identified.
func (m *Mem) RetireAccountKeys(account, anon string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, k := range m.acctKeys {
		if k.Account == account {
			k.Revoked, k.OwnerPub = true, ""
			m.acctKeys[id] = k
		}
	}
	for i := range m.ledger {
		if m.ledger[i].Holder == account && m.ledger[i].Kind == KindKeyEvent {
			m.ledger[i].Holder = anon
		}
	}
	return nil
}

// holdRefLocked is holdLocked whose hold ledger row names the request it reserves for.
// Caller holds m.mu.
func (m *Mem) holdRefLocked(user string, amount float64, ref string) bool {
	if m.wallet[user] < amount {
		return false
	}
	m.wallet[user] -= amount
	m.appendLedgerLocked(user, "consumer", KindHold, -amount, "", StatePending, ref, 0)
	return true
}
