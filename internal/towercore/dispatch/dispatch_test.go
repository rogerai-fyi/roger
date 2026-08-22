package dispatch

// dispatch_test.go is the spec for handing one unit of work to one Station and getting one
// verifiable answer back.
//
// The scenarios are features/tower/job_and_settlement.feature, and the ones this package is
// answerable for are named on each test. What is deliberately NOT here: money. The
// compensation ledger, funding reservations and the attempt-event chain the full spec binds
// a grant to do not exist, so nothing in this package settles, holds, or credits anything -
// see the package comment. A grant that pretended to authorize payment would be the single
// worst thing to get wrong here.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/towerobj"
)

const network = "roger-public"

func newRegistry(t *testing.T, now func() time.Time) (*Registry, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return New(Config{Network: network, Signer: priv, Now: now, Lifetime: time.Minute}), pub
}

func fixedClock(at *time.Time) func() time.Time { return func() time.Time { return *at } }

func target(stationKey ed25519.PublicKey) Target {
	return Target{
		TowerID: "tw-1", StationID: "st-1", StationEpoch: 3,
		Model: "m1", Modality: "text", AssertionKey: stationKey,
	}
}

func stationKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

// "Roger Core issues a one-use grant": the grant names exactly one Tower, one Station, one
// attempt, and commits to the exact request bytes.
func TestAGrantNamesOneStationAndOneRequest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, corePub := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)

	g, err := r.Issue(target(stPub), []byte(`{"prompt":"hi"}`))
	require.NoError(t, err)

	require.NotEmpty(t, g.AttemptID)
	require.NotEmpty(t, g.Nonce)
	require.Equal(t, "tw-1", g.TowerID)
	require.Equal(t, "st-1", g.StationID)
	require.Equal(t, int64(3), g.StationEpoch)
	require.Equal(t, now.Add(time.Minute), g.Deadline)

	// It commits to the REQUEST, not just to its length: the digest is what makes "the
	// Station received different plaintext" detectable at all.
	require.Equal(t, digestOf([]byte(`{"prompt":"hi"}`)), g.RequestDigest)

	// And it verifies under Core's key, which is what the Station checks before executing.
	require.NoError(t, towerobj.Verify(corePub, network, TypeGrant, Version, g.Signed, "core_sig"))
}

// Two grants are two different attempts, even for identical work. A shared attempt id would
// make "at most one attempt reaches executing state" unenforceable.
func TestEveryGrantIsItsOwnAttempt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)

	a, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)
	b, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)
	require.NotEqual(t, a.AttemptID, b.AttemptID)
	require.NotEqual(t, a.Nonce, b.Nonce)
}

// "Mutating any grant field invalidates authorization." Each of these is a field a relay
// would gain something by changing, and every one must break the signature.
func TestMutatingAnyGrantFieldInvalidatesIt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, corePub := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`{"prompt":"hi"}`))
	require.NoError(t, err)

	for _, field := range []string{
		"network", "type", "version", "job_id", "attempt_id", "tower_id", "station_id",
		"station_epoch", "model", "modality", "request_digest", "deadline", "nonce",
	} {
		var obj map[string]any
		require.NoError(t, json.Unmarshal(g.Signed, &obj))
		require.Contains(t, obj, field, "the grant must carry %q for it to be signed", field)
		obj[field] = "tampered"
		raw, merr := json.Marshal(obj)
		require.NoError(t, merr)
		require.Error(t,
			towerobj.Verify(corePub, network, TypeGrant, Version, raw, "core_sig"),
			"changing %q left the grant verifiable", field)
	}
}

// "A Station accepts a valid grant once" / "an exact sequential replay is rejected before
// executing again."
func TestAGrantIsClaimedExactlyOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)

	claimed, err := r.Claim(g.AttemptID, "tw-1")
	require.NoError(t, err)
	require.Equal(t, g.AttemptID, claimed.AttemptID)

	_, err = r.Claim(g.AttemptID, "tw-1")
	require.ErrorIs(t, err, ErrAlreadyClaimed)
}

// "Concurrent replay executes at most once." A mutex the claim path forgets is invisible in
// a sequential test and is the entire failure mode here.
func TestConcurrentClaimsExecuteAtMostOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)

	const racers = 16
	var wg sync.WaitGroup
	won := make(chan struct{}, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, cerr := r.Claim(g.AttemptID, "tw-1"); cerr == nil {
				won <- struct{}{}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(won)
	require.Len(t, won, 1, "exactly one claim may win")
}

// A grant belongs to the Tower it was issued for. Another Tower holding the attempt id -
// which is not a secret - must get nothing.
func TestAnotherTowerCannotClaimTheGrant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)

	_, err = r.Claim(g.AttemptID, "tw-someone-else")
	require.ErrorIs(t, err, ErrNotFound)

	// And the rightful Tower can still claim it: the wrong one did not consume it.
	_, err = r.Claim(g.AttemptID, "tw-1")
	require.NoError(t, err)
}

