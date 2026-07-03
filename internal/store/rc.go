package store

import (
	"sync"
	"time"
)

// rc.go is the roster store for /remote-control sessions (BASE STATION, v5.0.0). It holds
// ONLY metadata — id, owner wallet, name, the link-code HASH, the host-token HASH, per-device
// attach-token HASHES, timestamps, revoked. It NEVER holds a transcript or any frame: the
// broker is a content-blind relay and the HOST owns the conversation (see
// rogerai-internal-docs/REMOTE-CONTROL-DESIGN.md, AD-2). Mirrors bandStore/grantStore: its own
// mutex so RC ops never contend with the wallet/ledger/band locks. All secrets are stored as
// sha256 hashes only (the code is shown once at enable; the host/attach tokens are bearer
// secrets shown once), exactly like Band.CodeHash.

// RCSession is one remote-control session's roster row.
type RCSession struct {
	ID            string `json:"id"`           // "rcs_<rand>" — the DB id (NOT a secret)
	OwnerWallet   string `json:"owner_wallet"` // u_gh_<id> / u_apple_<id>: CLI + web unify on the WALLET
	Name          string `json:"name"`         // "hermes · RogerAI" (auto host · cwd)
	CodeHash      string `json:"-"`            // sha256(canonical link tail); rotatable; never the code
	CodeExpires   int64  `json:"-"`            // unix; the attach window (enable + rotate set now+10m); 0 = closed
	CodeDisplay   string `json:"code_display"` // MASKED, non-recoverable ("RC 147.520 MHz · ••••-••••")
	HostTokenHash string `json:"-"`            // sha256 of the host bearer (issued once at enable)
	CreatedAt     int64  `json:"created_at"`
	LastHostSeen  int64  `json:"last_host_seen"` // unix of the host's last poll (drives the online/offline dot)
	Revoked       bool   `json:"revoked"`
}

// Active reports whether the session is live (not revoked).
func (s RCSession) Active() bool { return !s.Revoked }

// CodeOpen reports whether the link code can still be used to attach as of now (unexpired,
// non-revoked). A 0 expiry means the window is closed (rotate/enable must re-open it).
func (s RCSession) CodeOpen(now time.Time) bool {
	return !s.Revoked && s.CodeExpires != 0 && now.Unix() < s.CodeExpires
}

// RCAttachToken binds a per-device bearer (hash-only) to a session, minted when a viewer
// successfully attaches with the link code. It lives as long as the session (revoked with it).
type RCAttachToken struct {
	Hash        string `json:"-"` // sha256 of the bearer secret
	SessionID   string `json:"session_id"`
	DeviceLabel string `json:"device_label"` // "web (Chrome)" / "roger @ macbook-air" — for origin tags
	CreatedAt   int64  `json:"created_at"`
}

// RCSessionQuota is the number of ACTIVE remote-control sessions an owner may hold. Separate
// from BandQuota (=1): sessions are ephemeral and free, capped only to bound abuse.
func RCSessionQuota(owner string) int {
	_ = owner
	return 5
}

// RCCodeTTL is how long a freshly-minted/rotated link code stays attachable.
const RCCodeTTL = 10 * time.Minute

// RCHostOfflineAfter is how long since the host's last poll before it is shown offline.
const RCHostOfflineAfter = 30 * time.Second

// RCIdleGC is how long a session may sit idle (no host poll) before it is garbage-collected.
const RCIdleGC = 7 * 24 * time.Hour

// --- Mem RC storage ------------------------------------------------------

type rcStore struct {
	mu         sync.Mutex
	sessions   map[string]RCSession     // id -> session
	byCodeHash map[string]string        // code_hash -> id (constant-work attach lookup)
	attach     map[string]RCAttachToken // attach-token hash -> token
}

func newRCStore() *rcStore {
	return &rcStore{
		sessions:   map[string]RCSession{},
		byCodeHash: map[string]string{},
		attach:     map[string]RCAttachToken{},
	}
}

func (m *Mem) CreateRCSession(s RCSession) error {
	m.rc.mu.Lock()
	defer m.rc.mu.Unlock()
	if s.CreatedAt == 0 {
		s.CreatedAt = time.Now().Unix()
	}
	m.rc.sessions[s.ID] = s
	if s.CodeHash != "" {
		m.rc.byCodeHash[s.CodeHash] = s.ID
	}
	return nil
}

