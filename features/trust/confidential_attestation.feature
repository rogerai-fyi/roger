# Confidential tier — the "TEE guarantee": a node only earns the `confidential ◆` badge
# after the broker CRYPTOGRAPHICALLY verifies a hardware attestation quote (today AMD
# SEV-SNP). The honesty rule runs both ways: a node with no TEE never sends a fake claim,
# and the broker never grants the badge unless the signature chain, the freshness/binding,
# AND the launch-measurement allowlist all pass. `roger use --confidential` then routes ONLY
# to verified nodes. This file is the founder-approvable spec; enforcement is the Go tests
# cited inline. Spec only — NO step definitions, NO Go.
#
# GROUND TRUTH:
#   Node side (the prover):
#     - internal/agent/attest.go:
#         detectTEE() -> teeAvailable() (build-tagged): "sev-snp" only inside a real CVM.
#         attestForRegistration(): fetch a fresh nonce, report_data = SHA-512(pubkey||nonce),
#           generateQuote(report_data); on NO device it returns an error and CLEARS the claim
#           (Confidential=false, Attestation/AttestKind/AttestNonce all emptied) — never a fake.
#         ConfidentialPreflight() -> ErrNoTEEDevice when /dev/sev-guest is absent (the cheap,
#           local "are you even eligible" check `roger share --confidential` runs first).
#     - internal/agent/attest_sevsnp.go (linux&&amd64): teeAvailable() opens /dev/sev-guest;
#         generateQuote() returns the raw extended report (ATTESTATION_REPORT || VCEK certs).
#     - internal/agent/attest_stub.go (all other platforms): teeAvailable() == "" always.
#     - internal/agent/agent.go: Start() runs attestForRegistration when cfg.Confidential;
#         the reregistrar re-attests on EVERY re-register (fresh nonce+quote) and drops to
#         standard for that round if re-attestation fails; Session.Confidential() reports
#         whether the broker GRANTED the badge on the last register (the response echo).
#         registerResult.Confidential is parsed from the broker register response.
#   Broker side (the verifier) — cmd/rogerai-broker/attest.go:
#         verifyRegistration(): no claim -> (false,nil); valid quote -> (true,nil);
#           failed claim with require=false -> (false,nil) fail-soft; require=true -> reject.
#         sevSNPVerifier.Verify(): gate 1 signature chain VCEK->ASK->ARK->AMD root
#           (go-sev-guest); gate 2 report_data == hash(pubkey||nonce); gate 3 launch
#           measurement in the allowlist; plus the TCB floor (MinimumTCB / MinimumLaunchTCB).
#         loadAttestRegistry(): allowlist from ROGERAI_TEE_MEASUREMENTS[_FILE]; empty => the
#           tier is UNAVAILABLE (fail-closed). require=ROGERAI_TEE_REQUIRE; reattest cadence
#           ROGERAI_TEE_REATTEST (1h); nonce TTL ROGERAI_TEE_NONCE_TTL (5m).
#         issueNonce()/consumeNonce(): single-use, short-lived (anti-replay).
#         reattestSweep() (tunnel.go): drops verified status that lapsed its cadence.
#   Broker register handler — cmd/rogerai-broker/tunnel.go register(): verifyRegistration
#         BEFORE b.mu; require-mode failure -> 403 with the reason; the OK response echoes
#         "confidential": <granted> so the node learns granted-vs-downgraded.
#   Consumer routing — cmd/rogerai-broker (market/pick): confidentialOnly routes ONLY to a
#         verified-confidential node; a standard node is never selected for confidential
#         traffic even if cheaper / the only one online.
#   Image + pinning (trust the math; not hardware-tested here):
#     - image/cvm/ reproducible direct-boot CVM (flake.nix): OVMF+kernel+initrd+dm-verity rootfs.
#     - tee/measure.sh: sev-snp-measure wrapper -> the 48-byte (96-hex) launch digest.
#     - tee/measurements/*.hex: the committed allowlist (one hex/line, # comments).
#     - .github/workflows/tee-measurement.yml: CI drift gate (rebuild -> measure -> must equal
#         the committed hex). docs/tee-runbook.md: the operator runbook.

