# ROGER EDGE - MODE: local by default, and you can always see it.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-19: "always being local by default so we should have a label clearly
# specifying the mode".
#
# TWO THINGS ARE BOTH CALLED LOCAL AND THEY MUST NEVER SHARE A LABEL. One is about trust and the
# other is about where your prompt went:
#
#   ROOT   what this Edge is rooted at: LOCAL (a machine the owner designated) or CORE.
#          It answers "can this Edge form and run with no internet?"  It is a property of the
#          Edge, it changes only when the owner changes it, and it is already built
#          (features/edge/enrollment.feature) but shown nowhere except `roger edge authority`.
#
#   ROUTE  where ONE TURN actually went: local (it stayed on hardware the owner owns) or market
#          (it went out through the broker, possibly to a stranger's station). It answers "did
#          this prompt leave the building?"  It is a property of a session and is new here.
#
# A third word, `edge.prefer`, is the owner's STANDING CHOICE about route, not a report of it:
# `local` (default - try the Edge first, fall out to the market), `market` (today's behaviour),
# `local-only` (never leave the Edge, and refuse honestly when the Edge cannot serve).
#
# THE RULE: a label is a report, never a promise. The ROUTE shown on a session is where the turn
# REALLY went, read from the evidence, never inferred from the preference that was set. A fleet
# that preferred local and fell out to the market says market, on the row, without being asked.
# And a status that could not be read is shown as unread - the Edge screen's existing rule
# (features/edge/empty_edge.feature) applies to the mode too: "rooted at Core" printed because a
# file could not be read is a confident lie the owner would act on.
#
# LOCAL-ONLY IS THE ONE THAT HAS TO BE TRUE. Under `local-only` no broker is contacted at all, by
# any path, and that is worth a structural test rather than a hopeful one: it is the setting
# somebody turns on because the data must not leave, and a single forgotten code path makes the
# whole claim false.
#
# GROUND TRUTH: internal/edgeauth (Descriptor.Kind, Names, NeedsNetwork - the ROOT, already
# built), internal/edge/status.go (SelfStatus, which already reports the authority and is
# drawn by all three surfaces), internal/edge/session.go (Session, which gains the ROUTE),
# internal/client/client.go (ProxyOptions and the dispatch that must honour prefer),
# features/edge/empty_edge.feature (the unread-is-unread rule this follows),
# features/edge/local_inference.feature (the ladder whose rungs produce these routes).
#
# Tags: @edge in internal/edge, @client in internal/client, @cli in cmd/rogerai, @tui in
# internal/tui, @console in internal/webui.

