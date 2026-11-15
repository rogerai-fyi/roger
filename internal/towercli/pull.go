package towercli

// pull.go: this Tower takes its work by pull (features/tower/tower_pull_dispatch.feature). A few
// held pulls on the current link session receive the requests Core would otherwise have dialed
// the hub with; each runs against the hub in process, exactly as if it had arrived on the
// listener, and the hub's answer goes back over the same link. Nothing connects into this Tower.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towerhub"
	"rogerai.fm/roger/v7/internal/towerjoin"
)

// sessionBox is the link's current session id, written by the link loop on every (re)open and
// read by the pulls, which are bound to it.
type sessionBox struct{ id atomic.Value }

func (s *sessionBox) set(id string) {
	if s != nil {
		s.id.Store(id)
	}
}

func (s *sessionBox) get() string {
	if s == nil {
		return ""
	}
	v, _ := s.id.Load().(string)
	return v
}

// Seams, so a test drives the loop against a real broker or a stand-in.
var (
	pullWorkFn   = towerjoin.PullWork
	answerWorkFn = towerjoin.AnswerWork
	pullAfter    = time.After
)

const (
	// pullWorkers is how many pulls this Tower holds open; Core allows four.
	pullWorkers = 2
	// maxPulledAnswer is the hub answer carried back: the hub client's own response cap.
	maxPulledAnswer = 32 << 20
	// maxPulledInFlight bounds pulled work running against the hub at once.
	maxPulledInFlight = 256
	// pullBackoffMax is the longest wait between pulls while Core cannot be reached.
	pullBackoffMax = 10 * time.Second
)

// runPulls starts the held pulls and returns a waiter that blocks until they, and every piece
// of work they started, have finished.
func runPulls(st *tower.State, hub http.Handler, box *sessionBox, out io.Writer, stop <-chan struct{}) func() {
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-stop; cancel() }()
	sem := make(chan struct{}, maxPulledInFlight)
	var lastErr atomic.Value
	report := func(msg string) {
		if prev, _ := lastErr.Load().(string); prev != msg {
			lastErr.Store(msg)
			if msg != "" {
				fmt.Fprintln(out, msg)
			}
		}
	}
	wait := func(d time.Duration) bool {
		select {
		case <-stop:
			return false
		case <-pullAfter(d):
			return true
		}
	}
	for i := 0; i < pullWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			backoff := time.Second
			for {
				select {
				case <-stop:
					return
				default:
				}
				sid := box.get()
				if sid == "" {
					if !wait(time.Second) {
						return
					}
					continue
				}
				work, err := pullWorkFn(ctx, st, sid)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					if errors.Is(err, towerjoin.ErrUnreachable) {
						report(fmt.Sprintf("pull: RogerAI is not reachable (%v) - retrying", err))
					} else {
						report(fmt.Sprintf("pull: refused (%v) - retrying", err))
					}
					if !wait(backoff) {
						return
					}
					backoff = min(backoff*2, pullBackoffMax)
					continue
				}
				report("")
				backoff = time.Second
				for _, w := range work {
					sem <- struct{}{}
					wg.Add(1)
					go func(w towerhub.PullWork) {
						defer wg.Done()
						defer func() { <-sem }()
						servePulled(st, hub, sid, w, out)
					}(w)
				}
			}
		}()
	}
	return wg.Wait
}

// servePulled runs one piece of work against the hub and posts the answer, retrying a post that
// did not reach Core a couple of times: the answer is someone's request.
func servePulled(st *tower.State, hub http.Handler, sid string, w towerhub.PullWork, out io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ans := towerhub.ServePulled(ctx, hub, w, maxPulledAnswer)
	for attempt := 0; attempt < 3; attempt++ {
		err := answerWorkFn(st, sid, ans)
		if err == nil || !errors.Is(err, towerjoin.ErrUnreachable) {
			if err != nil {
				fmt.Fprintf(out, "pull: Core refused the answer to %s: %v\n", w.ID, err)
			}
			return
		}
		<-pullAfter(time.Second)
	}
	fmt.Fprintf(out, "pull: the answer to %s did not reach Core\n", w.ID)
}
