package towerjoin

// hub_test.go covers the joined Tower's hub-plane client, which had NO tests at all: every
// function in hub.go, plus SetOwnState and FetchEarnings, measured 0.0%.
//
// That is not an even spread of missing coverage. SettleEdgeReceipt is the call that turns a
// node's signed receipt into money, and its whole job is deciding which Core answers mean
// "stop" and which mean "try again" - a courier that retries a permanent refusal hammers
// Core forever, and one that abandons a transient failure silently drops somebody's pay.
// Neither branch had ever been executed.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// hubCore is a stub Core for the hub plane: it records what was sent and replies with
// whatever the test wants.
type hubCore struct {
	srv     *httptest.Server
	seen    []string
	bodies  map[string]map[string]any
	pubkeys map[string]string
	replies map[string]func(w http.ResponseWriter)
}

func newHubCore(t *testing.T) *hubCore {
	t.Helper()
	c := &hubCore{
		bodies:  map[string]map[string]any{},
		pubkeys: map[string]string{},
		replies: map[string]func(http.ResponseWriter){},
	}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.seen = append(c.seen, r.URL.Path)
		c.pubkeys[r.URL.Path] = r.Header.Get("X-Roger-Pubkey")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.bodies[r.URL.Path] = body
		if fn, ok := c.replies[r.URL.Path]; ok {
			fn(w)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(c.srv.Close)
	t.Setenv("ROGER_BROKER", c.srv.URL)
	return c
}

// --- the dispatch key ---------------------------------------------------------
//
// This key is what consumer-submitted grants are verified against, so a Tower that accepts
// a malformed or wrong-length one authorizes work against a key nobody holds. The parsing
// here is the only thing standing between a bad response and that state.

func TestDispatchKeyIsReturnedWhenItIsAKey(t *testing.T) {
	core := newHubCore(t)
	want := make([]byte, 32)
	for i := range want {
		want[i] = byte(i)
	}
	core.replies["/tower/dispatch/key"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"dispatch_key":"` + hex.EncodeToString(want) + `"}`))
	}
	got, err := DispatchKey()
	require.NoError(t, err)
	require.Equal(t, want, []byte(got))
}

func TestDispatchKeyRefusesAnythingThatIsNotOne(t *testing.T) {
	// Each of these would otherwise be pinned for the lifetime of the serve and used to
	// verify every grant. A wrong-LENGTH key is the dangerous one: it is valid hex, so a
	// check that only decoded would accept it.
	for name, reply := range map[string]string{
		"not hex":       `{"dispatch_key":"zzzz"}`,
		"short":         `{"dispatch_key":"00112233"}`,
		"long":          `{"dispatch_key":"` + hex.EncodeToString(make([]byte, 33)) + `"}`,
		"absent":        `{}`,
		"not an object": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			core := newHubCore(t)
			core.replies["/tower/dispatch/key"] = func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(reply))
			}
			_, err := DispatchKey()
			require.Error(t, err)
		})
	}
}

func TestDispatchKeyReportsCoreRefusing(t *testing.T) {
	core := newHubCore(t)
	core.replies["/tower/dispatch/key"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`nope`))
	}
	_, err := DispatchKey()
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
}

// --- the fleet the hub serves --------------------------------------------------

func TestHubNodesCarryTheKeyTheHubVerifiesPollsAgainst(t *testing.T) {
	core := newHubCore(t)
	st := registeredTower(t)
	core.replies["/tower/hub/nodes"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"nodes":[
			{"station_id":"st-1","assertion_key":"aa","hub_token":"legacy-1","state":"active"},
			{"station_id":"st-2","assertion_key":"bb","state":"quarantine"}]}`))
	}
	got, err := HubNodes(st)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "st-1", got[0].StationID)
	require.Equal(t, "aa", got[0].AssertionKey)
	require.Equal(t, "legacy-1", got[0].HubToken)
	require.Equal(t, "quarantine", got[1].State)
	// Core scopes the answer to the tower that signed, so the id has to be in the body.
	// The ADMISSION id, never st.TowerID. Those are two different identifiers: st.TowerID is
	// the local identity minted by `init` before this Tower had ever heard of Core, and the
	// admission id is what Core admitted it as. Core matches the caller on the admission id,
	// so sending the local one is refused - and this line used to pin the refused value.
	require.Equal(t, "tw-1", core.bodies["/tower/hub/nodes"]["tower_id"])
	require.NotEmpty(t, core.pubkeys["/tower/hub/nodes"])
}

// --- settlement, and the three answers that mean different things ----------------

