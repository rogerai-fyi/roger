package deviceauth

// Tests for the storage seam behind a pending device login, per
// features/auth/device_login_persistence.feature.
//
// The point of the seam is that a login belongs to the DEPLOYMENT, not to one process's
// memory. These tests exercise the contract every Store implementation must satisfy, and
// then drive a Flow through the failure modes a real store has and a map never did:
// unreachable, corrupt, and concurrently mutated by another instance.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- the Store contract ---------------------------------------------------

// storeFactories is every implementation that must satisfy the contract. The shared
// (Valkey-backed) implementation lives in the broker package and is exercised there
// against a real server; here we prove the contract itself and the memory default.
func storeFactories() map[string]func() Store {
	return map[string]func() Store{
		"memory": func() Store { return NewMemStore() },
	}
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sampleRecord(now time.Time) Record {
	return Record{
		DevHash:   hashOf("dev-code"),
		UserHash:  hashOf("USERCODE"),
		BoundKey:  "pubkey-A",
		Status:    StatusPending,
		Requested: now,
		Expires:   now.Add(10 * time.Minute),
		Interval:  5 * time.Second,
	}
}

func TestStoreContract(t *testing.T) {
	for name, newStore := range storeFactories() {
		t.Run(name, func(t *testing.T) {
			now := time.Now()

			t.Run("a created record is found by both of its indexes", func(t *testing.T) {
				s := newStore()
				rec := sampleRecord(now)
				require.NoError(t, s.Create(rec))

				byDev, ok, err := s.ByDevice(rec.DevHash)
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, rec.BoundKey, byDev.BoundKey)

				byUser, ok, err := s.ByUser(rec.UserHash)
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, rec.DevHash, byUser.DevHash)
			})

			t.Run("an absent record is a miss, not an error", func(t *testing.T) {
				s := newStore()
				_, ok, err := s.ByDevice(hashOf("nothing"))
				require.NoError(t, err)
				require.False(t, ok)

				_, ok, err = s.ByUser(hashOf("nothing"))
				require.NoError(t, err)
				require.False(t, ok)
			})

			t.Run("a stored record carries no plaintext code", func(t *testing.T) {
				// Scenario: the device code and the user code are stored only as hashes.
				// The Record type must have nowhere to put a plaintext code even by
				// accident, so this asserts on the round-tripped value.
				s := newStore()
				rec := sampleRecord(now)
				require.NoError(t, s.Create(rec))

				got, _, err := s.ByDevice(rec.DevHash)
				require.NoError(t, err)
				require.NotContains(t, got.DevHash, "dev-code")
				require.NotContains(t, got.UserHash, "USERCODE")
				require.Len(t, got.DevHash, 64, "a sha256 hex digest")
				require.Len(t, got.UserHash, 64, "a sha256 hex digest")
			})

			t.Run("CAS applies an update only against the revision it read", func(t *testing.T) {
				s := newStore()
				rec := sampleRecord(now)
				require.NoError(t, s.Create(rec))

				read, _, err := s.ByDevice(rec.DevHash)
				require.NoError(t, err)

				stale := read
				read.Status = StatusApproved
				read.Account = "acct-1"
				ok, err := s.CAS(read)
				require.NoError(t, err)
				require.True(t, ok, "the first writer wins")

				// The second writer read the same revision and must lose outright.
				stale.Status = StatusDenied
				ok, err = s.CAS(stale)
				require.NoError(t, err)
				require.False(t, ok, "a write against a superseded revision is refused")

				after, _, err := s.ByDevice(rec.DevHash)
				require.NoError(t, err)
				require.Equal(t, StatusApproved, after.Status)
				require.Equal(t, "acct-1", after.Account)
			})

			t.Run("CAS against a record that no longer exists reports no write", func(t *testing.T) {
				s := newStore()
				rec := sampleRecord(now)
				require.NoError(t, s.Create(rec))
				read, _, err := s.ByDevice(rec.DevHash)
				require.NoError(t, err)

				require.NoError(t, s.Delete(rec.DevHash))

				ok, err := s.CAS(read)
				require.NoError(t, err)
				require.False(t, ok)
			})

			t.Run("exactly one of many concurrent CAS writers wins", func(t *testing.T) {
				// Scenario: a code is consumed once across the whole deployment. Two
				// processes reading the same record and both deciding "not consumed yet"
				// is the classic double-spend, so consumption has to be one atomic
				// decision in the store.
				s := newStore()
				rec := sampleRecord(now)
				require.NoError(t, s.Create(rec))
				read, _, err := s.ByDevice(rec.DevHash)
				require.NoError(t, err)

				const racers = 16
				var wins int64
				var mu sync.Mutex
				var wg sync.WaitGroup
				for i := 0; i < racers; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						attempt := read
						attempt.Consumed = true
						ok, err := s.CAS(attempt)
						require.NoError(t, err)
						if ok {
							mu.Lock()
							wins++
							mu.Unlock()
						}
					}()
				}
				wg.Wait()
				require.EqualValues(t, 1, wins, "exactly one writer may consume the login")
			})

			t.Run("the budget counts per submitter and survives independently", func(t *testing.T) {
				s := newStore()
				for i := 1; i <= 3; i++ {
					n, err := s.Penalize("submitter-A", time.Minute)
					require.NoError(t, err)
					require.Equal(t, i, n, "the increment returns the new total")
				}
				got, err := s.Budget("submitter-A")
				require.NoError(t, err)
				require.Equal(t, 3, got)

				other, err := s.Budget("submitter-B")
				require.NoError(t, err)
				require.Zero(t, other, "one submitter's guessing never spends another's budget")
			})

			t.Run("reaping removes expired and consumed records and nothing else", func(t *testing.T) {
				s := newStore()
				live := sampleRecord(now)
				require.NoError(t, s.Create(live))

				expired := sampleRecord(now)
				expired.DevHash = hashOf("dev-expired")
				expired.UserHash = hashOf("USEREXPIRED")
				expired.Expires = now.Add(-time.Second)
				require.NoError(t, s.Create(expired))

				spent := sampleRecord(now)
				spent.DevHash = hashOf("dev-spent")
				spent.UserHash = hashOf("USERSPENT")
				spent.Consumed = true
				require.NoError(t, s.Create(spent))

				require.NoError(t, s.Reap(now))

				_, ok, err := s.ByDevice(expired.DevHash)
				require.NoError(t, err)
				require.False(t, ok, "an expired record is removed")

				_, ok, err = s.ByDevice(spent.DevHash)
				require.NoError(t, err)
				require.False(t, ok, "a consumed record is removed")

				_, ok, err = s.ByDevice(live.DevHash)
				require.NoError(t, err)
				require.True(t, ok, "a live record survives the reap")

				_, ok, err = s.ByUser(expired.UserHash)
				require.NoError(t, err)
				require.False(t, ok, "the user index is reaped alongside the device index")
			})
		})
	}
}

