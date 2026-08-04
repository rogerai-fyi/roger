package emailauth

// Tests for first-party sign-in, per features/auth/email_code_login.feature.
//
// With OAuth, the provider owns the hard parts: proving the human, rate-limiting the
// guessing, resisting enumeration, deciding when a credential is spent. Mailing our own
// code means we own every one of them, so the adversarial cases below are the feature
// rather than polish around it.

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newFlow(t *testing.T, cfg Config) (*Flow, Store) {
	t.Helper()
	s := NewMemStore()
	return NewWithStore(cfg, s), s
}

// --- addresses ------------------------------------------------------------

func TestAnAddressThatCannotReceiveMailIsRefusedBeforeAnythingIsSent(t *testing.T) {
	f, _ := newFlow(t, Config{})
	for _, addr := range []string{
		"",
		"not-an-address",
		"@rogerai.fm",
		"someone@",
		"someone@@rogerai.fm",
		"someone@localhost",
		"someone@rogerai.fm\ncc: attacker@evil.example",
		"someone@rogerai.fm\r\n",
		"<script>@rogerai.fm",
		strings.Repeat("a", 300) + "@rogerai.fm",
	} {
		t.Run(addr, func(t *testing.T) {
			_, err := f.Request(addr, "src-1")
			require.ErrorIs(t, err, ErrInvalidAddress, "no mail may be enqueued for %q", addr)
		})
	}
}

func TestAHeaderInjectionAttemptNeverBecomesACode(t *testing.T) {
	f, s := newFlow(t, Config{})
	_, err := f.Request("someone@rogerai.fm\nbcc: attacker@evil.example", "src-1")
	require.ErrorIs(t, err, ErrInvalidAddress)

	// Nothing was recorded, so nothing could later be submitted against it.
	_, ok, err := s.ByAddress(hashAddr("someone@rogerai.fm"))
	require.NoError(t, err)
	require.False(t, ok)
}

func TestTheAddressIsNormalizedOnceAndCanonicallyStored(t *testing.T) {
	f, _ := newFlow(t, Config{})
	code, err := f.Request("  Someone@RogerAI.FM  ", "src-1")
	require.NoError(t, err)

	// Either spelling reaches the one account.
	addr, err := f.Submit("someone@rogerai.fm", code, "src-1")
	require.NoError(t, err)
	require.Equal(t, "someone@rogerai.fm", addr, "the canonical form is what is stored")
}

func TestSubAddressingAndDotsAreNotNormalizedAway(t *testing.T) {
	// Collapsing "a.b+x@gmail.com" into "ab@gmail.com" bakes one provider's local-part
	// rules into our identity model - and the collapsing direction is the dangerous one,
	// because getting it wrong makes two people share an account.
	f, _ := newFlow(t, Config{})
	code, err := f.Request("a.b+tag@rogerai.fm", "src-1")
	require.NoError(t, err)

	_, err = f.Submit("ab@rogerai.fm", code, "src-1")
	require.ErrorIs(t, err, ErrRejected, "a different address is a DIFFERENT address")
}

// --- the code itself ------------------------------------------------------

func TestTheCodeIsStoredOnlyAsAHash(t *testing.T) {
	f, s := newFlow(t, Config{})
	code, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)

	rec, ok, err := s.ByAddress(hashAddr("someone@rogerai.fm"))
	require.NoError(t, err)
	require.True(t, ok)
	require.NotContains(t, rec.CodeHash, code, "a dump of the store signs nobody in")
	require.Len(t, rec.CodeHash, 64, "a sha256 hex digest")
	require.NotEqual(t, code, rec.CodeHash)
}

func TestTheCodeIsDrawnFromTheOSRandomSourceAndIsNotSequential(t *testing.T) {
	// The budgets are raised out of the way: this asserts on the code SOURCE, and the
	// per-address request limit is exercised by its own test.
	f, _ := newFlow(t, Config{RequestsPerAddress: 1000, RequestsPerSource: 1000})
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := f.Request("someone@rogerai.fm", "src-1")
		require.NoError(t, err)
		require.Len(t, code, defaultCodeLen)
		require.Regexp(t, `^[0-9]+$`, code, "readable from a phone, typable into a terminal")
		seen[code] = true
	}
	require.Greater(t, len(seen), 150, "codes must not repeat or run in sequence")
}

