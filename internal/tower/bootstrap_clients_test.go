package tower

// bootstrap_clients_test.go covers the standalone plane's MULTI-CLIENT admission and
// revocation (features/tower/standalone_consumer_plane.feature: "More than one client can be
// admitted" and "A client can be cut off locally, and only that client"). The first draft
// admitted exactly one permanent operator; a private network needs several operators and
// agents, each independently admitted and independently revocable, with a consumed
// invitation staying dead across a revoke and across restarts.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// admitOne mints an invitation for a client key and consumes it, returning the invitation id.
func admitOne(t *testing.T, st *State, clientKey string) string {
	t.Helper()
	inv, code, err := st.CreateInvitation(clientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, clientKey)
	require.NoError(t, err, "a fresh invitation admits its client")
	return inv.ID
}

func TestMultipleClientsCanBeAdmitted(t *testing.T) {
	st, _ := newBootstrapStore(t)
	admitOne(t, st, "client-alice")
	admitOne(t, st, "client-bob")
	admitOne(t, st, "client-carol")

	for _, k := range []string{"client-alice", "client-bob", "client-carol"} {
		require.True(t, st.IsAdmitted(k), "%s must be admitted", k)
	}
	require.False(t, st.IsAdmitted("client-mallory"), "a key that never consumed an invitation is not admitted")

	admitted := st.AdmittedClients()
	require.Len(t, admitted, 3)
}

func TestFirstAdmittedIsTheOperator(t *testing.T) {
	st, _ := newBootstrapStore(t)
	admitOne(t, st, "client-alice")
	admitOne(t, st, "client-bob")

	op, err := st.LocalOperator()
	require.NoError(t, err)
	require.Equal(t, "client-alice", op.ClientKeyHash, "the FIRST admitted client is the operator")
}

func TestReadmittingAnAlreadyAdmittedClientIsRejected(t *testing.T) {
	st, _ := newBootstrapStore(t)
	admitOne(t, st, "client-alice")
	// A second invitation for the SAME already-admitted key is refused (uniform rejection).
	inv, code, err := st.CreateInvitation("client-alice", time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, "client-alice")
	require.Error(t, err, "a client already admitted cannot be admitted twice")
}

func TestRevokeCutsOffOnlyThatClient(t *testing.T) {
	st, dir := newBootstrapStore(t)
	invAlice := admitOne(t, st, "client-alice")
	admitOne(t, st, "client-bob")

	require.NoError(t, st.RevokeClient("client-alice"))
	require.False(t, st.IsAdmitted("client-alice"), "the revoked client is cut off")
	require.True(t, st.IsAdmitted("client-bob"), "every other client is unaffected")

	// The consumed invitation stays DEAD across the revoke - a revoke is not a re-admit path.
	invRec, err := st.Invitation(invAlice)
	require.NoError(t, err)
	require.True(t, invRec.Consumed, "the revoked client's invitation stays consumed")

	// And across a restart: reopen the same directory.
	st2, err := Open(dir)
	require.NoError(t, err)
	require.False(t, st2.IsAdmitted("client-alice"), "revocation is durable across restart")
	require.True(t, st2.IsAdmitted("client-bob"))
}

func TestRevokingAnUnknownClientIsANoOp(t *testing.T) {
	st, _ := newBootstrapStore(t)
	admitOne(t, st, "client-alice")
	require.NoError(t, st.RevokeClient("client-nobody"), "revoking a client that was never admitted is harmless")
	require.True(t, st.IsAdmitted("client-alice"))
}

func TestRouteWorksForAnyAdmittedClientAndRefusesOthers(t *testing.T) {
	st, _ := newBootstrapStore(t)
	admitOne(t, st, "client-alice")
	admitOne(t, st, "client-bob")
	_, err := st.AttachStation("st-1", "sk-1", []string{"llama-8b"})
	require.NoError(t, err)

	// Any admitted client may route.
	for _, k := range []string{"client-alice", "client-bob"} {
		rec, rerr := st.Route(k, "llama-8b")
		require.NoError(t, rerr, "%s is admitted and may route", k)
		require.Equal(t, "st-1", rec.StationID)
	}
	// A non-admitted key is refused.
	_, err = st.Route("client-mallory", "llama-8b")
	require.Error(t, err, "a non-admitted client may not route")
}

// A pre-multi-client network persisted only an Operator, no Clients map. Admission and
// revocation must still recognize that one client without any migration step forced on the
// operator - the network keeps working across the upgrade.
func TestLegacyOperatorOnlySnapshotStillWorks(t *testing.T) {
	st, dir := newBootstrapStore(t)
	// Hand-build the OLD shape: Operator set, Clients nil.
	store := NewFileStore(dir)
	bs, err := store.Load()
	require.NoError(t, err)
	bs.Operator = &Credential{ClientKeyHash: "legacy-op", NetworkID: st.LocalNetworkID, Role: RoleLocalOperator}
	bs.Clients = nil
	_, err = store.Save(bs)
	require.NoError(t, err)

	// Reopen and confirm the legacy operator is recognized as admitted.
	st2, err := Open(dir)
	require.NoError(t, err)
	require.True(t, st2.IsAdmitted("legacy-op"), "the legacy operator is admitted without a migration write")
	require.Len(t, st2.AdmittedClients(), 1)

	// A new client can join beside it, and the legacy operator can be revoked.
	admitOne(t, st2, "new-client")
	require.True(t, st2.IsAdmitted("new-client"))
	require.NoError(t, st2.RevokeClient("legacy-op"))
	require.False(t, st2.IsAdmitted("legacy-op"))
	require.True(t, st2.IsAdmitted("new-client"), "revoking the legacy operator leaves other clients")
}

