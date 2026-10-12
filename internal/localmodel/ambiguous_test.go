package localmodel

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// An on-disk name that matches several models is a typed error that still reads as before, and
// says whether the matches are quantisations of one repository (founder ruling 2026-10-11).
func TestAmbiguousSaysWhetherTheMatchesShareARepository(t *testing.T) {
	one := &Ambiguous{Query: "smol", Hits: []Model{
		{ID: "a/Smol-GGUF:Q4_K_M", Repo: "a/Smol-GGUF"}, {ID: "a/Smol-GGUF:Q8_0", Repo: "A/smol-gguf"}}}
	require.True(t, one.SameRepo())
	require.Equal(t, `"smol" matches 2 models, name one: a/Smol-GGUF:Q4_K_M, a/Smol-GGUF:Q8_0`, one.Error())

	two := &Ambiguous{Query: "smol", Hits: []Model{{ID: "a/x:Q4", Repo: "a/x"}, {ID: "b/x:Q4", Repo: "b/x"}}}
	require.False(t, two.SameRepo())
	placed := &Ambiguous{Query: "smol", Hits: []Model{{ID: "smol"}, {ID: "a/x:Q4", Repo: "a/x"}}}
	require.False(t, placed.SameRepo(), "a file the owner placed has no recorded repository")
	require.False(t, (&Ambiguous{}).SameRepo())

	var err error = two
	var got *Ambiguous
	require.True(t, errors.As(err, &got))
}
