package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"rogerai.fm/roger/v6/internal/store"
)

// screener is the OFF-PATH content screen for the paid chat relay (ROGERAI_MODERATION_MODE=
// async, the default; features/moderation/off_path_screening.feature). The relay hands it the
// screened text and continues to pick/hold/dispatch immediately - it never awaits the
// classifier, so a slow, throttled, dead, or unconfigured classifier changes nothing about
// relay latency, status codes, holds, or receipts. A small worker pool drains a bounded
// in-process queue with the SAME verdict policy the synchronous gate applies
// (moderation.classify), its own HTTP timeout, a per-instance token budget, and 429/5xx
// backoff that honors Retry-After. When the queue is full, the byte budget is exhausted, or a
// job outlives the max lag, the job is DROPPED and COUNTED (never blocked, never retried on
// the request goroutine). The obligations still land after the fact: a CSAM verdict
// preserves + queues + pages exactly as the in-line gate did; a block-net verdict is RECORDED
// against the consumer pseudonym (moderation_flags) and surfaced, never auto-enforced.
//
// Incident this replaces: one long-context consumer tripped the classifier's tokens-per-minute
// cap, and the synchronous gate fail-opened 100 relays UNSCREENED while adding up to 24s of
// classifier round-trip to every request (prod, 2026-09-07).

// screenerConfig holds the knobs (all env, all optional; defaults per the spec header).
type screenerConfig struct {
	queue      int           // ROGERAI_MODERATION_QUEUE: max queued jobs per instance
	queueBytes int64         // ROGERAI_MODERATION_QUEUE_BYTES: max bytes held (bodies + windows)
	workers    int           // ROGERAI_MODERATION_WORKERS: concurrent classifier calls
	window     int           // ROGERAI_MODERATION_WINDOW: chars screened per prompt (head 3/4 + tail 1/4)
	tpm        int           // ROGERAI_MODERATION_TPM: classifier tokens per minute per instance
	maxLag     time.Duration // ROGERAI_MODERATION_MAX_LAG: a job older than this is dropped (stale)
}

func defaultScreenerConfig() screenerConfig {
	return screenerConfig{queue: 512, queueBytes: 64 << 20, workers: 2, window: 16000, tpm: 24000, maxLag: 10 * time.Minute}
}

func loadScreenerConfig() screenerConfig {
	d := defaultScreenerConfig()
	return screenerConfig{
		queue:      envInt("ROGERAI_MODERATION_QUEUE", d.queue),
		queueBytes: int64(envInt("ROGERAI_MODERATION_QUEUE_BYTES", int(d.queueBytes))),
		workers:    envInt("ROGERAI_MODERATION_WORKERS", d.workers),
		window:     envInt("ROGERAI_MODERATION_WINDOW", d.window),
		tpm:        envInt("ROGERAI_MODERATION_TPM", d.tpm),
		maxLag:     envDuration("ROGERAI_MODERATION_MAX_LAG", d.maxLag),
	}
}

const (
	screenerSummaryEvery  = 5 * time.Minute  // periodic log summary (replaces per-request noise)
	screenerDropWindow    = 10 * time.Minute // drop-rate alert window
	screenerDropMinSample = 5                // no drop-rate verdict on fewer jobs than this
	screenerDownAfter     = 15 * time.Minute // consecutive classifier failure before paging
	screenerBackoffCap    = 30 * time.Second
	screenerDrainBudget   = 2 * time.Second // shutdown: drain what fits, count the rest
	repeatFlagThreshold   = 5               // block-net flags per pseudonym per day that page once
)

// screenJob is one relay awaiting an after-the-fact verdict. body is the FULL request body
// (a CSAM verdict preserves all of it, not just the window); window is the bounded text
// actually sent to the classifier. node is filled in once the relay has picked a station.
type screenJob struct {
	id, pseudonym, ip, model string
	body                     []byte
	window                   string
	enqueued                 time.Time
	attempts                 int
	last429                  bool

	mu   sync.Mutex
	node string
}

func (j *screenJob) setNode(node string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.node = node
	j.mu.Unlock()
}

func (j *screenJob) station() string { j.mu.Lock(); defer j.mu.Unlock(); return j.node }
func (j *screenJob) size() int64     { return int64(len(j.body) + len(j.window)) }

