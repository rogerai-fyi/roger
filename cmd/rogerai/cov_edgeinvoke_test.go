package main

// Coverage for `roger edge invoke` and `roger edge grant` as an owner types them: the refusals each
// flag and argument shape earns, the grant a command needs before anything is sent, and the real
// mutual-TLS dial against a real TLS server (pin checked, refusal bounded, reply decoded).

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// newFacedAuthority makes this machine the authority of a locally-rooted Edge, enrolled as name, with
// a LAN face recorded so a callee address resolves to something dialable.
func newFacedAuthority(t *testing.T, name string) string {
	t.Helper()
	useTempConfig(t)
	t.Setenv(edge.EnvDiscovery, "0")
	_, err := edgeDesignateCore(edgeAuthDir(), name)
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), name, "", "")
	require.NoError(t, err)
	setSelfFace(t, "127.0.0.1:44197", "abab", nil)
	return edgeThisNodeID()
}

// setSelfFace records a LAN face (and optionally instances) on this machine's own fleet node.
func setSelfFace(t *testing.T, addr, pin string, inst []store.EdgeInstance) {
	t.Helper()
	st, err := loadEdgeState()
	require.NoError(t, err)
	nodes, err := st.fleet.List()
	require.NoError(t, err)
	for _, n := range nodes {
		if n.ID == edgeThisNodeID() {
			n.Pin = pin
			n.Transports = []store.EdgeTransport{{Kind: "lan", Addr: addr, Fingerprint: pin}}
			if inst != nil {
				n.Instances = inst
			}
			require.NoError(t, st.db.UpdateEdgeNode(n))
		}
	}
	require.NoError(t, st.save())
}

func stubInvokeDial(t *testing.T, fn func(client.EdgePeer, string, json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error)) {
	t.Helper()
	prev := edgeDialInvokeTaskFn
	t.Cleanup(func() { edgeDialInvokeTaskFn = prev })
	edgeDialInvokeTaskFn = func(_ context.Context, p client.EdgePeer, _ *tls.Certificate, _ store.Grant, capability string, task json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		return fn(p, capability, task)
	}
}

func TestEdgeInvokeRefusesAMalformedCommandLine(t *testing.T) {
	useTempConfig(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"jetson", "hi", "--cap"}, "--cap needs a value"},
		{[]string{"jetson", "hi", "--model"}, "--model needs a value"},
		{[]string{"jetson", "hi", "--as"}, "--as needs a caller node id"},
		{[]string{"jetson"}, "usage: roger edge invoke"},
		{nil, "usage: roger edge invoke"},
	} {
		err := cmdEdgeInvoke(loadConfig(), tc.args)
		require.ErrorContains(t, err, tc.want, "%v", tc.args)
	}
}

func TestEdgeInvokeNamesTheGrantToIssueAndSendsNothingWithoutOne(t *testing.T) {
	newFacedAuthority(t, "jetson")
	sent := 0
	stubInvokeDial(t, func(client.EdgePeer, string, json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		sent++
		return edge.InvokeResult{}, protocol.UsageReceipt{}, nil
	})

	err := cmdEdgeInvoke(loadConfig(), []string{"nobody", "hello"})
	require.ErrorContains(t, err, `no such node on this Edge: "nobody"`)

	err = cmdEdgeInvoke(loadConfig(), []string{"jetson", "hello", "there"})
	require.ErrorContains(t, err, "no live grant lets")
	require.ErrorContains(t, err, "roger edge grant self operate jetson")

	err = cmdEdgeInvoke(loadConfig(), []string{"jetson", "hello", "--as", "n_someone", "--cap", "sense"})
	require.ErrorContains(t, err, "no live grant lets n_someone sense jetson")
	require.ErrorContains(t, err, "roger edge grant n_someone sense jetson", "an explicit caller is named in the hint")
	require.Zero(t, sent, "nothing is dialed without a grant")
}

