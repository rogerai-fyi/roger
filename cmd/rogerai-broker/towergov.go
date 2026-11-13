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
	"encoding/json"
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

// actorUnauthenticated is a request that named a Tower but was not the Tower's own signed
// request: anyone could have sent it, so the record must not say the Tower tried.
const actorUnauthenticated = "unauthenticated"

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

// towerEventBudget caps how many events of one Tower- or operator-driven kind one Tower may
// add to its record per hour, counted in the shared store so it holds across instances. The
// record is append-only and never reaped, so without a cap an enrolled Tower reconnecting in
// a loop could grow it without bound. Twenty an hour is far above any honest pattern.
const towerEventBudget = 20

// pacedKind reports whether a kind is driven by a Tower or an operator, and so capped.
// What Core or an administrator decides is never capped.
func pacedKind(kind string) bool {
	switch kind {
	case evSessionOpened, evSessionClosed, evSessionRefused, evSessionResumed, evLifecycleRefused,
		evStationAttached, evStationRevoked, evCertRenewed, evCanary:
		return true
	}
	return false
}

// pacedEvent reports whether an event counts against the budget: a paced kind, driven by
// the Tower itself or an operator. The same kind written by Core or an administrator is
// never capped, so an operator cannot crowd those records out.
//
// A canary result is the one exception to the actor rule: Core writes it, but the Tower
// decides it (a Tower can pass and fail probes at will), so it is capped whoever records it.
func pacedEvent(ev admit.Event) bool {
	if ev.Kind == evCanary {
		return true
	}
	return pacedKind(ev.Kind) && (ev.Actor == admit.ActorTower || strings.HasPrefix(ev.Actor, "operator:"))
}

// overBudget reports whether ev's actor has used its hourly budget for ev's kind on ev's
// Tower, and logs that once a minute when it has. Counted per actor in the shared store, so
// one account cannot use up another's room. The count and the insert are not one
// transaction: two instances racing can overshoot by their concurrency, which is bounded.
func (b *broker) overBudget(ev admit.Event) bool {
	actor := ev.Actor
	if ev.Kind == evCanary {
		actor = "" // see pacedEvent: capped per Tower, whoever records it
	}
	n, err := b.tower.registry.CountEvents(ev.TowerID, ev.Kind, "", actor, time.Now().Add(-time.Hour))
	if err != nil {
		b.towerGovUnavailable(err)
		return true
	}
	if n < towerEventBudget {
		return false
	}
	if b.towerLogPace.allow("budget|" + ev.TowerID + "|" + ev.Kind + "|" + actor) {
		log.Printf("tower-event: %s budget reached for tower %s by %s (%d an hour); further events are logged, not recorded",
			ev.Kind, logValue(ev.TowerID), logValue(ev.Actor), towerEventBudget)
	}
	return true
}

// towerEvent prints the line and records the event, best effort. A paced event over its
// hourly budget is logged and not recorded, and the log says so once a minute.
func (b *broker) towerEvent(ev admit.Event) {
	if ev.Instance == "" {
		ev.Instance = b.instanceID
	}
	ev.Reason = admit.CleanReason(ev.Reason)
	log.Print(formatTowerEvent(ev))
	if b.tower == nil {
		return
	}
	if pacedEvent(ev) && b.overBudget(ev) {
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

// logPacer lets one log line per key through each minute. It only ever paces logging:
// nothing it holds becomes a record, which is why it is separate from refusalLimiter.
type logPacer struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (p *logPacer) allow(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = map[string]time.Time{}
	}
	now := time.Now()
	if last, ok := p.last[key]; ok && now.Sub(last) < time.Minute {
		return false
	}
	p.last[key] = now
	return true
}

// prune forgets keys quiet for an hour, so callers cannot grow the map without bound.
func (p *logPacer) prune() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, at := range p.last {
		if time.Since(at) > time.Hour {
			delete(p.last, k)
		}
	}
}

// admittedSet is the ids of admitted Towers, refreshed by the sweep, so a refusal can be
// attributed to a real Tower without a store read per request and without keeping state
// for ids an attacker invents.
type admittedSet struct {
	mu  sync.Mutex
	ids map[string]bool
	at  time.Time
}

func (a *admittedSet) refresh(ids []string) {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	a.mu.Lock()
	a.ids, a.at = m, time.Now()
	a.mu.Unlock()
}

