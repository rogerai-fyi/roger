package tower

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Durable startup. Contract: features/tower/modes.feature, "Standalone durable startup
// fails instead of silently losing state" and "Development in-memory mode is unmistakably
// non-durable".
//
// The property that matters is not that a check exists but that a FAILING check refuses
// service AND tells the operator what to do. A readiness probe that says "not ready" and
// nothing else sends someone hunting through logs; the whole point of naming six
// dependency classes in the spec is that each has a different repair.

func durableConfig(t *testing.T, dir string) *Config {
	t.Helper()
	c, err := ParseConfig([]byte(minimalStandalone + "storage:\n  profile: durable\n"))
	require.NoError(t, err)
	c.Identity.Dir = dir
	return c
}

func TestProfileDefaultsToDevelopment(t *testing.T) {
	c, err := ParseConfig([]byte(minimalStandalone))
	require.NoError(t, err)
	require.Equal(t, ProfileDevelopment, c.Profile())
	require.False(t, c.IsDurable())
}

func TestProfileRejectsAnythingElse(t *testing.T) {
	// An EMPTY profile is deliberately absent from this list: YAML cannot distinguish
	// `profile:` with no value from the key being omitted, and omission legitimately
	// means development - so rejecting empty would reject the common case.
	for _, bad := range []string{"prod", "production", "persistent", "DURABLE", "local", "none"} {
		_, err := ParseConfig([]byte(minimalStandalone + "storage:\n  profile: " + bad + "\n"))
		require.Error(t, err, "profile %q must be rejected", bad)
	}
}

// --- the development profile is loud about what it is ---------------------

func TestDevelopmentProfileWarnsItIsNotDurable(t *testing.T) {
	c, err := ParseConfig([]byte(minimalStandalone))
	require.NoError(t, err)

	rep := Ready(c)
	require.True(t, rep.OK, "a development Tower is usable: %v", rep.Problems)
	require.NotEmpty(t, rep.Warnings, "it must say that state may be lost")
	joined := strings.ToLower(strings.Join(rep.Warnings, " "))
	require.Contains(t, joined, "lost")
	require.Contains(t, strings.ToLower(rep.String()), "not durable")
}

func TestDevelopmentProfileStillCannotReachThePublicNetwork(t *testing.T) {
	c, err := ParseConfig([]byte(minimalStandalone))
	require.NoError(t, err)
	require.Empty(t, c.PublicAuthority())
	require.False(t, c.AdvertisesPublicly())
}

// --- durable startup fails closed, with a repair instruction --------------

func TestDurableStartupIsReadyWhenEveryDependencyIsPresent(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)

	rep := Ready(durableConfig(t, dir))
	require.True(t, rep.OK, "a complete durable Tower must be ready: %v", rep.Problems)
	require.Empty(t, rep.Problems)
}

func TestDurableStartupRefusesWithoutAnIdentityVolume(t *testing.T) {
	rep := Ready(durableConfig(t, filepath.Join(t.TempDir(), "not-created")))
	require.False(t, rep.OK)
	require.NotEmpty(t, repairFor(rep, DepIdentityVolume), "the operator must be told how to repair it")
}

func TestDurableStartupRefusesWithoutTheOfflineRoot(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, offlineRoot)))

	rep := Ready(durableConfig(t, dir))
	require.False(t, rep.OK)
	require.NotEmpty(t, repairFor(rep, DepTrustRoot))
}

func TestDurableStartupRefusesWithoutABootstrapVerifierOrOperator(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)
	// A durable Tower with no admission history at all cannot serve anyone.
	rep := Ready(durableConfigRequiringOperator(t, dir))
	require.False(t, rep.OK)
	require.NotEmpty(t, repairFor(rep, DepOperator))
}

func TestDurableStartupRefusesAnUnreadableDatabaseSecret(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeStandalone)
	require.NoError(t, err)

	c := durableConfig(t, dir)
	c.Storage.URLFile = filepath.Join(dir, "no-such-url-file")

	rep := Ready(c)
	require.False(t, rep.OK)
	require.NotEmpty(t, repairFor(rep, DepDatabase))
}

// Every problem must name a repair. A readiness report that says only "not ready" makes
// an operator guess which of six things broke.
func TestEveryProblemCarriesARepairInstruction(t *testing.T) {
	rep := Ready(durableConfig(t, filepath.Join(t.TempDir(), "missing")))
	require.False(t, rep.OK)
	for _, p := range rep.Problems {
		require.NotEmpty(t, p.Dependency, "a problem must name which dependency failed")
		require.NotEmpty(t, p.Repair, "%s must carry a repair instruction", p.Dependency)
		require.NotEqual(t, p.Repair, p.Detail, "the repair must say what to DO, not restate the fault")
	}
}

func TestNotReadyIsRenderedForAHuman(t *testing.T) {
	rep := Ready(durableConfig(t, filepath.Join(t.TempDir(), "missing")))
	out := rep.String()
	require.Contains(t, out, "NOT READY")
	require.Contains(t, out, "repair:")
}

// A joined Tower has no local durability contract to check - Roger Core holds the state
// that matters.
func TestReadinessOnAJoinedTowerReportsItIsNotItsConcern(t *testing.T) {
	c, err := ParseConfig([]byte(minimalJoined))
	require.NoError(t, err)
	rep := Ready(c)
	require.True(t, rep.OK)
	require.Equal(t, ProfileDevelopment, c.Profile())
}

// --- helpers ---------------------------------------------------------------

// repairFor returns the repair instruction recorded for a dependency, or "".
func repairFor(r Readiness, dep Dependency) string {
	for _, p := range r.Problems {
		if p.Dependency == dep {
			return p.Repair
		}
	}
	return ""
}

func durableConfigRequiringOperator(t *testing.T, dir string) *Config {
	t.Helper()
	c := durableConfig(t, dir)
	c.RequireOperator = true
	return c
}
