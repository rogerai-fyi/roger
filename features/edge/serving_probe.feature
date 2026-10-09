# ROGER EDGE - THE SERVING PROBE: how an instance's `serve` goes from CLAIMED to VERIFIED, for real.
#
# STATUS: APPROVED 2026-09-28 (founder: "approved"); written the night of the second audit. Not godog-bound: every
# scenario below is pinned by Go tests instead - internal/edge/probe_test.go (the face path and
# the client), internal/edge/discovery_probe_test.go (discovery runs it), and
# cmd/rogerai/edgeselfprobe_test.go (the host probes its own household).
#
# WHY. instances.feature §2 says a claimed capability is CLAIMED "until it is verified", and that
# "the serving probe" is what verifies serve on ONE instance. local_inference.feature and
# addressed_inference.feature then route ONLY on a VERIFIED serve. The audit of 2026-09-27 found
# that nothing in production ever ran that probe: the only production path to VERIFIED serve was
# a broker-registered Station. On an Edge with no broker - the whole point of an airgap-capable
# fabric - every peer stayed CLAIMED forever, so the peer ladder never had a rung, `roger use
# <node>` always refused, and the Ask action never lit.
#
# WHAT. A member-only read on the LAN face: GET /edge/probe?band=<band>. The face resolves the
# band to the server behind it (the same Upstream the infer path uses) and asks THAT server for
# its model list; it answers 200 only when the server is up and lists the band. Nothing is
# generated, no tokens are spent, no session is receipted. Discovery runs the probe against each
# instance of a node it has just certificate-verified whose serve is still CLAIMED, and records a
# pass on THAT instance (RecordInstanceProbe). The host runs the same check on its own household,
# directly against its own face's upstream map - the same probe, not a self-declaration.
#
# WHAT IT IS NOT. A peer's word: the face cannot promote itself by answering "yes" - the probe
# reaches the server behind the face, and a face that lies about that lies with its certificate on.
# It is not a completion: a probe never costs the owner a token or a receipt.
#
# GROUND TRUTH: internal/edge/probe.go, internal/edge/discovery.go (verifyServe),
# internal/edge/fleet.go (RecordInstanceProbe, applyInstances), cmd/rogerai/edgehost.go (observeSelf).

