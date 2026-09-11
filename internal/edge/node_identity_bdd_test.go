package edge_test

// Executable spec: features/edge/node_identity.feature - the account-scoped, capability-typed
// node record ("a node does not have to host a model to belong").
//
// REAL dependencies, no mocks: real ed25519 keypairs generated per node, the real
// internal/store Mem backend (the Postgres half of the same behavior is covered by the
// parity test in internal/store), real store.Grant rows, and the real Station registration
// path (UpsertNode + BindNode) for the byte-identical compatibility scenario.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

type nodeIDState struct {
	t   *testing.T
	db  store.Store
	acc map[string]*edge.Fleet // account id -> fleet

	// the node under test
	priv    ed25519.PrivateKey
	pub     ed25519.PublicKey
	nodeID  string
	account string

	// scratch
	nameErr    error
	enrollErr  error
	verifyHow  string
	declared   string
	renamedID  string
	historyPre []store.EdgeEvent
	grant      store.Grant
	stationPre []byte
	receiptPre []store.Entry
}

func (s *nodeIDState) reset() {
	s.db = store.NewMem()
	s.acc = map[string]*edge.Fleet{}
	s.priv, s.pub, s.nodeID, s.account = nil, nil, "", ""
	s.nameErr, s.enrollErr = nil, nil
	s.verifyHow, s.renamedID, s.declared = "", "", ""
	s.historyPre, s.stationPre, s.receiptPre = nil, nil, nil
	s.grant = store.Grant{}
}

func (s *nodeIDState) fleet(account string) *edge.Fleet {
	if f, ok := s.acc[account]; ok {
		return f
	}
	f := edge.NewFleet(s.db, account)
	s.acc[account] = f
	return f
}

// newKey mints a real keypair the way a node does: on the node, never transmitted.
func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// --- 1. IDENTITY ---------------------------------------------------------

func (s *nodeIDState) aNodeJoinsAnEdge() error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	n, err := s.fleet(s.account).Enroll(edge.NewNode(s.pub, "joiner", edge.Host))
	if err != nil {
		return err
	}
	s.nodeID = n.ID
	return nil
}

func (s *nodeIDState) privateKeyNeverTransmitted() error {
	// The whole persisted fleet, as bytes: the private key must appear nowhere in it.
	all, err := s.fleet(s.account).List()
	if err != nil {
		return err
	}
	blob, _ := json.Marshal(all)
	secret := hex.EncodeToString(s.priv)
	if bytes.Contains(bytes.ToLower(blob), []byte(secret)) {
		return fmt.Errorf("the private key is present in the fleet record")
	}
	if bytes.Contains(blob, []byte(s.priv.Seed())) {
		return fmt.Errorf("the private key seed is present in the fleet record")
	}
	// And the record is derived from the PUBLIC half only.
	if edge.NodeID(s.pub) != s.nodeID {
		return fmt.Errorf("node id %q is not the id derived from the public key", s.nodeID)
	}
	return nil
}

func (s *nodeIDState) idDerivedFromPublicKey() error {
	if got := edge.NodeID(s.pub); got != s.nodeID {
		return fmt.Errorf("NodeID(pub) = %q, record id = %q", got, s.nodeID)
	}
	// Deterministic: the same key always derives the same id.
	if edge.NodeID(s.pub) != edge.NodeID(s.pub) {
		return fmt.Errorf("node id derivation is not deterministic")
	}
	return nil
}

func (s *nodeIDState) noTwoNodesShareAnIDWithoutTheKey() error {
	for i := 0; i < 32; i++ {
		other, _ := newKey(s.t)
		if edge.NodeID(other) == s.nodeID {
			return fmt.Errorf("a different key derived the same node id")
		}
	}
	// Same key, same id - an id IS the key, restated.
	if edge.NodeID(s.pub) != s.nodeID {
		return fmt.Errorf("the same key derived a different id")
	}
	return nil
}

func (s *nodeIDState) aNodeEnrolledToAccount(account string) error {
	s.pub, s.priv = newKey(s.t)
	s.account = account
	n, err := s.fleet(account).Enroll(edge.NewNode(s.pub, "bench-pi", edge.Host))
	if err != nil {
		return err
	}
	s.nodeID = n.ID
	return nil
}

func (s *nodeIDState) recordCarriesAccount(account string) error {
	n, ok, err := s.fleet(account).Get(s.nodeID)
	if err != nil || !ok {
		return fmt.Errorf("node not found in account %q (%v)", account, err)
	}
	if n.Account != account {
		return fmt.Errorf("record account = %q, want %q", n.Account, account)
	}
	return nil
}

