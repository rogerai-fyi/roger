// Package health keeps Core's health-check evidence per Tower: hub checks and Station canaries,
// counted per day for the trust ramp, and each kind's run of failures for the health tier. Contracts: features/tower/tower_trust_ramp.feature, placement_ranking.feature.
package health

import (
	"database/sql"
	"sync"
	"time"
)

// Summary is a Tower's health evidence since a day.
type Summary struct {
	Passed, Failed int
	// When the latest result was taken, how many failures in a row end the record, and when
	// that run of failures began (zero with no failures).
	LastAt      time.Time
	FailStreak  int
	StreakSince time.Time
	// The latest check's source (a hub check or a Station canary) and its round trip.
	LastSource string
	LastRTT    time.Duration
}

// RunStaleAfter is how long a kind of check may go unrefreshed, counting back from the Tower's
// latest result of any kind, before its run stops counting toward the Tower's streak: two
// canary intervals, the staleness rule station_mobility.feature uses for observations. It is
// what lets a Tower whose Stations were moved off - so no canary can run - recover on its hub
// checks.
const RunStaleAfter = 10 * time.Minute

// Sources of a health check.
const (
	SourceHubCheck = "hub_check"
	SourceCanary   = "canary"
)

// Check is one health-check result. Slot is the canary interval it belongs to (the broker
// computes it once per sweep): a run counts at most one failure per slot, so a hub check and a
// canary failing in the same sweep are one outage. MinGap is the least time between two counted
// failures of one run, so two broker instances recording one blip moments apart - across a
// slot boundary - count it once.
type Check struct {
	OK     bool
	At     time.Time
	Source string
	RTT    time.Duration
	Slot   int64
	MinGap time.Duration
}

// Rate is the pass rate, 0 with no evidence.
func (s Summary) Rate() float64 {
	if s.Passed+s.Failed == 0 {
		return 0
	}
	return float64(s.Passed) / float64(s.Passed+s.Failed)
}

// BothLastFailed is the health tier's test: the last two checks both failed.
func (s Summary) BothLastFailed() bool { return s.FailStreak >= 2 }

