package main

// acctkeys_notice_release_test.go: a key notice whose email the provider rejects is not lost
// for the window: the claim taken before the send is given back when the send is dropped, so
// the next crossing mails it; a sent notice still mails once (slice-5 review 2026-10-05).

import (
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
