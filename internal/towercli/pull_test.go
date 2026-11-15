package towercli

// The Tower's pull loop (features/tower/tower_pull_dispatch.feature): held pulls on the current
// session, each piece of work run against the hub in process, the answer posted on the session
// current at the time, a refused or unreachable Core retried with backoff, and a prompt stop.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towerhub"
	"rogerai.fm/roger/v7/internal/towerjoin"
)

// The whole round trip against a stub Core over the real towerjoin calls: Core hands over one
// identity check, the hub answers it with its proof, and the answer is posted naming the Tower.
func TestThePullLoopRunsWorkAgainstTheHubAndAnswers(t *testing.T) {
	core := newCoreStub(t)
	dir := joinedRegisteredTower(t)
	st, release, err := openDirWith(dir, "")
	require.NoError(t, err)
	defer release()
	work := towerhub.PullWork{ID: "pw-1", Method: http.MethodGet, Path: towerhub.PathPoll, Query: "nonce=abcd"}
	core.reply["/tower/work/pull"] = func(w http.ResponseWriter, n int) bool {
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"work": []towerhub.PullWork{work}})
			return true
		}
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	answered := make(chan towerhub.PullAnswer, 1)
	var answerURL atomic.Value
	core.reply["/tower/work/answer"] = func(w http.ResponseWriter, _ int) bool {
		var a towerhub.PullAnswer
		_ = json.Unmarshal(core.body("/tower/work/answer"), &a)
		select {
		case answered <- a:
		default:
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return true
	}
	_ = answerURL
	hub := http.NewServeMux()
	hub.HandleFunc(towerhub.PathPoll, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(towerhub.HubProofHeader, "proof-for-"+r.URL.Query().Get("nonce"))
		w.WriteHeader(http.StatusUnauthorized)
	})
	box := &sessionBox{}
	box.set("sess-1")
	stop := make(chan struct{})
	var out strings.Builder
	wait := runPulls(st, hub, box, &lockedWriter{w: &out}, stop)
	select {
	case a := <-answered:
		require.Equal(t, "pw-1", a.ID)
		require.Equal(t, "tw-1", a.TowerID)
		require.Equal(t, "sess-1", a.SessionID)
		require.Equal(t, http.StatusUnauthorized, a.Status)
		require.Equal(t, "proof-for-abcd", a.Header[towerhub.HubProofHeader])
	case <-time.After(5 * time.Second):
		t.Fatal("the answer was never posted")
	}
	close(stop)
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the pulls did not stop promptly")
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

