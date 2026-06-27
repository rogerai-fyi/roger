package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCORSPreflightShortCircuits locks the credentialed-CORS preflight on every account/
// owner/admin/metrics handler: an OPTIONS request is answered 204 No Content and returns
// BEFORE any auth/store work (the corsCredsPreflight gate). This is the browser preflight
// the web app issues before each credentialed cross-origin call.
func TestCORSPreflightShortCircuits(t *testing.T) {
	b, _ := brokerWithOwner(t)
	b.adminKey = "k"

	handlers := map[string]http.HandlerFunc{
		"account":         b.account,
		"accountDelete":   b.accountDelete,
		"accountExport":   b.accountExport,
		"accountLimit":    b.accountLimit,
		"adminAbuse":      b.adminAbuse,
		"adminActivity":   b.adminActivity,
		"adminAppeals":    b.adminAppeals,
		"adminOverview":   b.adminOverview,
		"adminPayouts":    b.adminPayouts,
		"adminUnbanNode":  b.adminUnbanNode,
		"adminUnhold":     b.adminUnhold,
		"adminWhoami":     b.adminWhoami,
		"balance":         b.balance,
		"bands":           b.bands,
		"bandsByID":       b.bandsByID,
		"billing":         b.billing,
		"checkout":        b.checkout,
		"connectOnboard":  b.connectOnboard,
		"connectStatus":   b.connectStatus,
		"console":         b.console,
		"earnings":        b.earnings,
		"grants":          b.grants,
		"me":              b.me,
		"metricsProvider": b.metricsProvider,
		"metricsSeries":   b.metricsSeries,
		"metricsUsage":    b.metricsUsage,
		"ownerAppeal":     b.ownerAppeal,
		"ownerStrikes":    b.ownerStrikes,
		"payoutLots":      b.payoutLots,
		"payoutsEarnings": b.payoutsEarnings,
		"payoutsHistory":  b.payoutsHistory,
		"payoutsRequest":  b.payoutsRequest,
		"payoutsSubtree":  b.payoutsSubtree,
		"providerModels":  b.providerModels,
		"report":          b.report,
		"usage":           b.usage,
	}

	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodOptions, "/"+name, nil)
			r.Header.Set("Origin", "https://app.rogerai.fyi")
			h(w, r)
			if w.Code != http.StatusNoContent {
				t.Fatalf("OPTIONS %s = %d, want 204 (preflight short-circuit)", name, w.Code)
			}
		})
	}
}
