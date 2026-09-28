package edge

// Executable pins for features/edge/drift.feature: the expectations, the precedence, and the judging -
// including that UNKNOWN is never counted as drift.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

func sw(v string) *store.EdgeSoftware { return &store.EdgeSoftware{Roger: v} }

// driftFleet: desk is in order against everything, shed against nothing, and unseen has reported
// nothing at all.
func driftFleet() []store.EdgeNode {
	verified := []store.EdgeCap{{Name: string(Serve), State: string(Verified)}}
	return []store.EdgeNode{
		{ID: "n_desk", Name: "desk", Presence: string(PresenceVerified), Tags: []string{"production"},
			Caps: []store.EdgeCap{{Name: string(Maintain), State: string(Claimed)}},
			Instances: []Instance{{Name: "desk", Software: sw("6.13.0"), Agent: true,
				Bands: []string{"qwen"}, ServeProven: []string{"qwen"}, Caps: verified}}},
		{ID: "n_shed", Name: "shed", Presence: string(PresenceVerified), Tags: []string{"production"},
			Instances: []Instance{{Name: "shed", Software: sw("6.9.0")}}},
		{ID: "n_unseen", Name: "unseen", Presence: string(PresenceVerified)},
	}
}

func TestAnExpectationIsRefusedWhenItCannotMeanWhatItSays(t *testing.T) {
	require.NoError(t, ValidateExpectation(Expectation{What: ExpectRoger, Value: "6.13.0"}))
	require.NoError(t, ValidateExpectation(Expectation{What: ExpectServes, Value: "qwen", Scope: TagScope("production")}))
	require.NoError(t, ValidateExpectation(Expectation{What: ExpectAgent}))
	require.NoError(t, ValidateExpectation(Expectation{What: ExpectMaintenance, Scope: "n_desk"}))

	for _, c := range []struct {
		name string
		e    Expectation
		says string
	}{
		{"unknown thing", Expectation{What: "colour", Value: "red"}, "is not something to expect"},
		{"roger with no version", Expectation{What: ExpectRoger}, "names the version"},
		{"roger with a non-version", Expectation{What: ExpectRoger, Value: "newest"}, "is not a version"},
		{"serves with no band", Expectation{What: ExpectServes}, "names it"},
		{"agent with a value", Expectation{What: ExpectAgent, Value: "yes"}, "takes no value"},
		{"a broken tag scope", Expectation{What: ExpectAgent, Scope: "tag:a b"}, "lowercase letters"},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.ErrorContains(t, ValidateExpectation(c.e), c.says)
		})
	}
	// The refusal names the whole vocabulary, so an owner is not left guessing.
	err := ValidateExpectation(Expectation{What: "colour"})
	for _, w := range ExpectWhats() {
		require.Contains(t, err.Error(), string(w))
	}
}

func TestTheMostSpecificExpectationWins(t *testing.T) {
	set := ExpectSet{
		{What: ExpectRoger, Value: "6.13.0"},
		{What: ExpectRoger, Value: "6.9.0", Scope: TagScope("production")},
		{What: ExpectRoger, Value: "6.12.0", Scope: "n_desk"},
	}
	e, ok := set.applying(ExpectRoger, "n_desk", []string{"production"})
	require.True(t, ok)
	require.Equal(t, "6.12.0", e.Value, "the node's own beats the group's")

	e, ok = set.applying(ExpectRoger, "n_shed", []string{"production"})
	require.True(t, ok)
	require.Equal(t, "6.9.0", e.Value, "the group's beats the fleet's")

	e, ok = set.applying(ExpectRoger, "n_other", nil)
	require.True(t, ok)
	require.Equal(t, "6.13.0", e.Value, "a unit in no group is held to the fleet's")

	// Two groups on one unit: the one that asks MORE applies, so a group cannot loosen the other.
	two := ExpectSet{
		{What: ExpectRoger, Value: "6.9.0", Scope: TagScope("production")},
		{What: ExpectRoger, Value: "6.13.0", Scope: TagScope("gpu")},
	}
	for range 5 {
		e, _ = two.applying(ExpectRoger, "n_desk", []string{"production", "gpu"})
		require.Equal(t, "6.13.0", e.Value, "the higher floor asks more")
	}
	// And nothing applies when nothing is scoped to it.
	_, ok = ExpectSet{{What: ExpectAgent, Scope: TagScope("winter")}}.applying(ExpectAgent, "n_desk", []string{"production"})
	require.False(t, ok)
}

