package main

// towergov.go writes the Tower governance record: what Core writes down about each Tower,
// who did it, and the one log line a reader greps for. Contract:
// features/tower/governance_record.feature (approved 2026-11-13).
//
// Why it exists: the first outside Tower was approved, ran for a day, and stopped. Core
// could not say whether it had ever connected, because opening, heartbeating and closing a
// session left no trace, a refused Tower request was a silent 403, and an administrator's
// approval was logged as "by an administrator". The cause (renewals signed with the wrong
// key) was a refusal nobody could see.
//
// Two kinds of write. Lifecycle changes go through admit.Registry.TransitionAs, so the change
// and its record commit together. Everything else here is telemetry: best effort, never
// blocking a Tower, and a store failure is logged once a minute.

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
)

// Event kinds. The spec names them; the dashboard and greps depend on the exact strings.
const (
	evSessionOpened    = "session_opened"
	evSessionResumed   = "session_resumed"
	evSessionClosed    = "session_closed"
	evSessionStale     = "session_stale"
	evSessionRefused   = "session_refused"
	evRequestRefused   = "request_refused"
	evLifecycle        = "lifecycle"
	evLifecycleRefused = "lifecycle_refused"
	evLeaseLapsed      = "lease_lapsed"
	evStationRetired   = "station_retired"
	evStationAttached  = "station_attached"
	evStationRevoked   = "station_revoked"
	evStationPromoted  = "station_promoted"
	evCertRevoked      = "cert_revoked"
	evCertRenewed      = "cert_renewed"
	evLeaseExpired     = "lease_expired"
	evEnrolled         = "enrolled"
	evEnrollRefused    = "enroll_refused"
	evCanary           = "canary"
)

// towerLinkKinds are the events that say where a Tower's link stands.
var towerLinkKinds = []string{evSessionOpened, evSessionResumed, evSessionClosed, evSessionStale}

// formatTowerEvent renders the one log line for an event. Values that could be mistaken for
// another field or another line are quoted.
func formatTowerEvent(ev admit.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tower-event kind=%s tower=%s actor=%s", logValue(ev.Kind), logValue(ev.TowerID), logValue(ev.Actor))
	if ev.From != "" {
		b.WriteString(" from=" + logValue(string(ev.From)))
	}
	if ev.To != "" {
		b.WriteString(" to=" + logValue(string(ev.To)))
	}
	if ev.Reason != "" {
		b.WriteString(" reason=" + logValue(ev.Reason))
	}
	fields := map[string]string{}
	for k, v := range ev.Detail {
		fields[k] = fmt.Sprint(v)
	}
	if ev.Instance != "" {
		fields["instance"] = ev.Instance
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(" " + logValue(k) + "=" + logValue(fields[k]))
	}
	return b.String()
}

// logValue quotes a value that is empty or holds a space, '=', a quote, a backslash or any
// non-printing character; anything else prints bare.
func logValue(s string) string {
	if s == "" {
		return `""`
	}
	for _, r := range s {
		if r == ' ' || r == '=' || r == '"' || r == '\\' || !unicode.IsPrint(r) {
			return strconv.Quote(s)
		}
	}
	return s
}

// logTowerEvent prints the line without recording it: for refusals that name no Tower Core
// admitted, and for requests too malformed to attribute to one.
func (b *broker) logTowerEvent(ev admit.Event) {
	if ev.Instance == "" {
		ev.Instance = b.instanceID
	}
	ev.Reason = admit.CleanReason(ev.Reason)
	log.Print(formatTowerEvent(ev))
}

// towerEvent prints the line and records the event, best effort.
func (b *broker) towerEvent(ev admit.Event) {
	if ev.Instance == "" {
		ev.Instance = b.instanceID
	}
	ev.Reason = admit.CleanReason(ev.Reason)
	log.Print(formatTowerEvent(ev))
	if b.tower == nil {
		return
	}
	if err := b.tower.registry.Record(ev); err != nil {
		b.towerGovUnavailable(err)
	}
}

// towerRecordIfLast is towerEvent for the events that must be written once across
// instances. It prints the line only when this instance won the write.
func (b *broker) towerRecordIfLast(ev admit.Event, kinds []string, lastID int64) bool {
	if ev.Instance == "" {
		ev.Instance = b.instanceID
	}
	won, err := b.tower.registry.RecordIfLast(ev, kinds, lastID)
	if err != nil {
		b.towerGovUnavailable(err)
		return false
	}
	if won {
		log.Print(formatTowerEvent(ev))
	}
	return won
}

