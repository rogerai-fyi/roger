package tower

// join.go is the bearer invite (features/join/invite.feature): the owner mints an invite with no
// client named, the joiner proves it holds the invite's code (internal/joincode, the Tower
// domain) without sending it, and the Tower admits the presenting key at that moment. A client
// invite admits a client; a Station invite attaches a Station with the models it reports.
//
// It keeps the bootstrap flow's rules: only a verifier is stored, never the code; every refusal
// is the same error; a wrong guess spends the global budget; a right code under the wrong name or
// for the wrong kind does not spend the invite.

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towerpin"
)

// JoinKind is what a bearer invite lets its holder do.
type JoinKind string

const (
	JoinClient  JoinKind = "client"
	JoinStation JoinKind = "station"
)

// ErrWrongInviteKind is a right code presented for the wrong kind: the invite is not spent, and
// the joiner is told which kind it holds (the code was right, so this is no oracle).
var ErrWrongInviteKind = errors.New("this invite is for the other kind of join")

// JoinRequest is one presentation of an invite's code.
type JoinRequest struct {
	Kind    JoinKind // what the joiner is joining as; empty means a client
	Proof   string   // joincode.Tower.Proof(code, Nonce, PubHex, the Tower's fingerprint)
	Nonce   string
	PubHex  string // the joiner's public key, hex
	KeyHash string // protocol.UserIDFromPubkey(PubHex): what admission records
	Name    string // the joiner's name (a Station's id; a named invite checks it)
	Models  []string
}

// JoinResult is what a successful join produced.
type JoinResult struct {
	Kind          JoinKind
	Credential    Credential // a client's admission
	Station       Station    // a Station's attachment
	Refused       []string   // a Station's models the invite does not allow
	AlreadyMember bool       // the key was already admitted; nothing was spent
}

// JoinFingerprint is this Tower's identity fingerprint: SHA-256 of its TLS public key, 64 hex.
// An invite carries it, a joiner pins it, and every proof binds it.
func (s *State) JoinFingerprint() (string, error) {
	k, err := s.TLSKey()
	if err != nil {
		return "", err
	}
	return towerpin.Fingerprint(k.Public().(ed25519.PublicKey)), nil
}

// CreateJoinInvitation mints a bearer invite of kind. forName limits it to a joiner of that name;
// models limits what a Station joining with it may serve. The code is returned exactly once.
func (s *State) CreateJoinInvitation(kind JoinKind, forName string, models []string, validFor time.Duration) (Invitation, string, error) {
	if s.Mode != ModeStandalone {
		return Invitation{}, "", ErrNotStandalone
	}
	if kind != JoinClient && kind != JoinStation {
		return Invitation{}, "", errors.New("an invite is for a client or a station")
	}
	if validFor <= 0 {
		return Invitation{}, "", errors.New("an invitation needs a positive validity period")
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return Invitation{}, "", err
	}
	id, err := randomHex(8)
	if err != nil {
		return Invitation{}, "", err
	}
	code := joincode.Tower.NewCode()
	inv := &Invitation{
		ID:           id,
		ExpiresAt:    time.Now().Add(validFor).UnixNano(),
		Budget:       1,
		Bearer:       true,
		Kind:         kind,
		For:          forName,
		Models:       append([]string(nil), models...),
		JoinVerifier: hex.EncodeToString(joincode.Tower.Verifier(code)),
	}
	bs.Invitations[id] = inv
	if err := s.saveBootstrap(bs); err != nil {
		return Invitation{}, "", err
	}
	return *inv, code, nil
}

// LiveInvitations lists the bearer invites that are still usable, without their verifiers.
func (s *State) LiveInvitations() []Invitation {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return nil
	}
	now := time.Now().UnixNano()
	var out []Invitation
	for _, inv := range bs.Invitations {
		if inv.Bearer && !inv.Consumed && !inv.Voided && now <= inv.ExpiresAt {
			c := *inv
			c.JoinVerifier = ""
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Invitation) int { return int(a.ExpiresAt - b.ExpiresAt) })
	return out
}

