package lanmode

// pin.go: the Towers this machine joined by invite or approval are pinned to their TLS key
// (features/join/tower_identity.feature). The process loads the pins from config once; Route then
// sends every request for a pinned Tower over TLS with a client that trusts exactly that key, and
// never falls back to plain http. An unpinned Tower stays plain http for the transition release,
// unverified.

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/towerpin"
)

var (
	pinMu sync.RWMutex
	pins  = map[string]string{}
)

// SetPins replaces the pinned Towers, keyed by host:port. An entry that is not a full fingerprint
// is ignored.
func SetPins(m map[string]string) {
	next := map[string]string{}
	for addr, fp := range m {
		if p, ok := towerpin.Parse(fp); ok {
			if a, err := ParseAddr(addr); err == nil {
				next[strings.ToLower(a.String())] = p
			}
		}
	}
	pinMu.Lock()
	pins = next
	pinMu.Unlock()
}

// PinFor is the pin for a Tower address (host:port), if it has one.
func PinFor(addr string) (string, bool) {
	a, err := ParseAddr(addr)
	if err != nil {
		return "", false
	}
	pinMu.RLock()
	defer pinMu.RUnlock()
	p, ok := pins[strings.ToLower(a.String())]
	return p, ok
}

// pinnedTimeout bounds a pinned client when the caller's client has none.
const pinnedTimeout = 30 * time.Second

// Route turns a Tower base URL into the one to use: a pinned Tower gets https and a client pinned
// to its key (keeping the caller's timeout); anything else is returned as given.
func Route(base string, hc *http.Client) (string, *http.Client, bool) {
	u, err := url.Parse(base)
	if err != nil {
		return base, hc, false
	}
	pin, ok := PinFor(u.Host)
	if !ok {
		return base, hc, false
	}
	u.Scheme = "https"
	timeout := pinnedTimeout
	if hc != nil && hc.Timeout > 0 {
		timeout = hc.Timeout
	}
	return u.String(), towerpin.Client(pin, timeout), true
}