// Past its deadline a grant is dead: not claimable, not completable. The deadline is the
// only thing bounding how long a Station may sit on work nobody is waiting for any more.
func TestAnExpiredGrantIsDead(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, stPriv := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)

	claimed, err := r.Claim(g.AttemptID, "tw-1")
	require.NoError(t, err)

	now = now.Add(2 * time.Minute)
	_, err = r.Complete(g.AttemptID, receiptFor(t, stPriv, claimed, []byte(`answer`)), []byte(`answer`))
	require.ErrorIs(t, err, ErrExpired)

	// A fresh grant issued after the clock moved is still fine - expiry is per attempt.
	g2, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)
	_, err = r.Claim(g2.AttemptID, "tw-1")
	require.NoError(t, err)
}

// "A result body must match the signed response digest." The Tower is the party holding the
// bytes and the one with something to gain from changing them.
func TestAResultMustMatchTheSignedResponseDigest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, stPriv := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)
	claimed, err := r.Claim(g.AttemptID, "tw-1")
	require.NoError(t, err)

	body := []byte(`the answer`)
	rec := receiptFor(t, stPriv, claimed, body)

	for name, altered := range map[string][]byte{
		"truncated":   []byte(`the answe`),
		"appended":    []byte(`the answer `),
		"prefixed":    []byte(` the answer`),
		"substituted": []byte(`a different answer`),
		"empty":       {},
	} {
		_, cerr := r.Complete(g.AttemptID, rec, altered)
		require.ErrorIs(t, cerr, ErrResultMismatch, "%s bytes were accepted", name)
	}

	// The exact bytes settle.
	done, err := r.Complete(g.AttemptID, rec, body)
	require.NoError(t, err)
	require.Equal(t, g.AttemptID, done.AttemptID)
}

// "A Tower cannot forge a Station assertion." The Tower holds a perfectly valid identity of
// its own, and that must buy it nothing here.
func TestATowerCannotForgeTheStationsReceipt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	_, towerPriv := stationKeys(t) // the Tower's own good key
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)
	claimed, err := r.Claim(g.AttemptID, "tw-1")
	require.NoError(t, err)

	body := []byte(`fabricated`)
	forged := receiptFor(t, towerPriv, claimed, body)

	_, err = r.Complete(g.AttemptID, forged, body)
	require.ErrorIs(t, err, ErrReceiptSignature)
}

// "A receipt for request B posted as the result for job A cannot settle either." The receipt
// names its attempt, and a receipt for another one is a context mismatch however valid its
// signature is.
func TestAReceiptForAnotherAttemptIsRefused(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, stPriv := stationKeys(t)

	a, err := r.Issue(target(stPub), []byte(`a`))
	require.NoError(t, err)
	b, err := r.Issue(target(stPub), []byte(`b`))
	require.NoError(t, err)
	claimedA, err := r.Claim(a.AttemptID, "tw-1")
	require.NoError(t, err)
	_, err = r.Claim(b.AttemptID, "tw-1")
	require.NoError(t, err)

	body := []byte(`answer`)
	receiptForA := receiptFor(t, stPriv, claimedA, body)

	// Posted as B's result. Perfectly signed, for the wrong attempt.
	_, err = r.Complete(b.AttemptID, receiptForA, body)
	require.ErrorIs(t, err, ErrContextMismatch)

	// And A is untouched by the attempt to misuse its receipt.
	_, err = r.Complete(a.AttemptID, receiptForA, body)
	require.NoError(t, err)
}

// A result can only follow a claim, and only once. "At most one result can later settle."
func TestAResultNeedsAClaimAndSettlesOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, stPriv := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)

	body := []byte(`answer`)
	// Before any claim there is nothing executing to have produced a result.
	early := Receipt{AttemptID: g.AttemptID}
	_, err = r.Complete(g.AttemptID, early, body)
	require.ErrorIs(t, err, ErrNotClaimed)

	claimed, err := r.Claim(g.AttemptID, "tw-1")
	require.NoError(t, err)
	rec := receiptFor(t, stPriv, claimed, body)

	_, err = r.Complete(g.AttemptID, rec, body)
	require.NoError(t, err)
	_, err = r.Complete(g.AttemptID, rec, body)
	require.ErrorIs(t, err, ErrAlreadySettled)
}

// An unknown attempt is not an error to be guessed at: it is refused the same way whether it
// never existed or has already been reaped, because the difference is not the caller's to
// learn from probing.
func TestAnUnknownAttemptIsRefusedUniformly(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))

	_, err := r.Claim("att-nobody", "tw-1")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.Complete("att-nobody", Receipt{AttemptID: "att-nobody"}, []byte(`x`))
	require.ErrorIs(t, err, ErrNotFound)
}

