package edge

// THE TROUBLE FEED (features/edge/trouble_feed.feature): the owner's phone, a member holding a
// certificate this authority issued, PULLS the standing trouble calls from this node's mTLS face,
// learns what changed since it last looked (so it notifies once), and answers back - roger, stand
// by - under its own certificate, attributed by its name. A non-member or a revoked member gets
// nothing; nothing is ever pushed off the machine.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	TroublePath        = "/edge/trouble"
	TroubleRogerPath   = "/edge/trouble/roger"
	TroubleStandByPath = "/edge/trouble/standby"
)

// TroubleFeed is one read of the feed. Every time is unix seconds from THIS machine; the reader
// shows ages against its own read time.
type TroubleFeed struct {
	At          int64               `json:"at"`
	Cursor      string              `json:"cursor"`
	Standing    []TroubleItem       `json:"standing"`
	Transitions []TroubleTransition `json:"transitions"`
	Rate        int                 `json:"rate"`
	SignOff     TroubleSignOff      `json:"sign_off"`
}

// TroubleItem is one standing call, with why it matters and what to do.
type TroubleItem struct {
	Rule     string `json:"rule"`
	NodeID   string `json:"node_id"`
	Node     string `json:"node"`
	Metric   string `json:"metric"`
	When     string `json:"when"`
	Value    string `json:"value"`
	Priority string `json:"priority"`
	SinceS   int64  `json:"since_s"`
	Stale    bool   `json:"stale"`
	Acked    bool   `json:"acked"`
	AckBy    string `json:"ack_by,omitempty"`
	AckAt    int64  `json:"ack_at,omitempty"`
	Shelved  bool   `json:"shelved"`
	UntilAt  int64  `json:"until_at,omitempty"`
	StandBy  string `json:"standby_by,omitempty"` // who quieted it
	Why      string `json:"why"`
	Do       string `json:"do"`
}

// TroubleTransition is one change: a call fired or cleared, or a test call.
type TroubleTransition struct {
	At       int64  `json:"at"`
	Rule     string `json:"rule"`
	NodeID   string `json:"node_id"`
	Node     string `json:"node"`
	When     string `json:"when"`
	Firing   bool   `json:"firing"`
	Priority string `json:"priority"`
	Test     bool   `json:"test,omitempty"`
	// Unreported marks a power call closed because the node stopped reporting its power.
	Unreported bool `json:"unreported,omitempty"`
}

type TroubleSignOff struct {
	Raised       int              `json:"raised"`
	Cleared      int              `json:"cleared"`
	Loudest      string           `json:"loudest,omitempty"`
	LoudestCount int              `json:"loudest_count,omitempty"`
	DeadAir      []TroubleDeadAir `json:"dead_air"`
}

type TroubleDeadAir struct {
	Node string `json:"node"`
	ForS int64  `json:"for_s"`
}

// Priority ranks a call the ISA-18.2 way, three levels: HI needs a hand now (heat, a full disk,
// a worn card), MED soon (dark, memory), LOW when convenient.
func Priority(m Metric) string {
	switch m {
	case MetricCPUTemp, MetricDisk, MetricSDWear, MetricThrottled:
		return "HI"
	case MetricOffline, MetricMemory:
		return "MED"
	}
	return "LOW"
}

// PriorityRank orders HI before MED before LOW.
func PriorityRank(p string) int {
	switch p {
	case "HI":
		return 0
	case "MED":
		return 1
	}
	return 2
}

// WhyAndDo is a call's two sentences: why it matters, and what to do. Every call ships its own
// action - a call nobody can act on is a status line, not a call. One wording for every surface.
func WhyAndDo(m Metric) (string, string) {
	switch m {
	case MetricCPUTemp:
		return "a hot processor throttles, then fails early", "check the cooling and airflow · bind it a lighter model or job"
	case MetricDisk:
		return "a full disk stops new models loading and logs writing", "free space (old models, caches) · or move the store to a bigger disk"
	case MetricMemory:
		return "a full memory swaps and stalls every job on it", "bind a smaller model · stop a roger you do not need on it"
	case MetricOffline:
		return "a dark unit serves nothing and its jobs wait", "check its power and network · if it is gone for good, forget it"
	case MetricSDWear:
		return "a worn card corrupts silently, then will not boot", "back it up now · replace the card before it fails"
	case MetricCPULoad:
		return "a saturated processor answers late", "spread the jobs · or add a unit to the rack"
	case MetricThrottled:
		return "a Pi short of power slows itself and corrupts its card", "use the official supply and a short, thick cable · check what it powers over USB"
	}
	return "a rule you set is over its line", "look at the unit · adjust the rule if the line was wrong"
}