func (s *nodeIDState) noSurfaceOfAccountCanReachIt(other string) error {
	f := s.fleet(other)
	list, err := f.List()
	if err != nil {
		return err
	}
	for _, n := range list {
		if n.ID == s.nodeID {
			return fmt.Errorf("account %q can list the node", other)
		}
	}
	if _, ok, _ := f.Get(s.nodeID); ok {
		return fmt.Errorf("account %q can describe the node", other)
	}
	if _, ok, _ := f.ByName("bench-pi"); ok {
		return fmt.Errorf("account %q can address the node by name", other)
	}
	if err := f.Rename(s.nodeID, "stolen"); err == nil {
		return fmt.Errorf("account %q could rename the node", other)
	}
	if err := f.Forget(s.nodeID); err == nil {
		return fmt.Errorf("account %q could forget the node", other)
	}
	return nil
}

func (s *nodeIDState) ownerNamesIt(name string) error {
	return s.fleet(s.account).Rename(s.nodeID, name)
}

func (s *nodeIDState) addressableAs(name string) error {
	n, ok, err := s.fleet(s.account).ByName(name)
	if err != nil || !ok {
		return fmt.Errorf("not addressable as %q (%v)", name, err)
	}
	if n.ID != s.nodeID {
		return fmt.Errorf("name %q resolves to %q, want %q", name, n.ID, s.nodeID)
	}
	return nil
}

func (s *nodeIDState) secondNodeCannotTakeTheName(name string) error {
	pub, _ := newKey(s.t)
	_, err := s.fleet(s.account).Enroll(edge.NewNode(pub, name, edge.Host))
	if err == nil {
		return fmt.Errorf("a second node in the same account took the name %q", name)
	}
	return nil
}

func (s *nodeIDState) differentAccountMayHoldTheSameName() error {
	pub, _ := newKey(s.t)
	if _, err := s.fleet("acct-elsewhere").Enroll(edge.NewNode(pub, "bench-pi", edge.Host)); err != nil {
		return fmt.Errorf("a node in another account was refused the same name: %v", err)
	}
	return nil
}

func (s *nodeIDState) aNodeWithHistoryAndAGrant(name string) error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	f := s.fleet(s.account)
	n, err := f.Enroll(edge.NewNode(s.pub, name, edge.Host, edge.Sense))
	if err != nil {
		return err
	}
	s.nodeID = n.ID
	if err := f.RecordEvent(n.ID, "read", "a sample was taken"); err != nil {
		return err
	}
	cur, _, err := f.Get(n.ID)
	if err != nil {
		return err
	}
	s.historyPre = append([]store.EdgeEvent(nil), cur.History...)
	s.grant = store.Grant{ID: "grant_1", SecretHash: "h1", Owner: s.account,
		Label: "agent", Nodes: []string{n.ID}}
	return s.db.CreateGrant(s.grant)
}

func (s *nodeIDState) ownerRenamesTo(name string) error {
	s.renamedID = s.nodeID
	return s.fleet(s.account).Rename(s.nodeID, name)
}

func (s *nodeIDState) idUnchanged() error {
	n, ok, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil || !ok {
		return fmt.Errorf("node vanished on rename (%v)", err)
	}
	if n.ID != s.renamedID {
		return fmt.Errorf("id changed on rename: %q -> %q", s.renamedID, n.ID)
	}
	return nil
}

func (s *nodeIDState) historyUnchanged() error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	for i, want := range s.historyPre {
		if i >= len(n.History) || n.History[i] != want {
			return fmt.Errorf("history %d changed: %+v", i, n.History)
		}
	}
	return nil
}

func (s *nodeIDState) grantStillAddressesIt() error {
	g, ok, err := s.db.GrantBySecretHash("h1")
	if err != nil || !ok {
		return fmt.Errorf("grant gone (%v)", err)
	}
	if err := s.fleet(s.account).AuthorizeInvoke(g, s.nodeID, ""); err != nil {
		return fmt.Errorf("the grant no longer addresses the node: %v", err)
	}
	return nil
}

// exampleName decodes the Examples-table spellings that cannot be written literally.
func exampleName(raw string) string {
	switch raw {
	case "(empty)":
		return ""
	case "a-name-of-65-chars...":
		return strings.Repeat("a", 65)
	}
	return raw
}

func (s *nodeIDState) ownerNamesANode(raw string) error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	_, s.nameErr = s.fleet(s.account).Enroll(edge.NewNode(s.pub, exampleName(raw), edge.Host))
	return nil
}

