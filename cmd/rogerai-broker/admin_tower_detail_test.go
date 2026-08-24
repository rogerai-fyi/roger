package main

// Contract: features/tower/admin_tower_detail.feature
//
// The admin Tower detail view: everything Core knows about ONE Tower, gathered for the
// dashboard - identity and lifecycle, the quality signals (with the thresholds they are
// judged against), the traffic by model, the country demand came from, and the Stations
// behind it. This test drives every scenario against a live subsystem and asserts the two
// promises the view rests on: it shows the operator what they need to manage quality, and it
// never exposes a secret or who any consumer was.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/earnings"
	"rogerai.fm/roger/v6/internal/towercore/reputation"
)

var errTestStore = errors.New("store read failed (test)")

func getTowerDetail(t *testing.T, b *broker, id string, authed bool) (int, map[string]any) {
	t.Helper()
	url := "/admin/tower"
	if id != "\x00" { // sentinel: omit the id param entirely
		url += "?id=" + id
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if authed {
		req.Header.Set("X-Roger-Admin", "admin-secret")
	}
	rec := httptest.NewRecorder()
	b.adminTowerDetail(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, body
}

func detailBroker(t *testing.T) (*broker, string) {
	t.Helper()
	b, _ := towerTestBroker(t)
	b.adminKey = "admin-secret"
	tw := enrolledTower(t, b, "op-alice")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	return b, tw.id
}

func TestTowerDetailAccessAndErrors(t *testing.T) {
	b, id := detailBroker(t)

	// Unauthenticated: refused, and nothing about the Tower in the body.
	code, body := getTowerDetail(t, b, id, false)
	require.Equal(t, http.StatusForbidden, code, "the admin gate is the same 403 as the queue")
	require.NotContains(t, body, "tower_id")

	// Wrong method.
	req := httptest.NewRequest(http.MethodPost, "/admin/tower?id="+id, nil)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	rec := httptest.NewRecorder()
	b.adminTowerDetail(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	// Unknown id: 404, not an empty Tower.
	code, body = getTowerDetail(t, b, "tw-does-not-exist", true)
	require.Equal(t, http.StatusNotFound, code)
	require.NotContains(t, body, "tower_id")

	// No id: 400.
	code, _ = getTowerDetail(t, b, "\x00", true)
	require.Equal(t, http.StatusBadRequest, code)
}

func TestTowerDetailIdentityAndNoSecrets(t *testing.T) {
	b, id := detailBroker(t)
	code, body := getTowerDetail(t, b, id, true)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, id, body["tower_id"])
	require.Equal(t, "op-alice", body["owner"])
	require.Equal(t, string(admit.StateActive), body["state"])
	_, terr := time.Parse(time.RFC3339, body["enrolled"].(string))
	require.NoError(t, terr, "enrolled is an RFC3339 timestamp")
	require.Contains(t, body, "link_live")

	// No secret anywhere in the serialized response.
	raw := rawDetail(t, b, id)
	for _, secret := range []string{"epoch", "dispatch_key", "session_key", "grant", "admin", "key_hash", "keyhash", "privkey", "private"} {
		require.NotContains(t, strings.ToLower(raw), secret, "the detail must not leak %q", secret)
	}
}

func rawDetail(t *testing.T, b *broker, id string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/tower?id="+id, nil)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	rec := httptest.NewRecorder()
	b.adminTowerDetail(rec, req)
	return rec.Body.String()
}

func TestTowerDetailQualityTallyAndThresholds(t *testing.T) {
	b, id := detailBroker(t)
	for i := 0; i < 8; i++ {
		b.recordOutcome(id, "", attemptN("cp", i), reputation.CanaryPass)
	}
	b.recordOutcome(id, "", "cf-0", reputation.CanaryFail)
	for i := 0; i < 20; i++ {
		b.recordOutcome(id, "", attemptN("co", i), reputation.Corroborated)
	}
	for i := 0; i < 5; i++ {
		b.recordOutcome(id, "", attemptN("un", i), reputation.Uncorroborated)
	}
	b.recordOutcome(id, "", "am-0", reputation.AuditMismatch)
	for i := 0; i < 3; i++ {
		b.recordOutcome(id, "", attemptN("sf", i), reputation.StationFault)
	}

	_, body := getTowerDetail(t, b, id, true)
	q := body["quality"].(map[string]any)
	require.Equal(t, float64(8), q["canary_pass"])
	require.Equal(t, float64(1), q["canary_fail"])
	require.Equal(t, float64(20), q["corroborated"])
	require.Equal(t, float64(5), q["uncorroborated"])
	require.Equal(t, float64(1), q["audit_mismatch"])
	require.Equal(t, float64(3), q["station_fault"])
	require.Contains(t, q, "total")
	// Thresholds it is judged against.
	require.Equal(t, float64(5), q["min_canaries"])
	require.InDelta(t, 0.40, q["max_canary_fail_rate"], 0.0001)
	require.InDelta(t, 1.0/9.0, q["canary_fail_rate"], 0.0001)
	require.Equal(t, true, q["rate_judgeable"])
	require.Equal(t, false, q["over_fail_threshold"])
}

func TestTowerDetailQualityThresholdFlags(t *testing.T) {
	// Over the failure threshold: 3 pass, 7 fail -> 70% > 40%, and >= 5 canaries.
	b, id := detailBroker(t)
	for i := 0; i < 3; i++ {
		b.recordOutcome(id, "", attemptN("p", i), reputation.CanaryPass)
	}
	for i := 0; i < 7; i++ {
		b.recordOutcome(id, "", attemptN("f", i), reputation.CanaryFail)
	}
	_, body := getTowerDetail(t, b, id, true)
	q := body["quality"].(map[string]any)
	require.Equal(t, true, q["over_fail_threshold"])
	require.Equal(t, true, q["rate_judgeable"])

	// Too few canaries: not yet judgeable, and never over-threshold from that.
	b2, id2 := detailBroker(t)
	b2.recordOutcome(id2, "", "p0", reputation.CanaryPass)
	b2.recordOutcome(id2, "", "f0", reputation.CanaryFail)
	_, body2 := getTowerDetail(t, b2, id2, true)
	q2 := body2["quality"].(map[string]any)
	require.Equal(t, false, q2["rate_judgeable"])
	require.Equal(t, false, q2["over_fail_threshold"])

	// Station faults are counted but never feed the suspension rate.
	b3, id3 := detailBroker(t)
	for i := 0; i < 5; i++ {
		b3.recordOutcome(id3, "", attemptN("cp", i), reputation.CanaryPass)
	}
	for i := 0; i < 40; i++ {
		b3.recordOutcome(id3, "", attemptN("sf", i), reputation.StationFault)
	}
	_, body3 := getTowerDetail(t, b3, id3, true)
	q3 := body3["quality"].(map[string]any)
	require.Equal(t, float64(40), q3["station_fault"])
	require.Equal(t, false, q3["over_fail_threshold"])
}

func TestTowerDetailPerStationQuality(t *testing.T) {
	b, id := detailBroker(t)
	for i := 0; i < 10; i++ {
		b.recordOutcome(id, "st-good", attemptN("g", i), reputation.CanaryPass)
	}
	for i := 0; i < 2; i++ {
		b.recordOutcome(id, "st-bad", attemptN("bp", i), reputation.CanaryPass)
	}
	for i := 0; i < 8; i++ {
		b.recordOutcome(id, "st-bad", attemptN("bf", i), reputation.CanaryFail)
	}
	_, body := getTowerDetail(t, b, id, true)
	q := body["quality"].(map[string]any)
	byStation := q["by_station"].([]any)
	got := map[string]float64{}
	for _, s := range byStation {
		m := s.(map[string]any)
		got[m["station_id"].(string)] = m["canary_fail"].(float64)
	}
	require.Equal(t, float64(0), got["st-good"])
	require.Equal(t, float64(8), got["st-bad"])
}

func TestTowerDetailTrafficByModel(t *testing.T) {
	b, id := detailBroker(t)
	acc := func(att, model string, in, out, micros int64, corr, self bool) {
		require.NoError(t, b.tower.earnings.Accrue(earnings.Accrual{
			TowerID: id, Owner: "op-alice", AttemptID: att, Model: model,
			UsageIn: in, UsageOut: out, Micros: micros, Corroborated: corr, SelfDealing: self,
			At: time.Now(),
		}))
	}
	acc("l1", "llama", 3000, 6000, 100, true, false)
	acc("l2", "llama", 3000, 6000, 100, false, false)
	acc("q1", "qwen", 1000, 2500, 90, true, false)
	acc("s1", "llama", 1, 1, 999, true, true) // self-dealing: surfaced, not owed

	_, body := getTowerDetail(t, b, id, true)
	tr := body["traffic"].(map[string]any)
	models := map[string]map[string]any{}
	for _, m := range tr["by_model"].([]any) {
		mm := m.(map[string]any)
		models[mm["model"].(string)] = mm
	}
	llama := models["llama"]
	require.Equal(t, float64(3), llama["attempts"])
	require.Equal(t, float64(2), llama["corroborated"])
	require.Equal(t, float64(1), llama["uncorroborated"])
	require.Equal(t, float64(200), llama["micros"], "self-dealing 999 excluded")
	require.Equal(t, float64(999), llama["self_dealt"])
	require.Equal(t, float64(90), models["qwen"]["micros"])
	require.Equal(t, float64(290), tr["micros"], "200 llama + 90 qwen")
	require.Equal(t, float64(999), tr["self_dealt"])

	// No consumer identity anywhere in the traffic block.
	raw := rawDetail(t, b, id)
	block := strings.ToLower(rawTrafficBlock(t, raw))
	for _, forbidden := range []string{"wallet", "pubkey", "consumer", "account", "\"ip\""} {
		require.NotContains(t, block, forbidden, "the traffic block must not carry %q", forbidden)
	}
}

// rawTrafficBlock isolates the traffic object text so identity assertions target it.
func rawTrafficBlock(t *testing.T, raw string) string {
	i := strings.Index(raw, `"traffic"`)
	if i < 0 {
		return ""
	}
	return raw[i:]
}

func TestTowerDetailOrigin(t *testing.T) {
	b, id := detailBroker(t)
	rec := func(att, country string) { require.NoError(t, b.tower.origin.Record(id, att, country, time.Now())) }
	for i := 0; i < 40; i++ {
		rec(attemptN("us", i), "US")
	}
	for i := 0; i < 15; i++ {
		rec(attemptN("de", i), "DE")
	}
	for i := 0; i < 5; i++ {
		rec(attemptN("br", i), "BR")
	}
	for i := 0; i < 7; i++ {
		rec(attemptN("no", i), "") // no header -> unknown
	}

	_, body := getTowerDetail(t, b, id, true)
	origin := map[string]float64{}
	for _, o := range body["origin"].([]any) {
		m := o.(map[string]any)
		origin[m["country"].(string)] = m["attempts"].(float64)
	}
	require.Equal(t, float64(40), origin["US"])
	require.Equal(t, float64(15), origin["DE"])
	require.Equal(t, float64(5), origin["BR"])
	require.Equal(t, float64(7), origin["unknown"])

	// No address or identity in the origin block.
	raw := rawDetail(t, b, id)
	oi := strings.Index(raw, `"origin"`)
	require.GreaterOrEqual(t, oi, 0)
	block := raw[oi:]
	for _, forbidden := range []string{"ip", "wallet", "pubkey", "account"} {
		require.NotContains(t, strings.ToLower(block[:min(len(block), 400)]), forbidden)
	}
}

func TestTowerDetailFleet(t *testing.T) {
	b, id := detailBroker(t)
	// Publish both Stations in one Replace (Replace wipes prior rows for the Tower).
	require.NoError(t, b.tower.routable.Replace(id, []fleetStation{
		{TowerID: id, StationID: "st-0aa1", OfferID: "self-a", Model: "llama", Modality: "text", Expires: time.Now().Add(time.Hour), Endpoint: "203.0.113.7:8443", PriceIn: 100, PriceOut: 200},
		{TowerID: id, StationID: "st-0bb2", OfferID: "self-b", Model: "qwen", Modality: "text", Expires: time.Now().Add(time.Hour), Endpoint: "203.0.113.8:8443", PriceIn: 50, PriceOut: 60},
	}))
	_, body := getTowerDetail(t, b, id, true)
	fleet := map[string]string{}
	for _, f := range body["fleet"].([]any) {
		m := f.(map[string]any)
		fleet[m["station_id"].(string)] = m["model"].(string)
		require.Contains(t, m, "price_in")
		require.Contains(t, m, "price_out")
	}
	require.Equal(t, "llama", fleet["st-0aa1"])
	require.Equal(t, "qwen", fleet["st-0bb2"])
}

func attemptN(prefix string, i int) string {
	return prefix + "-" + string(rune('a'+i%26)) + string(rune('0'+(i/26)%10))
}

// The origin country is taken from Cloudflare's CF-IPCountry, never a client-supplied
// header: an ordinary edge request cannot move the tally by inventing a country. (It is a
// monitoring aid, not an authorization input - a caller reaching the origin directly could
// forge it, and that is acceptable, so this pins the ordinary-path behaviour, not a security
// boundary.)
func TestClientCountryTakesCFHeaderNotClientHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("CF-IPCountry", "US")
	r.Header.Set("X-Country", "CN") // a forged client header must be ignored
	require.Equal(t, "US", clientCountry(r), "the CF header wins, the client header is ignored")

	// Absent: empty, which the origin store records as "unknown".
	r2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	require.Equal(t, "", clientCountry(r2))
}

// A failed store read must surface as a 500, never as a silent zero on a money/quality view.
// Each wrapper embeds the real store and fails ONE method, so the reads before it succeed and
// the handler is driven to that read's error branch specifically.
type failTally struct {
	reputation.Store
}

func (failTally) Tally(string, time.Time) (reputation.Tally, error) {
	return reputation.Tally{}, errTestStore
}

type failByStation struct {
	reputation.Store
}

func (failByStation) TallyByStation(string, time.Time) (map[string]reputation.Tally, error) {
	return nil, errTestStore
}

type failTraffic struct {
	earnings.Store
}

func (failTraffic) TowerTraffic(string, time.Time) (earnings.TowerTraffic, error) {
	return earnings.TowerTraffic{}, errTestStore
}

type failOrigin struct {
	originStoreIface
}

func (failOrigin) ByTower(string, time.Time) ([]originTally, error) { return nil, errTestStore }

type failFleet struct {
	fleetStoreIface
}

func (failFleet) ByTower(string, time.Time) ([]fleetStation, error) { return nil, errTestStore }

func TestTowerDetailSurfacesReadFailures(t *testing.T) {
	real := func() (*broker, string) { return detailBroker(t) }

	// outcomes.Tally fails.
	b, id := real()
	b.tower.outcomes = failTally{b.tower.outcomes}
	code, _ := getTowerDetail(t, b, id, true)
	require.Equal(t, http.StatusInternalServerError, code, "a Tally read failure is a 500")

	// TallyByStation fails (Tally still succeeds).
	b, id = real()
	b.tower.outcomes = failByStation{b.tower.outcomes}
	code, _ = getTowerDetail(t, b, id, true)
	require.Equal(t, http.StatusInternalServerError, code)

	// earnings.TowerTraffic fails.
	b, id = real()
	b.tower.earnings = failTraffic{b.tower.earnings}
	code, _ = getTowerDetail(t, b, id, true)
	require.Equal(t, http.StatusInternalServerError, code)

	// origin.ByTower fails.
	b, id = real()
	b.tower.origin = failOrigin{b.tower.origin}
	code, _ = getTowerDetail(t, b, id, true)
	require.Equal(t, http.StatusInternalServerError, code)

	// routable.ByTower fails.
	b, id = real()
	b.tower.routable = failFleet{b.tower.routable}
	code, _ = getTowerDetail(t, b, id, true)
	require.Equal(t, http.StatusInternalServerError, code)
}
