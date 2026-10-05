package main

// legacy_shared_boot_test.go - the pre-2026-10-04 shared-store boot, kept ONLY as a test
// fixture. Production no longer boots this way: a broker configured to share state that
// cannot reach the store at boot is NOT READY (bootShared, features/ops/shared_store_readiness
// .feature). features/moderation/off_path_screening.feature "a Valkey outage has no effect on
// screening or serving" still pins this old fallback (its step asserts the fallback log line);
// that scenario's premise needs a founder ruling, and this helper goes when it is re-ruled.

import "log"

// openSharedStore builds the shared-state layer from ROGERAI_REDIS_URL. UNSET (the
// default + the fallback) returns nil: the broker uses its in-memory maps with ZERO
// behavior change. SET connects a valkeyStore; a connection failure at startup
// DEGRADES GRACEFULLY - it logs a warning and returns nil so the broker boots on the
// in-memory path and NEVER crashes. (The returned store is closed on a connect
// failure so we leak no client.)
func openSharedStore() sharedStore { // legacy test fixture, see the file header
	tp, ok := valkeyTopologyFromEnv()
	if !ok {
		return nil // flag OFF: in-memory, byte-for-byte today's behavior.
	}
	vs, err := newValkeyStoreTopology(tp)
	if err != nil {
		if vs != nil {
			_ = vs.Close()
		}
		log.Printf("shared-state: ROGERAI_REDIS_URL set but connect failed, falling back to in-memory (broker continues): %v", err)
		return nil
	}
	log.Printf("shared-state: valkey connected (keys namespaced under %q) - sharing anon/concierge rate limits + node liveness across instances", keyPrefix)
	return vs
}
