package edge

// Table-driven units for the session ledger. The BDD suite in internal/tui proves the
// SCREEN; this proves the derivation and the admission rules underneath it - every
// refusal path, every boundary of the fade and the ring, and every way a receipt can fail
// to be this session's.

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

func recAt(request, node, model string, ts int64) protocol.UsageReceipt {
	return protocol.UsageReceipt{RequestID: request, NodeID: node, Model: model, TS: ts}
}

func voidAt(request, node, model, reason string, ts int64) protocol.UsageReceipt {
	r := recAt(request, node, model, ts)
	r.VoidReason = reason
	return r
}

func newLedger(t *testing.T) (*Sessions, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	s := NewSessions("acct-1")
	s.SetClock(func() time.Time { return now })
	return s, &now
}

func TestInitiatorVocabulary(t *testing.T) {
	require.Len(t, Initiators(), 5)
	for _, c := range []struct {
		i           Initiator
		valid       bool
		namesItself bool
		label       string
	}{
		{FromUse, true, false, "roger use"},
		{FromAgent, true, false, "agent"},
		{FromConsole, true, false, "console"},
		{FromGuest, true, true, "guest"},
		{FromDevice, true, true, "device"},
		{Initiator(""), false, false, ""},
		{Initiator("cron"), false, false, "cron"},
	} {
		require.Equal(t, c.valid, c.i.Valid(), c.i)
		require.Equal(t, c.namesItself, c.i.NamesItself(), c.i)
		require.Equal(t, c.label, c.i.Label(), c.i)
	}
}

func TestSessionAttribution(t *testing.T) {
	require.Equal(t, "roger use", Session{Kind: FromUse}.Attribution())
	require.Equal(t, "agent", Session{Kind: FromAgent}.Attribution())
	require.Equal(t, "console", Session{Kind: FromConsole}.Attribution())
	require.Equal(t, "opencode", Session{Kind: FromGuest, Who: "opencode"}.Attribution())
	require.Equal(t, "bench-pi", Session{Kind: FromDevice, Who: "bench-pi"}.Attribution())
}

func TestReceiptBelongs(t *testing.T) {
	for _, c := range []struct {
		name, rec, request string
		want               bool
	}{
		{"the first attempt is the request itself", "req-1", "req-1", true},
		{"a failover attempt wears the suffix", "req-1-2", "req-1", true},
		{"a third attempt too", "req-1-3", "req-1", true},
		{"another request entirely", "req-2", "req-1", false},
		{"a request that merely starts the same", "req-12", "req-1", false},
		{"an empty request matches nothing", "req-1", "", false},
		{"an empty receipt id", "", "req-1", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, ReceiptBelongs(recAt(c.rec, "n", "m", 0), c.request))
		})
	}
}

func TestRecordRefusesWhatItCannotDraw(t *testing.T) {
	s, now := newLedger(t)
	good := recAt("req-1", "house-cb", "gpt-oss-120b", now.Unix())
	for _, c := range []struct {
		name string
		t    Traffic
		want error
	}{
		{"another account's traffic",
			Traffic{Account: "acct-2", Kind: FromUse, Request: "req-1", Receipts: []protocol.UsageReceipt{good}},
			ErrOtherAccount},
		{"no account at all",
			Traffic{Kind: FromUse, Request: "req-1", Receipts: []protocol.UsageReceipt{good}},
			ErrOtherAccount},
		{"no receipt",
			Traffic{Account: "acct-1", Kind: FromUse, Request: "req-1"},
			ErrNoReceipt},
		{"somebody else's receipt",
			Traffic{Account: "acct-1", Kind: FromUse, Request: "req-1",
				Receipts: []protocol.UsageReceipt{recAt("req-9", "house-cb", "m", 0)}},
			ErrNoReceipt},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.Record(c.t)
			require.ErrorIs(t, err, c.want)
			require.Zero(t, s.Len())
		})
	}
	for _, c := range []struct {
		name, msg string
		t         Traffic
	}{
		{"no request", "names none",
			Traffic{Account: "acct-1", Kind: FromUse, Receipts: []protocol.UsageReceipt{good}}},
		{"not an initiator", "is not an initiator",
			Traffic{Account: "acct-1", Kind: Initiator("cron"), Request: "req-1", Receipts: []protocol.UsageReceipt{good}}},
		{"a guest with no name", "own name",
			Traffic{Account: "acct-1", Kind: FromGuest, Request: "req-1", Receipts: []protocol.UsageReceipt{good}}},
		{"a device with no name", "own name",
			Traffic{Account: "acct-1", Kind: FromDevice, Request: "req-1", Receipts: []protocol.UsageReceipt{good}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.Record(c.t)
			require.ErrorContains(t, err, c.msg)
			require.Zero(t, s.Len())
		})
	}
}

