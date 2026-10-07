package localplane

// Streaming through a standalone Tower (features/tower/standalone_streaming.feature). The
// Tower still dials nobody: a station that took a streamed job connects IN with each chunk on
// /local/chunk, authenticated exactly like /local/complete, and the plane forwards chunk n to
// the waiting consumer only after chunk n-1. Once the first byte has reached the consumer the
// reply is never silently restarted: a broken stream ends with an error event, a zero-cost
// usage chunk and [DONE].

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// Stream limits (RULED 2026-10-07). A token batch is a few KiB, so the chunk cap only stops a
// station using one chunk as an unbounded buffer; the total cap matches maxPromptBody; the
// duration cap is long enough for a slow local model and finite, so a stuck station cannot
// hold a concurrency slot forever.
const (
	defaultChunkMax    = 256 << 10
	defaultStreamTotal = 8 << 20
	defaultStreamIdle  = 60 * time.Second
	defaultStreamMax   = 10 * time.Minute
	// Chunk posts get their own station bucket: a real stream must never be throttled by the
	// poll bucket meant to stop a flooding key.
	defaultChunkRate  = 200.0
	defaultChunkBurst = 400.0
	// maxChunkBody is what the chunk route reads to verify a signature: above the chunk cap, so
	// an oversize chunk is refused as a 413, not mistaken for a bad signature.
	maxChunkBody = 1 << 20
	// streamEventBuffer is how far the station may run ahead of a slow consumer; a full buffer
	// answers 503 and the station resends the same sequence number. One extra slot is kept free
	// so a terminal event always fits.
	streamEventBuffer = 64
	// goneTTL is how long an ended streamed job is remembered, so its station's late chunk gets
	// an honest 410 rather than looking like a forged id.
	goneTTL = 10 * time.Minute
)

// streamEvent is one step of a streamed reply on its way to the consumer.
type streamEvent struct {
	data      []byte
	final     bool
	errMsg    string // a terminal, consumer-facing error; never the station's own text
	stationID string
}

// goneJob is an ended streamed job: who held it and why it ended.
type goneJob struct {
	station string
	reason  string
	exp     time.Time
}

const (
	goneConsumerLeft = "the consumer left"
	goneEnded        = "the stream has ended"
)

// streamWanted reports whether a consumer asked for a stream: only a JSON boolean true does,
// the same reading the broker's relay uses ("true", 1 and null are not a stream).
func streamWanted(body []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	return string(bytes.TrimSpace(m["stream"])) == "true"
}

// chunkStatus is what a station's chunk came to.
type chunkStatus int

const (
	chunkOK chunkStatus = iota
	chunkUnknown
	chunkNotStreamed
	chunkDuplicate
	chunkOutOfOrder
	chunkTooLarge
	chunkTotalExceeded
	chunkBusy
	chunkGone
)

// chunkLimits are the per-job caps the queue enforces when a chunk arrives.
type chunkLimits struct{ chunkMax, totalMax int }

// chunk accepts the next chunk of a streamed job, in sequence, from the station that took it.
// It returns the expected sequence for a 409 and the reason for a 410.
func (q *queue) chunk(id, stationID string, seq int, data []byte, final bool, errMsg string, lim chunkLimits) (chunkStatus, int, string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pruneGoneLocked()
	j, ok := q.inflight[id]
	if !ok {
		if g, ok := q.gone[id]; ok && g.station == stationID {
			return chunkGone, 0, g.reason
		}
		return chunkUnknown, 0, ""
	}
	if j.takenBy != stationID {
		return chunkUnknown, 0, ""
	}
	if !j.chunked {
		return chunkNotStreamed, 0, ""
	}
	if seq < j.nextSeq {
		return chunkDuplicate, j.nextSeq, ""
	}
	if seq > j.nextSeq {
		return chunkOutOfOrder, j.nextSeq, ""
	}
	if len(data) > lim.chunkMax {
		q.endLocked(j, "the local station sent a chunk over the size limit")
		return chunkTooLarge, 0, ""
	}
	if j.total+len(data) > lim.totalMax {
		q.endLocked(j, "the reply exceeded the size limit")
		return chunkTotalExceeded, 0, ""
	}
	if len(j.events) >= streamEventBuffer {
		return chunkBusy, j.nextSeq, ""
	}
	j.events <- streamEvent{data: data, final: final, errMsg: errMsg, stationID: stationID}
	j.nextSeq++
	j.total += len(data)
	if final || errMsg != "" {
		q.retireLocked(j, goneEnded)
	}
	return chunkOK, 0, ""
}

