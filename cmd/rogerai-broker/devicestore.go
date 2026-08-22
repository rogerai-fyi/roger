package main

// devicestore.go backs pending device logins with the shared (Valkey) store, so the flow
// completes across broker instances instead of only within the process that issued it.
//
// WHY THIS ONE DOES NOT FALL BACK. Every other sharedStore call site in this package is
// required to degrade to an in-memory path when the backend is unreachable, because every
// other one is an ACCELERATOR: a cache, a liveness hint, a rate-limit bucket whose local
// approximation is merely less accurate. A pending login is not an accelerator. It is the
// authority on whether a person approved something, and a per-instance fallback is exactly
// the split-brain that makes the flow uncompletable behind a load balancer - the approval
// lands in one process and the poll reads another. So a failure here is reported as
// ErrUnavailable and the flow refuses, rather than quietly serving a second answer.
//
// LAYOUT. One hash per login carries the record and its revision together, which is what
// lets CAS be a single atomic script rather than a read followed by a hopeful write:
//
//	rogerai:dev:rec:<devHash>   HASH {rec: <json>, rev: <int>}   PEXPIRE at the deadline
//	rogerai:dev:usr:<userHash>  STRING <devHash>                 PEXPIRE at the deadline
//	rogerai:dev:wrong:<who>     STRING <int>                     the guessing budget
//
// Only hashes are keyed and only hashes are stored. The store is reachable by anything
// holding its credential - a backup, a replica, an operational scan - so a plaintext code
// at rest would be a credential we had handed out.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"rogerai.fm/roger/v6/internal/deviceauth"
)

const (
	deviceRecPrefix   = keyPrefix + "dev:rec:"
	deviceUserPrefix  = keyPrefix + "dev:usr:"
	deviceWrongPrefix = keyPrefix + "dev:wrong:"
)

// deviceWrongTTL bounds how long a spent guessing budget is remembered. It must comfortably
// outlive a login's own lifetime, or an attacker could refill simply by pausing.
const deviceWrongTTL = time.Hour

// valkeyDeviceStore implements deviceauth.Store over the shared server.
type valkeyDeviceStore struct{ v *valkeyStore }

// newValkeyDeviceStore returns a shared-backed store, or nil when there is no shared
// backend - the caller then keeps the in-process default.
func newValkeyDeviceStore(s sharedStore) deviceauth.Store {
	v, ok := s.(*valkeyStore)
	if !ok || v == nil || v.rdb == nil {
		return nil
	}
	return &valkeyDeviceStore{v: v}
}

func (d *valkeyDeviceStore) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), sharedOpTimeout)
}

// ttlFor is how long a record may live: exactly until its own deadline. Expiry is
// therefore enforced by the server as well as by the flow, so a record cannot outlive the
// login it describes even if a reaper never runs.
func ttlFor(r deviceauth.Record) time.Duration {
	ttl := time.Until(r.Expires)
	if ttl <= 0 {
		return time.Millisecond // already dead; let it land and expire immediately
	}
	return ttl
}