func TestEdgeInvokePrintsTheReplyTheRefusalAndTheReceipt(t *testing.T) {
	newFacedAuthority(t, "jetson")
	_, _, err := edgeGrantIssueCore("self", "operate", "jetson", "1h", 0)
	require.NoError(t, err)

	var got edge.InvokeTask
	var peer client.EdgePeer
	reply := edge.InvokeResult{}
	rec := protocol.UsageReceipt{}
	stubInvokeDial(t, func(p client.EdgePeer, capability string, task json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		peer = p
		require.Equal(t, "operate", capability)
		require.NoError(t, json.Unmarshal(task, &got))
		return reply, rec, nil
	})

	text, _ := json.Marshal("all quiet on the logs")
	reply = edge.InvokeResult{Output: text}
	rec = protocol.UsageReceipt{RequestID: "einv_1", NodeID: "n_0123456789abcdef"}
	out, err := captureEdgeStdout(func() error {
		return cmdEdgeInvoke(loadConfig(), []string{"jetson", "summarize", "the", "logs", "--model", "tiny"})
	})
	require.NoError(t, err)
	require.Equal(t, edge.InvokeTask{Prompt: "summarize the logs", Model: "tiny"}, got)
	require.Equal(t, "127.0.0.1:44197", peer.Addr)
	require.Equal(t, "abab", peer.Pin)
	require.Contains(t, out, "all quiet on the logs\n")
	require.Contains(t, out, "(receipt einv_1 on n_01234567)")
	require.NotContains(t, out, "refused")

	// A non-string output is printed raw; a refusal is said; no receipt, no receipt line.
	reply = edge.InvokeResult{Refused: "the owner said no", Output: json.RawMessage(`{"n":1}`)}
	rec = protocol.UsageReceipt{}
	out, err = captureEdgeStdout(func() error { return cmdEdgeInvoke(loadConfig(), []string{"jetson", "go"}) })
	require.NoError(t, err)
	require.Contains(t, out, "refused: the owner said no")
	require.Contains(t, out, `{"n":1}`)
	require.NotContains(t, out, "receipt")

	// A dial error is the command's error.
	stubInvokeDial(t, func(client.EdgePeer, string, json.RawMessage) (edge.InvokeResult, protocol.UsageReceipt, error) {
		return edge.InvokeResult{}, protocol.UsageReceipt{}, fmt.Errorf("could not reach jetson: boom")
	})
	require.ErrorContains(t, cmdEdgeInvoke(loadConfig(), []string{"jetson", "go"}), "could not reach jetson")
}

func TestEdgeInvokeNeedsThisMachinesCertificate(t *testing.T) {
	newFacedAuthority(t, "jetson")
	_, _, err := edgeGrantIssueCore("self", "operate", "jetson", "1h", 0)
	require.NoError(t, err)
	// A caller named with --as on a grant that names no caller resolves, but this machine's identity is gone.
	require.NoError(t, edgeIdentityStore().ForgetIdentity())
	err = cmdEdgeInvoke(loadConfig(), []string{"jetson", "go", "--as", "n_any"})
	require.ErrorContains(t, err, "no Edge certificate to invoke with")
	require.ErrorContains(t, err, "roger edge enroll")
}

func TestEdgeResolveNodeFaceAddressesOneRoger(t *testing.T) {
	id := newFacedAuthority(t, "jetson")
	setSelfFace(t, "127.0.0.1:44197", "abab", []store.EdgeInstance{{Name: "roger", Port: 45001}, {Name: "old"}})

	p, got, err := edgeResolveNodeFace("./roger")
	require.NoError(t, err)
	require.Equal(t, id, got)
	require.Equal(t, "127.0.0.1:45001", p.Addr, "the instance's own face port is dialed")
	require.Equal(t, "roger", p.Instance)
	require.Equal(t, "abab", p.Pin, "same node, same certificate, same pin")

	p, _, err = edgeResolveNodeFace("self")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:44197", p.Addr)

	_, _, err = edgeResolveNodeFace("jetson/ghost")
	require.ErrorContains(t, err, `runs no roger called "ghost"`)
	_, _, err = edgeResolveNodeFace("jetson/old")
	require.ErrorContains(t, err, "reports no face of its own")

	setSelfFace(t, "not-an-address", "abab", nil)
	_, _, err = edgeResolveNodeFace("jetson/roger")
	require.ErrorContains(t, err, "cannot be read")

	setSelfFace(t, "", "", nil)
	_, _, err = edgeResolveNodeFace("jetson")
	require.ErrorContains(t, err, "has no LAN face to invoke")
}

