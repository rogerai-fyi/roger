package keypurpose

import "encoding/hex"

// The test-only surface. It lives here rather than in keyring.go so the production file
// contains only what production calls - a review found four such helpers sitting in it,
// and one of them (canSignWith) was carrying a spec property that the real signing path
// did not enforce at all. Keeping the seams visibly separate is what makes that kind of
// gap obvious instead of comfortable.

func (k *Key) secretHexForTest() string { return hex.EncodeToString(k.priv) }

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

// remove drops a purpose's key, standing in for a key that is missing, malformed, or
// unreadable at load.
func (r *Ring) remove(p Purpose) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, p)
}