func (s *nodeIDState) theNameIs(verdict string) error {
	switch verdict {
	case "accepted":
		if s.nameErr != nil {
			return fmt.Errorf("name rejected but should be accepted: %v", s.nameErr)
		}
	case "rejected":
		if s.nameErr == nil {
			return fmt.Errorf("name accepted but should be rejected")
		}
	default:
		return fmt.Errorf("unknown verdict %q", verdict)
	}
	return nil
}

// --- 2. CAPABILITIES -----------------------------------------------------

func (s *nodeIDState) aNodeThatDeclaresNothing() error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	n, err := s.fleet(s.account).Enroll(edge.NewNode(s.pub, "quiet", edge.Host))
	if err != nil {
		return err
	}
	s.nodeID = n.ID
	return nil
}

func (s *nodeIDState) theFleetIsListed() error { return nil }

func (s *nodeIDState) appearsWithNoCapabilityMarks() error {
	list, err := s.fleet(s.account).List()
	if err != nil {
		return err
	}
	for _, n := range list {
		if n.ID == s.nodeID {
			if len(n.Caps) != 0 {
				return fmt.Errorf("expected no capability marks, got %+v", n.Caps)
			}
			return nil
		}
	}
	return fmt.Errorf("the node is not listed at all")
}

func (s *nodeIDState) itIsAMember() error {
	n, ok, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil || !ok {
		return fmt.Errorf("a capability-less node is not a member (%v)", err)
	}
	if n.Account != s.account {
		return fmt.Errorf("membership is not ownership: account %q", n.Account)
	}
	return nil
}

func (s *nodeIDState) aNodeDeclaring(cap string) error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	n, err := s.fleet(s.account).Enroll(edge.NewNode(s.pub, "cap-"+cap, edge.Host, edge.Capability(cap)))
	if err != nil {
		return err
	}
	s.nodeID, s.declared = n.ID, cap
	return nil
}

func (s *nodeIDState) theFleetVerifiesIt() error {
	how, err := s.fleet(s.account).VerificationMethod(s.nodeID, edge.Capability(s.declared))
	s.verifyHow = how
	return err
}

func (s *nodeIDState) verificationIs(how string) error {
	if s.verifyHow != how {
		return fmt.Errorf("verification = %q, want %q", s.verifyHow, how)
	}
	return nil
}

func (s *nodeIDState) itsServingProbeFails() error {
	return s.fleet(s.account).RecordProbe(s.nodeID, edge.Serve, false)
}

func (s *nodeIDState) shownClaimedNotVerified() error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	for _, c := range n.Caps {
		if c.Name == string(edge.Serve) {
			if c.State != string(edge.Claimed) {
				return fmt.Errorf("serve state = %q, want CLAIMED", c.State)
			}
			return nil
		}
	}
	return fmt.Errorf("serve is missing from the record entirely")
}

func (s *nodeIDState) nothingRoutesOnTheClaim() error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	if edge.Routable(n, edge.Serve) {
		return fmt.Errorf("a merely CLAIMED capability is routable")
	}
	return nil
}

func (s *nodeIDState) nodeAcceptedWorldChangingCommands() error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	f := s.fleet(s.account)
	n, err := f.Enroll(edge.NewNode(s.pub, "relay-board", edge.Host, edge.Sense))
	if err != nil {
		return err
	}
	s.nodeID = n.ID
	// Behavior, recorded honestly: the node really did accept world-changing commands.
	for i := 0; i < 3; i++ {
		if err := f.RecordEvent(n.ID, "invoke", "a command that changed the world was accepted"); err != nil {
			return err
		}
	}
	return nil
}

func (s *nodeIDState) itDoesNotDeclare(cap string) error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	for _, c := range n.Caps {
		if c.Name == cap {
			return fmt.Errorf("the node already declares %q", cap)
		}
	}
	return nil
}

func (s *nodeIDState) fleetDoesNotAdd(cap string) error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	for _, c := range n.Caps {
		if c.Name == cap {
			return fmt.Errorf("the fleet inferred %q from behavior", cap)
		}
	}
	return nil
}

func (s *nodeIDState) nextSuchCommandRefused() error {
	g := store.Grant{ID: "g", Owner: s.account, Nodes: []string{s.nodeID}}
	if err := s.fleet(s.account).AuthorizeInvoke(g, s.nodeID, edge.Actuate); err == nil {
		return fmt.Errorf("an actuate invoke was authorized against an undeclared capability")
	}
	return nil
}

func (s *nodeIDState) aNodeDeclaringForTheFirstTime(cap string) error {
	return s.aNodeDeclaring(cap)
}

