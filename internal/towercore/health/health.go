// Package health keeps Core's health-check evidence per Tower: hub checks and Station canaries,
// counted per day for the trust ramp and summarized as the last two results for the health
// tier. Contracts: features/tower/tower_trust_ramp.feature, placement_ranking.feature.
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

// Sources of a health check.
const (
	SourceHubCheck = "hub_check"
	SourceCanary   = "canary"
)

// Check is one health-check result.
type Check struct {
	OK     bool
	At     time.Time
	Source string
	RTT    time.Duration
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
	// Record files a Station canary's result; RecordCheck files any check with its source and
	// round trip.
	Record(towerID string, ok bool, at time.Time) error
	RecordCheck(towerID string, c Check) error
	Summary(towerID string, since time.Time) (Summary, error)
	Reap(before time.Time) error
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

type latest struct {
	at     time.Time
	streak int
	since  time.Time
	source string
	rtt    time.Duration
}

// advance folds one result into the latest fields; a result older than the latest is counted
// per day but does not rewrite them.
func (l latest) advance(c Check) latest {
	ok, at := c.OK, c.At
	if !l.at.IsZero() && at.Before(l.at) {
		return l
	}
	l.at, l.source, l.rtt = at, c.Source, c.RTT
	switch {
	case ok:
		l.streak, l.since = 0, time.Time{}
	case l.streak == 0:
		l.streak, l.since = 1, at
	default:
		l.streak++
	}
	return l
}

// MemStore is the in-process Store.
type MemStore struct {
	mu    sync.Mutex
	days  map[string]map[time.Time][2]int
	heads map[string]latest
}

func NewMemStore() *MemStore {
	return &MemStore{days: map[string]map[time.Time][2]int{}, heads: map[string]latest{}}
}

func (m *MemStore) Record(towerID string, ok bool, at time.Time) error {
	return m.RecordCheck(towerID, Check{OK: ok, At: at, Source: SourceCanary})
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
	m.heads[towerID] = m.heads[towerID].advance(c)
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
	s.LastAt, s.FailStreak, s.StreakSince, s.LastSource, s.LastRTT = h.at, h.streak, h.since, h.source, h.rtt
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
    last_rtt_ms  BIGINT NOT NULL DEFAULT 0
)`

func NewPGStore(db *sql.DB) (*PGStore, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

func (p *PGStore) Record(towerID string, ok bool, at time.Time) error {
	return p.RecordCheck(towerID, Check{OK: ok, At: at, Source: SourceCanary})
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
	// The latest result moves only forward in time, in one statement, so two instances
	// recording at once cannot interleave a read and a write.
	_, err := p.db.Exec(`
        INSERT INTO rogerai.tower_health (tower_id, last_at, fail_streak, streak_since, last_source, last_rtt_ms)
        VALUES ($1, $3::timestamptz, CASE WHEN $2::boolean THEN 0 ELSE 1 END,
                CASE WHEN $2::boolean THEN NULL ELSE $3::timestamptz END, $4, $5)
        ON CONFLICT (tower_id) DO UPDATE
          SET last_at = EXCLUDED.last_at, last_source = EXCLUDED.last_source, last_rtt_ms = EXCLUDED.last_rtt_ms,
              fail_streak = CASE WHEN $2::boolean THEN 0 ELSE tower_health.fail_streak + 1 END,
              streak_since = CASE WHEN $2::boolean THEN NULL
                                  WHEN tower_health.fail_streak = 0 THEN EXCLUDED.last_at
                                  ELSE tower_health.streak_since END
          WHERE tower_health.last_at <= EXCLUDED.last_at`,
		towerID, ok, at.UTC(), c.Source, c.RTT.Milliseconds())
	return err
}

func (p *PGStore) Summary(towerID string, since time.Time) (Summary, error) {
	var s Summary
	if err := p.db.QueryRow(`SELECT COALESCE(SUM(passed),0), COALESCE(SUM(failed),0)
          FROM rogerai.tower_health_daily WHERE tower_id = $1 AND day >= $2`, towerID, dayOf(since)).
		Scan(&s.Passed, &s.Failed); err != nil {
		return Summary{}, err
	}
	var began sql.NullTime
	var rttMs int64
	err := p.db.QueryRow(`SELECT last_at, fail_streak, streak_since, last_source, last_rtt_ms FROM rogerai.tower_health WHERE tower_id = $1`,
		towerID).Scan(&s.LastAt, &s.FailStreak, &began, &s.LastSource, &rttMs)
	s.LastRTT = time.Duration(rttMs) * time.Millisecond
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