// --- a Flow driven against a failing store --------------------------------

// failingStore fails whichever operations the test arms. It is not a mock of behaviour -
// every method still delegates to a real memory store - it only injects the transport
// failure a network-backed store really has and a map never did.
type failingStore struct {
	Store
	mu                                    sync.Mutex
	failCreate, failRead, failCAS, broken bool
	corrupt                               bool
}

var errStoreDown = errors.New("store unreachable")

func newFailingStore() *failingStore { return &failingStore{Store: NewMemStore()} }

func (f *failingStore) arm(fn func(*failingStore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *failingStore) down() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.broken
}

func (f *failingStore) Create(r Record) error {
	f.mu.Lock()
	fail := f.failCreate || f.broken
	f.mu.Unlock()
	if fail {
		return errStoreDown
	}
	return f.Store.Create(r)
}

func (f *failingStore) ByDevice(h string) (Record, bool, error) {
	f.mu.Lock()
	fail, corrupt := f.failRead || f.broken, f.corrupt
	f.mu.Unlock()
	if fail {
		return Record{}, false, errStoreDown
	}
	if corrupt {
		return Record{}, false, ErrCorruptRecord
	}
	return f.Store.ByDevice(h)
}

func (f *failingStore) ByUser(h string) (Record, bool, error) {
	f.mu.Lock()
	fail, corrupt := f.failRead || f.broken, f.corrupt
	f.mu.Unlock()
	if fail {
		return Record{}, false, errStoreDown
	}
	if corrupt {
		return Record{}, false, ErrCorruptRecord
	}
	return f.Store.ByUser(h)
}

