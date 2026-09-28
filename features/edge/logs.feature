# ROGER EDGE - THE LOG TAIL: reading a unit's recent log from another machine, with secrets removed.
#
# STATUS: APPROVED 2026-09-28 (founder: "approved", after reading the REDACTION section). Not
# godog-bound: pinned by Go tests - internal/edge/logtail_test.go (the ring, the redaction),
# cmd/rogerai/edgelogs_test.go (the action and `roger edge logs`) and internal/tui/
# logcap_mirror_test.go (the mirror that makes the ring non-empty under a terminal).
#
# WHY. It is the feature an operator reaches for most in every fleet manager: a unit is behaving
# oddly and you want its last hundred lines without walking to it or opening an SSH session. Roger
# Edge could say a unit was in trouble, what it ran and what changed, and nothing at all about what
# it SAID while doing it. A terminal already keeps its own ring for /log; this is that, for a member.
#
# THE SAME TWO LOCKS AS MAINTENANCE, and deliberately so. A machine serves no log until its owner
# runs `roger edge maintenance on` THERE, and the caller must hold an authority-signed grant scoped
# to `maintain` on that node. Reading a machine's log is administrative access to that machine - not
# less than starting an agent it already named, arguably more - so it sits behind the same switch
# rather than a weaker one. If the founder later wants to allow reading without allowing acting,
# that is a capability split, and this spec is where it would be written.
#
# NO NEW PATH. The tail is a MAINTENANCE ACTION on /edge/invoke, not a face path of its own. That is
# the whole reason it is safe to add: it inherits every check the invoke path already makes - the
# mutual TLS, the grant's signature under this Edge's root, the revocation applied from the local
# authority store, the caller matched to its certificate, the capability scope of the grant, the
# owner's switch on this machine, the replay cache, the staleness bound, the loop check - and adds
# not one line of authorization code of its own. A first draft served a GET at /edge/logs and could
# not check the grant at all, because a GET carries none; that draft is not what shipped.
#
# REDACTION, which is the whole risk. Log lines on this fleet DO carry secrets: an upstream key is on
# the command line of every share process on this machine today. Before a line leaves the process it
# is redacted - API keys by their shapes (sk-, csk-, sk-or-v1-, xai-, gsk_), bearer tokens, anything
# after key=, token=, secret=, password= or authorization:, and any long unbroken high-entropy run -
# and each redaction is replaced by a marker that says something was removed rather than silently
# dropping it. Redaction is applied ON THE WAY OUT, so it also covers lines a roger logged before
# this existed, and it is applied on the SERVING side, so a caller cannot ask for the raw text.
#
# WHAT IT IS NOT. Not a file read: the only thing served is this process's own in-memory ring, so no
# path comes from the caller and nothing on disk is reachable. Not history: the ring holds the last
# few hundred lines and nothing older. Not another machine's: a roger serves its own lines only.
#
# GROUND TRUTH: internal/edge/logtail.go (the ring and the redaction), internal/edge/maintain.go
# (the action), cmd/rogerai/edgemaintain.go (the callee), cmd/rogerai/edgelogs.go (the arrow),
# cmd/rogerai/edgehost.go (the tee onto the std logger).

Feature: the log tail

  Background:
    Given an Edge whose authority issued certificates to "desk" and "shed"
    And maintenance is on at "shed", and desk holds a maintain grant for it

  # ---- the ring -------------------------------------------------------------

  @edge
  Scenario: a roger keeps its own last lines, and keeps logging where it always did
    When a roger writes log lines
    Then its ring holds them, newest last
    And the lines still reach the place they went before: the ring is a tee, not a diversion
    And the ring is bounded: past its line cap the oldest go, and one absurdly long line is cut

  @edge
  Scenario: the ring is safe to read while it is being written
    When lines are written from several goroutines while the tail is read
    Then no read tears a line and no read races a write

  # ---- redaction ------------------------------------------------------------

  @edge
  Scenario: a secret never leaves the process
    Given log lines containing an OpenAI-style key, a Cerebras key, an OpenRouter key, a bearer
      token, "key=hunter2", "authorization: Basic abc123", and a 60-character base64 run
    When the tail is read
    Then not one of those values is in the answer
    And each is replaced by a marker saying something was removed
    And the rest of the line is intact, so the line is still worth reading

  @edge
  Scenario: redaction happens on the serving side, on the way out
    When a caller asks for the tail
    Then the lines are redacted before they are written to the wire
    And a line logged before redaction existed is redacted too, because it is applied on the read
    And no option, flag or query asks for the raw text

  # ---- the face -------------------------------------------------------------

  @edge
  Scenario: the tail is a maintenance action behind the owner's switch
    When a member with a maintain grant asks for the tail
    Then the answer carries the lines, oldest first, with how many the ring holds
    And the node's history records that the log was read, and by whom
    When maintenance is off at "shed"
    Then it is refused, saying maintenance is off on that machine
    When a caller with no certificate asks
    Then it is refused 401 by the invoke path, as every other action is
    When a member whose grant does not carry maintain asks
    Then it is refused: the grant's capability scope is checked before the action runs
    When a member whose certificate was revoked asks
    Then it is refused
    And a replayed request is refused by the nonce cache

  @edge
  Scenario: the caller may ask for fewer lines, and never for more than the ring holds
    When a member asks for 20 lines
    Then 20 are returned, the newest 20
    When it asks for a thousand
    Then what the ring holds is returned, rather than refusing
    When it asks for none or a negative number
    Then a sensible default is returned

  @edge
  Scenario: a roger with nothing logged answers emptily, not with an error
    When the tail is read on a roger that has logged nothing
    Then the answer is 200 with no lines, and the surfaces say nothing has been logged

  # ---- the surfaces ---------------------------------------------------------

  @cli
  Scenario: `roger edge logs`
    When the owner runs `roger edge logs shed`
    Then the redacted lines are printed, newest last, with the unit named
    And `--lines 20` asks for twenty
    And with no grant it says which grant is missing and how to issue one
    And on a node with no LAN face it says that, rather than hinting at a grant