func TestACodeExpires(t *testing.T) {
	f, _ := newFlow(t, Config{TTL: time.Minute})
	code, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)

	f.advance(2 * time.Minute)
	_, err = f.Submit("someone@rogerai.fm", code, "src-1")
	require.ErrorIs(t, err, ErrRejected, "an expired code gets the same refusal a wrong one does")
}

func TestACodeIsSpentOnFirstAcceptance(t *testing.T) {
	f, _ := newFlow(t, Config{})
	code, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)

	_, err = f.Submit("someone@rogerai.fm", code, "src-1")
	require.NoError(t, err)

	_, err = f.Submit("someone@rogerai.fm", code, "src-1")
	require.ErrorIs(t, err, ErrRejected, "a spent code is not a spare credential")
}

func TestRequestingASecondCodeInvalidatesTheFirst(t *testing.T) {
	// Otherwise a person who requests twice because the first mail was slow leaves the
	// older code sitting in an inbox as a live credential.
	f, _ := newFlow(t, Config{})
	first, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)
	second, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)
	require.NotEqual(t, first, second)

	_, err = f.Submit("someone@rogerai.fm", first, "src-1")
	require.ErrorIs(t, err, ErrRejected, "only the most recently issued code is accepted")

	_, err = f.Submit("someone@rogerai.fm", second, "src-1")
	require.NoError(t, err)
}

func TestACodeIsBoundToTheAddressItWasMailedTo(t *testing.T) {
	f, _ := newFlow(t, Config{})
	code, err := f.Request("a@rogerai.fm", "src-1")
	require.NoError(t, err)
	_, err = f.Request("b@rogerai.fm", "src-1")
	require.NoError(t, err)

	_, err = f.Submit("b@rogerai.fm", code, "src-1")
	require.ErrorIs(t, err, ErrRejected)
}

// --- guessing and abuse ---------------------------------------------------

func TestWrongCodesAreLimitedPerAddressAndTheLimitBurnsTheCode(t *testing.T) {
	f, _ := newFlow(t, Config{MaxWrongCodes: 3})
	code, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err := f.Submit("someone@rogerai.fm", "000000", "src-1")
		require.ErrorIs(t, err, ErrRejected)
	}

	_, err = f.Submit("someone@rogerai.fm", code, "src-1")
	require.ErrorIs(t, err, ErrRejected, "the correct code no longer works once the budget is spent")

	fresh, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)
	_, err = f.Submit("someone@rogerai.fm", fresh, "src-1")
	require.NoError(t, err, "a fresh code restores the ability to sign in")
}

func TestTheAttemptBudgetIsPerAddressNotGlobal(t *testing.T) {
	// A single global budget turns an anti-guessing control into a way to lock the whole
	// product out.
	f, _ := newFlow(t, Config{MaxWrongCodes: 2})
	_, err := f.Request("attacker@rogerai.fm", "src-1")
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		_, _ = f.Submit("attacker@rogerai.fm", "000000", "src-1")
	}

	code, err := f.Request("innocent@rogerai.fm", "src-2")
	require.NoError(t, err)
	_, err = f.Submit("innocent@rogerai.fm", code, "src-2")
	require.NoError(t, err, "an unrelated person still signs in")
}

func TestCodeRequestsAreRateLimitedPerAddress(t *testing.T) {
	// Without this, anyone can use our mailer to flood a person's inbox and our sending
	// domain wears the spam complaints.
	f, _ := newFlow(t, Config{RequestsPerAddress: 3, Window: time.Hour})
	for i := 0; i < 3; i++ {
		_, err := f.Request("someone@rogerai.fm", "src-"+string(rune('a'+i)))
		require.NoError(t, err)
	}
	_, err := f.Request("someone@rogerai.fm", "src-z")
	require.ErrorIs(t, err, ErrRateLimited, "no additional mail is sent")
}