func (f *failingStore) CAS(r Record) (bool, error) {
	f.mu.Lock()
	fail := f.failCAS || f.broken
	f.mu.Unlock()
	if fail {
		return false, errStoreDown
	}
	return f.Store.CAS(r)
}

func TestFlowRefusesToIssueALoginItCannotRecord(t *testing.T) {
	// Scenario: a store outage refuses to start new logins rather than starting ones it
	// will lose. Issuing a code we cannot durably record is worse than refusing: the
	// person does all the work of approving and only then learns none of it counted.
	s := newFailingStore()
	s.arm(func(f *failingStore) { f.failCreate = true })
	flow := NewWithStore(Config{}, s)

	_, err := flow.Start("pubkey-A")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnavailable)
	require.NotErrorIs(t, err, errRejected, "an outage is not a rejection")
}

func TestPollDuringAnOutageIsRetryableNotInvalid(t *testing.T) {
	// Scenario: a store outage during a poll is reported as a retryable condition, and
	// the same code still completes when the store returns. The uniform "not valid"
	// rejection exists to deny a guesser signal; reusing it here would tell a legitimate
	// CLI its good code is bad.
	s := newFailingStore()
	flow := NewWithStore(Config{}, s)

	pending, err := flow.Start("pubkey-A")
	require.NoError(t, err)

	s.arm(func(f *failingStore) { f.broken = true })
	_, err = flow.Poll(pending.DeviceCode, "pubkey-A")
	require.ErrorIs(t, err, ErrUnavailable)
	require.NotErrorIs(t, err, errRejected)

	s.arm(func(f *failingStore) { f.broken = false })
	require.NoError(t, flow.Approve(pending.UserCode, "acct-1"))

	res, err := flow.Poll(pending.DeviceCode, "pubkey-A")
	require.NoError(t, err)
	require.Equal(t, StatusApproved, res.Status, "the same code still completes")
	require.Equal(t, "acct-1", res.Account)
}

func TestApprovalDuringAnOutageDoesNotReportSuccess(t *testing.T) {
	// Scenario: a store outage during approval does not report success, and the CLI's
	// next poll does not report an approval.
	s := newFailingStore()
	flow := NewWithStore(Config{}, s)
	pending, err := flow.Start("pubkey-A")
	require.NoError(t, err)

	s.arm(func(f *failingStore) { f.failCAS = true })
	require.ErrorIs(t, flow.Approve(pending.UserCode, "acct-1"), ErrUnavailable)

	s.arm(func(f *failingStore) { f.failCAS = false })
	res, err := flow.Poll(pending.DeviceCode, "pubkey-A")
	require.NoError(t, err)
	require.Equal(t, StatusPending, res.Status, "a failed approval is not an approval")
}

func TestACorruptRecordIsARefusalNeverAnApproval(t *testing.T) {
	// Scenario: a record that cannot be read is a refusal, never an approval.
	s := newFailingStore()
	flow := NewWithStore(Config{}, s)
	pending, err := flow.Start("pubkey-A")
	require.NoError(t, err)

	s.arm(func(f *failingStore) { f.corrupt = true })
	res, err := flow.Poll(pending.DeviceCode, "pubkey-A")
	require.Error(t, err)
	require.NotEqual(t, StatusApproved, res.Status)
	require.NotEqual(t, StatusDenied, res.Status)
}

func TestATamperedBoundKeyIsRefused(t *testing.T) {
	// Scenario: a record whose bound key does not match the poll is refused. The store is
	// the new tamper surface persistence introduces - "approval decides which ACCOUNT,
	// never which KEY" must hold against a store an operator can edit, not only against a
	// request an attacker can send.
	s := NewMemStore()
	flow := NewWithStore(Config{}, s)
	pending, err := flow.Start("pubkey-A")
	require.NoError(t, err)

	rec, ok, err := s.ByDevice(hashOf(pending.DeviceCode))
	require.NoError(t, err)
	require.True(t, ok)
	rec.BoundKey = "pubkey-ATTACKER"
	written, err := s.CAS(rec)
	require.NoError(t, err)
	require.True(t, written)

	_, err = flow.Poll(pending.DeviceCode, "pubkey-A")
	require.Error(t, err, "the issuing CLI's own poll is refused once the record disagrees")
}

