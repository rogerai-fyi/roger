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
type sessionBox struct {
	mu      sync.Mutex
	id      string
	changed chan struct{} // closed when id changes
}

func (s *sessionBox) set(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.id {
		s.id = id
		if s.changed != nil {
			close(s.changed)
			s.changed = nil
		}
	}
}

func (s *sessionBox) get() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

// changedFrom is closed once the session is no longer old (at once if it already is not).
func (s *sessionBox) changedFrom(old string) <-chan struct{} {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.id != old {
		c := make(chan struct{})
		close(c)
		return c
	}
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
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
					if errors.Is(err, towerjoin.ErrSessionGone) {
						// The link is re-opening: pull again as soon as the new session is in, or
						// after the backoff if the session never changes (a heartbeat can keep it).
						select {
						case <-box.changedFrom(sid):
							continue
						case <-pullAfter(backoff):
							backoff = min(backoff*2, pullBackoffMax)
							continue
						case <-stop:
							return
						}
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
					select {
					case sem <- struct{}{}:
					case <-stop:
						return
					}
					wg.Add(1)
					go func(w towerhub.PullWork) {
						defer wg.Done()
						defer func() { <-sem }()
						servePulled(ctx, st, hub, box, w, out)
					}(w)
				}
			}
		}()
	}
	return wg.Wait
}

// servePulled runs one piece of work against the hub and posts the answer, retrying a post that
// did not reach Core a couple of times: the answer is someone's request.
//
// The answer goes on the CURRENT session: a link that re-opened while the work ran has not lost
// the work, which Core bound to this Tower, not to a session, and an answer Core refuses for a
// session it no longer holds waits for the re-opened link. Work, posts and waits all stop with the
// Tower (ctx).
func servePulled(ctx context.Context, st *tower.State, hub http.Handler, box *sessionBox, w towerhub.PullWork, out io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ans := towerhub.ServePulled(ctx, hub, w, maxPulledAnswer)
	for unreachable := 0; ; {
		sid := box.get()
		err := answerWorkFn(ctx, st, sid, ans)
		switch {
		case err == nil:
			return
		case ctx.Err() != nil:
		case errors.Is(err, towerjoin.ErrSessionGone):
			select {
			case <-box.changedFrom(sid):
				continue
			case <-pullAfter(time.Second): // the same session may be live again
				continue
			case <-ctx.Done():
			}
		case errors.Is(err, towerjoin.ErrUnreachable):
			if unreachable++; unreachable < 3 {
				select {
				case <-pullAfter(time.Second):
					continue
				case <-ctx.Done():
				}
			}
		default:
			fmt.Fprintf(out, "pull: Core refused the answer to %s: %v\n", w.ID, err)
			return
		}
		break // stopped, timed out, or out of tries
	}
	fmt.Fprintf(out, "pull: the answer to %s did not reach Core\n", w.ID)
}
