package edgeauth

// codestore.go keeps the authority's live enrollment codes (features/edge/enrollment_root_trust
// .feature, sections 2, 6 and 7). A code is minted for ONE join - an allowed user key, or an
// adopted node id - and is attached as a root proof to the one answer that issues for that join,
// then spent in the same locked step. Minting again for the same join voids the previous code; a
// code older than CodeLifetime vouches for nothing and is pruned.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// enrollCode is one live code and the join it vouches for.
type enrollCode struct {
	Join string `json:"join"` // "key:<user key hex>", "node:<node id>" or "bearer:<random>"
	Code string `json:"code"` // canonical
	At   int64  `json:"at"`   // minted, unix seconds, on this authority's clock
	// For binds a bearer code to ONE node id (the id a nearby device advertised, derived from its node
	// key): only a request presenting that node is admitted with it (empty_edge_simple.feature §7).
	For string `json:"for,omitempty"`
	// Fails counts wrong guesses at a bearer code (bearer.go); a bearer code is void at the budget.
	Fails int `json:"fails,omitempty"`
	// SpentBy is the user key a bearer code admitted. A spent code admits nobody else, but it still
	// proves a retried answer to the key that spent it until it expires, so a reply lost on the way
	// back does not strand the device with a certificate it cannot verify (bearer.go).
	SpentBy string `json:"spent_by,omitempty"`
	// SpentAt is when it was spent (unix seconds): the code a key spent LAST is the one its retry is
	// checked against for free (bearer.go, bearerFind).
	SpentAt int64 `json:"spent_at,omitempty"`
}

func keyJoin(userKeyHex string) string { return "key:" + userKeyHex }
func nodeJoin(nodeID string) string    { return "node:" + nodeID }

// SetNow sets the clock this authority measures requests and codes against. Production keeps
// time.Now; a test sets it to say "a code minted sixteen minutes ago".
func (l *Local) SetNow(now func() time.Time) { l.iss.cfg.Now = now }

func (l *Local) now() time.Time { return l.iss.cfg.Now() }

// readCodes returns the live codes, dropping any that have expired. Unlocked; callers hold l.mu.
func (l *Local) readCodes() ([]enrollCode, error) {
	var got []enrollCode
	if err := l.readJSON(CodesFile, &got); err != nil {
		return nil, fmt.Errorf("the enrollment code store could not be read: %w", err)
	}
	now := l.now()
	live := got[:0]
	for _, c := range got {
		if c.At > 0 && now.Sub(time.Unix(c.At, 0)) <= CodeLifetime {
			live = append(live, c)
		}
	}
	return live, nil
}

func without(codes []enrollCode, join string) []enrollCode {
	out := codes[:0]
	for _, c := range codes {
		if c.Join != join {
			out = append(out, c)
		}
	}
	return out
}

func (l *Local) mint(join string) (string, error) {
	if strings.TrimSpace(strings.SplitN(join, ":", 2)[1]) == "" {
		return "", errors.New("an enrollment code needs a machine or a device to vouch for")
	}
	code := NewCode()
	err := l.withLock(func() error {
		got, err := l.readCodes()
		if err != nil {
			return err
		}
		return l.writeJSON(CodesFile, append(without(got, join), enrollCode{Join: join, Code: code, At: l.now().Unix()}))
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// MintKeyCode mints the code for a machine the owner allowed by its user key, voiding any earlier
// one for that key.
func (l *Local) MintKeyCode(userKeyHex string) (string, error) { return l.mint(keyJoin(userKeyHex)) }

// MintNodeCode mints the code for a device the owner adopted or re-admitted, voiding any earlier
// one for that node.
func (l *Local) MintNodeCode(nodeID string) (string, error) { return l.mint(nodeJoin(nodeID)) }

// NodeCode is the live code for an adopted device and when it expires, for the adopting panel.
func (l *Local) NodeCode(nodeID string) (string, time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	got, err := l.readCodes()
	if err != nil {
		return "", time.Time{}, false
	}
	for _, c := range got {
		if c.Join == nodeJoin(nodeID) {
			return c.Code, time.Unix(c.At, 0).Add(CodeLifetime), true
		}
	}
	return "", time.Time{}, false
}

// prove attaches the root proof for join to an issuing answer and spends the code, or leaves the
// answer without a proof when no live code vouches for that join. It also writes back the pruned
// store. Unlocked; the caller holds the lock and has not yet released the answer.
func (l *Local) prove(resp *Response, join, nonce string) error {
	got, err := l.readCodes()
	if err != nil {
		return err
	}
	resp.ProofStatus = ProofNone
	for _, c := range got {
		if c.Join == join {
			resp.ProofStatus = ""
			root, err := DecodeCert(resp.Root)
			if err != nil {
				return err
			}
			resp.RootProof = RootProof(c.Code, RootFingerprint(root), resp.Account, resp.NodeID, nonce)
			break
		}
	}
	return l.writeJSON(CodesFile, without(got, join))
}

// dropCode voids the code for one join. Unlocked; the caller holds the lock.
func (l *Local) dropCode(join string) error {
	got, err := l.readCodes()
	if err != nil {
		return err
	}
	return l.writeJSON(CodesFile, without(got, join))
}

// pruneCodes rewrites the store with only its live codes, when there is a store at all.
func (l *Local) pruneCodes() error {
	if _, err := os.Stat(filepath.Join(l.dir, CodesFile)); err != nil {
		return nil
	}
	return l.withLock(func() error {
		got, err := l.readCodes()
		if err != nil {
			return err
		}
		return l.writeJSON(CodesFile, got)
	})
}