// screenerSnapshot is the admin / summary view of the screener's counters and queue.
type screenerSnapshot struct {
	Mode            string           `json:"mode"`
	Queued          int64            `json:"queued"`
	Screened        int64            `json:"screened"`
	Flagged         int64            `json:"flagged"`
	CSAM            int64            `json:"csam"`
	Dropped         map[string]int64 `json:"dropped"`
	Classifier429   int64            `json:"classifier_429"`
	ClassifierError int64            `json:"classifier_error"`
	QueueDepth      int              `json:"queue_depth"`
	QueueBytes      int64            `json:"queue_bytes"` // bytes retained (full bodies + windows) - the memory bound
	OldestAgeSecs   int64            `json:"oldest_age_secs"`
	LagMaxSecs      float64          `json:"lag_max_secs"`
	InFlight        int              `json:"in_flight"`
	Workers         int              `json:"workers"`
	IdleWorkers     int              `json:"idle_workers"`
	BackingOff      int              `json:"backing_off"` // workers asleep in a 429/error/budget wait
	// Flags is the ?pseudonym= lookup on GET /admin/moderation (metadata only; the sealed
	// window is never serialized). Empty otherwise.
	Flags []store.ModerationFlag `json:"flags,omitempty"`
}

type screener struct {
	b       *broker
	cfg     screenerConfig
	mode    string
	enabled bool

	// Clock seam: now + an interruptible sleep (false when the screener is stopping). Tests
	// run backoff / lag / summary scenarios on a virtual clock through these two.
	now    func() time.Time
	sleep  func(time.Duration) bool
	stop   chan struct{}
	ctx    context.Context // canceled with stop: aborts in-flight classifier calls
	cancel context.CancelFunc

	stopOnce    sync.Once
	summaryOnce sync.Once
	workerWG    sync.WaitGroup

	mu      sync.Mutex
	cond    *sync.Cond
	q       []*screenJob
	qBytes  int64 // retained bytes (bodies + windows) - the memory bound
	closed  bool  // no more accepts; workers finish the queue then exit
	stopped bool  // workers exit now

	workers, idle, inflight, backingOff int
	holdUntil                           time.Time // global 429/error hold-off
	budgetStart                         time.Time
	budgetUsed                          int
	failOnset                           time.Time // first failure of the current outage (zero = healthy)
	lastErr                             string    // error class of the latest classifier failure
	repeatAt                            map[string]time.Time

	queued, screened, flagged, csam, c429, cerr int64
	dropped                                     map[string]int64
	lagMax                                      time.Duration

	winStart   time.Time
	winTotal   int64
	winDropped map[string]int64
}

func newScreener(b *broker, cfg screenerConfig) *screener {
	s := &screener{b: b, cfg: cfg, mode: b.mod.mode, now: time.Now, stop: make(chan struct{}),
		dropped: map[string]int64{}, winDropped: map[string]int64{}, repeatAt: map[string]time.Time{}}
	s.cond = sync.NewCond(&s.mu)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.sleep = func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-s.stop:
			return false
		}
	}
	s.winStart = s.now()
	switch s.mode {
	case modeOff:
		log.Printf("MODERATION: OFF (ROGERAI_MODERATION_MODE=off) - the chat relay is not screened")
	case modeSync:
		log.Printf("MODERATION: sync (legacy in-line gate) - the chat relay waits on the classifier before dispatch")
	default:
		s.mode = modeAsync
		log.Printf("MODERATION: async (off-path, best-effort) window=%d queue=%d/%dMiB workers=%d tpm=%d max_lag=%s",
			cfg.window, cfg.queue, cfg.queueBytes>>20, cfg.workers, cfg.tpm, cfg.maxLag)
		if !b.mod.configured() {
			log.Printf("MODERATION: no classifier configured - relays are UNSCREENED (set MODERATION_GROQ_KEY or MODERATION_URL)")
		} else {
			s.enabled = true
		}
	}
	return s
}

// start adds n workers (and, once, the summary loop). A no-op unless async + configured.
func (s *screener) start(n int) {
	if s == nil || !s.enabled {
		return
	}
	s.summaryOnce.Do(func() { go s.summaryLoop() })
	s.mu.Lock()
	s.workers += n
	s.mu.Unlock()
	for i := 0; i < n; i++ {
		s.workerWG.Add(1)
		go s.worker()
	}
}

