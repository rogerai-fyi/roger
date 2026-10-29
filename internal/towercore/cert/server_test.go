package cert

// The authority's server certificate (server.go): ServerAuth only, the authority's node id and LAN
// addresses, leaf -> root, never usable as a member; and the golden fixture the Roger app evaluates
// its trust against (testdata/authority_server_fixture.json).

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"testing"
	"time"
)

const fixtureNode = "n_21fe31dfa154a261626bf854046fd2271b7bed4b6abe45aa"

func TestTheServerCertificateIsTheAuthoritysAndOnlyThat(t *testing.T) {
	a, err := NewAuthority(Config{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ips := []net.IP{net.ParseIP("192.168.1.69"), net.ParseIP("192.168.1.183")}
	leaf, key, err := a.IssueServer(fixtureNode, ips, ServerCertTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || key == nil {
		t.Fatal("the server key is P-256")
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("EKU %v", leaf.ExtKeyUsage)
	}
	if got := leaf.NotAfter.Sub(leaf.NotBefore); got > ServerCertTTL+2*time.Minute {
		t.Fatalf("lives %v", got)
	}
	now := time.Now()
	if err := VerifyServer(a.Root(), leaf, fixtureNode, ips[1], now); err != nil {
		t.Fatal(err)
	}
	if VerifyServer(a.Root(), leaf, fixtureNode, net.ParseIP("192.168.1.70"), now) == nil {
		t.Fatal("an address it does not name passed")
	}
	if VerifyServer(a.Root(), leaf, "n_other", ips[0], now) == nil {
		t.Fatal("another node id passed")
	}
	if _, err := a.Authenticate(leaf); err == nil {
		t.Fatal("a server certificate authenticated as a member")
	}
	// And no member certificate can serve as the authority.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	member, err := a.Issue(fixtureNode, pub)
	if err != nil {
		t.Fatal(err)
	}
	member.IPAddresses = ips
	if VerifyServer(a.Root(), member, fixtureNode, ips[0], now) == nil {
		t.Fatal("a member certificate passed as the authority's server certificate")
	}
	// Another Edge's root refuses it.
	other, _ := NewAuthority(Config{TTL: time.Hour})
	if VerifyServer(other.Root(), leaf, fixtureNode, ips[0], now) == nil {
		t.Fatal("another root accepted it")
	}
	if _, _, err := NewVerifierMust(t, a).IssueServer(fixtureNode, ips, 0); err == nil {
		t.Fatal("a verify-only authority issued")
	}
}

func NewVerifierMust(t *testing.T, a *Authority) *Authority {
	t.Helper()
	v, err := NewVerifier(a.Root(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// authorityServerFixture is the golden pair the phone pins its trust evaluation to.
type authorityServerFixture struct {
	Note        string   `json:"note"`
	AuthorityID string   `json:"authority_node_id"`
	URISAN      string   `json:"uri_san"`
	IPs         []string `json:"ip_sans"`
	EKU         string   `json:"eku"`
	Chain       string   `json:"chain"`
	VerifyAt    int64    `json:"verify_at"` // a unix time inside both certificates' lives
	RootPEM     string   `json:"root_pem"`
	LeafPEM     string   `json:"leaf_pem"`
	// Must fail: the same address set, but the phone dialed this one.
	WrongIP string `json:"wrong_ip"`
}

// TestWriteAuthorityServerFixture regenerates the fixture when ROGERAI_WRITE_FIXTURES=1. The leaf in
// it is a REAL IssueServer leaf; only its life is stretched so the file stays valid for the phone's
// tests (production issues for ServerCertTTL).
func TestWriteAuthorityServerFixture(t *testing.T) {
	if os.Getenv("ROGERAI_WRITE_FIXTURES") != "1" {
		t.Skip("set ROGERAI_WRITE_FIXTURES=1 to regenerate testdata/authority_server_fixture.json")
	}
	a, err := NewAuthority(Config{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ips := []net.IP{net.ParseIP("192.168.1.69"), net.ParseIP("192.168.1.183")}
	leaf, _, err := a.IssueServer(fixtureNode, ips, 9*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	enc := func(c *x509.Certificate) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	}
	f := authorityServerFixture{
		Note: "A real cert.Authority.IssueServer leaf and its Edge root. The phone evaluates the leaf with the root " +
			"as its ONLY anchor at verify_at, and requires: ServerAuth as the only EKU, the one URI SAN equal to " +
			"spiffe://rogerai.fm/tower/<authority node id>, and an IP SAN equal to the address dialed.",
		AuthorityID: fixtureNode, URISAN: TowerURI(fixtureNode).String(), IPs: []string{"192.168.1.69", "192.168.1.183"},
		EKU: "serverAuth", Chain: "leaf -> root (no intermediate; the root's pathLen is 0)",
		VerifyAt: time.Now().Add(24 * time.Hour).Unix(), RootPEM: enc(a.Root()), LeafPEM: enc(leaf), WrongIP: "192.168.1.70",
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	if err := os.WriteFile("testdata/authority_server_fixture.json", append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheAuthorityServerFixtureHolds(t *testing.T) {
	raw, err := os.ReadFile("testdata/authority_server_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var f authorityServerFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	parse := func(s string) *x509.Certificate {
		b, _ := pem.Decode([]byte(s))
		if b == nil {
			t.Fatal("no PEM")
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	root, leaf := parse(f.RootPEM), parse(f.LeafPEM)
	at := time.Unix(f.VerifyAt, 0)
	for _, ip := range f.IPs {
		if err := VerifyServer(root, leaf, f.AuthorityID, net.ParseIP(ip), at); err != nil {
			t.Fatalf("%s: %v", ip, err)
		}
	}
	if VerifyServer(root, leaf, f.AuthorityID, net.ParseIP(f.WrongIP), at) == nil {
		t.Fatal("the wrong address passed")
	}
	if f.URISAN != leaf.URIs[0].String() || leaf.Subject.CommonName != f.AuthorityID {
		t.Fatal("the fixture's words do not match its leaf")
	}
	if _, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok {
		t.Fatal("the fixture leaf is not P-256")
	}
}
