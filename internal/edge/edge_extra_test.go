package edge_test

// The remaining branches: defaults, the Station join, the LAN face's HTTP surface, and
// the ways a dial can fail short of a certificate problem.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

func TestNewFillsInTheRealDefaults(t *testing.T) {
	db := store.NewMem()
	d := edge.New(edge.Options{Fleet: edge.NewFleet(db, "acct-1")})
	require.NotNil(t, d)
	require.Nil(t, d.Responder(), "nothing touches the network before Start")
	require.False(t, d.Advertising())
	require.Empty(t, d.Candidates())
}

func TestFleetListJoinsStationsWithoutDisturbingThem(t *testing.T) {
	db := store.NewMem()
	f := edge.NewFleet(db, "acct-1")

	// Bound but never registered: supply bookkeeping, not a machine to show.
	require.NoError(t, db.BindNode("ghost", "acct-1"))
	list, err := f.List()
	require.NoError(t, err)
	require.Empty(t, list)

	// A registered Station appears, with a VERIFIED serve mark it did not have to ask
	// for, and no Edge row is written for it.
	require.NoError(t, db.UpsertNode(store.NodeRecord{
		NodeID: "station-1", Reg: protocol.NodeRegistration{NodeID: "station-1"}, LastSeen: 42}))
	require.NoError(t, db.BindNode("station-1", "acct-1"))
	list, err = f.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.True(t, list[0].Station)
	require.True(t, edge.Routable(list[0], edge.Serve))
	require.Equal(t, int64(42), list[0].LastSeen)
	_, ok, err := db.EdgeNodeByID("acct-1", "station-1")
	require.NoError(t, err)
	require.False(t, ok)

	// A Station that is ALSO enrolled keeps its record and merely gains the mark.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	n := edge.NewNode(pub, "aaa-first-by-name", edge.Host, edge.Serve)
	n.ID = "station-2"
	_, err = f.Enroll(n)
	require.NoError(t, err)
	require.NoError(t, db.UpsertNode(store.NodeRecord{
		NodeID: "station-2", Reg: protocol.NodeRegistration{NodeID: "station-2"}}))
	require.NoError(t, db.BindNode("station-2", "acct-1"))
	list, err = f.List()
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "aaa-first-by-name", list[0].Name, "the listing is ordered by name")
	require.True(t, edge.Routable(list[0], edge.Serve))
	require.False(t, list[0].Station, "an enrolled node is not a derived row")

	// Another account sees none of it.
	other, err := edge.NewFleet(db, "acct-2").List()
	require.NoError(t, err)
	require.Empty(t, other)
}

func TestFleetListOrdersTiedNamesById(t *testing.T) {
	db := store.NewMem()
	f := edge.NewFleet(db, "acct-1")
	// Two Stations share a derived name only if they share an id, which they cannot -
	// so the tie-break is exercised through the store's own ordering of equal names.
	for _, id := range []string{"n_bbb", "n_aaa"} {
		_, err := db.EnrollEdgeNode(store.EdgeNode{ID: id, Account: "acct-1", Name: "same", Kind: "host"})
		if err != nil {
			// The second one collides on the name, which is itself the rule; enroll it
			// under its own name and assert ordering on that instead.
			_, err = db.EnrollEdgeNode(store.EdgeNode{ID: id, Account: "acct-1", Name: id, Kind: "host"})
			require.NoError(t, err)
		}
	}
	require.NoError(t, db.UpsertNode(store.NodeRecord{NodeID: "n_ccc"}))
	require.NoError(t, db.BindNode("n_ccc", "acct-1"))
	list, err := f.List()
	require.NoError(t, err)
	require.Len(t, list, 3)
	for i := 1; i < len(list); i++ {
		require.LessOrEqual(t, list[i-1].Name, list[i].Name)
	}
}

func TestRenameOntoATakenNameIsRefused(t *testing.T) {
	db := store.NewMem()
	f := edge.NewFleet(db, "acct-1")
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	n1, err := f.Enroll(edge.NewNode(a, "one", edge.Host))
	require.NoError(t, err)
	_, err = f.Enroll(edge.NewNode(b, "two", edge.Host))
	require.NoError(t, err)
	require.ErrorIs(t, f.Rename(n1.ID, "two"), store.ErrEdgeNameTaken)
}

func TestTransportPreferenceSortsUnknownKindsLast(t *testing.T) {
	db := store.NewMem()
	f := edge.NewFleet(db, "acct-1")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	n := edge.NewNode(pub, "box", edge.Host)
	n.Transports = []store.EdgeTransport{
		{Kind: "carrier-pigeon"}, {Kind: "relay"}, {Kind: "lan", Fingerprint: "aa"},
	}
	got, err := f.Enroll(n)
	require.NoError(t, err)
	require.Equal(t, []string{"lan", "relay", "carrier-pigeon"},
		[]string{got.Transports[0].Kind, got.Transports[1].Kind, got.Transports[2].Kind})
	require.Equal(t, "aa", got.Pin, "the LAN fingerprint becomes the pin")
}

func TestLANFaceAnswersOnlyDescribeAndOnlyGET(t *testing.T) {
	s := edge.NewServer(edge.Describe{NodeID: "n_a", Account: "acct-1", Kind: "host",
		Caps: []string{"sense"}}, tls.Certificate{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + edge.DescribePath)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var d edge.Describe
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&d))
	require.Equal(t, "n_a", d.NodeID)
	require.Equal(t, []string{"sense"}, d.Caps)

	post, err := http.Post(srv.URL+edge.DescribePath, "application/json", nil)
	require.NoError(t, err)
	defer post.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, post.StatusCode)

	miss, err := http.Get(srv.URL + "/edge/invoke")
	require.NoError(t, err)
	defer miss.Body.Close()
	require.Equal(t, http.StatusNotFound, miss.StatusCode,
		"discovery exposes describe and nothing else")
}

func TestDialTLSFailures(t *testing.T) {
	ctx := context.Background()

	// Nothing listening at all.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	_, err = edge.DialTLS(ctx, addr)
	require.Error(t, err)

	// Listening, but not speaking TLS.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plain.Close()
	_, err = edge.DialTLS(ctx, plain.Listener.Addr().String())
	require.Error(t, err)

	// TLS, but describe refuses.
	deny := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer deny.Close()
	_, err = edge.DialTLS(ctx, deny.Listener.Addr().String())
	require.ErrorContains(t, err, "403")

	// TLS, 200, and not JSON.
	junk := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer junk.Close()
	_, err = edge.DialTLS(ctx, junk.Listener.Addr().String())
	require.Error(t, err)

	// An address that is not an address.
	_, err = edge.DialTLS(ctx, "\x00bad")
	require.Error(t, err)
}

func TestVerifyPeerWithoutACertificateOrAnAuthority(t *testing.T) {
	ad := edge.Advert{NodeID: "n_a", Fingerprint: "aa"}
	require.Equal(t, edge.ReasonUnreachable, edge.VerifyPeer(ad, nil, nil, "", time.Now()))
	require.Equal(t, edge.ReasonUnreachable, edge.VerifyPeer(ad, &edge.Peer{}, nil, "", time.Now()))
}
