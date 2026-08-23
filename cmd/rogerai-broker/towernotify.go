package main

import (
	"fmt"
	"sync"
	"time"
)

// towerPendingNotifier tells the admin a Tower is waiting for approval - once, promptly,
// and without becoming a lever. Enrollment is self-service, so an unthrottled notifier
// hands anyone with an account a way to fill the admin's inbox; one email per owner per
// window, carrying the count, keeps the signal and starves the lever.
type towerPendingNotifier struct {
	send   func(owner, towerID string, suppressed int)
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	last   map[string]time.Time // owner -> last email
	queued map[string]int       // owner -> enrollments since that email
}

func newTowerPendingNotifier(send func(owner, towerID string, suppressed int)) *towerPendingNotifier {
	return &towerPendingNotifier{
		send: send, window: time.Hour, now: time.Now,
		last: map[string]time.Time{}, queued: map[string]int{},
	}
}

// enrolled records one admission and emails unless this owner already got one inside the
// window. The suppressed count rides the NEXT email, so a burst is visible as a burst.
func (n *towerPendingNotifier) enrolled(owner, towerID string) {
	if n == nil || n.send == nil {
		return
	}
	n.mu.Lock()
	at, seen := n.last[owner]
	if seen && n.now().Sub(at) < n.window {
		n.queued[owner]++
		n.mu.Unlock()
		return
	}
	suppressed := n.queued[owner]
	n.queued[owner] = 0
	n.last[owner] = n.now()
	n.mu.Unlock()
	n.send(owner, towerID, suppressed)
}

// towerPendingEmail composes the notification. Pure, so the words are testable without a
// mail provider: the admin needs the id to approve, the owner to judge, and the place to
// do it - and nothing here may carry a secret.
func towerPendingEmail(owner, towerID string, suppressed int) (subject, text string) {
	subject = fmt.Sprintf("Tower pending approval: %s", towerID)
	text = fmt.Sprintf(
		"A Tower finished enrollment and is waiting in quarantine.\n\n"+
			"  tower: %s\n  owner: %s\n\n"+
			"Approve, suspend, or revoke it from the admin dashboard (Towers panel).\n"+
			"Until approved it carries no traffic - that is the gate working, not a fault.\n",
		towerID, owner)
	if suppressed > 0 {
		text += fmt.Sprintf("\nThis owner enrolled %d more Tower(s) within the last hour; "+
			"those were not emailed separately.\n", suppressed)
	}
	return subject, text
}