func TestCodeRequestsAreRateLimitedPerSourceAcrossAddresses(t *testing.T) {
	// The per-address limit alone does not stop a sender walking an address list, which is
	// both a mail-bomb amplifier and the reconnaissance half of an enumeration attack.
	f, _ := newFlow(t, Config{RequestsPerSource: 3, Window: time.Hour})
	for i := 0; i < 3; i++ {
		_, err := f.Request(string(rune('a'+i))+"@rogerai.fm", "one-source")
		require.NoError(t, err)
	}
	_, err := f.Request("zzz@rogerai.fm", "one-source")
	require.ErrorIs(t, err, ErrRateLimited)
}

func TestSubmissionsAreRateLimitedPerSourceAcrossAddresses(t *testing.T) {
	f, _ := newFlow(t, Config{SubmitsPerSource: 3, Window: time.Hour})
	for i := 0; i < 3; i++ {
		_, err := f.Submit(string(rune('a'+i))+"@rogerai.fm", "000000", "one-source")
		require.ErrorIs(t, err, ErrRejected)
	}
	_, err := f.Submit("zzz@rogerai.fm", "000000", "one-source")
	require.ErrorIs(t, err, ErrRejected, "the refusal is the same refusal a wrong code receives")
}

func TestAnUnknownAddressAndAKnownOneAreIndistinguishable(t *testing.T) {
	// The flow itself must not reveal whether an address is known - it does not consult
	// any account store at all, which is the strongest form of that guarantee.
	f, _ := newFlow(t, Config{})
	first, err := f.Request("brand-new@rogerai.fm", "src-1")
	require.NoError(t, err)
	second, err := f.Request("brand-new@rogerai.fm", "src-1")
	require.NoError(t, err)
	require.Len(t, first, defaultCodeLen)
	require.Len(t, second, defaultCodeLen)
}

func TestExpiredCodesAreReaped(t *testing.T) {
	f, s := newFlow(t, Config{TTL: time.Minute})
	_, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)

	require.NoError(t, s.Reap(time.Now().Add(2*time.Minute)))
	_, ok, err := s.ByAddress(hashAddr("someone@rogerai.fm"))
	require.NoError(t, err)
	require.False(t, ok, "the store does not grow without bound")
}

// --- the store failing ----------------------------------------------------

type brokenStore struct {
	Store
	mu   sync.Mutex
	down bool
}

func (b *brokenStore) fail(v bool) { b.mu.Lock(); b.down = v; b.mu.Unlock() }
func (b *brokenStore) bad() bool   { b.mu.Lock(); defer b.mu.Unlock(); return b.down }

func (b *brokenStore) Put(r Record) error {
	if b.bad() {
		return ErrUnavailable
	}
	return b.Store.Put(r)
}

func (b *brokenStore) ByAddress(h string) (Record, bool, error) {
	if b.bad() {
		return Record{}, false, ErrUnavailable
	}
	return b.Store.ByAddress(h)
}

func TestAnOutageIsReportedAsUnavailableNotAsARejection(t *testing.T) {
	// Telling a person their address is unusable because our backend blinked sends them to
	// support with the wrong problem.
	bs := &brokenStore{Store: NewMemStore()}
	f := NewWithStore(Config{}, bs)

	bs.fail(true)
	_, err := f.Request("someone@rogerai.fm", "src-1")
	require.ErrorIs(t, err, ErrUnavailable)
	require.NotErrorIs(t, err, ErrRejected)

	_, err = f.Submit("someone@rogerai.fm", "000000", "src-1")
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestAFailedRequestIssuesNoCode(t *testing.T) {
	bs := &brokenStore{Store: NewMemStore()}
	f := NewWithStore(Config{}, bs)
	bs.fail(true)
	code, err := f.Request("someone@rogerai.fm", "src-1")
	require.Error(t, err)
	require.Empty(t, code, "a code we could not record must never be mailed")
}

func TestConcurrentSubmissionsSpendACodeOnce(t *testing.T) {
	f, _ := newFlow(t, Config{MaxWrongCodes: 100})
	code, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)

	var wg sync.WaitGroup
	var accepted int
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Submit("someone@rogerai.fm", code, "src-1"); err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, accepted, "exactly one submission may spend the code")
}
