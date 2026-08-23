package main

// features/tower/approval_visibility.feature, broker half: the heartbeat carries the
// state, the canary never dials inward, and the admin hears about arrivals - throttled.

import (
	"net"
	"net/http"
	"testing"
	"time"

	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerhub"
)

// The heartbeat answer names the admission state, and it CHANGES within one beat of the
// admin's decision - this is what lets serve announce an approval without a restart.
func TestHeartbeatCarriesTheAdmissionState(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	openBody := jsonOf(t, link.Hello{Network: link.PublicNetwork, Versions: []int{1},
		TowerID: lt.id, Capabilities: mandatoryCaps()})
	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session", openBody, &acc)
	require.Equal(t, http.StatusOK, code, raw)

	beat := func() string {
		var out struct {
			State string `json:"state"`
		}
		code, raw := lt.call(t, srv, "/tower/session/heartbeat",
			jsonOf(t, link.Frame{Network: link.PublicNetwork, Version: 1,
				TowerID: lt.id, SessionID: acc.SessionID}), &out)
		require.Equal(t, http.StatusOK, code, raw)
		return out.State
	}
	require.Equal(t, string(admit.StateQuarantine), beat(),
		"the operator's terminal learns it is waiting from this field")

	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	require.Equal(t, string(admit.StateActive), beat(),
		"the approval reaches the next heartbeat, not the next restart")
}

// Everything not publicly routable is refused by name; everything routable passes.
func TestVetPublicIPRefusesEveryInwardRange(t *testing.T) {
	for _, bad := range []string{
		"127.0.0.1", "::1", "10.0.0.1", "172.16.3.4", "192.168.1.10",
		"169.254.169.254", "fe80::1", "0.0.0.0", "::", "224.0.0.1",
		"::ffff:127.0.0.1", "::ffff:10.1.2.3",
	} {
		require.Error(t, vetPublicIP(net.ParseIP(bad)), "%s must be refused", bad)
	}
	for _, good := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		require.NoError(t, vetPublicIP(net.ParseIP(good)), "%s is publicly routable", good)
	}
}

