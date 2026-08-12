package towerjoin

// collect_test.go covers the receipt courier: Station outbox to Roger Core, confirmed only
// after Core answers.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/station"
)

// courierWorld stands up a real Station outbox behind real HTTP, and a Core whose settle
// route behaves as directed per attempt.
type courierWorld struct {
	outbox *station.Outbox
	// answer maps attempt id to the HTTP status Core returns; absent means 200.
	mu      sync.Mutex
	answer  map[string]int
	settled []string
}

func newCourierWorld(t *testing.T) (*courierWorld, string) {
	t.Helper()
	w := &courierWorld{outbox: station.NewOutbox(10), answer: map[string]int{}}

	stationMux := http.NewServeMux()
	stationMux.HandleFunc("/receipts/collect", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]any{"receipts": w.outbox.Collect(64)})
	})
	stationMux.HandleFunc("/receipts/settled", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			AttemptIDs []string `json:"attempt_ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.outbox.Settled(req.AttemptIDs)
		writeJSON(rw, map[string]any{})
	})
	stationSrv := httptest.NewServer(stationMux)
	t.Cleanup(stationSrv.Close)
	return w, stationSrv.URL
}

// courierCore mounts the settle route onto the enroll stub's mux, so the Tower's signed
// forwarding lands on something that can record it.
func courierCore(t *testing.T, w *courierWorld) {
	t.Helper()
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/edge/settle", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			AttemptID string `json:"attempt_id"`
			Receipt   string `json:"receipt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if raw, err := base64.StdEncoding.DecodeString(req.Receipt); err != nil || len(raw) == 0 {
			http.Error(rw, `{"error":{"message":"unreadable receipt"}}`, http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		status := w.answer[req.AttemptID]
		if status == 0 {
			status = http.StatusOK
			w.settled = append(w.settled, req.AttemptID)
		}
		w.mu.Unlock()
		if status != http.StatusOK {
			http.Error(rw, `{"error":{"message":"as directed"}}`, status)
			return
		}
		writeJSON(rw, map[string]any{"attempt_id": req.AttemptID, "corroborated": false})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)
}

// THE ROUND TRIP: evidence in the outbox reaches Core and is confirmed off the outbox, and
// what Core received is byte-for-byte what the Station signed - a courier that re-encoded
// receipts would be a courier whose deliveries do not verify.
func TestTheCourierMovesEvidenceAndConfirmsIt(t *testing.T) {
	w, stationURL := newCourierWorld(t)
	courierCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))

	w.outbox.Add(station.Evidence{AttemptID: "att-1", StationID: "st-1", Receipt: []byte("sig-1")})
	w.outbox.Add(station.Evidence{AttemptID: "att-2", StationID: "st-1", Receipt: []byte("sig-2")})

	var out syncWriter
	moved, err := CollectReceipts(st, map[string]string{"st-1": stationURL}, &out)
	require.NoError(t, err)
	require.Equal(t, 2, moved)
	require.ElementsMatch(t, []string{"att-1", "att-2"}, w.settled)
	require.Empty(t, w.outbox.Collect(10), "confirmed evidence leaves the outbox")

	// A second round is quiet: nothing pending, nothing forwarded.
	moved, err = CollectReceipts(st, map[string]string{"st-1": stationURL}, &out)
	require.NoError(t, err)
	require.Zero(t, moved)
}

// A refusal Core has made TERMINALLY is confirmed off the outbox too: a receipt Core will
// never accept would otherwise wedge the queue behind it forever. An unreachable Core is the
// opposite case - everything stays for the next round.
func TestTerminalRefusalsClearAndOutagesHold(t *testing.T) {
	w, stationURL := newCourierWorld(t)
	courierCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))

	w.outbox.Add(station.Evidence{AttemptID: "att-bad", StationID: "st-1", Receipt: []byte("sig")})
	w.outbox.Add(station.Evidence{AttemptID: "att-down", StationID: "st-1", Receipt: []byte("sig")})
	w.mu.Lock()
	w.answer["att-bad"] = http.StatusForbidden           // Core read it and said no: terminal
	w.answer["att-down"] = http.StatusServiceUnavailable // Core is having a moment: retry
	w.mu.Unlock()

	var out syncWriter
	_, err := CollectReceipts(st, map[string]string{"st-1": stationURL}, &out)
	require.NoError(t, err)

	left := w.outbox.Collect(10)
	require.Len(t, left, 1, "the terminal refusal must clear, the outage must hold")
	require.Equal(t, "att-down", left[0].AttemptID)
	require.Contains(t, out.String(), "att-bad", "a terminal refusal is reported, not silent")
}

// "Already settled" is a SUCCESS for the courier: it is the replay of its own earlier
// delivery, which at-least-once guarantees will happen, and the outbox entry must clear.
func TestAnAlreadySettledAnswerConfirmsTheEntry(t *testing.T) {
	w, stationURL := newCourierWorld(t)
	courierCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))

	w.outbox.Add(station.Evidence{AttemptID: "att-1", StationID: "st-1", Receipt: []byte("sig")})
	w.mu.Lock()
	w.answer["att-1"] = http.StatusConflict
	w.mu.Unlock()

	var out syncWriter
	_, err := CollectReceipts(st, map[string]string{"st-1": stationURL}, &out)
	require.NoError(t, err)
	require.Empty(t, w.outbox.Collect(10))
}