// pacer lets something happen at most once per interval.
type pacer struct {
	mu   sync.Mutex
	last time.Time
}

func (p *pacer) allow(every time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.last.IsZero() && time.Since(p.last) < every {
		return false
	}
	p.last = time.Now()
	return true
}

// towerGovUnavailable logs a store failure at most once a minute: a Tower heartbeating
// every few seconds against a down store must not bury the log.
func (b *broker) towerGovUnavailable(err error) {
	if b.towerGovFail.allow(time.Minute) {
		log.Printf("tower-event: governance record unavailable: %v", err)
	}
}

// --- refused Tower requests -------------------------------------------------------------

// refusalLimiter paces refusal logging (one line per key per minute) and recording (one
// event per key per hour, carrying the count it stands for).
type refusalLimiter struct {
	mu       sync.Mutex
	offset   time.Duration // test seam: shift moves the clock
	logged   map[string]time.Time
	recorded map[string]time.Time
	counts   map[string]int
}

func (l *refusalLimiter) now() time.Time { return time.Now().Add(l.offset) }

func (l *refusalLimiter) shift(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.offset += d
}

// note counts one refusal and says whether to log it, whether to record it, and how many
// refusals a record stands for.
func (l *refusalLimiter) note(key string) (logIt, record bool, count int, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logged == nil {
		l.logged, l.recorded, l.counts = map[string]time.Time{}, map[string]time.Time{}, map[string]int{}
	}
	now = l.now()
	l.counts[key]++
	if last, ok := l.logged[key]; !ok || now.Sub(last) >= time.Minute {
		l.logged[key], logIt = now, true
	}
	if last, ok := l.recorded[key]; !ok || now.Sub(last) >= time.Hour {
		l.recorded[key], record = now, true
		count, l.counts[key] = l.counts[key], 0
	}
	return logIt, record, count, now
}

// noteTowerRefusal logs and records a refused Tower request. The Tower's answer is the
// caller's and is unchanged: no reason is ever disclosed to whoever is asking.
func (b *broker) noteTowerRefusal(towerID, reason, route string) {
	if towerID == "" || b.tower == nil {
		return
	}
	if _, admitted := b.tower.registry.Get(towerID); !admitted {
		// Anyone can name any id, so these are paced as one stream, not per id, and never
		// recorded: a record per invented id would be a table anyone can fill.
		if logIt, _, _, _ := b.towerRefusals.note("unknown_tower"); logIt {
			b.logTowerEvent(admit.Event{TowerID: towerID, Kind: evRequestRefused, Actor: admit.ActorTower,
				Reason: "unknown_tower", Detail: map[string]any{"route": route}})
		}
		return
	}
	logIt, record, count, now := b.towerRefusals.note(towerID + "|" + reason)
	ev := admit.Event{TowerID: towerID, Kind: evRequestRefused, Actor: admit.ActorTower, Reason: reason,
		Detail: map[string]any{"route": route}}
	if record && !b.refusalRecordedElsewhere(towerID, reason, now) {
		ev.At = now
		ev.Detail["count"] = count
		b.towerEvent(ev)
		return
	}
	if logIt {
		b.logTowerEvent(ev)
	}
}

// refusalRecordedElsewhere reports whether another instance already recorded this reason
// for this Tower within the hour, so two instances do not both write one.
func (b *broker) refusalRecordedElsewhere(towerID, reason string, now time.Time) bool {
	last, ok, err := b.tower.registry.FindEvent(towerID, []string{evRequestRefused}, false)
	return err == nil && ok && last.Reason == reason && last.Instance != b.instanceID &&
		now.Sub(last.At) < time.Hour
}

// --- the link ----------------------------------------------------------------------------

// linkStateCache remembers, per Tower, the newest link event this instance knows of, so a
// heartbeat does not read the database. Entries older than a minute are re-read, which is
// how a "went quiet" event written by another instance is noticed.
type linkStateCache struct {
	mu sync.Mutex
	m  map[string]linkState
}

type linkState struct {
	kind    string
	checked time.Time
}

func (c *linkStateCache) get(towerID string) (linkState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.m[towerID]
	return s, ok && time.Since(s.checked) < time.Minute
}

func (c *linkStateCache) set(towerID, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]linkState{}
	}
	c.m[towerID] = linkState{kind: kind, checked: time.Now()}
}