Feature: The Edge says what roots it and where each turn went, prefers local, and means it when the owner says local-only

  # =========================================================================
  # 1. ROOT: WHAT THIS EDGE IS ROOTED AT
  # =========================================================================

  @edge
  Scenario: a locally-rooted Edge reports LOCAL ROOT and names the machine
    Given an Edge rooted at the designated machine "shed"
    Then its mode reports the root as LOCAL
    And it names "shed"
    And it says enrolling a new node needs no network beyond this LAN

  @edge
  Scenario: a Core-rooted Edge reports CORE
    Given an Edge rooted at Core
    Then its mode reports the root as CORE
    And it says enrolling a new node needs the network

  @edge
  Scenario: a root that cannot be read is unread, never guessed
    Given the machine's Edge record cannot be read
    Then its mode reports the root as unknown, with the reason
    And it does not claim LOCAL
    And it does not claim CORE

  @tui
  Scenario: the root badge is in the Edge screen's header
    Given an Edge rooted at the designated machine "shed"
    When the Edge screen renders at 100 columns
    Then the header carries a mode badge reading LOCAL
    And the badge is present whether or not the fleet is empty

  @cli
  Scenario: the root is on the fleet listing, not only under `roger edge authority`
    Given an Edge rooted at the designated machine "shed"
    When they run "roger edge"
    Then the output states the root is LOCAL and names "shed"

  @console
  Scenario: the console header carries the same badge, in the same words
    Given a console over an Edge rooted at the designated machine "shed"
    When the Edge data is read
    Then it carries the root as LOCAL and the authority "shed"
    And the panel shows the badge in the header

  # =========================================================================
  # 2. ROUTE: WHERE THIS TURN WENT
  # =========================================================================

  @edge
  Scenario: a turn served by an instance on this Edge is routed local
    Given a receipted turn served by the instance "workshop/share" on this Edge
    When the session is read
    Then its route is local
    And it names the instance that served it

  @edge
  Scenario: a turn served through the broker is routed market
    Given a receipted turn served through the broker by the station "house-or-1"
    When the session is read
    Then its route is market
    And it names the station that served it

  @edge
  Scenario: a turn this instance served from its own loaded model is routed local
    Given a receipted turn served by this instance's own model
    When the session is read
    Then its route is local
    And it names this instance

  @edge
  Scenario: the route is read from the evidence, never from the preference
    Given the preference is local
    And a turn that fell out to the market because no instance served the band
    When the session is read
    Then its route is market
    And nothing about the preference changed what the row says

  @edge
  Scenario: a refused turn still carries the route it was refused on
    Given a turn refused for "over-limit" by a station on the market
    When the session is read
    Then its route is market
    And its outcome is the refusal, with the reason

  @tui
  Scenario: every session row carries its route, distinguishable without colour
    Given sessions that went local and sessions that went to the market
    And NO_COLOR is set
    When the Edge screen renders at 100 columns
    Then each row says which route it took
    And the two are distinguishable by text alone

  @tui
  Scenario: the header says the mix, so a fleet that is leaking is obvious at a glance
    Given 3 sessions that went local and 1 that went to the market
    When the Edge screen renders at 100 columns
    Then the header reports 3 local and 1 market

  @cli
  Scenario: `roger edge sessions` carries the route, in the table and in the JSON
    Given a local turn and a market turn
    When they run "roger edge sessions"
    Then each row says its route
    When they run "roger edge sessions --json"
    Then each row carries a route field reading local or market

  # =========================================================================
  # 3. THE PREFERENCE
  # =========================================================================

  @edge
  Scenario: the preference defaults to local on a machine that has never set it
    Given a machine with no preference set
    Then its preference is local
    And the mode says so

  @cli
  Scenario Outline: the owner sets the preference and it is reported back
    When they run "roger edge prefer <value>"
    Then the preference is "<value>"
    And "roger edge prefer" alone reports "<value>" and what it means

    Examples:
      | value      |
      | local      |
      | market     |
      | local-only |

  @cli
  Scenario: a preference that is not one of the three is a usage error
    When they run "roger edge prefer sometimes"
    Then it is a usage error naming the three values
    And the preference is unchanged

  @client
  Scenario: under local, an Edge instance that serves the band is used before the market
    Given the preference is local
    And an instance on this Edge serving "gpt-oss-120b"
    When a turn is dispatched for "gpt-oss-120b"
    Then it is served by that instance
    And no broker was contacted

  @client
  Scenario: under local, a band no instance serves falls out to the market
    Given the preference is local
    And no instance on this Edge serves "gpt-oss-120b"
    When a turn is dispatched for "gpt-oss-120b"
    Then it is served through the broker
    And the session's route is market

  @client
  Scenario: under market, the Edge is not tried first
    Given the preference is market
    And an instance on this Edge serving "gpt-oss-120b"
    When a turn is dispatched for "gpt-oss-120b"
    Then it is served through the broker, exactly as before this spec existed

  # =========================================================================
  # 4. LOCAL-ONLY MEANS IT
  # =========================================================================

  @client
  Scenario: under local-only a band no instance serves is refused, not bought
    Given the preference is local-only
    And no instance on this Edge serves "gpt-oss-120b"
    When a turn is dispatched for "gpt-oss-120b"
    Then it is refused
    And the refusal names the band and says no instance on this Edge serves it
    And it names how to change that: serve it here, or set the preference back to local
    And no broker was contacted
    And no money moved

  @client
  Scenario: under local-only nothing reaches the broker by any path
    Given the preference is local-only
    When a turn, an agent turn and a console turn are each dispatched
    Then no request left this Edge
    And each refusal or local answer is drawn as what it was

  @edge @later
  Scenario: local-only is structural, not a flag one code path can forget
    Given the preference is local-only
    Then the dispatch path cannot reach the broker client at all
    And that is enforced by construction rather than by a check at each call site

  @tui
  Scenario: local-only is visible, because an owner turns it on to be sure
    Given the preference is local-only
    When the Edge screen renders at 100 columns
    Then the header says LOCAL-ONLY
    And it is distinguishable from a merely locally-rooted Edge

  # =========================================================================
  # 5. THE TWO WORDS DO NOT BLUR
  # =========================================================================

  @edge @later
  Scenario: a Core-rooted Edge can still route local
    Given an Edge rooted at Core
    And an instance on this Edge serving "gpt-oss-120b"
    And the preference is local
    When a turn is dispatched for "gpt-oss-120b"
    Then its route is local
    And the root is still reported as CORE
    And the two labels are shown separately, never collapsed into one word

  @edge @later
  Scenario: a locally-rooted Edge contacts Core for nothing, dispatch included
    Given an Edge rooted at the designated machine "shed"
    Then no path from enrolment, discovery or dispatch reaches Core
    And that is proven the way the local authority already proves it, by what the code links

  @tui
  Scenario: the header never shows one label where two are meant
    Given an Edge rooted at Core with the preference local
    When the Edge screen renders at 100 columns
    Then the root and the route mix are both readable
    And neither is presented as the other
