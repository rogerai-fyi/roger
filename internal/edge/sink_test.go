package edge

// EXTERNAL SINKS (features/edge/health.feature §5). An Edge is PRIVATE BY DEFAULT: a metric never
// leaves the machine unless the owner configures somewhere for it to go. When they do, the thing
// they hand over is a SECRET - a Discord webhook URL is a bearer credential, anybody holding it can
// post as that integration - so the rule that shapes this whole file is: the secret goes in, and it
// never comes back out. Not to a screen, not to a log, not to a receipt, not to a browser.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestASinkTakesASecretAndNeverGivesItBack(t *testing.T) {
	l := AlertLog{Dir: t.TempDir()}
	secret := "https://discord.com/api/webhooks/123456789/AbCdEf-verysecrettoken"
	got, err := l.AddSink(SinkDiscord, secret, time.Now())
	require.NoError(t, err)
	require.NotEmpty(t, got.ID)

	// What the owner sees names the sink and where it goes, and carries none of the secret.
	words := got.Words()
	require.Contains(t, words, "discord")
	require.Contains(t, words, "discord.com", "an owner must be able to tell which sink this is")
	require.NotContains(t, words, "AbCdEf-verysecrettoken")
	require.NotContains(t, words, "123456789", "the webhook id is half the credential")

	// And the listing is the same: the store hands back sinks whose secret is already gone.
	list, err := l.Sinks()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Empty(t, list[0].URL, "a read of the sinks carried the secret out of the store")
	require.Contains(t, list[0].Words(), "discord.com")
}

func TestAlertsGoNowhereUntilTheOwnerSaysWhere(t *testing.T) {
	l := AlertLog{Dir: t.TempDir()}
	list, err := l.Sinks()
	require.NoError(t, err)
	require.Empty(t, list, "an Edge with no sink configured has none")

	sent, err := l.Deliver(context.Background(), Alert{Node: "pi", Metric: "cpu_temp", Value: 82, Threshold: 80, Firing: true}, time.Now())
	require.NoError(t, err)
	require.Empty(t, sent, "an alert was sent off the machine with no sink configured")
}

func TestAConfiguredSinkReceivesTheAlertAndOnlyThatSink(t *testing.T) {
	var hits int64
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	var other int64
	srvOther := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&other, 1)
	}))
	defer srvOther.Close()

	l := AlertLog{Dir: t.TempDir()}
	_, err := l.AddSink(SinkWebhook, srv.URL, time.Now())
	require.NoError(t, err)

	a := Alert{At: time.Now(), Node: "pi", Metric: "cpu_temp", Value: 82, Threshold: 80, Op: OpGT, Firing: true}
	sent, err := l.Deliver(context.Background(), a, time.Now())
	require.NoError(t, err)
	require.Len(t, sent, 1)
	require.True(t, sent[0].OK, "delivery failed: %s", sent[0].Says)
	require.Equal(t, int64(1), atomic.LoadInt64(&hits))
	require.Equal(t, int64(0), atomic.LoadInt64(&other), "a sink nobody configured was contacted")

	// The alert names the node, the metric, the real value and the threshold it crossed.
	for _, want := range []string{"pi", "cpu_temp", "82", "80"} {
		require.Contains(t, body, want)
	}
}

func TestADeliveryLogNamesTheSinkAndTheOutcomeAndNeverTheSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	secret := srv.URL + "/hooks/AbCdEf-verysecrettoken"
	l := AlertLog{Dir: t.TempDir()}
	sk, err := l.AddSink(SinkWebhook, secret, time.Now())
	require.NoError(t, err)

	sent, err := l.Deliver(context.Background(), Alert{Node: "pi", Metric: "cpu_temp", Firing: true}, time.Now())
	require.NoError(t, err)
	require.Len(t, sent, 1)
	line := sent[0].Says + " " + sent[0].Sink
	require.Contains(t, line, "sk_", "the outcome does not name which sink it was")
	require.NotContains(t, line, "AbCdEf-verysecrettoken", "the secret is in the delivery outcome")
	require.NotContains(t, sk.Words(), "AbCdEf-verysecrettoken")
}