func TestSettleForwardsTheReceiptAndItsOwnByteCounts(t *testing.T) {
	core := newHubCore(t)
	st := registeredTower(t)
	core.replies["/tower/edge/settle"] = func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }

	require.NoError(t, SettleEdgeReceipt(st, "st-1", "att-1", []byte("receipt-bytes"), 111, 222))
	body := core.bodies["/tower/edge/settle"]
	require.Equal(t, "st-1", body["station_id"])
	require.Equal(t, "att-1", body["attempt_id"])
	require.Equal(t, float64(111), body["wire_in"])
	require.Equal(t, float64(222), body["wire_out"])
	// The receipt is opaque to the tower and must cross intact - Core verifies it against
	// the station's own key, so any re-encoding here would invalidate somebody's pay.
	raw, err := base64.StdEncoding.DecodeString(body["receipt"].(string))
	require.NoError(t, err)
	require.Equal(t, []byte("receipt-bytes"), raw)
}

func TestSettleTreatsAlreadySettledAsDone(t *testing.T) {
	// 409 is a retry or a race, and both are fine: the money already moved. Reporting it as
	// an error would make a courier retry forever against the one answer that means success.
	core := newHubCore(t)
	st := registeredTower(t)
	core.replies["/tower/edge/settle"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"message":"already settled"}}`))
	}
	require.NoError(t, SettleEdgeReceipt(st, "st-1", "att-1", []byte("r"), 0, 0))
}

func TestSettleMarksAJudgedRefusalPermanent(t *testing.T) {
	// A 4xx that is not 409 is Core saying the receipt is invalid. Retrying cannot change
	// that answer, so the courier has to be able to tell this apart and abandon loudly.
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity} {
		core := newHubCore(t)
		st := registeredTower(t)
		core.replies["/tower/edge/settle"] = func(w http.ResponseWriter) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":{"message":"bad receipt"}}`))
		}
		err := SettleEdgeReceipt(st, "st-1", "att-1", []byte("r"), 0, 0)
		require.Error(t, err, "code %d", code)
		require.ErrorIs(t, err, ErrSettlePermanent, "code %d must be permanent", code)
	}
}

func TestSettleLeavesATransientFailureRetryable(t *testing.T) {
	// The other half of the same decision, and the one that costs somebody money if it is
	// wrong: a 5xx or an unreachable Core is not a judgement about the receipt, so it must
	// NOT be marked permanent or the courier drops a valid receipt on a blip.
	for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		core := newHubCore(t)
		st := registeredTower(t)
		core.replies["/tower/edge/settle"] = func(w http.ResponseWriter) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":{"message":"try later"}}`))
		}
		err := SettleEdgeReceipt(st, "st-1", "att-1", []byte("r"), 0, 0)
		require.Error(t, err, "code %d", code)
		require.False(t, errors.Is(err, ErrSettlePermanent),
			"code %d is transient and must stay retryable", code)
	}
}

// --- audits ----------------------------------------------------------------------