// JoinedLine is how the owner is told, as it happens, who joined and how to undo it
// (features/join/invite.feature, "The owner sees the join as it happens"). The Tower's log, a
// waiting invite, the TUI and the console all say it the same way.
func JoinedLine(name, keyHash string, kind JoinKind) string {
	short := keyHash
	if len(short) > 6 {
		short = short[:4] + "..." + short[len(short)-2:]
	}
	if name == "" {
		name = short
	}
	if kind == JoinStation {
		return fmt.Sprintf("station %s joined (key %s)", name, short)
	}
	return fmt.Sprintf("%s joined (key %s); undo with: roger tower revoke-client %s", name, short, name)
}

// VoidInvitation stops an invite from ever working.
func (s *State) VoidInvitation(id string) error {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return err
	}
	inv, ok := bs.Invitations[id]
	if !ok || inv.Consumed || inv.Voided || !inv.Bearer {
		return errors.New("no outstanding invite has that id")
	}
	inv.Voided = true
	return s.saveBootstrap(bs)
}

// ConsumeJoin admits (or attaches) the joiner whose proof matches a live bearer invite.
func (s *State) ConsumeJoin(r JoinRequest) (JoinResult, error) {
	if s.Mode != ModeStandalone {
		return JoinResult{}, ErrNotStandalone
	}
	if r.Kind == "" {
		r.Kind = JoinClient
	}
	if r.PubHex == "" || r.KeyHash != protocol.UserIDFromPubkey(r.PubHex) {
		return JoinResult{}, errBootstrapRejected
	}
	fp, err := s.JoinFingerprint()
	if err != nil {
		return JoinResult{}, errBootstrapRejected
	}
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil {
		return JoinResult{}, errBootstrapRejected
	}
	now := time.Now()
	if bs.GlobalSince == 0 || now.Sub(time.Unix(bs.GlobalSince, 0)) > globalAttemptWindow {
		bs.GlobalAttempt, bs.GlobalSince = 0, now.Unix()
	}
	if bs.GlobalAttempt >= globalAttemptBudget {
		return JoinResult{}, errBootstrapRejected
	}
	var match *Invitation
	for _, inv := range bs.Invitations {
		if !inv.Bearer || inv.Consumed || inv.Voided || now.UnixNano() > inv.ExpiresAt {
			continue
		}
		ver, derr := hex.DecodeString(inv.JoinVerifier)
		if derr != nil {
			continue
		}
		if joincode.Tower.CheckWithVerifier(ver, r.Proof, r.Nonce, r.PubHex, fp) == nil {
			match = inv
			break
		}
	}
	if match == nil {
		// A wrong guess is not free: it spends the global budget, durably.
		bs.GlobalAttempt++
		_ = s.saveBootstrap(bs)
		return JoinResult{}, errBootstrapRejected
	}
	// The code is right. Name and kind are checked next; a mismatch does not spend the invite.
	if match.For != "" && match.For != r.Name {
		return JoinResult{}, errBootstrapRejected
	}
	if match.Kind != r.Kind {
		return JoinResult{Kind: match.Kind}, ErrWrongInviteKind
	}
	if r.Kind == JoinClient {
		if clientAdmitted(bs, r.KeyHash) {
			return JoinResult{Kind: JoinClient, AlreadyMember: true}, nil
		}
		cred := admitLocked(bs, s, r.KeyHash)
		if cred == nil {
			return JoinResult{}, errBootstrapRejected
		}
		cred.Name = r.Name
		match.Consumed, match.ClientKeyHash, match.JoinedName = true, r.KeyHash, r.Name
		if err := s.saveBootstrap(bs); err != nil {
			return JoinResult{}, errBootstrapRejected
		}
		return JoinResult{Kind: JoinClient, Credential: *cred}, nil
	}
	allowed, refused := r.Models, []string(nil)
	if len(match.Models) > 0 {
		allowed = nil
		for _, m := range r.Models {
			if slices.Contains(match.Models, m) {
				allowed = append(allowed, m)
			} else {
				refused = append(refused, m)
			}
		}
	}
	st, err := s.attachLocked(bs, r.Name, r.KeyHash, allowed, "")
	if err != nil {
		return JoinResult{}, err
	}
	match.Consumed, match.ClientKeyHash, match.JoinedName = true, r.KeyHash, r.Name
	if err := s.saveBootstrap(bs); err != nil {
		return JoinResult{}, errBootstrapRejected
	}
	return JoinResult{Kind: JoinStation, Station: st, Refused: refused}, nil
}

