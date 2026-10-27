package edge

// Executable pins for features/edge/tags.feature: the words, the fleet methods, and the precedence
// of node over tag over fleet when a rule is chosen.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/store"
)

func TestATagIsTheOwnersWordNormalisedAndValidated(t *testing.T) {
	require.Equal(t, "production", NormalizeTag("  Production  "))
	require.NoError(t, ValidTag("production"))
	require.NoError(t, ValidTag("rack1"))
	require.NoError(t, ValidTag("gpu.a100"))
	require.NoError(t, ValidTag("west-2"))
	require.NoError(t, ValidTag("0"))
	for _, bad := range []string{"", "a b", "tag:prod", "-lead", ".lead", "_lead", "über", "Prod", strings.Repeat("x", 33), "a:b"} {
		require.Error(t, ValidTag(bad), "%q must be refused", bad)
	}

	got, err := CleanTags([]string{"GPU", "production", "gpu", " production "})
	require.NoError(t, err)
	require.Equal(t, []string{"gpu", "production"}, got, "deduped and sorted")

	many := make([]string, 0, MaxTags+1)
	for i := 0; i <= MaxTags; i++ {
		many = append(many, string(rune('a'+i)))
	}
	_, err = CleanTags(many)
	require.ErrorContains(t, err, "at most 12 tags")
}

func TestScopeWordsForATag(t *testing.T) {
	require.Equal(t, "tag:production", TagScope("production"))
	got, ok := ScopeTag("tag:production")
	require.True(t, ok)
	require.Equal(t, "production", got)
	_, ok = ScopeTag("n_desk")
	require.False(t, ok)
	require.True(t, HasTag([]string{"a", "b"}, "b"))
	require.False(t, HasTag([]string{"a"}, "b"))
}

func tagFleet(t *testing.T) *Fleet {
	t.Helper()
	f := NewFleet(store.NewMem(), "acct-1")
	for _, n := range []store.EdgeNode{
		{ID: "n_desk", Name: "desk", Kind: string(Host), Presence: string(PresenceVerified)},
		{ID: "n_shed", Name: "shed", Kind: string(Host), Presence: string(PresenceVerified)},
	} {
		_, err := f.Enroll(n)
		require.NoError(t, err)
	}
	return f
}

func tagsOf(t *testing.T, f *Fleet, id string) []string {
	t.Helper()
	n, ok, err := f.Get(id)
	require.NoError(t, err)
	require.True(t, ok)
	return n.Tags
}

func TestTagsAreAddedRemovedReplacedAndCleared(t *testing.T) {
	f := tagFleet(t)
	require.NoError(t, f.SetTags("n_desk", []string{"Production", "gpu"}))
	require.Equal(t, []string{"gpu", "production"}, tagsOf(t, f, "n_desk"))

	require.NoError(t, f.EditTags("n_desk", []string{"rack1"}, []string{"gpu"}))
	require.Equal(t, []string{"production", "rack1"}, tagsOf(t, f, "n_desk"))

	require.NoError(t, f.EditTags("n_desk", nil, []string{"nothing-it-has"}), "removing what it lacks is not an error")
	require.Equal(t, []string{"production", "rack1"}, tagsOf(t, f, "n_desk"))

	require.NoError(t, f.SetTags("n_desk", []string{" bench ", "lab"}))
	require.Equal(t, []string{"bench", "lab"}, tagsOf(t, f, "n_desk"))

	require.NoError(t, f.SetTags("n_desk", nil))
	require.Empty(t, tagsOf(t, f, "n_desk"))

	// An invalid word changes nothing at all.
	require.NoError(t, f.SetTags("n_shed", []string{"production"}))
	require.Error(t, f.SetTags("n_shed", []string{"production", "a b"}))
	require.Equal(t, []string{"production"}, tagsOf(t, f, "n_shed"))
	require.Error(t, f.EditTags("n_shed", []string{"tag:x"}, nil))
	require.Equal(t, []string{"production"}, tagsOf(t, f, "n_shed"))

	// Every change is on the history, and a no-op writes none.
	n, _, _ := f.Get("n_shed")
	before := len(n.History)
	require.NoError(t, f.SetTags("n_shed", []string{"production"}))
	n, _, _ = f.Get("n_shed")
	require.Equal(t, before, len(n.History), "setting the same tags is not an event")
	require.Contains(t, n.History[before-1].Detail, "production")
	require.Equal(t, "tagged", n.History[before-1].What)

	// Forgetting takes the owner's words with the record.
	require.NoError(t, f.Forget("n_shed"))
	_, err := f.Enroll(store.EdgeNode{ID: "n_shed", Name: "shed", Kind: string(Host)})
	require.NoError(t, err)
	require.Empty(t, tagsOf(t, f, "n_shed"))
}

func TestTagsAreNeverReadFromThePeer(t *testing.T) {
	f := tagFleet(t)
	require.NoError(t, f.SetTags("n_desk", []string{"production"}))
	// A describe answer cannot carry tags at all: Observation has no such field. An observed node
	// keeps the owner's words and gains none.
	_, err := f.Observe("n_desk", Observation{Name: "desk", Kind: string(Host), Addr: "10.0.0.2:1", Fingerprint: "ab",
		Caps: []Capability{Serve}, Instances: []Instance{{Name: "desk"}}})
	require.NoError(t, err)
	require.Equal(t, []string{"production"}, tagsOf(t, f, "n_desk"))
}

