# OPERATOR-DECLARED MINIMUM PROBE INTERVAL - verification an expensive upstream can afford,
# at a price that makes it useless for gaming the mark.
#
# Measured 2026-10-04: two personal shares whose upstream runs a full agent session per
# request (about $0.10-0.19 each) received ~42,600 requests in 4 days, every one of them
# the broker's own canaries, about $585 of cost and zero real users. Browsing pulled the
# nodes back to the 30s floor indefinitely. The curated lane already protects metered
# commercial proxies (curated_probes.feature); an ordinary share had no way to say "this
# upstream is expensive, check me rarely".
#
# Founder approval 2026-10-04, with the question that shaped it: "what prevents any
# arbitrary number". The answer is that the declaration is capped and it costs the node:
#   - probe_min_s is a SIGNED registration field (it rides regSigningBytes like Curated)
#   - the broker clamps it to [0, ROGERAI_PROBE_MIN_CAP], default 24h
#   - verification is never extended: past the normal window the node reads as not
#     currently verified, and the staleness discount ranks it below fresh nodes

Feature: An operator can declare a minimum probe interval, and pays for it in verification
  As an operator whose upstream bills real money for every request
  I want the broker's canaries limited to an interval I choose
  So that verification cannot quietly cost more than my station earns.

  Scenario: No probe path can probe a station sooner than its declared minimum
    Given a station that declared a 6 hour minimum probe interval and was probed an hour ago
    When consumers browse the market repeatedly and a pick routes to it on a stale reading
    Then no probe fires before the declared minimum
    And once the declared minimum has passed the station is probed again

  Scenario: The first probe at first sight still happens
    Given a station that declared a 6 hour minimum probe interval and was never probed
    When probe rounds run
    Then the station is probed exactly once
    # the first canary is what earns verification, exactly as on the curated lane

  Scenario: An arbitrary number is clamped to the cap
    Given a station that declared a minimum probe interval of 30 days
    Then the broker honours at most 24 hours
    And the clamp is logged once for that station

  Scenario: Declaring a minimum never extends verification
    Given a station that declared a 6 hour minimum probe interval
    When its last passed probe is older than the normal verification window
    Then the market and discover show it as not currently verified
    And its verified tools mark is not re-asserted while it waits
    And real served traffic refreshes verification exactly as it does for any station

  Scenario: A rarely probed station ranks below a fresh one
    Given a rarely probed station and a freshly probed station for the same model at the same speed
    Then the rarely probed station takes the staleness discount and ranks below the fresh one

  Scenario: The trade is disclosed where the operator opts in
    Then the share help says the minimum is capped at 24h and that verification lapses between probes
