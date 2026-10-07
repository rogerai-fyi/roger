package agent

// The station half of standalone Tower streaming (features/tower/standalone_streaming.feature).
// A streamed job keeps its stream flag, the upstream's SSE events are coalesced into a chunk
// every 50 ms or 16 KiB (whichever first), and each chunk is posted IN to /local/chunk, signed,
// in sequence. The Tower writes the one [DONE]; a 410 from it means the consumer left or the
// stream ended, so the upstream is cancelled and the station goes back to polling.
//
// Contract: features/tower/standalone_streaming.feature.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// Batching (RULED 2026-10-07): one signed POST per token would cost a signature and a nonce
// per token; a chunk every 50 ms or 16 KiB keeps a stream live without that.
var (
	streamBatchEvery = 50 * time.Millisecond
	streamBatchBytes = 16 << 10
)

// chunkBusyRetries bounds resending one chunk the Tower could not take yet (its consumer is
// behind): the station waits briefly and resends the same sequence number.
const chunkBusyRetries = 40

// runLocalStream serves one streamed job: it runs the request against the node's upstream with
// the stream flag kept, and posts the reply to the Tower as sequenced chunks.
func runLocalStream(ctx context.Context, c *http.Client, cfg Config, priv ed25519.PrivateKey, job localJob) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	post := &chunkPoster{ctx: ctx, c: c, broker: cfg.Broker, priv: priv, jobID: job.ID}

	body := []byte(job.Request)
	if cfg.Osaurus {
		body = pinModel(body, cfg.Model)
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Upstream, bytes.NewReader(body))
	if err != nil {
		post.fail("could not build the upstream request")
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Accept", "text/event-stream")
	if cfg.UpstreamKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+cfg.UpstreamKey)
	}
	if cfg.Osaurus {
		upReq.Header.Set("X-Persist", "false")
	}
	resp, err := c.Do(upReq)
	if err != nil {
		post.fail("the local model did not answer")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		post.fail("the local model answered " + resp.Status)
		return
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		// An upstream that answered whole for a streamed request: it goes out as one chunk.
		ans, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		ans = redactUpstreamKey(ans, cfg.UpstreamKey)
		ev, ok := protocol.CompletionAsSSE(ans, cfg.Model)
		if !ok {
			post.fail("the local model returned an unreadable response")
			return
		}
		post.send(ev, true, "")
		return
	}

	events := make(chan []byte)
	readErr := make(chan error, 1)
	go func() { readErr <- readSSEEvents(ctx, resp.Body, events) }()

	var buf bytes.Buffer
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	armed := false
	flush := func(final bool) bool {
		if buf.Len() == 0 && !final {
			return true
		}
		data := redactUpstreamKey(append([]byte(nil), buf.Bytes()...), cfg.UpstreamKey)
		buf.Reset()
		return post.send(data, final, "")
	}
	sawDone := false
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				err := <-readErr
				if err != nil || !sawDone {
					// The upstream broke or stopped without finishing: what was sent stands, and the
					// Tower ends the consumer's stream with an error, never a restart.
					if flush(false) {
						post.fail("the local model's stream ended early")
					}
					return
				}
				flush(true)
				return
			}
			if isDoneEvent(ev) {
				sawDone = true
				continue
			}
			buf.Write(ev)
			if buf.Len() >= streamBatchBytes {
				if !flush(false) {
					return
				}
				continue
			}
			if !armed {
				timer.Reset(streamBatchEvery)
				armed = true
			}
		case <-timer.C:
			armed = false
			if !flush(false) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// readSSEEvents reads whole SSE events (lines up to a blank line) and sends each, normalized to
// end in "\n\n". It closes events when the body ends and reports a read error other than EOF.
func readSSEEvents(ctx context.Context, body io.Reader, events chan<- []byte) error {
	defer close(events)
	rd := bufio.NewReaderSize(body, 64<<10)
	var cur bytes.Buffer
	for {
		line, err := rd.ReadString('\n')
		if line != "" {
			l := strings.TrimRight(line, "\r\n")
			if l == "" {
				if cur.Len() > 0 {
					cur.WriteString("\n")
					select {
					case events <- append([]byte(nil), cur.Bytes()...):
					case <-ctx.Done():
						return ctx.Err()
					}
					cur.Reset()
				}
			} else {
				cur.WriteString(l + "\n")
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func isDoneEvent(ev []byte) bool {
	return string(bytes.TrimSpace(ev)) == "data: [DONE]"
}

// chunkPoster posts one job's chunks in sequence and stops the job when the Tower says to.
type chunkPoster struct {
	ctx    context.Context
	c      *http.Client
	broker string
	priv   ed25519.PrivateKey
	jobID  string
	seq    int
	done   bool
}

// send posts the next chunk and reports whether the stream should go on. A 410 (the consumer
// left, or the stream ended) or any other refusal ends it.
func (p *chunkPoster) send(data []byte, final bool, errMsg string) bool {
	if p.done {
		return false
	}
	m := map[string]any{"job_id": p.jobID, "seq": p.seq, "data": string(data)}
	if final {
		m["final"] = true
	}
	if errMsg != "" {
		m["error"] = errMsg
	}
	body, err := json.Marshal(m)
	if err != nil {
		p.done = true
		return false
	}
	for attempt := 0; attempt < chunkBusyRetries; attempt++ {
		req, err := http.NewRequestWithContext(p.ctx, http.MethodPost, p.broker+"/local/chunk", bytes.NewReader(body))
		if err != nil {
			p.done = true
			return false
		}
		signLocal(req, body, p.priv)
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.c.Do(req)
		if err != nil {
			p.done = true
			return false
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			p.seq++
			if final || errMsg != "" {
				p.done = true
				return false
			}
			return true
		case http.StatusServiceUnavailable:
			select {
			case <-p.ctx.Done():
				p.done = true
				return false
			case <-time.After(25 * time.Millisecond):
			}
			continue
		default:
			p.done = true
			return false
		}
	}
	p.done = true
	return false
}

// fail ends the job with a station-side error; the Tower words what the consumer sees.
func (p *chunkPoster) fail(msg string) { p.send(nil, true, msg) }