func (s *nodeIDState) itIsPendingConfirmation() error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	for _, c := range n.Caps {
		if c.Name == string(edge.Actuate) {
			if c.State != string(edge.PendingConfirmation) {
				return fmt.Errorf("actuate state = %q, want PENDING CONFIRMATION", c.State)
			}
			return nil
		}
	}
	return fmt.Errorf("actuate missing from the record")
}

func (s *nodeIDState) acceptsNoInvokeUntilConfirmed() error {
	g := store.Grant{ID: "g", Owner: s.account, Nodes: []string{s.nodeID}}
	if err := s.fleet(s.account).AuthorizeInvoke(g, s.nodeID, edge.Actuate); err == nil {
		return fmt.Errorf("an unconfirmed actuate accepted an invoke")
	}
	return nil
}

func (s *nodeIDState) confirmationRecordedWithWhoAndWhen() error {
	f := s.fleet(s.account)
	if err := f.ConfirmActuate(s.nodeID, "owner@acct-1"); err != nil {
		return err
	}
	n, _, err := f.Get(s.nodeID)
	if err != nil {
		return err
	}
	for _, c := range n.Caps {
		if c.Name == string(edge.Actuate) {
			if c.ConfirmedBy != "owner@acct-1" {
				return fmt.Errorf("confirmed_by = %q", c.ConfirmedBy)
			}
			if c.ConfirmedAt == 0 {
				return fmt.Errorf("confirmed_at not recorded")
			}
			if c.State != string(edge.Verified) {
				return fmt.Errorf("state after confirmation = %q, want VERIFIED", c.State)
			}
			// And only now does an invoke pass.
			g := store.Grant{ID: "g", Owner: s.account, Nodes: []string{s.nodeID}}
			return f.AuthorizeInvoke(g, s.nodeID, edge.Actuate)
		}
	}
	return fmt.Errorf("actuate missing from the record")
}

func (s *nodeIDState) aVerifiedNodeWith(cap string) error {
	if err := s.aNodeDeclaring(cap); err != nil {
		return err
	}
	return s.fleet(s.account).RecordProbe(s.nodeID, edge.Capability(cap), true)
}

func (s *nodeIDState) stopsServingAndDeclaresInstead(cap string) error {
	return s.fleet(s.account).Declare(s.nodeID, []edge.Capability{edge.Capability(cap)})
}

func (s *nodeIDState) idAndNameUnchanged() error {
	n, ok, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil || !ok {
		return fmt.Errorf("node vanished (%v)", err)
	}
	if n.ID != s.nodeID || n.Name != "cap-serve" {
		return fmt.Errorf("id/name changed: %q/%q", n.ID, n.Name)
	}
	return nil
}

func (s *nodeIDState) capDroppedAndCapAdded(dropped, added string) error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	var names []string
	for _, c := range n.Caps {
		names = append(names, c.Name)
	}
	for _, got := range names {
		if got == dropped {
			return fmt.Errorf("%q was not dropped: %v", dropped, names)
		}
	}
	for _, got := range names {
		if got == added {
			return nil
		}
	}
	return fmt.Errorf("%q was not added: %v", added, names)
}

func (s *nodeIDState) changeVisibleInHistory() error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	for _, e := range n.History {
		if strings.Contains(e.Detail, "sense") || strings.Contains(e.What, "capab") {
			return nil
		}
	}
	return fmt.Errorf("the capability change is not in the history: %+v", n.History)
}

// --- the Station compatibility scenario ----------------------------------

func (s *nodeIDState) anExistingStationRegisteredTheWayStationsRegisterToday() error {
	s.account = "acct-1"
	reg := protocol.NodeRegistration{
		NodeID: "station-1", PubKey: "cafebabe", Region: "us",
		Offers: []protocol.ModelOffer{{Model: "wave-7b"}},
	}
	rec := store.NodeRecord{NodeID: "station-1", Reg: reg, LastSeen: 1000, RegisteredAt: 900}
	if err := s.db.UpsertNode(rec); err != nil {
		return err
	}
	if err := s.db.BindNode("station-1", s.account); err != nil {
		return err
	}
	// A receipt on the existing money path, so "byte-identical" covers receipts too.
	if _, err := s.db.AddCredits("u1", 10); err != nil {
		return err
	}
	if _, err := s.db.Settle("u1", "station-1", 1, 0.9, protocol.UsageReceipt{
		RequestID: "r1", Model: "wave-7b", PromptTokens: 10, CompletionTokens: 20, TS: 1000,
	}); err != nil {
		return err
	}
	all, err := s.db.AllNodes()
	if err != nil {
		return err
	}
	s.stationPre, _ = json.Marshal(all)
	s.receiptPre, err = s.db.RecentByNode("station-1", 10)
	return err
}

