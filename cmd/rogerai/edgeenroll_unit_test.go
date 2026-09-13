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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/tui"
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

func TestTheBindAddressesAreThisMachinesToPin(t *testing.T) {
	require.Equal(t, ":0", edgeFaceBind())
	require.Equal(t, ":0", edgeAuthorityBind())
	t.Setenv(envFaceBind, "127.0.0.1:19443")
	t.Setenv(envAuthorityBind, "127.0.0.1:19444")
	require.Equal(t, "127.0.0.1:19443", edgeFaceBind())
	require.Equal(t, "127.0.0.1:19444", edgeAuthorityBind())
}

func TestAMachineThatHasNotEnrolledServesNothingAndIsNamedByItsStation(t *testing.T) {
	useTempConfig(t)
	t.Setenv(edge.EnvDiscovery, "0")
	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(h.stop)

	require.Empty(t, h.enrolledName(), "a machine that never joined an Edge has no name on one")
	require.Empty(t, h.authorityAddr(), "it is nobody's authority")
	require.Nil(t, h.authorityIssuer())
	require.False(t, h.serving())
	_, ok := h.startFace()
	require.False(t, ok, "there is nothing true to advertise yet")

	var hooks tui.Hooks
	h.wire(&hooks)
	require.Equal(t, "roger-desk", hooks.EdgeSelf, "so the Station name stands")
}

func TestAnEnrolledMachineServesItsOwnIdentityAndIsNamedByIt(t *testing.T) {
	offline(t)
	t.Setenv(edge.EnvDiscovery, "0")
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	_, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code)

	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	t.Cleanup(func() { h.closeListeners() })

	require.Equal(t, "workshop", h.enrolledName())
	var hooks tui.Hooks
	h.wire(&hooks)
	require.Equal(t, "workshop", hooks.EdgeSelf, "the owner's name for it beats the Station label")

	self, ok := h.startFace()
	require.True(t, ok)
	require.True(t, h.serving())
	require.NotEmpty(t, self.NodeID)
	require.Equal(t, edgeauth.LocalAccount, self.Account)
	require.NotEmpty(t, self.Fingerprint, "an advertisement commits it to a certificate")
	require.Positive(t, self.Port)

	// A second call binds no second socket.
	_, again := h.startFace()
	require.False(t, again)

	// And the designated machine serves enrollment on the LAN, so a second machine can
	// join an Edge that has never had internet.
	h.startAuthority()
	require.NotEmpty(t, h.authorityAddr())
	require.NotNil(t, h.authorityIssuer())
	addr := h.authorityAddr()
	h.startAuthority()
	require.Equal(t, addr, h.authorityAddr(), "re-arming binds no second socket")

	h.closeListeners()
	require.False(t, h.serving())
}

func TestASecondAuthorityIsRefusedAndAnUnreachableOneIsNot(t *testing.T) {
	offline(t)
	st := edgeIdentityStore()

	// Nothing held yet: whichever authority the owner names becomes this Edge's.
	require.NoError(t, edgeRefuseASecondAuthority(st, "http://127.0.0.1:1"))

	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	_, code = edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 0, code)

	// An authority that cannot say whose root it is is not evidence of anything; the
	// response check still refuses a root that is not ours.
	require.NoError(t, edgeRefuseASecondAuthority(st, "http://127.0.0.1:1"))

	// Its OWN authority, served for real, is not a second one.
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	mine := httptest.NewServer(enrollhttp.Handler(local))
	defer mine.Close()
	require.NoError(t, edgeRefuseASecondAuthority(st, mine.URL))

	// Somebody else's is.
	other, err := edgeauth.Designate(t.TempDir(), "annex")
	require.NoError(t, err)
	theirs := httptest.NewServer(enrollhttp.Handler(other))
	defer theirs.Close()
	err = edgeRefuseASecondAuthority(st, theirs.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), `the designated machine "shed"`)
	require.Contains(t, err.Error(), "exactly one root")
}

// A machine whose Edge directory is not a directory: every path that has to read or
// write it says so, rather than treating "I could not look" as "there is nothing here".
// An Edge that reports itself as empty because the disk is broken is the one failure
// this layer must never produce.
func TestABrokenEdgeDirectoryIsSaidOutLoudAndNeverReadAsAnEmptyEdge(t *testing.T) {
	offline(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath()), 0o700))
	require.NoError(t, os.WriteFile(edgeAuthDir(), []byte("not a directory"), 0o600))

	for _, args := range [][]string{
		{"edge", "authority"},
		{"edge", "authority", "local", "shed"},
		{"edge", "authority", "allow", strings.Repeat("aa", 32)},
		{"edge", "enroll", "workshop"},
	} {
		out, code := edgeRun(t, args...)
		require.NotEqual(t, 0, code, "roger %v answered as if nothing were wrong:\n%s", args, out)
	}
}

// An Edge record that cannot be READ is not an Edge rooted at Core. Saying so would be
// a confident lie the owner would act on.
func TestAnUnreadableAuthorityRecordIsUnknownAndNotCore(t *testing.T) {
	offline(t)
	require.NoError(t, os.MkdirAll(edgeAuthDir(), 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(edgeAuthDir(), edgeauth.DescriptorFile), []byte("{"), 0o600))

	out, code := edgeRun(t, "edge", "authority")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "could not be read")
	require.NotContains(t, out, "this Edge is rooted at Core")
}

// An Edge that cannot be WRITTEN must not report success. A machine that says it
// enrolled and then cannot show the node it enrolled is worse than one that refused.
func TestAnEdgeThatCannotBeWrittenIsNotReportedAsEnrolled(t *testing.T) {
	offline(t)
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	// The state file's path taken by a directory: the same shape as the corrupted
	// install TestEdgeHostRefusesToInventAFleetItCouldNotRead already guards against.
	require.NoError(t, os.MkdirAll(edgeStatePath(), 0o700))

	out, code := edgeRun(t, "edge", "enroll", "workshop")
	require.Equal(t, 1, code, out)

	out, code = edgeRun(t, "edge", "authority", "local", "annex", "--force")
	require.Equal(t, 1, code, out)
}