func TestWantedAuditsReadsWhatCoreAsksFor(t *testing.T) {
	core := newHubCore(t)
	st := registeredTower(t)
	core.replies["/tower/audit/wanted"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"wanted":[{"attempt_id":"att-1","station_id":"st-1"}]}`))
	}
	got, err := WantedAudits(st)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "att-1", got[0].AttemptID)
	require.Equal(t, "st-1", got[0].StationID)
	require.Equal(t, "tw-1", core.bodies["/tower/audit/wanted"]["tower_id"], "the admission id, never the local init id")
}

func TestForwardingATranscriptCarriesEveryPart(t *testing.T) {
	core := newHubCore(t)
	st := registeredTower(t)
	core.replies["/tower/audit/transcript"] = func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }

	require.NoError(t, ForwardAuditTranscript(st, "att-1", true, "sealed", "transcript", "req", "resp"))
	body := core.bodies["/tower/audit/transcript"]
	require.Equal(t, "att-1", body["attempt_id"])
	require.Equal(t, true, body["available"])
	require.Equal(t, "sealed", body["sealed_bundle"])
	require.Equal(t, "transcript", body["transcript"])
	require.Equal(t, "req", body["request"])
	require.Equal(t, "resp", body["response"])
}

func TestForwardingAnUnavailableTranscriptIsStillReported(t *testing.T) {
	// "the node could not produce it" is an ANSWER, not a failure to answer - silence would
	// leave Core waiting on an audit that is never coming.
	core := newHubCore(t)
	st := registeredTower(t)
	core.replies["/tower/audit/transcript"] = func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }

	require.NoError(t, ForwardAuditTranscript(st, "att-2", false, "", "", "", ""))
	require.Equal(t, false, core.bodies["/tower/audit/transcript"]["available"])
}

// --- lifecycle -------------------------------------------------------------------

func TestSetOwnStateIsSignedByTheAccount(t *testing.T) {
	core := newHubCore(t)
	st := registeredTower(t)
	core.replies["/tower/self/lifecycle"] = func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }

	require.NoError(t, SetOwnState(st, "drain"))
	require.Equal(t, "drain", core.bodies["/tower/self/lifecycle"]["state"])
	require.Equal(t, "tw-1", core.bodies["/tower/self/lifecycle"]["tower_id"])
	// Signed by the ACCOUNT rather than the Tower on purpose: retiring hardware has to work
	// when the Tower is the thing that has gone wrong.
	require.NotEmpty(t, core.pubkeys["/tower/self/lifecycle"])
}

func TestSetOwnStateRefusesBeforeRegistration(t *testing.T) {
	core := newHubCore(t)
	st := joinedTower(t)
	err := SetOwnState(st, "drain")
	require.Error(t, err)
	require.Contains(t, err.Error(), "register")
	require.Empty(t, core.seen, "an unregistered Tower must not reach Core at all")
}

// --- earnings ---------------------------------------------------------------------

func TestEarningsDistinguishAnAbsentSplitFromAZeroOne(t *testing.T) {
	// This is the reason FetchEarnings decodes twice, and it was never exercised. "Core
	// could not read the rollup" and "you have relayed nothing" are different facts, and
	// showing an operator a confident 0.00 for the second when it is the first is the kind
	// of wrong number that gets believed.
	t.Run("absent", func(t *testing.T) {
		core := newHubCore(t)
		core.replies["/tower/earnings/owed"] = func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"unit":"credits","held":1.5,"payable":2.5,"paid":3}`))
		}
		got, err := FetchEarnings()
		require.NoError(t, err)
		require.False(t, got.SplitKnown, "no from_relaying key means UNKNOWN, not zero")
		require.Equal(t, 1.5, got.Held)
		require.Equal(t, 2.5, got.Payable)
		require.Equal(t, float64(3), got.Paid)
	})

	t.Run("present and zero", func(t *testing.T) {
		core := newHubCore(t)
		core.replies["/tower/earnings/owed"] = func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"unit":"credits","from_relaying":0,"from_serving":0}`))
		}
		got, err := FetchEarnings()
		require.NoError(t, err)
		require.True(t, got.SplitKnown, "an explicit 0 is a known zero")
		require.Equal(t, float64(0), got.FromRelaying)
	})

	t.Run("present and non-zero", func(t *testing.T) {
		core := newHubCore(t)
		core.replies["/tower/earnings/owed"] = func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"unit":"credits","from_relaying":4.25,"from_serving":6.5,"attempts":9,"cash_out":"https://x/y"}`))
		}
		got, err := FetchEarnings()
		require.NoError(t, err)
		require.True(t, got.SplitKnown)
		require.Equal(t, 4.25, got.FromRelaying)
		require.Equal(t, 6.5, got.FromServing)
		require.Equal(t, int64(9), got.Attempts)
		require.Equal(t, "https://x/y", got.CashOut)
	})
}

func TestEarningsReportCoreRefusing(t *testing.T) {
	core := newHubCore(t)
	core.replies["/tower/earnings/owed"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"sign in first"}}`))
	}
	_, err := FetchEarnings()
	require.Error(t, err)
	require.Contains(t, err.Error(), "sign in first")
}

// --- what these calls do when Core says no --------------------------------------
//
// Each of these returns the error rather than an empty-but-successful result. The
// distinction matters at the call sites: an empty node list means "serve nobody", and a
// hub that treats a failed fetch as an empty fleet stops serving everyone it had.

func TestHubPlaneReadsReportRefusalsRatherThanReturningNothing(t *testing.T) {
	cases := map[string]struct {
		path string
		call func(t *testing.T) error
	}{
		"hub nodes": {"/tower/hub/nodes", func(t *testing.T) error {
			st := registeredTower(t)
			nodes, err := HubNodes(st)
			require.Nil(t, nodes, "a refusal must not look like an empty fleet")
			return err
		}},
		"wanted audits": {"/tower/audit/wanted", func(t *testing.T) error {
			st := registeredTower(t)
			wanted, err := WantedAudits(st)
			require.Nil(t, wanted, "a refusal must not look like nothing to audit")
			return err
		}},
		"forward transcript": {"/tower/audit/transcript", func(t *testing.T) error {
			st := registeredTower(t)
			return ForwardAuditTranscript(st, "att-1", true, "s", "t", "q", "r")
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			core := newHubCore(t)
			core.replies[c.path] = func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"message":"this tower is suspended"}}`))
			}
			err := c.call(t)
			require.Error(t, err)
			require.Contains(t, err.Error(), "suspended")
		})
	}
}

