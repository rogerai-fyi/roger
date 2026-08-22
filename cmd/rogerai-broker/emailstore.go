package main

// emailstore.go backs first-party sign-in with the shared store, so a mailed code can be
// typed back into whichever instance the load balancer picks.
//
// Email login shipped over the in-process store, which is the same defect device login had
// and is worse here because it is on the path of every sign-in:
//
//   - a restart drops outstanding codes, and the person who types one is told their code is
//     invalid when the truth is we forgot it;
//   - behind more than one instance, /auth/email/start lands on A and /auth/email/verify on
//     B, so the code is never found and first-party sign-in cannot complete at all;
//   - the rate limits are per-instance, which silently multiplies the per-address budget by
//     the instance count - and that budget is what stops our own mailer being used to flood
//     somebody's inbox.
//
// Like the device-login store, this one does NOT fall back to memory on error. Every other
// sharedStore call site is an accelerator whose local answer is merely less accurate; an
// outstanding sign-in code is the authority on whether a person proved they hold an
// address, and a per-instance fallback is the split-brain being removed.
//
// LAYOUT. One hash per address carries the record and its revision together, so spending a
// code is one atomic script rather than a read followed by a hopeful delete:
//
//	rogerai:eml:rec:<addrHash>   HASH {rec: <json>, rev: <int>}   PEXPIRE at the deadline
//	rogerai:eml:req:<addrHash>   STRING <count>                   per-address request budget
//	rogerai:eml:reqsrc:<source>  STRING <count>                   per-source request budget
//	rogerai:eml:sub:<source>     STRING <count>                   per-source submit budget
//
// Only hashes are keyed and only hashes are stored: the address and the code are never at
// rest here, because the store is reachable by anything holding its credential.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"rogerai.fm/roger/v6/internal/emailauth"
)

const (
	emailRecPrefix    = keyPrefix + "eml:rec:"
	emailReqPrefix    = keyPrefix + "eml:req:"
	emailReqSrcPrefix = keyPrefix + "eml:reqsrc:"
	emailSubSrcPrefix = keyPrefix + "eml:sub:"
)

// valkeyEmailStore implements emailauth.Store over the shared server.
type valkeyEmailStore struct{ v *valkeyStore }

// newValkeyEmailStore returns a shared-backed store, or nil when there is no shared
// backend - the caller then keeps the in-process default, which is the single-instance
// deployment and needs no configuration.
func newValkeyEmailStore(s sharedStore) emailauth.Store {
	v, ok := s.(*valkeyStore)
	if !ok || v == nil || v.rdb == nil {
		return nil
	}
	return &valkeyEmailStore{v: v}
}

func (d *valkeyEmailStore) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), sharedOpTimeout)
}

func emailUnavailable(op string, err error) error {
	return fmt.Errorf("%w: %s: %v", emailauth.ErrUnavailable, op, err)
}

// emailTTLFor is how long a record may live: exactly until its own deadline, so the server
// enforces expiry as well as the flow does.
func emailTTLFor(r emailauth.Record) time.Duration {
	ttl := time.Until(r.Expires)
	if ttl <= 0 {
		return time.Millisecond
	}
	return ttl
}

// putScript REPLACES whatever code was outstanding for the address and resets its guessing
// budget. Replacement is what retires the previous code: a person who requests twice
// because the first mail was slow must not leave a live spare in their inbox.
var putScript = redis.NewScript(`
redis.call('HSET', KEYS[1], 'rec', ARGV[1], 'rev', ARGV[2])
redis.call('HDEL', KEYS[1], 'attempts')
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`)

func (d *valkeyEmailStore) Put(r emailauth.Record) error {
	// A monotonic revision per write. The clock is not usable here (two writes in the same
	// nanosecond would collide), so the revision comes from the server's own counter.
	ctx, cancel := d.ctx()
	defer cancel()
	rev, err := d.v.rdb.Incr(ctx, keyPrefix+"eml:rev").Result()
	if err != nil {
		d.v.noteErr("emailPutRev", err)
		return emailUnavailable("put", err)
	}
	r.Rev = rev
	r.Attempts = 0
	blob, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := putScript.Run(ctx, d.v.rdb,
		[]string{emailRecPrefix + r.AddrHash},
		blob, rev, emailTTLFor(r).Milliseconds()).Err(); err != nil {
		d.v.noteErr("emailPut", err)
		return emailUnavailable("put", err)
	}
	d.v.setUp(true)
	return nil
}

