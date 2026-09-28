package edge

// Executable pins for features/edge/maintenance.feature: the capability, the task vocabulary, and
// what the face does with a maintenance invoke.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

func TestMaintainIsACapabilityWithTheOwnersLockAsItsGate(t *testing.T) {
	require.True(t, Maintain.Valid())
	require.NotEmpty(t, Maintain.VerificationMethod())
	require.True(t, DeclarationGated(Maintain), "no probe verifies it; the owner's switch on that machine does")
	require.Equal(t, string(Claimed), string(initialState(Maintain)))
	require.True(t, Host.MayDeclare(Maintain))
	require.False(t, Board.MayDeclare(Maintain), "a board runs firmware, not rogers")

	// A node that declares no maintain is never maintained, whatever the grant says. (The grant's
	// own capability scope is enforced by the callee's host wiring, before this runs: the caps check
	// in cmd/rogerai/edgeinstance.go, pinned by TestMaintenanceNeedsBothOfTheOwnersLocks.)
	f := NewFleet(store.NewMem(), "acct-1")
	_, err := f.Enroll(store.EdgeNode{ID: "n_shed", Name: "shed", Kind: string(Host), Account: "acct-1",
		Caps: []store.EdgeCap{{Name: string(Operate), State: string(Claimed)}}})
	require.NoError(t, err)
	g := store.Grant{Owner: "acct-1", Caller: "n_desk", Nodes: []string{"n_shed"}, Caps: []string{string(Maintain)}}
	require.ErrorIs(t, f.AuthorizeInvoke(g, "n_shed", Maintain), ErrNotVerified)

	// Declaring it is enough at the fleet level; the owner's switch on that machine is the other lock.
	_, err = f.Observe("n_shed", Observation{Name: "shed", Kind: string(Host), Addr: "10.0.0.3:1", Fingerprint: "ab",
		Caps: []Capability{Operate, Maintain}, Instances: []Instance{{Name: "shed",
			Caps: []store.EdgeCap{{Name: string(Operate), State: string(Claimed)}, {Name: string(Maintain), State: string(Claimed)}}}}})
	require.NoError(t, err)
	require.NoError(t, f.AuthorizeInvoke(g, "n_shed", Maintain))
}

func TestMaintenanceTaskRefusesEverythingItCannotMean(t *testing.T) {
	raw := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	good, refused := ParseMaintainTask(raw(MaintainTask{Action: "START", Target: " watcher "}))
	require.Equal(t, "", refused)
	require.Equal(t, MaintainStart, good.Action)
	require.Equal(t, "watcher", good.Target, "normalised before anything is looked up")

	cases := []struct {
		name string
		task json.RawMessage
		says string
	}{
		{"unreadable", json.RawMessage(`{`), "could not be read"},
		{"no action", raw(MaintainTask{Target: "watcher"}), "names an action"},
		{"no target", raw(MaintainTask{Action: MaintainStart}), "names the agent"},
		{"bad name", raw(MaintainTask{Action: MaintainStart, Target: "not a name"}), "not an agent name"},
		{"unknown", raw(MaintainTask{Action: "frobnicate", Target: "watcher"}), "is not a maintenance action"},
		{"stop", raw(MaintainTask{Action: MaintainStop, Target: "watcher"}), "not built yet"},
		{"restart", raw(MaintainTask{Action: MaintainRestart, Target: "watcher"}), "not built yet"},
		{"reboot", raw(MaintainTask{Action: MaintainReboot, Target: "watcher"}), "not built yet"},
		{"shutdown", raw(MaintainTask{Action: MaintainShutdown, Target: "watcher"}), "not built yet"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, says := ParseMaintainTask(c.task)
			require.Contains(t, says, c.says)
		})
	}
}