// The vet lives in the DIALER: a hostname resolving somewhere private is refused at the
// socket, which is the only place DNS rebinding cannot slip past.
func TestReachVettedRefusesAtTheSocket(t *testing.T) {
	// localhost resolves to loopback; the connect must fail on the vet, not on routing.
	_, hc, err := towerhub.ReachVetted("localhost:9", "", vetPublicIP)
	require.NoError(t, err, "building the client is fine; the guard is at dial time")
	req, _ := http.NewRequest(http.MethodGet, "http://localhost:9/", nil)
	_, err = hc.Do(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to dial", "the refusal must be the vet's, not a timeout")
	require.Contains(t, err.Error(), "loopback")

	// A nil vet is explicitly the unvetted path - the one nodes use on purpose.
	_, _, err = towerhub.ReachVetted("localhost:9", "", nil)
	require.NoError(t, err)
}

// One email per owner per window; the burst is visible as a count on the next one.
func TestTowerPendingNotifierThrottlesPerOwner(t *testing.T) {
	type sent struct {
		owner, id  string
		suppressed int
	}
	var got []sent
	n := newTowerPendingNotifier(func(o, id string, sup int) { got = append(got, sent{o, id, sup}) })
	clock := time.Now()
	n.now = func() time.Time { return clock }

	for i := 0; i < 5; i++ {
		n.enrolled("owner-a", "tw-1")
	}
	n.enrolled("owner-b", "tw-9")
	require.Len(t, got, 2, "five in one window is one email; another owner is another email")
	require.Equal(t, "owner-a", got[0].owner)
	require.Zero(t, got[0].suppressed)
	require.Equal(t, "owner-b", got[1].owner)

	clock = clock.Add(2 * time.Hour)
	n.enrolled("owner-a", "tw-2")
	require.Len(t, got, 3)
	require.Equal(t, 4, got[2].suppressed, "the burst rides the next email as a count")
}

// The words the admin reads: the id to approve, the owner to judge, the place to do it,
// the reassurance that waiting is the design - and never a secret.
func TestTowerPendingEmailSaysWhatTheAdminNeeds(t *testing.T) {
	subject, text := towerPendingEmail("octocat", "tw-abc123", 0)
	require.Contains(t, subject, "tw-abc123")
	for _, must := range []string{"tw-abc123", "octocat", "admin dashboard", "quarantine", "not a fault"} {
		require.Contains(t, text, must)
	}
	_, burst := towerPendingEmail("octocat", "tw-abc123", 3)
	require.Contains(t, burst, "3 more")
}

// The production constructor wires the vet. Without this pin, a lost line ships a canary
// that will dial whatever a Tower advertises - and every canary test would stay green,
// because the test fixtures build their brokers without buildBroker on purpose.
func TestBuildBrokerWiresTheCanaryVet(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	b := buildBroker(store.NewMem(), priv, 0.2, 0, time.Minute)
	require.NotNil(t, b.canaryVet, "production must never dial unvetted")
	require.Error(t, b.canaryVet(net.ParseIP("127.0.0.1")))
	require.NoError(t, b.canaryVet(net.ParseIP("93.184.216.34")))
}

func TestVetAndHostEdges(t *testing.T) {
	require.Error(t, vetPublicIP(net.ParseIP("ff02::1")), "IPv6 multicast")
	require.NoError(t, vetPublicIP(net.IP([]byte{1, 2})), "unparseable bytes hit no range and pass to the dialer's own failure")
	require.Equal(t, "", hostOf("no-port-here"))
	require.Equal(t, "10.0.0.5", hostOf("10.0.0.5:1"))
}

// The canary's pre-screen: a literal inward endpoint is SKIPPED - not probed, not failed.
func TestCanarySkipsALiteralInwardEndpoint(t *testing.T) {
	b, _ := towerTestBroker(t)
	b.canaryVet = vetPublicIP
	// canaryTargetFor demands MayTakeWork + a routable self- row; driving the full fixture
	// belongs to the sealed-canary suite. What must hold HERE is the predicate the skip
	// rides on, for the exact addresses a hostile Tower would advertise:
	for _, ep := range []string{"127.0.0.1:8444", "169.254.169.254:80", "[::1]:8444", "10.0.0.9:1"} {
		ip := net.ParseIP(hostOf(ep))
		require.NotNil(t, ip, ep)
		require.Error(t, b.canaryVet(ip), "the canary must never dial %s", ep)
	}
}

// The production notifier closure: with no ADMIN_EMAIL it logs rather than silently
// dropping the arrival; with recipients it walks them through the nil-safe mailer.
func TestBuildBrokerWiresThePendingNotifier(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	b := buildBroker(store.NewMem(), priv, 0.2, 0, time.Minute)
	require.NotNil(t, b.towerPending)

	b.adminEmails = nil
	b.towerPending.enrolled("owner-log", "tw-log") // the log branch

	b.adminEmails = []string{"a@example.com", "b@example.com"}
	b.towerPending.enrolled("owner-mail", "tw-mail") // the recipient loop, nil-safe mailer
}

// /admin/towers is the dashboard's read: every Tower, the waiting ones first, with what
// the approver needs and nothing they do not.
func TestAdminTowersListsTheQueue(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	older := enrolledTower(t, b, op.login)
	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+older.id+`","state":"active"}`)
	waiting := enrolledTower(t, b, op.login)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/towers", nil)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Towers []struct {
			TowerID  string `json:"tower_id"`
			Owner    string `json:"owner"`
			State    string `json:"state"`
			Enrolled string `json:"enrolled"`
			LinkLive bool   `json:"link_live"`
			Endpoint string `json:"endpoint"`
		} `json:"towers"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Towers, 2)
	require.Equal(t, waiting.id, out.Towers[0].TowerID, "the waiting Tower leads the queue")
	require.Equal(t, "quarantine", out.Towers[0].State)
	require.Equal(t, op.login, out.Towers[0].Owner)
	require.NotEmpty(t, out.Towers[0].Enrolled)
	require.Equal(t, "active", out.Towers[1].State)

	// And without the credential it does not exist.
	bare, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/towers", nil)
	r2, err := http.DefaultClient.Do(bare)
	require.NoError(t, err)
	r2.Body.Close()
	require.Equal(t, http.StatusForbidden, r2.StatusCode)
}

// Building the production mux must not panic - a route registered twice (once in main's
// table, once in registerTowerRoutes) compiles clean and kills the process at startup.
// This nearly shipped exactly that way.
func TestTheTowerRouteTableRegistersOnce(t *testing.T) {
	b, _ := towerTestBroker(t)
	require.NotPanics(t, func() {
		mux := http.NewServeMux()
		b.registerTowerRoutes(mux)
	})
}

// Unreachable-by-design must be recognisable wherever it surfaces - the pre-screen, the
// resolver path, and the dial-time backstop all wrap one sentinel.
func TestNotPublicIsOneSentinelEverywhere(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1"} {
		require.ErrorIs(t, vetPublicIP(net.ParseIP(ip)), errNotPublic, ip)
	}
	require.NoError(t, vetPublicIP(net.ParseIP("93.184.216.34")))

	// A hostname advert: resolved, vetted, and the sentinel survives the wrap.
	err := endpointNotPublic(context.Background(), "localhost:8444", vetPublicIP)
	require.ErrorIs(t, err, errNotPublic, "a LAN/loopback NAME is a design skip, not a failure")

	// A literal public address, and the nil-vet test seam, both pass clean.
	require.NoError(t, endpointNotPublic(context.Background(), "93.184.216.34:8444", vetPublicIP))
	require.NoError(t, endpointNotPublic(context.Background(), "localhost:8444", nil))

	// Unresolvable is NOT "not public": it may be broken, and broken is the canary's
	// business to discover and score.
	require.NoError(t, endpointNotPublic(context.Background(), "no-such-host.invalid:8444", vetPublicIP))
}

// The dial-time backstop's decision, tested directly: only the vet's own refusal is a
// design-skip; every other dial failure is a Tower that could not carry the work.
func TestCanaryDialVerdictSkipsOnlyDesignRefusals(t *testing.T) {
	require.True(t, isDesignSkip(fmt.Errorf("dial: %w", errNotPublic)),
		"a vet refusal at dial time is unreachable-by-design")
	require.False(t, isDesignSkip(nil),
		"a successful submit must NOT be read as a skip - the bug that scored healthy hubs as skip-fail")
	require.False(t, isDesignSkip(errors.New("connection refused")),
		"an ordinary transport error is a Tower that accepted work and dropped it")
	require.False(t, isDesignSkip(context.DeadlineExceeded))
}

// The bind wildcards: unreachable by design on the canary side too, so a Tower that
// somehow advertised one is skipped, never scored as failing.
func TestVetRefusesBindWildcards(t *testing.T) {
	require.ErrorIs(t, vetPublicIP(net.ParseIP("0.0.0.0")), errNotPublic)
	require.ErrorIs(t, vetPublicIP(net.ParseIP("::")), errNotPublic)
}
