package edge

// features/edge/trouble_feed.feature: the owner's phone, a MEMBER holding a certificate this
// authority issued, pulls the standing trouble calls from the machine's mTLS face, learns what
// changed since it last looked, and answers back under its own certificate. A non-member, or a
// revoked member, gets nothing. Real face, real mTLS, real alert log.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/cert"
)

type troubleRig struct {
	face      *httptest.Server
	phone     tls.Certificate
	phoneLeaf string // its serial, to revoke it
	stranger  tls.Certificate
	log       AlertLog
	revoked   []string
	authority *cert.Authority
	now       time.Time // the face's clock; zero = the real one
}

func newTroubleRig(t *testing.T) *troubleRig {
	t.Helper()
	authority, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	other, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	issue := func(a *cert.Authority, name string) (tls.Certificate, string) {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		leaf, err := a.Issue(name, pub)
		require.NoError(t, err)
		return tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv}, leaf.SerialNumber.String()
	}
	serveCert, _ := issue(authority, "n_desk")
	_, servePriv, _ := ed25519.GenerateKey(rand.Reader)
	rig := &troubleRig{log: AlertLog{Dir: t.TempDir()}, authority: authority}
	rig.phone, rig.phoneLeaf = issue(authority, "n_ibex")
	rig.stranger, _ = issue(other, "n_ibex")
	names := map[string]string{"n_desk": "desk", "n_ibex": "gentle-ibex-14"}
	srv := NewServer(Describe{NodeID: "n_desk", Account: "acct-1", Kind: "host"}, serveCert)
	srv.SetServing(Serving{
		Trust:    mustVerifier(t, authority, nil),
		TrustNow: func() *cert.Authority { return mustVerifier(t, authority, rig.revoked) },
		Member:   func(id string) bool { return names[id] != "" },
		NodeID:   "n_desk", Key: servePriv,
		Now: func() time.Time {
			if rig.now.IsZero() {
				return time.Now()
			}
			return rig.now
		},
		Trouble: &rig.log,
		Names:   func(id string) string { return names[id] },
	})
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.TLS = srv.ListenerTLS()
	ts.StartTLS()
	t.Cleanup(ts.Close)
	rig.face = ts
	return rig
}

func mustVerifier(t *testing.T, a *cert.Authority, revoked []string) *cert.Authority {
	t.Helper()
	v, err := cert.NewVerifier(a.Root(), revoked)
	require.NoError(t, err)
	return v
}