func TestAPendingLoginSurvivesARestart(t *testing.T) {
	// Scenario: a pending login survives a broker restart, and an approval given before a
	// restart is still an approval after it. A "restart" is a brand-new Flow over the same
	// store - which is precisely what a redeployed process is.
	store := NewMemStore()
	first := NewWithStore(Config{}, store)

	pending, err := first.Start("pubkey-A")
	require.NoError(t, err)
	require.NoError(t, first.Approve(pending.UserCode, "acct-1"))

	restarted := NewWithStore(Config{}, store)
	res, err := restarted.Poll(pending.DeviceCode, "pubkey-A")
	require.NoError(t, err)
	require.Equal(t, StatusApproved, res.Status)
	require.Equal(t, "acct-1", res.Account)
	require.Equal(t, "pubkey-A", res.BoundKey)
}

func TestADenialSurvivesARestartAndCannotBecomeAnApproval(t *testing.T) {
	store := NewMemStore()
	first := NewWithStore(Config{}, store)
	pending, err := first.Start("pubkey-A")
	require.NoError(t, err)
	require.NoError(t, first.Deny(pending.UserCode, "acct-1"))

	restarted := NewWithStore(Config{}, store)
	require.Error(t, restarted.Approve(pending.UserCode, "acct-1"),
		"a denial is permanent across a restart")

	res, err := restarted.Poll(pending.DeviceCode, "pubkey-A")
	require.NoError(t, err)
	require.Equal(t, StatusDenied, res.Status)
}

func TestAConsumedCodeStaysConsumedAcrossARestart(t *testing.T) {
	// Scenario: single-use is what stops a captured device code being replayed later. If
	// "consumed" lives only in memory, a restart is a replay window.
	store := NewMemStore()
	first := NewWithStore(Config{}, store)
	pending, err := first.Start("pubkey-A")
	require.NoError(t, err)
	require.NoError(t, first.Approve(pending.UserCode, "acct-1"))
	res, err := first.Poll(pending.DeviceCode, "pubkey-A")
	require.NoError(t, err)
	require.Equal(t, StatusApproved, res.Status)

	restarted := NewWithStore(Config{}, store)
	_, err = restarted.Poll(pending.DeviceCode, "pubkey-A")
	require.Error(t, err, "a spent code is spent after a restart too")
}

func TestExpiryIsMeasuredAgainstTheIssuedDeadlineNotProcessUptime(t *testing.T) {
	store := NewMemStore()
	first := NewWithStore(Config{TTL: time.Minute}, store)
	pending, err := first.Start("pubkey-A")
	require.NoError(t, err)

	// The record's own deadline has passed; a fresh process must honour it rather than
	// treating its own start time as the clock.
	rec, ok, err := store.ByDevice(hashOf(pending.DeviceCode))
	require.NoError(t, err)
	require.True(t, ok)
	rec.Expires = time.Now().Add(-time.Second)
	written, err := store.CAS(rec)
	require.NoError(t, err)
	require.True(t, written)

	restarted := NewWithStore(Config{TTL: time.Minute}, store)
	res, err := restarted.Poll(pending.DeviceCode, "pubkey-A")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, res.Status, "a restart does not extend any login's life")
}

func TestTheWrongCodeBudgetSurvivesARestart(t *testing.T) {
	// Scenario: restarting is not a way to refill the guessing budget - and we deploy often.
	store := NewMemStore()
	first := NewWithStore(Config{MaxWrongCodes: 3}, store)
	for i := 0; i < 3; i++ {
		require.Error(t, first.Approve("NOTACODE", "attacker"))
	}

	restarted := NewWithStore(Config{MaxWrongCodes: 3}, store)
	pending, err := restarted.Start("pubkey-A")
	require.NoError(t, err)
	require.Error(t, restarted.Approve(pending.UserCode, "attacker"),
		"the budget continues from where it stood before the restart")

	// A different submitter is unaffected, exactly as before persistence.
	require.NoError(t, restarted.Approve(pending.UserCode, "innocent"))
}