func TestDispatchKeyRefusesAnUntrustedBase(t *testing.T) {
	// The comment on DispatchKey is explicit that the transport delivering this key must be
	// trusted, because a forged key verifies every attacker-signed grant. Plain http to a
	// non-loopback host is exactly that untrusted transport.
	t.Setenv("ROGER_BROKER", "http://broker.example.com")
	_, err := DispatchKey()
	require.Error(t, err)
}

// --- the signed GET's own failure paths -------------------------------------------
//
// FetchStatus is the only caller of signedGet, so these reach branches nothing else does.
// They matter because this is the call an operator makes when something is already wrong,
// and an unhelpful answer here is an unhelpful answer at the worst moment.

func TestStatusReportsABareStatusWhenThereIsNoErrorEnvelope(t *testing.T) {
	// Not every refusal carries the {"error":{"message":...}} shape - a proxy, a WAF or a
	// load balancer answers with its own body. The operator still has to be told something,
	// and the status code is the only fact available.
	core := newHubCore(t)
	st := registeredTower(t)
	core.replies["/tower/status"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html>upstream is down</html>`))
	}
	_, err := FetchStatus(st)
	require.Error(t, err)
	require.Contains(t, err.Error(), "502")
}

func TestStatusSaysItCouldNotReachCore(t *testing.T) {
	// Transport failure is not an answer about the fleet, and must not be reported as one.
	core := newHubCore(t)
	st := registeredTower(t)
	core.srv.Close() // nothing is listening now
	_, err := FetchStatus(st)
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not reach")
}

func TestStatusRefusesAnUntrustedBase(t *testing.T) {
	registeredTower(t)
	t.Setenv("ROGER_BROKER", "http://broker.example.com")
	st := registeredTower(t)
	_, err := FetchStatus(st)
	require.Error(t, err)
}

func TestEarningsRejectAnAnswerItCannotRead(t *testing.T) {
	// The double decode means a type mismatch surfaces at the SECOND unmarshal, after the
	// raw map has already parsed happily. Reporting an error is the only safe answer: the
	// alternative is showing an operator a zero balance because a field arrived as a string.
	core := newHubCore(t)
	core.replies["/tower/earnings/owed"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"unit":"credits","held":"not-a-number"}`))
	}
	_, err := FetchEarnings()
	require.Error(t, err)
}

// Every call the hub plane makes to Core names this Tower by the id CORE knows - the
// admission id. A live v6.0.0 Tower held its link (link.go already used the admission id)
// while every hub-plane call was refused with "requires the Tower's own signed request",
// because they sent the local init id instead. Nodes could not be listed, receipts could
// not be settled, audits could not be answered: a Tower on the network, earning nothing.
func TestEveryHubPlaneCallNamesTheTowerByItsAdmissionID(t *testing.T) {
	core := newHubCore(t)
	st := registeredTower(t)

	_, _ = HubNodes(st)
	_ = SettleEdgeReceipt(st, "st-1", "at-1", []byte("r"), 1, 1)
	_, _ = WantedAudits(st)
	_ = ForwardAuditTranscript(st, "at-1", true, "", "", "", "")

	for _, path := range []string{"/tower/hub/nodes", "/tower/edge/settle", "/tower/audit/wanted", "/tower/audit/transcript"} {
		body, seen := core.bodies[path]
		require.True(t, seen, "%s was never called", path)
		require.Equal(t, "tw-1", body["tower_id"], "%s must name the Tower by its admission id", path)
		require.NotEqual(t, st.TowerID, body["tower_id"], "%s sent the LOCAL id, which Core refuses", path)
	}
}

// Before registration there is no admission id, and the hub plane must say so rather than
// fall back to the local id and be refused with a message that points at signing.
func TestHubPlaneCallsRefuseBeforeRegistration(t *testing.T) {
	core := newHubCore(t)
	st := joinedTower(t)

	// Every hub-plane call, not only the first one somebody thinks of: a single call that
	// fell back to the local id would be refused by Core with a message about signing.
	_, err := HubNodes(st)
	require.ErrorContains(t, err, "register")
	err = SettleEdgeReceipt(st, "st-1", "at-1", []byte("r"), 1, 1)
	require.ErrorContains(t, err, "register")
	_, err = WantedAudits(st)
	require.ErrorContains(t, err, "register")
	err = ForwardAuditTranscript(st, "at-1", true, "", "", "", "")
	require.ErrorContains(t, err, "register")
	require.Empty(t, core.seen, "nothing is sent to Core before this Tower is registered")
}
