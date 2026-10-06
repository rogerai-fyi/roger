package main

// acctkeys_notice_release_test.go: a key notice whose email the provider rejects is not lost
// for the window: the claim taken before the send is given back when the send is dropped, so
// the next crossing mails it; a sent notice still mails once (slice-5 review 2026-10-05).

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

type noticeOwnerStore struct{ store.Store }

func (noticeOwnerStore) OwnerByPubkey(pub string) (store.Owner, bool, error) {
	return store.Owner{Pubkey: pub, Email: "owner@example.com"}, true, nil
}

func TestKeyNoticeClaimReleasedWhenSendDropped(t *testing.T) {
	vs, _ := testValkeyShared(t)
	var mu sync.Mutex
	posts := 0
	m := enabledMailer(func(r *http.Request) (*http.Response, error) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		posts++
		first := posts == 1
		mu.Unlock()
		status := http.StatusOK
		if first {
			status = http.StatusUnprocessableEntity // a permanent rejection: the job is dropped
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"id":"x"}`)), Header: http.Header{}}, nil
	})
	b := &broker{db: noticeOwnerStore{store.NewMem()}, shared: vs, mail: m}
	s := keyLimitState{k: store.AccountKey{ID: "key_notice", Name: "n", LimitUSD: 1, Reset: "none", OwnerPub: "pub"}, spend: 1}
	count := func() int { mu.Lock(); defer mu.Unlock(); return posts }

	b.emailKeyNotice(s, "100", "life")
	require.Eventually(t, func() bool { return count() == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		b.emailKeyNotice(s, "100", "life") // the rejected notice is mailed again on the next crossing
		return count() >= 2
	}, 3*time.Second, 50*time.Millisecond, "a rejected notice must not use up the window's claim")
	time.Sleep(200 * time.Millisecond)
	b.emailKeyNotice(s, "100", "life")
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 2, count(), "a notice that was sent is mailed once per window")
}

// TestShutdownDropReleasesBeforeReturning: a notice dropped because the mailer is shutting down
// gives its claim back before the drop returns (the process may exit right after), bounded so a
// slow release cannot stall shutdown.
func TestShutdownDropReleasesBeforeReturning(t *testing.T) {
	m := enabledMailer(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	})
	m.q.mu.Lock()
	m.q.stopping = true
	m.q.mu.Unlock()
	released := make(chan struct{})
	m.sendEmailOrRelease("owner@example.com", "s", "<p>t</p>", "t", func() {
		time.Sleep(20 * time.Millisecond)
		close(released)
	})
	select {
	case <-released:
	default:
		t.Fatal("the claim was not released before the shutdown drop returned")
	}

	start := time.Now()
	m.sendEmailOrRelease("owner@example.com", "s", "<p>t</p>", "t", func() { time.Sleep(time.Hour) })
	require.Less(t, time.Since(start), 5*time.Second, "a release that hangs must not stall shutdown")
}

// TestShutdownDropReleasesSeveralOutsideTheLock: notices still queued when the drain budget
// runs out are all given back before drain returns, together under one deadline (not one wait
// per notice), and without holding the queue lock while they run.
func TestShutdownDropReleasesSeveralOutsideTheLock(t *testing.T) {
	m := enabledMailer(func(r *http.Request) (*http.Response, error) {
		// Retryable with a long hint: every notice stays queued until the drain gives up.
		return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader(`{}`)),
			Header: http.Header{"Retry-After": []string{"3600"}}}, nil
	})
	var mu sync.Mutex
	released, lockFree := 0, 0
	const n = 5
	for i := 0; i < n; i++ {
		m.sendEmailOrRelease("owner@example.com", fmt.Sprint("s", i), "<p>t</p>", "t", func() {
			// The queue lock is not held while a release runs: it can be taken within a moment
			// (the other releases take it briefly too, so a single TryLock would race them).
			for end := time.Now().Add(50 * time.Millisecond); time.Now().Before(end); time.Sleep(time.Millisecond) {
				if m.q.mu.TryLock() {
					m.q.mu.Unlock()
					mu.Lock()
					lockFree++
					mu.Unlock()
					break
				}
			}
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			released++
			mu.Unlock()
		})
	}
	time.Sleep(100 * time.Millisecond) // let the sender try each once and park them
	start := time.Now()
	m.drain(10 * time.Millisecond)
	took := time.Since(start)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, n, released, "every queued notice gives its claim back before drain returns")
	require.Equal(t, n, lockFree, "no release runs under the queue lock")
	require.Less(t, took, time.Duration(n)*300*time.Millisecond, "the releases share one deadline, not one wait each")
}
