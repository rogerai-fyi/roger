package towercli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
	"rogerai.fm/roger/v6/internal/toweradmin"
)

// TestTheOwnersScreenMintsAnInviteAndSeesWhoJoined drives the owner's-screen operations against a
// serving Tower: the invite is one `roger join` line naming the serving address and the Tower's
// fingerprint, it is not over until used, and once a device joins with it the line says who.
func TestTheOwnersScreenMintsAnInviteAndSeesWhoJoined(t *testing.T) {
	dir, st, _ := servingPlane(t)
	fp, err := OwnerFingerprint(dir)
	require.NoError(t, err)

	shown, err := MintOwnerInvite(dir, "192.168.1.20:8787")
	require.NoError(t, err)
	require.Equal(t, "invite for one device, valid 15 min:", shown.Heading)
	require.Equal(t, "on the other machine run:", shown.Next)
	require.True(t, strings.HasPrefix(shown.Line, "roger join roger://join?"), shown.Line)
	in, err := joininvite.Parse(strings.TrimPrefix(shown.Line, "roger join "))
	require.NoError(t, err)
	require.Equal(t, []string{"192.168.1.20:8787"}, in.Addrs)
	require.Equal(t, fp, in.FP, "the invite pins the Tower the owner is looking at")

	line, over := OwnerInviteOutcome(dir, shown.ID)
	require.False(t, over, "an unused invite is still waiting")
	require.Empty(t, line)

	// laptop-x joins with the invite's code.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	pubHex := hex.EncodeToString(pub)
	nonce := protocol.NewNonce()
	_, err = st.ConsumeJoin(tower.JoinRequest{
		Proof: joincode.Tower.Proof(in.Code, nonce, pubHex, in.FP), Nonce: nonce,
		PubHex: pubHex, KeyHash: protocol.UserIDFromPubkey(pubHex), Name: "laptop-x",
	})
	require.NoError(t, err)
	line, over = OwnerInviteOutcome(dir, shown.ID)
	require.True(t, over)
	require.Contains(t, line, "laptop-x joined")
}

// TestAnInviteThatRunsOutSaysSo: a voided invite is over and unused.
func TestAnInviteThatRunsOutSaysSo(t *testing.T) {
	dir, _, _ := servingPlane(t)
	shown, err := MintOwnerInvite(dir, "192.168.1.20:8787")
	require.NoError(t, err)
	require.NoError(t, toweradmin.Client{Dir: dir}.VoidInvitation(shown.ID))
	line, over := OwnerInviteOutcome(dir, shown.ID)
	require.True(t, over)
	require.Equal(t, inviteUnused, line)
	_, over = OwnerInviteOutcome(t.TempDir(), "nope")
	require.True(t, over, "an unreadable directory ends the wait")
	_, err = MintOwnerInvite(dir, "not-an-address")
	require.Error(t, err)
}

// TestTheOwnersScreenAnswersARequest: a waiting request is listed with its phrase; approving it
// admits, refusing it admits nothing.
func TestTheOwnersScreenAnswersARequest(t *testing.T) {
	dir, _, plane := servingPlane(t)
	reqs, err := OwnerRequests(dir)
	require.NoError(t, err)
	require.Empty(t, reqs)

	id := ask(t, plane, "laptop-x")
	reqs, err = OwnerRequests(dir)
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	require.Equal(t, "laptop-x", reqs[0].Name)
	require.NotEmpty(t, reqs[0].Phrase)
	require.NoError(t, OwnerDecide(dir, id, true))

	id2 := ask(t, plane, "phone")
	require.NoError(t, OwnerDecide(dir, id2, false))
	require.Error(t, OwnerDecide(dir, id2, true), "a refused request cannot be approved after")
	reqs, _ = OwnerRequests(dir)
	require.Empty(t, reqs)
}