func (r *troubleRig) client(c *tls.Certificate) *http.Client {
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	if c != nil {
		cfg.Certificates = []tls.Certificate{*c}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
}

func (r *troubleRig) get(t *testing.T, c *tls.Certificate, q string) (int, string) {
	t.Helper()
	resp, err := r.client(c).Get(r.face.URL + TroublePath + q)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (r *troubleRig) post(t *testing.T, c *tls.Certificate, path string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	hr, _ := http.NewRequest(http.MethodPost, r.face.URL+path, bytes.NewReader(b))
	hr.Header.Set("Content-Type", "application/json")
	resp, err := r.client(c).Do(hr)
	require.NoError(t, err)
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(rb)
}

func hotDesk(at time.Time, temp float64) []NodeHealth {
	tt := temp
	return []NodeHealth{{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(PresenceVerified), LastHeard: at,
		Readings: &store.EdgeResources{CPUTempC: &tt, ReadAt: at.Unix()}}}
}

func TestTroubleFeedIsReadByMembersOnly(t *testing.T) {
	rig := newTroubleRig(t)
	now := time.Now()
	_, _, err := rig.log.Judge(hotDesk(now, 95), now)
	require.NoError(t, err)

	code, body := rig.get(t, &rig.phone, "")
	require.Equal(t, http.StatusOK, code, body)
	var feed TroubleFeed
	require.NoError(t, json.Unmarshal([]byte(body), &feed))
	require.Len(t, feed.Standing, 1)
	c := feed.Standing[0]
	require.Equal(t, "desk", c.Node)
	require.Equal(t, "cpu_temp > 85", c.When)
	require.Equal(t, "HI", c.Priority)
	require.Equal(t, "95.0°C", c.Value)
	require.NotEmpty(t, c.Why)
	require.NotEmpty(t, c.Do)
	require.NotZero(t, feed.At)
	require.NotEmpty(t, feed.Cursor)
	require.Equal(t, 1, feed.Rate)

	// No certificate: refused, and nothing about the fleet in the reply.
	code, body = rig.get(t, nil, "")
	require.Equal(t, http.StatusUnauthorized, code)
	require.NotContains(t, body, "cpu_temp")
	require.NotContains(t, body, "desk")

	// A certificate from another authority: refused.
	code, body = rig.get(t, &rig.stranger, "")
	require.Equal(t, http.StatusUnauthorized, code)
	require.NotContains(t, body, "cpu_temp")

	// The owner forgot the phone: its certificate is revoked; the one it still holds is refused.
	rig.revoked = []string{rig.phoneLeaf}
	code, body = rig.get(t, &rig.phone, "")
	require.Equal(t, http.StatusUnauthorized, code)
	require.NotContains(t, body, "cpu_temp")
	code, _ = rig.post(t, &rig.phone, TroubleRogerPath, map[string]string{"rule": "cpu_temp", "node": "n_desk"})
	require.NotEqual(t, http.StatusOK, code, "a revoked member cannot answer either")
	calls, _ := rig.log.Standing(now)
	require.False(t, calls[0].Acked)

	// The read was recorded: the CLI can name the phone as the sink and when it last looked.
	reads, err := rig.log.Reads()
	require.NoError(t, err)
	require.Contains(t, reads, "n_ibex")
}

func TestTroubleFeedCursorTellsWhatChangedSoThePhoneNotifiesOnce(t *testing.T) {
	rig := newTroubleRig(t)
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	rig.now = t0.Add(time.Minute)
	_, _, _ = rig.log.Judge(hotDesk(t0, 95), t0)
	code, body := rig.get(t, &rig.phone, "")
	require.Equal(t, http.StatusOK, code)
	var first TroubleFeed
	require.NoError(t, json.Unmarshal([]byte(body), &first))
	require.Len(t, first.Transitions, 1, "no cursor: the last hour of transitions")

	_, _, _ = rig.log.Judge(hotDesk(t0, 70), t0.Add(5*time.Minute))  // clears
	_, _, _ = rig.log.Judge(hotDesk(t0, 95), t0.Add(10*time.Minute)) // fires again
	rig.now = t0.Add(11 * time.Minute)
	code, body = rig.get(t, &rig.phone, "?since="+first.Cursor)
	require.Equal(t, http.StatusOK, code)
	var second TroubleFeed
	require.NoError(t, json.Unmarshal([]byte(body), &second))
	// What changed after the cursor, plus two minutes of slack before it (a judgement that began
	// before the last read and landed after it must not be lost); the phone dedupes by
	// (rule, node, at), so the 12:00 firing it already saw comes again and is dropped there.
	require.Len(t, second.Transitions, 3)
	require.True(t, second.Transitions[0].Firing, "the 12:00 firing, inside the slack")
	require.False(t, second.Transitions[1].Firing, "the clear at 12:05")
	require.True(t, second.Transitions[2].Firing, "then the firing at 12:10")
	require.Len(t, second.Standing, 1, "the standing calls are still complete")
	require.NotEqual(t, first.Cursor, second.Cursor)
}

func TestThePhoneSaysRogerAndStandsBy(t *testing.T) {
	rig := newTroubleRig(t)
	now := time.Now()
	_, _, _ = rig.log.Judge(hotDesk(now, 95), now)

	code, body := rig.post(t, &rig.phone, TroubleRogerPath, map[string]string{"rule": "cpu_temp", "node": "n_desk"})
	require.Equal(t, http.StatusOK, code, body)
	calls, _ := rig.log.Standing(now)
	require.True(t, calls[0].Acked)
	require.Equal(t, "gentle-ibex-14", calls[0].AckBy, "the acknowledgement names the phone")
	_, body = rig.get(t, &rig.phone, "")
	var feed TroubleFeed
	require.NoError(t, json.Unmarshal([]byte(body), &feed))
	require.True(t, feed.Standing[0].Acked)
	require.Equal(t, "gentle-ibex-14", feed.Standing[0].AckBy)

	code, _ = rig.post(t, &rig.phone, TroubleStandByPath, map[string]any{"rule": "cpu_temp", "node": "n_desk", "hours": 2})
	require.Equal(t, http.StatusOK, code)
	calls, _ = rig.log.Standing(now)
	require.True(t, calls[0].Shelved)
	require.WithinDuration(t, time.Now().Add(2*time.Hour), calls[0].StandByUntil, 10*time.Second)
	code, _ = rig.post(t, &rig.phone, TroubleStandByPath, map[string]any{"rule": "cpu_temp", "node": "n_desk", "hours": 48})
	require.Equal(t, http.StatusOK, code)
	calls, _ = rig.log.Standing(now)
	require.WithinDuration(t, time.Now().Add(8*time.Hour), calls[0].StandByUntil, 10*time.Second, "bounded to eight hours")

	code, body = rig.post(t, &rig.phone, TroubleRogerPath, map[string]string{"rule": "disk", "node": "n_desk"})
	require.Equal(t, http.StatusNotFound, code, body)

	code, _ = rig.post(t, nil, TroubleRogerPath, map[string]string{"rule": "cpu_temp", "node": "n_desk"})
	require.Equal(t, http.StatusUnauthorized, code)
}

func TestTroubleFeedCarriesNoSecretAndTestCallsAreMarked(t *testing.T) {
	rig := newTroubleRig(t)
	now := time.Now()
	_, _, _ = rig.log.Judge(hotDesk(now, 95), now)
	require.NoError(t, rig.log.Roger("cpu_temp", "n_desk", "luis@example.com", now))
	require.NoError(t, rig.log.TestCall("gentle-ibex-14", now))
	_, body := rig.get(t, &rig.phone, "")
	for _, secret := range []string{"BEGIN", "@", "\"sig\"", "token", "key\":"} {
		require.NotContains(t, body, secret, "the feed carries no secret and no address")
	}
	var feed TroubleFeed
	require.NoError(t, json.Unmarshal([]byte(body), &feed))
	require.Equal(t, "luis", feed.Standing[0].AckBy, "a handle, never a whole address")
	tests := 0
	for _, tr := range feed.Transitions {
		if tr.Test {
			tests++
			require.Contains(t, tr.When, "TEST")
		}
	}
	require.Equal(t, 1, tests, "the test call appears once, clearly marked")
	require.Len(t, feed.Standing, 1, "and it is not a standing call: no rule state changed")
	st, _ := rig.log.State()
	require.Len(t, st, 1)
}

// The review's gaps: the last hour and no more; a member of another account is 403; a cursor the
// feed never gave is refused; a stand-by names who asked; a test call moves no rate and no
// sign-off; a forgotten reader leaves the sink line.
func TestTroubleFeedEdgesTheReviewAsked(t *testing.T) {
	rig := newTroubleRig(t)
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	rig.now = t0
	_, _, _ = rig.log.Judge(hotDesk(t0, 95), t0.Add(-2*time.Hour)) // fired two hours ago
	_, _, _ = rig.log.Judge(hotDesk(t0, 70), t0.Add(-90*time.Minute))
	_, _, _ = rig.log.Judge(hotDesk(t0, 95), t0.Add(-10*time.Minute))
	_, body := rig.get(t, &rig.phone, "")
	var feed TroubleFeed
	require.NoError(t, json.Unmarshal([]byte(body), &feed))
	require.Len(t, feed.Transitions, 1, "no cursor: the last hour of transitions and no more")

	code, body := rig.get(t, &rig.phone, "?since=yesterday")
	require.Equal(t, http.StatusBadRequest, code, body)
	code, _ = rig.get(t, &rig.phone, "?since=-5")
	require.Equal(t, http.StatusBadRequest, code)

	// A stand-by from the phone names the phone.
	code, _ = rig.post(t, &rig.phone, TroubleStandByPath, map[string]any{"rule": "cpu_temp", "node": "n_desk", "hours": 0.001})
	require.Equal(t, http.StatusOK, code)
	calls, _ := rig.log.Standing(t0)
	require.Equal(t, "gentle-ibex-14", calls[0].StandByBy)
	require.WithinDuration(t, t0.Add(time.Hour), calls[0].StandByUntil, time.Second, "an hour at least")
	_, body = rig.get(t, &rig.phone, "")
	require.NoError(t, json.Unmarshal([]byte(body), &feed))
	require.Equal(t, "gentle-ibex-14", feed.Standing[0].StandBy)

	// A test call is in the transitions, and nowhere else.
	require.NoError(t, rig.log.TestCall("gentle-ibex-14", t0))
	rate, _ := rig.log.Rate(t0, 10*time.Minute)
	require.Equal(t, 1, rate, "the real firing ten minutes ago, not the test")
	so, _ := rig.log.SignOff(t0, 8*time.Hour)
	require.Equal(t, 2, so.Raised, "two real firings, no test")

	// A forgotten reader leaves the sink line.
	reads, _ := rig.log.Reads()
	require.Contains(t, reads, "n_ibex")
	require.NoError(t, rig.log.ForgetReader("n_ibex"))
	reads, _ = rig.log.Reads()
	require.NotContains(t, reads, "n_ibex")
}

func TestTroubleFeedRefusesAMemberOfAnotherAccount(t *testing.T) {
	rig := newTroubleRig(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	leaf, err := rig.authority.Issue("n_other", pub) // under our root, but not on this fleet
	require.NoError(t, err)
	other := tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv}
	code, body := rig.get(t, &other, "")
	require.Equal(t, http.StatusForbidden, code)
	require.NotContains(t, body, "desk")
}