func sessionPrefix(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// recordSessionOpened records a successful session open.
func (b *broker) recordSessionOpened(towerID, sessionID string, version int, fullInventory, replaced bool) {
	ts := b.tower
	detail := map[string]any{"session": sessionPrefix(sessionID), "version": version, "full_inventory": fullInventory}
	if replaced {
		detail["replaced"] = true
	}
	if _, ever, err := ts.registry.FindEvent(towerID, []string{evSessionOpened}, true); err == nil && !ever {
		detail["first"] = true
	}
	if last, ok, err := ts.registry.FindEvent(towerID, towerLinkKinds, false); err == nil && ok &&
		(last.Kind == evSessionStale || last.Kind == evSessionClosed) {
		detail["back_after_seconds"] = int(time.Since(last.At).Seconds())
	}
	b.towerEvent(admit.Event{TowerID: towerID, Kind: evSessionOpened, Actor: admit.ActorTower, Detail: detail})
	b.towerLinkState.set(towerID, evSessionOpened)
}

// noteHeartbeat records a Tower coming back after it was recorded as gone quiet. Every
// other heartbeat writes nothing.
func (b *broker) noteHeartbeat(towerID string) {
	if s, fresh := b.towerLinkState.get(towerID); fresh && s.kind != evSessionStale {
		return
	}
	last, ok, err := b.tower.registry.FindEvent(towerID, towerLinkKinds, false)
	if err != nil {
		b.towerGovUnavailable(err)
		return
	}
	if !ok || last.Kind != evSessionStale {
		kind := ""
		if ok {
			kind = last.Kind
		}
		b.towerLinkState.set(towerID, kind)
		return
	}
	gone := time.Since(last.At)
	if seen, ok := last.Detail["last_seen"].(string); ok {
		if at, perr := time.Parse(time.RFC3339, seen); perr == nil {
			gone = time.Since(at)
		}
	}
	b.towerRecordIfLast(admit.Event{TowerID: towerID, Kind: evSessionResumed, Actor: admit.ActorTower,
		Detail: map[string]any{"gone_seconds": int(gone.Seconds())}}, towerLinkKinds, last.ID)
	b.towerLinkState.set(towerID, evSessionResumed)
}

// recordSessionClosed records an orderly close, with how long the session lasted.
func (b *broker) recordSessionClosed(towerID, sessionID string, lastSeen time.Time) {
	detail := map[string]any{"session": sessionPrefix(sessionID)}
	if opened, ok, err := b.tower.registry.FindEvent(towerID, []string{evSessionOpened}, false); err == nil && ok {
		detail["duration_seconds"] = int(time.Since(opened.At).Seconds())
	}
	if !lastSeen.IsZero() {
		detail["last_seen"] = lastSeen.UTC().Format(time.RFC3339)
	}
	b.towerEvent(admit.Event{TowerID: towerID, Kind: evSessionClosed, Actor: admit.ActorTower, Detail: detail})
	b.towerLinkState.set(towerID, evSessionClosed)
}

// towerGovSweepOnce is the governance half of the housekeeping sweep: it records Towers that
// went quiet and leases that lapsed, once each across all instances.
func (b *broker) towerGovSweepOnce(now time.Time) {
	ts := b.tower
	if ts == nil {
		return
	}
	for _, tw := range ts.registry.All() {
		b.sweepLink(tw.ID, now)
		b.sweepLease(tw, now)
	}
}

func (b *broker) sweepLink(towerID string, now time.Time) {
	last, ok, err := b.tower.registry.FindEvent(towerID, towerLinkKinds, false)
	if err != nil {
		b.towerGovUnavailable(err)
		return
	}
	if !ok || (last.Kind != evSessionOpened && last.Kind != evSessionResumed) {
		return // never connected, closed in order, or already recorded as quiet
	}
	seen, known := b.tower.link.LastSeen(towerID)
	if !known || seen.Before(last.At) {
		seen = last.At
	}
	if now.Sub(seen) <= towerFreshnessWindow {
		return
	}
	if b.towerRecordIfLast(admit.Event{TowerID: towerID, Kind: evSessionStale, Actor: admit.ActorSystem,
		Detail: map[string]any{"last_seen": seen.UTC().Format(time.RFC3339)}}, towerLinkKinds, last.ID) {
		b.towerLinkState.set(towerID, evSessionStale)
	}
}

func (b *broker) sweepLease(tw admitTower, now time.Time) {
	if !now.After(tw.LeaseExpires) || tw.State == admit.StateRevoked || tw.State == admit.StateExpired {
		return
	}
	last, ok, err := b.tower.registry.FindEvent(tw.ID, []string{evLeaseLapsed}, false)
	if err != nil {
		b.towerGovUnavailable(err)
		return
	}
	// Keyed on the lease's own end: one lapse per lease, however many sweeps see it.
	ended := tw.LeaseExpires.UTC().Format(time.RFC3339Nano)
	var lastID int64
	if ok {
		if last.Detail["lease_expired"] == ended {
			return // this lease's lapse is already on the record
		}
		lastID = last.ID
	}
	b.towerRecordIfLast(admit.Event{TowerID: tw.ID, Kind: evLeaseLapsed, Actor: admit.ActorSystem,
		Detail: map[string]any{"lease_expired": ended, "state": string(tw.State)}},
		[]string{evLeaseLapsed}, lastID)
}

// transitionRecorded moves a Tower and records the move in one write, answering the request
// itself when it cannot. A refused move is recorded too, with its reason class; a move whose
// record cannot be written does not happen.
func (b *broker) transitionRecorded(w http.ResponseWriter, ts *towerSubsystem, before admitTower,
	to admit.State, actor, reason string) bool {
	err := ts.registry.TransitionAs(before.ID, to, admit.Event{Kind: evLifecycle, Actor: actor,
		Reason: reason, Instance: b.instanceID})
	switch {
	case err == nil:
		b.logTowerEvent(admit.Event{TowerID: before.ID, Kind: evLifecycle, Actor: actor,
			From: before.State, To: to, Reason: reason})
		return true
	case errors.Is(err, admit.ErrAlreadyInState), errors.Is(err, admit.ErrIllegalTransition):
		class := "illegal_transition"
		if errors.Is(err, admit.ErrAlreadyInState) {
			class = "already_in_state"
		}
		b.towerEvent(admit.Event{TowerID: before.ID, Kind: evLifecycleRefused, Actor: actor,
			From: before.State, To: to, Reason: class})
		jsonErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, admit.ErrUnavailable):
		b.logTowerEvent(admit.Event{TowerID: before.ID, Kind: evLifecycleRefused, Actor: actor,
			From: before.State, To: to, Reason: "record_unavailable"})
		jsonErr(w, http.StatusServiceUnavailable,
			"the change was not made: Core could not record it - retry shortly")
	default:
		jsonErr(w, http.StatusConflict, err.Error())
	}
	return false
}