// submit hands one relay to the screener and returns immediately. It never blocks: a full
// queue or an exhausted byte budget drops the job (counted + logged). The byte budget is a
// high-water mark: a job is admitted while the bytes already held are under budget (so one
// job may carry the total past it, bounded by the relay's own body limit), and refused once
// they reach it. nil when nothing was queued (disabled, empty text, or dropped) - the relay
// ignores the result either way.
func (s *screener) submit(requestID, user, ip, model string, body []byte, text string) *screenJob {
	if s == nil || !s.enabled || strings.TrimSpace(text) == "" {
		return nil
	}
	job := &screenJob{id: requestID, pseudonym: s.b.pseudonym(user, "relay"), ip: ip, model: model,
		body: body, window: screenWindow(text, s.cfg.window), enqueued: s.now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	reason := ""
	switch {
	case s.closed:
		reason = "shutdown"
	case len(s.q) >= s.cfg.queue:
		reason = "queue-full"
	case s.qBytes >= s.cfg.queueBytes:
		reason = "queue-bytes"
	}
	if reason != "" {
		s.dropLocked(job, reason)
		return nil
	}
	s.q = append(s.q, job)
	s.qBytes += job.size()
	s.queued++
	s.cond.Signal()
	return job
}

// screenWindow bounds the text sent to the classifier: prompts up to `window` runes go whole;
// longer ones are screened as head (3/4) + tail (1/4) around one elision seam, split on rune
// boundaries so a multi-byte character is never cut.
func screenWindow(text string, window int) string {
	if window <= 0 || utf8.RuneCountInString(text) <= window {
		return text
	}
	head, tail := window*3/4, window-window*3/4
	hi := 0
	for i := range text {
		if head == 0 {
			hi = i
			break
		}
		head--
	}
	ti := len(text)
	for n := 0; n < tail && ti > hi; n++ {
		_, size := utf8.DecodeLastRuneInString(text[:ti])
		ti -= size
	}
	return fmt.Sprintf("%s\n[... %d chars elided ...]\n%s", text[:hi], utf8.RuneCountInString(text[hi:ti]), text[ti:])
}

func (s *screener) worker() {
	defer s.workerWG.Done()
	for {
		job := s.pop()
		if job == nil {
			return
		}
		s.process(job)
	}
}

func (s *screener) pop() *screenJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idle++
	for len(s.q) == 0 && !s.stopped && !s.closed {
		s.cond.Wait()
	}
	s.idle--
	if s.stopped || len(s.q) == 0 {
		return nil
	}
	job := s.q[0]
	s.q[0] = nil
	s.q = s.q[1:]
	s.qBytes -= job.size()
	return job
}

// process applies the verdict policy to one job, retrying classifier outages with backoff
// until the job goes stale. Waits go through nap (the clock seam) and count as backing off.
func (s *screener) process(job *screenJob) {
	for {
		now := s.now()
		if now.Sub(job.enqueued) > s.cfg.maxLag {
			s.drop(job, "stale")
			return
		}
		s.mu.Lock()
		wait := s.holdUntil.Sub(now)
		s.mu.Unlock()
		if wait <= 0 {
			wait = s.budgetWait(job)
		}
		if wait > 0 {
			if !s.nap(wait) {
				s.drop(job, "shutdown")
				return
			}
			continue
		}
		s.mu.Lock()
		s.inflight++
		s.mu.Unlock()
		res, cerr := s.b.mod.classifyOffPath(s.ctx, job.window)
		s.mu.Lock()
		s.inflight--
		stopped := s.stopped
		s.mu.Unlock()
		if cerr != nil {
			if stopped { // the call was cut off by shutdown, not by the classifier
				s.drop(job, "shutdown")
				return
			}
			s.noteFailure(job, cerr)
			continue
		}
		s.noteSuccess(job, res)
		return
	}
}

func (s *screener) nap(d time.Duration) bool {
	s.mu.Lock()
	s.backingOff++
	s.mu.Unlock()
	ok := s.sleep(d)
	s.mu.Lock()
	s.backingOff--
	s.mu.Unlock()
	return ok
}

