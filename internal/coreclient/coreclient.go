// Package coreclient records, process-wide, that code built or used a client that talks to
// Roger Core (a joined Tower's link, an enrollment, a Core settle, the sealed Edge consumer).
// The standalone Tower's serve refuses to start once anything has (features/packaging/
// one_binary.feature section 4): in standalone mode no such client may exist, so one that does is
// a bug, and the process fails closed instead of serving beside it.
//
// It is a leaf (standard library only), so any Core client can mark itself without an import
// cycle and the standalone serve can ask without linking a client.
package coreclient

import "sync"

var (
	mu    sync.Mutex
	noted []string
)

// Note records that a Core client named what was built or used.
func Note(what string) {
	mu.Lock()
	defer mu.Unlock()
	noted = append(noted, what)
}

// Noted is every Core client recorded so far, in order.
func Noted() []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), noted...)
}

// Reset forgets every record (tests start each scenario clean).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	noted = nil
}
