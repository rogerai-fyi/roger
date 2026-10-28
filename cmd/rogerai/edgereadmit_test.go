package main

// RE-ADMITTING A FORGOTTEN DEVICE (features/edge/node_identity.feature, approved 2026-10-02), as the
// CLI, the fleet screen and the console host do it: every forget leaves the short record, a re-admit
// resolves the name it had and asks in words, and the device comes back under that name. The domain
// rules themselves run under godog in internal/edge.

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/store"
)

func atticPi4(id, name string) store.EdgeNode {
	return store.EdgeNode{ID: id, Name: name, Kind: "host", Pin: strings.Repeat("cd", 32), Tags: []string{"loft"},
		Presence:  string(edge.PresenceDark),
		Instances: []store.EdgeInstance{{Name: "roger", Software: &store.EdgeSoftware{Machine: "Raspberry Pi 4 Model B"}}}}
}

func departedNow(t *testing.T) []edge.Departed {
	t.Helper()
	list, err := edgeLoadDeparted()
	require.NoError(t, err)
	return list
}

func TestEveryForgetPathLeavesTheShortRecord(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, atticPi4("n_attic", "attic"), atticPi4("n_shed", "shed"), atticPi4("n_loft", "loft"))

	// 1. The CLI.
	out, code := edgeRun(t, "edge", "forget", "attic", "--yes")
	require.Equal(t, 0, code, out)
	list := departedNow(t)
	require.Len(t, list, 1)
	require.Equal(t, "n_attic", list[0].ID)
	require.Equal(t, "attic", list[0].Name)
	require.Equal(t, "host", list[0].Kind)
	require.Equal(t, "Pi 4", list[0].Hardware)
	require.InDelta(t, time.Now().Unix(), list[0].At, 5)

	// The file is the owner's alone, and holds nothing else of the device.
	fi, err := os.Stat(edgeDepartedPath())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	raw, err := os.ReadFile(edgeDepartedPath())
	require.NoError(t, err)
	require.NotContains(t, string(raw), strings.Repeat("cd", 32), "not its pin")
	require.NotContains(t, string(raw), "loft\"", "not its tags")
	require.NotContains(t, string(raw), "enrolled", "not its history")

	// 2. The fleet screen.
	require.NoError(t, edgeFleetActions().Forget("shed"))
	// 3. The console / TUI host.
	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	require.NoError(t, h.forgetNode("n_loft"))

	names := map[string]bool{}
	for _, d := range departedNow(t) {
		names[d.Name] = true
	}
	require.Equal(t, map[string]bool{"attic": true, "shed": true, "loft": true}, names)

	// 4. A device adopted but not yet claimed, forgotten by the name its grant carried.
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim("n_porch", "porch"))
	out, code = edgeRun(t, "edge", "forget", "porch", "--yes")
	require.Equal(t, 0, code, out)
	d, ok, err := edge.FindDeparted(departedNow(t), "porch", time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "n_porch", d.ID)

	// Saying no keeps nothing: nothing was forgotten.
	edgeSeed(t, atticPi4("n_barn", "barn"))
	prevIn := edgeStdin
	t.Cleanup(func() { edgeStdin = prevIn })
	edgeStdin = strings.NewReader("n\n")
	out, code = edgeRun(t, "edge", "forget", "barn")
	require.Equal(t, 0, code, out)
	_, ok, _ = edge.FindDeparted(departedNow(t), "barn", time.Now())
	require.False(t, ok)
}

func TestTheDepartedRecordIsBoundedAndPerAccount(t *testing.T) {
	newFacedAuthority(t, "desk")
	for i := 0; i < edge.MaxDeparted+3; i++ {
		require.NoError(t, edgeRecordDeparted(store.EdgeNode{ID: "n_" + strings.Repeat("a", i+1), Name: "d", Kind: "board"}))
	}
	require.Len(t, departedNow(t), edge.MaxDeparted)

	// A record belonging to another account is not this Edge's to show.
	require.NoError(t, os.WriteFile(edgeDepartedPath(), []byte(`{"account":"someone-else","departed":[{"id":"n_x","name":"x","at":1}]}`), 0o600))
	require.Empty(t, departedNow(t))
	// And a corrupt one is said, not silently emptied.
	require.NoError(t, os.WriteFile(edgeDepartedPath(), []byte(`{`), 0o600))
	_, err := edgeLoadDeparted()
	require.Error(t, err)
	out, code := edgeRun(t, "edge", "readmit")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "could not read the record of forgotten devices")
}

