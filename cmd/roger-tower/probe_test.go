package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// probe refuses its missing arguments by name, before touching the network.
func TestProbeRefusesMissingArguments(t *testing.T) {
	var b bytes.Buffer
	require.ErrorContains(t, cmdProbe(nil, &b), "--model")
	t.Setenv("ROGER_BROKER", "")
	require.ErrorContains(t, cmdProbe([]string{"--model", "m"}, &b), "--broker")
	require.Error(t, cmdProbe([]string{"--wat"}, &b))
	require.Error(t, cmdProbe([]string{"--model", "m", "--broker", "http://x", "--ca", "/no/such"}, &b))
	require.Error(t, cmdProbe([]string{"--model", "m", "--broker", "http://x", "--body", "/no/such"}, &b))
}

// A probe against a broker that refuses authorize reports the failure rather than pretending
// to have run.
func TestProbeReportsAnAuthorizeFailure(t *testing.T) {
	var b bytes.Buffer
	err := cmdProbe([]string{"--model", "m", "--broker", "http://127.0.0.1:1"}, &b)
	require.ErrorContains(t, err, "authorize failed")
}

func TestUsageMentionsProbe(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, run(nil, &b))
	require.Contains(t, b.String(), "probe")
}
