package main

// sharedstore_towerpull.go - the shared state behind Tower pull dispatch
// (features/tower/tower_pull_dispatch.feature): a per-Tower work buffer with a wake-up channel,
// the same shape as the per-node cancel delivery, and a string read for the single-delivery
// claim, which records which Tower took a piece of work (or that its sender withdrew it).

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// towerWorkMax is the most pieces of work kept queued for one Tower.
const towerWorkMax = 256

const (
	towerWorkBufPrefix = keyPrefix + "towerwork:"
	busTowerWorkPrefix = keyPrefix + "bus:towerwork:"
)

// bulkOpMax caps one bulk call; bulkPoolSize is the bulk pool's connections per server.
const (
	bulkOpMax    = 30 * time.Second
	bulkPoolSize = 16
)

// bulkTimeout is the deadline for a bulk call moving n bytes: the hot path's bound plus the
// payload at 4 MiB/s, a floor any store link worth running on clears, capped at bulkOpMax.
func bulkTimeout(n int) time.Duration {
	const rate = 4 << 20
	if n >= int(bulkOpMax/time.Second)*rate {
		return bulkOpMax // and no overflow for a size the cap already covers
	}
	return min(sharedOpTimeout+time.Duration(n)*time.Second/rate, bulkOpMax)
}

// towerWorkDrainBudget is the payload a drain is timed for: a full batch of sealed requests plus
// what is left queued behind it.
const towerWorkDrainBudget = 40 << 20

func (v *valkeyStore) bulkClient() redis.UniversalClient {
	if v.bulk != nil {
		return v.bulk
	}
	return v.rdb
}

func (v *valkeyStore) towerWorkPush(tower string, entry []byte, ttl time.Duration) error {
	// The wake is one byte: a held pull drains the buffer on any wake, and a sealed request can
	// be megabytes that every subscriber would otherwise be handed and throw away.
	// Capped at the newest towerWorkMax: anything older is long past its 3 s pickup deadline on any
	// Tower that is pulling, and a Tower that is not must not pile sealed bodies up.
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	return v.bufPushOn(v.bulkClient(), bulkTimeout(len(entry)), "towerWorkPush", towerWorkBufPrefix+tower,
		busTowerWorkPrefix+tower, entry, []byte{1}, ttl, towerWorkMax)
}

func (v *valkeyStore) towerWorkDrain(tower string) ([][]byte, error) {
	if v == nil || v.rdb == nil {
		return nil, errNoSharedStore
	}
	return v.bufDrainOn(v.bulkClient(), bulkTimeout(towerWorkDrainBudget), "towerWorkDrain", towerWorkBufPrefix+tower)
}

func (v *valkeyStore) bulkSet(key string, val []byte, ttl time.Duration) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout(len(val)))
	defer cancel()
	if err := v.bulkClient().Set(ctx, cacheKeyPrefix+key, val, ttl).Err(); err != nil {
		v.noteErr("bulkSet", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) cacheExists(key string) (bool, error) {
	if v == nil || v.rdb == nil {
		return false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	n, err := v.rdb.Exists(ctx, cacheKeyPrefix+key).Result()
	if err != nil {
		v.noteErr("cacheExists", err)
		return false, err
	}
	v.setUp(true)
	return n > 0, nil
}

// bulkGet reads a bulkSet value; it is timed for the largest payload the store holds for pull.
func (v *valkeyStore) bulkGet(key string) ([]byte, bool, error) {
	if v == nil || v.rdb == nil {
		return nil, false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout(towerWorkDrainBudget))
	defer cancel()
	val, err := v.bulkClient().Get(ctx, cacheKeyPrefix+key).Bytes()
	if err == redis.Nil {
		v.setUp(true)
		return nil, false, nil
	}
	if err != nil {
		v.noteErr("bulkGet", err)
		return nil, false, err
	}
	v.setUp(true)
	return val, true, nil
}

func (v *valkeyStore) towerWorkSubscribe(ctx context.Context, tower string) (<-chan []byte, func(), error) {
	return v.busSubscribe(ctx, busTowerWorkPrefix+tower)
}

// stringGet reads a value written by setIfAbsent ("" and false when absent).
func (v *valkeyStore) stringGet(key string) (string, bool, error) {
	if v == nil || v.rdb == nil {
		return "", false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	s, err := v.rdb.Get(ctx, counterKeyPrefix+key).Result()
	if err == redis.Nil {
		v.setUp(true)
		return "", false, nil
	}
	if err != nil {
		v.noteErr("stringGet", err)
		return "", false, err
	}
	v.setUp(true)
	return s, true, nil
}

func (m *memStore) towerWorkPush(string, []byte, time.Duration) error { return errNoSharedStore }
func (m *memStore) towerWorkDrain(string) ([][]byte, error)           { return nil, errNoSharedStore }
func (m *memStore) towerWorkSubscribe(context.Context, string) (<-chan []byte, func(), error) {
	return nil, func() {}, errNoSharedStore
}
func (m *memStore) stringGet(string) (string, bool, error)      { return "", false, errNoSharedStore }
func (m *memStore) bulkSet(string, []byte, time.Duration) error { return errNoSharedStore }
func (m *memStore) bulkGet(string) ([]byte, bool, error)        { return nil, false, errNoSharedStore }
func (m *memStore) cacheExists(string) (bool, error)            { return false, errNoSharedStore }
