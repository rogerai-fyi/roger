package keypurpose

import (
	"encoding/hex"
	"time"
)

// The test-only surface. It lives here rather than in keyring.go so the production file
// contains only what production calls - a review found four such helpers sitting in it,
// and one of them (canSignWith) was carrying a spec property that the real signing path
// did not enforce at all. Keeping the seams visibly separate is what makes that kind of
// gap obvious instead of comfortable.

func (k *Key) secretHexForTest() string {
	if len(k.priv) > 0 {
		return hex.EncodeToString(k.priv)
	}
	return hex.EncodeToString(k.secret)
}

// canSignWith reports whether a key may still produce signatures. Sign enforces the same
// rule through canSignWithLocked; this is the read-only view of it.
func (r *Ring) canSignWith(k *Key) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.canSignWithLocked(k)
}

// canVerifyWith reports whether a key's signatures are still checkable.
func (r *Ring) canVerifyWith(k *Key) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.findLocked(k.KeyID) != nil
}

// remove drops a purpose's key, standing in for a key that is missing at load.
func (r *Ring) remove(p Purpose) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, p)
}

func (r *Ring) keyIDForTest(p Purpose) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if k := r.keys[p]; k != nil {
		return k.KeyID
	}
	return ""
}

func (r *Ring) secretForTest(p Purpose) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if k := r.keys[p]; k != nil {
		return k.secret
	}
	return nil
}

// use exercises a purpose whichever kind it is, so a table covering every role does not
// have to branch on kind at every row.
func (r *Ring) use(p Purpose, msg []byte) (Signature, error) {
	if KindOf(p) == KindSymmetric {
		return r.MAC(p, msg)
	}
	return r.Sign(p, msg)
}

// check is the verifying half of use.
func (r *Ring) check(p Purpose, msg []byte, sig Signature) error {
	if KindOf(p) == KindSymmetric {
		return r.VerifyMAC(p, msg, sig)
	}
	return r.Verify(p, msg, sig)
}

// shareSecretForTest makes two symmetric roles hold identical material, standing in for a
// configuration that stretched one secret across several roles.
func shareSecretForTest(r *Ring, a, b Purpose) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[b].secret = r.keys[a].secret
}

// ringFromSecretForTest builds an attacker's ring holding stolen bytes under a role they
// do not own.
func ringFromSecretForTest(p Purpose, secret []byte) *Ring {
	now := time.Now()
	r := &Ring{keys: map[Purpose]*Key{}, retired: map[string]*Key{}}
	r.keys[p] = &Key{
		Purpose: p, KeyID: "stolen", secret: secret,
		NotBefore: now, NotAfter: now.Add(time.Hour),
	}
	return r
}
