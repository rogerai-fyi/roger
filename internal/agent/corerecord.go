package agent

// corerecord.go - what Core's record says this share served and earned
// (features/serving/probe_accounting.feature R1). The share screen shows these numbers, never
// the node's own count: Core's checks look like customers to the node by design, so only Core
// knows which jobs were paid. The record is read from GET /nodes/served with the node's own
// token, at most every 30 seconds, and only while something reads it: a share nobody is
// looking at reads nothing.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// coreReadEvery is how often the share screen reads Core's record: just over Core's own 30 s
// limit, so network jitter does not turn every other read into a refusal.
const coreReadEvery = 31 * time.Second

// coreRecordClient reads the record; a seam so a test can stand in for a slow network.
var coreRecordClient = &http.Client{Timeout: 10 * time.Second}

type coreNumbers struct {
	served, tokens int64
	earned         float64
	current        bool // Core's latest answer; false before it first answered and while it cannot
}

type coreRecord struct {
	mu       sync.Mutex
	n        coreNumbers
	answered bool // Core has answered at least once
	lastTry  time.Time
	fetching bool
}

// read returns the last numbers Core gave and, when they are due, starts reading fresh ones.
func (c *coreRecord) read(s *Session) coreNumbers {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.fetching && s.rereg != nil && time.Since(c.lastTry) >= coreReadEvery {
		c.fetching, c.lastTry = true, time.Now()
		go c.fetch(s)
	}
	return c.n
}

func (c *coreRecord) fetch(s *Session) {
	n, ok, throttled := s.readCoreRecord()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetching = false
	switch {
	case ok:
		c.n, c.answered = n, true
	case throttled:
		// Core says it was asked inside 30 s (another read of the same node): the numbers held
		// are no older than that, and stay as current as they were.
	default:
		c.n.current = false // the last numbers Core gave, no longer current
	}
}

// readCoreRecord asks Core for this node's served and earned since the share went on air.
func (s *Session) readCoreRecord() (n coreNumbers, ok, throttled bool) {
	tok, _ := s.rereg.curToken()
	u := s.cfg.Broker + "/nodes/served?node=" + url.QueryEscape(s.cfg.NodeID) +
		"&since=" + strconv.FormatInt(s.started.Unix(), 10)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return n, false, false
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := coreRecordClient.Do(req)
	if err != nil {
		return n, false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return n, false, true
	}
	if resp.StatusCode != http.StatusOK {
		return n, false, false
	}
	var out struct {
		Served int64   `json:"served"`
		Tokens int64   `json:"tokens"`
		Earned float64 `json:"earned"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out) != nil {
		return n, false, false
	}
	return coreNumbers{served: out.Served, tokens: out.Tokens, earned: out.Earned, current: true}, true, false
}

// BridgeToken returns the node's live bridge token, the one its polls carry.
func (s *Session) BridgeToken() string {
	if s.rereg == nil {
		return ""
	}
	tok, _ := s.rereg.curToken()
	return tok
}

// ExpireCoreForTest makes the next read of Served or Earnings read Core's record again at once.
func (s *Session) ExpireCoreForTest() {
	s.core.mu.Lock()
	s.core.lastTry = time.Time{}
	s.core.mu.Unlock()
}

// SetCoreForTest sets what Core's record says, as if Core had answered (current) or as the last
// numbers it gave (not current). Test-only seam, for tests in other packages (the TUI's).
func (s *Session) SetCoreForTest(served, tokens int64, earned float64, current bool) {
	s.core.mu.Lock()
	s.core.n = coreNumbers{served: served, tokens: tokens, earned: earned, current: current}
	s.core.answered, s.core.lastTry = true, time.Now()
	s.core.mu.Unlock()
}

// CoreAnswered reports whether Core has given this session any numbers yet.
func (s *Session) CoreAnswered() bool {
	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	return s.core.answered
}
