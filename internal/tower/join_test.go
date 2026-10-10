package tower

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/protocol"
)

// joinClient is a client's key, the hash admission records, and the hex public key the proof binds.
func joinClient(t *testing.T) (pubHex, keyHash string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex = hex.EncodeToString(pub)
	return pubHex, protocol.UserIDFromPubkey(pubHex)
}

func joinProof(st *State, code, nonce, pubHex string) string {
	fp, _ := st.JoinFingerprint()
	return joincode.Tower.Proof(code, nonce, pubHex, fp)
}

// features/join/invite.feature: a bearer client invite admits whoever presents its code, binds
// the presenting key at that moment, and works once.
func TestABearerInviteAdmitsOnTheCodeAndWorksOnce(t *testing.T) {
	st, err := Init(t.TempDir(), ModeStandalone)
	require.NoError(t, err)
	inv, code, err := st.CreateJoinInvitation(JoinClient, "", nil, 15*time.Minute)
	require.NoError(t, err)
	require.Len(t, code, joincode.Tower.CodeLen)
	got, err := st.Invitation(inv.ID)
	require.NoError(t, err)
	require.Empty(t, got.ClientKeyHash, "no client is named at mint")

	pub, kh := joinClient(t)
	res, err := st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code, "n1", pub), Nonce: "n1", PubHex: pub, KeyHash: kh, Name: "laptop-x"})
	require.NoError(t, err)
	require.Equal(t, JoinClient, res.Kind)
	require.True(t, st.IsAdmitted(kh), "the presenting key is admitted")

	// The same invite never admits a second key.
	pub2, kh2 := joinClient(t)
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code, "n2", pub2), Nonce: "n2", PubHex: pub2, KeyHash: kh2})
	require.ErrorIs(t, err, ErrInviteSpent, "a holder of the used invite's code is told it was used (adversarial.feature)")
	require.False(t, st.IsAdmitted(kh2))
	require.Zero(t, st.JoinGuesses(), "presenting a used invite is not a guess")
}

// The proof binds the code to the presenting key: a proof made for one key admits no other.
func TestAProofForOneKeyAdmitsNoOther(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, code, err := st.CreateJoinInvitation(JoinClient, "", nil, 15*time.Minute)
	require.NoError(t, err)
	pub, _ := joinClient(t)
	_, other := joinClient(t)
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code, "n", pub), Nonce: "n", PubHex: pub, KeyHash: other})
	require.ErrorIs(t, err, errBootstrapRejected, "the key hash must be the presented key's")
}

// An expired invite admits nobody; a voided one neither.
func TestExpiredAndVoidedInvitesAdmitNobody(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, code, err := st.CreateJoinInvitation(JoinClient, "", nil, time.Nanosecond)
	require.NoError(t, err)
	time.Sleep(time.Millisecond)
	pub, kh := joinClient(t)
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh})
	require.ErrorIs(t, err, ErrInviteExpired, "a holder of the expired invite's code is told it expired (adversarial.feature)")
	require.False(t, st.IsAdmitted(kh))

	inv, code2, err := st.CreateJoinInvitation(JoinClient, "", nil, 15*time.Minute)
	require.NoError(t, err)
	require.NoError(t, st.VoidInvitation(inv.ID))
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code2, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh})
	require.ErrorIs(t, err, errBootstrapRejected)
}

// A named invite works only for that name, and a wrong name does not spend it.
func TestANamedInviteWorksOnlyForItsName(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, code, err := st.CreateJoinInvitation(JoinClient, "laptop-x", nil, 15*time.Minute)
	require.NoError(t, err)
	pub, kh := joinClient(t)
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh, Name: "other"})
	require.ErrorIs(t, err, errBootstrapRejected)
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code, "n2", pub), Nonce: "n2", PubHex: pub, KeyHash: kh, Name: "laptop-x"})
	require.NoError(t, err, "the wrong name did not spend it")
}