// The role a client gets is decided at admission, not by the invitation: first is operator,
// the rest are plain local clients with no admin authority.
func TestAdmittedRolesFirstOperatorRestClients(t *testing.T) {
	st, _ := newBootstrapStore(t)
	inv, code, err := st.CreateInvitation("first", time.Hour, 5)
	require.NoError(t, err)
	c1, err := st.ConsumeInvitation(inv.ID, code, "first")
	require.NoError(t, err)
	require.Equal(t, RoleLocalOperator, c1.Role, "the first admitted client is the operator")

	inv2, code2, err := st.CreateInvitation("second", time.Hour, 5)
	require.NoError(t, err)
	c2, err := st.ConsumeInvitation(inv2.ID, code2, "second")
	require.NoError(t, err)
	require.Equal(t, RoleLocalClient, c2.Role, "a later client is a plain local client, not an admin")
}

// Retiring the operator does NOT leave an outstanding invitation as a silent admin-appointment
// path: once the operator is revoked, the network admits no new client until re-init.
func TestNoNewAdmissionAfterOperatorRevoked(t *testing.T) {
	st, _ := newBootstrapStore(t)
	admitOne(t, st, "op")
	// Mint an invitation for a would-be new client BEFORE revoking the operator.
	inv, code, err := st.CreateInvitation("late", time.Hour, 5)
	require.NoError(t, err)

	require.NoError(t, st.RevokeClient("op"))

	// The outstanding invitation must NOT admit a new (operator) client.
	_, err = st.ConsumeInvitation(inv.ID, code, "late")
	require.Error(t, err, "a retired-operator network admits nobody until re-init")
	require.False(t, st.IsAdmitted("late"))
}

// A revoked client holding an unused code cannot walk back in: revoke closes open invitations
// bound to the revoked key.
func TestRevokeKillsTheRevokedClientsOpenInvitation(t *testing.T) {
	st, _ := newBootstrapStore(t)
	admitOne(t, st, "op") // operator, so the network stays admin-capable
	admitOne(t, st, "client")
	// A SECOND, still-open invitation was minted for the client (e.g. for key rotation).
	inv, code, err := st.CreateInvitation("client", time.Hour, 5)
	require.NoError(t, err)

	require.NoError(t, st.RevokeClient("client"))
	require.False(t, st.IsAdmitted("client"))

	// The still-open invitation is now dead - the revoked client cannot re-admit itself.
	_, err = st.ConsumeInvitation(inv.ID, code, "client")
	require.Error(t, err, "a revoke kills the revoked client's open invitations")
	require.False(t, st.IsAdmitted("client"))
}

// Revoking a LEGACY operator (a pre-multi-client snapshot: Operator set, Clients nil,
// Bootstrapped false) must not leave the network looking fresh - otherwise an outstanding
// invitation would silently re-appoint an admin. Revoke asserts Bootstrapped durably.
func TestRevokingLegacyOperatorDoesNotReopenAdmission(t *testing.T) {
	st, dir := newBootstrapStore(t)
	// Build the legacy shape directly: Operator set, Clients nil, Bootstrapped false.
	store := NewFileStore(dir)
	bs, err := store.Load()
	require.NoError(t, err)
	bs.Operator = &Credential{ClientKeyHash: "legacy-op", NetworkID: st.LocalNetworkID, Role: RoleLocalOperator}
	bs.Clients = nil
	bs.Bootstrapped = false
	// An invitation is outstanding for a would-be admin.
	inv, code, err := st.CreateInvitation("intruder", time.Hour, 5)
	require.NoError(t, err)
	// Re-save the legacy shape (CreateInvitation above wrote its own; reassert the legacy fields).
	bs2, err := store.Load()
	require.NoError(t, err)
	bs2.Operator = &Credential{ClientKeyHash: "legacy-op", NetworkID: st.LocalNetworkID, Role: RoleLocalOperator}
	bs2.Clients = nil
	bs2.Bootstrapped = false
	_, err = store.Save(bs2)
	require.NoError(t, err)

	st2, err := Open(dir)
	require.NoError(t, err)
	require.NoError(t, st2.RevokeClient("legacy-op"))

	// The outstanding invitation must be refused: a revoked-down-to-nothing network is not fresh.
	_, err = st2.ConsumeInvitation(inv.ID, code, "intruder")
	require.Error(t, err, "revoking a legacy operator must not reopen admission to an outstanding invitation")
	require.False(t, st2.IsAdmitted("intruder"))
}