func (s *nodeIDState) stationAppearsAsNodeWithCapability(cap string) error {
	list, err := s.fleet(s.account).List()
	if err != nil {
		return err
	}
	for _, n := range list {
		if n.ID != "station-1" {
			continue
		}
		for _, c := range n.Caps {
			if c.Name == cap && c.State == string(edge.Verified) {
				return nil
			}
		}
		return fmt.Errorf("the Station has no VERIFIED %q capability: %+v", cap, n.Caps)
	}
	return fmt.Errorf("the Station does not appear in the fleet")
}

func (s *nodeIDState) registrationOffersReceiptsByteIdentical() error {
	all, err := s.db.AllNodes()
	if err != nil {
		return err
	}
	post, _ := json.Marshal(all)
	if !bytes.Equal(s.stationPre, post) {
		return fmt.Errorf("the Station registration changed:\n pre: %s\npost: %s", s.stationPre, post)
	}
	got, err := s.db.RecentByNode("station-1", 10)
	if err != nil {
		return err
	}
	pre, _ := json.Marshal(s.receiptPre)
	now, _ := json.Marshal(got)
	if !bytes.Equal(pre, now) {
		return fmt.Errorf("the Station receipts changed:\n pre: %s\npost: %s", pre, now)
	}
	return nil
}

func (s *nodeIDState) noEdgeRecordRequiredToServe() error {
	// The fleet view is DERIVED: no edge_nodes row was written for the Station.
	if _, ok, err := s.db.EdgeNodeByID(s.account, "station-1"); err != nil || ok {
		return fmt.Errorf("an Edge record was written for a plain Station (ok=%v, err=%v)", ok, err)
	}
	// And it is still registered and serving from the untouched registry.
	all, err := s.db.AllNodes()
	if err != nil || len(all) != 1 || all[0].NodeID != "station-1" {
		return fmt.Errorf("the Station registry was disturbed: %+v (%v)", all, err)
	}
	return nil
}

// --- 3. KIND, TRANSPORTS, LIFECYCLE --------------------------------------

func (s *nodeIDState) aNodeOfKind(kind string) error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	n, err := s.fleet(s.account).Enroll(edge.NewNode(s.pub, "k-"+kind, edge.Kind(kind)))
	if err != nil {
		return err
	}
	s.nodeID = n.ID
	return nil
}

func (s *nodeIDState) itsEncodingIs(enc string) error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	if got := edge.Kind(n.Kind).Encoding(); got != enc {
		return fmt.Errorf("encoding = %q, want %q", got, enc)
	}
	return nil
}

func (s *nodeIDState) itMayDeclare(phrase string) error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	k := edge.Kind(n.Kind)
	var want map[edge.Capability]bool
	switch phrase {
	case "any capability":
		want = map[edge.Capability]bool{}
		for _, c := range edge.Capabilities() {
			want[c] = true
		}
	case "classify, sense, actuate only":
		want = map[edge.Capability]bool{edge.Classify: true, edge.Sense: true, edge.Actuate: true}
	case "any except relay":
		want = map[edge.Capability]bool{}
		for _, c := range edge.Capabilities() {
			want[c] = c != edge.Relay
		}
	default:
		return fmt.Errorf("unknown capability phrase %q", phrase)
	}
	for _, c := range edge.Capabilities() {
		if got := k.MayDeclare(c); got != want[c] {
			return fmt.Errorf("kind %q MayDeclare(%q) = %v, want %v", k, c, got, want[c])
		}
	}
	// And the rule is enforced, not merely reported.
	for _, c := range edge.Capabilities() {
		if want[c] {
			continue
		}
		if err := s.fleet(s.account).Declare(s.nodeID, []edge.Capability{c}); err == nil {
			return fmt.Errorf("kind %q was allowed to declare %q", k, c)
		}
	}
	return nil
}

func (s *nodeIDState) aNodeReachableOnLANAndRelay() error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	n := edge.NewNode(s.pub, "both-ways", edge.Host, edge.Serve)
	// Deliberately discovered relay-FIRST, so the ordering cannot be discovery order.
	n.Transports = []store.EdgeTransport{
		{Kind: "relay", Addr: "relay.rogerai.fm"},
		{Kind: "lan", Addr: "192.168.1.20:9443", Fingerprint: strings.Repeat("ab", 32)},
	}
	got, err := s.fleet(s.account).Enroll(n)
	if err != nil {
		return err
	}
	s.nodeID = got.ID
	return nil
}

