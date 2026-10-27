package main

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// The shared state behind pair cooldowns and the station threshold (paircool.go). Both are
// idempotent: the extend is a compare-and-raise in one script, the payer window a set whose
// member is the payer (a repeat refreshes its score, never adds a payer).

func pairCoolKey(payer string) string  { return keyPrefix + "paircool:" + payer }
func coolPayersKey(node string) string { return keyPrefix + "coolpayers:" + node }

var pairCoolScript = redis.NewScript(`
local cur = tonumber(redis.call('HGET', KEYS[1], ARGV[1]) or '0')
local u = tonumber(ARGV[2])
if u > cur then redis.call('HSET', KEYS[1], ARGV[1], ARGV[2]) else u = cur end
local ttl = tonumber(ARGV[3])
if redis.call('PTTL', KEYS[1]) < ttl then redis.call('PEXPIRE', KEYS[1], ttl) end
return u
`)

func (v *valkeyStore) pairCoolExtend(payer, field string, untilMs int64, ttl time.Duration) (int64, error) {
	if v == nil || v.rdb == nil {
		return 0, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	got, err := pairCoolScript.Run(ctx, v.rdb, []string{pairCoolKey(payer)}, field, untilMs, ttl.Milliseconds()).Int64()
	if err != nil {
		v.noteErr("pairCoolExtend", err)
		return 0, err
	}
	v.setUp(true)
	return got, nil
}

func (v *valkeyStore) pairCooling(payer string) (map[string]int64, error) {
	if v == nil || v.rdb == nil {
		return nil, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	raw, err := v.rdb.HGetAll(ctx, pairCoolKey(payer)).Result()
	if err != nil {
		v.noteErr("pairCooling", err)
		return nil, err
	}
	v.setUp(true)
	out := make(map[string]int64, len(raw))
	for f, s := range raw {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			out[f] = n
		}
	}
	return out, nil
}

func (v *valkeyStore) coolPayerNote(node, payer string, nowMs int64, window time.Duration) (int, error) {
	if v == nil || v.rdb == nil {
		return 0, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	key := coolPayersKey(node)
	var card *redis.IntCmd
	if _, err := v.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZAdd(ctx, key, redis.Z{Score: float64(nowMs), Member: payer})
		p.ZRemRangeByScore(ctx, key, "-inf", "("+strconv.FormatInt(nowMs-window.Milliseconds(), 10))
		card = p.ZCard(ctx, key)
		p.PExpire(ctx, key, window+time.Minute)
		return nil
	}); err != nil {
		v.noteErr("coolPayerNote", err)
		return 0, err
	}
	v.setUp(true)
	return int(card.Val()), nil
}

// memStore has no shared backend: the broker keeps both per instance (single-instance only).
func (m *memStore) pairCoolExtend(string, string, int64, time.Duration) (int64, error) {
	return 0, errNoSharedStore
}
func (m *memStore) pairCooling(string) (map[string]int64, error) { return nil, errNoSharedStore }
func (m *memStore) coolPayerNote(string, string, int64, time.Duration) (int, error) {
	return 0, errNoSharedStore
}