// budgetWait reserves the job's estimated classifier tokens - (policy prompt + window) chars/4,
// since the policy rides on every call - against the per-minute budget, or returns how long to
// defer until the next minute window. A job larger than the whole budget still runs when the
// window is empty (defer, never starve).
func (s *screener) budgetWait(job *screenJob) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.budgetStart.IsZero() || now.Sub(s.budgetStart) >= time.Minute {
		s.budgetStart, s.budgetUsed = now, 0
	}
	est := (len(moderationPolicy)+len(job.window))/4 + 1
	if s.budgetUsed > 0 && s.budgetUsed+est > s.cfg.tpm {
		return s.budgetStart.Add(time.Minute).Sub(now)
	}
	s.budgetUsed += est
	return 0
}

// backoffFor is the classifier backoff when no Retry-After was given: 1s, 2s, 4s, ... plus up
// to 25% jitter, capped at screenerBackoffCap.
func backoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	base := time.Second << uint(attempt-1)
	d := base + time.Duration(rand.Int63n(int64(base/4)+1))
	if d > screenerBackoffCap {
		d = screenerBackoffCap
	}
	return d
}

func (s *screener) noteFailure(job *screenJob, cerr *classifierErr) {
	job.attempts++
	job.last429 = cerr.status == http.StatusTooManyRequests
	back := cerr.retryAfter
	if back <= 0 {
		back = backoffFor(job.attempts)
	}
	if back > s.cfg.maxLag {
		back = s.cfg.maxLag
	}
	s.mu.Lock()
	now := s.now()
	if job.last429 {
		s.c429++
	} else {
		s.cerr++
	}
	if until := now.Add(back); until.After(s.holdUntil) {
		s.holdUntil = until
	}
	onset := s.failOnset.IsZero()
	if onset {
		s.failOnset = now
	}
	down := now.Sub(s.failOnset) >= screenerDownAfter
	drops := sumCounts(s.dropped)
	c429, cerrN := s.c429, s.cerr
	s.lastErr = cerr.what
	s.mu.Unlock()
	if onset {
		log.Printf("MODERATION: classifier failing (%s) - backing off %s and retrying off-path (relays are unaffected)", cerr.what, back)
	}
	if down {
		s.alertDown(cerr.what, drops, c429, cerrN)
	}
}

// alertDown pages the founder ONCE (onset dedup in adminAlert) for a sustained outage.
func (s *screener) alertDown(what string, drops, c429, cerrN int64) {
	s.mu.Lock()
	since := s.failOnset.UTC().Format(time.RFC3339)
	s.mu.Unlock()
	s.b.adminAlert("moderation_down", "content classifier down", "Content classifier failing for 15+ minutes",
		[][2]string{{"Error class", what}, {"Failing since", since},
			{"Dropped so far", fmt.Sprintf("%d", drops)}, {"Classifier 429s", fmt.Sprintf("%d", c429)}, {"Classifier errors", fmt.Sprintf("%d", cerrN)}},
		"Every classifier call has failed for 15 minutes. Relays are being served (off-path screening never blocks them); jobs past the max lag are being dropped unscreened and counted.")
}

// checkDown re-evaluates the outage on the summary tick, so a classifier that has been failing
// for 15 minutes pages even when no job happens to be retrying at that instant.
func (s *screener) checkDown() {
	s.mu.Lock()
	down := !s.failOnset.IsZero() && s.now().Sub(s.failOnset) >= screenerDownAfter
	what, drops, c429, cerrN := s.lastErr, sumCounts(s.dropped), s.c429, s.cerr
	s.mu.Unlock()
	if down {
		s.alertDown(what, drops, c429, cerrN)
	}
}

func (s *screener) noteSuccess(job *screenJob, res modResult) {
	s.mu.Lock()
	now := s.now()
	recovered := !s.failOnset.IsZero()
	s.failOnset = time.Time{}
	s.screened++
	s.winTotal++
	if lag := now.Sub(job.enqueued); lag > s.lagMax {
		s.lagMax = lag
	}
	switch {
	case res.csam:
		s.csam++
	case res.status == http.StatusUnavailableForLegalReasons:
		s.flagged++
	}
	s.mu.Unlock()
	if recovered {
		log.Printf("MODERATION: classifier recovered (request=%s screened after %d retries)", job.id, job.attempts)
		s.b.alertClear("moderation_down")
	}
	switch {
	case res.csam:
		// The obligation lands exactly as the in-line gate's did: PRESERVE the FULL body sealed,
		// QUEUE the CyberTipline report, page the founder. The relay was already served.
		s.b.preserveCSAM(job.pseudonym, job.ip, res.category, job.body)
	case res.status == http.StatusUnavailableForLegalReasons:
		s.recordFlag(job, res.category)
	}
}