func TestDriftIsWhatDoesNotMatchAndUnknownIsNotDrift(t *testing.T) {
	set := ExpectSet{
		{What: ExpectRoger, Value: "6.13.0"},
		{What: ExpectServes, Value: "qwen", Scope: TagScope("production")},
		{What: ExpectAgent, Scope: TagScope("production")},
		{What: ExpectMaintenance},
	}
	out, checked := set.Judge(driftFleet())
	require.Equal(t, 3, checked, "every unit is looked at")

	byNode := map[string][]Drift{}
	for _, d := range out {
		byNode[d.Node] = append(byNode[d.Node], d)
	}
	require.Empty(t, byNode["desk"], "desk matches everything asked of it")

	// shed is held to all four and fails all four, each said in the owner's terms.
	require.Len(t, byNode["shed"], 4)
	found := map[ExpectWhat]Drift{}
	for _, d := range byNode["shed"] {
		require.Equal(t, DriftOut, d.State)
		found[d.What] = d
	}
	require.Equal(t, "runs 6.9.0", found[ExpectRoger].Found)
	require.Equal(t, "roger 6.13.0", found[ExpectRoger].Expected)
	require.Equal(t, "does not serve it", found[ExpectServes].Found)
	require.Equal(t, "serves qwen", found[ExpectServes].Expected)
	require.Equal(t, "runs no agent", found[ExpectAgent].Found)
	require.Equal(t, "maintenance is off", found[ExpectMaintenance].Found)

	// unseen has reported nothing: UNKNOWN against what needs a fact, and never DRIFT. It is not in
	// the production group, so only the fleet-wide ones apply to it.
	require.Len(t, byNode["unseen"], 2)
	for _, d := range byNode["unseen"] {
		require.Equal(t, DriftUnknown, d.State, d.What)
		require.Contains(t, d.Found, "has not reported")
	}

	// An Edge with no expectations drifts from nothing, and that is not the same as being in order -
	// the surfaces say which, and Judge simply has nothing to report.
	out, checked = ExpectSet{}.Judge(driftFleet())
	require.Empty(t, out)
	require.Equal(t, 3, checked)

	// A DARK unit is judged on what it last reported, and the report says it is dark.
	dark := driftFleet()
	dark[1].Presence = string(PresenceDark)
	out, _ = ExpectSet{{What: ExpectRoger, Value: "6.13.0"}}.Judge(dark)
	require.Len(t, out, 2, "the dark unit, and the one that has reported nothing")
	byName := map[string]Drift{}
	for _, d := range out {
		byName[d.Node] = d
	}
	require.True(t, byName["shed"].Dark)
	require.Equal(t, DriftOut, byName["shed"].State, "dark is not an excuse: it ran 6.9.0 when last heard")
	require.Equal(t, DriftUnknown, byName["unseen"].State)
}

func TestExpectationsAreKeptReplacedAndRemoved(t *testing.T) {
	l := AlertLog{Dir: t.TempDir()}
	got, err := l.Expectations()
	require.NoError(t, err)
	require.Empty(t, got)

	require.NoError(t, l.SetExpectation(Expectation{What: ExpectRoger, Value: "6.13.0"}))
	require.NoError(t, l.SetExpectation(Expectation{What: ExpectServes, Value: "qwen", Scope: TagScope("production")}))
	got, err = l.Expectations()
	require.NoError(t, err)
	require.Len(t, got, 2)

	// One per thing per scope: setting it again replaces it.
	require.NoError(t, l.SetExpectation(Expectation{What: ExpectRoger, Value: "6.9.0"}))
	got, _ = l.Expectations()
	require.Len(t, got, 2)
	for _, e := range got {
		if e.What == ExpectRoger {
			require.Equal(t, "6.9.0", e.Value)
		}
	}
	// The same thing at a DIFFERENT scope is a different expectation.
	require.NoError(t, l.SetExpectation(Expectation{What: ExpectRoger, Value: "6.13.0", Scope: "n_desk"}))
	got, _ = l.Expectations()
	require.Len(t, got, 3)

	// An invalid one is refused and changes nothing.
	require.Error(t, l.SetExpectation(Expectation{What: ExpectRoger, Value: "soon"}))
	got, _ = l.Expectations()
	require.Len(t, got, 3)

	had, err := l.RemoveExpectation(ExpectRoger, "")
	require.NoError(t, err)
	require.True(t, had)
	had, err = l.RemoveExpectation(ExpectRoger, "")
	require.NoError(t, err)
	require.False(t, had, "removing what is not there is not an error")
	got, _ = l.Expectations()
	require.Len(t, got, 2)
}