func TestRecordDerivesFromTheReceipts(t *testing.T) {
	ts := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).Unix()
	for _, c := range []struct {
		name                        string
		recs                        []protocol.UsageReceipt
		fallback                    string
		band, station, left, reason string
		out                         Outcome
	}{
		{name: "one receipt names the band and the station",
			recs: []protocol.UsageReceipt{recAt("r", "house-cb", "gpt-oss-120b", ts)},
			band: "gpt-oss-120b", station: "house-cb", out: OutcomeServed},
		{name: "a failover names both stations",
			recs: []protocol.UsageReceipt{
				voidAt("r", "cold-cb", "wave-nano", protocol.VoidEmptyOutput, ts),
				recAt("r-2", "house-cb", "wave-nano", ts)},
			band: "wave-nano", station: "house-cb", left: "cold-cb", out: OutcomeServed},
		{name: "two failed attempts before one that served",
			recs: []protocol.UsageReceipt{
				voidAt("r", "cold-a", "wave-nano", protocol.VoidUpstreamThrottled, ts),
				voidAt("r-2", "cold-b", "wave-nano", protocol.VoidEmptyOutput, ts),
				recAt("r-3", "house-cb", "wave-nano", ts)},
			band: "wave-nano", station: "house-cb", left: "cold-a", out: OutcomeServed},
		{name: "every attempt voided is a refusal, with the last reason",
			recs: []protocol.UsageReceipt{
				voidAt("r", "cold-a", "wave-nano", protocol.VoidUpstreamThrottled, ts),
				voidAt("r-2", "cold-b", "wave-nano", protocol.VoidEmptyOutput, ts)},
			band: "wave-nano", reason: protocol.VoidEmptyOutput, out: OutcomeRefused},
		{name: "nothing was ever dispatched, so no station is named",
			recs: []protocol.UsageReceipt{voidAt("r", "", "wave-nano", RefusedNoStation, ts)},
			band: "wave-nano", reason: RefusedNoStation, out: OutcomeRefused},
		{name: "a receipt with no model falls back to the band asked",
			recs:     []protocol.UsageReceipt{recAt("r", "house-cb", "", ts)},
			fallback: "wave-nano", band: "wave-nano", station: "house-cb", out: OutcomeServed},
		{name: "a void on the SAME station it later served is not a station left",
			recs: []protocol.UsageReceipt{
				voidAt("r", "house-cb", "m", protocol.VoidEmptyOutput, ts),
				recAt("r-2", "house-cb", "m", ts)},
			band: "m", station: "house-cb", out: OutcomeServed},
	} {
		t.Run(c.name, func(t *testing.T) {
			band, station, left, out, reason := deriveFromReceipts(c.recs, c.fallback)
			require.Equal(t, c.band, band)
			require.Equal(t, c.station, station)
			require.Equal(t, c.left, left)
			require.Equal(t, c.out, out)
			require.Equal(t, c.reason, reason)
		})
	}
}