// FormatValue words a call's value in its metric's unit.
func FormatValue(m Metric, v float64) string {
	switch m {
	case MetricCPUTemp:
		return fmt.Sprintf("%.1f°C", v)
	case MetricOffline:
		return Age(time.Duration(v * float64(time.Minute)))
	case MetricCPULoad:
		return fmt.Sprintf("%.2f", v)
	case MetricThrottled:
		if v > 0 {
			return "now"
		}
		return "clear"
	}
	return fmt.Sprintf("%.0f%%", v)
}

// Handle is a login as a handle, never a whole address on a screen or in a feed.
func Handle(who string) string {
	if i := strings.IndexByte(who, '@'); i > 0 {
		return who[:i]
	}
	return who
}

// feedSlack is how far before the reader's cursor transitions are still sent: a judgement that
// began before the last read and landed after it carries an earlier time than the cursor, and
// must not be lost. The reader dedupes by (rule, node, at).
const feedSlack = 2 * time.Minute

// Feed is the feed as of now: the standing calls, the transitions after since (the last hour
// when since is zero), the rate and the sign-off, with node ids worded by names. One read of the
// history, under the log's lock, so a feed is one consistent moment.
func (l AlertLog) Feed(since, now time.Time, names func(id string) string) (feed TroubleFeed, err error) {
	err = l.withLock(func() error {
		hist, err := l.Recent(1 << 20)
		if err != nil {
			return err
		}
		calls, rate, so, err := l.reportFrom(hist, now)
		if err != nil {
			return err
		}
		feed = l.feedFrom(hist, calls, rate, so, since, now, names)
		return nil
	})
	return feed, err
}

func (l AlertLog) feedFrom(hist []Alert, calls []TroubleCall, rate int, so SignOffReport, since, now time.Time, names func(id string) string) TroubleFeed {
	name := func(id, fallback string) string {
		if names != nil {
			if n := names(id); n != "" && n != id {
				return n
			}
		}
		if fallback != "" {
			return fallback
		}
		return id
	}
	feed := TroubleFeed{At: now.Unix(), Cursor: strconv.FormatInt(now.UnixNano(), 10), Standing: []TroubleItem{},
		Transitions: []TroubleTransition{}, Rate: rate, SignOff: TroubleSignOff{Raised: so.Raised, Cleared: so.Cleared,
			Loudest: so.Loudest, LoudestCount: so.LoudestCount, DeadAir: []TroubleDeadAir{}}}
	SortCalls(calls)
	for _, c := range calls {
		why, do := WhyAndDo(c.Metric)
		it := TroubleItem{Rule: c.RuleID, NodeID: c.NodeID, Node: name(c.NodeID, c.Node), Metric: string(c.Metric), When: c.When,
			Value: FormatValue(c.Metric, c.Value), Priority: Priority(c.Metric), SinceS: int64(c.Since.Seconds()), Stale: c.Stale,
			Acked: c.Acked, AckBy: Handle(c.AckBy), Shelved: c.Shelved, Why: why, Do: do}
		if c.Acked {
			it.AckAt = c.AckAt.Unix()
		}
		if c.Shelved {
			it.UntilAt, it.StandBy = c.StandByUntil.Unix(), Handle(c.StandByBy)
		}
		feed.Standing = append(feed.Standing, it)
	}
	if since.IsZero() {
		since = now.Add(-time.Hour)
	} else {
		since = since.Add(-feedSlack)
	}
	for _, a := range hist {
		if !a.At.After(since) || a.At.After(now) {
			continue
		}
		when := a.RuleID
		if a.Test {
			when = "TEST · " + a.RuleID
		} else if a.Unreported {
			when = "no longer reports " + unreportedReading(a.Metric)
		} else if a.Op != "" {
			when = fmt.Sprintf("%s %s %s", a.Metric, a.Op, fmtThreshold(a.Threshold))
		}
		feed.Transitions = append(feed.Transitions, TroubleTransition{At: a.At.Unix(), Rule: a.RuleID, NodeID: a.NodeID,
			Node: name(a.NodeID, a.Node), When: when, Firing: a.Firing, Priority: Priority(a.Metric), Test: a.Test,
			Unreported: a.Unreported})
	}
	for id, d := range so.DeadAir {
		feed.SignOff.DeadAir = append(feed.SignOff.DeadAir, TroubleDeadAir{Node: name(id, ""), ForS: int64(d.Seconds())})
	}
	sort.Slice(feed.SignOff.DeadAir, func(i, j int) bool { return feed.SignOff.DeadAir[i].ForS > feed.SignOff.DeadAir[j].ForS })
	return feed
}

