package main

// Sweep: every GET feed the account pages call, for an email user, with the status each
// must give. The page scripts treat a 401 as "logged out", so a feed that 401s a valid
// email session is a bug (the 2026-10 login loop). Operator-only feeds answer a clear 403
// to a plain consumer - never a 401 - and 200 to an email operator.

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

func TestEveryPageFeedTreatsAnEmailUserAsSignedIn(t *testing.T) {
	const consumer, operatorOnly = http.StatusOK, http.StatusForbidden
	for _, operator := range []bool{false, true} {
		b, cap := emailTestBroker(t)
		if operator {
			require.NoError(t, b.db.BindOwner(store.Owner{
				Pubkey: "pk-e", Login: "op@rogerai.fm", Email: "op@rogerai.fm", EmailVerifiedAt: time.Now().Unix(),
			}))
		}
		sess := signInByEmail(t, b, cap, "op@rogerai.fm")

		feeds := []struct {
			path string
			h    http.HandlerFunc
			want int // for a plain consumer; an operator always gets 200
		}{
			{"/account", b.account, consumer},
			{"/me", b.me, consumer},
			{"/billing", b.billing, consumer},
			{"/usage", b.usage, consumer},
			{"/metrics/series", b.metricsSeries, consumer},
			{"/console", b.console, consumer},
			{"/rc/sessions", b.rcSessions, consumer},
			{"/stations", b.stations, operatorOnly},
			{"/payouts/earnings", b.payoutsEarnings, operatorOnly},
			{"/payouts/history", b.payoutsHistory, operatorOnly},
			{"/provider/models", b.providerModels, operatorOnly},
			{"/connect/status", b.connectStatus, operatorOnly},
			{"/grants", b.grants, operatorOnly},
		}
		for _, f := range feeds {
			want := f.want
			if operator {
				want = http.StatusOK
			}
			rec := getWithSession(f.h, f.path, sess)
			require.Equal(t, want, rec.Code, "operator=%v %s: %s", operator, f.path, rec.Body.String())
			require.NotEqual(t, http.StatusUnauthorized, rec.Code, "%s must never 401 a valid session", f.path)
		}
	}
}