func (m *Mem) RCSessionByID(id string) (RCSession, bool, error) {
	m.rc.mu.Lock()
	defer m.rc.mu.Unlock()
	s, ok := m.rc.sessions[id]
	return s, ok, nil
}

func (m *Mem) RCSessionByCodeHash(hash string) (RCSession, bool, error) {
	m.rc.mu.Lock()
	defer m.rc.mu.Unlock()
	id, ok := m.rc.byCodeHash[hash]
	if !ok {
		return RCSession{}, false, nil
	}
	s, ok := m.rc.sessions[id]
	return s, ok, nil
}

func (m *Mem) RCSessionsByOwner(wallet string) ([]RCSession, error) {
	m.rc.mu.Lock()
	defer m.rc.mu.Unlock()
	var out []RCSession
	for _, s := range m.rc.sessions {
		if s.OwnerWallet == wallet {
			out = append(out, s)
		}
	}
	return out, nil
}

// UpdateRCSession rewrites a session row (rotate code / revoke / touch last-seen). It keeps
// the byCodeHash index consistent when the code hash changes (rotation): the OLD hash is
// dropped so a rotated-away code can never resolve again.
func (m *Mem) UpdateRCSession(s RCSession) error {
	m.rc.mu.Lock()
	defer m.rc.mu.Unlock()
	old, ok := m.rc.sessions[s.ID]
	if ok && old.CodeHash != "" && old.CodeHash != s.CodeHash {
		delete(m.rc.byCodeHash, old.CodeHash)
	}
	m.rc.sessions[s.ID] = s
	if s.CodeHash != "" {
		m.rc.byCodeHash[s.CodeHash] = s.ID
	}
	return nil
}

func (m *Mem) PutRCAttachToken(t RCAttachToken) error {
	m.rc.mu.Lock()
	defer m.rc.mu.Unlock()
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().Unix()
	}
	m.rc.attach[t.Hash] = t
	return nil
}

func (m *Mem) RCAttachTokenByHash(hash string) (RCAttachToken, bool, error) {
	m.rc.mu.Lock()
	defer m.rc.mu.Unlock()
	t, ok := m.rc.attach[hash]
	return t, ok, nil
}

// RevokeRCSessions marks every one of an owner's sessions revoked and drops their attach
// tokens + code-hash lookups (revoke-all / account-delete). Returns how many it revoked.
func (m *Mem) RevokeRCSessions(wallet string) (int, error) {
	m.rc.mu.Lock()
	defer m.rc.mu.Unlock()
	n := 0
	revoked := map[string]bool{}
	for id, s := range m.rc.sessions {
		if s.OwnerWallet != wallet || s.Revoked {
			continue
		}
		s.Revoked = true
		s.CodeExpires = 0
		if s.CodeHash != "" {
			delete(m.rc.byCodeHash, s.CodeHash)
		}
		m.rc.sessions[id] = s
		revoked[id] = true
		n++
	}
	for h, t := range m.rc.attach {
		if revoked[t.SessionID] {
			delete(m.rc.attach, h)
		}
	}
	return n, nil
}

// PruneRCSessions hard-deletes an owner's revoked sessions and any idle since before idleCutoff
// (unix), cleaning the code-hash + attach indexes. Live/recently-offline rows are kept.
func (m *Mem) PruneRCSessions(wallet string, idleCutoff int64) (int, error) {
	m.rc.mu.Lock()
	defer m.rc.mu.Unlock()
	dead := map[string]bool{}
	for id, s := range m.rc.sessions {
		if s.OwnerWallet != wallet {
			continue
		}
		if s.Revoked || s.LastHostSeen < idleCutoff {
			if s.CodeHash != "" {
				delete(m.rc.byCodeHash, s.CodeHash)
			}
			delete(m.rc.sessions, id)
			dead[id] = true
		}
	}
	for h, t := range m.rc.attach {
		if dead[t.SessionID] {
			delete(m.rc.attach, h)
		}
	}
	return len(dead), nil
}
