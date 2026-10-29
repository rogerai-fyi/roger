package localmodel

// THE ONE FETCH IN FLIGHT, recorded so every surface can see it.
//
// A fetch runs inside a roger: the CLI's, the one serving the console, the TUI's, or a member's
// answering an Edge request. Its progress is written to a small file beside the models, so a page
// that reloads, a screen that was left and came back, and an authority looking at the fleet all
// read the same truth. One at a time per machine: two multi-gigabyte downloads racing for the same
// card help nobody, and "which one is it showing" is a question a screen should never have to ask.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/hfhub"
	"rogerai.fm/roger/v7/internal/provision"
)

// Job states.
const (
	JobFetching    = "fetching"
	JobDone        = "done"
	JobFailed      = "failed"
	JobInterrupted = "interrupted" // its roger went away before it finished; the partial resumes
)

// Job is the fetch in flight, or the last one.
type Job struct {
	Repo       string    `json:"repo"`
	File       string    `json:"file"`
	ID         string    `json:"id,omitempty"` // set when done
	Total      int64     `json:"total"`
	Done       int64     `json:"done"`
	Rate       float64   `json:"rate"`
	RemainingS int64     `json:"remaining_s,omitempty"`
	Resumed    bool      `json:"resumed,omitempty"`
	State      string    `json:"state"`
	Err        string    `json:"err,omitempty"`
	Reason     string    `json:"reason,omitempty"` // which failure: no_space, unreachable, gated, not_found, rate_limited, corrupt, upstream
	PID        int       `json:"pid"`
	Started    time.Time `json:"started"`
	Updated    time.Time `json:"updated"`
}

// ErrBusy is a second fetch asked for while one is running.
var ErrBusy = errors.New("a model is already being fetched on this machine")

func (h Home) jobPath() string { return filepath.Join(h.Dir, "fetch.json") }

// jobStall is how long a "fetching" record may stand without moving before it is not believed: the
// progress callback writes several times a second while bytes flow, so ten minutes of silence is a
// fetch that died - even if some unrelated process now holds its pid.
const jobStall = 10 * time.Minute

// Job is the current or last fetch. A fetch whose roger has gone is reported interrupted, never
// as still running and never as done.
func (h Home) Job() (Job, bool) {
	b, err := os.ReadFile(h.jobPath())
	if err != nil {
		return Job{}, false
	}
	var j Job
	if json.Unmarshal(b, &j) != nil || j.State == "" {
		return Job{}, false
	}
	if j.State == JobFetching && (j.PID <= 0 || !alive(j.PID) || time.Since(j.Updated) > jobStall) {
		j.State = JobInterrupted
	}
	return j, true
}

func (h Home) writeJob(j Job) {
	j.Updated = time.Now().UTC()
	b, err := json.Marshal(j)
	if err != nil {
		return
	}
	_ = os.MkdirAll(h.Dir, 0o700)
	tmp := fmt.Sprintf("%s.%d.tmp", h.jobPath(), os.Getpid()) // one writer's file, never another process's
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, h.jobPath())
	}
}

// claimFetch records j as the fetch in flight, unless one already is - checked and written under a
// file lock, so two processes (the console and a member's face) cannot both start one.
func (h Home) claimFetch(j Job) (bool, error) {
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return false, err
	}
	unlock, err := edge.FileLock(h.jobPath() + ".lock")
	if err != nil {
		return false, err
	}
	defer unlock()
	if cur, ok := h.Job(); ok && cur.State == JobFetching {
		return false, nil
	}
	j.State, j.PID, j.Started = JobFetching, os.Getpid(), time.Now().UTC()
	h.writeJob(j)
	return true, nil
}

// Fetch runs Get and records its progress as it goes. It blocks; StartFetch is the same in the
// background.
func (h Home) Fetch(ctx context.Context, hub *hfhub.Client, f provision.Fetcher, repo string, file hfhub.File, license string) (Model, error) {
	j := Job{Repo: repo, File: file.Path, Total: file.Bytes}
	claimed, err := h.claimFetch(j)
	if err != nil {
		return Model{}, err
	}
	if !claimed {
		return Model{}, ErrBusy
	}
	j, _ = h.Job()

	outer := f.OnProgress
	f.OnProgress = func(p provision.Progress) {
		j.Done, j.Rate, j.RemainingS, j.Resumed = p.Done, p.Rate, int64(p.Remaining/time.Second), p.Resumed
		if p.Total > 0 {
			j.Total = p.Total
		}
		h.writeJob(j)
		if outer != nil {
			outer(p)
		}
	}
	m, err := h.Get(ctx, hub, &f, repo, file, license)
	if err != nil {
		j.State, j.Err, j.Reason = JobFailed, err.Error(), Reason(err)
	} else {
		j.State, j.ID, j.Done = JobDone, m.ID, j.Total
	}
	h.writeJob(j)
	return m, err
}

// StartFetch begins a fetch in the background and returns once it is recorded as running, so a
// caller that answers "started" is never contradicted by a page that reads the record a moment
// later. done, if set, is told the outcome.
func (h Home) StartFetch(hub *hfhub.Client, f provision.Fetcher, repo string, file hfhub.File, license string, done func(Model, error)) error {
	if why := Unservable(file.Path); why != "" {
		return errors.New(file.Path + " is " + why)
	}
	if _, err := h.PathFor(repo, file.Path); err != nil {
		return err
	}
	started := make(chan error, 1)
	go func() {
		// The first write of the record is the "started"; Fetch makes it before any byte moves.
		var once sync.Once
		inner := f.OnProgress
		f.OnProgress = func(p provision.Progress) {
			once.Do(func() { started <- nil })
			if inner != nil {
				inner(p)
			}
		}
		m, err := h.Fetch(context.Background(), hub, f, repo, file, license)
		once.Do(func() { started <- err })
		if done != nil {
			done(m, err)
		}
	}()
	select {
	case err := <-started:
		if errors.Is(err, ErrBusy) {
			return err
		}
		return nil // a failure after starting is in the record, which is where a screen reads it
	case <-time.After(2 * time.Second):
		return nil // still connecting; the record already says it is running
	}
}

// Reason names which failure a fetch met, because the fix differs for each and a screen in another
// process can only read the record, not the error.
func Reason(err error) string {
	for _, r := range []struct {
		err  error
		word string
	}{
		{provision.ErrNoSpace, "no_space"}, {provision.ErrUnreachable, "unreachable"}, {provision.ErrGated, "gated"},
		{provision.ErrNotFound, "not_found"}, {provision.ErrRateLimited, "rate_limited"}, {provision.ErrCorrupt, "corrupt"},
		{provision.ErrUpstream, "upstream"},
	} {
		if errors.Is(err, r.err) {
			return r.word
		}
	}
	return ""
}