func TestApprovalOnOneInstanceCompletesAPollOnAnother(t *testing.T) {
	// Scenario: two broker instances over one shared store. This is the case that is
	// simply broken today: the approval lands in one process's map and the poll reads
	// another's, so the CLI polls a pending login until it expires.
	store := NewMemStore()
	instanceA := NewWithStore(Config{}, store)
	instanceB := NewWithStore(Config{}, store)

	pending, err := instanceA.Start("pubkey-A")
	require.NoError(t, err)

	// The human's browser reaches instance B.
	info, ok := instanceB.Describe(pending.UserCode, "acct-1")
	require.True(t, ok, "the pending login is found on the instance that did not issue it")
	require.Equal(t, pending.UserCode, info.UserCode)

	require.NoError(t, instanceB.Approve(pending.UserCode, "acct-1"))

	// The CLI keeps polling instance A.
	res, err := instanceA.Poll(pending.DeviceCode, "pubkey-A")
	require.NoError(t, err)
	require.Equal(t, StatusApproved, res.Status)
	require.Equal(t, "pubkey-A", res.BoundKey, "the key bound is the key recorded at issue")
}

func TestACodeIsConsumedOnceAcrossTheDeployment(t *testing.T) {
	store := NewMemStore()
	instanceA := NewWithStore(Config{}, store)
	instanceB := NewWithStore(Config{}, store)

	pending, err := instanceA.Start("pubkey-A")
	require.NoError(t, err)
	require.NoError(t, instanceA.Approve(pending.UserCode, "acct-1"))

	type outcome struct {
		res Result
		err error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, inst := range []*Flow{instanceA, instanceB} {
		wg.Add(1)
		go func(f *Flow) {
			defer wg.Done()
			r, err := f.Poll(pending.DeviceCode, "pubkey-A")
			results <- outcome{r, err}
		}(inst)
	}
	wg.Wait()
	close(results)

	var approvals int
	for o := range results {
		if o.err == nil && o.res.Status == StatusApproved {
			approvals++
		}
	}
	require.Equal(t, 1, approvals, "exactly one poll reports the approval; the other is refused")
}

func TestTheWrongCodeBudgetIsSharedAcrossInstances(t *testing.T) {
	// Scenario: spreading guesses across instances does not multiply the allowance.
	store := NewMemStore()
	instanceA := NewWithStore(Config{MaxWrongCodes: 4}, store)
	instanceB := NewWithStore(Config{MaxWrongCodes: 4}, store)

	for i := 0; i < 2; i++ {
		require.Error(t, instanceA.Approve("NOTACODE", "attacker"))
		require.Error(t, instanceB.Approve("NOTACODE", "attacker"))
	}

	pending, err := instanceA.Start("pubkey-A")
	require.NoError(t, err)
	require.Error(t, instanceB.Approve(pending.UserCode, "attacker"),
		"the budget was spent across both instances, not twice over")
}

func TestConcurrentApprovalAndDenialSettleOnExactlyOne(t *testing.T) {
	// Scenario: an approval reaching one instance and a denial reaching another at the
	// same moment settle on exactly one outcome, and every later poll reports it.
	store := NewMemStore()
	instanceA := NewWithStore(Config{}, store)
	instanceB := NewWithStore(Config{}, store)

	pending, err := instanceA.Start("pubkey-A")
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = instanceA.Approve(pending.UserCode, "acct-1") }()
	go func() { defer wg.Done(); errs[1] = instanceB.Deny(pending.UserCode, "acct-1") }()
	wg.Wait()

	var settled int
	for _, err := range errs {
		if err == nil {
			settled++
		}
	}
	require.Equal(t, 1, settled, "exactly one of approval and denial takes effect")

	first, err := instanceA.Poll(pending.DeviceCode, "pubkey-A")
	require.NoError(t, err)
	require.Contains(t, []Status{StatusApproved, StatusDenied}, first.Status)
}