// Issuing refuses what it cannot bind. Each of these would produce a grant that authorizes
// something ambiguous.
func TestIssuingRefusesAnUnbindableTarget(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)

	for name, mut := range map[string]func(*Target){
		"no tower":         func(tg *Target) { tg.TowerID = "" },
		"no station":       func(tg *Target) { tg.StationID = "" },
		"no model":         func(tg *Target) { tg.Model = "" },
		"no modality":      func(tg *Target) { tg.Modality = "" },
		"no assertion key": func(tg *Target) { tg.AssertionKey = nil },
	} {
		tg := target(stPub)
		mut(&tg)
		_, err := r.Issue(tg, []byte(`x`))
		require.Error(t, err, name)
	}

	// And an empty request body: a grant committing to the digest of nothing would let any
	// empty request be substituted for any other.
	_, err := r.Issue(target(stPub), nil)
	require.Error(t, err)
}

// Reaping bounds the table. An attempt registry that only grows is a memory leak with a
// deadline attached, and the deadline is what makes it safe to drop them.
func TestSettledAndExpiredAttemptsAreReaped(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)

	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)
	require.Equal(t, 1, r.Pending())

	now = now.Add(2 * time.Minute)

	// THE HORIZON IS THE CALLER'S, and a cutoff behind the record's deadline takes nothing.
	// This is the arm that makes the assertion below mean something: without it a Reap that
	// deleted the whole table on any cutoff would pass exactly as well.
	n, err := r.Reap(now.Add(-time.Hour))
	require.NoError(t, err)
	require.Zero(t, n, "a cutoff behind the deadline reaps nothing")
	require.Equal(t, 1, r.Pending(), "and the dead-but-retained attempt is still there")

	n, err = r.Reap(now)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Zero(t, r.Pending())

	// And the reaped attempt is gone rather than resurrectable.
	_, err = r.Claim(g.AttemptID, "tw-1")
	require.ErrorIs(t, err, ErrNotFound)
}

// receiptFor is what a Station produces: its signature over the attempt it was granted and
// the exact bytes it is returning.
func receiptFor(t *testing.T, priv ed25519.PrivateKey, g Grant, body []byte) Receipt {
	t.Helper()
	rec, err := SignReceipt(priv, network, g, []byte("req-"+"x"), body, Usage{In: 1, Out: int64(len(body))}, Usage{})
	require.NoError(t, err)
	return rec
}

// A zero Config is still safe: an unbounded attempt lifetime would mean a Station could hold
// work forever, and a nil clock would panic on the first grant.
func TestAZeroConfigIsStillBounded(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	r := New(Config{Network: network, Signer: priv})
	stPub, _ := stationKeys(t)

	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)
	require.True(t, g.Deadline.After(time.Now()), "a grant with no deadline is never dead")
	require.True(t, g.Deadline.Before(time.Now().Add(time.Hour)), "and the default must be short")
	require.NoError(t, towerobj.Verify(pub, network, TypeGrant, Version, g.Signed, "core_sig"))
}

// A settled attempt cannot be claimed again either. Complete closes it against the claim
// path as well as against itself - otherwise a second Tower could pick up work whose answer
// has already been accepted.
func TestASettledAttemptCannotBeClaimedAgain(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, stPriv := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)

	claimedGrant, err := r.Claim(g.AttemptID, "tw-1")
	require.NoError(t, err)
	_, err = r.Complete(g.AttemptID, receiptFor(t, stPriv, claimedGrant, []byte(`a`)), []byte(`a`))
	require.NoError(t, err)

	_, err = r.Claim(g.AttemptID, "tw-1")
	require.ErrorIs(t, err, ErrAlreadySettled)
}

// Claiming past the deadline is refused too, not only completing. A Tower that polls slowly
// must not start work Core has already stopped waiting for.
func TestAnExpiredAttemptCannotBeClaimed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)

	now = now.Add(2 * time.Minute)
	_, err = r.Claim(g.AttemptID, "tw-1")
	require.ErrorIs(t, err, ErrExpired)
}

// Reaping leaves live attempts alone. A sweep that took everything would be a much worse bug
// than one that took nothing, and both look like "the table is bounded" from outside.
func TestReapingSparesLiveAttempts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)

	old, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)
	now = now.Add(90 * time.Second) // past the first, before the second
	fresh, err := r.Issue(target(stPub), []byte(`y`))
	require.NoError(t, err)

	n, err := r.Reap(now)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, 1, r.Pending())
	_, err = r.Claim(fresh.AttemptID, "tw-1")
	require.NoError(t, err, "the live attempt survived the sweep")
	_, err = r.Claim(old.AttemptID, "tw-1")
	require.ErrorIs(t, err, ErrNotFound)
}

