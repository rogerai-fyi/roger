package main

// attempt_id_test.go: the per-attempt id (contract §14.B7 #13) keeps its shape on every broker,
// including one built without a signing key (some unit fixtures), and is stable per attempt.

import (
	"crypto/ed25519"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

var attemptShape = regexp.MustCompile(`^att_[0-9a-f]{24}$`)

func TestAttemptID(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	for name, b := range map[string]*broker{"keyed": {priv: priv}, "no key": {}} {
		a1, a2 := b.attemptID("R", 1), b.attemptID("R", 2)
		require.Regexp(t, attemptShape, a1, name)
		require.Regexp(t, attemptShape, a2, name)
		require.NotEqual(t, a1, a2, name)
		require.Equal(t, a1, b.attemptID("R", 1), "%s: stable per (request, attempt)", name)
		require.Equal(t, a1, b.attemptID("R", 0), "%s: n below 1 is attempt 1", name)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	require.NotEqual(t, (&broker{priv: priv}).attemptID("R", 1), (&broker{priv: other}).attemptID("R", 1),
		"another broker key derives other ids")
}

// slice-6 review 2026-10-06: every secret the broker derives from its signing key goes through
// one helper, and none of them panics on a broker built without a key.
func TestDerivedSecretsWithoutAKey(t *testing.T) {
	b := &broker{}
	require.NotPanics(t, func() { _ = b.affinityKey("payer", "session", "m") })
	require.Regexp(t, `^aff:[0-9a-f]{40}$`, b.affinityKey("payer", "session", "m"))
	require.NotPanics(t, func() { _ = b.pseudonym("u", "n") })
	require.Regexp(t, `^u_[0-9a-f]{16}$`, b.probePseudonym("n"))
	require.NotEqual(t, b.probePseudonym("n"), b.probePseudonym("n"), "a canary identity rotates per probe")

	_, priv, _ := ed25519.GenerateKey(nil)
	k := &broker{priv: priv}
	require.Equal(t, k.deriveSecret("x"), k.deriveSecret("x"), "stable per label")
	require.NotEqual(t, k.deriveSecret("x"), k.deriveSecret("y"), "distinct per label")
	require.NotEqual(t, k.affinityKey("p", "s", "m"), (&broker{}).affinityKey("p", "s", "m"), "keyed")
}
