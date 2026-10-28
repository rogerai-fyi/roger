package edge

// A member learns what every other member is called from the name each one enrolled under, which a
// member states in its describe answer. It is a hint: an owner's own rename on this machine wins, and
// a name another node here already holds is not taken. Until a hint arrives the record keeps its id
// and surfaces call it by its hardware word (found 2026-10-10: a member listed the Pis as "Pi 5 b4a3"
// while the authority called them pi-1).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/cert"
)

type namedPeer struct {
	id, fp string
	d      *Discovery
	advert Advert
}

func peerNamed(t *testing.T, f *Fleet, name string) namedPeer {
	t.Helper()
	authority, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	id := NodeID(pub)
	leaf, err := authority.Issue(id, pub)
	require.NoError(t, err)
	fp := Fingerprint(leaf.Raw)
	d := New(Options{
		Fleet: f, Authority: authority, CallerCert: &tls.Certificate{},
		Self: Advert{NodeID: "n_self", Account: "acct-1"},
		Dial: func(context.Context, string) (*Peer, error) {
			return &Peer{Cert: leaf, Fingerprint: fp, Describe: Describe{NodeID: id, Name: name, Account: "acct-1", Kind: string(Host)}}, nil
		},
	})
	return namedPeer{id: id, fp: fp, d: d, advert: Advert{NodeID: id, Account: "acct-1", Kind: string(Host), IP: net.ParseIP("10.0.0.5"), Port: 44100, Fingerprint: fp}}
}

func (p namedPeer) see(t *testing.T, f *Fleet) store.EdgeNode {
	t.Helper()
	var rep Report
	p.d.consider(context.Background(), p.advert, &rep)
	require.Len(t, rep.Verified, 1)
	n, ok, err := f.Get(p.id)
	require.NoError(t, err)
	require.True(t, ok)
	return n
}

func TestAMemberIsListedUnderTheNameItEnrolledUnder(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	require.Equal(t, "pi-1", peerNamed(t, f, "pi-1").see(t, f).Name)
}

func TestAnUnnamedRecordTakesTheEnrolledNameOnTheNextSighting(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	p := peerNamed(t, f, "")
	require.Equal(t, p.id, p.see(t, f).Name, "no name stated: the record keeps its id")
	p2 := p
	p2.d = New(Options{Fleet: f, Authority: p.d.opts.Authority, CallerCert: &tls.Certificate{},
		Self: Advert{NodeID: "n_self", Account: "acct-1"},
		Dial: func(ctx context.Context, addr string) (*Peer, error) {
			peer, err := p.d.opts.Dial(ctx, addr)
			if err == nil {
				peer.Describe.Name = "pi-1"
			}
			return peer, err
		}})
	require.Equal(t, "pi-1", p2.see(t, f).Name, "a record never named here takes the name the member enrolled under")
}

func TestTheOwnersRenameOnThisMachineWins(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	p := peerNamed(t, f, "pi-1")
	p.see(t, f)
	require.NoError(t, f.Rename(p.id, "garage"))
	require.Equal(t, "garage", p.see(t, f).Name)
}

func TestANameAnotherNodeHoldsIsNotTaken(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	first := peerNamed(t, f, "pi-1")
	first.see(t, f)
	second := peerNamed(t, f, "pi-1")
	require.Equal(t, second.id, second.see(t, f).Name, "a second node claiming a held name keeps its id")
}

func TestAnIDShapedOrInvalidNameIsIgnored(t *testing.T) {
	f := NewFleet(store.NewMem(), "acct-1")
	p := peerNamed(t, f, "n_0123456789abcdef0123456789abcdef")
	require.Equal(t, p.id, p.see(t, f).Name)
	q := peerNamed(t, f, "bad name with spaces!!")
	require.Equal(t, q.id, q.see(t, f).Name)
}