// TestCall puts one clearly-marked test transition in the feed for a member, changing no rule
// state: the owner proving the phone hears the Edge.
func (l AlertLog) TestCall(member string, now time.Time) error {
	return l.withLock(func() error {
		a := Alert{At: now, RuleID: "test", Node: member, Metric: "test", Firing: true, Test: true}
		return l.appendHistory([]Alert{a})
	})
}

// Reads is when each member last read the feed (node id -> unix seconds): the sink line's facts.
func (l AlertLog) Reads() (map[string]int64, error) {
	out := map[string]int64{}
	err := l.readJSON("reads.json", &out)
	return out, err
}

// RecordRead notes that a member read the feed now - once a minute per member, so a tight poll
// does not write a file on every request.
func (l AlertLog) RecordRead(nodeID string, now time.Time) error {
	return l.withLock(func() error {
		reads, err := l.Reads()
		if err != nil {
			return err
		}
		if last := reads[nodeID]; last > 0 && now.Unix()-last < 60 {
			return nil
		}
		reads[nodeID] = now.Unix()
		return l.writeJSON("reads.json", reads)
	})
}

// --- the face -----------------------------------------------------------------------------------

// handleTrouble serves GET /edge/trouble to a member.
func (s *Server) handleTrouble(w http.ResponseWriter, r *http.Request) {
	id, log, ok := s.troubleCaller(w, r) // who asks is settled before anything else is said
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		inferError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	now := s.now()
	var since time.Time
	if c := r.URL.Query().Get("since"); c != "" {
		ns, err := strconv.ParseInt(c, 10, 64)
		if err != nil || ns <= 0 {
			inferError(w, http.StatusBadRequest, "since must be a cursor this feed gave you")
			return
		}
		since = time.Unix(0, ns)
	}
	feed, err := log.Feed(since, now, s.name)
	if err != nil {
		inferError(w, http.StatusInternalServerError, "the trouble log could not be read")
		return
	}
	_ = log.RecordRead(id, now)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(feed)
}

// handleTroubleRoger serves POST /edge/trouble/roger: the member acknowledges a standing call.
func (s *Server) handleTroubleRoger(w http.ResponseWriter, r *http.Request) {
	id, log, ok := s.troubleCaller(w, r)
	if !ok || !s.troublePost(w, r) {
		return
	}
	var req struct {
		Rule string `json:"rule"`
		Node string `json:"node"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || req.Rule == "" || req.Node == "" {
		inferError(w, http.StatusBadRequest, "rule and node required")
		return
	}
	if err := log.Roger(req.Rule, req.Node, s.name(id), s.now()); err != nil {
		inferError(w, http.StatusNotFound, "no such standing call")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleTroubleStandBy serves POST /edge/trouble/standby: the member quiets a call for 1..8 hours.
func (s *Server) handleTroubleStandBy(w http.ResponseWriter, r *http.Request) {
	id, log, ok := s.troubleCaller(w, r)
	if !ok || !s.troublePost(w, r) {
		return
	}
	var req struct {
		Rule  string  `json:"rule"`
		Node  string  `json:"node"`
		Hours float64 `json:"hours"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || req.Rule == "" || req.Node == "" {
		inferError(w, http.StatusBadRequest, "rule and node required")
		return
	}
	if req.Hours < 1 { // an hour at least, eight at most
		req.Hours = 1
	}
	if req.Hours > 8 {
		req.Hours = 8
	}
	if err := log.StandByAs(req.Rule, req.Node, s.name(id), s.now().Add(time.Duration(req.Hours*float64(time.Hour)))); err != nil {
		inferError(w, http.StatusNotFound, "no such standing call")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// troubleCaller authenticates the member and finds the log; it writes the refusal itself.
func (s *Server) troubleCaller(w http.ResponseWriter, r *http.Request) (string, *AlertLog, bool) {
	id, status, msg := s.caller(r) // authenticate first: a stranger learns nothing, not even that
	if status != 0 {
		inferError(w, status, msg)
		return "", nil, false
	}
	if s.serving.Trouble == nil {
		inferError(w, http.StatusNotFound, "this node keeps no trouble log")
		return "", nil, false
	}
	return id, s.serving.Trouble, true
}

func (s *Server) troublePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		inferError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}

// name is the member's name by id, or the id when this node knows no name for it.
func (s *Server) name(id string) string {
	if s.serving != nil && s.serving.Names != nil {
		if n := s.serving.Names(id); n != "" {
			return n
		}
	}
	return id
}