func TestReadmitByTheNameItHad(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, atticPi4("n_attic", "attic"))
	out, code := edgeRun(t, "edge", "forget", "attic", "--yes")
	require.Equal(t, 0, code, out)
	backdateDeparted(t, 3*24*time.Hour)

	prevIn := edgeStdin
	t.Cleanup(func() { edgeStdin = prevIn })
	edgeStdin = strings.NewReader("y\n")
	out, code = edgeRun(t, "edge", "readmit", "attic")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "re-admit attic, a Pi 4 forgotten 3 days ago? its previous certificate is revoked")
	require.Contains(t, out, "re-admitted attic - its old certificate is revoked")
	require.NotContains(t, out, "n_attic")

	// The claim it was granted carries the name it had.
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	grants, err := local.Claims()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, edgeauth.ClaimGrant{NodeID: "n_attic", Name: "attic", At: grants[0].At}, grants[0])

	// When it checks in with its fresh certificate it comes back named "attic", whatever it calls itself.
	st, err := loadEdgeState()
	require.NoError(t, err)
	got, err := st.fleet.Observe("n_attic", edge.Observation{Name: "raspberrypi", Kind: "host", Addr: "192.168.1.40:4181",
		Fingerprint: strings.Repeat("ef", 32)})
	require.NoError(t, err)
	require.Equal(t, "attic", got.Name)
	require.NoError(t, st.save())

	// Back on the Edge, it is no longer offered for re-admitting.
	out, code = edgeRun(t, "edge", "readmit")
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "attic,")
}

func TestReadmitAnIDThisEdgeHasNoRecordOf(t *testing.T) {
	newFacedAuthority(t, "desk")
	prevIn := edgeStdin
	t.Cleanup(func() { edgeStdin = prevIn })
	edgeStdin = strings.NewReader("y\n")
	out, code := edgeRun(t, "edge", "readmit", "n_gone")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, `re-admit the device you typed as "n_gone"? this Edge has no record of it; any certificate it holds from this Edge is revoked`)
	require.Contains(t, out, `re-admitted the device you typed as "n_gone" - any certificate it held is revoked; it can claim a fresh one and rejoin, and it has no name until you give it one.`)
	require.NotContains(t, out, "re-admitted n_gone")

	// It is admitted with no name: never the id it was typed as.
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	grants, err := local.Claims()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, "n_gone", grants[0].NodeID)
	require.Equal(t, "", grants[0].Name)

	// Something typed that is neither a forgotten name nor an id is a mistake, not a device.
	out, code = edgeRun(t, "edge", "readmit", "atic", "--yes")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, `no forgotten device is called "atic"`)
	require.Contains(t, out, "roger edge readmit")
}

func TestReadmitWithNothingNamedListsWhatCanBeReAdmitted(t *testing.T) {
	newFacedAuthority(t, "desk")

	out, code := edgeRun(t, "edge", "readmit")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "no forgotten devices on this Edge")

	unnamed := "n_0123456789abcdef0123456789abcdef01234567"
	edgeSeed(t, atticPi4("n_attic", "attic"), store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "board"},
		store.EdgeNode{ID: unnamed, Name: unnamed, Kind: "mobile"})
	for _, w := range []string{"attic", "shed", unnamed} {
		out, code = edgeRun(t, "edge", "forget", w, "--yes")
		require.Equal(t, 0, code, out)
	}
	// Spread them out so newest-first is observable.
	list := departedNow(t)
	for i := range list {
		list[i].At = time.Now().Add(-time.Duration(10-i) * time.Hour).Unix()
	}
	require.NoError(t, edgeSaveDeparted(list))

	out, code = edgeRun(t, "edge", "readmit")
	require.Equal(t, 0, code, out)
	iPhone := strings.Index(out, "a phone forgotten 8 hours ago")
	iShed := strings.Index(out, "shed, a board forgotten 9 hours ago")
	iAttic := strings.Index(out, "attic, a Pi 4 forgotten 10 hours ago")
	require.True(t, iPhone >= 0 && iShed > iPhone && iAttic > iShed, "newest first:\n%s", out)
	require.Contains(t, out, "roger edge readmit attic")
	require.Contains(t, out, "roger edge readmit "+unnamed, "an unnamed device is re-admitted by its id, as an argument")
	require.Contains(t, out, "re-admit one with `roger edge readmit <name>`")
}

