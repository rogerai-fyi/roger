# BROADCAST 010 - "Run a Tower", the relay operator's field guide.
#
# The Tower shipped, and the only places that say so are a concept page and a manual
# section. Neither is something a person finds by wondering out loud whether they can host
# part of an AI network. This broadcast is that door: what a Tower is, what it can see,
# what it pays, and the commands to bring one up.
#
# GROUND TRUTH. Every load-bearing claim traces to a committed spec, and the article may
# not get ahead of any of them:
#
#   features/tower/operator_revenue_share.feature - the split is 90 to the serving node,
#     5 to the tower operator, 5 to the platform (rates amended by the 2026-09-01 fee
#     ruling; 70/10/20 at first airing), measured against the NODE'S OWN listed
#     price (gross), on Core's own recount rather than the node's claim.
#
#   features/tower/edge_dispatch.feature - the consumer authorizes at Core and seals the
#     request to the SERVING NODE'S session key. The tower's hub queues ciphertext; the
#     node opens, serves, signs the token receipt and seals the answer back. The tower
#     holds no key to the content in either direction.
#
#   features/tower/packaging.feature - Linux amd64/arm64 only, installed by the shared
#     installer with ROGERAI_COMPONENT=tower, checksum-verified before it installs.
#
# THE HONESTY RAIL THAT MATTERS. Relay compensation is built end to end and lands on the
# same Stripe payout rail as serving. What is thin is TRAFFIC, not pay. So the article may
# describe the mechanism as live, and may NOT present the 10% as an expected income: every
# earning sentence stays conditional on demand, and the page says out loud that a new
# Tower's figure starts at zero and follows the network rather than the install date. An
# operator who reads this page and then sees zero on their payouts page must find that
# unsurprising, not misleading.
#
# THE CLAIM THIS PAGE MAY MAKE THAT tower.html MAY NOT. features/web/tower.feature forbids
# tower.html from claiming end-to-end encryption, because the sections above its download
# box describe the BROKER, which does handle prompt content. This article is about the
# OPERATOR-RUN Tower only, which genuinely holds no content key - so it may say so, and it
# must scope the claim to the relay rather than to the platform.

Feature: The field guide that turns the Tower into something a person can run

  Background:
    Given the built site

  Scenario: The broadcast exists and is findable as a broadcast
    Then broadcasts-run-a-tower.html builds and is in the sitemap
    And the broadcast index lists it at the top as the newest transmission
    And it carries a broadcast number that follows the previous one
    And it links to the concept page and to the operator runbook

  Scenario: The split is stated with the base it is measured against
    Then the article states 90% to the node, 5% to the tower operator, 5% to the platform
    And it says the percentage is taken against the serving node's own listed price
    And it never describes the 5% as a rate the platform sets

  Scenario: Earning copy stays conditional on demand
    Then the article says plainly that traffic through any one Tower is early
    And it tells the reader the relay figure starts at zero and follows demand
    And it makes no projection, estimate or per-month figure of relay income

  Scenario: The confidentiality claim is scoped to the relay
    Then the article says the key to the content is never handed to the Tower
    And it explains that the consumer seals the work to the serving node's key
    And it admits the Tower still sees traffic shape - which station, how many bytes, when

  # The first draft of this page was headlined "get paid to carry what you cannot read".
  # Two things are wrong with that. It reads as a dare - the natural reply to "you cannot"
  # is "watch me" - and it locates the guarantee in the operator's restraint, when the
  # actual guarantee is that the key is never handed over. An operator who wanted to read
  # the traffic could not, and that is a stronger and more honest sentence than one that
  # asks them not to.
  Scenario: The confidentiality claim is never phrased as a dare
    Then no copy tells the reader what they cannot read
    And the guarantee is stated as a key never given rather than a rule to keep
    And this holds in the page metadata and the transmission log entry too

  # THE SECOND REASON TO RUN ONE. features/tower/modes.feature: a Tower is initialized as
  # exactly one of joined or standalone, once per data directory, forever. Standalone is a
  # private local network with its own trust root, loopback by default, no RogerAI
  # discovery or settlement, and it shipped FIRST (phase 1 of the network plan). It is a
  # real product surface for anyone who wants one endpoint in front of their own machines,
  # and the page should say so - the honesty rail is that it cannot earn.
  Scenario: Standalone is offered as a private relay with its trade stated
    Then the article names both modes and prints the --mode standalone flag
    And it says a standalone Tower has its own trust root and binds to loopback by default
    And it says outright that a standalone Tower earns nothing
    And it attributes that to construction rather than to policy or to a missing feature
    And it warns that a data directory is one mode for life

  Scenario: The install surface matches what the release actually ships
    Then the article prints the ROGERAI_COMPONENT=tower installer line
    And it says Linux amd64 and arm64 only
    And it never tells the reader a Tower needs a GPU

  Scenario: Sign-in is not described as GitHub-only
    Then where the article mentions signing in, it names more than one provider