// The loop's edges through its seams: no session yet, a refused and an unreachable Core (said
// once each, retried), an answer on the session that is current when the work finishes, an
// answer post retried while Core is unreachable, and a stop that ends a backoff at once.
func TestThePullLoopEdges(t *testing.T) {
	prevPull, prevAns, prevAfter := pullWorkFn, answerWorkFn, pullAfter
	t.Cleanup(func() { pullWorkFn, answerWorkFn, pullAfter = prevPull, prevAns, prevAfter })
	pullAfter = func(time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
	var calls atomic.Int64
	box := &sessionBox{}
	pullWorkFn = func(ctx context.Context, _ *tower.State, sid string) ([]towerhub.PullWork, error) {
		switch calls.Add(1) {
		case 1:
			return nil, towerjoin.ErrUnreachable
		case 2:
			return nil, towerjoin.ErrUnreachable
		case 3:
			return nil, errors.New("refused")
		case 4:
			box.set("sess-new") // the link re-opened while this pull was held
			return []towerhub.PullWork{{ID: "pw-2", Method: http.MethodPost, Path: "/not-allowed"}}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var answers atomic.Int64
	gotSess := make(chan string, 1)
	answerWorkFn = func(_ context.Context, _ *tower.State, sid string, a towerhub.PullAnswer) error {
		if answers.Add(1) == 1 {
			return towerjoin.ErrUnreachable
		}
		require.Equal(t, http.StatusForbidden, a.Status, "work that is not one of the two hub requests never reaches the hub")
		gotSess <- sid
		return nil
	}
	stop := make(chan struct{})
	var out lockedWriter
	out.w = &strings.Builder{}
	wait := runPulls(nil, http.NewServeMux(), box, &out, stop)
	time.Sleep(20 * time.Millisecond) // no session yet: the loop waits for one
	require.Zero(t, calls.Load())
	box.set("sess-old")
	select {
	case sid := <-gotSess:
		require.Equal(t, "sess-new", sid, "the answer goes on the session current when the work finished")
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
	close(stop)
	wait()
	log := out.String()
	require.Contains(t, log, "not reachable")
	require.Contains(t, log, "refused")
	var nilBox *sessionBox
	nilBox.set("x")
	require.Empty(t, nilBox.get())
}

func TestAnAnswerThatNeverReachesCoreIsSaid(t *testing.T) {
	prevAns, prevAfter := answerWorkFn, pullAfter
	t.Cleanup(func() { answerWorkFn, pullAfter = prevAns, prevAfter })
	pullAfter = func(time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
	answerWorkFn = func(context.Context, *tower.State, string, towerhub.PullAnswer) error {
		return towerjoin.ErrUnreachable
	}
	var out strings.Builder
	box := &sessionBox{}
	servePulled(context.Background(), nil, http.NewServeMux(), box, towerhub.PullWork{ID: "pw-3"}, &out)
	require.Contains(t, out.String(), "did not reach Core")
	answerWorkFn = func(context.Context, *tower.State, string, towerhub.PullAnswer) error { return errors.New("no") }
	out.Reset()
	servePulled(context.Background(), nil, http.NewServeMux(), box, towerhub.PullWork{ID: "pw-4"}, &out)
	require.Contains(t, out.String(), "refused the answer")
}

// Review finding: an answer finished while the link re-opens met a 409 for the old session and
// was dropped. It now waits for the new session and goes on it.
func TestAnAnswerWaitsOutAReopeningLink(t *testing.T) {
	prevAns := answerWorkFn
	t.Cleanup(func() { answerWorkFn = prevAns })
	box := &sessionBox{}
	box.set("sess-old")
	var sessions []string
	var mu sync.Mutex
	answerWorkFn = func(_ context.Context, _ *tower.State, sid string, _ towerhub.PullAnswer) error {
		mu.Lock()
		sessions = append(sessions, sid)
		mu.Unlock()
		if sid == "sess-old" {
			go func() { time.Sleep(20 * time.Millisecond); box.set("sess-new") }()
			return towerjoin.ErrSessionGone
		}
		return nil
	}
	var out strings.Builder
	servePulled(context.Background(), nil, http.NewServeMux(), box, towerhub.PullWork{ID: "pw-5"}, &out)
	require.Equal(t, []string{"sess-old", "sess-new"}, sessions)
	require.Empty(t, out.String(), "the answer was delivered")

	// A link that never comes back gives up when the work's time is up, and says so.
	answerWorkFn = func(context.Context, *tower.State, string, towerhub.PullAnswer) error {
		return towerjoin.ErrSessionGone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	servePulled(ctx, nil, http.NewServeMux(), box, towerhub.PullWork{ID: "pw-6"}, &out)
	require.Contains(t, out.String(), "did not reach Core")
}

// Review finding: stopping the Tower waited for every in-flight answer post, minutes with Core
// down. The post and the wait between tries both end with the Tower.
func TestStoppingTheTowerEndsAnAnswerPostAtOnce(t *testing.T) {
	prevAns, prevAfter := answerWorkFn, pullAfter
	t.Cleanup(func() { answerWorkFn, pullAfter = prevAns, prevAfter })
	pullAfter = func(time.Duration) <-chan time.Time { return nil } // a wait that never ends by itself
	answerWorkFn = func(context.Context, *tower.State, string, towerhub.PullAnswer) error {
		return towerjoin.ErrUnreachable
	}
	box := &sessionBox{}
	box.set("s")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		servePulled(ctx, nil, http.NewServeMux(), box, towerhub.PullWork{ID: "pw-7"}, &strings.Builder{})
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a stopped Tower kept waiting to post an answer")
	}

	answerWorkFn = func(ctx context.Context, _ *tower.State, _ string, _ towerhub.PullAnswer) error {
		<-ctx.Done() // a post hung on a dead network
		return towerjoin.ErrUnreachable
	}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() {
		servePulled(ctx, nil, http.NewServeMux(), box, towerhub.PullWork{ID: "pw-8"}, &strings.Builder{})
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a stopped Tower kept a hung answer post open")
	}
}

// Review finding: a pull refused for a session Core no longer holds backed off up to 10 s even
// after the link re-opened. It now pulls again as soon as the session changes.
func TestAPullOnAGoneSessionPullsAgainOnTheNewOne(t *testing.T) {
	prevPull, prevAfter := pullWorkFn, pullAfter
	t.Cleanup(func() { pullWorkFn, pullAfter = prevPull, prevAfter })
	pullAfter = func(time.Duration) <-chan time.Time { return nil } // backoff would never end
	box := &sessionBox{}
	box.set("sess-old")
	pulledNew := make(chan struct{})
	var once sync.Once
	pullWorkFn = func(ctx context.Context, _ *tower.State, sid string) ([]towerhub.PullWork, error) {
		if sid == "sess-old" {
			return nil, towerjoin.ErrSessionGone
		}
		once.Do(func() { close(pulledNew) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	stop := make(chan struct{})
	wait := runPulls(nil, http.NewServeMux(), box, &strings.Builder{}, stop)
	time.Sleep(20 * time.Millisecond)
	box.set("sess-new")
	select {
	case <-pulledNew:
	case <-time.After(5 * time.Second):
		t.Fatal("pulls did not resume on the re-opened session")
	}
	close(stop)
	wait()
}

func TestTheSessionBoxSaysWhenTheSessionChanges(t *testing.T) {
	box := &sessionBox{}
	box.set("a")
	ch := box.changedFrom("a")
	select {
	case <-ch:
		t.Fatal("unchanged")
	default:
	}
	box.set("a")
	select {
	case <-ch:
		t.Fatal("setting the same id is no change")
	default:
	}
	box.set("b")
	<-ch
	<-box.changedFrom("a") // already different: closed at once
	var nilBox *sessionBox
	require.Nil(t, nilBox.changedFrom("x"))
}

// Review finding: a 409 whose session never changes (a heartbeat kept the same session, or Core's
// check met a store blip) left the pulls waiting forever. They retry after the backoff too.
func TestAGoneSessionThatNeverChangesIsRetried(t *testing.T) {
	prevPull, prevAns, prevAfter := pullWorkFn, answerWorkFn, pullAfter
	t.Cleanup(func() { pullWorkFn, answerWorkFn, pullAfter = prevPull, prevAns, prevAfter })
	pullAfter = func(time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
	box := &sessionBox{}
	box.set("sess")
	var calls atomic.Int64
	again := make(chan struct{})
	var once sync.Once
	pullWorkFn = func(ctx context.Context, _ *tower.State, _ string) ([]towerhub.PullWork, error) {
		if calls.Add(1) <= 2 {
			return nil, towerjoin.ErrSessionGone
		}
		once.Do(func() { close(again) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	stop := make(chan struct{})
	wait := runPulls(nil, http.NewServeMux(), box, &strings.Builder{}, stop)
	select {
	case <-again:
	case <-time.After(5 * time.Second):
		t.Fatal("pulls stopped for good on a session that never changed")
	}
	close(stop)
	wait()

	var posts atomic.Int64
	answerWorkFn = func(context.Context, *tower.State, string, towerhub.PullAnswer) error {
		if posts.Add(1) == 1 {
			return towerjoin.ErrSessionGone
		}
		return nil
	}
	var out strings.Builder
	servePulled(context.Background(), nil, http.NewServeMux(), box, towerhub.PullWork{ID: "pw-9"}, &out)
	require.Equal(t, int64(2), posts.Load(), "an answer refused for a session that never changed was not posted again")
	require.Empty(t, out.String())
}