Feature: the serving probe

  Background:
    Given an Edge whose authority issued certificates to "desk" and to the phone "gentle-ibex-14"
    And "desk" runs an instance "serve" that claims serve for the band "qwen"
    And behind "desk"'s face an OpenAI-compatible server lists the model "qwen"

  # ---- the face path -------------------------------------------------------

  @edge
  Scenario: a member probes a band the node really serves
    When a member GETs /edge/probe?band=qwen with its certificate
    Then the answer is 200 and names the instance "serve" and the band "qwen" as served

  @edge
  Scenario: the probe is a member-only read
    When a caller with no certificate probes "qwen"
    Then it is refused 401
    When a caller holding a certificate from another authority probes "qwen"
    Then it is refused 401, as every member-only path refuses a certificate not under this root
    When a caller under this root but not a member of this Edge probes "qwen"
    Then it is refused 403

  @edge
  Scenario: a probe names the band it checks
    When a member probes with no band
    Then it is refused 400 and told a serving probe names the band it checks

  @edge
  Scenario: a band this node does not serve is a 404, naming what it does serve
    When a member probes "gpt-oss-120b"
    Then it is refused 404 and the message says this node does not serve it

  @edge
  Scenario: the server behind the face must actually list the band
    Given the server behind the face lists only "llama-3"
    When a member probes "qwen"
    Then it is refused 502 and the message says the server behind qwen does not list it
    And serve is not verified

  @edge
  Scenario: a server that is down fails the probe
    Given the server behind the face is not answering
    When a member probes "qwen"
    Then it is refused 502

  @edge
  Scenario: the probe is a GET
    When a member POSTs to /edge/probe
    Then it is refused 405

  # ---- the client ------------------------------------------------------------

  @edge
  Scenario: the client pins the face before it sends a byte
    When the probe client dials a face whose certificate does not match the pin
    Then it fails, naming the pin, and no request reaches the face

  @edge
  Scenario: the client carries the face's reason
    When the probe client is refused with a nested error message
    Then the error it returns carries that message, not just the status

  # ---- discovery ---------------------------------------------------------------

  @edge
  Scenario: every band an instance advertises must answer
    Given "desk" runs an instance "serve" claiming serve for "qwen" and "mistral"
    And the server behind the face lists only "qwen"
    When serve is probed on "desk/serve"
    Then it stays CLAIMED: proving one band would let the fleet route the other
    When the server lists both
    Then serve on "desk/serve" is VERIFIED

  @edge
  Scenario: the record says WHICH bands were proven, and only those route
    Given serve on "desk/serve" is VERIFIED, proven for "qwen" and "mistral"
    Then the record names those two bands as proven
    And the ladder dispatches "qwen" to it
    When the instance also advertises "llama-3", which nothing proved
    Then the ladder does not dispatch "llama-3" to it, though the instance advertises it
    And a record written before the probe kept a band list falls back to what it advertises,
      so an older Edge routes exactly as it did

  @edge
  Scenario: a set that grows back to an already-proven shape is not probed again
    Given serve was proven for "qwen" and "mistral"
    When the instance advertises "qwen" only, and later "qwen" and "mistral" again
    Then nothing is probed on either change, and serve stays VERIFIED throughout
    And a band outside the proven set still asks for a fresh probe

  @edge
  Scenario: a shrinking band set keeps the verdict; a growing one is re-probed
    Given serve on "desk/serve" is VERIFIED for "qwen" and "mistral"
    When the instance advertises "qwen" only
    Then serve stays VERIFIED and nothing is probed again
    When it advertises "qwen", "mistral" and "llama-3"
    Then serve returns to CLAIMED until the probe answers for all three
    And an instance advertising nothing is never counted as proven

  @edge
  Scenario: discovery verifies serve on the instance the probe passed, and only that one
    Given the node "desk" was just certificate-verified with instances "serve" (claims serve, band qwen) and "lab" (claims serve, band qwen)
    And the probe for "qwen" answers with the instance "serve"
    When discovery verifies serve on "desk"
    Then serve on "desk/serve" is VERIFIED
    And serve on "desk/lab" is still CLAIMED

  @edge
  Scenario: a failed probe leaves serve CLAIMED
    Given the probe fails
    When discovery verifies serve on "desk"
    Then serve on "desk/serve" is still CLAIMED

  @edge
  Scenario: an instance already VERIFIED is not probed again
    Given serve on "desk/serve" is already VERIFIED
    When discovery verifies serve on "desk"
    Then the probe is not called

  @edge
  Scenario: a machine that has not enrolled probes nothing
    Given this machine holds no certificate
    When discovery verifies serve on "desk"
    Then the probe is not called

  # ---- the host's own household -------------------------------------------------

  @edge
  Scenario: the host verifies its own instance through its own face's upstream map
    Given this machine's roger has "qwen" on air, served by a local server that lists it
    When the host observes itself
    Then serve on its own instance is VERIFIED
    And the peer ladder and the Ask action now have a rung on it

  @edge
  Scenario: the host does not verify what its face cannot route
    Given a household instance claims serve for "mistral" but this face's upstream map has no "mistral"
    When the host observes itself
    Then serve on that instance is still CLAIMED

  # regression 2026-10-09: a member that joined after a peer was already serving saw that peer's serve
  # stay CLAIMED forever, because a second sighting filed the advertised bands as proven.
  @edge
  Scenario: a claimed serve seen again is still probed
    Given "desk" was observed with serve CLAIMED for "qwen" and has not been probed
    When "desk" is observed again with the same instances
    Then no band is recorded as proven for it
    And the next discovery pass probes it and serve becomes VERIFIED
