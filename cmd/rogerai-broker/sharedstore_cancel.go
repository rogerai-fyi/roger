package main

// sharedstore_cancel.go - the shared state behind job cancels (features/multinode/job_cancel.feature):
// a node's cancel capability (a value carrying its own expiry, so an expiry is honoured even where
// the store's TTL has not yet reaped the key) and the per-node cancel delivery (a pub/sub channel
// the node's held cancel poll subscribes to, plus a short buffer for a node between polls). Every
// entry carries its own expiry for the same reason.

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	cancelCapPrefix = keyPrefix + "cancelcap:"
	busCancelPrefix = keyPrefix + "bus:cancel:"
	cancelBufPrefix = keyPrefix + "cancelbuf:"
)

func (v *valkeyStore) cancelCapSet(node string, untilMs int64, ttl time.Duration) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	if err := v.rdb.Set(ctx, cancelCapPrefix+node, strconv.FormatInt(untilMs, 10), ttl).Err(); err != nil {
		v.noteErr("cancelCapSet", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) cancelCapGet(node string) (int64, error) {
	if v == nil || v.rdb == nil {
		return 0, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	s, err := v.rdb.Get(ctx, cancelCapPrefix+node).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		v.noteErr("cancelCapGet", err)
		return 0, err
	}
	v.setUp(true)
	n, _ := strconv.ParseInt(s, 10, 64)
	return n, nil
}

// cancelPush publishes entry to node's held cancel poll; when nobody is listening it is kept on
// the node's buffer list (TTL ttl) for its next poll.
func (v *valkeyStore) cancelPush(node string, entry []byte, ttl time.Duration) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), busPublishTimeout)
	defer cancel()
	n, err := v.rdb.Publish(ctx, busCancelPrefix+node, entry).Result()
	if err != nil {
		v.noteErr("cancelPush", err)
		return err
	}
	if n == 0 {
		key := cancelBufPrefix + node
		if perr := v.rdb.RPush(ctx, key, entry).Err(); perr != nil {
			v.noteErr("cancelPush", perr)
			return perr
		}
		v.rdb.Expire(ctx, key, ttl)
	}
	v.setUp(true)
	return nil
}

// cancelDrain removes and returns every buffered entry for node.
func (v *valkeyStore) cancelDrain(node string) ([][]byte, error) {
	if v == nil || v.rdb == nil {
		return nil, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	key := cancelBufPrefix + node
	pipe := v.rdb.TxPipeline()
	lr := pipe.LRange(ctx, key, 0, -1)
	pipe.Del(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		v.noteErr("cancelDrain", err)
		return nil, err
	}
	v.setUp(true)
	out := make([][]byte, 0, len(lr.Val()))
	for _, s := range lr.Val() {
		out = append(out, []byte(s))
	}
	return out, nil
}

func (v *valkeyStore) cancelSubscribe(ctx context.Context, node string) (<-chan []byte, func(), error) {
	return v.busSubscribe(ctx, busCancelPrefix+node)
}

func (m *memStore) cancelCapSet(string, int64, time.Duration) error { return errNoSharedStore }
func (m *memStore) cancelCapGet(string) (int64, error)              { return 0, errNoSharedStore }
func (m *memStore) cancelPush(string, []byte, time.Duration) error  { return errNoSharedStore }
func (m *memStore) cancelDrain(string) ([][]byte, error)            { return nil, errNoSharedStore }
func (m *memStore) cancelSubscribe(context.Context, string) (<-chan []byte, func(), error) {
	return nil, func() {}, errNoSharedStore
}