// SignReceipt refuses a key that is not a key rather than producing something that will fail
// verification much later, at a distance from the mistake.
func TestSigningAReceiptNeedsARealKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	g, err := r.Issue(target(stPub), []byte(`x`))
	require.NoError(t, err)

	require.Panics(t, func() {
		_, _ = SignReceipt(ed25519.PrivateKey("short"), network, g, []byte(`r`), []byte(`a`), Usage{}, Usage{})
	},
		"ed25519 panics on a malformed key; catching it here names the cause")
}

// --- the Station's side of the grant ---------------------------------------

// The happy path, and the shape the Station relies on: a grant it can verify, addressed to
// it, over the exact bytes it was handed.
func TestAStationAcceptsAGrantAddressedToItOverTheseBytes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, corePub := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	request := []byte(`{"prompt":"hi"}`)

	g, err := r.Issue(target(stPub), request)
	require.NoError(t, err)

	got, err := ParseGrant(g.Signed, corePub, network, "st-1", request, now)
	require.NoError(t, err)
	require.Equal(t, g.AttemptID, got.AttemptID)
	require.Equal(t, g.JobID, got.JobID)
	require.Equal(t, "tw-1", got.TowerID)
	require.Equal(t, int64(3), got.StationEpoch)
	require.Equal(t, "m1", got.Model)
	require.Equal(t, g.Deadline, got.Deadline)
}

// EVERY REASON A STATION MUST REFUSE. Each is something a relay is positioned to do, and the
// Station is the last place any of them can be caught - it is about to spend real compute and
// then sign for the result.
func TestAStationRefusesAGrantItCannotTrust(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, corePub := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	otherPub, _ := stationKeys(t)
	request := []byte(`{"prompt":"hi"}`)
	g, err := r.Issue(target(stPub), request)
	require.NoError(t, err)

	t.Run("not signed by Core", func(t *testing.T) {
		// The relay's own perfectly good key is not Core's.
		_, err := ParseGrant(g.Signed, otherPub, network, "st-1", request, now)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not signed by Roger Core")
	})

	t.Run("for another Station", func(t *testing.T) {
		// A valid grant, pointed at the wrong machine by the relay holding it.
		_, err := ParseGrant(g.Signed, corePub, network, "st-somebody-else", request, now)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not this one")
	})

	t.Run("for another network", func(t *testing.T) {
		// Refused by the SIGNATURE rather than by a field comparison: towerobj binds the
		// network into the signed bytes, so a grant for another network cannot verify here
		// at all.
		_, err := ParseGrant(g.Signed, corePub, "some-other-network", "st-1", request, now)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not signed by Roger Core")
	})

	t.Run("past its deadline", func(t *testing.T) {
		_, err := ParseGrant(g.Signed, corePub, network, "st-1", request, now.Add(2*time.Minute))
		require.ErrorIs(t, err, ErrExpired)
	})

	t.Run("over different bytes", func(t *testing.T) {
		// THE ONE THAT MATTERS MOST. A relay passing a real grant alongside a request of its
		// own choosing would otherwise get a Station to sign for work nobody authorized.
		_, err := ParseGrant(g.Signed, corePub, network, "st-1", []byte(`{"prompt":"something else"}`), now)
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not match what this grant authorizes")
	})

	t.Run("not a grant at all", func(t *testing.T) {
		_, err := ParseGrant([]byte("{nope"), corePub, network, "st-1", request, now)
		require.Error(t, err)
	})
}

// A grant whose numeric members are not numbers is refused rather than defaulted. A deadline
// that failed to parse and silently became zero would be a grant that is always expired -
// or, with the comparison the other way round, one that never is.
func TestAGrantWithUnreadableNumbersIsRefused(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, corePub := newRegistry(t, fixedClock(&now))
	stPub, _ := stationKeys(t)
	request := []byte(`x`)

	for _, member := range []string{"station_epoch", "deadline"} {
		g, err := r.Issue(target(stPub), request)
		require.NoError(t, err)

		// Re-signed after the edit, so this tests the PARSE rather than the signature: a
		// tampered grant is already covered, and would mask this.
		var obj map[string]any
		require.NoError(t, json.Unmarshal(g.Signed, &obj))
		delete(obj, "core_sig")
		obj[member] = "not-a-number"
		body, merr := json.Marshal(obj)
		require.NoError(t, merr)
		resigned, serr := towerobj.Sign(coreSignerOf(t, r), network, TypeGrant, Version, body, "core_sig")
		require.NoError(t, serr)

		_, err = ParseGrant(resigned, corePub, network, "st-1", request, now)
		require.Error(t, err, member)
	}
}

// coreSignerOf reaches the registry's key so a test can produce a grant Core would never
// write but that is nonetheless correctly signed.
func coreSignerOf(t *testing.T, r *Registry) ed25519.PrivateKey {
	t.Helper()
	return r.cfg.Signer
}