func TestAFailedDeliveryIsRetriedAndNeverLostSilently(t *testing.T) {
	var tries int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&tries, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	l := AlertLog{Dir: t.TempDir()}
	l.SinkBackoff = time.Millisecond // the wait is a seam, so the spec does not sleep for real
	_, err := l.AddSink(SinkWebhook, srv.URL, time.Now())
	require.NoError(t, err)

	sent, err := l.Deliver(context.Background(), Alert{Node: "pi", Metric: "cpu_temp", Firing: true}, time.Now())
	require.NoError(t, err)
	require.Len(t, sent, 1)
	require.True(t, sent[0].OK, "a sink that recovered was given up on: %s", sent[0].Says)
	require.GreaterOrEqual(t, atomic.LoadInt64(&tries), int64(3), "it did not retry")

	// A sink that never recovers is reported as undelivered, with the reason - never dropped.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	l2 := AlertLog{Dir: t.TempDir(), SinkBackoff: time.Millisecond}
	_, err = l2.AddSink(SinkWebhook, dead.URL, time.Now())
	require.NoError(t, err)
	sent, err = l2.Deliver(context.Background(), Alert{Node: "pi", Metric: "cpu_temp", Firing: true}, time.Now())
	require.NoError(t, err)
	require.Len(t, sent, 1)
	require.False(t, sent[0].OK)
	require.NotEmpty(t, sent[0].Says, "an undelivered alert said nothing about why")
}

func TestATestAlertIsMarkedAsOneAndChangesNothing(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	l := AlertLog{Dir: t.TempDir()}
	sk, err := l.AddSink(SinkWebhook, srv.URL, time.Now())
	require.NoError(t, err)

	before, err := l.State()
	require.NoError(t, err)
	sent, err := l.TestSink(context.Background(), sk.ID, time.Now())
	require.NoError(t, err)
	require.True(t, sent.OK, sent.Says)
	require.True(t, strings.Contains(strings.ToLower(body), "test"), "the test message is not marked as one: %s", body)

	after, err := l.State()
	require.NoError(t, err)
	require.Equal(t, len(before), len(after), "a test alert changed the rule state")
}

func TestASinkIsRemovedByIdAndOnlyThatOne(t *testing.T) {
	l := AlertLog{Dir: t.TempDir()}
	a, err := l.AddSink(SinkWebhook, "http://192.168.1.50/hook", time.Now())
	require.NoError(t, err)
	b, err := l.AddSink(SinkDiscord, "https://discord.com/api/webhooks/1/x", time.Now())
	require.NoError(t, err)

	had, err := l.RemoveSink(a.ID)
	require.NoError(t, err)
	require.True(t, had)
	list, _ := l.Sinks()
	require.Len(t, list, 1)
	require.Equal(t, b.ID, list[0].ID)

	had, err = l.RemoveSink("sk_nothing")
	require.NoError(t, err)
	require.False(t, had, "removing what is not there is not an error")
}

func TestASinkIsRefusedWhenItCannotWork(t *testing.T) {
	l := AlertLog{Dir: t.TempDir()}
	for _, c := range []struct{ kind, url, says string }{
		{string(SinkWebhook), "", "needs a URL"},
		{string(SinkWebhook), "not a url", "http"},
		{string(SinkWebhook), "ftp://box/hook", "http"},
		{"pigeon", "https://x/y", "not a kind of sink"},
		{string(SinkEmail), "mailto:a@b", "not built"},
	} {
		_, err := l.AddSink(SinkKind(c.kind), c.url, time.Now())
		require.ErrorContains(t, err, c.says, "%s %s", c.kind, c.url)
	}
	list, _ := l.Sinks()
	require.Empty(t, list, "a refused sink was stored anyway")
}

// A LAN webhook needs no internet, which is the whole point on an airgapped Edge.
func TestALANWebhookIsAllowedAndNeedsNoInternet(t *testing.T) {
	var hit int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hit, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	l := AlertLog{Dir: t.TempDir()}
	_, err := l.AddSink(SinkWebhook, srv.URL, time.Now())
	require.NoError(t, err)
	sent, err := l.Deliver(context.Background(), Alert{Node: "pi", Metric: "offline", Firing: true}, time.Now())
	require.NoError(t, err)
	require.True(t, sent[0].OK)
	require.Equal(t, int64(1), atomic.LoadInt64(&hit))
}
