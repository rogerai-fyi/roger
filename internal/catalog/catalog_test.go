package catalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A valid entry, copied per-test so a case can spoil exactly one field.
func good() Entry {
	return Entry{
		ID:       "wave-micro-350m-instruct",
		Repo:     "https://huggingface.co/rogerai-fyi/wave-micro-350m-instruct",
		File:     "wave-micro-350m-instruct.Q4_K_M.gguf",
		Bytes:    240 << 20,
		SHA256:   strings.Repeat("a", 64),
		ServeMem: 600 << 20,
		License:  "Apache-2.0",
		Runtime:  "llama.cpp",
		Parent:   "",
	}
}

func TestValidateAcceptsACompleteEntry(t *testing.T) {
	require.NoError(t, good().Validate())
}

func TestValidateRejectsIncompleteOrDishonestEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*Entry)
		want  string
	}{
		{"no id", func(e *Entry) { e.ID = "" }, "id"},
		{"no repo", func(e *Entry) { e.Repo = "" }, "repo"},
		// An operator has to be able to see WHERE bytes come from before consenting.
		{"repo is not a url", func(e *Entry) { e.Repo = "rogerai-fyi/wave" }, "repo"},
		{"no file", func(e *Entry) { e.File = "" }, "file"},
		// Size is what consent is given against, so it can never be unknown.
		{"zero bytes", func(e *Entry) { e.Bytes = 0 }, "bytes"},
		{"negative bytes", func(e *Entry) { e.Bytes = -1 }, "bytes"},
		{"zero serve memory", func(e *Entry) { e.ServeMem = 0 }, "memory"},
		// Without a usable digest a corrupt artifact cannot be caught.
		{"no digest", func(e *Entry) { e.SHA256 = "" }, "sha256"},
		{"short digest", func(e *Entry) { e.SHA256 = "abc123" }, "sha256"},
		{"non-hex digest", func(e *Entry) { e.SHA256 = strings.Repeat("z", 64) }, "sha256"},
		// Licence is shown before download; an unlicensed artifact is not offerable.
		{"no license", func(e *Entry) { e.License = "" }, "license"},
		{"no runtime", func(e *Entry) { e.Runtime = "" }, "runtime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := good()
			tc.spoil(&e)
			err := e.Validate()
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), tc.want)
		})
	}
}

func TestValidateAcceptsAnUppercaseDigest(t *testing.T) {
	e := good()
	e.SHA256 = strings.ToUpper(e.SHA256)
	require.NoError(t, e.Validate())
}

func TestManifestRejectsDuplicateIDs(t *testing.T) {
	a, b := good(), good()
	_, err := NewManifest([]Entry{a, b})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "duplicate")
}

func TestManifestRejectsAnyInvalidEntryAndNamesIt(t *testing.T) {
	bad := good()
	bad.ID = "broken-one"
	bad.SHA256 = ""
	_, err := NewManifest([]Entry{good(), bad})
	require.Error(t, err)
	require.Contains(t, err.Error(), "broken-one", "the error names the offending entry")
}

func TestManifestKeepsEntriesInAStableOrder(t *testing.T) {
	z, a := good(), good()
	z.ID, a.ID = "z-model", "a-model"
	m, err := NewManifest([]Entry{z, a})
	require.NoError(t, err)
	require.Equal(t, []string{"a-model", "z-model"}, ids(m.Entries))
}

// ---- fit ------------------------------------------------------------------

func TestAssessFitUsesRealMemoryNotAGuess(t *testing.T) {
	const gb = int64(1) << 30
	for _, tc := range []struct {
		name      string
		available int64
		need      int64
		want      Fit
	}{
		{"comfortable", 32 * gb, 4 * gb, FitFits},
		{"tight", 8 * gb, 7 * gb, FitTight},
		{"will not fit", 8 * gb, 24 * gb, FitWontFit},
		{"exactly available is tight, not fitting", 8 * gb, 8 * gb, FitTight},
		{"one byte over will not fit", 8 * gb, 8*gb + 1, FitWontFit},
		// Unknown memory must never be reported as a fit - that would invite an
		// operator to acquire gigabytes onto a machine that cannot serve them.
		{"unknown memory", 0, 4 * gb, FitUnknown},
		{"unknown requirement", 32 * gb, 0, FitUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, AssessFit(tc.available, tc.need))
		})
	}
}

func TestFitStringsAreOperatorReadable(t *testing.T) {
	require.Equal(t, "fits", FitFits.String())
	require.Equal(t, "tight", FitTight.String())
	require.Equal(t, "will not fit", FitWontFit.String())
	require.Equal(t, "unknown", FitUnknown.String())
}

// ---- the merged SHARE list ------------------------------------------------

func TestMergeMarksACatalogueModelAlreadyRunningAsDetected(t *testing.T) {
	e := good()
	got := Merge([]string{e.ID}, []Entry{e})
	require.Len(t, got, 1)
	require.Equal(t, StateDetected, got[0].State)
	require.NotNil(t, got[0].Entry, "a detected model we publish keeps its catalogue facts")
}

func TestMergeKeepsADetectedModelWeDoNotPublish(t *testing.T) {
	got := Merge([]string{"qwen3-vl-8b"}, nil)
	require.Len(t, got, 1)
	require.Equal(t, StateDetected, got[0].State)
	require.Nil(t, got[0].Entry, "a third-party model has no catalogue entry")
}

func TestMergeOffersACatalogueModelThatIsNotRunning(t *testing.T) {
	e := good()
	got := Merge(nil, []Entry{e})
	require.Len(t, got, 1)
	require.Equal(t, StateOffered, got[0].State)
	require.Equal(t, e.ID, got[0].ID)
}

func TestMergeListsDetectedBeforeOfferedAndSortsWithinEachGroup(t *testing.T) {
	z, a := good(), good()
	z.ID, a.ID = "z-offered", "a-offered"
	got := Merge([]string{"m-running", "b-running"}, []Entry{z, a})
	require.Equal(t, []string{"b-running", "m-running", "a-offered", "z-offered"}, shareIDs(got),
		"a ready model outranks one that needs gigabytes downloading first")
}

func TestMergeDeduplicatesRepeatedDetections(t *testing.T) {
	got := Merge([]string{"dup", "dup"}, nil)
	require.Len(t, got, 1)
}

func TestMergeIgnoresBlankDetectedNames(t *testing.T) {
	require.Empty(t, Merge([]string{"", "   "}, nil))
}

func TestOfferedModelIsNotReadyToBroadcast(t *testing.T) {
	e := good()
	offered := Merge(nil, []Entry{e})[0]
	require.False(t, offered.ReadyToBroadcast(), "nothing can go on air before it exists locally")

	detected := Merge([]string{e.ID}, []Entry{e})[0]
	require.True(t, detected.ReadyToBroadcast())
}

func ids(es []Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func shareIDs(ss []Shareable) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}