func (a *admittedSet) has(id string) (known, fresh bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ids[id], !a.at.IsZero() && time.Since(a.at) < 2*towerGovSweepInterval
}

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

// clock is now() for callers outside the lock.
func (l *refusalLimiter) clock() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.now()
}

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

// prune forgets keys with nothing pending and nothing logged or recorded for an hour.
func (l *refusalLimiter) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k := range l.logged {
		if l.counts[k] == 0 && now.Sub(l.logged[k]) > time.Hour && now.Sub(l.recorded[k]) > time.Hour {
			delete(l.logged, k)
			delete(l.recorded, k)
			delete(l.counts, k)
		}
	}
}

// carry returns a count to the pending pile when its record was not written.
func (l *refusalLimiter) carry(key string, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[key] += n
}

// due hands back every key with refusals pending whose last record is an hour old or more,
// and resets them: what the sweep writes so a burst that stopped is not lost.
func (l *refusalLimiter) due() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	out := map[string]int{}
	for key, n := range l.counts {
		if n == 0 {
			continue
		}
		if now.Sub(l.recorded[key]) >= time.Hour {
			out[key] = n
			l.counts[key], l.recorded[key] = 0, now
		}
	}
	return out
}

// flushRefusals writes the refusal counts that are pending, one event per Tower and reason.
func (b *broker) flushRefusals() {
	for key, n := range b.towerRefusals.due() {
		towerID, reason, ok := strings.Cut(key, "|")
		if !ok {
			continue
		}
		if b.refusalRecordedElsewhere(towerID, reason, b.towerRefusals.clock()) {
			b.towerRefusals.carry(key, n) // another instance wrote this hour's; try again next hour
			continue
		}
		b.towerEvent(admit.Event{TowerID: towerID, Kind: evRequestRefused, Actor: refusalActor(reason), Reason: reason,
			Detail: map[string]any{"count": n, "flushed": true}})
	}
}

// refusalActor names who a refusal is attributed to. Only a request the Tower itself signed
// (its admitted key, so the refusal is about its standing) is the Tower; an unsigned or
// wrongly signed request could have come from anyone.
func refusalActor(reason string) string {
	switch reason {
	case "unsigned", "key_mismatch", "no_tower_id", "unavailable":
		return actorUnauthenticated
	}
	return admit.ActorTower
}

// noteTowerRefusal logs and records a refused Tower request. The Tower's answer is the
// caller's and is unchanged: no reason is ever disclosed to whoever is asking.
func (b *broker) noteTowerRefusal(towerID, reason, route string) {
	if towerID == "" || b.tower == nil {
		return
	}
	switch reason {
	case "unsigned", "no_tower_id", "unavailable":
		// Anyone can send an unsigned request naming any id. It is attributed to a Tower only
		// when the id is one the sweep knows is admitted - no store read per request, and no
		// state kept for invented ids - and is otherwise part of the unknown stream below.
		known, fresh := b.towerAdmitted.has(towerID)
		if !fresh && b.towerLogPace.allow("admitted-refresh") {
			b.refreshAdmitted()
			known, _ = b.towerAdmitted.has(towerID)
		}
		if !known {
			reason = "unknown_tower"
		}
	}
	if reason == "unknown_tower" {
		// Anyone can name any id, so these are paced as one stream, not per id, and never
		// recorded: a record per invented id would be a table anyone can fill.
		if b.towerLogPace.allow("unknown_tower") {
			b.logTowerEvent(admit.Event{TowerID: towerID, Kind: evRequestRefused, Actor: actorUnauthenticated,
				Reason: "unknown_tower", Detail: map[string]any{"route": route}})
		}
		return
	}
	// From here the Tower is admitted: either towerCallerWhy found it in the registry, or the
	// admitted set did. Its refusals are counted for it.
	logIt, record, count, now := b.towerRefusals.note(towerID + "|" + reason)
	ev := admit.Event{TowerID: towerID, Kind: evRequestRefused, Actor: refusalActor(reason), Reason: reason,
		Detail: map[string]any{"route": route}}
	if record {
		if b.refusalRecordedElsewhere(towerID, reason, now) {
			b.towerRefusals.carry(towerID+"|"+reason, count) // not lost: the sweep writes it later
		} else {
			ev.At = now
			ev.Detail["count"] = count
			b.towerEvent(ev)
			return
		}
	}
	if logIt {
		b.logTowerEvent(ev)
	}
}