// sweptIDs lists a sweep's Station ids for its log line.
func sweptIDs(s []attach.Swept) string {
	ids := make([]string, len(s))
	for i, x := range s {
		ids[i] = x.StationID
	}
	return strings.Join(ids, ",")
}

// recordStationsSwept writes one station_retired event per Station a sweep ended, on the
// Tower it was behind. A direct-origin Station has no Tower record to land on.
func (b *broker) recordStationsSwept(s []attach.Swept, why string) {
	for _, x := range s {
		if x.TowerID == "" {
			continue
		}
		b.towerEvent(admit.Event{TowerID: x.TowerID, Kind: evStationRetired, Actor: admit.ActorSystem,
			Reason: why, Detail: map[string]any{"station": x.StationID}})
	}
}

// noteCanary logs every failed probe with its Station and records a probe result only when
// it differs from the last one recorded, so the record shows changes rather than a heartbeat.
func (b *broker) noteCanary(towerID, stationID string, outcome reputation.Outcome) {
	result := string(outcome)
	switch outcome {
	case reputation.CanaryPass:
		result = "pass"
	case reputation.CanaryFail:
		result = "fail"
		log.Printf("canary: tower %s failed station=%s outcome=%s", towerID, stationID, outcome)
	case "":
		return
	}
	b.recordCanaryChange(towerID, stationID, result, string(outcome))
}

// noteCanarySkip records, once until the result changes, that a Tower's endpoint was skipped
// as unreachable by design.
func (b *broker) noteCanarySkip(towerID, stationID string) {
	b.recordCanaryChange(towerID, stationID, "unreachable_by_design", "")
}

func (b *broker) recordCanaryChange(towerID, stationID, result, reason string) {
	if b.tower == nil {
		return
	}
	last, ok, err := b.tower.registry.FindEvent(towerID, []string{evCanary}, false)
	if err != nil {
		b.towerGovUnavailable(err)
		return
	}
	if ok && last.Detail["result"] == result {
		return
	}
	var lastID int64
	if ok {
		lastID = last.ID
	}
	b.towerRecordIfLast(admit.Event{TowerID: towerID, Kind: evCanary, Actor: admit.ActorSystem, Reason: reason,
		Detail: map[string]any{"result": result, "station": stationID}}, []string{evCanary}, lastID)
}
