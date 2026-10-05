package main

// cap_notice_query_count_test.go pins the cost of the monthly-cap notice on the hot paid path:
// once a threshold's once-a-month notice has been sent, further paid requests above that
// threshold do no owner lookups for it, and a paid request reads the account's cap once (the
// pre-hold check), not again after the settle. Runs on the real broker and store (in-memory, or
// Postgres when ROGERAI_TEST_DATABASE_URL is set).

import (
	"sync/atomic"
	"testing"

	"rogerai.fm/roger/v6/internal/store"
)

// capNoticeCountingStore counts the store reads the cap-notice path can make.
type capNoticeCountingStore struct {
	store.Store
	capReads, ownerReads int64
}

func (c *capNoticeCountingStore) MonthlyCapOf(holder string) (float64, error) {
	atomic.AddInt64(&c.capReads, 1)
	return c.Store.MonthlyCapOf(holder)
}

func (c *capNoticeCountingStore) OwnerByPubkey(p string) (store.Owner, bool, error) {
	atomic.AddInt64(&c.ownerReads, 1)
	return c.Store.OwnerByPubkey(p)
}

func (c *capNoticeCountingStore) OwnerByLogin(l string) (store.Owner, bool, error) {
	atomic.AddInt64(&c.ownerReads, 1)
	return c.Store.OwnerByLogin(l)
}

func (c *capNoticeCountingStore) OwnerByVerifiedEmail(e string) (store.Owner, bool, error) {
	atomic.AddInt64(&c.ownerReads, 1)
	return c.Store.OwnerByVerifiedEmail(e)
}

func (c *capNoticeCountingStore) OwnerByAppleSub(s string) (store.Owner, bool, error) {
	atomic.AddInt64(&c.ownerReads, 1)
	return c.Store.OwnerByAppleSub(s)
}

func newCapNoticeHarness(t *testing.T) *cnState {
	t.Helper()
	st := &cnState{eaState: &eaState{foState: &foState{t: t, logs: &utLog{}}}}
	if err := st.reset(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.teardown)
	if err := st.paidStationOnAir("n-1", "qwen3-32b"); err != nil {
		t.Fatal(err)
	}
	return st
}

// relayCounting relays one paid request through a counting wrapper and returns the reads.
func (s *cnState) relayCounting(t *testing.T) (capReads, ownerReads int64) {
	t.Helper()
	real := s.b.db
	c := &capNoticeCountingStore{Store: real}
	s.b.db = c
	defer func() { s.b.db = real }()
	if rec := s.relayPaid(s.acctKey, ""); rec.Code != 200 {
		t.Fatalf("paid relay = %d %s", rec.Code, rec.Body.String())
	}
	s.settled()
	return atomic.LoadInt64(&c.capReads), atomic.LoadInt64(&c.ownerReads)
}

func TestCapNoticeAddsNoOwnerLookupsOnceTheThresholdIsClaimed(t *testing.T) {
	s := newCapNoticeHarness(t)
	if err := s.accountWithCapSpent("GitHub-linked", "hot@example.com", "10.00", "1.00"); err != nil {
		t.Fatal(err)
	}
	// Baseline: a paid request well below 80% (no notice path at all), taken after one warm-up
	// request so the per-key wallet cache is already filled and does not count as a lookup.
	s.relayCounting(t)
	_, base := s.relayCounting(t)

	// Cross 80% once: the notice is sent and its once-a-month claim taken.
	if err := s.spent("6.70"); err != nil {
		t.Fatal(err)
	}
	s.relayPaid(s.acctKey, "")
	if n := s.countTo("Monthly spend at 80%", s.acctAddr); n != 1 {
		t.Fatalf("setup: %d 80%% notices, want 1", n)
	}
	// Every later paid request above 80% this month: the claim is already taken, so the
	// notice path must not look the owner up again.
	for i := 0; i < 3; i++ {
		_, owner := s.relayCounting(t)
		if owner != base {
			t.Fatalf("request %d above 80%% made %d owner reads, want the below-threshold baseline %d", i+1, owner, base)
		}
	}
}

func TestPaidRequestReadsTheCapOnce(t *testing.T) {
	for _, tc := range []struct {
		name, cap string
	}{
		{"no cap", ""},
		{"a cap far from the threshold", "100.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCapNoticeHarness(t)
			if err := s.makeAccount("GitHub-linked", "once@example.com"); err != nil {
				t.Fatal(err)
			}
			if tc.cap != "" {
				if err := s.setCap(tc.cap); err != nil {
					t.Fatal(err)
				}
			}
			caps, _ := s.relayCounting(t)
			if caps != 1 {
				t.Fatalf("a paid request read the monthly cap %d times, want once", caps)
			}
		})
	}
}