// The fleet screen's and the console's re-admit go through a routine adopt, which sends a forgotten
// (revoked) device to the deliberate readmit - naming it the same way, by the name it had.
func TestARoutineAdoptOfAForgottenDeviceNamesItByTheNameItHad(t *testing.T) {
	newFacedAuthority(t, "desk")
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	id := forgetAClaimedPhone(t, local)
	require.NoError(t, edgeRecordDeparted(store.EdgeNode{ID: id, Name: "pocket", Kind: "mobile"}))
	err = edgeAdoptByClaim(id, "iPhone")
	require.Error(t, err)
	require.Contains(t, err.Error(), "pocket was removed and its certificate revoked")
	require.Contains(t, err.Error(), "roger edge readmit pocket")
	require.NotContains(t, err.Error(), id)
}

// backdateDeparted moves every record back by d, as if it had been forgotten that long ago.
func backdateDeparted(t *testing.T, d time.Duration) {
	t.Helper()
	list := departedNow(t)
	for i := range list {
		list[i].At -= int64(d / time.Second)
	}
	require.NoError(t, edgeSaveDeparted(list))
}

// forgetAClaimedPhone adopts and claims a real certificate for a fresh phone key, then forgets it
// at the authority (its serial is revoked), and returns its node id.
func forgetAClaimedPhone(t *testing.T, local *edgeauth.Local) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	id := edgeauth.NodeID(pub)
	require.NoError(t, local.GrantClaim(id, "pocket"))
	c := edgeauth.NewClaimRequest("pocket", "mobile", pub, time.Now())
	c.Sign(priv)
	_, err = local.Claim(c, time.Now())
	require.NoError(t, err)
	_, _, err = local.Forget(id)
	require.NoError(t, err)
	return id
}

// SECURITY (review 2026-10-02): a candidate's name is what a device ANNOUNCES about itself, so a LAN
// device calling itself "attic" must never be what `roger edge readmit attic` - the very command the
// list tells the owner to run - re-admits. The forgotten device's record decides, and a different
// device carrying the name makes the name ambiguous: refused, with the way to pick.
func TestAnAnnouncingCandidateNamedLikeAForgottenDeviceNeverWins(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, atticPi4("n_attic", "attic"))
	out, code := edgeRun(t, "edge", "forget", "attic", "--yes")
	require.Equal(t, 0, code, out)
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = append(st.candidates, store.EdgeNode{ID: "n_impostor", Name: "attic", Kind: "mobile"})
	require.NoError(t, st.save())

	out, code = edgeRun(t, "edge", "readmit", "attic", "--yes")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, `"attic" is also what a device on this network calls itself`)
	require.Contains(t, out, "a Pi 4 forgotten just now")
	require.Contains(t, out, "`roger edge readmit n_attic`")
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.False(t, local.ClaimGranted("n_impostor"), "the announcing device was granted a claim")
	require.False(t, local.ClaimGranted("n_attic"), "an ambiguous name re-admits nothing")

	// The list does not offer the name either: it offers the id, as the argument.
	out, code = edgeRun(t, "edge", "readmit")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "roger edge readmit n_attic")
	require.NotContains(t, out, "roger edge readmit attic")

	// Picking it by its id re-admits the forgotten device, under the name it had.
	out, code = edgeRun(t, "edge", "readmit", "n_attic", "--yes")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "re-admitted attic")
	require.True(t, local.ClaimGranted("n_attic"))
	require.False(t, local.ClaimGranted("n_impostor"))

	// The same device announcing its own old name is no conflict.
	newFacedAuthority(t, "desk")
	edgeSeed(t, atticPi4("n_attic", "attic"))
	out, code = edgeRun(t, "edge", "forget", "attic", "--yes")
	require.Equal(t, 0, code, out)
	st, err = loadEdgeState()
	require.NoError(t, err)
	st.candidates = append(st.candidates, store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "host"})
	require.NoError(t, st.save())
	out, code = edgeRun(t, "edge", "readmit", "attic", "--yes")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "re-admitted attic")
}

