package emailauth

// The paths a happy-path test never reaches: the defaults, the malformed domains, and
// every branch where the store says no. They matter because each one decides whether a
// person is told "your code is wrong" or "we are briefly unable to answer", and getting
// that backwards is how somebody concludes their account is broken.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTheDefaultConstructorIsUsableAndSafe(t *testing.T) {
	// A zero Config must still be safe: New is what production uses.
	f := New(Config{})
	require.NotNil(t, f)

	code, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)
	require.Len(t, code, defaultCodeLen)

	addr, err := f.Submit("someone@rogerai.fm", code, "src-1")
	require.NoError(t, err)
	require.Equal(t, "someone@rogerai.fm", addr)
}

func TestANilStoreStillYieldsAWorkingFlow(t *testing.T) {
	f := NewWithStore(Config{}, nil)
	code, err := f.Request("someone@rogerai.fm", "src-1")
	require.NoError(t, err)
	_, err = f.Submit("someone@rogerai.fm", code, "src-1")
	require.NoError(t, err)
}

func TestConfigFloorsApply(t *testing.T) {
	// Each floor exists so a zero or negative value cannot silently disable a control -
	// a MaxWrongCodes of 0 would otherwise mean "no guessing allowed ever", and a Window
	// of 0 would reset every rate limit on every call.
	c := Config{TTL: -1, MaxWrongCodes: -1, RequestsPerAddress: -1, RequestsPerSource: -1, SubmitsPerSource: -1, Window: -1}
	c.withDefaults()
	require.Positive(t, c.TTL)
	require.Positive(t, c.MaxWrongCodes)
	require.Positive(t, c.RequestsPerAddress)
	require.Positive(t, c.RequestsPerSource)
	require.Positive(t, c.SubmitsPerSource)
	require.Positive(t, c.Window)
}

func TestValidAddressRejectsMalformedDomains(t *testing.T) {
	for _, bad := range []string{
		"someone@.rogerai.fm",                    // leading dot
		"someone@rogerai.fm.",                    // trailing dot
		"someone@rogerai",                        // no dot at all: a local name, mail never leaves the host
		strings.Repeat("a", 250) + "@rogerai.fm", // past the RFC 5321 path cap
	} {
		require.False(t, ValidAddress(bad), "%q must be refused", bad)
	}
	require.True(t, ValidAddress("someone@rogerai.fm"))
	require.True(t, ValidAddress("a.b+tag@sub.rogerai.fm"))
}

func TestAnInvalidAddressOnSubmitGetsTheUniformRefusal(t *testing.T) {
	// Not ErrInvalidAddress: on the SUBMIT side an invalid address must not be a cheaper
	// way to probe than a valid one that has no outstanding code.
	f, _ := newFlow(t, Config{})
	_, err := f.Submit("not-an-address", "000000", "src-1")
	require.ErrorIs(t, err, ErrRejected)
	require.NotErrorIs(t, err, ErrInvalidAddress)
}

func TestUnavailablePassesThroughWithoutDoubleWrapping(t *testing.T) {
	require.ErrorIs(t, unavailable(ErrUnavailable), ErrUnavailable)
	wrapped := unavailable(errors.New("dial tcp: refused"))
	require.ErrorIs(t, wrapped, ErrUnavailable)
	require.Contains(t, wrapped.Error(), "refused", "the cause survives for an operator")
}

// refusingStore fails exactly one named operation, so each store-failure branch can be
// reached on its own rather than all at once.
type refusingStore struct {
	Store
	failAllowRequest bool
	failAllowSubmit  bool
	failPenalize     bool
	failConsume      bool
}

func (r *refusingStore) AllowRequest(a, s string, pa, ps int, w time.Duration, n time.Time) (bool, error) {
	if r.failAllowRequest {
		return false, ErrUnavailable
	}
	return r.Store.AllowRequest(a, s, pa, ps, w, n)
}

