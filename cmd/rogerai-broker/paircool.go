package main

// paircool.go - cooldowns are per (station, payer) first (contract §14.2, #4).
//
// A 429 cools the (station, payer, model) PAIR for its Retry-After (the same default, cap and
// extend-never-stack rule as the station cooldown). The station cools for EVERY payer (the
// node-wide coolStation) only once ROGERAI_COOLDOWN_MIN_PAYERS distinct payers got a 429 from
// it within ROGERAI_COOLDOWN_PAYER_WINDOW: one heavy payer can no longer take a station off
// the air for everyone else. A pair cooldown is routing state for that payer alone: it never
// pages, never counts the station off air, never touches trust.
//
// STATE: both the pair expiries and the payer window live in the shared store (sharedStore
// pairCoolExtend / pairCooling / coolPayerNote), so every instance sees the same pairs and
// the same count, across restarts. The local maps below are the single-instance fallback
// for a broker with no shared store configured (memStore answers errNoSharedStore), and the
// payer window's outage fallback; they are never the source of truth while the shared store
// answers. On a shared-store OUTAGE a pair cooldown fails OPEN (routing preference: the
// payer may be routed to the station again; its 429 still fails the attempt over), and the
// station threshold counts this instance's own payers - the station cooldown's existing
// outage rule (a per-instance cooldown until the store returns).

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

func cooldownMinPayers() int {
	if n := envInt("ROGERAI_COOLDOWN_MIN_PAYERS", 3); n >= 1 {
		return n
	}
	return 1
}

func cooldownPayerWindow() time.Duration {
	return envDuration("ROGERAI_COOLDOWN_PAYER_WINDOW", 60*time.Second)
}

// sharedOrNone is the shared store, or the no-backend store for a broker built without one
// (which answers errNoSharedStore, selecting the single-instance fallback).
func (b *broker) sharedOrNone() sharedStore {
	if b.shared == nil {
		return noSharedStore
	}
	return b.shared
}

var noSharedStore = newMemStore()

// pairField is a pair's field in its payer's cooldown hash.
func pairField(node, model string) string { return node + "|" + model }

// coolPair records that node's upstream said 429 to payer on model: the pair cools, and the
// station cools for everyone once enough distinct payers hit it in the window. It returns the
// pair's expiry.
func (b *broker) coolPair(node, model, payer string, retryAfterSec int) time.Time {
	if payer == "" {
		return b.coolStation(node, model, retryAfterSec) // no payer identity: today's rule
	}
	d := b.cooldownFor(retryAfterSec)
	now := b.now()
	until := now.Add(d)
	b.stats.pairCooldowns.Add(1)
	field := pairField(node, model)
	got, err := b.sharedOrNone().pairCoolExtend(payer, field, until.UnixMilli(), d)
	switch {
	case err == nil:
		until = time.UnixMilli(got)
	case err == errNoSharedStore:
		until = b.localPairExtend(payer, field, until)
	default:
		b.noteCoolOutage(err)
	}
	payers, err := b.sharedOrNone().coolPayerNote(node, payer, now.UnixMilli(), cooldownPayerWindow())
	if err != nil {
		if err != errNoSharedStore {
			// The station cooldown's existing outage rule (upstream_failover.feature: an
			// unreachable shared store falls back to a per-instance cooldown): count this
			// instance's own payers until the store returns.
			b.noteCoolOutage(err)
		}
		payers = b.localPayerNote(node, payer, now)
	}
	log.Printf("COOLDOWN pair node=%s model=%s for=%s (retry_after_sec=%d) payers_in_window=%d - routing this payer around it, not a strike", node, model, d, retryAfterSec, payers)
	if payers >= cooldownMinPayers() {
		b.coolStation(node, model, retryAfterSec)
	}
	return until
}

// noteCoolOutage logs a cooldown shared-store failure once (the same once as coolStation's).
func (b *broker) noteCoolOutage(err error) {
	b.coolFallbackOnce.Do(func() {
		log.Printf("cooldown: shared store unavailable (%v) - pair cooldowns fail open and cooldowns are per-instance until it returns", err)
	})
}

// payerCooling is the payer's live pair cooldowns (field -> expiry), read once per request
// OUTSIDE the routing locks. An outage reads as none (fail open).
func (b *broker) payerCooling(payer string) map[string]time.Time {
	if payer == "" {
		return nil
	}
	now := b.now()
	raw, err := b.sharedOrNone().pairCooling(payer)
	if err == errNoSharedStore {
		return b.localPairCooling(payer, now)
	}
	if err != nil {
		return nil
	}
	out := map[string]time.Time{}
	for f, ms := range raw {
		if t := time.UnixMilli(ms); now.Before(t) {
			out[f] = t
		}
	}
	return out
}

// pairUntil is the pair's expiry in a payerCooling map (zero when not cooling).
func pairUntil(m map[string]time.Time, node, model string) (time.Time, bool) {
	t, ok := m[pairField(node, model)]
	return t, ok
}

// ---- single-instance fallback (no shared store configured) ----------------------------------

func (b *broker) localPairExtend(payer, field string, until time.Time) time.Time {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if b.pairCoolLocal == nil {
		b.pairCoolLocal = map[string]map[string]time.Time{}
	}
	m := b.pairCoolLocal[payer]
	if m == nil {
		m = map[string]time.Time{}
		b.pairCoolLocal[payer] = m
	}
	if prev := m[field]; prev.After(until) {
		return prev // extend, never stack, never shorten
	}
	m[field] = until
	return until
}

func (b *broker) localPairCooling(payer string, now time.Time) map[string]time.Time {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	out := map[string]time.Time{}
	for f, t := range b.pairCoolLocal[payer] {
		if now.Before(t) {
			out[f] = t
		} else {
			delete(b.pairCoolLocal[payer], f)
		}
	}
	return out
}

func (b *broker) localPayerNote(node, payer string, now time.Time) int {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if b.coolPayersLocal == nil {
		b.coolPayersLocal = map[string]map[string]time.Time{}
	}
	m := b.coolPayersLocal[node]
	if m == nil {
		m = map[string]time.Time{}
		b.coolPayersLocal[node] = m
	}
	m[payer] = now
	cutoff := now.Add(-cooldownPayerWindow())
	for p, at := range m {
		if at.Before(cutoff) {
			delete(m, p)
		}
	}
	return len(m)
}

// tpmRequestShare is ROGERAI_TPM_REQUEST_SHARE as a fraction (default 50%).
func tpmRequestShare() float64 {
	v := strings.TrimSuffix(strings.TrimSpace(os.Getenv("ROGERAI_TPM_REQUEST_SHARE")), "%")
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 100 {
		return f / 100
	}
	return 0.5
}

// overTPMShare: a curated station's declared tokens-per-minute budget cannot take a request
// whose measured prompt is more than the share of it (contract §14.2). No declared tpm, no
// guard.
func overTPMShare(tpm, promptTokens int) bool {
	return tpm > 0 && float64(promptTokens) > tpmRequestShare()*float64(tpm)
}
