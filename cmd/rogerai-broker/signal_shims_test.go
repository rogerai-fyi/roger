package main

// signal_shims_test.go holds the two thin signal-scoring shims that exist ONLY for the
// monotonicity / pure-math tests (TestMarketSignal, TestSignalAndLoadHelpers,
// signal_routing_test). Production scores channels via computeSignal directly with the full
// evidence (market.go), so these belong with the tests, not in the shipped binary.

// offerSignal scores a SINGLE offer 0..100 using the exact same blend /market uses, so the
// band list's meter and the /market view agree for the same node. providers=1 for one offer;
// an offline offer is 0 (no supply).
func offerSignal(online bool, inflight int, tps, ttftMs, successRate, trust, recency float64, verified bool) int {
	if !online {
		return 0
	}
	return computeSignal(signalInput{
		providers: 1, inflight: inflight, bestTPS: tps, ttftMs: ttftMs,
		successRate: successRate, trust: trust, recency: recency, verified: verified,
	}).Total
}

// marketSignal is the original 5-arg blend (supply + speed + success + trust, idle-fresh,
// no probe evidence) kept as a concise test helper for the monotonicity assertions.
func marketSignal(providers, inflight int, bestTPS, successRate, trust float64) int {
	return computeSignal(signalInput{
		providers: providers, inflight: inflight, bestTPS: bestTPS,
		successRate: successRate, trust: trust, recency: 1,
	}).Total
}