// createScript writes a record and its user index only if the record is absent, so a
// replayed Create can never reset a login that is already under way.
var createScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('HSET', KEYS[1], 'rec', ARGV[1], 'rev', 1)
redis.call('PEXPIRE', KEYS[1], ARGV[2])
redis.call('SET', KEYS[2], ARGV[3], 'PX', ARGV[2])
return 1
`)

func (d *valkeyDeviceStore) Create(r deviceauth.Record) error {
	r.Rev = 1
	blob, err := json.Marshal(r)
	if err != nil {
		return err
	}
	ctx, cancel := d.ctx()
	defer cancel()
	ms := ttlFor(r).Milliseconds()
	err = createScript.Run(ctx, d.v.rdb,
		[]string{deviceRecPrefix + r.DevHash, deviceUserPrefix + r.UserHash},
		blob, ms, r.DevHash).Err()
	if err != nil {
		d.v.noteErr("deviceCreate", err)
		return err
	}
	d.v.setUp(true)
	return nil
}

func (d *valkeyDeviceStore) ByDevice(devHash string) (deviceauth.Record, bool, error) {
	ctx, cancel := d.ctx()
	defer cancel()
	raw, err := d.v.rdb.HGet(ctx, deviceRecPrefix+devHash, "rec").Bytes()
	if err == redis.Nil {
		d.v.setUp(true)
		return deviceauth.Record{}, false, nil
	}
	if err != nil {
		d.v.noteErr("deviceByDevice", err)
		return deviceauth.Record{}, false, err
	}
	d.v.setUp(true)
	var r deviceauth.Record
	if err := json.Unmarshal(raw, &r); err != nil {
		// A record we cannot decode is not evidence that anybody approved anything.
		return deviceauth.Record{}, false, deviceauth.ErrCorruptRecord
	}
	return r, true, nil
}

func (d *valkeyDeviceStore) ByUser(userHash string) (deviceauth.Record, bool, error) {
	ctx, cancel := d.ctx()
	defer cancel()
	dev, err := d.v.rdb.Get(ctx, deviceUserPrefix+userHash).Result()
	if err == redis.Nil {
		d.v.setUp(true)
		return deviceauth.Record{}, false, nil
	}
	if err != nil {
		d.v.noteErr("deviceByUser", err)
		return deviceauth.Record{}, false, err
	}
	d.v.setUp(true)
	return d.ByDevice(dev)
}

// casScript is the whole reason the record and its revision share one key. It writes only
// if the revision the caller read is still current, so of N instances acting on the same
// read, exactly one wins - which is what makes "a code is consumed once across the
// deployment" true rather than merely likely.
var casScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[1], 'rev')
if not cur then return 0 end
if cur ~= ARGV[1] then return 0 end
redis.call('HSET', KEYS[1], 'rec', ARGV[2], 'rev', ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
redis.call('SET', KEYS[2], ARGV[5], 'PX', ARGV[4])
return 1
`)

func (d *valkeyDeviceStore) CAS(r deviceauth.Record) (bool, error) {
	expect := r.Rev
	next := expect + 1
	stored := r
	stored.Rev = next
	blob, err := json.Marshal(stored)
	if err != nil {
		return false, err
	}
	ctx, cancel := d.ctx()
	defer cancel()
	res, err := casScript.Run(ctx, d.v.rdb,
		[]string{deviceRecPrefix + r.DevHash, deviceUserPrefix + r.UserHash},
		expect, blob, next, ttlFor(r).Milliseconds(), r.DevHash).Int()
	if err != nil {
		d.v.noteErr("deviceCAS", err)
		return false, err
	}
	d.v.setUp(true)
	return res == 1, nil
}

func (d *valkeyDeviceStore) Delete(devHash string) error {
	rec, ok, err := d.ByDevice(devHash)
	if err != nil && !errors.Is(err, deviceauth.ErrCorruptRecord) {
		return err
	}
	ctx, cancel := d.ctx()
	defer cancel()
	keys := []string{deviceRecPrefix + devHash}
	if ok {
		keys = append(keys, deviceUserPrefix+rec.UserHash)
	}
	if err := d.v.rdb.Del(ctx, keys...).Err(); err != nil {
		d.v.noteErr("deviceDelete", err)
		return err
	}
	d.v.setUp(true)
	return nil
}

func (d *valkeyDeviceStore) Budget(submitter string) (int, error) {
	ctx, cancel := d.ctx()
	defer cancel()
	n, err := d.v.rdb.Get(ctx, deviceWrongPrefix+submitter).Int()
	if err == redis.Nil {
		d.v.setUp(true)
		return 0, nil
	}
	if err != nil {
		d.v.noteErr("deviceBudget", err)
		return 0, err
	}
	d.v.setUp(true)
	return n, nil
}

// penalizeScript increments and (re)arms the expiry in one round trip, so a budget cannot
// be refilled by spreading guesses across instances or by waiting out a lost EXPIRE.
var penalizeScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ARGV[1])
return n
`)

func (d *valkeyDeviceStore) Penalize(submitter string, ttl time.Duration) (int, error) {
	if ttl < deviceWrongTTL {
		ttl = deviceWrongTTL
	}
	ctx, cancel := d.ctx()
	defer cancel()
	n, err := penalizeScript.Run(ctx, d.v.rdb,
		[]string{deviceWrongPrefix + submitter}, ttl.Milliseconds()).Int()
	if err != nil {
		d.v.noteErr("devicePenalize", err)
		return 0, err
	}
	d.v.setUp(true)
	return n, nil
}

// Reap is a no-op here, and deliberately so: every key this store writes carries a PEXPIRE
// set to the login's own deadline, so the server removes them without us scanning for
// them. A SCAN across a SHARED Valkey instance is exactly the un-prefixed, whole-keyspace
// operation this package forbids.
func (d *valkeyDeviceStore) Reap(time.Time) error { return nil }
