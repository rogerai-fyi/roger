package runtime_test

// THE RUNTIME MANIFEST (features/serving/local_models.feature §3).
//
// Two of these tests exist because of things that ALREADY happened upstream rather than things
// that might: asset names rotted between releases, and the arm64 system-library floor was raised
// by moving a build runner with no announcement, which locked out the most common Raspberry Pi
// install there is. A manifest that is only checked when it breaks is checked by the owner.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/runtime"
)

func TestEveryEntryCarriesWhatVerifyingBeforeExecutingNeeds(t *testing.T) {
	assets := runtime.Assets()
	require.NotEmpty(t, assets)
	seen := map[string]bool{}
	for _, a := range assets {
		require.NotEmpty(t, a.Platform)
		require.Equal(t, runtime.Tag(), a.Tag, "%s is pinned to a different build than the rest", a.Platform)
		require.NotEmpty(t, a.Name)
		require.NotEmpty(t, a.Exe, "%s: nothing says where the server sits inside the archive", a.Platform)
		require.Greater(t, a.Bytes, int64(1<<20), "%s: an implausible size", a.Platform)
		require.Len(t, a.SHA256, 64, "%s: a runtime without a full digest cannot be verified before it is run", a.Platform)
		require.NotContains(t, a.SHA256, "sha256:", "%s: the digest kept its prefix", a.Platform)

		// The URL is the pinned release's, and it carries the asset name verbatim rather than
		// anything assembled from a template at run time.
		require.True(t, strings.HasPrefix(a.URL, "https://"), a.Platform)
		require.Contains(t, a.URL, a.Tag, a.Platform)
		require.True(t, strings.HasSuffix(a.URL, a.Name),
			"%s: the URL and the asset name disagree", a.Platform)

		require.False(t, seen[a.Platform], "%s appears twice", a.Platform)
		seen[a.Platform] = true
	}
}

func TestEachSupportedMachineResolvesToExactlyOneRuntime(t *testing.T) {
	for _, h := range []runtime.Host{
		{OS: "linux", Arch: "amd64"},
		{OS: "linux", Arch: "arm64", GlibcMajor: 2, GlibcMinor: 41, GlibcKnown: true},
		{OS: "darwin", Arch: "arm64"},
		{OS: "darwin", Arch: "amd64"},
		{OS: "windows", Arch: "amd64"},
		{OS: "windows", Arch: "arm64"},
	} {
		got, err := runtime.Pick(h)
		require.NoError(t, err, "%s/%s", h.OS, h.Arch)
		require.NotEmpty(t, got.URL)
	}
}

// THE LIVE CASE. roger ships a 32-bit arm binary and no inference runtime exists for it from
// anybody. The refusal has to say what the board CAN do, or an owner reads it as a broken build.
func TestABoardWithNoRuntimeAnywhereIsToldWhatItCanStillDo(t *testing.T) {
	_, err := runtime.Pick(runtime.Host{OS: "linux", Arch: "arm"})
	require.ErrorIs(t, err, runtime.ErrNoRuntime)
	require.Contains(t, err.Error(), "relay")
	require.Contains(t, err.Error(), "cannot serve")
	require.Contains(t, err.Error(), "linux/arm")

	_, err = runtime.Pick(runtime.Host{OS: "plan9", Arch: "amd64"})
	require.ErrorIs(t, err, runtime.ErrNoRuntime)
}

// THE ONE THAT ALREADY BIT. Upstream moved the arm64 build to a newer runner and raised its glibc
// floor to 2.38 with no announcement. Raspberry Pi OS Bookworm ships 2.36, so the published
// binary cannot load there AT ALL - and that is the most common Pi install there is. Refusing
// with the two version numbers is the whole difference between a clear answer and a mystery.
func TestAPiOnTheCommonestInstallIsRefusedWithBothVersions(t *testing.T) {
	bookworm := runtime.Host{OS: "linux", Arch: "arm64", GlibcMajor: 2, GlibcMinor: 36, GlibcKnown: true}
	_, err := runtime.Pick(bookworm)
	require.ErrorIs(t, err, runtime.ErrNoRuntime)
	require.Contains(t, err.Error(), "2.38", "the refusal does not say what the build needs")
	require.Contains(t, err.Error(), "2.36", "the refusal does not say what the machine has")
	require.Contains(t, err.Error(), "newer OS", "it does not say what would fix it")

	// A newer Pi OS runs it.
	trixie := runtime.Host{OS: "linux", Arch: "arm64", GlibcMajor: 2, GlibcMinor: 41, GlibcKnown: true}
	_, err = runtime.Pick(trixie)
	require.NoError(t, err)

	// Exactly at the floor is fine: the check is "at least", not "newer than".
	exact := runtime.Host{OS: "linux", Arch: "arm64", GlibcMajor: 2, GlibcMinor: 38, GlibcKnown: true}
	_, err = runtime.Pick(exact)
	require.NoError(t, err)
}

// An unreadable version is not treated as too old. Refusing a machine because a version string
// could not be parsed would be guessing in the direction that helps nobody: this check exists to
// catch one specific known breakage, not to gate every machine on a probe.
func TestAnUnreadableLibraryVersionDoesNotRefuseTheMachine(t *testing.T) {
	_, err := runtime.Pick(runtime.Host{OS: "linux", Arch: "arm64"})
	require.NoError(t, err, "a machine whose glibc could not be read was refused on a guess")
}

// Platforms with no glibc question are not asked it.
func TestPlatformsWithNoFloorAreNotGatedOnOne(t *testing.T) {
	for _, h := range []runtime.Host{
		{OS: "darwin", Arch: "arm64", GlibcMajor: 2, GlibcMinor: 1, GlibcKnown: true},
		{OS: "windows", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 1, GlibcKnown: true},
	} {
		_, err := runtime.Pick(h)
		require.NoError(t, err, "%s was gated on a library it does not use", h.OS)
	}
}

func TestTheVersionStringsRealToolsPrintAreUnderstood(t *testing.T) {
	for in, want := range map[string][2]int{
		"glibc 2.42":                            {2, 42},
		"glibc 2.36\n":                          {2, 36},
		"ldd (GNU libc) 2.36":                   {2, 36},
		"ldd (Ubuntu GLIBC 2.39-0ubuntu8) 2.39": {2, 39},
		"2.36.1":                                {2, 36},
	} {
		maj, min, ok := runtime.ParseGlibcVersion(in)
		require.True(t, ok, in)
		require.Equal(t, want[0], maj, in)
		require.Equal(t, want[1], min, in)
	}
	for _, in := range []string{"", "musl libc", "no numbers here", "7"} {
		_, _, ok := runtime.ParseGlibcVersion(in)
		require.False(t, ok, "%q was read as a version", in)
	}
}

// ThisHost describes the machine the tests are running on, and must not invent anything.
func TestThisHostDescribesTheRealMachine(t *testing.T) {
	h := runtime.ThisHost()
	require.NotEmpty(t, h.OS)
	require.NotEmpty(t, h.Arch)
	if h.GlibcKnown {
		require.Greater(t, h.GlibcMajor, 0)
	} else {
		require.Zero(t, h.GlibcMajor, "an unknown version was reported with a number anyway")
	}
}