// A Station being down is its operator's ordinary morning: reported, skipped, retried - and
// the other Stations' evidence still moves this round.
func TestADownStationDoesNotStopTheRound(t *testing.T) {
	w, stationURL := newCourierWorld(t)
	courierCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	w.outbox.Add(station.Evidence{AttemptID: "att-1", StationID: "st-1", Receipt: []byte("sig")})

	var out syncWriter
	moved, err := CollectReceipts(st, map[string]string{
		"st-down": "http://127.0.0.1:1", "st-1": stationURL,
	}, &out)
	require.NoError(t, err)
	require.Equal(t, 1, moved)
	require.Contains(t, out.String(), "st-down")
}

func TestAnUnregisteredTowerHasNowhereToDeliver(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	st := joinedTower(t)
	var out syncWriter
	_, err := CollectReceipts(st, map[string]string{"st-1": "http://127.0.0.1:1"}, &out)
	require.ErrorContains(t, err, "not registered")
}

// The loop itself: ticks collect, stop stops.
func TestTheCourierLoopRunsUntilStopped(t *testing.T) {
	w, stationURL := newCourierWorld(t)
	courierCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	w.outbox.Add(station.Evidence{AttemptID: "att-1", StationID: "st-1", Receipt: []byte("sig")})

	tick := make(chan time.Time, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	var out syncWriter
	go func() {
		defer close(done)
		KeepCollecting(st, map[string]string{"st-1": stationURL}, &out, stop,
			func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} })
	}()
	tick <- time.Now()
	require.Eventually(t, func() bool { return len(w.outbox.Collect(1)) == 0 },
		5*time.Second, 10*time.Millisecond)
	close(stop)
	<-done
	require.Contains(t, out.String(), "settled 1 receipt(s)")
}

// A courier with no Stations to visit returns immediately rather than ticking forever over
// nothing.
func TestACourierWithNoStationsStandsDown(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	st := joinedTower(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		KeepCollecting(st, nil, &syncWriter{}, nil,
			func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the courier should have stood down")
	}
}

// A Station that answers collection but refuses confirmation: Core already has the evidence,
// so nothing is lost - the entry is re-presented, the replay loses Core's one-use swap, and
// the noise says which Station to go look at.
func TestARefusedConfirmationIsNoisyRatherThanStuck(t *testing.T) {
	w, _ := newCourierWorld(t)
	courierCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	w.outbox.Add(station.Evidence{AttemptID: "att-1", StationID: "st-1", Receipt: []byte("sig")})

	// A Station whose collect works and whose settled endpoint refuses.
	mux := http.NewServeMux()
	mux.HandleFunc("/receipts/collect", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]any{"receipts": w.outbox.Collect(64)})
	})
	mux.HandleFunc("/receipts/settled", func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, "no", http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var out syncWriter
	moved, err := CollectReceipts(st, map[string]string{"st-1": srv.URL}, &out)
	require.NoError(t, err)
	require.Equal(t, 1, moved, "Core settled it even though the Station could not be told")
	require.Contains(t, out.String(), "could not confirm")
}

// --- audit courier -----------------------------------------------------------

// auditWorld stands up a Station serving transcripts and a Core answering wanted/transcript.
type auditWorld struct {
	mu         sync.Mutex
	wanted     []wantedItem
	received   []map[string]any
	stationURL string
}

