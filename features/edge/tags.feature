# ROGER EDGE - TAGS: the owner's own words for what a unit IS, and rules that cover a group.
#
# STATUS: PROPOSED 2026-09-27. Not godog-bound: pinned by Go tests - internal/edge/tag_test.go
# (validation, the fleet methods, precedence), internal/edge/alert_test.go (a tag-scoped rule
# judged), cmd/rogerai/edgetag_test.go (`roger edge tag`, `list --tag`, `alert --scope tag:`),
# internal/fleetview and internal/webui tests (the tags on the readout and the card).
# Founder review pending.
#
# WHY. Every fleet manager lets an operator label a device and then act on the label: it is how a
# fleet of a dozen becomes three groups an operator can reason about. Roger Edge had exactly two
# scopes for a rule - the whole fleet, or one node by id - so "warn me when any PRODUCTION box runs
# hot, and leave the bench alone" could not be said at all. Tags are the owner's words, kept on the
# node record beside the name they chose, and a rule may be scoped to one.
#
# WHAT. A node carries up to twelve tags. A tag is the owner's own word, lowercased: letters, digits,
# dot, dash, underscore, starting with a letter or a digit, at most 32 characters, never containing a
# colon (the scope prefix uses one). `roger edge tag <node>` shows them; `+word` adds, `-word`
# removes, a bare list replaces, `--clear` empties. `roger edge list --tag <tag>` lists that group.
# An alert rule's scope may be `tag:<tag>`, and the rule in force for a node is the strictest of:
# its own node-scoped rule, then the tightest rule on any tag it carries, then the fleet rule.
#
# WHAT A TAG IS NOT. Not a capability, not a permission, and not a grant: tagging a node "authority"
# or "admin" gives it nothing. Tags name and group; the certificate and the grant decide what may
# happen. A tag is never read from a peer's advertisement or its describe - it is the OWNER's word,
# written here, so a node cannot label itself into a group whose rules are looser.
#
# GROUND TRUTH: internal/edge/tag.go, internal/edge/alert.go (RuleSet.applying), internal/store/
# edgenode.go (Tags), cmd/rogerai/edgetag.go, cmd/rogerai/edgealert.go.

Feature: tags and group rules

  Background:
    Given an Edge with the members "desk", "shed" and "quiet-pi"

  # ---- the words themselves -------------------------------------------------

  @edge
  Scenario: a tag is the owner's word, normalised and validated
    When the owner tags "desk" with "Production"
    Then the tag is kept as "production"
    And tagging with "  prod  " keeps "prod"
    And these are refused, each saying why: "", "a b", "tag:prod", "-lead", "ÜBER", a 33-character word
    And a node may carry at most twelve tags, and the thirteenth is refused
    And the same tag twice is one tag, and the list is kept sorted

  @edge
  Scenario: tags are the owner's, never the node's
    Given a peer's describe answer claims the tags "production" and "authority"
    When it is verified and observed
    Then its record carries no tags from that answer
    And a tag gives it no capability, no permission and no grant

  @edge
  Scenario: adding, removing, replacing and clearing
    Given "desk" carries "production" and "gpu"
    When the owner adds "+rack1" and removes "-gpu"
    Then "desk" carries "production" and "rack1"
    When the owner sets the list "bench, lab"
    Then "desk" carries exactly "bench" and "lab"
    When the owner clears them
    Then "desk" carries none, and that is not an error
    And removing a tag it does not carry is not an error
    And every change is on the node's history

  @edge
  Scenario: forgetting a node forgets its tags with it
    Given "shed" carries "production"
    When the owner forgets "shed" and later re-admits it
    Then it carries no tags: the owner's words went with the record

  # ---- rules that cover a group ---------------------------------------------

  @edge
  Scenario: a rule scoped to a tag judges every node carrying it
    Given the fleet rule "cpu_temp > 90" and the rule "cpu_temp > 70" scoped to "tag:production"
    And "desk" carries "production" and "quiet-pi" carries nothing
    When desk reads 75°C and quiet-pi reads 75°C
    Then a call stands for desk, against 70
    And no call stands for quiet-pi, which is judged against 90

  @edge
  Scenario: the node's own rule beats a tag rule, and a tag rule beats the fleet
    Given the fleet rule "disk > 90", a "tag:production" rule "disk > 80" and a desk rule "disk > 95"
    And "desk" carries "production"
    When desk reads 92%
    Then no call stands for desk: its own rule is what applies

  @edge
  Scenario: two tag rules on one node: the tightest applies
    Given a "tag:production" rule "cpu_temp > 70" and a "tag:summer" rule "cpu_temp > 65"
    And "desk" carries both tags
    When desk reads 68°C
    Then a call stands for desk, against 65
    And for a "lt" metric the tightest is the HIGHEST threshold, not the lowest
    And when two tag rules tie, the same one is chosen every time

  @edge
  Scenario: a rule scoped to a tag nothing carries is kept and judges nothing
    Given a "tag:winter" rule and no node carrying "winter"
    When the fleet is judged
    Then nothing fires from it, and the rule still lists with its scope

  @edge
  Scenario: an unparseable or empty tag scope is refused where the rule is made
    When the owner sets a rule scoped to "tag:" or "tag:a b"
    Then it is refused, saying what a tag may be
    And a scope that is neither a node nor "tag:<tag>" is refused as an unknown node

  # ---- the surfaces ---------------------------------------------------------

  @cli
  Scenario: `roger edge tag` and `roger edge list --tag`
    When the owner runs `roger edge tag desk`
    Then it prints the tags desk carries, or says it carries none
    When the owner runs `roger edge tag desk +production +gpu`
    Then it says what desk carries now
    When the owner runs `roger edge list --tag production`
    Then only the nodes carrying "production" are listed, with the tag column
    And `roger edge list --tag nothing` says no node carries it, rather than printing an empty fleet

  @edge
  Scenario: the fleet screen and the console show the tags
    When the owner selects "desk"
    Then the readout has a tags row reading "production · gpu"
    And the console's node card shows the same words
    And a node with no tags draws no row, never an empty one
