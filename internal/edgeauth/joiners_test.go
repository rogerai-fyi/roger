package edgeauth_test

// checks_root (features/edge/enrollment_root_trust.feature, D9 as fixed 2026-10-13): a joiner says it
// verifies the root proof, the authority remembers what it heard, and an authority that predates the
// field still enrolls the joiner exactly as before.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

// The flag is outside the signed form on all three requests, so the golden canonical bytes the phone
// and the CLI pin are unchanged, and an authority that never heard of it verifies the same signature.
func TestChecksRootIsOutsideTheRequestSignature(t *testing.T) {
	_, user := keypair(t)
	req, _ := signedRequest(t, "", user)
	require.NoError(t, req.VerifySignature())
	req.SignChecksRoot(user)
	require.NoError(t, req.VerifySignature(), "checks_root and its signature change nothing that the request signature covers")
	require.True(t, req.ChecksRootProven())

	_, pub, priv, _ := claimFixture(t)
	c := signedClaim(t, pub, priv, time.Now())
	c.SignChecksRoot(priv)
	require.NoError(t, c.VerifySignature())
	require.True(t, c.ChecksRootProven())
}

// An older authority decodes with plain json.Unmarshal (no DisallowUnknownFields): a field it does not
// know is skipped. Shown here with a field THIS build does not know either, through the same decode.
func TestAnUnknownRequestFieldIsIgnoredNotRefused(t *testing.T) {
	_, user := keypair(t)
	req, _ := signedRequest(t, "", user)
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	m["checks_root"] = true
	m["a_field_from_the_future"] = "x"
	raw, err = json.Marshal(m)
	require.NoError(t, err)
	var back edgeauth.Request
	require.NoError(t, json.Unmarshal(raw, &back))
	require.True(t, back.ChecksRoot)
	require.NoError(t, back.VerifySignature())

	// And the field travels only when set: an old joiner's JSON is byte for byte what it was.
	plain, err := json.Marshal(req)
	require.NoError(t, err)
	require.NotContains(t, string(plain), "checks_root")
}

// A phone polls its claim before it is adopted: that is when the authority hears whether it checks
// codes, in time for the adopt to say so. Only a flag the phone SIGNED is credited.
func TestAClaimBeforeAdoptIsHeard(t *testing.T) {
	local, pub, priv, id := claimFixture(t)
	_, heard := local.NodeChecksRoot(id)
	require.False(t, heard)

	c := signedClaim(t, pub, priv, time.Now())
	c.SignChecksRoot(priv)
	_, err := local.Claim(c, time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)
	checks, heard := local.NodeChecksRoot(id)
	require.True(t, heard)
	require.True(t, checks)

	// An old app's claim says nothing, and is heard as one that does not check.
	local2, pub2, priv2, id2 := claimFixture(t)
	_, err = local2.Claim(signedClaim(t, pub2, priv2, time.Now()), time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)
	checks, heard = local2.NodeChecksRoot(id2)
	require.True(t, heard)
	require.False(t, checks)

	// A claim whose signature does not check out is not heard at all.
	local3, pub3, priv3, id3 := claimFixture(t)
	bad := signedClaim(t, pub3, priv3, time.Now())
	bad.SignChecksRoot(priv3)
	bad.Sig = "00"
	_, err = local3.Claim(bad, time.Now())
	require.Error(t, err)
	_, heard = local3.NodeChecksRoot(id3)
	require.False(t, heard, "only a signed, fresh request is noted")
}

// Audit 2026-10-13: an old app's captured claim, replayed with checks_root set by whoever replays it,
// must not silence the warning that protects that old app. Unsigned, the flag counts for nothing; and
// a refused request never overwrites a note already there.
func TestAReplayedClaimCannotClaimToCheck(t *testing.T) {
	local, pub, priv, id := claimFixture(t)
	old := signedClaim(t, pub, priv, time.Now()) // an old app: no flag
	_, err := local.Claim(old, time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)

	replayed := old
	replayed.ChecksRoot = true // set in transit; no signature can be made for it without the key
	_, err = local.Claim(replayed, time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)
	checks, heard := local.NodeChecksRoot(id)
	require.True(t, heard)
	require.False(t, checks, "an unsigned flag is not credited")

	// A forged signature (another key's) is not credited either.
	_, otherPriv := keypair(t)
	forged := signedClaim(t, pub, priv, time.Now())
	forged.SignChecksRoot(otherPriv)
	require.False(t, forged.ChecksRootProven())
}

