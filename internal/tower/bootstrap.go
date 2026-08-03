package tower

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The bootstrap flow is how the FIRST local client of a standalone network becomes its
// operator. It is the one moment the network hands out authority, so it is deliberately
// unforgiving.
//
// Design notes worth keeping in view:
//
//   - The plaintext code is shown ONCE and never persisted. Only an HMAC verifier is
//     stored, so reading the data directory is not equivalent to holding the invitation.
//   - Every rejection returns the SAME error. A caller must not learn whether the
//     invitation id, the code, or the client binding was the part that was wrong.
//   - The attempt budget is consumed BEFORE the code is compared, and persists across
//     restarts, so guessing costs the attacker something even if they restart the process.
//   - A wrong BINDING (right code, wrong client) fails without consuming the invitation:
//     an attacker who learns a code must not be able to burn it out of spite.
const (
	// bootstrapEntropyBytes is 16 bytes = 128 bits from the OS CSPRNG.
	bootstrapEntropyBytes = 16
	// globalAttemptBudget bounds anonymous probing of invitation IDs across the whole
	// Tower, so an unknown-ID guess is not free.
	globalAttemptBudget = 50
	// globalAttemptWindow is how long the probe budget takes to decay.
	globalAttemptWindow = time.Hour
	bootstrapFile       = "bootstrap.json"
)

// RoleLocalOperator is the single administrative role a standalone network issues at
// bootstrap. v1 has exactly one, for its whole life.
const RoleLocalOperator = "local_operator"

// errBootstrapRejected is the ONLY error consumption returns. Uniform by design: a
// distinguishable error is an oracle.
var errBootstrapRejected = errors.New("bootstrap rejected")

// ErrNotStandalone is returned when a local-admission operation is attempted on a joined
// Tower, whose clients are admitted by Roger Core instead.
var ErrNotStandalone = errors.New("local bootstrap exists only in standalone mode")

// Invitation is the durable record of a bootstrap code. It holds a VERIFIER, never the
// code: `Verifier` is HMAC-SHA-256 over the plaintext under a per-Tower secret.
type Invitation struct {
	ID       string `json:"id"`
	Verifier string `json:"verifier"`
	// ExpiresAt is UnixNANO. Second granularity silently truncated any window shorter
	// than a second to "already now", which made short-lived invitations meaningless.
	ExpiresAt int64  `json:"expires_at"`
	Budget    int    `json:"budget"`
	Attempts  int    `json:"attempts"`
	Consumed  bool   `json:"consumed"`
	Role      string `json:"role"`
	// ClientKeyHash binds the invitation to the client that requested it. A correct
	// code presented by a DIFFERENT client is refused without consuming the
	// invitation, so learning a code does not let an attacker burn it.
	ClientKeyHash string `json:"client_key_hash"`
}

// String renders the invitation for display. It exists so that printing a record can
// never accidentally re-expose a code - there is nothing secret in it to print.
func (i Invitation) String() string {
	state := "open"
	switch {
	case i.Consumed:
		state = "consumed"
	case i.Attempts >= i.Budget:
		state = "locked"
	case time.Now().UnixNano() > i.ExpiresAt:
		state = "expired"
	}
	return fmt.Sprintf("invitation %s role=%s state=%s attempts=%d/%d", i.ID, i.Role, state, i.Attempts, i.Budget)
}

// Credential is what a consumed invitation issues: a scoped local client credential
// pinned to the network, the offline root, and the client's own key.
type Credential struct {
	ClientKeyHash   string `json:"client_key_hash"`
	NetworkID       string `json:"network_id"`
	RootFingerprint string `json:"root_fingerprint"`
	Role            string `json:"role"`
	IssuedAt        int64  `json:"issued_at"`
}

// bootstrapState is the durable local-admission state. It is written atomically so a
// crash can never leave both a reusable code and an issued credential.
type bootstrapState struct {
	HMACKey       string                 `json:"hmac_key"`
	Invitations   map[string]*Invitation `json:"invitations"`
	GlobalAttempt int                    `json:"global_attempts"`
	// GlobalSince bounds the probe counter to a window. Without it the limiter is not a
	// limiter but a brick: enough bogus ids and the network can never be bootstrapped.
	GlobalSince int64               `json:"global_attempts_since,omitempty"`
	Operator    *Credential         `json:"operator,omitempty"`
	Stations    map[string]*Station `json:"stations,omitempty"`
}