func (s *nodeIDState) transportsReadLANFirstRelaySecond() error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	if len(n.Transports) != 2 || n.Transports[0].Kind != "lan" || n.Transports[1].Kind != "relay" {
		return fmt.Errorf("transports are not LAN-first: %+v", n.Transports)
	}
	return nil
}

func (s *nodeIDState) lanEntryCarriesThePin() error {
	n, _, err := s.fleet(s.account).Get(s.nodeID)
	if err != nil {
		return err
	}
	if n.Transports[0].Fingerprint == "" {
		return fmt.Errorf("the LAN transport carries no pinned fingerprint")
	}
	if n.Transports[0].Fingerprint != n.Pin {
		return fmt.Errorf("the LAN fingerprint %q is not the node's pin %q", n.Transports[0].Fingerprint, n.Pin)
	}
	return nil
}

func (s *nodeIDState) aVerifiedNodeWithPinAndHistory() error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	f := s.fleet(s.account)
	n := edge.NewNode(s.pub, "pinned", edge.Host, edge.Serve)
	n.Pin = strings.Repeat("cd", 32)
	got, err := f.Enroll(n)
	if err != nil {
		return err
	}
	s.nodeID = got.ID
	if err := f.RecordProbe(got.ID, edge.Serve, true); err != nil {
		return err
	}
	// Money the forget must never touch.
	if _, err := s.db.AddCredits("u1", 10); err != nil {
		return err
	}
	if _, err := s.db.Settle("u1", got.ID, 1, 0.9, protocol.UsageReceipt{
		RequestID: "r-forget", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: 5,
	}); err != nil {
		return err
	}
	s.receiptPre, err = s.db.RecentByNode(got.ID, 10)
	return err
}

func (s *nodeIDState) ownerForgetsIt() error { return s.fleet(s.account).Forget(s.nodeID) }

func (s *nodeIDState) goneFromTheFleet() error {
	if _, ok, _ := s.fleet(s.account).Get(s.nodeID); ok {
		return fmt.Errorf("the node is still in the fleet")
	}
	list, err := s.fleet(s.account).List()
	if err != nil {
		return err
	}
	for _, n := range list {
		if n.ID == s.nodeID {
			return fmt.Errorf("the node is still listed")
		}
	}
	return nil
}

func (s *nodeIDState) pinClearedSoItMayBeAdoptedAgain() error {
	// Re-enrolling the SAME key succeeds and starts with no pin: the old pin is gone.
	n, err := s.fleet(s.account).Enroll(edge.NewNode(s.pub, "pinned-again", edge.Host))
	if err != nil {
		return fmt.Errorf("the node could not be adopted again: %v", err)
	}
	if n.Pin != "" {
		return fmt.Errorf("a stale pin survived the forget: %q", n.Pin)
	}
	return nil
}

func (s *nodeIDState) receiptsAndLedgerUntouched() error {
	got, err := s.db.RecentByNode(s.nodeID, 10)
	if err != nil {
		return err
	}
	pre, _ := json.Marshal(s.receiptPre)
	now, _ := json.Marshal(got)
	if !bytes.Equal(pre, now) {
		return fmt.Errorf("forget changed the receipts:\n pre: %s\npost: %s", pre, now)
	}
	rows, err := s.db.LedgerOf("u1", nil, 100)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("the ledger is empty; the fixture proved nothing")
	}
	return nil
}

func (s *nodeIDState) aGrantAddressedToANode() error {
	s.pub, s.priv = newKey(s.t)
	s.account = "acct-1"
	n, err := s.fleet(s.account).Enroll(edge.NewNode(s.pub, "targeted", edge.Host, edge.Sense))
	if err != nil {
		return err
	}
	s.nodeID = n.ID
	s.grant = store.Grant{ID: "grant_2", SecretHash: "h2", Owner: s.account,
		Label: "agent", Nodes: []string{n.ID}}
	if err := s.db.CreateGrant(s.grant); err != nil {
		return err
	}
	// It authorizes today, so the refusal after the forget means something.
	return s.fleet(s.account).AuthorizeInvoke(s.grant, s.nodeID, "")
}

func (s *nodeIDState) ownerForgetsThatNode() error { return s.fleet(s.account).Forget(s.nodeID) }

func (s *nodeIDState) grantNoLongerAuthorizes() error {
	g, ok, err := s.db.GrantBySecretHash("h2")
	if err != nil || !ok {
		return fmt.Errorf("grant lookup failed (%v)", err)
	}
	if err := s.fleet(s.account).AuthorizeInvoke(g, s.nodeID, ""); err == nil {
		return fmt.Errorf("the grant still authorizes a forgotten node")
	}
	return nil
}

