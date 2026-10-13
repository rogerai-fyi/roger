package edge

// THE REPLAY CACHE every signed request on the Edge spends its nonce in: presence, invocations, a
// phone's turns. A captured request is refused for as long as it could still be fresh.
//
// Audit 2026-10-13: one cache shared by every member, bounded only in total, let ONE member spend
// the whole bound with fresh nonces and lock every other member out until the window passed, and
// every admit scanned the whole map to prune it. Now each nonce is kept under the node that spent it,
// each node has its own quota within a window, and pruning pops expired entries in expiry order.

import (
	"container/heap"
	"sync"
)

const (
	// maxNonces bounds the whole cache: far above any real fleet's signed traffic over one window.
	maxNonces = 100000
	// maxNoncesPerNode bounds what one node may hold at once: a phone beating every 30 s and asking
	// for a turn every second stays far below it over a five-minute window; a flood stops at it, and
	// stops only that node.
	maxNoncesPerNode = 600
)

type nonceEntry struct {
	key, owner string
	expiry     int64
}

type nonceHeap []nonceEntry

func (h nonceHeap) Len() int           { return len(h) }
func (h nonceHeap) Less(i, j int) bool { return h[i].expiry < h[j].expiry }
func (h nonceHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nonceHeap) Push(x any)        { *h = append(*h, x.(nonceEntry)) }
func (h *nonceHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

type nonceCache struct {
	mu      sync.Mutex
	seen    map[string]int64 // owner|nonce -> unix expiry
	byOwner map[string]int
	order   nonceHeap
}

func newNonceCache() *nonceCache {
	return &nonceCache{seen: map[string]int64{}, byOwner: map[string]int{}}
}

// admit returns true the FIRST time owner spends nonce, and false on a repeat, on a nonce that is not
// exactly 16 random bytes as 32 lowercase hex characters (what every client sends: the CLI, the agent
// and the Roger app), and when owner or the whole cache is at its bound with nonces still live (fail
// closed on memory). owner is the node that signed. Audit 2026-10-13: only a floor was checked, and an
// 800 KiB "nonce" was kept.
func (n *nonceCache) admit(owner, nonce string, expiry, nowUnix int64) bool {
	if !isLowerHex(nonce, 32) {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for n.order.Len() > 0 && n.order[0].expiry < nowUnix {
		e := heap.Pop(&n.order).(nonceEntry)
		delete(n.seen, e.key)
		if n.byOwner[e.owner]--; n.byOwner[e.owner] <= 0 {
			delete(n.byOwner, e.owner)
		}
	}
	key := owner + "|" + nonce
	if _, ok := n.seen[key]; ok {
		return false
	}
	if len(n.seen) >= maxNonces || n.byOwner[owner] >= maxNoncesPerNode {
		return false
	}
	n.seen[key] = expiry
	n.byOwner[owner]++
	heap.Push(&n.order, nonceEntry{key: key, owner: owner, expiry: expiry})
	return true
}
