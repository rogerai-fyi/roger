package main

// `roger edge sink` (features/edge/health.feature §5). An Edge is private by default; this is the
// command that changes that, and the URL it takes is a bearer credential that must never come back.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTheSinkCommandTakesASecretAndNeverPrintsIt(t *testing.T) {
	useTempConfig(t)
	out, code := edgeRun(t, "edge", "sink")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "no sink is configured")
	require.Contains(t, out, "stay on this machine")

	secret := "https://discord.com/api/webhooks/123456789/AbCdEf-verysecrettoken"
	out, code = edgeRun(t, "edge", "sink", "add", "discord", secret)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "discord.com", "the owner cannot tell which sink they just made")
	require.NotContains(t, out, "AbCdEf-verysecrettoken", "adding a sink printed its secret back")
	require.NotContains(t, out, "123456789", "the webhook id is half the credential")

	// And the listing never prints it either, however many times it is asked.
	out, code = edgeRun(t, "edge", "sink")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "discord")
	require.Contains(t, out, "discord.com")
	require.NotContains(t, out, "AbCdEf-verysecrettoken")
}

func TestTheSinkCommandProvesASinkWorks(t *testing.T) {
	useTempConfig(t)
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	out, code := edgeRun(t, "edge", "sink", "add", "webhook", srv.URL)
	require.Equal(t, 0, code, out)
	id := strings.Fields(out)[3] // "alerts now go to webhook · host · sk_xxxx"
	require.True(t, strings.HasPrefix(id, "webhook") || strings.Contains(out, "sk_"), out)

	// The id from the listing, which is what an owner would actually copy.
	list, _ := edgeRun(t, "edge", "sink")
	for _, f := range strings.Fields(list) {
		if strings.HasPrefix(f, "sk_") {
			id = f
		}
	}
	out, code = edgeRun(t, "edge", "sink", "test", id)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "test reached")
	require.Contains(t, strings.ToLower(body), "test", "the message is not marked as a test: %s", body)

	out, code = edgeRun(t, "edge", "sink", "rm", id)
	require.Equal(t, 0, code, out)
	list, _ = edgeRun(t, "edge", "sink")
	require.Contains(t, list, "no sink is configured")
}

func TestTheSinkCommandRefusesWhatCannotWork(t *testing.T) {
	useTempConfig(t)
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"edge", "sink", "add", "webhook", "not-a-url"}, "http"},
		{[]string{"edge", "sink", "add", "pigeon", "https://x/y"}, "not a kind of sink"},
		{[]string{"edge", "sink", "add", "email", "mailto:a@b"}, "not built"},
		{[]string{"edge", "sink", "rm", "sk_nothing"}, "no sink has the id"},
		{[]string{"edge", "sink", "test", "sk_nothing"}, "no sink has the id"},
		{[]string{"edge", "sink", "sideways"}, "add, rm or test"},
	} {
		out, code := edgeRun(t, c.args...)
		require.NotEqual(t, 0, code, out)
		require.Contains(t, out, c.says, "%v", c.args)
	}
}