// endLocked ends a streamed job with a terminal error for the consumer. Caller holds q.mu.
func (q *queue) endLocked(j *job, msg string) {
	select {
	case j.events <- streamEvent{errMsg: msg, stationID: j.takenBy}:
	default:
	}
	q.retireLocked(j, goneEnded)
}

// retireLocked removes a taken job from in-flight and remembers why. Caller holds q.mu.
func (q *queue) retireLocked(j *job, reason string) {
	delete(q.inflight, j.id)
	if j.takenBy != "" {
		q.gone[j.id] = goneJob{station: j.takenBy, reason: reason, exp: time.Now().Add(goneTTL)}
	}
}

// abandonFor drops a job whose consumer stream ended (the consumer left, or a timeout), and
// remembers why for the station's next chunk. Idempotent.
func (q *queue) abandonFor(id, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if j, ok := q.inflight[id]; ok {
		q.retireLocked(j, reason)
		return
	}
	for i, j := range q.pending {
		if j.id == id {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			return
		}
	}
}

func (q *queue) pruneGoneLocked() {
	now := time.Now()
	for id, g := range q.gone {
		if now.After(g.exp) {
			delete(q.gone, id)
		}
	}
}

// chunkRequest is a station's next chunk of a streamed job.
type chunkRequest struct {
	JobID string `json:"job_id"`
	Seq   *int   `json:"seq"`
	Data  string `json:"data"`
	Final bool   `json:"final"`
	Error string `json:"error"`
}

// localChunk takes a streamed job's next chunk from the station that took it. Every
// authentication failure, and a chunk for a job this station does not hold, is the uniform 401.
func (s *Server) localChunk(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, maxChunkBody))
	}
	station, ok := s.authStationWith(r, body, s.chunkRL)
	if !ok {
		unauthorized(w)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req chunkRequest
	if err := json.Unmarshal(body, &req); err != nil || req.JobID == "" || req.Seq == nil || *req.Seq < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "a job_id and a seq are required"})
		return
	}
	// A station's own upstream error text is never echoed: it can carry the upstream's key.
	errMsg := ""
	if req.Error != "" {
		errMsg = "a local upstream failure ended the reply"
	}
	status, expected, reason := s.q.chunk(req.JobID, station.ID, *req.Seq, []byte(req.Data), req.Final, errMsg,
		chunkLimits{chunkMax: s.chunkMax, totalMax: s.streamTotal})
	switch status {
	case chunkOK:
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true})
	case chunkUnknown:
		unauthorized(w)
	case chunkNotStreamed:
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this job is not streamed: return it whole to /local/complete"})
	case chunkDuplicate:
		writeJSON(w, http.StatusConflict, map[string]any{"error": "duplicate chunk", "expected_seq": expected})
	case chunkOutOfOrder:
		writeJSON(w, http.StatusConflict, map[string]any{"error": "out of order", "expected_seq": expected})
	case chunkTooLarge, chunkTotalExceeded:
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "over the stream size limit"})
	case chunkBusy:
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "the consumer is behind: resend this chunk", "expected_seq": expected})
	case chunkGone:
		writeJSON(w, http.StatusGone, map[string]any{"error": reason})
	}
}