func TestRecordIsAnUpsertOnTheRequest(t *testing.T) {
	s, now := newLedger(t)
	base := Traffic{Account: "acct-1", Kind: FromUse, Request: "req-1", Route: "log",
		Receipts: []protocol.UsageReceipt{voidAt("req-1", "cold-cb", "m", protocol.VoidEmptyOutput, now.Unix())}}
	first, err := s.Record(base)
	require.NoError(t, err)
	require.Equal(t, OutcomeRefused, first.Outcome)

	*now = now.Add(5 * time.Second)
	base.Receipts = append(base.Receipts, recAt("req-1-2", "house-cb", "m", now.Unix()))
	base.Route = "" // the relay does not re-state what it already said
	second, err := s.Record(base)
	require.NoError(t, err)
	require.Equal(t, 1, s.Len(), "an upsert must not make a second session")
	require.Equal(t, OutcomeServed, second.Outcome)
	require.Equal(t, "house-cb", second.Station)
	require.Equal(t, "cold-cb", second.Left)
	require.Equal(t, first.At, second.At, "the session is the one it always was")
	require.Equal(t, "log", second.Route, "an unstated route keeps the one it had")
}

func TestARefusalNeverCarriesAnAnswer(t *testing.T) {
	s, now := newLedger(t)
	got, err := s.Record(Traffic{Account: "acct-1", Kind: FromUse, Request: "req-1",
		Answer:   "the model said something",
		Receipts: []protocol.UsageReceipt{voidAt("req-1", "", "m", RefusedNoStation, now.Unix())}})
	require.NoError(t, err)
	require.Empty(t, got.Answer)
	require.Empty(t, got.Station)
}

func TestRoutingChangesOnlyTheDestination(t *testing.T) {
	s, now := newLedger(t)
	before, err := s.Record(Traffic{Account: "acct-1", Kind: FromDevice, Who: "bench-pi", Request: "req-1",
		Escalate: true, Answer: `{"verdict":"chattering"}`, Route: "log",
		Receipts: []protocol.UsageReceipt{recAt("req-1", "house-cb", "wave-nano", now.Unix())}})
	require.NoError(t, err)

	after, err := s.Route("req-1", "human-review")
	require.NoError(t, err)
	require.Equal(t, "human-review", after.Route)
	after.Route = before.Route
	require.Equal(t, before, after, "routing touched more than the destination")

	_, err = s.Route("req-missing", "log")
	require.ErrorIs(t, err, ErrNoSuchSession)
}

func TestSessionsFadeAndNeverAccumulate(t *testing.T) {
	s, now := newLedger(t)
	_, err := s.Record(Traffic{Account: "acct-1", Kind: FromUse, Request: "req-1",
		Receipts: []protocol.UsageReceipt{recAt("req-1", "house-cb", "m", now.Unix())}})
	require.NoError(t, err)
	require.Equal(t, 1, s.Len())

	// The boundary: still visible AT the life, gone past it.
	*now = now.Add(SessionLife)
	require.Equal(t, 1, s.Len(), "a session must live its whole life")
	_, ok := s.Get("req-1")
	require.True(t, ok)

	*now = now.Add(time.Second)
	require.Zero(t, s.Len())
	require.Empty(t, s.Live())
	_, ok = s.Get("req-1")
	require.False(t, ok)
}

func TestSetLifeShortensTheFade(t *testing.T) {
	s, now := newLedger(t)
	s.SetLife(time.Second)
	_, err := s.Record(Traffic{Account: "acct-1", Kind: FromUse, Request: "req-1",
		Receipts: []protocol.UsageReceipt{recAt("req-1", "house-cb", "m", now.Unix())}})
	require.NoError(t, err)
	*now = now.Add(2 * time.Second)
	require.Zero(t, s.Len())
}

