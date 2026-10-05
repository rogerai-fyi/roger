package agent

// cancel.go: the node side of features/multinode/job_cancel.feature. The node advertises
// X-Roger-Cancel on every poll; a broker that supports it queues a cancel when the consumer
// leaves, and cancelLoop's long-poll on GET /agent/cancels picks it up and stops that job's
// upstream request. A broker without the endpoint answers 404: the loop backs off and never
// re-registers for it (the poll loop owns re-registering).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// cancelHeader tells the broker this node stops a job when told to.
const cancelHeader = "X-Roger-Cancel"

// cancelledStatus is the result status of a job stopped by a cancel: the client closed the
// request (nginx's 499), nothing delivered.
const cancelledStatus = 499

// cancelBackoff is the wait after a 404 on /agent/cancels (an older broker without the
// endpoint, or one that forgot the node; the poll loop re-registers in that case).
var cancelBackoff = time.Minute

// cancelRetry is the wait after a transport error or another unexpected status.
var cancelRetry = 2 * time.Second

// inflightJobs maps the jobs being served now to the cancel of their upstream request.
type inflightJobs struct {
	mu   sync.Mutex
	jobs map[string]context.CancelFunc
}

// start registers job id and returns its context plus the func that ends it.
func (f *inflightJobs) start(id string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	if id == "" {
		return ctx, cancel
	}
	f.mu.Lock()
	if f.jobs == nil {
		f.jobs = map[string]context.CancelFunc{}
	}
	f.jobs[id] = cancel
	f.mu.Unlock()
	return ctx, func() {
		f.mu.Lock()
		delete(f.jobs, id)
		f.mu.Unlock()
		cancel()
	}
}

// cancel stops job id if it is being served; an unknown or finished id is a no-op (C4).
func (f *inflightJobs) cancel(id string) bool {
	f.mu.Lock()
	c, ok := f.jobs[id]
	f.mu.Unlock()
	if ok {
		c()
	}
	return ok
}

func cancelledResult(id string) protocol.JobResult {
	return protocol.JobResult{ID: id, Status: cancelledStatus}
}

// cancelLoop holds GET /agent/cancels until the session stops, cancelling each returned job.
func cancelLoop(cfg Config, sess *Session) {
	client := &http.Client{Timeout: 35 * time.Second} // must exceed the broker's hold
	u := cfg.Broker + "/agent/cancels?node=" + url.QueryEscape(cfg.NodeID)
	wait := func(d time.Duration) bool {
		select {
		case <-sess.stop:
			return false
		case <-time.After(d):
			return true
		}
	}
	for {
		select {
		case <-sess.stop:
			return
		default:
		}
		token, _ := sess.rereg.curToken()
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			if !wait(cancelRetry) {
				return
			}
			continue
		}
		var out struct {
			IDs []string `json:"ids"`
		}
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(resp.Body).Decode(&out)
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			for _, id := range out.IDs {
				sess.inflight.cancel(id)
			}
		case http.StatusNoContent:
		case http.StatusNotFound:
			if !wait(cancelBackoff) {
				return
			}
		default:
			if !wait(cancelRetry) {
				return
			}
		}
	}
}
