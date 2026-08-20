package main

// towertls_test.go is about the half-done change.
//
// A Tower advertises one data plane, and THREE parties are told where it is: the serving node
// (at attach), the edge consumer (at authorize), and Core's own canary (from the projection).
// Each of them used to build "http://" + endpoint for itself. Threading a TLS advertisement to
// one of them and not the others does not degrade gracefully - it leaves half the traffic
// dialling plaintext into a TLS listener, which is a total outage for that half, and it leaves
// the other half believing the whole path is protected when it is not.
//
// So every test here asserts that the SAME pin, from the SAME live session, reaches every party
// that is told the address.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// testHubPin is the shape a tower computes from its own certificate: hex sha256 over the
// SubjectPublicKeyInfo. Its VALUE is meaningless to Core, which never dials the hub with it and
// never checks it against anything - Core's whole job here is to carry the tower's own statement
// to the two parties that will.
func testHubPin() string { return strings.Repeat("ab", 32) }

// liveEdgeTowerTLS is liveEdgeTower with a hub that speaks TLS: the same enrolment and the same
// session, plus the certificate pin the operator's `--hub-tls` produced.
func liveEdgeTowerTLS(t *testing.T, b *broker, srv *httptest.Server, login, endpoint, pin string) linkTower {
	t.Helper()
	op := signedInOperator(t, b, login)
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(), RelayEndpoint: endpoint, RelayTLSSPKI: pin,
	}), &acc)
	require.Equal(t, http.StatusOK, code, raw)
	return lt
}

// THE NODE'S LEG. A self-attaching node is told where to poll; it must be told what will answer
// there in the same breath, or it polls a TLS listener over http forever and never earns.
func TestSelfAttachHandsTheNodeTheHubCertificatePin(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTowerTLS(t, b, srv, "tower-op", "203.0.113.9:8443", testHubPin())

	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	var out struct {
		Endpoint        string `json:"endpoint"`
		EndpointTLSSPKI string `json:"endpoint_tls_spki"`
	}
	code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, "203.0.113.9:8443", out.Endpoint)
	require.Equal(t, testHubPin(), out.EndpointTLSSPKI,
		"the node cannot verify the hub Core sent it to without the pin Core was given")

	// THE RETRY ANSWER TOO. A node that lost the first reply is the same node; an answer that
	// came back without the pin would send it to the same TLS listener in plaintext, and this
	// branch exists precisely to rescue that caller.
	var retry struct {
		EndpointTLSSPKI string `json:"endpoint_tls_spki"`
		Note            string `json:"note"`
	}
	code, raw = node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &retry)
	require.Equal(t, http.StatusOK, code, raw)
	require.Contains(t, retry.Note, "already attached")
	require.Equal(t, testHubPin(), retry.EndpointTLSSPKI)
}

// THE CONSUMER'S LEG, AND THE PROJECTION UNDERNEATH IT. The consumer submits sealed work to the
// same hub the node polls, through a different Core route reading a different store, so this is
// the half most likely to be left behind.
func TestAuthorizeHandsTheConsumerTheSameHubCertificatePin(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTowerTLS(t, b, srv, "tower-op", "203.0.113.9:8443", testHubPin())

	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	body["model"] = "my-model"
	var attached struct {
		StationID string `json:"station_id"`
	}
	code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &attached)
	require.Equal(t, http.StatusOK, code, raw)

	// The projection is where the consumer's answer and the canary's target both come from.
	rows, err := b.tower.routable.Candidates("my-model", time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "203.0.113.9:8443", rows[0].Endpoint)
	require.Equal(t, testHubPin(), rows[0].TLSSPKI,
		"a row carrying an address with no pin is what sends a canary plaintext into a TLS hub")

	consumer := signedInConsumer(t, b)
	code, out := consumerCall(t, srv, consumer, "/tower/edge/authorize",
		map[string]any{"model": "my-model", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, "203.0.113.9:8443", out["endpoint"])
	require.Equal(t, testHubPin(), out["endpoint_tls_spki"],
		"the consumer submits sealed work to the same hub and must verify the same certificate")
}

// A TOWER THAT HAS NOT TURNED TLS ON IS UNCHANGED, and this is the compatibility claim made
// concrete rather than asserted in a comment: no pin, empty field, plaintext, exactly the
// behaviour every relay in the fleet has today. Making TLS mandatory is a separate decision with
// a migration cost, and it is not taken here.
func TestAPlaintextTowerIsAdvertisedExactlyAsItAlwaysWas(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	var out struct {
		Endpoint        string `json:"endpoint"`
		EndpointTLSSPKI string `json:"endpoint_tls_spki"`
	}
	code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, "203.0.113.9:8443", out.Endpoint)
	require.Empty(t, out.EndpointTLSSPKI, "no pin means plaintext means what this fleet does today")
}
