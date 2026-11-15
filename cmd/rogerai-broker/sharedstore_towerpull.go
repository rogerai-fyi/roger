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

func (v *valkeyStore) towerWorkPush(tower string, entry []byte, ttl time.Duration) error {
	// The wake is one byte: a held pull drains the buffer on any wake, and a sealed request can
	// be megabytes that every subscriber would otherwise be handed and throw away.
	// Capped at the newest towerWorkMax: anything older is long past its 3 s pickup deadline on any
	// Tower that is pulling, and a Tower that is not must not pile sealed bodies up.
	return v.bufPush("towerWorkPush", towerWorkBufPrefix+tower, busTowerWorkPrefix+tower, entry, []byte{1}, ttl, towerWorkMax)
}

func (v *valkeyStore) towerWorkDrain(tower string) ([][]byte, error) {
	return v.bufDrain("towerWorkDrain", towerWorkBufPrefix+tower)
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
func (m *memStore) stringGet(string) (string, bool, error) { return "", false, errNoSharedStore }