func (s *nodeIDState) laterInvokeAgainstThatIDRefused() error {
	g := store.Grant{ID: "grant_2", Owner: s.account, Nodes: []string{s.nodeID}}
	if err := s.fleet(s.account).AuthorizeInvoke(g, s.nodeID, edge.Sense); err == nil {
		return fmt.Errorf("an invoke against a forgotten node id was authorized")
	}
	return nil
}

func (s *nodeIDState) attemptsToEnrollToWhileEnrolled(other string) error {
	_, s.enrollErr = s.fleet(other).Enroll(edge.NewNode(s.pub, "poached", edge.Host))
	return nil
}

func (s *nodeIDState) secondEnrollmentRefused() error {
	if s.enrollErr == nil {
		return fmt.Errorf("the second enrollment succeeded")
	}
	return nil
}

func (s *nodeIDState) refusalNamesTheAccountItBelongsTo() error {
	var ee *edge.EnrollError
	if !errors.As(s.enrollErr, &ee) {
		return fmt.Errorf("the refusal is not an EnrollError: %v", s.enrollErr)
	}
	if ee.Existing != "acct-1" {
		return fmt.Errorf("the refusal does not name acct-1: %+v", ee)
	}
	return nil
}

func (s *nodeIDState) otherOwnerLearnsNothing(other, secret string) error {
	// Everything the OTHER account can see: the error text it is shown, and its fleet.
	if strings.Contains(s.enrollErr.Error(), secret) {
		return fmt.Errorf("the message shown to %s names %s: %q", other, secret, s.enrollErr)
	}
	list, err := s.fleet(other).List()
	if err != nil {
		return err
	}
	blob, _ := json.Marshal(list)
	if bytes.Contains(blob, []byte(secret)) {
		return fmt.Errorf("%s's fleet view leaks %s: %s", other, secret, blob)
	}
	if len(list) != 0 {
		return fmt.Errorf("%s gained a node it does not own: %+v", other, list)
	}
	return nil
}

