package edgeauth

// bearer.go is the key-less Edge invite (features/join/invite.feature, "roger edge invite needs no
// joiner key first"; ruling 2026-10-10, superseding enrollment D15). The owner mints a BEARER code;
// whichever device proves it holds that code is admitted, once. The device's proof is the
// joincode.EdgeJoin proof over the authority's root fingerprint, its user key, its node and the
// request's nonce, so it cannot be lifted onto another request or replayed at another Edge. Wrong
// guesses are budgeted: every failed proof counts against every live bearer code, and a code is
// void at BearerGuessBudget. "roger edge authority allow <key>" still works beside it.

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"rogerai.fm/roger/v6/internal/joincode"
)

const (
	// BearerGuessBudget is how many wrong proofs a live bearer code survives.
	BearerGuessBudget = 10
	// maxBearerCodes caps the live bearer codes, so a request costs at most this many stretches.
	maxBearerCodes = 8
	bearerPrefix   = "bearer:"
)

// JoinProof is the device's proof that it holds code, for the Edge whose root hashes to rootFP.
func JoinProof(code, rootFP string, r Request) string {
	return joincode.EdgeJoin.Proof(code, r.Nonce, joinFields(rootFP, r)...)
}

func joinFields(rootFP string, r Request) []string {
	return []string{strings.TrimPrefix(rootFP, "sha256:"), r.UserKey, r.NodeID, r.NodeKey}
}

// MintBearerCode mints a key-less invite code: it admits whichever device proves it, once.
func (l *Local) MintBearerCode() (string, error) {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	join := bearerPrefix + hex.EncodeToString(id[:])
	code := NewCode()
	err := l.withLock(func() error {
		got, err := l.readCodes()
		if err != nil {
			return err
		}
		var bearer int
		for _, c := range got {
			if strings.HasPrefix(c.Join, bearerPrefix) {
				bearer++
			}
		}
		// The oldest bearer code makes room, so the live set never exceeds the cap.
		for bearer >= maxBearerCodes {
			for i, c := range got {
				if strings.HasPrefix(c.Join, bearerPrefix) {
					got = append(got[:i:i], got[i+1:]...)
					bearer--
					break
				}
			}
		}
		return l.writeJSON(CodesFile, append(got, enrollCode{Join: join, Code: code, At: l.now().Unix()}))
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// bearerMatch finds the live bearer code r's join proof was made with. A proof that matches none
// counts as one wrong guess against every live bearer code. Unlocked; the caller holds the lock.
func (l *Local) bearerMatch(r Request) (string, bool, error) {
	got, err := l.readCodes()
	if err != nil {
		return "", false, err
	}
	fields := joinFields(l.Descriptor().Fingerprint, r)
	for _, c := range got {
		if strings.HasPrefix(c.Join, bearerPrefix) &&
			joincode.EdgeJoin.Check(c.Code, r.JoinProof, r.Nonce, fields...) == nil {
			return c.Join, true, nil
		}
	}
	out := got[:0]
	for _, c := range got {
		if strings.HasPrefix(c.Join, bearerPrefix) {
			c.Fails++
			if c.Fails >= BearerGuessBudget {
				continue
			}
		}
		out = append(out, c)
	}
	return "", false, l.writeJSON(CodesFile, out)
}

// allowLocked adds a user key to the allow-list. Unlocked; the caller holds the lock.
func (l *Local) allowLocked(userKeyHex string) error {
	got, err := l.readJSON2Allowed()
	if err != nil {
		return err
	}
	for _, k := range got {
		if k == userKeyHex {
			return nil
		}
	}
	return l.writeJSON(AllowFile, append(got, userKeyHex))
}
