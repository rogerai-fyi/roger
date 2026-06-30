# Cross-instance RATE LIMITS (P2-3). The per-IP anon + concierge limiters were wired onto a
# SHARED Valkey bucket, but the per-identity (b.rl) and per-grant (b.grantRL) limiters were
# left LOCAL — a "Stage 1" carve-out. At the 2-instance cap that means a signed user / grant
# key gets ~2x its configured RPM: each instance enforces its own private bucket, so the load
# balancer doubles the real throughput one key can push. The shared token bucket (rateAllow)
# already exists; this just wires b.rl + b.grantRL onto it, under DISTINCT key namespaces so
# the two limiters never collide.
#
# GROUND TRUTH: main.go (buildBroker limiter wiring) + ratelimit.go (allow/allowAt -> shared
# rateAllow, with a local fallback on a Valkey error). Enforced by rate_limits_bdd_test.go:
# two REAL brokers (buildBroker) sharing ONE in-process Valkey — a key whose budget is
# exhausted on instance A is then DENIED on instance B (one shared limit, not two).

Feature: Cross-instance rate limits

  Scenario: A signed-user RPM is enforced ACROSS instances, not per-instance (no 2x)
    Given two brokers A and B built on one shared Valkey with a per-identity burst of 2
    When identity "u1" exhausts its request budget on instance A
    Then the same identity "u1" is denied on instance B

  Scenario: A grant-key RPM is enforced ACROSS instances, not per-instance (no 2x)
    Given two brokers A and B built on one shared Valkey
    When grant "g1" exhausts a burst of 2 on instance A
    Then the same grant "g1" is denied on instance B

  Scenario: buildBroker wires the per-identity + per-grant limiters onto distinct shared buckets
    Given a broker built with a shared Valkey backend
    Then its per-identity and per-grant limiters each have a named shared bucket
    And those two bucket names are distinct