func TestNodeIdentityFeature(t *testing.T) {
	st := &nodeIDState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return c, nil
			})
			sc.Step(`^a node joins an Edge$`, st.aNodeJoinsAnEdge)
			sc.Step(`^the private key was generated on the node and never transmitted$`, st.privateKeyNeverTransmitted)
			sc.Step(`^the node id is derived from the public key$`, st.idDerivedFromPublicKey)
			sc.Step(`^two nodes can never share an id without sharing a key$`, st.noTwoNodesShareAnIDWithoutTheKey)
			sc.Step(`^a node enrolled to account "([^"]*)"$`, st.aNodeEnrolledToAccount)
			sc.Step(`^its record carries account "([^"]*)"$`, st.recordCarriesAccount)
			sc.Step(`^no surface of account "([^"]*)" can list it, describe it, or address it$`, st.noSurfaceOfAccountCanReachIt)
			sc.Step(`^the owner names it "([^"]*)"$`, st.ownerNamesIt)
			sc.Step(`^it is addressable as "([^"]*)" within that account$`, st.addressableAs)
			sc.Step(`^a second node in the same account cannot take the name "([^"]*)"$`, st.secondNodeCannotTakeTheName)
			sc.Step(`^a node in a DIFFERENT account may hold the same name without conflict$`, st.differentAccountMayHoldTheSameName)
			sc.Step(`^a node "([^"]*)" with a history and a grant addressed to it$`, st.aNodeWithHistoryAndAGrant)
			sc.Step(`^the owner renames it to "([^"]*)"$`, st.ownerRenamesTo)
			sc.Step(`^its id is unchanged$`, st.idUnchanged)
			sc.Step(`^its history is unchanged$`, st.historyUnchanged)
			sc.Step(`^the grant still addresses it$`, st.grantStillAddressesIt)
			sc.Step(`^the owner names a node "([^"]*)"$`, st.ownerNamesANode)
			sc.Step(`^the name is (accepted|rejected)$`, st.theNameIs)
			sc.Step(`^a node that declares nothing$`, st.aNodeThatDeclaresNothing)
			sc.Step(`^the fleet is listed$`, st.theFleetIsListed)
			sc.Step(`^it appears with no capability marks$`, st.appearsWithNoCapabilityMarks)
			sc.Step(`^it is a member, because membership is ownership, not usefulness$`, st.itIsAMember)
			sc.Step(`^a node declaring "([^"]*)"$`, st.aNodeDeclaring)
			sc.Step(`^the fleet verifies it$`, st.theFleetVerifiesIt)
			sc.Step(`^verification is "([^"]*)"$`, st.verificationIs)
			sc.Step(`^its serving probe fails$`, st.itsServingProbeFails)
			sc.Step(`^the capability is shown as CLAIMED and not VERIFIED$`, st.shownClaimedNotVerified)
			sc.Step(`^nothing routes to it on the strength of that claim$`, st.nothingRoutesOnTheClaim)
			sc.Step(`^a node that has accepted commands that changed the world$`, st.nodeAcceptedWorldChangingCommands)
			sc.Step(`^it does not declare "([^"]*)"$`, st.itDoesNotDeclare)
			sc.Step(`^the fleet does not add "([^"]*)"$`, st.fleetDoesNotAdd)
			sc.Step(`^the next such command is refused$`, st.nextSuchCommandRefused)
			sc.Step(`^a node declaring "([^"]*)" for the first time$`, st.aNodeDeclaringForTheFirstTime)
			sc.Step(`^it is PENDING CONFIRMATION$`, st.itIsPendingConfirmation)
			sc.Step(`^it accepts no invoke until the owner confirms$`, st.acceptsNoInvokeUntilConfirmed)
			sc.Step(`^the confirmation is recorded with who confirmed it and when$`, st.confirmationRecordedWithWhoAndWhen)
			sc.Step(`^a verified node with "([^"]*)"$`, st.aVerifiedNodeWith)
			sc.Step(`^it stops serving and declares "([^"]*)" instead$`, st.stopsServingAndDeclaresInstead)
			sc.Step(`^its id and name are unchanged$`, st.idAndNameUnchanged)
			sc.Step(`^"([^"]*)" is dropped and "([^"]*)" is added$`, st.capDroppedAndCapAdded)
			sc.Step(`^the change is visible in its history$`, st.changeVisibleInHistory)
			sc.Step("^an existing `roger share` Station registered the way Stations register today$", st.anExistingStationRegisteredTheWayStationsRegisterToday)
			sc.Step(`^it appears in the owner's fleet as a node with capability "([^"]*)"$`, st.stationAppearsAsNodeWithCapability)
			sc.Step(`^its market registration, its offers and its receipts are byte-identical to before$`, st.registrationOffersReceiptsByteIdentical)
			sc.Step(`^nothing about the Edge record is required for it to serve$`, st.noEdgeRecordRequiredToServe)
			sc.Step(`^a node of kind "([^"]*)"$`, st.aNodeOfKind)
			sc.Step(`^its encoding is "([^"]*)"$`, st.itsEncodingIs)
			sc.Step(`^it may declare (.+)$`, st.itMayDeclare)
			sc.Step(`^a node reachable on the LAN and through a relay$`, st.aNodeReachableOnLANAndRelay)
			sc.Step(`^its transports read LAN first, relay second$`, st.transportsReadLANFirstRelaySecond)
			sc.Step(`^the LAN entry carries the pinned certificate fingerprint$`, st.lanEntryCarriesThePin)
			sc.Step(`^a verified node with a pin and a history$`, st.aVerifiedNodeWithPinAndHistory)
			sc.Step(`^the owner forgets it$`, st.ownerForgetsIt)
			sc.Step(`^it is gone from the fleet$`, st.goneFromTheFleet)
			sc.Step(`^its pin is cleared, so it may be adopted again later$`, st.pinClearedSoItMayBeAdoptedAgain)
			sc.Step(`^its receipts and ledger rows are untouched, because money is not fleet state$`, st.receiptsAndLedgerUntouched)
			sc.Step(`^a grant addressed to a node$`, st.aGrantAddressedToANode)
			sc.Step(`^the owner forgets that node$`, st.ownerForgetsThatNode)
			sc.Step(`^the grant no longer authorizes anything on it$`, st.grantNoLongerAuthorizes)
			sc.Step(`^a later invoke against that id is refused$`, st.laterInvokeAgainstThatIDRefused)
			sc.Step(`^it attempts to enroll to account "([^"]*)" while still enrolled$`, st.attemptsToEnrollToWhileEnrolled)
			sc.Step(`^the second enrollment is refused$`, st.secondEnrollmentRefused)
			sc.Step(`^the refusal names the account it already belongs to$`, st.refusalNamesTheAccountItBelongsTo)
			sc.Step(`^the owner of "([^"]*)" learns nothing about "([^"]*)"$`, st.otherOwnerLearnsNothing)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/edge/node_identity.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the node identity scenarios failed")
	}
}