// bootstrapMu serialises local-admission transitions within a process. Cross-process
// exclusion is the identity-directory lock; this guards the read-modify-write so two
// goroutines cannot both consume one invitation.
var bootstrapMu sync.Mutex

func (s *State) bootstrapPath() string { return filepath.Join(s.dir, bootstrapFile) }

func (s *State) loadBootstrap() (*bootstrapState, error) {
	b, err := os.ReadFile(s.bootstrapPath())
	if os.IsNotExist(err) {
		key, kerr := randomHex(32)
		if kerr != nil {
			return nil, kerr
		}
		return &bootstrapState{HMACKey: key, Invitations: map[string]*Invitation{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var bs bootstrapState
	if err := json.Unmarshal(b, &bs); err != nil {
		return nil, err
	}
	if bs.Invitations == nil {
		bs.Invitations = map[string]*Invitation{}
	}
	return &bs, nil
}

// saveBootstrap writes the whole admission state atomically: a temp file beside the
// target, then a rename.
//
// This is what makes "a crash cannot leave both a reusable code and an issued
// credential" true rather than aspirational, and it holds for a simple reason - the
// consumed flag and the issued operator live in the SAME file. A rename either lands or
// it does not, so the two facts persist together or not at all. A crash before the
// rename leaves the prior state, in which the code is unused and no credential exists:
// consistent, and safe in the direction that matters.
func (s *State) saveBootstrap(bs *bootstrapState) error {
	b, err := json.MarshalIndent(bs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.bootstrapPath() + ".tmp"
	// os.WriteFile with keyPerm; the file holds the HMAC verifier secret.
	if err := os.WriteFile(tmp, b, keyPerm); err != nil {
		return err
	}
	return os.Rename(tmp, s.bootstrapPath())
}

// CreateInvitation mints a one-time bootstrap code. The plaintext is returned exactly
// once, to be shown through a local trusted channel; it is never stored, logged, or
// retrievable afterwards.
func (s *State) CreateInvitation(clientKeyHash string, validFor time.Duration, budget int) (Invitation, string, error) {
	if s.Mode != ModeStandalone {
		return Invitation{}, "", ErrNotStandalone
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	if clientKeyHash == "" {
		return Invitation{}, "", errors.New("an invitation must be bound to the requesting client's public-key hash")
	}
	// Floor these rather than minting an invitation that is born locked (budget 0) or
	// born expired (ttl <= 0) - both would fail later with the uniform rejection, which
	// tells the operator nothing about what they did wrong.
	if budget <= 0 {
		return Invitation{}, "", errors.New("an invitation needs a positive attempt budget")
	}
	if validFor <= 0 {
		return Invitation{}, "", errors.New("an invitation needs a positive validity period")
	}
	bs, err := s.loadBootstrap()
	if err != nil {
		return Invitation{}, "", err
	}

	raw := make([]byte, bootstrapEntropyBytes)
	if _, err := rand.Read(raw); err != nil {
		return Invitation{}, "", err
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)

	id, err := randomHex(8)
	if err != nil {
		return Invitation{}, "", err
	}
	inv := &Invitation{
		ID:            id,
		Verifier:      verifierFor(bs.HMACKey, code),
		ExpiresAt:     time.Now().Add(validFor).UnixNano(),
		Budget:        budget,
		Role:          RoleLocalOperator,
		ClientKeyHash: clientKeyHash,
	}
	bs.Invitations[id] = inv
	if err := s.saveBootstrap(bs); err != nil {
		return Invitation{}, "", err
	}
	return *inv, code, nil
}

// Invitation returns the durable record, which by construction contains no code.
func (s *State) Invitation(id string) (Invitation, error) {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return Invitation{}, err
	}
	inv, ok := bs.Invitations[id]
	if !ok {
		return Invitation{}, errBootstrapRejected
	}
	out := *inv
	out.Verifier = "" // the verifier is secret; String() is careful not to show it either
	return out, nil
}

// ConsumeInvitation admits a client. It returns the same error for every failure.
//
// Order matters: the attempt budget is claimed durably BEFORE the verifier is compared,
// so a guess costs the attacker a budget slot whether or not it was close. A wrong
// client binding fails AFTER the code matches but does NOT mark the invitation
// consumed, so learning a code does not let an attacker burn it.
func (s *State) ConsumeInvitation(id, code, clientKeyHash string) (Credential, error) {
	if s.Mode != ModeStandalone {
		return Credential{}, ErrNotStandalone
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

	bs, err := s.loadBootstrap()
	if err != nil {
		return Credential{}, errBootstrapRejected
	}

	// Global anonymous-attempt limiter: probing unknown ids is not free, but the budget
	// decays so a burst of probes cannot permanently brick the network.
	now := time.Now()
	if bs.GlobalSince == 0 || now.Sub(time.Unix(bs.GlobalSince, 0)) > globalAttemptWindow {
		bs.GlobalAttempt, bs.GlobalSince = 0, now.Unix()
	}
	if bs.GlobalAttempt >= globalAttemptBudget {
		return Credential{}, errBootstrapRejected
	}

	inv, known := bs.Invitations[id]
	if !known {
		bs.GlobalAttempt++
		_ = s.saveBootstrap(bs)
		return Credential{}, errBootstrapRejected
	}

	// Claim the per-invitation budget durably before any comparison.
	if inv.Consumed || inv.Attempts >= inv.Budget || time.Now().UnixNano() > inv.ExpiresAt {
		return Credential{}, errBootstrapRejected
	}
	inv.Attempts++
	if err := s.saveBootstrap(bs); err != nil {
		return Credential{}, errBootstrapRejected
	}

	if !hmac.Equal([]byte(verifierFor(bs.HMACKey, code)), []byte(inv.Verifier)) {
		return Credential{}, errBootstrapRejected
	}

	// The code is right. Binding checks come next, and a mismatch must NOT consume the
	// invitation - only the attempt is spent, so an attacker who learns a code cannot
	// burn it by presenting it under the wrong identity.
	if clientKeyHash == "" || !hmac.Equal([]byte(clientKeyHash), []byte(inv.ClientKeyHash)) {
		return Credential{}, errBootstrapRejected
	}
	// v1 admits exactly one local operator, for the life of the network.
	if bs.Operator != nil {
		return Credential{}, errBootstrapRejected
	}

	fp, err := s.rootFingerprint()
	if err != nil {
		return Credential{}, errBootstrapRejected
	}
	cred := Credential{
		ClientKeyHash:   clientKeyHash,
		NetworkID:       s.LocalNetworkID,
		RootFingerprint: fp,
		Role:            inv.Role,
		IssuedAt:        time.Now().Unix(),
	}
	// One atomic write marks the invitation consumed AND installs the operator, so a
	// crash cannot leave a reusable code beside an issued credential.
	inv.Consumed = true
	bs.Operator = &cred
	if err := s.saveBootstrap(bs); err != nil {
		return Credential{}, errBootstrapRejected
	}
	return cred, nil
}

// LocalOperator returns the network's single local operator credential.
func (s *State) LocalOperator() (Credential, error) {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return Credential{}, err
	}
	if bs.Operator == nil {
		return Credential{}, errors.New("this standalone network has no local operator yet: consume a bootstrap invitation first")
	}
	return *bs.Operator, nil
}

// rootFingerprint is the pinned offline-root fingerprint an admitted client stores, so a
// later reconnect can reject a different root. It returns an error rather than "" - a
// credential issued with an empty fingerprint would silently pin nothing, which is worse
// than refusing to issue one.
func (s *State) rootFingerprint() (string, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, offlineRoot))
	if err != nil {
		return "", fmt.Errorf("cannot read this network's offline root: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16]), nil
}

func verifierFor(key, code string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(code))
	return hex.EncodeToString(m.Sum(nil))
}