func TestTheRingIsBoundedAndDropsTheOldest(t *testing.T) {
	s, now := newLedger(t)
	for i := 0; i < SessionsMax+50; i++ {
		req := "req-" + strings.Repeat("0", 4-len(itoa(i))) + itoa(i)
		_, err := s.Record(Traffic{Account: "acct-1", Kind: FromUse, Request: req,
			Receipts: []protocol.UsageReceipt{recAt(req, "house-cb", "m", now.Unix())}})
		require.NoError(t, err)
	}
	require.Equal(t, SessionsMax, s.Len())
	_, ok := s.Get("req-0000")
	require.False(t, ok, "the oldest is what gives way, so the relay never is refused")
	_, ok = s.Get("req-0305")
	require.True(t, ok)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestLiveIsNewestFirstAndACopy(t *testing.T) {
	s, now := newLedger(t)
	for i, req := range []string{"req-a", "req-b"} {
		*now = now.Add(time.Duration(i) * time.Second)
		_, err := s.Record(Traffic{Account: "acct-1", Kind: FromUse, Request: req,
			Receipts: []protocol.UsageReceipt{recAt(req, "house-cb", "m", now.Unix())}})
		require.NoError(t, err)
	}
	live := s.Live()
	require.Len(t, live, 2)
	require.Equal(t, "req-b", live[0].Request)

	live[0].Band = "tampered"
	require.Equal(t, "m", s.Live()[0].Band, "Live must hand out a copy")
	require.Equal(t, "acct-1", s.Account())
}

// ---- the contract --------------------------------------------------------

func TestProductionFramingIsTheShippedText(t *testing.T) {
	js, err := os.ReadFile("../../web/src/js/playbox.js")
	require.NoError(t, err, "the production framing map must be readable")
	classes := TaskClasses()
	require.NotEmpty(t, classes)
	for _, c := range classes {
		f := ProductionFraming(c)
		require.NotEmpty(t, f, c)
		require.Contains(t, string(js), f,
			"the framing for %q is not the one the Playbox ships - a paraphrase has crept in", c)
	}
	require.Empty(t, ProductionFraming("no-such-class"))
}

func TestSetContractRefusesAnUnframedDevice(t *testing.T) {
	db := store.NewMem()
	f := NewFleet(db, "acct-1")
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	board, err := f.Enroll(NewNode(pub, "bench-pi", Board, Classify))
	require.NoError(t, err)

	pub2, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sensor, err := f.Enroll(NewNode(pub2, "bench-sensor", Board, Sense))
	require.NoError(t, err)

	for _, c := range []struct {
		name, id, msg string
		ct            Contract
	}{
		{"no task class", board.ID, "task class", Contract{Framing: "x"}},
		{"no framing", board.ID, "one unit", Contract{Class: "alarm_triage"}},
		{"a node that does not classify", sensor.ID, "does not declare classify",
			Contract{Class: "alarm_triage", Framing: "x"}},
		{"no such node", "nope", "no such node",
			Contract{Class: "alarm_triage", Framing: "x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.ErrorContains(t, f.SetContract(c.id, c.ct), c.msg)
		})
	}

	want := Contract{Class: "alarm_triage", Framing: ProductionFraming("alarm_triage"),
		Labels: []string{"ok", "escalate"}}
	require.NoError(t, f.SetContract(board.ID, want))
	got, ok, err := f.Get(board.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, got.Contract)
}

func TestARecordedSessionKeepsItsOwnReceipts(t *testing.T) {
	s, now := newLedger(t)
	recs := []protocol.UsageReceipt{recAt("req-1", "house-cb", "m", now.Unix())}
	got, err := s.Record(Traffic{Account: "acct-1", Kind: FromUse, Request: "req-1", Receipts: recs})
	require.NoError(t, err)
	recs[0].NodeID = "somewhere-else"
	require.Equal(t, "house-cb", got.Receipts[0].NodeID, "the ledger must copy the evidence it was given")
	require.True(t, errors.Is(ErrNoReceipt, ErrNoReceipt))
}