// A client invite cannot attach a station, and a station invite cannot admit a client; neither
// is spent by the wrong kind.
func TestTheWrongKindIsRefusedWithoutSpending(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, code, err := st.CreateJoinInvitation(JoinStation, "", nil, 15*time.Minute)
	require.NoError(t, err)
	pub, kh := joinClient(t)
	_, err = st.ConsumeJoin(JoinRequest{Kind: JoinClient, Proof: joinProof(st, code, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh})
	require.ErrorIs(t, err, ErrWrongInviteKind)
	res, err := st.ConsumeJoin(JoinRequest{Kind: JoinStation, Proof: joinProof(st, code, "n2", pub), Nonce: "n2", PubHex: pub, KeyHash: kh,
		Name: "kitchen-pi", Models: []string{"qwen3-8b", "smollm2-135m-instruct"}})
	require.NoError(t, err)
	require.Equal(t, JoinStation, res.Kind)
	require.Equal(t, []string{"qwen3-8b", "smollm2-135m-instruct"}, res.Station.Models)
}

// The owner can limit what a station may serve: models outside the invite's list are dropped
// and reported.
func TestAStationInviteLimitsItsModels(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, code, err := st.CreateJoinInvitation(JoinStation, "", []string{"qwen3-8b"}, 15*time.Minute)
	require.NoError(t, err)
	pub, kh := joinClient(t)
	res, err := st.ConsumeJoin(JoinRequest{Kind: JoinStation, Proof: joinProof(st, code, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh,
		Name: "pi", Models: []string{"qwen3-8b", "gpt-oss-20b"}})
	require.NoError(t, err)
	require.Equal(t, []string{"qwen3-8b"}, res.Station.Models)
	require.Equal(t, []string{"gpt-oss-20b"}, res.Refused)
}

// A wrong proof spends the global guess budget, and once it is gone even a right one is refused
// (adversarial.feature: brute force).
func TestWrongProofsSpendTheGlobalBudget(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, code, err := st.CreateJoinInvitation(JoinClient, "", nil, 15*time.Minute)
	require.NoError(t, err)
	pub, kh := joinClient(t)
	for i := 0; i < globalAttemptBudget; i++ {
		_, err := st.ConsumeJoin(JoinRequest{Proof: joinProof(st, joincode.Tower.NewCode(), "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh})
		require.ErrorIs(t, err, errBootstrapRejected)
	}
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh})
	require.ErrorIs(t, err, errBootstrapRejected, "the budget is spent")
}

// Joining again with a client already admitted is a no-op that does not spend the invite.
func TestAnAdmittedClientDoesNotSpendAFreshInvite(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, c1, _ := st.CreateJoinInvitation(JoinClient, "", nil, 15*time.Minute)
	pub, kh := joinClient(t)
	_, err := st.ConsumeJoin(JoinRequest{Proof: joinProof(st, c1, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh})
	require.NoError(t, err)
	_, c2, _ := st.CreateJoinInvitation(JoinClient, "", nil, 15*time.Minute)
	res, err := st.ConsumeJoin(JoinRequest{Proof: joinProof(st, c2, "n2", pub), Nonce: "n2", PubHex: pub, KeyHash: kh})
	require.NoError(t, err)
	require.True(t, res.AlreadyMember)
	pub2, kh2 := joinClient(t)
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, c2, "n3", pub2), Nonce: "n3", PubHex: pub2, KeyHash: kh2})
	require.NoError(t, err, "the fresh invite was not spent")
}

// The fingerprint is the Tower's TLS key, stable across opens.
func TestTheJoinFingerprintIsTheTLSKey(t *testing.T) {
	dir := t.TempDir()
	st, _ := Init(dir, ModeStandalone)
	fp, err := st.JoinFingerprint()
	require.NoError(t, err)
	require.Len(t, fp, 64)
	again, _ := Open(dir)
	fp2, _ := again.JoinFingerprint()
	require.Equal(t, fp, fp2)
}

// invite --list shows each live invite by id, kind and time left, never the code or verifier;
// --void stops one and leaves the other.
func TestLiveInvitationsListAndVoid(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	a, codeA, _ := st.CreateJoinInvitation(JoinClient, "", nil, 15*time.Minute)
	b, codeB, _ := st.CreateJoinInvitation(JoinStation, "", nil, 15*time.Minute)
	live := st.LiveInvitations()
	require.Len(t, live, 2)
	for _, inv := range live {
		require.Empty(t, inv.JoinVerifier)
		require.NotContains(t, inv.String(), codeA)
		require.NotContains(t, inv.String(), codeB)
	}
	require.NoError(t, st.VoidInvitation(a.ID))
	live = st.LiveInvitations()
	require.Len(t, live, 1)
	require.Equal(t, b.ID, live[0].ID)
	require.Error(t, st.VoidInvitation("nope"))
	require.Error(t, st.VoidInvitation(a.ID), "a voided invite can't be voided again")
}

func TestCreateJoinInvitationRefusesNonsense(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, _, err := st.CreateJoinInvitation("toaster", "", nil, time.Minute)
	require.Error(t, err)
	_, _, err = st.CreateJoinInvitation(JoinClient, "", nil, 0)
	require.Error(t, err)
	joined, _ := Init(t.TempDir(), ModeJoined)
	_, _, err = joined.CreateJoinInvitation(JoinClient, "", nil, time.Minute)
	require.ErrorIs(t, err, ErrNotStandalone)
	_, err = joined.ConsumeJoin(JoinRequest{})
	require.ErrorIs(t, err, ErrNotStandalone)
}

// A key hash that is not the presented key's is refused before any invite is looked at.
func TestConsumeJoinRefusesAKeyHashNotTheKeys(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, err := st.ConsumeJoin(JoinRequest{PubHex: "", KeyHash: "u_x"})
	require.ErrorIs(t, err, errBootstrapRejected)
	pub, _ := joinClient(t)
	_, err = st.ConsumeJoin(JoinRequest{PubHex: pub, KeyHash: "u_wrong"})
	require.ErrorIs(t, err, errBootstrapRejected)
}

// A Station join that reports no model is refused; the invite is not spent by a failed attach.
func TestAStationJoinWithNoModelIsRefused(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, code, _ := st.CreateJoinInvitation(JoinStation, "", nil, 15*time.Minute)
	pub, kh := joinClient(t)
	_, err := st.ConsumeJoin(JoinRequest{Kind: JoinStation, Proof: joinProof(st, code, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh, Name: "pi"})
	require.Error(t, err)
	_, err = st.ConsumeJoin(JoinRequest{Kind: JoinStation, Proof: joinProof(st, code, "n2", pub), Nonce: "n2", PubHex: pub, KeyHash: kh, Name: "pi", Models: []string{"m"}})
	require.NoError(t, err)
}

// A wrong guess counts against every live bearer invite; after JoinGuessBudget even the right code
// admits nobody (features/join/adversarial.feature).
func TestWrongGuessesExhaustALiveInvite(t *testing.T) {
	st, _ := Init(t.TempDir(), ModeStandalone)
	_, code, err := st.CreateJoinInvitation(JoinClient, "", nil, 15*time.Minute)
	require.NoError(t, err)
	pub, kh := joinClient(t)
	for i := 0; i < JoinGuessBudget; i++ {
		_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, joincode.Tower.NewCode(), "g", pub), Nonce: "g", PubHex: pub, KeyHash: kh})
		require.ErrorIs(t, err, errBootstrapRejected)
	}
	require.Equal(t, JoinGuessBudget, st.JoinGuesses())
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh})
	require.ErrorIs(t, err, errBootstrapRejected, "an exhausted invite is dead, even for the right code")
	require.False(t, st.IsAdmitted(kh))
}
