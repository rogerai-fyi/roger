package tower

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// RoleLocalOperator is the administrative role, held by the FIRST admitted client - the one
// that may admit and revoke others. RoleLocalClient is every subsequent admitted client: it
// may route, but holds no admin authority. The role is assigned at CONSUME time, not minted
// into the invitation, so an invitation cannot pre-decide who becomes the admin.
const (
	RoleLocalOperator = "local_operator"
	RoleLocalClient   = "local_client"
)

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

// bootstrapMu serialises local-admission transitions within a process. Cross-process
// exclusion is the identity-directory lock; this guards the read-modify-write so two
// goroutines cannot both consume one invitation.
var bootstrapMu sync.Mutex

// store is the persistence seam. A State opened from a data directory uses the file
// store; a durable deployment can supply a database-backed one without this package
// gaining the ability to dial anything.
func (s *State) store() Store {
	if s.st != nil {
		return s.st
	}
	return NewFileStore(s.dir)
}

func (s *State) loadBootstrap() (*Snapshot, error) { return s.store().Load() }

func (s *State) saveBootstrap(bs *Snapshot) error {
	_, err := s.store().Save(bs)
	return err
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
		ID:        id,
		Verifier:  verifierFor(bs.HMACKey, code),
		ExpiresAt: time.Now().Add(validFor).UnixNano(),
		Budget:    budget,
		// Role is NOT decided here: an invitation cannot pre-appoint an admin. The role is
		// assigned at consume time from whether an operator already exists.
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
	// A client already admitted cannot be admitted a second time - the uniform rejection,
	// so a probe cannot tell "already admitted" apart from any other refusal.
	if clientAdmitted(bs, clientKeyHash) {
		return Credential{}, errBootstrapRejected
	}
	// Once the operator has been RETIRED (revoked), the network admits no new client until it
	// is re-initialized - otherwise an outstanding invitation would become a silent path to
	// re-appoint an admin. The one nil-operator case that still admits is a FRESH network,
	// one that has never bootstrapped; Bootstrapped tells the two apart (an empty Clients map
	// does not persist, so it cannot).
	if bs.Operator == nil && bs.Bootstrapped {
		return Credential{}, errBootstrapRejected
	}

	// Role is decided HERE, not by the invitation: the first admitted client is the operator
	// (the admin), every subsequent one a plain local client with no admin authority.
	role := RoleLocalClient
	if bs.Operator == nil {
		role = RoleLocalOperator
	}

	fp, err := s.rootFingerprint()
	if err != nil {
		return Credential{}, errBootstrapRejected
	}
	cred := Credential{
		ClientKeyHash:   clientKeyHash,
		NetworkID:       s.LocalNetworkID,
		RootFingerprint: fp,
		Role:            role,
		IssuedAt:        time.Now().Unix(),
	}
	// One atomic write marks the invitation consumed AND admits the client, so a crash
	// cannot leave a reusable code beside an issued credential. The FIRST admitted client is
	// also recorded as the operator (the admin role); every client, operator included, lives
	// in the Clients set that admission and routing check.
	inv.Consumed = true
	if bs.Clients == nil {
		bs.Clients = map[string]*Credential{}
		// Migrate a pre-multi-client operator into the set on first multi-client write, so the
		// map is the single source of truth from here and no client lives only in Operator.
		if bs.Operator != nil {
			bs.Clients[bs.Operator.ClientKeyHash] = bs.Operator
		}
	}
	bs.Clients[clientKeyHash] = &cred
	bs.Bootstrapped = true // never cleared: marks that this network has admitted a client
	if bs.Operator == nil {
		bs.Operator = &cred
	}
	if err := s.saveBootstrap(bs); err != nil {
		return Credential{}, errBootstrapRejected
	}
	return cred, nil
}

// clientAdmitted reports whether a client-key hash is in the admitted set. It counts the
// operator as an implicit member so a pre-multi-client snapshot (Operator set, Clients nil)
// still recognizes its one admitted client without a migration write.
func clientAdmitted(bs *Snapshot, clientKeyHash string) bool {
	if clientKeyHash == "" {
		return false
	}
	if _, ok := bs.Clients[clientKeyHash]; ok {
		return true
	}
	return bs.Operator != nil && hmac.Equal([]byte(bs.Operator.ClientKeyHash), []byte(clientKeyHash))
}

// IsAdmitted reports whether a client-key hash may use this standalone network. It is the
// one admission question the consumer plane's authentication asks.
func (s *State) IsAdmitted(clientKeyHash string) bool {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return false
	}
	return clientAdmitted(bs, clientKeyHash)
}

// AdmittedClients lists every admitted client credential, the operator included.
func (s *State) AdmittedClients() []Credential {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return nil
	}
	out := make([]Credential, 0, len(bs.Clients)+1)
	seen := map[string]bool{}
	for _, c := range bs.Clients {
		out = append(out, *c)
		seen[c.ClientKeyHash] = true
	}
	// A pre-multi-client snapshot records its one client only as Operator.
	if bs.Operator != nil && !seen[bs.Operator.ClientKeyHash] {
		out = append(out, *bs.Operator)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClientKeyHash < out[j].ClientKeyHash })
	return out
}

// RevokeClient cuts off one admitted client and only that client. The client's invitation
// stays consumed (a revoke is not a re-admit path), so it cannot be replayed to get back in.
// Revoking an unknown client is a harmless no-op. Revoking the operator clears the operator
// role too; a network with no operator can still serve its remaining clients, but admits no
// new one until re-initialized - the deliberate cost of retiring the admin credential.
func (s *State) RevokeClient(clientKeyHash string) error {
	if s.Mode != ModeStandalone {
		return ErrNotStandalone
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return err
	}
	_, inClients := bs.Clients[clientKeyHash]
	isOperator := bs.Operator != nil && hmac.Equal([]byte(bs.Operator.ClientKeyHash), []byte(clientKeyHash))
	if !inClients && !isOperator {
		return nil // never admitted: nothing to revoke
	}
	// A network that has something to revoke was bootstrapped. Assert it durably: a LEGACY
	// snapshot (Clients nil, Bootstrapped false) revoked down to nothing would otherwise look
	// fresh, and an outstanding invitation could then silently re-appoint an operator.
	bs.Bootstrapped = true
	delete(bs.Clients, clientKeyHash)
	if isOperator {
		// Retiring the operator's credential leaves the network with no admin (and admits no
		// new client until re-init), but its remaining clients keep serving.
		bs.Operator = nil
	}
	// Kill any OPEN invitation still bound to this key: a revoke that left an unused code
	// alive would be a re-admit path (the revoked client consumes it and walks back in). A
	// consumed invitation is already dead and is left as-is. Marking Consumed both closes the
	// code and records that this identity's admission was deliberately ended.
	for _, inv := range bs.Invitations {
		if !inv.Consumed && hmac.Equal([]byte(inv.ClientKeyHash), []byte(clientKeyHash)) {
			inv.Consumed = true
		}
	}
	return s.saveBootstrap(bs)
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