@trust @confidential @tee
Feature: Confidential tier — verified TEE attestation (AMD SEV-SNP)

  Background:
    Given the broker pins an approved launch-measurement allowlist
    And a fresh single-use attestation nonce is issued per registration

  # =========================================================================
  # NODE-SIDE HONESTY: no hardware -> no claim, ever (the prover never lies).
  # Ground: internal/agent/attest.go, attest_stub.go; cmd/rogerai cmdShare.
  #   Go: internal/agent TestConfidentialPreflightNoDevice,
  #       TestAttestForRegistrationNoDeviceClearsClaim;
  #       cmd/rogerai TestShareConfidentialNoDeviceAborts.
  # =========================================================================
  Rule: A node with no TEE device never sends a confidential claim

    @honesty
    Scenario: `roger share --confidential` on a non-CVM host aborts before registering
      Given the host has no /dev/sev-guest device
      When the operator runs `roger share --confidential`
      Then the preflight fails with "not an AMD SEV-SNP confidential VM (no /dev/sev-guest)"
      And no registration is sent to the broker
      And no attestation quote is produced

    @honesty
    Scenario: attestation on a non-TEE host clears every confidential field
      Given the host has no /dev/sev-guest device
      When the node attempts to attest for registration
      Then it returns "no TEE hardware detected (need an AMD SEV-SNP confidential VM)"
      And the registration's confidential claim is false
      And the attestation, attest kind, and attest nonce are all empty

  # =========================================================================
  # BROKER VERIFICATION — three independent gates, ALL must pass.
  # Ground: cmd/rogerai-broker/attest.go sevSNPVerifier.Verify.
  #   Go: TestSEVSNPVerifier_ValidQuote, _ForgedSignature,
  #       _ReplayedToDifferentKey, _NonAllowlistedMeasurement, _EmptyAllowlistRejects.
  # =========================================================================
  Rule: The badge is granted only when signature chain, binding, and measurement all verify

    @gate-signature
    Scenario: a genuinely signed quote with an allowlisted measurement is granted ◆
      Given a node inside a CVM whose launch measurement is on the allowlist
      And it produces a VCEK-signed quote bound to (its pubkey, the issued nonce)
      When it registers claiming confidential
      Then the signature chain verifies VCEK -> ASK -> ARK -> the AMD root
      And the report_data equals hash(pubkey || nonce)
      And the measurement matches an allowlisted value
      And the node is granted verified-confidential status

    @gate-signature @adversarial
    Scenario: a forged signature is rejected
      Given a quote whose signature bytes have been tampered
      When it registers claiming confidential
      Then signature-chain verification fails
      And the node is not granted confidential status

    @gate-binding @adversarial
    Scenario Outline: a quote bound to one identity cannot be replayed under another
      Given a quote bound to (pubkey "<bound>", nonce "<bound-nonce>")
      When it is presented as (pubkey "<claim>", nonce "<claim-nonce>")
      Then the report_data binding check fails
      And the node is not granted confidential status

      Examples:
        | bound | bound-nonce | claim | claim-nonce |
        | pubA  | n1          | pubB  | n1          |
        | pubA  | n1          | pubA  | n2          |

    @gate-measurement @adversarial
    Scenario: a valid quote whose measurement is not on the allowlist is rejected
      Given a node inside a CVM running an UNBLESSED image
      And it produces a correctly signed, correctly bound quote
      When it registers claiming confidential
      Then the launch measurement is not found in the allowlist
      And the node is not granted confidential status

    @gate-measurement @fail-closed
    Scenario: with an empty allowlist the tier is unavailable and nobody is granted
      Given the broker has no approved measurements configured
      When any node registers claiming confidential with a valid quote
      Then verification fails with "no approved TEE measurements configured"
      And the confidential tier is reported UNAVAILABLE
      And no node ever receives the badge

    @gate-tcb @adversarial
    Scenario: a quote from firmware below the configured TCB floor is rejected
      Given the broker sets a minimum TCB floor via ROGERAI_TEE_MIN_*_SPL
      And a node presents a quote whose reported TCB is below that floor
      When it registers claiming confidential
      Then validation fails on the TCB floor
      And the node is not granted confidential status

  # =========================================================================
  # FRESHNESS / ANTI-REPLAY — the nonce is single-use and short-lived.
  # Ground: cmd/rogerai-broker/attest.go issueNonce/consumeNonce.
  #   Go: TestNonceSingleUseAndExpiry, TestVerifyRegistration_StaleNonceRejected,
  #       TestChallengeHandlerIssuesUsableNonce, TestReportDataBindingDeterministic.
  # =========================================================================
  Rule: Each attestation nonce verifies at most once, before it expires

    @replay @adversarial
    Scenario: a captured quote cannot be replayed once its nonce is spent
      Given a node registered confidential with nonce "n1"
      When the same quote is presented again with nonce "n1"
      Then the nonce is unknown or already used
      And the second registration is not granted confidential status

    @replay @adversarial
    Scenario: a quote bound to an expired nonce is rejected
      Given nonce "n1" was issued more than the nonce TTL ago
      When a node presents a quote bound to "n1"
      Then the nonce is expired
      And the node is not granted confidential status

  # =========================================================================
  # REQUIRE MODE — fail-soft vs fail-hard on a failed confidential claim.
  # Ground: cmd/rogerai-broker/attest.go verifyRegistration;
  #         cmd/rogerai-broker/tunnel.go register (403 on require-mode failure).
  #   Go: TestVerifyRegistration_RequireModeRejects, _FailSoftWhenNotRequired,
  #       _NoClaimNoBadge, _ValidGrantsBadge.
  # =========================================================================
  Rule: ROGERAI_TEE_REQUIRE decides whether a failed claim is rejected or downgraded

    @require
    Scenario: with require=1 a failed confidential claim is rejected outright
      Given ROGERAI_TEE_REQUIRE is set
      And a node claims confidential with a quote that does not verify
      When it registers
      Then the registration is rejected with 403
      And the failure reason names attestation

    @require
    Scenario: with require=0 a failed confidential claim is downgraded to standard
      Given ROGERAI_TEE_REQUIRE is not set
      And a node claims confidential with a quote that does not verify
      When it registers
      Then the registration succeeds as a standard node
      And no badge is granted
      And the broker logs the downgrade

    @require
    Scenario: a node that does not claim confidential is standard with no badge
      Given a node registers without claiming confidential
      Then verification is skipped
      And the node is standard with no badge

  # =========================================================================
  # NODE FEEDBACK — the node LEARNS whether it got the badge (no silent downgrade).
  # Ground: tunnel.go register response echoes "confidential": <granted>;
  #         agent registerResult.Confidential + Session.Confidential();
  #         cmd/rogerai cmdShare surfaces the three outcomes.
  #   Go: cmd/rogerai-broker TestRegisterResponseEchoesConfidentialGrant;
  #       internal/agent TestRegisterResultParsesConfidential,
  #       TestSessionConfidentialReflectsGrant;
  #       cmd/rogerai TestShareConfidentialMessaging.
  # =========================================================================
  Rule: A confidential claimant always learns whether the badge was granted

    @feedback
    Scenario: a granted node is told it is verified-confidential
      Given a node claims confidential with a quote that verifies
      When the register response returns
      Then the response echoes confidential true
      And `roger share` reports the confidential ◆ badge as verified

    @feedback
    Scenario: a downgraded node is told it is running standard and why
      Given a node claims confidential but the broker grants standard (require=0)
      When the register response returns
      Then the response echoes confidential false
      And `roger share` warns it is running standard, not confidential
      And the message points at the launch measurement not being on the allowlist

  # =========================================================================
  # RE-ATTESTATION — the guarantee is re-proven on a cadence, never granted forever.
  # Ground: cmd/rogerai-broker/tunnel.go reattestSweep; internal/agent reregistrar.
  #   Go: cmd/rogerai-broker TestReattestSweepDropsLapsed.
  # =========================================================================
  Rule: Verified-confidential status lapses without a fresh quote on cadence

    @reattest
    Scenario: a node that does not re-attest within the cadence loses the badge
      Given a verified-confidential node
      When more than the re-attest TTL passes with no fresh quote
      Then the re-attest sweep drops its verified-confidential status
      And it no longer receives confidential traffic

    @reattest
    Scenario: every re-register presents a fresh nonce-bound quote
      Given a verified-confidential node re-registers
      Then it fetches a new nonce and generates a fresh quote
      And a transient re-attestation failure drops it to standard for that round only

  # =========================================================================
  # CONSUMER ROUTING — confidential demand reaches ONLY verified supply.
  # Ground: cmd/rogerai-broker market/pick confidentialOnly filter;
  #         internal/client Use sets X-Roger-Confidential.
  #   Go: cmd/rogerai-broker TestConfidentialRouteFilterOnlyVerified.
  # =========================================================================
  Rule: `--confidential` traffic is served only by verified-confidential nodes

    @routing
    Scenario: a cheaper standard node is never chosen for confidential traffic
      Given a verified-confidential node and a cheaper standard node serve the same model
      When a consumer tunes in with --confidential
      Then only the verified-confidential node is eligible
      And the cheaper standard node is never selected

    @routing
    Scenario: when no verified node is online confidential traffic has no route
      Given the only confidential node has lapsed its attestation
      When a consumer tunes in with --confidential
      Then no confidential route exists
      And the consumer is not silently downgraded to a standard node

  # =========================================================================
  # MEASUREMENT PINNING — reproducible image -> precomputed digest -> CI drift gate.
  # Ground: cmd/rogerai-broker/attest.go parseMeasurements/loadAttestRegistry;
  #         image/cvm/flake.nix; tee/measure.sh; tee/measurements/*.hex;
  #         .github/workflows/tee-measurement.yml; docs/tee-runbook.md.
  #   Go: cmd/rogerai-broker TestParseMeasurements (hex/comments/invalid).
  # =========================================================================
  Rule: The allowlist is config-as-code, reproducible, and drift-gated

    @pinning
    Scenario Outline: the measurement file parses hex lines and ignores comments/blanks
      Given a measurements file line "<line>"
      Then it is "<treated>"

      Examples:
        | line                                     | treated  |
        | 0a1b2c                                   | accepted |
        | # a comment                              | ignored  |
        |                                          | ignored  |
        | not-hex-zz                               | skipped  |

    @pinning
    Scenario: the reproducible image yields the committed launch measurement
      Given the CVM image is rebuilt from the pinned flake inputs
      When sev-snp-measure recomputes the launch digest
      Then it equals the measurement committed under tee/measurements
      And CI fails the build on any drift

    @pinning
    Scenario: rolling a new image adds a measurement and draining removes the old
      Given a new image version with a new launch measurement
      When its measurement is added to the allowlist and providers migrate
      Then both measurements verify during the overlap
      And removing the old line revokes the old image within the re-attest TTL