// The real dial, against a real TLS server: the pin is checked before any byte is sent, a refusal is
// reported with its status, a reply is decoded, and an unreadable reply is said.
func TestEdgeDialInvokeTaskRealAgainstARealFace(t *testing.T) {
	var body edge.InvokeRequest
	status, reply := http.StatusOK, `{"output":"done"}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, edge.InvokePath, r.URL.Path)
		require.True(t, strings.HasPrefix(r.Header.Get("X-Roger-Request"), "einv_"))
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	defer srv.Close()
	leaf := srv.TLS.Certificates[0]
	pin := fmt.Sprintf("%x", sha256.Sum256(leaf.Certificate[0]))
	addr := strings.TrimPrefix(srv.URL, "https://")
	g := store.Grant{ID: "grant_x", Caps: []string{"operate"}}
	ctx := context.Background()

	res, _, err := edgeDialInvoke(ctx, client.EdgePeer{Addr: addr, Pin: pin, Node: "jetson"}, &leaf, g, "operate", "hi", "tiny")
	require.NoError(t, err)
	require.JSONEq(t, `"done"`, string(res.Output))
	require.Equal(t, "grant_x", body.Grant.ID)
	require.Equal(t, "operate", body.Capability)
	require.Len(t, body.Nonce, 32, "a fresh 16-byte nonce rides every request")
	require.NotZero(t, body.TS)

	status, reply = http.StatusForbidden, "  grant revoked  "
	_, _, err = edgeDialInvoke(ctx, client.EdgePeer{Addr: addr, Pin: pin, Node: "jetson"}, &leaf, g, "operate", "hi", "")
	require.EqualError(t, err, "jetson refused the invoke (403): grant revoked")

	status, reply = http.StatusOK, "not json"
	_, _, err = edgeDialInvoke(ctx, client.EdgePeer{Addr: addr, Pin: pin, Node: "jetson"}, &leaf, g, "operate", "hi", "")
	require.ErrorContains(t, err, "the callee's reply could not be read")

	_, _, err = edgeDialInvoke(ctx, client.EdgePeer{Addr: addr, Pin: "beef", Node: "jetson"}, &leaf, g, "operate", "hi", "")
	require.ErrorContains(t, err, "could not reach jetson")
	require.ErrorContains(t, err, "does not match its pin")

	srv.Close()
	_, _, err = edgeDialInvoke(ctx, client.EdgePeer{Addr: addr, Pin: pin, Node: "jetson"}, &leaf, g, "operate", "hi", "")
	require.ErrorContains(t, err, "could not reach jetson")
}

func TestEdgeGrantCommandIssuesListsAndRevokes(t *testing.T) {
	newFacedAuthority(t, "jetson")
	out, err := captureEdgeStdout(func() error { return cmdEdgeGrant(loadConfig(), nil) })
	require.NoError(t, err)
	require.Contains(t, out, "no edge grants issued yet")

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"self", "operate", "jetson", "--for"}, "--for needs a value"},
		{[]string{"self", "operate", "jetson", "--hops"}, "--hops needs a number"},
		{[]string{"self", "operate", "jetson", "--hops", "-1"}, "--hops wants a non-negative number"},
		{[]string{"self", "operate", "jetson", "--hops", "x"}, "--hops wants a non-negative number"},
		{[]string{"self", "operate"}, "usage: roger edge grant"},
		{[]string{"self", "operate", "jetson", "--for", "soon"}, `bad --for "soon"`},
		{[]string{"self", "teleport", "jetson"}, "is not an edge capability"},
		{[]string{"self", "operate", "ghost"}, "no such node"},
		{[]string{"ghost", "operate", "jetson"}, "no such node"},
		{[]string{"revoke"}, "usage: roger edge grant revoke <id>"},
		{[]string{"revoke", "grant_nope"}, `no such grant "grant_nope"`},
	} {
		err := cmdEdgeGrant(loadConfig(), tc.args)
		require.ErrorContains(t, err, tc.want, "%v", tc.args)
	}

	out, err = captureEdgeStdout(func() error {
		return cmdEdgeGrant(loadConfig(), []string{"self", "operate", ".", "--for", "7d", "--hops", "2"})
	})
	require.NoError(t, err)
	require.Contains(t, out, "issued grant_")
	require.Contains(t, out, "your own instances may operate . until")
	require.Contains(t, out, "any member verifies it with the public root alone")
	gs, err := edgeGrantStore().List()
	require.NoError(t, err)
	require.Len(t, gs, 1)
	require.Equal(t, 2, gs[0].MaxHops)
	require.True(t, gs[0].Self)

	out, err = captureEdgeStdout(func() error { return cmdEdgeGrant(loadConfig(), []string{"ls"}) })
	require.NoError(t, err)
	require.Contains(t, out, gs[0].ID+"  live")
	require.Contains(t, out, "your own instances may operate")

	out, err = captureEdgeStdout(func() error { return cmdEdgeGrant(loadConfig(), []string{"rm", gs[0].ID}) })
	require.NoError(t, err)
	require.Contains(t, out, "revoked "+gs[0].ID)
	require.Contains(t, out, "until the grant-CRL ships", "the reach of a revocation is said honestly")

	out, err = captureEdgeStdout(func() error { return cmdEdgeGrant(loadConfig(), []string{"list"}) })
	require.NoError(t, err)
	require.Contains(t, out, "REVOKED")

	// An expired grant reads as expired.
	g := gs[0]
	g.ID, g.Revoked, g.ExpiresAt = "grant_old", false, 1
	require.NoError(t, edgeGrantStore().Put(g))
	out, _ = captureEdgeStdout(func() error { return cmdEdgeGrantList() })
	require.Contains(t, out, "grant_old  expired")
}

func TestEdgeGrantIsIssuedOnlyWhereTheRootLives(t *testing.T) {
	useTempConfig(t)
	err := cmdEdgeGrant(loadConfig(), []string{"self", "operate", "jetson"})
	require.ErrorContains(t, err, "not the authority of a locally-rooted Edge")
	require.ErrorContains(t, err, "roger edge authority local")
}