func TestARuleScopedToATagJudgesTheGroup(t *testing.T) {
	rules := RuleSet{
		{ID: "fleet-temp", Metric: MetricCPUTemp, Op: OpGT, Value: 90},
		{ID: "prod-temp", Metric: MetricCPUTemp, Op: OpGT, Value: 70, Scope: TagScope("production")},
	}
	r, ok := rules.applying(MetricCPUTemp, "n_desk", []string{"production"})
	require.True(t, ok)
	require.Equal(t, "prod-temp", r.ID)
	r, ok = rules.applying(MetricCPUTemp, "n_pi", nil)
	require.True(t, ok)
	require.Equal(t, "fleet-temp", r.ID, "a node in no group is judged by the fleet rule")

	// The node's own rule beats a tag rule, which beats the fleet.
	withOwn := append(rules, Rule{ID: "desk-temp", Metric: MetricCPUTemp, Op: OpGT, Value: 95, Scope: "n_desk"})
	r, _ = withOwn.applying(MetricCPUTemp, "n_desk", []string{"production"})
	require.Equal(t, "desk-temp", r.ID)

	// Two tag rules: the TIGHTEST applies, and the choice is stable.
	two := RuleSet{
		{ID: "prod", Metric: MetricCPUTemp, Op: OpGT, Value: 70, Scope: TagScope("production")},
		{ID: "summer", Metric: MetricCPUTemp, Op: OpGT, Value: 65, Scope: TagScope("summer")},
	}
	for range 5 {
		r, _ = two.applying(MetricCPUTemp, "n_desk", []string{"production", "summer"})
		require.Equal(t, "summer", r.ID, "the lowest gt threshold fires soonest")
	}
	// For a "lt" metric the tightest is the HIGHEST threshold.
	lt := RuleSet{
		{ID: "a", Metric: MetricDisk, Op: OpLT, Value: 5, Scope: TagScope("x")},
		{ID: "b", Metric: MetricDisk, Op: OpLT, Value: 10, Scope: TagScope("y")},
	}
	r, _ = lt.applying(MetricDisk, "n_desk", []string{"x", "y"})
	require.Equal(t, "b", r.ID)
	// A tie is broken by id, every time.
	tie := RuleSet{
		{ID: "b-rule", Metric: MetricCPUTemp, Op: OpGT, Value: 70, Scope: TagScope("x")},
		{ID: "a-rule", Metric: MetricCPUTemp, Op: OpGT, Value: 70, Scope: TagScope("y")},
	}
	for range 5 {
		r, _ = tie.applying(MetricCPUTemp, "n_desk", []string{"x", "y"})
		require.Equal(t, "a-rule", r.ID)
	}
	// A tag nothing carries judges nothing, and an off rule never applies.
	_, ok = RuleSet{{ID: "w", Metric: MetricCPUTemp, Op: OpGT, Value: 1, Scope: TagScope("winter")}}.applying(MetricCPUTemp, "n_desk", []string{"production"})
	require.False(t, ok)
	_, ok = RuleSet{{ID: "p", Metric: MetricCPUTemp, Op: OpGT, Value: 1, Scope: TagScope("production"), Off: true}}.applying(MetricCPUTemp, "n_desk", []string{"production"})
	require.False(t, ok)
}

func TestATagScopedRuleFiresForTheGroupOnly(t *testing.T) {
	l := AlertLog{Dir: t.TempDir()}
	now := time.Now()
	require.NoError(t, l.SetRule(Rule{ID: "fleet-temp", Metric: MetricCPUTemp, Op: OpGT, Value: 90}))
	require.NoError(t, l.SetRule(Rule{ID: "prod-temp", Metric: MetricCPUTemp, Op: OpGT, Value: 70, Scope: TagScope("production")}))
	warm := 75.0
	hs := []NodeHealth{
		{ID: "n_desk", Name: "desk", Kind: "host", Presence: string(PresenceVerified), Tags: []string{"production"}, LastHeard: now,
			Readings: &store.EdgeResources{CPUTempC: &warm, ReadAt: now.Unix()}},
		{ID: "n_pi", Name: "quiet-pi", Kind: "host", Presence: string(PresenceVerified), LastHeard: now,
			Readings: &store.EdgeResources{CPUTempC: &warm, ReadAt: now.Unix()}},
	}
	_, alerts, err := l.Judge(hs, now)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.Equal(t, "n_desk", alerts[0].NodeID)
	require.Equal(t, "prod-temp", alerts[0].RuleID)
	require.Equal(t, 75.0, alerts[0].Value, "the reading")
	require.Equal(t, 70.0, alerts[0].Threshold, "judged against the group rule, not the fleet rule")

	// A rule scoped to a tag with an invalid word is refused where it is made.
	require.Error(t, ValidateRule(Rule{ID: "x", Metric: MetricCPUTemp, Op: OpGT, Value: 1, Scope: "tag:"}))
	require.Error(t, ValidateRule(Rule{ID: "x", Metric: MetricCPUTemp, Op: OpGT, Value: 1, Scope: "tag:a b"}))
	require.NoError(t, ValidateRule(Rule{ID: "x", Metric: MetricCPUTemp, Op: OpGT, Value: 1, Scope: "tag:production"}))
}