// recordFlag RECORDS a block-net verdict against the consumer pseudonym (never enforced) and
// pages the founder once per day per pseudonym once it reaches repeatFlagThreshold flags.
func (s *screener) recordFlag(job *screenJob, category string) {
	now := s.now()
	f := store.ModerationFlag{Pseudonym: job.pseudonym, RequestID: job.id, Model: job.model, Node: job.station(),
		Category: category, Window: s.b.encryptCSAM([]byte(job.window)), CreatedAt: now.Unix()}
	if _, err := s.b.db.AddModerationFlag(f); err != nil {
		log.Printf("MODERATION: flag record FAILED (category=%s request=%s pseudonym=%s): %v", category, job.id, job.pseudonym, err)
		return
	}
	log.Printf("MODERATION: flagged after the fact (category=%s request=%s pseudonym=%s model=%s node=%s) - recorded for review, not enforced",
		category, job.id, job.pseudonym, job.model, f.Node)
	flags, err := s.b.db.ModerationFlagsByPseudonym(job.pseudonym, now.Add(-24*time.Hour).Unix(), 0)
	if err != nil || len(flags) < repeatFlagThreshold {
		return
	}
	s.mu.Lock()
	last, seen := s.repeatAt[job.pseudonym]
	due := !seen || now.Sub(last) >= 24*time.Hour
	if due {
		s.repeatAt[job.pseudonym] = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	cats := map[string]bool{}
	for _, fl := range flags {
		cats[fl.Category] = true
	}
	key := "moderation:repeat-flags:" + job.pseudonym
	s.b.alertClear(key) // a new day re-arms the onset dedup
	s.b.adminAlert(key, "repeat moderation flags on "+job.pseudonym, "Repeated block-net flags on one consumer",
		[][2]string{{"Pseudonym", job.pseudonym}, {"Flags (24h)", fmt.Sprintf("%d", len(flags))},
			{"Categories", strings.Join(sortedKeys(cats), ", ")}, {"Lookup", "GET /admin/moderation?pseudonym=" + job.pseudonym}},
		"One consumer accumulated repeated block-net verdicts today. Nothing was auto-blocked; review the flags and decide.")
}

func (s *screener) drop(job *screenJob, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropLocked(job, reason)
}

func (s *screener) dropLocked(job *screenJob, reason string) {
	s.dropped[reason]++
	s.winTotal++
	s.winDropped[reason]++
	age := s.now().Sub(job.enqueued).Round(time.Millisecond)
	if reason == "stale" {
		why := "error backoff"
		if job.last429 {
			why = "429 backoff"
		} else if job.attempts == 0 {
			why = "budget defer"
		}
		log.Printf("MODERATION SKIPPED (stale after %s) request=%s pseudonym=%s model=%s age=%s attempts=%d - served UNSCREENED, counted", why, job.id, job.pseudonym, job.model, age, job.attempts)
		return
	}
	log.Printf("MODERATION DROPPED (%s) request=%s pseudonym=%s model=%s queue=%d/%d bytes=%d/%d - served UNSCREENED, counted",
		reason, job.id, job.pseudonym, job.model, len(s.q), s.cfg.queue, s.qBytes, s.cfg.queueBytes)
}

func sumCounts(m map[string]int64) int64 {
	var t int64
	for _, v := range m {
		t += v
	}
	return t
}

func (s *screener) summaryLoop() {
	for s.sleep(screenerSummaryEvery) {
		snap := s.snapshot()
		log.Printf("MODERATION: 5m summary screened=%d flagged=%d csam=%d dropped=%d 429=%d lag_max=%.0fs queue=%d/%d bytes=%d in_flight=%d",
			snap.Screened, snap.Flagged, snap.CSAM, sumCounts(snap.Dropped), snap.Classifier429, snap.LagMaxSecs,
			snap.QueueDepth, s.cfg.queue, snap.QueueBytes, snap.InFlight)
		s.checkDropRate()
		s.checkDown()
	}
}

// checkDropRate pages the founder once when more than 20% of the jobs finished in the last
// drop window were dropped (any reason), naming the dominant reason; clears on a healthy window.
func (s *screener) checkDropRate() {
	s.mu.Lock()
	now := s.now()
	if now.Sub(s.winStart) < screenerDropWindow {
		s.mu.Unlock()
		return
	}
	total, dropped := s.winTotal, sumCounts(s.winDropped)
	dominant, top := "", int64(0)
	for r, n := range s.winDropped {
		if n > top {
			dominant, top = r, n
		}
	}
	s.winStart, s.winTotal, s.winDropped = now, 0, map[string]int64{}
	s.mu.Unlock()
	if total >= screenerDropMinSample && dropped*5 > total {
		s.b.adminAlert("moderation_drops", "content screening dropping jobs", "Off-path screening is dropping jobs",
			[][2]string{{"Window", screenerDropWindow.String()}, {"Jobs", fmt.Sprintf("%d", total)}, {"Dropped", fmt.Sprintf("%d", dropped)},
				{"Dominant reason", dominant}},
			"More than 20% of screening jobs were dropped unscreened in the last window. Relays are unaffected; raise the queue/budget knobs or check the classifier.")
		return
	}
	s.b.alertClear("moderation_drops")
}

// shutdown stops accepting, lets the workers drain what fits in the budget, then counts the
// remainder as dropped ("shutdown") in one final line. Nil-safe; idempotent.
func (s *screener) shutdown(budget time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	already := s.closed
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	if already {
		return
	}
	done := make(chan struct{})
	go func() { s.workerWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(budget):
	}
	s.mu.Lock()
	s.stopped = true
	s.cond.Broadcast()
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop); s.cancel() }) // wakes sleeping workers, aborts in-flight calls
	s.workerWG.Wait()                                   // prompt: every wait and call is interruptible
	s.mu.Lock()
	rest := s.q
	s.q, s.qBytes = nil, 0
	for _, j := range rest {
		s.dropLocked(j, "shutdown")
	}
	screened, dropped, shut := s.screened, sumCounts(s.dropped), s.dropped["shutdown"]
	s.mu.Unlock()
	if s.enabled {
		log.Printf("MODERATION: shutdown screened=%d dropped=%d (shutdown=%d)", screened, dropped, shut)
	}
}