// admitLocked admits keyHash into bs (the caller holds bootstrapMu and saves). It returns nil
// when the network no longer admits anyone (its operator was retired).
func admitLocked(bs *Snapshot, s *State, keyHash string) *Credential {
	if bs.Operator == nil && bs.Bootstrapped {
		return nil
	}
	role := RoleLocalClient
	if bs.Operator == nil {
		role = RoleLocalOperator
	}
	fp, err := s.rootFingerprint()
	if err != nil {
		return nil
	}
	cred := &Credential{ClientKeyHash: keyHash, NetworkID: s.LocalNetworkID, RootFingerprint: fp, Role: role, IssuedAt: time.Now().Unix()}
	if bs.Clients == nil {
		bs.Clients = map[string]*Credential{}
		if bs.Operator != nil {
			bs.Clients[bs.Operator.ClientKeyHash] = bs.Operator
		}
	}
	bs.Clients[keyHash] = cred
	bs.Bootstrapped = true
	if bs.Operator == nil {
		bs.Operator = cred
	}
	return cred
}

// ClientName is the name an admitted client joined under, or "" when it gave none (or is unknown).
func (s *State) ClientName(keyHash string) string {
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()
	bs, err := s.loadBootstrap()
	if err != nil || bs.Clients == nil {
		return ""
	}
	if c, ok := bs.Clients[keyHash]; ok {
		return c.Name
	}
	return ""
}

// RotateTLSKey replaces the Tower's TLS key (features/join/tower_identity.feature, a reset Tower):
// the Tower then serves under a new fingerprint, and every outstanding invite, which names the old
// one, stops working. Clients and Stations pinned to the old key are refused until they re-join.
// The identity key is untouched. It returns the new fingerprint and how many invites it voided.
func (s *State) RotateTLSKey() (fingerprint string, voided int, err error) {
	if s.Mode != ModeStandalone {
		return "", 0, ErrNotStandalone
	}
	tmp := filepath.Join(s.dir, tlsKey+".new")
	if _, err := newKeyFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, tlsKey)); err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	bootstrapMu.Lock()
	bs, err := s.loadBootstrap()
	if err == nil {
		now := time.Now().UnixNano()
		for _, inv := range bs.Invitations {
			if inv.Bearer && !inv.Consumed && !inv.Voided && now <= inv.ExpiresAt {
				inv.Voided = true
				voided++
			}
		}
		if voided > 0 {
			err = s.saveBootstrap(bs)
		}
	}
	bootstrapMu.Unlock()
	if err != nil {
		return "", voided, err
	}
	fingerprint, err = s.JoinFingerprint()
	return fingerprint, voided, err
}

// ErrUnknownClient is a name or key hash no admitted client has.
var ErrUnknownClient = errors.New("no admitted client has that name or key")

// RevokeClientNamed revokes the admitted client with this name (the one it joined under) or key
// hash, and returns its key hash. Unlike RevokeClient it refuses a name it does not know, so a
// typo never reads as success.
func (s *State) RevokeClientNamed(nameOrHash string) (string, error) {
	bootstrapMu.Lock()
	bs, err := s.loadBootstrap()
	bootstrapMu.Unlock()
	if err != nil {
		return "", err
	}
	target := ""
	if clientAdmitted(bs, nameOrHash) {
		target = nameOrHash
	}
	for kh, c := range bs.Clients {
		if target == "" && c.Name != "" && c.Name == nameOrHash {
			target = kh
		}
	}
	if target == "" {
		return "", ErrUnknownClient
	}
	return target, s.RevokeClient(target)
}