// The same holds the other way: once a phone proved it checks, a replay of its earlier, unflagged
// claim does not turn the note back.
func TestARefusedRequestNeverOverwritesANote(t *testing.T) {
	local, pub, priv, id := claimFixture(t)
	earlier := signedClaim(t, pub, priv, time.Now().Add(-time.Minute))
	later := signedClaim(t, pub, priv, time.Now())
	later.SignChecksRoot(priv)
	_, _ = local.Claim(later, time.Now())
	_, _ = local.Claim(earlier, time.Now())
	checks, _ := local.NodeChecksRoot(id)
	require.True(t, checks)
}

// The checks_root signature is by the key that signs the request: the user key for an enrolment.
func TestAnEnrolmentChecksRootIsSignedByTheUserKey(t *testing.T) {
	_, user := keypair(t)
	req, _ := signedRequest(t, "", user)
	req.SignChecksRoot(user)
	require.True(t, req.ChecksRootProven())
	_, other := keypair(t)
	req.SignChecksRoot(other)
	require.False(t, req.ChecksRootProven(), "signed by another key")
	req.SignChecksRoot(user)
	req.Name = "renamed" // the signature covers the request's canonical bytes
	require.False(t, req.ChecksRootProven())
}

// The note survives the claim that joins, and a restart.
func TestAJoinIsNotedAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)
	pub, priv := keypair(t)
	id := edgeauth.NodeID(pub)
	require.NoError(t, local.GrantClaim(id, "pocket"))
	c := signedClaim(t, pub, priv, time.Now())
	c.SignChecksRoot(priv)
	_, err = local.Claim(c, time.Now())
	require.NoError(t, err)

	again, ok, err := edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	require.True(t, ok)
	checks, heard := again.NodeChecksRoot(id)
	require.True(t, heard)
	require.True(t, checks)
	require.True(t, edgeauth.ReadJoiners(dir)[id].ChecksRoot, "the fleet window reads it without the key")
}

// A machine that asks before the owner allows it is heard by its user key, so the allow can say whether
// it checks; once allowed, its enrolment is noted by node id too, and what it said when it JOINED wins.
func TestAnEnrolmentIsHeardByUserKeyAndNode(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	_, user := keypair(t)
	userHex := hex.EncodeToString(user.Public().(ed25519.PublicKey))

	early, _ := signedRequest(t, "", user)
	early.SignChecksRoot(user)
	_, err = local.Issue(early)
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	checks, heard := local.UserChecksRoot(userHex)
	require.True(t, heard)
	require.True(t, checks)
	_, heard = local.NodeChecksRoot(early.NodeID)
	require.False(t, heard, "a refused user-key request proves no node, so no node is noted")

	require.NoError(t, local.Allow(userHex))
	req, _ := signedRequest(t, "", user)
	resp, err := local.Issue(req) // this one says nothing: an old roger
	require.NoError(t, err)
	checks, heard = local.NodeChecksRoot(resp.NodeID)
	require.True(t, heard)
	require.False(t, checks)
	checks, _ = local.UserChecksRoot(userHex)
	require.False(t, checks, "what it said when it joined wins over what it said before")
}

// Refused requests are signed by keys anyone can make, so new notes from them are budgeted per minute,
// and they are kept apart from the notes of members that joined: a flood never evicts a member.
func TestRefusedRequestsCannotFloodTheNotes(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	mpub, mpriv := keypair(t)
	member := edgeauth.NodeID(mpub)
	require.NoError(t, local.GrantClaim(member, "pocket"))
	c := signedClaim(t, mpub, mpriv, time.Now())
	c.SignChecksRoot(mpriv)
	_, err = local.Claim(c, time.Now())
	require.NoError(t, err)

	base := time.Now().Truncate(time.Minute).Add(10 * time.Second)
	heardCount := 0
	for minute := 0; minute < 12; minute++ {
		now := base.Add(time.Duration(minute) * time.Minute)
		for i := 0; i < 40; i++ {
			pub, priv := keypair(t)
			_, _ = local.Claim(signedClaim(t, pub, priv, now), now)
			if minute == 0 {
				if _, h := local.NodeChecksRoot(edgeauth.NodeID(pub)); h {
					heardCount++
				}
			}
		}
	}
	require.Equal(t, 30, heardCount, "thirty new strangers a minute are noted; the rest are not")
	checks, heard := local.NodeChecksRoot(member)
	require.True(t, heard, "three hundred and sixty strangers did not evict a member's note")
	require.True(t, checks)
}
