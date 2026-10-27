package main

// freelimit.go - free traffic is rate-limited on its own (contract §14.3, #7). Free inference
// is what an abuser farms, and a keypair costs nothing to mint, so free traffic is limited
// per client IP (ROGERAI_FREE_RATE_RPM, 20), per (client IP, station)
// (ROGERAI_FREE_STATION_RPM, 10), and pinning one free station via order/only/pin is capped
// per caller (ROGERAI_FREE_PIN_RPM, 10). Self-use and free grants have their own limits and
// are not free traffic here; paid traffic never draws on these buckets.
//
// STATE: the three limiters use the SHARED token bucket (sharedStore.rateAllow) when a
// shared store is configured, so one limit holds across instances. On a shared-store outage
// they degrade to this instance's own bucket - the existing rate-limit convention: the
// limit is never lifted, it is enforced per instance until the store returns.

import (
	"net/http"
	"strconv"
	"sync"
)

type freeLimiters struct {
	once                sync.Once
	perIP, station, pin *rateLimiter
}

func (b *broker) freeLimits() *freeLimiters {
	f := &b.freeRL
	f.once.Do(func() {
		mk := func(name string) *rateLimiter {
			rl := &rateLimiter{buckets: map[string]*tokenBucket{}, name: name}
			if b.shared != nil {
				rl.shared = b.shared
			}
			return rl
		}
		f.perIP, f.station, f.pin = mk("free"), mk("freestation"), mk("freepin")
	})
	return f
}

// freeTrafficRefused applies the free-traffic limits to a request whose pick landed on a
// free offer, and answers the refusal when one applies (true = answered). pinned is a
// station the caller named (order, only or the pin header); caller is the caller's
// identity for the pin cap (the strike payer key: account, grant or per-IP bucket).
func (b *broker) freeTrafficRefused(w http.ResponseWriter, ip, node, caller string, pinned bool) bool {
	f := b.freeLimits()
	refuse := func(code, msg string, retry int) bool {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		w.Header().Set("X-RogerAI-Cost", "0")
		jsonErrCode(w, http.StatusTooManyRequests, code, msg)
		return true
	}
	if pinned {
		if rpm := envFloat("ROGERAI_FREE_PIN_RPM", 10); rpm > 0 {
			if ok, retry := f.pin.allowAt(caller+"|"+node, rpm, rpm); !ok {
				return refuse("free_pin_limited", "too many requests pinned to one free station - slow down or let the router choose", retry)
			}
		}
	}
	if rpm := envFloat("ROGERAI_FREE_RATE_RPM", 20); rpm > 0 {
		if ok, retry := f.perIP.allowAt(ip, rpm, rpm); !ok {
			return refuse("free_rate_limited", "free-model rate limit exceeded for this network - slow down", retry)
		}
	}
	if rpm := envFloat("ROGERAI_FREE_STATION_RPM", 10); rpm > 0 {
		if ok, retry := f.station.allowAt(ip+"|"+node, rpm, rpm); !ok {
			return refuse("free_rate_limited", "free-model rate limit exceeded on this station for this network - slow down", retry)
		}
	}
	return false
}
