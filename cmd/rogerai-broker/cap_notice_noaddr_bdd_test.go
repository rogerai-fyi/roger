package main

// cap_notice_noaddr_bdd_test.go: steps for the PR #142 follow-up scenarios in
// cap_notice_emails.feature. An account above a threshold with no mailable address must not
// repeat the address lookup on every request: the answer is remembered in the shared store
// (every instance sees it) and lapses on its own, after which a newly gained address is mailed.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/cucumber/godog"
)

func (s *cnState) registerNoAddrSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the account relays another paid request above 80%$`, func() error { return s.relayCountingOn(s.b) })
	sc.Step(`^the account relays another paid request above 80% on the second instance$`, func() error {
		if s.b2 == nil {
			return fmt.Errorf("no second instance")
		}
		s.b2.mail = s.capturingMailer()
		return s.relayCountingOn(s.b2)
	})
	sc.Step(`^that request looked up no address for the notice$`, s.lookedUpNoAddress)
	sc.Step(`^the remembered "no address" answer lapses$`, s.noAddrLapses)
}

// relayCountingOn relays one paid request through bk with its store wrapped to count the
// owner reads the notice path can make, and records the count for the Then step.
func (s *cnState) relayCountingOn(bk *broker) error {
	real := bk.db
	c := &capNoticeCountingStore{Store: real}
	bk.db = c
	defer func() { bk.db = real }()
	body := s.chatBody("qwen3-32b", false)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	signReq(req, s.acctKey, body)
	req.Header.Set("X-Roger-Node", s.st("n-1").id)
	rec := httptest.NewRecorder()
	bk.relay(rec, req)
	s.resp = rec
	if rec.Code != 200 {
		return fmt.Errorf("the paid relay was not served: %d %s", rec.Code, rec.Body.String())
	}
	s.settled()
	s.ownerReads = atomic.LoadInt64(&c.ownerReads)
	return nil
}

func (s *cnState) lookedUpNoAddress() error {
	if s.ownerReads != 0 {
		return fmt.Errorf("the request made %d owner lookups for the notice, want none while the \"no address\" answer is remembered", s.ownerReads)
	}
	return nil
}

// noAddrLapses moves the shared store's clock past any short-lived answer. The monthly
// notice claims live for weeks, so they are unaffected.
func (s *cnState) noAddrLapses() error {
	if s.mr == nil {
		return fmt.Errorf("no shared store in this scenario")
	}
	s.mr.FastForward(time.Hour)
	return nil
}