// SECURITY (review 2026-10-02): forget A ("attic"), name a new device C "attic": `readmit attic`
// must never resolve to C and revoke its live certificate.
func TestAMemberThatTookTheOldNameIsNeverRevokedByReadmit(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, atticPi4("n_attic", "attic"))
	out, code := edgeRun(t, "edge", "forget", "attic", "--yes")
	require.Equal(t, 0, code, out)

	// C claims a real certificate and joins as "attic".
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	cID := edgeauth.NodeID(pub)
	require.NoError(t, local.GrantClaim(cID, "attic"))
	req := edgeauth.NewClaimRequest("attic", "host", pub, time.Now())
	req.Sign(priv)
	resp, err := local.Claim(req, time.Now())
	require.NoError(t, err)
	leaf, err := edgeauth.DecodeCert(resp.Cert)
	require.NoError(t, err)
	edgeSeed(t, store.EdgeNode{ID: cID, Name: "attic", Kind: "host", Presence: string(edge.PresenceVerified)})

	for _, args := range [][]string{{"edge", "readmit", "attic", "--yes"}, {"edge", "readmit", "attic"}} {
		out, code = edgeRun(t, args...)
		require.Equal(t, 1, code, out)
		require.Contains(t, out, `"attic" is also the name of a member of this Edge now`)
		require.Contains(t, out, "`roger edge readmit n_attic`")
		require.NotContains(t, out, cID)
	}
	revs, err := local.Revocations()
	require.NoError(t, err)
	require.NotContains(t, revs, leaf.SerialNumber.String(), "the member that took the name had its certificate revoked")
	require.False(t, local.ClaimGranted(cID))

	out, code = edgeRun(t, "edge", "readmit")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "roger edge readmit n_attic")
	require.NotContains(t, out, "roger edge readmit attic")
}

// Two forgotten devices that had one name: the refusal says each by what it is and when it left.
func TestTwoForgottenDevicesWithOneNameAreToldApart(t *testing.T) {
	newFacedAuthority(t, "desk")
	require.NoError(t, edgeRecordDeparted(atticPi4("n_attic", "attic")))
	require.NoError(t, edgeRecordDeparted(store.EdgeNode{ID: "n_attic2", Name: "attic", Kind: "mobile"}))
	out, code := edgeRun(t, "edge", "readmit", "attic", "--yes")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, `"attic" names 2 forgotten devices - re-admit the one you mean: `)
	require.Contains(t, out, "a Pi 4 forgotten just now (`roger edge readmit n_attic`)")
	require.Contains(t, out, "a phone forgotten just now (`roger edge readmit n_attic2`)")
	out, code = edgeRun(t, "edge", "readmit")
	require.Equal(t, 0, code, out)
	require.NotContains(t, out, "roger edge readmit attic\n")
	require.Contains(t, out, "roger edge readmit n_attic2")
}

// Pre-push audit (2026-10-26, minor): with no forgotten device by that name, `readmit attic` fell
// through to a device on the network calling itself attic and asked "re-admit attic?" as if it were
// one this Edge knew. A name a device announces is only what it says about itself, so the question
// says so.
func TestReadmitByAnAnnouncedNameSaysTheNameIsOnlyAnnounced(t *testing.T) {
	newFacedAuthority(t, "desk")
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{ID: "n_" + strings.Repeat("7a", 24), Name: "attic", Kind: "board"}}
	require.NoError(t, st.save())
	edgeStdin = strings.NewReader("n\n")
	out, code := edgeRun(t, "edge", "readmit", "attic")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing this Edge forgot is called attic")
	require.Contains(t, out, "calls itself attic")
	require.Contains(t, out, "left alone")
}