func (r *refusingStore) AllowSubmit(s string, ps int, w time.Duration, n time.Time) (bool, error) {
	if r.failAllowSubmit {
		return false, ErrUnavailable
	}
	return r.Store.AllowSubmit(s, ps, w, n)
}

func (r *refusingStore) Penalize(a string, ttl time.Duration) (int, error) {
	if r.failPenalize {
		return 0, ErrUnavailable
	}
	return r.Store.Penalize(a, ttl)
}

func (r *refusingStore) Consume(rec Record) (bool, error) {
	if r.failConsume {
		return false, ErrUnavailable
	}
	return r.Store.Consume(rec)
}

func TestEveryStoreFailureBranchReportsUnavailable(t *testing.T) {
	t.Run("the request budget cannot be charged", func(t *testing.T) {
		f := NewWithStore(Config{}, &refusingStore{Store: NewMemStore(), failAllowRequest: true})
		_, err := f.Request("someone@rogerai.fm", "src-1")
		require.ErrorIs(t, err, ErrUnavailable)
	})

	t.Run("the submit budget cannot be charged", func(t *testing.T) {
		f := NewWithStore(Config{}, &refusingStore{Store: NewMemStore(), failAllowSubmit: true})
		_, err := f.Submit("someone@rogerai.fm", "000000", "src-1")
		require.ErrorIs(t, err, ErrUnavailable)
	})

	t.Run("a wrong guess cannot be charged", func(t *testing.T) {
		rs := &refusingStore{Store: NewMemStore()}
		f := NewWithStore(Config{}, rs)
		_, err := f.Request("someone@rogerai.fm", "src-1")
		require.NoError(t, err)

		rs.failPenalize = true
		_, err = f.Submit("someone@rogerai.fm", "000000", "src-1")
		require.ErrorIs(t, err, ErrUnavailable,
			"if the guess cannot be counted, the guess must not be allowed to proceed silently")
	})

	t.Run("a correct code cannot be spent", func(t *testing.T) {
		rs := &refusingStore{Store: NewMemStore()}
		f := NewWithStore(Config{}, rs)
		code, err := f.Request("someone@rogerai.fm", "src-1")
		require.NoError(t, err)

		rs.failConsume = true
		_, err = f.Submit("someone@rogerai.fm", code, "src-1")
		require.ErrorIs(t, err, ErrUnavailable,
			"a code we cannot mark as spent must not sign anybody in - it would stay redeemable")
	})
}

func TestConsumingAnAbsentRecordReportsNoWrite(t *testing.T) {
	s := NewMemStore()
	won, err := s.Consume(Record{AddrHash: "nothing", Rev: 1})
	require.NoError(t, err)
	require.False(t, won)
}

func TestPenalizingAnAbsentRecordIsHarmless(t *testing.T) {
	// A guess against an address with no outstanding code has nothing to charge. It must
	// not error, because the per-source budget above it is what makes that guess costly.
	s := NewMemStore()
	n, err := s.Penalize("nothing", time.Minute)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestReapPrunesStaleRateWindows(t *testing.T) {
	// Without this the counter map grows for every address and every source ever seen.
	s := NewMemStore()
	now := time.Now()
	ok, err := s.AllowRequest(hashAddr("a@rogerai.fm"), "src-1", 5, 20, time.Hour, now)
	require.NoError(t, err)
	require.True(t, ok)

	ms, isMem := s.(*memStore)
	require.True(t, isMem)
	require.NotEmpty(t, ms.counts)

	require.NoError(t, s.Reap(now.Add(2*time.Hour)))
	require.Empty(t, ms.counts, "a window nobody has touched for an hour is dead weight")
}

func TestRandomCodeLengthIsHonoured(t *testing.T) {
	c, err := randomCode(4)
	require.NoError(t, err)
	require.Len(t, c, 4)
	require.Regexp(t, `^[0-9]{4}$`, c)
}