func (d *valkeyEmailStore) ByAddress(addrHash string) (emailauth.Record, bool, error) {
	ctx, cancel := d.ctx()
	defer cancel()
	raw, err := d.v.rdb.HGet(ctx, emailRecPrefix+addrHash, "rec").Bytes()
	if err == redis.Nil {
		d.v.setUp(true)
		return emailauth.Record{}, false, nil
	}
	if err != nil {
		d.v.noteErr("emailByAddress", err)
		return emailauth.Record{}, false, emailUnavailable("read", err)
	}
	d.v.setUp(true)
	var r emailauth.Record
	if err := json.Unmarshal(raw, &r); err != nil {
		// An unreadable record is not evidence that anybody holds this address.
		return emailauth.Record{}, false, emailUnavailable("read", err)
	}
	return r, true, nil
}

// consumeScript deletes the record only if the revision the caller read is still current.
// That single decision is what makes "exactly one submission spends the code" true across
// instances, rather than merely likely.
var consumeScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[1], 'rev')
if not cur then return 0 end
if cur ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
return 1
`)

func (d *valkeyEmailStore) Consume(r emailauth.Record) (bool, error) {
	ctx, cancel := d.ctx()
	defer cancel()
	res, err := consumeScript.Run(ctx, d.v.rdb,
		[]string{emailRecPrefix + r.AddrHash}, r.Rev).Int()
	if err != nil {
		d.v.noteErr("emailConsume", err)
		return false, emailUnavailable("consume", err)
	}
	d.v.setUp(true)
	return res == 1, nil
}

// penalizeScript increments the guessing budget ON the record, so retiring a code also
// clears its budget and a restart cannot refill one.
var penalizeEmailScript = redis.NewScript(`
local raw = redis.call('HGET', KEYS[1], 'rec')
if not raw then return 0 end
local n = tonumber(redis.call('HINCRBY', KEYS[1], 'attempts', 1))
return n
`)

func (d *valkeyEmailStore) Penalize(addrHash string, _ time.Duration) (int, error) {
	ctx, cancel := d.ctx()
	defer cancel()
	n, err := penalizeEmailScript.Run(ctx, d.v.rdb, []string{emailRecPrefix + addrHash}).Int()
	if err != nil {
		d.v.noteErr("emailPenalize", err)
		return 0, emailUnavailable("penalize", err)
	}
	d.v.setUp(true)
	if n == 0 {
		return 0, nil
	}
	// The attempt count lives in its own hash field so the increment is atomic; fold it
	// back into the stored record so a later read sees it.
	if err := d.syncAttempts(ctx, addrHash, n); err != nil {
		return n, err
	}
	return n, nil
}

// syncAttempts writes the atomically-incremented count back into the JSON record. The
// counter field is the authority during a burst of guesses; this keeps the record honest
// for the next reader.
func (d *valkeyEmailStore) syncAttempts(ctx context.Context, addrHash string, n int) error {
	raw, err := d.v.rdb.HGet(ctx, emailRecPrefix+addrHash, "rec").Bytes()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return emailUnavailable("penalize", err)
	}
	var r emailauth.Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return emailUnavailable("penalize", err)
	}
	r.Attempts = n
	blob, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := d.v.rdb.HSet(ctx, emailRecPrefix+addrHash, "rec", blob).Err(); err != nil {
		return emailUnavailable("penalize", err)
	}
	return nil
}

// allowScript is a fixed-window counter: increment, arm the expiry on first use, and report
// whether the caller is still inside the limit. One round trip, so two instances cannot
// both read "not yet at the limit" and both proceed.
var allowScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
if n > tonumber(ARGV[1]) then return 0 end
return 1
`)

func (d *valkeyEmailStore) allow(key string, limit int, window time.Duration) (bool, error) {
	ctx, cancel := d.ctx()
	defer cancel()
	res, err := allowScript.Run(ctx, d.v.rdb, []string{key}, limit, window.Milliseconds()).Int()
	if err != nil {
		d.v.noteErr("emailAllow", err)
		return false, emailUnavailable("rate limit", err)
	}
	d.v.setUp(true)
	return res == 1, nil
}

func (d *valkeyEmailStore) AllowRequest(addrHash, source string, perAddress, perSource int, window time.Duration, _ time.Time) (bool, error) {
	// The address budget is charged first and the source budget only if it passed, so a
	// blocked address does not also burn the sender's wider allowance.
	ok, err := d.allow(emailReqPrefix+addrHash, perAddress, window)
	if err != nil || !ok {
		return false, err
	}
	return d.allow(emailReqSrcPrefix+source, perSource, window)
}

func (d *valkeyEmailStore) AllowSubmit(source string, perSource int, window time.Duration, _ time.Time) (bool, error) {
	return d.allow(emailSubSrcPrefix+source, perSource, window)
}

// Reap is a no-op: every key here carries a PEXPIRE, so the server removes them without us
// scanning. A SCAN across a SHARED instance is exactly the whole-keyspace operation this
// package forbids.
func (d *valkeyEmailStore) Reap(time.Time) error { return nil }