// streamAnswer relays a chunked job to the consumer as an SSE stream. Before the first byte it
// behaves like the one-shot path (a 504 on timeout, no SSE headers); after it, it never
// restarts: it ends with the station's final chunk, or an error event, then the zero-cost usage
// chunk and [DONE]. The concurrency slots the caller holds are held until this returns.
func (s *Server) streamAnswer(w http.ResponseWriter, r *http.Request, clientKeyHash, model string, j *job) {
	var first streamEvent
	firstTimer := time.NewTimer(s.completionTimeout)
	defer firstTimer.Stop()
	select {
	case first = <-j.events:
	case res := <-j.result:
		s.writeWrapped(w, clientKeyHash, model, res)
		return
	case <-r.Context().Done():
		s.q.abandonFor(j.id, goneConsumerLeft)
		if res, ok := drain(j); ok {
			_, _ = s.st.RecordReceipt(clientKeyHash, res.stationID, model)
		}
		return
	case <-firstTimer.C:
		s.q.abandonFor(j.id, goneEnded)
		select {
		case first = <-j.events:
		default:
			if res, ok := drain(j); ok {
				s.writeWrapped(w, clientKeyHash, model, res)
				return
			}
			writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "no local station served this request in time"})
			return
		}
	}

	s.sseHeaders(w, first.stationID)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	forwarded := false
	end := func(errMsg string) {
		if errMsg != "" {
			writeSSEError(w, errMsg)
		}
		writeSSEUsageDone(w, model)
		flush()
		if forwarded || errMsg == "" {
			_, _ = s.st.RecordReceipt(clientKeyHash, first.stationID, model)
		}
	}
	idle := time.NewTimer(s.streamIdle)
	defer idle.Stop()
	deadline := time.NewTimer(time.Until(j.start.Add(s.streamMax)))
	defer deadline.Stop()
	ev := first
	for {
		if len(ev.data) > 0 {
			if _, err := w.Write(stripDone(ev.data)); err == nil {
				forwarded = true
			}
			flush()
		}
		if ev.errMsg != "" {
			end(ev.errMsg)
			return
		}
		if ev.final {
			end("")
			return
		}
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(s.streamIdle)
		select {
		case ev = <-j.events:
		case <-r.Context().Done():
			s.q.abandonFor(j.id, goneConsumerLeft)
			if forwarded {
				_, _ = s.st.RecordReceipt(clientKeyHash, first.stationID, model)
			}
			return
		case <-idle.C:
			s.q.abandonFor(j.id, goneEnded)
			end("the local station stopped mid-reply")
			return
		case <-deadline.C:
			s.q.abandonFor(j.id, goneEnded)
			end("the reply ran too long")
			return
		}
	}
}

// writeWrapped answers a streamed request whose station returned the whole answer (a station
// without chunk support): the answer goes out as a single-chunk SSE stream.
func (s *Server) writeWrapped(w http.ResponseWriter, clientKeyHash, model string, res jobResult) {
	s.sseHeaders(w, res.stationID)
	_, _ = s.st.RecordReceipt(clientKeyHash, res.stationID, model)
	if ev, ok := protocol.CompletionAsSSE(res.answer, model); ok {
		_, _ = w.Write(ev)
	} else {
		writeSSEError(w, "the local station returned an unreadable answer")
	}
	writeSSEUsageDone(w, model)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sseHeaders writes a local stream's headers, before the first event: free, local, and
// curated when the serving station is a curated proxy.
func (s *Server) sseHeaders(w http.ResponseWriter, stationID string) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Roger-Cost", "0")
	h.Set("X-Roger-Local", "1")
	if stations, err := s.st.Stations(); err == nil {
		for _, st := range stations {
			if st.ID == stationID && st.Curated {
				h.Set("X-Roger-Curated", st.CuratedProvider)
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

// stripDone drops any "data: [DONE]" line a station passed through: the plane writes the one
// [DONE], after its usage chunk, so a station cannot end the consumer's stream early.
func stripDone(b []byte) []byte {
	if !bytes.Contains(b, []byte("[DONE]")) {
		return b
	}
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(b, []byte("\n")) {
		if string(bytes.TrimSpace(line)) == "data: [DONE]" {
			continue
		}
		out.Write(line)
	}
	return out.Bytes()
}

func writeSSEError(w http.ResponseWriter, msg string) {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "local_station_error"}})
	_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
}

func writeSSEUsageDone(w http.ResponseWriter, model string) {
	b, _ := json.Marshal(map[string]any{"object": "chat.completion.chunk", "model": model, "choices": []any{}, "usage": map[string]any{"cost": 0}})
	_, _ = w.Write([]byte("data: " + string(b) + "\n\ndata: [DONE]\n\n"))
}
