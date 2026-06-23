package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Criteria are the user's routing constraints - the same knobs honored by the
// broker's matcher. Failover re-selection MUST respect every one of these so an
// alternative provider is never a downgrade the user didn't ask for.
type Criteria struct {
	Model        string
	Confidential bool
	MinTPS       float64 // require measured tok/s >= this (0 = no floor)
	MaxPrice     float64 // skip offers whose input price exceeds this (0 = no cap)
}

// Offer is one discoverable provider offer (a subset of the broker's /discover
// view, just the fields selection needs).
type Offer struct {
	NodeID       string  `json:"node_id"`
	Model        string  `json:"model"`
	PriceIn      float64 `json:"price_in"`
	Online       bool    `json:"online"`
	Confidential bool    `json:"confidential"`
	TPS          float64 `json:"tps"`
}

// failoverPolicy bounds the auto-recovery loop. Defaults are conservative so a
// flapping broker can't turn one client request into a retry storm.
type failoverPolicy struct {
	maxAttempts int           // total tries (initial + retries)
	baseBackoff time.Duration // first backoff; doubles each retry (capped)
	maxBackoff  time.Duration
}

func defaultPolicy() failoverPolicy {
	return failoverPolicy{maxAttempts: 4, baseBackoff: 200 * time.Millisecond, maxBackoff: 2 * time.Second}
}

// retryable reports whether a relay outcome warrants failing over to another
// provider. Transport errors (timeout / connection drop) and broker/node 5xx
// (incl. 502/503/504) are retryable; a 4xx is the caller's fault (bad request,
// no credits) and is surfaced immediately. statusCode<=0 means a transport error.
func retryable(statusCode int, err error) bool {
	if err != nil {
		return true
	}
	return statusCode >= 500
}

// backoff returns the delay before attempt n (0-based: attempt 0 has no prior
// delay; this is called for attempt n>=1), exponential with a cap.
func (p failoverPolicy) backoff(attempt int) time.Duration {
	d := p.baseBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= p.maxBackoff {
			return p.maxBackoff
		}
	}
	return d
}

// selectAlternative re-queries /discover and returns the best online offer that
// still satisfies the criteria, skipping any node in `exclude` (the providers
// that just failed). "Best" = highest measured tok/s among eligible, tie-broken
// by lowest input price - we want the failover target to be both fast and cheap.
// Returns ("", false) when nothing eligible remains.
func selectAlternative(broker string, c Criteria, exclude map[string]bool) (string, bool) {
	offers, err := discover(broker)
	if err != nil {
		return "", false
	}
	return pickAlternative(offers, c, exclude)
}

// pickAlternative is the pure selection step (no I/O) so it is unit-testable.
func pickAlternative(offers []Offer, c Criteria, exclude map[string]bool) (string, bool) {
	var eligible []Offer
	for _, o := range offers {
		if !o.Online || o.Model != c.Model {
			continue
		}
		if exclude[o.NodeID] {
			continue
		}
		if c.Confidential && !o.Confidential {
			continue
		}
		if c.MaxPrice > 0 && o.PriceIn > c.MaxPrice {
			continue
		}
		// Only exclude nodes MEASURED as too slow; unmeasured (tps==0) get a
		// chance so new providers aren't permanently passed over (mirrors broker).
		if c.MinTPS > 0 && o.TPS > 0 && o.TPS < c.MinTPS {
			continue
		}
		eligible = append(eligible, o)
	}
	if len(eligible) == 0 {
		return "", false
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].TPS != eligible[j].TPS {
			return eligible[i].TPS > eligible[j].TPS // faster first
		}
		return eligible[i].PriceIn < eligible[j].PriceIn // then cheaper
	})
	return eligible[0].NodeID, true
}

// discover fetches the current offer list from the broker.
func discover(broker string) ([]Offer, error) {
	resp, err := http.Get(broker + "/discover")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discover: status %d", resp.StatusCode)
	}
	var d struct {
		Offers []Offer `json:"offers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return d.Offers, nil
}
