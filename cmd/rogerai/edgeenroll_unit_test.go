package main

// The corners of `roger edge enroll` / `roger edge authority` that the executable spec
// reaches through, rather than at: the offline authority machine enrolling ITSELF with
// no login and no address, coming back after a migration, handing the Edge back to
// Core, and the usage mistakes.
//
// The first of these is a REGRESSION TEST. A live run of the built binary found both:
// the login check fired on the very machine that holds the root (so an airgap Edge
// could not be formed unless the owner typed their own address back to themselves), and
// re-enrolling after a migration was refused the name the member was still listed
// under. A hundred green scenarios had missed both, because every one of them passed
// --authority.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
)

// offline is a machine with no login and no route anywhere.
func offline(t *testing.T) {
	t.Helper()
	useTempConfig(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
}

func TestTheAuthorityMachineEnrollsItselfWithNoLoginAndNoAddress(t *testing.T) {
	offline(t)

	out, code := edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 2, code, "with no login and no authority there is no Edge to join")
	require.Contains(t, out, "log in first")

	out, code = edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "private half stays on this machine")

	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, `enrolled "workshop"`)
	require.Contains(t, out, `the designated machine "shed"`)

	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, edgeauth.LocalAccount, id.Account)

	// Nothing crossed a wire, so nothing about the certificate needed one.
	verifier, _, err := edgeIdentityStore().Trust(time.Now())
	require.NoError(t, err)
	_, err = verifier.Authenticate(id.Cert)
	require.NoError(t, err)

	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "nothing to do", "a second enroll is a clean no-op")
}

func TestAMemberToldToReenrollCanActuallyReenroll(t *testing.T) {
	offline(t)
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	_, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code)
	before, _, _, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)

	out, code := edgeRun(t, "edge", "authority", "local", "annex", "--force")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "must re-enroll")

	st, err := loadEdgeState()
	require.NoError(t, err)
	list, err := st.fleet.List()
	require.NoError(t, err)
	require.Len(t, list, 1, "members are not silently dropped")
	require.Equal(t, string(edge.PresenceReenroll), list[0].Presence)

	out, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code, out)
	after, _, _, err := edgeIdentityStore().LoadIdentity()
	require.NoError(t, err)
	require.NotEqual(t, before.NodeID, after.NodeID, "a new root means a new identity")
	require.False(t, before.Root.Equal(after.Root))

	st, err = loadEdgeState()
	require.NoError(t, err)
	list, err = st.fleet.List()
	require.NoError(t, err)
	require.Len(t, list, 1, "the member came back, it did not arrive beside itself")
	require.Equal(t, "workshop", list[0].Name)
	require.Equal(t, string(edge.PresenceVerified), list[0].Presence)
}

func TestMovingAnEdgeBackToCoreIsDeliberate(t *testing.T) {
	offline(t)

	out, code := edgeRun(t, "edge", "authority", "core")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "already rooted at Core")

	_, code = edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)

	out, code = edgeRun(t, "edge", "authority", "core")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "every member re-enrolls")
	require.Contains(t, out, "--force")
	_, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok, "a refusal changes nothing")

	out, code = edgeRun(t, "edge", "authority", "core", "--force")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "rooted at Core")
	_, ok, err = edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.False(t, ok, "the root it was leaving is gone")
	require.Equal(t, edgeauth.KindCore, edgeDescriptor().Kind)
}

func TestTheAuthorityCommandRefusesWhatItCannotDo(t *testing.T) {
	offline(t)

	out, code := edgeRun(t, "edge", "authority", "frobnicate")
	require.Equal(t, 2, code)
	require.Contains(t, out, "roger edge authority takes")

	out, code = edgeRun(t, "edge", "authority", "allow")
	require.Equal(t, 2, code)
	require.Contains(t, out, "usage:")

	out, code = edgeRun(t, "edge", "authority", "allow", strings.Repeat("aa", 32))
	require.Equal(t, 1, code)
	require.Contains(t, out, "not an Edge authority")

	_, code = edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)

	out, code = edgeRun(t, "edge", "authority", "allow", "not-a-key")
	require.Equal(t, 2, code)
	require.Contains(t, out, "not a machine's user key")

	out, code = edgeRun(t, "edge", "authority", "allow", strings.Repeat("aa", 32))
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "may now enroll")

	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	allowed, err := local.Allowed()
	require.NoError(t, err)
	require.Len(t, allowed, 2, "the designating machine, and the one just allowed")

	out, code = edgeRun(t, "edge", "authority", "local", "annex")
	require.Equal(t, 1, code)
	require.Contains(t, out, "already holds an Edge root")

	out, code = edgeRun(t, "edge", "enroll", "..")
	require.Equal(t, 2, code)
	require.Contains(t, out, "not a usable node name")
}

func TestADefaultNameIsThisMachinesOwn(t *testing.T) {
	require.NoError(t, edge.ValidName(edgeDefaultName()))
	require.Equal(t, "127.0.0.1:9443", edgeAuthorityLabel("http://127.0.0.1:9443/"))
	require.Equal(t, "not a url", edgeAuthorityLabel("not a url"))
}