func newAuditWorld(t *testing.T) (*auditWorld, string) {
	t.Helper()
	w := &auditWorld{}

	// The Station: a couple of transcripts it will serve.
	stationMux := http.NewServeMux()
	stationMux.HandleFunc("/transcripts/get", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			AttemptID string `json:"attempt_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.AttemptID == "att-have" {
			writeJSON(rw, map[string]any{"available": true, "transcript": "dHI=",
				"request": "cQ==", "response": "YQ=="})
			return
		}
		writeJSON(rw, map[string]any{"available": false})
	})
	stationSrv := httptest.NewServer(stationMux)
	t.Cleanup(stationSrv.Close)
	w.stationURL = stationSrv.URL
	return w, stationSrv.URL
}

func auditCore(t *testing.T, w *auditWorld) {
	t.Helper()
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/audit/wanted", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		writeJSON(rw, map[string]any{"wanted": w.wanted})
	})
	mux.HandleFunc("/tower/audit/transcript", func(rw http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.mu.Lock()
		w.received = append(w.received, got)
		w.mu.Unlock()
		writeJSON(rw, map[string]any{"resolved": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)
}

// The courier fetches wanted transcripts from the right Station and forwards them.
func TestTheAuditCourierFetchesAndForwards(t *testing.T) {
	w, stationURL := newAuditWorld(t)
	auditCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	w.wanted = []wantedItem{{AttemptID: "att-have", StationID: "st-1"}}

	var out syncWriter
	n, err := CollectAudits(st, map[string]string{"st-1": stationURL}, &out)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.received, 1)
	require.Equal(t, "att-have", w.received[0]["attempt_id"])
	require.Equal(t, true, w.received[0]["available"])
}

// A transcript the Station did not keep is forwarded as "not available" - Core needs the
// answer to record a "cannot produce" rather than waiting out the deadline.
func TestTheAuditCourierForwardsUnavailable(t *testing.T) {
	w, stationURL := newAuditWorld(t)
	auditCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	w.wanted = []wantedItem{{AttemptID: "att-missing", StationID: "st-1"}}

	var out syncWriter
	_, err := CollectAudits(st, map[string]string{"st-1": stationURL}, &out)
	require.NoError(t, err)
	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.received, 1)
	require.Equal(t, false, w.received[0]["available"])
}

// Core wanting a transcript from a Station this Tower does not carry is forwarded as
// unavailable rather than silently dropped.
func TestAnAuditForAnUnknownStationIsAnswered(t *testing.T) {
	w, _ := newAuditWorld(t)
	auditCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	w.wanted = []wantedItem{{AttemptID: "att-x", StationID: "st-gone"}}

	var out syncWriter
	_, err := CollectAudits(st, map[string]string{"st-1": "http://127.0.0.1:1"}, &out)
	require.NoError(t, err)
	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.received, 1)
	require.Equal(t, false, w.received[0]["available"])
	require.Contains(t, out.String(), "unknown Station")
}

func TestAnUnregisteredTowerCannotAnswerAudits(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	st := joinedTower(t)
	_, err := CollectAudits(st, map[string]string{"st-1": "http://127.0.0.1:1"}, &syncWriter{})
	require.ErrorContains(t, err, "not registered")
}

func TestTheAuditLoopRunsAndStops(t *testing.T) {
	w, stationURL := newAuditWorld(t)
	auditCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	w.wanted = []wantedItem{{AttemptID: "att-have", StationID: "st-1"}}

	tick := make(chan time.Time, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	var out syncWriter
	go func() {
		defer close(done)
		KeepAuditing(st, map[string]string{"st-1": stationURL}, &out, stop,
			func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} })
	}()
	tick <- time.Now()
	require.Eventually(t, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return len(w.received) > 0
	}, 5*time.Second, 10*time.Millisecond)
	close(stop)
	<-done
}

// A Core that refuses the wanted list surfaces as an error the loop reports.
func TestFetchingWantedCanFail(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/audit/wanted", func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, `{"error":{"message":"no"}}`, http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))

	_, err := CollectAudits(st, map[string]string{"st-1": "http://127.0.0.1:1"}, &syncWriter{})
	require.Error(t, err)
}

// A Station that errors on a transcript fetch is reported and skipped, not fatal.
func TestATranscriptFetchErrorIsSkipped(t *testing.T) {
	w, _ := newAuditWorld(t)
	auditCore(t, w)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	w.wanted = []wantedItem{{AttemptID: "att-have", StationID: "st-1"}}

	// A Station whose /transcripts/get 500s.
	mux := http.NewServeMux()
	mux.HandleFunc("/transcripts/get", func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var out syncWriter
	n, err := CollectAudits(st, map[string]string{"st-1": srv.URL}, &out)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Contains(t, out.String(), "could not fetch transcript")
}

func TestForwardingATranscriptCanFail(t *testing.T) {
	w, stationURL := newAuditWorld(t)
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/audit/wanted", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		writeJSON(rw, map[string]any{"wanted": w.wanted})
	})
	mux.HandleFunc("/tower/audit/transcript", func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, `{"error":{"message":"no"}}`, http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))
	w.wanted = []wantedItem{{AttemptID: "att-have", StationID: "st-1"}}

	var out syncWriter
	n, err := CollectAudits(st, map[string]string{"st-1": stationURL}, &out)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Contains(t, out.String(), "could not forward transcript")
}

// The audit loop stands down immediately with no Stations.
func TestTheAuditLoopStandsDownWithNoStations(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	st := joinedTower(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		KeepAuditing(st, nil, &syncWriter{}, nil,
			func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the audit loop should have stood down")
	}
}

// A failed audit round is reported by the loop rather than killing it.
func TestTheAuditLoopSurvivesAFailedRound(t *testing.T) {
	t.Setenv("ROGER_CONFIG_DIR", t.TempDir())
	_, mux := newStubCoreMux(t)
	mux.HandleFunc("/tower/audit/wanted", func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, "no", http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("ROGER_BROKER", srv.URL)
	st := joinedTower(t)
	require.NoError(t, Register(st, Account{Login: "alice"}))

	tick := make(chan time.Time, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	var out syncWriter
	go func() {
		defer close(done)
		KeepAuditing(st, map[string]string{"st-1": "http://127.0.0.1:1"}, &out, stop,
			func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} })
	}()
	tick <- time.Now()
	require.Eventually(t, func() bool { return len(out.String()) > 0 },
		5*time.Second, 10*time.Millisecond)
	close(stop)
	<-done
	require.Contains(t, out.String(), "audit collection failed")
}