// refusalRecordedElsewhere reports whether this reason is already on the Tower's record
// within the hour, whoever wrote it, so two instances or a restart do not write a second.
// A store that cannot answer is treated as "recorded": the line is still logged, and the
// pending count waits for the sweep.
func (b *broker) refusalRecordedElsewhere(towerID, reason string, now time.Time) bool {
	n, err := b.tower.registry.CountEvents(towerID, evRequestRefused, reason, "", now.Add(-time.Hour))
	return err != nil || n > 0
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
func (b *broker) recordSessionOpened(towerID, sessionID string, version int, fullInventory bool, replaced string) {
	ts := b.tower
	detail := map[string]any{"session": sessionPrefix(sessionID), "version": version, "full_inventory": fullInventory}
	if replaced != "" && replaced != sessionID {
		detail["replaced"] = true
		detail["replaced_session"] = sessionPrefix(replaced)
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
	towers := ts.registry.All()
	ids := make([]string, 0, len(towers))
	for _, tw := range towers {
		ids = append(ids, tw.ID)
		b.sweepLink(tw.ID, now)
		b.sweepLease(tw, now)
	}
	b.towerAdmitted.refresh(ids)
	b.flushRefusals()
	b.towerRefusals.prune()
	b.towerLogPace.prune()
}

// refreshAdmitted reloads the admitted set outside the sweep, at most once a minute.
func (b *broker) refreshAdmitted() {
	towers := b.tower.registry.All()
	ids := make([]string, 0, len(towers))
	for _, tw := range towers {
		ids = append(ids, tw.ID)
	}
	b.towerAdmitted.refresh(ids)
}

func (b *broker) sweepLink(towerID string, now time.Time) {
	last, ok, err := b.tower.registry.FindEvent(towerID, towerLinkKinds, false)
	if err != nil {
		b.towerGovUnavailable(err)
		return
	}
	if !ok || last.Kind == evSessionStale {
		return // never connected, or already recorded as quiet
	}
	// A newest "closed" is not the last word when the link holds an open session: the open
	// that followed may have been logged but not recorded (over the budget). The link, which
	// the Current check below consults, decides.
	// UNKNOWN IS NOT GONE. The last-seen time comes from this instance's session or the
	// shared view, whichever is newer; an instance that cannot read the shared view skips the
	// Tower this round rather than write a permanent "went quiet" for one heartbeating to a
	// peer.
	seen, known, err := b.tower.link.LastSeenChecked(towerID)
	if err != nil {
		b.towerGovUnavailable(err)
		return
	}
	// A session that is no longer there was closed, whether or not its close made the
	// record (a close over the budget is logged, not recorded). Closed is not quiet.
	if _, open := b.tower.link.Current(towerID); !open {
		return
	}
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
	written, err := ts.registry.TransitionRecorded(before.ID, to, admit.Event{Kind: evLifecycle, Actor: actor,
		Reason: reason, Instance: b.instanceID})
	switch {
	case err == nil:
		b.logTowerEvent(written) // the line says what the record says
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

// recordEnrolled writes a Tower's enrollment once. Enroll is idempotent per transaction, so
// a replay succeeds again; it must not append a second "enrolled" (with the state the Tower
// is in now). One per Tower, across instances.
func (b *broker) recordEnrolled(towerID, owner string, state admit.State, serial, lifecycleHash string) {
	b.towerRecordIfLast(admit.Event{TowerID: towerID, Kind: evEnrolled, Actor: "operator:" + owner, To: state,
		Detail: map[string]any{"serial": serial, "lifecycle_hash": lifecycleHash}}, []string{evEnrolled}, 0)
}

// sweptIDs lists a sweep's Station ids for its log line.
func sweptIDs(s []attach.Swept) string {
	ids := make([]string, len(s))
	for i, x := range s {
		ids[i] = x.StationID
	}
	return strings.Join(ids, ",")
}

// maxNamedStations caps how many Station ids one station_retired event names.
const maxNamedStations = 20

// recordStationsSwept writes ONE station_retired event per Tower per sweep, with the count
// and the first Station ids (all of them are in the log line). One per Station would let an
// account's attach/revoke churn become an uncapped stream of system events at reap time. A
// direct-origin Station has no Tower record to land on.
func (b *broker) recordStationsSwept(s []attach.Swept, why string) {
	byTower := map[string][]string{}
	var order []string
	for _, x := range s {
		if x.TowerID == "" {
			continue
		}
		if _, seen := byTower[x.TowerID]; !seen {
			order = append(order, x.TowerID)
		}
		byTower[x.TowerID] = append(byTower[x.TowerID], x.StationID)
	}
	for _, towerID := range order {
		ids := byTower[towerID]
		named := ids
		if len(named) > maxNamedStations {
			named = named[:maxNamedStations]
		}
		b.towerEvent(admit.Event{TowerID: towerID, Kind: evStationRetired, Actor: admit.ActorSystem,
			Reason: why, Detail: map[string]any{"count": len(ids), "stations": named}})
	}
}

// noteCanary logs every failed probe with its Station and records a probe result only when
// it differs from the last one recorded, so the record shows changes rather than a heartbeat.
func (b *broker) noteCanary(towerID, stationID string, outcome reputation.Outcome) {
	b.noteCanaryCause(towerID, stationID, outcome, "")
}

// noteCanaryCause is noteCanary with the drive's failure class, when it named one.
func (b *broker) noteCanaryCause(towerID, stationID string, outcome reputation.Outcome, cause string) {
	result := string(outcome)
	switch outcome {
	case reputation.CanaryPass:
		result = "pass"
	case reputation.CanaryFail:
		result = "fail"
		line := fmt.Sprintf("canary: tower %s failed station=%s outcome=%s", towerID, stationID, outcome)
		if cause != "" {
			line += " cause=" + logValue(cause)
		}
		log.Print(line)
	case "":
		return
	}
	b.recordCanaryChange(towerID, stationID, result, string(outcome), cause)
}

// noteCanarySkip records, once until the result changes, that a Tower's endpoint was skipped
// as unreachable by design.
func (b *broker) noteCanarySkip(towerID, stationID string) {
	b.recordCanaryChange(towerID, stationID, "unreachable_by_design", "", "")
}

func (b *broker) recordCanaryChange(towerID, stationID, result, reason, cause string) {
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
	detail := map[string]any{"result": result, "station": stationID}
	if cause != "" {
		detail["cause"] = admit.CleanReason(cause)
	}
	ev := admit.Event{TowerID: towerID, Kind: evCanary, Actor: admit.ActorSystem, Reason: reason, Detail: detail}
	if b.overBudget(ev) {
		return // a Tower flipping its own probe results cannot grow the record without bound
	}
	b.towerRecordIfLast(ev, []string{evCanary}, lastID)
}

// statusCapture remembers the status and the start of the body a handler wrote, so a
// refusal can be logged with the handler's own reason.
type statusCapture struct {
	http.ResponseWriter
	code int
	head []byte
}

func (c *statusCapture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *statusCapture) Write(p []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	if room := 512 - len(c.head); room > 0 {
		c.head = append(c.head, p[:min(room, len(p))]...)
	}
	return c.ResponseWriter.Write(p)
}

// logAttachRefused logs a refused self-attach with the message the handler answered. No
// Tower is named: a refused attach was never placed on one.
func (b *broker) logAttachRefused(c *statusCapture, who string) {
	if c.code < 400 {
		return
	}
	// Paced per caller and status: an unsigned loop is one line a minute, not one per request.
	if !b.towerLogPace.allow("attach|" + who + "|" + strconv.Itoa(c.code)) {
		return
	}
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	reason := http.StatusText(c.code)
	if json.Unmarshal(c.head, &env) == nil && env.Error.Message != "" {
		reason = env.Error.Message
	}
	b.logTowerEvent(admit.Event{TowerID: "-", Kind: "station_attach_refused", Actor: who, Reason: reason,
		Detail: map[string]any{"status": c.code}})
}

// towerGovSweepInterval paces the governance sweep: going quiet, lapsed leases, and pending
// refusal counts. Short against the freshness window, so a quiet Tower is recorded promptly.
const towerGovSweepInterval = time.Minute

// towerGovSweep runs the governance sweep on its own ticker. It depends on the Tower
// subsystem only - not on any other store - so no deployment shape can switch it off.
func (b *broker) towerGovSweep(stop <-chan struct{}, every time.Duration) {
	if b.tower == nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			b.towerGovSweepOnce(now)
		}
	}
}