// Store records results and answers summaries; every instance shares the PG one.
type Store interface {
	// RecordCheck files one check with its source, round trip and slot.
	RecordCheck(towerID string, c Check) error
	Summary(towerID string, since time.Time) (Summary, error)
	Reap(before time.Time) error
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// latest is the most recent result of any kind.
type latest struct {
	at     time.Time
	source string
	rtt    time.Duration
}

// run is one kind of check's run of failures (founder ruling 2026-11-14: a passing hub check
// does not clear canary failures, nor a passing canary hub-check failures). A Tower's streak is
// its longer run.
type run struct {
	streak     int
	since      time.Time
	slot       int64 // the latest slot this run has seen
	slotFailed bool  // a failure in that slot is already counted
	lastFail   time.Time
	lastAt     time.Time // the latest result of this kind
	seen       bool
}

// advance folds one result of this run's kind in: a result from an earlier slot never moves
// it, a pass resets it, and a failure counts once per slot and never closer than MinGap to the
// last counted one.
func (r run) advance(c Check) run {
	if r.seen && c.At.Sub(r.lastAt) > RunStaleAfter {
		// This kind went quiet longer than the window: what it said before no longer speaks
		// for the Tower, so the run starts over from this result.
		r = run{}
	}
	if c.At.After(r.lastAt) {
		r.lastAt = c.At
	}
	if r.seen && c.Slot < r.slot {
		return r
	}
	if !r.seen || c.Slot > r.slot {
		r.slot, r.slotFailed, r.seen = c.Slot, false, true
	}
	switch {
	case c.OK:
		r.streak, r.since, r.slotFailed, r.lastFail = 0, time.Time{}, false, time.Time{}
	case r.slotFailed:
		// already counted in this slot
	case !r.lastFail.IsZero() && c.At.Sub(r.lastFail) < c.MinGap:
		// the same blip, recorded again moments later
	case r.streak == 0:
		r.streak, r.since, r.slotFailed, r.lastFail = 1, c.At, true, c.At
	default:
		r.streak, r.slotFailed, r.lastFail = r.streak+1, true, c.At
	}
	return r
}

// MemStore is the in-process Store.
type MemStore struct {
	mu    sync.Mutex
	days  map[string]map[time.Time][2]int
	heads map[string]latest
	runs  map[string]map[string]run // tower -> source -> run
}

func NewMemStore() *MemStore {
	return &MemStore{days: map[string]map[time.Time][2]int{}, heads: map[string]latest{},
		runs: map[string]map[string]run{}}
}

func (m *MemStore) RecordCheck(towerID string, c Check) error {
	ok, at := c.OK, c.At
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.days[towerID] == nil {
		m.days[towerID] = map[time.Time][2]int{}
	}
	n := m.days[towerID][dayOf(at)]
	if ok {
		n[0]++
	} else {
		n[1]++
	}
	m.days[towerID][dayOf(at)] = n
	c.At = at.UTC()
	if h := m.heads[towerID]; h.at.IsZero() || !c.At.Before(h.at) {
		m.heads[towerID] = latest{at: c.At, source: c.Source, rtt: c.RTT}
	}
	if m.runs[towerID] == nil {
		m.runs[towerID] = map[string]run{}
	}
	m.runs[towerID][c.Source] = m.runs[towerID][c.Source].advance(c)
	return nil
}

func (m *MemStore) Summary(towerID string, since time.Time) (Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var s Summary
	for d, c := range m.days[towerID] {
		if !d.Before(dayOf(since)) {
			s.Passed += c[0]
			s.Failed += c[1]
		}
	}
	h := m.heads[towerID]
	s.LastAt, s.LastSource, s.LastRTT = h.at, h.source, h.rtt
	for _, r := range m.runs[towerID] {
		if r.lastAt.Before(h.at.Add(-RunStaleAfter)) {
			continue // this kind has gone quiet: its run no longer speaks for the Tower
		}
		if r.streak > s.FailStreak || (r.streak == s.FailStreak && r.streak > 0 && r.since.Before(s.StreakSince)) {
			s.FailStreak, s.StreakSince = r.streak, r.since
		}
	}
	return s, nil
}

func (m *MemStore) Reap(before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, days := range m.days {
		for d := range days {
			if d.Before(dayOf(before)) {
				delete(days, d)
			}
		}
	}
	return nil
}

// PGStore is the shared Store.
type PGStore struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_health_daily (
    tower_id TEXT NOT NULL,
    day      DATE NOT NULL,
    passed   INT  NOT NULL DEFAULT 0,
    failed   INT  NOT NULL DEFAULT 0,
    PRIMARY KEY (tower_id, day)
);
CREATE TABLE IF NOT EXISTS rogerai.tower_health (
    tower_id     TEXT PRIMARY KEY,
    last_at      TIMESTAMPTZ NOT NULL,
    fail_streak  INT NOT NULL DEFAULT 0,
    streak_since TIMESTAMPTZ,
    last_source  TEXT NOT NULL DEFAULT '',
    last_rtt_ms  BIGINT NOT NULL DEFAULT 0,
    last_slot    BIGINT,
    slot_failed  BOOLEAN NOT NULL DEFAULT false
);
ALTER TABLE rogerai.tower_health ADD COLUMN IF NOT EXISTS last_source TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.tower_health ADD COLUMN IF NOT EXISTS last_rtt_ms BIGINT NOT NULL DEFAULT 0;
ALTER TABLE rogerai.tower_health ADD COLUMN IF NOT EXISTS last_slot BIGINT;
ALTER TABLE rogerai.tower_health ADD COLUMN IF NOT EXISTS slot_failed BOOLEAN NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS rogerai.tower_health_runs (
    tower_id     TEXT NOT NULL,
    source       TEXT NOT NULL,
    fail_streak  INT NOT NULL DEFAULT 0,
    streak_since TIMESTAMPTZ,
    last_slot    BIGINT NOT NULL,
    slot_failed  BOOLEAN NOT NULL DEFAULT false,
    last_fail_at TIMESTAMPTZ,
    last_at      TIMESTAMPTZ,
    PRIMARY KEY (tower_id, source)
);
ALTER TABLE rogerai.tower_health_runs ADD COLUMN IF NOT EXISTS last_at TIMESTAMPTZ`

func NewPGStore(db *sql.DB) (*PGStore, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

func (p *PGStore) RecordCheck(towerID string, c Check) error {
	ok, at := c.OK, c.At
	pass, fail := 0, 1
	if ok {
		pass, fail = 1, 0
	}
	if _, err := p.db.Exec(`
        INSERT INTO rogerai.tower_health_daily (tower_id, day, passed, failed) VALUES ($1, $2, $3, $4)
        ON CONFLICT (tower_id, day) DO UPDATE
          SET passed = tower_health_daily.passed + EXCLUDED.passed,
              failed = tower_health_daily.failed + EXCLUDED.failed`,
		towerID, dayOf(at), pass, fail); err != nil {
		return err
	}
	// The latest result moves forward in time over every kind of check.
	if _, err := p.db.Exec(`
        INSERT INTO rogerai.tower_health (tower_id, last_at, last_source, last_rtt_ms)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (tower_id) DO UPDATE SET
          last_at     = CASE WHEN EXCLUDED.last_at >= tower_health.last_at THEN EXCLUDED.last_at ELSE tower_health.last_at END,
          last_source = CASE WHEN EXCLUDED.last_at >= tower_health.last_at THEN EXCLUDED.last_source ELSE tower_health.last_source END,
          last_rtt_ms = CASE WHEN EXCLUDED.last_at >= tower_health.last_at THEN EXCLUDED.last_rtt_ms ELSE tower_health.last_rtt_ms END`,
		towerID, at.UTC(), c.Source, c.RTT.Milliseconds()); err != nil {
		return err
	}
	// This kind's run moves by slot (see run.advance), in one statement so two instances cannot
	// interleave a read and a write. "counts" is a failure in a new or not-yet-failed slot that
	// is at least MinGap after the run's last counted failure.
	_, err := p.db.Exec(`
        INSERT INTO rogerai.tower_health_runs AS r (tower_id, source, fail_streak, streak_since, last_slot, slot_failed, last_fail_at, last_at)
        VALUES ($1, $2, CASE WHEN $3::boolean THEN 0 ELSE 1 END, CASE WHEN $3::boolean THEN NULL ELSE $4::timestamptz END,
                $5, NOT $3::boolean, CASE WHEN $3::boolean THEN NULL ELSE $4::timestamptz END, $4::timestamptz)
        ON CONFLICT (tower_id, source) DO UPDATE SET
          last_at = GREATEST(COALESCE(r.last_at, $4::timestamptz), $4::timestamptz),
          fail_streak = CASE
            WHEN (r.last_at IS NOT NULL AND $4::timestamptz - r.last_at > make_interval(secs => $7)) THEN CASE WHEN $3::boolean THEN 0 ELSE 1 END
            WHEN $5 < r.last_slot THEN r.fail_streak
            WHEN $3::boolean THEN 0
            WHEN $5 = r.last_slot AND r.slot_failed THEN r.fail_streak
            WHEN r.last_fail_at IS NOT NULL AND $4::timestamptz - r.last_fail_at < make_interval(secs => $6) THEN r.fail_streak
            ELSE r.fail_streak + 1 END,
          streak_since = CASE
            WHEN (r.last_at IS NOT NULL AND $4::timestamptz - r.last_at > make_interval(secs => $7)) THEN CASE WHEN $3::boolean THEN NULL ELSE $4::timestamptz END
            WHEN $5 < r.last_slot THEN r.streak_since
            WHEN $3::boolean THEN NULL
            WHEN $5 = r.last_slot AND r.slot_failed THEN r.streak_since
            WHEN r.last_fail_at IS NOT NULL AND $4::timestamptz - r.last_fail_at < make_interval(secs => $6) THEN r.streak_since
            WHEN r.fail_streak = 0 THEN $4::timestamptz
            ELSE r.streak_since END,
          last_fail_at = CASE
            WHEN (r.last_at IS NOT NULL AND $4::timestamptz - r.last_at > make_interval(secs => $7)) THEN CASE WHEN $3::boolean THEN NULL ELSE $4::timestamptz END
            WHEN $5 < r.last_slot THEN r.last_fail_at
            WHEN $3::boolean THEN NULL
            WHEN $5 = r.last_slot AND r.slot_failed THEN r.last_fail_at
            WHEN r.last_fail_at IS NOT NULL AND $4::timestamptz - r.last_fail_at < make_interval(secs => $6) THEN r.last_fail_at
            ELSE $4::timestamptz END,
          slot_failed = CASE
            WHEN (r.last_at IS NOT NULL AND $4::timestamptz - r.last_at > make_interval(secs => $7)) THEN NOT $3::boolean
            WHEN $5 < r.last_slot THEN r.slot_failed
            WHEN $3::boolean THEN false
            WHEN $5 = r.last_slot AND r.slot_failed THEN true
            WHEN r.last_fail_at IS NOT NULL AND $4::timestamptz - r.last_fail_at < make_interval(secs => $6)
              THEN CASE WHEN $5 > r.last_slot THEN false ELSE r.slot_failed END
            ELSE true END,
          last_slot = CASE WHEN (r.last_at IS NOT NULL AND $4::timestamptz - r.last_at > make_interval(secs => $7)) THEN $5 ELSE GREATEST(r.last_slot, $5) END`,
		towerID, c.Source, ok, at.UTC(), c.Slot, c.MinGap.Seconds(), RunStaleAfter.Seconds())
	return err
}

func (p *PGStore) Summary(towerID string, since time.Time) (Summary, error) {
	var s Summary
	if err := p.db.QueryRow(`SELECT COALESCE(SUM(passed),0), COALESCE(SUM(failed),0)
          FROM rogerai.tower_health_daily WHERE tower_id = $1 AND day >= $2`, towerID, dayOf(since)).
		Scan(&s.Passed, &s.Failed); err != nil {
		return Summary{}, err
	}
	var rttMs int64
	err := p.db.QueryRow(`SELECT last_at, last_source, last_rtt_ms FROM rogerai.tower_health WHERE tower_id = $1`,
		towerID).Scan(&s.LastAt, &s.LastSource, &rttMs)
	if err == sql.ErrNoRows {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.LastRTT = time.Duration(rttMs) * time.Millisecond
	// The Tower's streak is its longer run; on a tie, the one that began first.
	var began sql.NullTime
	err = p.db.QueryRow(`SELECT fail_streak, streak_since FROM rogerai.tower_health_runs
          WHERE tower_id = $1 AND fail_streak > 0 AND COALESCE(last_at, '-infinity') >= $2::timestamptz
          ORDER BY fail_streak DESC, streak_since ASC LIMIT 1`,
		towerID, s.LastAt.Add(-RunStaleAfter)).Scan(&s.FailStreak, &began)
	if err == sql.ErrNoRows {
		return s, nil
	}
	if began.Valid {
		s.StreakSince = began.Time
	}
	return s, err
}

func (p *PGStore) Reap(before time.Time) error {
	_, err := p.db.Exec(`DELETE FROM rogerai.tower_health_daily WHERE day < $1`, dayOf(before))
	return err
}