func (s *screener) snapshot() screenerSnapshot {
	if s == nil {
		return screenerSnapshot{Dropped: map[string]int64{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := screenerSnapshot{Mode: s.mode, Queued: s.queued, Screened: s.screened, Flagged: s.flagged, CSAM: s.csam,
		Dropped: map[string]int64{}, Classifier429: s.c429, ClassifierError: s.cerr,
		QueueDepth: len(s.q), QueueBytes: s.qBytes, LagMaxSecs: s.lagMax.Seconds(),
		InFlight: s.inflight, Workers: s.workers, IdleWorkers: s.idle, BackingOff: s.backingOff}
	for k, v := range s.dropped {
		out.Dropped[k] = v
	}
	if len(s.q) > 0 {
		out.OldestAgeSecs = int64(s.now().Sub(s.q[0].enqueued).Seconds())
	}
	return out
}

// adminModeration handles GET /admin/moderation (admin-authed via the SAME requireAdmin gate
// as every other admin route - 403 to anything that is not the founder): the screener's
// counters + queue state, plus ?pseudonym= to list one consumer's flags (metadata only; the
// sealed window is never serialized).
func (b *broker) adminModeration(w http.ResponseWriter, r *http.Request) {
	if corsCredsPreflight(w, r) {
		return
	}
	corsCreds(w, r)
	if !allow(w, r, http.MethodGet) {
		return
	}
	if b.requireAdmin(w, r) {
		return
	}
	snap := b.scr.snapshot()
	if p := r.URL.Query().Get("pseudonym"); p != "" {
		flags, err := b.db.ModerationFlagsByPseudonym(p, 0, 100)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "store error")
			return
		}
		snap.Flags = flags
	}
	writeJSON(w, http.StatusOK, snap)
}
